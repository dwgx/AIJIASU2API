package api

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ajsaccountd/internal/account"
	"ajsaccountd/internal/gate"
	"ajsaccountd/internal/model"
	"ajsaccountd/internal/runtime"
	"ajsaccountd/internal/store"
)

// The test in this file is the API-level reopen falsifier of
// REVIEW-MULTISLOT-RESIDUALS finding 2: the logout handler holds the runtime's
// account-wide stop claim across the store mutation, so a login that begins
// after the fence but before the pause commits installs no gate, session,
// listener or LIVE node. The old handler ran the hook, returned to the store
// mutation, and a concurrent SyncAccount could start a gate in that window.

const (
	apiLoginReply = `{"Login":{"AuthResult":"OK","LoginToken":"tok-1|user",` +
		`"UserName":"vendor-user","ConnectPassword":"cp-1","MembershipStatus":"vip"}}`
	apiSyncReply = `{"Login":{"ConnectPassword":"cp-2"},"Servers":{"s1":{"id":"s1",` +
		`"vsid":1,"ctry":"CN","svr":"203.0.113.10","tit":"Shanghai 01","cmt":"",` +
		`"sts":"ok","ord":"1","cat":["cn"],"par":{"fv":{"u":1001,"p":443}},"loc":[{"c":"CN","n":"Shanghai","o":1}]}}}`
)

// apiPlainSealer is a reversible in-memory password sealer for the fixture.
type apiPlainSealer struct{}

func (apiPlainSealer) Seal(_ string, plaintext []byte) ([]byte, error) {
	return append([]byte("sealed:"), plaintext...), nil
}

func (apiPlainSealer) Open(_ string, sealed []byte) ([]byte, error) {
	return bytes.TrimPrefix(sealed, []byte("sealed:")), nil
}

func (apiPlainSealer) Delete(string) error { return nil }

// apiLoginControl serves one vendor control login and its loginSync. It is the
// same wire shape the runtime LoginService tests use, so the production login
// path runs in the test.
func newAPILoginControl(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read error", http.StatusInternalServerError)
			return
		}
		form, err := url.ParseQuery(string(body))
		if err != nil {
			http.Error(w, "parse error", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if form.Get("_client_login") != "" {
			_, _ = io.WriteString(w, apiSyncReply)
			return
		}
		_, _ = io.WriteString(w, apiLoginReply)
	}))
	t.Cleanup(server.Close)
	return server
}

// apiFakeSession is one authenticated session for the fixture. It carries a SID
// so the controller commits LIVE, and it records Close.
type apiFakeSession struct {
	sid    string
	closed atomic.Bool
}

func (s *apiFakeSession) SID() string               { return s.sid }
func (s *apiFakeSession) Fields() map[string]string { return map[string]string{"sip": "10.0.0.9"} }
func (s *apiFakeSession) Healthy() bool             { return !s.closed.Load() }
func (s *apiFakeSession) OpenFlow(context.Context, string, int) (net.Conn, error) {
	return nil, io.ErrClosedPipe
}
func (s *apiFakeSession) Close() error {
	s.closed.Store(true)
	return nil
}

type apiFakeOpener struct {
	mu       sync.Mutex
	sessions map[string]*apiFakeSession
}

func newAPIFakeOpener() *apiFakeOpener {
	return &apiFakeOpener{sessions: make(map[string]*apiFakeSession)}
}

func (o *apiFakeOpener) Open(_ context.Context, req account.SessionRequest) (account.FlowSession, error) {
	session := &apiFakeSession{sid: "api-sid-" + req.Server.ID}
	o.mu.Lock()
	o.sessions[req.Server.ID] = session
	o.mu.Unlock()
	return session, nil
}

func (o *apiFakeOpener) session(serverID string) *apiFakeSession {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.sessions[serverID]
}

// apiSignalingLogin signals when the production Login returned, i.e. when the
// vendor call, MarkLogin and remember all committed. The test needs that
// instant to reproduce "login after the fence but before logout commits": the
// logout's store pause must land after the login reactivated the account.
type apiSignalingLogin struct {
	*account.LoginService
	done chan struct{}
	once sync.Once
}

func (l *apiSignalingLogin) Login(ctx context.Context, accountID string) (model.Account, error) {
	acct, err := l.LoginService.Login(ctx, accountID)
	l.once.Do(func() { close(l.done) })
	return acct, err
}

// apiBlockingGate blocks the first gate's Close. That is the deterministic
// window between the fence (inside BeginAccountStop/StopAccount) and the
// logout handler's store mutation: the test starts the racing login there.
type apiBlockingGate struct {
	runtime.Gate
	closeOnce sync.Once
	entered   chan struct{}
	release   chan struct{}
}

func (g *apiBlockingGate) Close() error {
	g.closeOnce.Do(func() { close(g.entered) })
	<-g.release
	return g.Gate.Close()
}

type apiGateFactory struct {
	mu      sync.Mutex
	built   int
	entered chan struct{}
	release chan struct{}
}

func newAPIGateFactory() *apiGateFactory {
	return &apiGateFactory{entered: make(chan struct{}), release: make(chan struct{})}
}

func (f *apiGateFactory) build(cfg gate.Config, session gate.Session) (runtime.Gate, error) {
	srv, err := gate.NewServer(cfg, session)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	first := f.built == 0
	f.built++
	f.mu.Unlock()
	if !first {
		return srv, nil
	}
	return &apiBlockingGate{Gate: srv, entered: f.entered, release: f.release}, nil
}

type apiHTTPResult struct {
	status int
	body   []byte
	err    error
}

// apiDo performs one authenticated API request off the test goroutine, so a
// blocked handler does not call t.Fatalf from a non-test goroutine.
func apiDo(ts *httptest.Server, method, path string) apiHTTPResult {
	req, err := http.NewRequest(method, ts.URL+path, nil)
	if err != nil {
		return apiHTTPResult{err: err}
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	if method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions {
		req.Header.Set("X-AJS-CSRF", "1")
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		return apiHTTPResult{err: err}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return apiHTTPResult{status: resp.StatusCode, body: body, err: err}
}

func waitSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func waitResult(t *testing.T, ch <-chan apiHTTPResult, what string) apiHTTPResult {
	t.Helper()
	select {
	case result := <-ch:
		if result.err != nil {
			t.Fatalf("%s: %v", what, result.err)
		}
		return result
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		return apiHTTPResult{}
	}
}

// TestLogoutHoldsAccountClaimAcrossStoreMutation runs the real HTTP logout
// handler, the real LoginService and the real NodeController. The gate Close
// is blocked after the fence, a login request starts and completes in that
// window, and only then does the logout store mutation commit. After the
// logout returns the account must be paused with no gate, session, listener or
// LIVE node.
func TestLogoutHoldsAccountClaimAcrossStoreMutation(t *testing.T) {
	clock := &apiClock{t: time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)}
	st := store.NewWithSealer(apiPlainSealer{})
	if err := st.Restore([]byte(`{"version":1}`)); err != nil {
		t.Fatalf("Restore empty snapshot: %v", err)
	}
	acct, err := st.CreateAccount(model.CreateAccountInput{
		Label:       "logout-claim",
		Username:    "logout-claim-user",
		Password:    "logout-claim-password-not-a-credential",
		Platform:    model.PlatformLinux,
		Concurrency: 1,
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
	control := newAPILoginControl(t)
	service, err := account.NewLoginService(st, account.LoginConfig{
		Hosts:      []string{control.URL},
		Enabled:    true,
		HTTPClient: control.Client(),
		Now:        clock.Now,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("NewLoginService: %v", err)
	}
	t.Cleanup(service.Close)
	opener := newAPIFakeOpener()
	gates := newAPIGateFactory()
	controller, err := runtime.NewNodeController(runtime.NodeControllerConfig{
		Store:            st,
		Logins:           service,
		Sessions:         opener,
		Gates:            gates.build,
		MaxServers:       1,
		PortBase:         13900,
		Now:              clock.Now,
		LivenessInterval: -1,
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

	ctx := context.Background()
	if _, err := service.Login(ctx, acct.ID); err != nil {
		t.Fatalf("initial Login: %v", err)
	}
	if err := controller.SyncAccount(ctx, acct.ID); err != nil {
		t.Fatalf("initial SyncAccount: %v", err)
	}
	before := controller.Snapshot()
	if len(before) != 1 || before[0].Port == 0 {
		t.Fatalf("snapshot after the initial sync = %+v, want one bound gate", before)
	}
	port := before[0].Port
	if !apiDial(port) {
		t.Fatalf("listener %d does not accept before logout", port)
	}

	// The racing login goes through a wrapper that signals when the production
	// Login (vendor call, MarkLogin, remember) has returned. The test only
	// releases the logout's store mutation after that signal, so the login
	// really is "after the fence, before the logout commit".
	signaling := &apiSignalingLogin{LoginService: service, done: make(chan struct{})}
	hookRelease := make(chan struct{})
	server := New(Config{
		AdminToken:   testToken,
		Catalog:      controller,
		LoginService: signaling,
		LoginHook:    controller.SyncAccount,
		// The hook is the pre-fix path: it stops the gates and then blocks
		// before returning to the handler, which is the window the old code
		// left open. The fixed handler uses the controller's claim instead and
		// never calls it.
		AccountLogoutHook: func(ctx context.Context, accountID string) error {
			if err := controller.StopAccount(ctx, accountID); err != nil {
				return err
			}
			<-hookRelease
			return nil
		},
	}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)

	logoutCh := make(chan apiHTTPResult, 1)
	go func() {
		logoutCh <- apiDo(ts, http.MethodPost, "/v1/accounts/"+acct.ID+"/actions/logout")
	}()
	// The fence is installed before the gate Close blocks, so the login below
	// begins after the fence. Advance the pinned clock so the attempt start is
	// strictly later, which is what makes it a would-be operator reopen.
	waitSignal(t, gates.entered, "the logout gate close")
	clock.Advance(time.Second)

	loginCh := make(chan apiHTTPResult, 1)
	go func() {
		loginCh <- apiDo(ts, http.MethodPost, "/v1/accounts/"+acct.ID+"/actions/login")
	}()
	// Wait until the login's vendor call, MarkLogin and remember committed.
	// Only then does the logout's store mutation run, which is the exact
	// "after the fence, before the commit" ordering the residual names.
	waitSignal(t, signaling.done, "the racing login's production Login")
	// Let the logout proceed. The fixed handler commits the pause and then
	// releases the claim; the old handler returns to its hook block first, so
	// the racing SyncAccount starts a gate before the store mutation.
	close(gates.release)
	loginResult := waitResult(t, loginCh, "the racing login")
	close(hookRelease)
	logoutResult := waitResult(t, logoutCh, "the logout")

	if loginResult.status != http.StatusOK {
		t.Fatalf("racing login status = %d, want 200: %s", loginResult.status, loginResult.body)
	}
	if logoutResult.status != http.StatusOK {
		t.Fatalf("logout status = %d, want 200: %s", logoutResult.status, logoutResult.body)
	}

	// Zero gate, session, listener and LIVE node after logout returns.
	if running := controller.Snapshot(); len(running) != 0 {
		t.Fatalf("snapshot after logout = %+v, want no running gate", running)
	}
	if apiDial(port) {
		t.Fatalf("listener %d still accepts after logout", port)
	}
	if session := opener.session("s1"); session != nil && !session.closed.Load() {
		t.Fatalf("session s1 is still open after logout: %+v", session)
	}
	for _, node := range st.Nodes() {
		if node.AccountID == acct.ID && node.State == model.NodeLive {
			t.Fatalf("node %s is LIVE after logout: %+v", node.ID, node)
		}
	}
	if current, ok := st.Account(acct.ID); !ok || current.State != model.AccountPaused {
		t.Fatalf("account after logout = %+v, want paused", current)
	}
	if result, ok := service.Result(acct.ID); ok {
		t.Fatalf("logout retained a login result after Forget: %+v", result.UserName)
	}
	// A later hook run cannot reopen the paused account.
	if err := controller.SyncAccount(ctx, acct.ID); err == nil {
		t.Fatal("SyncAccount after logout succeeded")
	}
	if running := controller.Snapshot(); len(running) != 0 {
		t.Fatalf("late sync left %d gates", len(running))
	}
}

// apiClock is a pinned clock for the controller and login service.
type apiClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *apiClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *apiClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// apiDial reports whether a loopback listener accepts.
func apiDial(port int) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 250*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// Compile-time proof the fixture session satisfies the account view the
// controller and gate expect.
var _ account.FlowSession = (*apiFakeSession)(nil)
