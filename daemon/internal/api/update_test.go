package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"ajsaccountd/internal/store"
)

// newUpdateServer builds an authenticated server with the update check
// pointed at the given GitHub stub.
func newUpdateServer(t *testing.T, repo, base string) *httptest.Server {
	t.Helper()
	st := store.New()
	server := New(Config{
		AdminToken:    testToken,
		Version:       "0.1.0",
		UpdateRepo:    repo,
		UpdateAPIBase: base,
	}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)
	return ts
}

// gitHubStub answers one JSON payload on /repos/<repo>/releases/latest
// and records whether it was contacted.
func gitHubStub(t *testing.T, status int, body string) (*httptest.Server, *bool) {
	t.Helper()
	called := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.URL.Path != "/repos/example/ajs/releases/latest" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if got := r.Header.Get("Accept"); got != "application/vnd.github+json" {
			t.Errorf("Accept header = %q, want application/vnd.github+json", got)
		}
		if got := r.Header.Get("User-Agent"); got != "ajs-accountd" {
			t.Errorf("User-Agent header = %q, want ajs-accountd", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(ts.Close)
	return ts, &called
}

const releaseJSON = `{"tag_name":"v0.2.0","name":"v0.2.0 — gate hardening","html_url":"https://github.com/example/ajs/releases/tag/v0.2.0","published_at":"2026-10-20T00:00:00Z","draft":false,"prerelease":false}`

func TestSystemUpdateDisabled(t *testing.T) {
	// No UpdateRepo: the answer is enabled:false and the GitHub stub
	// (had it been wired) is never contacted — no network at all.
	stub, called := gitHubStub(t, http.StatusOK, releaseJSON)
	ts := newUpdateServer(t, "", stub.URL)
	resp, body := apiCall(t, ts, http.MethodGet, "/v1/system/update", nil)
	requireStatus(t, resp, http.StatusOK)
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload["enabled"] != false {
		t.Fatalf("enabled = %v, want false", payload["enabled"])
	}
	if payload["current"] != "0.1.0" {
		t.Fatalf("current = %v, want 0.1.0", payload["current"])
	}
	if _, ok := payload["latest"]; ok {
		t.Fatalf("disabled response must not carry latest (body %s)", body)
	}
	if *called {
		t.Fatalf("disabled update check contacted GitHub")
	}
}

func TestSystemUpdateDisabledAuthRequired(t *testing.T) {
	ts := newUpdateServer(t, "", "")
	resp, body := doRequest(t, ts, newRequest(t, http.MethodGet, ts.URL+"/v1/system/update", nil))
	requireStatus(t, resp, http.StatusUnauthorized)
	requireErrorCode(t, body, CodeUnauthorized)
}

func TestSystemUpdateNewer(t *testing.T) {
	stub, _ := gitHubStub(t, http.StatusOK, releaseJSON)
	ts := newUpdateServer(t, "example/ajs", stub.URL)
	resp, body := apiCall(t, ts, http.MethodGet, "/v1/system/update", nil)
	requireStatus(t, resp, http.StatusOK)
	var payload updateCheckResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !payload.Enabled || payload.Current != "0.1.0" || payload.Latest != "v0.2.0" {
		t.Fatalf("unexpected payload: %+v", payload)
	}
	if payload.URL != "https://github.com/example/ajs/releases/tag/v0.2.0" {
		t.Fatalf("url = %q", payload.URL)
	}
	if payload.Name == "" || payload.PublishedAt == "" || payload.CheckedAt == "" {
		t.Fatalf("release metadata missing: %+v", payload)
	}
}

func TestSystemUpdateSameAndOlder(t *testing.T) {
	same := `{"tag_name":"v0.1.0","name":"current","html_url":"https://x","published_at":"2026-01-01T00:00:00Z","draft":false,"prerelease":false}`
	stub, _ := gitHubStub(t, http.StatusOK, same)
	ts := newUpdateServer(t, "example/ajs", stub.URL)
	resp, body := apiCall(t, ts, http.MethodGet, "/v1/system/update", nil)
	requireStatus(t, resp, http.StatusOK)
	payload := decodeBody[updateCheckResponse](t, body)
	if payload.UpdateAvailable {
		t.Fatalf("same version reported as update")
	}

	older := `{"tag_name":"v0.0.9","name":"old","html_url":"https://x","published_at":"2026-01-01T00:00:00Z","draft":false,"prerelease":false}`
	stub2, _ := gitHubStub(t, http.StatusOK, older)
	ts2 := newUpdateServer(t, "example/ajs", stub2.URL)
	resp, body = apiCall(t, ts2, http.MethodGet, "/v1/system/update", nil)
	requireStatus(t, resp, http.StatusOK)
	payload = decodeBody[updateCheckResponse](t, body)
	if payload.UpdateAvailable {
		t.Fatalf("older version reported as update")
	}
}

func TestSystemUpdatePreReleaseRules(t *testing.T) {
	// Pre-release tag while running a stable version: not newer.
	pre := `{"tag_name":"v0.3.0-rc1","name":"rc","html_url":"https://x","published_at":"2026-01-01T00:00:00Z","draft":false,"prerelease":true}`
	stub, _ := gitHubStub(t, http.StatusOK, pre)
	ts := newUpdateServer(t, "example/ajs", stub.URL)
	_, body := apiCall(t, ts, http.MethodGet, "/v1/system/update", nil)
	payload := decodeBody[updateCheckResponse](t, body)
	if payload.UpdateAvailable {
		t.Fatalf("prerelease over stable reported as update")
	}

	// Draft tag while running a stable version: not newer.
	draft := `{"tag_name":"v0.3.0","name":"draft","html_url":"https://x","published_at":"2026-01-01T00:00:00Z","draft":true,"prerelease":false}`
	stub2, _ := gitHubStub(t, http.StatusOK, draft)
	ts2 := newUpdateServer(t, "example/ajs", stub2.URL)
	_, body = apiCall(t, ts2, http.MethodGet, "/v1/system/update", nil)
	payload = decodeBody[updateCheckResponse](t, body)
	if payload.UpdateAvailable {
		t.Fatalf("draft over stable reported as update")
	}

	// Pre-release tag while running a pre-release version: newer rc wins.
	preCurrent := Config{AdminToken: testToken, Version: "0.2.1-rc1", UpdateRepo: "example/ajs", UpdateAPIBase: stub.URL}
	server := New(preCurrent, store.New(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts3 := httptest.NewServer(server)
	t.Cleanup(ts3.Close)
	_, body = apiCall(t, ts3, http.MethodGet, "/v1/system/update", nil)
	payload = decodeBody[updateCheckResponse](t, body)
	if !payload.UpdateAvailable {
		t.Fatalf("prerelease v0.3.0-rc1 over 0.2.1-rc1 not reported as update")
	}
}

func TestSystemUpdateUpstreamErrors(t *testing.T) {
	// HTTP 500 from GitHub.
	stub, _ := gitHubStub(t, http.StatusInternalServerError, `{"message":"nope"}`)
	ts := newUpdateServer(t, "example/ajs", stub.URL)
	resp, body := apiCall(t, ts, http.MethodGet, "/v1/system/update", nil)
	requireStatus(t, resp, http.StatusBadGateway)
	requireErrorCode(t, body, CodeUpstreamUnavailable)

	// Malformed JSON.
	stub2, _ := gitHubStub(t, http.StatusOK, `{"tag_name":`)
	ts2 := newUpdateServer(t, "example/ajs", stub2.URL)
	resp, body = apiCall(t, ts2, http.MethodGet, "/v1/system/update", nil)
	requireStatus(t, resp, http.StatusBadGateway)
	requireErrorCode(t, body, CodeUpstreamUnavailable)

	// Missing tag_name.
	stub3, _ := gitHubStub(t, http.StatusOK, `{"name":"untagged"}`)
	ts3 := newUpdateServer(t, "example/ajs", stub3.URL)
	resp, body = apiCall(t, ts3, http.MethodGet, "/v1/system/update", nil)
	requireStatus(t, resp, http.StatusBadGateway)
	requireErrorCode(t, body, CodeUpstreamUnavailable)

	// Transport failure: the base points at a closed server.
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	ts4 := newUpdateServer(t, "example/ajs", deadURL)
	resp, body = apiCall(t, ts4, http.MethodGet, "/v1/system/update", nil)
	requireStatus(t, resp, http.StatusBadGateway)
	requireErrorCode(t, body, CodeUpstreamUnavailable)
}

func TestUpdateVersionCompare(t *testing.T) {
	cases := []struct {
		tag, current string
		want         bool
	}{
		{"v0.2.0", "0.1.0", true},
		{"0.2.0", "v0.1.0", true},
		{"v0.1.0", "v0.1.0", false},
		{"v0.1.9", "v0.1.10", false},
		{"v1.0", "v0.9.9", true},
		{"v0.10.0", "v0.9.0", true},
		{"v0.2.0", "0.2.0", false},
		{"v0.2.1", "v0.2.0", true},
	}
	for _, tc := range cases {
		if got := newerVersion(tc.tag, tc.current, false, false); got != tc.want {
			t.Errorf("newerVersion(%q, %q) = %v, want %v", tc.tag, tc.current, got, tc.want)
		}
	}
}
