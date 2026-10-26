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
