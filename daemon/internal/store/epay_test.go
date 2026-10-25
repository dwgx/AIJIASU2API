package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"ajsaccountd/internal/model"
)

func TestMarkOrderCheckoutAndPendingEpayOrders(t *testing.T) {
	f := newOrderFixture(t)
	order := f.createOrder(t, "epay-checkout")
	if pending := f.st.PendingEpayOrders(); len(pending) != 0 {
		t.Fatalf("new order is already an epay pending order: %+v", pending)
	}
	marked, err := f.st.MarkOrderCheckout(f.tenant.ID, order.ID, model.ProviderEpay)
	if err != nil {
		t.Fatalf("MarkOrderCheckout: %v", err)
	}
	if marked.Provider != model.ProviderEpay || marked.State != model.OrderAwaitingPayment {
		t.Fatalf("marked order = %+v", marked)
	}
	pending := f.st.PendingEpayOrders()
	if len(pending) != 1 || pending[0].ID != order.ID {
		t.Fatalf("pending epay orders = %+v", pending)
	}
	// A foreign tenant cannot mark the order.
	if _, err := f.st.MarkOrderCheckout("t-foreign", order.ID, model.ProviderEpay); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign mark error = %v, want ErrNotFound", err)
	}
	if _, err := f.st.MarkOrderCheckout(f.tenant.ID, order.ID, "unknown"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown provider error = %v, want ErrInvalid", err)
	}

	event := model.PaymentEvent{
		Provider:    model.ProviderEpay,
		ID:          "trade-9001",
		Kind:        model.PaymentSettled,
		OrderID:     order.ID,
		ProviderRef: "trade-9001",
		AmountMinor: order.Plan.AmountMinor,
		Currency:    order.Plan.Currency,
		OccurredAt:  iso(*f.now),
	}
	settled := f.apply(t, event)
	if settled.Provider != model.ProviderEpay || settled.State != model.OrderActive {
		t.Fatalf("settled order = %+v", settled)
	}
	if pending := f.st.PendingEpayOrders(); len(pending) != 0 {
		t.Fatalf("settled order remains pending: %+v", pending)
	}
	// Checkout after settlement is a conflict, not a silent provider rewrite.
	if _, err := f.st.MarkOrderCheckout(f.tenant.ID, order.ID, model.ProviderEpay); !errors.Is(err, ErrConflict) {
		t.Fatalf("post-settlement checkout error = %v, want ErrConflict", err)
	}
}

func TestApplyPaymentEventRejectsProviderMismatch(t *testing.T) {
	f := newOrderFixture(t)
	order := f.createOrder(t, "epay-provider-mismatch")
	event := model.PaymentEvent{
		Provider:    model.ProviderEpay,
		ID:          "trade-mismatch",
		Kind:        model.PaymentSettled,
		OrderID:     order.ID,
		ProviderRef: "trade-mismatch",
		AmountMinor: order.Plan.AmountMinor,
		Currency:    order.Plan.Currency,
		OccurredAt:  iso(*f.now),
	}
	if _, err := f.st.ApplyPaymentEvent(model.ProviderEpay, event, "system"); !errors.Is(err, ErrConflict) {
		t.Fatalf("epay settlement of a manual order error = %v, want ErrConflict", err)
	}
	current, _ := f.st.Order(order.ID)
	if current.State != model.OrderAwaitingPayment || current.Provider != model.ProviderManual {
		t.Fatalf("rejected settlement changed the order: %+v", current)
	}

	// After the customer actually selects epay, the same event applies.
	if _, err := f.st.MarkOrderCheckout(f.tenant.ID, order.ID, model.ProviderEpay); err != nil {
		t.Fatalf("MarkOrderCheckout: %v", err)
	}
	settled, err := f.st.ApplyPaymentEvent(model.ProviderEpay, event, "system")
	if err != nil {
		t.Fatalf("epay settlement after checkout: %v", err)
	}
	if settled.State != model.OrderActive || settled.Provider != model.ProviderEpay {
		t.Fatalf("settled order = %+v", settled)
	}
}

func TestCreateOrderEvictionKeepsPendingEpayOrders(t *testing.T) {
	f := newOrderFixture(t)
	orders := make([]orderRecord, 0, MaxOrders)
	// Newest first: a protected epay row sits at the oldest end, where the
	// generic cap would evict it first.
	for i := range MaxOrders - 1 {
		orders = append(orders, orderRecord{Order: model.Order{
			ID:       fmt.Sprintf("old-%04d", i),
			UserID:   fmt.Sprintf("other-%04d", i),
			State:    model.OrderCancelled,
			Provider: model.ProviderManual,
		}})
	}
	orders = append(orders, orderRecord{Order: model.Order{
		ID:       "old-epay-pending",
		UserID:   "other-epay",
		State:    model.OrderAwaitingPayment,
		Provider: model.ProviderEpay,
	}})
	f.st.orders = orders

	created, _, err := f.st.CreateOrder(f.tenant.ID, f.user.ID, f.plan.ID, "eviction-guard", "tenant")
	if err != nil {
		t.Fatalf("CreateOrder at cap: %v", err)
	}
	if len(f.st.orders) != MaxOrders {
		t.Fatalf("order count = %d, want %d", len(f.st.orders), MaxOrders)
	}
	foundCreated, foundPending := false, false
	for _, record := range f.st.orders {
		if record.ID == created.ID {
			foundCreated = true
		}
		if record.ID == "old-epay-pending" {
			foundPending = true
		}
	}
	if !foundCreated || !foundPending {
		t.Fatalf("eviction lost an order: created=%t pendingEpay=%t", foundCreated, foundPending)
	}
	if f.st.orders[0].ID != created.ID {
		t.Fatalf("new order is not newest: %+v", f.st.orders[0])
	}
}

func TestOrderEvictionKeepsPendingEpayOrders(t *testing.T) {
	records := []orderRecord{
		{Order: model.Order{ID: "ord-new", State: model.OrderPaid, Provider: model.ProviderManual}},
		{Order: model.Order{ID: "ord-epay", State: model.OrderAwaitingPayment, Provider: model.ProviderEpay}},
		{Order: model.Order{ID: "ord-old", State: model.OrderCancelled, Provider: model.ProviderManual}},
	}
	out, ok := prependOrderCapped(records, orderRecord{Order: model.Order{ID: "ord-insert"}}, 3)
	if !ok {
		t.Fatalf("prependOrderCapped refused an evictable order set")
	}
	ids := make(map[string]bool, len(out))
	for _, record := range out {
		ids[record.ID] = true
	}
	if !ids["ord-epay"] {
		t.Fatalf("pending epay order was evicted: %+v", out)
	}
	if ids["ord-old"] {
		t.Fatalf("oldest evictable order was not evicted: %+v", out)
	}
	if !ids["ord-insert"] || len(out) != 3 {
		t.Fatalf("insert result = %+v", out)
	}

	// When every row is a pending epay order, insertion must fail rather
	// than drop money that a later notify can still report.
	protected := []orderRecord{
		{Order: model.Order{ID: "ord-p1", State: model.OrderAwaitingPayment, Provider: model.ProviderEpay}},
		{Order: model.Order{ID: "ord-p2", State: model.OrderAwaitingPayment, Provider: model.ProviderEpay}},
	}
	before := append([]orderRecord(nil), protected...)
	out, ok = prependOrderCapped(protected, orderRecord{Order: model.Order{ID: "ord-insert"}}, 2)
	if ok {
		t.Fatalf("prependOrderCapped accepted an all-protected order set")
	}
	if len(out) != len(before) || out[0].ID != before[0].ID || out[1].ID != before[1].ID {
		t.Fatalf("all-protected result changed: %+v", out)
	}
}

func TestEpayOrderAndEventSurviveSnapshotRestore(t *testing.T) {
	f := newOrderFixture(t)
	order := f.createOrder(t, "epay-restore")
	if _, err := f.st.MarkOrderCheckout(f.tenant.ID, order.ID, model.ProviderEpay); err != nil {
		t.Fatalf("MarkOrderCheckout: %v", err)
	}
	event := model.PaymentEvent{
		Provider:    model.ProviderEpay,
		ID:          "trade-restore",
		Kind:        model.PaymentSettled,
		OrderID:     order.ID,
		ProviderRef: "trade-restore",
		AmountMinor: order.Plan.AmountMinor,
		Currency:    order.Plan.Currency,
		OccurredAt:  iso(*f.now),
	}
	f.apply(t, event)

	snapshot, err := f.st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	text := string(snapshot)
	if !strings.Contains(text, `"provider":"epay"`) && !strings.Contains(text, `"provider": "epay"`) {
		t.Fatalf("snapshot does not record the epay provider")
	}
	var decoded map[string]any
	if err := json.Unmarshal(snapshot, &decoded); err != nil {
		t.Fatalf("snapshot JSON: %v", err)
	}
	restored := newEmptyWithClock(func() time.Time { return *f.now }, nil)
	if err := restored.Restore(snapshot); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	got, ok := restored.Order(order.ID)
	if !ok || got.Provider != model.ProviderEpay || got.State != model.OrderActive {
		t.Fatalf("restored order = %+v, ok=%t", got, ok)
	}
}
