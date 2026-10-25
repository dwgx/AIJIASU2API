package store

import (
	"encoding/json"
	"errors"
	"testing"

	"ajsaccountd/internal/model"
)

// The tests in this file are the AJS-364 reopen falsifiers of
// REVIEW-MULTISLOT-RESIDUALS finding 3's remaining Slot-0 bypass:
//
//	Slot 0 was exempt from every admission and restore check: a READY legacy
//	node counted as capacity and could be leased with its remembered dead
//	port, and restore kept legacy leases whose node was missing, not LIVE or
//	on another account, duplicate node claims, and more active legacy
//	entitlements than the account capacity. A legacy lease pointed at a slot
//	node also skipped the slot checks entirely.

// legacyNode writes one Slot-0 node.
func legacyNode(t *testing.T, st *Store, accountID, id string, state model.NodeState, gatePort int) model.ProxyNode {
	t.Helper()
	node, err := st.PutNode(model.ProxyNode{
		ID: id, AccountID: accountID, Label: id, Region: "cn-zj",
		State: state, Protocol: model.ProtocolSOCKS5, GatePort: gatePort, Slot: 0,
	})
	if err != nil {
		t.Fatalf("PutNode(%s): %v", id, err)
	}
	return node
}

// restoreMutated snapshots st, lets mutate edit the document and installs it
// into a fresh store. The mutation runs on the decoded snapshot, so a test can
// build the exact persisted shape the old code accepted.
func restoreMutated(t *testing.T, st *Store, mutate func(*Snapshot)) *Store {
	t.Helper()
	raw, err := st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	var snap Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	mutate(&snap)
	doc, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("encode snapshot: %v", err)
	}
	restored := NewEmpty()
	if err := restored.Restore(doc); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	return restored
}

func requireLeaseReason(t *testing.T, err error, want string) {
	t.Helper()
	var leaseErr *LeaseError
	if !errors.As(err, &leaseErr) {
		t.Fatalf("error = %v, want *LeaseError", err)
	}
	if leaseErr.Reason != want {
		t.Fatalf("lease reason = %q, want %q", leaseErr.Reason, want)
	}
}

// TestLegacyReadyNodeIsNotCapacityOrLeasable proves finding 3's admission
// half: a Slot-0 READY node is neither serving capacity nor a lease target,
// with or without a remembered port. The old code counted both and minted
// lease-01 on the unbound 14100 endpoint.
func TestLegacyReadyNodeIsNotCapacityOrLeasable(t *testing.T) {
	t.Setenv("AJS_ACCOUNT_SLOTS", "1")
	st, account, user := residualStore(t)
	legacyNode(t, st, account.ID, "legacy-ready-port", model.NodeReady, 14100)
	legacyNode(t, st, account.ID, "legacy-ready-zero", model.NodeReady, 0)

	if capacity := st.accountCapacityLocked(account, testNow()); capacity != 0 {
		t.Fatalf("account capacity = %d, want 0: no READY node is a bound listener", capacity)
	}
	_, _, err := st.CreateLease(model.CreateLeaseInput{
		UserID: user.ID, Platform: model.PlatformAndroid, AccountID: account.ID,
	})
	requireLeaseReason(t, err, "no_capacity")
	if leases := st.Leases(); len(leases) != 0 {
		t.Fatalf("leases = %+v, want none for an unbound legacy node", leases)
	}
}

// TestLegacyLiveNodeRemainsLeasable is the positive control: the same shape
// with a LIVE node and a bound port still serves, so the new rule refuses the
// unbound state rather than every legacy row.
func TestLegacyLiveNodeRemainsLeasable(t *testing.T) {
	t.Setenv("AJS_ACCOUNT_SLOTS", "1")
	st, account, user := residualStore(t)
	node := legacyNode(t, st, account.ID, "legacy-live", model.NodeLive, 14100)

	lease, _, err := st.CreateLease(model.CreateLeaseInput{
		UserID: user.ID, Platform: model.PlatformAndroid, AccountID: account.ID,
	})
	if err != nil {
		t.Fatalf("CreateLease: %v", err)
	}
	if lease.NodeID != node.ID || lease.GatePort != node.GatePort || lease.Slot != 0 {
		t.Fatalf("lease = %+v, want the legacy LIVE node %s at port %d", lease, node.ID, node.GatePort)
	}
}

// TestRestoreDropsLegacyLeaseOnUnboundNode proves the restore half: a legacy
// lease must name a LIVE, bound legacy node of the same account. READY with a
// remembered port and an unknown node are both dropped with an audit row.
func TestRestoreDropsLegacyLeaseOnUnboundNode(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Snapshot, model.Lease)
		reason string
	}{
		{
			name: "ready node with remembered port",
			mutate: func(snap *Snapshot, _ model.Lease) {
				for i := range snap.Nodes {
					if snap.Nodes[i].Slot == 0 {
						snap.Nodes[i].State = model.NodeReady
					}
				}
			},
			reason: "node is READY",
		},
		{
			name: "node row missing",
			mutate: func(snap *Snapshot, lease model.Lease) {
				for i := range snap.Leases {
					if snap.Leases[i].ID == lease.ID {
						snap.Leases[i].NodeID = "ghost-legacy-node"
					}
				}
			},
			reason: "node row is missing",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AJS_ACCOUNT_SLOTS", "1")
			st, account, user := residualStore(t)
			legacyNode(t, st, account.ID, "legacy-live", model.NodeLive, 14100)
			lease, _, err := st.CreateLease(model.CreateLeaseInput{
				UserID: user.ID, Platform: model.PlatformAndroid, AccountID: account.ID,
			})
			if err != nil {
				t.Fatalf("CreateLease: %v", err)
			}
			restored := restoreMutated(t, st, func(snap *Snapshot) { tc.mutate(snap, lease) })
			if leases := restored.Leases(); len(leases) != 0 {
				t.Fatalf("restored leases = %+v, want the unbound legacy claim dropped", leases)
			}
			event := findAudit(t, restored, "lease.unbound_dropped", func(event model.AuditEvent) bool {
				return event.Target == lease.ID
			})
			if event.Meta["reason"] != tc.reason || event.Meta["slot"] != "legacy" {
				t.Fatalf("audit meta = %v, want reason %q slot legacy", event.Meta, tc.reason)
			}
		})
	}
}

// TestRestoreDropsLegacyDuplicateNodeLease proves at most one active legacy
// lease may claim a node. Two distinct nodes keep their two claims (the
// capacity positive control), while two claims on one node collapse to one.
func TestRestoreDropsLegacyDuplicateNodeLease(t *testing.T) {
	t.Setenv("AJS_ACCOUNT_SLOTS", "2")
	st, account, user := residualStore(t)
	if _, err := st.SetAccountUseCount(account.ID, 2); err != nil {
		t.Fatalf("SetAccountUseCount: %v", err)
	}
	node := legacyNode(t, st, account.ID, "legacy-live", model.NodeLive, 14100)
	legacyNode(t, st, account.ID, "legacy-spare", model.NodeLive, 14101)
	first, _, err := st.CreateLease(model.CreateLeaseInput{
		UserID: user.ID, Platform: model.PlatformAndroid, AccountID: account.ID,
	})
	if err != nil {
		t.Fatalf("CreateLease: %v", err)
	}
	second, err := st.CreateUser(model.CreateUserInput{Name: "legacy-duplicate-bot"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	restored := restoreMutated(t, st, func(snap *Snapshot) {
		duplicate := snap.Leases[0]
		duplicate.ID = "lease-legacy-duplicate"
		duplicate.UserID = second.ID
		duplicate.UserName = second.Name
		duplicate.NodeID = node.ID
		duplicate.IdempotencyKey = ""
		snap.Leases = append(snap.Leases, duplicate)
	})
	leases := restored.Leases()
	if len(leases) != 1 || leases[0].ID != first.ID {
		t.Fatalf("restored leases = %+v, want only %s", leases, first.ID)
	}
	findAudit(t, restored, "lease.duplicate_dropped", func(event model.AuditEvent) bool {
		return event.Target == "lease-legacy-duplicate" && event.Meta["nodeId"] == node.ID
	})
}

// TestRestoreCapsLegacyLeasesToCapacity proves the exact shape the review
// reported: two legacy entitlements survive a snapshot written under a wider
// cap, and a restart at cap 1 must keep only the first. The audit row carries
// the capacity and the active count.
func TestRestoreCapsLegacyLeasesToCapacity(t *testing.T) {
	t.Setenv("AJS_ACCOUNT_SLOTS", "2")
	st, account, user := residualStore(t)
	if _, err := st.SetAccountUseCount(account.ID, 2); err != nil {
		t.Fatalf("SetAccountUseCount: %v", err)
	}
	legacyNode(t, st, account.ID, "legacy-live-a", model.NodeLive, 14100)
	legacyNode(t, st, account.ID, "legacy-live-b", model.NodeLive, 14101)
	first, _, err := st.CreateLease(model.CreateLeaseInput{
		UserID: user.ID, Platform: model.PlatformAndroid, AccountID: account.ID,
	})
	if err != nil {
		t.Fatalf("CreateLease(first): %v", err)
	}
	secondUser, err := st.CreateUser(model.CreateUserInput{Name: "legacy-second-bot"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	second, _, err := st.CreateLease(model.CreateLeaseInput{
		UserID: secondUser.ID, Platform: model.PlatformAndroid, AccountID: account.ID,
	})
	if err != nil {
		t.Fatalf("CreateLease(second): %v", err)
	}
	if got := len(st.Leases()); got != 2 {
		t.Fatalf("fixture leases = %d, want 2", got)
	}

	t.Setenv("AJS_ACCOUNT_SLOTS", "1")
	restored := restoreMutated(t, st, func(*Snapshot) {})
	leases := restored.Leases()
	if len(leases) != 1 || leases[0].ID != first.ID {
		t.Fatalf("restored leases = %+v, want only the first (%s), second %s dropped", leases, first.ID, second.ID)
	}
	event := findAudit(t, restored, "lease.over_capacity_dropped", func(event model.AuditEvent) bool {
		return event.Target == second.ID
	})
	if event.Meta["capacity"] != "1" || event.Meta["active"] != "1" {
		t.Fatalf("audit meta = %v, want capacity 1 active 1", event.Meta)
	}
}
