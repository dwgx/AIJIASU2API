package runtime_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"ajsaccountd/internal/account"
	"ajsaccountd/internal/api"
	"ajsaccountd/internal/gate"
	"ajsaccountd/internal/model"
	"ajsaccountd/internal/runtime"
	"ajsaccountd/internal/store"
	"ajsaccountd/internal/webhook"
	"ajsproxy/apifacade"
)

// testSealer is a reversible in-memory store.Sealer. It is enough for
// AccountSecret to answer "a sealed password exists" without a real key file,
// which is all the auto-login eligibility check reads.
type testSealer struct{}

func (testSealer) Seal(id string, plaintext []byte) ([]byte, error) {
	return append([]byte(id+":"), plaintext...), nil
}

func (testSealer) Open(id string, sealed []byte) ([]byte, error) {
	prefix := id + ":"
	if !strings.HasPrefix(string(sealed), prefix) {
		return nil, errors.New("sealed blob belongs to another account")
	}
	return append([]byte(nil), sealed[len(prefix):]...), nil
}

func (testSealer) Delete(string) error { return nil }

// newAutoLoginStore returns an empty store that seals account passwords.
func newAutoLoginStore(t *testing.T) *store.Store {
	t.Helper()
	st := store.NewWithSealer(testSealer{})
	if err := st.Restore([]byte(`{"version":1}`)); err != nil {
		t.Fatalf("Restore empty snapshot: %v", err)
	}
	return st
}

func newAutoLoginAccount(t *testing.T, st *store.Store, label string) model.Account {
	t.Helper()
	acct, err := st.CreateAccount(model.CreateAccountInput{
		Label:       label,
		Username:    "user-" + label,
		Password:    "auto-login-password-not-a-credential",
		Platform:    model.PlatformLinux,
		Concurrency: 1,
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	return acct
}

// putAutoLoginNode gives the account a current node, the second half of the
// auto-login eligibility rule.
func putAutoLoginNode(t *testing.T, st *store.Store, acct model.Account, label string) model.ProxyNode {
	t.Helper()
	node, err := st.PutNode(model.ProxyNode{
		ID:        acct.ID + ":s1",
		AccountID: acct.ID,
		Label:     label,
		Region:    "cn",
		State:     model.NodeLive,
		GatePort:  13000,
		EgressIP:  "203.0.113.9",
		Capacity:  1,
		Protocol:  model.ProtocolSOCKS5,
	})
	if err != nil {
		t.Fatalf("PutNode: %v", err)
	}
	return node
}

// fakeLoginCall records every Login call and can fail selected accounts.
type fakeLoginCall struct {
	store *store.Store

	mu    sync.Mutex
	calls []string
	errs  map[string]error
	delay time.Duration
}

func newFakeLoginCall(st *store.Store) *fakeLoginCall {
	return &fakeLoginCall{store: st, errs: make(map[string]error)}
}

func (f *fakeLoginCall) Login(ctx context.Context, accountID string) (model.Account, error) {
	f.mu.Lock()
	f.calls = append(f.calls, accountID)
	err := f.errs[accountID]
	delay := f.delay
	f.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
	if err != nil {
		return model.Account{}, err
	}
	acct, ok := f.store.Account(accountID)
	if !ok {
		return model.Account{}, errors.New("fake login: account disappeared")
	}
	acct.State = model.AccountActive
	return acct, nil
}

func (f *fakeLoginCall) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeLoginCall) countFor(accountID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, id := range f.calls {
		if id == accountID {
			n++
		}
	}
	return n
}

// fakeEventSink records the account.login events the runner publishes.
type fakeEventSink struct {
	mu     sync.Mutex
	events []recordedLoginEvent
}

type recordedLoginEvent struct {
	Type webhook.EventType
	Data map[string]any
}

func (f *fakeEventSink) Emit(event webhook.EventType, data map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, recordedLoginEvent{Type: event, Data: data})
}

func (f *fakeEventSink) snapshot() []recordedLoginEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]recordedLoginEvent, len(f.events))
	copy(out, f.events)
	return out
}

func TestRecoverOnStartupDemotesUngatedNodes(t *testing.T) {
	st := store.New()
	var demotable []string
	for _, node := range st.Nodes() {
		if node.State == model.NodeLive || node.State == model.NodeDraining {
			demotable = append(demotable, node.ID)
		}
	}
	if len(demotable) == 0 {
		t.Fatal("seeded store has no LIVE or DRAINING node")
	}
	wantLeases := 0
	for _, lease := range st.Leases() {
		for _, id := range demotable {
			if lease.NodeID == id {
				wantLeases++
			}
		}
	}
	leasesBefore := len(st.Leases())
	auditsBefore := len(st.Audit(store.AuditRetention))

	result, err := runtime.RecoverOnStartup(st, nil, discardLogger())
	if err != nil {
		t.Fatalf("RecoverOnStartup: %v", err)
	}
	if len(result.Demoted) != len(demotable) {
		t.Fatalf("demoted = %v, want the %d LIVE/DRAINING nodes", result.Demoted, len(demotable))
	}
	if len(result.Kept) != 0 {
		t.Fatalf("kept = %v, want none without a gate", result.Kept)
	}
	if result.Leases != wantLeases {
		t.Fatalf("leases closed = %d, want %d", result.Leases, wantLeases)
	}

	for _, id := range demotable {
		node, ok := st.Node(id)
		if !ok {
			t.Fatalf("node %s disappeared", id)
		}
		if node.State != model.NodeStale || node.GatePort != 0 || node.EgressIP != "" || node.InUse != 0 {
			t.Fatalf("node %s = %+v, want STALE with no gate, egress or flows", id, node)
		}
	}
	// A READY or DOWN row never claimed a gate, so recovery leaves it alone.
	for _, node := range st.Nodes() {
		switch node.ID {
		case "node-03", "node-11":
			if node.State != model.NodeReady {
				t.Fatalf("node %s state = %q, want READY untouched", node.ID, node.State)
			}
		case "node-12":
			if node.State != model.NodeDown {
				t.Fatalf("node %s state = %q, want DOWN untouched", node.ID, node.State)
			}
		}
	}
	if got := len(st.Leases()); got != leasesBefore-wantLeases {
		t.Fatalf("leases after recovery = %d, want %d", got, leasesBefore-wantLeases)
	}
	for _, account := range st.Accounts() {
		if account.LiveNodeCount != 0 {
			t.Fatalf("account %s liveNodeCount = %d, want 0", account.ID, account.LiveNodeCount)
		}
	}

	events := st.Audit(store.AuditRetention)
	if got := len(events) - auditsBefore; got != len(demotable) {
		t.Fatalf("recovery audit rows = %d, want %d (one per node)", got, len(demotable))
	}
	labels := make(map[string]struct{}, len(demotable))
	for _, id := range demotable {
		node, _ := st.Node(id)
		labels[node.Label] = struct{}{}
	}
	for _, event := range events[:len(demotable)] {
		if event.Action != "node.recovered_stale" || event.Actor != "system" || event.Severity != model.SeverityWarn {
			t.Fatalf("audit = %+v, want node.recovered_stale by system at warn", event)
		}
		if _, ok := labels[event.Target]; !ok {
			t.Fatalf("audit target = %q, want a demoted node label", event.Target)
		}
		delete(labels, event.Target)
		previous := event.Meta["previousState"]
		if previous != string(model.NodeLive) && previous != string(model.NodeDraining) {
			t.Fatalf("audit meta previousState = %q", previous)
		}
		if _, err := strconv.Atoi(event.Meta["leasesClosed"]); err != nil {
			t.Fatalf("audit meta leasesClosed = %q: %v", event.Meta["leasesClosed"], err)
		}
	}
	if len(labels) != 0 {
		t.Fatalf("nodes without an audit row: %v", labels)
	}
}

func TestRecoverOnStartupIsIdempotent(t *testing.T) {
	st := store.New()
	if _, err := runtime.RecoverOnStartup(st, nil, discardLogger()); err != nil {
		t.Fatalf("first recovery: %v", err)
	}
	audits := len(st.Audit(store.AuditRetention))
	leases := len(st.Leases())

	second, err := runtime.RecoverOnStartup(st, nil, discardLogger())
	if err != nil {
		t.Fatalf("second recovery: %v", err)
	}
	if len(second.Demoted) != 0 || len(second.Kept) != 0 || second.Leases != 0 {
		t.Fatalf("second recovery = %+v, want a no-op", second)
	}
	if got := len(st.Audit(store.AuditRetention)); got != audits {
		t.Fatalf("audit rows after the second boot = %d, want %d", got, audits)
	}
	if got := len(st.Leases()); got != leases {
		t.Fatalf("leases after the second boot = %d, want %d", got, leases)
	}
	for _, node := range st.Nodes() {
		if node.State != model.NodeStale && (node.State == model.NodeLive || node.State == model.NodeDraining) {
			t.Fatalf("node %s = %q after the second boot, want STALE", node.ID, node.State)
		}
	}
}

func TestRecoverOnStartupKeepsGatedNode(t *testing.T) {
	st := newNodeTestStore(t)
	acct := newNodeTestAccount(t, st, "recovery-gated")
	logins := newFakeLoginResults()
	logins.set(acct.ID, account.LoginResult{
		AuthResult:      "OK",
		UserName:        "vendor-user",
		ConnectPassword: account.Secret(testConnectPassword),
		Servers:         map[string]apifacade.Server{"s1": testServer("s1")},
	})
	controller, err := runtime.NewNodeController(runtime.NodeControllerConfig{
		Store:    st,
		Logins:   logins,
		Sessions: newFakeSessionOpener(),
		Gates: func(gate.Config, gate.Session) (runtime.Gate, error) {
			return &fakeGate{}, nil
		},
		Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("NewNodeController: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = controller.Stop(stopCtx)
	})
	if err := controller.SyncAccount(context.Background(), acct.ID); err != nil {
		t.Fatalf("SyncAccount: %v", err)
	}
	node := nodeByServer(t, st, acct.ID, "s1")
	if node.State != model.NodeLive || node.GatePort == 0 {
		t.Fatalf("synced node = %+v, want LIVE with a gate port", node)
	}

	result, err := runtime.RecoverOnStartup(st, controller, discardLogger())
	if err != nil {
		t.Fatalf("RecoverOnStartup: %v", err)
	}
	if len(result.Demoted) != 0 {
		t.Fatalf("demoted = %v, want none while the gate runs", result.Demoted)
	}
	if len(result.Kept) != 1 || result.Kept[0] != node.ID {
		t.Fatalf("kept = %v, want [%s]", result.Kept, node.ID)
	}
	kept, ok := st.Node(node.ID)
	if !ok || kept.State != model.NodeLive || kept.GatePort != node.GatePort || kept.EgressIP != node.EgressIP {
		t.Fatalf("gated node = %+v, want LIVE with its gate port and egress", kept)
	}
	for _, event := range st.Audit(store.AuditRetention) {
		if event.Action == "node.recovered_stale" {
			t.Fatalf("gated node wrote a recovery audit: %+v", event)
		}
	}
}

func TestRecoverOnStartupStaleInNodesView(t *testing.T) {
	st := store.New()
	if _, err := runtime.RecoverOnStartup(st, nil, discardLogger()); err != nil {
		t.Fatalf("RecoverOnStartup: %v", err)
	}
	ts := httptest.NewServer(api.New(api.Config{AllowInsecure: true}, st, discardLogger()))
	t.Cleanup(ts.Close)

	resp, err := ts.Client().Get(ts.URL + "/v1/nodes")
	if err != nil {
		t.Fatalf("GET /v1/nodes: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/nodes status = %d (body %s)", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), `"state":"STALE"`) {
		t.Fatalf("nodes view does not carry STALE: %s", body)
	}
	var nodes []model.ProxyNode
	if err := json.Unmarshal(body, &nodes); err != nil {
		t.Fatalf("decode nodes: %v", err)
	}
	var stale, live int
	for _, node := range nodes {
		switch node.State {
		case model.NodeLive, model.NodeDraining:
			live++
		case model.NodeStale:
			stale++
		}
	}
	if live != 0 {
		t.Fatalf("nodes view still claims %d LIVE/DRAINING nodes", live)
	}
	if stale == 0 {
		t.Fatal("nodes view has no STALE node after recovery")
	}
	for _, node := range nodes {
		if node.ID != "node-01" {
			continue
		}
		if node.State != model.NodeStale || node.GatePort != 0 || node.EgressIP != "" {
			t.Fatalf("node-01 in the nodes view = %+v, want STALE without a gate or egress", node)
		}
		return
	}
	t.Fatal("node-01 is missing from the nodes view")
}

// TestDemoteNodeGuards pins the store helper contract recovery relies on: a
// row that never claimed a gate is untouched, and an unknown id is ErrNotFound.
func TestDemoteNodeGuards(t *testing.T) {
	st := store.New()
	audits := len(st.Audit(store.AuditRetention))
	node, changed, err := st.DemoteNode("node-03") // READY
	if err != nil || changed || node.State != model.NodeReady {
		t.Fatalf("DemoteNode(READY) = %+v/%v/%v, want an untouched READY node", node, changed, err)
	}
	if got := len(st.Audit(store.AuditRetention)); got != audits {
		t.Fatalf("a READY demote wrote %d audit rows", got-audits)
	}
	if _, _, err := st.DemoteNode("node-99"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("DemoteNode(unknown) error = %v, want ErrNotFound", err)
	}
}

func TestAutoLoginLogsInOncePerAccount(t *testing.T) {
	st := newAutoLoginStore(t)
	acct := newAutoLoginAccount(t, st, "auto-login")
	putAutoLoginNode(t, st, acct, "auto-login-one")

	logins := newFakeLoginCall(st)
	events := &fakeEventSink{}
	var synced []string
	runner, err := runtime.NewAutoLogin(runtime.AutoLoginConfig{
		Store:   st,
		Logins:  logins,
		Events:  events,
		Logger:  discardLogger(),
		Backoff: -1,
		Sync: func(_ context.Context, accountID string) error {
			synced = append(synced, accountID)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("NewAutoLogin: %v", err)
	}

	result := runner.Run(context.Background())
	if len(result.Attempted) != 1 || result.Attempted[0] != acct.ID {
		t.Fatalf("attempted = %v, want [%s]", result.Attempted, acct.ID)
	}
	if len(result.LoggedIn) != 1 || result.LoggedIn[0] != acct.ID || len(result.Failed) != 0 {
		t.Fatalf("result = %+v, want one successful login", result)
	}
	if got := logins.count(); got != 1 {
		t.Fatalf("Login calls = %d, want 1", got)
	}
	if len(synced) != 1 || synced[0] != acct.ID {
		t.Fatalf("sync calls = %v, want [%s]", synced, acct.ID)
	}
	published := events.snapshot()
	if len(published) != 1 || published[0].Type != webhook.EventAccountLogin {
		t.Fatalf("events = %+v, want one account.login", published)
	}
	if published[0].Data["accountId"] != acct.ID || published[0].Data["label"] != acct.Label {
		t.Fatalf("event data = %v, want the account identity", published[0].Data)
	}

	// A second pass in the same process never repeats the attempt.
	again := runner.Run(context.Background())
	if len(again.Attempted) != 0 || logins.count() != 1 || len(events.snapshot()) != 1 {
		t.Fatalf("second pass = %+v with %d logins and %d events, want a no-op", again, logins.count(), len(events.snapshot()))
	}
}

func TestAutoLoginSingleAttemptOnFailure(t *testing.T) {
	st := newAutoLoginStore(t)
	first := newAutoLoginAccount(t, st, "auto-login-first")
	putAutoLoginNode(t, st, first, "auto-login-first-one")
	second := newAutoLoginAccount(t, st, "auto-login-second")
	putAutoLoginNode(t, st, second, "auto-login-second-one")
	// insertAccountLocked prepends, so the store order is second, first.
	accounts := st.Accounts()
	if len(accounts) != 2 {
		t.Fatalf("accounts = %d, want 2", len(accounts))
	}
	failing, other := accounts[0], accounts[1]

	logins := newFakeLoginCall(st)
	logins.errs[failing.ID] = errors.New("vendor rejected the login")
	var sleeps []time.Duration
	runner, err := runtime.NewAutoLogin(runtime.AutoLoginConfig{
		Store:  st,
		Logins: logins,
		Logger: discardLogger(),
		Sleep: func(_ context.Context, d time.Duration) error {
			sleeps = append(sleeps, d)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("NewAutoLogin: %v", err)
	}
	if runtime.DefaultAutoLoginBackoff != 60*time.Second {
		t.Fatalf("DefaultAutoLoginBackoff = %s, want 60s", runtime.DefaultAutoLoginBackoff)
	}

	result := runner.Run(context.Background())
	if len(result.Attempted) != 2 || result.Attempted[0] != failing.ID || result.Attempted[1] != other.ID {
		t.Fatalf("attempted = %v, want [%s %s]", result.Attempted, failing.ID, other.ID)
	}
	if len(result.Failed) != 1 || result.Failed[0] != failing.ID {
		t.Fatalf("failed = %v, want [%s]", result.Failed, failing.ID)
	}
	if len(result.LoggedIn) != 1 || result.LoggedIn[0] != other.ID {
		t.Fatalf("logged in = %v, want [%s]", result.LoggedIn, other.ID)
	}
	if got := logins.countFor(failing.ID); got != 1 {
		t.Fatalf("Login calls for the failing account = %d, want exactly 1", got)
	}
	if len(sleeps) != 1 {
		t.Fatalf("backoff sleeps = %v, want exactly one between the two accounts", sleeps)
	}
	if sleeps[0] < 48*time.Second || sleeps[0] >= 72*time.Second {
		t.Fatalf("backoff = %s, want ±20%% around 60s", sleeps[0])
	}

	// The failed account is never retried in this process.
	again := runner.Run(context.Background())
	if len(again.Attempted) != 0 {
		t.Fatalf("second pass attempted = %v, want none", again.Attempted)
	}
	if got := logins.countFor(failing.ID); got != 1 {
		t.Fatalf("Login calls after the second pass = %d, want 1", got)
	}
}

func TestAutoLoginSkipsIneligibleAccounts(t *testing.T) {
	st := newAutoLoginStore(t)
	paused := newAutoLoginAccount(t, st, "auto-login-paused")
	putAutoLoginNode(t, st, paused, "auto-login-paused-one")
	pausedState := model.AccountPaused
	if _, err := st.UpdateAccount(paused.ID, model.AccountPatch{State: &pausedState}); err != nil {
		t.Fatalf("pause account: %v", err)
	}
	expired := newAutoLoginAccount(t, st, "auto-login-expired")
	putAutoLoginNode(t, st, expired, "auto-login-expired-one")
	expiredState := model.AccountExpired
	if _, err := st.UpdateAccount(expired.ID, model.AccountPatch{State: &expiredState}); err != nil {
		t.Fatalf("expire account: %v", err)
	}
	// A sealed password with neither a node nor a selection.
	newAutoLoginAccount(t, st, "auto-login-idle")

	logins := newFakeLoginCall(st)
	runner, err := runtime.NewAutoLogin(runtime.AutoLoginConfig{
		Store:   st,
		Logins:  logins,
		Logger:  discardLogger(),
		Backoff: -1,
	})
	if err != nil {
		t.Fatalf("NewAutoLogin: %v", err)
	}
	result := runner.Run(context.Background())
	if len(result.Attempted) != 0 || logins.count() != 0 {
		t.Fatalf("attempted = %v with %d Login calls, want none", result.Attempted, logins.count())
	}
}

func TestAutoLoginRequiresSealedPassword(t *testing.T) {
	st := store.New() // no sealer: CreateAccount discards the password
	if err := st.Restore([]byte(`{"version":1}`)); err != nil {
		t.Fatalf("Restore empty snapshot: %v", err)
	}
	acct := newAutoLoginAccount(t, st, "auto-login-unsealed")
	putAutoLoginNode(t, st, acct, "auto-login-unsealed-one")

	logins := newFakeLoginCall(st)
	runner, err := runtime.NewAutoLogin(runtime.AutoLoginConfig{
		Store:   st,
		Logins:  logins,
		Logger:  discardLogger(),
		Backoff: -1,
	})
	if err != nil {
		t.Fatalf("NewAutoLogin: %v", err)
	}
	if result := runner.Run(context.Background()); len(result.Attempted) != 0 || logins.count() != 0 {
		t.Fatalf("attempted = %v with %d Login calls, want none without a sealed password", result.Attempted, logins.count())
	}
}

func TestAutoLoginUsesSelectionWithoutNode(t *testing.T) {
	st := newAutoLoginStore(t)
	acct := newAutoLoginAccount(t, st, "auto-login-selection")
	if _, err := st.SetSelection(acct.ID, "srv-a", "operator"); err != nil {
		t.Fatalf("SetSelection: %v", err)
	}
	logins := newFakeLoginCall(st)
	runner, err := runtime.NewAutoLogin(runtime.AutoLoginConfig{
		Store:   st,
		Logins:  logins,
		Logger:  discardLogger(),
		Backoff: -1,
	})
	if err != nil {
		t.Fatalf("NewAutoLogin: %v", err)
	}
	result := runner.Run(context.Background())
	if len(result.LoggedIn) != 1 || result.LoggedIn[0] != acct.ID {
		t.Fatalf("result = %+v, want the selection-only account logged in", result)
	}
}

func TestAutoLoginSerializesConcurrentRuns(t *testing.T) {
	st := newAutoLoginStore(t)
	acct := newAutoLoginAccount(t, st, "auto-login-race")
	putAutoLoginNode(t, st, acct, "auto-login-race-one")

	logins := newFakeLoginCall(st)
	logins.delay = 50 * time.Millisecond
	runner, err := runtime.NewAutoLogin(runtime.AutoLoginConfig{
		Store:   st,
		Logins:  logins,
		Logger:  discardLogger(),
		Backoff: -1,
	})
	if err != nil {
		t.Fatalf("NewAutoLogin: %v", err)
	}

	results := make([]runtime.AutoLoginResult, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i] = runner.Run(context.Background())
		}(i)
	}
	close(start)
	wg.Wait()

	if got := logins.count(); got != 1 {
		t.Fatalf("Login calls = %d, want exactly 1 across concurrent runs", got)
	}
	if total := len(results[0].Attempted) + len(results[1].Attempted); total != 1 {
		t.Fatalf("attempted across both runs = %d, want 1", total)
	}
}

// TestUndrainStaleNodeDoesNotClaimLive pins the follow-up honesty rule: a
// demoted row has no gate, so the operator's "activate" (undrain) must not
// turn it LIVE. Only a login can reopen the gate.
func TestUndrainStaleNodeDoesNotClaimLive(t *testing.T) {
	st := store.New()
	if _, err := runtime.RecoverOnStartup(st, nil, discardLogger()); err != nil {
		t.Fatalf("RecoverOnStartup: %v", err)
	}
	node, err := st.UndrainNode("node-01")
	if err != nil {
		t.Fatalf("UndrainNode: %v", err)
	}
	if node.State != model.NodeReady || node.GatePort != 0 || node.EgressIP != "" {
		t.Fatalf("undrained stale node = %+v, want READY without a gate or egress", node)
	}
}
