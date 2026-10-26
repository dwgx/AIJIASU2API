package payment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"ajsaccountd/internal/model"
)

type epayAuditRow struct {
	severity model.Severity
	actor    string
	action   string
	target   string
	detail   string
}

type fakeEpayStore struct {
	mu       sync.Mutex
	pending  []model.Order
	applied  []model.PaymentEvent
	audits   []epayAuditRow
	applyErr error
	// durabilityErr makes the reconciler's poison check testable.
	durabilityErr error
}

func (f *fakeEpayStore) PendingEpayOrders() []model.Order {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]model.Order(nil), f.pending...)
}

func (f *fakeEpayStore) ApplyPaymentEvent(provider string, event model.PaymentEvent, actor string) (model.Order, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.applyErr != nil {
		return model.Order{}, f.applyErr
	}
	event.Provider = provider
	f.applied = append(f.applied, event)
	out := model.Order{ID: event.OrderID, State: model.OrderPaid}
	kept := f.pending[:0]
	for _, order := range f.pending {
		if order.ID != event.OrderID {
			kept = append(kept, order)
		}
	}
	f.pending = kept
	return out, nil
}

func (f *fakeEpayStore) RecordAudit(severity model.Severity, actor, action, target, detail string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.audits = append(f.audits, epayAuditRow{severity, actor, action, target, detail})
	return nil
}

func (f *fakeEpayStore) DurabilityError() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.durabilityErr
}

func (f *fakeEpayStore) snapshot() ([]model.PaymentEvent, []epayAuditRow) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]model.PaymentEvent(nil), f.applied...), append([]epayAuditRow(nil), f.audits...)
}

func epayQueryServer(t *testing.T, payload string, status int, requests *int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests != nil {
			*requests = *requests + 1
		}
		query := r.URL.Query()
		if query.Get("act") != "order" || query.Get("pid") != "1001" ||
			query.Get("key") != epayTestKey || query.Get("out_trade_no") == "" {
			t.Errorf("query params = %v", query)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, payload)
	}))
}

func TestEpayQueryOrderParsesStatusAndNestedData(t *testing.T) {
	// Standard 易支付 order query: status=1 means paid, with no trade_status.
	payload := `{"code":1,"msg":"ok","trade_no":"trade-1","out_trade_no":"ord-001","type":"alipay","name":"Monthly","money":"30.00","status":1}`
	server := epayQueryServer(t, payload, http.StatusOK, nil)
	defer server.Close()
	provider := NewEpay(EpayConfig{Gateway: server.URL, PID: "1001", Key: epayTestKey})
	status, err := provider.QueryOrder(context.Background(), "ord-001")
	if err != nil {
		t.Fatalf("QueryOrder: %v", err)
	}
	if status.TradeNo != "trade-1" || status.OutTradeNo != "ord-001" ||
		status.Money != "30.00" || status.Status != 1 || !status.Paid() {
		t.Fatalf("status = %+v", status)
	}

	// A string status is accepted by several forks.
	quoted := `{"code":1,"trade_no":"trade-3","out_trade_no":"ord-003","money":"30.00","status":"1"}`
	server3 := epayQueryServer(t, quoted, http.StatusOK, nil)
	defer server3.Close()
	provider3 := NewEpay(EpayConfig{Gateway: server3.URL, PID: "1001", Key: epayTestKey})
	status, err = provider3.QueryOrder(context.Background(), "ord-003")
	if err != nil || status.Status != 1 || !status.Paid() {
		t.Fatalf("quoted status = %+v, %v", status, err)
	}

	nested := `{"code":"1","data":{"trade_no":"trade-2","out_trade_no":"ord-002","money":"0.01","trade_status":"TRADE_SUCCESS","status":1}}`
	server2 := epayQueryServer(t, nested, http.StatusOK, nil)
	defer server2.Close()
	provider2 := NewEpay(EpayConfig{Gateway: server2.URL, PID: "1001", Key: epayTestKey})
	status, err = provider2.QueryOrder(context.Background(), "ord-002")
	if err != nil || status.TradeNo != "trade-2" || status.OutTradeNo != "ord-002" ||
		status.Status != 1 {
		t.Fatalf("nested status = %+v, %v", status, err)
	}
}

func TestEpayQueryOrderErrors(t *testing.T) {
	notFound := epayQueryServer(t, `{"code":0,"msg":"not found"}`, http.StatusOK, nil)
	defer notFound.Close()
	provider := NewEpay(EpayConfig{Gateway: notFound.URL, PID: "1001", Key: epayTestKey})
	if _, err := provider.QueryOrder(context.Background(), "ord-404"); !errors.Is(err, ErrOrderNotFound) {
		t.Fatalf("not-found error = %v", err)
	}

	broken := epayQueryServer(t, `not-json`, http.StatusOK, nil)
	defer broken.Close()
	provider = NewEpay(EpayConfig{Gateway: broken.URL, PID: "1001", Key: epayTestKey})
	if _, err := provider.QueryOrder(context.Background(), "ord-001"); !errors.Is(err, ErrQueryFailed) {
		t.Fatalf("malformed error = %v", err)
	}

	failing := epayQueryServer(t, `{}`, http.StatusBadGateway, nil)
	defer failing.Close()
	provider = NewEpay(EpayConfig{Gateway: failing.URL, PID: "1001", Key: epayTestKey})
	_, err := provider.QueryOrder(context.Background(), "ord-001")
	if !errors.Is(err, ErrQueryFailed) {
		t.Fatalf("status error = %v", err)
	}
	if strings.Contains(err.Error(), epayTestKey) || strings.Contains(err.Error(), "key=") {
		t.Fatalf("query error leaks the key: %q", err)
	}
}

func TestEpayQueryErrorClassification(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    error
	}{
		{"order not found", `{"code":-1,"msg":"订单号不存在"}`, ErrOrderNotFound},
		{"merchant key error", `{"code":-3,"msg":"商户密钥错误"}`, ErrQueryAuth},
		{"signature error", `{"code":-1,"msg":"签名错误"}`, ErrQueryAuth},
		{"gateway busy", `{"code":-2,"msg":"系统繁忙"}`, ErrQueryTransient},
		{"unknown zero code", `{"code":0,"msg":"unexpected"}`, ErrQueryTransient},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := epayQueryServer(t, tc.payload, http.StatusOK, nil)
			defer server.Close()
			provider := NewEpay(EpayConfig{Gateway: server.URL, PID: "1001", Key: epayTestKey})
			_, err := provider.QueryOrder(context.Background(), "ord-001")
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if tc.want != ErrOrderNotFound && !errors.Is(err, ErrQueryFailed) {
				t.Fatalf("typed error %v does not wrap ErrQueryFailed", err)
			}
		})
	}
}

func TestEpayQueryDoesNotFollowRedirects(t *testing.T) {
	followed := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		followed = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":1,"out_trade_no":"ord-001","money":"30.00","status":1}`)
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirect.Close()

	provider := NewEpay(EpayConfig{Gateway: redirect.URL, PID: "1001", Key: epayTestKey})
	_, err := provider.QueryOrder(context.Background(), "ord-001")
	if !errors.Is(err, ErrQueryFailed) {
		t.Fatalf("redirect query error = %v, want ErrQueryFailed", err)
	}
	if followed {
		t.Fatalf("query followed a redirect to another host")
	}
}

func TestEpayReconcileAuditsTransientErrors(t *testing.T) {
	server := epayQueryServer(t, `{"code":-2,"msg":"系统繁忙"}`, http.StatusOK, nil)
	defer server.Close()
	provider := NewEpay(EpayConfig{Gateway: server.URL, PID: "1001", Key: epayTestKey})
	store := &fakeEpayStore{pending: []model.Order{testEpayOrder()}}
	reconciler := NewEpayReconciler(EpayReconcilerConfig{
		Provider: provider, Store: store,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err := reconciler.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	applied, audits := store.snapshot()
	if len(applied) != 0 {
		t.Fatalf("transient error applied %d events", len(applied))
	}
	if len(audits) != 1 || audits[0].severity != model.SeverityWarn ||
		audits[0].action != "epay.reconcile" ||
		!strings.Contains(audits[0].detail, "retry") {
		t.Fatalf("transient audit = %+v", audits)
	}
}

func TestEpayReconcileRotatesFullBatch(t *testing.T) {
	seen := make(map[string]int)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen[r.URL.Query().Get("out_trade_no")]++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":1,"out_trade_no":"`+r.URL.Query().Get("out_trade_no")+`","money":"30.00","status":0}`)
	}))
	defer server.Close()
	orders := make([]model.Order, 5)
	for i := range orders {
		orders[i] = testEpayOrder()
		orders[i].ID = fmt.Sprintf("ord-%03d", i+1)
	}
	store := &fakeEpayStore{pending: orders}
	provider := NewEpay(EpayConfig{Gateway: server.URL, PID: "1001", Key: epayTestKey})
	reconciler := NewEpayReconciler(EpayReconcilerConfig{
		Provider: provider, Store: store,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		MaxOrders: 3,
	})
	for range 2 {
		if err := reconciler.ReconcileOnce(context.Background()); err != nil {
			t.Fatalf("ReconcileOnce: %v", err)
		}
	}
	if seen["ord-004"] == 0 || seen["ord-005"] == 0 {
		t.Fatalf("full-batch rotation never reached newest orders: %v", seen)
	}
}

// TestEpayReconcileRotatesMixedAwaitingAndPaidRows proves a batch smaller than
// the queue still reaches paid rows behind awaiting ones: the store hands back
// one ordered list and the poller rotates its start, so neither state starves
// the other.
func TestEpayReconcileRotatesMixedAwaitingAndPaidRows(t *testing.T) {
	seen := make(map[string]int)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen[r.URL.Query().Get("out_trade_no")]++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":1,"out_trade_no":"`+r.URL.Query().Get("out_trade_no")+`","money":"30.00","status":0}`)
	}))
	defer server.Close()
	orders := make([]model.Order, 6)
	for i := range orders {
		orders[i] = testEpayOrder()
		orders[i].ID = fmt.Sprintf("ord-%03d", i+1)
		orders[i].State = model.OrderAwaitingPayment
		if i%2 == 1 {
			orders[i].State = model.OrderPaid
		}
	}
	store := &fakeEpayStore{pending: orders}
	provider := NewEpay(EpayConfig{Gateway: server.URL, PID: "1001", Key: epayTestKey})
	reconciler := NewEpayReconciler(EpayReconcilerConfig{
		Provider: provider, Store: store,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		MaxOrders: 3,
	})
	for range 2 {
		if err := reconciler.ReconcileOnce(context.Background()); err != nil {
			t.Fatalf("ReconcileOnce: %v", err)
		}
	}
	for _, order := range orders {
		if seen[order.ID] == 0 {
			t.Fatalf("mixed rotation starved %s (%s): %v", order.ID, order.State, seen)
		}
	}
}

func TestEpayReconcileSkipsNonCNYOrder(t *testing.T) {
	server := epayQueryServer(t, `{"code":1,"trade_no":"trade-usd","out_trade_no":"ord-usd","money":"30.00","status":1}`, http.StatusOK, nil)
	defer server.Close()
	provider := NewEpay(EpayConfig{Gateway: server.URL, PID: "1001", Key: epayTestKey})
	order := testEpayOrder()
	order.ID = "ord-usd"
	order.Plan.Currency = "USD"
	store := &fakeEpayStore{pending: []model.Order{order}}
	reconciler := NewEpayReconciler(EpayReconcilerConfig{
		Provider: provider, Store: store,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err := reconciler.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	applied, audits := store.snapshot()
	if len(applied) != 0 {
		t.Fatalf("non-CNY order settled: %+v", applied)
	}
	if len(audits) != 1 || !strings.Contains(audits[0].detail, "not CNY") {
		t.Fatalf("non-CNY audit = %+v", audits)
	}
}

func TestEpayReconcileSettlesPendingOrder(t *testing.T) {
	payload := `{"code":1,"trade_no":"trade-9001","out_trade_no":"ord-001","money":"30.00","trade_status":"TRADE_SUCCESS"}`
	server := epayQueryServer(t, payload, http.StatusOK, nil)
	defer server.Close()
	provider := NewEpay(EpayConfig{Gateway: server.URL, PID: "1001", Key: epayTestKey})
	store := &fakeEpayStore{pending: []model.Order{testEpayOrder()}}
	now := time.Date(2026, 10, 25, 13, 0, 0, 0, time.UTC)
	reconciler := NewEpayReconciler(EpayReconcilerConfig{
		Provider: provider,
		Store:    store,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:      func() time.Time { return now },
		Interval: time.Minute,
	})
	if reconciler == nil {
		t.Fatalf("reconciler is nil")
	}
	if err := reconciler.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	applied, audits := store.snapshot()
	if len(applied) != 1 {
		t.Fatalf("applied events = %d, want 1", len(applied))
	}
	event := applied[0]
	if event.Provider != model.ProviderEpay || event.ID != "trade-9001" ||
		event.ProviderRef != "trade-9001" || event.Kind != model.PaymentSettled ||
		event.OrderID != "ord-001" || event.AmountMinor != 3000 ||
		event.Currency != "CNY" || event.OccurredAt != now.Format(time.RFC3339Nano) {
		t.Fatalf("event = %+v", event)
	}
	if len(audits) != 1 || audits[0].action != "epay.reconcile" ||
		audits[0].severity != model.SeverityInfo || audits[0].target != "ord-001" {
		t.Fatalf("audits = %+v", audits)
	}

	// The fake store removed the order from the pending queue; a second pass
	// must not replay it.
	if err := reconciler.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("second ReconcileOnce: %v", err)
	}
	applied, _ = store.snapshot()
	if len(applied) != 1 {
		t.Fatalf("second pass applied %d events", len(applied))
	}
}

func TestEpayReconcileRejectsMismatchAndBoundsBatch(t *testing.T) {
	requests := 0
	payload := `{"code":1,"trade_no":"trade-1","out_trade_no":"ord-001","money":"0.01","trade_status":"TRADE_SUCCESS"}`
	server := epayQueryServer(t, payload, http.StatusOK, &requests)
	defer server.Close()
	provider := NewEpay(EpayConfig{Gateway: server.URL, PID: "1001", Key: epayTestKey})
	orders := []model.Order{
		{ID: "ord-001", Plan: model.PlanSnapshot{Name: "One", AmountMinor: 3000, Currency: "CNY"}},
		{ID: "ord-002", Plan: model.PlanSnapshot{Name: "Two", AmountMinor: 3000, Currency: "CNY"}},
		{ID: "ord-003", Plan: model.PlanSnapshot{Name: "Three", AmountMinor: 3000, Currency: "CNY"}},
	}
	store := &fakeEpayStore{pending: orders}
	reconciler := NewEpayReconciler(EpayReconcilerConfig{
		Provider: provider, Store: store,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		MaxOrders: 2,
	})
	if err := reconciler.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	if requests != 2 {
		t.Fatalf("queried %d orders, want 2", requests)
	}
	applied, audits := store.snapshot()
	if len(applied) != 0 {
		t.Fatalf("mismatched amount applied %d events", len(applied))
	}
	if len(audits) != 2 || audits[0].severity != model.SeverityWarn ||
		audits[0].action != "epay.reconcile" {
		t.Fatalf("audits = %+v", audits)
	}
}

// TestEpayReconcileQueriesPaidUnfulfilledOrder proves the poller handles a paid
// order the store selected for reconciliation: it applies the gateway fact and
// reports the payment as recorded but not yet served, instead of skipping a row
// that is no longer awaiting_payment.
func TestEpayReconcileQueriesPaidUnfulfilledOrder(t *testing.T) {
	payload := `{"code":1,"trade_no":"trade-paid","out_trade_no":"ord-001","money":"30.00","trade_status":"TRADE_SUCCESS"}`
	server := epayQueryServer(t, payload, http.StatusOK, nil)
	defer server.Close()
	provider := NewEpay(EpayConfig{Gateway: server.URL, PID: "1001", Key: epayTestKey})
	order := testEpayOrder()
	order.State = model.OrderPaid
	store := &fakeEpayStore{pending: []model.Order{order}}
	reconciler := NewEpayReconciler(EpayReconcilerConfig{
		Provider: provider, Store: store,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err := reconciler.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	applied, audits := store.snapshot()
	if len(applied) != 1 || applied[0].ID != "trade-paid" {
		t.Fatalf("applied = %+v, want one reconciliation fact", applied)
	}
	if len(audits) != 1 || audits[0].action != "epay.reconcile" ||
		audits[0].severity != model.SeverityInfo || audits[0].target != "ord-001" ||
		!strings.Contains(audits[0].detail, "pending fulfilment") {
		t.Fatalf("audits = %+v, want a paid-but-unfulfilled reconciliation row", audits)
	}
}

func TestEpayReconcileDisabledAndUnpaid(t *testing.T) {
	disabled := &EpayReconciler{provider: NewEpay(EpayConfig{})}
	if err := disabled.ReconcileOnce(context.Background()); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled error = %v", err)
	}
	if NewEpayReconciler(EpayReconcilerConfig{}) != nil {
		t.Fatalf("nil provider produced a reconciler")
	}

	payload := `{"code":1,"trade_no":"trade-1","out_trade_no":"ord-001","money":"30.00","trade_status":"WAIT_BUYER_PAY"}`
	server := epayQueryServer(t, payload, http.StatusOK, nil)
	defer server.Close()
	provider := NewEpay(EpayConfig{Gateway: server.URL, PID: "1001", Key: epayTestKey})
	store := &fakeEpayStore{pending: []model.Order{testEpayOrder()}}
	reconciler := NewEpayReconciler(EpayReconcilerConfig{
		Provider: provider, Store: store,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err := reconciler.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	applied, _ := store.snapshot()
	if len(applied) != 0 {
		t.Fatalf("unpaid order settled")
	}
}

func TestEpayQueryResponseShape(t *testing.T) {
	var response epayQueryResponse
	if err := json.Unmarshal([]byte(`{"code":1,"trade_status":"TRADE_SUCCESS"}`), &response); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if response.code() != 1 || !response.status().Paid() {
		t.Fatalf("response = %+v", response)
	}
}

// TestEpayReconcileStopsWhenStoreIsPoisoned proves the loop checks the poison
// before every query: the gateway is unreachable on purpose, so an audit row
// would appear if a query had been attempted.
func TestEpayReconcileStopsWhenStoreIsPoisoned(t *testing.T) {
	poison := errors.New("journal commit failed")
	fake := &fakeEpayStore{pending: []model.Order{testEpayOrder()}, durabilityErr: poison}
	provider := NewEpay(EpayConfig{Gateway: "http://127.0.0.1:1", PID: "1001", Key: epayTestKey})
	reconciler := NewEpayReconciler(EpayReconcilerConfig{
		Provider: provider, Store: fake,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err := reconciler.ReconcileOnce(context.Background()); !errors.Is(err, poison) {
		t.Fatalf("ReconcileOnce error = %v, want the durability error", err)
	}
	if applied, audits := fake.snapshot(); len(applied) != 0 || len(audits) != 0 {
		t.Fatalf("poisoned store still queried: applied=%d audits=%d", len(applied), len(audits))
	}
}
