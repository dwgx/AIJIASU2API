package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ajsaccountd/internal/model"
)

func mustCreateScopedKey(t *testing.T, ts *httptest.Server, body any) (model.ScopedKeyCreated, []byte) {
	t.Helper()
	resp, data := apiCall(t, ts, http.MethodPost, "/v1/scoped-keys", body)
	requireStatus(t, resp, http.StatusCreated)
	return decodeBody[model.ScopedKeyCreated](t, data), data
}

func scopedCall(t *testing.T, ts *httptest.Server, secret, method, path string, body any) (*http.Response, []byte) {
	t.Helper()
	req := newRequest(t, method, ts.URL+path, body)
	req.Header.Set("Authorization", "Bearer "+secret)
	return doRequest(t, ts, req)
}

func TestScopedKeyCRUDShowsSecretOnce(t *testing.T) {
	ts, st := newTestServer(t)

	created, createBody := mustCreateScopedKey(t, ts, map[string]any{
		"userId":     "u-01",
		"label":      "CI read only",
		"scopes":     []string{"accounts:read"},
		"accountIds": []string{"acc-01"},
		"regions":    []string{"cn-zj"},
	})
	if !strings.HasPrefix(created.Secret, "ajs_sk_") {
		t.Fatalf("secret = %q, want the ajs_sk_ prefix", created.Secret)
	}
	if created.Key.ID == "" || created.Key.UserID != "u-01" || created.Key.UserName != "ada.ops" {
		t.Fatalf("created key = %+v", created.Key)
	}
	if created.Key.Prefix != created.Secret[:16] {
		t.Fatalf("prefix = %q, want %q", created.Key.Prefix, created.Secret[:16])
	}
	if created.Key.ExpiresAt != nil || created.Key.LastUsedAt != nil || created.Key.RevokedAt != nil {
		t.Fatalf("new key has optional timestamps set: %+v", created.Key)
	}
	if !bytes.Contains(createBody, []byte(created.Secret)) {
		t.Fatal("create response does not carry the one-time secret")
	}

	// The listing and the detail view must never repeat the secret.
	resp, listBody := apiCall(t, ts, http.MethodGet, "/v1/scoped-keys", nil)
	requireStatus(t, resp, http.StatusOK)
	listed := decodeBody[[]model.ScopedKey](t, listBody)
	if len(listed) != 1 || listed[0].ID != created.Key.ID {
		t.Fatalf("list = %+v, want the created key", listed)
	}
	if bytes.Contains(listBody, []byte(created.Secret)) {
		t.Fatal("list response contains the scoped-key secret")
	}
	resp, detailBody := apiCall(t, ts, http.MethodGet, "/v1/scoped-keys/"+created.Key.ID, nil)
	requireStatus(t, resp, http.StatusOK)
	if bytes.Contains(detailBody, []byte(created.Secret)) {
		t.Fatal("detail response contains the scoped-key secret")
	}

	// The persisted snapshot carries the hash, never the secret.
	snapshot, err := st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if bytes.Contains(snapshot, []byte(created.Secret)) {
		t.Fatal("snapshot contains the scoped-key secret")
	}

	// Revocation is a delete of the credential, not of the row.
	resp, _ = apiCall(t, ts, http.MethodDelete, "/v1/scoped-keys/"+created.Key.ID, nil)
	requireStatus(t, resp, http.StatusNoContent)
	resp, revokedBody := apiCall(t, ts, http.MethodGet, "/v1/scoped-keys/"+created.Key.ID, nil)
	requireStatus(t, resp, http.StatusOK)
	revoked := decodeBody[model.ScopedKey](t, revokedBody)
	if revoked.RevokedAt == nil {
		t.Fatal("revokedAt is nil after DELETE")
	}
	resp, _ = apiCall(t, ts, http.MethodDelete, "/v1/scoped-keys/"+created.Key.ID, nil)
	requireStatus(t, resp, http.StatusNoContent)

	resp, body := scopedCall(t, ts, created.Secret, http.MethodGet, "/v1/accounts", nil)
	requireStatus(t, resp, http.StatusUnauthorized)
	requireErrorCode(t, body, CodeUnauthorized)
}

func TestScopedKeyEnforcementMatrix(t *testing.T) {
	ts, _ := newTestServer(t)

	readKey, _ := mustCreateScopedKey(t, ts, map[string]any{
		"userId": "u-01",
		"label":  "accounts reader",
		"scopes": []string{"accounts:read"},
	})
	resp, body := scopedCall(t, ts, readKey.Secret, http.MethodGet, "/v1/accounts", nil)
	requireStatus(t, resp, http.StatusOK)
	resp, body = scopedCall(t, ts, readKey.Secret, http.MethodPost, "/v1/accounts", map[string]any{
		"label": "forbidden", "username": "nope", "password": "x", "platform": "android",
	})
	requireStatus(t, resp, http.StatusForbidden)
	requireErrorCode(t, body, CodeForbidden)
	resp, body = scopedCall(t, ts, readKey.Secret, http.MethodGet, "/v1/settings", nil)
	requireStatus(t, resp, http.StatusForbidden)
	requireErrorCode(t, body, CodeForbidden)
	resp, _ = scopedCall(t, ts, readKey.Secret, http.MethodGet, "/v1/metrics", nil)
	requireStatus(t, resp, http.StatusForbidden)

	metricsKey, _ := mustCreateScopedKey(t, ts, map[string]any{
		"userId": "u-01",
		"label":  "metrics reader",
		"scopes": []string{"metrics:read"},
	})
	resp, _ = scopedCall(t, ts, metricsKey.Secret, http.MethodGet, "/v1/metrics", nil)
	requireStatus(t, resp, http.StatusOK)
	resp, _ = scopedCall(t, ts, metricsKey.Secret, http.MethodGet, "/v1/accounts", nil)
	requireStatus(t, resp, http.StatusForbidden)
}

func TestScopedKeyAdminScopeCanManageKeys(t *testing.T) {
	ts, _ := newTestServer(t)
	adminKey, _ := mustCreateScopedKey(t, ts, map[string]any{
		"userId": "u-01",
		"label":  "operator",
		"scopes": []string{"admin"},
	})
	resp, body := scopedCall(t, ts, adminKey.Secret, http.MethodGet, "/v1/users", nil)
	requireStatus(t, resp, http.StatusOK)
	resp, body = scopedCall(t, ts, adminKey.Secret, http.MethodPost, "/v1/scoped-keys", map[string]any{
		"userId": "u-02",
		"label":  "issued by admin key",
		"scopes": []string{"accounts:read"},
	})
	requireStatus(t, resp, http.StatusCreated)
	if !strings.HasPrefix(decodeBody[model.ScopedKeyCreated](t, body).Secret, "ajs_sk_") {
		t.Fatal("admin-scoped key could not issue another key")
	}
}

func TestPlainAPIKeyIsNotABearerCredential(t *testing.T) {
	ts, _ := newTestServer(t)
	resp, body := apiCall(t, ts, http.MethodPost, "/v1/apikeys", map[string]any{
		"userId": "u-01", "label": "plain key",
	})
	requireStatus(t, resp, http.StatusCreated)
	plain := decodeBody[model.CreateKeyResult](t, body)
	if !strings.HasPrefix(plain.Secret, "ajs_live_") {
		t.Fatalf("plain secret = %q", plain.Secret)
	}
	resp, _ = scopedCall(t, ts, plain.Secret, http.MethodGet, "/v1/accounts", nil)
	requireStatus(t, resp, http.StatusUnauthorized)
}

func TestScopedKeyExpiryAnswers401(t *testing.T) {
	ts, _ := newTestServer(t)
	expires := time.Now().Add(2 * time.Second).UTC().Format(time.RFC3339)
	created, _ := mustCreateScopedKey(t, ts, map[string]any{
		"userId":    "u-01",
		"label":     "short lived",
		"scopes":    []string{"accounts:read"},
		"expiresAt": expires,
	})
	resp, _ := scopedCall(t, ts, created.Secret, http.MethodGet, "/v1/accounts", nil)
	requireStatus(t, resp, http.StatusOK)
	time.Sleep(2100 * time.Millisecond)
	resp, body := scopedCall(t, ts, created.Secret, http.MethodGet, "/v1/accounts", nil)
	requireStatus(t, resp, http.StatusUnauthorized)
	requireErrorCode(t, body, CodeUnauthorized)
}

func TestScopedKeyAccountRestrictions(t *testing.T) {
	ts, _ := newTestServer(t)
	created, _ := mustCreateScopedKey(t, ts, map[string]any{
		"userId":     "u-01",
		"label":      "one account",
		"scopes":     []string{"accounts:read", "leases:read", "catalog:read"},
		"accountIds": []string{"acc-01"},
	})
	resp, body := scopedCall(t, ts, created.Secret, http.MethodGet, "/v1/accounts", nil)
	requireStatus(t, resp, http.StatusOK)
	accounts := decodeBody[[]model.Account](t, body)
	if len(accounts) != 1 || accounts[0].ID != "acc-01" {
		t.Fatalf("restricted account list = %+v, want only acc-01", accounts)
	}
	resp, _ = scopedCall(t, ts, created.Secret, http.MethodGet, "/v1/accounts/acc-01", nil)
	requireStatus(t, resp, http.StatusOK)
	resp, body = scopedCall(t, ts, created.Secret, http.MethodGet, "/v1/accounts/acc-02", nil)
	requireStatus(t, resp, http.StatusForbidden)
	requireErrorCode(t, body, CodeForbidden)

	resp, body = scopedCall(t, ts, created.Secret, http.MethodGet, "/v1/leases", nil)
	requireStatus(t, resp, http.StatusOK)
	for _, lease := range decodeBody[[]model.Lease](t, body) {
		if lease.AccountID != "acc-01" {
			t.Fatalf("restricted lease list leaked account %q", lease.AccountID)
		}
	}
	resp, body = scopedCall(t, ts, created.Secret, http.MethodGet, "/v1/nodes", nil)
	requireStatus(t, resp, http.StatusOK)
	for _, node := range decodeBody[[]model.ProxyNode](t, body) {
		if node.AccountID != "acc-01" {
			t.Fatalf("restricted node list leaked account %q", node.AccountID)
		}
	}
	resp, _ = scopedCall(t, ts, created.Secret, http.MethodGet, "/v1/nodes?account_id=acc-02", nil)
	requireStatus(t, resp, http.StatusForbidden)

	regionKey, _ := mustCreateScopedKey(t, ts, map[string]any{
		"userId":  "u-01",
		"label":   "one region",
		"scopes":  []string{"accounts:read"},
		"regions": []string{"de-fra"},
	})
	resp, body = scopedCall(t, ts, regionKey.Secret, http.MethodGet, "/v1/accounts", nil)
	requireStatus(t, resp, http.StatusOK)
	regional := decodeBody[[]model.Account](t, body)
	if len(regional) != 1 || regional[0].ID != "acc-04" {
		t.Fatalf("region-restricted list = %+v, want only acc-04", regional)
	}
	resp, _ = scopedCall(t, ts, regionKey.Secret, http.MethodGet, "/v1/accounts/acc-01", nil)
	requireStatus(t, resp, http.StatusForbidden)
}

func TestScopedKeyRecordsLastUsedAt(t *testing.T) {
	ts, _ := newTestServer(t)
	created, _ := mustCreateScopedKey(t, ts, map[string]any{
		"userId": "u-01",
		"label":  "used key",
		"scopes": []string{"accounts:read"},
	})
	// Start with enough room before the minute rolls so both requests land in
	// the same throttle window. The store-level test proves the minute reset.
	for time.Now().Second() > 56 {
		time.Sleep(50 * time.Millisecond)
	}
	resp, _ := scopedCall(t, ts, created.Secret, http.MethodGet, "/v1/accounts", nil)
	requireStatus(t, resp, http.StatusOK)
	resp, body := apiCall(t, ts, http.MethodGet, "/v1/scoped-keys/"+created.Key.ID, nil)
	requireStatus(t, resp, http.StatusOK)
	first := decodeBody[model.ScopedKey](t, body)
	if first.LastUsedAt == nil {
		t.Fatal("lastUsedAt is nil after an authenticated request")
	}

	resp, _ = scopedCall(t, ts, created.Secret, http.MethodGet, "/v1/accounts", nil)
	requireStatus(t, resp, http.StatusOK)
	resp, body = apiCall(t, ts, http.MethodGet, "/v1/scoped-keys/"+created.Key.ID, nil)
	requireStatus(t, resp, http.StatusOK)
	second := decodeBody[model.ScopedKey](t, body)
	if second.LastUsedAt == nil || *second.LastUsedAt != *first.LastUsedAt {
		t.Fatalf("lastUsedAt changed inside one minute: %v -> %v", first.LastUsedAt, second.LastUsedAt)
	}
}

func TestScopedKeyCreateValidation(t *testing.T) {
	ts, _ := newTestServer(t)
	cases := []struct {
		name string
		body map[string]any
	}{
		{"missing label", map[string]any{"userId": "u-01", "scopes": []string{"accounts:read"}}},
		{"unknown scope", map[string]any{"userId": "u-01", "label": "x", "scopes": []string{"root"}}},
		{"unknown user", map[string]any{"userId": "u-99", "label": "x", "scopes": []string{"accounts:read"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := apiCall(t, ts, http.MethodPost, "/v1/scoped-keys", tc.body)
			want := http.StatusBadRequest
			if tc.name == "unknown user" {
				want = http.StatusNotFound
			}
			requireStatus(t, resp, want)
			if resp.StatusCode == http.StatusBadRequest {
				requireErrorCode(t, body, CodeInvalidArgument)
			}
		})
	}
}

func TestScopedKeyGetUnknownAnswers404(t *testing.T) {
	ts, _ := newTestServer(t)
	resp, body := apiCall(t, ts, http.MethodGet, "/v1/scoped-keys/sk-99", nil)
	requireStatus(t, resp, http.StatusNotFound)
	requireErrorCode(t, body, CodeNotFound)
	resp, body = apiCall(t, ts, http.MethodDelete, "/v1/scoped-keys/sk-99", nil)
	requireStatus(t, resp, http.StatusNotFound)
	requireErrorCode(t, body, CodeNotFound)
}
