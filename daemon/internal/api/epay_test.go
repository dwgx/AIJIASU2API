package api

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/payment"
	"ajsaccountd/internal/store"
)

const (
	epayAPITestKey = "epay-api-test-key"
	epayAPITestPID = "1001"
)

func newEpayAPITestServer(t *testing.T, cfg Config) (*httptest.Server, *store.Store, *recordingSink) {
	t.Helper()
	if cfg.Epay == nil {
		cfg.Epay = payment.NewEpay(payment.EpayConfig{
			Gateway:  "https://pay.example.test/",
			PID:      epayAPITestPID,
			Key:      epayAPITestKey,
			SignType: "MD5",
			Now:      func() time.Time { return time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC) },
		})
	}
	return newOrderTestServer(t, cfg)
}

func epayCheckout(t *testing.T, ts *httptest.Server, orderID, cookie, csrf, kind string) (*http.Response, []byte) {
	t.Helper()
	return portalOrderCall(t, ts, http.MethodPost, "/v1/portal/orders/"+orderID+"/checkout",
		orderCheckoutRequest{Type: kind}, cookie, csrf, nil)
}

func epayNotifyCall(t *testing.T, ts *httptest.Server, form url.Values) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, ts.URL+epayNotifyPath, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("new notify request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return doRequest(t, ts, req)
}

func epayNotifyQueryCall(t *testing.T, ts *httptest.Server, method string, form url.Values) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, ts.URL+epayNotifyPath+"?"+form.Encode(), nil)
	if err != nil {
		t.Fatalf("new query notify request: %v", err)
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	return doRequest(t, ts, req)
}

func epayNotifyForm(t *testing.T, key string, order model.Order, tradeNo, money string) url.Values {
	t.Helper()
	form := url.Values{
		"pid":          {epayAPITestPID},
		"trade_no":     {tradeNo},
		"out_trade_no": {order.ID},
		"type":         {"alipay"},
		"name":         {order.Plan.Name},
		"money":        {money},
		"trade_status": {"TRADE_SUCCESS"},
		"sign_type":    {"MD5"},
	}
	form.Set("sign", payment.SignMD5(form, key))
	return form
}

func TestPortalEpayCheckoutShapeAndDisabledPath(t *testing.T) {
	ts, st, _ := newEpayAPITestServer(t, Config{})
	plan := createOrderPlan(t, ts, true, 3000)
	_, cookie, csrf := activePortalTenant(t, ts, st, "epay-checkout@example.com", "u-01")
	order, _ := createPortalOrder(t, ts, cookie, csrf, plan.ID, "epay-key")

	resp, body := epayCheckout(t, ts, order.ID, cookie, csrf, "alipay")
	requireStatus(t, resp, http.StatusOK)
	out := decodeBody[orderCheckoutResponse](t, body)
	parsed, err := url.Parse(out.PayURL)
	if err != nil {
		t.Fatalf("parse payUrl: %v", err)
	}
	query := parsed.Query()
	for key, want := range map[string]string{
		"pid":          epayAPITestPID,
		"type":         "alipay",
		"out_trade_no": order.ID,
		"money":        "30.00",
		"sign_type":    "MD5",
		"notify_url":   portalTestOrigin + epayNotifyPath,
		"return_url":   portalTestOrigin + "/portal",
	} {
		if got := query.Get(key); got != want {
			t.Fatalf("checkout %s = %q, want %q", key, got, want)
		}
	}
	if got := payment.SignMD5(query, epayAPITestKey); got != query.Get("sign") {
		t.Fatalf("checkout signature does not verify")
	}
	// The checkout only records intent; it never settles the order.
	resp, body = portalOrderCall(t, ts, http.MethodGet, "/v1/portal/orders/"+order.ID, nil, cookie, csrf, nil)
	requireStatus(t, resp, http.StatusOK)
	stored := decodeBody[model.Order](t, body)
	if stored.State != model.OrderAwaitingPayment || stored.Provider != model.ProviderEpay {
		t.Fatalf("after checkout order = %+v", stored)
	}

	// A different type is accepted; an unknown one is rejected.
	if resp, _ := epayCheckout(t, ts, order.ID, cookie, csrf, "wxpay"); resp.StatusCode != http.StatusOK {
		t.Fatalf("wxpay checkout status = %d", resp.StatusCode)
	}
	resp, body = epayCheckout(t, ts, order.ID, cookie, csrf, "card")
	requireStatus(t, resp, http.StatusBadRequest)
	requireErrorCode(t, body, CodeInvalidArgument)

	// A foreign order id is a 404, never a provider hint.
	_, otherCookie, otherCSRF := activePortalTenant(t, ts, st, "epay-other@example.com", "u-02")
	resp, _ = epayCheckout(t, ts, order.ID, otherCookie, otherCSRF, "alipay")
	requireStatus(t, resp, http.StatusNotFound)

	// A cancelled order cannot be checked out.
	resp, _ = portalOrderCall(t, ts, http.MethodPost, "/v1/portal/orders/"+order.ID+"/cancel", nil, cookie, csrf, nil)
	requireStatus(t, resp, http.StatusOK)
	resp, body = epayCheckout(t, ts, order.ID, cookie, csrf, "alipay")
	requireStatus(t, resp, http.StatusConflict)
	requireErrorCode(t, body, CodeConflict)

	// The disabled path is a clean 503, not a manual fallback.
	disabledTS, disabledStore, _ := newOrderTestServer(t, Config{})
	disabledPlan := createOrderPlan(t, disabledTS, true, 3000)
	_, disabledCookie, disabledCSRF := activePortalTenant(t, disabledTS, disabledStore,
		"epay-disabled@example.com", "u-01")
	disabledOrder, _ := createPortalOrder(t, disabledTS, disabledCookie, disabledCSRF, disabledPlan.ID, "epay-disabled")
	resp, body = epayCheckout(t, disabledTS, disabledOrder.ID, disabledCookie, disabledCSRF, "alipay")
	requireStatus(t, resp, http.StatusServiceUnavailable)
	requireErrorCode(t, body, CodeUpstreamUnavailable)
}

func TestPortalEpayCheckoutPollOnlyWithoutTenantOrigin(t *testing.T) {
	ts, st, _ := newEpayAPITestServer(t, Config{})
	plan := createOrderPlan(t, ts, true, 3000)
	tenant, cookie, csrf := activePortalTenant(t, ts, st, "epay-poll-only@example.com", "u-01")
	order, _ := createPortalOrder(t, ts, cookie, csrf, plan.ID, "epay-poll-only")

	user, ok := st.User("u-01")
	if !ok {
		t.Fatalf("seeded user u-01 not found")
	}
	// The real portal refuses an empty tenant origin at login, so drive the
	// handler directly with an authenticated principal: the checkout guard is
	// what this test pins down.
	server := New(Config{
		AdminToken:     testToken,
		TenantPortal:   true,
		ManualPayments: payment.NewManual(time.Now),
		Epay: payment.NewEpay(payment.EpayConfig{
			Gateway: "https://pay.example.test/", PID: epayAPITestPID, Key: epayAPITestKey,
		}),
	}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	req := newRequest(t, http.MethodPost, "/v1/portal/orders/"+order.ID+"/checkout",
		orderCheckoutRequest{Type: "alipay"})
	req = req.WithContext(context.WithValue(req.Context(), paramsKey{}, map[string]string{"id": order.ID}))
	req = withPortalPrincipal(req, portalPrincipal{Tenant: tenant, User: user})
	rec := httptest.NewRecorder()
	server.handlePortalCheckoutOrder(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("poll-only checkout status = %d, body = %s", rec.Code, rec.Body.String())
	}
	out := decodeBody[orderCheckoutResponse](t, rec.Body.Bytes())
	parsed, err := url.Parse(out.PayURL)
	if err != nil {
		t.Fatalf("parse poll-only payUrl: %v", err)
	}
	query := parsed.Query()
	if query.Has("notify_url") || query.Has("return_url") {
		t.Fatalf("poll-only checkout carried callback URLs: %s", out.PayURL)
	}
	if got := payment.SignMD5(query, epayAPITestKey); got != query.Get("sign") {
		t.Fatalf("poll-only checkout signature does not verify")
	}
}

func TestEpayNotifySettlesIdempotentlyAndRejectsSpoofs(t *testing.T) {
	ts, st, _ := newEpayAPITestServer(t, Config{})
	plan := createOrderPlan(t, ts, true, 3000)
	_, cookie, csrf := activePortalTenant(t, ts, st, "epay-notify@example.com", "u-01")
	freeFixtureCapacity(t, st)
	order, _ := createPortalOrder(t, ts, cookie, csrf, plan.ID, "epay-notify-key")
	resp, _ := epayCheckout(t, ts, order.ID, cookie, csrf, "alipay")
	requireStatus(t, resp, http.StatusOK)

	assertAwaiting := func() {
		t.Helper()
		current, ok := st.Order(order.ID)
		if !ok || current.State != model.OrderAwaitingPayment || current.Grant != nil {
			t.Fatalf("spoof changed the order: %+v, ok=%t", current, ok)
		}
	}

	// Wrong signature.
	badSign := epayNotifyForm(t, epayAPITestKey, order, "trade-spoof-sign", "30.00")
	badSign.Set("sign", strings.Repeat("0", 32))
	resp, body := epayNotifyCall(t, ts, badSign)
	if resp.StatusCode != http.StatusOK || string(body) != "fail" {
		t.Fatalf("wrong sign response = %d %q", resp.StatusCode, body)
	}
	assertAwaiting()

	// Valid signature over the wrong amount.
	badAmount := epayNotifyForm(t, epayAPITestKey, order, "trade-spoof-amount", "0.01")
	resp, body = epayNotifyCall(t, ts, badAmount)
	if resp.StatusCode != http.StatusOK || string(body) != "fail" {
		t.Fatalf("wrong amount response = %d %q", resp.StatusCode, body)
	}
	assertAwaiting()

	// Valid signature over the wrong merchant id.
	badPID := epayNotifyForm(t, epayAPITestKey, order, "trade-spoof-pid", "30.00")
	badPID.Set("pid", "9999")
	badPID.Set("sign", payment.SignMD5(badPID, epayAPITestKey))
	resp, body = epayNotifyCall(t, ts, badPID)
	if resp.StatusCode != http.StatusOK || string(body) != "fail" {
		t.Fatalf("wrong pid response = %d %q", resp.StatusCode, body)
	}
	assertAwaiting()

	// Unknown order.
	unknown := epayNotifyForm(t, epayAPITestKey, model.Order{ID: "ord-404",
		Plan: model.PlanSnapshot{Name: "x", AmountMinor: 1, Currency: "CNY"}}, "trade-unknown", "0.01")
	resp, body = epayNotifyCall(t, ts, unknown)
	if resp.StatusCode != http.StatusOK || string(body) != "fail" {
		t.Fatalf("unknown order response = %d %q", resp.StatusCode, body)
	}

	// The valid callback settles once and grants exactly one lease.
	valid := epayNotifyForm(t, epayAPITestKey, order, "trade-9001", "30.00")
	resp, body = epayNotifyCall(t, ts, valid)
	if resp.StatusCode != http.StatusOK || string(body) != "success" {
		t.Fatalf("valid notify response = %d %q", resp.StatusCode, body)
	}
	settled, ok := st.Order(order.ID)
	if !ok || settled.State != model.OrderActive || settled.Provider != model.ProviderEpay ||
		settled.Grant == nil || settled.ProviderRef == nil || *settled.ProviderRef != "trade-9001" {
		t.Fatalf("settled order = %+v, ok=%t", settled, ok)
	}
	leases := len(st.Leases())

	// Replaying the same trade_no is a duplicate: acknowledged, never
	// double-granted.
	resp, body = epayNotifyCall(t, ts, valid)
	if resp.StatusCode != http.StatusOK || string(body) != "success" {
		t.Fatalf("replay response = %d %q", resp.StatusCode, body)
	}
	replayed, _ := st.Order(order.ID)
	if len(st.Leases()) != leases || replayed.Grant == nil || replayed.Grant.LeaseID != settled.Grant.LeaseID {
		t.Fatalf("replay changed the grant: before=%+v after=%+v leases=%d/%d",
			settled.Grant, replayed.Grant, leases, len(st.Leases()))
	}

}

// TestEpaySettlementAtCapacityStaysPaidWithoutGrant is the honesty half of the
// capacity rule: the demo fixture already holds every account's single verified
// slot, so a valid epay callback must still record the money, acknowledge the
// provider, and leave the order paid without an entitlement. The refusal is
// typed in the retry audit row instead of being papered over with a lease.
func TestEpaySettlementAtCapacityStaysPaidWithoutGrant(t *testing.T) {
	ts, st, _ := newEpayAPITestServer(t, Config{})
	plan := createOrderPlan(t, ts, true, 3000)
	_, cookie, csrf := activePortalTenant(t, ts, st, "epay-capacity@example.com", "u-01")
	order, _ := createPortalOrder(t, ts, cookie, csrf, plan.ID, "epay-capacity")
	resp, _ := epayCheckout(t, ts, order.ID, cookie, csrf, "alipay")
	requireStatus(t, resp, http.StatusOK)
	leasesBefore := len(st.Leases())

	valid := epayNotifyForm(t, epayAPITestKey, order, "trade-capacity", "30.00")
	resp, body := epayNotifyCall(t, ts, valid)
	if resp.StatusCode != http.StatusOK || string(body) != "success" {
		t.Fatalf("capacity notify response = %d %q", resp.StatusCode, body)
	}

	settled, ok := st.Order(order.ID)
	if !ok || settled.State != model.OrderPaid || settled.Grant != nil ||
		settled.PaidAt == nil || settled.ActivatedAt != nil {
		t.Fatalf("over-capacity settlement = %+v, ok=%t, want paid without a grant", settled, ok)
	}
	if settled.ProviderRef == nil || *settled.ProviderRef != "trade-capacity" {
		t.Fatalf("settlement reference = %v", settled.ProviderRef)
	}
	if len(st.Leases()) != leasesBefore {
		t.Fatalf("capacity refusal minted a lease: %d -> %d", leasesBefore, len(st.Leases()))
	}

	// The customer surface is honest too: paid, not active, no entitlement.
	resp, body = portalOrderCall(t, ts, http.MethodGet, "/v1/portal/orders/"+order.ID, nil, cookie, csrf, nil)
	requireStatus(t, resp, http.StatusOK)
	if shown := decodeBody[model.Order](t, body); shown.State != model.OrderPaid || shown.Grant != nil {
		t.Fatalf("portal order = %+v, want paid without a grant", shown)
	}

	// The blocker is a typed reason, not only prose.
	refusals := 0
	for _, event := range st.Audit(100) {
		if event.Action != "payment.activation_retry" || event.Target != order.ID {
			continue
		}
		refusals++
		if event.Meta["reason"] != "no_capacity" {
			t.Fatalf("activation retry reason = %q, want no_capacity", event.Meta["reason"])
		}
	}
	if refusals == 0 {
		t.Fatalf("missing payment.activation_retry audit for %s", order.ID)
	}

	// A replay retries activation, still refuses capacity, and never records a
	// second settlement fact.
	resp, body = epayNotifyCall(t, ts, valid)
	if resp.StatusCode != http.StatusOK || string(body) != "success" {
		t.Fatalf("replay response = %d %q", resp.StatusCode, body)
	}
	replayed, _ := st.Order(order.ID)
	if replayed.State != model.OrderPaid || replayed.Grant != nil || len(st.Leases()) != leasesBefore {
		t.Fatalf("replay = %+v, leases=%d", replayed, len(st.Leases()))
	}
	if got := len(st.PaymentEvents()); got != 1 {
		t.Fatalf("payment events = %d, want one settlement fact", got)
	}
}

func TestEpayNotifyAcceptsGETAndPOSTQueryParams(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			ts, st, _ := newEpayAPITestServer(t, Config{})
			plan := createOrderPlan(t, ts, true, 3000)
			email := "epay-query-" + strings.ToLower(method) + "@example.com"
			_, cookie, csrf := activePortalTenant(t, ts, st, email, "u-01")
			freeFixtureCapacity(t, st)
			order, _ := createPortalOrder(t, ts, cookie, csrf, plan.ID, "epay-query-"+method)
			resp, _ := epayCheckout(t, ts, order.ID, cookie, csrf, "alipay")
			requireStatus(t, resp, http.StatusOK)

			form := epayNotifyForm(t, epayAPITestKey, order, "trade-query-"+method, "30.00")
			resp, body := epayNotifyQueryCall(t, ts, method, form)
			if resp.StatusCode != http.StatusOK || string(body) != "success" {
				t.Fatalf("%s query notify = %d %q", method, resp.StatusCode, body)
			}
			settled, ok := st.Order(order.ID)
			if !ok || settled.State != model.OrderActive || settled.Provider != model.ProviderEpay {
				t.Fatalf("%s notify order = %+v, ok=%t", method, settled, ok)
			}
		})
	}
}

func TestEpayNotifyRejectsNeverCheckedOutOrder(t *testing.T) {
	ts, st, _ := newEpayAPITestServer(t, Config{})
	plan := createOrderPlan(t, ts, true, 3000)
	_, cookie, csrf := activePortalTenant(t, ts, st, "epay-never-checkout@example.com", "u-01")
	order, _ := createPortalOrder(t, ts, cookie, csrf, plan.ID, "epay-never-checkout")
	if order.Provider != model.ProviderManual || order.State != model.OrderAwaitingPayment {
		t.Fatalf("new order = %+v, want manual/awaiting", order)
	}

	form := epayNotifyForm(t, epayAPITestKey, order, "trade-never-checkout", "30.00")
	resp, body := epayNotifyCall(t, ts, form)
	if resp.StatusCode != http.StatusOK || string(body) != "fail" {
		t.Fatalf("notify for never-checked-out order = %d %q", resp.StatusCode, body)
	}
	current, _ := st.Order(order.ID)
	if current.State != model.OrderAwaitingPayment || current.Provider != model.ProviderManual {
		t.Fatalf("notify changed a manual order: %+v", current)
	}
}

func TestEpayCheckoutRejectsNonCNYPlan(t *testing.T) {
	ts, st, _ := newEpayAPITestServer(t, Config{})
	resp, body := apiCall(t, ts, http.MethodPost, "/v1/admin/plans", model.PlanInput{
		Name: "USD monthly", AmountMinor: 3000, Currency: "USD",
		LeaseTTLSeconds: 3600, QuotaBytes: 5 << 30,
	})
	requireStatus(t, resp, http.StatusCreated)
	plan := decodeBody[model.Plan](t, body)
	_, cookie, csrf := activePortalTenant(t, ts, st, "epay-usd-plan@example.com", "u-01")
	order, _ := createPortalOrder(t, ts, cookie, csrf, plan.ID, "epay-usd-plan")

	resp, body = epayCheckout(t, ts, order.ID, cookie, csrf, "alipay")
	requireStatus(t, resp, http.StatusBadRequest)
	requireErrorCode(t, body, CodeInvalidArgument)
	if !strings.Contains(string(body), "CNY") {
		t.Fatalf("non-CNY checkout body = %s", body)
	}
	current, _ := st.Order(order.ID)
	if current.State != model.OrderAwaitingPayment || current.Provider != model.ProviderManual {
		t.Fatalf("rejected checkout changed the order: %+v", current)
	}
}

func TestEpayNotifyDisabledPath(t *testing.T) {
	ts, _, _ := newOrderTestServer(t, Config{})
	form := url.Values{"pid": {epayAPITestPID}, "out_trade_no": {"ord-001"}, "sign": {"x"}}
	resp, body := epayNotifyCall(t, ts, form)
	if resp.StatusCode != http.StatusServiceUnavailable || string(body) != "fail" {
		t.Fatalf("disabled notify = %d %q", resp.StatusCode, body)
	}
}

func TestEpaySecretsNeverAppearInLogsOrState(t *testing.T) {
	const secret = "super-secret-epay-key"
	var logs bytes.Buffer
	st := store.New()
	cfg := Config{
		AdminToken:     testToken,
		TenantPortal:   true,
		TenantOrigin:   portalTestOrigin,
		ManualPayments: payment.NewManual(time.Now),
		Epay: payment.NewEpay(payment.EpayConfig{
			Gateway: "https://pay.example.test/", PID: epayAPITestPID, Key: secret, SignType: "MD5",
		}),
	}
	server := New(cfg, st, slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)

	plan := createOrderPlan(t, ts, true, 3000)
	_, cookie, csrf := activePortalTenant(t, ts, st, "epay-logs@example.com", "u-01")
	order, _ := createPortalOrder(t, ts, cookie, csrf, plan.ID, "epay-logs")
	resp, body := epayCheckout(t, ts, order.ID, cookie, csrf, "alipay")
	requireStatus(t, resp, http.StatusOK)
	checkout := decodeBody[orderCheckoutResponse](t, body)

	// A bad-signature callback exercises the rejection/audit path.
	bad := epayNotifyForm(t, secret, order, "trade-bad", "30.00")
	bad.Set("sign", strings.Repeat("1", 32))
	epayNotifyCall(t, ts, bad)

	logText := logs.String()
	if strings.Contains(logText, secret) {
		t.Fatalf("logs contain the merchant key")
	}
	if strings.Contains(logText, "sign=") || strings.Contains(logText, checkout.PayURL) {
		t.Fatalf("logs contain a signature query or the pay URL")
	}
	snapshot, err := st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if strings.Contains(string(snapshot), secret) {
		t.Fatalf("state contains the merchant key")
	}
	for _, event := range st.Audit(50) {
		if strings.Contains(event.Detail, secret) {
			t.Fatalf("audit detail contains the merchant key")
		}
	}
}

func TestEpayQueryReconciliationSettlesRealOrder(t *testing.T) {
	ts, st, _ := newEpayAPITestServer(t, Config{})
	plan := createOrderPlan(t, ts, true, 3000)
	_, cookie, csrf := activePortalTenant(t, ts, st, "epay-reconcile@example.com", "u-01")
	freeFixtureCapacity(t, st)
	order, _ := createPortalOrder(t, ts, cookie, csrf, plan.ID, "epay-reconcile")
	resp, _ := epayCheckout(t, ts, order.ID, cookie, csrf, "alipay")
	requireStatus(t, resp, http.StatusOK)

	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("act") != "order" || r.URL.Query().Get("out_trade_no") != order.ID {
			t.Errorf("unexpected query: %v", r.URL.Query())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":1,"trade_no":"trade-reconcile","out_trade_no":"` + order.ID + `","money":"30.00","trade_status":"TRADE_SUCCESS"}`))
	}))
	defer gateway.Close()
	provider := payment.NewEpay(payment.EpayConfig{Gateway: gateway.URL, PID: epayAPITestPID, Key: epayAPITestKey})
	reconciler := payment.NewEpayReconciler(payment.EpayReconcilerConfig{
		Provider: provider,
		Store:    st,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if reconciler == nil {
		t.Fatalf("reconciler is nil")
	}
	if err := reconciler.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	settled, ok := st.Order(order.ID)
	if !ok || settled.State != model.OrderActive || settled.Grant == nil ||
		settled.ProviderRef == nil || *settled.ProviderRef != "trade-reconcile" {
		t.Fatalf("reconciled order = %+v, ok=%t", settled, ok)
	}
	found := false
	for _, event := range st.Audit(100) {
		if event.Action == "epay.reconcile" && event.Target == order.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing epay.reconcile audit")
	}
}
