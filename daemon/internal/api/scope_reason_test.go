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

// TestScopedKeyForbiddenReasons pins the machine-readable reasons of a
// scoped-key 403. The metrics exposition is a global, unattributed read, so an
// account-restricted key is refused with scoped_key_restricted. The audit log
// is operator-global, so every non-admin scoped key is refused with
// admin_required whatever its grant. A key missing the route scope carries
// scope_required. Usage and the request log are per-user reads (AJS-451), so
// they are served rather than refused; a scoped account= usage filter is the
// handler's 400.
func TestScopedKeyForbiddenReasons(t *testing.T) {
	ts, _ := newPlatformServer(t, Config{Usage: usageAggregator()})

	operatorReads := []string{"/v1/audit", "/v1/audit/export.csv"}

	restricted := mustIssueScopedKey(t, ts,
		[]string{"usage:read", "audit:read", "requests:read", "metrics:read"},
		[]string{"acc-01"}, nil)
	resp, body := scopedCall(t, ts, restricted.Secret, http.MethodGet, "/metrics", nil)
	requireStatus(t, resp, http.StatusForbidden)
	requireErrorCode(t, body, CodeForbidden)
	requireErrorReason(t, body, ReasonScopedKeyRestricted)
	if !strings.Contains(string(body), "scoped key is restricted to named accounts") {
		t.Fatalf("/metrics message changed: %s", body)
	}
	// The audit refusal is about the operator boundary, not the account list:
	// the same key with no accountIds answers the same reason.
	for _, path := range operatorReads {
		resp, body := scopedCall(t, ts, restricted.Secret, http.MethodGet, path, nil)
		requireStatus(t, resp, http.StatusForbidden)
		requireErrorCode(t, body, CodeForbidden)
		requireErrorReason(t, body, ReasonAdminRequired)
		if !strings.Contains(string(body), "operator-global") {
			t.Fatalf("%s message changed: %s", path, body)
		}
	}

	// Usage and the request log are per-user reads, not refused global ones.
	for _, path := range []string{"/v1/usage", "/v1/requests"} {
		resp, body := scopedCall(t, ts, restricted.Secret, http.MethodGet, path, nil)
		requireStatus(t, resp, http.StatusOK)
		if strings.Contains(string(body), "scoped key is restricted to named accounts") {
			t.Fatalf("%s still carries a restriction refusal: %s", path, body)
		}
	}

	// A scoped account= usage filter is INVALID_ARGUMENT: the log has no
	// stable account id, so the handler refuses the filter instead of guessing.
	resp, body = scopedCall(t, ts, restricted.Secret, http.MethodGet, "/v1/usage?account=acc-02", nil)
	requireStatus(t, resp, http.StatusBadRequest)
	requireErrorCode(t, body, CodeInvalidArgument)
	if !strings.Contains(string(body), "account filtering is not available to a scoped key") {
		t.Fatalf("account-filter message changed: %s", body)
	}

	// A region-only restriction is the same per-user view, not a refusal.
	region := mustIssueScopedKey(t, ts, []string{"usage:read"}, nil, []string{"hk"})
	resp, body = scopedCall(t, ts, region.Secret, http.MethodGet, "/v1/usage", nil)
	requireStatus(t, resp, http.StatusOK)

	// A key with the wrong scope is refused by the router before the handler
	// runs, and the reason names the missing grant.
	scopeless := mustIssueScopedKey(t, ts, []string{"accounts:read"}, []string{"acc-01"}, nil)
	for _, path := range append([]string{"/metrics"}, operatorReads...) {
		resp, body := scopedCall(t, ts, scopeless.Secret, http.MethodGet, path, nil)
		requireStatus(t, resp, http.StatusForbidden)
		requireErrorCode(t, body, CodeForbidden)
		requireErrorReason(t, body, ReasonScopeRequired)
		if !strings.Contains(string(body), "credential lacks the permission required by this route") {
			t.Fatalf("%s message changed: %s", path, body)
		}
	}

	// An unrestricted customer key keeps the metrics exposition and not the
	// audit log: no account grant turns audit into a customer surface.
	open := mustIssueScopedKey(t, ts,
		[]string{"usage:read", "audit:read", "requests:read", "metrics:read"}, nil, nil)
	for _, path := range operatorReads {
		resp, body := scopedCall(t, ts, open.Secret, http.MethodGet, path, nil)
		requireStatus(t, resp, http.StatusForbidden)
		requireErrorReason(t, body, ReasonAdminRequired)
	}
	resp, _ = scopedCall(t, ts, open.Secret, http.MethodGet, "/metrics", nil)
	requireStatus(t, resp, http.StatusOK)

	// Admin tokens and admin-scoped keys keep their 200 contract.
	adminKey := mustIssueScopedKey(t, ts, []string{"admin"}, nil, nil)
	for _, path := range append([]string{"/metrics"}, operatorReads...) {
		resp, _ := scopedCall(t, ts, adminKey.Secret, http.MethodGet, path, nil)
		requireStatus(t, resp, http.StatusOK)
		resp, _ = apiCall(t, ts, http.MethodGet, path, nil)
		requireStatus(t, resp, http.StatusOK)
	}
}
