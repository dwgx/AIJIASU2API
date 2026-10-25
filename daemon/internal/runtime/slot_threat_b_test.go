package runtime_test

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"ajsaccountd/internal/account"
	"ajsaccountd/internal/gate"
	"ajsaccountd/internal/model"
	"ajsaccountd/internal/runtime"
	"ajsaccountd/internal/store"
	"ajsproxy/apifacade"
)

// The tests in this file are the containment proofs of REVIEW-MULTISLOT-THREAT.md
// findings 4, 5, 8 and 10. Each test fails on the code that preceded the fix:
//
//  4. slot planning could pick a server another slot already served, opening a
//     second vendor session and then closing the sibling;
//  5. a failed break-before-make switch left a dead lease and a stale
//     publication, and a later cleanup silently deleted the leased node;
//  8. a same-server reconnect bumped the slot generation but left the lease on
//     the old generation;
//  10. switch/disconnect audits and metered flow rows could not attribute the
//      action to a slot and session generation.

// slotPairFixture is one account with the device pool enabled, a recording gate
// factory, a recording reconciler and a pinned clock. It can serve one or two
// slots depending on the requested capacity.
type slotPairFixture struct {
	st         *store.Store
	acct       model.Account
	logins     *fakeLoginResults
	opener     *threatOpener
	gates      *switchGateFactory
	reconciler *fakeReconciler
	clock      *switchClock
	controller *runtime.NodeController
	key1       model.SlotKey
	key2       model.SlotKey
}

func newSlotPairFixture(t *testing.T, slots int) *slotPairFixture {
	t.Helper()
	st := newNodeTestPoolStore(t)
	acct := newNodeTestAccount(t, st, "slot-pair")
	grantSlots(t, st, acct, slots)
	logins := newFakeLoginResults()
	logins.set(acct.ID, account.LoginResult{
		AuthResult:      "OK",
		UserName:        "vendor-user",
		ConnectPassword: account.Secret(testConnectPassword),
		Servers: map[string]apifacade.Server{
			"s1": testServer("s1"),
			"s2": testServer("s2"),
		},
		FetchedAt: time.Date(2026, 10, 26, 11, 0, 0, 0, time.UTC),
	})
	opener := newThreatOpener()
	gates := newSwitchGateFactory()
	reconciler := &fakeReconciler{}
	clock := newSwitchClock()
	controller, err := runtime.NewNodeController(runtime.NodeControllerConfig{
		GateAuth:         gate.AuthLoopback,
		Store:            st,
		Logins:           logins,
		Sessions:         opener,
		Gates:            gates.build,
		Reconciler:       reconciler,
		MaxServers:       0, // the slot allowance decides
		Now:              clock.Now,
		LivenessInterval: -1,
		ConnectCooldown:  -1,
		Logger:           discardLogger(),
	})
	if err != nil {
		t.Fatalf("NewNodeController: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = controller.Stop(stopCtx)
	})
	return &slotPairFixture{
		st: st, acct: acct, logins: logins, opener: opener, gates: gates,
		reconciler: reconciler, clock: clock, controller: controller,
		key1: model.SlotKey{AccountID: acct.ID, Index: 1},
		key2: model.SlotKey{AccountID: acct.ID, Index: 2},
	}
}

func (f *slotPairFixture) sync(t *testing.T) {
	t.Helper()
	if err := f.controller.SyncAccount(context.Background(), f.acct.ID); err != nil {
		t.Fatalf("SyncAccount: %v", err)
	}
}

// setServers replaces the retained catalog. An empty map makes every slot plan
// resolve to no server.
func (f *slotPairFixture) setServers(servers map[string]apifacade.Server) {
	f.logins.set(f.acct.ID, account.LoginResult{
		AuthResult:      "OK",
		UserName:        "vendor-user",
		ConnectPassword: account.Secret(testConnectPassword),
		Servers:         servers,
		FetchedAt:       time.Date(2026, 10, 26, 11, 30, 0, 0, time.UTC),
	})
}

// leaseUser creates one panel user and one lease for the fixture account.
func (f *slotPairFixture) leaseUser(t *testing.T, name string) (model.PanelUser, model.Lease) {
	t.Helper()
	user, err := f.st.CreateUser(model.CreateUserInput{Name: name})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	lease, _, err := f.st.CreateLease(model.CreateLeaseInput{
		UserID: user.ID, Platform: model.PlatformLinux, AccountID: f.acct.ID,
	})
	if err != nil {
		t.Fatalf("CreateLease: %v", err)
	}
	return user, lease
}

func (f *slotPairFixture) node(t *testing.T, key model.SlotKey, serverID string) model.ProxyNode {
	t.Helper()
	node, ok := f.st.Node(key.NodeID(serverID))
	if !ok {
		t.Fatalf("node %s is missing", key.NodeID(serverID))
	}
	return node
}

func (f *slotPairFixture) auditMeta(t *testing.T, action string) map[string]string {
	t.Helper()
	for _, event := range f.st.Audit(500) {
		if event.Action == action {
			return event.Meta
		}
	}
	t.Fatalf("audit %q is missing", action)
	return nil
}

// TestSlotPlanningNeverStealsSiblingServer proves finding 4: when slot 1's
// target disappears, its fallback must not take the server slot 2 is already
// serving. Slot 2 keeps its gate, session and node; only slot 1 fails.
func TestSlotPlanningNeverStealsSiblingServer(t *testing.T) {
	f := newSlotPairFixture(t, 2)
	f.sync(t)
	if got := f.opener.callCount(); got != 2 {
		t.Fatalf("session opens after first sync = %d, want 2", got)
	}
	node2 := f.node(t, f.key2, "s2")
	if node2.State != model.NodeLive {
		t.Fatalf("slot 2 node = %+v, want LIVE", node2)
	}
	gate2 := f.gates.gate(f.key2.NodeID("s2"))
	if gate2 == nil {
		t.Fatal("slot 2 gate is missing")
	}

	// Slot 1's server leaves the catalog; slot 2 still owns s2.
	f.setServers(map[string]apifacade.Server{"s2": testServer("s2")})
	if err := f.controller.SyncAccount(context.Background(), f.acct.ID); err != nil {
		t.Fatalf("second SyncAccount: %v", err)
	}

	if gate2.closed.Load() {
		t.Fatal("slot 2's gate was closed: slot 1 stole its server")
	}
	if got := len(f.opener.requestsFor("s2")); got != 1 {
		t.Fatalf("s2 session opens = %d, want 1: a second session would kick slot 2", got)
	}
	if after := f.node(t, f.key2, "s2"); after.State != model.NodeLive || after.GatePort != node2.GatePort {
		t.Fatalf("slot 2 node after the sync = %+v, want the untouched LIVE row", after)
	}
	if slot, ok := f.st.Slot(f.key2); !ok || slot.State != model.SlotLive {
		t.Fatalf("slot 2 row = %+v, want LIVE", slot)
	}
	if slot, ok := f.st.Slot(f.key1); !ok || slot.State == model.SlotLive {
		t.Fatalf("slot 1 row = %+v, want it down", slot)
	}
}

// TestFailedSwitchRevokesLeaseAndPublication proves finding 5's failure half:
// once break-before-make closed the old session, a failed open revokes the old
// lease, takes the old node out of service (no port, no egress) and reconciles
// the publication in the same request.
func TestFailedSwitchRevokesLeaseAndPublication(t *testing.T) {
	f := newSlotPairFixture(t, 1)
	f.sync(t)
	_, lease := f.leaseUser(t, "failed-switch-bot")
	oldNode := f.node(t, f.key1, "s1")
	reconciles := f.reconciler.callCount()

	f.opener.mu.Lock()
	f.opener.fail["s2"] = errors.New("vendor refused the new session")
	f.opener.mu.Unlock()

	res, err := f.controller.ConnectSlot(context.Background(), f.key1, "s2", "", "manual")
	if err == nil {
		t.Fatalf("ConnectSlot succeeded, want a failure: %+v", res)
	}
	if res.Rollback != runtime.ReasonMetadataOnly {
		t.Fatalf("rollback = %q, want %q", res.Rollback, runtime.ReasonMetadataOnly)
	}
	if got := f.st.Leases(); len(got) != 0 {
		t.Fatalf("leases after the failed switch = %+v, want the old lease revoked", got)
	}
	node := f.node(t, f.key1, "s1")
	if node.State != model.NodeDown || node.GatePort != 0 || node.EgressIP != "" {
		t.Fatalf("old node = %+v, want DOWN without port or egress", node)
	}
	if node.ID != oldNode.ID {
		t.Fatalf("old node id = %q, want %q", node.ID, oldNode.ID)
	}
	revoke := f.auditMeta(t, "lease.revoke")
	if revoke["leaseId"] != lease.ID || revoke["reason"] != "switch_failed:session_open_failed" {
		t.Fatalf("lease.revoke meta = %+v, want the failed-switch reason", revoke)
	}
	switchMeta := f.auditMeta(t, "slot.switch_failed")
	if switchMeta["leaseIds"] != lease.ID || switchMeta["nodeId"] != oldNode.ID {
		t.Fatalf("slot.switch_failed meta = %+v, want the revoked lease named", switchMeta)
	}
	if f.reconciler.callCount() <= reconciles {
		t.Fatal("no reconcile ran after the failed switch: the stale publication survived")
	}
}

// TestCleanupKeepsLeasedGate proves finding 5's cleanup half: a sync that
// resolves no server must not close a gate or delete a node that still carries
// an active lease. The slot stays serving and the pass reports it.
func TestCleanupKeepsLeasedGate(t *testing.T) {
	f := newSlotPairFixture(t, 1)
	f.sync(t)
	_, lease := f.leaseUser(t, "cleanup-bot")
	gate := f.gates.gate(f.key1.NodeID("s1"))
	if gate == nil {
		t.Fatal("slot 1 gate is missing")
	}

	// A failed target change left the slot row pointing at s2 while the live
	// gate and lease are still on s1, and the catalog no longer lists s1.
	slot, ok := f.st.Slot(f.key1)
	if !ok {
		t.Fatalf("slot %s is missing", f.key1.String())
	}
	slot.ServerID = "s2"
	if err := f.st.UpsertSlot(slot); err != nil {
		t.Fatalf("UpsertSlot: %v", err)
	}
	f.setServers(map[string]apifacade.Server{})

	if err := f.controller.SyncAccount(context.Background(), f.acct.ID); err == nil {
		t.Fatal("SyncAccount reported success without a planned slot")
	}
	if gate.closed.Load() {
		t.Fatal("the leased gate was closed by cleanup")
	}
	if _, ok := f.st.Node(f.key1.NodeID("s1")); !ok {
		t.Fatal("the leased node was deleted by cleanup")
	}
	if leases := f.st.Leases(); len(leases) != 1 || leases[0].ID != lease.ID {
		t.Fatalf("leases after cleanup = %+v, want the live lease preserved", leases)
	}
}

// TestPlanningKeepsBlockedSlotServer proves finding 4's blocked-slot case: a
// slot above the capacity that still serves a lease keeps its server reserved,
// so the in-capacity slot's fallback cannot open a second session against it.
func TestPlanningKeepsBlockedSlotServer(t *testing.T) {
	f := newSlotPairFixture(t, 2)
	f.sync(t)
	_, lease1 := f.leaseUser(t, "blocked-bot-1")
	_, lease2 := f.leaseUser(t, "blocked-bot-2")
	if lease1.NodeID == lease2.NodeID {
		t.Fatalf("both leases landed on %q", lease1.NodeID)
	}
	gate2 := f.gates.gate(f.key2.NodeID("s2"))
	if gate2 == nil {
		t.Fatal("slot 2 gate is missing")
	}

	// Capacity shrinks to one; slot 2 is above it but a lease pins it, so the
	// reduction keeps it serving. Slot 1 then loses its server.
	grantSlots(t, f.st, f.acct, 1)
	f.setServers(map[string]apifacade.Server{"s2": testServer("s2")})
	if err := f.controller.SyncAccount(context.Background(), f.acct.ID); err == nil {
		t.Fatal("SyncAccount reported success without a planned slot")
	}

	if got := len(f.opener.requestsFor("s2")); got != 1 {
		t.Fatalf("s2 session opens = %d, want 1: slot 1 opened a second session on the blocked slot's server", got)
	}
	if gate2.closed.Load() {
		t.Fatal("slot 2's gate was closed")
	}
	leases := f.st.Leases()
	if len(leases) != 2 {
		t.Fatalf("leases = %+v, want both preserved", leases)
	}
	for _, lease := range leases {
		if lease.ID != lease1.ID && lease.ID != lease2.ID {
			t.Fatalf("unexpected lease %+v", lease)
		}
	}
	if slot, ok := f.st.Slot(f.key2); !ok || slot.State != model.SlotLive {
		t.Fatalf("blocked slot row = %+v, want LIVE", slot)
	}
}

// TestSameServerReconnectAdvancesLeaseGeneration proves finding 8: a reconnect
// on the same server keeps the node id and port but advances the slot
// generation, and the lease follows it. The success audit records the new
// generation (finding 10's audit half).
func TestSameServerReconnectAdvancesLeaseGeneration(t *testing.T) {
	f := newSlotPairFixture(t, 1)
	f.sync(t)
	_, lease := f.leaseUser(t, "reconnect-bot")
	if lease.Slot != 1 || lease.Generation != 1 {
		t.Fatalf("lease = %+v, want slot 1 generation 1", lease)
	}
	before := f.node(t, f.key1, "s1")
	// The initial start is audited with its slot, node and generation.
	if meta := f.auditMeta(t, "slot.start"); meta["generation"] != "1" || meta["nodeId"] != before.ID {
		t.Fatalf("slot.start meta = %+v, want generation 1 on %q", meta, before.ID)
	}

	res, err := f.controller.ReconnectSlot(context.Background(), f.key1, "", "reconnect")
	if err != nil {
		t.Fatalf("ReconnectSlot: %v", err)
	}
	if res.Generation != 2 {
		t.Fatalf("result generation = %d, want 2", res.Generation)
	}
	leases := f.st.Leases()
	if len(leases) != 1 {
		t.Fatalf("leases after reconnect = %+v, want the lease kept", leases)
	}
	if leases[0].ID != lease.ID || leases[0].NodeID != before.ID {
		t.Fatalf("lease after reconnect = %+v, want it kept on %q", leases[0], before.ID)
	}
	if leases[0].Generation != 2 || leases[0].Slot != 1 {
		t.Fatalf("lease generation = %d slot = %d, want 2/1", leases[0].Generation, leases[0].Slot)
	}
	after := f.node(t, f.key1, "s1")
	if after.ID != before.ID || after.GatePort != before.GatePort || after.State != model.NodeLive {
		t.Fatalf("node after reconnect = %+v, want the same id/port LIVE", after)
	}
	if meta := f.auditMeta(t, "catalog.reconnect"); meta["generation"] != "2" {
		t.Fatalf("catalog.reconnect meta = %+v, want generation 2", meta)
	}
}

// TestDisconnectAllAuditNamesEverySlot proves finding 10's disconnect half: a
// cascade audit identifies every affected slot, node, generation and lease
// instead of one aggregate row.
func TestDisconnectAllAuditNamesEverySlot(t *testing.T) {
	f := newSlotPairFixture(t, 2)
	f.sync(t)
	_, lease1 := f.leaseUser(t, "disconnect-bot-1")
	_, lease2 := f.leaseUser(t, "disconnect-bot-2")
	if lease1.Slot == lease2.Slot {
		t.Fatalf("leases landed on one slot: %+v / %+v", lease1, lease2)
	}

	res, err := f.controller.DisconnectAccount(context.Background(), f.acct.ID)
	if err != nil {
		t.Fatalf("DisconnectAccount: %v", err)
	}
	if len(res.Slots) != 2 {
		t.Fatalf("disconnect slots = %+v, want two entries", res.Slots)
	}
	meta := f.auditMeta(t, "catalog.disconnect")
	for _, lease := range []model.Lease{lease1, lease2} {
		prefix := "slot." + strconv.Itoa(lease.Slot) + "."
		if meta[prefix+"nodeId"] != lease.NodeID {
			t.Fatalf("audit meta[%snodeId] = %q, want %q", prefix, meta[prefix+"nodeId"], lease.NodeID)
		}
		if meta[prefix+"generation"] != "1" {
			t.Fatalf("audit meta[%sgeneration] = %q, want 1", prefix, meta[prefix+"generation"])
		}
		if meta[prefix+"leaseIds"] != lease.ID {
			t.Fatalf("audit meta[%sleaseIds] = %q, want %q", prefix, meta[prefix+"leaseIds"], lease.ID)
		}
	}
}

// TestGateFlowRowsCarrySlotAndGeneration proves finding 10's flow half: a real
// flow through a real gate is metered with the slot and session generation that
// served it.
func TestGateFlowRowsCarrySlotAndGeneration(t *testing.T) {
	fixture := newMeterFixture(t, "meter-slot-attribution")
	driveFlow(t, fixture.gatePort(t), "example.com", []byte("ping"))

	rows := waitGateRows(t, fixture.store, 1)
	if len(rows) != 1 {
		t.Fatalf("metered rows = %d, want 1", len(rows))
	}
	if rows[0].Slot != 1 || rows[0].Generation != 1 {
		t.Fatalf("request row slot/generation = %d/%d, want 1/1", rows[0].Slot, rows[0].Generation)
	}
}
