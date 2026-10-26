package store

import (
	"errors"
	"testing"
	"time"

	"ajsaccountd/internal/model"
)

// slotKickFixture is one account with a LIVE slot row and a LIVE slot-backed
// node, so admission has something to hand out before a kick removes it.
type slotKickFixture struct {
	st   *Store
	acct model.Account
	user model.PanelUser
	key  model.SlotKey
	node model.ProxyNode
}

func newSlotKickFixture(t *testing.T, now time.Time) slotKickFixture {
	t.Helper()
	t.Setenv("AJS_ACCOUNT_SLOTS", "1")
	st := newEmptyWithClock(func() time.Time { return now }, nil)
	acct, err := st.CreateAccount(model.CreateAccountInput{
		Label:       "Slot Kick",
		Username:    "slot-kick",
		Password:    "local-placeholder",
		Platform:    model.PlatformLinux,
		Concurrency: 8,
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if _, err := st.SetAccountUseCount(acct.ID, 1); err != nil {
		t.Fatalf("SetAccountUseCount: %v", err)
	}
	key := model.SlotKey{AccountID: acct.ID, Index: 1}
	if err := st.UpsertSlot(model.AccountSlot{
		SlotKey: key, ServerID: "s1", GatePort: 13000, HTTPPort: 14000,
		State: model.SlotLive, Health: model.SlotHealthHealthy, Generation: 1,
	}); err != nil {
		t.Fatalf("UpsertSlot: %v", err)
	}
	node, err := st.PutNode(model.ProxyNode{
		ID: key.NodeID("s1"), AccountID: acct.ID, Label: "cn-sh-01", Region: "cn-sh",
		State: model.NodeLive, Protocol: model.ProtocolSOCKS5,
		GatePort: 13000, Slot: 1, ServerID: "s1",
	})
	if err != nil {
		t.Fatalf("PutNode: %v", err)
	}
	return slotKickFixture{
		st: st, acct: acct, user: addLeaseUser(t, st, "kick-bot", nil), key: key, node: node,
	}
}

// TestRecordSlotKickLeavesServiceAndBlocksRetry pins the slot side of the kick
// containment: the row leaves LIVE, keeps its port pair and device reference,
// counts the kick, records the retry window and quarantines a repeat offender.
func TestRecordSlotKickLeavesServiceAndBlocksRetry(t *testing.T) {
	now := time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)
	f := newSlotKickFixture(t, now)
	retry := now.Add(5 * time.Minute)

	slot, err := f.st.RecordSlotKick(SlotKick{
		Key: f.key, ServerID: "s1", Detail: "vendor_kick", RetryAfter: retry,
	})
	if err != nil {
		t.Fatalf("RecordSlotKick: %v", err)
	}
	if slot.State != model.SlotDown || slot.Health != model.SlotHealthVendorKick {
		t.Fatalf("slot = %+v, want DOWN with health vendor_kick", slot)
	}
	if slot.KickCount != 1 || slot.RetryAfter != retry.UTC().Format(time.RFC3339) {
		t.Fatalf("slot = %+v, want one kick and retryAfter %s", slot, retry.UTC().Format(time.RFC3339))
	}
	if slot.GatePort != 13000 || slot.HTTPPort != 14000 || slot.ServerID != "s1" {
		t.Fatalf("slot = %+v, want the port pair and server kept", slot)
	}
	if slot.Generation != 1 {
		t.Fatalf("generation = %d, want the stored 1", slot.Generation)
	}

	// A kicked slot admits nothing: its node is no longer a leasable target.
	_, _, err = f.st.CreateLease(model.CreateLeaseInput{
		UserID: f.user.ID, Platform: model.PlatformLinux, AccountID: f.acct.ID,
	})
	var leaseErr *LeaseError
	if !errors.As(err, &leaseErr) || leaseErr.Reason != "no_capacity" {
		t.Fatalf("CreateLease after kick = %v, want no_capacity", err)
	}

	// The quarantine threshold is cumulative across automatic restarts.
	for kick := 2; kick <= SlotKickQuarantineThreshold; kick++ {
		slot, err = f.st.RecordSlotKick(SlotKick{Key: f.key, ServerID: "s1", Detail: "vendor_kick"})
		if err != nil {
			t.Fatalf("RecordSlotKick #%d: %v", kick, err)
		}
		if slot.KickCount != kick {
			t.Fatalf("kick %d: count = %d", kick, slot.KickCount)
		}
	}
	if slot.State != model.SlotQuarantined {
		t.Fatalf("slot after %d kicks = %q, want quarantined", SlotKickQuarantineThreshold, slot.State)
	}
	audits := 0
	for _, event := range f.st.Audit(200) {
		if event.Action == "slot.vendor_kick" {
			audits++
		}
	}
	if audits != SlotKickQuarantineThreshold {
		t.Fatalf("slot.vendor_kick audits = %d, want %d", audits, SlotKickQuarantineThreshold)
	}
}

// TestInvalidateNodeLeasesRevokesAndRecountsInUse pins the lease side of the
// kick containment: every lease on the node is revoked with an explicit audit
// and the node's InUse counter follows the remaining rows.
func TestInvalidateNodeLeasesRevokesAndRecountsInUse(t *testing.T) {
	now := time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)
	f := newSlotKickFixture(t, now)
	lease, _, err := f.st.CreateLease(model.CreateLeaseInput{
		UserID: f.user.ID, Platform: model.PlatformLinux, AccountID: f.acct.ID,
	})
	if err != nil {
		t.Fatalf("CreateLease: %v", err)
	}
	if lease.NodeID != f.node.ID || lease.Slot != 1 || lease.Generation != 1 {
		t.Fatalf("lease = %+v, want the slot node at generation 1", lease)
	}

	revoked, err := f.st.InvalidateNodeLeases(f.node.ID, "vendor_kick")
	if err != nil {
		t.Fatalf("InvalidateNodeLeases: %v", err)
	}
	if len(revoked) != 1 || revoked[0].ID != lease.ID || revoked[0].Status != model.LeaseRevoked {
		t.Fatalf("revoked = %+v, want lease %s revoked", revoked, lease.ID)
	}
	if _, ok := f.st.Lease(lease.ID); ok {
		t.Fatalf("lease %s is still active", lease.ID)
	}
	node, ok := f.st.Node(f.node.ID)
	if !ok || node.InUse != 0 {
		t.Fatalf("node = %+v/%v, want InUse 0", node, ok)
	}
	found := false
	for _, event := range f.st.Audit(200) {
		if event.Action == "lease.revoke" && event.Meta["leaseId"] == lease.ID {
			found = event.Meta["reason"] == "vendor_kick"
		}
	}
	if !found {
		t.Fatalf("no lease.revoke audit with reason vendor_kick: %+v", f.st.Audit(200))
	}
	// Idempotent: a second containment finds nothing to revoke.
	if again, err := f.st.InvalidateNodeLeases(f.node.ID, "vendor_kick"); err != nil {
		t.Fatalf("InvalidateNodeLeases: %v", err)
	} else if len(again) != 0 {
		t.Fatalf("second invalidate revoked %d leases, want 0", len(again))
	}
}
