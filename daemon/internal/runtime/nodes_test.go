package runtime_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ajsaccountd/internal/account"
	"ajsaccountd/internal/api"
	"ajsaccountd/internal/gate"
	"ajsaccountd/internal/model"
	"ajsaccountd/internal/runtime"
	"ajsaccountd/internal/store"
	"ajsproxy/apifacade"
)

const testConnectPassword = "connect-password-not-a-credential"

// newNodeTestStore returns an empty store: Restore replaces the dashboard seed
// so the controller's first gate can bind 127.0.0.1:13000. The device pool is
// disabled, which is the shipped no-master-key mode: only slot 1 has the
// legacy in-memory identity.
func newNodeTestStore(t *testing.T) *store.Store {
	t.Helper()
	st := store.New()
	if err := st.Restore([]byte(`{"version":1}`)); err != nil {
		t.Fatalf("Restore empty snapshot: %v", err)
	}
	return st
}

// testDeviceSealer is a reversible in-memory store.DeviceSealer.
type testDeviceSealer struct {
	mu    sync.Mutex
	blobs map[string][]byte
}

func newTestDeviceSealer() *testDeviceSealer {
	return &testDeviceSealer{blobs: make(map[string][]byte)}
}

func (s *testDeviceSealer) Seal(id string, plaintext []byte) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blobs[id] = append([]byte(nil), plaintext...)
	return []byte("sealed:" + id), nil
}

func (s *testDeviceSealer) OpenID(id string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	blob, ok := s.blobs[id]
	if !ok {
		return nil, errors.New("sealed device material is missing")
	}
	return append([]byte(nil), blob...), nil
}

func (s *testDeviceSealer) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.blobs, id)
	return nil
}

func (s *testDeviceSealer) IDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.blobs))
	for id := range s.blobs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// newNodeTestPoolStore returns an empty store with the device pool enabled, so
// every slot can bind and reopen its own sealed identity.
func newNodeTestPoolStore(t *testing.T) *store.Store {
	t.Helper()
	st := store.NewEmptyWithSealers(testSealer{}, newTestDeviceSealer())
	if err := st.Restore([]byte(`{"version":1}`)); err != nil {
		t.Fatalf("Restore empty snapshot: %v", err)
	}
	return st
}

// newNodeTestAccount creates an account with a stored Linux device identity,
// which is what SyncAccount requires.
func newNodeTestAccount(t *testing.T, st *store.Store, label string) model.Account {
	t.Helper()
	acct, err := st.CreateAccount(model.CreateAccountInput{
		Label:       label,
		Username:    "user-" + label,
		Password:    "node-test-password-not-a-credential",
		Platform:    model.PlatformLinux,
		Concurrency: 8,
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	device, err := account.NewDeviceForProfile(account.Linux423Binary)
	if err != nil {
		t.Fatalf("NewDeviceForProfile: %v", err)
	}
	if _, err := st.EnsureAccountDevice(acct.ID, account.Identify(device)); err != nil {
		t.Fatalf("EnsureAccountDevice: %v", err)
	}
	return acct
}

// grantSlots raises one account's slot capacity for a test. AJS_ACCOUNT_SLOTS
// is process-wide, so a test that needs more than one session sets it before
// the controller syncs.
func grantSlots(t *testing.T, st *store.Store, acct model.Account, slots int) {
	t.Helper()
	t.Setenv("AJS_ACCOUNT_SLOTS", strconv.Itoa(slots))
	if _, err := st.SetAccountUseCount(acct.ID, slots); err != nil {
		t.Fatalf("SetAccountUseCount(%d): %v", slots, err)
	}
}

// testServer is one usable vendor server entry.
func testServer(id string) apifacade.Server {
	return apifacade.Server{
		ID:   id,
		Svr:  "203.0.113.10",
		Tit:  "Shanghai 01",
		Ctry: "CN",
		Sts:  "ok",
		Ord:  "1",
		Par:  apifacade.ServerPar{FV: apifacade.ServerFV{P: apifacade.FlexInt(443)}},
		Loc:  []apifacade.ServerLoc{{C: "CN", N: "Shanghai", O: apifacade.FlexInt(1)}},
	}
}

type fakeLoginResults struct {
	mu      sync.Mutex
	results map[string]account.LoginResult
}

func newFakeLoginResults() *fakeLoginResults {
	return &fakeLoginResults{results: make(map[string]account.LoginResult)}
}

func (f *fakeLoginResults) set(accountID string, result account.LoginResult) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.results[accountID] = result
}

func (f *fakeLoginResults) Result(accountID string) (account.LoginResult, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	result, ok := f.results[accountID]
	return result, ok
}

// fakeFlowSession is a FlowSession whose flows are in-memory pipes that echo.
// killed models a vendor kick: the SID and the local session object survive,
// but the session can no longer carry flows, which is exactly what Alive
// reports and Healthy does not.
type fakeFlowSession struct {
	fields map[string]string
	closed atomic.Bool
	killed atomic.Bool

	mu    sync.Mutex
	conns []net.Conn
}

func (s *fakeFlowSession) SID() string { return "fake-sid" }

func (s *fakeFlowSession) Fields() map[string]string { return s.fields }

func (s *fakeFlowSession) Healthy() bool { return !s.closed.Load() }

// Alive implements the runtime's optional liveness view.
func (s *fakeFlowSession) Alive() bool { return !s.closed.Load() && !s.killed.Load() }

// kill marks the session as taken over by another connection, the way a vendor
// kick leaves a closed session holding its SID.
func (s *fakeFlowSession) kill() { s.killed.Store(true) }

func (s *fakeFlowSession) isClosed() bool { return s.closed.Load() }

func (s *fakeFlowSession) OpenFlow(context.Context, string, int) (net.Conn, error) {
	client, server := net.Pipe()
	go func() {
		defer server.Close()
		buf := make([]byte, 1024)
		for {
			n, err := server.Read(buf)
			if n > 0 {
				if _, writeErr := server.Write(buf[:n]); writeErr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	s.mu.Lock()
	s.conns = append(s.conns, client)
	s.mu.Unlock()
	return client, nil
}

func (s *fakeFlowSession) Close() error {
	if s.closed.Swap(true) {
		return nil
	}
	s.mu.Lock()
	conns := s.conns
	s.conns = nil
	s.mu.Unlock()
	for _, conn := range conns {
		_ = conn.Close()
	}
	return nil
}

// fakeSessionOpener records every SessionRequest and returns a fake session or
// the configured per-server error.
type fakeSessionOpener struct {
	mu       sync.Mutex
	requests []account.SessionRequest
	sessions map[string]*fakeFlowSession
	fail     map[string]error
	fields   map[string]string
}

func newFakeSessionOpener() *fakeSessionOpener {
	return &fakeSessionOpener{
		sessions: make(map[string]*fakeFlowSession),
		fail:     make(map[string]error),
		fields:   map[string]string{"sip": "198.51.100.7"},
	}
}

func (o *fakeSessionOpener) Open(_ context.Context, req account.SessionRequest) (account.FlowSession, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.requests = append(o.requests, req)
	if err, ok := o.fail[req.Server.ID]; ok {
		return nil, err
	}
	session := &fakeFlowSession{fields: o.fields}
	o.sessions[req.Server.ID] = session
	return session, nil
}

func (o *fakeSessionOpener) callCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.requests)
}

func (o *fakeSessionOpener) requestsFor(serverID string) []account.SessionRequest {
	o.mu.Lock()
	defer o.mu.Unlock()
	var out []account.SessionRequest
	for _, req := range o.requests {
		if req.Server.ID == serverID {
			out = append(out, req)
		}
	}
	return out
}

func (o *fakeSessionOpener) session(serverID string) *fakeFlowSession {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.sessions[serverID]
}

type fakeReconciler struct {
	calls atomic.Int32
	err   error
}

func (f *fakeReconciler) Reconcile(context.Context) (runtime.Topology, error) {
	f.calls.Add(1)
	if f.err != nil {
		return runtime.Topology{}, f.err
	}
	return runtime.Topology{}, nil
}

func (f *fakeReconciler) callCount() int { return int(f.calls.Load()) }

// mutationPanel records the store state at every Reconcile. It embeds the rest
// of runtime.Panel as a nil interface: the account handlers must call only
// Reconcile, so any other panel call panics and fails the test.
type mutationPanel struct {
	runtime.Panel
	st        *store.Store
	accountID string

	calls     int
	liveAt    []int
	accountAt []bool
	stateAt   []model.AccountState
}

func (p *mutationPanel) Reconcile(context.Context) (runtime.Topology, error) {
	p.calls++
	live := 0
	for _, node := range p.st.Nodes() {
		if node.AccountID == p.accountID && node.State == model.NodeLive {
			live++
		}
	}
	p.liveAt = append(p.liveAt, live)
	account, ok := p.st.Account(p.accountID)
	p.accountAt = append(p.accountAt, ok)
	state := model.AccountState("")
	if ok {
		state = account.State
	}
	p.stateAt = append(p.stateAt, state)
	return runtime.Topology{}, nil
}

func newNodeController(t *testing.T, st *store.Store, logins runtime.LoginResults, opener runtime.SessionOpener, rec runtime.Reconciler) *runtime.NodeController {
	t.Helper()
	controller, err := runtime.NewNodeController(runtime.NodeControllerConfig{
		Store:      st,
		Logins:     logins,
		Sessions:   opener,
		Reconciler: rec,
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
	return controller
}

// requirePort13000 skips a test when another process owns the port the
// acceptance fixture pins. The controller still allocates upward from 13000.
func requirePort13000(t *testing.T) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:13000")
	if err != nil {
		t.Skipf("127.0.0.1:13000 is not available: %v", err)
	}
	_ = listener.Close()
}

// socks5Connect performs the no-auth CONNECT handshake and returns the SOCKS5
// reply code. A 0x00 reply means the gate relayed the flow.
func socks5Connect(t *testing.T, port int) (byte, net.Conn) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 2*time.Second)
	if err != nil {
		t.Fatalf("dial gate %d: %v", port, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatalf("write greeting: %v", err)
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatalf("read method reply: %v", err)
	}
	if reply[0] != 0x05 || reply[1] != 0x00 {
		t.Fatalf("method reply = %v, want 05 00", reply)
	}
	request := append([]byte{0x05, 0x01, 0x00, 0x03, 0x0b}, []byte("example.com")...)
	request = append(request, 0x00, 0x50)
	if _, err := conn.Write(request); err != nil {
		t.Fatalf("write CONNECT: %v", err)
	}
	head := make([]byte, 4)
	if _, err := io.ReadFull(conn, head); err != nil {
		t.Fatalf("read CONNECT reply: %v", err)
	}
	var skip int
	switch head[3] {
	case 0x01:
		skip = 4 + 2
	case 0x04:
		skip = 16 + 2
	case 0x03:
		length := make([]byte, 1)
		if _, err := io.ReadFull(conn, length); err != nil {
			t.Fatalf("read bound domain length: %v", err)
		}
		skip = int(length[0]) + 2
	default:
		t.Fatalf("CONNECT reply atyp = %d, want 1/3/4", head[3])
	}
	if skip > 0 {
		if _, err := io.ReadFull(conn, make([]byte, skip)); err != nil {
			t.Fatalf("read bound address: %v", err)
		}
	}
	return head[1], conn
}

// echoOnce proves the gate relays through the session, then closes the client
// so no admitted flow is left for Drain/Close to wait on.
func echoOnce(t *testing.T, conn net.Conn) {
	t.Helper()
	defer conn.Close()
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("write flow: %v", err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read flow: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("flow echoed %q, want ping", buf)
	}
}

// slot1NodeID is the runtime node id of slot 1 in tests.
func slot1NodeID(accountID, serverID string) string {
	return model.SlotKey{AccountID: accountID, Index: 1}.NodeID(serverID)
}

// slotNodeID is the runtime node id of one slot in tests.
func slotNodeID(accountID string, index int, serverID string) string {
	return model.SlotKey{AccountID: accountID, Index: index}.NodeID(serverID)
}

// nodeByServer finds the node one account serves serverID on. One server is
// assigned to at most one slot, so the server id is unique per account.
func nodeByServer(t *testing.T, st *store.Store, accountID, serverID string) model.ProxyNode {
	t.Helper()
	for _, node := range st.Nodes() {
		if node.AccountID == accountID && node.ServerID == serverID {
			return node
		}
	}
	t.Fatalf("node for %s server %s is missing", accountID, serverID)
	return model.ProxyNode{}
}

func TestNodeControllerSyncCreatesReusesAndRemoves(t *testing.T) {
	requirePort13000(t)
	st := newNodeTestPoolStore(t)
	acct := newNodeTestAccount(t, st, "node-sync")
	logins := newFakeLoginResults()
	logins.set(acct.ID, account.LoginResult{
		AuthResult:      "OK",
		UserName:        "vendor-user",
		ConnectPassword: account.Secret(testConnectPassword),
		Servers: map[string]apifacade.Server{
			"s1": testServer("s1"),
			"s2": testServer("s2"),
		},
	})
	opener := newFakeSessionOpener()
	rec := &fakeReconciler{}
	grantSlots(t, st, acct, 2)
	controller := newNodeController(t, st, logins, opener, rec)

	if err := controller.SyncAccount(context.Background(), acct.ID); err != nil {
		t.Fatalf("SyncAccount: %v", err)
	}

	first := nodeByServer(t, st, acct.ID, "s1")
	second := nodeByServer(t, st, acct.ID, "s2")
	if first.GatePort != 13000 || second.GatePort != 13001 {
		t.Fatalf("gate ports = %d/%d, want 13000/13001", first.GatePort, second.GatePort)
	}
	for _, node := range []model.ProxyNode{first, second} {
		if node.State != model.NodeLive || node.Protocol != model.ProtocolSOCKS5 {
			t.Fatalf("node %s = %+v, want LIVE socks5", node.ID, node)
		}
		if node.ServerID != "s1" && node.ServerID != "s2" {
			t.Fatalf("node %s ServerID = %q", node.ID, node.ServerID)
		}
		if node.EgressIP != "198.51.100.7" {
			t.Fatalf("node %s EgressIP = %q, want the session sip", node.ID, node.EgressIP)
		}
		if node.AccountID != acct.ID || node.AccountLabel != acct.Label {
			t.Fatalf("node %s account = %q/%q, want %q/%q", node.ID, node.AccountID, node.AccountLabel, acct.ID, acct.Label)
		}
		if node.Capacity != 32 {
			t.Fatalf("node %s capacity = %d, want the gate limit 32", node.ID, node.Capacity)
		}
	}
	if first.Region != "cn" || first.Country != "CN" || first.City != "Shanghai" {
		t.Fatalf("node location = %q/%q/%q, want cn/CN/Shanghai", first.Region, first.Country, first.City)
	}
	if first.Slot != 1 || second.Slot != 2 {
		t.Fatalf("node slots = %d/%d, want 1/2", first.Slot, second.Slot)
	}
	if first.ID != slotNodeID(acct.ID, 1, "s1") || second.ID != slotNodeID(acct.ID, 2, "s2") {
		t.Fatalf("node ids = %q/%q, want the slot-scoped form", first.ID, second.ID)
	}

	if got := opener.callCount(); got != 2 {
		t.Fatalf("session opens = %d, want 2", got)
	}
	if got := rec.callCount(); got != 1 {
		t.Fatalf("reconciles = %d, want 1", got)
	}
	t.Logf("sync 1: %s server=%s label=%s port=%d state=%s egress=%s; %s server=%s label=%s port=%d state=%s",
		first.ID, first.ServerID, first.Label, first.GatePort, first.State, first.EgressIP,
		second.ID, second.ServerID, second.Label, second.GatePort, second.State)
	requests := opener.requestsFor("s1")
	if len(requests) != 1 {
		t.Fatalf("s1 session requests = %d, want 1", len(requests))
	}
	if requests[0].UserName != "vendor-user" || requests[0].ConnectPassword.Reveal() != testConnectPassword {
		t.Fatal("session request does not carry the login credentials")
	}
	if requests[0].Slot != (model.SlotKey{AccountID: acct.ID, Index: 1}) {
		t.Fatalf("s1 request slot = %q, want slot 1", requests[0].Slot.String())
	}
	secondRequests := opener.requestsFor("s2")
	if len(secondRequests) != 1 || secondRequests[0].UserName != "vendor-user#2" {
		t.Fatalf("s2 session requests = %+v, want the slot-2 username", secondRequests)
	}
	if secondRequests[0].Slot != (model.SlotKey{AccountID: acct.ID, Index: 2}) {
		t.Fatalf("s2 request slot = %q, want slot 2", secondRequests[0].Slot.String())
	}
	identity, ok := st.AccountDevice(acct.ID)
	if !ok || requests[0].Device.DeviceID != identity.OSDID {
		t.Fatal("session request does not carry the stored device identity")
	}

	code, conn := socks5Connect(t, first.GatePort)
	if code != 0x00 {
		t.Fatalf("SOCKS5 reply = 0x%02x, want success", code)
	}
	echoOnce(t, conn)

	// A second sync reuses both gates: no new sessions, no new ports.
	if err := controller.SyncAccount(context.Background(), acct.ID); err != nil {
		t.Fatalf("second SyncAccount: %v", err)
	}
	if got := opener.callCount(); got != 2 {
		t.Fatalf("session opens after reuse = %d, want 2", got)
	}
	if got := rec.callCount(); got != 2 {
		t.Fatalf("reconciles after reuse = %d, want 2", got)
	}
	if reused := nodeByServer(t, st, acct.ID, "s1"); reused.GatePort != first.GatePort {
		t.Fatalf("reused port = %d, want %d", reused.GatePort, first.GatePort)
	}
	t.Logf("sync 2: reused %s port=%d without a new session", first.ID, first.GatePort)

	// Dropping s2 stops its gate and marks the slot down; the DOWN node row is
	// kept until the slot leaves the capacity. s1 keeps serving.
	logins.set(acct.ID, account.LoginResult{
		AuthResult:      "OK",
		UserName:        "vendor-user",
		ConnectPassword: account.Secret(testConnectPassword),
		Servers:         map[string]apifacade.Server{"s1": testServer("s1")},
	})
	if err := controller.SyncAccount(context.Background(), acct.ID); err != nil {
		t.Fatalf("SyncAccount after server removal: %v", err)
	}
	down, ok := st.Node(second.ID)
	if !ok || down.State != model.NodeDown || down.GatePort != 0 {
		t.Fatalf("node for the removed server = %+v/%v, want DOWN without a port", down, ok)
	}
	slot2, ok := st.Slot(model.SlotKey{AccountID: acct.ID, Index: 2})
	if !ok || slot2.State != model.SlotDown || slot2.LastError != "no_server" {
		t.Fatalf("slot 2 after server removal = %+v/%v, want DOWN no_server", slot2, ok)
	}
	if session := opener.session("s2"); session == nil || !session.isClosed() {
		t.Fatal("removed server's session is not closed")
	}
	if conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(second.GatePort)), 500*time.Millisecond); err == nil {
		_ = conn.Close()
		t.Fatal("removed server's gate still accepts connections")
	}
	t.Logf("sync 3: %s marked DOWN, port %d refused, %s kept port %d", second.ID, second.GatePort, first.ID, first.GatePort)

	// Shrinking the capacity to one removes the out-of-cap slot and its node.
	if _, err := st.SetAccountUseCount(acct.ID, 1); err != nil {
		t.Fatalf("SetAccountUseCount(1): %v", err)
	}
	if err := controller.SyncAccount(context.Background(), acct.ID); err != nil {
		t.Fatalf("SyncAccount after capacity shrink: %v", err)
	}
	if _, ok := st.Node(second.ID); ok {
		t.Fatalf("out-of-cap node %s survived the shrink", second.ID)
	}
	if slot2, ok := st.Slot(model.SlotKey{AccountID: acct.ID, Index: 2}); !ok || slot2.State != model.SlotDown || slot2.GatePort != 0 {
		t.Fatalf("disabled slot 2 = %+v/%v, want a DOWN row without ports", slot2, ok)
	}
	code, conn = socks5Connect(t, first.GatePort)
	if code != 0x00 {
		t.Fatalf("kept gate reply = 0x%02x, want success", code)
	}
	echoOnce(t, conn)
}

func TestNodeControllerDrainUndrain(t *testing.T) {
	requirePort13000(t)
	st := newNodeTestStore(t)
	acct := newNodeTestAccount(t, st, "node-drain")
	logins := newFakeLoginResults()
	logins.set(acct.ID, account.LoginResult{
		AuthResult:      "OK",
		UserName:        "vendor-user",
		ConnectPassword: account.Secret(testConnectPassword),
		Servers:         map[string]apifacade.Server{"s1": testServer("s1")},
	})
	opener := newFakeSessionOpener()
	rec := &fakeReconciler{}
	controller := newNodeController(t, st, logins, opener, rec)

	if err := controller.SyncAccount(context.Background(), acct.ID); err != nil {
		t.Fatalf("SyncAccount: %v", err)
	}
	node := nodeByServer(t, st, acct.ID, "s1")

	if err := controller.DrainNode(context.Background(), node.ID); err != nil {
		t.Fatalf("DrainNode: %v", err)
	}
	drained, ok := st.Node(node.ID)
	if !ok || drained.State != model.NodeDraining {
		t.Fatalf("drained node = %+v, want DRAINING", drained)
	}
	code, _ := socks5Connect(t, node.GatePort)
	if code != 0x05 {
		t.Fatalf("draining gate reply = 0x%02x, want 0x05 refused", code)
	}
	t.Logf("drain %s: state=%s gate reply=0x%02x", node.ID, drained.State, code)

	if err := controller.UndrainNode(context.Background(), node.ID); err != nil {
		t.Fatalf("UndrainNode: %v", err)
	}
	undrained, ok := st.Node(node.ID)
	if !ok || undrained.State != model.NodeLive {
		t.Fatalf("undrained node = %+v, want LIVE", undrained)
	}
	code, conn := socks5Connect(t, node.GatePort)
	if code != 0x00 {
		t.Fatalf("undrained gate reply = 0x%02x, want success", code)
	}
	echoOnce(t, conn)
	t.Logf("undrain %s: state=%s gate reply=0x%02x", node.ID, undrained.State, code)

	// A login refresh must not undo an operator drain.
	if err := controller.DrainNode(context.Background(), node.ID); err != nil {
		t.Fatalf("second DrainNode: %v", err)
	}
	if err := controller.SyncAccount(context.Background(), acct.ID); err != nil {
		t.Fatalf("SyncAccount after drain: %v", err)
	}
	if refreshed, _ := st.Node(node.ID); refreshed.State != model.NodeDraining {
		t.Fatalf("refreshed node state = %q, want DRAINING to survive", refreshed.State)
	}
	// sync + drain + undrain + drain + sync.
	if got := rec.callCount(); got != 5 {
		t.Fatalf("reconciles = %d, want 5", got)
	}

	if err := controller.DrainNode(context.Background(), "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("DrainNode missing error = %v, want ErrNotFound", err)
	}
}

// TestNodeDrainHTTPHookDrivesGate wires the API seam to the real controller:
// POST drain must close the gate before the store reports DRAINING and POST
// undrain must reopen it. Everything stays on loopback plus the fake session.
func TestNodeDrainHTTPHookDrivesGate(t *testing.T) {
	requirePort13000(t)
	st := newNodeTestStore(t)
	acct := newNodeTestAccount(t, st, "node-http-drain")
	logins := newFakeLoginResults()
	logins.set(acct.ID, account.LoginResult{
		AuthResult:      "OK",
		UserName:        "vendor-user",
		ConnectPassword: account.Secret(testConnectPassword),
		Servers:         map[string]apifacade.Server{"s1": testServer("s1")},
	})
	opener := newFakeSessionOpener()
	rec := &fakeReconciler{}
	controller := newNodeController(t, st, logins, opener, rec)
	if err := controller.SyncAccount(context.Background(), acct.ID); err != nil {
		t.Fatalf("SyncAccount: %v", err)
	}
	node := nodeByServer(t, st, acct.ID, "s1")

	ts := httptest.NewServer(api.New(api.Config{
		AllowInsecure: true,
		NodeDrainHook: func(ctx context.Context, nodeID string, drain bool) error {
			if drain {
				return controller.DrainNode(ctx, nodeID)
			}
			return controller.UndrainNode(ctx, nodeID)
		},
	}, st, slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(ts.Close)

	post := func(action string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/nodes/"+url.PathEscape(node.ID)+"/actions/"+action, nil)
		if err != nil {
			t.Fatalf("new %s request: %v", action, err)
		}
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatalf("POST %s: %v", action, err)
		}
		return resp
	}

	resp := post("drain")
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("drain status = %d, want 200 (body %s)", resp.StatusCode, body)
	}
	_ = resp.Body.Close()
	drained, ok := st.Node(node.ID)
	if !ok || drained.State != model.NodeDraining {
		t.Fatalf("drained node = %+v, want DRAINING", drained)
	}
	code, _ := socks5Connect(t, node.GatePort)
	if code != 0x05 {
		t.Fatalf("draining gate reply = 0x%02x, want 0x05 refused", code)
	}
	t.Logf("http drain %s: state=%s gate reply=0x%02x", node.ID, drained.State, code)

	resp = post("undrain")
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("undrain status = %d, want 200 (body %s)", resp.StatusCode, body)
	}
	_ = resp.Body.Close()
	undrained, ok := st.Node(node.ID)
	if !ok || undrained.State != model.NodeLive {
		t.Fatalf("undrained node = %+v, want LIVE", undrained)
	}
	code, conn := socks5Connect(t, node.GatePort)
	if code != 0x00 {
		t.Fatalf("undrained gate reply = 0x%02x, want success", code)
	}
	echoOnce(t, conn)
	t.Logf("http undrain %s: state=%s gate reply=0x%02x", node.ID, undrained.State, code)

	// A controller failure must abort the HTTP drain: after Stop the gate is
	// closed, so the store must keep the node LIVE instead of DRAINING.
	if err := controller.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	resp = post("drain")
	if resp.StatusCode != http.StatusInternalServerError {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("drain after Stop status = %d, want 500 (body %s)", resp.StatusCode, body)
	}
	_ = resp.Body.Close()
	kept, ok := st.Node(node.ID)
	if !ok || kept.State != model.NodeLive {
		t.Fatalf("node after the aborted drain = %+v, want LIVE", kept)
	}
	t.Logf("http drain after stop: 500 INTERNAL, node stayed %s", kept.State)
}

func TestNodeControllerStopClosesGates(t *testing.T) {
	requirePort13000(t)
	st := newNodeTestPoolStore(t)
	acct := newNodeTestAccount(t, st, "node-stop")
	logins := newFakeLoginResults()
	logins.set(acct.ID, account.LoginResult{
		AuthResult:      "OK",
		UserName:        "vendor-user",
		ConnectPassword: account.Secret(testConnectPassword),
		Servers: map[string]apifacade.Server{
			"s1": testServer("s1"),
			"s2": testServer("s2"),
		},
	})
	opener := newFakeSessionOpener()
	rec := &fakeReconciler{}
	grantSlots(t, st, acct, 2)
	controller := newNodeController(t, st, logins, opener, rec)

	if err := controller.SyncAccount(context.Background(), acct.ID); err != nil {
		t.Fatalf("SyncAccount: %v", err)
	}
	first := nodeByServer(t, st, acct.ID, "s1")
	second := nodeByServer(t, st, acct.ID, "s2")

	if err := controller.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got := rec.callCount(); got != 2 {
		t.Fatalf("reconciles = %d, want 2 (sync + stop)", got)
	}
	for _, session := range []*fakeFlowSession{opener.session("s1"), opener.session("s2")} {
		if session == nil || !session.isClosed() {
			t.Fatal("Stop left a session open")
		}
	}
	for _, port := range []int{first.GatePort, second.GatePort} {
		if conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 500*time.Millisecond); err == nil {
			_ = conn.Close()
			t.Fatalf("gate %d still accepts connections after Stop", port)
		}
	}
	t.Logf("stop: ports %d/%d refused, both sessions closed, reconciles=%d", first.GatePort, second.GatePort, rec.callCount())
	if err := controller.Stop(context.Background()); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
	if err := controller.SyncAccount(context.Background(), acct.ID); !errors.Is(err, runtime.ErrControllerStopped) {
		t.Fatalf("SyncAccount after Stop error = %v, want ErrControllerStopped", err)
	}
	if err := controller.DrainNode(context.Background(), first.ID); !errors.Is(err, runtime.ErrControllerStopped) {
		t.Fatalf("DrainNode after Stop error = %v, want ErrControllerStopped", err)
	}
}

func TestNodeControllerStopAccount(t *testing.T) {
	requirePort13000(t)
	st := newNodeTestPoolStore(t)
	first := newNodeTestAccount(t, st, "node-stop-account-a")
	second := newNodeTestAccount(t, st, "node-stop-account-b")
	logins := newFakeLoginResults()
	logins.set(first.ID, account.LoginResult{
		AuthResult:      "OK",
		UserName:        "vendor-user",
		ConnectPassword: account.Secret(testConnectPassword),
		Servers: map[string]apifacade.Server{
			"s1": testServer("s1"),
			"s2": testServer("s2"),
		},
	})
	logins.set(second.ID, account.LoginResult{
		AuthResult:      "OK",
		UserName:        "vendor-user",
		ConnectPassword: account.Secret(testConnectPassword),
		Servers:         map[string]apifacade.Server{"t1": testServer("t1")},
	})
	opener := newFakeSessionOpener()
	rec := &fakeReconciler{}
	grantSlots(t, st, first, 2)
	controller := newNodeController(t, st, logins, opener, rec)

	for _, id := range []string{first.ID, second.ID} {
		if err := controller.SyncAccount(context.Background(), id); err != nil {
			t.Fatalf("SyncAccount(%s): %v", id, err)
		}
	}
	firstNode := nodeByServer(t, st, first.ID, "s1")
	secondNode := nodeByServer(t, st, first.ID, "s2")
	keptNode := nodeByServer(t, st, second.ID, "t1")
	if got := rec.callCount(); got != 2 {
		t.Fatalf("reconciles after the syncs = %d, want 2", got)
	}

	if err := controller.StopAccount(context.Background(), first.ID); err != nil {
		t.Fatalf("StopAccount: %v", err)
	}
	// StopAccount stops gates only: the API handler reconciles after the store
	// mutation, so no reconcile happens here.
	if got := rec.callCount(); got != 2 {
		t.Fatalf("reconciles after StopAccount = %d, want 2 (the syncs only)", got)
	}
	for _, port := range []int{firstNode.GatePort, secondNode.GatePort} {
		if conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 500*time.Millisecond); err == nil {
			_ = conn.Close()
			t.Fatalf("stopped account's gate %d still accepts connections", port)
		}
	}
	for _, serverID := range []string{"s1", "s2"} {
		if session := opener.session(serverID); session == nil || !session.isClosed() {
			t.Fatalf("%s session is not closed after StopAccount", serverID)
		}
	}
	if session := opener.session("t1"); session == nil || session.isClosed() {
		t.Fatal("StopAccount closed the other account's session")
	}
	code, conn := socks5Connect(t, keptNode.GatePort)
	if code != 0x00 {
		t.Fatalf("other account's gate reply = 0x%02x, want success", code)
	}
	echoOnce(t, conn)

	// StopAccount stops gates, not store nodes: the store cascade is the
	// caller's job.
	for _, node := range []model.ProxyNode{firstNode, secondNode, keptNode} {
		if _, ok := st.Node(node.ID); !ok {
			t.Fatalf("StopAccount deleted store node %s", node.ID)
		}
	}

	// Idempotent: a second call and an unknown account change nothing and do
	// not reconcile.
	if err := controller.StopAccount(context.Background(), first.ID); err != nil {
		t.Fatalf("second StopAccount: %v", err)
	}
	if err := controller.StopAccount(context.Background(), "acc-does-not-exist"); err != nil {
		t.Fatalf("StopAccount for an unknown account: %v", err)
	}
	if got := rec.callCount(); got != 2 {
		t.Fatalf("reconciles after idempotent calls = %d, want 2", got)
	}
	code, conn = socks5Connect(t, keptNode.GatePort)
	if code != 0x00 {
		t.Fatalf("kept gate reply after idempotent calls = 0x%02x, want success", code)
	}
	echoOnce(t, conn)
	t.Logf("stop-account %s: ports %d/%d refused, sessions closed, %s kept port %d, reconciles=%d",
		first.ID, firstNode.GatePort, secondNode.GatePort, keptNode.ID, keptNode.GatePort, rec.callCount())
}

func TestNodeControllerStopAccountAggregatesErrors(t *testing.T) {
	st := newNodeTestPoolStore(t)
	acct := newNodeTestAccount(t, st, "node-stop-account-errors")
	logins := newFakeLoginResults()
	logins.set(acct.ID, account.LoginResult{
		AuthResult:      "OK",
		UserName:        "vendor-user",
		ConnectPassword: account.Secret(testConnectPassword),
		Servers: map[string]apifacade.Server{
			"s1": testServer("s1"),
			"s2": testServer("s2"),
		},
	})
	closeErr := errors.New("gate close failed")
	reconcileErr := errors.New("resin unavailable")
	opener := newFakeSessionOpener()
	grantSlots(t, st, acct, 2)
	var built int
	gates := make([]*fakeGate, 0, 2)
	controller, err := runtime.NewNodeController(runtime.NodeControllerConfig{
		Store:      st,
		Logins:     logins,
		Sessions:   opener,
		Reconciler: &fakeReconciler{err: reconcileErr},
		Gates: func(_ gate.Config, _ gate.Session) (runtime.Gate, error) {
			built++
			g := &fakeGate{}
			if built == 1 {
				g.closeErr = closeErr
			}
			gates = append(gates, g)
			return g, nil
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("NewNodeController: %v", err)
	}
	t.Cleanup(func() { _ = controller.Stop(context.Background()) })

	// The sync succeeds at the gate level but reports the reconcile failure.
	if err := controller.SyncAccount(context.Background(), acct.ID); !errors.Is(err, reconcileErr) {
		t.Fatalf("SyncAccount error = %v, want the reconcile failure", err)
	}
	if built != 2 || len(gates) != 2 {
		t.Fatalf("gates built = %d/%d, want 2", built, len(gates))
	}

	err = controller.StopAccount(context.Background(), acct.ID)
	if !errors.Is(err, closeErr) {
		t.Fatalf("StopAccount error = %v, want the close failure", err)
	}
	// StopAccount no longer reconciles, so the reconciler's error is not part
	// of its result; it is reported by SyncAccount above.
	if errors.Is(err, reconcileErr) {
		t.Fatalf("StopAccount error = %v, want only the close failure", err)
	}
	stopErr := err
	for i, g := range gates {
		if !g.closed.Load() {
			t.Fatalf("gate %d was not closed despite the error", i)
		}
	}
	for _, serverID := range []string{"s1", "s2"} {
		if session := opener.session(serverID); session == nil || !session.isClosed() {
			t.Fatalf("%s session is not closed despite the gate error", serverID)
		}
	}
	// A failed close still drops the gate, so a retry is a clean no-op.
	if err := controller.StopAccount(context.Background(), acct.ID); err != nil {
		t.Fatalf("second StopAccount = %v, want nil", err)
	}
	t.Logf("stop-account errors: %v", stopErr)
}

// TestAccountDeleteStopsGatesBeforeStoreCascade pins the B0 delete seam end to
// end: DELETE runs the controller's StopAccount before store.DeleteAccount, so
// the gates and sessions are closed and the store cascade then removes the
// account and its nodes. The handler reconciles the panel only after that
// cascade, so the publish sees zero LIVE nodes and no account.
func TestAccountDeleteStopsGatesBeforeStoreCascade(t *testing.T) {
	requirePort13000(t)
	st := newNodeTestPoolStore(t)
	acct := newNodeTestAccount(t, st, "node-delete")
	logins := newFakeLoginResults()
	logins.set(acct.ID, account.LoginResult{
		AuthResult:      "OK",
		UserName:        "vendor-user",
		ConnectPassword: account.Secret(testConnectPassword),
		Servers: map[string]apifacade.Server{
			"s1": testServer("s1"),
			"s2": testServer("s2"),
		},
	})
	opener := newFakeSessionOpener()
	rec := &mutationPanel{st: st, accountID: acct.ID}
	grantSlots(t, st, acct, 2)
	controller := newNodeController(t, st, logins, opener, rec)
	if err := controller.SyncAccount(context.Background(), acct.ID); err != nil {
		t.Fatalf("SyncAccount: %v", err)
	}
	first := nodeByServer(t, st, acct.ID, "s1")
	second := nodeByServer(t, st, acct.ID, "s2")

	ts := httptest.NewServer(api.New(api.Config{
		AllowInsecure:     true,
		Panel:             rec,
		AccountDeleteHook: controller.StopAccount,
	}, st, slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(ts.Close)

	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/v1/accounts/"+acct.ID, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want 204", resp.StatusCode)
	}

	if got := rec.calls; got != 2 {
		t.Fatalf("reconciles = %d, want 2 (sync + post-delete)", got)
	}
	if got := rec.liveAt[1]; got != 0 {
		t.Fatalf("LIVE nodes at the post-delete reconcile = %d, want 0", got)
	}
	if rec.accountAt[1] {
		t.Fatal("the post-delete reconcile ran before store.DeleteAccount removed the account")
	}
	for _, port := range []int{first.GatePort, second.GatePort} {
		if conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 500*time.Millisecond); err == nil {
			_ = conn.Close()
			t.Fatalf("deleted account's gate %d still accepts connections", port)
		}
	}
	for _, serverID := range []string{"s1", "s2"} {
		if session := opener.session(serverID); session == nil || !session.isClosed() {
			t.Fatalf("%s session is not closed after DELETE", serverID)
		}
	}
	if _, ok := st.Account(acct.ID); ok {
		t.Fatal("DELETE left the account in the store")
	}
	if nodes := st.Nodes(); len(nodes) != 0 {
		t.Fatalf("DELETE left %d nodes in the store", len(nodes))
	}

	// A second delete of the same id is a plain 404 and must not run the hook
	// again.
	req, err = http.NewRequest(http.MethodDelete, ts.URL+"/v1/accounts/"+acct.ID, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err = ts.Client().Do(req)
	if err != nil {
		t.Fatalf("second DELETE: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("second DELETE status = %d, want 404", resp.StatusCode)
	}
	if got := rec.calls; got != 2 {
		t.Fatalf("reconciles after the 404 = %d, want 2", got)
	}
	t.Logf("delete %s: gates %d/%d refused, sessions closed, nodes removed, reconciles=%d",
		acct.ID, first.GatePort, second.GatePort, rec.calls)
}

// TestAccountLogoutStopsGatesBeforeStoreDrain pins the B0 logout seam end to
// end: POST /v1/accounts/{id}/actions/logout runs the controller's StopAccount
// before store.LogoutAccount, so the gates and FV sessions are closed first and
// the store then pauses the account and marks its nodes DRAINING. The handler
// reconciles the panel only after that store mutation, so the publish sees zero
// LIVE nodes. The other account keeps its gate, session and LIVE node.
func TestAccountLogoutStopsGatesBeforeStoreDrain(t *testing.T) {
	requirePort13000(t)
	st := newNodeTestPoolStore(t)
	loggedOut := newNodeTestAccount(t, st, "node-logout")
	kept := newNodeTestAccount(t, st, "node-logout-other")
	logins := newFakeLoginResults()
	logins.set(loggedOut.ID, account.LoginResult{
		AuthResult:      "OK",
		UserName:        "vendor-user",
		ConnectPassword: account.Secret(testConnectPassword),
		Servers: map[string]apifacade.Server{
			"s1": testServer("s1"),
			"s2": testServer("s2"),
		},
	})
	logins.set(kept.ID, account.LoginResult{
		AuthResult:      "OK",
		UserName:        "vendor-user",
		ConnectPassword: account.Secret(testConnectPassword),
		Servers:         map[string]apifacade.Server{"t1": testServer("t1")},
	})
	opener := newFakeSessionOpener()
	rec := &mutationPanel{st: st, accountID: loggedOut.ID}
	grantSlots(t, st, loggedOut, 2)
	controller := newNodeController(t, st, logins, opener, rec)
	for _, id := range []string{loggedOut.ID, kept.ID} {
		if err := controller.SyncAccount(context.Background(), id); err != nil {
			t.Fatalf("SyncAccount(%s): %v", id, err)
		}
	}
	first := nodeByServer(t, st, loggedOut.ID, "s1")
	second := nodeByServer(t, st, loggedOut.ID, "s2")
	keptNode := nodeByServer(t, st, kept.ID, "t1")

	ts := httptest.NewServer(api.New(api.Config{
		AllowInsecure:     true,
		Panel:             rec,
		AccountLogoutHook: controller.StopAccount,
	}, st, slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(ts.Close)

	logout := func(want int) model.Account {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/accounts/"+loggedOut.ID+"/actions/logout", nil)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatalf("logout: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("logout status = %d, want %d", resp.StatusCode, want)
		}
		var out model.Account
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode logout body: %v", err)
		}
		return out
	}

	account := logout(http.StatusOK)
	if account.State != model.AccountPaused || account.LiveNodeCount != 0 {
		t.Fatalf("logout account = %+v, want paused with 0 live nodes", account)
	}
	if got := rec.calls; got != 3 {
		t.Fatalf("reconciles = %d, want 3 (two syncs + post-logout)", got)
	}
	if got := rec.liveAt[2]; got != 0 {
		t.Fatalf("LIVE nodes at the post-logout reconcile = %d, want 0", got)
	}
	if !rec.accountAt[2] || rec.stateAt[2] != model.AccountPaused {
		t.Fatalf("state at the post-logout reconcile = (present %v, %q), want (true, paused)",
			rec.accountAt[2], rec.stateAt[2])
	}
	for _, port := range []int{first.GatePort, second.GatePort} {
		if conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 500*time.Millisecond); err == nil {
			_ = conn.Close()
			t.Fatalf("logged-out account's gate %d still accepts connections", port)
		}
	}
	for _, serverID := range []string{"s1", "s2"} {
		if session := opener.session(serverID); session == nil || !session.isClosed() {
			t.Fatalf("%s session is not closed after logout", serverID)
		}
	}
	if session := opener.session("t1"); session == nil || session.isClosed() {
		t.Fatal("logout closed the other account's session")
	}

	// The store transition happened after the gates closed: paused account,
	// every node DRAINING, and the other account untouched.
	stored, ok := st.Account(loggedOut.ID)
	if !ok {
		t.Fatal("logout removed the account from the store")
	}
	if stored.State != model.AccountPaused {
		t.Fatalf("stored account state = %q, want paused", stored.State)
	}
	for _, node := range []model.ProxyNode{first, second} {
		current, ok := st.Node(node.ID)
		if !ok {
			t.Fatalf("logout removed store node %s", node.ID)
		}
		if current.State != model.NodeDraining {
			t.Fatalf("node %s state = %q after logout, want DRAINING", node.ID, current.State)
		}
	}
	if other, ok := st.Account(kept.ID); !ok || other.State != model.AccountActive {
		t.Fatalf("other account = %+v, want active", other)
	}
	if current, ok := st.Node(keptNode.ID); !ok || current.State != model.NodeLive {
		t.Fatalf("other account's node = %+v, want LIVE", current)
	}
	code, conn := socks5Connect(t, keptNode.GatePort)
	if code != 0x00 {
		t.Fatalf("other account's gate reply = 0x%02x, want success", code)
	}
	echoOnce(t, conn)

	// Idempotent at the gate/store layer: a second logout answers 200 and runs
	// no gate close. The handler still reconciles after the store mutation, so
	// the panel gets one more idempotent publish.
	account = logout(http.StatusOK)
	if account.State != model.AccountPaused {
		t.Fatalf("second logout state = %q, want paused", account.State)
	}
	if got := rec.calls; got != 4 {
		t.Fatalf("reconciles after the idempotent logout = %d, want 4", got)
	}
	if got := rec.liveAt[3]; got != 0 {
		t.Fatalf("LIVE nodes at the second post-logout reconcile = %d, want 0", got)
	}
	code, conn = socks5Connect(t, keptNode.GatePort)
	if code != 0x00 {
		t.Fatalf("kept gate reply after the idempotent logout = 0x%02x, want success", code)
	}
	echoOnce(t, conn)
	t.Logf("logout %s: gates %d/%d refused, sessions closed, nodes DRAINING, %s kept port %d, reconciles=%d",
		loggedOut.ID, first.GatePort, second.GatePort, keptNode.ID, keptNode.GatePort, rec.calls)
}

func TestNodeControllerPerServerFailure(t *testing.T) {
	requirePort13000(t)
	st := newNodeTestPoolStore(t)
	acct := newNodeTestAccount(t, st, "node-partial")
	logins := newFakeLoginResults()
	logins.set(acct.ID, account.LoginResult{
		AuthResult:      "OK",
		UserName:        "vendor-user",
		ConnectPassword: account.Secret(testConnectPassword),
		Servers: map[string]apifacade.Server{
			"s1": testServer("s1"),
			"s2": testServer("s2"),
		},
	})
	opener := newFakeSessionOpener()
	opener.fail["s2"] = errors.New("fv auth denied")
	rec := &fakeReconciler{}
	grantSlots(t, st, acct, 2)
	controller := newNodeController(t, st, logins, opener, rec)

	if err := controller.SyncAccount(context.Background(), acct.ID); err != nil {
		t.Fatalf("SyncAccount with one working server: %v", err)
	}
	live := nodeByServer(t, st, acct.ID, "s1")
	if live.State != model.NodeLive || live.GatePort != 13000 {
		t.Fatalf("working node = %+v, want LIVE on 13000", live)
	}
	down := nodeByServer(t, st, acct.ID, "s2")
	if down.State != model.NodeDown || down.GatePort != 0 {
		t.Fatalf("failed node = %+v, want DOWN without a gate port", down)
	}
	t.Logf("partial: %s state=%s port=%d; %s state=%s port=%d", live.ID, live.State, live.GatePort, down.ID, down.State, down.GatePort)
	if got := rec.callCount(); got != 1 {
		t.Fatalf("reconciles = %d, want 1", got)
	}
	code, conn := socks5Connect(t, live.GatePort)
	if code != 0x00 {
		t.Fatalf("working gate reply = 0x%02x, want success", code)
	}
	echoOnce(t, conn)
}

func TestNodeControllerNoNodeStarted(t *testing.T) {
	requirePort13000(t)
	st := newNodeTestPoolStore(t)
	acct := newNodeTestAccount(t, st, "node-all-fail")
	logins := newFakeLoginResults()
	logins.set(acct.ID, account.LoginResult{
		AuthResult:      "OK",
		UserName:        "vendor-user",
		ConnectPassword: account.Secret(testConnectPassword),
		Servers: map[string]apifacade.Server{
			"s1": testServer("s1"),
			"s2": testServer("s2"),
		},
	})
	opener := newFakeSessionOpener()
	opener.fail["s1"] = errors.New("fv auth denied")
	opener.fail["s2"] = errors.New("fv auth denied")
	rec := &fakeReconciler{}
	grantSlots(t, st, acct, 2)
	controller := newNodeController(t, st, logins, opener, rec)

	err := controller.SyncAccount(context.Background(), acct.ID)
	if err == nil {
		t.Fatal("SyncAccount succeeded with no working server")
	}
	for _, id := range []string{slotNodeID(acct.ID, 1, "s1"), slotNodeID(acct.ID, 2, "s2")} {
		if !strings.Contains(err.Error(), id) {
			t.Fatalf("aggregate error %q does not name %q", err, id)
		}
	}
	for _, serverID := range []string{"s1", "s2"} {
		node := nodeByServer(t, st, acct.ID, serverID)
		if node.State != model.NodeDown || node.GatePort != 0 {
			t.Fatalf("node %s = %+v, want DOWN without a gate port", node.ID, node)
		}
	}
	if got := rec.callCount(); got != 1 {
		t.Fatalf("reconciles = %d, want 1", got)
	}
}

func TestNodeControllerNoUsableServers(t *testing.T) {
	st := newNodeTestStore(t)
	acct := newNodeTestAccount(t, st, "node-empty")
	unusable := testServer("s1")
	unusable.Sts = "down"
	logins := newFakeLoginResults()
	logins.set(acct.ID, account.LoginResult{
		AuthResult:      "OK",
		UserName:        "vendor-user",
		ConnectPassword: account.Secret(testConnectPassword),
		Servers:         map[string]apifacade.Server{"s1": unusable},
	})
	opener := newFakeSessionOpener()
	rec := &fakeReconciler{}
	controller := newNodeController(t, st, logins, opener, rec)

	if err := controller.SyncAccount(context.Background(), acct.ID); err == nil {
		t.Fatal("SyncAccount succeeded with no usable server")
	}
	if got := opener.callCount(); got != 0 {
		t.Fatalf("session opens = %d, want 0", got)
	}
	if got := rec.callCount(); got != 1 {
		t.Fatalf("reconciles = %d, want 1", got)
	}
	if nodes := st.Nodes(); len(nodes) != 0 {
		t.Fatalf("nodes = %d, want 0", len(nodes))
	}
}

func TestNodeControllerRequiresLoginAndDevice(t *testing.T) {
	st := newNodeTestStore(t)
	acct := newNodeTestAccount(t, st, "node-prereq")
	opener := newFakeSessionOpener()
	controller := newNodeController(t, st, newFakeLoginResults(), opener, nil)

	if err := controller.SyncAccount(context.Background(), acct.ID); !errors.Is(err, runtime.ErrNoLoginResult) {
		t.Fatalf("SyncAccount without a login error = %v, want ErrNoLoginResult", err)
	}

	noDevice, err := st.CreateAccount(model.CreateAccountInput{
		Label:       "node-prereq-device",
		Username:    "user-node-prereq-device",
		Password:    "node-test-password-not-a-credential",
		Platform:    model.PlatformLinux,
		Concurrency: 4,
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	logins := newFakeLoginResults()
	logins.set(noDevice.ID, account.LoginResult{
		AuthResult:      "OK",
		UserName:        "vendor-user",
		ConnectPassword: account.Secret(testConnectPassword),
		Servers:         map[string]apifacade.Server{"s1": testServer("s1")},
	})
	controller = newNodeController(t, st, logins, opener, nil)
	if err := controller.SyncAccount(context.Background(), noDevice.ID); err == nil {
		t.Fatal("SyncAccount without a device identity succeeded")
	}
}

func TestNodeControllerConcurrentSync(t *testing.T) {
	requirePort13000(t)
	st := newNodeTestStore(t)
	acct := newNodeTestAccount(t, st, "node-concurrent")
	logins := newFakeLoginResults()
	logins.set(acct.ID, account.LoginResult{
		AuthResult:      "OK",
		UserName:        "vendor-user",
		ConnectPassword: account.Secret(testConnectPassword),
		Servers:         map[string]apifacade.Server{"s1": testServer("s1")},
	})
	opener := newFakeSessionOpener()
	rec := &fakeReconciler{}
	controller := newNodeController(t, st, logins, opener, rec)

	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- controller.SyncAccount(context.Background(), acct.ID)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent SyncAccount: %v", err)
		}
	}
	if got := opener.callCount(); got != 1 {
		t.Fatalf("session opens = %d, want 1 (the gate is reused)", got)
	}
	if nodes := st.Nodes(); len(nodes) != 1 {
		t.Fatalf("nodes = %d, want 1", len(nodes))
	}
	if got := rec.callCount(); got != 4 {
		t.Fatalf("reconciles = %d, want one per sync", got)
	}
}

// newNodeControllerWithLimit is newNodeController with a MaxServers cap.
func newNodeControllerWithLimit(t *testing.T, st *store.Store, logins runtime.LoginResults, opener runtime.SessionOpener, rec runtime.Reconciler, maxServers int) *runtime.NodeController {
	t.Helper()
	controller, err := runtime.NewNodeController(runtime.NodeControllerConfig{
		Store:      st,
		Logins:     logins,
		Sessions:   opener,
		Reconciler: rec,
		MaxServers: maxServers,
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
	return controller
}

// fiveServers is a five-server login fixture with deliberately unsorted keys,
// so the deterministic sort decides which servers survive a cap.
func fiveServers() map[string]apifacade.Server {
	servers := make(map[string]apifacade.Server, 5)
	for _, id := range []string{"s5", "s1", "s3", "s2", "s4"} {
		servers[id] = testServer(id)
	}
	return servers
}

func TestNodeControllerMaxServersCapsSlots(t *testing.T) {
	requirePort13000(t)
	st := newNodeTestPoolStore(t)
	acct := newNodeTestAccount(t, st, "node-limit")
	logins := newFakeLoginResults()
	logins.set(acct.ID, account.LoginResult{
		AuthResult:      "OK",
		UserName:        "vendor-user",
		ConnectPassword: account.Secret(testConnectPassword),
		Servers:         fiveServers(),
	})
	opener := newFakeSessionOpener()
	rec := &fakeReconciler{}
	grantSlots(t, st, acct, 5)
	controller := newNodeControllerWithLimit(t, st, logins, opener, rec, 2)

	if err := controller.SyncAccount(context.Background(), acct.ID); err != nil {
		t.Fatalf("SyncAccount: %v", err)
	}

	if got := opener.callCount(); got != 2 {
		t.Fatalf("session opens = %d, want 2 (MaxServers caps the slot capacity)", got)
	}
	nodes := st.Nodes()
	if len(nodes) != 2 {
		t.Fatalf("nodes = %d, want 2", len(nodes))
	}
	// The deterministic sort keeps the first two server IDs: s1 and s2.
	first := nodeByServer(t, st, acct.ID, "s1")
	second := nodeByServer(t, st, acct.ID, "s2")
	if first.GatePort != 13000 || second.GatePort != 13001 {
		t.Fatalf("gate ports = %d/%d, want 13000/13001", first.GatePort, second.GatePort)
	}
	if first.Slot != 1 || second.Slot != 2 {
		t.Fatalf("node slots = %d/%d, want 1/2", first.Slot, second.Slot)
	}
	if got := st.SlotCount(acct.ID); got != 2 {
		t.Fatalf("slot rows = %d, want the capped capacity 2", got)
	}
	for _, id := range []string{"s3", "s4", "s5"} {
		if requests := opener.requestsFor(id); len(requests) != 0 {
			t.Fatalf("capped-out server %s got %d session requests, want 0", id, len(requests))
		}
	}
	// A second sync stays capped: no new session for the skipped servers.
	if err := controller.SyncAccount(context.Background(), acct.ID); err != nil {
		t.Fatalf("second SyncAccount: %v", err)
	}
	if got := opener.callCount(); got != 2 {
		t.Fatalf("session opens after resync = %d, want 2", got)
	}
	t.Logf("cap 2 of 5 slots: started %s port=%d and %s port=%d, skipped s3/s4/s5",
		first.ID, first.GatePort, second.ID, second.GatePort)
}

func TestNodeControllerMaxServersZeroUsesSlotCapacity(t *testing.T) {
	requirePort13000(t)
	st := newNodeTestPoolStore(t)
	acct := newNodeTestAccount(t, st, "node-limit-zero")
	logins := newFakeLoginResults()
	logins.set(acct.ID, account.LoginResult{
		AuthResult:      "OK",
		UserName:        "vendor-user",
		ConnectPassword: account.Secret(testConnectPassword),
		Servers:         fiveServers(),
	})
	opener := newFakeSessionOpener()
	rec := &fakeReconciler{}
	grantSlots(t, st, acct, 5)
	controller := newNodeControllerWithLimit(t, st, logins, opener, rec, 0)

	if err := controller.SyncAccount(context.Background(), acct.ID); err != nil {
		t.Fatalf("SyncAccount: %v", err)
	}
	if got := opener.callCount(); got != 5 {
		t.Fatalf("session opens = %d, want 5 (limit 0 leaves the slot capacity in charge)", got)
	}
	nodes := st.Nodes()
	if len(nodes) != 5 {
		t.Fatalf("nodes = %d, want 5", len(nodes))
	}
	for _, id := range []string{"s1", "s2", "s3", "s4", "s5"} {
		node := nodeByServer(t, st, acct.ID, id)
		if node.Slot == 0 {
			t.Fatalf("node %s has no slot", node.ID)
		}
	}
	t.Logf("limit 0 of 5 slots: all 5 slots started")
}

func TestNodeControllerMaxServersNegativeRejected(t *testing.T) {
	st := newNodeTestStore(t)
	logins := newFakeLoginResults()
	_, err := runtime.NewNodeController(runtime.NodeControllerConfig{
		Store:      st,
		Logins:     logins,
		Sessions:   newFakeSessionOpener(),
		MaxServers: -1,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err == nil {
		t.Fatal("NewNodeController accepted MaxServers -1")
	}
	if !strings.Contains(err.Error(), "MaxServers") {
		t.Fatalf("error = %v, want a MaxServers rejection", err)
	}
	t.Logf("negative MaxServers rejected: %v", err)
}

func TestNodeControllerMaxServersLowerLimitRemovesGates(t *testing.T) {
	requirePort13000(t)
	st := newNodeTestPoolStore(t)
	acct := newNodeTestAccount(t, st, "node-limit-lower")
	logins := newFakeLoginResults()
	logins.set(acct.ID, account.LoginResult{
		AuthResult:      "OK",
		UserName:        "vendor-user",
		ConnectPassword: account.Secret(testConnectPassword),
		Servers:         fiveServers(),
	})
	opener := newFakeSessionOpener()
	rec := &fakeReconciler{}
	grantSlots(t, st, acct, 3)

	// One controller with three slots running, then the same daemon restarts
	// with a lower cap: the old controller is stopped (its gates close) and a
	// fresh controller over the same store disables the out-of-cap slots on
	// the next sync, which is what a resync after a cap change looks like.
	controller := newNodeControllerWithLimit(t, st, logins, opener, rec, 3)
	if err := controller.SyncAccount(context.Background(), acct.ID); err != nil {
		t.Fatalf("SyncAccount: %v", err)
	}
	if got := opener.callCount(); got != 3 {
		t.Fatalf("session opens = %d, want 3", got)
	}
	s1 := nodeByServer(t, st, acct.ID, "s1")
	s3 := nodeByServer(t, st, acct.ID, "s3")
	s1Port, s3Port := s1.GatePort, s3.GatePort
	if s1Port != 13000 || s3Port != 13002 {
		t.Fatalf("gate ports = %d/%d, want 13000/13002", s1Port, s3Port)
	}

	// The old daemon's gates are gone before the new daemon syncs; the store
	// keeps the three nodes and slot rows for the resync to prune.
	if err := controller.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// A resync with a lower cap keeps the in-cap slot and removes the rest.
	capped := newNodeControllerWithLimit(t, st, logins, opener, rec, 1)
	if err := capped.SyncAccount(context.Background(), acct.ID); err != nil {
		t.Fatalf("capped SyncAccount: %v", err)
	}
	// s1 is the only in-cap server: it stays LIVE on its immutable pair.
	kept := nodeByServer(t, st, acct.ID, "s1")
	if kept.State != model.NodeLive || kept.GatePort != s1Port {
		t.Fatalf("kept server s1 = %+v, want LIVE on its reserved port %d", kept, s1Port)
	}
	for _, id := range []string{"s2", "s3", "s4", "s5"} {
		if _, ok := st.Node(slotNodeID(acct.ID, 2, id)); ok {
			t.Fatalf("node for out-of-cap server %s survived the resync", id)
		}
	}
	for _, index := range []int{2, 3} {
		slot, ok := st.Slot(model.SlotKey{AccountID: acct.ID, Index: index})
		if !ok || slot.State != model.SlotDown || slot.GatePort != 0 {
			t.Fatalf("disabled slot %d = %+v/%v, want DOWN without ports", index, slot, ok)
		}
	}
	if session := opener.session("s2"); session == nil || !session.isClosed() {
		t.Fatal("out-of-cap server s2 session is not closed")
	}
	if session := opener.session("s3"); session == nil || !session.isClosed() {
		t.Fatal("out-of-cap server s3 session is not closed")
	}
	if conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(s3Port)), 500*time.Millisecond); err == nil {
		_ = conn.Close()
		t.Fatalf("out-of-cap server s3 gate on port %d still accepts connections", s3Port)
	}
	code, conn := socks5Connect(t, kept.GatePort)
	if code != 0x00 {
		t.Fatalf("kept gate reply = 0x%02x, want success", code)
	}
	echoOnce(t, conn)
	t.Logf("cap 3 -> 1: s1 stayed LIVE on port %d; s2/s3 gates closed, nodes deleted, port %d refused",
		kept.GatePort, s3Port)
}

// Ensure the fake session keeps satisfying the production interfaces.
var (
	_ runtime.SessionOpener = (*fakeSessionOpener)(nil)
	_ runtime.Gate          = (*fakeGate)(nil)
)

// fakeGate proves the Gate interface is injectable without a listener.
type fakeGate struct {
	started  atomic.Bool
	draining atomic.Bool
	closed   atomic.Bool
	closeErr error
	addr     net.Addr
}

func (g *fakeGate) Start(context.Context) error { g.started.Store(true); return nil }
func (g *fakeGate) Drain(context.Context) error { g.draining.Store(true); return nil }
func (g *fakeGate) Undrain() error              { g.draining.Store(false); return nil }
func (g *fakeGate) Close() error {
	g.closed.Store(true)
	return g.closeErr
}
func (g *fakeGate) Addr() net.Addr { return g.addr }

func TestNodeControllerGateFactory(t *testing.T) {
	st := newNodeTestStore(t)
	acct := newNodeTestAccount(t, st, "node-factory")
	logins := newFakeLoginResults()
	logins.set(acct.ID, account.LoginResult{
		AuthResult:      "OK",
		UserName:        "vendor-user",
		ConnectPassword: account.Secret(testConnectPassword),
		Servers:         map[string]apifacade.Server{"s1": testServer("s1")},
	})
	injected := &fakeGate{}
	var built int
	controller, err := runtime.NewNodeController(runtime.NodeControllerConfig{
		Store:    st,
		Logins:   logins,
		Sessions: newFakeSessionOpener(),
		Gates: func(_ gate.Config, _ gate.Session) (runtime.Gate, error) {
			built++
			return injected, nil
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("NewNodeController: %v", err)
	}
	t.Cleanup(func() { _ = controller.Stop(context.Background()) })

	if err := controller.SyncAccount(context.Background(), acct.ID); err != nil {
		t.Fatalf("SyncAccount: %v", err)
	}
	if built != 1 || !injected.started.Load() {
		t.Fatalf("gate factory calls = %d started = %v, want 1/true", built, injected.started.Load())
	}
	node := nodeByServer(t, st, acct.ID, "s1")
	if node.GatePort < runtime.DefaultGatePortBase {
		t.Fatalf("node gate port = %d, want >= %d", node.GatePort, runtime.DefaultGatePortBase)
	}
	if err := controller.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if !injected.closed.Load() {
		t.Fatal("Stop did not close the injected gate")
	}
}
