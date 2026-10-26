package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"ajsaccountd/internal/store"
)

// TestAJS415LegacyWhoamiReportsAdmin pins the compatibility-mode contract: the
// shared bearer has full admin authority even though it has no operator entry.
func TestAJS415LegacyWhoamiReportsAdmin(t *testing.T) {
	st := store.New()
	srv := New(Config{AdminToken: testToken}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	req := newRequest(t, http.MethodGet, ts.URL+"/v1/whoami", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, body := doRequest(t, ts, req)
	requireStatus(t, resp, http.StatusOK)
	got := decodeBody[whoamiResponse](t, body)
	if got.Name != adminOperatorFallback || got.Role != string(RoleAdmin) || got.Kind != "legacy" || got.StrictRBAC {
		t.Fatalf("legacy whoami = %+v, want operator/admin/legacy/false", got)
	}

	// An audit label names the legacy request without changing its role.
	req = newRequest(t, http.MethodGet, ts.URL+"/v1/whoami", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set(adminOperatorHeader, "alice")
	resp, body = doRequest(t, ts, req)
	requireStatus(t, resp, http.StatusOK)
	got = decodeBody[whoamiResponse](t, body)
	if got.Name != "alice" || got.Role != string(RoleAdmin) || got.Kind != "legacy" {
		t.Fatalf("labelled legacy whoami = %+v, want alice/admin/legacy", got)
	}
}
