package store

import (
	"errors"
	"testing"
	"time"

	"ajsaccountd/internal/model"
)

// The tests in this file are the AJS-357 reopen falsifiers of
// REVIEW-MULTISLOT-FIXES findings 7 and 10 that the earlier store fixes did
// not close:
//
//  7. a device could be released while its slot was only DRAINING (not LIVE),
//     so the reused gate's next SyncAccount committed LIVE with no owned
//     identity; an empty record id was a wildcard even in a pool-enabled store;
// 10. slot.failure, lease.revoke, slot.vendor_kick, slot.disable and node
//     deletion omitted slot/generation (and sometimes node id), and every
//     DRAINING->LIVE recommit of the same generation wrote a second
//     slot.start.

// findAudit returns the newest audit row for action whose meta satisfies ok.
// The store prepends, so the slice is newest-first.
func findAudit(t *testing.T, st *Store, action string, ok func(model.AuditEvent) bool) model.AuditEvent {
	t.Helper()
	events := st.Audit(1000)
	for i := range events {
		if events[i].Action != action {
			continue
		}
		if ok == nil || ok(events[i]) {
			return events[i]
		}
	}
	t.Fatalf("no %s audit row in %d events", action, len(events))
	return model.AuditEvent{}
}

// countAudit counts one audit action.
func countAudit(st *Store, action string) int {
	count := 0
	for _, event := range st.Audit(1000) {
		if event.Action == action {
			count++
		}
	}
	return count
}

// TestLifecycleAuditCarriesSlotAndGeneration proves finding 10's attribution
// half: every required lifecycle row names the slot, its generation and (when
// a node is involved) the node id, so a two-slot trace can be reconstructed
// from retained records instead of guessed.
func TestLifecycleAuditCarriesSlotAndGeneration(t *testing.T) {
	f := newSlotKickFixture(t, testNow())
	user := f.user
	key := f.key
	node := f.node

	// lease.revoke (operator) carries slot/generation/node.
	first, _, err := f.st.CreateLease(model.CreateLeaseInput{
		UserID: user.ID, Platform: model.PlatformLinux, AccountID: f.acct.ID,
	})
	if err != nil {
		t.Fatalf("CreateLease: %v", err)
	}
	if _, err := f.st.RevokeLease(first.ID); err != nil {
		t.Fatalf("RevokeLease: %v", err)
	}
	revoked := findAudit(t, f.st, "lease.revoke", func(event model.AuditEvent) bool {
		return event.Meta["leaseId"] == first.ID
	})
	if got := revoked.Meta; got["slot"] != "1" || got["generation"] != "1" || got["nodeId"] != node.ID {
		t.Fatalf("lease.revoke meta = %+v, want slot 1 generation 1 node %s", got, node.ID)
	}

	// A silent node deletion revokes its lease explicitly instead of dropping
	// the row with the node.
	second, _, err := f.st.CreateLease(model.CreateLeaseInput{
		UserID: user.ID, Platform: model.PlatformLinux, AccountID: f.acct.ID,
	})
	if err != nil {
		t.Fatalf("CreateLease second: %v", err)
	}
	if err := f.st.DeleteNode(node.ID); err != nil {
		t.Fatalf("DeleteNode: %v", err)
	}
	deleted := findAudit(t, f.st, "node.delete", func(event model.AuditEvent) bool {
		return event.Meta["nodeId"] == node.ID
	})
	if got := deleted.Meta; got["slot"] != "1" || got["generation"] != "1" {
		t.Fatalf("node.delete meta = %+v, want slot 1 generation 1", got)
	}
	cascade := findAudit(t, f.st, "lease.revoke", func(event model.AuditEvent) bool {
		return event.Meta["leaseId"] == second.ID
	})
	if got := cascade.Meta; got["slot"] != "1" || got["generation"] != "1" || got["reason"] != "node_deleted" {
		t.Fatalf("cascade lease.revoke meta = %+v, want slot 1 generation 1 reason node_deleted", got)
	}
	if _, ok := f.st.Lease(second.ID); ok {
		t.Fatal("deleted node kept its lease row")
	}

	// Restore a row for the remaining lifecycle rows.
	node, err = f.st.PutNode(node)
	if err != nil {
		t.Fatalf("PutNode: %v", err)
	}

	if err := f.st.SetSlotFailure(key, "s1", "boom"); err != nil {
		t.Fatalf("SetSlotFailure: %v", err)
	}
	failed := findAudit(t, f.st, "slot.failure", nil)
	if got := failed.Meta; got["slot"] != key.String() || got["generation"] != "1" || got["nodeId"] != node.ID {
		t.Fatalf("slot.failure meta = %+v, want slot %s generation 1 node %s", got, key.String(), node.ID)
	}

	if _, err := f.st.RecordSlotKick(SlotKick{Key: key, ServerID: "s1", Detail: "vendor_kick"}); err != nil {
		t.Fatalf("RecordSlotKick: %v", err)
	}
	kicked := findAudit(t, f.st, "slot.vendor_kick", nil)
	if got := kicked.Meta; got["slot"] != key.String() || got["generation"] != "1" || got["nodeId"] != node.ID {
		t.Fatalf("slot.vendor_kick meta = %+v, want slot %s generation 1 node %s", got, key.String(), node.ID)
	}

	if err := f.st.DisableSlot(key); err != nil {
		t.Fatalf("DisableSlot: %v", err)
	}
	disabled := findAudit(t, f.st, "slot.disable", nil)
	if got := disabled.Meta; got["slot"] != key.String() || got["generation"] != "1" || got["nodeId"] != node.ID {
		t.Fatalf("slot.disable meta = %+v, want slot %s generation 1 node %s", got, key.String(), node.ID)
	}

	removed, err := f.st.DeleteNodeUnleased(node.ID)
	if err != nil || !removed {
		t.Fatalf("DeleteNodeUnleased = %v, %v; want removed", removed, err)
	}
	unleased := findAudit(t, f.st, "node.delete", func(event model.AuditEvent) bool {
		return event.Meta["nodeId"] == node.ID
	})
	if got := unleased.Meta; got["slot"] != "1" || got["generation"] != "1" {
		t.Fatalf("node.delete meta = %+v, want slot 1 generation 1", got)
	}
}

// TestCommitSlotStartWritesOneStartPerGeneration proves finding 10's
// exactly-once half: re-committing the same generation after the node left
// LIVE (the DRAINING/refresh path) is not a second start.
func TestCommitSlotStartWritesOneStartPerGeneration(t *testing.T) {
	t.Setenv("AJS_ACCOUNT_SLOTS", "1")
	st := NewEmpty()
	acct, err := st.CreateAccount(model.CreateAccountInput{
		Label: "Dedupe Ops", Username: "dedupe-ops", Password: "local-placeholder",
		Platform: model.PlatformLinux, Concurrency: 8,
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	key := model.SlotKey{AccountID: acct.ID, Index: 1}
	if err := st.UpsertSlot(model.AccountSlot{
		SlotKey: key, ServerID: "s1", GatePort: 13000,
		State: model.SlotLive, Health: model.SlotHealthHealthy, Generation: 1,
	}); err != nil {
		t.Fatalf("UpsertSlot: %v", err)
	}
	node := model.ProxyNode{
		ID: key.NodeID("s1"), AccountID: acct.ID, Label: "dedupe-01", Region: "cn-zj",
		State: model.NodeDraining, Protocol: model.ProtocolSOCKS5,
		GatePort: 13000, Slot: 1, ServerID: "s1",
	}
	commit := SlotStartCommit{
		Slot: model.AccountSlot{
			SlotKey: key, ServerID: "s1", GatePort: 13000,
			State: model.SlotLive, Health: model.SlotHealthHealthy, Generation: 1,
		},
		Node: node,
	}
	if _, err := st.CommitSlotStart(commit); err != nil {
		t.Fatalf("first CommitSlotStart: %v", err)
	}
	if got := countAudit(st, "slot.start"); got != 1 {
		t.Fatalf("slot.start after first commit = %d, want 1", got)
	}
	// The same generation comes back LIVE: a refresh of one gate, not a start.
	commit.Node.State = model.NodeLive
	if _, err := st.CommitSlotStart(commit); err != nil {
		t.Fatalf("second CommitSlotStart: %v", err)
	}
	if got := countAudit(st, "slot.start"); got != 1 {
		t.Fatalf("slot.start after same-generation recommit = %d, want 1", got)
	}
	started := findAudit(t, st, "slot.start", nil)
	if got := started.Meta; got["generation"] != "1" || got["nodeId"] != node.ID {
		t.Fatalf("slot.start meta = %+v, want generation 1 node %s", got, node.ID)
	}

	// A new generation is a new start.
	commit.Slot.Generation = 2
	commit.Node.State = model.NodeLive
	if _, err := st.CommitSlotStart(commit); err != nil {
		t.Fatalf("generation 2 CommitSlotStart: %v", err)
	}
	if got := countAudit(st, "slot.start"); got != 2 {
		t.Fatalf("slot.start after generation 2 = %d, want 2", got)
	}
}

// TestPoolEnabledCommitRequiresBinding proves finding 7's empty-id half: an
// empty DeviceRecordID is the pool-disabled legacy identity, not a wildcard. A
// pool-enabled slot that lost (or never had) its binding cannot commit LIVE.
func TestPoolEnabledCommitRequiresBinding(t *testing.T) {
	f := newPoolFixture(t)
	acct := f.createAccount(t, "residual-e", "residual-e-user")
	key := slotKey(acct.ID, 1)
	if err := f.st.UpsertSlot(model.AccountSlot{
		SlotKey: key, ServerID: "s1", GatePort: 13000,
		State: model.SlotLive, Health: model.SlotHealthHealthy, Generation: 1,
	}); err != nil {
		t.Fatalf("UpsertSlot: %v", err)
	}
	node, err := f.st.PutNode(model.ProxyNode{
		ID: key.NodeID("s1"), AccountID: acct.ID, Label: "residual-e-01", Region: "cn-zj",
		State: model.NodeLive, Protocol: model.ProtocolSOCKS5,
		GatePort: 13000, Slot: 1, ServerID: "s1",
	})
	if err != nil {
		t.Fatalf("PutNode: %v", err)
	}
	commit := SlotStartCommit{
		Slot: model.AccountSlot{
			SlotKey: key, ServerID: "s1", GatePort: 13000,
			State: model.SlotLive, Health: model.SlotHealthHealthy, Generation: 1,
		},
		Node: node,
	}
	if _, err := f.st.CommitSlotStart(commit); !errors.Is(err, ErrConflict) {
		t.Fatalf("CommitSlotStart with no binding = %v, want ErrConflict", err)
	}

	// A bound slot still commits with its record id.
	material, err := f.st.BindForLoginSlot(key, testLinuxProfile)
	if err != nil {
		t.Fatalf("BindForLoginSlot: %v", err)
	}
	commit.DeviceRecordID = material.DeviceID
	if _, err := f.st.CommitSlotStart(commit); err != nil {
		t.Fatalf("CommitSlotStart with binding: %v", err)
	}

	// The bound-then-released slot is the reported hole: the release is legal
	// only after the node left service, but the next commit must still fail.
	if _, err := f.st.DrainNode(node.ID); err != nil {
		t.Fatalf("DrainNode: %v", err)
	}
	if _, err := f.st.ReleaseDevice(material.DeviceID); !errors.Is(err, ErrConflict) {
		t.Fatalf("release while DRAINING = %v, want ErrConflict", err)
	}
	if _, err := f.st.PutNode(model.ProxyNode{
		ID: node.ID, AccountID: acct.ID, Label: node.Label, Region: node.Region,
		State: model.NodeDown, Protocol: model.ProtocolSOCKS5,
		GatePort: 0, Slot: 1, ServerID: "s1",
	}); err != nil {
		t.Fatalf("PutNode down: %v", err)
	}
	if _, err := f.st.ReleaseDevice(material.DeviceID); err != nil {
		t.Fatalf("release after DOWN: %v", err)
	}
	// The reused gate's row can look LIVE again while its identity is gone
	// (the reported hole: release during DRAINING, then SyncAccount reuse).
	live, err := f.st.PutNode(model.ProxyNode{
		ID: node.ID, AccountID: acct.ID, Label: node.Label, Region: node.Region,
		State: model.NodeLive, Protocol: model.ProtocolSOCKS5,
		GatePort: 13000, Slot: 1, ServerID: "s1",
	})
	if err != nil {
		t.Fatalf("PutNode live: %v", err)
	}
	commit.Node = live
	commit.DeviceRecordID = ""
	if _, err := f.st.CommitSlotStart(commit); !errors.Is(err, ErrConflict) {
		t.Fatalf("CommitSlotStart after release = %v, want ErrConflict", err)
	}
}

// TestReleaseAndRetireRefuseServingSlot proves finding 7's release half: a
// DRAINING node still owns a listener, so its device is not releasable; the
// refusal is a conflict, and the same call succeeds once the node is DOWN.
func TestReleaseAndRetireRefuseServingSlot(t *testing.T) {
	f := newPoolFixture(t)
	acct := f.createAccount(t, "residual-release", "residual-release-user")
	key := slotKey(acct.ID, 1)
	material, err := f.st.BindForLoginSlot(key, testLinuxProfile)
	if err != nil {
		t.Fatalf("BindForLoginSlot: %v", err)
	}
	node, err := f.st.PutNode(model.ProxyNode{
		ID: key.NodeID("s1"), AccountID: acct.ID, Label: "residual-rel-01", Region: "cn-zj",
		State: model.NodeLive, Protocol: model.ProtocolSOCKS5,
		GatePort: 13000, Slot: 1, ServerID: "s1",
	})
	if err != nil {
		t.Fatalf("PutNode: %v", err)
	}
	if _, err := f.st.DrainNode(node.ID); err != nil {
		t.Fatalf("DrainNode: %v", err)
	}
	if _, err := f.st.ReleaseDevice(material.DeviceID); !errors.Is(err, ErrConflict) {
		t.Fatalf("ReleaseDevice while DRAINING = %v, want ErrConflict", err)
	}
	if _, err := f.st.RetireDevice(material.DeviceID, "operator"); !errors.Is(err, ErrConflict) {
		t.Fatalf("RetireDevice while DRAINING = %v, want ErrConflict", err)
	}
	if err := f.st.PurgeDevice(material.DeviceID); !errors.Is(err, ErrConflict) {
		t.Fatalf("PurgeDevice while assigned = %v, want ErrConflict", err)
	}
	if _, err := f.st.QuarantineDevice(material.DeviceID, "operator"); !errors.Is(err, ErrConflict) {
		t.Fatalf("QuarantineDevice while assigned = %v, want ErrConflict", err)
	}
	assertSlotBinding(t, f.st, key, material.DeviceID)

	if _, err := f.st.PutNode(model.ProxyNode{
		ID: node.ID, AccountID: acct.ID, Label: node.Label, Region: node.Region,
		State: model.NodeDown, Protocol: model.ProtocolSOCKS5,
		GatePort: 0, Slot: 1, ServerID: "s1",
	}); err != nil {
		t.Fatalf("PutNode down: %v", err)
	}
	if _, err := f.st.ReleaseDevice(material.DeviceID); err != nil {
		t.Fatalf("ReleaseDevice after DOWN: %v", err)
	}
	assertNoSlotBinding(t, f.st, key)
}

// testNow is the pinned instant the slot fixtures use.
func testNow() time.Time {
	return time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)
}
