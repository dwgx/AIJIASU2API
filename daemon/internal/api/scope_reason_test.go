package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// requireErrorReason pins the stable machine-readable reason of an error
// envelope. A missing reason is a failure, not a generic fallback: clients
// branch on the value instead of parsing the message.
func requireErrorReason(t *testing.T, body []byte, want string) {
	t.Helper()
	var payload errorResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode error body %q: %v", body, err)
	}
	if payload.Error.Reason != want {
		t.Fatalf("error reason = %q, want %q (body %s)", payload.Error.Reason, want, body)
	}
}

// TestScopedKeyForbiddenReasons pins AJS-440: every refused global read
// carries error.reason=scoped_key_restricted, a key missing the route scope
// carries error.reason=scope_required, the human-readable message is
// unchanged, and admin/unrestricted callers still get 200.
func TestScopedKeyForbiddenReasons(t *testing.T) {
	ts, _ := newPlatformServer(t, Config{Usage: usageAggregator()})

	globalReads := []string{
		"/v1/usage",
		"/v1/audit",
		"/v1/audit/export.csv",
		"/v1/requests",
		"/metrics",
	}

	restricted := mustIssueScopedKey(t, ts,
		[]string{"usage:read", "audit:read", "requests:read", "metrics:read"},
		[]string{"acc-01"}, nil)
	for _, path := range globalReads {
		resp, body := scopedCall(t, ts, restricted.Secret, http.MethodGet, path, nil)
		requireStatus(t, resp, http.StatusForbidden)
		requireErrorCode(t, body, CodeForbidden)
		requireErrorReason(t, body, ReasonScopedKeyRestricted)
		if !strings.Contains(string(body), "scoped key is restricted to named accounts") {
			t.Fatalf("%s message changed: %s", path, body)
		}
	}

	// The usage account filter naming an account outside the key is refused by
	// the auth middleware before the handler; that refusal carries the same
	// reason and keeps its own message.
	resp, body := scopedCall(t, ts, restricted.Secret, http.MethodGet, "/v1/usage?account=acc-02", nil)
	requireStatus(t, resp, http.StatusForbidden)
	requireErrorCode(t, body, CodeForbidden)
	requireErrorReason(t, body, ReasonScopedKeyRestricted)
	if !strings.Contains(string(body), "scoped key is not allowed to use this account") {
		t.Fatalf("account-filter message changed: %s", body)
	}

	// A region-only restriction is the same refusal: the request log carries
	// no region, so a region-restricted aggregate cannot be honest either.
	region := mustIssueScopedKey(t, ts, []string{"usage:read"}, nil, []string{"hk"})
	resp, body = scopedCall(t, ts, region.Secret, http.MethodGet, "/v1/usage", nil)
	requireStatus(t, resp, http.StatusForbidden)
	requireErrorReason(t, body, ReasonScopedKeyRestricted)

	// A key with the wrong scope is refused by the router before the handler
	// runs, and the reason names the missing grant.
	scopeless := mustIssueScopedKey(t, ts, []string{"accounts:read"}, []string{"acc-01"}, nil)
	for _, path := range globalReads {
		resp, body := scopedCall(t, ts, scopeless.Secret, http.MethodGet, path, nil)
		requireStatus(t, resp, http.StatusForbidden)
		requireErrorCode(t, body, CodeForbidden)
		requireErrorReason(t, body, ReasonScopeRequired)
		if !strings.Contains(string(body), "credential lacks the permission required by this route") {
			t.Fatalf("%s message changed: %s", path, body)
		}
	}

	// Admin tokens and unrestricted scoped keys keep their 200 contract.
	open := mustIssueScopedKey(t, ts,
		[]string{"usage:read", "audit:read", "requests:read", "metrics:read"}, nil, nil)
	for _, path := range globalReads {
		resp, _ := scopedCall(t, ts, open.Secret, http.MethodGet, path, nil)
		requireStatus(t, resp, http.StatusOK)
		resp, _ = apiCall(t, ts, http.MethodGet, path, nil)
		requireStatus(t, resp, http.StatusOK)
	}
}
