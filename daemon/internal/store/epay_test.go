package store

import (
	"encoding/json"
	"errors"
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
