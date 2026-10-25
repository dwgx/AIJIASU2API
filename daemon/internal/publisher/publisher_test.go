package publisher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"ajsaccountd/internal/resin"
)

const testToken = "test-admin-token-not-a-credential"

// fakeClock makes coalescing and backoff deterministic: Sleep advances the
// same clock Now reads.
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

// adminCall is one request observed by the fake Resin admin API.
type adminCall struct {
	Method string
	Path   string
	Body   []byte
}

// fakeAdmin serves the Resin routes the publisher uses.
type fakeAdmin struct {
	t *testing.T

	mu                 sync.Mutex
	calls              []adminCall
	platform           resin.Platform
	content            string
	refreshFailures    int
	deleteFailures     int
	restoreFailures    int
	systemConfigStatus int
	originalFilters    []string
}

func (f *fakeAdmin) recorded() []adminCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]adminCall(nil), f.calls...)
}

func (f *fakeAdmin) count(method, pathPrefix string) int {
	n := 0
	for _, call := range f.recorded() {
		if call.Method == method && strings.HasPrefix(call.Path, pathPrefix) {
			n++
		}
	}
	return n
}

func (f *fakeAdmin) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.calls = append(f.calls, adminCall{Method: r.Method, Path: r.URL.Path, Body: body})
		f.mu.Unlock()

		switch {
		case r.Method == http.MethodPatch && r.URL.Path == "/api/v1/system/config":
			f.mu.Lock()
			status := f.systemConfigStatus
			f.mu.Unlock()
			if status != 0 {
				writeError(w, status, "INVALID_ARGUMENT", "unsupported")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{})

		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/api/v1/subscriptions/"):
			var patch resin.SubscriptionPatch
			if err := json.Unmarshal(body, &patch); err != nil {
				f.t.Errorf("decode subscription patch: %v", err)
			}
			f.mu.Lock()
			if patch.Content != nil {
				f.content = *patch.Content
			}
			content := f.content
			f.mu.Unlock()
			writeJSON(w, http.StatusOK, resin.Subscription{ID: "sub-1", Content: content})

		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/actions/refresh"):
			f.mu.Lock()
			if f.refreshFailures > 0 {
				f.refreshFailures--
				f.mu.Unlock()
				writeError(w, http.StatusInternalServerError, "INTERNAL", "refresh failed")
				return
			}
			f.mu.Unlock()
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})

		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/platforms/"):
			f.mu.Lock()
			platform := f.platform
			f.mu.Unlock()
			writeJSON(w, http.StatusOK, platform)

		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/api/v1/platforms/"):
			var patch resin.UpdatePlatformRequest
			if err := json.Unmarshal(body, &patch); err != nil {
				f.t.Errorf("decode platform patch: %v", err)
			}
			f.mu.Lock()
			if patch.RegexFilters != nil {
				if f.restoreFailures > 0 && equalStrings(*patch.RegexFilters, f.originalFilters) {
					f.restoreFailures--
					f.mu.Unlock()
					writeError(w, http.StatusInternalServerError, "INTERNAL", "restore failed")
					return
				}
				f.platform.RegexFilters = append([]string(nil), (*patch.RegexFilters)...)
			}
			platform := f.platform
			f.mu.Unlock()
			writeJSON(w, http.StatusOK, platform)

		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/leases/"):
			f.mu.Lock()
			if f.deleteFailures > 0 {
				f.deleteFailures--
				f.mu.Unlock()
				writeError(w, http.StatusInternalServerError, "INTERNAL", "delete failed")
				return
			}
			f.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)

		default:
			f.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			writeError(w, http.StatusNotFound, "NOT_FOUND", "no route")
		}
	}
}

// newPublisher starts the fake admin API and returns a real resin.Client
// wired to a publisher with a fake clock.
func newPublisher(t *testing.T, admin *fakeAdmin, mutate func(*Config)) (*Publisher, *fakeClock) {
	t.Helper()
	server := httptest.NewServer(admin.handler())
	t.Cleanup(server.Close)
	client, err := resin.New(resin.Config{BaseURL: server.URL, AdminToken: testToken})
	if err != nil {
		t.Fatalf("resin.New: %v", err)
	}
	clock := newFakeClock()
	cfg := Config{
		SubscriptionID: "sub-1",
		Now:            clock.Now,
		Sleep:          clock.Sleep,
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if mutate != nil {
		mutate(&cfg)
	}
	return New(client, cfg), clock
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestRenderGolden(t *testing.T) {
	content, err := Render([]Node{
		{Tag: "hk-02", GatePort: 13002},
		{Tag: "cn-zj-01", GatePort: 13001},
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	want := "socks5://127.0.0.1:13001#cn-zj-01\nsocks5://127.0.0.1:13002#hk-02\n"
	if content != want {
		t.Fatalf("content = %q, want %q", content, want)
	}
}

func TestRenderEmpty(t *testing.T) {
	content, err := Render(nil)
	if err != nil || content != "" {
		t.Fatalf("Render(nil) = %q, %v", content, err)
	}
}

func TestRenderRejectsBadNodes(t *testing.T) {
	cases := []struct {
		name  string
		nodes []Node
		want  error
	}{
		{"duplicate", []Node{{Tag: "cn-zj-01", GatePort: 1}, {Tag: "cn-zj-01", GatePort: 2}}, ErrDuplicateTag},
		{"empty tag", []Node{{Tag: "", GatePort: 1}}, ErrInvalidTag},
		{"uppercase tag", []Node{{Tag: "CN-ZJ-01", GatePort: 1}}, ErrInvalidTag},
		{"tag with slash", []Node{{Tag: "ajs-live/cn-zj-01", GatePort: 1}}, ErrInvalidTag},
		{"zero port", []Node{{Tag: "cn-zj-01", GatePort: 0}}, ErrInvalidPort},
		{"port too high", []Node{{Tag: "cn-zj-01", GatePort: 70000}}, ErrInvalidPort},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Render(tc.nodes); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestValidTag(t *testing.T) {
	for _, tag := range []string{"cn-zj-01", "hk-01", "xx-07", "us-ca-100"} {
		if !ValidTag(tag) {
			t.Errorf("ValidTag(%q) = false", tag)
		}
	}
	for _, tag := range []string{"", "-cn", "cn-", "cn--01", "CN-01", "cn zj", "cn_zj"} {
		if ValidTag(tag) {
			t.Errorf("ValidTag(%q) = true", tag)
		}
	}
}

func TestTagAllocatorIsStablePerAccountServer(t *testing.T) {
	alloc := NewTagAllocator()
	first := alloc.Tag("CN", "ZJ", "acc-1", "srv-1")
	if first != "cn-zj-01" {
		t.Fatalf("first tag = %q", first)
	}
	if again := alloc.Tag("CN", "ZJ", "acc-1", "srv-1"); again != first {
		t.Fatalf("tag churned: %q -> %q", first, again)
	}
	if second := alloc.Tag("cn", "zj", "acc-1", "srv-2"); second != "cn-zj-02" {
		t.Fatalf("second tag = %q", second)
	}
	// The sequence is global to the allocator so tags stay unique across the
	// whole subscription; a different (account, server) pair gets the next one.
	if other := alloc.Tag("hk", "", "acc-2", "srv-9"); other != "hk-03" {
		t.Fatalf("other tag = %q", other)
	}
	if fallback := alloc.Tag("", "", "acc-3", "srv-9"); fallback != "xx-04" {
		t.Fatalf("fallback tag = %q", fallback)
	}
}

func TestPublishCoalescesAndSkipsUnchanged(t *testing.T) {
	admin := &fakeAdmin{t: t}
	pub, clock := newPublisher(t, admin, nil)
	ctx := context.Background()

	if err := pub.Publish(ctx, []Node{{Tag: "cn-zj-01", GatePort: 13001}}); err != nil {
		t.Fatalf("first publish: %v", err)
	}
	if got := admin.count(http.MethodPatch, "/api/v1/subscriptions/"); got != 1 {
		t.Fatalf("patches after first publish = %d", got)
	}
	if got := admin.count(http.MethodPost, "/api/v1/subscriptions/"); got != 1 {
		t.Fatalf("refreshes after first publish = %d", got)
	}

	// Different content inside the window must wait for the slot.
	if err := pub.Publish(ctx, []Node{
		{Tag: "cn-zj-01", GatePort: 13001},
		{Tag: "hk-02", GatePort: 13002},
	}); err != nil {
		t.Fatalf("second publish: %v", err)
	}
	if len(clock.sleeps) != 1 || clock.sleeps[0] != DefaultMinPublishInterval {
		t.Fatalf("sleeps = %v", clock.sleeps)
	}
	if got := admin.count(http.MethodPatch, "/api/v1/subscriptions/"); got != 2 {
		t.Fatalf("patches after second publish = %d", got)
	}

	// Identical content is a no-op: no PATCH, no refresh, no sleep.
	before := len(clock.sleeps)
	if err := pub.Publish(ctx, []Node{
		{Tag: "cn-zj-01", GatePort: 13001},
		{Tag: "hk-02", GatePort: 13002},
	}); err != nil {
		t.Fatalf("third publish: %v", err)
	}
	if got := admin.count(http.MethodPatch, "/api/v1/subscriptions/"); got != 2 {
		t.Fatalf("patches after unchanged publish = %d", got)
	}
	if got := admin.count(http.MethodPost, "/api/v1/subscriptions/"); got != 2 {
		t.Fatalf("refreshes after unchanged publish = %d", got)
	}
	if len(clock.sleeps) != before {
		t.Fatalf("unchanged publish slept: %v", clock.sleeps)
	}
}

func TestPublishRefusesEmptyContent(t *testing.T) {
	admin := &fakeAdmin{t: t}
	pub, _ := newPublisher(t, admin, nil)

	err := pub.Publish(context.Background(), nil)
	if !errors.Is(err, ErrEmptyPublish) {
		t.Fatalf("err = %v, want ErrEmptyPublish", err)
	}
	if len(admin.recorded()) != 0 {
		t.Fatalf("unexpected calls: %+v", admin.recorded())
	}
}

func TestPublishEmptyRenderKeepsLastGood(t *testing.T) {
	admin := &fakeAdmin{t: t}
	pub, clock := newPublisher(t, admin, nil)
	ctx := context.Background()

	if err := pub.Publish(ctx, []Node{{Tag: "cn-zj-01", GatePort: 13001}}); err != nil {
		t.Fatalf("first publish: %v", err)
	}
	want := pub.LastGood()
	clock.t = clock.t.Add(DefaultMinPublishInterval)
	if err := pub.Publish(ctx, nil); !errors.Is(err, ErrEmptyPublish) {
		t.Fatalf("empty publish err = %v, want ErrEmptyPublish", err)
	}
	if pub.LastGood() != want {
		t.Fatalf("lastGood = %q, want %q", pub.LastGood(), want)
	}
	if got := admin.count(http.MethodPatch, "/api/v1/subscriptions/"); got != 1 {
		t.Fatalf("patches = %d, want 1", got)
	}
}

func TestPublishRefreshRetriesThenSucceeds(t *testing.T) {
	admin := &fakeAdmin{t: t, refreshFailures: 2}
	pub, clock := newPublisher(t, admin, func(cfg *Config) {
		cfg.RefreshAttempts = 3
		cfg.RefreshBackoff = 100 * time.Millisecond
	})

	if err := pub.Publish(context.Background(), []Node{{Tag: "cn-zj-01", GatePort: 13001}}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if got := admin.count(http.MethodPost, "/api/v1/subscriptions/"); got != 3 {
		t.Fatalf("refresh attempts = %d", got)
	}
	wantSleeps := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond}
	if len(clock.sleeps) != len(wantSleeps) {
		t.Fatalf("sleeps = %v", clock.sleeps)
	}
	for i := range wantSleeps {
		if clock.sleeps[i] != wantSleeps[i] {
			t.Fatalf("sleep[%d] = %v, want %v", i, clock.sleeps[i], wantSleeps[i])
		}
	}
	if pub.LastGood() != "socks5://127.0.0.1:13001#cn-zj-01\n" {
		t.Fatalf("lastGood = %q", pub.LastGood())
	}
}

func TestPublishRefreshFailureKeepsLastGood(t *testing.T) {
	admin := &fakeAdmin{t: t, refreshFailures: 99}
	pub, clock := newPublisher(t, admin, func(cfg *Config) {
		cfg.RefreshAttempts = 3
		cfg.RefreshBackoff = 100 * time.Millisecond
	})
	nodes := []Node{{Tag: "cn-zj-01", GatePort: 13001}}

	err := pub.Publish(context.Background(), nodes)
	if err == nil {
		t.Fatal("expected refresh error")
	}
	wantContent := "socks5://127.0.0.1:13001#cn-zj-01\n"
	if pub.LastGood() != wantContent {
		t.Fatalf("lastGood = %q", pub.LastGood())
	}
	if got := admin.count(http.MethodPost, "/api/v1/subscriptions/"); got != 3 {
		t.Fatalf("refresh attempts = %d", got)
	}

	// The next publish retries only the refresh, without a second PATCH.
	admin.mu.Lock()
	admin.refreshFailures = 0
	admin.mu.Unlock()
	clock.t = clock.t.Add(DefaultMinPublishInterval)
	if err := pub.Publish(context.Background(), nodes); err != nil {
		t.Fatalf("retry publish: %v", err)
	}
	if got := admin.count(http.MethodPatch, "/api/v1/subscriptions/"); got != 1 {
		t.Fatalf("patches = %d, want 1", got)
	}
	if got := admin.count(http.MethodPost, "/api/v1/subscriptions/"); got != 4 {
		t.Fatalf("refreshes = %d, want 4", got)
	}
	if pub.LastGood() != wantContent {
		t.Fatalf("lastGood after retry = %q", pub.LastGood())
	}
}

func TestRotateUsesFilterExclusionThenRestores(t *testing.T) {
	admin := &fakeAdmin{
		t:               t,
		platform:        resin.Platform{ID: "plat-1", Name: "u-01", RegexFilters: []string{"ajs-live/.*"}},
		originalFilters: []string{"ajs-live/.*"},
	}
	pub, clock := newPublisher(t, admin, nil)
	ctx := context.Background()

	if err := pub.Rotate(ctx, "plat-1", "acct-1", "ajs-live/cn-zj-01", true); err != nil {
		t.Fatalf("Rotate: %v", err)
	}

	calls := admin.recorded()
	want := []string{
		"GET /api/v1/platforms/plat-1",
		"PATCH /api/v1/platforms/plat-1",
		"DELETE /api/v1/platforms/plat-1/leases/acct-1",
		"PATCH /api/v1/platforms/plat-1",
	}
	if len(calls) != len(want) {
		t.Fatalf("calls = %+v", calls)
	}
	for i := range want {
		if got := calls[i].Method + " " + calls[i].Path; got != want[i] {
			t.Fatalf("call[%d] = %q, want %q", i, got, want[i])
		}
	}

	var exclude resin.UpdatePlatformRequest
	if err := json.Unmarshal(calls[1].Body, &exclude); err != nil {
		t.Fatalf("decode exclude patch: %v", err)
	}
	if exclude.RegexFilters == nil || !equalStrings(*exclude.RegexFilters,
		[]string{"ajs-live/.*", "!^ajs-live/cn-zj-01$"}) {
		t.Fatalf("exclude filters = %v", exclude.RegexFilters)
	}
	var restore resin.UpdatePlatformRequest
	if err := json.Unmarshal(calls[3].Body, &restore); err != nil {
		t.Fatalf("decode restore patch: %v", err)
	}
	if restore.RegexFilters == nil || !equalStrings(*restore.RegexFilters, []string{"ajs-live/.*"}) {
		t.Fatalf("restore filters = %v", restore.RegexFilters)
	}

	// One rotation per account per 10s.
	if err := pub.Rotate(ctx, "plat-1", "acct-1", "ajs-live/cn-zj-01", true); !errors.Is(err, ErrRotationRateLimit) {
		t.Fatalf("second rotate err = %v, want rate limit", err)
	}
	if len(admin.recorded()) != len(calls) {
		t.Fatalf("rate-limited rotation made calls: %+v", admin.recorded())
	}
	clock.t = clock.t.Add(DefaultRotationInterval)
	if err := pub.Rotate(ctx, "plat-1", "acct-1", "ajs-live/cn-zj-01", true); err != nil {
		t.Fatalf("rotate after window: %v", err)
	}
}

func TestRotateRestoresFiltersWhenDeleteFails(t *testing.T) {
	admin := &fakeAdmin{
		t:               t,
		platform:        resin.Platform{ID: "plat-1", RegexFilters: []string{"ajs-live/.*"}},
		originalFilters: []string{"ajs-live/.*"},
		deleteFailures:  1,
	}
	pub, _ := newPublisher(t, admin, nil)

	err := pub.Rotate(context.Background(), "plat-1", "acct-1", "ajs-live/cn-zj-01", true)
	if err == nil {
		t.Fatal("expected delete error")
	}
	if !strings.Contains(err.Error(), "delete") {
		t.Fatalf("err = %v", err)
	}
	calls := admin.recorded()
	if len(calls) != 4 {
		t.Fatalf("calls = %+v", calls)
	}
	var restore resin.UpdatePlatformRequest
	if err := json.Unmarshal(calls[3].Body, &restore); err != nil {
		t.Fatalf("decode restore patch: %v", err)
	}
	if restore.RegexFilters == nil || !equalStrings(*restore.RegexFilters, []string{"ajs-live/.*"}) {
		t.Fatalf("restore filters = %v", restore.RegexFilters)
	}
	admin.mu.Lock()
	filters := admin.platform.RegexFilters
	admin.mu.Unlock()
	if !equalStrings(filters, []string{"ajs-live/.*"}) {
		t.Fatalf("platform filters not restored: %v", filters)
	}
}

func TestRotateRetriesRestore(t *testing.T) {
	admin := &fakeAdmin{
		t:               t,
		platform:        resin.Platform{ID: "plat-1", RegexFilters: []string{"ajs-live/.*"}},
		originalFilters: []string{"ajs-live/.*"},
		restoreFailures: 1,
	}
	pub, clock := newPublisher(t, admin, func(cfg *Config) {
		cfg.RestoreAttempts = 3
		cfg.RestoreBackoff = 10 * time.Millisecond
	})
	if err := pub.Rotate(context.Background(), "plat-1", "acct-1", "ajs-live/cn-zj-01", true); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if got := admin.count(http.MethodPatch, "/api/v1/platforms/"); got != 3 {
		t.Fatalf("filter patches = %d, want 3 (exclude + failed restore + retry)", got)
	}
	if len(clock.sleeps) != 1 || clock.sleeps[0] != 10*time.Millisecond {
		t.Fatalf("sleeps = %v", clock.sleeps)
	}
	admin.mu.Lock()
	filters := admin.platform.RegexFilters
	admin.mu.Unlock()
	if !equalStrings(filters, []string{"ajs-live/.*"}) {
		t.Fatalf("filters = %v", filters)
	}
}

func TestRotateSkipsDuplicateExclusion(t *testing.T) {
	admin := &fakeAdmin{
		t:               t,
		platform:        resin.Platform{ID: "plat-1", RegexFilters: []string{"ajs-live/.*", "!^ajs-live/cn-zj-01$"}},
		originalFilters: []string{"ajs-live/.*", "!^ajs-live/cn-zj-01$"},
	}
	pub, _ := newPublisher(t, admin, nil)
	if err := pub.Rotate(context.Background(), "plat-1", "acct-1", "ajs-live/cn-zj-01", true); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	calls := admin.recorded()
	if len(calls) != 3 {
		t.Fatalf("calls = %+v", calls)
	}
	if calls[1].Method != http.MethodDelete {
		t.Fatalf("call[1] = %s %s, want DELETE (no duplicate exclude)", calls[1].Method, calls[1].Path)
	}
}

func TestRotateWithoutExclusionDeletesOnly(t *testing.T) {
	admin := &fakeAdmin{t: t, platform: resin.Platform{ID: "plat-1"}}
	pub, _ := newPublisher(t, admin, nil)
	if err := pub.Rotate(context.Background(), "plat-1", "acct-1", "", false); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	calls := admin.recorded()
	if len(calls) != 1 || calls[0].Method != http.MethodDelete {
		t.Fatalf("calls = %+v", calls)
	}
}

func TestDropLeasesOnOpenCircuits(t *testing.T) {
	admin := &fakeAdmin{t: t}
	pub, _ := newPublisher(t, admin, nil)

	nodes := []NodeHealth{
		{Hash: "h-open", CircuitOpenSince: "2026-10-23T02:58:39.450007907Z"},
		{Hash: "h-draining", CircuitOpenSince: "2026-10-23T02:58:39.450007907Z", Draining: true},
		{Hash: "h-ok"},
	}
	leases := []resin.Lease{
		{PlatformID: "plat-1", Account: "acct-open", NodeHash: "h-open"},
		{PlatformID: "plat-1", Account: "acct-draining", NodeHash: "h-draining"},
		{PlatformID: "plat-1", Account: "acct-ok", NodeHash: "h-ok"},
		{PlatformID: "plat-1", Account: "acct-open-2", NodeHash: "h-open"},
		{PlatformID: "plat-other", Account: "acct-other", NodeHash: "h-open"},
	}

	deleted, err := pub.DropLeasesOnOpenCircuits(context.Background(), "plat-1", nodes, leases)
	if err != nil {
		t.Fatalf("DropLeasesOnOpenCircuits: %v", err)
	}
	want := []LeaseID{
		{PlatformID: "plat-1", Account: "acct-open"},
		{PlatformID: "plat-1", Account: "acct-open-2"},
	}
	if len(deleted) != len(want) {
		t.Fatalf("deleted = %+v", deleted)
	}
	for i := range want {
		if deleted[i] != want[i] {
			t.Fatalf("deleted[%d] = %+v, want %+v", i, deleted[i], want[i])
		}
	}
	if got := admin.count(http.MethodDelete, "/api/v1/platforms/plat-1/leases/"); got != 2 {
		t.Fatalf("delete calls = %d", got)
	}
}

func TestDropLeasesIgnoresClosedAndDrainingOnly(t *testing.T) {
	admin := &fakeAdmin{t: t}
	pub, _ := newPublisher(t, admin, nil)
	deleted, err := pub.DropLeasesOnOpenCircuits(context.Background(), "plat-1",
		[]NodeHealth{{Hash: "h-ok"}, {Hash: "h-drain", Draining: true}},
		[]resin.Lease{{PlatformID: "plat-1", Account: "a1", NodeHash: "h-ok"}})
	if err != nil || len(deleted) != 0 {
		t.Fatalf("deleted = %+v, err = %v", deleted, err)
	}
	if len(admin.recorded()) != 0 {
		t.Fatalf("unexpected calls: %+v", admin.recorded())
	}
}

func TestDropLeasesReportsDeleteFailure(t *testing.T) {
	admin := &fakeAdmin{t: t, deleteFailures: 1}
	pub, _ := newPublisher(t, admin, nil)
	deleted, err := pub.DropLeasesOnOpenCircuits(context.Background(), "plat-1",
		[]NodeHealth{{Hash: "h-open", CircuitOpenSince: "2026-10-23T02:58:39Z"}},
		[]resin.Lease{
			{PlatformID: "plat-1", Account: "a1", NodeHash: "h-open"},
			{PlatformID: "plat-1", Account: "a2", NodeHash: "h-open"},
		})
	if err == nil {
		t.Fatal("expected delete error")
	}
	if len(deleted) != 1 || deleted[0].Account != "a2" {
		t.Fatalf("deleted = %+v", deleted)
	}
}

// TestRenderSortsByTagNotPort covers a fixture where the gate ports run
// opposite to the tags: the documented order is by tag.
func TestRenderSortsByTagNotPort(t *testing.T) {
	content, err := Render([]Node{
		{Tag: "hk-02", GatePort: 13000},
		{Tag: "cn-zj-01", GatePort: 13009},
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	want := "socks5://127.0.0.1:13009#cn-zj-01\nsocks5://127.0.0.1:13000#hk-02\n"
	if content != want {
		t.Fatalf("content = %q, want %q", content, want)
	}
}

// serialClient is an in-memory Client that records the platform filters seen
// by every DeleteLease, so a dropped exclusion is observable without a live
// Resin.
type serialClient struct {
	mu         sync.Mutex
	filters    []string
	exclusions map[string]string
	deletes    []string
	missing    []string
	delay      time.Duration
}

func (c *serialClient) PatchSubscription(context.Context, string, string) (resin.Subscription, error) {
	return resin.Subscription{}, nil
}

func (c *serialClient) DeleteSubscription(context.Context, string) error { return nil }

func (c *serialClient) RefreshSubscription(context.Context, string) error { return nil }

func (c *serialClient) GetPlatform(_ context.Context, id string) (resin.Platform, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return resin.Platform{ID: id, RegexFilters: append([]string(nil), c.filters...)}, nil
}

func (c *serialClient) PatchPlatformFilters(_ context.Context, platformID string, filters []string) (resin.Platform, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.filters = append([]string(nil), filters...)
	return resin.Platform{ID: platformID, RegexFilters: append([]string(nil), filters...)}, nil
}

func (c *serialClient) DeleteLease(_ context.Context, platformID, account string) error {
	if c.delay > 0 {
		time.Sleep(c.delay)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deletes = append(c.deletes, account)
	if want := c.exclusions[account]; want != "" && !containsString(c.filters, want) {
		c.missing = append(c.missing, account+" without "+want)
	}
	return nil
}

func (c *serialClient) PatchSystemConfig(context.Context, string, any) error { return nil }

// TestRotateSerialisesConcurrentRotations runs two rotations for different
// accounts (the rate limit is per account) and asserts each lease delete still
// sees its own exclusion and the original filter list is restored.
func TestRotateSerialisesConcurrentRotations(t *testing.T) {
	client := &serialClient{
		filters: []string{"ajs-live/.*"},
		exclusions: map[string]string{
			"acct-a": "!^ajs-live/cn-zj-01$",
			"acct-b": "!^ajs-live/hk-02$",
		},
		delay: 2 * time.Millisecond,
	}
	pub := New(client, Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})

	rotations := []struct{ account, tag string }{
		{"acct-a", "cn-zj-01"},
		{"acct-b", "hk-02"},
	}
	var wg sync.WaitGroup
	errs := make(chan error, len(rotations))
	for _, item := range rotations {
		wg.Add(1)
		go func(item struct{ account, tag string }) {
			defer wg.Done()
			errs <- pub.Rotate(context.Background(), "plat-1", item.account, "ajs-live/"+item.tag, true)
		}(item)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Rotate: %v", err)
		}
	}

	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.deletes) != len(rotations) {
		t.Fatalf("lease deletes = %v, want %d", client.deletes, len(rotations))
	}
	if len(client.missing) != 0 {
		t.Fatalf("exclusion dropped before lease delete: %v", client.missing)
	}
	if !equalStrings(client.filters, []string{"ajs-live/.*"}) {
		t.Fatalf("filters after rotations = %v, want the original list", client.filters)
	}
}

func TestEnsureProbeConfig(t *testing.T) {
	admin := &fakeAdmin{t: t}
	pub, _ := newPublisher(t, admin, nil)
	if err := pub.EnsureProbeConfig(context.Background()); err != nil {
		t.Fatalf("EnsureProbeConfig: %v", err)
	}
	calls := admin.recorded()
	if len(calls) != 1 || calls[0].Path != "/api/v1/system/config" {
		t.Fatalf("calls = %+v", calls)
	}
	var patch map[string]any
	if err := json.Unmarshal(calls[0].Body, &patch); err != nil {
		t.Fatalf("decode patch: %v", err)
	}
	if patch[ProbeConfigKey] != ProbeConfigValue {
		t.Fatalf("patch = %v", patch)
	}
}

func TestEnsureProbeConfigToleratesOldBuilds(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusMethodNotAllowed} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			admin := &fakeAdmin{t: t, systemConfigStatus: status}
			var logs bytes.Buffer
			pub, _ := newPublisher(t, admin, func(cfg *Config) {
				cfg.Logger = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
			})
			if err := pub.EnsureProbeConfig(context.Background()); err != nil {
				t.Fatalf("EnsureProbeConfig = %v, want nil", err)
			}
			if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), ProbeConfigKey) {
				t.Fatalf("missing warning log: %s", logs.String())
			}
			if strings.Contains(logs.String(), testToken) {
				t.Fatalf("token leaked into log: %s", logs.String())
			}
		})
	}
}

func TestEnsureProbeConfigReturnsOtherErrors(t *testing.T) {
	admin := &fakeAdmin{t: t, systemConfigStatus: http.StatusInternalServerError}
	pub, _ := newPublisher(t, admin, nil)
	if err := pub.EnsureProbeConfig(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

func TestPublishRequiresSubscriptionID(t *testing.T) {
	admin := &fakeAdmin{t: t}
	pub, _ := newPublisher(t, admin, func(cfg *Config) { cfg.SubscriptionID = "" })
	err := pub.Publish(context.Background(), []Node{{Tag: "cn-zj-01", GatePort: 13001}})
	if err == nil || !strings.Contains(err.Error(), "subscription id") {
		t.Fatalf("err = %v", err)
	}
	if len(admin.recorded()) != 0 {
		t.Fatalf("unexpected calls: %+v", admin.recorded())
	}
}
