package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/store"
)

// rateLimitClock is a mutex-guarded fake clock shared by the test goroutine
// and the HTTP handler goroutines, so -race sees a real happens-before edge.
type rateLimitClock struct {
	mu  sync.Mutex
	now time.Time
}

func newRateLimitClock() *rateLimitClock {
	return &rateLimitClock{now: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)}
}

func (c *rateLimitClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *rateLimitClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newRateLimitTestServer(t *testing.T, clock *rateLimitClock) (*httptest.Server, *store.Store) {
	t.Helper()
	st := store.New()
	server := New(
		Config{AdminToken: testToken, RateLimitClock: clock.Now},
		st,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)
	return ts, st
}

func requireHeader(t *testing.T, resp *http.Response, name, want string) {
	t.Helper()
	if got := resp.Header.Get(name); got != want {
		t.Fatalf("%s %s: %s = %q, want %q", resp.Request.Method, resp.Request.URL.Path, name, got, want)
	}
}

func TestScopedKeyRateLimitHeadersAnd429(t *testing.T) {
	clock := newRateLimitClock()
	ts, _ := newRateLimitTestServer(t, clock)
	created, _ := mustCreateScopedKey(t, ts, map[string]any{
		"userId":          "u-01",
		"label":           "two per minute",
		"scopes":          []string{"accounts:read"},
		"rateLimitPerMin": 2,
	})

	resp, _ := scopedCall(t, ts, created.Secret, http.MethodGet, "/v1/accounts", nil)
	requireStatus(t, resp, http.StatusOK)
	requireHeader(t, resp, "X-RateLimit-Limit", "2")
	requireHeader(t, resp, "X-RateLimit-Remaining", "1")
	requireHeader(t, resp, "X-RateLimit-Reset", strconv.FormatInt(clock.Now().Add(30*time.Second).Unix(), 10))

	resp, _ = scopedCall(t, ts, created.Secret, http.MethodGet, "/v1/accounts", nil)
	requireStatus(t, resp, http.StatusOK)
	requireHeader(t, resp, "X-RateLimit-Remaining", "0")

	resp, body := scopedCall(t, ts, created.Secret, http.MethodGet, "/v1/accounts", nil)
	requireStatus(t, resp, http.StatusTooManyRequests)
	requireErrorCode(t, body, CodeRateLimited)
	requireHeader(t, resp, "Retry-After", "30")
	requireHeader(t, resp, "X-RateLimit-Limit", "2")
	requireHeader(t, resp, "X-RateLimit-Remaining", "0")
	requireHeader(t, resp, "X-RateLimit-Reset", strconv.FormatInt(clock.Now().Add(time.Minute).Unix(), 10))

	// One refill interval restores exactly one token.
	clock.Advance(30 * time.Second)
	resp, _ = scopedCall(t, ts, created.Secret, http.MethodGet, "/v1/accounts", nil)
	requireStatus(t, resp, http.StatusOK)
	requireHeader(t, resp, "X-RateLimit-Remaining", "0")
}

func TestScopedKeyRateLimitHeadersOnForbidden(t *testing.T) {
	clock := newRateLimitClock()
	ts, _ := newRateLimitTestServer(t, clock)
	created, _ := mustCreateScopedKey(t, ts, map[string]any{
		"userId": "u-01",
		"label":  "accounts reader",
		"scopes": []string{"accounts:read"},
	})

	resp, body := scopedCall(t, ts, created.Secret, http.MethodGet, "/v1/settings", nil)
	requireStatus(t, resp, http.StatusForbidden)
	requireErrorCode(t, body, CodeForbidden)
	requireHeader(t, resp, "X-RateLimit-Limit", "60")
	requireHeader(t, resp, "X-RateLimit-Remaining", "59")
}

func TestScopedKeyRateLimitIsPerKey(t *testing.T) {
	clock := newRateLimitClock()
	ts, _ := newRateLimitTestServer(t, clock)
	first, _ := mustCreateScopedKey(t, ts, map[string]any{
		"userId":          "u-01",
		"label":           "first",
		"scopes":          []string{"accounts:read"},
		"rateLimitPerMin": 1,
	})
	second, _ := mustCreateScopedKey(t, ts, map[string]any{
		"userId":          "u-01",
		"label":           "second",
		"scopes":          []string{"accounts:read"},
		"rateLimitPerMin": 1,
	})

	resp, _ := scopedCall(t, ts, first.Secret, http.MethodGet, "/v1/accounts", nil)
	requireStatus(t, resp, http.StatusOK)
	resp, body := scopedCall(t, ts, first.Secret, http.MethodGet, "/v1/accounts", nil)
	requireStatus(t, resp, http.StatusTooManyRequests)
	requireErrorCode(t, body, CodeRateLimited)
	requireHeader(t, resp, "Retry-After", "60")

	resp, _ = scopedCall(t, ts, second.Secret, http.MethodGet, "/v1/accounts", nil)
	requireStatus(t, resp, http.StatusOK)
	requireHeader(t, resp, "X-RateLimit-Remaining", "0")
}

func TestAdminTokenBypassesRateLimit(t *testing.T) {
	clock := newRateLimitClock()
	ts, _ := newRateLimitTestServer(t, clock)

	for i := range 100 {
		resp, _ := apiCall(t, ts, http.MethodGet, "/v1/accounts", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("admin request %d: status = %d, want 200", i+1, resp.StatusCode)
		}
		if got := resp.Header.Get("X-RateLimit-Limit"); got != "" {
			t.Fatalf("admin response carries X-RateLimit-Limit = %q", got)
		}
	}
}

func TestScopedKeyRateLimitZeroIsUnlimited(t *testing.T) {
	clock := newRateLimitClock()
	ts, _ := newRateLimitTestServer(t, clock)
	created, _ := mustCreateScopedKey(t, ts, map[string]any{
		"userId":          "u-01",
		"label":           "unlimited",
		"scopes":          []string{"accounts:read"},
		"rateLimitPerMin": 0,
	})
	if created.Key.RateLimitPerMin != 0 {
		t.Fatalf("created key rateLimitPerMin = %d, want 0", created.Key.RateLimitPerMin)
	}

	for i := range 100 {
		resp, _ := scopedCall(t, ts, created.Secret, http.MethodGet, "/v1/accounts", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("unlimited request %d: status = %d, want 200", i+1, resp.StatusCode)
		}
		requireHeader(t, resp, "X-RateLimit-Limit", "0")
		requireHeader(t, resp, "X-RateLimit-Remaining", "0")
		requireHeader(t, resp, "X-RateLimit-Reset", "0")
	}
}

func TestRevokedScopedKeyAnswers401BeforeRateLimit(t *testing.T) {
	clock := newRateLimitClock()
	ts, _ := newRateLimitTestServer(t, clock)
	created, _ := mustCreateScopedKey(t, ts, map[string]any{
		"userId":          "u-01",
		"label":           "revoked",
		"scopes":          []string{"accounts:read"},
		"rateLimitPerMin": 1,
	})
	resp, _ := apiCall(t, ts, http.MethodDelete, "/v1/scoped-keys/"+created.Key.ID, nil)
	requireStatus(t, resp, http.StatusNoContent)

	resp, body := scopedCall(t, ts, created.Secret, http.MethodGet, "/v1/accounts", nil)
	requireStatus(t, resp, http.StatusUnauthorized)
	requireErrorCode(t, body, CodeUnauthorized)
	if got := resp.Header.Get("Retry-After"); got != "" {
		t.Fatalf("revoked key response carries Retry-After = %q", got)
	}
}

func TestRateLimitHeadersExposedToCORS(t *testing.T) {
	clock := newRateLimitClock()
	ts, _ := newRateLimitTestServer(t, clock)
	req := newRequest(t, http.MethodGet, ts.URL+"/v1/accounts", nil)
	req.Header.Set("Origin", "http://127.0.0.1:18081")
	resp, _ := doRequest(t, ts, req)
	requireStatus(t, resp, http.StatusUnauthorized)
	exposed := resp.Header.Get("Access-Control-Expose-Headers")
	for _, header := range []string{"X-RateLimit-Limit", "X-RateLimit-Remaining", "X-RateLimit-Reset"} {
		if !strings.Contains(exposed, header) {
			t.Fatalf("expose headers = %q, missing %s", exposed, header)
		}
	}
}

func TestScopedKeyRateLimitCreateDefaultsAndValidation(t *testing.T) {
	clock := newRateLimitClock()
	ts, _ := newRateLimitTestServer(t, clock)

	defaulted, _ := mustCreateScopedKey(t, ts, map[string]any{
		"userId": "u-01",
		"label":  "default budget",
		"scopes": []string{"accounts:read"},
	})
	if defaulted.Key.RateLimitPerMin != model.DefaultScopedKeyRateLimitPerMin {
		t.Fatalf("default rateLimitPerMin = %d, want %d", defaulted.Key.RateLimitPerMin, model.DefaultScopedKeyRateLimitPerMin)
	}
	resp, body := apiCall(t, ts, http.MethodGet, "/v1/scoped-keys/"+defaulted.Key.ID, nil)
	requireStatus(t, resp, http.StatusOK)
	if got := decodeBody[model.ScopedKey](t, body).RateLimitPerMin; got != model.DefaultScopedKeyRateLimitPerMin {
		t.Fatalf("detail rateLimitPerMin = %d, want %d", got, model.DefaultScopedKeyRateLimitPerMin)
	}

	resp, body = apiCall(t, ts, http.MethodPost, "/v1/scoped-keys", map[string]any{
		"userId":          "u-01",
		"label":           "negative",
		"scopes":          []string{"accounts:read"},
		"rateLimitPerMin": -1,
	})
	requireStatus(t, resp, http.StatusBadRequest)
	requireErrorCode(t, body, CodeInvalidArgument)
}
