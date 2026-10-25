package payment

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"ajsaccountd/internal/model"
)

// TestOverlayEpayQueryStandardStatusField pins the shape documented by
// mainstream 易支付 gateways: api.php?act=order returns `status` (int, 1 paid)
// and has no `trade_status` field. The adapter must still recognize it.
func TestOverlayEpayQueryStandardStatusField(t *testing.T) {
	var gotMethod, gotURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotURL = r.URL.String()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"code":1,"msg":"查询订单号成功！","trade_no":"trade-1","out_trade_no":"ord-001","type":"alipay","name":"VIP会员","money":"30.00","status":1}`)
	}))
	defer server.Close()

	provider := NewEpay(EpayConfig{Gateway: server.URL, PID: "1001", Key: epayTestKey})
	status, err := provider.QueryOrder(context.Background(), "ord-001")
	if err != nil {
		t.Fatalf("QueryOrder: %v", err)
	}
	t.Logf("request: %s %s", gotMethod, gotURL)
	t.Logf("standard status=1 response -> Paid()=%v TradeStatus=%q", status.Paid(), status.TradeStatus)
	if !status.Paid() {
		t.Fatalf("standard 易支付 order-query status=1 was not recognized as paid")
	}
}

// TestOverlayEpayQueryErrorCodeIsNotOrderNotFound proves that every non-1
// response code (including an authentication failure) is currently reduced to
// ErrOrderNotFound, which the reconciler silently skips.
func TestOverlayEpayQueryErrorCodeIsNotOrderNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"code":-1,"msg":"签名错误"}`)
	}))
	defer server.Close()

	provider := NewEpay(EpayConfig{Gateway: server.URL, PID: "1001", Key: epayTestKey})
	_, err := provider.QueryOrder(context.Background(), "ord-001")
	t.Logf("gateway code=-1 -> err=%v ErrOrderNotFound=%v", err, errors.Is(err, ErrOrderNotFound))
	if errors.Is(err, ErrOrderNotFound) {
		t.Fatalf("a gateway authentication/configuration error is classified as order-not-found")
	}
}

// TestOverlayEpayPlaintextGatewayAccepted proves the adapter accepts a plain
// http gateway. QueryOrder then sends the merchant key in the URL and trusts an
// unsigned JSON response, so an on-path attacker can forge TRADE_SUCCESS.
func TestOverlayEpayPlaintextGatewayAccepted(t *testing.T) {
	provider := NewEpay(EpayConfig{
		Gateway: "http://pay.example.test/",
		PID:     "1001",
		Key:     "super-secret-epay-key",
	})
	t.Logf("http gateway: Enabled=%v reason=%q", provider.Enabled(), provider.DisabledReason())
	if provider.Enabled() {
		t.Fatalf("plaintext http gateway is accepted; the query path sends the merchant key in the URL")
	}
}

// TestOverlayEpayReconcileStarvesNewestOrders proves that a full first page of
// never-paid orders is re-queried on every pass; a 51st order never gets a
// query because PendingEpayOrders has no cursor or age filter.
func TestOverlayEpayReconcileStarvesNewestOrders(t *testing.T) {
	var mu sync.Mutex
	seen := make(map[string]int)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("out_trade_no")
		mu.Lock()
		seen[id]++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"code":1,"trade_no":"trade-%s","out_trade_no":%q,"money":"30.00","trade_status":"WAIT_BUYER_PAY"}`, id, id)
	}))
	defer server.Close()

	orders := make([]model.Order, 51)
	for i := range orders {
		id := fmt.Sprintf("ord-%03d", i+1)
		orders[i] = model.Order{
			ID:    id,
			State: model.OrderAwaitingPayment,
			Plan:  model.PlanSnapshot{Name: "Monthly", AmountMinor: 3000, Currency: "CNY"},
		}
	}
	store := &fakeEpayStore{pending: orders}
	provider := NewEpay(EpayConfig{Gateway: server.URL, PID: "1001", Key: epayTestKey})
	reconciler := NewEpayReconciler(EpayReconcilerConfig{
		Provider:  provider,
		Store:     store,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		MaxOrders: 50,
	})
	if reconciler == nil {
		t.Fatalf("reconciler is nil")
	}
	for pass := range 2 {
		if err := reconciler.ReconcileOnce(context.Background()); err != nil {
			t.Fatalf("ReconcileOnce pass %d: %v", pass, err)
		}
	}
	mu.Lock()
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	newestCount := seen["ord-051"]
	mu.Unlock()
	t.Logf("queried %d distinct orders over two passes; ord-051 queried %d times", len(ids), newestCount)
	if newestCount == 0 {
		t.Fatalf("the 51st pending order was never queried: the oldest 50 are re-queried every pass")
	}
}

// TestOverlayEpayQueryKeyInURL records the transport shape of the query call.
func TestOverlayEpayQueryKeyInURL(t *testing.T) {
	var gotURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.String()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"code":0,"msg":"not found"}`)
	}))
	defer server.Close()
	provider := NewEpay(EpayConfig{Gateway: server.URL, PID: "1001", Key: epayTestKey})
	_, _ = provider.QueryOrder(context.Background(), "ord-001")
	t.Logf("query URL: %s", gotURL)
	if !strings.Contains(gotURL, "key="+epayTestKey) {
		t.Fatalf("expected the merchant key in the query URL; got %q", gotURL)
	}
}
