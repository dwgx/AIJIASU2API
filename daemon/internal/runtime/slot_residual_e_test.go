package runtime_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ajsaccountd/internal/account"
	"ajsaccountd/internal/model"
	"ajsaccountd/internal/store"
	"ajsproxy/apifacade"
)

// The tests in this file are the AJS-357 reopen falsifiers of
// REVIEW-MULTISLOT-FIXES findings 5 and 7 that the earlier runtime fixes did
// not close:
//
//  5. cleanup checked NodeLeases, then blocked in the gate close, then deleted
//     the node unconditionally, taking a lease created in that window with it;
//     a failed switch of the only LIVE slot left the old subscription
//     advertising the dead port;
//  7. a device released while its slot was only DRAINING left the gate usable,
//     so the next SyncAccount committed LIVE with no owned identity.

// TestCleanupKeepsLeaseCreatedDuringGateClose blocks the gate close, creates a
// lease in that window, and requires the node and lease to survive: cleanup may
// not silently delete a customer entitlement it did not see at check time.
func TestCleanupKeepsLeaseCreatedDuringGateClose(t *testing.T) {
	f := newSlotThreatFixture(t)
	f.sync(t)
	node := nodeByServer(t, f.st, f.acct.ID, "s1")

	// A catalog with no usable server no longer pins the running gate: the
	// slot row loses its target and the login result is empty, so the next sync
	// routes the node through removeUndesiredLocked.
	slot := f.slot(t)
	slot.ServerID = ""
	if err := f.st.UpsertSlot(slot); err != nil {
		t.Fatalf("UpsertSlot without target: %v", err)
	}
	f.logins.set(f.acct.ID, account.LoginResult{
		AuthResult:      "OK",
		UserName:        "vendor-user",
		ConnectPassword: account.Secret(testConnectPassword),
		Servers:         map[string]apifacade.Server{},
		FetchedAt:       time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC),
	})

	block := f.gates.blockClose(node.ID)
	done := make(chan error, 1)
	go func() { done <- f.controller.SyncAccount(context.Background(), f.acct.ID) }()
	waitEntered(t, block)

	user := f.leaseUser(t)
	lease := f.lease(t, user.ID)
	if lease.NodeID != node.ID {
		t.Fatalf("lease node = %q, want %q", lease.NodeID, node.ID)
	}

	close(block.release)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("SyncAccount returned nil; the kept lease must be reported")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("SyncAccount did not return after the gate close released")
	}

	if _, ok := f.st.Node(node.ID); !ok {
		t.Fatal("cleanup deleted a node whose lease appeared during the gate close")
	}
	if _, ok := f.st.Lease(lease.ID); !ok {
		t.Fatal("cleanup deleted the lease created during the gate close")
	}
	kept, ok := f.st.Node(node.ID)
	if !ok {
		t.Fatal("node row disappeared")
	}
	if kept.State != model.NodeDown || kept.GatePort != 0 {
		t.Fatalf("kept node = %+v, want DOWN without a gate port", kept)
	}
}

// TestReleaseDeviceRefusedWhileSlotDraining reproduces finding 7 through the
// controller: drain a running slot (the gate keeps its listener), then try to
// release its device. The store refuses because a DRAINING node is still in
// service, so the reused gate keeps the identity its session was opened with.
func TestReleaseDeviceRefusedWhileSlotDraining(t *testing.T) {
	f := newSlotThreatFixture(t)
	f.sync(t)
	node := nodeByServer(t, f.st, f.acct.ID, "s1")
	recordID, ok := f.st.SlotDeviceRecordID(f.key)
	if !ok || recordID == "" {
		t.Fatal("the running slot has no device binding")
	}

	if err := f.controller.DrainNode(context.Background(), node.ID); err != nil {
		t.Fatalf("DrainNode: %v", err)
	}
	if _, err := f.st.ReleaseDevice(recordID); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("ReleaseDevice while DRAINING = %v, want store.ErrConflict", err)
	}
	if bound, ok := f.st.SlotDeviceRecordID(f.key); !ok || bound != recordID {
		t.Fatalf("slot binding after refused release = %q/%v, want %q", bound, ok, recordID)
	}

	// The gate is still running, so the next sync reuses it. The slot must
	// stay LIVE with the same owned identity; a release must not have made the
	// reused commit possible with no device.
	if err := f.controller.SyncAccount(context.Background(), f.acct.ID); err != nil {
		t.Fatalf("SyncAccount after refused release: %v", err)
	}
	slot := f.slot(t)
	if slot.State != model.SlotLive {
		t.Fatalf("slot after reuse = %q, want LIVE", slot.State)
	}
	if bound, ok := f.st.SlotDeviceRecordID(f.key); !ok || bound != recordID {
		t.Fatalf("slot binding after reuse = %q/%v, want %q", bound, ok, recordID)
	}
	if running := f.controller.Snapshot(); len(running) != 1 {
		t.Fatalf("running gates = %d, want the drained gate reused", len(running))
	}
}

// TestNoTargetCloseRevokesLeaseCreatedDuringClose covers the second window the
// review named: the slot keeps its persisted target, so the no-target plan does
// not route through node cleanup, but the gate still closes. A lease created
// between the slot's lease check and that close is a dead endpoint and must be
// revoked explicitly, with slot/generation, not left advertised.
func TestNoTargetCloseRevokesLeaseCreatedDuringClose(t *testing.T) {
	f := newSlotThreatFixture(t)
	f.sync(t)
	node := nodeByServer(t, f.st, f.acct.ID, "s1")

	// The slot keeps its target; the catalog no longer lists that server, so
	// the plan has no target and the node stays "desired" for cleanup.
	f.logins.set(f.acct.ID, account.LoginResult{
		AuthResult:      "OK",
		UserName:        "vendor-user",
		ConnectPassword: account.Secret(testConnectPassword),
		Servers:         map[string]apifacade.Server{},
		FetchedAt:       time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC),
	})
	block := f.gates.blockClose(node.ID)
	done := make(chan error, 1)
	go func() { done <- f.controller.SyncAccount(context.Background(), f.acct.ID) }()
	waitEntered(t, block)

	user := f.leaseUser(t)
	lease := f.lease(t, user.ID)
	if lease.NodeID != node.ID {
		t.Fatalf("lease node = %q, want %q", lease.NodeID, node.ID)
	}

	close(block.release)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("SyncAccount returned nil; the closed slot must be reported")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("SyncAccount did not return after the gate close released")
	}

	if _, ok := f.st.Lease(lease.ID); ok {
		t.Fatal("a lease created during the no-target close survived a dead listener")
	}
	found := false
	for _, event := range f.st.Audit(500) {
		if event.Action == "lease.revoke" && event.Meta["leaseId"] == lease.ID &&
			event.Meta["slot"] == "1" && event.Meta["generation"] == "1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no attributed lease.revoke for %s in %+v", lease.ID, f.st.Audit(50))
	}
	down := nodeByServer(t, f.st, f.acct.ID, "s1")
	if down.State != model.NodeDown || down.GatePort != 0 {
		t.Fatalf("closed node = %+v, want DOWN without a port", down)
	}
}

// TestEmptyRenderWithdrawsSubscription reproduces finding 5's publication half:
// when the last LIVE node leaves service, reconcile must remove the Resin
// subscription instead of leaving it advertising the dead port, and the next
// LIVE render must publish again.
func TestEmptyRenderWithdrawsSubscription(t *testing.T) {
	st := store.New()
	panel, fake, _ := newTestPanel(t, st)
	if err := panel.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if fake.subscriptionValue() == nil {
		t.Fatal("startup published no subscription")
	}

	drainLiveNodes(t, st)
	if _, err := panel.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile with no LIVE nodes: %v", err)
	}
	if got := fake.countExact("DELETE", "/api/v1/subscriptions/sub-1"); got != 1 {
		t.Fatalf("subscription deletes = %d, want 1 (calls: %v)", got, fake.paths())
	}
	if sub := fake.subscriptionValue(); sub != nil {
		t.Fatalf("Resin still serves subscription %q after an empty render", sub.Content)
	}
	topology, err := panel.Topology(context.Background())
	if err != nil {
		t.Fatalf("Topology: %v", err)
	}
	if topology.Content != "" || topology.LastGood != "" || topology.SubscriptionID != "" {
		t.Fatalf("topology after withdrawal = content %q lastGood %q sub %q, want empty",
			topology.Content, topology.LastGood, topology.SubscriptionID)
	}

	// A second empty reconcile is a no-op: there is nothing left to withdraw.
	if _, err := panel.Reconcile(context.Background()); err != nil {
		t.Fatalf("second empty Reconcile: %v", err)
	}
	if got := fake.countExact("DELETE", "/api/v1/subscriptions/sub-1"); got != 1 {
		t.Fatalf("subscription deletes after second reconcile = %d, want 1", got)
	}

	// The next LIVE node recreates the subscription with the live endpoint.
	var revived model.ProxyNode
	for _, node := range st.Nodes() {
		if node.State == model.NodeDraining {
			var err error
			revived, err = st.UndrainNode(node.ID)
			if err != nil {
				t.Fatalf("UndrainNode(%s): %v", node.ID, err)
			}
			break
		}
	}
	if revived.State != model.NodeLive {
		t.Fatalf("revived node state = %q, want LIVE", revived.State)
	}
	if _, err := panel.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile after undrain: %v", err)
	}
	sub := fake.subscriptionValue()
	if sub == nil {
		t.Fatal("subscription was not recreated after a LIVE node returned")
	}
	if !strings.Contains(sub.Content, "socks5://127.0.0.1:") {
		t.Fatalf("recreated subscription content = %q, want a live endpoint", sub.Content)
	}
}
