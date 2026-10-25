package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/runtime"
	"ajsaccountd/internal/store"
)

// fakeOperationCatalog implements CatalogConnector plus CatalogServerLister so
// select-node can be exercised without a node controller or a vendor host. It
// records every guarded-switch call; block/release lets a test hold an
// operation in flight and prove the ACCOUNT_BUSY guard.
type fakeOperationCatalog struct {
	mu         sync.Mutex
	snapshotID string
	lookup     runtime.ServerLookup
	servers    []runtime.CatalogServer
	connect    runtime.SwitchResult
	connectErr error

	calls       []string
	block       bool
	started     chan struct{}
	release     chan struct{}
	startedOnce sync.Once
}

func (f *fakeOperationCatalog) SnapshotID() string { return f.snapshotID }

func (f *fakeOperationCatalog) LookupServer(accountID, serverID string) runtime.ServerLookup {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "lookup|"+accountID+"|"+serverID)
	return f.lookup
}

func (f *fakeOperationCatalog) CatalogServers(accountID string) ([]runtime.CatalogServer, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "catalog|"+accountID)
	if !f.lookup.LoginAvailable {
		return nil, false
	}
	return append([]runtime.CatalogServer{}, f.servers...), true
}

func (f *fakeOperationCatalog) ConnectServer(_ context.Context, accountID, serverID, snapshotID, trigger string) (runtime.SwitchResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, "connect|"+accountID+"|"+serverID+"|"+snapshotID+"|"+trigger)
	block := f.block
	started := f.started
	release := f.release
	f.mu.Unlock()
	if block {
		if started != nil {
			f.startedOnce.Do(func() { close(started) })
		}
		if release != nil {
			<-release
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connect, f.connectErr
}

func (f *fakeOperationCatalog) ReconnectAccount(context.Context, string, string, string) (runtime.SwitchResult, error) {
	return runtime.SwitchResult{}, nil
}

func (f *fakeOperationCatalog) DisconnectAccount(context.Context, string) (runtime.DisconnectResult, error) {
	return runtime.DisconnectResult{}, nil
}

func (f *fakeOperationCatalog) callCount(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for _, call := range f.calls {
		if len(call) >= len(prefix) && call[:len(prefix)] == prefix {
			count++
		}
	}
	return count
}

func (f *fakeOperationCatalog) lastCall() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return ""
	}
	return f.calls[len(f.calls)-1]
}

func (f *fakeOperationCatalog) unblock() {
	f.mu.Lock()
	f.block = false
	release := f.release
	f.mu.Unlock()
	if release != nil {
		close(release)
	}
}

func newOperationTestServer(t *testing.T, catalog CatalogConnector) (*httptest.Server, *store.Store, model.Account, model.PanelUser) {
	t.Helper()
	st := store.NewEmpty()
	account, err := st.CreateAccount(model.CreateAccountInput{
		Label: "Ops Account", Username: "vendor-user", Password: "secret",
		Platform: model.PlatformLinux, Concurrency: 1,
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	user, err := st.CreateUser(model.CreateUserInput{Name: "Ops User", Platform: "ops"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	server := New(Config{AdminToken: testToken, Catalog: catalog}, st,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)
	return ts, st, account, user
}

func eligibleLookup() runtime.ServerLookup {
	return runtime.ServerLookup{Title: "Test Server", Found: true, Eligible: true, LoginAvailable: true}
}

func putLiveNode(t *testing.T, st *store.Store, accountID, serverID, label string) {
	t.Helper()
	_, err := st.PutNode(model.ProxyNode{
		ID:        accountID + "#1:" + serverID,
		AccountID: accountID,
		Label:     label,
		Region:    "cn",
		State:     model.NodeLive,
		GatePort:  13002,
		EgressIP:  "198.51.100.7",
		ServerID:  serverID,
		Slot:      1,
	})
	if err != nil {
		t.Fatalf("PutNode: %v", err)
	}
}

func TestSelectNodeReturns202AndRecordsDurableOperation(t *testing.T) {
	catalog := &fakeOperationCatalog{
		lookup: eligibleLookup(),
		connect: runtime.SwitchResult{
			AccountID: "acc-01", ServerID: "srv-a", NodeID: "acc-01#1:srv-a",
			NodeState: model.NodeLive, GatePort: 13003, EgressIP: "198.51.100.8",
			DurationMs: 120, Warnings: []string{},
		},
	}
	ts, st, account, _ := newOperationTestServer(t, catalog)

	resp, body := apiCall(t, ts, http.MethodPost,
		"/v1/accounts/"+account.ID+"/actions/select-node",
		map[string]any{"target": map[string]any{"serverId": "srv-a"}})
	requireStatus(t, resp, http.StatusAccepted)
	if got := resp.Header.Get("Location"); got != "/v1/operations/op-0001" {
		t.Fatalf("Location = %q", got)
	}
	if got := resp.Header.Get("Retry-After"); got != "1" {
		t.Fatalf("Retry-After = %q", got)
	}
	created := decodeBody[model.Operation](t, body)
	if created.ID != "op-0001" || created.State != model.OperationQueued {
		t.Fatalf("202 operation = %+v", created)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("decode raw operation: %v", err)
	}
	for _, key := range []string{"id", "state", "startedAt", "finishedAt", "steps", "result", "error"} {
		if _, ok := raw[key]; !ok {
			t.Fatalf("queued operation JSON is missing %q: %s", key, body)
		}
	}
	if created.Type != model.OperationTypeConnect || created.AccountID != account.ID {
		t.Fatalf("202 operation metadata = %+v", created)
	}

	// The fake finishes immediately; wait on the durable record instead of
	// polling HTTP in the test.
	final, err := st.WaitOperation(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("WaitOperation: %v", err)
	}
	if final.State != model.OperationSucceeded || final.StartedAt == "" || final.FinishedAt == "" {
		t.Fatalf("terminal operation = %+v", final)
	}
	if final.Result == nil || !final.Result.Changed || final.Result.GatePort != 13003 || !final.Result.SessionHealthy {
		t.Fatalf("result = %+v", final.Result)
	}
	if final.Error != nil {
		t.Fatalf("succeeded operation error = %+v", final.Error)
	}
	if catalog.callCount("connect|") != 1 {
		t.Fatalf("guarded switch calls = %v", catalog.calls)
	}

	resp, body = apiCall(t, ts, http.MethodGet, "/v1/operations/"+created.ID, nil)
	requireStatus(t, resp, http.StatusOK)
	got := decodeBody[model.Operation](t, body)
	if got.State != model.OperationSucceeded || got.FinishedAt == "" {
		t.Fatalf("GET operation = %+v", got)
	}

	resp, body = apiCall(t, ts, http.MethodGet,
		"/v1/operations?account="+account.ID+"&state=succeeded&limit=10", nil)
	requireStatus(t, resp, http.StatusOK)
	listed := decodeBody[[]model.Operation](t, body)
	if len(listed) != 1 || listed[0].ID != created.ID {
		t.Fatalf("listed operations = %+v", listed)
	}

	actions := make(map[string]bool)
	for _, audit := range st.Audit(50) {
		actions[audit.Action] = true
	}
	if !actions["operation.create"] || !actions["operation.finish"] {
		t.Fatalf("audit actions = %v", actions)
	}
}

func TestSelectNodeWaitReturnsTerminalResult(t *testing.T) {
	catalog := &fakeOperationCatalog{
		lookup: eligibleLookup(),
		connect: runtime.SwitchResult{
			AccountID: "acc-01", ServerID: "srv-b", NodeID: "acc-01#1:srv-b",
			NodeState: model.NodeLive, GatePort: 13004, EgressIP: "198.51.100.9",
			SwitchedFrom: "srv-a", PrevNodeID: "acc-01#1:srv-a", DurationMs: 900,
			Warnings: []string{"reconcile_failed"},
		},
	}
	ts, _, account, _ := newOperationTestServer(t, catalog)

	resp, body := apiCall(t, ts, http.MethodPost,
		"/v1/accounts/"+account.ID+"/actions/select-node",
		map[string]any{"target": map[string]any{"serverId": "srv-b"}, "wait": true})
	requireStatus(t, resp, http.StatusOK)
	result := decodeBody[model.SelectNodeResult](t, body)
	if !result.Changed || result.ServerID != "srv-b" || result.GatePort != 13004 || !result.SessionHealthy {
		t.Fatalf("result = %+v", result)
	}
	if result.PreviousNodeID != "acc-01#1:srv-a" {
		t.Fatalf("previousNodeId = %q", result.PreviousNodeID)
	}
	if len(result.Warnings) != 1 {
		t.Fatalf("warnings = %v", result.Warnings)
	}
	if catalog.callCount("connect|") != 1 {
		t.Fatalf("guarded switch calls = %v", catalog.calls)
	}
}

func TestSelectNodeNoopAndIfCurrent(t *testing.T) {
	catalog := &fakeOperationCatalog{
		lookup: eligibleLookup(),
		servers: []runtime.CatalogServer{
			{ID: "srv-a", Region: "cn", Eligible: true},
			{ID: "srv-b", Region: "cn", Eligible: true},
		},
		connect: runtime.SwitchResult{
			AccountID: "acc-01", ServerID: "srv-b", NodeID: "acc-01#1:srv-b",
			NodeState: model.NodeLive, GatePort: 13005,
		},
	}
	ts, st, account, _ := newOperationTestServer(t, catalog)
	putLiveNode(t, st, account.ID, "srv-a", "ops-node-a")

	resp, body := apiCall(t, ts, http.MethodPost,
		"/v1/accounts/"+account.ID+"/actions/select-node",
		map[string]any{"target": map[string]any{"serverId": "srv-a"}, "wait": true})
	requireStatus(t, resp, http.StatusOK)
	noop := decodeBody[model.SelectNodeResult](t, body)
	if noop.Changed {
		t.Fatalf("no-op result = %+v", noop)
	}
	if noop.NodeID != account.ID+"#1:srv-a" || noop.GatePort != 13002 {
		t.Fatalf("no-op node = %+v", noop)
	}
	if catalog.callCount("connect|") != 0 {
		t.Fatalf("no-op called the guarded switch: %v", catalog.calls)
	}

	resp, body = apiCall(t, ts, http.MethodPost,
		"/v1/accounts/"+account.ID+"/actions/select-node",
		map[string]any{"target": map[string]any{"serverId": "srv-a"}, "ifCurrent": "error"})
	requireStatus(t, resp, http.StatusConflict)
	payload := decodeCatalogError(t, body)
	if payload.Error.Reason != "ALREADY_CURRENT" {
		t.Fatalf("ifCurrent=error reason = %q", payload.Error.Reason)
	}

	resp, body = apiCall(t, ts, http.MethodPost,
		"/v1/accounts/"+account.ID+"/actions/select-node",
		map[string]any{"target": map[string]any{"serverId": "srv-a"}, "ifCurrent": "reselect", "wait": true})
	requireStatus(t, resp, http.StatusOK)
	reselected := decodeBody[model.SelectNodeResult](t, body)
	if reselected.ServerID != "srv-b" {
		t.Fatalf("reselect result = %+v", reselected)
	}
	if catalog.callCount("connect|") != 1 {
		t.Fatalf("reselect calls = %v", catalog.calls)
	}
}

func TestSelectNodeReopensDeadSession(t *testing.T) {
	catalog := &fakeSnapshotCatalog{fakeOperationCatalog: &fakeOperationCatalog{
		lookup: eligibleLookup(),
		connect: runtime.SwitchResult{
			AccountID: "acc-01", ServerID: "srv-a", NodeID: "acc-01#1:srv-a",
			NodeState: model.NodeLive, GatePort: 13002,
		},
	}}
	ts, st, account, _ := newOperationTestServer(t, catalog)
	putLiveNode(t, st, account.ID, "srv-a", "ops-node-a")

	// The store says LIVE but the runtime snapshot is empty: select-node must
	// reopen the same server through the guarded switch instead of no-oping,
	// and report the session honestly.
	resp, body := apiCall(t, ts, http.MethodPost,
		"/v1/accounts/"+account.ID+"/actions/select-node",
		map[string]any{"target": map[string]any{"serverId": "srv-a"}, "wait": true})
	requireStatus(t, resp, http.StatusOK)
	result := decodeBody[model.SelectNodeResult](t, body)
	if !result.Changed || result.SessionHealthy {
		t.Fatalf("dead-session reopen = %+v", result)
	}
	if got := catalog.lastCall(); got != "connect|"+account.ID+"|srv-a||reconnect" {
		t.Fatalf("guarded switch call = %q, want reconnect trigger", got)
	}
}

// fakeSnapshotCatalog adds the runtime snapshot seam to the operation fake.
type fakeSnapshotCatalog struct {
	*fakeOperationCatalog
}

func (f *fakeSnapshotCatalog) Snapshot() []runtime.NodeRuntime { return nil }

// panicCatalog proves a panicking executor fails the durable record instead of
// crashing the process or leaving it running forever.
type panicCatalog struct {
	*fakeOperationCatalog
}

func (f *panicCatalog) ConnectServer(context.Context, string, string, string, string) (runtime.SwitchResult, error) {
	panic("vendor panic")
}

func TestSelectNodeExecutorPanicFailsOperation(t *testing.T) {
	catalog := &panicCatalog{fakeOperationCatalog: &fakeOperationCatalog{lookup: eligibleLookup()}}
	ts, _, account, _ := newOperationTestServer(t, catalog)

	resp, body := apiCall(t, ts, http.MethodPost,
		"/v1/accounts/"+account.ID+"/actions/select-node",
		map[string]any{"target": map[string]any{"serverId": "srv-a"}, "wait": true})
	requireStatus(t, resp, http.StatusInternalServerError)
	payload := decodeCatalogError(t, body)
	if payload.Error.Code != CodeInternal || payload.Error.Details["operationId"] == "" {
		t.Fatalf("panic error = %+v", payload.Error)
	}
	resp, body = apiCall(t, ts, http.MethodGet, "/v1/operations/"+payload.Error.Details["operationId"], nil)
	requireStatus(t, resp, http.StatusOK)
	failed := decodeBody[model.Operation](t, body)
	if failed.State != model.OperationFailed || failed.Error == nil || failed.Error.Code != "INTERNAL" {
		t.Fatalf("panic operation = %+v", failed)
	}
}

func TestSelectNodeValidationMatrix(t *testing.T) {
	catalog := &fakeOperationCatalog{lookup: eligibleLookup()}
	ts, st, account, _ := newOperationTestServer(t, catalog)
	base := "/v1/accounts/" + account.ID + "/actions/select-node"

	resp, _ := apiCall(t, ts, http.MethodPost, "/v1/accounts/acc-99/actions/select-node",
		map[string]any{"target": map[string]any{"serverId": "srv-a"}})
	requireStatus(t, resp, http.StatusNotFound)

	cases := []struct {
		name   string
		body   any
		status int
		reason string
	}{
		{name: "no target", body: map[string]any{}, status: http.StatusBadRequest},
		{name: "empty target", body: map[string]any{"target": map[string]any{}}, status: http.StatusBadRequest},
		{name: "two forms", body: map[string]any{"target": map[string]any{"serverId": "srv-a", "nodeId": account.ID + ":srv-a"}}, status: http.StatusBadRequest},
		{name: "strategy without region", body: map[string]any{"target": map[string]any{"serverId": "srv-a", "strategy": "next"}}, status: http.StatusBadRequest},
		{name: "unknown field", body: map[string]any{"target": map[string]any{"serverId": "srv-a"}, "bogus": 1}, status: http.StatusBadRequest},
		{name: "bad drain", body: map[string]any{"target": map[string]any{"serverId": "srv-a"}, "drainSeconds": 61}, status: http.StatusBadRequest},
		{name: "bad timeout", body: map[string]any{"target": map[string]any{"serverId": "srv-a"}, "timeoutSeconds": 5}, status: http.StatusBadRequest},
		{name: "bad ifCurrent", body: map[string]any{"target": map[string]any{"serverId": "srv-a"}, "ifCurrent": "maybe"}, status: http.StatusBadRequest},
		{name: "foreign node", body: map[string]any{"target": map[string]any{"nodeId": "other:srv-a"}}, status: http.StatusNotFound},
		{name: "malformed node", body: map[string]any{"target": map[string]any{"nodeId": account.ID + "#x"}}, status: http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := apiCall(t, ts, http.MethodPost, base, tc.body)
			requireStatus(t, resp, tc.status)
			if tc.status >= 400 {
				var payload errorResponse
				if err := json.Unmarshal(body, &payload); err != nil {
					t.Fatalf("decode error: %v", err)
				}
				if payload.Error.Code == "" {
					t.Fatalf("error body = %s", body)
				}
			}
		})
	}

	catalog.lookup = runtime.ServerLookup{}
	resp, body := apiCall(t, ts, http.MethodPost, base,
		map[string]any{"target": map[string]any{"serverId": "srv-a"}})
	requireStatus(t, resp, http.StatusConflict)
	if payload := decodeCatalogError(t, body); payload.Error.Reason != runtime.ReasonLoginRequired {
		t.Fatalf("login required reason = %q", payload.Error.Reason)
	}

	catalog.lookup = runtime.ServerLookup{LoginAvailable: true}
	resp, body = apiCall(t, ts, http.MethodPost, base,
		map[string]any{"target": map[string]any{"serverId": "gone"}})
	requireStatus(t, resp, http.StatusNotFound)
	if payload := decodeCatalogError(t, body); payload.Error.Reason != runtime.ReasonUnknownServer {
		t.Fatalf("unknown server reason = %q", payload.Error.Reason)
	}

	catalog.lookup = runtime.ServerLookup{LoginAvailable: true, Found: true, IneligibleReason: "full_load"}
	resp, body = apiCall(t, ts, http.MethodPost, base,
		map[string]any{"target": map[string]any{"serverId": "busy"}})
	requireStatus(t, resp, http.StatusConflict)
	payload := decodeCatalogError(t, body)
	if payload.Error.Reason != "SERVER_UNAVAILABLE" || payload.Error.Details["ineligibleReason"] != "full_load" {
		t.Fatalf("ineligible error = %+v", payload.Error)
	}

	// Region targets need the catalog lister; a connector without it is an
	// honest 503 rather than a guessed server.
	plain := &fakeCatalog{lookup: eligibleLookup()}
	plainTS, _, plainAccount, _ := newOperationTestServer(t, plain)
	resp, _ = apiCall(t, plainTS, http.MethodPost,
		"/v1/accounts/"+plainAccount.ID+"/actions/select-node",
		map[string]any{"target": map[string]any{"region": "cn", "strategy": "next"}})
	requireStatus(t, resp, http.StatusServiceUnavailable)

	catalog.lookup = eligibleLookup()
	catalog.servers = nil
	resp, body = apiCall(t, ts, http.MethodPost, base,
		map[string]any{"target": map[string]any{"region": "cn", "strategy": "next"}})
	requireStatus(t, resp, http.StatusConflict)
	if payload := decodeCatalogError(t, body); payload.Error.Reason != "NO_ALTERNATE_SERVER" {
		t.Fatalf("no alternate reason = %q", payload.Error.Reason)
	}

	// A missing target falls back to the persisted selection.
	if _, err := st.SetSelection(account.ID, "srv-a", "operator"); err != nil {
		t.Fatalf("SetSelection: %v", err)
	}
	catalog.connect = runtime.SwitchResult{
		AccountID: account.ID, ServerID: "srv-a", NodeID: account.ID + "#1:srv-a",
		NodeState: model.NodeLive, GatePort: 13006,
	}
	resp, _ = apiCall(t, ts, http.MethodPost, base, map[string]any{"wait": true})
	requireStatus(t, resp, http.StatusOK)
}

func TestSelectNodeAccountBusy(t *testing.T) {
	catalog := &fakeOperationCatalog{
		lookup:  eligibleLookup(),
		connect: runtime.SwitchResult{AccountID: "acc-01", ServerID: "srv-a", NodeState: model.NodeLive, GatePort: 13002},
		block:   true,
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	ts, st, account, _ := newOperationTestServer(t, catalog)
	base := "/v1/accounts/" + account.ID + "/actions/select-node"

	resp, body := apiCall(t, ts, http.MethodPost, base,
		map[string]any{"target": map[string]any{"serverId": "srv-a"}})
	requireStatus(t, resp, http.StatusAccepted)
	first := decodeBody[model.Operation](t, body)

	select {
	case <-catalog.started:
	case <-time.After(2 * time.Second):
		t.Fatal("executor never reached the guarded switch")
	}
	resp, body = apiCall(t, ts, http.MethodGet, "/v1/operations/"+first.ID, nil)
	requireStatus(t, resp, http.StatusOK)
	running := decodeBody[model.Operation](t, body)
	if running.State != model.OperationRunning || running.StartedAt == "" || running.FinishedAt != "" {
		t.Fatalf("running operation = %+v", running)
	}
	resp, body = apiCall(t, ts, http.MethodPost, base,
		map[string]any{"target": map[string]any{"serverId": "srv-a"}})
	requireStatus(t, resp, http.StatusConflict)
	payload := decodeCatalogError(t, body)
	if payload.Error.Reason != "ACCOUNT_BUSY" || payload.Error.Details["currentOperationId"] != first.ID {
		t.Fatalf("busy error = %+v", payload.Error)
	}

	catalog.unblock()
	if _, err := st.WaitOperation(context.Background(), first.ID); err != nil {
		t.Fatalf("WaitOperation: %v", err)
	}
	resp, _ = apiCall(t, ts, http.MethodPost, base,
		map[string]any{"target": map[string]any{"serverId": "srv-a"}, "wait": true})
	requireStatus(t, resp, http.StatusOK)
}

func TestSelectNodeFailureIsRecorded(t *testing.T) {
	catalog := &fakeOperationCatalog{
		lookup: eligibleLookup(),
		connectErr: &runtime.SwitchError{
			Reason: runtime.ReasonSessionOpen, Message: "could not open the new session",
			Details: map[string]string{"rollback": "metadata_only"},
		},
	}
	ts, _, account, _ := newOperationTestServer(t, catalog)

	resp, body := apiCall(t, ts, http.MethodPost,
		"/v1/accounts/"+account.ID+"/actions/select-node",
		map[string]any{"target": map[string]any{"serverId": "srv-a"}, "wait": true})
	requireStatus(t, resp, http.StatusServiceUnavailable)
	payload := decodeCatalogError(t, body)
	if payload.Error.Code != CodeUpstreamUnavailable || payload.Error.Details["operationId"] == "" {
		t.Fatalf("wait error = %+v", payload.Error)
	}

	resp, body = apiCall(t, ts, http.MethodGet, "/v1/operations/"+payload.Error.Details["operationId"], nil)
	requireStatus(t, resp, http.StatusOK)
	failed := decodeBody[model.Operation](t, body)
	if failed.State != model.OperationFailed || failed.Error == nil {
		t.Fatalf("failed operation = %+v", failed)
	}
	if failed.Error.Code != "UPSTREAM_UNAVAILABLE" {
		t.Fatalf("error code = %q", failed.Error.Code)
	}
	if failed.StartedAt == "" || failed.FinishedAt == "" {
		t.Fatalf("failure timestamps = started %q finished %q", failed.StartedAt, failed.FinishedAt)
	}
	if len(failed.Steps) != 2 || failed.Steps[1].State != model.OperationStepFailed {
		t.Fatalf("failure steps = %+v", failed.Steps)
	}

	// A cooldown failure keeps the retry hint on the durable record and on the
	// wait:true response.
	catalog.connectErr = &runtime.SwitchError{
		Reason: runtime.ReasonConnectCooldown, Message: "connect cooldown",
		RetryAfter: 3200 * time.Millisecond,
	}
	resp, body = apiCall(t, ts, http.MethodPost,
		"/v1/accounts/"+account.ID+"/actions/select-node",
		map[string]any{"target": map[string]any{"serverId": "srv-a"}, "wait": true})
	requireStatus(t, resp, http.StatusTooManyRequests)
	if got := resp.Header.Get("Retry-After"); got != "4" {
		t.Fatalf("Retry-After = %q, want 4", got)
	}
	payload = decodeCatalogError(t, body)
	if payload.Error.Details["retryAfterSeconds"] != "4" {
		t.Fatalf("cooldown details = %+v", payload.Error.Details)
	}
	resp, body = apiCall(t, ts, http.MethodGet, "/v1/operations/"+payload.Error.Details["operationId"], nil)
	requireStatus(t, resp, http.StatusOK)
	cooldown := decodeBody[model.Operation](t, body)
	if cooldown.Error == nil || cooldown.Error.Code != "RATE_LIMITED" ||
		cooldown.Error.Details["retryAfterSeconds"] != "4" {
		t.Fatalf("cooldown operation = %+v", cooldown)
	}
}

func TestOperationsListFiltersAndScopedKeys(t *testing.T) {
	catalog := &fakeOperationCatalog{
		lookup:  eligibleLookup(),
		connect: runtime.SwitchResult{AccountID: "acc-01", ServerID: "srv-a", NodeState: model.NodeLive, GatePort: 13002},
	}
	ts, st, account, user := newOperationTestServer(t, catalog)
	other, err := st.CreateAccount(model.CreateAccountInput{
		Label: "Other Account", Username: "other-user", Password: "secret",
		Platform: model.PlatformLinux, Concurrency: 1,
	})
	if err != nil {
		t.Fatalf("CreateAccount(other): %v", err)
	}

	seed := func(accountID, serverID string, state model.OperationState) model.Operation {
		t.Helper()
		op, busy, err := st.BeginOperation(model.Operation{
			Type: model.OperationTypeSwitch, AccountID: accountID,
			Target: model.OperationTarget{ServerID: serverID}, RequestedBy: "admin",
		})
		if err != nil || busy != nil {
			t.Fatalf("BeginOperation(%s): busy=%+v err=%v", accountID, busy, err)
		}
		if !st.MarkOperationRunning(op.ID) {
			t.Fatalf("MarkOperationRunning(%s) = false", op.ID)
		}
		var opErr *model.OperationError
		if state == model.OperationFailed {
			opErr = &model.OperationError{Code: "UPSTREAM_UNAVAILABLE", Message: "failed"}
		}
		if _, ok := st.FinishOperation(op.ID, state, nil, nil, opErr); !ok {
			t.Fatalf("FinishOperation(%s) = false", op.ID)
		}
		return op
	}
	first := seed(account.ID, "srv-a", model.OperationSucceeded)
	second := seed(account.ID, "srv-b", model.OperationFailed)
	third := seed(other.ID, "srv-c", model.OperationSucceeded)

	resp, body := apiCall(t, ts, http.MethodGet, "/v1/operations?limit=50", nil)
	requireStatus(t, resp, http.StatusOK)
	if got := decodeBody[[]model.Operation](t, body); len(got) != 3 {
		t.Fatalf("all operations = %+v", got)
	}
	resp, body = apiCall(t, ts, http.MethodGet, "/v1/operations?account="+account.ID, nil)
	requireStatus(t, resp, http.StatusOK)
	if got := decodeBody[[]model.Operation](t, body); len(got) != 2 {
		t.Fatalf("account filter = %+v", got)
	}
	resp, body = apiCall(t, ts, http.MethodGet,
		"/v1/operations?accountId="+account.ID+"&state=failed", nil)
	requireStatus(t, resp, http.StatusOK)
	got := decodeBody[[]model.Operation](t, body)
	if len(got) != 1 || got[0].ID != second.ID {
		t.Fatalf("state filter = %+v", got)
	}
	resp, body = apiCall(t, ts, http.MethodGet, "/v1/operations?limit=1", nil)
	requireStatus(t, resp, http.StatusOK)
	if got := decodeBody[[]model.Operation](t, body); len(got) != 1 {
		t.Fatalf("limit filter = %+v", got)
	}
	for _, query := range []string{"?state=bogus", "?limit=0", "?limit=201", "?limit=nope"} {
		resp, _ = apiCall(t, ts, http.MethodGet, "/v1/operations"+query, nil)
		requireStatus(t, resp, http.StatusBadRequest)
	}

	readKey, _ := mustCreateScopedKey(t, ts, map[string]any{
		"userId": user.ID, "label": "ops read",
		"scopes": []string{"catalog:read"}, "accountIds": []string{account.ID},
	})
	resp, body = scopedCall(t, ts, readKey.Secret, http.MethodGet, "/v1/operations", nil)
	requireStatus(t, resp, http.StatusOK)
	if got := decodeBody[[]model.Operation](t, body); len(got) != 2 {
		t.Fatalf("restricted list = %+v", got)
	}
	resp, _ = scopedCall(t, ts, readKey.Secret, http.MethodGet, "/v1/operations/"+first.ID, nil)
	requireStatus(t, resp, http.StatusOK)
	resp, _ = scopedCall(t, ts, readKey.Secret, http.MethodGet, "/v1/operations/"+third.ID, nil)
	requireStatus(t, resp, http.StatusNotFound)
	resp, _ = scopedCall(t, ts, readKey.Secret, http.MethodGet,
		"/v1/operations?account="+other.ID, nil)
	requireStatus(t, resp, http.StatusNotFound)

	resp, _ = scopedCall(t, ts, readKey.Secret, http.MethodPost,
		"/v1/accounts/"+account.ID+"/actions/select-node",
		map[string]any{"target": map[string]any{"serverId": "srv-a"}})
	requireStatus(t, resp, http.StatusForbidden)

	writeKey, _ := mustCreateScopedKey(t, ts, map[string]any{
		"userId": user.ID, "label": "ops write",
		"scopes": []string{"catalog:read", "catalog:write"}, "accountIds": []string{account.ID},
	})
	resp, _ = scopedCall(t, ts, writeKey.Secret, http.MethodPost,
		"/v1/accounts/"+account.ID+"/actions/select-node",
		map[string]any{"target": map[string]any{"serverId": "srv-a"}, "wait": true})
	requireStatus(t, resp, http.StatusOK)

	accountsOnly, _ := mustCreateScopedKey(t, ts, map[string]any{
		"userId": user.ID, "label": "accounts only",
		"scopes": []string{"accounts:read"},
	})
	resp, _ = scopedCall(t, ts, accountsOnly.Secret, http.MethodGet, "/v1/operations", nil)
	requireStatus(t, resp, http.StatusForbidden)
}

func TestCancelOperationEndpoint(t *testing.T) {
	catalog := &fakeOperationCatalog{lookup: eligibleLookup()}
	ts, st, account, _ := newOperationTestServer(t, catalog)

	queued, busy, err := st.BeginOperation(model.Operation{
		Type: model.OperationTypeSwitch, AccountID: account.ID,
		Target: model.OperationTarget{ServerID: "srv-a"}, RequestedBy: "admin",
	})
	if err != nil || busy != nil {
		t.Fatalf("BeginOperation: busy=%+v err=%v", busy, err)
	}
	resp, body := apiCall(t, ts, http.MethodDelete, "/v1/operations/"+queued.ID, nil)
	requireStatus(t, resp, http.StatusOK)
	canceled := decodeBody[model.Operation](t, body)
	if canceled.State != model.OperationCanceled || canceled.FinishedAt == "" {
		t.Fatalf("canceled operation = %+v", canceled)
	}
	resp, _ = apiCall(t, ts, http.MethodDelete, "/v1/operations/"+queued.ID, nil)
	requireStatus(t, resp, http.StatusConflict)

	running, busy, err := st.BeginOperation(model.Operation{
		Type: model.OperationTypeSwitch, AccountID: account.ID,
		Target: model.OperationTarget{ServerID: "srv-b"}, RequestedBy: "admin",
	})
	if err != nil || busy != nil {
		t.Fatalf("BeginOperation(running): busy=%+v err=%v", busy, err)
	}
	if !st.MarkOperationRunning(running.ID) {
		t.Fatal("MarkOperationRunning = false")
	}
	resp, _ = apiCall(t, ts, http.MethodDelete, "/v1/operations/"+running.ID, nil)
	requireStatus(t, resp, http.StatusConflict)
	resp, _ = apiCall(t, ts, http.MethodDelete, "/v1/operations/op-9999", nil)
	requireStatus(t, resp, http.StatusNotFound)
}

func TestOperationDurableAcrossServerRestart(t *testing.T) {
	catalog := &fakeOperationCatalog{
		lookup: eligibleLookup(),
		connect: runtime.SwitchResult{
			AccountID: "acc-01", ServerID: "srv-a", NodeID: "acc-01#1:srv-a",
			NodeState: model.NodeLive, GatePort: 13002,
		},
	}
	ts, st, account, _ := newOperationTestServer(t, catalog)
	resp, body := apiCall(t, ts, http.MethodPost,
		"/v1/accounts/"+account.ID+"/actions/select-node",
		map[string]any{"target": map[string]any{"serverId": "srv-a"}, "wait": true})
	requireStatus(t, resp, http.StatusOK)
	var result model.SelectNodeResult
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	operations := st.Operations(store.OperationFilter{AccountID: account.ID})
	if len(operations) != 1 {
		t.Fatalf("operations before restart = %+v", operations)
	}
	finishedID := operations[0].ID

	data, err := st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	restored := store.NewEmpty()
	if err := restored.Restore(data); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	restarted := New(Config{AdminToken: testToken, Catalog: catalog}, restored,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	restartedTS := httptest.NewServer(restarted)
	t.Cleanup(restartedTS.Close)

	resp, body = apiCall(t, restartedTS, http.MethodGet, "/v1/operations/"+finishedID, nil)
	requireStatus(t, resp, http.StatusOK)
	after := decodeBody[model.Operation](t, body)
	if after.State != model.OperationSucceeded || after.FinishedAt == "" || after.Result == nil {
		t.Fatalf("operation after restart = %+v", after)
	}
	if after.Result.GatePort != 13002 {
		t.Fatalf("result after restart = %+v", after.Result)
	}
}
