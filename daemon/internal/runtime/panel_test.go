package runtime_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"sync"
	"testing"
	"time"

	"ajsaccountd/internal/api"
	"ajsaccountd/internal/model"
	"ajsaccountd/internal/publisher"
	"ajsaccountd/internal/resin"
	"ajsaccountd/internal/runtime"
	"ajsaccountd/internal/store"
)

const (
	testToken = "test-admin-token-not-a-credential"
	// wantContent is the seed's eight LIVE nodes rendered by
	// publisher.Render: sorted by tag, one socks5 line each.
	wantContent = "socks5://127.0.0.1:13001#cn-sh-01\n" +
		"socks5://127.0.0.1:13000#cn-zj-01\n" +
		"socks5://127.0.0.1:13008#de-fra-01\n" +
		"socks5://127.0.0.1:13005#hk-02\n" +
		"socks5://127.0.0.1:13003#jp-tyo-01\n" +
		"socks5://127.0.0.1:13004#sg-01\n" +
		"socks5://127.0.0.1:13006#tw-tpe-01\n" +
		"socks5://127.0.0.1:13009#uk-lon-01\n"
	// wantContentAfterDrain drops node-06 (hk-02, DRAINING after the test).
	wantContentAfterDrain = "socks5://127.0.0.1:13001#cn-sh-01\n" +
		"socks5://127.0.0.1:13000#cn-zj-01\n" +
		"socks5://127.0.0.1:13008#de-fra-01\n" +
		"socks5://127.0.0.1:13003#jp-tyo-01\n" +
		"socks5://127.0.0.1:13004#sg-01\n" +
		"socks5://127.0.0.1:13006#tw-tpe-01\n" +
		"socks5://127.0.0.1:13009#uk-lon-01\n"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeClock makes publish coalescing deterministic: Sleep advances the same
// clock Now reads.
type fakeClock struct {
	t      time.Time
	sleeps []time.Duration
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time { return c.t }

func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.sleeps = append(c.sleeps, d)
	c.t = c.t.Add(d)
	return nil
}

// fakeCall is one request observed by the fake Resin admin API.
type fakeCall struct {
	Method string
	Path   string
	Body   []byte
}

// fakeResin implements the admin routes the runtime package uses. Failure
// switches return a 500 (or the configured status) so error propagation can
// be tested without a live Resin.
type fakeResin struct {
	t *testing.T

	mu           sync.Mutex
	calls        []fakeCall
	subscription *resin.Subscription
	platform     *resin.Platform
	version      string

	systemConfigStatus       int
	subscriptionCreateStatus int
	subscriptionPatchStatus  int
	platformCreateStatus     int
	refreshStatus            int

	// leases is the fake's per-account lease table. DELETE records the call
	// but keeps the row, so a follow-up GetLease can return the post-rotation
	// lease the panel is expected to apply.
	leases            map[string]resin.Lease
	leaseDeleteStatus int

	// leaseDeleteStarted/Release block a DELETE so a test can hold a rotation
	// open and prove a concurrent reconcile waits for it.
	leaseDeleteStarted chan struct{}
	leaseDeleteRelease chan struct{}
	leaseDeleteOnce    sync.Once
}

func newFakeResin(t *testing.T) *fakeResin {
	t.Helper()
	return &fakeResin{t: t, version: "v1.3.0", leases: make(map[string]resin.Lease)}
}

func (f *fakeResin) recorded() []fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeCall(nil), f.calls...)
}

// paths returns "METHOD path" for every recorded call, in order.
func (f *fakeResin) paths() []string {
	calls := f.recorded()
	out := make([]string, 0, len(calls))
	for _, call := range calls {
		out = append(out, call.Method+" "+call.Path)
	}
	return out
}

func (f *fakeResin) count(method, pathPrefix string) int {
	n := 0
	for _, call := range f.recorded() {
		if call.Method == method && strings.HasPrefix(call.Path, pathPrefix) {
			n++
		}
	}
	return n
}

// countExact counts calls whose path equals path exactly.
func (f *fakeResin) countExact(method, path string) int {
	n := 0
	for _, call := range f.recorded() {
		if call.Method == method && call.Path == path {
			n++
		}
	}
	return n
}

// bodyFor returns the body of the first call with this exact method and path.
func (f *fakeResin) bodyFor(method, path string) []byte {
	for _, call := range f.recorded() {
		if call.Method == method && call.Path == path {
			return call.Body
		}
	}
	f.t.Fatalf("no %s %s call recorded", method, path)
	return nil
}

func (f *fakeResin) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = nil
}

func (f *fakeResin) subscriptionContent() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.subscription == nil {
		return ""
	}
	return f.subscription.Content
}

func (f *fakeResin) platformValue() resin.Platform {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.platform == nil {
		return resin.Platform{}
	}
	return *f.platform
}

func (f *fakeResin) setLease(account string, lease resin.Lease) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.leases[account] = lease
}

func (f *fakeResin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.calls = append(f.calls, fakeCall{Method: r.Method, Path: r.URL.Path, Body: body})
	f.mu.Unlock()

	switch {
	case r.Method == http.MethodPatch && r.URL.Path == "/api/v1/system/config":
		f.mu.Lock()
		status := f.systemConfigStatus
		f.mu.Unlock()
		if status != 0 {
			writeError(w, status, "INVALID_ARGUMENT", "runtime config unsupported")
			return
		}
		w.WriteHeader(http.StatusNoContent)

	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/subscriptions":
		f.mu.Lock()
		sub := f.subscription
		f.mu.Unlock()
		items := []resin.Subscription{}
		if sub != nil {
			items = append(items, *sub)
		}
		writeJSON(w, http.StatusOK, resin.Page[resin.Subscription]{Items: items, Total: len(items)})

	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/subscriptions":
		var req resin.CreateSubscriptionRequest
		if err := json.Unmarshal(body, &req); err != nil {
			f.t.Errorf("decode subscription create: %v", err)
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "bad body")
			return
		}
		f.mu.Lock()
		status := f.subscriptionCreateStatus
		f.mu.Unlock()
		if status != 0 {
			writeError(w, status, "INTERNAL", "subscription create failed")
			return
		}
		sub := &resin.Subscription{
			ID:             "sub-1",
			Name:           req.Name,
			SourceType:     req.SourceType,
			Content:        req.Content,
			UpdateInterval: req.UpdateInterval,
			Ephemeral:      req.Ephemeral,
			Enabled:        true,
		}
		f.mu.Lock()
		f.subscription = sub
		f.mu.Unlock()
		writeJSON(w, http.StatusCreated, sub)

	case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/api/v1/subscriptions/"):
		var patch resin.SubscriptionPatch
		if err := json.Unmarshal(body, &patch); err != nil {
			f.t.Errorf("decode subscription patch: %v", err)
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "bad body")
			return
		}
		f.mu.Lock()
		status := f.subscriptionPatchStatus
		if status != 0 {
			f.mu.Unlock()
			writeError(w, status, "INTERNAL", "subscription patch failed")
			return
		}
		if f.subscription == nil {
			f.mu.Unlock()
			writeError(w, http.StatusNotFound, "NOT_FOUND", "subscription not found")
			return
		}
		if patch.Content != nil {
			f.subscription.Content = *patch.Content
		}
		if patch.UpdateInterval != nil {
			f.subscription.UpdateInterval = *patch.UpdateInterval
		}
		sub := *f.subscription
		f.mu.Unlock()
		writeJSON(w, http.StatusOK, sub)

	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/actions/refresh"):
		f.mu.Lock()
		status := f.refreshStatus
		f.mu.Unlock()
		if status != 0 {
			writeError(w, status, "INTERNAL", "refresh failed")
			return
		}
		w.WriteHeader(http.StatusNoContent)

	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/platforms":
		f.mu.Lock()
		platform := f.platform
		f.mu.Unlock()
		items := []resin.Platform{}
		if platform != nil {
			items = append(items, *platform)
		}
		writeJSON(w, http.StatusOK, resin.Page[resin.Platform]{Items: items, Total: len(items)})

	case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/leases/"):
		account := path.Base(r.URL.Path)
		f.mu.Lock()
		lease, ok := f.leases[account]
		f.mu.Unlock()
		if !ok {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "lease not found")
			return
		}
		writeJSON(w, http.StatusOK, lease)

	case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/leases/"):
		f.mu.Lock()
		status := f.leaseDeleteStatus
		started := f.leaseDeleteStarted
		release := f.leaseDeleteRelease
		f.mu.Unlock()
		if started != nil {
			f.leaseDeleteOnce.Do(func() { close(started) })
		}
		if release != nil {
			<-release
		}
		if status != 0 {
			writeError(w, status, "INTERNAL", "lease delete failed")
			return
		}
		w.WriteHeader(http.StatusNoContent)

	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/platforms/"):
		f.mu.Lock()
		platform := f.platform
		f.mu.Unlock()
		if platform == nil {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "platform not found")
			return
		}
		writeJSON(w, http.StatusOK, *platform)

	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/platforms":
		var req resin.CreatePlatformRequest
		if err := json.Unmarshal(body, &req); err != nil {
			f.t.Errorf("decode platform create: %v", err)
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "bad body")
			return
		}
		f.mu.Lock()
		status := f.platformCreateStatus
		f.mu.Unlock()
		if status != 0 {
			writeError(w, status, "INTERNAL", "platform create failed")
			return
		}
		platform := &resin.Platform{
			ID:               "plat-1",
			Name:             req.Name,
			StickyTTL:        req.StickyTTL,
			RegexFilters:     req.RegexFilters,
			RegionFilters:    req.RegionFilters,
			AllocationPolicy: req.AllocationPolicy,
		}
		if req.PassiveCircuitBreakerDisabled != nil {
			platform.PassiveCircuitBreakerDisabled = *req.PassiveCircuitBreakerDisabled
		}
		f.mu.Lock()
		f.platform = platform
		f.mu.Unlock()
		writeJSON(w, http.StatusCreated, platform)

	case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/api/v1/platforms/"):
		var patch resin.UpdatePlatformRequest
		if err := json.Unmarshal(body, &patch); err != nil {
			f.t.Errorf("decode platform patch: %v", err)
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "bad body")
			return
		}
		f.mu.Lock()
		if f.platform == nil {
			f.mu.Unlock()
			writeError(w, http.StatusNotFound, "NOT_FOUND", "platform not found")
			return
		}
		if patch.StickyTTL != nil {
			f.platform.StickyTTL = *patch.StickyTTL
		}
		if patch.RegexFilters != nil {
			f.platform.RegexFilters = *patch.RegexFilters
		}
		if patch.RegionFilters != nil {
			f.platform.RegionFilters = *patch.RegionFilters
		}
		if patch.AllocationPolicy != nil {
			f.platform.AllocationPolicy = *patch.AllocationPolicy
		}
		if patch.PassiveCircuitBreakerDisabled != nil {
			f.platform.PassiveCircuitBreakerDisabled = *patch.PassiveCircuitBreakerDisabled
		}
		platform := *f.platform
		f.mu.Unlock()
		writeJSON(w, http.StatusOK, platform)

	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/system/info":
		f.mu.Lock()
		version := f.version
		f.mu.Unlock()
		writeJSON(w, http.StatusOK, resin.SystemInfo{Version: version, GitCommit: "5473c7ee"})

	default:
		f.t.Errorf("unexpected Resin admin request %s %s", r.Method, r.URL.Path)
		writeError(w, http.StatusNotFound, "NOT_FOUND", "no route")
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

// newTestPanel wires a real resin.Client and publisher against the fake admin
// server. No Resin process or network call is involved.
func newTestPanel(t *testing.T, st *store.Store) (*runtime.ResinPanel, *fakeResin, *fakeClock) {
	t.Helper()
	fake := newFakeResin(t)
	ts := httptest.NewServer(fake)
	t.Cleanup(ts.Close)

	client, err := resin.New(resin.Config{BaseURL: ts.URL, AdminToken: testToken, Logger: discardLogger()})
	if err != nil {
		t.Fatalf("resin.New: %v", err)
	}
	clock := newFakeClock()
	pub := publisher.New(client, publisher.Config{
		Now:    clock.Now,
		Sleep:  clock.Sleep,
		Logger: discardLogger(),
	})
	panel, err := runtime.NewResinPanel(st, client, pub, runtime.Config{Logger: discardLogger()})
	if err != nil {
		t.Fatalf("NewResinPanel: %v", err)
	}
	return panel, fake, clock
}

func TestStartCallOrderAndPayloads(t *testing.T) {
	st := store.New()
	panel, fake, _ := newTestPanel(t, st)

	if err := panel.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	want := []string{
		"PATCH /api/v1/system/config",
		"GET /api/v1/subscriptions",
		"POST /api/v1/subscriptions",
		"GET /api/v1/platforms",
		"POST /api/v1/platforms",
		"PATCH /api/v1/subscriptions/sub-1",
		"POST /api/v1/subscriptions/sub-1/actions/refresh",
		"GET /api/v1/system/info",
	}
	got := fake.paths()
	if len(got) != len(want) {
		t.Fatalf("startup calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("startup call %d = %q, want %q (all: %v)", i, got[i], want[i], got)
		}
	}

	var configPatch map[string]string
	if err := json.Unmarshal(fake.bodyFor(http.MethodPatch, "/api/v1/system/config"), &configPatch); err != nil {
		t.Fatalf("decode system config: %v", err)
	}
	if configPatch["max_latency_test_interval"] != "30s" {
		t.Fatalf("system config = %v, want max_latency_test_interval=30s", configPatch)
	}

	var create resin.CreateSubscriptionRequest
	if err := json.Unmarshal(fake.bodyFor(http.MethodPost, "/api/v1/subscriptions"), &create); err != nil {
		t.Fatalf("decode subscription create: %v", err)
	}
	if create.Name != "ajs-live" || create.SourceType != "local" || !create.Ephemeral {
		t.Fatalf("subscription create = %+v", create)
	}
	if create.UpdateInterval != "30s" {
		t.Fatalf("update_interval = %q, want 30s", create.UpdateInterval)
	}
	if create.Content != wantContent {
		t.Fatalf("subscription content =\n%s\nwant\n%s", create.Content, wantContent)
	}

	var platform resin.CreatePlatformRequest
	if err := json.Unmarshal(fake.bodyFor(http.MethodPost, "/api/v1/platforms"), &platform); err != nil {
		t.Fatalf("decode platform create: %v", err)
	}
	if platform.Name != "ajs-live" || platform.StickyTTL != "8h" || platform.AllocationPolicy != "BALANCED" {
		t.Fatalf("platform create = %+v", platform)
	}
	if len(platform.RegexFilters) != 1 || platform.RegexFilters[0] != "ajs-live/.*" {
		t.Fatalf("regex_filters = %v, want [ajs-live/.*]", platform.RegexFilters)
	}

	var contentPatch resin.SubscriptionPatch
	if err := json.Unmarshal(fake.bodyFor(http.MethodPatch, "/api/v1/subscriptions/sub-1"), &contentPatch); err != nil {
		t.Fatalf("decode subscription patch: %v", err)
	}
	if contentPatch.Content == nil || *contentPatch.Content != wantContent {
		t.Fatalf("published content = %v, want the rendered nodes", contentPatch.Content)
	}
}

func TestTopologyContent(t *testing.T) {
	panel, _, _ := newTestPanel(t, store.New())
	if err := panel.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	topology, err := panel.Topology(context.Background())
	if err != nil {
		t.Fatalf("Topology: %v", err)
	}
	if topology.SubscriptionID != "sub-1" || topology.PlatformID != "plat-1" {
		t.Fatalf("ids = %q/%q, want sub-1/plat-1", topology.SubscriptionID, topology.PlatformID)
	}
	if topology.Content != wantContent {
		t.Fatalf("content =\n%s\nwant\n%s", topology.Content, wantContent)
	}
	if topology.LastGood != wantContent {
		t.Fatalf("last_good =\n%s\nwant\n%s", topology.LastGood, wantContent)
	}
	if topology.ResinVersion != "v1.3.0" {
		t.Fatalf("resin_version = %q, want v1.3.0", topology.ResinVersion)
	}
	if len(topology.Nodes) != 8 {
		t.Fatalf("nodes = %v, want 8 LIVE entries", topology.Nodes)
	}
	if topology.Nodes["node-01"] != "cn-zj-01" || topology.Nodes["node-10"] != "uk-lon-01" {
		t.Fatalf("unexpected node tags: %v", topology.Nodes)
	}
	for _, excluded := range []string{"node-03", "node-08", "node-11", "node-12"} {
		if _, ok := topology.Nodes[excluded]; ok {
			t.Fatalf("%s is not LIVE but appears in the topology", excluded)
		}
	}

	raw, err := json.Marshal(topology)
	if err != nil {
		t.Fatalf("marshal topology: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("decode topology: %v", err)
	}
	wantFields := []string{"subscription_id", "platform_id", "content", "nodes", "last_good", "resin_version"}
	if len(fields) != len(wantFields) {
		t.Fatalf("topology has %d fields, want %d: %s", len(fields), len(wantFields), raw)
	}
	for _, field := range wantFields {
		if _, ok := fields[field]; !ok {
			t.Fatalf("topology is missing %q: %s", field, raw)
		}
	}
	if strings.Contains(string(raw), testToken) {
		t.Fatalf("topology leaked the admin token: %s", raw)
	}
}

func TestReconcileRepublishes(t *testing.T) {
	st := store.New()
	panel, fake, clock := newTestPanel(t, st)
	if err := panel.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	fake.reset()

	if _, err := st.DrainNode("node-06"); err != nil {
		t.Fatalf("DrainNode: %v", err)
	}

	topology, err := panel.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	want := []string{
		"PATCH /api/v1/system/config",
		"GET /api/v1/subscriptions",
		"PATCH /api/v1/subscriptions/sub-1",
		"GET /api/v1/platforms",
		"PATCH /api/v1/subscriptions/sub-1",
		"POST /api/v1/subscriptions/sub-1/actions/refresh",
		"GET /api/v1/system/info",
	}
	got := fake.paths()
	if len(got) != len(want) {
		t.Fatalf("reconcile calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("reconcile call %d = %q, want %q (all: %v)", i, got[i], want[i], got)
		}
	}

	if topology.Content != wantContentAfterDrain || topology.LastGood != wantContentAfterDrain {
		t.Fatalf("reconciled content =\n%s\nwant\n%s", topology.Content, wantContentAfterDrain)
	}
	if len(topology.Nodes) != 7 {
		t.Fatalf("reconciled nodes = %v, want 7", topology.Nodes)
	}
	if _, ok := topology.Nodes["node-06"]; ok {
		t.Fatalf("drained node-06 still in the topology: %v", topology.Nodes)
	}
	if content := fake.subscriptionContent(); content != wantContentAfterDrain {
		t.Fatalf("Resin content =\n%s\nwant\n%s", content, wantContentAfterDrain)
	}
	if len(clock.sleeps) != 1 || clock.sleeps[0] != publisher.DefaultMinPublishInterval {
		t.Fatalf("coalescing sleeps = %v, want [%s]", clock.sleeps, publisher.DefaultMinPublishInterval)
	}
}

func TestEmptyNodeGuard(t *testing.T) {
	st := store.New()
	drainLiveNodes(t, st)
	panel, fake, _ := newTestPanel(t, st)

	if err := panel.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	want := []string{
		"PATCH /api/v1/system/config",
		"GET /api/v1/platforms",
		"POST /api/v1/platforms",
		"GET /api/v1/system/info",
	}
	got := fake.paths()
	if len(got) != len(want) {
		t.Fatalf("empty-pool calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("empty-pool call %d = %q, want %q (all: %v)", i, got[i], want[i], got)
		}
	}
	for _, call := range fake.recorded() {
		if strings.HasPrefix(call.Path, "/api/v1/subscriptions") {
			t.Fatalf("empty pool touched the subscription: %s %s", call.Method, call.Path)
		}
	}

	topology, err := panel.Topology(context.Background())
	if err != nil {
		t.Fatalf("Topology: %v", err)
	}
	if topology.Content != "" || topology.LastGood != "" {
		t.Fatalf("empty pool content/last_good = %q/%q, want empty", topology.Content, topology.LastGood)
	}
	if topology.SubscriptionID != "" {
		t.Fatalf("empty pool subscription_id = %q, want empty", topology.SubscriptionID)
	}
	if topology.PlatformID != "plat-1" {
		t.Fatalf("empty pool platform_id = %q, want plat-1", topology.PlatformID)
	}
	if len(topology.Nodes) != 0 {
		t.Fatalf("empty pool nodes = %v, want none", topology.Nodes)
	}
}

func TestProbeConfigTolerated(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusMethodNotAllowed} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			st := store.New()
			panel, fake, _ := newTestPanel(t, st)
			fake.systemConfigStatus = status

			if err := panel.Start(context.Background()); err != nil {
				t.Fatalf("Start with %d probe config: %v", status, err)
			}
			if got := fake.countExact(http.MethodPost, "/api/v1/subscriptions"); got != 1 {
				t.Fatalf("subscription creates = %d, want 1 (startup continued)", got)
			}
			topology, err := panel.Topology(context.Background())
			if err != nil {
				t.Fatalf("Topology: %v", err)
			}
			if topology.LastGood != wantContent {
				t.Fatalf("last_good =\n%s\nwant\n%s", topology.LastGood, wantContent)
			}
		})
	}
}

func TestStartErrorPropagation(t *testing.T) {
	cases := []struct {
		name      string
		configure func(*fakeResin)
		wantErr   string
		wantCalls int
	}{
		{
			name:      "probe config",
			configure: func(f *fakeResin) { f.systemConfigStatus = http.StatusInternalServerError },
			wantErr:   "max_latency_test_interval",
			wantCalls: 1,
		},
		{
			name:      "subscription create",
			configure: func(f *fakeResin) { f.subscriptionCreateStatus = http.StatusInternalServerError },
			wantErr:   "ensure subscription",
			wantCalls: 3,
		},
		{
			name:      "platform create",
			configure: func(f *fakeResin) { f.platformCreateStatus = http.StatusInternalServerError },
			wantErr:   "ensure platform",
			wantCalls: 5,
		},
		{
			name:      "publish",
			configure: func(f *fakeResin) { f.subscriptionPatchStatus = http.StatusInternalServerError },
			wantErr:   "publish subscription",
			wantCalls: 6,
		},
		{
			name:      "refresh",
			configure: func(f *fakeResin) { f.refreshStatus = http.StatusInternalServerError },
			wantErr:   "refresh subscription",
			wantCalls: 9, // 3 publisher attempts, then it gives up
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			panel, fake, _ := newTestPanel(t, store.New())
			tc.configure(fake)

			err := panel.Start(context.Background())
			if err == nil {
				t.Fatalf("Start succeeded, want %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %q, want it to mention %q", err, tc.wantErr)
			}
			if got := len(fake.recorded()); got != tc.wantCalls {
				t.Fatalf("calls = %v (%d), want %d calls", fake.paths(), got, tc.wantCalls)
			}
			if _, topoErr := panel.Topology(context.Background()); topoErr != runtime.ErrNotStarted {
				t.Fatalf("Topology error = %v, want ErrNotStarted", topoErr)
			}
		})
	}
}

func TestAPIWiring(t *testing.T) {
	// Resin disabled: the store-derived topology stays, reconcile is 503.
	st := store.New()
	plain := httptest.NewServer(api.New(api.Config{AllowInsecure: true}, st, discardLogger()))
	t.Cleanup(plain.Close)

	status, body := apiCall(t, plain, http.MethodGet, "/v1/topology")
	if status != http.StatusOK {
		t.Fatalf("GET /v1/topology = %d, want 200", status)
	}
	storeTopology := decode[model.Topology](t, body)
	if storeTopology.Subscription != "ajs-live" || len(storeTopology.Nodes) != 12 {
		t.Fatalf("store topology = %+v", storeTopology)
	}

	status, body = apiCall(t, plain, http.MethodPost, "/v1/reconcile")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("POST /v1/reconcile without panel = %d, want 503", status)
	}
	if code := errorCode(t, body); code != api.CodeUpstreamUnavailable {
		t.Fatalf("reconcile error code = %q, want %s", code, api.CodeUpstreamUnavailable)
	}

	// Panel configured but not started yet: topology is 503.
	unstarted, _, _ := newTestPanel(t, store.New())
	pending := httptest.NewServer(api.New(api.Config{AllowInsecure: true, Panel: unstarted}, store.New(), discardLogger()))
	t.Cleanup(pending.Close)
	status, body = apiCall(t, pending, http.MethodGet, "/v1/topology")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("GET /v1/topology before Start = %d, want 503", status)
	}
	if code := errorCode(t, body); code != api.CodeUpstreamUnavailable {
		t.Fatalf("topology error code = %q, want %s", code, api.CodeUpstreamUnavailable)
	}

	// Started panel: both routes serve the Resin view.
	panel, fake, _ := newTestPanel(t, st)
	if err := panel.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	live := httptest.NewServer(api.New(api.Config{AllowInsecure: true, Panel: panel}, st, discardLogger()))
	t.Cleanup(live.Close)

	status, body = apiCall(t, live, http.MethodGet, "/v1/topology")
	if status != http.StatusOK {
		t.Fatalf("GET /v1/topology with panel = %d, want 200", status)
	}
	topology := decode[runtime.Topology](t, body)
	if topology.SubscriptionID != "sub-1" || topology.PlatformID != "plat-1" || len(topology.Nodes) != 8 {
		t.Fatalf("panel topology = %+v", topology)
	}
	if topology.LastGood != wantContent {
		t.Fatalf("panel last_good =\n%s\nwant\n%s", topology.LastGood, wantContent)
	}

	fake.reset()
	status, body = apiCall(t, live, http.MethodPost, "/v1/reconcile")
	if status != http.StatusOK {
		t.Fatalf("POST /v1/reconcile = %d, want 200", status)
	}
	reconciled := decode[runtime.Topology](t, body)
	if reconciled.Content != wantContent || len(reconciled.Nodes) != 8 {
		t.Fatalf("reconciled topology = %+v", reconciled)
	}
	if got := fake.count(http.MethodGet, "/api/v1/subscriptions"); got != 1 {
		t.Fatalf("reconcile did not read the subscription (%d reads)", got)
	}
}

func drainLiveNodes(t *testing.T, st *store.Store) {
	t.Helper()
	for _, node := range st.Nodes() {
		if node.State != model.NodeLive {
			continue
		}
		if _, err := st.DrainNode(node.ID); err != nil {
			t.Fatalf("drain %s: %v", node.ID, err)
		}
	}
}

func apiCall(t *testing.T, ts *httptest.Server, method, path string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, ts.URL+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return resp.StatusCode, body
}

func decode[T any](t *testing.T, body []byte) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode body %q: %v", body, err)
	}
	return out
}

func errorCode(t *testing.T, body []byte) string {
	t.Helper()
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode error body %q: %v", body, err)
	}
	return payload.Error.Code
}
