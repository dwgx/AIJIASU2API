package resin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const testToken = "test-admin-token-not-a-credential"

// recorded is one request observed by the fake Resin server.
type recorded struct {
	Method      string
	Path        string
	EscapedPath string
	Query       string
	Header      http.Header
	Body        []byte
}

// fakeServer records requests and delegates to handler.
type fakeServer struct {
	t       *testing.T
	mu      sync.Mutex
	records []recorded
}

func (f *fakeServer) requests() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recorded(nil), f.records...)
}

func (f *fakeServer) last() recorded {
	f.t.Helper()
	reqs := f.requests()
	if len(reqs) == 0 {
		f.t.Fatal("no requests recorded")
	}
	return reqs[len(reqs)-1]
}

func (f *fakeServer) count(method string) int {
	n := 0
	for _, req := range f.requests() {
		if req.Method == method {
			n++
		}
	}
	return n
}

// newClient starts an httptest fake and returns a client pointed at it.
func newClient(t *testing.T, token string, handler func(w http.ResponseWriter, r *http.Request, body []byte)) (*Client, *fakeServer) {
	t.Helper()
	fake := &fakeServer{t: t}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		fake.mu.Lock()
		fake.records = append(fake.records, recorded{
			Method:      r.Method,
			Path:        r.URL.Path,
			EscapedPath: r.URL.EscapedPath(),
			Query:       r.URL.RawQuery,
			Header:      r.Header.Clone(),
			Body:        body,
		})
		fake.mu.Unlock()
		handler(w, r, body)
	}))
	t.Cleanup(server.Close)

	client, err := New(Config{BaseURL: server.URL, AdminToken: token, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client, fake
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
}

func TestEnsureSubscriptionCreatesWhenAbsent(t *testing.T) {
	client, fake := newClient(t, testToken, func(w http.ResponseWriter, r *http.Request, body []byte) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, Page[Subscription]{Items: []Subscription{}, Total: 0, Limit: 1000})
		case http.MethodPost:
			var req CreateSubscriptionRequest
			if err := json.Unmarshal(body, &req); err != nil {
				t.Errorf("decode create body: %v", err)
			}
			if req.Name != "ajs-live" || req.SourceType != "local" || req.Ephemeral != true {
				t.Errorf("create body = %+v", req)
			}
			if req.Content != "socks5://127.0.0.1:13001#cn-zj-01\n" || req.UpdateInterval != "30s" {
				t.Errorf("create content/interval = %q/%q", req.Content, req.UpdateInterval)
			}
			writeJSON(w, http.StatusCreated, Subscription{
				ID: "sub-1", Name: req.Name, SourceType: req.SourceType,
				Content: req.Content, UpdateInterval: req.UpdateInterval, Ephemeral: true,
			})
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	})

	sub, created, err := client.EnsureSubscription(context.Background(), "ajs-live",
		"socks5://127.0.0.1:13001#cn-zj-01\n", "30s")
	if err != nil {
		t.Fatalf("EnsureSubscription: %v", err)
	}
	if !created || sub.ID != "sub-1" {
		t.Fatalf("created=%v id=%q", created, sub.ID)
	}
	if fake.count(http.MethodGet) != 1 || fake.count(http.MethodPost) != 1 || fake.count(http.MethodPatch) != 0 {
		t.Fatalf("unexpected calls: %+v", fake.requests())
	}
	if got := fake.last().Header.Get("Authorization"); got != "Bearer "+testToken {
		t.Fatalf("Authorization = %q", got)
	}
}

func TestEnsureSubscriptionUnchangedNoPatch(t *testing.T) {
	client, fake := newClient(t, testToken, func(w http.ResponseWriter, r *http.Request, body []byte) {
		writeJSON(w, http.StatusOK, Page[Subscription]{
			Items: []Subscription{{
				ID: "sub-1", Name: "ajs-live", SourceType: "local",
				Content: "socks5://127.0.0.1:13001#cn-zj-01\n", UpdateInterval: "30s",
			}},
			Total: 1, Limit: 1000,
		})
	})

	sub, created, err := client.EnsureSubscription(context.Background(), "ajs-live",
		"socks5://127.0.0.1:13001#cn-zj-01\n", "30s")
	if err != nil {
		t.Fatalf("EnsureSubscription: %v", err)
	}
	if created || sub.ID != "sub-1" {
		t.Fatalf("created=%v id=%q", created, sub.ID)
	}
	if fake.count(http.MethodPatch) != 0 {
		t.Fatalf("unexpected PATCH: %+v", fake.requests())
	}
}

func TestEnsureSubscriptionPatchesChangedContent(t *testing.T) {
	newContent := "socks5://127.0.0.1:13001#cn-zj-01\nsocks5://127.0.0.1:13002#hk-02\n"
	client, fake := newClient(t, testToken, func(w http.ResponseWriter, r *http.Request, body []byte) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, Page[Subscription]{
				Items: []Subscription{{
					ID: "sub-1", Name: "ajs-live", Content: "socks5://127.0.0.1:13001#cn-zj-01\n",
					UpdateInterval: "30s",
				}},
				Total: 1,
			})
		case http.MethodPatch:
			var patch map[string]any
			if err := json.Unmarshal(body, &patch); err != nil {
				t.Errorf("decode patch: %v", err)
			}
			if patch["content"] != newContent {
				t.Errorf("patch content = %v", patch["content"])
			}
			if _, ok := patch["update_interval"]; ok {
				t.Errorf("patch unexpectedly set update_interval: %v", patch)
			}
			writeJSON(w, http.StatusOK, Subscription{ID: "sub-1", Name: "ajs-live", Content: newContent})
		}
	})

	sub, created, err := client.EnsureSubscription(context.Background(), "ajs-live", newContent, "30s")
	if err != nil {
		t.Fatalf("EnsureSubscription: %v", err)
	}
	if created {
		t.Fatal("created should be false")
	}
	if sub.Content != newContent {
		t.Fatalf("content = %q", sub.Content)
	}
	if fake.count(http.MethodPost) != 0 || fake.count(http.MethodPatch) != 1 {
		t.Fatalf("unexpected calls: %+v", fake.requests())
	}
	if fake.last().Path != "/api/v1/subscriptions/sub-1" {
		t.Fatalf("patch path = %q", fake.last().Path)
	}
}

func TestEnsurePlatformCreatesWhenAbsent(t *testing.T) {
	client, _ := newClient(t, testToken, func(w http.ResponseWriter, r *http.Request, body []byte) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, Page[Platform]{Items: []Platform{}, Total: 0})
		case http.MethodPost:
			var req CreatePlatformRequest
			if err := json.Unmarshal(body, &req); err != nil {
				t.Errorf("decode platform body: %v", err)
			}
			if req.Name != "u-01" || req.StickyTTL != "8h" || req.AllocationPolicy != "BALANCED" {
				t.Errorf("platform body = %+v", req)
			}
			if len(req.RegexFilters) != 1 || req.RegexFilters[0] != "ajs-live/.*" {
				t.Errorf("filters = %v", req.RegexFilters)
			}
			writeJSON(w, http.StatusCreated, Platform{ID: "plat-1", Name: req.Name})
		}
	})

	platform, created, err := client.EnsurePlatform(context.Background(), PlatformSpec{
		Name: "u-01", StickyTTL: "8h", RegexFilters: []string{"ajs-live/.*"}, AllocationPolicy: "BALANCED",
	})
	if err != nil {
		t.Fatalf("EnsurePlatform: %v", err)
	}
	if !created || platform.ID != "plat-1" {
		t.Fatalf("created=%v id=%q", created, platform.ID)
	}
}

func TestEnsurePlatformUpdatesOnlyDifferingFields(t *testing.T) {
	client, fake := newClient(t, testToken, func(w http.ResponseWriter, r *http.Request, body []byte) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, Page[Platform]{
				Items: []Platform{{
					ID: "plat-1", Name: "u-01", StickyTTL: "1h0m0s",
					RegexFilters: []string{"ajs-live/.*"}, AllocationPolicy: "BALANCED",
					PassiveCircuitBreakerDisabled: false,
				}},
				Total: 1,
			})
		case http.MethodPatch:
			var patch map[string]any
			if err := json.Unmarshal(body, &patch); err != nil {
				t.Errorf("decode patch: %v", err)
			}
			if _, ok := patch["sticky_ttl"]; ok {
				t.Errorf("sticky_ttl should not be patched: %v", patch)
			}
			if _, ok := patch["allocation_policy"]; ok {
				t.Errorf("allocation_policy should not be patched: %v", patch)
			}
			filters, ok := patch["regex_filters"].([]any)
			if !ok || len(filters) != 2 || filters[0] != "ajs-live/.*" || filters[1] != "!^ajs-live/hk-02$" {
				t.Errorf("regex_filters = %v", patch["regex_filters"])
			}
			if patch["passive_circuit_breaker_disabled"] != true {
				t.Errorf("passive_circuit_breaker_disabled = %v", patch["passive_circuit_breaker_disabled"])
			}
			writeJSON(w, http.StatusOK, Platform{ID: "plat-1", Name: "u-01"})
		}
	})

	disabled := true
	_, created, err := client.EnsurePlatform(context.Background(), PlatformSpec{
		Name: "u-01", StickyTTL: "1h", // equal after duration normalisation
		RegexFilters:                  []string{"ajs-live/.*", "!^ajs-live/hk-02$"},
		AllocationPolicy:              "BALANCED",
		PassiveCircuitBreakerDisabled: &disabled,
	})
	if err != nil {
		t.Fatalf("EnsurePlatform: %v", err)
	}
	if created {
		t.Fatal("created should be false")
	}
	if fake.count(http.MethodPatch) != 1 {
		t.Fatalf("expected one PATCH, got %+v", fake.requests())
	}
}

func TestEnsurePlatformNoChangeSkipsPatch(t *testing.T) {
	client, fake := newClient(t, testToken, func(w http.ResponseWriter, r *http.Request, body []byte) {
		writeJSON(w, http.StatusOK, Page[Platform]{
			Items: []Platform{{
				ID: "plat-1", Name: "u-01", StickyTTL: "8h0m0s",
				RegexFilters: []string{"ajs-live/.*"}, AllocationPolicy: "BALANCED",
			}},
			Total: 1,
		})
	})
	_, created, err := client.EnsurePlatform(context.Background(), PlatformSpec{
		Name: "u-01", StickyTTL: "8h", RegexFilters: []string{"ajs-live/.*"}, AllocationPolicy: "balanced",
	})
	if err != nil {
		t.Fatalf("EnsurePlatform: %v", err)
	}
	if created || fake.count(http.MethodPatch) != 0 || fake.count(http.MethodPost) != 0 {
		t.Fatalf("unexpected calls: %+v", fake.requests())
	}
}

func TestPatchPlatformFiltersReplacesList(t *testing.T) {
	client, fake := newClient(t, testToken, func(w http.ResponseWriter, r *http.Request, body []byte) {
		var patch UpdatePlatformRequest
		if err := json.Unmarshal(body, &patch); err != nil {
			t.Errorf("decode patch: %v", err)
		}
		if patch.RegexFilters == nil || len(*patch.RegexFilters) != 2 {
			t.Errorf("regex_filters = %v", patch.RegexFilters)
		}
		writeJSON(w, http.StatusOK, Platform{ID: "plat-1", RegexFilters: *patch.RegexFilters})
	})
	platform, err := client.PatchPlatformFilters(context.Background(), "plat-1",
		[]string{"ajs-live/.*", "!^ajs-live/hk-02$"})
	if err != nil {
		t.Fatalf("PatchPlatformFilters: %v", err)
	}
	if len(platform.RegexFilters) != 2 {
		t.Fatalf("filters = %v", platform.RegexFilters)
	}
	if fake.last().Path != "/api/v1/platforms/plat-1" {
		t.Fatalf("path = %q", fake.last().Path)
	}
}

func TestGetAndDeleteLease(t *testing.T) {
	client, fake := newClient(t, testToken, func(w http.ResponseWriter, r *http.Request, body []byte) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, Lease{
				PlatformID: "plat-1", Account: "acct 1", NodeHash: "n1",
				NodeTag: "ajs-live/cn-zj-01", EgressIP: "203.0.113.7",
			})
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		}
	})

	lease, err := client.GetLease(context.Background(), "plat-1", "acct 1")
	if err != nil {
		t.Fatalf("GetLease: %v", err)
	}
	if lease.NodeTag != "ajs-live/cn-zj-01" || lease.EgressIP != "203.0.113.7" {
		t.Fatalf("lease = %+v", lease)
	}
	if got := fake.last().EscapedPath; got != "/api/v1/platforms/plat-1/leases/acct%201" {
		t.Fatalf("escaped path = %q", got)
	}
	if err := client.DeleteLease(context.Background(), "plat-1", "acct 1"); err != nil {
		t.Fatalf("DeleteLease: %v", err)
	}
}

func TestDeleteLeaseToleratesNotFound(t *testing.T) {
	client, _ := newClient(t, testToken, func(w http.ResponseWriter, r *http.Request, body []byte) {
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "lease not found")
	})
	if err := client.DeleteLease(context.Background(), "plat-1", "acct1"); err != nil {
		t.Fatalf("DeleteLease on 404 = %v, want nil", err)
	}
}

func TestGetLeaseNotFoundIsTyped(t *testing.T) {
	client, _ := newClient(t, testToken, func(w http.ResponseWriter, r *http.Request, body []byte) {
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "lease not found")
	})
	_, err := client.GetLease(context.Background(), "plat-1", "acct1")
	if !IsNotFound(err) {
		t.Fatalf("err = %v, want NOT_FOUND", err)
	}
}

func TestListRequestLogsBuildsQuery(t *testing.T) {
	client, fake := newClient(t, testToken, func(w http.ResponseWriter, r *http.Request, body []byte) {
		writeJSON(w, http.StatusOK, RequestLogPage{
			Items:   []RequestLog{{ID: "log-1", Account: "acct1", EgressIP: "203.0.113.7", NetOK: true}},
			Limit:   50,
			HasMore: false,
		})
	})
	from := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)
	page, err := client.ListRequestLogs(context.Background(), RequestLogQuery{
		PlatformID: "plat-1", Account: "acct1", From: from, To: to, Limit: 50,
	})
	if err != nil {
		t.Fatalf("ListRequestLogs: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != "log-1" {
		t.Fatalf("page = %+v", page)
	}
	query := fake.last().Query
	for _, want := range []string{
		"platform_id=plat-1",
		"account=acct1",
		"from=2026-10-23T00%3A00%3A00Z",
		"to=2026-10-24T00%3A00%3A00Z",
		"limit=50",
	} {
		if !strings.Contains(query, want) {
			t.Errorf("query %q missing %q", query, want)
		}
	}
	if strings.Contains(query, "offset=") || strings.Contains(query, "cursor=") {
		t.Errorf("query unexpectedly has offset/cursor: %q", query)
	}
}

func TestListRequestLogsOffsetAndCursor(t *testing.T) {
	client, fake := newClient(t, testToken, func(w http.ResponseWriter, r *http.Request, body []byte) {
		writeJSON(w, http.StatusOK, RequestLogPage{})
	})
	if _, err := client.ListRequestLogs(context.Background(), RequestLogQuery{Offset: 100, Cursor: "abc"}); err != nil {
		t.Fatalf("ListRequestLogs: %v", err)
	}
	query := fake.last().Query
	if !strings.Contains(query, "offset=100") || !strings.Contains(query, "cursor=abc") {
		t.Fatalf("query = %q", query)
	}
}

func TestSystemInfo(t *testing.T) {
	client, _ := newClient(t, testToken, func(w http.ResponseWriter, r *http.Request, body []byte) {
		writeJSON(w, http.StatusOK, SystemInfo{Version: "v1.3.0", GitCommit: "5473c7ee"})
	})
	info, err := client.SystemInfo(context.Background())
	if err != nil {
		t.Fatalf("SystemInfo: %v", err)
	}
	if info.Version != "v1.3.0" || info.GitCommit != "5473c7ee" {
		t.Fatalf("info = %+v", info)
	}
}

func TestListNodesFollowsPagination(t *testing.T) {
	client, fake := newClient(t, testToken, func(w http.ResponseWriter, r *http.Request, body []byte) {
		switch {
		case strings.Contains(r.URL.RawQuery, "offset=0"):
			writeJSON(w, http.StatusOK, Page[Node]{
				Items: []Node{{NodeHash: "a"}, {NodeHash: "b"}}, Total: 3, Limit: 2,
			})
		case strings.Contains(r.URL.RawQuery, "offset=2"):
			writeJSON(w, http.StatusOK, Page[Node]{
				Items: []Node{{NodeHash: "c"}}, Total: 3, Limit: 2,
			})
		default:
			t.Errorf("unexpected query %q", r.URL.RawQuery)
		}
	})
	nodes, err := client.ListNodes(context.Background())
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(nodes) != 3 || nodes[2].NodeHash != "c" {
		t.Fatalf("nodes = %+v", nodes)
	}
	if fake.count(http.MethodGet) != 2 {
		t.Fatalf("expected two pages, got %+v", fake.requests())
	}
}

func TestGetNodeDecodesCircuitOpenSince(t *testing.T) {
	open := "2026-10-23T02:58:39.450007907Z"
	client, _ := newClient(t, testToken, func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.URL.Path != "/api/v1/nodes/55aba6110fddf4d6b8c04d631af30e0c" {
			t.Errorf("path = %q", r.URL.Path)
		}
		writeJSON(w, http.StatusOK, Node{
			NodeHash:         "55aba6110fddf4d6b8c04d631af30e0c",
			CircuitOpenSince: open,
			Tags:             []NodeTag{{SubscriptionName: "ajs-live", Tag: "cn-zj-01"}},
		})
	})
	node, err := client.GetNode(context.Background(), "55aba6110fddf4d6b8c04d631af30e0c")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if !node.CircuitOpen() || node.CircuitOpenSince != open {
		t.Fatalf("node = %+v", node)
	}
}

func TestGetNodeNullCircuitIsClosed(t *testing.T) {
	client, _ := newClient(t, testToken, func(w http.ResponseWriter, r *http.Request, body []byte) {
		_, _ = w.Write([]byte(`{"node_hash":"n1","circuit_open_since":null}`))
	})
	node, err := client.GetNode(context.Background(), "n1")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if node.CircuitOpen() {
		t.Fatalf("node = %+v", node)
	}
}

func TestPatchSystemConfig(t *testing.T) {
	client, fake := newClient(t, testToken, func(w http.ResponseWriter, r *http.Request, body []byte) {
		var patch map[string]any
		if err := json.Unmarshal(body, &patch); err != nil {
			t.Errorf("decode patch: %v", err)
		}
		if patch["max_latency_test_interval"] != "30s" {
			t.Errorf("patch = %v", patch)
		}
		writeJSON(w, http.StatusOK, patch)
	})
	if err := client.PatchSystemConfig(context.Background(), "max_latency_test_interval", "30s"); err != nil {
		t.Fatalf("PatchSystemConfig: %v", err)
	}
	if fake.last().Path != "/api/v1/system/config" || fake.last().Method != http.MethodPatch {
		t.Fatalf("request = %+v", fake.last())
	}
}

func TestAuthHeaderPresentAndTokenNeverInErrors(t *testing.T) {
	client, fake := newClient(t, testToken, func(w http.ResponseWriter, r *http.Request, body []byte) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+testToken {
			t.Errorf("Authorization = %q", got)
		}
		if r.Header.Get("X-Request-Id") == "" {
			t.Error("missing X-Request-Id")
		}
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL", "boom")
	})

	_, err := client.SystemInfo(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err type = %T", err)
	}
	if apiErr.Status != http.StatusInternalServerError || apiErr.Code != "INTERNAL" {
		t.Fatalf("apiErr = %+v", apiErr)
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatalf("token leaked in error: %v", err)
	}
	if strings.Contains(err.Error(), "Authorization") || strings.Contains(err.Error(), "Bearer") {
		t.Fatalf("auth header leaked in error: %v", err)
	}
	if fake.count(http.MethodGet) != 1 {
		t.Fatalf("requests = %+v", fake.requests())
	}
}

func TestErrorWithoutEnvelopeKeepsStatus(t *testing.T) {
	client, _ := newClient(t, testToken, func(w http.ResponseWriter, r *http.Request, body []byte) {
		http.Error(w, "gateway down", http.StatusBadGateway)
	})
	_, err := client.SystemInfo(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsStatus(err, http.StatusBadGateway) {
		t.Fatalf("err = %v", err)
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Code != "HTTP_502" {
		t.Fatalf("code = %q", apiErr.Code)
	}
}

func TestConfigFromEnvDefaults(t *testing.T) {
	t.Setenv(EnvBaseURL, "")
	t.Setenv(EnvAdminToken, "  tok  ")
	cfg := ConfigFromEnv()
	if cfg.BaseURL != DefaultBaseURL {
		t.Fatalf("BaseURL = %q", cfg.BaseURL)
	}
	if cfg.AdminToken != "tok" {
		t.Fatalf("AdminToken = %q", cfg.AdminToken)
	}
	if cfg.Timeout != DefaultTimeout {
		t.Fatalf("Timeout = %v", cfg.Timeout)
	}
}

func TestNewRejectsEmbeddedCredentials(t *testing.T) {
	if _, err := New(Config{BaseURL: "http://user:pass@127.0.0.1:2260"}); err == nil {
		t.Fatal("expected error for credentials in base URL")
	}
	if _, err := New(Config{BaseURL: "ftp://127.0.0.1:2260"}); err == nil {
		t.Fatal("expected error for non-http scheme")
	}
}

func TestTimeoutIsEnforced(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		writeJSON(w, http.StatusOK, SystemInfo{})
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, Timeout: 20 * time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	started := time.Now()
	if _, err := client.SystemInfo(context.Background()); err == nil {
		t.Fatal("expected timeout error")
	}
	if elapsed := time.Since(started); elapsed > 150*time.Millisecond {
		t.Fatalf("timeout took %v", elapsed)
	}
}

// TestSuccessBodyIsSizeLimited is the F9 regression: an unbounded success body
// must fail the call instead of growing memory without limit.
func TestSuccessBodyIsSizeLimited(t *testing.T) {
	client, _ := newClient(t, testToken, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"version":"`)
		chunk := strings.Repeat("a", 1<<20)
		for range 9 {
			if _, err := io.WriteString(w, chunk); err != nil {
				return
			}
		}
		_, _ = io.WriteString(w, `"}`)
	})

	_, err := client.SystemInfo(context.Background())
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("SystemInfo error = %v, want the response size limit", err)
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatalf("token leaked into the error: %v", err)
	}
}

func TestStringSetEqual(t *testing.T) {
	if !stringSetEqual([]string{"a", "b"}, []string{"b", "a"}) {
		t.Fatal("order-insensitive comparison failed")
	}
	if !stringSetEqual(nil, []string{}) {
		t.Fatal("nil vs empty should be equal")
	}
	if stringSetEqual([]string{"a"}, []string{"a", "b"}) {
		t.Fatal("length mismatch should differ")
	}
	if stringSetEqual([]string{"a", "a"}, []string{"a", "b"}) {
		t.Fatal("multiset mismatch should differ")
	}
}
