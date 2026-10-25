package runtime_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"ajsaccountd/internal/account"
	"ajsaccountd/internal/model"
	"ajsaccountd/internal/runtime"
	"ajsaccountd/internal/store"
	"ajsproxy/apifacade"
)

// The tests in this file are the containment proofs of REVIEW-MULTISLOT-THREAT.md
// findings 1, 2 and 7. Each finding's primary test fails on the code that
// preceded the fix:
//
//  1. a vendor kick left the slot LIVE and leasable;
//  2. a start could commit a gate after StopAccount returned, leaving an orphan
//     listener behind the delete/logout mutation;
//  7. a device release during a slow start could commit LIVE with a retired
//     identity.

// threatBlock holds one session open until the test releases it. The channels
// make the race deterministic: the test knows the exact instant the session is
// opening, with the device material already loaded.
type threatBlock struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

// threatOpener wraps the recording fake opener and can hold one server's open.
type threatOpener struct {
	*fakeSessionOpener

	mu      sync.Mutex
	blocked map[string]*threatBlock
}

func newThreatOpener() *threatOpener {
	return &threatOpener{
		fakeSessionOpener: newFakeSessionOpener(),
		blocked:           make(map[string]*threatBlock),
	}
}

// block makes the next Open for serverID wait until release is closed.
func (o *threatOpener) block(serverID string) *threatBlock {
	block := &threatBlock{entered: make(chan struct{}), release: make(chan struct{})}
	o.mu.Lock()
	o.blocked[serverID] = block
	o.mu.Unlock()
	return block
}

func (o *threatOpener) Open(ctx context.Context, req account.SessionRequest) (account.FlowSession, error) {
	o.mu.Lock()
	block := o.blocked[req.Server.ID]
	o.mu.Unlock()
	if block != nil {
		block.once.Do(func() { close(block.entered) })
		select {
		case <-block.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return o.fakeSessionOpener.Open(ctx, req)
}

// slotThreatFixture is one account with the device pool enabled, a recording
// gate factory (no loopback bind, so the tests need no free port) and a pinned
// clock.
type slotThreatFixture struct {
	st         *store.Store
	acct       model.Account
	logins     *fakeLoginResults
	opener     *threatOpener
	gates      *switchGateFactory
	controller *runtime.NodeController
	clock      *switchClock
	key        model.SlotKey
}

func newSlotThreatFixture(t *testing.T) *slotThreatFixture {
	t.Helper()
	// The background scan is off by default: every test forces its own scan
	// through CheckLiveness, so no assertion depends on a timer.
	return newSlotThreatFixtureInterval(t, -1)
}

func newSlotThreatFixtureInterval(t *testing.T, liveness time.Duration) *slotThreatFixture {
	t.Helper()
	st := newNodeTestPoolStore(t)
	acct := newNodeTestAccount(t, st, "slot-threat")
	logins := newFakeLoginResults()
	logins.set(acct.ID, account.LoginResult{
		AuthResult:      "OK",
		UserName:        "vendor-user",
		ConnectPassword: account.Secret(testConnectPassword),
		Servers: map[string]apifacade.Server{
			"s1": testServer("s1"),
			"s2": testServer("s2"),
		},
		FetchedAt: time.Date(2026, 10, 24, 11, 0, 0, 0, time.UTC),
	})
	opener := newThreatOpener()
	gates := newSwitchGateFactory()
	clock := newSwitchClock()
	controller, err := runtime.NewNodeController(runtime.NodeControllerConfig{
		Store:      st,
		Logins:     logins,
		Sessions:   opener,
		Gates:      gates.build,
		MaxServers: 1,
		Now:        clock.Now,
		// Negative disables the background scan; a positive value is the
		// monitor test's window.
		LivenessInterval: liveness,
		Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("NewNodeController: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = controller.Stop(stopCtx)
	})
	return &slotThreatFixture{
		st: st, acct: acct, logins: logins, opener: opener, gates: gates,
		controller: controller, clock: clock,
		key: model.SlotKey{AccountID: acct.ID, Index: 1},
	}
}

func (f *slotThreatFixture) sync(t *testing.T) {
	t.Helper()
	if err := f.controller.SyncAccount(context.Background(), f.acct.ID); err != nil {
		t.Fatalf("SyncAccount: %v", err)
	}
}

func (f *slotThreatFixture) slot(t *testing.T) model.AccountSlot {
	t.Helper()
	slot, ok := f.st.Slot(f.key)
	if !ok {
		t.Fatalf("slot %s is missing", f.key.String())
	}
	return slot
}

// auditCount counts one audit action.
func (f *slotThreatFixture) auditCount(action string) int {
	count := 0
	for _, event := range f.st.Audit(500) {
		if event.Action == action {
			count++
		}
	}
	return count
}

// leaseUser creates the panel user a lease is issued to.
func (f *slotThreatFixture) leaseUser(t *testing.T) model.PanelUser {
	t.Helper()
	user, err := f.st.CreateUser(model.CreateUserInput{Name: "threat-bot"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	return user
}

// lease creates one lease for the fixture account and fails the test otherwise.
func (f *slotThreatFixture) lease(t *testing.T, userID string) model.Lease {
	t.Helper()
	lease, _, err := f.st.CreateLease(model.CreateLeaseInput{
		UserID: userID, Platform: model.PlatformLinux, AccountID: f.acct.ID,
	})
	if err != nil {
		t.Fatalf("CreateLease: %v", err)
	}
	return lease
}

// waitEntered blocks until the opener reached the blocked session open.
func waitEntered(t *testing.T, block *threatBlock) {
	t.Helper()
	select {
	case <-block.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the session opener never entered")
	}
}

// TestVendorKickIsContained proves finding 1: a lost vendor session leaves LIVE
// within one liveness window, its leases are revoked and the slot is not
// leasable while it is dead; the retry window then allows exactly one
// controlled reopen.
func TestVendorKickIsContained(t *testing.T) {
	f := newSlotThreatFixture(t)
	f.sync(t)
	node := nodeByServer(t, f.st, f.acct.ID, "s1")
	if node.State != model.NodeLive {
		t.Fatalf("node state = %q, want LIVE", node.State)
	}
	slot := f.slot(t)
	if slot.State != model.SlotLive || slot.Generation != 1 {
		t.Fatalf("slot = %+v, want LIVE generation 1", slot)
	}
	user := f.leaseUser(t)
	lease := f.lease(t, user.ID)
	if lease.Slot != 1 || lease.Generation != 1 {
		t.Fatalf("lease = %+v, want slot 1 generation 1", lease)
	}

	// The vendor takes the account over: the session keeps its SID and the
	// local listener keeps accepting, exactly the state the review described.
	session := f.opener.session("s1")
	session.kill()

	if got := f.controller.CheckLiveness(); got != 1 {
		t.Fatalf("CheckLiveness contained %d gates, want 1", got)
	}

	// Out of service: no gate, no session, no listener in the controller.
	if running := f.controller.Snapshot(); len(running) != 0 {
		t.Fatalf("snapshot = %+v, want no running gate after a kick", running)
	}
	if gate := f.gates.gate(slot1NodeID(f.acct.ID, "s1")); gate == nil || !gate.closed.Load() {
		t.Fatalf("gate = %+v, want the kicked gate closed", gate)
	}
	if !session.isClosed() {
		t.Fatal("the kicked session is still open")
	}

	// The node is out of service and the slot is DOWN with the kick marker.
	node = nodeByServer(t, f.st, f.acct.ID, "s1")
	if node.State != model.NodeDown || node.GatePort != 0 {
		t.Fatalf("node = %+v, want DOWN without a gate port", node)
	}
	slot = f.slot(t)
	if slot.State != model.SlotDown {
		t.Fatalf("slot state = %q, want DOWN", slot.State)
	}
	if slot.Health != model.SlotHealthVendorKick || slot.KickCount != 1 {
		t.Fatalf("slot = %+v, want health vendor_kick and kick count 1", slot)
	}
	wantRetry := f.clock.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339)
	if slot.RetryAfter != wantRetry {
		t.Fatalf("retryAfter = %q, want %q", slot.RetryAfter, wantRetry)
	}
	if f.auditCount("slot.vendor_kick") != 1 {
		t.Fatalf("audits = %+v, want one slot.vendor_kick", f.st.Audit(500))
	}

	// The customer lease is gone and was revoked explicitly, not silently.
	if _, ok := f.st.Lease(lease.ID); ok {
		t.Fatalf("lease %s survived the kick", lease.ID)
	}
	if f.auditCount("lease.revoke") != 1 {
		t.Fatalf("audits = %+v, want one lease.revoke", f.st.Audit(500))
	}

	// Not leasable while dead: admission has no live node to hand out.
	_, _, err := f.st.CreateLease(model.CreateLeaseInput{
		UserID: user.ID, Platform: model.PlatformLinux, AccountID: f.acct.ID,
	})
	var leaseErr *store.LeaseError
	if !errors.As(err, &leaseErr) || leaseErr.Reason != "no_capacity" {
		t.Fatalf("lease after kick = %v, want no_capacity", err)
	}

	// A sync inside the retry window must not reopen the slot: the vendor still
	// holds the stolen session.
	opens := f.opener.callCount()
	if err := f.controller.SyncAccount(context.Background(), f.acct.ID); err == nil {
		t.Fatal("SyncAccount inside the retry window succeeded")
	}
	if got := f.opener.callCount(); got != opens {
		t.Fatalf("session opens inside the retry window = %d, want %d", got, opens)
	}
	if slot = f.slot(t); slot.State != model.SlotDown || slot.Health != model.SlotHealthVendorKick {
		t.Fatalf("slot after blocked sync = %+v, want DOWN/vendor_kick", slot)
	}

	// One controlled reopen after the window: the same pair, a new generation.
	f.clock.Advance(5 * time.Minute)
	if err := f.controller.SyncAccount(context.Background(), f.acct.ID); err != nil {
		t.Fatalf("SyncAccount after the retry window: %v", err)
	}
	if got := f.opener.callCount(); got != opens+1 {
		t.Fatalf("session opens after the window = %d, want %d", got, opens+1)
	}
	slot = f.slot(t)
	if slot.State != model.SlotLive || slot.Health != model.SlotHealthHealthy {
		t.Fatalf("slot after reopen = %+v, want LIVE/healthy", slot)
	}
	if slot.Generation != 2 {
		t.Fatalf("generation after reopen = %d, want 2", slot.Generation)
	}
	if node = nodeByServer(t, f.st, f.acct.ID, "s1"); node.State != model.NodeLive || node.GatePort == 0 {
		t.Fatalf("node after reopen = %+v, want LIVE with a port", node)
	}
	t.Logf("kick: slot %s DOWN/vendor_kick, lease %s revoked, reopen at generation %d on port %d",
		f.key.String(), lease.ID, slot.Generation, node.GatePort)
}

// TestLivenessMonitorContainsWithinOneWindow proves the background scan itself
// (not just the exported CheckLiveness) contains a kick inside one window: the
// controller starts the loop on its own interval and the slot leaves LIVE
// without anyone calling it.
func TestLivenessMonitorContainsWithinOneWindow(t *testing.T) {
	const window = 2 * time.Millisecond
	f := newSlotThreatFixtureInterval(t, window)
	f.sync(t)
	f.opener.session("s1").kill()

	deadline := time.Now().Add(10 * time.Second)
	for {
		slot, ok := f.st.Slot(f.key)
		if ok && slot.State == model.SlotDown && slot.Health == model.SlotHealthVendorKick {
			if running := f.controller.Snapshot(); len(running) != 0 {
				t.Fatalf("monitor left %d gates running", len(running))
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("kick was not contained within the %s window: %+v", window, slot)
		}
		time.Sleep(window)
	}
}

// TestLivenessClosesGateOfDeletedAccount proves the delete safety net: if the
// store cascade commits while a gate is still running (a login that raced into
// the delete window), the next liveness scan closes the listener and session
// instead of leaving them until process exit.
func TestLivenessClosesGateOfDeletedAccount(t *testing.T) {
	f := newSlotThreatFixture(t)
	f.sync(t)
	gate := f.gates.gate(slot1NodeID(f.acct.ID, "s1"))
	if gate == nil {
		t.Fatal("the sync built no gate")
	}

	// The cascade runs without the stop hook, which is exactly the ordering a
	// racing start can produce.
	if err := f.st.DeleteAccount(f.acct.ID); err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}
	if got := f.controller.CheckLiveness(); got != 1 {
		t.Fatalf("CheckLiveness closed %d gates, want 1", got)
	}
	if !gate.closed.Load() {
		t.Fatal("the deleted account's gate is still open")
	}
	if session := f.opener.session("s1"); session == nil || !session.isClosed() {
		t.Fatalf("session = %+v, want the deleted account's session closed", session)
	}
	if running := f.controller.Snapshot(); len(running) != 0 {
		t.Fatalf("snapshot = %+v, want no running gate", running)
	}
	found := false
	for _, event := range f.st.Audit(500) {
		if event.Action == "slot.orphan_closed" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no slot.orphan_closed audit: %+v", f.st.Audit(500))
	}
}

// TestVendorKickQuarantinesRepeats proves the repeated-kick stop: the third
// kick moves the slot to quarantined, which no automatic sync retries.
func TestVendorKickQuarantinesRepeats(t *testing.T) {
	f := newSlotThreatFixture(t)
	f.sync(t)
	server := "s1"
	for kick := 1; kick <= store.SlotKickQuarantineThreshold; kick++ {
		f.opener.session(server).kill()
		if got := f.controller.CheckLiveness(); got != 1 {
			t.Fatalf("kick %d: CheckLiveness contained %d gates, want 1", kick, got)
		}
		slot := f.slot(t)
		if slot.KickCount != kick {
			t.Fatalf("kick %d: count = %d", kick, slot.KickCount)
		}
		if kick < store.SlotKickQuarantineThreshold {
			if slot.State != model.SlotDown {
				t.Fatalf("kick %d: state = %q, want DOWN", kick, slot.State)
			}
			f.clock.Advance(5 * time.Minute)
			if err := f.controller.SyncAccount(context.Background(), f.acct.ID); err != nil {
				t.Fatalf("kick %d: reopen: %v", kick, err)
			}
			continue
		}
		if slot.State != model.SlotQuarantined {
			t.Fatalf("kick %d: state = %q, want quarantined", kick, slot.State)
		}
	}
	opens := f.opener.callCount()
	f.clock.Advance(24 * time.Hour)
	if err := f.controller.SyncAccount(context.Background(), f.acct.ID); err == nil {
		t.Fatal("SyncAccount retried a quarantined slot")
	}
	if got := f.opener.callCount(); got != opens {
		t.Fatalf("session opens for a quarantined slot = %d, want %d", got, opens)
	}
	if slot := f.slot(t); slot.State != model.SlotQuarantined {
		t.Fatalf("quarantined slot changed to %q", slot.State)
	}
	t.Logf("quarantine after %d kicks: no automatic reopen after 24h",
		store.SlotKickQuarantineThreshold)
}

// TestStopAccountWaitsForStartAndFencesLateStart proves finding 2: a delete or
// logout hook that runs while a slot start is opening waits for it to settle
// and then closes what it committed; every later start for the account refuses
// until a fresh login, so no orphan listener or vendor session survives the
// store mutation.
func TestStopAccountWaitsForStartAndFencesLateStart(t *testing.T) {
	f := newSlotThreatFixture(t)
	f.sync(t)
	block := f.opener.block("s2")

	// A switch to s2 is in flight and holds the account lock while the vendor
	// open is blocked.
	connectErr := make(chan error, 1)
	go func() {
		_, err := f.controller.ConnectSlot(context.Background(), f.key, "s2", "", "manual")
		connectErr <- err
	}()
	waitEntered(t, block)

	// The delete/logout hook runs now. It must not return while the start can
	// still commit a gate.
	stopDone := make(chan error, 1)
	go func() { stopDone <- f.controller.StopAccount(context.Background(), f.acct.ID) }()
	select {
	case err := <-stopDone:
		t.Fatalf("StopAccount returned (%v) while a slot start was still opening", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(block.release)
	if err := <-connectErr; err != nil {
		t.Fatalf("ConnectSlot: %v", err)
	}
	if err := <-stopDone; err != nil {
		t.Fatalf("StopAccount: %v", err)
	}

	// Nothing the start committed survives: no running gate, no open session.
	if running := f.controller.Snapshot(); len(running) != 0 {
		t.Fatalf("snapshot = %+v, want no running gate after the stop", running)
	}
	for _, serverID := range []string{"s1", "s2"} {
		if gate := f.gates.gate(slot1NodeID(f.acct.ID, serverID)); gate != nil && !gate.closed.Load() {
			t.Fatalf("%s gate is still open after StopAccount", serverID)
		}
		if session := f.opener.session(serverID); session != nil && !session.isClosed() {
			t.Fatalf("%s session is still open after StopAccount", serverID)
		}
	}

	// A late start after the hook must refuse: the store still says ACTIVE and
	// the connect cooldown has expired, so only the fence can stop it.
	f.clock.Advance(runtime.DefaultConnectCooldown + time.Second)
	built := f.gates.built()
	_, err := f.controller.ConnectSlot(context.Background(), f.key, "s1", "", "manual")
	requireSwitchReason(t, err, runtime.ReasonAccountState)
	if got := f.gates.built(); got != built {
		t.Fatalf("gates built after the late connect = %d, want %d", got, built)
	}
	if running := f.controller.Snapshot(); len(running) != 0 {
		t.Fatalf("late connect left %d gates running", len(running))
	}

	// The login sync that was already retained before the stop is late too.
	if err := f.controller.SyncAccount(context.Background(), f.acct.ID); err == nil {
		t.Fatal("SyncAccount with the pre-stop login result reopened the account")
	}
	if got := f.opener.callCount(); got != 2 {
		t.Fatalf("session opens after the late sync = %d, want 2 (s1 and s2 only)", got)
	}

	// A login completed after the stop is the operator reopening the account:
	// it clears the fence, so logout followed by login keeps working.
	f.logins.set(f.acct.ID, account.LoginResult{
		AuthResult:      "OK",
		UserName:        "vendor-user",
		ConnectPassword: account.Secret(testConnectPassword),
		Servers: map[string]apifacade.Server{
			"s1": testServer("s1"),
			"s2": testServer("s2"),
		},
		FetchedAt: f.clock.Now().Add(time.Second),
	})
	if err := f.controller.SyncAccount(context.Background(), f.acct.ID); err != nil {
		t.Fatalf("SyncAccount after a fresh login: %v", err)
	}
	if running := f.controller.Snapshot(); len(running) != 1 {
		t.Fatalf("snapshot after the fresh login = %+v, want one running gate", running)
	}
}

// TestDeviceReleaseDuringSlowStartFailsClosed proves finding 7: a release that
// lands between the material read and the LIVE commit fails the start, so no
// LIVE node is published with a retired identity.
func TestDeviceReleaseDuringSlowStartFailsClosed(t *testing.T) {
	f := newSlotThreatFixture(t)
	block := f.opener.block("s1")

	syncErr := make(chan error, 1)
	go func() { syncErr <- f.controller.SyncAccount(context.Background(), f.acct.ID) }()
	waitEntered(t, block)

	// The material is loaded and the session open is blocked; the slot owns a
	// device but has no LIVE node yet, so the operator release succeeds.
	slot := f.slot(t)
	if slot.DeviceID == "" {
		t.Fatal("the slot has no device binding to release")
	}
	if slot.State == model.SlotLive {
		t.Fatalf("slot is already LIVE before the session opened: %+v", slot)
	}
	released, err := f.st.ReleaseDevice(slot.DeviceID)
	if err != nil {
		t.Fatalf("ReleaseDevice: %v", err)
	}
	if released.State != store.DeviceRetired {
		t.Fatalf("released device state = %q, want retired", released.State)
	}

	close(block.release)
	if err := <-syncErr; err == nil {
		t.Fatal("SyncAccount committed LIVE after its device was released")
	}

	// The start closed what it opened and never became LIVE.
	if running := f.controller.Snapshot(); len(running) != 0 {
		t.Fatalf("snapshot = %+v, want no running gate", running)
	}
	if gate := f.gates.gate(slot1NodeID(f.acct.ID, "s1")); gate != nil && !gate.closed.Load() {
		t.Fatal("the gate built by the failed start is still open")
	}
	if session := f.opener.session("s1"); session == nil || !session.isClosed() {
		t.Fatalf("session = %+v, want the failed start's session closed", session)
	}
	slot = f.slot(t)
	if slot.State == model.SlotLive {
		t.Fatalf("slot committed LIVE without an owned device: %+v", slot)
	}
	if slot.State != model.SlotDown || slot.LastError != "device_released" {
		t.Fatalf("slot = %+v, want DOWN with reason device_released", slot)
	}
	for _, node := range f.st.Nodes() {
		if node.AccountID == f.acct.ID && node.State == model.NodeLive {
			t.Fatalf("node %s is LIVE without an owned device", node.ID)
		}
	}
	// The release kept the record retired: the start did not resurrect it.
	if record, ok := f.st.DeviceRecord(slot.DeviceID); ok && record.State == store.DeviceAssigned {
		t.Fatalf("device %s is assigned again after the release", slot.DeviceID)
	}
}
