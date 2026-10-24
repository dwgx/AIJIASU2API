package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"ajsaccountd/internal/health"
	"ajsaccountd/internal/model"
	"ajsaccountd/internal/renewal"
	"ajsaccountd/internal/runtime"
	"ajsaccountd/internal/store"
	"ajsaccountd/internal/usage"
	"ajsaccountd/internal/webhook"
)

func newPlatformServer(t *testing.T, cfg Config) (*httptest.Server, *store.Store) {
	t.Helper()
	st := store.New()
	cfg.AdminToken = testToken
	server := New(cfg, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)
	return ts, st
}

func TestRenewalPlansStaticFallback(t *testing.T) {
	// The fetcher points at a closed loopback port, so the live refresh fails
	// and the service must serve its built-in table with source=static.
	service := renewal.New(renewal.Config{BaseURL: "http://127.0.0.1:1", TTL: time.Hour})
	ts, _ := newPlatformServer(t, Config{Renewal: service})

	resp, body := apiCall(t, ts, http.MethodGet, "/v1/renewal/plans", nil)
	requireStatus(t, resp, http.StatusOK)
	snapshot := decodeBody[renewal.PlansSnapshot](t, body)
	if snapshot.Source != renewal.SourceStatic {
		t.Fatalf("source = %q, want %q (body %s)", snapshot.Source, renewal.SourceStatic, body)
	}
	if !snapshot.Stale {
		t.Fatalf("static fallback must report stale")
	}
	if len(snapshot.Plans) == 0 {
		t.Fatalf("static snapshot has no plans")
	}
}

func TestRenewalAccountStates(t *testing.T) {
	ts, _ := newPlatformServer(t, Config{Renewal: renewal.New(renewal.Config{BaseURL: "http://127.0.0.1:1"})})

	resp, body := apiCall(t, ts, http.MethodGet, "/v1/renewal/accounts/acc-01", nil)
	requireStatus(t, resp, http.StatusOK)
	active := decodeBody[renewal.AccountRenewal](t, body)
	if active.AccountID != "acc-01" {
		t.Fatalf("accountId = %q, want acc-01", active.AccountID)
	}
	if active.RenewalURL != renewal.OfficialPurchaseURL {
		t.Fatalf("renewalUrl = %q, want %q", active.RenewalURL, renewal.OfficialPurchaseURL)
	}
	if active.DaysLeft <= 0 || active.Urgency != renewal.UrgencyNone {
		t.Fatalf("active account daysLeft=%d urgency=%q, want a positive none-urgency window", active.DaysLeft, active.Urgency)
	}

	resp, body = apiCall(t, ts, http.MethodGet, "/v1/renewal/accounts/acc-06", nil)
	requireStatus(t, resp, http.StatusOK)
	expired := decodeBody[renewal.AccountRenewal](t, body)
	if expired.Urgency != renewal.UrgencyOverdue || expired.DaysLeft >= 0 {
		t.Fatalf("expired account daysLeft=%d urgency=%q, want overdue", expired.DaysLeft, expired.Urgency)
	}

	resp, body = apiCall(t, ts, http.MethodGet, "/v1/renewal/accounts/acc-unknown", nil)
	requireStatus(t, resp, http.StatusNotFound)
	requireErrorCode(t, body, CodeNotFound)
}

func TestRenewalDisabled(t *testing.T) {
	ts, _ := newPlatformServer(t, Config{})
	resp, body := apiCall(t, ts, http.MethodGet, "/v1/renewal/plans", nil)
	requireStatus(t, resp, http.StatusServiceUnavailable)
	requireErrorCode(t, body, CodeUpstreamUnavailable)

	resp, body = apiCall(t, ts, http.MethodGet, "/v1/renewal/accounts/acc-01", nil)
	requireStatus(t, resp, http.StatusServiceUnavailable)
	requireErrorCode(t, body, CodeUpstreamUnavailable)
}

func TestUsageSummaryAndValidation(t *testing.T) {
	ts, _ := newPlatformServer(t, Config{Usage: usageAggregator()})
	now := time.Now().UTC()
	from := now.Add(-48 * time.Hour).Format(time.RFC3339)
	to := now.Add(time.Hour).Format(time.RFC3339)

	resp, body := apiCall(t, ts, http.MethodGet, "/v1/usage?from="+from+"&to="+to, nil)
	requireStatus(t, resp, http.StatusOK)
	summary := decodeBody[usageSummary](t, body)
	if summary.Requests == 0 {
		t.Fatalf("usage summary counted no seeded requests (body %s)", body)
	}
	if len(summary.Days) != 3 {
		t.Fatalf("days = %d, want 3 for a 48h window", len(summary.Days))
	}
	if len(summary.Accounts) == 0 {
		t.Fatalf("usage summary has no account roll-up")
	}
	if summary.RowsRetained != usage.DefaultRetentionRows {
		t.Fatalf("rowsRetained = %d, want %d", summary.RowsRetained, usage.DefaultRetentionRows)
	}
	if summary.RetentionDays <= 0 {
		t.Fatalf("retentionDays = %d, want a positive retained span", summary.RetentionDays)
	}
	if summary.QuotaEnforced {
		t.Fatal("quotaEnforced = true, but the gate has no quota logic")
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("decode raw usage body %q: %v", body, err)
	}
	if value, ok := raw["quotaEnforced"]; !ok || string(value) != "false" {
		t.Fatalf("quotaEnforced wire value = %s (present %v), want false", value, ok)
	}

	// The account filter accepts the label or the account id.
	resp, body = apiCall(t, ts, http.MethodGet,
		"/v1/usage?from="+from+"&to="+to+"&account=Northwind+Ops", nil)
	requireStatus(t, resp, http.StatusOK)
	filtered := decodeBody[usageSummary](t, body)
	if len(filtered.Accounts) != 1 || filtered.Accounts[0].Account != "Northwind Ops" {
		t.Fatalf("account filter returned %+v", filtered.Accounts)
	}

	resp, body = apiCall(t, ts, http.MethodGet,
		"/v1/usage?from="+from+"&to="+to+"&account=acc-01", nil)
	requireStatus(t, resp, http.StatusOK)
	byID := decodeBody[usageSummary](t, body)
	if len(byID.Accounts) != 1 || byID.Accounts[0].Account != "Northwind Ops" {
		t.Fatalf("account id filter returned %+v", byID.Accounts)
	}

	resp, body = apiCall(t, ts, http.MethodGet, "/v1/usage?from=not-a-date", nil)
	requireStatus(t, resp, http.StatusBadRequest)
	requireErrorCode(t, body, CodeInvalidArgument)

	// A reversed window and a window beyond usage.MaxWindowDays are both 400.
	resp, body = apiCall(t, ts, http.MethodGet, "/v1/usage?from=2026-10-25&to=2026-10-24", nil)
	requireStatus(t, resp, http.StatusBadRequest)
	requireErrorCode(t, body, CodeInvalidArgument)

	resp, body = apiCall(t, ts, http.MethodGet, "/v1/usage?from=2000-01-01&to=2026-10-24", nil)
	requireStatus(t, resp, http.StatusBadRequest)
	requireErrorCode(t, body, CodeInvalidArgument)
}

// TestUsageDateOnlyBoundsCoverFinalDay pins the P2-5 fix: a bare YYYY-MM-DD
// `to` is the end of that UTC day, so a row at 23:59:59Z is included, while an
// explicit midnight still behaves as an exact instant.
func TestUsageDateOnlyBoundsCoverFinalDay(t *testing.T) {
	ts, st := newPlatformServer(t, Config{Usage: usageAggregator()})
	rows := []model.RequestLog{
		{ID: "boundary-midnight", TS: "2026-10-01T00:00:00Z", AccountLabel: "Northwind Ops", Target: "a.example:443", Status: 200, Bytes: 10, LatencyMs: 1, Source: model.SourceGate},
		{ID: "boundary-last-second", TS: "2026-10-01T23:59:59Z", AccountLabel: "Northwind Ops", Target: "b.example:443", Status: 200, Bytes: 20, LatencyMs: 1, Source: model.SourceGate},
		{ID: "boundary-next-day", TS: "2026-10-02T00:00:00Z", AccountLabel: "Northwind Ops", Target: "c.example:443", Status: 200, Bytes: 40, LatencyMs: 1, Source: model.SourceGate},
	}
	for _, row := range rows {
		st.AppendRequest(row)
	}

	cases := []struct {
		name  string
		query string
		want  int
		bytes int64
	}{
		{"date-only to includes the whole day", "from=2026-10-01&to=2026-10-01", 2, 30},
		{"explicit end of day agrees", "from=2026-10-01&to=2026-10-01T23:59:59Z", 2, 30},
		{"explicit midnight is exact", "from=2026-10-01&to=2026-10-01T00:00:00Z", 1, 10},
		{"date-only from starts at midnight", "from=2026-10-02&to=2026-10-02", 1, 40},
		{"two-day date-only window", "from=2026-10-01&to=2026-10-02", 3, 70},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := apiCall(t, ts, http.MethodGet, "/v1/usage?account=Northwind+Ops&"+tc.query, nil)
			requireStatus(t, resp, http.StatusOK)
			summary := decodeBody[usageSummary](t, body)
			if summary.Requests != tc.want || summary.Bytes != tc.bytes {
				t.Fatalf("%s: requests/bytes = %d/%d, want %d/%d (body %s)",
					tc.query, summary.Requests, summary.Bytes, tc.want, tc.bytes, body)
			}
		})
	}
}

// usageSummary mirrors the fields this test asserts; the wire type itself is
// usage.Summary.
type usageSummary struct {
	Requests      int   `json:"requests"`
	Bytes         int64 `json:"bytes"`
	RowsRetained  int   `json:"rowsRetained"`
	RetentionDays int   `json:"retentionDays"`
	QuotaEnforced bool  `json:"quotaEnforced"`
	Days          []struct {
		Day string `json:"day"`
	} `json:"days"`
	Accounts []struct {
		Account string `json:"account"`
	} `json:"accounts"`
}

func usageAggregator() *usage.Aggregator {
	return usage.New(usage.Config{})
}

func TestHealthNodesDisabled(t *testing.T) {
	ts, _ := newPlatformServer(t, Config{})
	resp, body := apiCall(t, ts, http.MethodGet, "/v1/health/nodes", nil)
	requireStatus(t, resp, http.StatusOK)
	var payload struct {
		Enabled bool              `json:"enabled"`
		Reason  string            `json:"reason"`
		Nodes   []json.RawMessage `json:"nodes"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode health body %q: %v", body, err)
	}
	if payload.Enabled {
		t.Fatal("health reported enabled without a sampler")
	}
	if payload.Reason == "" {
		t.Fatal("disabled health must carry a reason")
	}
	if payload.Nodes == nil {
		t.Fatalf("disabled health nodes must be an empty array, got %s", body)
	}
}

func TestHealthNodesReportsWatchedNode(t *testing.T) {
	sampler := health.New(health.Config{
		Interval: time.Hour,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			client, server := net.Pipe()
			_ = server.Close()
			return client, nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sampler.Start(ctx)
	t.Cleanup(sampler.Stop)

	ts, st := newPlatformServer(t, Config{Health: sampler})
	if _, err := st.PutNode(model.ProxyNode{
		ID: "node-1", AccountID: "acc-01", Label: "test-node-one", Region: "test-region",
		State: model.NodeLive, GatePort: 20001, Capacity: 10, Protocol: model.ProtocolSOCKS5,
	}); err != nil {
		t.Fatalf("put node: %v", err)
	}
	if err := sampler.Watch("node-1", 20001); err != nil {
		t.Fatalf("watch: %v", err)
	}
	waitForSample(t, sampler, "node-1")

	resp, body := apiCall(t, ts, http.MethodGet, "/v1/health/nodes", nil)
	requireStatus(t, resp, http.StatusOK)
	var payload struct {
		Enabled bool             `json:"enabled"`
		Nodes   []healthNodeView `json:"nodes"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode health body %q: %v", body, err)
	}
	if !payload.Enabled || len(payload.Nodes) != 1 {
		t.Fatalf("health payload = %+v, want one watched node", payload)
	}
	node := payload.Nodes[0]
	if node.NodeID != "node-1" || node.State != health.StateOK || node.Count == 0 || node.OK == 0 {
		t.Fatalf("health node = %+v, want an ok sample", node)
	}
	if node.Last == nil || !node.Last.OK {
		t.Fatalf("health node has no successful last sample: %+v", node)
	}
}

func waitForSample(t *testing.T, sampler *health.Sampler, key string) health.Stats {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if stats, ok := sampler.Stats(key); ok && stats.Count > 0 {
			return stats
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("sampler recorded no sample for %q", key)
	return health.Stats{}
}

func TestWebhookCRUDAndDeliveryRing(t *testing.T) {
	dispatcher := webhook.New(webhook.Config{
		Workers: 1, Backoff: []time.Duration{0},
		// The test receiver is an httptest loopback URL.
		AllowPrivate: true,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	t.Cleanup(func() { _ = dispatcher.Close() })
	ts, _ := newPlatformServer(t, Config{Webhooks: dispatcher})

	resp, body := apiCall(t, ts, http.MethodPost, "/v1/webhooks", map[string]any{
		"url":         "http://127.0.0.1:9/hook",
		"events":      []string{"account.login", "node.connected"},
		"description": "test receiver",
	})
	requireStatus(t, resp, http.StatusCreated)
	created := decodeBody[webhookCreated](t, body)
	if created.ID == "" || created.Secret == "" {
		t.Fatalf("create returned id=%q secret-empty=%t", created.ID, created.Secret == "")
	}
	if !created.SecretAvailable || !created.Enabled {
		t.Fatalf("created endpoint = %+v, want an enabled endpoint with a secret", created)
	}
	secret := created.Secret

	// The list must never expose the secret.
	resp, body = apiCall(t, ts, http.MethodGet, "/v1/webhooks", nil)
	requireStatus(t, resp, http.StatusOK)
	list := decodeBody[[]webhookEndpointView](t, body)
	if len(list) != 1 || list[0].ID != created.ID || !list[0].SecretAvailable {
		t.Fatalf("list = %+v, want the created endpoint", list)
	}
	if strings.Contains(string(body), secret) {
		t.Fatalf("list response leaked the signing secret: %s", body)
	}

	resp, body = apiCall(t, ts, http.MethodGet, "/v1/webhooks/"+created.ID+"/deliveries", nil)
	requireStatus(t, resp, http.StatusOK)
	deliveries := decodeBody[[]webhook.Delivery](t, body)
	if len(deliveries) != 0 {
		t.Fatalf("deliveries = %+v, want empty", deliveries)
	}

	resp, body = apiCall(t, ts, http.MethodPatch, "/v1/webhooks/"+created.ID, map[string]any{"enabled": false})
	requireStatus(t, resp, http.StatusOK)
	patched := decodeBody[webhookEndpointView](t, body)
	if patched.Enabled {
		t.Fatalf("patch did not disable the endpoint: %+v", patched)
	}

	resp, body = apiCall(t, ts, http.MethodDelete, "/v1/webhooks/"+created.ID, nil)
	requireStatus(t, resp, http.StatusNoContent)

	resp, body = apiCall(t, ts, http.MethodGet, "/v1/webhooks", nil)
	requireStatus(t, resp, http.StatusOK)
	if list := decodeBody[[]webhookEndpointView](t, body); len(list) != 0 {
		t.Fatalf("list after delete = %+v, want empty", list)
	}

	resp, body = apiCall(t, ts, http.MethodGet, "/v1/webhooks/"+created.ID+"/deliveries", nil)
	requireStatus(t, resp, http.StatusNotFound)
	requireErrorCode(t, body, CodeNotFound)
}

func TestWebhookCreateRollsBackInvalidReceiver(t *testing.T) {
	dispatcher := webhook.New(webhook.Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	t.Cleanup(func() { _ = dispatcher.Close() })
	ts, _ := newPlatformServer(t, Config{Webhooks: dispatcher})

	resp, body := apiCall(t, ts, http.MethodPost, "/v1/webhooks", map[string]any{
		"url": "ftp://example.com/hook",
	})
	requireStatus(t, resp, http.StatusBadRequest)
	requireErrorCode(t, body, CodeInvalidArgument)

	resp, body = apiCall(t, ts, http.MethodGet, "/v1/webhooks", nil)
	requireStatus(t, resp, http.StatusOK)
	if list := decodeBody[[]webhookEndpointView](t, body); len(list) != 0 {
		t.Fatalf("invalid receiver was not rolled back: %+v", list)
	}
}

func TestWebhookDisabled(t *testing.T) {
	ts, _ := newPlatformServer(t, Config{})
	resp, body := apiCall(t, ts, http.MethodPost, "/v1/webhooks", map[string]any{"url": "https://example.com/hook"})
	requireStatus(t, resp, http.StatusServiceUnavailable)
	requireErrorCode(t, body, CodeUpstreamUnavailable)

	resp, body = apiCall(t, ts, http.MethodGet, "/v1/webhooks", nil)
	requireStatus(t, resp, http.StatusOK)
	if list := decodeBody[[]webhookEndpointView](t, body); len(list) != 0 {
		t.Fatalf("disabled webhook list = %+v, want empty", list)
	}
}

type recordedEvent struct {
	event webhook.EventType
	data  map[string]any
}

type recordingSink struct {
	mu     sync.Mutex
	events []recordedEvent
}

func (s *recordingSink) Emit(event webhook.EventType, data map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, recordedEvent{event: event, data: data})
}

func (s *recordingSink) types() []webhook.EventType {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]webhook.EventType, 0, len(s.events))
	for _, event := range s.events {
		out = append(out, event.event)
	}
	return out
}

func (s *recordingSink) count(event webhook.EventType) int {
	count := 0
	for _, got := range s.types() {
		if got == event {
			count++
		}
	}
	return count
}

type recordingHealth struct {
	mu        sync.Mutex
	watched   map[string]int
	unwatched []string
}

func (h *recordingHealth) Watch(key string, port int) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.watched == nil {
		h.watched = make(map[string]int)
	}
	h.watched[key] = port
	return nil
}

func (h *recordingHealth) Unwatch(key string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.watched, key)
	h.unwatched = append(h.unwatched, key)
}

func (h *recordingHealth) Stats(string) (health.Stats, bool) { return health.Stats{}, false }

func (h *recordingHealth) watchPort(key string) (int, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	port, ok := h.watched[key]
	return port, ok
}

func TestEventEmissionAndHealthLifecycle(t *testing.T) {
	catalog := &fakeCatalog{}
	sink := &recordingSink{}
	probes := &recordingHealth{}
	ts, _ := newPlatformServer(t, Config{
		Catalog: catalog,
		Events:  sink,
		Health:  probes,
	})

	resp, _ := apiCall(t, ts, http.MethodPost, "/v1/accounts/acc-01/actions/login", nil)
	requireStatus(t, resp, http.StatusOK)
	if sink.count(webhook.EventAccountLogin) != 1 {
		t.Fatalf("login emitted %v, want account.login", sink.types())
	}

	catalog.connect = runtime.SwitchResult{
		AccountID: "acc-01", NodeID: "acc-01:srv-1", ServerID: "srv-1",
		NodeState: model.NodeLive, GatePort: 20001,
	}
	resp, _ = apiCall(t, ts, http.MethodPost, "/v1/accounts/acc-01/actions/connect", map[string]any{
		"confirm": true, "serverId": "srv-1",
	})
	requireStatus(t, resp, http.StatusOK)
	if sink.count(webhook.EventNodeConnected) != 1 {
		t.Fatalf("connect emitted %v, want node.connected", sink.types())
	}
	if port, ok := probes.watchPort("acc-01:srv-1"); !ok || port != 20001 {
		t.Fatalf("health did not watch the connected gate: port=%d ok=%t", port, ok)
	}

	// A switch emits node.switched and drops the old gate from the sampler.
	catalog.connect = runtime.SwitchResult{
		AccountID: "acc-01", NodeID: "acc-01:srv-2", ServerID: "srv-2", SwitchedFrom: "srv-1",
		NodeState: model.NodeLive, GatePort: 20002,
	}
	resp, _ = apiCall(t, ts, http.MethodPost, "/v1/accounts/acc-01/actions/connect", map[string]any{
		"confirm": true, "serverId": "srv-2",
	})
	requireStatus(t, resp, http.StatusOK)
	if sink.count(webhook.EventNodeSwitched) != 1 {
		t.Fatalf("switch emitted %v, want node.switched", sink.types())
	}
	if _, ok := probes.watchPort("acc-01:srv-1"); ok {
		t.Fatal("switch kept sampling the old gate")
	}
	if port, ok := probes.watchPort("acc-01:srv-2"); !ok || port != 20002 {
		t.Fatalf("health did not watch the switched gate: port=%d ok=%t", port, ok)
	}

	catalog.disconnect = runtime.DisconnectResult{
		AccountID: "acc-01", NodeID: "acc-01:srv-2", ServerID: "srv-2", ActiveFlows: 1,
	}
	resp, _ = apiCall(t, ts, http.MethodPost, "/v1/accounts/acc-01/actions/disconnect", map[string]any{
		"confirm": true,
	})
	requireStatus(t, resp, http.StatusOK)
	if sink.count(webhook.EventNodeDisconnected) != 1 {
		t.Fatalf("disconnect emitted %v, want node.disconnected", sink.types())
	}
	if _, ok := probes.watchPort("acc-01:srv-2"); ok {
		t.Fatal("disconnect kept sampling the closed gate")
	}

	resp, _ = apiCall(t, ts, http.MethodPost, "/v1/accounts/acc-01/actions/logout", nil)
	requireStatus(t, resp, http.StatusOK)
	if sink.count(webhook.EventAccountLogout) != 1 {
		t.Fatalf("logout emitted %v, want account.logout", sink.types())
	}
}

func TestWebhookDeliveriesMergePersistedAndRing(t *testing.T) {
	dispatcher := webhook.New(webhook.Config{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	t.Cleanup(func() { _ = dispatcher.Close() })
	ts, st := newPlatformServer(t, Config{Webhooks: dispatcher})

	resp, body := apiCall(t, ts, http.MethodPost, "/v1/webhooks", map[string]any{
		"url": "https://example.com/hook",
	})
	requireStatus(t, resp, http.StatusCreated)
	created := decodeBody[webhookCreated](t, body)

	base := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	for _, row := range []model.WebhookDelivery{
		{
			ID: "evt-a:1", EndpointID: created.ID, Event: "account.login", Attempt: 1,
			Status: "success", HTTPStatus: 200, At: base.Format(time.RFC3339Nano), DurationMs: 4,
		},
		{
			ID: "evt-b:1", EndpointID: created.ID, Event: "node.connected", Attempt: 1,
			Status: "failed", HTTPStatus: 503, Error: "http status 503",
			At: base.Add(time.Second).Format(time.RFC3339Nano), DurationMs: 9,
		},
	} {
		if err := st.AppendWebhookDelivery(row); err != nil {
			t.Fatalf("append %q: %v", row.ID, err)
		}
	}
	// The ring repeats evt-b:1 (the store copy must win the dedupe) and adds a
	// newer attempt the store has not seen.
	dispatcher.Log().Append(webhook.Delivery{
		ID: "evt-b:1", EndpointID: created.ID, Event: webhook.EventNodeConnected, Attempt: 1,
		Status: webhook.StatusFailed, HTTPStatus: 503, At: base.Add(time.Second),
	})
	dispatcher.Log().Append(webhook.Delivery{
		ID: "evt-c:1", EndpointID: created.ID, Event: webhook.EventNodeDisconnected, Attempt: 1,
		Status: webhook.StatusSuccess, HTTPStatus: 204, At: base.Add(2 * time.Second),
	})

	resp, body = apiCall(t, ts, http.MethodGet, "/v1/webhooks/"+created.ID+"/deliveries", nil)
	requireStatus(t, resp, http.StatusOK)
	deliveries := decodeBody[[]webhook.Delivery](t, body)
	ids := make([]string, 0, len(deliveries))
	for _, delivery := range deliveries {
		ids = append(ids, delivery.ID)
	}
	want := []string{"evt-c:1", "evt-b:1", "evt-a:1"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("deliveries = %v, want %v (newest first, deduped)", ids, want)
	}
	if deliveries[1].Error != "http status 503" || deliveries[1].DurationMs != 9 {
		t.Fatalf("dedupe kept the ring copy instead of the persisted row: %+v", deliveries[1])
	}

	resp, body = apiCall(t, ts, http.MethodGet, "/v1/webhooks/"+created.ID+"/deliveries?limit=2", nil)
	requireStatus(t, resp, http.StatusOK)
	if page := decodeBody[[]webhook.Delivery](t, body); len(page) != 2 || page[0].ID != "evt-c:1" || page[1].ID != "evt-b:1" {
		t.Fatalf("limit=2 returned %+v, want the two newest", page)
	}

	for _, raw := range []string{"0", "-1", "many"} {
		resp, body = apiCall(t, ts, http.MethodGet, "/v1/webhooks/"+created.ID+"/deliveries?limit="+raw, nil)
		requireStatus(t, resp, http.StatusBadRequest)
		requireErrorCode(t, body, CodeInvalidArgument)
	}
}

func TestWebhookDeliveriesSurviveRestart(t *testing.T) {
	st := store.NewEmpty()
	endpoint, err := st.CreateWebhookEndpoint(model.WebhookEndpointInput{URL: "https://example.com/hook"})
	if err != nil {
		t.Fatalf("create endpoint: %v", err)
	}
	base := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	for i := 1; i <= 3; i++ {
		row := model.WebhookDelivery{
			ID:         "evt-1:" + strconv.Itoa(i),
			EndpointID: endpoint.ID,
			Event:      "account.login",
			Attempt:    i,
			Status:     "success",
			HTTPStatus: 200,
			At:         base.Add(time.Duration(i) * time.Second).Format(time.RFC3339Nano),
			DurationMs: int64(i),
		}
		if err := st.AppendWebhookDelivery(row); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	data, err := st.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	restored := store.NewEmpty()
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}

	// A fresh dispatcher has an empty ring: the response can only come from
	// the reloaded snapshot.
	dispatcher := webhook.New(webhook.Config{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	t.Cleanup(func() { _ = dispatcher.Close() })
	server := New(Config{AdminToken: testToken, Webhooks: dispatcher}, restored, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)

	resp, body := apiCall(t, ts, http.MethodGet, "/v1/webhooks/"+endpoint.ID+"/deliveries", nil)
	requireStatus(t, resp, http.StatusOK)
	deliveries := decodeBody[[]webhook.Delivery](t, body)
	if len(deliveries) != 3 {
		t.Fatalf("restored deliveries = %+v, want 3 rows", deliveries)
	}
	for i, delivery := range deliveries {
		wantID := "evt-1:" + strconv.Itoa(3-i)
		if delivery.ID != wantID {
			t.Fatalf("delivery %d = %q, want %q (newest first)", i, delivery.ID, wantID)
		}
		if !delivery.At.Equal(base.Add(time.Duration(3-i) * time.Second)) {
			t.Fatalf("delivery %d timestamp = %s", i, delivery.At)
		}
	}
}

func TestWebhookDeliveriesWithoutDispatcher(t *testing.T) {
	ts, st := newPlatformServer(t, Config{})
	endpoint, err := st.CreateWebhookEndpoint(model.WebhookEndpointInput{URL: "https://example.com/hook"})
	if err != nil {
		t.Fatalf("create endpoint: %v", err)
	}
	if err := st.AppendWebhookDelivery(model.WebhookDelivery{
		ID: "evt-1:1", EndpointID: endpoint.ID, Event: "account.login", Attempt: 1,
		Status: "success", HTTPStatus: 200,
		At: time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("append: %v", err)
	}

	resp, body := apiCall(t, ts, http.MethodGet, "/v1/webhooks/"+endpoint.ID+"/deliveries", nil)
	requireStatus(t, resp, http.StatusOK)
	deliveries := decodeBody[[]webhook.Delivery](t, body)
	if len(deliveries) != 1 || deliveries[0].ID != "evt-1:1" {
		t.Fatalf("deliveries without a dispatcher = %+v, want the persisted row", deliveries)
	}
}
