package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"ajsaccountd/internal/fulfilment"
	"ajsaccountd/internal/model"
	"ajsaccountd/internal/payment"
	"ajsaccountd/internal/store"
	"ajsaccountd/internal/webhook"
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

// portalUserQuota reads the monthly quota of the user linked to one tenant.
// It is the financial-effect probe for settlement tests: recording money must
// not move it, and exactly one activation may.
func portalUserQuota(t *testing.T, st *store.Store, tenantID string) *int64 {
	t.Helper()
	tenant, ok := st.Tenant(tenantID)
	if !ok || tenant.UserID == nil {
		t.Fatalf("tenant %s has no linked user", tenantID)
	}
	user, ok := st.User(*tenant.UserID)
	if !ok {
		t.Fatalf("user %s missing", *tenant.UserID)
	}
	return user.QuotaBytesMonth
}

// requirePortalQuota fails unless the linked user's quota still equals want.
func requirePortalQuota(t *testing.T, st *store.Store, tenantID string, want *int64) {
	t.Helper()
	got := portalUserQuota(t, st, tenantID)
	switch {
	case got == nil && want == nil:
		return
	case got == nil || want == nil:
		t.Fatalf("user quota = %v, want %v", got, want)
	case *got != *want:
		t.Fatalf("user quota = %d, want %d", *got, *want)
	}
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
		if !ok || current.State != model.OrderAwaitingPayment || current.Grant != nil ||
			current.Fulfilment != nil {
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
	// None of the rejected callbacks recorded money or work.
	if got := len(st.PaymentEvents()); got != 0 {
		t.Fatalf("rejected callbacks recorded %d payment events", got)
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
	if settled.Fulfilment != nil {
		t.Fatalf("serving order kept a fulfilment job: %+v", settled.Fulfilment)
	}
	leases := len(st.Leases())

	// Replaying the same trade_no is a duplicate: acknowledged, never
	// double-granted and never given a second job.
	resp, body = epayNotifyCall(t, ts, valid)
	if resp.StatusCode != http.StatusOK || string(body) != "success" {
		t.Fatalf("replay response = %d %q", resp.StatusCode, body)
	}
	replayed, _ := st.Order(order.ID)
	if len(st.Leases()) != leases || replayed.Grant == nil || replayed.Grant.LeaseID != settled.Grant.LeaseID {
		t.Fatalf("replay changed the grant: before=%+v after=%+v leases=%d/%d",
			settled.Grant, replayed.Grant, leases, len(st.Leases()))
	}
	if replayed.Fulfilment != nil {
		t.Fatalf("replay created a fulfilment job: %+v", replayed.Fulfilment)
	}
	if got := len(st.PaymentEvents()); got != 1 {
		t.Fatalf("payment events = %d, want one settlement fact", got)
	}
}

// TestEpaySettlementAtCapacityStaysPaidWithoutGrant is the honesty half of the
// capacity rule: the demo fixture already holds every account's single verified
// slot, so a valid epay callback must still record the money, attach exactly one
// durable fulfilment job, and answer the gateway "fail" instead of claiming
// service that does not exist. The refusal is typed in the retry audit row
// instead of being papered over with a lease.
func TestEpaySettlementAtCapacityStaysPaidWithoutGrant(t *testing.T) {
	ts, st, _ := newEpayAPITestServer(t, Config{})
	plan := createOrderPlan(t, ts, true, 3000)
	tenant, cookie, csrf := activePortalTenant(t, ts, st, "epay-capacity@example.com", "u-01")
	order, _ := createPortalOrder(t, ts, cookie, csrf, plan.ID, "epay-capacity")
	resp, _ := epayCheckout(t, ts, order.ID, cookie, csrf, "alipay")
	requireStatus(t, resp, http.StatusOK)
	leasesBefore := len(st.Leases())
	quotaBefore := portalUserQuota(t, st, tenant.ID)

	valid := epayNotifyForm(t, epayAPITestKey, order, "trade-capacity", "30.00")
	resp, body := epayNotifyCall(t, ts, valid)
	if resp.StatusCode != http.StatusOK || string(body) != "fail" {
		t.Fatalf("capacity notify response = %d %q, want 200 fail", resp.StatusCode, body)
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
	// Money was recorded, so the user's quota must not have moved: quota is
	// entitlement state, not payment state.
	requirePortalQuota(t, st, tenant.ID, quotaBefore)

	// The payment is recorded and exactly one durable job owns the missing
	// publication. A callback alone is not an attempt, so the job is still
	// queued at zero attempts.
	job := settled.Fulfilment
	if job == nil || job.Mode != model.FulfilmentInitial || job.State != model.FulfilmentQueued ||
		job.Attempts != 0 || job.NextAttemptAt == nil {
		t.Fatalf("fulfilment job = %+v, want one queued initial job", job)
	}
	queuedAt := job.UpdatedAt

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

	// A replay answers fail again, retries activation, and never records a
	// second settlement fact, job or grant.
	resp, body = epayNotifyCall(t, ts, valid)
	if resp.StatusCode != http.StatusOK || string(body) != "fail" {
		t.Fatalf("replay response = %d %q, want 200 fail", resp.StatusCode, body)
	}
	replayed, _ := st.Order(order.ID)
	if replayed.State != model.OrderPaid || replayed.Grant != nil || len(st.Leases()) != leasesBefore {
		t.Fatalf("replay = %+v, leases=%d", replayed, len(st.Leases()))
	}
	if replayed.Fulfilment == nil || replayed.Fulfilment.UpdatedAt != queuedAt ||
		replayed.Fulfilment.Attempts != 0 || replayed.Fulfilment.State != model.FulfilmentQueued {
		t.Fatalf("replay rewrote the job: %+v", replayed.Fulfilment)
	}
	if got := len(st.PaymentEvents()); got != 1 {
		t.Fatalf("payment events = %d, want one settlement fact", got)
	}
	requirePortalQuota(t, st, tenant.ID, quotaBefore)
}

// TestEpayNotifyDuringPublishingStillAnswersFail proves the ack follows the
// final service state, not the queue: a duplicate callback while an attempt is
// in flight may not claim success, may not start a second job, and may not
// rewrite the in-flight attempt.
func TestEpayNotifyDuringPublishingStillAnswersFail(t *testing.T) {
	ts, st, _ := newEpayAPITestServer(t, Config{})
	plan := createOrderPlan(t, ts, true, 3000)
	tenant, cookie, csrf := activePortalTenant(t, ts, st, "epay-publishing@example.com", "u-01")
	order, _ := createPortalOrder(t, ts, cookie, csrf, plan.ID, "epay-publishing")
	resp, _ := epayCheckout(t, ts, order.ID, cookie, csrf, "alipay")
	requireStatus(t, resp, http.StatusOK)
	quotaBefore := portalUserQuota(t, st, tenant.ID)

	valid := epayNotifyForm(t, epayAPITestKey, order, "trade-publishing", "30.00")
	resp, body := epayNotifyCall(t, ts, valid)
	if resp.StatusCode != http.StatusOK || string(body) != "fail" {
		t.Fatalf("capacity notify response = %d %q", resp.StatusCode, body)
	}
	// Begin reserves the target, so the fixture must have a free slot before
	// the test can model an in-flight publication.
	freeFixtureCapacity(t, st)
	attempt, err := st.BeginFulfilment(order.ID)
	if err != nil {
		t.Fatalf("BeginFulfilment: %v", err)
	}
	if attempt.Number != 1 {
		t.Fatalf("attempt = %+v", attempt)
	}

	resp, body = epayNotifyCall(t, ts, valid)
	if resp.StatusCode != http.StatusOK || string(body) != "fail" {
		t.Fatalf("publishing notify response = %d %q, want 200 fail", resp.StatusCode, body)
	}
	current, _ := st.Order(order.ID)
	if current.State != model.OrderPaid || current.Grant != nil {
		t.Fatalf("publishing replay = %+v, want paid without a grant", current)
	}
	job := current.Fulfilment
	if job == nil || job.State != model.FulfilmentPublishing || job.Attempts != 1 ||
		job.AttemptLeaseID != attempt.LeaseID {
		t.Fatalf("in-flight job = %+v, want the untouched publishing attempt", job)
	}
	requirePortalQuota(t, st, tenant.ID, quotaBefore)
}

// TestEpayNotifyLateSettlementOnTerminalOrderAnswersFail proves a valid
// callback for an order that will never resume records the fact for review,
// answers fail and never attaches fulfilment work.
func TestEpayNotifyLateSettlementOnTerminalOrderAnswersFail(t *testing.T) {
	ts, st, _ := newEpayAPITestServer(t, Config{})
	plan := createOrderPlan(t, ts, true, 3000)
	tenant, cookie, csrf := activePortalTenant(t, ts, st, "epay-late@example.com", "u-01")
	order, _ := createPortalOrder(t, ts, cookie, csrf, plan.ID, "epay-late")
	resp, _ := epayCheckout(t, ts, order.ID, cookie, csrf, "alipay")
	requireStatus(t, resp, http.StatusOK)
	quotaBefore := portalUserQuota(t, st, tenant.ID)
	resp, body := portalOrderCall(t, ts, http.MethodPost, "/v1/portal/orders/"+order.ID+"/cancel",
		nil, cookie, csrf, nil)
	requireStatus(t, resp, http.StatusOK)

	valid := epayNotifyForm(t, epayAPITestKey, order, "trade-late", "30.00")
	resp, body = epayNotifyCall(t, ts, valid)
	if resp.StatusCode != http.StatusOK || string(body) != "fail" {
		t.Fatalf("late notify response = %d %q, want 200 fail", resp.StatusCode, body)
	}
	current, _ := st.Order(order.ID)
	if current.State != model.OrderCancelled || !current.ReviewRequired || current.Grant != nil {
		t.Fatalf("late settlement = %+v, want cancelled with review and no grant", current)
	}
	if current.Fulfilment != nil {
		t.Fatalf("terminal order was enqueued: %+v", current.Fulfilment)
	}

	// A replay is a no-op with the same honest answer.
	resp, body = epayNotifyCall(t, ts, valid)
	if resp.StatusCode != http.StatusOK || string(body) != "fail" {
		t.Fatalf("late replay response = %d %q, want 200 fail", resp.StatusCode, body)
	}
	if replayed, _ := st.Order(order.ID); replayed.Fulfilment != nil {
		t.Fatalf("late replay enqueued work: %+v", replayed.Fulfilment)
	}
	requirePortalQuota(t, st, tenant.ID, quotaBefore)
}

// TestEpayNotifyPaidReviewOrderAnswersFail proves a paid order quarantined for
// review is never acknowledged as serving and never receives fulfilment work:
// the review is an operator decision, not something a callback can bypass.
func TestEpayNotifyPaidReviewOrderAnswersFail(t *testing.T) {
	ts, st, _ := newEpayAPITestServer(t, Config{})
	plan := createOrderPlan(t, ts, true, 3000)
	tenant, cookie, csrf := activePortalTenant(t, ts, st, "epay-paid-review@example.com", "u-01")
	order, _ := createPortalOrder(t, ts, cookie, csrf, plan.ID, "epay-paid-review")
	resp, _ := epayCheckout(t, ts, order.ID, cookie, csrf, "alipay")
	requireStatus(t, resp, http.StatusOK)
	quotaBefore := portalUserQuota(t, st, tenant.ID)

	// Record the settlement directly so the callback path below is the first
	// thing that could attach work. No capacity means the order stays paid.
	settle := func(eventID string) {
		t.Helper()
		if _, err := st.ApplyPaymentEvent(model.ProviderEpay, model.PaymentEvent{
			Provider: model.ProviderEpay, ID: eventID, Kind: model.PaymentSettled,
			OrderID: order.ID, ProviderRef: eventID,
			AmountMinor: plan.AmountMinor, Currency: plan.Currency,
		}, "system"); err != nil {
			t.Fatalf("ApplyPaymentEvent(%s): %v", eventID, err)
		}
	}
	settle("trade-paid-review-a")
	// A conflicting settlement reference is review-only and keeps the order
	// paid, so the callback must not be able to fulfil it.
	settle("trade-paid-review-b")
	reviewed, _ := st.Order(order.ID)
	if reviewed.State != model.OrderPaid || !reviewed.ReviewRequired || reviewed.Grant != nil {
		t.Fatalf("reviewed order = %+v, want paid with review and no grant", reviewed)
	}

	valid := epayNotifyForm(t, epayAPITestKey, order, "trade-paid-review-c", "30.00")
	resp, body := epayNotifyCall(t, ts, valid)
	if resp.StatusCode != http.StatusOK || string(body) != "fail" {
		t.Fatalf("reviewed notify response = %d %q, want 200 fail", resp.StatusCode, body)
	}
	current, _ := st.Order(order.ID)
	if current.State != model.OrderPaid || !current.ReviewRequired || current.Grant != nil {
		t.Fatalf("reviewed notify = %+v, want paid with review and no grant", current)
	}
	if current.Fulfilment != nil {
		t.Fatalf("review-required order was enqueued: %+v", current.Fulfilment)
	}
	requirePortalQuota(t, st, tenant.ID, quotaBefore)
}

// TestEpayNotifyPaidDeadLetterAnswersFail proves a dead-lettered job is not
// replaced, reset or retried by a callback, and that the order stays paid with
// no grant and no quota change.
func TestEpayNotifyPaidDeadLetterAnswersFail(t *testing.T) {
	ts, st, _ := newEpayAPITestServer(t, Config{})
	plan := createOrderPlan(t, ts, true, 3000)
	tenant, cookie, csrf := activePortalTenant(t, ts, st, "epay-dead-letter@example.com", "u-01")
	order, _ := createPortalOrder(t, ts, cookie, csrf, plan.ID, "epay-dead-letter")
	resp, _ := epayCheckout(t, ts, order.ID, cookie, csrf, "alipay")
	requireStatus(t, resp, http.StatusOK)
	quotaBefore := portalUserQuota(t, st, tenant.ID)

	valid := epayNotifyForm(t, epayAPITestKey, order, "trade-dead-letter", "30.00")
	resp, body := epayNotifyCall(t, ts, valid)
	if resp.StatusCode != http.StatusOK || string(body) != "fail" {
		t.Fatalf("capacity notify response = %d %q", resp.StatusCode, body)
	}
	// Begin reserves the target, so free the fixture slot before modelling the
	// in-flight attempt that dead-letters.
	freeFixtureCapacity(t, st)
	attempt, err := st.BeginFulfilment(order.ID)
	if err != nil {
		t.Fatalf("BeginFulfilment: %v", err)
	}
	if _, err := st.AbortFulfilment(order.ID, attempt.LeaseID,
		model.FulfilmentReasonPublishFailed, "publication refused", false); err != nil {
		t.Fatalf("AbortFulfilment: %v", err)
	}
	dead, _ := st.Order(order.ID)
	if dead.Fulfilment == nil || dead.Fulfilment.State != model.FulfilmentDeadLetter ||
		!dead.Fulfilment.Blocked || dead.Fulfilment.Attempts != 1 {
		t.Fatalf("dead-letter job = %+v", dead.Fulfilment)
	}
	deadAt := dead.Fulfilment.UpdatedAt

	resp, body = epayNotifyCall(t, ts, valid)
	if resp.StatusCode != http.StatusOK || string(body) != "fail" {
		t.Fatalf("dead-letter notify response = %d %q, want 200 fail", resp.StatusCode, body)
	}
	current, _ := st.Order(order.ID)
	if current.State != model.OrderPaid || current.Grant != nil {
		t.Fatalf("dead-letter replay = %+v, want paid without a grant", current)
	}
	if current.Fulfilment == nil || current.Fulfilment.State != model.FulfilmentDeadLetter ||
		current.Fulfilment.Attempts != 1 || current.Fulfilment.UpdatedAt != deadAt {
		t.Fatalf("dead-letter job was rewritten: %+v", current.Fulfilment)
	}
	requirePortalQuota(t, st, tenant.ID, quotaBefore)
}

// TestEpayNotifyActiveWithoutLeaseAnswersFail proves an active order whose
// grant lease has ended is not serving: a duplicate callback answers fail,
// attaches no work and leaves the recorded quota alone.
func TestEpayNotifyActiveWithoutLeaseAnswersFail(t *testing.T) {
	ts, st, _ := newEpayAPITestServer(t, Config{})
	plan := createOrderPlan(t, ts, true, 3000)
	tenant, cookie, csrf := activePortalTenant(t, ts, st, "epay-unserved@example.com", "u-01")
	freeFixtureCapacity(t, st)
	order, _ := createPortalOrder(t, ts, cookie, csrf, plan.ID, "epay-unserved")
	resp, _ := epayCheckout(t, ts, order.ID, cookie, csrf, "alipay")
	requireStatus(t, resp, http.StatusOK)

	valid := epayNotifyForm(t, epayAPITestKey, order, "trade-unserved", "30.00")
	resp, body := epayNotifyCall(t, ts, valid)
	if resp.StatusCode != http.StatusOK || string(body) != "success" {
		t.Fatalf("serving notify response = %d %q, want 200 success", resp.StatusCode, body)
	}
	active, _ := st.Order(order.ID)
	if active.State != model.OrderActive || active.Grant == nil {
		t.Fatalf("activated order = %+v", active)
	}
	grantedQuota := portalUserQuota(t, st, tenant.ID)
	if grantedQuota == nil || *grantedQuota != plan.QuotaBytes {
		t.Fatalf("granted quota = %v, want %d", grantedQuota, plan.QuotaBytes)
	}
	if err := st.ReleaseLease(active.Grant.LeaseID); err != nil {
		t.Fatalf("ReleaseLease: %v", err)
	}

	resp, body = epayNotifyCall(t, ts, valid)
	if resp.StatusCode != http.StatusOK || string(body) != "fail" {
		t.Fatalf("unserved notify response = %d %q, want 200 fail", resp.StatusCode, body)
	}
	current, _ := st.Order(order.ID)
	if current.State != model.OrderActive || current.Grant == nil ||
		current.Grant.RevokedAt != nil || current.Fulfilment != nil {
		t.Fatalf("unserved order = %+v, want active with an unrevoked grant and no job", current)
	}
	requirePortalQuota(t, st, tenant.ID, grantedQuota)
}

// TestEpayNotifyUnsupportedCurrencyAnswersFail proves a callback for an order
// whose snapshot is not CNY is rejected before any money is recorded, even when
// the checkout marker was set out of band.
func TestEpayNotifyUnsupportedCurrencyAnswersFail(t *testing.T) {
	ts, st, _ := newEpayAPITestServer(t, Config{})
	resp, body := apiCall(t, ts, http.MethodPost, "/v1/admin/plans", model.PlanInput{
		Name: "USD monthly", AmountMinor: 3000, Currency: "USD",
		LeaseTTLSeconds: 3600, QuotaBytes: 5 << 30,
	})
	requireStatus(t, resp, http.StatusCreated)
	plan := decodeBody[model.Plan](t, body)
	tenant, cookie, csrf := activePortalTenant(t, ts, st, "epay-usd-notify@example.com", "u-01")
	order, _ := createPortalOrder(t, ts, cookie, csrf, plan.ID, "epay-usd-notify")
	if _, err := st.MarkOrderCheckout(tenant.ID, order.ID, model.ProviderEpay); err != nil {
		t.Fatalf("MarkOrderCheckout: %v", err)
	}
	quotaBefore := portalUserQuota(t, st, tenant.ID)

	form := epayNotifyForm(t, epayAPITestKey, order, "trade-usd-notify", "30.00")
	resp, body = epayNotifyCall(t, ts, form)
	if resp.StatusCode != http.StatusOK || string(body) != "fail" {
		t.Fatalf("unsupported currency response = %d %q, want 200 fail", resp.StatusCode, body)
	}
	current, _ := st.Order(order.ID)
	if current.State != model.OrderAwaitingPayment || current.Grant != nil ||
		current.Fulfilment != nil || current.ReviewRequired {
		t.Fatalf("unsupported currency order = %+v, want untouched awaiting", current)
	}
	if got := len(st.PaymentEvents()); got != 0 {
		t.Fatalf("payment events = %d, want none", got)
	}
	requirePortalQuota(t, st, tenant.ID, quotaBefore)
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

// TestEpayReconcileSettlesPaidUnfulfilledOrder proves the reconciler's store
// selection includes a paid order that never received an entitlement, not only
// awaiting_payment rows: once capacity appears, the next query pass activates
// the order without recording a second payment fact.
func TestEpayReconcileSettlesPaidUnfulfilledOrder(t *testing.T) {
	ts, st, _ := newEpayAPITestServer(t, Config{})
	plan := createOrderPlan(t, ts, true, 3000)
	tenant, cookie, csrf := activePortalTenant(t, ts, st, "epay-paid-reconcile@example.com", "u-01")
	order, _ := createPortalOrder(t, ts, cookie, csrf, plan.ID, "epay-paid-reconcile")
	resp, _ := epayCheckout(t, ts, order.ID, cookie, csrf, "alipay")
	requireStatus(t, resp, http.StatusOK)
	quotaBefore := portalUserQuota(t, st, tenant.ID)
	leasesBefore := len(st.Leases())

	// No capacity: the callback records the money, queues the initial job and
	// honestly answers fail. Neither the grant nor the quota may move yet.
	valid := epayNotifyForm(t, epayAPITestKey, order, "trade-paid-reconcile", "30.00")
	resp, body := epayNotifyCall(t, ts, valid)
	if resp.StatusCode != http.StatusOK || string(body) != "fail" {
		t.Fatalf("capacity notify response = %d %q", resp.StatusCode, body)
	}
	paid, _ := st.Order(order.ID)
	if paid.State != model.OrderPaid || paid.Fulfilment == nil || paid.Grant != nil {
		t.Fatalf("capacity settlement = %+v, want paid with a queued job", paid)
	}
	if len(st.Leases()) != leasesBefore {
		t.Fatalf("paid settlement minted a lease: %d -> %d", leasesBefore, len(st.Leases()))
	}
	requirePortalQuota(t, st, tenant.ID, quotaBefore)

	freeFixtureCapacity(t, st)
	leasesFree := len(st.Leases())
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("act") != "order" || r.URL.Query().Get("out_trade_no") != order.ID {
			t.Errorf("unexpected query: %v", r.URL.Query())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":1,"trade_no":"trade-paid-reconcile","out_trade_no":"` + order.ID + `","money":"30.00","trade_status":"TRADE_SUCCESS"}`))
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

	active, ok := st.Order(order.ID)
	if !ok || active.State != model.OrderActive || active.Grant == nil {
		t.Fatalf("reconciled order = %+v, ok=%t, want active with a grant", active, ok)
	}
	if active.Fulfilment != nil {
		t.Fatalf("serving order kept a fulfilment job: %+v", active.Fulfilment)
	}
	// Activation is the only step allowed to move the quota, and it does so
	// exactly once with exactly one lease.
	if len(st.Leases()) != leasesFree+1 {
		t.Fatalf("leases = %d, want %d", len(st.Leases()), leasesFree+1)
	}
	if active.Grant.QuotaBytes != plan.QuotaBytes {
		t.Fatalf("grant quota = %d, want %d", active.Grant.QuotaBytes, plan.QuotaBytes)
	}
	granted := portalUserQuota(t, st, tenant.ID)
	if granted == nil || *granted != plan.QuotaBytes {
		t.Fatalf("granted quota = %v, want %d", granted, plan.QuotaBytes)
	}
	// The grant records the pre-settlement quota as the value it replaced, so
	// the quota moved exactly once, from that value to the plan quota.
	switch {
	case quotaBefore == nil && active.Grant.PreviousQuotaBytes != nil:
		t.Fatalf("previous quota = %d, want nil", *active.Grant.PreviousQuotaBytes)
	case quotaBefore != nil && (active.Grant.PreviousQuotaBytes == nil ||
		*active.Grant.PreviousQuotaBytes != *quotaBefore):
		t.Fatalf("previous quota = %v, want %d", active.Grant.PreviousQuotaBytes, *quotaBefore)
	}
	if got := len(st.PaymentEvents()); got != 1 {
		t.Fatalf("payment events = %d, want one settlement fact", got)
	}

	// Now that the order serves, the duplicate callback is acknowledged.
	resp, body = epayNotifyCall(t, ts, valid)
	if resp.StatusCode != http.StatusOK || string(body) != "success" {
		t.Fatalf("post-activation notify response = %d %q, want 200 success", resp.StatusCode, body)
	}
}

// epayFulfilPublisher is the publication seam for the notify tests: it records
// every reserved lease and optionally fails the publication.
type epayFulfilPublisher struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (p *epayFulfilPublisher) PublishReservedLease(context.Context, model.Lease) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return p.err
}

func (p *epayFulfilPublisher) WithdrawReservedLease(context.Context, model.Lease) error {
	return nil
}

func (p *epayFulfilPublisher) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// releasingFulfiller models the blocker clearing between settlement and the
// callback's immediate attempt. The seeded account holds its only slot while
// the money is recorded, so the store cannot activate the order; the
// fulfilment attempt then releases that seeded lease and publishes the
// reservation through the real service.
type releasingFulfiller struct {
	st  *store.Store
	svc *fulfilment.Service
}

func (f *releasingFulfiller) FulfilNow(ctx context.Context, orderID string) (model.Order, error) {
	for _, lease := range f.st.Leases() {
		if lease.AccountID != "acc-01" {
			continue
		}
		if err := f.st.ReleaseLease(lease.ID); err != nil {
			return model.Order{}, err
		}
	}
	return f.svc.FulfilNow(ctx, orderID)
}

// The metrics endpoint asserts this interface on Config.Fulfiller; the test
// decorator delegates to the real worker so the counters are observable.
func (f *releasingFulfiller) FulfilmentAttempts() map[string]map[string]int {
	return f.svc.FulfilmentAttempts()
}

func (f *releasingFulfiller) FulfilmentDeadLetters() int {
	return f.svc.FulfilmentDeadLetters()
}

// newEpayNotifyConfig is the shared configuration of the fulfilment notify
// tests: a fully configured epay adapter, the tenant portal and one event sink.
func newEpayNotifyConfig(st *store.Store, sink *recordingSink, fulfiller Fulfiller) Config {
	return Config{
		AdminToken:     testToken,
		TenantPortal:   true,
		TenantOrigin:   portalTestOrigin,
		ManualPayments: payment.NewManual(time.Now),
		Epay: payment.NewEpay(payment.EpayConfig{
			Gateway:  "https://pay.example.test/",
			PID:      epayAPITestPID,
			Key:      epayAPITestKey,
			SignType: "MD5",
			Now:      func() time.Time { return time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC) },
		}),
		Events:    sink,
		Fulfiller: fulfiller,
	}
}

// newEpayFulfilmentServer wires the real fulfilment service over a fake
// publisher, so the notify tests exercise the callback, the store gate and the
// publication seam together.
func newEpayFulfilmentServer(t *testing.T, publisher *epayFulfilPublisher) (*httptest.Server, *store.Store, *recordingSink, *Server) {
	t.Helper()
	st := store.New()
	sink := &recordingSink{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	// Mirror main: the server exists first, the worker publishes and emits
	// through it, and SetFulfiller installs the worker before serving.
	srv := New(newEpayNotifyConfig(st, sink, nil), st, logger)
	svc := fulfilment.New(fulfilment.Config{
		Store: st, Publisher: publisher, Events: srv, Logger: logger,
	})
	srv.SetFulfiller(&releasingFulfiller{st: st, svc: svc})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts, st, sink, srv
}

// TestEpayNotifyFulfilsImmediatelyAndAcksSuccess proves the callback attempts
// the queued job and answers success only after the order is actually serving.
func TestEpayNotifyFulfilsImmediatelyAndAcksSuccess(t *testing.T) {
	publisher := &epayFulfilPublisher{}
	ts, st, sink, _ := newEpayFulfilmentServer(t, publisher)
	plan := createOrderPlan(t, ts, true, 3000)
	_, cookie, csrf := activePortalTenant(t, ts, st, "epay-immediate@example.com", "u-01")
	order, _ := createPortalOrder(t, ts, cookie, csrf, plan.ID, "epay-immediate")
	resp, _ := epayCheckout(t, ts, order.ID, cookie, csrf, "alipay")
	requireStatus(t, resp, http.StatusOK)

	valid := epayNotifyForm(t, epayAPITestKey, order, "trade-immediate", "30.00")
	resp, body := epayNotifyCall(t, ts, valid)
	if resp.StatusCode != http.StatusOK || string(body) != "success" {
		t.Fatalf("notify response = %d %q, want 200 success", resp.StatusCode, body)
	}
	current, ok := st.Order(order.ID)
	if !ok || current.State != model.OrderActive || current.Grant == nil || current.Fulfilment != nil {
		t.Fatalf("order after notify = %+v, ok=%t, want active with a grant and no job", current, ok)
	}
	if publisher.count() != 1 {
		t.Fatalf("publications = %d, want exactly one", publisher.count())
	}
	lease, ok := st.Lease(current.Grant.LeaseID)
	if !ok || lease.Status != model.LeaseActive || lease.UserID != current.UserID {
		t.Fatalf("grant lease = %+v, ok=%t, want the one active reservation", lease, ok)
	}
	for _, candidate := range st.Leases() {
		if candidate.Status == model.LeasePendingPublication {
			t.Fatalf("committed order left a provisional reservation: %+v", candidate)
		}
	}
	if grants := st.Grants(); len(grants) != 1 || grants[0].OrderID != order.ID {
		t.Fatalf("grants = %+v, want exactly one for %s", grants, order.ID)
	}
	if got := len(st.PaymentEvents()); got != 1 {
		t.Fatalf("payment events = %d, want one settlement fact", got)
	}
	if sink.count(webhook.EventOrderActivated) != 1 || sink.count(webhook.EventLeaseCreated) != 1 {
		t.Fatalf("events = %v, want one order.activated and one lease.created", sink.types())
	}

	// A duplicate callback is acknowledged from the serving state and emits no
	// second activation or lease event.
	resp, body = epayNotifyCall(t, ts, valid)
	if resp.StatusCode != http.StatusOK || string(body) != "success" {
		t.Fatalf("duplicate notify response = %d %q, want 200 success", resp.StatusCode, body)
	}
	if sink.count(webhook.EventOrderActivated) != 1 || sink.count(webhook.EventLeaseCreated) != 1 {
		t.Fatalf("duplicate notify emitted events: %v", sink.types())
	}
}

// TestEpayNotifyFulfilFailureLeavesJobQueued proves a failed immediate attempt
// keeps the payment, leaves the job queued with a retry, grants nothing and
// answers the gateway fail.
func TestEpayNotifyFulfilFailureLeavesJobQueued(t *testing.T) {
	publisher := &epayFulfilPublisher{err: errors.New("resin unavailable")}
	ts, st, sink, srv := newEpayFulfilmentServer(t, publisher)
	plan := createOrderPlan(t, ts, true, 3000)
	_, cookie, csrf := activePortalTenant(t, ts, st, "epay-immediate-fail@example.com", "u-01")
	order, _ := createPortalOrder(t, ts, cookie, csrf, plan.ID, "epay-immediate-fail")
	resp, _ := epayCheckout(t, ts, order.ID, cookie, csrf, "alipay")
	requireStatus(t, resp, http.StatusOK)
	before := time.Now()

	valid := epayNotifyForm(t, epayAPITestKey, order, "trade-immediate-fail", "30.00")
	resp, body := epayNotifyCall(t, ts, valid)
	if resp.StatusCode != http.StatusOK || string(body) != "fail" {
		t.Fatalf("notify response = %d %q, want 200 fail", resp.StatusCode, body)
	}
	current, ok := st.Order(order.ID)
	if !ok || current.State != model.OrderPaid || current.Grant != nil {
		t.Fatalf("order after failed fulfil = %+v, ok=%t, want paid without a grant", current, ok)
	}
	job := current.Fulfilment
	if job == nil || job.State != model.FulfilmentQueued || job.Attempts != 1 ||
		job.LastReason != model.FulfilmentReasonPublishFailed || job.NextAttemptAt == nil {
		t.Fatalf("job = %+v, want one queued failed attempt", job)
	}
	next, err := time.Parse(time.RFC3339Nano, *job.NextAttemptAt)
	if err != nil || !next.After(before) {
		t.Fatalf("nextAttemptAt = %q, want a future retry", *job.NextAttemptAt)
	}
	for _, candidate := range st.Leases() {
		if candidate.Status == model.LeasePendingPublication {
			t.Fatalf("failed attempt left a provisional reservation: %+v", candidate)
		}
	}
	if grants := st.Grants(); len(grants) != 0 {
		t.Fatalf("failed attempt granted %+v", grants)
	}
	if publisher.count() != 1 {
		t.Fatalf("publications = %d, want exactly one", publisher.count())
	}
	if got := len(st.PaymentEvents()); got != 1 {
		t.Fatalf("payment events = %d, want one settlement fact", got)
	}
	if sink.count(webhook.EventOrderActivated) != 0 || sink.count(webhook.EventLeaseCreated) != 0 {
		t.Fatalf("failed attempt emitted service events: %v", sink.types())
	}
	var metrics strings.Builder
	srv.writeFulfilmentMetrics(&metrics)
	for _, want := range []string{
		`ajs_order_fulfilment_jobs{state="queued",reason="publish_failed"} 1`,
		`ajs_order_fulfilment_attempts_total{result="publish_failed",reason="publish_failed"} 1`,
		"ajs_order_fulfilment_dead_letters_total 0",
		"ajs_order_paid_unfulfilled_seconds ",
	} {
		if !strings.Contains(metrics.String(), want) {
			t.Fatalf("metrics missing %q:\n%s", want, metrics.String())
		}
	}
}

// recordingFulfiller captures the context the notify path passes and fails
// immediately, so the timeout wiring is observable without waiting ten seconds.
type recordingFulfiller struct {
	mu       sync.Mutex
	calls    int
	deadline time.Time
	alive    bool
}

func (f *recordingFulfiller) FulfilNow(ctx context.Context, _ string) (model.Order, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.deadline, _ = ctx.Deadline()
	f.alive = ctx.Err() == nil
	return model.Order{}, context.DeadlineExceeded
}

// TestEpayNotifyFulfilTimeoutUsesDetachedDeadline proves the callback attempts
// the job with its own ~10s deadline, not the request context, and answers fail
// while the job stays queued.
func TestEpayNotifyFulfilTimeoutUsesDetachedDeadline(t *testing.T) {
	st := store.New()
	sink := &recordingSink{}
	fulfiller := &recordingFulfiller{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ts := httptest.NewServer(New(newEpayNotifyConfig(st, sink, fulfiller), st, logger))
	t.Cleanup(ts.Close)
	plan := createOrderPlan(t, ts, true, 3000)
	_, cookie, csrf := activePortalTenant(t, ts, st, "epay-timeout@example.com", "u-01")
	order, _ := createPortalOrder(t, ts, cookie, csrf, plan.ID, "epay-timeout")
	resp, _ := epayCheckout(t, ts, order.ID, cookie, csrf, "alipay")
	requireStatus(t, resp, http.StatusOK)

	valid := epayNotifyForm(t, epayAPITestKey, order, "trade-timeout", "30.00")
	resp, body := epayNotifyCall(t, ts, valid)
	if resp.StatusCode != http.StatusOK || string(body) != "fail" {
		t.Fatalf("notify response = %d %q, want 200 fail", resp.StatusCode, body)
	}
	fulfiller.mu.Lock()
	calls, deadline, alive := fulfiller.calls, fulfiller.deadline, fulfiller.alive
	fulfiller.mu.Unlock()
	if calls != 1 {
		t.Fatalf("fulfilment calls = %d, want 1", calls)
	}
	if !alive {
		t.Fatal("the immediate attempt ran on a cancelled request context")
	}
	remaining := time.Until(deadline)
	if remaining < 8*time.Second || remaining > 11*time.Second {
		t.Fatalf("attempt deadline = %s away, want the bounded ~10s context", remaining)
	}
	current, ok := st.Order(order.ID)
	if !ok || current.State != model.OrderPaid || current.Grant != nil {
		t.Fatalf("order after timeout = %+v, ok=%t, want paid without a grant", current, ok)
	}
	if job := current.Fulfilment; job == nil || job.State != model.FulfilmentQueued ||
		job.Attempts != 0 || job.NextAttemptAt == nil {
		t.Fatalf("job = %+v, want the queued job left for the worker", current.Fulfilment)
	}
}

// seedPaidQueuedOrder frees the seeded account slot and creates one paid order
// for u-01 with a queued initial job. The settlement happens while the user is
// suspended, so the order is paid without a grant — the state an initial
// fulfilment job starts from.
func seedPaidQueuedOrder(t *testing.T, st *store.Store) string {
	t.Helper()
	freeFixtureCapacity(t, st)
	if err := st.SignupTenant(model.TenantSignupInput{
		Email: "fulfilment-seed@example.com", Password: portalTestPassword, DisplayName: "Fulfilment Seed",
	}); err != nil {
		t.Fatalf("SignupTenant: %v", err)
	}
	var tenant model.Tenant
	for _, candidate := range st.Tenants() {
		if candidate.Email == "fulfilment-seed@example.com" {
			tenant = candidate
		}
	}
	if tenant.ID == "" {
		t.Fatal("signup did not store a tenant")
	}
	if _, err := st.ApproveTenant(tenant.ID, "u-01", nil); err != nil {
		t.Fatalf("ApproveTenant: %v", err)
	}
	plan, err := st.CreatePlan(model.PlanInput{
		Name: "Monthly 5 GiB", AmountMinor: 3000, Currency: "CNY",
		LeaseTTLSeconds: 3600, QuotaBytes: 5 << 30,
	})
	if err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	order, _, err := st.CreateOrder(tenant.ID, "u-01", plan.ID, "fulfilment-seed", "test")
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	if _, err := st.SetUserEnabled("u-01", false); err != nil {
		t.Fatalf("suspend user: %v", err)
	}
	paid, err := st.ApplyPaymentEvent(model.ProviderManual, model.PaymentEvent{
		Provider: model.ProviderManual, ID: "evt-fulfilment-seed", Kind: model.PaymentSettled,
		OrderID: order.ID, ProviderRef: "receipt-fulfilment-seed",
	}, "test")
	if err != nil {
		t.Fatalf("ApplyPaymentEvent: %v", err)
	}
	if _, err := st.SetUserEnabled("u-01", true); err != nil {
		t.Fatalf("resume user: %v", err)
	}
	if paid.State != model.OrderPaid || paid.Grant != nil {
		t.Fatalf("settled order = %+v, want paid without a grant", paid)
	}
	if _, err := st.EnqueueFulfilment(order.ID, model.FulfilmentInitial); err != nil {
		t.Fatalf("EnqueueFulfilment: %v", err)
	}
	return order.ID
}

// TestPublishReservedLeaseMatrix pins the idempotent publish-or-inspect
// contract: a pending reservation is published once, withdrawal reaches both
// the Panel and the hook, an active row is reported published without another
// panel or hook call, and a missing row is a conflict.
func TestPublishReservedLeaseMatrix(t *testing.T) {
	st := store.New()
	panel := &leasePanel{}
	hookCalls := 0
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(Config{
		Panel: panel,
		LeasePublishHook: func(context.Context, LeaseAction, model.Lease) error {
			hookCalls++
			return nil
		},
	}, st, logger)

	orderID := seedPaidQueuedOrder(t, st)
	attempt, err := st.BeginFulfilment(orderID)
	if err != nil {
		t.Fatalf("BeginFulfilment: %v", err)
	}
	if _, err := st.MarkFulfilmentPublished(orderID, attempt.LeaseID); err != nil {
		t.Fatalf("MarkFulfilmentPublished: %v", err)
	}

	if err := srv.PublishReservedLease(context.Background(), attempt.Lease); err != nil {
		t.Fatalf("publish pending reservation: %v", err)
	}
	if panel.publishes != 1 || hookCalls != 1 {
		t.Fatalf("pending publish = %d panel, %d hook; want one each", panel.publishes, hookCalls)
	}
	// The withdrawal must reach the configured Panel and the hook; the row is
	// released by the worker's completion, not by this inspection call.
	if err := srv.WithdrawReservedLease(context.Background(), attempt.Lease); err != nil {
		t.Fatalf("withdraw pending reservation: %v", err)
	}
	if panel.withdrawals != 1 || hookCalls != 2 {
		t.Fatalf("pending withdrawal = %d panel, %d hook; want one each",
			panel.withdrawals, hookCalls)
	}
	if _, err := st.CommitFulfilment(orderID, attempt.LeaseID); err != nil {
		t.Fatalf("CommitFulfilment: %v", err)
	}
	active, ok := st.Lease(attempt.LeaseID)
	if !ok || active.Status != model.LeaseActive {
		t.Fatalf("committed lease = %+v, ok=%t", active, ok)
	}
	if err := srv.PublishReservedLease(context.Background(), active); err != nil {
		t.Fatalf("publish active lease: %v", err)
	}
	if panel.publishes != 1 || hookCalls != 2 {
		t.Fatalf("active row republished: %d panel, %d hook", panel.publishes, hookCalls)
	}
	if err := srv.PublishReservedLease(context.Background(), model.Lease{ID: "lease-missing"}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("missing row error = %v, want ErrConflict", err)
	}
	if panel.publishes != 1 || hookCalls != 2 {
		t.Fatalf("missing row reached the publisher: %d panel, %d hook", panel.publishes, hookCalls)
	}
	// Withdrawal must never remove a serving lease.
	if err := srv.WithdrawReservedLease(context.Background(), active); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("withdraw active lease error = %v, want ErrConflict", err)
	}
	// A terminal transition can drop the row before the marker; the caller
	// still holds the attempt lease identity, so the panel must be reached from
	// the passed account even when no row remains.
	before := panel.withdrawals
	if err := srv.WithdrawReservedLease(context.Background(), model.Lease{
		ID: "lease-orphan", AccountID: "acc-orphan",
	}); err != nil {
		t.Fatalf("withdraw orphaned publication: %v", err)
	}
	if panel.withdrawals != before+1 {
		t.Fatalf("orphaned withdrawal panel calls = %d, want %d", panel.withdrawals, before+1)
	}
}

// TestFulfilmentDeadLetterWithdrawsThroughPanel proves the production wiring:
// the worker publishes and withdraws through the same server, and a
// published-then-dead-lettered job reaches the configured Panel and the hook.
func TestFulfilmentDeadLetterWithdrawsThroughPanel(t *testing.T) {
	st := store.New()
	panel := &leasePanel{}
	revokes := 0
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(Config{
		Panel: panel,
		LeasePublishHook: func(_ context.Context, action LeaseAction, _ model.Lease) error {
			switch action {
			case LeaseActionCreated:
				// Force every commit to lose its race after publication.
				_, err := st.SetUserEnabled("u-01", false)
				return err
			case LeaseActionRevoked:
				revokes++
			}
			return nil
		},
	}, st, logger)
	svc := fulfilment.New(fulfilment.Config{
		Store: st, Publisher: srv, Events: srv, Logger: logger,
	})
	srv.SetFulfiller(svc)

	orderID := seedPaidQueuedOrder(t, st)
	for i := range store.MaxFulfilmentAttempts {
		if _, err := svc.FulfilNow(context.Background(), orderID); err == nil {
			t.Fatalf("attempt %d succeeded, want a commit refusal", i+1)
		}
	}
	order, ok := st.Order(orderID)
	if !ok || order.Fulfilment == nil || order.Fulfilment.State != model.FulfilmentDeadLetter {
		t.Fatalf("order after cleanup = %+v, ok=%t, want a dead letter", order, ok)
	}
	if panel.publishes != 1 {
		t.Fatalf("panel publishes = %d, want one (commit retries never republish)", panel.publishes)
	}
	if panel.withdrawals != 1 {
		t.Fatalf("panel withdrawals = %d, want 1", panel.withdrawals)
	}
	if revokes != 1 {
		t.Fatalf("hook revokes = %d, want 1", revokes)
	}
	for _, lease := range st.Leases() {
		if lease.Status == model.LeasePendingPublication {
			t.Fatalf("cleanup left a reservation: %+v", lease)
		}
		if lease.AccountID == "acc-01" {
			t.Fatalf("cleanup left an acc-01 lease: %+v", lease)
		}
	}
	if grants := st.Grants(); len(grants) != 0 {
		t.Fatalf("grants = %+v, want none", grants)
	}
	if got := svc.FulfilmentDeadLetters(); got != 1 {
		t.Fatalf("dead-letter counter = %d, want 1", got)
	}
}
