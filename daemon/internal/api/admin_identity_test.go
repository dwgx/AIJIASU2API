package api

import (
	"net/http"
	"strings"
	"testing"

	"ajsaccountd/internal/model"
)

// TestAdminOperatorAuditIdentity is the AJS-405 acceptance test: two
// proxy-authenticated operators must produce distinct audit actors for the
// same admin mutation, and a request without the header must keep the
// historical "operator" fallback. The bearer is always the shared admin
// token; X-AJS-Operator only labels the actor.
func TestAdminOperatorAuditIdentity(t *testing.T) {
	ts, st, _ := newAdminCSRFServer(t)

	suspendAs := func(t *testing.T, operator string) model.AuditEvent {
		t.Helper()
		headers := map[string]string{adminCSRFHeader: adminCSRFValue}
		if operator != "" {
			headers[adminOperatorHeader] = operator
		}
		resp, body := adminCSRFRequest(t, ts, http.MethodPost, "/v1/users/u-01/suspend", nil, headers)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("suspend as %q: status = %d (body %s)", operator, resp.StatusCode, body)
		}
		row, ok := findAuditAction(st.Audit(20), "user.suspend")
		if !ok {
			t.Fatalf("suspend as %q wrote no user.suspend audit row", operator)
		}
		return row
	}

	alice := suspendAs(t, "alice")
	if alice.Actor != "alice" {
		t.Fatalf("alice audit actor = %q, want alice", alice.Actor)
	}
	bob := suspendAs(t, "bob")
	if bob.Actor != "bob" {
		t.Fatalf("bob audit actor = %q, want bob", bob.Actor)
	}
	if alice.ID == bob.ID {
		t.Fatal("two mutations returned the same audit row")
	}
	fallback := suspendAs(t, "")
	if fallback.Actor != adminOperatorFallback {
		t.Fatalf("missing header actor = %q, want %q", fallback.Actor, adminOperatorFallback)
	}

	// A malformed or oversized header must not be copied into an audit row;
	// it falls back exactly like an absent one.
	malformed := suspendAs(t, "alice forged")
	if malformed.Actor != adminOperatorFallback {
		t.Fatalf("malformed header actor = %q, want %q", malformed.Actor, adminOperatorFallback)
	}
	oversized := suspendAs(t, strings.Repeat("a", maxAdminOperatorLen+1))
	if oversized.Actor != adminOperatorFallback {
		t.Fatalf("oversized header actor = %q, want %q", oversized.Actor, adminOperatorFallback)
	}
}

// TestAdminOperatorAuditIdentityAcrossMutationFamilies pins attribution on
// representative API-layer and store-layer audit writers: backup create,
// account export and device generation.
func TestAdminOperatorAuditIdentityAcrossMutationFamilies(t *testing.T) {
	ts, st, _ := newAdminCSRFServer(t)

	cases := []struct {
		name   string
		method string
		path   string
		body   any
		action string
		status int
	}{
		{
			name:   "backup",
			method: http.MethodPost,
			path:   "/v1/backup",
			body:   map[string]any{"passphrase": backupTestPassphrase},
			action: "backup.create",
			status: http.StatusOK,
		},
		{
			name:   "account export",
			method: http.MethodPost,
			path:   "/v1/accounts/export",
			body:   map[string]any{"format": "encrypted", "passphrase": "operator-export-passphrase"},
			action: "account.export",
			status: http.StatusOK,
		},
		{
			name:   "account create",
			method: http.MethodPost,
			path:   "/v1/accounts",
			body:   map[string]any{"label": "operator-created", "username": "ops@example.test", "password": "operator-password", "platform": "linux"},
			action: "account.create",
			status: http.StatusCreated,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			headers := map[string]string{
				adminCSRFHeader:     adminCSRFValue,
				adminOperatorHeader: "alice",
			}
			resp, body := adminCSRFRequest(t, ts, tc.method, tc.path, tc.body, headers)
			if resp.StatusCode != tc.status {
				t.Fatalf("status = %d, want %d (body %s)", resp.StatusCode, tc.status, body)
			}
			row, ok := findAuditAction(st.Audit(30), tc.action)
			if !ok {
				t.Fatalf("no %s audit row", tc.action)
			}
			if row.Actor != "alice" {
				t.Fatalf("%s actor = %q, want alice", tc.action, row.Actor)
			}
		})
	}
}

// TestAdminOperatorHeaderDoesNotAuthenticate proves the header cannot stand
// in for the shared bearer and that a rejected request writes no audit row.
func TestAdminOperatorHeaderDoesNotAuthenticate(t *testing.T) {
	ts, st := newTestServer(t)
	before := len(st.Audit(1000))

	for _, tc := range []struct {
		name   string
		header string
	}{
		{name: "operator header only", header: "alice"},
		{name: "invalid bearer plus operator", header: "alice"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := newRequest(t, http.MethodPost, ts.URL+"/v1/users/u-01/suspend", nil)
			req.Header.Set(adminCSRFHeader, adminCSRFValue)
			req.Header.Set(adminOperatorHeader, tc.header)
			if tc.name == "invalid bearer plus operator" {
				req.Header.Set("Authorization", "Bearer not-the-admin-token")
			}
			resp, body := doRequest(t, ts, req)
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401 (body %s)", resp.StatusCode, body)
			}
			requireErrorCode(t, body, CodeUnauthorized)
		})
	}
	if after := len(st.Audit(1000)); after != before {
		t.Fatalf("unauthenticated requests wrote %d audit rows", after-before)
	}
	user, ok := st.User("u-01")
	if !ok || !user.Enabled {
		t.Fatalf("unauthenticated request changed user state: %+v (ok=%v)", user, ok)
	}
}

// TestAdminOperatorScopedKeyCannotSpoof covers the other admin-capable
// principal: an admin-scoped key has its own "key:" principal and must not be
// able to claim an operator name through the proxy header.
func TestAdminOperatorScopedKeyCannotSpoof(t *testing.T) {
	ts, st := newTestServer(t)
	created, body := apiCall(t, ts, http.MethodPost, "/v1/scoped-keys", map[string]any{
		"userId": "u-01",
		"label":  "operator-spoof",
		"scopes": []string{"admin"},
	})
	if created.StatusCode != http.StatusCreated {
		t.Fatalf("create scoped key: status = %d (body %s)", created.StatusCode, body)
	}
	key := decodeBody[model.ScopedKeyCreated](t, body)

	req := newRequest(t, http.MethodPost, ts.URL+"/v1/users/u-01/suspend", nil)
	req.Header.Set("Authorization", "Bearer "+key.Secret)
	req.Header.Set(adminCSRFHeader, adminCSRFValue)
	req.Header.Set(adminOperatorHeader, "mallory")
	resp, respBody := doRequest(t, ts, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", resp.StatusCode, respBody)
	}
	row, ok := findAuditAction(st.Audit(20), "user.suspend")
	if !ok {
		t.Fatal("no user.suspend audit row")
	}
	if row.Actor != "key:"+key.Key.Prefix {
		t.Fatalf("scoped key actor = %q, want %q (the header must not name a key actor)",
			row.Actor, "key:"+key.Key.Prefix)
	}
}

// TestScopedKeyAuditActor pins the attribution of a scoped-key mutation: the
// audit row names the key principal ("key:<prefix>"), never the generic
// operator fallback and never the secret, while admin-token attribution is
// unchanged (the proxy-authenticated name when present, "operator" otherwise).
func TestScopedKeyAuditActor(t *testing.T) {
	ts, st := newTestServer(t)
	key := restrictedWriteKey(t, ts, []string{"acc-01"}, nil)

	resp, body := scopedCall(t, ts, key.Secret, http.MethodDelete, "/v1/leases/lease-01",
		map[string]any{"confirm": true})
	requireStatus(t, resp, http.StatusNoContent)
	row, ok := findAuditAction(st.Audit(50), "lease.revoke")
	if !ok {
		t.Fatalf("no lease.revoke audit row (body %s)", body)
	}
	want := "key:" + key.Key.Prefix
	if row.Actor != want {
		t.Fatalf("scoped-key audit actor = %q, want %q", row.Actor, want)
	}
	if strings.Contains(row.Actor, key.Secret) {
		t.Fatal("the audit actor carries the key secret")
	}

	// Admin attribution is unchanged: the historical fallback without the
	// proxy header…
	resp, _ = apiCall(t, ts, http.MethodDelete, "/v1/leases/lease-02", map[string]any{"confirm": true})
	requireStatus(t, resp, http.StatusNoContent)
	row, ok = findAuditAction(st.Audit(50), "lease.revoke")
	if !ok || row.Actor != adminOperatorFallback {
		t.Fatalf("admin audit actor = %q, want %q", row.Actor, adminOperatorFallback)
	}

	// …and the authenticated operator name when the header is present.
	req := newRequest(t, http.MethodDelete, ts.URL+"/v1/leases/lease-04", map[string]any{"confirm": true})
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set(adminCSRFHeader, adminCSRFValue)
	req.Header.Set(adminOperatorHeader, "alice")
	resp, body = doRequest(t, ts, req)
	requireStatus(t, resp, http.StatusNoContent)
	row, ok = findAuditAction(st.Audit(50), "lease.revoke")
	if !ok || row.Actor != "alice" {
		t.Fatalf("operator audit actor = %q, want alice (body %s)", row.Actor, body)
	}
}

func TestSanitizeOperator(t *testing.T) {
	cases := map[string]string{
		"":                             "",
		"alice":                        "alice",
		"  alice  ":                    "alice",
		"ops.team-1_2@example+tag":     "ops.team-1_2@example+tag",
		"bad actor":                    "",
		"bad\tactor":                   "",
		"alice:root":                   "",
		"alice/root":                   "",
		strings.Repeat("a", 64):        strings.Repeat("a", 64),
		strings.Repeat("a", 65):        "",
		"<script>alert(1)</script>":    "",
		"alice\r\nX-AJS-Operator: bob": "",
	}
	for raw, want := range cases {
		if got := sanitizeOperator(raw); got != want {
			t.Errorf("sanitizeOperator(%q) = %q, want %q", raw, got, want)
		}
	}
}
