package runtime_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ajsaccountd/internal/account"
	"ajsaccountd/internal/gate"
	"ajsaccountd/internal/model"
	"ajsaccountd/internal/runtime"
	"ajsaccountd/internal/store"
	"ajsproxy/apifacade"
)

// switchGate is a listener-free Gate that records drain/close/start and can be
// configured to fail, so the switch ordering and rollback are observable.
type switchGate struct {
	nodeID  string
	started atomic.Bool
	drained atomic.Bool
	closed  atomic.Bool
	active  int

	closeBlock *threatBlock
	startErr   error
	drainErr   error
	closeErr   error
}

func (g *switchGate) Start(context.Context) error {
	if g.startErr != nil {
		return g.startErr
	}
	g.started.Store(true)
	return nil
}

func (g *switchGate) Drain(context.Context) error {
	g.drained.Store(true)
	return g.drainErr
}

func (g *switchGate) Undrain() error {
	g.drained.Store(false)
	return nil
}

func (g *switchGate) Close() error {
	if g.closeBlock != nil {
		g.closeBlock.once.Do(func() { close(g.closeBlock.entered) })
		<-g.closeBlock.release
	}
	g.closed.Store(true)
	return g.closeErr
}

func (g *switchGate) Addr() net.Addr { return nil }

func (g *switchGate) State() gate.State {
	return gate.State{NodeID: g.nodeID, Active: g.active, Draining: g.drained.Load()}
}

// switchGateFactory records one gate per node id and lets a test inject start,
// drain and close failures.
type switchGateFactory struct {
	mu          sync.Mutex
	gates       map[string]*switchGate
	closeBlocks map[string]*threatBlock
	startErrs   map[string]error
	drainErrs   map[string]error
	closeErrs   map[string]error
}

func newSwitchGateFactory() *switchGateFactory {
	return &switchGateFactory{
		gates:       make(map[string]*switchGate),
		closeBlocks: make(map[string]*threatBlock),
		startErrs:   make(map[string]error),
		drainErrs:   make(map[string]error),
		closeErrs:   make(map[string]error),
	}
}

// blockClose makes the next Close of nodeID wait until the returned block is
// released. It is the deterministic window between a cleanup's lease check and
// its node deletion.
func (f *switchGateFactory) blockClose(nodeID string) *threatBlock {
	block := &threatBlock{entered: make(chan struct{}), release: make(chan struct{})}
	f.mu.Lock()
	f.closeBlocks[nodeID] = block
	if g, ok := f.gates[nodeID]; ok {
		g.closeBlock = block
	}
	f.mu.Unlock()
	return block
}

func (f *switchGateFactory) build(cfg gate.Config, _ gate.Session) (runtime.Gate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	g := &switchGate{
		nodeID:     cfg.NodeID,
		closeBlock: f.closeBlocks[cfg.NodeID],
		startErr:   f.startErrs[cfg.NodeID],
		drainErr:   f.drainErrs[cfg.NodeID],
		closeErr:   f.closeErrs[cfg.NodeID],
	}
	f.gates[cfg.NodeID] = g
	return g, nil
}

func (f *switchGateFactory) gate(nodeID string) *switchGate {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gates[nodeID]
}

func (f *switchGateFactory) built() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.gates)
}

// switchClock is a pinned clock; the cooldown test advances it instead of
// sleeping.
type switchClock struct {
	mu sync.Mutex
	t  time.Time
}

func newSwitchClock() *switchClock {
	return &switchClock{t: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)}
}

func (c *switchClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *switchClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// blockingOpener blocks one server's session open so a concurrent connect can
// observe the per-account switch marker.
type blockingOpener struct {
	*fakeSessionOpener
	blockID string
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (o *blockingOpener) Open(ctx context.Context, req account.SessionRequest) (account.FlowSession, error) {
	if req.Server.ID == o.blockID {
		o.once.Do(func() { close(o.entered) })
		select {
		case <-o.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return o.fakeSessionOpener.Open(ctx, req)
}

type switchFixture struct {
	st         *store.Store
	acct       model.Account
	other      model.Account
	logins     *fakeLoginResults
	opener     *fakeSessionOpener
	gates      *switchGateFactory
	controller *runtime.NodeController
	clock      *switchClock
	// reconciler is optional; a test that needs the failed-switch cleanup or
	// a publication failure injects one through newSwitchFixtureWith.
	reconciler runtime.Reconciler
}

func newSwitchFixture(t *testing.T) *switchFixture {
	t.Helper()
	return newSwitchFixtureWith(t, func(*switchFixture) {})
}

func newSwitchFixtureWith(t *testing.T, configure func(*switchFixture)) *switchFixture {
	t.Helper()
	st := newNodeTestPoolStore(t)
	acct := newNodeTestAccount(t, st, "switch")
	other := newNodeTestAccount(t, st, "other")
	logins := newFakeLoginResults()
	logins.set(acct.ID, account.LoginResult{
		AuthResult:      "OK",
		UserName:        "vendor-user",
		ConnectPassword: account.Secret(testConnectPassword),
		Servers: map[string]apifacade.Server{
			"s1":  testServer("s1"),
			"s2":  testServer("s2"),
			"bad": ineligibleServer("bad"),
		},
		FetchedAt: time.Date(2026, 10, 24, 11, 0, 0, 0, time.UTC),
	})
	opener := newFakeSessionOpener()
	gates := newSwitchGateFactory()
	clock := newSwitchClock()
	f := &switchFixture{
		st: st, acct: acct, other: other, logins: logins,
		opener: opener, gates: gates, clock: clock,
	}
	configure(f)
	controller, err := runtime.NewNodeController(runtime.NodeControllerConfig{
		Store:      st,
		Logins:     logins,
		Sessions:   opener,
		Gates:      gates.build,
		Reconciler: f.reconciler,
		MaxServers: 1,
		Now:        clock.Now,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("NewNodeController: %v", err)
	}
	f.controller = controller
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = controller.Stop(stopCtx)
	})
	return f
}

// ineligibleServer is a vendor entry with a non-ok status: usableServers
// filters it out, and the connect path refuses it.
func ineligibleServer(id string) apifacade.Server {
	server := testServer(id)
	server.Sts = "full"
	return server
}

func (f *switchFixture) sync(t *testing.T) {
	t.Helper()
	if err := f.controller.SyncAccount(context.Background(), f.acct.ID); err != nil {
		t.Fatalf("SyncAccount: %v", err)
	}
}

func (f *switchFixture) nodes() []model.ProxyNode {
	var out []model.ProxyNode
	for _, node := range f.st.Nodes() {
		if node.AccountID == f.acct.ID {
			out = append(out, node)
		}
	}
	return out
}

func (f *switchFixture) auditCount(action string) int {
	count := 0
	for _, event := range f.st.Audit(500) {
		if event.Action == action {
			count++
		}
	}
	return count
}

func (f *switchFixture) lastAudit(t *testing.T, action string) model.AuditEvent {
	t.Helper()
	for _, event := range f.st.Audit(500) {
		if event.Action == action {
			return event
		}
	}
	t.Fatalf("no %s audit event", action)
	return model.AuditEvent{}
}

func requireSwitchReason(t *testing.T, err error, want string) *runtime.SwitchError {
	t.Helper()
	var switchErr *runtime.SwitchError
	if !errors.As(err, &switchErr) {
		t.Fatalf("error = %v, want *runtime.SwitchError", err)
	}
	if switchErr.Reason != want {
		t.Fatalf("reason = %q, want %q (error %v)", switchErr.Reason, want, err)
	}
	return switchErr
}

func TestCatalogConnectSwitch(t *testing.T) {
	f := newSwitchFixture(t)
	f.sync(t)
	snapshot := f.controller.SnapshotID()

	result, err := f.controller.ConnectServer(context.Background(), f.acct.ID, "s2", snapshot, "manual")
	if err != nil {
		t.Fatalf("ConnectServer: %v", err)
	}
	if result.ServerID != "s2" || result.SwitchedFrom != "s1" {
		t.Fatalf("result = %+v, want s1 -> s2", result)
	}
	if result.NodeState != model.NodeLive || result.GatePort == 0 || result.AuditID == "" {
		t.Fatalf("result = %+v, want LIVE node with a port and audit id", result)
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("warnings = %v, want none", result.Warnings)
	}
	oldGate := f.gates.gate(slot1NodeID(f.acct.ID, "s1"))
	newGate := f.gates.gate(slot1NodeID(f.acct.ID, "s2"))
	if oldGate == nil || !oldGate.drained.Load() || !oldGate.closed.Load() {
		t.Fatalf("old gate = %+v, want drained and closed", oldGate)
	}
	if newGate == nil || !newGate.started.Load() || newGate.closed.Load() {
		t.Fatalf("new gate = %+v, want started and open", newGate)
	}
	if f.opener.callCount() != 2 {
		t.Fatalf("session opens = %d, want 2", f.opener.callCount())
	}
	if session := f.opener.session("s1"); session == nil || !session.isClosed() {
		t.Fatalf("old session was not closed")
	}
	nodes := f.nodes()
	if len(nodes) != 1 || nodes[0].ServerID != "s2" || nodes[0].State != model.NodeLive {
		t.Fatalf("nodes = %+v, want exactly one LIVE s2", nodes)
	}
	if selection, ok := f.st.Selection(f.acct.ID); !ok || selection.ServerID != "s2" {
		t.Fatalf("selection = %+v/%v, want s2", selection, ok)
	}
	event := f.lastAudit(t, "catalog.switch")
	if event.Meta["fromServerId"] != "s1" || event.Meta["toServerId"] != "s2" ||
		event.Meta["result"] != "ok" || event.Meta["trigger"] != "manual" {
		t.Fatalf("audit meta = %v", event.Meta)
	}
	stat, ok := f.st.ServerStat(f.acct.ID, "s2")
	if !ok || stat.Attempts != 1 || stat.Successes != 1 || stat.LastErrorClass != "" {
		t.Fatalf("stat = %+v/%v, want one success", stat, ok)
	}
}

func TestCatalogConnectAlreadyConnected(t *testing.T) {
	f := newSwitchFixture(t)
	f.sync(t)
	opens := f.opener.callCount()
	audits := f.auditCount("catalog.switch") + f.auditCount("catalog.connect")

	result, err := f.controller.ConnectServer(context.Background(), f.acct.ID, "s1", "", "manual")
	if err != nil {
		t.Fatalf("ConnectServer: %v", err)
	}
	if !result.AlreadyConnected {
		t.Fatalf("result = %+v, want alreadyConnected", result)
	}
	if f.opener.callCount() != opens {
		t.Fatalf("session opens = %d, want unchanged %d", f.opener.callCount(), opens)
	}
	if got := f.auditCount("catalog.switch") + f.auditCount("catalog.connect"); got != audits {
		t.Fatalf("audits = %d, want unchanged %d", got, audits)
	}
	if _, ok := f.st.Selection(f.acct.ID); ok {
		t.Fatalf("already-connected must not persist a selection")
	}
}

func TestCatalogConnectCooldown(t *testing.T) {
	f := newSwitchFixture(t)
	f.sync(t)
	if _, err := f.controller.ConnectServer(context.Background(), f.acct.ID, "s2", "", "manual"); err != nil {
		t.Fatalf("first connect: %v", err)
	}
	opens := f.opener.callCount()

	f.clock.Advance(time.Second)
	_, err := f.controller.ConnectServer(context.Background(), f.acct.ID, "s1", "", "manual")
	switchErr := requireSwitchReason(t, err, runtime.ReasonConnectCooldown)
	if switchErr.RetryAfter <= 0 || switchErr.RetryAfter > 4*time.Second {
		t.Fatalf("retry after = %v, want (0,4s]", switchErr.RetryAfter)
	}
	if f.opener.callCount() != opens {
		t.Fatalf("cooldown opened a session")
	}

	f.clock.Advance(5 * time.Second)
	if _, err := f.controller.ConnectServer(context.Background(), f.acct.ID, "s1", "", "manual"); err != nil {
		t.Fatalf("connect after cooldown: %v", err)
	}
	if f.opener.callCount() != opens+1 {
		t.Fatalf("opens = %d, want %d", f.opener.callCount(), opens+1)
	}
}

func TestCatalogConnectIneligible(t *testing.T) {
	f := newSwitchFixture(t)
	f.sync(t)
	opens := f.opener.callCount()

	_, err := f.controller.ConnectServer(context.Background(), f.acct.ID, "bad", "", "manual")
	switchErr := requireSwitchReason(t, err, runtime.ReasonServerIneligible)
	if switchErr.Details["ineligibleReason"] != "vendor_status" {
		t.Fatalf("details = %v, want vendor_status", switchErr.Details)
	}
	if f.opener.callCount() != opens {
		t.Fatalf("ineligible server opened a session")
	}
	if nodes := f.nodes(); len(nodes) != 1 || nodes[0].ServerID != "s1" {
		t.Fatalf("nodes = %+v, want the old s1 only", nodes)
	}
}

func TestCatalogConnectUnknownServerAndLoginRequired(t *testing.T) {
	f := newSwitchFixture(t)
	f.sync(t)

	_, err := f.controller.ConnectServer(context.Background(), f.acct.ID, "nope", "", "manual")
	requireSwitchReason(t, err, runtime.ReasonUnknownServer)

	empty := newFakeLoginResults()
	st := newNodeTestStore(t)
	acct := newNodeTestAccount(t, st, "no-login")
	controller, err := runtime.NewNodeController(runtime.NodeControllerConfig{
		Store:    st,
		Logins:   empty,
		Sessions: newFakeSessionOpener(),
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("NewNodeController: %v", err)
	}
	t.Cleanup(func() { _ = controller.Stop(context.Background()) })
	_, err = controller.ConnectServer(context.Background(), acct.ID, "s1", "", "manual")
	requireSwitchReason(t, err, runtime.ReasonLoginRequired)

	if _, err := f.st.LogoutAccount(f.acct.ID); err != nil {
		t.Fatalf("LogoutAccount: %v", err)
	}
	_, err = f.controller.ConnectServer(context.Background(), f.acct.ID, "s2", "", "manual")
	requireSwitchReason(t, err, runtime.ReasonAccountState)
}

func TestCatalogConnectConcurrent(t *testing.T) {
	var block *blockingOpener
	f := newSwitchFixtureWith(t, func(f *switchFixture) {
		block = &blockingOpener{
			fakeSessionOpener: f.opener,
			blockID:           "s2",
			entered:           make(chan struct{}),
			release:           make(chan struct{}),
		}
	})
	// Rebuild the controller with the blocking opener; the fixture's cleanup
	// still stops the first controller, which owns no gates yet.
	controller, err := runtime.NewNodeController(runtime.NodeControllerConfig{
		Store:      f.st,
		Logins:     f.logins,
		Sessions:   block,
		Gates:      f.gates.build,
		MaxServers: 1,
		Now:        f.clock.Now,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("NewNodeController: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = controller.Stop(stopCtx)
	})
	if err := controller.SyncAccount(context.Background(), f.acct.ID); err != nil {
		t.Fatalf("SyncAccount: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := controller.ConnectServer(context.Background(), f.acct.ID, "s2", "", "manual")
		done <- err
	}()
	<-block.entered

	_, err = controller.ConnectServer(context.Background(), f.acct.ID, "s1", "", "manual")
	requireSwitchReason(t, err, runtime.ReasonSwitchInProgress)

	close(block.release)
	if err := <-done; err != nil {
		t.Fatalf("in-flight connect: %v", err)
	}
	if block.callCount() != 2 {
		t.Fatalf("session opens = %d, want 2", block.callCount())
	}
}

func TestCatalogConnectOpenFailureMetadataOnly(t *testing.T) {
	f := newSwitchFixture(t)
	f.sync(t)
	f.opener.fail["s2"] = errors.New("vendor session refused")

	_, err := f.controller.ConnectServer(context.Background(), f.acct.ID, "s2", "", "manual")
	switchErr := requireSwitchReason(t, err, runtime.ReasonSessionOpen)
	if switchErr.Details["rollback"] != runtime.ReasonMetadataOnly {
		t.Fatalf("details = %v, want rollback %s", switchErr.Details, runtime.ReasonMetadataOnly)
	}
	// Break before make: the old session is already closed, so it cannot be
	// restored without risking a vendor kick.
	oldGate := f.gates.gate(slot1NodeID(f.acct.ID, "s1"))
	if oldGate == nil || !oldGate.closed.Load() || !oldGate.drained.Load() {
		t.Fatalf("old gate = %+v, want drained and closed", oldGate)
	}
	if session := f.opener.session("s1"); session == nil || !session.isClosed() {
		t.Fatal("old session was not closed before the failed open")
	}
	slot, ok := f.st.Slot(model.SlotKey{AccountID: f.acct.ID, Index: 1})
	if !ok || slot.State != model.SlotDown || slot.ServerID != "s2" {
		t.Fatalf("slot after the failed open = %+v/%v, want DOWN targeting s2", slot, ok)
	}
	down := nodeByServer(t, f.st, f.acct.ID, "s2")
	if down.State != model.NodeDown || down.GatePort != 0 {
		t.Fatalf("failed target node = %+v, want DOWN without a port", down)
	}
	if _, ok := f.st.Selection(f.acct.ID); ok {
		t.Fatalf("failed connect persisted a selection")
	}
	event := f.lastAudit(t, "catalog.switch")
	if event.Meta["result"] != "failed" || event.Meta["rollback"] != runtime.ReasonMetadataOnly {
		t.Fatalf("audit meta = %v, want failed/metadata_only", event.Meta)
	}
	if event.Meta["slot"] != "acc-01#1" {
		t.Fatalf("audit slot = %q, want acc-01#1", event.Meta["slot"])
	}
	stat, ok := f.st.ServerStat(f.acct.ID, "s2")
	if !ok || stat.Attempts != 1 || stat.Successes != 0 || stat.LastErrorClass != "network" {
		t.Fatalf("stat = %+v/%v, want one network failure", stat, ok)
	}
	if f.auditCount("catalog.switch") != 1 {
		t.Fatalf("switch audits = %d, want exactly 1", f.auditCount("catalog.switch"))
	}
}

func TestCatalogConnectStoreFailureMetadataOnly(t *testing.T) {
	f := newSwitchFixture(t)
	f.sync(t)
	targetLabel := testNodeTag(f.acct.ID, "s2")
	blocker, err := f.st.PutNode(model.ProxyNode{
		ID: "blocker-node", AccountID: f.other.ID, Label: targetLabel,
		Region: "cn", State: model.NodeLive, Capacity: 1, Protocol: model.ProtocolSOCKS5,
	})
	if err != nil {
		t.Fatalf("PutNode blocker: %v", err)
	}
	_ = blocker

	_, err = f.controller.ConnectServer(context.Background(), f.acct.ID, "s2", "", "manual")
	switchErr := requireSwitchReason(t, err, runtime.ReasonStoreFailed)
	if switchErr.Details["rollback"] != runtime.ReasonMetadataOnly {
		t.Fatalf("details = %v, want rollback %s", switchErr.Details, runtime.ReasonMetadataOnly)
	}
	if session := f.opener.session("s2"); session == nil || !session.isClosed() {
		t.Fatalf("new session was not closed after the failed commit")
	}
	if g := f.gates.gate(slot1NodeID(f.acct.ID, "s2")); g == nil || !g.closed.Load() {
		t.Fatalf("new gate was not closed after the failed commit")
	}
	oldGate := f.gates.gate(slot1NodeID(f.acct.ID, "s1"))
	if oldGate == nil || !oldGate.closed.Load() || !oldGate.drained.Load() {
		t.Fatalf("old gate = %+v, want drained and closed", oldGate)
	}
	slot, ok := f.st.Slot(model.SlotKey{AccountID: f.acct.ID, Index: 1})
	if !ok || slot.State != model.SlotDown {
		t.Fatalf("slot after the failed commit = %+v/%v, want DOWN", slot, ok)
	}
	// The blocker keeps the store from writing the target row at all; the
	// invariant is that no LIVE node claims the failed target.
	if down, ok := f.st.Node(slot1NodeID(f.acct.ID, "s2")); ok && (down.State != model.NodeDown || down.GatePort != 0) {
		t.Fatalf("failed target node = %+v, want DOWN without a port", down)
	}
	if event := f.lastAudit(t, "catalog.switch"); event.Meta["result"] != "failed" {
		t.Fatalf("audit meta = %v, want failed", event.Meta)
	}
}

// TestCatalogSwitchSlotIsolation proves the switch predicate is slot-scoped:
// a manual connect on slot 2 replaces only slot 2's node, leaves slot 1's gate
// and node serving, and each slot opens its own vendor username.
func TestCatalogSwitchSlotIsolation(t *testing.T) {
	f := newSwitchFixture(t)
	grantSlots(t, f.st, f.acct, 2)
	controller, err := runtime.NewNodeController(runtime.NodeControllerConfig{
		Store: f.st, Logins: f.logins, Sessions: f.opener, Gates: f.gates.build,
		Now: f.clock.Now, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("NewNodeController: %v", err)
	}
	t.Cleanup(func() { _ = controller.Stop(context.Background()) })
	if err := controller.SyncAccount(context.Background(), f.acct.ID); err != nil {
		t.Fatalf("SyncAccount: %v", err)
	}
	first := nodeByServer(t, f.st, f.acct.ID, "s1")
	second := nodeByServer(t, f.st, f.acct.ID, "s2")
	if first.Slot != 1 || second.Slot != 2 {
		t.Fatalf("node slots = %d/%d, want 1/2", first.Slot, second.Slot)
	}

	result, err := controller.ConnectSlot(context.Background(),
		model.SlotKey{AccountID: f.acct.ID, Index: 2}, "s1", "", "manual")
	if err != nil {
		t.Fatalf("ConnectSlot(slot 2): %v", err)
	}
	if result.Slot != 2 || result.SwitchedFrom != "s2" || result.PrevNodeID != second.ID {
		t.Fatalf("result = %+v, want slot 2 s2 -> s1 with prevNodeId %s", result, second.ID)
	}
	// Slot 1 keeps serving: its gate was never drained or closed.
	if gate := f.gates.gate(slot1NodeID(f.acct.ID, "s1")); gate == nil || gate.closed.Load() || gate.drained.Load() {
		t.Fatalf("slot 1 gate = %+v, want open and undrained", gate)
	}
	// Slot 2's old gate closed before its new gate started.
	if gate := f.gates.gate(slotNodeID(f.acct.ID, 2, "s2")); gate == nil || !gate.closed.Load() {
		t.Fatalf("slot 2 old gate = %+v, want closed", gate)
	}
	if gate := f.gates.gate(slotNodeID(f.acct.ID, 2, "s1")); gate == nil || !gate.started.Load() || gate.closed.Load() {
		t.Fatalf("slot 2 new gate = %+v, want started and open", gate)
	}
	if nodes := f.nodes(); len(nodes) != 2 {
		t.Fatalf("nodes = %+v, want one per slot", nodes)
	}
	liveBySlot := map[int]model.ProxyNode{}
	for _, node := range f.nodes() {
		if node.State != model.NodeLive {
			t.Fatalf("node %s state = %s, want LIVE", node.ID, node.State)
		}
		liveBySlot[node.Slot] = node
	}
	if liveBySlot[1].ServerID != "s1" || liveBySlot[2].ServerID != "s1" {
		t.Fatalf("live nodes = %+v, want s1 on both slots", liveBySlot)
	}
	// Each slot opens the vendor session under its own username.
	requests := f.opener.requestsFor("s1")
	if len(requests) != 2 || requests[0].UserName != "vendor-user" || requests[1].UserName != "vendor-user#2" {
		t.Fatalf("s1 session requests = %+v, want slot 1 then slot 2 usernames", requests)
	}
	// A slot-2 manual connect must not rewrite slot 1's selection.
	if _, ok := f.st.Selection(f.acct.ID); ok {
		t.Fatal("slot-2 connect persisted a slot-1 selection")
	}
}

func TestCatalogConnectCleanupFailureWarns(t *testing.T) {
	f := newSwitchFixtureWith(t, func(f *switchFixture) {
		f.gates.closeErrs[slot1NodeID(f.acct.ID, "s1")] = errors.New("close failed")
	})
	f.sync(t)

	result, err := f.controller.ConnectServer(context.Background(), f.acct.ID, "s2", "", "manual")
	if err != nil {
		t.Fatalf("ConnectServer: %v", err)
	}
	if len(result.Warnings) != 1 || result.Warnings[0] != "orphan_gate" {
		t.Fatalf("warnings = %v, want orphan_gate", result.Warnings)
	}
	if f.auditCount("catalog.switch_cleanup_failed") != 1 {
		t.Fatalf("cleanup audit = %d, want 1", f.auditCount("catalog.switch_cleanup_failed"))
	}
	if nodes := f.nodes(); len(nodes) != 1 || nodes[0].ServerID != "s2" {
		t.Fatalf("nodes = %+v, want the committed s2", nodes)
	}
}

func TestCatalogReconnectCleanupFailureKeepsNewGate(t *testing.T) {
	f := newSwitchFixtureWith(t, func(f *switchFixture) {
		f.gates.closeErrs[slot1NodeID(f.acct.ID, "s1")] = errors.New("close failed")
	})
	f.sync(t)
	if _, err := f.st.SetSelection(f.acct.ID, "s1", "operator"); err != nil {
		t.Fatalf("SetSelection: %v", err)
	}
	result, err := f.controller.ReconnectAccount(context.Background(), f.acct.ID, "", "reconnect")
	if err != nil {
		t.Fatalf("ReconnectAccount: %v", err)
	}
	if len(result.Warnings) != 1 || result.Warnings[0] != "orphan_gate" {
		t.Fatalf("warnings = %v, want orphan_gate", result.Warnings)
	}
	running := f.controller.Snapshot()
	if len(running) != 1 || running[0].ServerID != "s1" || running[0].Port == 0 {
		t.Fatalf("running = %+v, want the new s1 gate still registered", running)
	}
}

func TestCatalogDisconnect(t *testing.T) {
	f := newSwitchFixture(t)
	f.sync(t)
	if _, err := f.st.SetSelection(f.acct.ID, "s1", "operator"); err != nil {
		t.Fatalf("SetSelection: %v", err)
	}
	audits := f.auditCount("catalog.disconnect")

	result, err := f.controller.DisconnectAccount(context.Background(), f.acct.ID)
	if err != nil {
		t.Fatalf("DisconnectAccount: %v", err)
	}
	if result.AlreadyDisconnected || result.NodeID == "" || result.ServerID != "s1" || result.AuditID == "" {
		t.Fatalf("result = %+v, want a disconnected s1", result)
	}
	if gate := f.gates.gate(slot1NodeID(f.acct.ID, "s1")); gate == nil || !gate.closed.Load() {
		t.Fatalf("gate was not closed")
	}
	if session := f.opener.session("s1"); session == nil || !session.isClosed() {
		t.Fatalf("session was not closed")
	}
	if nodes := f.nodes(); len(nodes) != 0 {
		t.Fatalf("nodes = %+v, want none", nodes)
	}
	if selection, ok := f.st.Selection(f.acct.ID); !ok || selection.ServerID != "s1" {
		t.Fatalf("selection = %+v/%v, want the target kept", selection, ok)
	}
	if got := f.auditCount("catalog.disconnect"); got != audits+1 {
		t.Fatalf("disconnect audits = %d, want %d", got, audits+1)
	}
	if f.auditCount("node.delete") != 0 {
		t.Fatalf("disconnect wrote the generic node.delete audit")
	}

	second, err := f.controller.DisconnectAccount(context.Background(), f.acct.ID)
	if err != nil {
		t.Fatalf("second DisconnectAccount: %v", err)
	}
	if !second.AlreadyDisconnected {
		t.Fatalf("second result = %+v, want alreadyDisconnected", second)
	}
	if got := f.auditCount("catalog.disconnect"); got != audits+1 {
		t.Fatalf("second disconnect wrote an audit: %d", got)
	}
}

func TestCatalogReconnect(t *testing.T) {
	f := newSwitchFixture(t)
	f.sync(t)
	if _, err := f.st.SetSelection(f.acct.ID, "s1", "operator"); err != nil {
		t.Fatalf("SetSelection: %v", err)
	}

	result, err := f.controller.ReconnectAccount(context.Background(), f.acct.ID, "", "reconnect")
	if err != nil {
		t.Fatalf("ReconnectAccount: %v", err)
	}
	if result.ServerID != "s1" || result.SwitchedFrom != "s1" || result.AlreadyConnected {
		t.Fatalf("result = %+v, want a real s1 reopen", result)
	}
	if f.opener.callCount() != 2 {
		t.Fatalf("session opens = %d, want 2", f.opener.callCount())
	}
	if nodes := f.nodes(); len(nodes) != 1 || nodes[0].ServerID != "s1" {
		t.Fatalf("nodes = %+v, want one s1", nodes)
	}
	if f.auditCount("catalog.reconnect") != 1 {
		t.Fatalf("reconnect audits = %d, want 1", f.auditCount("catalog.reconnect"))
	}

	// With no running session, reconnect uses the persisted target.
	if _, err := f.controller.DisconnectAccount(context.Background(), f.acct.ID); err != nil {
		t.Fatalf("DisconnectAccount: %v", err)
	}
	f.clock.Advance(6 * time.Second)
	result, err = f.controller.ReconnectAccount(context.Background(), f.acct.ID, "", "reconnect")
	if err != nil {
		t.Fatalf("ReconnectAccount from selection: %v", err)
	}
	if result.ServerID != "s1" || result.SwitchedFrom != "" {
		t.Fatalf("result = %+v, want a fresh s1", result)
	}
}

func TestCatalogReconnectNoTarget(t *testing.T) {
	f := newSwitchFixture(t)
	f.sync(t)
	if _, err := f.controller.DisconnectAccount(context.Background(), f.acct.ID); err != nil {
		t.Fatalf("DisconnectAccount: %v", err)
	}
	_, err := f.controller.ReconnectAccount(context.Background(), f.acct.ID, "", "reconnect")
	requireSwitchReason(t, err, runtime.ReasonNoTarget)
}

func TestCatalogSyncUsesSelection(t *testing.T) {
	f := newSwitchFixture(t)
	f.sync(t)
	if _, err := f.st.SetSelection(f.acct.ID, "s2", "operator"); err != nil {
		t.Fatalf("SetSelection: %v", err)
	}
	f.sync(t)

	nodes := f.nodes()
	if len(nodes) != 1 || nodes[0].ServerID != "s2" || nodes[0].State != model.NodeLive {
		t.Fatalf("nodes = %+v, want the selected s2 only", nodes)
	}
	if gate := f.gates.gate(slot1NodeID(f.acct.ID, "s1")); gate == nil || !gate.closed.Load() {
		t.Fatalf("old s1 gate was not closed")
	}
	event := f.lastAudit(t, "catalog.switch")
	if event.Meta["trigger"] != "login" || event.Meta["fromServerId"] != "s1" || event.Meta["toServerId"] != "s2" {
		t.Fatalf("audit meta = %v", event.Meta)
	}

	// A selection that disappeared falls back to the default desired set and
	// audits catalog.selection_lost.
	if _, err := f.st.SetSelection(f.acct.ID, "gone", "operator"); err != nil {
		t.Fatalf("SetSelection gone: %v", err)
	}
	f.sync(t)
	if _, ok := f.st.Selection(f.acct.ID); ok {
		t.Fatalf("lost selection was not cleared")
	}
	if f.auditCount("catalog.selection_lost") != 1 {
		t.Fatalf("selection_lost audits = %d, want 1", f.auditCount("catalog.selection_lost"))
	}
	// A lost selection keeps the slot's persisted target instead of moving the
	// session to the sorted fallback.
	if nodes := f.nodes(); len(nodes) != 1 || nodes[0].ServerID != "s2" || nodes[0].State != model.NodeLive {
		t.Fatalf("nodes = %+v, want the kept s2", nodes)
	}
}

func TestCatalogSnapshotChanged(t *testing.T) {
	f := newSwitchFixture(t)
	f.sync(t)
	_, err := f.controller.ConnectServer(context.Background(), f.acct.ID, "s2", "cat-stale", "manual")
	switchErr := requireSwitchReason(t, err, runtime.ReasonSnapshotChanged)
	if switchErr.Details["snapshotId"] != f.controller.SnapshotID() {
		t.Fatalf("details = %v, want the current snapshot id", switchErr.Details)
	}
}

func TestCatalogSnapshotIDStable(t *testing.T) {
	f := newSwitchFixture(t)
	f.sync(t)
	first := f.controller.SnapshotID()
	if first == "" || first != f.controller.SnapshotID() {
		t.Fatalf("snapshot id = %q, want stable non-empty", first)
	}
	result, _ := f.logins.Result(f.acct.ID)
	result.FetchedAt = result.FetchedAt.Add(time.Minute)
	f.logins.set(f.acct.ID, result)
	if second := f.controller.SnapshotID(); second == first {
		t.Fatalf("snapshot id did not change after a new login fetch")
	}
}

// testNodeTag mirrors runtime.nodeTag for the ASCII ids used in tests.
func testNodeTag(accountID, serverID string) string {
	clean := func(value string) string {
		var b strings.Builder
		for _, r := range strings.ToLower(value) {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
				b.WriteRune(r)
			}
		}
		return b.String()
	}
	return fmt.Sprintf("cn-%s-%s", clean(accountID), clean(serverID))
}
