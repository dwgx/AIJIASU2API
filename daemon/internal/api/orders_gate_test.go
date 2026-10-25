package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/payment"
	"ajsaccountd/internal/store"
)

// newPortalOffOrderServer reproduces the deployed shape that AJS-302 flagged:
// an admin token is configured and AJS_TENANT_PORTAL is not. The admin plan
// and order chains must still be reachable, while the customer portal stays
// unregistered.
func newPortalOffOrderServer(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	st := store.New()
	server := New(Config{
		AdminToken:     testToken,
		ManualPayments: payment.NewManual(time.Now),
	}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)
	return ts, st
}

// seedOrderTenant creates one active tenant bound to a fresh enabled user
// without touching the portal HTTP chain, which is exactly the state a
// portal-off deployment reaches through signup plus approval.
func seedOrderTenant(t *testing.T, st *store.Store, email, name string) (model.Tenant, model.PanelUser) {
	t.Helper()
	user, err := st.CreateUser(model.CreateUserInput{Name: name})
	if err != nil {
		t.Fatalf("create user %q: %v", name, err)
	}
	if err := st.SignupTenant(model.TenantSignupInput{
		Email: email, Password: portalTestPassword, DisplayName: name,
	}); err != nil {
		t.Fatalf("signup tenant %q: %v", email, err)
	}
	tenant := tenantByEmail(t, st, email)
	if _, err := st.ApproveTenant(tenant.ID, user.ID, nil); err != nil {
		t.Fatalf("approve tenant %q: %v", tenant.ID, err)
	}
	return tenant, user
}

// TestAdminPlanAndOrderRoutesIgnorePortalMode locks the deployed contract:
// /v1/admin/plans* and /v1/admin/orders* are admin surfaces registered even
// when AJS_TENANT_PORTAL is off, while /v1/portal/* and /v1/admin/tenants*
// stay behind the flag.
func TestAdminPlanAndOrderRoutesIgnorePortalMode(t *testing.T) {
	ts, st := newPortalOffOrderServer(t)

	// The admin surface is registered but never anonymous: without a bearer
	// the answer is 401, not the router's 404.
	for _, path := range []string{"/v1/admin/plans", "/v1/admin/orders"} {
		resp, body := doRequest(t, ts, newRequest(t, http.MethodGet, ts.URL+path, nil))
		requireStatus(t, resp, http.StatusUnauthorized)
		requireErrorCode(t, body, CodeUnauthorized)
	}
	resp, body := doRequest(t, ts, newRequest(t, http.MethodPost, ts.URL+"/v1/admin/plans", nil))
	requireStatus(t, resp, http.StatusUnauthorized)
	requireErrorCode(t, body, CodeUnauthorized)

	// The portal chains are still opt-in. They answer 404 both with and
	// without the admin bearer, because no route is registered for them.
	for _, path := range []string{
		"/v1/portal/plans",
		"/v1/portal/orders",
		"/v1/portal/session",
	} {
		resp, _ := apiCall(t, ts, http.MethodGet, path, nil)
		requireStatus(t, resp, http.StatusNotFound)
		resp, _ = doRequest(t, ts, newRequest(t, http.MethodGet, ts.URL+path, nil))
		requireStatus(t, resp, http.StatusNotFound)
	}
	// Tenant administration belongs to the portal feature too: the admin
	// bearer reaches the router and finds no route. (Without a bearer the
	// tenant-admin guard still answers 401 before routing.)
	resp, _ = apiCall(t, ts, http.MethodGet, "/v1/admin/tenants", nil)
	requireStatus(t, resp, http.StatusNotFound)

	// Plan CRUD works with the admin bearer alone.
	resp, body = apiCall(t, ts, http.MethodPost, "/v1/admin/plans", model.PlanInput{
		Name:            "Portal-off monthly",
		AmountMinor:     2500,
		Currency:        "USD",
		LeaseTTLSeconds: 3600,
		QuotaBytes:      1 << 30,
	})
	requireStatus(t, resp, http.StatusCreated)
	plan := decodeBody[model.Plan](t, body)
	if plan.ID == "" {
		t.Fatal("created plan has no id")
	}

	resp, body = apiCall(t, ts, http.MethodGet, "/v1/admin/plans", nil)
	requireStatus(t, resp, http.StatusOK)
	plans := decodeBody[[]model.Plan](t, body)
	if len(plans) != 1 || plans[0].ID != plan.ID {
		t.Fatalf("admin plan list = %+v, want only %q", plans, plan.ID)
	}

	resp, body = apiCall(t, ts, http.MethodGet, "/v1/admin/plans/"+plan.ID, nil)
	requireStatus(t, resp, http.StatusOK)
	if got := decodeBody[model.Plan](t, body); got.ID != plan.ID {
		t.Fatalf("admin plan = %+v, want %q", got, plan.ID)
	}

	renamed := "Portal-off renamed"
	resp, body = apiCall(t, ts, http.MethodPatch, "/v1/admin/plans/"+plan.ID,
		model.PlanPatch{Name: &renamed})
	requireStatus(t, resp, http.StatusOK)
	if got := decodeBody[model.Plan](t, body); got.Name != renamed {
		t.Fatalf("patched plan name = %q, want %q", got.Name, renamed)
	}

	// Orders are created by the portal in normal operation; the admin chain
	// must still read and settle them with the portal off, so seed the store
	// directly and drive every admin order route.
	tenantA, userA := seedOrderTenant(t, st, "portal-off-a@example.com", "Portal Off A")
	tenantB, userB := seedOrderTenant(t, st, "portal-off-b@example.com", "Portal Off B")
	freeFixtureCapacity(t, st)
	order, _, err := st.CreateOrder(tenantA.ID, userA.ID, plan.ID, "", "operator")
	if err != nil {
		t.Fatalf("seed order: %v", err)
	}
	cancelled, _, err := st.CreateOrder(tenantB.ID, userB.ID, plan.ID, "", "operator")
	if err != nil {
		t.Fatalf("seed cancellable order: %v", err)
	}

	resp, body = apiCall(t, ts, http.MethodGet, "/v1/admin/orders", nil)
	requireStatus(t, resp, http.StatusOK)
	orders := decodeBody[[]model.Order](t, body)
	if len(orders) != 2 {
		t.Fatalf("admin order list = %+v, want 2 rows", orders)
	}

	resp, body = apiCall(t, ts, http.MethodGet, "/v1/admin/orders/"+order.ID, nil)
	requireStatus(t, resp, http.StatusOK)
	if got := decodeBody[model.Order](t, body); got.State != model.OrderAwaitingPayment {
		t.Fatalf("admin order state = %q, want awaiting_payment", got.State)
	}

	resp, body = apiCall(t, ts, http.MethodPost, "/v1/admin/orders/"+order.ID+"/settle",
		model.OrderSettleRequest{Reference: "RCPT-OFFLINE-1", OfflineFundsReceived: true})
	requireStatus(t, resp, http.StatusOK)
	// The seeded tenant and user are active, so settlement also grants the
	// order entitlement; the admin route must run the whole transition.
	if got := decodeBody[model.Order](t, body); got.State != model.OrderActive || got.Grant == nil {
		t.Fatalf("settled order = %+v, want active with a grant", got)
	}

	resp, body = apiCall(t, ts, http.MethodPost, "/v1/admin/orders/"+order.ID+"/refund",
		model.OrderRefundRequest{Reference: "RFND-OFFLINE-1", Reason: "operator correction"})
	requireStatus(t, resp, http.StatusOK)
	if got := decodeBody[model.Order](t, body); got.State != model.OrderRefunded {
		t.Fatalf("refunded order state = %q, want refunded", got.State)
	}

	resp, body = apiCall(t, ts, http.MethodPost, "/v1/admin/orders/"+order.ID+"/chargeback",
		model.OrderChargebackRequest{Reference: "CB-OFFLINE-1", Reason: "dispute evidence"})
	requireStatus(t, resp, http.StatusOK)
	if got := decodeBody[model.Order](t, body); got.State != model.OrderChargedBack {
		t.Fatalf("chargeback order state = %q, want charged_back", got.State)
	}

	resp, body = apiCall(t, ts, http.MethodPost, "/v1/admin/orders/"+cancelled.ID+"/cancel", nil)
	requireStatus(t, resp, http.StatusOK)
	if got := decodeBody[model.Order](t, body); got.State != model.OrderCancelled {
		t.Fatalf("cancelled order state = %q, want cancelled", got.State)
	}

	resp, _ = apiCall(t, ts, http.MethodDelete, "/v1/admin/plans/"+plan.ID, nil)
	requireStatus(t, resp, http.StatusNoContent)
}
