package runtime_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
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
// so the controller's first gate can bind 127.0.0.1:13000.
func newNodeTestStore(t *testing.T) *store.Store {
	t.Helper()
	st := store.New()
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

// testServer is one usable vendor server entry.
func testServer(id string) apifacade.Server {
	return apifacade.Server{
		ID:   id,
		Svr:  "203.0.113.10",
		Tit:  "Shanghai 01",
		Ctry: "CN",
		Sts:  "ok",
		Ord:  "1",
		Par:  apifacade.ServerPar{FV: apifacade.ServerFV{P: 443}},
		Loc:  []apifacade.ServerLoc{{C: "CN", N: "Shanghai", O: 1}},
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
type fakeFlowSession struct {
	fields map[string]string
	closed atomic.Bool

	mu    sync.Mutex
	conns []net.Conn
}

func (s *fakeFlowSession) SID() string { return "fake-sid" }

func (s *fakeFlowSession) Fields() map[string]string { return s.fields }

func (s *fakeFlowSession) Healthy() bool { return !s.closed.Load() }

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

func nodeByServer(t *testing.T, st *store.Store, accountID, serverID string) model.ProxyNode {
	t.Helper()
	node, ok := st.Node(accountID + ":" + serverID)
	if !ok {
		t.Fatalf("node %s:%s is missing", accountID, serverID)
	}
	return node
}

func TestNodeControllerSyncCreatesReusesAndRemoves(t *testing.T) {
	requirePort13000(t)
	st := newNodeTestStore(t)
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

	// Dropping s2 stops its gate and deletes its node; s1 keeps serving.
	logins.set(acct.ID, account.LoginResult{
		AuthResult:      "OK",
		UserName:        "vendor-user",
		ConnectPassword: account.Secret(testConnectPassword),
		Servers:         map[string]apifacade.Server{"s1": testServer("s1")},
	})
	if err := controller.SyncAccount(context.Background(), acct.ID); err != nil {
		t.Fatalf("SyncAccount after server removal: %v", err)
	}
	if _, ok := st.Node(acct.ID + ":s2"); ok {
		t.Fatal("node for the removed server survived")
	}
	if session := opener.session("s2"); session == nil || !session.isClosed() {
		t.Fatal("removed server's session is not closed")
	}
	if conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(second.GatePort)), 500*time.Millisecond); err == nil {
		_ = conn.Close()
		t.Fatal("removed server's gate still accepts connections")
	}
	t.Logf("sync 3: removed %s, port %d refused, %s kept port %d", second.ID, second.GatePort, first.ID, first.GatePort)
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
		req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/nodes/"+node.ID+"/actions/"+action, nil)
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
	st := newNodeTestStore(t)
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
	st := newNodeTestStore(t)
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
	if got := rec.callCount(); got != 3 {
		t.Fatalf("reconciles after StopAccount = %d, want 3", got)
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
	// not reconcile again.
	if err := controller.StopAccount(context.Background(), first.ID); err != nil {
		t.Fatalf("second StopAccount: %v", err)
	}
	if err := controller.StopAccount(context.Background(), "acc-does-not-exist"); err != nil {
		t.Fatalf("StopAccount for an unknown account: %v", err)
	}
	if got := rec.callCount(); got != 3 {
		t.Fatalf("reconciles after idempotent calls = %d, want 3", got)
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
	st := newNodeTestStore(t)
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
	if !errors.Is(err, reconcileErr) {
		t.Fatalf("StopAccount error = %v, want the reconcile failure", err)
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
// account and its nodes.
func TestAccountDeleteStopsGatesBeforeStoreCascade(t *testing.T) {
	requirePort13000(t)
	st := newNodeTestStore(t)
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
	rec := &fakeReconciler{}
	controller := newNodeController(t, st, logins, opener, rec)
	if err := controller.SyncAccount(context.Background(), acct.ID); err != nil {
		t.Fatalf("SyncAccount: %v", err)
	}
	first := nodeByServer(t, st, acct.ID, "s1")
	second := nodeByServer(t, st, acct.ID, "s2")

	ts := httptest.NewServer(api.New(api.Config{
		AllowInsecure:     true,
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

	if got := rec.callCount(); got != 2 {
		t.Fatalf("reconciles = %d, want 2 (sync + delete stop)", got)
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
	if got := rec.callCount(); got != 2 {
		t.Fatalf("reconciles after the 404 = %d, want 2", got)
	}
	t.Logf("delete %s: gates %d/%d refused, sessions closed, nodes removed, reconciles=%d",
		acct.ID, first.GatePort, second.GatePort, rec.callCount())
}

func TestNodeControllerPerServerFailure(t *testing.T) {
	requirePort13000(t)
	st := newNodeTestStore(t)
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
	st := newNodeTestStore(t)
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
	controller := newNodeController(t, st, logins, opener, rec)

	err := controller.SyncAccount(context.Background(), acct.ID)
	if err == nil {
		t.Fatal("SyncAccount succeeded with no working server")
	}
	for _, id := range []string{acct.ID + ":s1", acct.ID + ":s2"} {
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
