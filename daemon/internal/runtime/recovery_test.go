package runtime_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
		ID:        slot1NodeID(acct.ID, "s1"),
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
	if result.Leases != wantLeases || len(result.Revoked) != wantLeases {
		t.Fatalf("leases revoked = %d/%d, want %d", result.Leases, len(result.Revoked), wantLeases)
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
	if got := len(events) - auditsBefore; got != len(demotable)+wantLeases {
		t.Fatalf("recovery audit rows = %d, want %d (one per node plus one per revoked lease)",
			got, len(demotable)+wantLeases)
	}
	labels := make(map[string]struct{}, len(demotable))
	for _, id := range demotable {
		node, _ := st.Node(id)
		labels[node.Label] = struct{}{}
	}
	nodeRows, leaseRows := 0, 0
	for _, event := range events {
		switch event.Action {
		case "node.recovered_stale":
			nodeRows++
			if event.Actor != "system" || event.Severity != model.SeverityWarn {
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
		case "lease.revoke":
			leaseRows++
			if event.Meta["reason"] != "daemon_restart" {
				t.Fatalf("lease.revoke meta = %v, want reason daemon_restart", event.Meta)
			}
		}
	}
	if len(labels) != 0 {
		t.Fatalf("nodes without an audit row: %v", labels)
	}
	if nodeRows != len(demotable) {
		t.Fatalf("node.recovered_stale rows = %d, want %d", nodeRows, len(demotable))
	}
	if leaseRows != wantLeases {
		t.Fatalf("lease.revoke rows = %d, want %d", leaseRows, wantLeases)
	}
}

// TestRecoverOnStartupRacesLeaseCreate pins the store/runtime boundary: a
// lease created in the window between the node read and the demotion is either
// refused (node already STALE) or revoked by the same DemoteNode lock, never
// left active on a gate-less node. The recovery count must match the rows it
// actually returned, not a pre-read.
func TestRecoverOnStartupRacesLeaseCreate(t *testing.T) {
	st := store.NewEmpty()
	account, err := st.CreateAccount(model.CreateAccountInput{
		Label: "Race Lease", Username: "race-lease",
		Password: "local-placeholder", Platform: model.PlatformAndroid, Concurrency: 1,
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if _, err := st.PutNode(model.ProxyNode{
		ID: "node-race-lease", AccountID: account.ID, Label: "cn-gz-race",
		Region: "cn-gz", State: model.NodeLive, EgressIP: "203.0.113.34",
		Protocol: model.ProtocolSOCKS5, GatePort: 13034,
	}); err != nil {
		t.Fatalf("PutNode: %v", err)
	}
	user, err := st.CreateUser(model.CreateUserInput{Name: "race.lease"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _, _ = st.CreateLease(model.CreateLeaseInput{
			UserID: user.ID, Platform: model.PlatformAndroid,
		})
	}()
	var result runtime.RecoveryResult
	go func() {
		defer wg.Done()
		result, _ = runtime.RecoverOnStartup(st, nil, discardLogger())
	}()
	wg.Wait()

	if result.Leases != len(result.Revoked) {
		t.Fatalf("recovery count = %d, revoked rows = %d", result.Leases, len(result.Revoked))
	}
	if leases := st.Leases(); len(leases) != 0 {
		t.Fatalf("active leases after the race = %+v, want none", leases)
	}
	for _, end := range st.LeaseEnds() {
		if end.Reason != "daemon_restart" {
			t.Fatalf("lease end reason = %q, want daemon_restart", end.Reason)
		}
	}
}

// TestDaemonRestartPersistsLeaseOutcome walks the release-rehearsal shape
// end to end: a saved snapshot with one LIVE node and one active lease is
// loaded, startup recovery runs, and the next snapshot still names the lease
// in leaseEnds with reason daemon_restart while the active collection is empty.
func TestDaemonRestartPersistsLeaseOutcome(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	st := store.NewEmpty()
	account, err := st.CreateAccount(model.CreateAccountInput{
		Label: "Restart Durable", Username: "restart-durable",
		Password: "local-placeholder", Platform: model.PlatformAndroid, Concurrency: 1,
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	node, err := st.PutNode(model.ProxyNode{
		ID: "node-restart-durable", AccountID: account.ID, Label: "cn-sz-restart",
		Region: "cn-sz", State: model.NodeLive, EgressIP: "203.0.113.33",
		Protocol: model.ProtocolSOCKS5, GatePort: 13033,
	})
	if err != nil {
		t.Fatalf("PutNode: %v", err)
	}
	user, err := st.CreateUser(model.CreateUserInput{Name: "restart.durable"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	lease, _, err := st.CreateLease(model.CreateLeaseInput{
		UserID: user.ID, Platform: model.PlatformAndroid,
	})
	if err != nil {
		t.Fatalf("CreateLease: %v", err)
	}
	if lease.NodeID != node.ID {
		t.Fatalf("lease node = %s, want %s", lease.NodeID, node.ID)
	}
	if err := st.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// The new process loads the file and owns no gate yet.
	restarted := store.NewEmpty()
	if err := restarted.Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := restarted.Leases(); len(got) != 1 || got[0].ID != lease.ID {
		t.Fatalf("loaded leases = %+v, want the active %s", got, lease.ID)
	}
	if _, err := runtime.RecoverOnStartup(restarted, nil, discardLogger()); err != nil {
		t.Fatalf("RecoverOnStartup: %v", err)
	}
	if err := restarted.Save(path); err != nil {
		t.Fatalf("Save after recovery: %v", err)
	}

	// A second load proves the outcome is in the document, not just memory.
	reloaded := store.NewEmpty()
	if err := reloaded.Load(path); err != nil {
		t.Fatalf("Load after recovery: %v", err)
	}
	if got := reloaded.Leases(); len(got) != 0 {
		t.Fatalf("active leases after restart = %+v, want none", got)
	}
	ends := reloaded.LeaseEnds()
	if len(ends) != 1 || ends[0].Lease.ID != lease.ID || ends[0].Reason != "daemon_restart" ||
		ends[0].Lease.CreatedAt != lease.CreatedAt {
		t.Fatalf("leaseEnds after restart = %+v, want the original %s with daemon_restart", ends, lease.ID)
	}
	var audit model.AuditEvent
	for _, event := range reloaded.Audit(store.AuditRetention) {
		if event.Action == "lease.revoke" && event.Meta["leaseId"] == lease.ID {
			audit = event
		}
	}
	if audit.Action == "" || audit.Meta["reason"] != "daemon_restart" {
		t.Fatalf("audit after restart = %+v, want lease.revoke daemon_restart", audit)
	}
}

// TestRecoverOnStartupWritesPerLeaseRevokeAudit is the AJS-390 falsifier: on
// the old code DemoteNode filtered the lease row out and wrote only a
// leasesClosed count on the node.recovered_stale row, so the test fails with
// no per-lease audit. The new path must write one lease.revoke row with
// reason daemon_restart for every revoked entitlement.
func TestRecoverOnStartupWritesPerLeaseRevokeAudit(t *testing.T) {
	st := store.NewEmpty()
	account, err := st.CreateAccount(model.CreateAccountInput{
		Label: "Restart Lease", Username: "restart-lease",
		Password: "local-placeholder", Platform: model.PlatformAndroid, Concurrency: 1,
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	node, err := st.PutNode(model.ProxyNode{
		ID: "node-restart-lease", AccountID: account.ID, Label: "cn-sh-restart",
		Region: "cn-sh", State: model.NodeLive, EgressIP: "203.0.113.31",
		Protocol: model.ProtocolSOCKS5, GatePort: 13031,
	})
	if err != nil {
		t.Fatalf("PutNode: %v", err)
	}
	user, err := st.CreateUser(model.CreateUserInput{Name: "restart.customer"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	lease, _, err := st.CreateLease(model.CreateLeaseInput{
		UserID: user.ID, Platform: model.PlatformAndroid,
	})
	if err != nil {
		t.Fatalf("CreateLease: %v", err)
	}
	if lease.NodeID != node.ID {
		t.Fatalf("lease node = %s, want %s", lease.NodeID, node.ID)
	}

	if _, err := runtime.RecoverOnStartup(st, nil, discardLogger()); err != nil {
		t.Fatalf("RecoverOnStartup: %v", err)
	}

	var revoked model.AuditEvent
	for _, event := range st.Audit(store.AuditRetention) {
		if event.Action == "lease.revoke" && event.Meta["leaseId"] == lease.ID {
			revoked = event
		}
	}
	if revoked.Action == "" {
		t.Fatal("restart wrote no per-lease lease.revoke audit row")
	}
	if revoked.Meta["reason"] != "daemon_restart" || revoked.Meta["userId"] != user.ID ||
		revoked.Meta["nodeId"] != node.ID || revoked.Meta["slot"] != "0" {
		t.Fatalf("lease.revoke meta = %v, want daemon_restart/user/node/slot", revoked.Meta)
	}
	if got := st.Leases(); len(got) != 0 {
		t.Fatalf("leases after restart = %+v, want none", got)
	}

	// The same row is visible through the admin audit API, so an operator can
	// see why the lease ended without reading the state file.
	ts := httptest.NewServer(api.New(api.Config{AllowInsecure: true}, st, discardLogger()))
	t.Cleanup(ts.Close)
	resp, err := ts.Client().Get(ts.URL + "/v1/audit?limit=50")
	if err != nil {
		t.Fatalf("GET /v1/audit: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read audit body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/audit status = %d (body %s)", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), `"action":"lease.revoke"`) ||
		!strings.Contains(string(body), `"reason":"daemon_restart"`) {
		t.Fatalf("audit API does not show the restart revocation: %s", body)
	}
}

// TestRecoverOnStartupRecordsLeaseEndAndEvent proves the durable outcome and
// the event side of the same transition: the leaseEnds snapshot row keeps the
// lease id, user and reason, and the store's revoke hook fires once with the
// row (the daemon maps that hook onto lease.revoked).
func TestRecoverOnStartupRecordsLeaseEndAndEvent(t *testing.T) {
	st := store.NewEmpty()
	account, err := st.CreateAccount(model.CreateAccountInput{
		Label: "Restart Outcome", Username: "restart-outcome",
		Password: "local-placeholder", Platform: model.PlatformLinux, Concurrency: 1,
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	node, err := st.PutNode(model.ProxyNode{
		ID: "node-restart-outcome", AccountID: account.ID, Label: "cn-hk-restart",
		Region: "cn-hk", State: model.NodeLive, EgressIP: "203.0.113.32",
		Protocol: model.ProtocolSOCKS5, GatePort: 13032,
	})
	if err != nil {
		t.Fatalf("PutNode: %v", err)
	}
	user, err := st.CreateUser(model.CreateUserInput{Name: "restart.outcome"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	lease, _, err := st.CreateLease(model.CreateLeaseInput{
		UserID: user.ID, Platform: model.PlatformLinux,
	})
	if err != nil {
		t.Fatalf("CreateLease: %v", err)
	}
	if lease.NodeID != node.ID {
		t.Fatalf("lease node = %s, want %s", lease.NodeID, node.ID)
	}

	var events []model.Lease
	st.SetLeaseRevokeHook(func(got model.Lease) { events = append(events, got) })
	result, err := runtime.RecoverOnStartup(st, nil, discardLogger())
	if err != nil {
		t.Fatalf("RecoverOnStartup: %v", err)
	}
	if result.Leases != 1 || len(result.Revoked) != 1 || result.Revoked[0].ID != lease.ID {
		t.Fatalf("recovery result = %+v, want one revoked %s", result, lease.ID)
	}
	if len(events) != 1 || events[0].ID != lease.ID || events[0].Status != model.LeaseRevoked {
		t.Fatalf("revoke events = %+v, want one revoked %s", events, lease.ID)
	}
	ends := st.LeaseEnds()
	if len(ends) != 1 {
		t.Fatalf("lease ends = %+v, want one row", ends)
	}
	if ends[0].Lease.ID != lease.ID || ends[0].Lease.UserID != user.ID ||
		ends[0].Lease.Status != model.LeaseRevoked || ends[0].Reason != "daemon_restart" ||
		ends[0].EndedAt == "" {
		t.Fatalf("lease end = %+v, want the daemon_restart outcome for %s", ends[0], lease.ID)
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
	node, revoked, changed, err := st.DemoteNode("node-03") // READY
	if err != nil || changed || node.State != model.NodeReady || len(revoked) != 0 {
		t.Fatalf("DemoteNode(READY) = %+v/%v/%v/%v, want an untouched READY node", node, revoked, changed, err)
	}
	if got := len(st.Audit(store.AuditRetention)); got != audits {
		t.Fatalf("a READY demote wrote %d audit rows", got-audits)
	}
	if _, _, _, err := st.DemoteNode("node-99"); !errors.Is(err, store.ErrNotFound) {
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

// newRecoveryOrder creates one active tenant linked to a fresh enabled user and
// returns the order it owns. The runtime package cannot reach the store's
// internal fixtures, so this builds the same state through the public API.
func newRecoveryOrder(t *testing.T, st *store.Store, key string, ttlSeconds int) model.Order {
	t.Helper()
	plan, err := st.CreatePlan(model.PlanInput{
		Name: "Recovery " + key, AmountMinor: 3000, Currency: "CNY",
		LeaseTTLSeconds: ttlSeconds, QuotaBytes: 10 << 30,
	})
	if err != nil {
		t.Fatalf("CreatePlan(%s): %v", key, err)
	}
	user, err := st.CreateUser(model.CreateUserInput{Name: "recovery-" + key, Platform: "android"})
	if err != nil {
		t.Fatalf("CreateUser(%s): %v", key, err)
	}
	email := key + "@recovery.example.com"
	if err := st.SignupTenant(model.TenantSignupInput{
		Email: email, Password: "recovery-password", DisplayName: "Recovery " + key,
	}); err != nil {
		t.Fatalf("SignupTenant(%s): %v", key, err)
	}
	var tenant model.Tenant
	for _, candidate := range st.Tenants() {
		if candidate.Email == email {
			tenant = candidate
			break
		}
	}
	if tenant.ID == "" {
		t.Fatalf("SignupTenant(%s) stored no tenant", key)
	}
	if _, err := st.ApproveTenant(tenant.ID, user.ID, nil); err != nil {
		t.Fatalf("ApproveTenant(%s): %v", key, err)
	}
	order, replayed, err := st.CreateOrder(tenant.ID, user.ID, plan.ID, key, "test")
	if err != nil || replayed {
		t.Fatalf("CreateOrder(%s) = %+v replayed=%t err=%v", key, order, replayed, err)
	}
	return order
}

// settleRecoveryOrder applies one offline settlement fact, the same path the
// admin attestation uses.
func settleRecoveryOrder(t *testing.T, st *store.Store, order model.Order, key string) model.Order {
	t.Helper()
	settled, err := st.ApplyPaymentEvent(model.ProviderManual, model.PaymentEvent{
		Provider: model.ProviderManual, ID: "evt-" + key, Kind: model.PaymentSettled,
		OrderID: order.ID, ProviderRef: "receipt-" + key,
	}, "test")
	if err != nil {
		t.Fatalf("ApplyPaymentEvent(%s): %v", key, err)
	}
	return settled
}

// expireOrderPaidTerm rewrites one order's paidAt in a snapshot and restores it
// into a fresh store, so a paid term can be ended without waiting out the
// plan's minimum TTL.
func expireOrderPaidTerm(t *testing.T, st *store.Store, orderID string, paidAt time.Time) *store.Store {
	t.Helper()
	data, err := st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	orders, ok := doc["orders"].([]any)
	if !ok {
		t.Fatalf("snapshot carries no orders")
	}
	found := false
	for _, raw := range orders {
		record, ok := raw.(map[string]any)
		if !ok || record["id"] != orderID {
			continue
		}
		record["paidAt"] = paidAt.UTC().Format(time.RFC3339Nano)
		found = true
	}
	if !found {
		t.Fatalf("snapshot has no order %s", orderID)
	}
	edited, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("encode snapshot: %v", err)
	}
	restored := store.New()
	if err := restored.Restore(edited); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	return restored
}

// recoveryQuota reads one user's monthly quota. Recording money must not move
// it; only a committed entitlement may.
func recoveryQuota(t *testing.T, st *store.Store, userID string) *int64 {
	t.Helper()
	user, ok := st.User(userID)
	if !ok {
		t.Fatalf("user %s missing", userID)
	}
	return user.QuotaBytesMonth
}

// TestRecoverPaidOrdersQueuesInitialFulfilmentOnce pins the boot scan: a paid
// order with no grant and no work receives exactly one initial fulfilment job,
// an existing job is never duplicated, a review-required order is an operator
// decision, and a paid term that already ended is dead-lettered instead of
// activated. Active and terminal orders are not recovery work either.
func TestRecoverPaidOrdersQueuesInitialFulfilmentOnce(t *testing.T) {
	st := store.New()
	logger := discardLogger()

	// The demo fixture holds the account's only slot; release it so the active
	// case below can actually activate.
	for _, lease := range st.Leases() {
		if err := st.ReleaseLease(lease.ID); err != nil {
			t.Fatalf("ReleaseLease(%s): %v", lease.ID, err)
		}
	}

	// A paid order with no work at all: the scan must queue it once.
	paidOrder := newRecoveryOrder(t, st, "boot-paid", 3600)
	if _, err := st.SetUserEnabled(paidOrder.UserID, false); err != nil {
		t.Fatalf("SetUserEnabled: %v", err)
	}
	paid := settleRecoveryOrder(t, st, paidOrder, "boot-paid")
	if paid.State != model.OrderPaid || paid.Grant != nil || paid.Fulfilment != nil {
		t.Fatalf("paid fixture = %+v, want paid without a grant or job", paid)
	}

	// A paid order that already owns a queued job: the scan must not duplicate
	// it or rewrite its schedule.
	existingOrder := newRecoveryOrder(t, st, "boot-existing", 3600)
	if _, err := st.SetUserEnabled(existingOrder.UserID, false); err != nil {
		t.Fatalf("SetUserEnabled(existing): %v", err)
	}
	existingPaid := settleRecoveryOrder(t, st, existingOrder, "boot-existing")
	if existingPaid.State != model.OrderPaid || existingPaid.Grant != nil {
		t.Fatalf("existing fixture = %+v, want paid without a grant", existingPaid)
	}
	if _, err := st.EnqueueFulfilment(existingOrder.ID, model.FulfilmentInitial); err != nil {
		t.Fatalf("EnqueueFulfilment(existing): %v", err)
	}
	existingBefore, _ := st.Order(existingOrder.ID)
	if existingBefore.Fulfilment == nil {
		t.Fatalf("existing fixture has no job")
	}
	existingUpdated := existingBefore.Fulfilment.UpdatedAt

	// A paid order quarantined for review: the money is recorded but
	// fulfilment is an operator decision, so the boot scan must skip it.
	reviewOrder := newRecoveryOrder(t, st, "boot-review", 3600)
	if _, err := st.MarkOrderCheckout(reviewOrder.TenantID, reviewOrder.ID, model.ProviderEpay); err != nil {
		t.Fatalf("MarkOrderCheckout(review): %v", err)
	}
	if _, err := st.SetUserEnabled(reviewOrder.UserID, false); err != nil {
		t.Fatalf("SetUserEnabled(review): %v", err)
	}
	for _, eventID := range []string{"evt-boot-review", "evt-boot-review-conflict"} {
		if _, err := st.ApplyPaymentEvent(model.ProviderEpay, model.PaymentEvent{
			Provider: model.ProviderEpay, ID: eventID, Kind: model.PaymentSettled,
			OrderID: reviewOrder.ID, ProviderRef: eventID,
			AmountMinor: 3000, Currency: "CNY",
		}, "test"); err != nil {
			t.Fatalf("ApplyPaymentEvent(review/%s): %v", eventID, err)
		}
	}
	reviewed, _ := st.Order(reviewOrder.ID)
	if reviewed.State != model.OrderPaid || !reviewed.ReviewRequired ||
		reviewed.Grant != nil || reviewed.Fulfilment != nil {
		t.Fatalf("review fixture = %+v, want paid with review and no job", reviewed)
	}

	// A paid term that already ended cannot serve: the scan dead-letters it
	// instead of activating anything. The term is ended by rewriting the
	// persisted paidAt, because the plan contract refuses a TTL below a minute.
	expiredStore := store.New()
	expiredOrder := newRecoveryOrder(t, expiredStore, "boot-expired", 60)
	if _, err := expiredStore.SetUserEnabled(expiredOrder.UserID, false); err != nil {
		t.Fatalf("SetUserEnabled(expired): %v", err)
	}
	expired := settleRecoveryOrder(t, expiredStore, expiredOrder, "boot-expired")
	if expired.State != model.OrderPaid || expired.Grant != nil || expired.Fulfilment != nil {
		t.Fatalf("expired fixture = %+v, want paid without a grant or job", expired)
	}
	expiredStore = expireOrderPaidTerm(t, expiredStore, expiredOrder.ID, time.Now().Add(-2*time.Hour))
	expiredResult, err := runtime.RecoverPaidOrders(expiredStore, logger)
	if err != nil {
		t.Fatalf("RecoverPaidOrders(expired): %v", err)
	}
	if len(expiredResult.Queued) != 0 || len(expiredResult.DeadLettered) != 1 ||
		expiredResult.DeadLettered[0] != expiredOrder.ID {
		t.Fatalf("expired recovery = %+v, want only %s dead-lettered", expiredResult, expiredOrder.ID)
	}
	expiredAfter, _ := expiredStore.Order(expiredOrder.ID)
	if expiredAfter.State != model.OrderPaid || expiredAfter.Grant != nil {
		t.Fatalf("expired order = %+v, want paid without a grant", expiredAfter)
	}
	expiredJob := expiredAfter.Fulfilment
	if expiredJob == nil || expiredJob.State != model.FulfilmentDeadLetter || !expiredJob.Blocked ||
		expiredJob.LastReason != model.FulfilmentReasonEntitlementExpired ||
		expiredJob.Attempts != 0 || expiredJob.NextAttemptAt != nil {
		t.Fatalf("expired job = %+v, want a blocked entitlement_expired dead letter", expiredJob)
	}
	expiredAt := expiredJob.UpdatedAt
	if recoveryQuota(t, expiredStore, expiredOrder.UserID) != nil {
		t.Fatalf("expired order moved the quota")
	}
	// A second boot leaves the dead letter alone.
	expiredResult, err = runtime.RecoverPaidOrders(expiredStore, logger)
	if err != nil {
		t.Fatalf("second RecoverPaidOrders(expired): %v", err)
	}
	if len(expiredResult.Queued) != 0 || len(expiredResult.DeadLettered) != 0 {
		t.Fatalf("second expired recovery = %+v, want no work", expiredResult)
	}
	if again, _ := expiredStore.Order(expiredOrder.ID); again.Fulfilment == nil ||
		again.Fulfilment.State != model.FulfilmentDeadLetter || again.Fulfilment.UpdatedAt != expiredAt {
		t.Fatalf("expired dead letter was rewritten: %+v", again.Fulfilment)
	}

	// An active order and a terminal order are not recovery work.
	activeOrder := newRecoveryOrder(t, st, "boot-active", 3600)
	if active := settleRecoveryOrder(t, st, activeOrder, "boot-active"); active.State != model.OrderActive {
		t.Fatalf("active fixture = %+v, want active", active)
	}
	cancelledOrder := newRecoveryOrder(t, st, "boot-cancelled", 3600)
	if _, err := st.CancelOrder(cancelledOrder.ID, "test"); err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}

	result, err := runtime.RecoverPaidOrders(st, logger)
	if err != nil {
		t.Fatalf("RecoverPaidOrders: %v", err)
	}
	if len(result.Queued) != 1 || result.Queued[0] != paid.ID {
		t.Fatalf("queued = %v, want only %s", result.Queued, paid.ID)
	}
	if len(result.DeadLettered) != 0 {
		t.Fatalf("dead-lettered = %v, want none in the healthy store", result.DeadLettered)
	}
	queued, _ := st.Order(paid.ID)
	if queued.Fulfilment == nil || queued.Fulfilment.Mode != model.FulfilmentInitial ||
		queued.Fulfilment.State != model.FulfilmentQueued || queued.Fulfilment.Attempts != 0 {
		t.Fatalf("queued job = %+v", queued.Fulfilment)
	}
	queuedAt := queued.Fulfilment.UpdatedAt
	if queued.Grant != nil || recoveryQuota(t, st, paid.UserID) != nil {
		t.Fatalf("boot scan granted service or quota: grant=%+v quota=%v",
			queued.Grant, recoveryQuota(t, st, paid.UserID))
	}

	existingAfter, _ := st.Order(existingOrder.ID)
	if existingAfter.Fulfilment == nil || existingAfter.Fulfilment.UpdatedAt != existingUpdated ||
		existingAfter.Fulfilment.Attempts != 0 ||
		existingAfter.Fulfilment.State != model.FulfilmentQueued {
		t.Fatalf("existing job = %+v, want the original untouched", existingAfter.Fulfilment)
	}
	if recoveryQuota(t, st, existingOrder.UserID) != nil {
		t.Fatalf("existing paid order moved the quota")
	}
	reviewAfter, _ := st.Order(reviewOrder.ID)
	if reviewAfter.Fulfilment != nil || !reviewAfter.ReviewRequired || reviewAfter.Grant != nil {
		t.Fatalf("review order = %+v, want review without a job", reviewAfter)
	}
	if recoveryQuota(t, st, reviewOrder.UserID) != nil {
		t.Fatalf("review order moved the quota")
	}
	if activeQuota := recoveryQuota(t, st, activeOrder.UserID); activeQuota == nil ||
		*activeQuota != 10<<30 {
		t.Fatalf("active order quota = %v, want the plan quota", activeQuota)
	}

	// In-flight publication is not recovery work either: the next boot scan
	// must neither duplicate nor reset the publishing attempt.
	existingAttempt, err := st.BeginFulfilment(existingOrder.ID)
	if err != nil {
		t.Fatalf("BeginFulfilment(existing): %v", err)
	}

	// A second boot attaches nothing and rewrites nothing.
	result, err = runtime.RecoverPaidOrders(st, logger)
	if err != nil {
		t.Fatalf("second RecoverPaidOrders: %v", err)
	}
	if len(result.Queued) != 0 || len(result.DeadLettered) != 0 {
		t.Fatalf("second recovery = %+v, want no work", result)
	}
	existingPublishing, _ := st.Order(existingOrder.ID)
	if existingPublishing.Fulfilment == nil ||
		existingPublishing.Fulfilment.State != model.FulfilmentPublishing ||
		existingPublishing.Fulfilment.Attempts != 1 ||
		existingPublishing.Fulfilment.AttemptLeaseID != existingAttempt.LeaseID {
		t.Fatalf("publishing job = %+v, want the in-flight attempt untouched",
			existingPublishing.Fulfilment)
	}
	again, _ := st.Order(paid.ID)
	if again.Fulfilment == nil || again.Fulfilment.UpdatedAt != queuedAt ||
		again.Fulfilment.Attempts != 0 || again.Fulfilment.State != model.FulfilmentQueued {
		t.Fatalf("second scan rewrote the job: %+v", again.Fulfilment)
	}

	// Dead-lettered work is an operator queue: the boot scan leaves it alone.
	attempt, err := st.BeginFulfilment(paid.ID)
	if err != nil {
		t.Fatalf("BeginFulfilment: %v", err)
	}
	if _, err := st.AbortFulfilment(paid.ID, attempt.LeaseID, model.FulfilmentReasonPublishFailed,
		"publication refused", false); err != nil {
		t.Fatalf("AbortFulfilment: %v", err)
	}
	result, err = runtime.RecoverPaidOrders(st, logger)
	if err != nil {
		t.Fatalf("third RecoverPaidOrders: %v", err)
	}
	if len(result.Queued) != 0 || len(result.DeadLettered) != 0 {
		t.Fatalf("recovery after dead letter = %+v, want no work", result)
	}
	dead, _ := st.Order(paid.ID)
	if dead.Fulfilment == nil || dead.Fulfilment.State != model.FulfilmentDeadLetter ||
		dead.Fulfilment.Attempts != 1 {
		t.Fatalf("dead-letter job = %+v, want the operator item untouched", dead.Fulfilment)
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
