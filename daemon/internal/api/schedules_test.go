package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/runtime"
	"ajsaccountd/internal/schedule"
	"ajsaccountd/internal/store"
)

// scheduleTestCatalog implements both CatalogConnector and CatalogLister so a
// rotation pool can be validated without a node controller.
type scheduleTestCatalog struct {
	mu             sync.Mutex
	servers        map[string][]runtime.CatalogServer
	connectCalls   int
	reconnectCalls int
	connectErr     error
	reconnectErr   error
}

func newScheduleTestCatalog(accountID string) *scheduleTestCatalog {
	return &scheduleTestCatalog{servers: map[string][]runtime.CatalogServer{
		accountID: {
			{ID: "srv-a", Title: "A", Region: "cn", Eligible: true},
			{ID: "srv-b", Title: "B", Region: "cn", Eligible: true},
		},
	}}
}

func (f *scheduleTestCatalog) SnapshotID() string { return "cat-test" }

func (f *scheduleTestCatalog) LookupServer(accountID, serverID string) runtime.ServerLookup {
	f.mu.Lock()
	defer f.mu.Unlock()
	servers, ok := f.servers[accountID]
	if !ok {
		return runtime.ServerLookup{}
	}
	for _, server := range servers {
		if server.ID == serverID {
			return runtime.ServerLookup{Title: server.Title, Found: true, Eligible: server.Eligible, LoginAvailable: true}
		}
	}
	return runtime.ServerLookup{LoginAvailable: true}
}

func (f *scheduleTestCatalog) CatalogServers(accountID string) ([]runtime.CatalogServer, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	servers, ok := f.servers[accountID]
	if !ok {
		return nil, false
	}
	return append([]runtime.CatalogServer(nil), servers...), true
}

func (f *scheduleTestCatalog) ConnectServer(_ context.Context, accountID, serverID, snapshotID, trigger string) (runtime.SwitchResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connectCalls++
	if f.connectErr != nil {
		return runtime.SwitchResult{}, f.connectErr
	}
	return runtime.SwitchResult{AccountID: accountID, ServerID: serverID, NodeID: "node-" + serverID,
		NodeState: model.NodeLive, SwitchedFrom: "srv-old"}, nil
}

func (f *scheduleTestCatalog) ReconnectAccount(context.Context, string, string, string) (runtime.SwitchResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reconnectCalls++
	if f.reconnectErr != nil {
		return runtime.SwitchResult{}, f.reconnectErr
	}
	return runtime.SwitchResult{AccountID: "acc", ServerID: "srv-a", NodeID: "node-srv-a",
		NodeState: model.NodeLive}, nil
}

func (f *scheduleTestCatalog) DisconnectAccount(context.Context, string) (runtime.DisconnectResult, error) {
	return runtime.DisconnectResult{}, nil
}

func (f *scheduleTestCatalog) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connectCalls, f.reconnectCalls
}

func newScheduleTestServer(t *testing.T) (*httptest.Server, *store.Store, *scheduleTestCatalog, *schedule.Engine, model.Account) {
	t.Helper()
	st := store.NewEmpty()
	account, err := st.CreateAccount(model.CreateAccountInput{
		Label: "API Schedule", Username: "user", Password: "secret",
		Platform: model.PlatformLinux, Concurrency: 1, Regions: []string{"cn"},
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if _, err := st.SetSelection(account.ID, "srv-a", "test"); err != nil {
		t.Fatalf("SetSelection: %v", err)
	}
	catalog := newScheduleTestCatalog(account.ID)
	engine, err := schedule.NewEngine(schedule.Config{
		Store:          st,
		Catalog:        catalog,
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:            time.Now,
		NodeLimit:      1,
		RenewalEnabled: false,
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	if err := engine.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = engine.Stop(stopCtx)
	})
	server := New(Config{AdminToken: testToken, Catalog: catalog, Schedules: engine}, st,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)
	return ts, st, catalog, engine, account
}

func TestScheduleAPIListCreatePatchDelete(t *testing.T) {
	ts, _, _, _, account := newScheduleTestServer(t)
	create := map[string]any{
		"name":    "Every 5m",
		"action":  "account.reconnect",
		"target":  map[string]any{"kind": "account", "id": account.ID},
		"trigger": map[string]any{"kind": "interval", "everySec": 300},
	}
	resp, body := apiCall(t, ts, http.MethodPost, "/v1/schedules", create)
	requireStatus(t, resp, http.StatusCreated)
	created := decodeBody[model.Schedule](t, body)
	if created.ID != "sch-001" || created.Revision != 1 {
		t.Fatalf("created = %+v", created)
	}
	if created.Enabled {
		t.Fatal("create default enabled must be false")
	}
	if !created.Guard.DryRun {
		t.Fatal("create default dryRun must be true")
	}
	etag := resp.Header.Get("ETag")
	if etag != `"sch-001:1"` {
		t.Fatalf("ETag = %q", etag)
	}
	if resp.Header.Get("X-APC-Server-Time") == "" {
		t.Fatal("missing X-APC-Server-Time")
	}

	resp, body = apiCall(t, ts, http.MethodGet, "/v1/schedules", nil)
	requireStatus(t, resp, http.StatusOK)
	list := decodeBody[[]model.Schedule](t, body)
	if len(list) != 1 || list[0].ID != created.ID {
		t.Fatalf("list = %+v", list)
	}

	resp, body = apiCall(t, ts, http.MethodGet, "/v1/schedules/"+created.ID, nil)
	requireStatus(t, resp, http.StatusOK)
	if got := decodeBody[model.Schedule](t, body); got.ID != created.ID {
		t.Fatalf("get = %+v", got)
	}

	req := newRequest(t, http.MethodPatch, ts.URL+"/v1/schedules/"+created.ID, map[string]any{"enabled": true})
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set(adminCSRFHeader, adminCSRFValue)
	req.Header.Set("If-Match", etag)
	resp, body = doRequest(t, ts, req)
	requireStatus(t, resp, http.StatusOK)
	updated := decodeBody[model.Schedule](t, body)
	if !updated.Enabled || updated.Revision != 2 {
		t.Fatalf("updated = %+v", updated)
	}
	if updated.NextRunAt == "" {
		t.Fatal("enabling did not arm nextRunAt")
	}

	req = newRequest(t, http.MethodPatch, ts.URL+"/v1/schedules/"+created.ID, map[string]any{"name": "stale"})
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set(adminCSRFHeader, adminCSRFValue)
	req.Header.Set("If-Match", etag)
	resp, body = doRequest(t, ts, req)
	requireStatus(t, resp, http.StatusPreconditionFailed)
	requireErrorCode(t, body, "PRECONDITION_FAILED")

	resp, body = apiCall(t, ts, http.MethodGet, "/v1/schedules/"+created.ID+"/runs", nil)
	requireStatus(t, resp, http.StatusOK)
	if runs := decodeBody[[]model.ScheduleRun](t, body); len(runs) != 0 {
		t.Fatalf("runs = %+v", runs)
	}

	resp, _ = apiCall(t, ts, http.MethodDelete, "/v1/schedules/"+created.ID, nil)
	requireStatus(t, resp, http.StatusNoContent)
	resp, _ = apiCall(t, ts, http.MethodGet, "/v1/schedules/"+created.ID, nil)
	requireStatus(t, resp, http.StatusNotFound)
}

func TestScheduleAPIPreview(t *testing.T) {
	ts, _, _, _, _ := newScheduleTestServer(t)
	query := url.Values{}
	query.Set("kind", "cron")
	query.Set("expr", "0 3 * * *")
	query.Set("timezone", "UTC")
	query.Set("count", "2")
	resp, body := apiCall(t, ts, http.MethodGet, "/v1/schedules/preview?"+query.Encode(), nil)
	requireStatus(t, resp, http.StatusOK)
	preview := decodeBody[model.SchedulePreview](t, body)
	if len(preview.Runs) != 2 || preview.ServerTime == "" {
		t.Fatalf("preview = %+v", preview)
	}
	if !strings.HasSuffix(preview.Runs[0].At, "T03:00:00Z") {
		t.Fatalf("first preview run = %s", preview.Runs[0].At)
	}

	resp, body = apiCall(t, ts, http.MethodPost, "/v1/schedules/preview", map[string]any{
		"action":  "account.reconnect",
		"trigger": map[string]any{"kind": "interval", "everySec": 600},
	})
	requireStatus(t, resp, http.StatusOK)
	preview = decodeBody[model.SchedulePreview](t, body)
	if len(preview.Runs) != 5 {
		t.Fatalf("default preview count = %d, want 5", len(preview.Runs))
	}

	query.Set("expr", "not a cron")
	resp, body = apiCall(t, ts, http.MethodGet, "/v1/schedules/preview?"+query.Encode(), nil)
	requireStatus(t, resp, http.StatusBadRequest)
	requireErrorCode(t, body, CodeInvalidArgument)

	unreachable := url.Values{}
	unreachable.Set("kind", "cron")
	unreachable.Set("expr", "0 0 * * *")
	unreachable.Set("days", "0,1,2,3,4,5,6")
	unreachable.Set("start", "00:01")
	unreachable.Set("end", "00:02")
	resp, body = apiCall(t, ts, http.MethodGet, "/v1/schedules/preview?"+unreachable.Encode(), nil)
	requireStatus(t, resp, http.StatusOK)
	preview = decodeBody[model.SchedulePreview](t, body)
	if len(preview.Runs) != 0 || len(preview.Warnings) == 0 || preview.Warnings[0] != "window_unreachable" {
		t.Fatalf("unreachable preview = %+v", preview)
	}
}

func TestScheduleAPICreateValidation(t *testing.T) {
	ts, _, _, _, account := newScheduleTestServer(t)
	base := func() map[string]any {
		return map[string]any{
			"name":    "Rotate",
			"action":  "account.rotate",
			"target":  map[string]any{"kind": "account", "id": account.ID, "params": map[string]any{"strategy": "pool", "serverIds": []string{"srv-a", "srv-b"}}},
			"trigger": map[string]any{"kind": "interval", "everySec": 600},
		}
	}
	body := base()
	body["guard"] = map[string]any{"dryRun": false}
	resp, payload := apiCall(t, ts, http.MethodPost, "/v1/schedules", body)
	requireStatus(t, resp, http.StatusConflict)
	requireErrorCode(t, payload, "CONFIRMATION_REQUIRED")

	body["confirm"] = "ARM"
	resp, payload = apiCall(t, ts, http.MethodPost, "/v1/schedules", body)
	requireStatus(t, resp, http.StatusCreated)
	created := decodeBody[model.Schedule](t, payload)
	if created.Guard.DryRun {
		t.Fatal("explicit dryRun:false was overridden")
	}

	duplicate := base()
	duplicate["name"] = "rotate"
	resp, payload = apiCall(t, ts, http.MethodPost, "/v1/schedules", duplicate)
	requireStatus(t, resp, http.StatusConflict)
	requireErrorCode(t, payload, CodeConflict)

	bad := base()
	bad["name"] = "Bad cron"
	bad["trigger"] = map[string]any{"kind": "cron", "expr": "not a cron"}
	resp, payload = apiCall(t, ts, http.MethodPost, "/v1/schedules", bad)
	requireStatus(t, resp, http.StatusBadRequest)
	requireErrorCode(t, payload, CodeInvalidArgument)

	missingServer := base()
	missingServer["name"] = "Missing server"
	missingServer["target"] = map[string]any{"kind": "account", "id": account.ID,
		"params": map[string]any{"strategy": "pool", "serverIds": []string{"srv-a", "srv-missing"}}}
	resp, payload = apiCall(t, ts, http.MethodPost, "/v1/schedules", missingServer)
	requireStatus(t, resp, http.StatusBadRequest)
	requireErrorCode(t, payload, CodeInvalidArgument)

	noTarget := base()
	noTarget["name"] = "No target"
	delete(noTarget, "target")
	resp, payload = apiCall(t, ts, http.MethodPost, "/v1/schedules", noTarget)
	requireStatus(t, resp, http.StatusBadRequest)
	requireErrorCode(t, payload, CodeInvalidArgument)
}

func TestScheduleAPIArmConfirmation(t *testing.T) {
	ts, _, _, _, account := newScheduleTestServer(t)
	create := map[string]any{
		"name":    "Arm later",
		"action":  "account.reconnect",
		"target":  map[string]any{"kind": "account", "id": account.ID},
		"trigger": map[string]any{"kind": "interval", "everySec": 600},
	}
	resp, body := apiCall(t, ts, http.MethodPost, "/v1/schedules", create)
	requireStatus(t, resp, http.StatusCreated)
	created := decodeBody[model.Schedule](t, body)
	etag := resp.Header.Get("ETag")

	req := newRequest(t, http.MethodPatch, ts.URL+"/v1/schedules/"+created.ID,
		map[string]any{"guard": map[string]any{"dryRun": false}})
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set(adminCSRFHeader, adminCSRFValue)
	req.Header.Set("If-Match", etag)
	resp, body = doRequest(t, ts, req)
	requireStatus(t, resp, http.StatusConflict)
	requireErrorCode(t, body, "CONFIRMATION_REQUIRED")

	req = newRequest(t, http.MethodPatch, ts.URL+"/v1/schedules/"+created.ID,
		map[string]any{"guard": map[string]any{"dryRun": false}, "confirm": "ARM"})
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set(adminCSRFHeader, adminCSRFValue)
	req.Header.Set("If-Match", etag)
	resp, body = doRequest(t, ts, req)
	requireStatus(t, resp, http.StatusOK)
	updated := decodeBody[model.Schedule](t, body)
	if updated.Guard.DryRun {
		t.Fatal("armed schedule still reports dryRun true")
	}
}

func TestScheduleAPIManualRun(t *testing.T) {
	ts, _, catalog, _, account := newScheduleTestServer(t)
	create := map[string]any{
		"name":    "Manual reconnect",
		"action":  "account.reconnect",
		"target":  map[string]any{"kind": "account", "id": account.ID},
		"trigger": map[string]any{"kind": "interval", "everySec": 600},
	}
	resp, body := apiCall(t, ts, http.MethodPost, "/v1/schedules", create)
	requireStatus(t, resp, http.StatusCreated)
	created := decodeBody[model.Schedule](t, body)

	resp, body = apiCall(t, ts, http.MethodPost, "/v1/schedules/"+created.ID+"/actions/run",
		map[string]any{"dryRun": true})
	requireStatus(t, resp, http.StatusOK)
	// The documented contract is exactly 200 + {run: ScheduleRun}: no
	// additional envelope fields, and no 202.
	envelope := decodeBody[map[string]any](t, body)
	if len(envelope) != 1 {
		t.Fatalf("run envelope = %v, want only the run field", envelope)
	}
	if _, ok := envelope["run"]; !ok {
		t.Fatalf("run envelope = %v, want a run field", envelope)
	}
	runEnvelope := decodeBody[struct {
		Run model.ScheduleRun `json:"run"`
	}](t, body)
	if runEnvelope.Run.Result != "dry_run" {
		t.Fatalf("run = %+v", runEnvelope.Run)
	}
	if _, reconnects := catalog.counts(); reconnects != 0 {
		t.Fatalf("dry run called the catalog %d times", reconnects)
	}

	resp, body = apiCall(t, ts, http.MethodGet, "/v1/schedules/"+created.ID+"/runs", nil)
	requireStatus(t, resp, http.StatusOK)
	if runs := decodeBody[[]model.ScheduleRun](t, body); len(runs) != 1 {
		t.Fatalf("runs = %+v", runs)
	}

	// The body is optional: no body at all keeps the schedule guard (a dry
	// run by default) and still answers the same 200 envelope.
	resp, body = apiCall(t, ts, http.MethodPost, "/v1/schedules/"+created.ID+"/actions/run", nil)
	requireStatus(t, resp, http.StatusOK)
	runEnvelope = decodeBody[struct {
		Run model.ScheduleRun `json:"run"`
	}](t, body)
	if runEnvelope.Run.Result != "dry_run" {
		t.Fatalf("run without a body = %+v, want the guarded dry run", runEnvelope.Run)
	}
	resp, body = apiCall(t, ts, http.MethodGet, "/v1/schedules/"+created.ID+"/runs", nil)
	requireStatus(t, resp, http.StatusOK)
	if runs := decodeBody[[]model.ScheduleRun](t, body); len(runs) != 2 {
		t.Fatalf("runs after the bodyless run = %+v, want two", runs)
	}

	rotate := map[string]any{
		"name":    "Manual rotate",
		"action":  "account.rotate",
		"target":  map[string]any{"kind": "account", "id": account.ID, "params": map[string]any{"strategy": "pool", "serverIds": []string{"srv-a", "srv-b"}}},
		"trigger": map[string]any{"kind": "interval", "everySec": 600},
		"guard":   map[string]any{"dryRun": false},
		"confirm": "ARM",
	}
	resp, body = apiCall(t, ts, http.MethodPost, "/v1/schedules", rotate)
	requireStatus(t, resp, http.StatusCreated)
	rotateSchedule := decodeBody[model.Schedule](t, body)

	resp, body = apiCall(t, ts, http.MethodPost, "/v1/schedules/"+rotateSchedule.ID+"/actions/run",
		map[string]any{"dryRun": false})
	requireStatus(t, resp, http.StatusConflict)
	requireErrorCode(t, body, "CONFIRMATION_REQUIRED")

	resp, body = apiCall(t, ts, http.MethodPost, "/v1/schedules/"+rotateSchedule.ID+"/actions/run",
		map[string]any{"dryRun": false, "confirm": "RUN"})
	requireStatus(t, resp, http.StatusOK)
	runEnvelope = decodeBody[struct {
		Run model.ScheduleRun `json:"run"`
	}](t, body)
	if runEnvelope.Run.Result != "ok" {
		t.Fatalf("manual rotate run = %+v", runEnvelope.Run)
	}
	if connects, _ := catalog.counts(); connects != 1 {
		t.Fatalf("ConnectServer calls = %d, want 1", connects)
	}
}

func TestScheduleAPIDisabled(t *testing.T) {
	st := store.NewEmpty()
	server := New(Config{AdminToken: testToken}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)

	resp, body := apiCall(t, ts, http.MethodGet, "/v1/schedules", nil)
	requireStatus(t, resp, http.StatusOK)
	if list := decodeBody[[]model.Schedule](t, body); len(list) != 0 {
		t.Fatalf("list = %+v", list)
	}
	resp, body = apiCall(t, ts, http.MethodPost, "/v1/schedules", map[string]any{})
	requireStatus(t, resp, http.StatusServiceUnavailable)
	requireErrorCode(t, body, CodeUpstreamUnavailable)

	// Every scheduler mutation answers 503 while the engine is absent; the
	// operations document that branch, so the handler test pins all four.
	for _, call := range []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodPatch, "/v1/schedules/sch-any", map[string]any{"name": "disabled"}},
		{http.MethodDelete, "/v1/schedules/sch-any", nil},
		{http.MethodPost, "/v1/schedules/sch-any/actions/run", map[string]any{"dryRun": true}},
	} {
		resp, body = apiCall(t, ts, call.method, call.path, call.body)
		requireStatus(t, resp, http.StatusServiceUnavailable)
		requireErrorCode(t, body, CodeUpstreamUnavailable)
	}

	query := url.Values{}
	query.Set("kind", "interval")
	query.Set("everySec", "300")
	resp, _ = apiCall(t, ts, http.MethodGet, "/v1/schedules/preview?"+query.Encode(), nil)
	requireStatus(t, resp, http.StatusOK)
}

func TestScheduleAPIMetrics(t *testing.T) {
	ts, _, _, _, _ := newScheduleTestServer(t)
	resp, body := apiCall(t, ts, http.MethodGet, "/metrics", nil)
	requireStatus(t, resp, http.StatusOK)
	text := string(body)
	for _, want := range []string{"ajs_schedules_total 0", "ajs_schedule_runs_total{result=\"ok\"} 0", "ajs_scheduler_inflight 0"} {
		if !strings.Contains(text, want) {
			t.Fatalf("metrics missing %q:\n%s", want, text)
		}
	}
}

// TestScopedKeyRestrictedSchedules covers the schedule surface for a key that
// may only read one account: the list prefilter runs before the query filters
// and pagination, invisible and unknown resources answer the same 404, writes
// may only target a visible account, and the pure preview stays allowed.
func TestScopedKeyRestrictedSchedules(t *testing.T) {
	ts, st, _, _, accountA := newScheduleTestServer(t)
	accountB, err := st.CreateAccount(model.CreateAccountInput{
		Label: "Second Tenant", Username: "second", Password: "secret",
		Platform: model.PlatformLinux, Concurrency: 1, Regions: []string{"jp"},
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	resp, body := apiCall(t, ts, http.MethodPost, "/v1/users", map[string]any{"name": "schedules.bot"})
	requireStatus(t, resp, http.StatusCreated)
	user := decodeBody[model.PanelUser](t, body)
	created, _ := mustCreateScopedKey(t, ts, map[string]any{
		"userId":     user.ID,
		"label":      "schedule tenant",
		"scopes":     []string{"settings:read", "settings:write"},
		"accountIds": []string{accountA.ID},
	})
	key := created.Secret

	createSchedule := func(name, accountID string) (model.Schedule, string) {
		t.Helper()
		resp, payload := apiCall(t, ts, http.MethodPost, "/v1/schedules", map[string]any{
			"name":    name,
			"action":  "account.reconnect",
			"target":  map[string]any{"kind": "account", "id": accountID},
			"trigger": map[string]any{"kind": "interval", "everySec": 600},
		})
		requireStatus(t, resp, http.StatusCreated)
		return decodeBody[model.Schedule](t, payload), resp.Header.Get("ETag")
	}
	// B is created first so a filter applied after pagination would leak it as
	// the first row of a ?limit=1 page.
	scheduleB, _ := createSchedule("Tenant B reconnect", accountB.ID)
	scheduleA, etagA := createSchedule("Tenant A reconnect", accountA.ID)

	resp, body = scopedCall(t, ts, key, http.MethodGet, "/v1/schedules?limit=1", nil)
	requireStatus(t, resp, http.StatusOK)
	page := decodeBody[[]model.Schedule](t, body)
	if len(page) != 1 || page[0].ID != scheduleA.ID {
		t.Fatalf("restricted schedule page = %+v, want only %s", page, scheduleA.ID)
	}

	resp, body = scopedCall(t, ts, key, http.MethodGet, "/v1/schedules?accountId="+accountB.ID, nil)
	requireStatus(t, resp, http.StatusOK)
	if got := decodeBody[[]model.Schedule](t, body); len(got) != 0 {
		t.Fatalf("outside accountId list = %+v, want empty", got)
	}
	resp, body = scopedCall(t, ts, key, http.MethodGet, "/v1/schedules?accountId=acc-unknown", nil)
	requireStatus(t, resp, http.StatusOK)
	if got := decodeBody[[]model.Schedule](t, body); len(got) != 0 {
		t.Fatalf("unknown accountId list = %+v, want empty", got)
	}
	if got := strings.TrimSpace(string(body)); got != "[]" {
		t.Fatalf("unknown accountId body = %q, want []", got)
	}
	resp, body = scopedCall(t, ts, key, http.MethodGet, "/v1/schedules?enabled=false", nil)
	requireStatus(t, resp, http.StatusOK)
	if got := decodeBody[[]model.Schedule](t, body); len(got) != 1 || got[0].ID != scheduleA.ID {
		t.Fatalf("enabled filter = %+v, want only %s", got, scheduleA.ID)
	}

	// Detail: an invisible schedule and an unknown id are indistinguishable.
	invisibleResp, invisibleBody := scopedCall(t, ts, key, http.MethodGet, "/v1/schedules/"+scheduleB.ID, nil)
	requireStatus(t, invisibleResp, http.StatusNotFound)
	unknownResp, unknownBody := scopedCall(t, ts, key, http.MethodGet, "/v1/schedules/sch-unknown", nil)
	requireStatus(t, unknownResp, http.StatusNotFound)
	invisibleErr := decodeBody[errorResponse](t, invisibleBody)
	unknownErr := decodeBody[errorResponse](t, unknownBody)
	if invisibleErr.Error.Code != CodeNotFound ||
		invisibleErr.Error.Code != unknownErr.Error.Code ||
		invisibleErr.Error.Message != unknownErr.Error.Message {
		t.Fatalf("invisible error = %+v, unknown error = %+v", invisibleErr.Error, unknownErr.Error)
	}

	// Runs authorize the parent schedule before any run row is read.
	for _, path := range []string{"/v1/schedules/" + scheduleB.ID + "/runs", "/v1/schedules/sch-unknown/runs"} {
		resp, body = scopedCall(t, ts, key, http.MethodGet, path, nil)
		requireStatus(t, resp, http.StatusNotFound)
		requireErrorCode(t, body, CodeNotFound)
	}

	// Authorization precedes If-Match: an invisible schedule is 404 even when
	// the precondition header is absent.
	resp, body = scopedCall(t, ts, key, http.MethodPatch, "/v1/schedules/"+scheduleB.ID,
		map[string]any{"name": "hijack"})
	requireStatus(t, resp, http.StatusNotFound)
	requireErrorCode(t, body, CodeNotFound)
	// A visible schedule cannot be retargeted to an invisible account.
	req := newRequest(t, http.MethodPatch, ts.URL+"/v1/schedules/"+scheduleA.ID,
		map[string]any{"target": map[string]any{"kind": "account", "id": accountB.ID}})
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set(adminCSRFHeader, adminCSRFValue)
	req.Header.Set("If-Match", etagA)
	resp, body = doRequest(t, ts, req)
	requireStatus(t, resp, http.StatusNotFound)
	requireErrorCode(t, body, CodeNotFound)
	unchanged, ok := st.Schedule(scheduleA.ID)
	if !ok || unchanged.Target.ID != accountA.ID || unchanged.Name != scheduleA.Name {
		t.Fatalf("schedule A changed after refused PATCH: %+v", unchanged)
	}

	// Create may only target a visible account.
	for _, targetID := range []string{accountB.ID, "acc-unknown"} {
		resp, body = scopedCall(t, ts, key, http.MethodPost, "/v1/schedules", map[string]any{
			"name":    "Outside " + targetID,
			"action":  "account.reconnect",
			"target":  map[string]any{"kind": "account", "id": targetID},
			"trigger": map[string]any{"kind": "interval", "everySec": 600},
		})
		requireStatus(t, resp, http.StatusNotFound)
		requireErrorCode(t, body, CodeNotFound)
	}
	resp, body = scopedCall(t, ts, key, http.MethodPost, "/v1/schedules", map[string]any{
		"name":    "Tenant A manual",
		"action":  "account.reconnect",
		"target":  map[string]any{"kind": "account", "id": accountA.ID},
		"trigger": map[string]any{"kind": "interval", "everySec": 900},
	})
	requireStatus(t, resp, http.StatusCreated)

	// Delete and run fetch the schedule first; an invisible one is not touched.
	for _, path := range []string{"/v1/schedules/" + scheduleB.ID, "/v1/schedules/sch-unknown"} {
		resp, _ = scopedCall(t, ts, key, http.MethodDelete, path, nil)
		requireStatus(t, resp, http.StatusNotFound)
	}
	if _, ok := st.Schedule(scheduleB.ID); !ok {
		t.Fatal("a refused DELETE removed the invisible schedule")
	}
	for _, path := range []string{"/v1/schedules/" + scheduleB.ID + "/actions/run", "/v1/schedules/sch-unknown/actions/run"} {
		resp, body = scopedCall(t, ts, key, http.MethodPost, path, map[string]any{"dryRun": true})
		requireStatus(t, resp, http.StatusNotFound)
		requireErrorCode(t, body, CodeNotFound)
	}

	// Preview is pure and hardcodes its target, so it stays allowed.
	resp, body = scopedCall(t, ts, key, http.MethodGet, "/v1/schedules/preview?kind=interval&everySec=300", nil)
	requireStatus(t, resp, http.StatusOK)
	if runs := decodeBody[model.SchedulePreview](t, body).Runs; len(runs) == 0 {
		t.Fatal("preview returned no runs for a restricted key")
	}

	// Admin compatibility: the same rows stay visible to the admin token.
	resp, body = apiCall(t, ts, http.MethodGet, "/v1/schedules", nil)
	requireStatus(t, resp, http.StatusOK)
	seen := make(map[string]bool)
	for _, scheduleValue := range decodeBody[[]model.Schedule](t, body) {
		seen[scheduleValue.ID] = true
	}
	if !seen[scheduleA.ID] || !seen[scheduleB.ID] {
		t.Fatalf("admin schedule list = %v, want both tenants", seen)
	}
}
