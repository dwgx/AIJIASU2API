package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"ajsaccountd/internal/model"
)

// TestOverlayEpayNotifyGET pins the transport documented by mainstream 易支付
// gateways: the payment notification is a GET with query parameters. The
// daemon registers only POST and reads only r.PostForm.
func TestOverlayEpayNotifyGET(t *testing.T) {
	ts, st, _ := newEpayAPITestServer(t, Config{})
	plan := createOrderPlan(t, ts, true, 3000)
	_, cookie, csrf := activePortalTenant(t, ts, st, "epay-get-notify@example.com", "u-01")
	order, _ := createPortalOrder(t, ts, cookie, csrf, plan.ID, "epay-get-notify")
	resp, _ := epayCheckout(t, ts, order.ID, cookie, csrf, "alipay")
	requireStatus(t, resp, http.StatusOK)

	form := epayNotifyForm(t, epayAPITestKey, order, "trade-get-9001", "30.00")
	req, err := http.NewRequest(http.MethodGet, ts.URL+epayNotifyPath+"?"+form.Encode(), nil)
	if err != nil {
		t.Fatalf("new GET notify request: %v", err)
	}
	resp, body := doRequest(t, ts, req)
	t.Logf("GET notify -> HTTP %d body=%q", resp.StatusCode, body)
	current, _ := st.Order(order.ID)
	t.Logf("order after GET notify: state=%s provider=%s", current.State, current.Provider)
	if resp.StatusCode != http.StatusOK || string(body) != "success" {
		t.Fatalf("standard 易支付 GET notify was not accepted: HTTP %d body=%q", resp.StatusCode, body)
	}
}

// TestOverlayEpayNotifyPOSTQueryParams proves that even a POST that carries the
// signed parameters in the query string is ignored: the handler reads only
// r.PostForm, which is empty.
func TestOverlayEpayNotifyPOSTQueryParams(t *testing.T) {
	ts, st, _ := newEpayAPITestServer(t, Config{})
	plan := createOrderPlan(t, ts, true, 3000)
	_, cookie, csrf := activePortalTenant(t, ts, st, "epay-post-query@example.com", "u-01")
	order, _ := createPortalOrder(t, ts, cookie, csrf, plan.ID, "epay-post-query")
	resp, _ := epayCheckout(t, ts, order.ID, cookie, csrf, "alipay")
	requireStatus(t, resp, http.StatusOK)

	form := epayNotifyForm(t, epayAPITestKey, order, "trade-post-query-1", "30.00")
	req, err := http.NewRequest(http.MethodPost, ts.URL+epayNotifyPath+"?"+form.Encode(), nil)
	if err != nil {
		t.Fatalf("new POST query notify request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, body := doRequest(t, ts, req)
	t.Logf("POST notify with query params -> HTTP %d body=%q", resp.StatusCode, body)
	current, _ := st.Order(order.ID)
	t.Logf("order after POST query notify: state=%s", current.State)
	if resp.StatusCode != http.StatusOK || string(body) != "success" {
		t.Fatalf("signed notify parameters in the query string were ignored: HTTP %d body=%q", resp.StatusCode, body)
	}
}

// TestOverlayEpayNotifySettlesNonEpayOrder proves that a valid epay notify can
// settle an order that never selected epay: the handler and adapter never
// compare order.Provider with the event provider.
func TestOverlayEpayNotifySettlesNonEpayOrder(t *testing.T) {
	ts, st, _ := newEpayAPITestServer(t, Config{})
	plan := createOrderPlan(t, ts, true, 3000)
	_, cookie, csrf := activePortalTenant(t, ts, st, "epay-non-epay@example.com", "u-01")
	order, _ := createPortalOrder(t, ts, cookie, csrf, plan.ID, "epay-non-epay")
	if order.Provider != model.ProviderManual || order.State != model.OrderAwaitingPayment {
		t.Fatalf("new order = %+v, want manual/awaiting", order)
	}

	form := epayNotifyForm(t, epayAPITestKey, order, "trade-non-epay-1", "30.00")
	resp, body := epayNotifyCall(t, ts, form)
	t.Logf("POST notify for never-checked-out order -> HTTP %d body=%q", resp.StatusCode, body)
	current, _ := st.Order(order.ID)
	t.Logf("order after notify: state=%s provider=%s", current.State, current.Provider)
	if current.State != model.OrderAwaitingPayment {
		t.Fatalf("a valid epay notify settled an order that never selected epay (state=%s)", current.State)
	}
}

// TestOverlayEpayCheckoutAcceptsNonCNYPlan proves that epay checkout has no
// currency guard: epay money carries no currency, but the plan snapshot may be
// any ISO-4217 code, so 30.00 USD is sent as 30.00 CNY-equivalent.
func TestOverlayEpayCheckoutAcceptsNonCNYPlan(t *testing.T) {
	ts, st, _ := newEpayAPITestServer(t, Config{})
	resp, body := apiCall(t, ts, http.MethodPost, "/v1/admin/plans", model.PlanInput{
		Name: "USD monthly", AmountMinor: 3000, Currency: "USD",
		LeaseTTLSeconds: 3600, QuotaBytes: 5 << 30,
	})
	requireStatus(t, resp, http.StatusCreated)
	plan := decodeBody[model.Plan](t, body)
	_, cookie, csrf := activePortalTenant(t, ts, st, "epay-usd@example.com", "u-01")
	order, _ := createPortalOrder(t, ts, cookie, csrf, plan.ID, "epay-usd")

	resp, body = epayCheckout(t, ts, order.ID, cookie, csrf, "alipay")
	t.Logf("USD plan checkout -> HTTP %d body=%s", resp.StatusCode, body)
	if resp.StatusCode == http.StatusOK {
		var out orderCheckoutResponse
		if err := json.Unmarshal(body, &out); err == nil {
			parsed, _ := url.Parse(out.PayURL)
			t.Logf("payUrl money=%q for plan currency=%q", parsed.Query().Get("money"), order.Plan.Currency)
		}
		t.Fatalf("epay checkout accepted a USD plan; epay money has no currency")
	}
}

// TestOverlayEpaySettingsStatus records that GET /v1/settings carries no
// epayEnabled flag, so the dashboard's provider chip can only say "unknown".
func TestOverlayEpaySettingsStatus(t *testing.T) {
	ts, _, _ := newEpayAPITestServer(t, Config{})
	resp, body := apiCall(t, ts, http.MethodGet, "/v1/settings", nil)
	requireStatus(t, resp, http.StatusOK)
	var settings map[string]any
	if err := json.Unmarshal(body, &settings); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	_, ok := settings["epayEnabled"]
	t.Logf("GET /v1/settings keys=%v epayEnabled=%v", auditKeysOf(settings), ok)
	if !ok {
		t.Fatalf("settings does not report epayEnabled; the dashboard chip stays unknown in live mode")
	}
}

func auditKeysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
