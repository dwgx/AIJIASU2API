package api

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/payment"
	"ajsaccountd/internal/store"
)

const adminTestOrigin = "https://admin.example.test"

// newAdminCSRFServer builds the full mutation surface: users, tenant approval,
// orders and backup/restore. The admin origin allowlist is deliberately a
// non-default origin so a same-origin browser test cannot accidentally pass
// through the loopback development defaults.
func newAdminCSRFServer(t *testing.T) (*httptest.Server, *store.Store, string) {
	t.Helper()
	st := store.New()
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	if err := st.Save(statePath); err != nil {
		t.Fatalf("save state: %v", err)
	}
	server := New(Config{
		AdminToken:         testToken,
		AdminOrigins:       []string{adminTestOrigin},
		TenantPortal:       true,
		TenantOrigin:       portalTestOrigin,
		ManualPayments:     payment.NewManual(time.Now),
		StateDir:           dir,
		StatePath:          statePath,
		AllowBackupRestore: true,
		Restore:            &RestoreFlag{},
	}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)
	return ts, st, statePath
}

// adminCSRFRequest sends one admin request with explicit CSRF-related headers.
// The Authorization header is always the valid admin bearer so a rejection is
// attributable to the CSRF gate, never to authentication.
func adminCSRFRequest(t *testing.T, ts *httptest.Server, method, path string, body any, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	req := newRequest(t, method, ts.URL+path, body)
	req.Header.Set("Authorization", "Bearer "+testToken)
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	return doRequest(t, ts, req)
}

func TestNormalizeAdminOrigins(t *testing.T) {
	got, err := NormalizeAdminOrigins([]string{
		" https://Admin.Example.COM/ ",
		"http://127.0.0.1:18081",
		"https://admin.example.com",
		"",
	})
	if err != nil {
		t.Fatalf("NormalizeAdminOrigins: %v", err)
	}
	want := []string{"https://admin.example.com", "http://127.0.0.1:18081"}
	if len(got) != len(want) {
		t.Fatalf("origins = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("origins[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	for _, raw := range []string{
		"https://admin.example.com/admin",
		"https://admin.example.com?x=1",
		"https://user@admin.example.com",
		"ftp://admin.example.com",
		"admin.example.com",
	} {
		if _, err := NormalizeAdminOrigins([]string{raw}); err == nil {
			t.Fatalf("NormalizeAdminOrigins(%q) accepted an invalid origin", raw)
		}
	}

	empty, err := NormalizeAdminOrigins(nil)
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty list = %v/%v, want empty", empty, err)
	}
}

func TestAdminCSRFHeaderAndOriginMatrix(t *testing.T) {
	ts, st, _ := newAdminCSRFServer(t)

	cases := []struct {
		name     string
		origin   string
		csrf     string
		secFetch string
		want     int
	}{
		{"allowed origin and header", adminTestOrigin, adminCSRFValue, "", http.StatusOK},
		{"allowed origin missing header", adminTestOrigin, "", "", http.StatusForbidden},
		{"allowed origin wrong header", adminTestOrigin, "0", "", http.StatusForbidden},
		{"hostile origin with header", "https://attacker.invalid", adminCSRFValue, "", http.StatusForbidden},
		{"hostile origin form simple", "https://attacker.invalid", "", "", http.StatusForbidden},
		{"origin prefix is not exact", "https://admin.example.test.evil", adminCSRFValue, "", http.StatusForbidden},
		{"scheme mismatch", "http://admin.example.test", adminCSRFValue, "", http.StatusForbidden},
		{"null origin", "null", adminCSRFValue, "", http.StatusForbidden},
		{"cross-site fetch metadata", adminTestOrigin, adminCSRFValue, "cross-site", http.StatusForbidden},
		{"non-browser client", "", adminCSRFValue, "", http.StatusOK},
		{"non-browser missing header", "", "", "", http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := st.SetUserEnabled("u-01", true); err != nil {
				t.Fatalf("reset user: %v", err)
			}
			headers := map[string]string{}
			if tc.origin != "" {
				headers["Origin"] = tc.origin
			}
			if tc.csrf != "" {
				headers[adminCSRFHeader] = tc.csrf
			}
			if tc.secFetch != "" {
				headers["Sec-Fetch-Site"] = tc.secFetch
			}
			resp, body := adminCSRFRequest(t, ts, http.MethodPost, "/v1/users/u-01/suspend", nil, headers)
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d (body %s)", resp.StatusCode, tc.want, body)
			}
			user, ok := st.User("u-01")
			if !ok {
				t.Fatal("user u-01 disappeared")
			}
			if tc.want == http.StatusOK {
				if user.Enabled {
					t.Fatal("allowed suspend left the user enabled")
				}
				return
			}
			if !user.Enabled {
				t.Fatal("rejected request changed the user state")
			}
			requireErrorCode(t, body, CodeForbidden)
		})
	}
}

// TestAdminCSRFCrossOriginDestructiveRoutesFailClosed reproduces the review's
// falsifier: every representative destructive admin path must answer 403 for a
// hostile origin and leave the store and the state directory unchanged. The
// requests carry a valid bearer and X-AJS-CSRF, so only the Origin allowlist
// can be responsible for the refusal.
func TestAdminCSRFCrossOriginDestructiveRoutesFailClosed(t *testing.T) {
	ts, st, statePath := newAdminCSRFServer(t)

	portalSignup(t, ts, "csrf-tenant@example.com")
	pending := tenantByEmail(t, st, "csrf-tenant@example.com")
	if pending.Status != model.TenantPending {
		t.Fatalf("pending tenant status = %q", pending.Status)
	}

	plan := createOrderPlan(t, ts, true, 3000)
	_, cookie, csrf := activePortalTenant(t, ts, st, "csrf-order@example.com", "u-02")
	order, status := createPortalOrder(t, ts, cookie, csrf, plan.ID, "csrf-order-key")
	if status != http.StatusCreated || order.State != model.OrderAwaitingPayment {
		t.Fatalf("created order = %+v, status %d", order, status)
	}

	resp, envelope := apiCall(t, ts, http.MethodPost, "/v1/backup", map[string]any{
		"passphrase": backupTestPassphrase,
	})
	requireStatus(t, resp, http.StatusOK)
	// Make the on-disk state different from the envelope so a restore that
	// slipped past the CSRF gate would be observable.
	if err := os.WriteFile(statePath, []byte(`{"version":1,"accounts":[]}`), 0o600); err != nil {
		t.Fatalf("mutate state file: %v", err)
	}
	beforeRestore, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read mutated state: %v", err)
	}

	hostile := map[string]string{
		"Origin":           "https://attacker.invalid",
		"Sec-Fetch-Site":   "cross-site",
		adminCSRFHeader:    adminCSRFValue,
		"X-Forwarded-Host": "admin.example.test",
	}

	t.Run("suspend", func(t *testing.T) {
		resp, body := adminCSRFRequest(t, ts, http.MethodPost, "/v1/users/u-01/suspend", nil, hostile)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (body %s)", resp.StatusCode, body)
		}
		requireErrorCode(t, body, CodeForbidden)
		user, ok := st.User("u-01")
		if !ok || !user.Enabled {
			t.Fatalf("user state changed: %+v (ok=%v)", user, ok)
		}
	})

	t.Run("delete", func(t *testing.T) {
		resp, body := adminCSRFRequest(t, ts, http.MethodDelete, "/v1/users/u-01", nil, hostile)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (body %s)", resp.StatusCode, body)
		}
		requireErrorCode(t, body, CodeForbidden)
		if _, ok := st.User("u-01"); !ok {
			t.Fatal("rejected delete removed the user")
		}
	})

	t.Run("approve", func(t *testing.T) {
		resp, body := adminCSRFRequest(t, ts, http.MethodPost,
			"/v1/admin/tenants/"+pending.ID+"/approve",
			model.TenantApproveRequest{UserID: "u-01"}, hostile)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (body %s)", resp.StatusCode, body)
		}
		requireErrorCode(t, body, CodeForbidden)
		after, ok := st.Tenant(pending.ID)
		if !ok || after.Status != model.TenantPending || after.UserID != nil {
			t.Fatalf("tenant state changed: %+v (ok=%v)", after, ok)
		}
	})

	t.Run("settle", func(t *testing.T) {
		resp, body := adminCSRFRequest(t, ts, http.MethodPost,
			"/v1/admin/orders/"+order.ID+"/settle",
			model.OrderSettleRequest{Reference: "csrf-ref", OfflineFundsReceived: true}, hostile)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (body %s)", resp.StatusCode, body)
		}
		requireErrorCode(t, body, CodeForbidden)
		after, ok := st.Order(order.ID)
		if !ok || after.State != model.OrderAwaitingPayment {
			t.Fatalf("order state changed: %+v (ok=%v)", after, ok)
		}
	})

	t.Run("refund", func(t *testing.T) {
		freeFixtureCapacity(t, st)
		resp, body := settleOrderAPI(t, ts, order.ID, "csrf-settle", true)
		requireStatus(t, resp, http.StatusOK)
		settled, ok := st.Order(order.ID)
		if !ok || (settled.State != model.OrderPaid && settled.State != model.OrderActive) {
			t.Fatalf("settled order = %+v (ok=%v)", settled, ok)
		}
		before := settled.State

		resp, body = adminCSRFRequest(t, ts, http.MethodPost,
			"/v1/admin/orders/"+order.ID+"/refund",
			model.OrderRefundRequest{Reference: "csrf-refund", Reason: "csrf attempt"}, hostile)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (body %s)", resp.StatusCode, body)
		}
		requireErrorCode(t, body, CodeForbidden)
		after, ok := st.Order(order.ID)
		if !ok || after.State != before {
			t.Fatalf("order state changed: %+v -> %+v (ok=%v)", before, after, ok)
		}
	})

	t.Run("restore", func(t *testing.T) {
		resp, body := adminCSRFRequest(t, ts, http.MethodPost, "/v1/backup/restore", map[string]any{
			"envelope":   envelope,
			"passphrase": backupTestPassphrase,
			"dryRun":     false,
			"confirm":    "RESTORE",
		}, hostile)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (body %s)", resp.StatusCode, body)
		}
		requireErrorCode(t, body, CodeForbidden)
		after, err := os.ReadFile(statePath)
		if err != nil {
			t.Fatalf("read state after rejected restore: %v", err)
		}
		if !bytes.Equal(after, beforeRestore) {
			t.Fatal("rejected restore changed state.json")
		}
	})
}

// TestAdminCSRFLeavesPortalAndProviderCallbacksAlone proves the two excluded
// chains keep their own authentication: a portal mutation still needs its
// session CSRF token, while a provider callback reaches its handler (and
// answers 503 because no adapter is configured) instead of being refused as a
// missing admin CSRF request.
func TestAdminCSRFLeavesPortalAndProviderCallbacksAlone(t *testing.T) {
	ts, st, _ := newAdminCSRFServer(t)
	portalSignup(t, ts, "csrf-portal@example.com")
	tenant := tenantByEmail(t, st, "csrf-portal@example.com")
	approveTenant(t, ts, tenant.ID, map[string]string{"userId": "u-01"})

	// A portal mutation without its session CSRF token is refused by the
	// portal chain, not by the admin gate.
	resp, body := portalCall(t, ts, http.MethodPost, "/v1/portal/login", model.TenantLoginInput{
		Email: "csrf-portal@example.com", Password: portalTestPassword,
	}, "", "")
	requireStatus(t, resp, http.StatusOK)
	cookie := ""
	for _, candidate := range resp.Cookies() {
		if candidate.Name == portalCookieName {
			cookie = candidate.Value
		}
	}
	if cookie == "" {
		t.Fatal("portal login did not set a cookie")
	}
	_ = body
	resp, body = portalCall(t, ts, http.MethodPost, "/v1/portal/logout", nil, cookie, "")
	requireStatus(t, resp, http.StatusForbidden)
	requireErrorCode(t, body, CodeForbidden)

	for _, path := range []string{stripeWebhookPath, epayNotifyPath} {
		req := newRequest(t, http.MethodPost, ts.URL+path, map[string]any{})
		resp, _ := doRequest(t, ts, req)
		if resp.StatusCode == http.StatusForbidden {
			t.Fatalf("%s was refused by the admin CSRF gate", path)
		}
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("%s status = %d, want 503 from the disabled adapter", path, resp.StatusCode)
		}
	}
}

func TestAdminCSRFAppliesToScopedKeyMutations(t *testing.T) {
	ts, st, _ := newAdminCSRFServer(t)
	created, _ := mustCreateScopedKey(t, ts, map[string]any{
		"userId": "u-01",
		"label":  "csrf scoped key",
		"scopes": []string{"admin"},
	})

	req := newRequest(t, http.MethodPost, ts.URL+"/v1/users/u-01/suspend", nil)
	req.Header.Set("Authorization", "Bearer "+created.Secret)
	resp, body := doRequest(t, ts, req)
	requireStatus(t, resp, http.StatusForbidden)
	requireErrorCode(t, body, CodeForbidden)
	if user, _ := st.User("u-01"); !user.Enabled {
		t.Fatal("scoped-key request without CSRF changed the user state")
	}

	req = newRequest(t, http.MethodPost, ts.URL+"/v1/users/u-01/suspend", nil)
	req.Header.Set("Authorization", "Bearer "+created.Secret)
	req.Header.Set(adminCSRFHeader, adminCSRFValue)
	resp, body = doRequest(t, ts, req)
	requireStatus(t, resp, http.StatusOK)
	if user, _ := st.User("u-01"); user.Enabled {
		t.Fatalf("scoped-key request with CSRF did not suspend: %s", body)
	}
}
