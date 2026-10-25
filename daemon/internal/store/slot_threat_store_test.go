package store

import (
	"encoding/json"
	"errors"
	"strconv"
	"testing"
	"time"

	"ajsaccountd/internal/model"
)

// The tests in this file are the store-level containment proofs of
// REVIEW-MULTISLOT-THREAT.md findings 3, 5, 6, 8 and 9. Each test fails on the
// code that preceded the fix:
//
//  3. a READY slot node with no bound listener (or a duplicate row for one
//     slot) satisfied admission, and restart recovery left the matching slot
//     row LIVE;
//  5. slot disable only saw a lease on the persisted target, so a lease on the
//     previous server of the same slot was deleted by a later shrink;
//  6. paid-order activation bypassed slot capacity and stamped no
//     slot/generation on its lease;
//  8. a same-server reconnect refreshed the node but not its lease;
//  9. cross-field port collisions (A.gate/B.http, A.http/B.gate) were accepted
//     by mutation and by snapshot load.

// slotThreatStore returns an empty store with one android account and one
// panel user. The account is created with the default one-slot allowance.
func slotThreatStore(t *testing.T) (*Store, model.Account, model.PanelUser) {
	t.Helper()
	st := NewEmpty()
	account, err := st.CreateAccount(model.CreateAccountInput{
		Label: "Threat Ops", Username: "threat-ops", Password: "local-placeholder",
		Platform: model.PlatformAndroid, Concurrency: 8,
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	user, err := st.CreateUser(model.CreateUserInput{Name: "threat-bot"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	return st, account, user
}

// liveSlot writes one LIVE slot row for the account.
func liveSlot(t *testing.T, st *Store, accountID string, index, gatePort int, generation uint64) model.SlotKey {
	t.Helper()
	key := model.SlotKey{AccountID: accountID, Index: index}
	if err := st.UpsertSlot(model.AccountSlot{
		SlotKey: key, ServerID: "s" + strconv.Itoa(index),
		GatePort: gatePort, State: model.SlotLive, Generation: generation,
	}); err != nil {
		t.Fatalf("UpsertSlot(%s): %v", key.String(), err)
	}
	return key
}

// putSlotNode writes one node row for a slot.
func putSlotNode(t *testing.T, st *Store, accountID string, index int, serverID string, state model.NodeState, gatePort int) model.ProxyNode {
	t.Helper()
	key := model.SlotKey{AccountID: accountID, Index: index}
	node, err := st.PutNode(model.ProxyNode{
		ID: key.NodeID(serverID), AccountID: accountID, Label: "threat-" + strconv.Itoa(index) + "-" + serverID,
		Region: "cn-zj", State: state, Protocol: model.ProtocolSOCKS5,
		GatePort: gatePort, ServerID: serverID, Slot: index,
	})
	if err != nil {
		t.Fatalf("PutNode(%s): %v", node.ID, err)
	}
	return node
}

// TestReadySlotNodeWithoutListenerIsNotLeasable proves finding 3's admission
// half: a slot row that is LIVE over a READY node with GatePort 0 is not
// serving capacity, so no lease can be minted for an endpoint that has no
// listener.
func TestReadySlotNodeWithoutListenerIsNotLeasable(t *testing.T) {
	t.Setenv("AJS_ACCOUNT_SLOTS", "1")
	st, account, user := slotThreatStore(t)
	// The slot row claims LIVE and keeps a port, but the node never bound it.
	liveSlot(t, st, account.ID, 1, 14000, 1)
	putSlotNode(t, st, account.ID, 1, "s1", model.NodeReady, 0)

	_, _, err := st.CreateLease(model.CreateLeaseInput{
		UserID: user.ID, Platform: model.PlatformAndroid, AccountID: account.ID,
	})
	var leaseErr *LeaseError
	if !errors.As(err, &leaseErr) || !errors.Is(err, ErrQuota) {
		t.Fatalf("CreateLease = %v, want a quota refusal for an ungated slot", err)
	}
	if leaseErr.Reason != "no_capacity" && leaseErr.Reason != "account_slots_exhausted" {
		t.Fatalf("lease refusal reason = %q, want no_capacity/account_slots_exhausted", leaseErr.Reason)
	}
	if got := len(st.Leases()); got != 0 {
		t.Fatalf("leases = %d, want 0: an ungated slot must not mint a lease", got)
	}
}

// TestRestartDemotionLeavesNoLiveSlot proves finding 3's recovery half:
// demoting a gate-less node also demotes the matching slot row, so a later
// undrain (READY, still no listener) cannot satisfy admission.
func TestRestartDemotionLeavesNoLiveSlot(t *testing.T) {
	t.Setenv("AJS_ACCOUNT_SLOTS", "1")
	st, account, user := slotThreatStore(t)
	key := liveSlot(t, st, account.ID, 1, 14000, 1)
	node := putSlotNode(t, st, account.ID, 1, "s1", model.NodeLive, 14000)

	demoted, changed, err := st.DemoteNode(node.ID)
	if err != nil || !changed {
		t.Fatalf("DemoteNode = %+v/%v/%v, want a changed STALE node", demoted, changed, err)
	}
	slot, ok := st.Slot(key)
	if !ok || slot.State == model.SlotLive {
		t.Fatalf("slot after demotion = %+v, want a non-LIVE row", slot)
	}
	if slot.State != model.SlotDown {
		t.Fatalf("slot state = %q, want DOWN", slot.State)
	}

	if _, err := st.UndrainNode(node.ID); err != nil {
		t.Fatalf("UndrainNode: %v", err)
	}
	if _, _, err := st.CreateLease(model.CreateLeaseInput{
		UserID: user.ID, Platform: model.PlatformAndroid, AccountID: account.ID,
	}); err == nil {
		t.Fatal("CreateLease succeeded for a recovered slot with no listener")
	}
	if got := len(st.Leases()); got != 0 {
		t.Fatalf("leases = %d, want 0 after recovery", got)
	}
}

// TestDuplicateSlotNodeDoesNotWidenAdmission proves finding 3's denominator
// half: two node rows for the same slot count as one serving slot, and the
// second lease is refused instead of landing on the duplicate row.
func TestDuplicateSlotNodeDoesNotWidenAdmission(t *testing.T) {
	t.Setenv("AJS_ACCOUNT_SLOTS", "2")
	st, account, user := slotThreatStore(t)
	if _, err := st.SetAccountUseCount(account.ID, 2); err != nil {
		t.Fatalf("SetAccountUseCount: %v", err)
	}
	liveSlot(t, st, account.ID, 1, 14001, 1)
	putSlotNode(t, st, account.ID, 1, "s1", model.NodeLive, 14001)
	putSlotNode(t, st, account.ID, 1, "s2", model.NodeLive, 14002)

	if _, _, err := st.CreateLease(model.CreateLeaseInput{
		UserID: user.ID, Platform: model.PlatformAndroid, AccountID: account.ID,
	}); err != nil {
		t.Fatalf("first CreateLease: %v", err)
	}
	second, err := st.CreateUser(model.CreateUserInput{Name: "threat-bot-2"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	_, _, err = st.CreateLease(model.CreateLeaseInput{
		UserID: second.ID, Platform: model.PlatformAndroid, AccountID: account.ID,
	})
	var leaseErr *LeaseError
	if !errors.As(err, &leaseErr) || leaseErr.Reason != "account_slots_exhausted" {
		t.Fatalf("second CreateLease = %v, want account_slots_exhausted", err)
	}
	if got := len(st.Leases()); got != 1 {
		t.Fatalf("leases = %d, want 1: a duplicate slot row is not extra capacity", got)
	}
}

// TestDisableSlotSeesLeaseOnPreviousServer proves finding 5's shrink half:
// DisableSlot must refuse while any node row of the slot carries a lease, not
// only the persisted target. A failed target change leaves the old server's
// node behind; shrinking capacity used to disable the slot and then delete that
// lease as collateral.
func TestDisableSlotSeesLeaseOnPreviousServer(t *testing.T) {
	t.Setenv("AJS_ACCOUNT_SLOTS", "1")
	st, account, user := slotThreatStore(t)
	// The slot row already points at s2 while its reserved pair and live lease
	// still belong to s1, which is exactly what a failed target change leaves.
	key := model.SlotKey{AccountID: account.ID, Index: 1}
	if err := st.UpsertSlot(model.AccountSlot{
		SlotKey: key, ServerID: "s2", GatePort: 14001, State: model.SlotLive, Generation: 2,
	}); err != nil {
		t.Fatalf("UpsertSlot: %v", err)
	}
	putSlotNode(t, st, account.ID, 1, "s1", model.NodeLive, 14001)
	putSlotNode(t, st, account.ID, 1, "s2", model.NodeLive, 14002)
	lease, _, err := st.CreateLease(model.CreateLeaseInput{
		UserID: user.ID, Platform: model.PlatformAndroid, AccountID: account.ID,
	})
	if err != nil {
		t.Fatalf("CreateLease: %v", err)
	}
	if lease.NodeID != key.NodeID("s1") {
		t.Fatalf("lease node = %q, want the s1 row", lease.NodeID)
	}

	if err := st.DisableSlot(key); !errors.Is(err, ErrConflict) {
		t.Fatalf("DisableSlot = %v, want ErrConflict while the previous server is leased", err)
	}
	if got := len(st.Leases()); got != 1 {
		t.Fatalf("leases = %d, want the lease preserved", got)
	}
	if slot, ok := st.Slot(key); !ok || slot.State != model.SlotLive {
		t.Fatalf("slot after refused disable = %+v, want it untouched", slot)
	}
}

// TestDeleteNodeUnleasedKeepsLeasedNode proves finding 5's cleanup guard: a
// node that still carries an active lease is not deletable by the cleanup path,
// so a pass that runs before the explicit revocation cannot silently drop the
// entitlement.
func TestDeleteNodeUnleasedKeepsLeasedNode(t *testing.T) {
	t.Setenv("AJS_ACCOUNT_SLOTS", "1")
	st, account, user := slotThreatStore(t)
	liveSlot(t, st, account.ID, 1, 14001, 1)
	node := putSlotNode(t, st, account.ID, 1, "s1", model.NodeLive, 14001)
	if _, _, err := st.CreateLease(model.CreateLeaseInput{
		UserID: user.ID, Platform: model.PlatformAndroid, AccountID: account.ID,
	}); err != nil {
		t.Fatalf("CreateLease: %v", err)
	}

	removed, err := st.DeleteNodeUnleased(node.ID)
	if err != nil {
		t.Fatalf("DeleteNodeUnleased: %v", err)
	}
	if removed {
		t.Fatal("DeleteNodeUnleased removed a node with an active lease")
	}
	if _, ok := st.Node(node.ID); !ok {
		t.Fatal("node row disappeared")
	}
	if got := len(st.Leases()); got != 1 {
		t.Fatalf("leases = %d, want 1", got)
	}
}

// TestOrderActivationRespectsSlotCapacity proves finding 6's capacity half:
// with two LIVE slot nodes but capacity one and one active lease, a second paid
// order is refused instead of taking the fallback's free node. It also pins the
// slot/generation stamp on the order lease that does activate.
func TestOrderActivationRespectsSlotCapacity(t *testing.T) {
	t.Setenv("AJS_ACCOUNT_SLOTS", "1")
	st, account, _ := slotThreatStore(t)
	if _, err := st.SetAccountUseCount(account.ID, 1); err != nil {
		t.Fatalf("SetAccountUseCount: %v", err)
	}
	liveSlot(t, st, account.ID, 1, 14001, 3)
	putSlotNode(t, st, account.ID, 1, "s1", model.NodeLive, 14001)
	putSlotNode(t, st, account.ID, 1, "s2", model.NodeLive, 14002)

	user, err := st.CreateUser(model.CreateUserInput{Name: "order-bot", Platform: "android"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	tenant := signupTestTenant(t, st, "slot-order@example.com", "Slot Order")
	tenant, err = st.ApproveTenant(tenant.ID, user.ID, nil)
	if err != nil {
		t.Fatalf("ApproveTenant: %v", err)
	}
	plan, err := st.CreatePlan(model.PlanInput{
		Name: "Slot Monthly", AmountMinor: 3000, Currency: "CNY",
		LeaseTTLSeconds: 3600, QuotaBytes: 1 << 30,
	})
	if err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	// One lease already occupies the account's single serving slot.
	if _, _, err := st.CreateLease(model.CreateLeaseInput{
		UserID: user.ID, Platform: model.PlatformAndroid, AccountID: account.ID,
	}); err != nil {
		t.Fatalf("CreateLease: %v", err)
	}

	now := time.Now().UTC()
	first, replayed, err := st.CreateOrder(tenant.ID, user.ID, plan.ID, "order-slot-1", "tenant:"+tenant.ID)
	if err != nil || replayed {
		t.Fatalf("CreateOrder = %+v/%v/%v", first, replayed, err)
	}
	settled, err := st.ApplyPaymentEvent(model.ProviderManual, model.PaymentEvent{
		Provider: model.ProviderManual, ID: "evt-slot-1", Kind: model.PaymentSettled,
		OrderID: first.ID, ProviderRef: "ref-1", OccurredAt: now.UTC().Format(time.RFC3339Nano),
	}, "operator")
	if err != nil {
		t.Fatalf("ApplyPaymentEvent: %v", err)
	}
	if settled.State != model.OrderPaid || settled.Grant != nil {
		t.Fatalf("over-capacity order = %+v, want it paid without a grant", settled)
	}
	if got := len(st.Leases()); got != 1 {
		t.Fatalf("leases = %d, want 1: the fallback must not exceed slot capacity", got)
	}

	// Free the slot and activate: the granted lease carries the exact slot and
	// the generation it was issued under.
	if err := st.ReleaseLease(st.Leases()[0].ID); err != nil {
		t.Fatalf("ReleaseLease: %v", err)
	}
	settled, err = st.ApplyPaymentEvent(model.ProviderManual, model.PaymentEvent{
		Provider: model.ProviderManual, ID: "evt-slot-1-retry", Kind: model.PaymentSettled,
		OrderID: first.ID, ProviderRef: "ref-1", OccurredAt: now.UTC().Format(time.RFC3339Nano),
	}, "operator")
	if err != nil {
		t.Fatalf("ApplyPaymentEvent(retry): %v", err)
	}
	if settled.State != model.OrderActive || settled.Grant == nil {
		t.Fatalf("retried order = %+v, want active with a grant", settled)
	}
	var granted *model.Lease
	for _, lease := range st.Leases() {
		if lease.ID == settled.Grant.LeaseID {
			copied := lease
			granted = &copied
		}
	}
	if granted == nil {
		t.Fatalf("granted lease %q is missing", settled.Grant.LeaseID)
	}
	if granted.Slot != 1 || granted.Generation != 3 {
		t.Fatalf("order lease = %+v, want slot 1 generation 3", granted)
	}
}

// TestSlotPortsRejectCrossFieldCollisions proves finding 9 at mutation time:
// all four cross-slot collisions are refused, including gate-to-http and
// http-to-gate.
func TestSlotPortsRejectCrossFieldCollisions(t *testing.T) {
	st := NewEmpty()
	first, err := st.CreateAccount(model.CreateAccountInput{
		Label: "Port A", Username: "port-a", Password: "local-placeholder",
		Platform: model.PlatformAndroid, Concurrency: 1,
	})
	if err != nil {
		t.Fatalf("CreateAccount(A): %v", err)
	}
	second, err := st.CreateAccount(model.CreateAccountInput{
		Label: "Port B", Username: "port-b", Password: "local-placeholder",
		Platform: model.PlatformAndroid, Concurrency: 1,
	})
	if err != nil {
		t.Fatalf("CreateAccount(B): %v", err)
	}
	keyA := model.SlotKey{AccountID: first.ID, Index: 1}
	keyB := model.SlotKey{AccountID: second.ID, Index: 1}
	if err := st.UpsertSlot(model.AccountSlot{
		SlotKey: keyA, GatePort: 15000, HTTPPort: 16000, State: model.SlotProvisioning,
	}); err != nil {
		t.Fatalf("UpsertSlot(A): %v", err)
	}

	cases := []struct {
		name        string
		socks, http int
	}{
		{"gate/gate", 15000, 0},
		{"http/http", 0, 16000},
		{"gate/http", 16000, 0},
		{"http/gate", 0, 15000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := st.SetSlotPorts(keyB, tc.socks, tc.http); !errors.Is(err, ErrConflict) {
				t.Fatalf("SetSlotPorts(%d, %d) = %v, want ErrConflict", tc.socks, tc.http, err)
			}
			if err := st.UpsertSlot(model.AccountSlot{
				SlotKey: keyB, GatePort: tc.socks, HTTPPort: tc.http, State: model.SlotProvisioning,
			}); !errors.Is(err, ErrConflict) {
				t.Fatalf("UpsertSlot(%d, %d) = %v, want ErrConflict", tc.socks, tc.http, err)
			}
		})
	}
}

// TestSnapshotRejectsCrossFieldPortCollision proves finding 9 at load time: a
// snapshot whose slot A owns HTTPPort 15000 and slot B owns GatePort 15000 is
// rejected, so a hand-edited or older document cannot reintroduce the
// collision the mutation path now refuses.
func TestSnapshotRejectsCrossFieldPortCollision(t *testing.T) {
	st := NewEmpty()
	first, err := st.CreateAccount(model.CreateAccountInput{
		Label: "Snap A", Username: "snap-a", Password: "local-placeholder",
		Platform: model.PlatformAndroid, Concurrency: 1,
	})
	if err != nil {
		t.Fatalf("CreateAccount(A): %v", err)
	}
	second, err := st.CreateAccount(model.CreateAccountInput{
		Label: "Snap B", Username: "snap-b", Password: "local-placeholder",
		Platform: model.PlatformAndroid, Concurrency: 1,
	})
	if err != nil {
		t.Fatalf("CreateAccount(B): %v", err)
	}
	if err := st.UpsertSlot(model.AccountSlot{
		SlotKey:  model.SlotKey{AccountID: first.ID, Index: 1},
		GatePort: 15000, HTTPPort: 16000, State: model.SlotProvisioning,
	}); err != nil {
		t.Fatalf("UpsertSlot(A): %v", err)
	}
	if err := st.UpsertSlot(model.AccountSlot{
		SlotKey:  model.SlotKey{AccountID: second.ID, Index: 1},
		GatePort: 15002, HTTPPort: 16002, State: model.SlotProvisioning,
	}); err != nil {
		t.Fatalf("UpsertSlot(B): %v", err)
	}
	data, err := st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	slots, ok := doc["accountSlots"].([]any)
	if !ok || len(slots) != 2 {
		t.Fatalf("snapshot accountSlots = %#v, want two rows", doc["accountSlots"])
	}
	for _, raw := range slots {
		slot, _ := raw.(map[string]any)
		if slot["accountId"] != second.ID {
			continue
		}
		slot["gatePort"] = 16000
	}
	mutated, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("encode snapshot: %v", err)
	}
	if err := NewEmpty().Restore(mutated); err == nil {
		t.Fatal("Restore accepted a snapshot with a gate/http cross-field port collision")
	}
}
