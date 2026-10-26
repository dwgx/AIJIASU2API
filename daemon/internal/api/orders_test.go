package api

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/payment"
	"ajsaccountd/internal/store"
	"ajsaccountd/internal/webhook"
)

const orderTestStripeSecret = "whsec_order_test"

func newOrderTestServer(t *testing.T, cfg Config) (*httptest.Server, *store.Store, *recordingSink) {
	t.Helper()
	st := store.New()
	sink := &recordingSink{}
	cfg.AdminToken = testToken
	cfg.TenantPortal = true
	cfg.TenantOrigin = portalTestOrigin
	if cfg.ManualPayments == nil {
		cfg.ManualPayments = payment.NewManual(time.Now)
	}
	cfg.Events = sink
	server := New(cfg, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)
	return ts, st, sink
}

// freeFixtureCapacity releases the seeded leases on the fixture account whose
// verified allowance is a single slot under the default AJS_ACCOUNT_SLOTS=1
// (the seed's vendor UseCount is two, but the operator cap is what admission
// applies). A test about settlement mechanics calls it so the order it settles
// can actually take that slot. A capacity-honesty test must leave the seed
// alone: there the refusal is the behavior under test.
func freeFixtureCapacity(t *testing.T, st *store.Store) {
	t.Helper()
	released := 0
	for _, lease := range st.Leases() {
		if lease.AccountID != "acc-01" {
			continue
		}
		if err := st.ReleaseLease(lease.ID); err != nil {
			t.Fatalf("release fixture lease %s: %v", lease.ID, err)
		}
		released++
	}
	if released == 0 {
		t.Fatal("fixture account acc-01 holds no seeded lease to release")
	}
}

func createOrderPlan(t *testing.T, ts *httptest.Server, active bool, amountMinor int64) model.Plan {
	t.Helper()
	in := model.PlanInput{
		Name: "Monthly 5 GiB", AmountMinor: amountMinor, Currency: "CNY",
		LeaseTTLSeconds: 3600, QuotaBytes: 5 << 30,
	}
	if !active {
		in.Active = new(false)
	}
	resp, body := apiCall(t, ts, http.MethodPost, "/v1/admin/plans", in)
	requireStatus(t, resp, http.StatusCreated)
	return decodeBody[model.Plan](t, body)
}

func portalOrderCall(t *testing.T, ts *httptest.Server, method, path string, body any, cookie, csrf string, headers map[string]string) (*http.Response, []byte) {
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
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	return doRequest(t, ts, req)
}

func createPortalOrder(t *testing.T, ts *httptest.Server, cookie, csrf, planID, idempotencyKey string) (model.Order, int) {
	t.Helper()
	headers := map[string]string{}
	if idempotencyKey != "" {
		headers["Idempotency-Key"] = idempotencyKey
	}
	resp, body := portalOrderCall(t, ts, http.MethodPost, "/v1/portal/orders",
		model.OrderCreateRequest{PlanID: planID}, cookie, csrf, headers)
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		t.Fatalf("create order: status %d body %s", resp.StatusCode, body)
	}
	return decodeBody[model.Order](t, body), resp.StatusCode
}

func settleOrderAPI(t *testing.T, ts *httptest.Server, orderID, reference string, attest bool) (*http.Response, []byte) {
	t.Helper()
	return apiCall(t, ts, http.MethodPost, "/v1/admin/orders/"+orderID+"/settle",
		model.OrderSettleRequest{Reference: reference, OfflineFundsReceived: attest})
}

func refundOrderAPI(t *testing.T, ts *httptest.Server, orderID, reference string) (*http.Response, []byte) {
	t.Helper()
	return apiCall(t, ts, http.MethodPost, "/v1/admin/orders/"+orderID+"/refund",
		model.OrderRefundRequest{Reference: reference, Reason: "offline refund"})
}

func chargebackOrderAPI(t *testing.T, ts *httptest.Server, orderID, reference string) (*http.Response, []byte) {
	t.Helper()
	return apiCall(t, ts, http.MethodPost, "/v1/admin/orders/"+orderID+"/chargeback",
		model.OrderChargebackRequest{Reference: reference, Reason: "bank dispute"})
}

func stripeTestHeader(secret string, body []byte, timestamp int64) http.Header {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.", timestamp)
	mac.Write(body)
	header := http.Header{}
	header.Set("Stripe-Signature", fmt.Sprintf("t=%d,v1=%s", timestamp, hex.EncodeToString(mac.Sum(nil))))
	header.Set("Content-Type", "application/json")
	return header
}

func stripeWebhookCall(t *testing.T, ts *httptest.Server, body []byte, timestamp int64, secret string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, ts.URL+stripeWebhookPath, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new webhook request: %v", err)
	}
	for key, values := range stripeTestHeader(secret, body, timestamp) {
		req.Header[key] = values
	}
	return doRequest(t, ts, req)
}

func stripeCheckoutPayload(eventID, orderID string, amountMinor int64) []byte {
	return []byte(fmt.Sprintf(
		`{"id":%q,"type":"checkout.session.completed","created":%d,"data":{"object":{"id":"cs_1","amount_total":%d,"currency":"cny","payment_intent":"pi_1","metadata":{"orderId":%q}}}}`,
		eventID, time.Now().Unix(), amountMinor, orderID))
}

func stripeRefundPayload(eventID, orderID string, amountMinor int64) []byte {
	return []byte(fmt.Sprintf(
		`{"id":%q,"type":"charge.refunded","created":%d,"data":{"object":{"id":"ch_1","amount_refunded":%d,"currency":"cny","payment_intent":"pi_1","metadata":{"orderId":%q}}}}`,
		eventID, time.Now().Unix(), amountMinor, orderID))
}

func stripeDisputePayload(eventID, orderID string, amountMinor int64) []byte {
	return []byte(fmt.Sprintf(
		`{"id":%q,"type":"charge.dispute.created","created":%d,"data":{"object":{"id":"dp_1","amount":%d,"currency":"cny","payment_intent":"pi_1","metadata":{"orderId":%q}}}}`,
		eventID, time.Now().Unix(), amountMinor, orderID))
}

func orderEvents(sink *recordingSink) []webhookEventType {
	types := sink.types()
	out := make([]webhookEventType, 0, len(types))
	for _, eventType := range types {
		out = append(out, webhookEventType(eventType))
	}
	return out
}

// webhookEventType keeps the assertions readable without importing the
// webhook package for a string alias.
type webhookEventType = string

func TestPortalOrderCreateRejectsInactiveAndUnknownPlan(t *testing.T) {
	ts, st, _ := newOrderTestServer(t, Config{})
	_, cookie, csrf := activePortalTenant(t, ts, st, "orders-a@example.com", "u-01")

	inactive := createOrderPlan(t, ts, false, 3000)
	resp, body := portalOrderCall(t, ts, http.MethodPost, "/v1/portal/orders",
		model.OrderCreateRequest{PlanID: inactive.ID}, cookie, csrf, nil)
	requireStatus(t, resp, http.StatusConflict)
	requireErrorCode(t, body, CodeConflict)
	if len(st.Orders()) != 0 {
		t.Fatalf("inactive plan created an order")
	}

	resp, _ = portalOrderCall(t, ts, http.MethodPost, "/v1/portal/orders",
		model.OrderCreateRequest{PlanID: "plan-404"}, cookie, csrf, nil)
	requireStatus(t, resp, http.StatusNotFound)

	active := createOrderPlan(t, ts, true, 3000)
	order, status := createPortalOrder(t, ts, cookie, csrf, active.ID, "key-1")
	if status != http.StatusCreated || order.State != model.OrderAwaitingPayment {
		t.Fatalf("created order = %+v, status %d", order, status)
	}
	if order.Plan.AmountMinor != 3000 || order.Plan.LeaseTTLSeconds != 3600 {
		t.Fatalf("order snapshot = %+v", order.Plan)
	}
}

func TestPortalOrderIdempotencyAndOwnership(t *testing.T) {
	ts, st, _ := newOrderTestServer(t, Config{})
	plan := createOrderPlan(t, ts, true, 3000)
	otherPlan := createOrderPlan(t, ts, true, 5000)
	_, cookieA, csrfA := activePortalTenant(t, ts, st, "orders-a@example.com", "u-01")
	_, cookieB, csrfB := activePortalTenant(t, ts, st, "orders-b@example.com", "u-02")

	first, status := createPortalOrder(t, ts, cookieA, csrfA, plan.ID, "idem-key")
	if status != http.StatusCreated {
		t.Fatalf("first create status = %d", status)
	}
	replay, status := createPortalOrder(t, ts, cookieA, csrfA, plan.ID, "idem-key")
	if status != http.StatusOK || replay.ID != first.ID {
		t.Fatalf("replay = %+v, status %d", replay, status)
	}
	resp, body := portalOrderCall(t, ts, http.MethodPost, "/v1/portal/orders",
		model.OrderCreateRequest{PlanID: otherPlan.ID}, cookieA, csrfA,
		map[string]string{"Idempotency-Key": "idem-key"})
	requireStatus(t, resp, http.StatusConflict)
	requireErrorCode(t, body, CodeConflict)

	// Foreign read and cancel are 404, exactly like an unknown id.
	resp, _ = portalOrderCall(t, ts, http.MethodGet, "/v1/portal/orders/"+first.ID, nil, cookieB, csrfB, nil)
	requireStatus(t, resp, http.StatusNotFound)
	resp, _ = portalOrderCall(t, ts, http.MethodPost, "/v1/portal/orders/"+first.ID+"/cancel", nil, cookieB, csrfB, nil)
	requireStatus(t, resp, http.StatusNotFound)

	resp, body = portalOrderCall(t, ts, http.MethodGet, "/v1/portal/orders/"+first.ID, nil, cookieA, csrfA, nil)
	requireStatus(t, resp, http.StatusOK)
	own := decodeBody[model.Order](t, body)
	if own.ID != first.ID || own.State != model.OrderAwaitingPayment {
		t.Fatalf("own order = %+v", own)
	}
	// The list never includes the other tenant's row.
	resp, body = portalOrderCall(t, ts, http.MethodGet, "/v1/portal/orders", nil, cookieB, csrfB, nil)
	requireStatus(t, resp, http.StatusOK)
	listed := decodeBody[[]model.Order](t, body)
	if len(listed) != 0 {
		t.Fatalf("tenant B sees %d foreign orders", len(listed))
	}
}

func TestPortalOrderCancelIsTerminalAndGrantsNothing(t *testing.T) {
	ts, st, _ := newOrderTestServer(t, Config{})
	plan := createOrderPlan(t, ts, true, 3000)
	_, cookie, csrf := activePortalTenant(t, ts, st, "orders-c@example.com", "u-01")
	order, _ := createPortalOrder(t, ts, cookie, csrf, plan.ID, "")
	leasesBefore := len(st.Leases())
	quotaBefore := *mustUser(t, st, "u-01").QuotaBytesMonth

	// The cancel is an unsafe portal method: without the CSRF token it is
	// refused before the store is touched.
	resp, _ := portalOrderCall(t, ts, http.MethodPost, "/v1/portal/orders/"+order.ID+"/cancel", nil, cookie, "", nil)
	requireStatus(t, resp, http.StatusForbidden)
	if stored, _ := st.Order(order.ID); stored.State != model.OrderAwaitingPayment {
		t.Fatalf("CSRF-less cancel mutated the order: %+v", stored)
	}

	resp, body := portalOrderCall(t, ts, http.MethodPost, "/v1/portal/orders/"+order.ID+"/cancel", nil, cookie, csrf, nil)
	requireStatus(t, resp, http.StatusOK)
	cancelled := decodeBody[model.Order](t, body)
	if cancelled.State != model.OrderCancelled || cancelled.CancelledAt == nil || cancelled.Grant != nil {
		t.Fatalf("cancelled order = %+v", cancelled)
	}
	if len(st.Leases()) != leasesBefore {
		t.Fatalf("cancel created a lease")
	}
	if got := *mustUser(t, st, "u-01").QuotaBytesMonth; got != quotaBefore {
		t.Fatalf("cancel changed quota %d -> %d", quotaBefore, got)
	}
	// The command is idempotent and the order stays terminal.
	resp, body = portalOrderCall(t, ts, http.MethodPost, "/v1/portal/orders/"+order.ID+"/cancel", nil, cookie, csrf, nil)
	requireStatus(t, resp, http.StatusOK)
	if again := decodeBody[model.Order](t, body); again.State != model.OrderCancelled {
		t.Fatalf("cancel replay = %+v", again)
	}
	// Late settlement is recorded for review and grants nothing.
	resp, body = settleOrderAPI(t, ts, order.ID, "late-receipt", true)
	requireStatus(t, resp, http.StatusOK)
	late := decodeBody[model.Order](t, body)
	if late.State != model.OrderCancelled || !late.ReviewRequired || late.Grant != nil {
		t.Fatalf("late settlement = %+v", late)
	}
	if len(st.Leases()) != leasesBefore {
		t.Fatalf("late settlement created a lease")
	}
}

func TestManualSettleRequiresAttestationAndReference(t *testing.T) {
	ts, st, _ := newOrderTestServer(t, Config{})
	plan := createOrderPlan(t, ts, true, 3000)
	_, cookie, csrf := activePortalTenant(t, ts, st, "orders-d@example.com", "u-01")
	order, _ := createPortalOrder(t, ts, cookie, csrf, plan.ID, "")
	leasesBefore := len(st.Leases())

	// No admin identity: the portal cookie cannot stand in for the admin token.
	req := newRequest(t, http.MethodPost, ts.URL+"/v1/admin/orders/"+order.ID+"/settle",
		model.OrderSettleRequest{Reference: "receipt", OfflineFundsReceived: true})
	req.Header.Set("Origin", portalTestOrigin)
	req.Header.Set("Cookie", portalCookieName+"="+cookie)
	req.Header.Set("X-CSRF-Token", csrf)
	resp, _ := doRequest(t, ts, req)
	requireStatus(t, resp, http.StatusForbidden)

	// An anonymous request is 401, not a settlement attempt.
	resp, _ = doRequest(t, ts, newRequest(t, http.MethodPost,
		ts.URL+"/v1/admin/orders/"+order.ID+"/settle",
		model.OrderSettleRequest{Reference: "receipt", OfflineFundsReceived: true}))
	requireStatus(t, resp, http.StatusUnauthorized)

	resp, body := settleOrderAPI(t, ts, order.ID, "receipt", false)
	requireStatus(t, resp, http.StatusBadRequest)
	requireErrorCode(t, body, CodeInvalidArgument)

	resp, body = settleOrderAPI(t, ts, order.ID, "  ", true)
	requireStatus(t, resp, http.StatusBadRequest)
	requireErrorCode(t, body, CodeInvalidArgument)

	stored, _ := st.Order(order.ID)
	if stored.State != model.OrderAwaitingPayment || stored.ProviderRef != nil {
		t.Fatalf("refused settlement mutated the order: %+v", stored)
	}
	if len(st.Leases()) != leasesBefore || len(st.PaymentEvents()) != 0 {
		t.Fatalf("refused settlement recorded payment or granted")
	}
}

func TestManualSettleActivatesAndReplaysOnce(t *testing.T) {
	ts, st, sink := newOrderTestServer(t, Config{})
	plan := createOrderPlan(t, ts, true, 3000)
	_, cookie, csrf := activePortalTenant(t, ts, st, "orders-e@example.com", "u-01")
	freeFixtureCapacity(t, st)
	order, _ := createPortalOrder(t, ts, cookie, csrf, plan.ID, "")
	leasesBefore := len(st.Leases())
	quotaBefore := *mustUser(t, st, "u-01").QuotaBytesMonth

	resp, body := settleOrderAPI(t, ts, order.ID, "receipt-1", true)
	requireStatus(t, resp, http.StatusOK)
	active := decodeBody[model.Order](t, body)
	if active.State != model.OrderActive || active.Grant == nil || active.PaidAt == nil || active.ActivatedAt == nil {
		t.Fatalf("settled order = %+v", active)
	}
	if len(st.Leases()) != leasesBefore+1 {
		t.Fatalf("leases = %d, want %d", len(st.Leases()), leasesBefore+1)
	}
	if got := *mustUser(t, st, "u-01").QuotaBytesMonth; got != plan.QuotaBytes {
		t.Fatalf("quota = %d, want %d", got, plan.QuotaBytes)
	}
	if active.Grant.PreviousQuotaBytes == nil || *active.Grant.PreviousQuotaBytes != quotaBefore {
		t.Fatalf("grant previous quota = %v, want %d", active.Grant.PreviousQuotaBytes, quotaBefore)
	}

	resp, body = settleOrderAPI(t, ts, order.ID, "receipt-1", true)
	requireStatus(t, resp, http.StatusOK)
	replay := decodeBody[model.Order](t, body)
	if replay.Grant == nil || replay.Grant.LeaseID != active.Grant.LeaseID ||
		replay.Grant.ExpiresAt != active.Grant.ExpiresAt {
		t.Fatalf("settle replay grant = %+v, want %+v", replay.Grant, active.Grant)
	}
	if len(st.Leases()) != leasesBefore+1 {
		t.Fatalf("replay duplicated the lease")
	}
	// A different reference on an already settled order conflicts.
	resp, _ = settleOrderAPI(t, ts, order.ID, "receipt-2", true)
	requireStatus(t, resp, http.StatusConflict)

	types := orderEvents(sink)
	for _, want := range []string{"order.created", "order.paid", "order.activated"} {
		if !containsString(types, want) {
			t.Fatalf("missing event %q in %v", want, types)
		}
	}
}

func TestSettleWhileSuspendedThenResume(t *testing.T) {
	ts, st, _ := newOrderTestServer(t, Config{})
	plan := createOrderPlan(t, ts, true, 3000)
	_, cookie, csrf := activePortalTenant(t, ts, st, "orders-f@example.com", "u-01")
	freeFixtureCapacity(t, st)
	order, _ := createPortalOrder(t, ts, cookie, csrf, plan.ID, "")
	leasesBefore := len(st.Leases())

	resp, _ := apiCall(t, ts, http.MethodPost, "/v1/users/u-01/suspend", nil)
	requireStatus(t, resp, http.StatusOK)
	resp, body := settleOrderAPI(t, ts, order.ID, "receipt-suspended", true)
	requireStatus(t, resp, http.StatusOK)
	paid := decodeBody[model.Order](t, body)
	if paid.State != model.OrderPaid || paid.Grant != nil {
		t.Fatalf("settled while suspended = %+v, want paid without grant", paid)
	}
	if len(st.Leases()) != leasesBefore {
		t.Fatalf("suspended activation created a lease")
	}
	// The retry still cannot grant while the user is disabled.
	resp, body = settleOrderAPI(t, ts, order.ID, "receipt-suspended", true)
	requireStatus(t, resp, http.StatusOK)
	if again := decodeBody[model.Order](t, body); again.State != model.OrderPaid || again.Grant != nil {
		t.Fatalf("retry while suspended = %+v", again)
	}

	resp, _ = apiCall(t, ts, http.MethodPost, "/v1/users/u-01/resume", nil)
	requireStatus(t, resp, http.StatusOK)
	resp, body = settleOrderAPI(t, ts, order.ID, "receipt-suspended", true)
	requireStatus(t, resp, http.StatusOK)
	active := decodeBody[model.Order](t, body)
	if active.State != model.OrderActive || active.Grant == nil || len(st.Leases()) != leasesBefore+1 {
		t.Fatalf("retry after resume = %+v, leases=%d", active, len(st.Leases()))
	}
}

func TestStripeWebhookDisabledUnsignedStaleAndMismatch(t *testing.T) {
	// A server without a Stripe adapter answers 503, never manual settlement.
	disabled, stDisabled, _ := newOrderTestServer(t, Config{})
	plan := createOrderPlan(t, disabled, true, 3000)
	_, cookie, csrf := activePortalTenant(t, disabled, stDisabled, "orders-g@example.com", "u-01")
	order, _ := createPortalOrder(t, disabled, cookie, csrf, plan.ID, "")
	body := stripeCheckoutPayload("evt_disabled", order.ID, 3000)
	resp, _ := stripeWebhookCall(t, disabled, body, time.Now().Unix(), orderTestStripeSecret)
	requireStatus(t, resp, http.StatusServiceUnavailable)

	ts, st, _ := newOrderTestServer(t, Config{
		StripeWebhooks: payment.NewStripeCompatible(payment.StripeConfig{Secret: orderTestStripeSecret}),
	})
	plan = createOrderPlan(t, ts, true, 3000)
	_, cookie, csrf = activePortalTenant(t, ts, st, "orders-h@example.com", "u-01")
	freeFixtureCapacity(t, st)
	order, _ = createPortalOrder(t, ts, cookie, csrf, plan.ID, "")
	leasesBefore := len(st.Leases())

	body = stripeCheckoutPayload("evt_unsigned", order.ID, 3000)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+stripeWebhookPath, bytes.NewReader(body))
	resp, _ = doRequest(t, ts, req)
	requireStatus(t, resp, http.StatusBadRequest)

	stale := time.Now().Add(-10 * time.Minute).Unix()
	resp, _ = stripeWebhookCall(t, ts, body, stale, orderTestStripeSecret)
	requireStatus(t, resp, http.StatusBadRequest)
	stored, _ := st.Order(order.ID)
	if stored.State != model.OrderAwaitingPayment || stored.ReviewRequired {
		t.Fatalf("rejected webhook mutated the order: %+v", stored)
	}
	if len(st.PaymentEvents()) != 0 {
		t.Fatalf("rejected webhook recorded a payment event")
	}

	// A verified event with the wrong amount is recorded for review, grants
	// nothing, and is acknowledged so the provider stops retrying.
	mismatch := stripeCheckoutPayload("evt_mismatch", order.ID, 9999)
	resp, _ = stripeWebhookCall(t, ts, mismatch, time.Now().Unix(), orderTestStripeSecret)
	requireStatus(t, resp, http.StatusOK)
	stored, _ = st.Order(order.ID)
	if !stored.ReviewRequired || stored.State != model.OrderAwaitingPayment || stored.Grant != nil {
		t.Fatalf("mismatched settlement = %+v", stored)
	}
	if len(st.Leases()) != leasesBefore {
		t.Fatalf("mismatch created a lease")
	}

	correct := stripeCheckoutPayload("evt_correct", order.ID, 3000)
	resp, _ = stripeWebhookCall(t, ts, correct, time.Now().Unix(), orderTestStripeSecret)
	requireStatus(t, resp, http.StatusOK)
	stored, _ = st.Order(order.ID)
	if stored.State != model.OrderActive || stored.Grant == nil {
		t.Fatalf("correct settlement = %+v", stored)
	}
	if len(st.Leases()) != leasesBefore+1 {
		t.Fatalf("correct settlement leases = %d", len(st.Leases()))
	}
}

func TestStripeWebhookConcurrentSettlementRecordsOneEvent(t *testing.T) {
	ts, st, _ := newOrderTestServer(t, Config{
		StripeWebhooks: payment.NewStripeCompatible(payment.StripeConfig{Secret: orderTestStripeSecret}),
	})
	plan := createOrderPlan(t, ts, true, 3000)
	_, cookie, csrf := activePortalTenant(t, ts, st, "orders-i@example.com", "u-01")
	freeFixtureCapacity(t, st)
	order, _ := createPortalOrder(t, ts, cookie, csrf, plan.ID, "")
	leasesBefore := len(st.Leases())
	body := stripeCheckoutPayload("evt_concurrent", order.ID, 3000)
	timestamp := time.Now().Unix()

	const workers = 5
	var wg sync.WaitGroup
	statuses := make(chan int, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, _ := stripeWebhookCall(t, ts, body, timestamp, orderTestStripeSecret)
			statuses <- resp.StatusCode
		}()
	}
	wg.Wait()
	close(statuses)
	for status := range statuses {
		if status != http.StatusOK {
			t.Fatalf("concurrent webhook status = %d", status)
		}
	}
	stored, _ := st.Order(order.ID)
	if stored.State != model.OrderActive || stored.Grant == nil {
		t.Fatalf("concurrent settlement = %+v", stored)
	}
	if len(st.Leases()) != leasesBefore+1 {
		t.Fatalf("concurrent settlement leases = %d, want %d", len(st.Leases()), leasesBefore+1)
	}
	if len(st.PaymentEvents()) != 1 {
		t.Fatalf("concurrent settlement events = %d, want 1", len(st.PaymentEvents()))
	}
	// A later duplicate is a no-op.
	resp, _ := stripeWebhookCall(t, ts, body, timestamp, orderTestStripeSecret)
	requireStatus(t, resp, http.StatusOK)
	if len(st.PaymentEvents()) != 1 || len(st.Leases()) != leasesBefore+1 {
		t.Fatalf("duplicate webhook duplicated state")
	}
}

func TestStripeWebhookUnsupportedEventIsAcknowledged(t *testing.T) {
	ts, st, _ := newOrderTestServer(t, Config{
		StripeWebhooks: payment.NewStripeCompatible(payment.StripeConfig{Secret: orderTestStripeSecret}),
	})
	body := []byte(fmt.Sprintf(`{"id":"evt_unknown","type":"customer.created","created":%d,"data":{"object":{"id":"cus_1"}}}`, time.Now().Unix()))
	resp, data := stripeWebhookCall(t, ts, body, time.Now().Unix(), orderTestStripeSecret)
	requireStatus(t, resp, http.StatusOK)
	ack := decodeBody[map[string]bool](t, data)
	if !ack["received"] || !ack["ignored"] {
		t.Fatalf("unsupported event ack = %v", ack)
	}
	if len(st.PaymentEvents()) != 0 {
		t.Fatalf("unsupported event recorded a payment")
	}
	found := false
	for _, event := range st.Audit(50) {
		if event.Action == "webhook.rejected" {
			found = true
		}
	}
	if !found {
		t.Fatalf("unsupported event was not audited")
	}
}

func TestRefundAndChargebackRevokeThroughAPI(t *testing.T) {
	ts, st, sink := newOrderTestServer(t, Config{})
	plan := createOrderPlan(t, ts, true, 3000)
	_, cookie, csrf := activePortalTenant(t, ts, st, "orders-j@example.com", "u-01")
	freeFixtureCapacity(t, st)
	order, _ := createPortalOrder(t, ts, cookie, csrf, plan.ID, "")
	quotaBefore := *mustUser(t, st, "u-01").QuotaBytesMonth
	leasesBefore := len(st.Leases())
	resp, _ := settleOrderAPI(t, ts, order.ID, "receipt-refund", true)
	requireStatus(t, resp, http.StatusOK)

	resp, body := refundOrderAPI(t, ts, order.ID, "offline-refund-1")
	requireStatus(t, resp, http.StatusOK)
	refunded := decodeBody[model.Order](t, body)
	if refunded.State != model.OrderRefunded || refunded.Grant == nil || refunded.Grant.RevokedAt == nil {
		t.Fatalf("refunded order = %+v", refunded)
	}
	if len(st.Leases()) != leasesBefore {
		t.Fatalf("refund left the entitlement lease")
	}
	if got := *mustUser(t, st, "u-01").QuotaBytesMonth; got != quotaBefore {
		t.Fatalf("refund quota = %d, want restored %d", got, quotaBefore)
	}
	// The refund command is idempotent.
	resp, body = refundOrderAPI(t, ts, order.ID, "offline-refund-1")
	requireStatus(t, resp, http.StatusOK)
	if again := decodeBody[model.Order](t, body); again.State != model.OrderRefunded {
		t.Fatalf("refund replay = %+v", again)
	}

	resp, body = chargebackOrderAPI(t, ts, order.ID, "dispute-1")
	requireStatus(t, resp, http.StatusOK)
	charged := decodeBody[model.Order](t, body)
	if charged.State != model.OrderChargedBack || !charged.ReviewRequired {
		t.Fatalf("chargeback = %+v", charged)
	}
	if charged.PaidAt == nil || charged.RefundedAt == nil {
		t.Fatalf("chargeback deleted payment history: %+v", charged)
	}
	types := orderEvents(sink)
	for _, want := range []string{"order.refunded", "order.charged_back"} {
		if !containsString(types, want) {
			t.Fatalf("missing event %q in %v", want, types)
		}
	}
}

func TestPartialRefundThroughWebhookStaysActive(t *testing.T) {
	ts, st, _ := newOrderTestServer(t, Config{
		StripeWebhooks: payment.NewStripeCompatible(payment.StripeConfig{Secret: orderTestStripeSecret}),
	})
	plan := createOrderPlan(t, ts, true, 3000)
	_, cookie, csrf := activePortalTenant(t, ts, st, "orders-k@example.com", "u-01")
	freeFixtureCapacity(t, st)
	order, _ := createPortalOrder(t, ts, cookie, csrf, plan.ID, "")
	resp, _ := settleOrderAPI(t, ts, order.ID, "receipt-partial", true)
	requireStatus(t, resp, http.StatusOK)
	leasesBefore := len(st.Leases())

	partial := stripeRefundPayload("evt_partial", order.ID, 1000)
	resp, _ = stripeWebhookCall(t, ts, partial, time.Now().Unix(), orderTestStripeSecret)
	requireStatus(t, resp, http.StatusOK)
	stored, _ := st.Order(order.ID)
	if stored.State != model.OrderActive || !stored.ReviewRequired || stored.Grant == nil || stored.Grant.RevokedAt != nil {
		t.Fatalf("partial refund = %+v, want active with review", stored)
	}
	if len(st.Leases()) != leasesBefore {
		t.Fatalf("partial refund revoked the lease")
	}
}

func TestAdminPlansCRUDAndDeactivationScope(t *testing.T) {
	ts, st, _ := newOrderTestServer(t, Config{})
	plan := createOrderPlan(t, ts, true, 3000)
	_, cookie, csrf := activePortalTenant(t, ts, st, "orders-l@example.com", "u-01")
	freeFixtureCapacity(t, st)
	order, _ := createPortalOrder(t, ts, cookie, csrf, plan.ID, "")
	resp, _ := settleOrderAPI(t, ts, order.ID, "receipt-plan", true)
	requireStatus(t, resp, http.StatusOK)

	resp, body := apiCall(t, ts, http.MethodGet, "/v1/admin/plans/"+plan.ID, nil)
	requireStatus(t, resp, http.StatusOK)
	if got := decodeBody[model.Plan](t, body); got.ID != plan.ID {
		t.Fatalf("admin get plan = %+v", got)
	}
	amount := int64(4200)
	resp, body = apiCall(t, ts, http.MethodPatch, "/v1/admin/plans/"+plan.ID, model.PlanPatch{AmountMinor: &amount})
	requireStatus(t, resp, http.StatusOK)
	if got := decodeBody[model.Plan](t, body); got.AmountMinor != 4200 {
		t.Fatalf("patched plan = %+v", got)
	}

	// Deactivating the plan leaves the settled order and its snapshot alone.
	resp, _ = apiCall(t, ts, http.MethodDelete, "/v1/admin/plans/"+plan.ID, nil)
	requireStatus(t, resp, http.StatusNoContent)
	stored, _ := st.Order(order.ID)
	if stored.State != model.OrderActive || stored.Plan.AmountMinor != 3000 {
		t.Fatalf("deactivation changed the existing order: %+v", stored)
	}
	resp, body = portalOrderCall(t, ts, http.MethodGet, "/v1/portal/plans", nil, cookie, csrf, nil)
	requireStatus(t, resp, http.StatusOK)
	if listed := decodeBody[[]model.Plan](t, body); len(listed) != 0 {
		t.Fatalf("portal lists a deactivated plan: %+v", listed)
	}
	// A new order for the deactivated plan is refused.
	_, cookieB, csrfB := activePortalTenant(t, ts, st, "orders-m@example.com", "u-02")
	resp, _ = portalOrderCall(t, ts, http.MethodPost, "/v1/portal/orders",
		model.OrderCreateRequest{PlanID: plan.ID}, cookieB, csrfB, nil)
	requireStatus(t, resp, http.StatusConflict)

	// PATCH active:true reactivates.
	resp, body = apiCall(t, ts, http.MethodPatch, "/v1/admin/plans/"+plan.ID, model.PlanPatch{Active: new(true)})
	requireStatus(t, resp, http.StatusOK)
	if got := decodeBody[model.Plan](t, body); !got.Active {
		t.Fatalf("reactivated plan = %+v", got)
	}
	resp, _ = apiCall(t, ts, http.MethodGet, "/v1/admin/plans/plan-404", nil)
	requireStatus(t, resp, http.StatusNotFound)
}

func mustUser(t *testing.T, st *store.Store, id string) model.PanelUser {
	t.Helper()
	user, ok := st.User(id)
	if !ok {
		t.Fatalf("user %q not found", id)
	}
	return user
}

// reviveOrderAPILease puts a node demoted by a simulated daemon restart back
// into service with the endpoint the lease advertised before.
func reviveOrderAPILease(t *testing.T, st *store.Store, lease model.Lease) {
	t.Helper()
	node, ok := st.Node(lease.NodeID)
	if !ok {
		t.Fatalf("node %q missing", lease.NodeID)
	}
	node.State = model.NodeLive
	node.GatePort = lease.GatePort
	node.EgressIP = lease.EgressIP
	node.Protocol = lease.Protocol
	node.Region = lease.Region
	node.Label = lease.NodeLabel
	if _, err := st.PutNode(node); err != nil {
		t.Fatalf("revive node %q: %v", lease.NodeID, err)
	}
}

// TestPortalOrderReissueAfterDaemonRestart is the customer-facing AJS-395
// path: after a restart ends the lease, the paid customer re-issues the same
// order and receives a replacement lease with one lease.reissued event and no
// new order, grant or payment.
func TestPortalOrderReissueAfterDaemonRestart(t *testing.T) {
	ts, st, sink := newOrderTestServer(t, Config{})
	plan := createOrderPlan(t, ts, true, 3000)
	_, cookie, csrf := activePortalTenant(t, ts, st, "orders-reissue@example.com", "u-01")
	freeFixtureCapacity(t, st)
	order, _ := createPortalOrder(t, ts, cookie, csrf, plan.ID, "reissue-order")

	resp, body := settleOrderAPI(t, ts, order.ID, "receipt-reissue", true)
	requireStatus(t, resp, http.StatusOK)
	active := decodeBody[model.Order](t, body)
	if active.State != model.OrderActive || active.Grant == nil {
		t.Fatalf("settled order = %+v", active)
	}
	oldLease, ok := st.Lease(active.Grant.LeaseID)
	if !ok {
		t.Fatalf("grant lease %q missing", active.Grant.LeaseID)
	}
	if _, _, _, err := st.DemoteNode(oldLease.NodeID); err != nil {
		t.Fatalf("DemoteNode: %v", err)
	}
	reviveOrderAPILease(t, st, oldLease)
	leasesBefore := len(st.Leases())
	paymentsBefore := len(st.PaymentEvents())
	grantsBefore := len(st.Grants())

	resp, body = portalOrderCall(t, ts, http.MethodPost,
		"/v1/portal/orders/"+order.ID+"/reissue", nil, cookie, csrf, nil)
	requireStatus(t, resp, http.StatusOK)
	after := decodeBody[model.Order](t, body)
	if after.Grant == nil || after.Grant.LeaseID == oldLease.ID || after.Grant.LeaseID == "" {
		t.Fatalf("reissued grant = %+v, old lease %s", after.Grant, oldLease.ID)
	}
	if after.Grant.ExpiresAt != active.Grant.ExpiresAt {
		t.Fatalf("reissue changed the paid expiry: %s -> %s", active.Grant.ExpiresAt, after.Grant.ExpiresAt)
	}
	if got := len(st.Leases()); got != leasesBefore+1 {
		t.Fatalf("leases = %d, want %d", got, leasesBefore+1)
	}
	if got := len(st.Grants()); got != grantsBefore {
		t.Fatalf("grants = %d, want the original %d", got, grantsBefore)
	}
	if got := len(st.PaymentEvents()); got != paymentsBefore {
		t.Fatalf("payment events = %d, want the original %d", got, paymentsBefore)
	}
	if got := sink.count(webhook.EventLeaseReissued); got != 1 {
		t.Fatalf("lease.reissued events = %d, want 1", got)
	}

	// The retry is refused and emits nothing further.
	resp, body = portalOrderCall(t, ts, http.MethodPost,
		"/v1/portal/orders/"+order.ID+"/reissue", nil, cookie, csrf, nil)
	requireStatus(t, resp, http.StatusConflict)
	if got := sink.count(webhook.EventLeaseReissued); got != 1 {
		t.Fatalf("retry emitted another lease.reissued: %d", got)
	}
	if got := len(st.Leases()); got != leasesBefore+1 {
		t.Fatalf("retry changed leases: %d", got)
	}
}

// TestAdminOrderReissueAfterDaemonRestart proves the admin path exists even
// without a portal session and uses the same single-grant operation.
func TestAdminOrderReissueAfterDaemonRestart(t *testing.T) {
	ts, st, sink := newOrderTestServer(t, Config{})
	plan := createOrderPlan(t, ts, true, 3000)
	_, cookie, csrf := activePortalTenant(t, ts, st, "orders-admin-reissue@example.com", "u-01")
	freeFixtureCapacity(t, st)
	order, _ := createPortalOrder(t, ts, cookie, csrf, plan.ID, "admin-reissue-order")
	resp, body := settleOrderAPI(t, ts, order.ID, "receipt-admin-reissue", true)
	requireStatus(t, resp, http.StatusOK)
	active := decodeBody[model.Order](t, body)
	oldLease, ok := st.Lease(active.Grant.LeaseID)
	if !ok {
		t.Fatalf("grant lease missing")
	}
	if _, _, _, err := st.DemoteNode(oldLease.NodeID); err != nil {
		t.Fatalf("DemoteNode: %v", err)
	}
	reviveOrderAPILease(t, st, oldLease)

	resp, body = apiCall(t, ts, http.MethodPost, "/v1/admin/orders/"+order.ID+"/reissue", nil)
	requireStatus(t, resp, http.StatusOK)
	after := decodeBody[model.Order](t, body)
	if after.Grant == nil || after.Grant.LeaseID == oldLease.ID {
		t.Fatalf("admin reissue grant = %+v", after.Grant)
	}
	if got := sink.count(webhook.EventLeaseReissued); got != 1 {
		t.Fatalf("lease.reissued events = %d, want 1", got)
	}
}

// TestPortalCreateOrderAfterEndedLeaseDoesNot409 is the AJS-395(c) API
// falsifier: the old check saw only grant.ExpiresAt and returned the 409
// "already holds an unexpired entitlement" even though the lease was ended.
// The same plan now takes the re-issue path and returns the existing order
// with a replacement lease, so the customer is not charged again.
func TestPortalCreateOrderAfterEndedLeaseDoesNot409(t *testing.T) {
	ts, st, sink := newOrderTestServer(t, Config{})
	plan := createOrderPlan(t, ts, true, 3000)
	_, cookie, csrf := activePortalTenant(t, ts, st, "orders-ended@example.com", "u-01")
	freeFixtureCapacity(t, st)
	order, _ := createPortalOrder(t, ts, cookie, csrf, plan.ID, "ended-order")
	resp, body := settleOrderAPI(t, ts, order.ID, "receipt-ended", true)
	requireStatus(t, resp, http.StatusOK)
	active := decodeBody[model.Order](t, body)
	lease, ok := st.Lease(active.Grant.LeaseID)
	if !ok {
		t.Fatalf("grant lease missing")
	}
	if _, _, _, err := st.DemoteNode(lease.NodeID); err != nil {
		t.Fatalf("DemoteNode: %v", err)
	}
	reviveOrderAPILease(t, st, lease)
	ordersBefore := len(st.Orders())
	paymentsBefore := len(st.PaymentEvents())
	leasesBefore := len(st.Leases())

	resp, body = portalOrderCall(t, ts, http.MethodPost, "/v1/portal/orders",
		model.OrderCreateRequest{PlanID: plan.ID}, cookie, csrf, nil)
	requireStatus(t, resp, http.StatusOK)
	reissued := decodeBody[model.Order](t, body)
	if reissued.ID != order.ID || reissued.State != model.OrderActive || reissued.Grant == nil {
		t.Fatalf("reissued order = %+v, want the original active order", reissued)
	}
	if reissued.Grant.LeaseID == lease.ID {
		t.Fatalf("order still points at the ended lease %s", lease.ID)
	}
	if got := len(st.Orders()); got != ordersBefore {
		t.Fatalf("orders = %d, want the original %d (no second order)", got, ordersBefore)
	}
	if got := len(st.PaymentEvents()); got != paymentsBefore {
		t.Fatalf("payment events = %d, want the original %d (no second charge)", got, paymentsBefore)
	}
	if got := len(st.Leases()); got != leasesBefore+1 {
		t.Fatalf("leases = %d, want %d", got, leasesBefore+1)
	}
	if got := sink.count(webhook.EventLeaseReissued); got != 1 {
		t.Fatalf("lease.reissued events = %d, want 1", got)
	}
}
