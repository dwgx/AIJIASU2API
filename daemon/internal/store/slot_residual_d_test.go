package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	"ajsaccountd/internal/model"
)

// The tests in this file are the reopen falsifiers of REVIEW-MULTISLOT-FIXES
// findings 3, 6 and 8 that the earlier store fixes did not close:
//
//  3. usedSlots keyed only the integer slot index across all accounts, so a
//     lease on account A#1 blocked account B#1; restore accepted a LIVE slot
//     row over a READY node with no listener, and duplicate active leases for
//     one slot;
//  6. the paid-order fallback exempted every account without a Slot>0 node,
//     so an order could mint a second lease on a legacy account whose single
//     allowance was already spent by ordinary admission;
//  8. restore kept a lease whose generation predated its slot's serving
//     generation, with no repair and no audit row.

// residualStore returns an empty store with one android account, one user and
// the default one-slot allowance. The caller sets AJS_ACCOUNT_SLOTS first.
func residualStore(t *testing.T) (*Store, model.Account, model.PanelUser) {
	t.Helper()
	st := NewEmpty()
	account, err := st.CreateAccount(model.CreateAccountInput{
		Label: "Residual Ops", Username: "residual-ops", Password: "local-placeholder",
		Platform: model.PlatformAndroid, Concurrency: 8,
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	user, err := st.CreateUser(model.CreateUserInput{Name: "residual-bot"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	return st, account, user
}

// residualSlotNode writes a LIVE slot row and its matching LIVE node.
func residualSlotNode(t *testing.T, st *Store, accountID string, index, gatePort int, generation uint64) (model.SlotKey, model.ProxyNode) {
	t.Helper()
	key := model.SlotKey{AccountID: accountID, Index: index}
	serverID := "s" + strconv.Itoa(index)
	if err := st.UpsertSlot(model.AccountSlot{
		SlotKey: key, ServerID: serverID, GatePort: gatePort,
		State: model.SlotLive, Generation: generation,
	}); err != nil {
		t.Fatalf("UpsertSlot(%s): %v", key.String(), err)
	}
	node, err := st.PutNode(model.ProxyNode{
		ID: key.NodeID(serverID), AccountID: accountID,
		Label:  fmt.Sprintf("residual-%s-%d", accountID, index),
		Region: "cn-zj",
		State:  model.NodeLive, Protocol: model.ProtocolSOCKS5,
		GatePort: gatePort, ServerID: serverID, Slot: index,
		EgressIP: "203.0.113.7",
	})
	if err != nil {
		t.Fatalf("PutNode(%s): %v", key.NodeID(serverID), err)
	}
	return key, node
}

// TestLeaseSlotKeyIncludesAccount is finding 3's cross-account falsifier: with
// A's slot 1 leased, B must still lease its own LIVE slot 1. The old map was
// keyed by the integer index alone, so B was refused no_capacity.
func TestLeaseSlotKeyIncludesAccount(t *testing.T) {
	t.Setenv("AJS_ACCOUNT_SLOTS", "1")
	st := NewEmpty()
	accountA, err := st.CreateAccount(model.CreateAccountInput{
		Label: "Slot A", Username: "slot-a", Password: "local-placeholder",
		Platform: model.PlatformAndroid, Concurrency: 8,
	})
	if err != nil {
		t.Fatalf("CreateAccount(A): %v", err)
	}
	accountB, err := st.CreateAccount(model.CreateAccountInput{
		Label: "Slot B", Username: "slot-b", Password: "local-placeholder",
		Platform: model.PlatformAndroid, Concurrency: 8,
	})
	if err != nil {
		t.Fatalf("CreateAccount(B): %v", err)
	}
	keyA, _ := residualSlotNode(t, st, accountA.ID, 1, 14001, 1)
	keyB, _ := residualSlotNode(t, st, accountB.ID, 1, 14101, 1)
	userA, err := st.CreateUser(model.CreateUserInput{Name: "slot-a-bot"})
	if err != nil {
		t.Fatalf("CreateUser(A): %v", err)
	}
	userB, err := st.CreateUser(model.CreateUserInput{Name: "slot-b-bot"})
	if err != nil {
		t.Fatalf("CreateUser(B): %v", err)
	}

	leaseA, _, err := st.CreateLease(model.CreateLeaseInput{
		UserID: userA.ID, Platform: model.PlatformAndroid, AccountID: accountA.ID,
	})
	if err != nil {
		t.Fatalf("CreateLease(A): %v", err)
	}
	if leaseA.Slot != keyA.Index || leaseA.AccountID != accountA.ID {
		t.Fatalf("lease A = %+v, want account A slot 1", leaseA)
	}
	leaseB, _, err := st.CreateLease(model.CreateLeaseInput{
		UserID: userB.ID, Platform: model.PlatformAndroid, AccountID: accountB.ID,
	})
	if err != nil {
		t.Fatalf("CreateLease(B) = %v, want B to lease its own slot 1", err)
	}
	if leaseB.Slot != keyB.Index || leaseB.AccountID != accountB.ID || leaseB.NodeID == leaseA.NodeID {
		t.Fatalf("lease B = %+v, want account B slot 1 on its own node", leaseB)
	}
	if got := len(st.Leases()); got != 2 {
		t.Fatalf("leases = %d, want one per account slot", got)
	}
}

// TestRestoreDemotesUnboundReadySlotAndDropsLease is finding 3's restore
// falsifier: a LIVE slot row over a READY node remembers a port but has no
// listener. Restore must demote the slot and drop the active lease that pointed
// at it; the old loader installed both and then admitted another lease.
func TestRestoreDemotesUnboundReadySlotAndDropsLease(t *testing.T) {
	t.Setenv("AJS_ACCOUNT_SLOTS", "1")
	st, account, user := residualStore(t)
	key, node := residualSlotNode(t, st, account.ID, 1, 14001, 2)
	if _, _, err := st.CreateLease(model.CreateLeaseInput{
		UserID: user.ID, Platform: model.PlatformAndroid, AccountID: account.ID,
	}); err != nil {
		t.Fatalf("CreateLease: %v", err)
	}

	raw, err := st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	var snap Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	for i := range snap.Nodes {
		if snap.Nodes[i].ID == node.ID {
			// The gate died; the persisted port and the LIVE slot row remain.
			snap.Nodes[i].State = model.NodeReady
		}
	}
	doc, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("encode snapshot: %v", err)
	}

	restored := NewEmpty()
	if err := restored.Restore(doc); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	slot, ok := restored.Slot(key)
	if !ok || slot.State != model.SlotDown || slot.Health != model.SlotHealthDegraded {
		t.Fatalf("restored slot = %+v, want DOWN/degraded", slot)
	}
	if got := len(restored.Leases()); got != 0 {
		t.Fatalf("restored leases = %d, want the unbound entitlement dropped", got)
	}
	second, err := restored.CreateUser(model.CreateUserInput{Name: "residual-bot-2"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	_, _, err = restored.CreateLease(model.CreateLeaseInput{
		UserID: second.ID, Platform: model.PlatformAndroid, AccountID: account.ID,
	})
	var leaseErr *LeaseError
	if !errors.As(err, &leaseErr) || leaseErr.Reason != "no_capacity" {
		t.Fatalf("CreateLease after restore = %v, want no_capacity", err)
	}
	actions := map[string]bool{}
	var revoked model.AuditEvent
	for _, event := range restored.Audit(1000) {
		actions[event.Action] = true
		if event.Action == "lease.revoke" {
			revoked = event
		}
	}
	for _, want := range []string{"slot.recovered_unbound", "lease.revoke"} {
		if !actions[want] {
			t.Fatalf("audit actions = %v, want %q", actions, want)
		}
	}
	if revoked.Meta["reason"] != "daemon_restart" || revoked.Meta["detail"] != "slot is down" {
		t.Fatalf("lease.revoke meta = %v, want reason daemon_restart detail slot is down", revoked.Meta)
	}
}

// TestRestoreDropsDuplicateSlotEntitlement is finding 3's duplicate falsifier:
// two active leases for one (account, index) cannot both be served. Restore
// keeps the first and drops the duplicate with an audit row.
func TestRestoreDropsDuplicateSlotEntitlement(t *testing.T) {
	t.Setenv("AJS_ACCOUNT_SLOTS", "2")
	st, account, user := residualStore(t)
	residualSlotNode(t, st, account.ID, 1, 14001, 1)
	if _, _, err := st.CreateLease(model.CreateLeaseInput{
		UserID: user.ID, Platform: model.PlatformAndroid, AccountID: account.ID,
	}); err != nil {
		t.Fatalf("CreateLease: %v", err)
	}
	second, err := st.CreateUser(model.CreateUserInput{Name: "residual-bot-2"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	raw, err := st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	var snap Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if len(snap.Leases) != 1 {
		t.Fatalf("fixture leases = %d, want 1", len(snap.Leases))
	}
	duplicate := snap.Leases[0]
	duplicate.ID = "lease-duplicate"
	duplicate.UserID = second.ID
	duplicate.UserName = second.Name
	duplicate.IdempotencyKey = ""
	snap.Leases = append(snap.Leases, duplicate)
	doc, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("encode snapshot: %v", err)
	}

	restored := NewEmpty()
	if err := restored.Restore(doc); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	leases := restored.Leases()
	if len(leases) != 1 || leases[0].ID != "lease-01" {
		t.Fatalf("restored leases = %+v, want only the first lease", leases)
	}
	found := false
	for _, event := range restored.Audit(1000) {
		if event.Action == "lease.duplicate_dropped" && event.Target == duplicate.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("restore dropped the duplicate without a lease.duplicate_dropped audit row")
	}
}

// TestOrderFallbackLegacyNodesRespectCapacity is finding 6's legacy falsifier:
// two legacy LIVE nodes, AJS_ACCOUNT_SLOTS=1, and one ordinary lease already
// spent. A paid order must stay paid without a grant; the old fallback exempted
// every account without a Slot>0 node and minted a second lease.
func TestOrderFallbackLegacyNodesRespectCapacity(t *testing.T) {
	t.Setenv("AJS_ACCOUNT_SLOTS", "1")
	st := NewEmpty()
	account, err := st.CreateAccount(model.CreateAccountInput{
		Label: "Legacy Ops", Username: "legacy-ops", Password: "local-placeholder",
		Platform: model.PlatformAndroid, Concurrency: 8,
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if _, err := st.SetAccountUseCount(account.ID, 1); err != nil {
		t.Fatalf("SetAccountUseCount: %v", err)
	}
	for _, node := range []model.ProxyNode{
		{ID: "legacy-1", Label: "legacy-1", GatePort: 15001},
		{ID: "legacy-2", Label: "legacy-2", GatePort: 15002},
	} {
		node.AccountID = account.ID
		node.Region = "cn-zj"
		node.State = model.NodeLive
		node.Protocol = model.ProtocolSOCKS5
		if _, err := st.PutNode(node); err != nil {
			t.Fatalf("PutNode(%s): %v", node.ID, err)
		}
	}
	first, err := st.CreateUser(model.CreateUserInput{Name: "legacy-bot-1"})
	if err != nil {
		t.Fatalf("CreateUser(1): %v", err)
	}
	orderUser, err := st.CreateUser(model.CreateUserInput{Name: "legacy-bot-2", Platform: "android"})
	if err != nil {
		t.Fatalf("CreateUser(2): %v", err)
	}
	// Ordinary admission spends the account's single allowed lease.
	if _, _, err := st.CreateLease(model.CreateLeaseInput{
		UserID: first.ID, Platform: model.PlatformAndroid, AccountID: account.ID,
	}); err != nil {
		t.Fatalf("CreateLease: %v", err)
	}

	tenant := signupTestTenant(t, st, "legacy-order@example.com", "Legacy Order")
	tenant, err = st.ApproveTenant(tenant.ID, orderUser.ID, nil)
	if err != nil {
		t.Fatalf("ApproveTenant: %v", err)
	}
	plan, err := st.CreatePlan(model.PlanInput{
		Name: "Legacy Monthly", AmountMinor: 3000, Currency: "CNY",
		LeaseTTLSeconds: 3600, QuotaBytes: 1 << 30,
	})
	if err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	order, replayed, err := st.CreateOrder(tenant.ID, orderUser.ID, plan.ID, "order-legacy-1", "tenant:"+tenant.ID)
	if err != nil || replayed {
		t.Fatalf("CreateOrder = %+v/%v/%v", order, replayed, err)
	}
	settled, err := st.ApplyPaymentEvent(model.ProviderManual, model.PaymentEvent{
		Provider: model.ProviderManual, ID: "evt-legacy-1", Kind: model.PaymentSettled,
		OrderID: order.ID, ProviderRef: "ref-legacy-1",
		OccurredAt: time.Now().UTC().Format(time.RFC3339Nano),
	}, "operator")
	if err != nil {
		t.Fatalf("ApplyPaymentEvent: %v", err)
	}
	if settled.State != model.OrderPaid || settled.Grant != nil {
		t.Fatalf("over-capacity order = %+v, want paid without a grant", settled)
	}
	if got := len(st.Leases()); got != 1 {
		t.Fatalf("leases = %d, want 1: the legacy fallback must not exceed capacity", got)
	}
}

// TestRestoreRepairsLeaseGeneration is finding 8's migration falsifier: a
// pre-fix snapshot carries slot generation 2 and lease generation 1 with stale
// mirrored fields. Restore must advance the lease atomically to the slot's
// generation, refresh the node-mirrored endpoint, and write an audit row.
func TestRestoreRepairsLeaseGeneration(t *testing.T) {
	t.Setenv("AJS_ACCOUNT_SLOTS", "1")
	st, account, user := residualStore(t)
	_, node := residualSlotNode(t, st, account.ID, 1, 14001, 2)
	if _, _, err := st.CreateLease(model.CreateLeaseInput{
		UserID: user.ID, Platform: model.PlatformAndroid, AccountID: account.ID,
	}); err != nil {
		t.Fatalf("CreateLease: %v", err)
	}

	raw, err := st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	var snap Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if len(snap.Leases) != 1 || snap.Leases[0].Generation != 2 {
		t.Fatalf("fixture leases = %+v, want one generation-2 lease", snap.Leases)
	}
	snap.Leases[0].Generation = 1
	snap.Leases[0].NodeLabel = "stale-label"
	snap.Leases[0].Region = "stale-region"
	snap.Leases[0].EgressIP = "198.51.100.9"
	snap.Leases[0].GatePort = 1
	doc, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("encode snapshot: %v", err)
	}

	restored := NewEmpty()
	if err := restored.Restore(doc); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	leases := restored.Leases()
	if len(leases) != 1 {
		t.Fatalf("restored leases = %d, want 1", len(leases))
	}
	lease := leases[0]
	if lease.Generation != 2 || lease.Slot != 1 {
		t.Fatalf("restored lease = %+v, want slot 1 generation 2", lease)
	}
	if lease.NodeLabel != node.Label || lease.Region != node.Region ||
		lease.EgressIP != node.EgressIP || lease.GatePort != node.GatePort {
		t.Fatalf("restored lease mirror = %+v, want node %+v", lease, node)
	}
	found := false
	for _, event := range restored.Audit(1000) {
		if event.Action != "lease.generation_repaired" {
			continue
		}
		if event.Meta["fromGeneration"] != "1" || event.Meta["toGeneration"] != "2" ||
			event.Meta["leaseId"] != lease.ID {
			t.Fatalf("generation audit = %+v, want from=1 to=2 lease=%s", event, lease.ID)
		}
		found = true
	}
	if !found {
		t.Fatal("restore repaired the generation without a lease.generation_repaired audit row")
	}
}
