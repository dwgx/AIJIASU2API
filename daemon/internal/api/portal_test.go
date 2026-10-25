package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/store"
	"ajsaccountd/internal/usage"
)

const (
	portalTestOrigin   = "https://portal.example.test"
	portalTestPassword = "correct horse battery staple"
)

func newPortalTestServer(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	return newPortalTestServerWithConfig(t, Config{})
}

func newPortalTestServerWithConfig(t *testing.T, cfg Config) (*httptest.Server, *store.Store) {
	t.Helper()
	st := store.New()
	cfg.AdminToken = testToken
	cfg.TenantPortal = true
	cfg.TenantOrigin = portalTestOrigin
	if cfg.Usage == nil {
		cfg.Usage = usageAggregator()
	}
	server := New(cfg, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)
	return ts, st
}

// portalCall sends one portal request. Unsafe methods carry the configured
// Origin; cookie/csrf are attached when supplied.
func portalCall(t *testing.T, ts *httptest.Server, method, path string, body any, cookie, csrf string) (*http.Response, []byte) {
	t.Helper()
	req := newRequest(t, method, ts.URL+path, body)
	if isUnsafeMethod(method) {
		req.Header.Set("Origin", portalTestOrigin)
	}
	if cookie != "" {
		req.Header.Set("Cookie", portalCookieName+"="+cookie)
	}
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	return doRequest(t, ts, req)
}

func portalSignup(t *testing.T, ts *httptest.Server, email string) []byte {
	t.Helper()
	resp, body := portalCall(t, ts, http.MethodPost, "/v1/portal/signup", model.TenantSignupInput{
		Email: email, Password: portalTestPassword, DisplayName: "Portal User",
	}, "", "")
	requireStatus(t, resp, http.StatusAccepted)
	return body
}

func tenantByEmail(t *testing.T, st *store.Store, email string) model.Tenant {
	t.Helper()
	for _, tenant := range st.Tenants() {
		if tenant.Email == email {
			return tenant
		}
	}
	t.Fatalf("tenant %q not found", email)
	return model.Tenant{}
}

func approveTenant(t *testing.T, ts *httptest.Server, tenantID string, body any) model.Tenant {
	t.Helper()
	resp, data := apiCall(t, ts, http.MethodPost, "/v1/admin/tenants/"+tenantID+"/approve", body)
	requireStatus(t, resp, http.StatusOK)
	return decodeBody[model.Tenant](t, data)
}

func portalLogin(t *testing.T, ts *httptest.Server, email, password string) (string, string, model.TenantSessionView) {
	t.Helper()
	resp, body := portalCall(t, ts, http.MethodPost, "/v1/portal/login", model.TenantLoginInput{
		Email: email, Password: password,
	}, "", "")
	requireStatus(t, resp, http.StatusOK)
	view := decodeBody[model.TenantSessionView](t, body)
	cookie := ""
	for _, candidate := range resp.Cookies() {
		if candidate.Name == portalCookieName {
			cookie = candidate.Value
		}
	}
	if cookie == "" {
		t.Fatalf("login did not set %s", portalCookieName)
	}
	if view.CSRFToken == "" {
		t.Fatalf("login did not return a csrf token")
	}
	return cookie, view.CSRFToken, view
}

// activePortalTenant signs up a tenant, binds it to an existing seeded user and
// logs in, returning everything a follow-up request needs.
func activePortalTenant(t *testing.T, ts *httptest.Server, st *store.Store, email, userID string) (model.Tenant, string, string) {
	t.Helper()
	portalSignup(t, ts, email)
	tenant := tenantByEmail(t, st, email)
	approveTenant(t, ts, tenant.ID, map[string]string{"userId": userID})
	cookie, csrf, _ := portalLogin(t, ts, email, portalTestPassword)
	return tenant, cookie, csrf
}

func TestPortalSignupSameResponseAndNoSideEffects(t *testing.T) {
	ts, st := newPortalTestServer(t)
	usersBefore := len(st.Users())
	keysBefore := len(st.Keys())

	first := portalSignup(t, ts, "signup@example.com")
	second := portalSignup(t, ts, "signup@example.com")
	if !bytes.Equal(first, second) {
		t.Fatalf("new and duplicate signup bodies differ: %s vs %s", first, second)
	}
	payload := decodeBody[map[string]string](t, first)
	if payload["status"] != "pending" || len(payload) != 1 {
		t.Fatalf("signup body = %v, want only status=pending", payload)
	}
	if got := len(st.Tenants()); got != 1 {
		t.Fatalf("tenants = %d, want 1", got)
	}
	if got := len(st.Users()); got != usersBefore {
		t.Fatalf("signup created a user: %d -> %d", usersBefore, got)
	}
	if got := len(st.Keys()); got != keysBefore {
		t.Fatalf("signup created a key: %d -> %d", keysBefore, got)
	}
	tenant := tenantByEmail(t, st, "signup@example.com")
	if tenant.Status != model.TenantPending || tenant.UserID != nil {
		t.Fatalf("signup tenant = %+v, want pending with no link", tenant)
	}
}

func TestPortalLoginRefusesPendingAndSuspended(t *testing.T) {
	ts, st := newPortalTestServer(t)
	portalSignup(t, ts, "pending@example.com")
	tenant := tenantByEmail(t, st, "pending@example.com")

	resp, body := portalCall(t, ts, http.MethodPost, "/v1/portal/login", model.TenantLoginInput{
		Email: "pending@example.com", Password: portalTestPassword,
	}, "", "")
	requireStatus(t, resp, http.StatusForbidden)
	requireErrorCode(t, body, CodeForbidden)
	if len(resp.Cookies()) != 0 {
		t.Fatalf("pending login set a cookie")
	}

	resp, body = portalCall(t, ts, http.MethodPost, "/v1/portal/login", model.TenantLoginInput{
		Email: "pending@example.com", Password: "wrong password value",
	}, "", "")
	requireStatus(t, resp, http.StatusUnauthorized)
	requireErrorCode(t, body, CodeUnauthorized)
	if len(resp.Cookies()) != 0 {
		t.Fatalf("bad-password login set a cookie")
	}

	approveTenant(t, ts, tenant.ID, map[string]string{"userId": "u-01"})
	resp, _ = apiCall(t, ts, http.MethodPost, "/v1/admin/tenants/"+tenant.ID+"/suspend", nil)
	requireStatus(t, resp, http.StatusOK)

	resp, body = portalCall(t, ts, http.MethodPost, "/v1/portal/login", model.TenantLoginInput{
		Email: "pending@example.com", Password: portalTestPassword,
	}, "", "")
	requireStatus(t, resp, http.StatusForbidden)
	requireErrorCode(t, body, CodeForbidden)
	if len(resp.Cookies()) != 0 {
		t.Fatalf("suspended login set a cookie")
	}
}

func TestPortalActiveLoginCookieAndSession(t *testing.T) {
	ts, st := newPortalTestServer(t)
	portalSignup(t, ts, "active@example.com")
	tenant := tenantByEmail(t, st, "active@example.com")
	approveTenant(t, ts, tenant.ID, map[string]string{"userId": "u-01"})

	resp, body := portalCall(t, ts, http.MethodPost, "/v1/portal/login", model.TenantLoginInput{
		Email: "active@example.com", Password: portalTestPassword,
	}, "", "")
	requireStatus(t, resp, http.StatusOK)
	view := decodeBody[model.TenantSessionView](t, body)
	if view.Tenant.Status != model.TenantActive || view.Tenant.UserID == nil || *view.Tenant.UserID != "u-01" {
		t.Fatalf("login tenant = %+v", view.Tenant)
	}
	if view.User == nil || view.User.ID != "u-01" {
		t.Fatalf("login user = %+v, want u-01", view.User)
	}
	if view.CSRFToken == "" || len(view.CSRFToken) != 64 {
		t.Fatalf("csrf token = %q, want 32-byte hex", view.CSRFToken)
	}
	if bytes.Contains(body, []byte("passwordHash")) || bytes.Contains(body, []byte("csrfHash")) ||
		bytes.Contains(body, []byte("secret")) {
		t.Fatalf("login body leaks hash/secret material: %s", body)
	}

	rawCookie := resp.Header.Get("Set-Cookie")
	for _, want := range []string{
		portalCookieName + "=",
		"Path=/",
		"Secure",
		"HttpOnly",
		"SameSite=Strict",
		"Max-Age=28800",
	} {
		if !strings.Contains(rawCookie, want) {
			t.Fatalf("Set-Cookie %q is missing %q", rawCookie, want)
		}
	}
	cookie := ""
	for _, candidate := range resp.Cookies() {
		if candidate.Name == portalCookieName {
			cookie = candidate.Value
		}
	}

	resp, body = portalCall(t, ts, http.MethodGet, "/v1/portal/session", nil, cookie, "")
	requireStatus(t, resp, http.StatusOK)
	sessionView := decodeBody[model.TenantSessionView](t, body)
	if sessionView.CSRFToken != view.CSRFToken {
		t.Fatalf("session csrf = %q, want the login token", sessionView.CSRFToken)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if got := resp.Header.Get("Vary"); !strings.Contains(got, "Cookie") {
		t.Fatalf("Vary = %q, want Cookie", got)
	}
}

func TestPortalLogoutRevokesAcrossRestart(t *testing.T) {
	ts, st := newPortalTestServer(t)
	_, cookie, csrf := activePortalTenant(t, ts, st, "logout@example.com", "u-01")

	// A live session that will be carried across the restart.
	liveCookie, _, _ := portalLogin(t, ts, "logout@example.com", portalTestPassword)

	resp, _ := portalCall(t, ts, http.MethodPost, "/v1/portal/logout", nil, cookie, csrf)
	requireStatus(t, resp, http.StatusNoContent)
	resp, body := portalCall(t, ts, http.MethodGet, "/v1/portal/session", nil, cookie, "")
	requireStatus(t, resp, http.StatusUnauthorized)
	requireErrorCode(t, body, CodeUnauthorized)

	resp, _ = portalCall(t, ts, http.MethodGet, "/v1/portal/session", nil, "not-a-real-cookie", "")
	requireStatus(t, resp, http.StatusUnauthorized)

	data, err := st.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	restored := store.NewEmpty()
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	restartServer := New(Config{
		AdminToken: testToken, TenantPortal: true, TenantOrigin: portalTestOrigin,
		Usage: usageAggregator(),
	}, restored, slog.New(slog.NewTextHandler(io.Discard, nil)))
	restarted := httptest.NewServer(restartServer)
	t.Cleanup(restarted.Close)

	resp, _ = portalCall(t, restarted, http.MethodGet, "/v1/portal/session", nil, cookie, "")
	requireStatus(t, resp, http.StatusUnauthorized)
	resp, _ = portalCall(t, restarted, http.MethodGet, "/v1/portal/session", nil, liveCookie, "")
	requireStatus(t, resp, http.StatusOK)
}

func TestPortalCSRFAndOriginEnforcement(t *testing.T) {
	ts, st := newPortalTestServer(t)
	_, cookie, _ := activePortalTenant(t, ts, st, "csrf@example.com", "u-01")
	keysBefore := len(st.Keys())

	resp, body := portalCall(t, ts, http.MethodPost, "/v1/portal/logout", nil, cookie, "")
	requireStatus(t, resp, http.StatusForbidden)
	requireErrorCode(t, body, CodeForbidden)
	resp, _ = portalCall(t, ts, http.MethodGet, "/v1/portal/session", nil, cookie, "")
	requireStatus(t, resp, http.StatusOK)

	resp, _ = portalCall(t, ts, http.MethodPost, "/v1/portal/logout", nil, cookie, "wrong-token")
	requireStatus(t, resp, http.StatusForbidden)

	resp, _ = portalCall(t, ts, http.MethodPost, "/v1/portal/keys", model.TenantKeyCreateRequest{Label: "x"}, cookie, "")
	requireStatus(t, resp, http.StatusForbidden)
	if got := len(st.Keys()); got != keysBefore {
		t.Fatalf("refused CSRF request created a key: %d -> %d", keysBefore, got)
	}

	// Wrong and missing Origin are both refused for signup, and nothing is
	// stored.
	tenantsBefore := len(st.Tenants())
	req := newRequest(t, http.MethodPost, ts.URL+"/v1/portal/signup", model.TenantSignupInput{
		Email: "origin@example.com", Password: portalTestPassword, DisplayName: "Origin",
	})
	req.Header.Set("Origin", "https://evil.example.test")
	resp, _ = doRequest(t, ts, req)
	requireStatus(t, resp, http.StatusForbidden)
	resp, _ = doRequest(t, ts, newRequest(t, http.MethodPost, ts.URL+"/v1/portal/signup", model.TenantSignupInput{
		Email: "origin@example.com", Password: portalTestPassword, DisplayName: "Origin",
	}))
	requireStatus(t, resp, http.StatusForbidden)
	if got := len(st.Tenants()); got != tenantsBefore {
		t.Fatalf("refused origin created a tenant: %d -> %d", tenantsBefore, got)
	}

	req = newRequest(t, http.MethodPost, ts.URL+"/v1/portal/login", model.TenantLoginInput{
		Email: "csrf@example.com", Password: portalTestPassword,
	})
	req.Header.Set("Origin", portalTestOrigin)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	resp, body = doRequest(t, ts, req)
	requireStatus(t, resp, http.StatusForbidden)
	requireErrorCode(t, body, CodeForbidden)

	// A safe read is not origin-checked.
	req = newRequest(t, http.MethodGet, ts.URL+"/v1/portal/session", nil)
	req.Header.Set("Cookie", portalCookieName+"="+cookie)
	req.Header.Set("Origin", "https://evil.example.test")
	resp, _ = doRequest(t, ts, req)
	requireStatus(t, resp, http.StatusOK)
}

func TestPortalOwnDataOnly(t *testing.T) {
	ts, st := newPortalTestServer(t)
	_, cookie, csrf := activePortalTenant(t, ts, st, "own@example.com", "u-01")

	resp, body := portalCall(t, ts, http.MethodGet, "/v1/portal/leases", nil, cookie, "")
	requireStatus(t, resp, http.StatusOK)
	leases := decodeBody[[]model.Lease](t, body)
	if len(leases) == 0 {
		t.Fatalf("portal listed no leases for u-01")
	}
	for _, lease := range leases {
		if lease.UserID != "u-01" {
			t.Fatalf("portal leaked lease %s owned by %s", lease.ID, lease.UserID)
		}
	}

	resp, body = portalCall(t, ts, http.MethodGet, "/v1/portal/keys", nil, cookie, "")
	requireStatus(t, resp, http.StatusOK)
	keys := decodeBody[[]model.APIKey](t, body)
	if len(keys) != 2 {
		t.Fatalf("portal keys = %d, want the two u-01 keys", len(keys))
	}
	for _, key := range keys {
		if key.UserID != "u-01" {
			t.Fatalf("portal leaked key %s owned by %s", key.ID, key.UserID)
		}
	}

	// A foreign key id is indistinguishable from an unknown one.
	resp, body = portalCall(t, ts, http.MethodDelete, "/v1/portal/keys/key-03", nil, cookie, csrf)
	requireStatus(t, resp, http.StatusNotFound)
	requireErrorCode(t, body, CodeNotFound)
	if got, _ := st.Key("key-03"); got.RevokedAt != nil {
		t.Fatalf("foreign revoke changed key-03")
	}
	resp, _ = portalCall(t, ts, http.MethodDelete, "/v1/portal/keys/key-01", nil, cookie, csrf)
	requireStatus(t, resp, http.StatusNoContent)

	// A caller-supplied owner filter is ignored: only the session's user is
	// aggregated.
	resp, body = portalCall(t, ts, http.MethodGet, "/v1/portal/usage?userId=u-02&user_id=u-02&account=Field+Lab", nil, cookie, "")
	requireStatus(t, resp, http.StatusOK)
	summary := decodeBody[usage.Summary](t, body)
	if len(summary.Users) != 1 || summary.Users[0].UserID != "u-01" {
		t.Fatalf("portal usage users = %+v, want only u-01", summary.Users)
	}
	for _, account := range summary.Accounts {
		if account.TrafficBudgetBytes != nil || account.RemainingBytes != nil {
			t.Fatalf("portal usage leaked account budget for %s", account.Account)
		}
	}
}

func TestPortalUsageIgnoresAmbiguousRows(t *testing.T) {
	ts, st := newPortalTestServer(t)
	_, cookie, _ := activePortalTenant(t, ts, st, "usage@example.com", "u-01")

	resp, body := portalCall(t, ts, http.MethodGet, "/v1/portal/usage", nil, cookie, "")
	requireStatus(t, resp, http.StatusOK)
	before := decodeBody[usage.Summary](t, body)

	now := time.Now().UTC().Format(time.RFC3339Nano)
	st.AppendRequest(model.RequestLog{
		TS: now, UserID: "", UserName: "ada.ops", AccountLabel: "Northwind Ops",
		Method: "GET", Target: "api.example.com:443", Status: 200, Bytes: 999_999,
		Source: model.SourceGate,
	})
	st.AppendRequest(model.RequestLog{
		TS: now, UserID: "u-02", UserName: "ada.ops", AccountLabel: "Northwind Ops",
		Method: "GET", Target: "api.example.com:443", Status: 200, Bytes: 888_888,
		Source: model.SourceGate,
	})

	resp, body = portalCall(t, ts, http.MethodGet, "/v1/portal/usage", nil, cookie, "")
	requireStatus(t, resp, http.StatusOK)
	after := decodeBody[usage.Summary](t, body)
	if after.Bytes != before.Bytes || after.Requests != before.Requests {
		t.Fatalf("portal usage counted unowned rows: before=%d/%d after=%d/%d",
			before.Bytes, before.Requests, after.Bytes, after.Requests)
	}
	if len(after.Users) != 1 || after.Users[0].UserID != "u-01" {
		t.Fatalf("portal usage users = %+v, want only u-01", after.Users)
	}
}

func TestPortalKeyIssueListRevoke(t *testing.T) {
	ts, st := newPortalTestServer(t)
	_, cookie, csrf := activePortalTenant(t, ts, st, "keys@example.com", "u-01")

	resp, body := portalCall(t, ts, http.MethodPost, "/v1/portal/keys",
		model.TenantKeyCreateRequest{Label: "portal key"}, cookie, csrf)
	requireStatus(t, resp, http.StatusCreated)
	created := decodeBody[model.CreateKeyResult](t, body)
	if created.Key.UserID != "u-01" || !strings.HasPrefix(created.Secret, "ajs_live_") {
		t.Fatalf("created key = %+v", created)
	}
	if _, ok := st.SecretHash(created.Key.ID); !ok {
		t.Fatalf("key hash was not stored")
	}
	if bytes.Contains(body, []byte("keyHashes")) || bytes.Contains(body, []byte("secretHash")) {
		t.Fatalf("create response leaks hash state: %s", body)
	}

	resp, body = portalCall(t, ts, http.MethodGet, "/v1/portal/keys", nil, cookie, "")
	requireStatus(t, resp, http.StatusOK)
	keys := decodeBody[[]model.APIKey](t, body)
	found := false
	for _, key := range keys {
		if key.ID == created.Key.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("issued key missing from the list")
	}

	resp, _ = portalCall(t, ts, http.MethodDelete, "/v1/portal/keys/"+created.Key.ID, nil, cookie, csrf)
	requireStatus(t, resp, http.StatusNoContent)
	resp, _ = portalCall(t, ts, http.MethodDelete, "/v1/portal/keys/"+created.Key.ID, nil, cookie, csrf)
	requireStatus(t, resp, http.StatusNoContent)
	if got, _ := st.Key(created.Key.ID); got.RevokedAt == nil {
		t.Fatalf("revoke did not mark the key")
	}
}

func TestTenantApprovalIsAtomicAndExclusive(t *testing.T) {
	ts, st := newPortalTestServer(t)
	portalSignup(t, ts, "bind@example.com")
	tenant := tenantByEmail(t, st, "bind@example.com")

	resp, body := apiCall(t, ts, http.MethodPost, "/v1/admin/tenants/"+tenant.ID+"/approve", map[string]string{})
	requireStatus(t, resp, http.StatusBadRequest)
	requireErrorCode(t, body, CodeInvalidArgument)
	resp, body = apiCall(t, ts, http.MethodPost, "/v1/admin/tenants/"+tenant.ID+"/approve", map[string]any{
		"userId": "u-01", "user": map[string]string{"name": "both"},
	})
	requireStatus(t, resp, http.StatusBadRequest)
	requireErrorCode(t, body, CodeInvalidArgument)
	if got, _ := st.Tenant(tenant.ID); got.Status != model.TenantPending {
		t.Fatalf("invalid approval mutated the tenant: %+v", got)
	}

	resp, body = apiCall(t, ts, http.MethodPost, "/v1/admin/tenants/"+tenant.ID+"/approve", map[string]string{
		"userId": "u-does-not-exist",
	})
	requireStatus(t, resp, http.StatusNotFound)
	requireErrorCode(t, body, CodeNotFound)

	active := approveTenant(t, ts, tenant.ID, map[string]string{"userId": "u-01"})
	if active.Status != model.TenantActive || active.UserID == nil || *active.UserID != "u-01" {
		t.Fatalf("approval = %+v", active)
	}

	portalSignup(t, ts, "second-bind@example.com")
	second := tenantByEmail(t, st, "second-bind@example.com")
	resp, body = apiCall(t, ts, http.MethodPost, "/v1/admin/tenants/"+second.ID+"/approve", map[string]string{
		"userId": "u-01",
	})
	requireStatus(t, resp, http.StatusConflict)
	requireErrorCode(t, body, CodeConflict)
	if got, _ := st.Tenant(second.ID); got.Status != model.TenantPending || got.UserID != nil {
		t.Fatalf("duplicate bind mutated the tenant: %+v", got)
	}

	portalSignup(t, ts, "create@example.com")
	third := tenantByEmail(t, st, "create@example.com")
	created := approveTenant(t, ts, third.ID, map[string]any{
		"user": map[string]string{"name": "portal-created-user"},
	})
	if created.UserID == nil {
		t.Fatalf("create approval returned no user id")
	}
	if _, ok := st.User(*created.UserID); !ok {
		t.Fatalf("created user %q missing", *created.UserID)
	}
}

func TestPortalSuspendResume(t *testing.T) {
	ts, st := newPortalTestServer(t)
	tenant, cookie, csrf := activePortalTenant(t, ts, st, "suspend@example.com", "u-01")

	resp, body := portalCall(t, ts, http.MethodPost, "/v1/portal/keys",
		model.TenantKeyCreateRequest{Label: "before suspend"}, cookie, csrf)
	requireStatus(t, resp, http.StatusCreated)
	key := decodeBody[model.CreateKeyResult](t, body)

	resp, body = apiCall(t, ts, http.MethodPost, "/v1/admin/tenants/"+tenant.ID+"/suspend", nil)
	requireStatus(t, resp, http.StatusOK)
	if got := decodeBody[model.Tenant](t, body); got.Status != model.TenantSuspended {
		t.Fatalf("suspend status = %q", got.Status)
	}
	if got, _ := st.User("u-01"); got.Enabled {
		t.Fatalf("suspend left u-01 enabled")
	}
	if got, _ := st.Key(key.Key.ID); got.RevokedAt == nil {
		t.Fatalf("suspend left the tenant key active")
	}

	for _, path := range []string{"/v1/portal/session", "/v1/portal/leases", "/v1/portal/keys"} {
		resp, _ = portalCall(t, ts, http.MethodGet, path, nil, cookie, "")
		requireStatus(t, resp, http.StatusUnauthorized)
	}
	resp, _ = portalCall(t, ts, http.MethodPost, "/v1/portal/keys",
		model.TenantKeyCreateRequest{Label: "after suspend"}, cookie, csrf)
	requireStatus(t, resp, http.StatusUnauthorized)

	resp, body = portalCall(t, ts, http.MethodPost, "/v1/portal/login", model.TenantLoginInput{
		Email: "suspend@example.com", Password: portalTestPassword,
	}, "", "")
	requireStatus(t, resp, http.StatusForbidden)

	resp, body = apiCall(t, ts, http.MethodPost, "/v1/admin/tenants/"+tenant.ID+"/resume", nil)
	requireStatus(t, resp, http.StatusOK)
	if got := decodeBody[model.Tenant](t, body); got.Status != model.TenantActive {
		t.Fatalf("resume status = %q", got.Status)
	}
	if got, _ := st.User("u-01"); !got.Enabled {
		t.Fatalf("resume left u-01 disabled")
	}
	if got, _ := st.Key(key.Key.ID); got.RevokedAt == nil {
		t.Fatalf("resume un-revoked the key")
	}
	resp, _ = portalCall(t, ts, http.MethodGet, "/v1/portal/session", nil, cookie, "")
	requireStatus(t, resp, http.StatusUnauthorized)
	_, _, view := portalLogin(t, ts, "suspend@example.com", portalTestPassword)
	if view.Tenant.Status != model.TenantActive {
		t.Fatalf("login after resume = %+v", view.Tenant)
	}
}

func TestPortalIdentityBoundaries(t *testing.T) {
	ts, st := newPortalTestServer(t)
	_, cookie, _ := activePortalTenant(t, ts, st, "bounds@example.com", "u-01")

	// A portal cookie is never a dashboard credential.
	req := newRequest(t, http.MethodGet, ts.URL+"/v1/users", nil)
	req.Header.Set("Cookie", portalCookieName+"="+cookie)
	resp, body := doRequest(t, ts, req)
	requireStatus(t, resp, http.StatusUnauthorized)
	requireErrorCode(t, body, CodeUnauthorized)

	// An admin token is never a tenant identity.
	req = newRequest(t, http.MethodGet, ts.URL+"/v1/portal/session", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, body = doRequest(t, ts, req)
	requireStatus(t, resp, http.StatusUnauthorized)
	requireErrorCode(t, body, CodeUnauthorized)

	// A portal session is refused explicitly (403) on the tenant-admin chain,
	// not treated as an anonymous bearer client.
	req = newRequest(t, http.MethodGet, ts.URL+"/v1/admin/tenants", nil)
	req.Header.Set("Cookie", portalCookieName+"="+cookie)
	resp, body = doRequest(t, ts, req)
	requireStatus(t, resp, http.StatusForbidden)
	requireErrorCode(t, body, CodeForbidden)

	// A scoped key without the admin scope cannot administer tenants; one with
	// the admin scope can.
	resp, body = apiCall(t, ts, http.MethodPost, "/v1/scoped-keys", model.ScopedKeyCreateRequest{
		UserID: "u-01", Label: "leases reader", Scopes: []model.Scope{model.ScopeLeasesRead},
	})
	requireStatus(t, resp, http.StatusCreated)
	reader := decodeBody[model.ScopedKeyCreated](t, body)

	req = newRequest(t, http.MethodGet, ts.URL+"/v1/admin/tenants", nil)
	req.Header.Set("Authorization", "Bearer "+reader.Secret)
	resp, body = doRequest(t, ts, req)
	requireStatus(t, resp, http.StatusForbidden)
	requireErrorCode(t, body, CodeForbidden)

	resp, body = apiCall(t, ts, http.MethodPost, "/v1/scoped-keys", model.ScopedKeyCreateRequest{
		UserID: "u-01", Label: "tenant admin", Scopes: []model.Scope{model.ScopeAdmin},
	})
	requireStatus(t, resp, http.StatusCreated)
	adminKey := decodeBody[model.ScopedKeyCreated](t, body)
	req = newRequest(t, http.MethodGet, ts.URL+"/v1/admin/tenants", nil)
	req.Header.Set("Authorization", "Bearer "+adminKey.Secret)
	resp, body = doRequest(t, ts, req)
	requireStatus(t, resp, http.StatusOK)

	// A scoped key is not a portal session either.
	req = newRequest(t, http.MethodGet, ts.URL+"/v1/portal/session", nil)
	req.Header.Set("Authorization", "Bearer "+reader.Secret)
	resp, _ = doRequest(t, ts, req)
	requireStatus(t, resp, http.StatusUnauthorized)
}

func TestPortalDisabledByDefault(t *testing.T) {
	st := store.New()
	server := New(Config{AdminToken: testToken}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)

	resp, _ := portalCall(t, ts, http.MethodPost, "/v1/portal/signup", model.TenantSignupInput{
		Email: "off@example.com", Password: portalTestPassword, DisplayName: "Off",
	}, "", "")
	requireStatus(t, resp, http.StatusNotFound)
	resp, _ = portalCall(t, ts, http.MethodGet, "/v1/portal/session", nil, "", "")
	requireStatus(t, resp, http.StatusNotFound)
	resp, _ = apiCall(t, ts, http.MethodGet, "/v1/admin/tenants", nil)
	requireStatus(t, resp, http.StatusNotFound)
}

func TestPortalDoesNotBypassInsecureMode(t *testing.T) {
	st := store.New()
	server := New(Config{
		AdminToken: testToken, AllowInsecure: true, TenantPortal: true, TenantOrigin: portalTestOrigin,
	}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)

	// Legacy dashboard routes keep the documented insecure behaviour.
	resp, _ := doRequest(t, ts, newRequest(t, http.MethodGet, ts.URL+"/v1/users", nil))
	requireStatus(t, resp, http.StatusOK)

	// The portal and tenant admin chains still require their own identity.
	resp, body := portalCall(t, ts, http.MethodGet, "/v1/portal/session", nil, "", "")
	requireStatus(t, resp, http.StatusUnauthorized)
	requireErrorCode(t, body, CodeUnauthorized)
	resp, _ = doRequest(t, ts, newRequest(t, http.MethodGet, ts.URL+"/v1/admin/tenants", nil))
	requireStatus(t, resp, http.StatusUnauthorized)

	resp, _ = apiCall(t, ts, http.MethodGet, "/v1/admin/tenants", nil)
	requireStatus(t, resp, http.StatusOK)
}

func TestPortalRateLimitsAreIsolated(t *testing.T) {
	ts, st := newPortalTestServer(t)

	for i := range portalSignupIPLimit {
		portalSignup(t, ts, "limit"+string(rune('a'+i))+"@example.com")
	}
	resp, body := portalCall(t, ts, http.MethodPost, "/v1/portal/signup", model.TenantSignupInput{
		Email: "limit-over@example.com", Password: portalTestPassword, DisplayName: "Over",
	}, "", "")
	requireStatus(t, resp, http.StatusTooManyRequests)
	requireErrorCode(t, body, CodeRateLimited)
	if resp.Header.Get("Retry-After") == "" {
		t.Fatalf("429 did not carry Retry-After")
	}

	// The login buckets are separate from the signup bucket.
	for i := range portalLoginIPLimit {
		resp, _ = portalCall(t, ts, http.MethodPost, "/v1/portal/login", model.TenantLoginInput{
			Email: "missing" + string(rune('a'+i)) + "@example.com", Password: "wrong password value",
		}, "", "")
		requireStatus(t, resp, http.StatusUnauthorized)
	}
	resp, body = portalCall(t, ts, http.MethodPost, "/v1/portal/login", model.TenantLoginInput{
		Email: "missing-over@example.com", Password: "wrong password value",
	}, "", "")
	requireStatus(t, resp, http.StatusTooManyRequests)
	requireErrorCode(t, body, CodeRateLimited)
	if resp.Header.Get("Retry-After") == "" {
		t.Fatalf("login 429 did not carry Retry-After")
	}

	// Portal saturation never spends a legacy credential's budget.
	resp, body = apiCall(t, ts, http.MethodPost, "/v1/scoped-keys", model.ScopedKeyCreateRequest{
		UserID: "u-01", Label: "legacy", Scopes: []model.Scope{model.ScopeLeasesRead},
	})
	requireStatus(t, resp, http.StatusCreated)
	legacy := decodeBody[model.ScopedKeyCreated](t, body)
	req := newRequest(t, http.MethodGet, ts.URL+"/v1/leases", nil)
	req.Header.Set("Authorization", "Bearer "+legacy.Secret)
	resp, _ = doRequest(t, ts, req)
	requireStatus(t, resp, http.StatusOK)

	// A restart starts from empty buckets: the saturated address is not
	// remembered across a new limiter instance.
	restartServer := New(Config{
		AdminToken: testToken, TenantPortal: true, TenantOrigin: portalTestOrigin,
		Usage: usageAggregator(),
	}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	restarted := httptest.NewServer(restartServer)
	t.Cleanup(restarted.Close)
	portalSignup(t, restarted, "after-restart@example.com")
}

func TestPortalRateLimitsPerEmail(t *testing.T) {
	ts, _ := newPortalTestServer(t)
	for range portalLoginEmailLimit {
		resp, _ := portalCall(t, ts, http.MethodPost, "/v1/portal/login", model.TenantLoginInput{
			Email: "same@example.com", Password: "wrong password value",
		}, "", "")
		requireStatus(t, resp, http.StatusUnauthorized)
	}
	resp, body := portalCall(t, ts, http.MethodPost, "/v1/portal/login", model.TenantLoginInput{
		Email: "SAME@example.com", Password: "wrong password value",
	}, "", "")
	requireStatus(t, resp, http.StatusTooManyRequests)
	requireErrorCode(t, body, CodeRateLimited)
}

func TestTenantAdminListFiltersAndValidates(t *testing.T) {
	ts, st := newPortalTestServer(t)
	portalSignup(t, ts, "list-pending@example.com")
	pending := tenantByEmail(t, st, "list-pending@example.com")
	portalSignup(t, ts, "list-active@example.com")
	active := tenantByEmail(t, st, "list-active@example.com")
	approveTenant(t, ts, active.ID, map[string]string{"userId": "u-01"})

	resp, body := apiCall(t, ts, http.MethodGet, "/v1/admin/tenants?status=pending", nil)
	requireStatus(t, resp, http.StatusOK)
	rows := decodeBody[[]model.Tenant](t, body)
	if len(rows) != 1 || rows[0].ID != pending.ID {
		t.Fatalf("pending filter = %+v", rows)
	}
	resp, body = apiCall(t, ts, http.MethodGet, "/v1/admin/tenants?status=active", nil)
	requireStatus(t, resp, http.StatusOK)
	rows = decodeBody[[]model.Tenant](t, body)
	if len(rows) != 1 || rows[0].ID != active.ID {
		t.Fatalf("active filter = %+v", rows)
	}
	resp, _ = apiCall(t, ts, http.MethodPost, "/v1/admin/tenants/"+active.ID+"/suspend", nil)
	requireStatus(t, resp, http.StatusOK)
	resp, body = apiCall(t, ts, http.MethodGet, "/v1/admin/tenants?status=suspended", nil)
	requireStatus(t, resp, http.StatusOK)
	rows = decodeBody[[]model.Tenant](t, body)
	if len(rows) != 1 || rows[0].ID != active.ID {
		t.Fatalf("suspended filter = %+v", rows)
	}
	resp, body = apiCall(t, ts, http.MethodGet, "/v1/admin/tenants?status=bogus", nil)
	requireStatus(t, resp, http.StatusBadRequest)
	requireErrorCode(t, body, CodeInvalidArgument)
}

func TestPortalKeyCapReturnsQuotaExceeded(t *testing.T) {
	ts, st := newPortalTestServer(t)
	_, cookie, csrf := activePortalTenant(t, ts, st, "cap@example.com", "u-01")
	// u-01 already owns two active seeded keys.
	for range store.MaxTenantKeys - 2 {
		resp, _ := portalCall(t, ts, http.MethodPost, "/v1/portal/keys",
			model.TenantKeyCreateRequest{Label: "bulk"}, cookie, csrf)
		requireStatus(t, resp, http.StatusCreated)
	}
	resp, body := portalCall(t, ts, http.MethodPost, "/v1/portal/keys",
		model.TenantKeyCreateRequest{Label: "over"}, cookie, csrf)
	requireStatus(t, resp, http.StatusTooManyRequests)
	requireErrorCode(t, body, CodeQuotaExceeded)
}

func TestPortalBodyBound(t *testing.T) {
	ts, _ := newPortalTestServer(t)
	resp, body := portalCall(t, ts, http.MethodPost, "/v1/portal/signup", model.TenantSignupInput{
		Email:       "big@example.com",
		Password:    portalTestPassword,
		DisplayName: strings.Repeat("a", 70<<10),
	}, "", "")
	requireStatus(t, resp, http.StatusRequestEntityTooLarge)
	requireErrorCode(t, body, CodeInvalidArgument)
}

func TestPortalSessionDoesNotExposeStoreSecrets(t *testing.T) {
	ts, st := newPortalTestServer(t)
	_, cookie, _ := activePortalTenant(t, ts, st, "fields@example.com", "u-01")
	resp, body := portalCall(t, ts, http.MethodGet, "/v1/portal/session", nil, cookie, "")
	requireStatus(t, resp, http.StatusOK)
	for _, forbidden := range []string{"passwordHash", "csrfHash", "idHash", "keyHashes", `"secret"`} {
		if bytes.Contains(body, []byte(forbidden)) {
			t.Fatalf("session body contains %q: %s", forbidden, body)
		}
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(body, &object); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	assertKeys(t, "portal session", object, "tenant", "user", "csrfToken")
}

func TestNormalizeTenantOrigin(t *testing.T) {
	cases := []struct {
		raw  string
		want string
		ok   bool
	}{
		{"", "", true},
		{" https://Panel.Example.COM/ ", "https://panel.example.com", true},
		{"http://127.0.0.1:18081", "http://127.0.0.1:18081", true},
		{"https://panel.example.com/admin", "", false},
		{"https://panel.example.com?x=1", "", false},
		{"ftp://panel.example.com", "", false},
		{"panel.example.com", "", false},
		{"https://user@panel.example.com", "", false},
	}
	for _, tc := range cases {
		got, err := NormalizeTenantOrigin(tc.raw)
		if tc.ok && err != nil {
			t.Fatalf("NormalizeTenantOrigin(%q): %v", tc.raw, err)
		}
		if !tc.ok && err == nil {
			t.Fatalf("NormalizeTenantOrigin(%q) accepted an invalid origin", tc.raw)
		}
		if got != tc.want {
			t.Fatalf("NormalizeTenantOrigin(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestParseTrustedProxies(t *testing.T) {
	prefixes, err := ParseTrustedProxies([]string{"127.0.0.1", "10.0.0.0/8", "  ", "::1"})
	if err != nil {
		t.Fatalf("ParseTrustedProxies: %v", err)
	}
	if len(prefixes) != 3 {
		t.Fatalf("prefixes = %v, want three entries", prefixes)
	}
	if _, err := ParseTrustedProxies([]string{"proxy.internal"}); err == nil {
		t.Fatal("ParseTrustedProxies accepted a hostname")
	}
}

func TestPortalClientIPTrustsOnlyConfiguredProxies(t *testing.T) {
	trusted, err := ParseTrustedProxies([]string{"127.0.0.1", "10.0.0.0/8"})
	if err != nil {
		t.Fatalf("parse proxies: %v", err)
	}
	server := &Server{trustedProxies: trusted}

	req := httptest.NewRequest(http.MethodGet, "/v1/portal/session", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	req.Header.Set("X-Forwarded-For", "203.0.113.9, 10.1.2.3")
	if got := server.portalClientIP(req); got != "203.0.113.9" {
		t.Fatalf("trusted proxy client IP = %q, want 203.0.113.9", got)
	}

	req.RemoteAddr = "192.0.2.10:5555"
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	if got := server.portalClientIP(req); got != "192.0.2.10" {
		t.Fatalf("untrusted proxy client IP = %q, want the socket peer", got)
	}
}
