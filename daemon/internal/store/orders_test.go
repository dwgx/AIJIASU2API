package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"ajsaccountd/internal/model"
)

// orderFixture is one empty store with an android account, a free node, an
// enabled user, an active tenant linked to that user and one active plan.
type orderFixture struct {
	st     *Store
	now    *time.Time
	tenant model.Tenant
	user   model.PanelUser
	plan   model.Plan
}

func newOrderFixture(t *testing.T) *orderFixture {
	t.Helper()
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	st := newEmptyWithClock(func() time.Time { return now }, nil)
	account, err := st.CreateAccount(model.CreateAccountInput{
		Label: "Orders Ops", Username: "orders-ops", Password: "local-placeholder",
		Platform: model.PlatformAndroid, Concurrency: 10,
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if _, err := st.PutNode(model.ProxyNode{
		ID: "node-orders", AccountID: account.ID, Label: "orders-1", Region: "cn-zj",
		State: model.NodeLive, EgressIP: "203.0.113.90",
		Protocol: model.ProtocolSOCKS5, GatePort: 13090,
	}); err != nil {
		t.Fatalf("PutNode: %v", err)
	}
	user, err := st.CreateUser(model.CreateUserInput{
		Name: "order-user", Platform: "android", QuotaBytesMonth: ptrInt64(1000),
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	tenant := signupTestTenant(t, st, "order@example.com", "Order User")
	if _, err := st.ApproveTenant(tenant.ID, user.ID, nil); err != nil {
		t.Fatalf("ApproveTenant: %v", err)
	}
	tenant, _ = st.Tenant(tenant.ID)
	plan, err := st.CreatePlan(model.PlanInput{
		Name: "Monthly 10 GiB", AmountMinor: 3000, Currency: "CNY",
		LeaseTTLSeconds: 3600, QuotaBytes: 10 << 30,
	})
	if err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	return &orderFixture{st: st, now: &now, tenant: tenant, user: user, plan: plan}
}

func (f *orderFixture) createOrder(t *testing.T, key string) model.Order {
	t.Helper()
	order, replayed, err := f.st.CreateOrder(f.tenant.ID, f.user.ID, f.plan.ID, key, "tenant:"+f.tenant.ID)
	if err != nil {
		t.Fatalf("CreateOrder(%q): %v", key, err)
	}
	if replayed {
		t.Fatalf("CreateOrder(%q) replayed unexpectedly", key)
	}
	return order
}

func (f *orderFixture) settleEvent(orderID, eventID, reference string) model.PaymentEvent {
	return model.PaymentEvent{
		Provider:    model.ProviderManual,
		ID:          eventID,
		Kind:        model.PaymentSettled,
		OrderID:     orderID,
		ProviderRef: reference,
		OccurredAt:  iso(*f.now),
	}
}

func (f *orderFixture) apply(t *testing.T, event model.PaymentEvent) model.Order {
	t.Helper()
	order, err := f.st.ApplyPaymentEvent(event.Provider, event, "operator")
	if err != nil {
		t.Fatalf("ApplyPaymentEvent(%s/%s): %v", event.Provider, event.ID, err)
	}
	return order
}

func auditActions(st *Store) []string {
	actions := make([]string, 0, len(st.Audit(200)))
	for _, event := range st.Audit(200) {
		actions = append(actions, event.Action)
	}
	return actions
}

func containsAction(actions []string, want string) bool {
	for _, action := range actions {
		if action == want {
			return true
		}
	}
	return false
}

func TestPlanLifecycleAndValidation(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	st := newEmptyWithClock(func() time.Time { return now }, nil)

	invalid := []model.PlanInput{
		{Name: "", AmountMinor: 100, Currency: "CNY", LeaseTTLSeconds: 3600, QuotaBytes: 1},
		{Name: "Zero", AmountMinor: 0, Currency: "CNY", LeaseTTLSeconds: 3600, QuotaBytes: 1},
		{Name: "Bad currency", AmountMinor: 100, Currency: "C1Y", LeaseTTLSeconds: 3600, QuotaBytes: 1},
		{Name: "Short ttl", AmountMinor: 100, Currency: "CNY", LeaseTTLSeconds: 5, QuotaBytes: 1},
		{Name: "Zero quota", AmountMinor: 100, Currency: "CNY", LeaseTTLSeconds: 3600, QuotaBytes: 0},
	}
	for _, in := range invalid {
		if _, err := st.CreatePlan(in); !errors.Is(err, ErrInvalid) {
			t.Fatalf("CreatePlan(%+v) error = %v, want ErrInvalid", in, err)
		}
	}

	plan, err := st.CreatePlan(model.PlanInput{
		Name: "Monthly", AmountMinor: 3000, Currency: "cny",
		LeaseTTLSeconds: 3600, QuotaBytes: 1000,
	})
	if err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	if !plan.Active || plan.Currency != "CNY" || plan.ID == "" {
		t.Fatalf("created plan = %+v, want active with normalized currency", plan)
	}

	amount := int64(4500)
	updated, err := st.UpdatePlan(plan.ID, model.PlanPatch{AmountMinor: &amount})
	if err != nil {
		t.Fatalf("UpdatePlan: %v", err)
	}
	if updated.AmountMinor != amount || updated.Name != plan.Name {
		t.Fatalf("updated plan = %+v", updated)
	}

	deactivated, err := st.DeactivatePlan(plan.ID)
	if err != nil {
		t.Fatalf("DeactivatePlan: %v", err)
	}
	if deactivated.Active {
		t.Fatalf("deactivated plan is still active")
	}
	// Deactivation is idempotent.
	if again, err := st.DeactivatePlan(plan.ID); err != nil || again.Active {
		t.Fatalf("DeactivatePlan replay = %+v, %v", again, err)
	}
	if _, err := st.UpdatePlan(plan.ID, model.PlanPatch{Active: new(true)}); err != nil {
		t.Fatalf("reactivate: %v", err)
	}
	if plan, _ := st.Plan(plan.ID); !plan.Active {
		t.Fatalf("plan did not reactivate")
	}
}

func TestCreateOrderIdempotencyOwnershipAndCancel(t *testing.T) {
	f := newOrderFixture(t)
	order := f.createOrder(t, "idem-1")

	replayed, ok, err := f.st.CreateOrder(f.tenant.ID, f.user.ID, f.plan.ID, "idem-1", "tenant")
	if err != nil || !ok || replayed.ID != order.ID {
		t.Fatalf("idempotent replay = %+v, replayed=%t, err=%v", replayed, ok, err)
	}
	if got := len(f.st.Orders()); got != 1 {
		t.Fatalf("orders after replay = %d, want 1", got)
	}
	other, err := f.st.CreatePlan(model.PlanInput{
		Name: "Other", AmountMinor: 100, Currency: "USD", LeaseTTLSeconds: 3600, QuotaBytes: 100,
	})
	if err != nil {
		t.Fatalf("CreatePlan(other): %v", err)
	}
	if _, _, err := f.st.CreateOrder(f.tenant.ID, f.user.ID, other.ID, "idem-1", "tenant"); !errors.Is(err, ErrConflict) {
		t.Fatalf("same key different body error = %v, want ErrConflict", err)
	}
	if _, _, err := f.st.CreateOrder(f.tenant.ID, f.user.ID, f.plan.ID, "bad key", "tenant"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("whitespace key error = %v, want ErrInvalid", err)
	}

	// One open order per user: a second create is refused while it is open.
	if _, _, err := f.st.CreateOrder(f.tenant.ID, f.user.ID, f.plan.ID, "", "tenant"); !errors.Is(err, ErrConflict) {
		t.Fatalf("second open order error = %v, want ErrConflict", err)
	}

	// Foreign reads and cancels are indistinguishable from unknown ids.
	if _, err := f.st.TenantOrder("t-other", order.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign TenantOrder error = %v, want ErrNotFound", err)
	}
	if _, err := f.st.CancelTenantOrder("t-other", order.ID, "tenant"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign cancel error = %v, want ErrNotFound", err)
	}

	cancelled, err := f.st.CancelTenantOrder(f.tenant.ID, order.ID, "tenant")
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if cancelled.State != model.OrderCancelled || cancelled.CancelledAt == nil {
		t.Fatalf("cancelled order = %+v", cancelled)
	}
	if len(f.st.Leases()) != 0 || len(f.st.Grants()) != 0 {
		t.Fatalf("cancel granted an entitlement")
	}
	if user, _ := f.st.User(f.user.ID); user.QuotaBytesMonth == nil || *user.QuotaBytesMonth != 1000 {
		t.Fatalf("cancel changed the user quota")
	}
	if again, err := f.st.CancelOrder(order.ID, "operator"); err != nil || again.State != model.OrderCancelled {
		t.Fatalf("cancel replay = %+v, %v", again, err)
	}
	// Cancellation frees the user for a new order.
	second := f.createOrder(t, "")
	if second.ID == order.ID {
		t.Fatalf("new order reused the cancelled id")
	}
}

func TestManualSettlementGrantsOnceAndSurvivesRestart(t *testing.T) {
	f := newOrderFixture(t)
	order := f.createOrder(t, "settle-once")
	event := f.settleEvent(order.ID, "evt-settle-1", "receipt-1")

	active := f.apply(t, event)
	if active.State != model.OrderActive || active.Grant == nil || active.PaidAt == nil || active.ActivatedAt == nil {
		t.Fatalf("settled order = %+v, want active with a grant", active)
	}
	if len(f.st.Leases()) != 1 {
		t.Fatalf("leases = %d, want 1", len(f.st.Leases()))
	}
	user, _ := f.st.User(f.user.ID)
	if user.QuotaBytesMonth == nil || *user.QuotaBytesMonth != f.plan.QuotaBytes {
		t.Fatalf("user quota = %v, want %d", user.QuotaBytesMonth, f.plan.QuotaBytes)
	}
	grant := active.Grant
	if grant.PreviousQuotaBytes == nil || *grant.PreviousQuotaBytes != 1000 {
		t.Fatalf("grant previous quota = %v, want 1000", grant.PreviousQuotaBytes)
	}
	paidAt, _ := leaseExpiry(*active.PaidAt)
	expiresAt, _ := leaseExpiry(grant.ExpiresAt)
	if want := paidAt.Add(time.Duration(f.plan.LeaseTTLSeconds) * time.Second); !expiresAt.Equal(want) {
		t.Fatalf("grant expiry = %s, want paidAt+ttl = %s", expiresAt, want)
	}
	if len(f.st.PaymentEvents()) != 1 {
		t.Fatalf("payment events = %d, want 1", len(f.st.PaymentEvents()))
	}

	// Replaying the same provider event must not create a second lease or
	// extend the TTL.
	replay := f.apply(t, event)
	if replay.Grant == nil || replay.Grant.LeaseID != grant.LeaseID || replay.Grant.ExpiresAt != grant.ExpiresAt {
		t.Fatalf("replay grant = %+v, want unchanged %+v", replay.Grant, grant)
	}
	if len(f.st.Leases()) != 1 || len(f.st.PaymentEvents()) != 1 {
		t.Fatalf("replay duplicated state: leases=%d events=%d", len(f.st.Leases()), len(f.st.PaymentEvents()))
	}

	// A restart restores the order, the grant and the event; a replay after
	// restore still grants nothing new.
	snapshot, err := f.st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	restored := newEmptyWithClock(func() time.Time { return *f.now }, nil)
	if err := restored.Restore(snapshot); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	restoredOrder, ok := restored.Order(order.ID)
	if !ok || restoredOrder.State != model.OrderActive || restoredOrder.Grant == nil {
		t.Fatalf("restored order = %+v, ok=%t", restoredOrder, ok)
	}
	if _, err := restored.ApplyPaymentEvent(event.Provider, event, "operator"); err != nil {
		t.Fatalf("replay after restore: %v", err)
	}
	if len(restored.Leases()) != 1 || len(restored.PaymentEvents()) != 1 {
		t.Fatalf("restore replay duplicated state: leases=%d events=%d",
			len(restored.Leases()), len(restored.PaymentEvents()))
	}
}

func TestSettlementWhileSuspendedStaysPaidThenRetries(t *testing.T) {
	f := newOrderFixture(t)
	order := f.createOrder(t, "suspended")
	if _, err := f.st.SetUserEnabled(f.user.ID, false); err != nil {
		t.Fatalf("suspend user: %v", err)
	}
	event := f.settleEvent(order.ID, "evt-suspended", "receipt-suspended")

	paid := f.apply(t, event)
	if paid.State != model.OrderPaid || paid.Grant != nil || paid.PaidAt == nil {
		t.Fatalf("settled while suspended = %+v, want paid without grant", paid)
	}
	if len(f.st.Leases()) != 0 {
		t.Fatalf("suspended activation created a lease")
	}
	if !containsAction(auditActions(f.st), "payment.activation_retry") {
		t.Fatalf("missing payment.activation_retry audit: %v", auditActions(f.st))
	}
	// A duplicate event retries activation, still blocked.
	if again := f.apply(t, event); again.State != model.OrderPaid || len(f.st.Leases()) != 0 {
		t.Fatalf("retry while suspended = %+v, leases=%d", again, len(f.st.Leases()))
	}

	if _, err := f.st.SetUserEnabled(f.user.ID, true); err != nil {
		t.Fatalf("resume user: %v", err)
	}
	active := f.apply(t, event)
	if active.State != model.OrderActive || active.Grant == nil || len(f.st.Leases()) != 1 {
		t.Fatalf("retry after resume = %+v, leases=%d", active, len(f.st.Leases()))
	}
	if len(f.st.PaymentEvents()) != 1 {
		t.Fatalf("payment events = %d, want one settlement fact", len(f.st.PaymentEvents()))
	}
}

func TestPartialRefundReviewAndFullRefundRevocation(t *testing.T) {
	f := newOrderFixture(t)
	order := f.createOrder(t, "refund")
	active := f.apply(t, f.settleEvent(order.ID, "evt-refund-settle", "receipt-settle"))
	grant := active.Grant

	partial := model.PaymentEvent{
		Provider: model.ProviderStripe, ID: "evt-partial", Kind: model.PaymentRefunded,
		OrderID: order.ID, ProviderRef: "pi_1", AmountMinor: f.plan.AmountMinor - 1,
		Currency: f.plan.Currency, OccurredAt: iso(*f.now),
	}
	reviewed := f.apply(t, partial)
	if !reviewed.ReviewRequired || reviewed.State != model.OrderActive {
		t.Fatalf("partial refund = %+v, want active with review", reviewed)
	}
	if reviewed.Grant == nil || reviewed.Grant.RevokedAt != nil || len(f.st.Leases()) != 1 {
		t.Fatalf("partial refund revoked the grant")
	}
	if !containsAction(auditActions(f.st), "refund.review") {
		t.Fatalf("missing refund.review audit")
	}

	full := model.PaymentEvent{
		Provider: model.ProviderStripe, ID: "evt-full", Kind: model.PaymentRefunded,
		OrderID: order.ID, ProviderRef: "pi_1", AmountMinor: f.plan.AmountMinor,
		Currency: f.plan.Currency, OccurredAt: iso(*f.now),
	}
	refunded := f.apply(t, full)
	if refunded.State != model.OrderRefunded || refunded.RefundedAt == nil {
		t.Fatalf("full refund = %+v, want refunded", refunded)
	}
	if refunded.Grant == nil || refunded.Grant.RevokedAt == nil || refunded.Grant.LeaseID != grant.LeaseID {
		t.Fatalf("full refund grant = %+v, want revoked original grant", refunded.Grant)
	}
	if len(f.st.Leases()) != 0 {
		t.Fatalf("full refund left a lease")
	}
	user, _ := f.st.User(f.user.ID)
	if user.QuotaBytesMonth == nil || *user.QuotaBytesMonth != 1000 {
		t.Fatalf("full refund quota = %v, want restored 1000", user.QuotaBytesMonth)
	}
	if !containsAction(auditActions(f.st), "refund.completed") ||
		!containsAction(auditActions(f.st), "entitlement.revoked") {
		t.Fatalf("missing refund audit rows: %v", auditActions(f.st))
	}
	if again := f.apply(t, full); again.State != model.OrderRefunded || len(f.st.Leases()) != 0 {
		t.Fatalf("refund replay = %+v", again)
	}
	if got := len(f.st.PaymentEvents()); got != 3 {
		t.Fatalf("payment events = %d, want settlement+partial+full", got)
	}
}

func TestRefundPolicyHookCanDeny(t *testing.T) {
	f := newOrderFixture(t)
	order := f.createOrder(t, "policy")
	active := f.apply(t, f.settleEvent(order.ID, "evt-policy-settle", "receipt-policy"))
	if active.Grant == nil {
		t.Fatalf("settlement did not grant")
	}
	var seen RefundRequest
	f.st.SetRefundPolicy(func(request RefundRequest) RefundDecision {
		seen = request
		if request.Reference == "deny" {
			return RefundDeny
		}
		return RefundReview
	})
	refund := model.PaymentEvent{
		Provider: model.ProviderManual, ID: "evt-policy-refund", Kind: model.PaymentRefunded,
		OrderID: order.ID, ProviderRef: "deny", AmountMinor: f.plan.AmountMinor,
		Currency: f.plan.Currency, OccurredAt: iso(*f.now),
	}
	after := f.apply(t, refund)
	if seen.AmountMinor != f.plan.AmountMinor || seen.Reference != "deny" || seen.Order.ID != order.ID {
		t.Fatalf("policy request = %+v", seen)
	}
	if after.State != model.OrderActive || !after.ReviewRequired {
		t.Fatalf("denied refund = %+v, want unchanged with review", after)
	}
	if after.Grant == nil || after.Grant.RevokedAt != nil || len(f.st.Leases()) != 1 {
		t.Fatalf("denied refund revoked the grant")
	}
	if events := f.st.PaymentEvents(); events[0].Result != model.PaymentReviewOnly {
		t.Fatalf("denied refund event = %+v", events[0])
	}
}

func TestChargebackRevokesAndFlagsReview(t *testing.T) {
	f := newOrderFixture(t)
	order := f.createOrder(t, "chargeback")
	f.apply(t, f.settleEvent(order.ID, "evt-cb-settle", "receipt-cb"))
	chargeback := model.PaymentEvent{
		Provider: model.ProviderManual, ID: "evt-cb", Kind: model.PaymentChargedBack,
		OrderID: order.ID, ProviderRef: "dispute-1", AmountMinor: f.plan.AmountMinor,
		Currency: f.plan.Currency, OccurredAt: iso(*f.now),
	}
	after := f.apply(t, chargeback)
	if after.State != model.OrderChargedBack || !after.ReviewRequired {
		t.Fatalf("chargeback = %+v, want charged_back with review", after)
	}
	if after.Grant == nil || after.Grant.RevokedAt == nil || len(f.st.Leases()) != 0 {
		t.Fatalf("chargeback did not revoke the grant")
	}
	if after.PaidAt == nil || after.RefundedAt != nil {
		t.Fatalf("chargeback deleted or invented payment history: %+v", after)
	}
	if !containsAction(auditActions(f.st), "chargeback.recorded") {
		t.Fatalf("missing chargeback.recorded audit")
	}
	// A replay is a no-op; a second event ID is a terminal replay too.
	if again := f.apply(t, chargeback); again.State != model.OrderChargedBack {
		t.Fatalf("chargeback replay = %+v", again)
	}
	other := chargeback
	other.ID = "evt-cb-2"
	if again := f.apply(t, other); again.State != model.OrderChargedBack || again.ReviewRequired != true {
		t.Fatalf("second chargeback = %+v", again)
	}
}

func TestLateSettlementAndMismatchQuarantineWithoutGrant(t *testing.T) {
	f := newOrderFixture(t)
	order := f.createOrder(t, "late")
	if _, err := f.st.CancelOrder(order.ID, "tenant"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	late := f.apply(t, f.settleEvent(order.ID, "evt-late", "receipt-late"))
	if late.State != model.OrderCancelled || !late.ReviewRequired || late.Grant != nil {
		t.Fatalf("late settlement = %+v, want cancelled with review and no grant", late)
	}
	if len(f.st.Leases()) != 0 {
		t.Fatalf("late settlement created a lease")
	}
	if events := f.st.PaymentEvents(); len(events) != 1 || events[0].Result != model.PaymentReviewOnly {
		t.Fatalf("late settlement event = %+v", events)
	}

	// Plan edits never rewrite an order snapshot: a webhook carrying the new
	// price is a mismatch and grants nothing; the snapshot price settles.
	second := f.createOrder(t, "snapshot")
	amount := int64(9999)
	if _, err := f.st.UpdatePlan(f.plan.ID, model.PlanPatch{AmountMinor: &amount}); err != nil {
		t.Fatalf("update plan: %v", err)
	}
	stale := model.PaymentEvent{
		Provider: model.ProviderStripe, ID: "evt-stale", Kind: model.PaymentSettled,
		OrderID: second.ID, ProviderRef: "pi_stale", AmountMinor: amount,
		Currency: f.plan.Currency, OccurredAt: iso(*f.now),
	}
	after := f.apply(t, stale)
	if after.State != model.OrderAwaitingPayment || !after.ReviewRequired || after.Grant != nil {
		t.Fatalf("mismatched settlement = %+v, want awaiting with review and no grant", after)
	}
	correct := model.PaymentEvent{
		Provider: model.ProviderStripe, ID: "evt-correct", Kind: model.PaymentSettled,
		OrderID: second.ID, ProviderRef: "pi_correct", AmountMinor: second.Plan.AmountMinor,
		Currency: second.Plan.Currency, OccurredAt: iso(*f.now),
	}
	activated := f.apply(t, correct)
	if activated.State != model.OrderActive || activated.Grant == nil {
		t.Fatalf("correct settlement after review = %+v", activated)
	}
	if second.Plan.AmountMinor != 3000 {
		t.Fatalf("order snapshot changed to %d", second.Plan.AmountMinor)
	}
}

func TestConcurrentSettlementGrantsOnce(t *testing.T) {
	f := newOrderFixture(t)
	order := f.createOrder(t, "concurrent")
	event := f.settleEvent(order.ID, "evt-concurrent", "receipt-concurrent")
	const workers = 8
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.st.ApplyPaymentEvent(event.Provider, event, "operator")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent apply: %v", err)
		}
	}
	if len(f.st.Leases()) != 1 {
		t.Fatalf("concurrent settlement created %d leases, want 1", len(f.st.Leases()))
	}
	if len(f.st.PaymentEvents()) != 1 {
		t.Fatalf("concurrent settlement recorded %d events, want 1", len(f.st.PaymentEvents()))
	}
	stored, _ := f.st.Order(order.ID)
	if stored.State != model.OrderActive || stored.Grant == nil {
		t.Fatalf("stored order = %+v", stored)
	}
}

func TestConcurrentOrderCreationKeepsOneOpenOrder(t *testing.T) {
	f := newOrderFixture(t)
	const workers = 8
	type outcome struct {
		order    model.Order
		replayed bool
		err      error
	}
	results := make(chan outcome, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			order, replayed, err := f.st.CreateOrder(
				f.tenant.ID, f.user.ID, f.plan.ID, fmt.Sprintf("race-%d", i), "tenant")
			results <- outcome{order: order, replayed: replayed, err: err}
		}()
	}
	wg.Wait()
	close(results)
	created, conflicts := 0, 0
	for result := range results {
		switch {
		case result.err == nil:
			created++
		case errors.Is(result.err, ErrConflict):
			conflicts++
		default:
			t.Fatalf("concurrent create error = %v", result.err)
		}
	}
	if created != 1 || conflicts != workers-1 {
		t.Fatalf("concurrent create: created=%d conflicts=%d", created, conflicts)
	}
	if got := len(f.st.Orders()); got != 1 {
		t.Fatalf("orders = %d, want 1", got)
	}

	// Concurrent retries of one idempotency key all return the same order and
	// create nothing new. Cancel the open order first so the initial create is
	// not refused by the one-open-order guard.
	orders := f.st.Orders()
	if _, err := f.st.CancelOrder(orders[0].ID, "tenant"); err != nil {
		t.Fatalf("cancel before replay: %v", err)
	}
	shared, _, err := f.st.CreateOrder(f.tenant.ID, f.user.ID, f.plan.ID, "race-shared", "tenant")
	if err != nil {
		t.Fatalf("create shared key: %v", err)
	}
	replayResults := make(chan outcome, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			order, replayed, err := f.st.CreateOrder(
				f.tenant.ID, f.user.ID, f.plan.ID, "race-shared", "tenant")
			replayResults <- outcome{order: order, replayed: replayed, err: err}
		}()
	}
	wg.Wait()
	close(replayResults)
	firstID := shared.ID
	for result := range replayResults {
		if result.err != nil {
			t.Fatalf("concurrent replay error = %v", result.err)
		}
		if firstID == "" {
			firstID = result.order.ID
		}
		if result.order.ID != firstID {
			t.Fatalf("concurrent replay ids differ: %s vs %s", result.order.ID, firstID)
		}
	}
	if got := len(f.st.Orders()); got != 2 {
		t.Fatalf("orders after concurrent replay = %d, want 2", got)
	}
}

func TestRenewalReversalDoesNotClobberNewerQuota(t *testing.T) {
	f := newOrderFixture(t)
	first := f.createOrder(t, "renewal-1")
	f.apply(t, f.settleEvent(first.ID, "evt-renewal-1", "receipt-renewal-1"))
	*f.now = f.now.Add(time.Duration(f.plan.LeaseTTLSeconds)*time.Second + time.Second)

	secondPlan, err := f.st.CreatePlan(model.PlanInput{
		Name: "Monthly 20 GiB", AmountMinor: 5000, Currency: "CNY",
		LeaseTTLSeconds: 7200, QuotaBytes: 20 << 30,
	})
	if err != nil {
		t.Fatalf("CreatePlan(second): %v", err)
	}
	second, _, err := f.st.CreateOrder(f.tenant.ID, f.user.ID, secondPlan.ID, "renewal-2", "tenant")
	if err != nil {
		t.Fatalf("CreateOrder(second): %v", err)
	}
	f.apply(t, f.settleEvent(second.ID, "evt-renewal-2", "receipt-renewal-2"))
	if user, _ := f.st.User(f.user.ID); user.QuotaBytesMonth == nil || *user.QuotaBytesMonth != secondPlan.QuotaBytes {
		t.Fatalf("renewal quota = %v, want %d", user.QuotaBytesMonth, secondPlan.QuotaBytes)
	}

	// Reversing the expired older order must not restore its quota over the
	// renewal's quota.
	if _, err := f.st.RecordManualRefund(first.ID, "receipt-old-refund", "", "operator"); err != nil {
		t.Fatalf("refund older order: %v", err)
	}
	user, _ := f.st.User(f.user.ID)
	if user.QuotaBytesMonth == nil || *user.QuotaBytesMonth != secondPlan.QuotaBytes {
		t.Fatalf("older reversal clobbered the newer quota: %v", user.QuotaBytesMonth)
	}
	if len(f.st.Leases()) != 1 {
		t.Fatalf("leases = %d, want the renewal lease only", len(f.st.Leases()))
	}
}

func TestExpiryPermitsRenewal(t *testing.T) {
	f := newOrderFixture(t)
	order := f.createOrder(t, "expiry")
	f.apply(t, f.settleEvent(order.ID, "evt-expiry", "receipt-expiry"))
	if _, _, err := f.st.CreateOrder(f.tenant.ID, f.user.ID, f.plan.ID, "", "tenant"); !errors.Is(err, ErrConflict) {
		t.Fatalf("renewal before expiry error = %v, want ErrConflict", err)
	}
	*f.now = f.now.Add(time.Duration(f.plan.LeaseTTLSeconds)*time.Second + time.Second)
	renewal := f.createOrder(t, "renewal")
	if renewal.State != model.OrderAwaitingPayment {
		t.Fatalf("renewal = %+v", renewal)
	}
	if len(f.st.Leases()) != 0 {
		t.Fatalf("expiry sweep left %d leases", len(f.st.Leases()))
	}
	// The expired order is still readable with its revoked history intact.
	old, _ := f.st.Order(order.ID)
	if old.State != model.OrderActive || old.Grant == nil || old.Grant.RevokedAt != nil {
		t.Fatalf("expired order history = %+v", old)
	}
}

func TestRestoreQuarantinesMalformedOrder(t *testing.T) {
	f := newOrderFixture(t)
	order := f.createOrder(t, "quarantine")
	f.apply(t, f.settleEvent(order.ID, "evt-quarantine", "receipt-quarantine"))
	data, err := f.st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if len(snap.Orders) != 1 {
		t.Fatalf("snapshot orders = %d", len(snap.Orders))
	}
	snap.Orders[0].Plan.AmountMinor = 0
	corrupt, err := json.Marshal(&snap)
	if err != nil {
		t.Fatalf("encode corrupt snapshot: %v", err)
	}
	restored := newEmptyWithClock(func() time.Time { return *f.now }, nil)
	if err := restored.Restore(corrupt); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	quarantined, ok := restored.Order(order.ID)
	if !ok {
		t.Fatalf("quarantined order disappeared")
	}
	if !quarantined.ReviewRequired || quarantined.Grant != nil || quarantined.State != model.OrderCancelled {
		t.Fatalf("quarantined order = %+v", quarantined)
	}
	if len(restored.Leases()) != 0 {
		// The quarantined order's lease is dropped with its grant: a corrupt
		// snapshot must not keep service running.
		t.Fatalf("restored leases = %d, want 0", len(restored.Leases()))
	}
}

// reviveOrderNode puts a node demoted by a simulated daemon restart back into
// service with the lease-visible endpoint it had before. The re-issue path
// targets a live node; the restart itself leaves every node STALE until the
// runtime reconnects it.
func reviveOrderNode(t *testing.T, st *Store, lease model.Lease) {
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

// TestReissueOrderLeaseAfterDaemonRestart is the AJS-395 acceptance test: a
// restart ends the grant's lease with reason daemon_restart, but the paid order
// must be able to obtain a replacement lease without a second grant or charge.
func TestReissueOrderLeaseAfterDaemonRestart(t *testing.T) {
	f := newOrderFixture(t)
	order := f.createOrder(t, "reissue")
	active := f.apply(t, f.settleEvent(order.ID, "evt-reissue", "receipt-reissue"))
	if active.State != model.OrderActive || active.Grant == nil {
		t.Fatalf("settled order = %+v, want an active grant", active)
	}
	oldLeaseID := active.Grant.LeaseID
	oldExpiry := active.Grant.ExpiresAt
	oldLease, ok := f.st.Lease(oldLeaseID)
	if !ok {
		t.Fatalf("grant lease %q missing", oldLeaseID)
	}
	paymentsBefore := len(f.st.PaymentEvents())
	grantsBefore := len(f.st.Grants())

	if _, _, _, err := f.st.DemoteNode(oldLease.NodeID); err != nil {
		t.Fatalf("DemoteNode: %v", err)
	}
	ends := f.st.LeaseEnds()
	if len(ends) != 1 || ends[0].Lease.ID != oldLeaseID || ends[0].Reason != "daemon_restart" {
		t.Fatalf("lease ends = %+v, want one daemon_restart row for %s", ends, oldLeaseID)
	}
	reviveOrderNode(t, f.st, oldLease)

	after, lease, err := f.st.ReissueOrderLease(order.ID, "operator")
	if err != nil {
		t.Fatalf("ReissueOrderLease: %v", err)
	}
	if after.State != model.OrderActive || after.Grant == nil {
		t.Fatalf("reissued order = %+v, want active with a grant", after)
	}
	if lease.ID == oldLeaseID || after.Grant.LeaseID != lease.ID {
		t.Fatalf("grant lease = %s, new lease = %s, old = %s", after.Grant.LeaseID, lease.ID, oldLeaseID)
	}
	if after.Grant.ExpiresAt != oldExpiry || lease.ExpiresAt != oldExpiry {
		t.Fatalf("reissue extended the paid term: grant %s, lease %s, want %s",
			after.Grant.ExpiresAt, lease.ExpiresAt, oldExpiry)
	}
	if after.Grant.RevokedAt != nil {
		t.Fatalf("reissued grant is revoked: %+v", after.Grant)
	}
	if len(f.st.Leases()) != 1 {
		t.Fatalf("active leases = %d, want exactly one replacement", len(f.st.Leases()))
	}
	if len(f.st.Grants()) != grantsBefore {
		t.Fatalf("grants = %d, want the original %d (no second grant)", len(f.st.Grants()), grantsBefore)
	}
	if len(f.st.PaymentEvents()) != paymentsBefore {
		t.Fatalf("payment events = %d, want the original %d (no second charge)",
			len(f.st.PaymentEvents()), paymentsBefore)
	}
	audit := findAudit(t, f.st, "lease.reissued", func(event model.AuditEvent) bool {
		return event.Meta["orderId"] == order.ID
	})
	if audit.Meta["previousLeaseId"] != oldLeaseID || audit.Meta["leaseId"] != lease.ID ||
		audit.Meta["reason"] != "daemon_restart" {
		t.Fatalf("lease.reissued meta = %v", audit.Meta)
	}

	// A retry while the replacement is live is refused, not allocated twice.
	if _, _, err := f.st.ReissueOrderLease(order.ID, "operator"); !errors.Is(err, ErrConflict) {
		t.Fatalf("second reissue error = %v, want ErrConflict", err)
	}
	if len(f.st.Leases()) != 1 || countAudit(f.st, "lease.reissued") != 1 {
		t.Fatalf("second reissue changed state: leases=%d audit=%d",
			len(f.st.Leases()), countAudit(f.st, "lease.reissued"))
	}

	// The replacement grant survives a save/load cycle.
	data, err := f.st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	restored := newEmptyWithClock(func() time.Time { return *f.now }, nil)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	stored, ok := restored.Order(order.ID)
	if !ok || stored.Grant == nil || stored.Grant.LeaseID != lease.ID {
		t.Fatalf("restored order grant = %+v, want lease %s", stored.Grant, lease.ID)
	}
	if len(restored.Leases()) != 1 || restored.Leases()[0].ID != lease.ID {
		t.Fatalf("restored leases = %+v, want %s", restored.Leases(), lease.ID)
	}
}

// TestEndedLeaseDoesNotBlockNewOrder is the AJS-395(c) falsifier: before the
// fix an active order whose grant lease had ended still returned the 409
// "already holds an unexpired entitlement" on a new order.
func TestEndedLeaseDoesNotBlockNewOrder(t *testing.T) {
	f := newOrderFixture(t)
	order := f.createOrder(t, "ended-lease")
	active := f.apply(t, f.settleEvent(order.ID, "evt-ended", "receipt-ended"))
	if active.Grant == nil {
		t.Fatalf("settled order has no grant")
	}
	lease, ok := f.st.Lease(active.Grant.LeaseID)
	if !ok {
		t.Fatalf("grant lease missing")
	}
	if _, _, _, err := f.st.DemoteNode(lease.NodeID); err != nil {
		t.Fatalf("DemoteNode: %v", err)
	}
	renewal, replayed, err := f.st.CreateOrder(f.tenant.ID, f.user.ID, f.plan.ID, "after-restart", "tenant")
	if err != nil {
		t.Fatalf("CreateOrder after an ended lease: %v", err)
	}
	if replayed || renewal.State != model.OrderAwaitingPayment {
		t.Fatalf("renewal = %+v, replayed=%t", renewal, replayed)
	}
}

// TestReissueOrderLeaseRefusesExpiredGrant proves the re-issue path cannot
// extend service beyond the paid term.
func TestReissueOrderLeaseRefusesExpiredGrant(t *testing.T) {
	f := newOrderFixture(t)
	order := f.createOrder(t, "reissue-expired")
	active := f.apply(t, f.settleEvent(order.ID, "evt-reissue-expired", "receipt-reissue-expired"))
	if active.Grant == nil {
		t.Fatalf("settled order has no grant")
	}
	*f.now = f.now.Add(time.Duration(f.plan.LeaseTTLSeconds)*time.Second + time.Second)
	if _, _, err := f.st.ReissueOrderLease(order.ID, "operator"); !errors.Is(err, ErrConflict) {
		t.Fatalf("reissue expired grant error = %v, want ErrConflict", err)
	}
	if len(f.st.Leases()) != 0 {
		t.Fatalf("expired reissue created %d leases", len(f.st.Leases()))
	}
}

// TestConcurrentReissueAllocatesOneReplacement proves the grant pointer is the
// transaction's single-winner guard even when every request races the restart.
func TestConcurrentReissueAllocatesOneReplacement(t *testing.T) {
	f := newOrderFixture(t)
	order := f.createOrder(t, "reissue-concurrent")
	active := f.apply(t, f.settleEvent(order.ID, "evt-reissue-concurrent", "receipt-reissue-concurrent"))
	oldLease, ok := f.st.Lease(active.Grant.LeaseID)
	if !ok {
		t.Fatal("original grant lease missing")
	}
	if _, _, _, err := f.st.DemoteNode(oldLease.NodeID); err != nil {
		t.Fatalf("DemoteNode: %v", err)
	}
	reviveOrderNode(t, f.st, oldLease)
	const attempts = 12
	start := make(chan struct{})
	errs := make(chan error, attempts)
	var wg sync.WaitGroup
	for range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, _, err := f.st.ReissueOrderLease(order.ID, "operator")
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	succeeded := 0
	for err := range errs {
		if err == nil {
			succeeded++
		} else if !errors.Is(err, ErrConflict) {
			t.Fatalf("concurrent reissue unexpected error: %v", err)
		}
	}
	if succeeded != 1 || len(f.st.Leases()) != 1 || countAudit(f.st, "lease.reissued") != 1 {
		t.Fatalf("concurrent reissue succeeded=%d leases=%d audits=%d; want exactly one",
			succeeded, len(f.st.Leases()), countAudit(f.st, "lease.reissued"))
	}
}

// TestReissueRefusesAnotherActiveUserLease proves replacing an ended grant
// does not bypass the ordinary one-active-lease-per-user/platform invariant.
func TestReissueRefusesAnotherActiveUserLease(t *testing.T) {
	f := newOrderFixture(t)
	order := f.createOrder(t, "reissue-other-lease")
	active := f.apply(t, f.settleEvent(order.ID, "evt-reissue-other", "receipt-reissue-other"))
	oldLeaseID := active.Grant.LeaseID
	oldLease, ok := f.st.Lease(oldLeaseID)
	if !ok {
		t.Fatalf("original grant lease %q missing", oldLeaseID)
	}
	if _, _, _, err := f.st.DemoteNode(oldLease.NodeID); err != nil {
		t.Fatalf("DemoteNode: %v", err)
	}
	reviveOrderNode(t, f.st, oldLease)
	secondAccount, err := f.st.CreateAccount(model.CreateAccountInput{
		Label: "Orders Spare", Username: "orders-spare", Password: "local-placeholder",
		Platform: model.PlatformAndroid, Concurrency: 2,
	})
	if err != nil {
		t.Fatalf("CreateAccount(second): %v", err)
	}
	if _, err := f.st.PutNode(model.ProxyNode{
		ID: "node-reissue-other", AccountID: secondAccount.ID, Label: "orders-2",
		Region: oldLease.Region, State: model.NodeLive, EgressIP: "203.0.113.91",
		Protocol: model.ProtocolSOCKS5, GatePort: 13091,
	}); err != nil {
		t.Fatalf("PutNode(second): %v", err)
	}
	other, _, err := f.st.CreateLease(model.CreateLeaseInput{
		UserID: f.user.ID, Platform: model.PlatformAndroid,
	})
	if err != nil {
		t.Fatalf("CreateLease(other): %v", err)
	}
	if _, _, err := f.st.ReissueOrderLease(order.ID, "operator"); !errors.Is(err, ErrConflict) {
		t.Fatalf("reissue with another active user lease = %v, want ErrConflict", err)
	}
	stored, ok := f.st.Order(order.ID)
	if !ok || stored.Grant == nil || stored.Grant.LeaseID != oldLeaseID {
		t.Fatalf("grant changed after refused reissue: %+v", stored.Grant)
	}
	leases := f.st.Leases()
	if len(leases) != 1 || leases[0].ID != other.ID {
		t.Fatalf("refused reissue changed active leases: %+v", leases)
	}
}

// TestReissueCannotUseFallbackBesideAnotherPlatformLease covers the indirect
// portal-create path: a saturated preferred platform falls through in target
// selection, but a current lease must refuse that fallback rather than mint a
// second active entitlement for the same user.
func TestReissueCannotUseFallbackBesideAnotherPlatformLease(t *testing.T) {
	f := newOrderFixture(t)
	order := f.createOrder(t, "reissue-fallback-conflict")
	active := f.apply(t, f.settleEvent(order.ID, "evt-reissue-fallback", "receipt-reissue-fallback"))
	oldLease, ok := f.st.Lease(active.Grant.LeaseID)
	if !ok {
		t.Fatal("original grant lease missing")
	}
	if _, _, _, err := f.st.DemoteNode(oldLease.NodeID); err != nil {
		t.Fatalf("DemoteNode: %v", err)
	}
	reviveOrderNode(t, f.st, oldLease)
	iosAccount, err := f.st.CreateAccount(model.CreateAccountInput{
		Label: "Orders iOS", Username: "orders-ios", Password: "local-placeholder",
		Platform: model.PlatformIOS, Concurrency: 2,
	})
	if err != nil {
		t.Fatalf("CreateAccount(iOS): %v", err)
	}
	if _, err := f.st.PutNode(model.ProxyNode{
		ID: "node-reissue-ios", AccountID: iosAccount.ID, Label: "orders-ios",
		Region: oldLease.Region, State: model.NodeLive, EgressIP: "203.0.113.92",
		Protocol: model.ProtocolSOCKS5, GatePort: 13092,
	}); err != nil {
		t.Fatalf("PutNode(iOS): %v", err)
	}
	// The only Android node is already held by the user's current lease;
	// without the conflict guard, reissue would fall back to the iOS node.
	current, _, err := f.st.CreateLease(model.CreateLeaseInput{
		UserID: f.user.ID, Platform: model.PlatformAndroid,
	})
	if err != nil {
		t.Fatalf("CreateLease(current): %v", err)
	}
	if _, candidate := f.st.ReissueCandidate(f.tenant.ID, f.user.ID, f.plan.ID); !candidate {
		t.Fatal("ended paid grant was not a reissue candidate")
	}
	if _, _, err := f.st.ReissueOrderLease(order.ID, "operator"); !errors.Is(err, ErrConflict) {
		t.Fatalf("fallback reissue beside another-platform lease = %v, want ErrConflict", err)
	}
	leases := f.st.Leases()
	if len(leases) != 1 || leases[0].ID != current.ID {
		t.Fatalf("refused fallback changed active leases: %+v", leases)
	}
	stored, _ := f.st.Order(order.ID)
	if stored.Grant == nil || stored.Grant.LeaseID != oldLease.ID {
		t.Fatalf("refused fallback changed grant: %+v", stored.Grant)
	}
}

// TestRollbackOrderReissueRestoresEndedGrant proves a publication failure can
// be compensated without losing the paid grant or leaving a second lease.
func TestRollbackOrderReissueRestoresEndedGrant(t *testing.T) {
	f := newOrderFixture(t)
	order := f.createOrder(t, "reissue-rollback")
	active := f.apply(t, f.settleEvent(order.ID, "evt-reissue-rollback", "receipt-reissue-rollback"))
	oldLeaseID := active.Grant.LeaseID
	oldLease, ok := f.st.Lease(oldLeaseID)
	if !ok {
		t.Fatal("original grant lease missing")
	}
	if _, _, _, err := f.st.DemoteNode(oldLease.NodeID); err != nil {
		t.Fatalf("DemoteNode: %v", err)
	}
	reviveOrderNode(t, f.st, oldLease)
	_, replacement, err := f.st.ReissueOrderLease(order.ID, "operator")
	if err != nil {
		t.Fatalf("ReissueOrderLease: %v", err)
	}
	if err := f.st.RollbackOrderReissue(order.ID, replacement.ID, oldLeaseID, "panel unavailable"); err != nil {
		t.Fatalf("RollbackOrderReissue: %v", err)
	}
	stored, ok := f.st.Order(order.ID)
	if !ok || stored.Grant == nil || stored.Grant.LeaseID != oldLeaseID {
		t.Fatalf("rolled-back grant = %+v, want original %s", stored.Grant, oldLeaseID)
	}
	if len(f.st.Leases()) != 0 {
		t.Fatalf("rollback left active leases: %+v", f.st.Leases())
	}
	node, _ := f.st.Node(oldLease.NodeID)
	if node.InUse != 0 {
		t.Fatalf("rollback left node inUse=%d", node.InUse)
	}
	if !containsAction(auditActions(f.st), "lease.publish_failed") {
		t.Fatal("rollback wrote no lease.publish_failed audit")
	}
}

// TestActivationRetryDoesNotTreatEndedGrantLeaseAsActive proves a paid order
// whose grant points at an ended lease stays paid on a settlement retry; it
// must not be flipped back to active without a serving lease.
func TestActivationRetryDoesNotTreatEndedGrantLeaseAsActive(t *testing.T) {
	f := newOrderFixture(t)
	order := f.createOrder(t, "retry-ended")
	active := f.apply(t, f.settleEvent(order.ID, "evt-retry-ended", "receipt-retry-ended"))
	if active.Grant == nil {
		t.Fatalf("settled order has no grant")
	}
	lease, ok := f.st.Lease(active.Grant.LeaseID)
	if !ok {
		t.Fatalf("grant lease missing")
	}
	if _, _, _, err := f.st.DemoteNode(lease.NodeID); err != nil {
		t.Fatalf("DemoteNode: %v", err)
	}

	// White-box: model the retry state a restored or interrupted activation
	// can leave behind, where the order is paid but still carries its grant.
	f.st.mu.Lock()
	idx := f.st.orderIndexLocked(order.ID)
	f.st.orders[idx].Order.State = model.OrderPaid
	f.st.mu.Unlock()

	after := f.apply(t, f.settleEvent(order.ID, "evt-retry-ended", "receipt-retry-ended"))
	if after.State != model.OrderPaid {
		t.Fatalf("retry state = %s, want paid with the ended lease", after.State)
	}
	if !containsAction(auditActions(f.st), "payment.activation_retry") {
		t.Fatal("no payment.activation_retry audit row")
	}
	if len(f.st.Leases()) != 0 {
		t.Fatalf("retry created %d leases without a live grant", len(f.st.Leases()))
	}
}

// paidOrderWithoutGrant settles an order while the fixture's user is
// suspended. The existing settlement path then records the payment and leaves
// the order paid without a grant, which is the state an initial fulfilment job
// starts from.
func (f *orderFixture) paidOrderWithoutGrant(t *testing.T, key string) model.Order {
	t.Helper()
	order := f.createOrder(t, key)
	if _, err := f.st.SetUserEnabled(f.user.ID, false); err != nil {
		t.Fatalf("suspend user: %v", err)
	}
	paid := f.apply(t, f.settleEvent(order.ID, "evt-"+key, "receipt-"+key))
	if paid.State != model.OrderPaid || paid.Grant != nil {
		t.Fatalf("settled while suspended = %+v, want paid without a grant", paid)
	}
	return paid
}

func enqueueInitial(t *testing.T, st *Store, orderID string) model.Order {
	t.Helper()
	order, err := st.EnqueueFulfilment(orderID, model.FulfilmentInitial)
	if err != nil {
		t.Fatalf("EnqueueFulfilment(%s, initial): %v", orderID, err)
	}
	return order
}

func fulfilmentJobOf(t *testing.T, st *Store, orderID string) model.FulfilmentJob {
	t.Helper()
	order, ok := st.Order(orderID)
	if !ok {
		t.Fatalf("order %q missing", orderID)
	}
	if order.Fulfilment == nil {
		t.Fatalf("order %q has no fulfilment job", orderID)
	}
	return *order.Fulfilment
}

func requireNextAttempt(t *testing.T, job model.FulfilmentJob, want time.Time) {
	t.Helper()
	if job.NextAttemptAt == nil {
		t.Fatalf("job in state %s has no next attempt, want %s", job.State, iso(want))
	}
	got, ok := leaseExpiry(*job.NextAttemptAt)
	if !ok || !got.Equal(want) {
		t.Fatalf("next attempt = %s, want %s", *job.NextAttemptAt, iso(want))
	}
}

func TestEnqueueFulfilmentIsIdempotentPerOrder(t *testing.T) {
	f := newOrderFixture(t)
	paid := f.paidOrderWithoutGrant(t, "enqueue")
	if paid.Fulfilment != nil {
		t.Fatalf("paid order already carries a job: %+v", paid.Fulfilment)
	}

	queued := enqueueInitial(t, f.st, paid.ID)
	job := queued.Fulfilment
	if job == nil || job.OrderID != paid.ID || job.Mode != model.FulfilmentInitial ||
		job.State != model.FulfilmentQueued || job.Attempts != 0 || job.AttemptLeaseID != "" {
		t.Fatalf("enqueued job = %+v", job)
	}
	requireNextAttempt(t, *job, *f.now)
	firstUpdated := job.UpdatedAt

	// A replay returns the same job: no second job, no schedule rewrite.
	*f.now = f.now.Add(time.Minute)
	replay := enqueueInitial(t, f.st, paid.ID)
	if replay.Fulfilment == nil || replay.Fulfilment.UpdatedAt != firstUpdated ||
		replay.Fulfilment.Attempts != 0 || replay.Fulfilment.State != model.FulfilmentQueued {
		t.Fatalf("replayed job = %+v, want the original", replay.Fulfilment)
	}

	// A different mode for the same order is a conflict, never a second job.
	if _, err := f.st.EnqueueFulfilment(paid.ID, model.FulfilmentReissue); !errors.Is(err, ErrConflict) {
		t.Fatalf("second mode error = %v, want ErrConflict", err)
	}
	if _, err := f.st.EnqueueFulfilment(paid.ID, "renew"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown mode error = %v, want ErrInvalid", err)
	}
	if _, err := f.st.EnqueueFulfilment("ord-missing", model.FulfilmentInitial); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown order error = %v, want ErrNotFound", err)
	}
}

func TestEnqueueFulfilmentRefusesInapplicableOrders(t *testing.T) {
	f := newOrderFixture(t)
	awaiting := f.createOrder(t, "awaiting")
	if _, err := f.st.EnqueueFulfilment(awaiting.ID, model.FulfilmentInitial); !errors.Is(err, ErrConflict) {
		t.Fatalf("initial job for awaiting order error = %v, want ErrConflict", err)
	}
	if _, err := f.st.EnqueueFulfilment(awaiting.ID, model.FulfilmentReissue); !errors.Is(err, ErrConflict) {
		t.Fatalf("reissue job for awaiting order error = %v, want ErrConflict", err)
	}
	if _, err := f.st.BeginFulfilment(awaiting.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("begin without a job error = %v, want ErrConflict", err)
	}
	if _, err := f.st.AbortFulfilment(awaiting.ID, "lease-x", model.FulfilmentReasonInternal, "", true); !errors.Is(err, ErrConflict) {
		t.Fatalf("abort without a job error = %v, want ErrConflict", err)
	}
	if _, err := f.st.CancelOrder(awaiting.ID, "tenant"); err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}
	if _, err := f.st.EnqueueFulfilment(awaiting.ID, model.FulfilmentInitial); !errors.Is(err, ErrConflict) {
		t.Fatalf("initial job for cancelled order error = %v, want ErrConflict", err)
	}
	if _, err := f.st.BeginFulfilment(awaiting.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("begin on cancelled order error = %v, want ErrConflict", err)
	}
	if _, err := f.st.AbortFulfilment(awaiting.ID, "lease-x", model.FulfilmentReasonInternal, "", true); !errors.Is(err, ErrConflict) {
		t.Fatalf("abort on cancelled order error = %v, want ErrConflict", err)
	}
	if _, err := f.st.BeginFulfilment("ord-missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("begin unknown order error = %v, want ErrNotFound", err)
	}
	if _, err := f.st.AbortFulfilment("ord-missing", "lease-x", model.FulfilmentReasonInternal, "", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("abort unknown order error = %v, want ErrNotFound", err)
	}

	// A paid order accepts only the initial mode.
	paid := f.paidOrderWithoutGrant(t, "paid-only")
	if _, err := f.st.EnqueueFulfilment(paid.ID, model.FulfilmentReissue); !errors.Is(err, ErrConflict) {
		t.Fatalf("reissue job for paid order error = %v, want ErrConflict", err)
	}
}

func TestEnqueueFulfilmentReissueNeedsEndedUnexpiredGrant(t *testing.T) {
	f := newOrderFixture(t)
	order := f.createOrder(t, "reissue-job")
	active := f.apply(t, f.settleEvent(order.ID, "evt-reissue-job", "receipt-reissue-job"))
	if active.State != model.OrderActive || active.Grant == nil {
		t.Fatalf("settled order = %+v, want active with a grant", active)
	}
	// A serving lease has nothing to reissue, and an active order never gets
	// an initial job.
	if _, err := f.st.EnqueueFulfilment(order.ID, model.FulfilmentReissue); !errors.Is(err, ErrConflict) {
		t.Fatalf("reissue while serving error = %v, want ErrConflict", err)
	}
	if _, err := f.st.EnqueueFulfilment(order.ID, model.FulfilmentInitial); !errors.Is(err, ErrConflict) {
		t.Fatalf("initial job for active order error = %v, want ErrConflict", err)
	}

	// End the grant's lease the way a daemon restart does.
	lease, ok := f.st.Lease(active.Grant.LeaseID)
	if !ok {
		t.Fatalf("grant lease %q missing", active.Grant.LeaseID)
	}
	if _, _, _, err := f.st.DemoteNode(lease.NodeID); err != nil {
		t.Fatalf("DemoteNode: %v", err)
	}
	queued, err := f.st.EnqueueFulfilment(order.ID, model.FulfilmentReissue)
	if err != nil {
		t.Fatalf("EnqueueFulfilment(reissue): %v", err)
	}
	if queued.Fulfilment == nil || queued.Fulfilment.Mode != model.FulfilmentReissue ||
		queued.Fulfilment.State != model.FulfilmentQueued || queued.Fulfilment.Attempts != 0 {
		t.Fatalf("reissue job = %+v", queued.Fulfilment)
	}
	attempt, err := f.st.BeginFulfilment(order.ID)
	if err != nil {
		t.Fatalf("BeginFulfilment(reissue): %v", err)
	}
	if attempt.Mode != model.FulfilmentReissue || attempt.Number != 1 || attempt.LeaseID == "" {
		t.Fatalf("reissue attempt = %+v", attempt)
	}

	// A paid term that has expired cannot be reissued.
	f2 := newOrderFixture(t)
	expiring := f2.createOrder(t, "expired")
	f2.apply(t, f2.settleEvent(expiring.ID, "evt-expired", "receipt-expired"))
	*f2.now = f2.now.Add(time.Duration(f2.plan.LeaseTTLSeconds)*time.Second + time.Second)
	if _, err := f2.st.EnqueueFulfilment(expiring.ID, model.FulfilmentReissue); !errors.Is(err, ErrConflict) {
		t.Fatalf("reissue after expiry error = %v, want ErrConflict", err)
	}
}

func TestFulfilmentBeginAbortFencing(t *testing.T) {
	f := newOrderFixture(t)
	paid := f.paidOrderWithoutGrant(t, "fence")
	enqueueInitial(t, f.st, paid.ID)

	// Nothing is in flight before the first begin.
	if _, err := f.st.AbortFulfilment(paid.ID, "lease-stale", model.FulfilmentReasonCapacity, "", true); !errors.Is(err, ErrConflict) {
		t.Fatalf("abort before begin error = %v, want ErrConflict", err)
	}

	attempt, err := f.st.BeginFulfilment(paid.ID)
	if err != nil {
		t.Fatalf("BeginFulfilment: %v", err)
	}
	if attempt.OrderID != paid.ID || attempt.Mode != model.FulfilmentInitial ||
		attempt.Number != 1 || attempt.LeaseID == "" {
		t.Fatalf("first attempt = %+v", attempt)
	}
	job := fulfilmentJobOf(t, f.st, paid.ID)
	if job.State != model.FulfilmentPublishing || job.Attempts != 1 ||
		job.AttemptLeaseID != attempt.LeaseID || job.NextAttemptAt != nil {
		t.Fatalf("publishing job = %+v", job)
	}
	// One in-flight attempt per job.
	if _, err := f.st.BeginFulfilment(paid.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("second begin while publishing error = %v, want ErrConflict", err)
	}

	// A stale attempt lease cannot abort the current attempt.
	if _, err := f.st.AbortFulfilment(paid.ID, "lease-stale", model.FulfilmentReasonCapacity, "", true); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale abort error = %v, want ErrConflict", err)
	}
	if got := fulfilmentJobOf(t, f.st, paid.ID); got.State != model.FulfilmentPublishing || got.Attempts != 1 {
		t.Fatalf("stale abort changed the job: %+v", got)
	}

	queued, err := f.st.AbortFulfilment(paid.ID, attempt.LeaseID, model.FulfilmentReasonCapacity, "", true)
	if err != nil {
		t.Fatalf("AbortFulfilment: %v", err)
	}
	job = *queued.Fulfilment
	if job.State != model.FulfilmentQueued || job.Attempts != 1 ||
		job.AttemptLeaseID != "" || job.LastReason != model.FulfilmentReasonCapacity {
		t.Fatalf("aborted job = %+v", job)
	}
	requireNextAttempt(t, job, f.now.Add(5*time.Second))
	// The same attempt cannot abort twice.
	if _, err := f.st.AbortFulfilment(paid.ID, attempt.LeaseID, model.FulfilmentReasonCapacity, "", true); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate abort error = %v, want ErrConflict", err)
	}

	second, err := f.st.BeginFulfilment(paid.ID)
	if err != nil {
		t.Fatalf("BeginFulfilment(second): %v", err)
	}
	if second.Number != 2 || second.LeaseID == attempt.LeaseID {
		t.Fatalf("second attempt = %+v, want a distinct lease from %q", second, attempt.LeaseID)
	}
	if _, err := f.st.AbortFulfilment(paid.ID, attempt.LeaseID, model.FulfilmentReasonCapacity, "", true); !errors.Is(err, ErrConflict) {
		t.Fatalf("abort with the first attempt lease error = %v, want ErrConflict", err)
	}
	aborted, err := f.st.AbortFulfilment(paid.ID, second.LeaseID, model.FulfilmentReasonCapacity, "", true)
	if err != nil {
		t.Fatalf("AbortFulfilment(second): %v", err)
	}
	if aborted.Fulfilment.Attempts != 2 {
		t.Fatalf("attempts = %d, want 2", aborted.Fulfilment.Attempts)
	}
	requireNextAttempt(t, *aborted.Fulfilment, f.now.Add(15*time.Second))
}

func TestFulfilmentBackoffScheduleAndDeadLetter(t *testing.T) {
	f := newOrderFixture(t)
	paid := f.paidOrderWithoutGrant(t, "backoff")
	enqueueInitial(t, f.st, paid.ID)

	schedule := []time.Duration{
		5 * time.Second, 15 * time.Second, 45 * time.Second,
		2 * time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute,
	}
	for i, delay := range schedule {
		attempt, err := f.st.BeginFulfilment(paid.ID)
		if err != nil {
			t.Fatalf("begin attempt %d: %v", i+1, err)
		}
		if attempt.Number != i+1 {
			t.Fatalf("attempt number = %d, want %d", attempt.Number, i+1)
		}
		aborted, err := f.st.AbortFulfilment(paid.ID, attempt.LeaseID, model.FulfilmentReasonCapacity, "", true)
		if err != nil {
			t.Fatalf("abort attempt %d: %v", i+1, err)
		}
		job := *aborted.Fulfilment
		if job.State != model.FulfilmentQueued || job.Attempts != i+1 {
			t.Fatalf("attempt %d job = %+v", i+1, job)
		}
		requireNextAttempt(t, job, f.now.Add(delay))
		*f.now = f.now.Add(delay)
	}
	if got := countAudit(f.st, "fulfilment.retry"); got != 7 {
		t.Fatalf("fulfilment.retry rows = %d, want 7", got)
	}

	// The eighth failure dead-letters instead of scheduling another retry.
	attempt, err := f.st.BeginFulfilment(paid.ID)
	if err != nil {
		t.Fatalf("begin eighth attempt: %v", err)
	}
	if attempt.Number != MaxFulfilmentAttempts {
		t.Fatalf("attempt number = %d, want %d", attempt.Number, MaxFulfilmentAttempts)
	}
	dead, err := f.st.AbortFulfilment(paid.ID, attempt.LeaseID, model.FulfilmentReasonCapacity, "", true)
	if err != nil {
		t.Fatalf("abort eighth attempt: %v", err)
	}
	job := *dead.Fulfilment
	if job.State != model.FulfilmentDeadLetter || job.Attempts != MaxFulfilmentAttempts ||
		job.AttemptLeaseID != "" || job.NextAttemptAt != nil {
		t.Fatalf("dead-lettered job = %+v", job)
	}
	if _, err := f.st.BeginFulfilment(paid.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("begin after dead letter error = %v, want ErrConflict", err)
	}
	if due := f.st.DueFulfilments(f.now.Add(24 * time.Hour)); len(due) != 0 {
		t.Fatalf("dead-lettered job is due: %+v", due)
	}
	event := findAudit(t, f.st, "fulfilment.dead_letter", func(e model.AuditEvent) bool {
		return e.Meta["orderId"] == paid.ID
	})
	if event.Severity != model.SeverityCritical || event.Meta["attempts"] != "8" {
		t.Fatalf("dead-letter audit = %+v", event)
	}

	// A non-retryable failure dead-letters at attempt one.
	f2 := newOrderFixture(t)
	paid2 := f2.paidOrderWithoutGrant(t, "nonretry")
	enqueueInitial(t, f2.st, paid2.ID)
	first, err := f2.st.BeginFulfilment(paid2.ID)
	if err != nil {
		t.Fatalf("BeginFulfilment(non-retryable): %v", err)
	}
	refused, err := f2.st.AbortFulfilment(paid2.ID, first.LeaseID, model.FulfilmentReasonPublishFailed, "publication refused", false)
	if err != nil {
		t.Fatalf("AbortFulfilment(non-retryable): %v", err)
	}
	if refused.Fulfilment.State != model.FulfilmentDeadLetter ||
		refused.Fulfilment.Attempts != 1 || refused.Fulfilment.NextAttemptAt != nil {
		t.Fatalf("non-retryable job = %+v", refused.Fulfilment)
	}
}

func TestFulfilmentPersistenceRoundtrip(t *testing.T) {
	f := newOrderFixture(t)
	paid := f.paidOrderWithoutGrant(t, "persist")
	enqueueInitial(t, f.st, paid.ID)
	attempt, err := f.st.BeginFulfilment(paid.ID)
	if err != nil {
		t.Fatalf("BeginFulfilment: %v", err)
	}
	if _, err := f.st.AbortFulfilment(paid.ID, attempt.LeaseID, model.FulfilmentReasonCapacity, "", true); err != nil {
		t.Fatalf("AbortFulfilment: %v", err)
	}
	before := fulfilmentJobOf(t, f.st, paid.ID)

	data, err := f.st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if !bytes.Contains(data, []byte(`"fulfilment"`)) {
		t.Fatalf("snapshot does not carry the fulfilment job")
	}
	restored := newEmptyWithClock(func() time.Time { return *f.now }, nil)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	after := fulfilmentJobOf(t, restored, paid.ID)
	if !fulfilmentJobsEqual(before, after) {
		t.Fatalf("restored job = %+v, want %+v", after, before)
	}
	// The restored job continues from its persisted attempt count.
	next, err := restored.BeginFulfilment(paid.ID)
	if err != nil {
		t.Fatalf("BeginFulfilment after restore: %v", err)
	}
	if next.Number != 2 {
		t.Fatalf("attempt after restore = %d, want 2", next.Number)
	}

	// An order without a job keeps the old snapshot shape: the field is
	// omitted and loads back as nil.
	f2 := newOrderFixture(t)
	plain := f2.paidOrderWithoutGrant(t, "no-job")
	plainData, err := f2.st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot(no job): %v", err)
	}
	if bytes.Contains(plainData, []byte(`"fulfilment"`)) {
		t.Fatalf("snapshot without a job carries the field")
	}
	plainRestored := newEmptyWithClock(func() time.Time { return *f2.now }, nil)
	if err := plainRestored.Restore(plainData); err != nil {
		t.Fatalf("Restore(no job): %v", err)
	}
	plainOrder, ok := plainRestored.Order(plain.ID)
	if !ok || plainOrder.Fulfilment != nil || plainOrder.State != model.OrderPaid {
		t.Fatalf("restored plain order = %+v, ok=%t", plainOrder, ok)
	}

	// A job restored beside a terminal order is stale work: the order and its
	// recorded money survive, the job is dropped.
	f3 := newOrderFixture(t)
	refundOrder := f3.paidOrderWithoutGrant(t, "refunded-job")
	enqueueInitial(t, f3.st, refundOrder.ID)
	refunded, err := f3.st.RecordManualRefund(refundOrder.ID, "refund-receipt", "customer request", "operator")
	if err != nil {
		t.Fatalf("RecordManualRefund: %v", err)
	}
	if refunded.State != model.OrderRefunded {
		t.Fatalf("refunded order = %+v", refunded)
	}
	refundData, err := f3.st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot(refunded): %v", err)
	}
	refundRestored := newEmptyWithClock(func() time.Time { return *f3.now }, nil)
	if err := refundRestored.Restore(refundData); err != nil {
		t.Fatalf("Restore(refunded): %v", err)
	}
	stored, ok := refundRestored.Order(refundOrder.ID)
	if !ok || stored.State != model.OrderRefunded || stored.Fulfilment != nil ||
		stored.RefundedAt == nil || stored.ReviewRequired {
		t.Fatalf("restored refunded order = %+v, ok=%t", stored, ok)
	}
}

func fulfilmentJobsEqual(a, b model.FulfilmentJob) bool {
	if a.OrderID != b.OrderID || a.Mode != b.Mode || a.State != b.State ||
		a.Attempts != b.Attempts || a.AttemptLeaseID != b.AttemptLeaseID ||
		a.EntitlementExpiresAt != b.EntitlementExpiresAt || a.Blocked != b.Blocked ||
		a.LastReason != b.LastReason || a.LastError != b.LastError ||
		a.UpdatedAt != b.UpdatedAt {
		return false
	}
	if (a.NextAttemptAt == nil) != (b.NextAttemptAt == nil) {
		return false
	}
	return a.NextAttemptAt == nil || *a.NextAttemptAt == *b.NextAttemptAt
}

func TestRestoreDropsMalformedFulfilmentJobWithoutLosingMoney(t *testing.T) {
	const validStamp = "2026-10-01T12:00:00Z"
	cases := []struct {
		name   string
		mutate func(*model.FulfilmentJob)
	}{
		{"unknown state", func(j *model.FulfilmentJob) { j.State = "done" }},
		{"unknown mode", func(j *model.FulfilmentJob) { j.Mode = "renew" }},
		{"attempts above bound", func(j *model.FulfilmentJob) { j.Attempts = MaxFulfilmentAttempts + 1 }},
		{"negative attempts", func(j *model.FulfilmentJob) { j.Attempts = -1 }},
		{"publishing without lease", func(j *model.FulfilmentJob) {
			j.State = model.FulfilmentPublishing
			j.Attempts = 1
			j.AttemptLeaseID = ""
		}},
		{"publishing without attempt", func(j *model.FulfilmentJob) {
			j.State = model.FulfilmentPublishing
			j.Attempts = 0
			j.AttemptLeaseID = "lease-x"
		}},
		{"publishing with schedule", func(j *model.FulfilmentJob) {
			j.State = model.FulfilmentPublishing
			j.Attempts = 1
			j.AttemptLeaseID = "lease-x"
			j.NextAttemptAt = ptrString(validStamp)
		}},
		{"publishing blocked", func(j *model.FulfilmentJob) {
			j.State = model.FulfilmentPublishing
			j.Attempts = 1
			j.AttemptLeaseID = "lease-x"
			j.Blocked = true
		}},
		{"queued holding a lease", func(j *model.FulfilmentJob) {
			j.Attempts = 1
			j.AttemptLeaseID = "lease-x"
		}},
		{"queued after the last attempt", func(j *model.FulfilmentJob) {
			j.Attempts = MaxFulfilmentAttempts
		}},
		{"queued without schedule", func(j *model.FulfilmentJob) { j.NextAttemptAt = nil }},
		{"queued blocked", func(j *model.FulfilmentJob) { j.Blocked = true }},
		{"dead letter with schedule", func(j *model.FulfilmentJob) {
			j.State = model.FulfilmentDeadLetter
			j.Attempts = 1
			j.Blocked = true
			j.LastReason = model.FulfilmentReasonInternal
			j.NextAttemptAt = ptrString(validStamp)
		}},
		{"dead letter not blocked", func(j *model.FulfilmentJob) {
			j.State = model.FulfilmentDeadLetter
			j.Attempts = 1
			j.LastReason = model.FulfilmentReasonInternal
		}},
		{"dead letter without reason", func(j *model.FulfilmentJob) {
			j.State = model.FulfilmentDeadLetter
			j.Attempts = 1
			j.Blocked = true
		}},
		{"dead letter holding a lease", func(j *model.FulfilmentJob) {
			j.State = model.FulfilmentDeadLetter
			j.Attempts = 1
			j.Blocked = true
			j.LastReason = model.FulfilmentReasonInternal
			j.AttemptLeaseID = "lease-x"
		}},
		{"reissue job on paid order", func(j *model.FulfilmentJob) {
			j.Mode = model.FulfilmentReissue
		}},
		{"foreign order", func(j *model.FulfilmentJob) { j.OrderID = "ord-999" }},
		{"invalid next attempt", func(j *model.FulfilmentJob) { j.NextAttemptAt = ptrString("tomorrow") }},
		{"invalid updatedAt", func(j *model.FulfilmentJob) { j.UpdatedAt = "" }},
		{"missing entitlement expiry", func(j *model.FulfilmentJob) { j.EntitlementExpiresAt = "" }},
		{"entitlement expiry mismatch", func(j *model.FulfilmentJob) {
			j.EntitlementExpiresAt = "2020-01-01T00:00:00Z"
		}},
		{"unknown reason", func(j *model.FulfilmentJob) {
			j.LastReason = model.FulfilmentReason("trade_ref_123")
		}},
		{"overlong last error", func(j *model.FulfilmentJob) {
			j.LastError = strings.Repeat("x", maxFulfilmentDetailLen+1)
		}},
		{"multi-line last error", func(j *model.FulfilmentJob) {
			j.LastError = "first\nsecond"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newOrderFixture(t)
			paid := f.paidOrderWithoutGrant(t, "quarantine-job")
			enqueueInitial(t, f.st, paid.ID)
			data, err := f.st.Snapshot()
			if err != nil {
				t.Fatalf("Snapshot: %v", err)
			}
			var snap Snapshot
			if err := json.Unmarshal(data, &snap); err != nil {
				t.Fatalf("decode snapshot: %v", err)
			}
			if len(snap.Orders) != 1 || snap.Orders[0].Fulfilment == nil {
				t.Fatalf("snapshot orders = %+v", snap.Orders)
			}
			tc.mutate(snap.Orders[0].Fulfilment)
			corrupt, err := json.Marshal(&snap)
			if err != nil {
				t.Fatalf("encode corrupt snapshot: %v", err)
			}
			restored := newEmptyWithClock(func() time.Time { return *f.now }, nil)
			if err := restored.Restore(corrupt); err != nil {
				t.Fatalf("Restore: %v", err)
			}
			order, ok := restored.Order(paid.ID)
			if !ok {
				t.Fatalf("order disappeared")
			}
			// Only the job is dropped: the paid state, its timestamps, the
			// provider reference and the payment events must all survive.
			if !order.ReviewRequired || order.State != model.OrderPaid ||
				order.Fulfilment != nil || order.Grant != nil ||
				order.PaidAt == nil || order.ProviderRef == nil || order.CancelledAt != nil {
				t.Fatalf("order after dropped job = %+v", order)
			}
			if got := len(restored.PaymentEvents()); got != 1 {
				t.Fatalf("payment events after dropped job = %d, want 1", got)
			}
			if order.Plan.AmountMinor != f.plan.AmountMinor {
				t.Fatalf("plan amount after dropped job = %d, want %d",
					order.Plan.AmountMinor, f.plan.AmountMinor)
			}
		})
	}
}

func TestRestoreDropsInitialJobOnActiveOrder(t *testing.T) {
	f := newOrderFixture(t)
	order := f.createOrder(t, "active-initial")
	active := f.apply(t, f.settleEvent(order.ID, "evt-active-initial", "receipt-active-initial"))
	if active.Grant == nil {
		t.Fatalf("settled order has no grant")
	}

	// White-box: attach an initial job the way a corrupt document would.
	f.st.mu.Lock()
	idx := f.st.orderIndexLocked(order.ID)
	expires, ok := orderFulfilmentExpiry(f.st.orders[idx].Order, model.FulfilmentInitial)
	if !ok {
		f.st.mu.Unlock()
		t.Fatalf("active order has no paid term")
	}
	f.st.orders[idx].Order.Fulfilment = &model.FulfilmentJob{
		OrderID:              order.ID,
		Mode:                 model.FulfilmentInitial,
		State:                model.FulfilmentQueued,
		EntitlementExpiresAt: iso(expires),
		NextAttemptAt:        ptrString(iso(*f.now)),
		UpdatedAt:            iso(*f.now),
	}
	f.st.mu.Unlock()

	data, err := f.st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	restored := newEmptyWithClock(func() time.Time { return *f.now }, nil)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	stored, ok := restored.Order(order.ID)
	if !ok || stored.State != model.OrderActive || stored.Grant == nil ||
		stored.Fulfilment != nil || !stored.ReviewRequired {
		t.Fatalf("restored active order = %+v, ok=%t", stored, ok)
	}
	if len(restored.Leases()) != 1 {
		t.Fatalf("dropping the job revoked service: leases=%d", len(restored.Leases()))
	}
}

func TestRecoverFulfilmentsRequeuesInterruptedAttempts(t *testing.T) {
	f := newOrderFixture(t)
	paid := f.paidOrderWithoutGrant(t, "recover")
	enqueueInitial(t, f.st, paid.ID)
	attempt, err := f.st.BeginFulfilment(paid.ID)
	if err != nil {
		t.Fatalf("BeginFulfilment: %v", err)
	}
	data, err := f.st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	restored := newEmptyWithClock(func() time.Time { return *f.now }, nil)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if job := fulfilmentJobOf(t, restored, paid.ID); job.State != model.FulfilmentPublishing ||
		job.Attempts != 1 || job.AttemptLeaseID != attempt.LeaseID {
		t.Fatalf("restored publishing job = %+v", job)
	}

	if err := restored.RecoverFulfilments(); err != nil {
		t.Fatalf("RecoverFulfilments: %v", err)
	}
	job := fulfilmentJobOf(t, restored, paid.ID)
	if job.State != model.FulfilmentQueued || job.Attempts != 1 ||
		job.AttemptLeaseID != "" || job.LastReason != model.FulfilmentReasonInterrupted {
		t.Fatalf("recovered job = %+v", job)
	}
	requireNextAttempt(t, job, *f.now)
	if due := restored.DueFulfilments(*f.now); len(due) != 1 || due[0].ID != paid.ID {
		t.Fatalf("due after recovery = %+v", due)
	}
	if got := countAudit(restored, "fulfilment.interrupted"); got != 1 {
		t.Fatalf("fulfilment.interrupted rows = %d, want 1", got)
	}

	// Recovery is idempotent: a queued job is not touched and no second
	// interrupted row is written.
	if err := restored.RecoverFulfilments(); err != nil {
		t.Fatalf("second RecoverFulfilments: %v", err)
	}
	if again := fulfilmentJobOf(t, restored, paid.ID); !fulfilmentJobsEqual(job, again) {
		t.Fatalf("second recovery changed the job: %+v, want %+v", again, job)
	}
	if got := countAudit(restored, "fulfilment.interrupted"); got != 1 {
		t.Fatalf("fulfilment.interrupted rows after second recovery = %d, want 1", got)
	}

	// A dead letter is an operator queue, never an automatic retry.
	f2 := newOrderFixture(t)
	dead := f2.paidOrderWithoutGrant(t, "recover-dead")
	enqueueInitial(t, f2.st, dead.ID)
	first, err := f2.st.BeginFulfilment(dead.ID)
	if err != nil {
		t.Fatalf("BeginFulfilment(dead): %v", err)
	}
	if _, err := f2.st.AbortFulfilment(dead.ID, first.LeaseID, model.FulfilmentReasonPublishFailed, "publication refused", false); err != nil {
		t.Fatalf("AbortFulfilment(dead): %v", err)
	}
	if err := f2.st.RecoverFulfilments(); err != nil {
		t.Fatalf("RecoverFulfilments(dead): %v", err)
	}
	deadJob := fulfilmentJobOf(t, f2.st, dead.ID)
	if deadJob.State != model.FulfilmentDeadLetter || deadJob.Attempts != 1 || deadJob.NextAttemptAt != nil {
		t.Fatalf("recovered dead letter = %+v", deadJob)
	}
	if got := countAudit(f2.st, "fulfilment.interrupted"); got != 0 {
		t.Fatalf("dead letter wrote %d interrupted rows", got)
	}
}

func TestDueFulfilmentsHonoursSchedule(t *testing.T) {
	f := newOrderFixture(t)
	paid := f.paidOrderWithoutGrant(t, "due")
	if due := f.st.DueFulfilments(*f.now); len(due) != 0 {
		t.Fatalf("due before enqueue = %+v", due)
	}
	enqueueInitial(t, f.st, paid.ID)
	if due := f.st.DueFulfilments(*f.now); len(due) != 1 || due[0].ID != paid.ID {
		t.Fatalf("due after enqueue = %+v", due)
	}
	if due := f.st.DueFulfilments(f.now.Add(-time.Second)); len(due) != 0 {
		t.Fatalf("job is due before its schedule: %+v", due)
	}

	attempt, err := f.st.BeginFulfilment(paid.ID)
	if err != nil {
		t.Fatalf("BeginFulfilment: %v", err)
	}
	if due := f.st.DueFulfilments(f.now.Add(time.Hour)); len(due) != 0 {
		t.Fatalf("publishing job is due: %+v", due)
	}
	if _, err := f.st.AbortFulfilment(paid.ID, attempt.LeaseID, model.FulfilmentReasonCapacity, "", true); err != nil {
		t.Fatalf("AbortFulfilment: %v", err)
	}
	if due := f.st.DueFulfilments(f.now.Add(4 * time.Second)); len(due) != 0 {
		t.Fatalf("job is due before the first backoff: %+v", due)
	}
	if due := f.st.DueFulfilments(f.now.Add(5 * time.Second)); len(due) != 1 {
		t.Fatalf("job is not due at the first backoff: %+v", due)
	}

	*f.now = f.now.Add(5 * time.Second)
	second, err := f.st.BeginFulfilment(paid.ID)
	if err != nil {
		t.Fatalf("BeginFulfilment(second): %v", err)
	}
	if _, err := f.st.AbortFulfilment(paid.ID, second.LeaseID, model.FulfilmentReasonCapacity, "", true); err != nil {
		t.Fatalf("AbortFulfilment(second): %v", err)
	}
	if due := f.st.DueFulfilments(f.now.Add(14 * time.Second)); len(due) != 0 {
		t.Fatalf("job is due before the second backoff: %+v", due)
	}
	if due := f.st.DueFulfilments(f.now.Add(15 * time.Second)); len(due) != 1 {
		t.Fatalf("job is not due at the second backoff: %+v", due)
	}
}

func TestConcurrentBeginFulfilmentStartsOneAttempt(t *testing.T) {
	f := newOrderFixture(t)
	paid := f.paidOrderWithoutGrant(t, "race")
	enqueueInitial(t, f.st, paid.ID)

	const workers = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan FulfilmentAttempt, workers)
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			attempt, err := f.st.BeginFulfilment(paid.ID)
			if err != nil {
				errs <- err
				return
			}
			results <- attempt
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)

	won := make([]FulfilmentAttempt, 0, workers)
	for attempt := range results {
		won = append(won, attempt)
	}
	if len(won) != 1 {
		t.Fatalf("concurrent begins succeeded %d times, want exactly one", len(won))
	}
	for err := range errs {
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("losing begin error = %v, want ErrConflict", err)
		}
	}
	job := fulfilmentJobOf(t, f.st, paid.ID)
	if job.State != model.FulfilmentPublishing || job.Attempts != 1 ||
		job.AttemptLeaseID != won[0].LeaseID {
		t.Fatalf("job after the race = %+v, winner = %+v", job, won[0])
	}
}

// flagOrderReview applies a wrong-amount settlement to a paid order. The
// existing event path records the mismatch review-only and leaves the order
// paid, which is the state fulfilment must refuse.
func flagOrderReview(t *testing.T, f *orderFixture, orderID string) model.Order {
	t.Helper()
	event := f.settleEvent(orderID, "evt-review-"+orderID, "receipt-review-"+orderID)
	event.AmountMinor = 1
	reviewed := f.apply(t, event)
	if !reviewed.ReviewRequired || reviewed.State != model.OrderPaid {
		t.Fatalf("wrong-amount settlement = %+v, want paid with review", reviewed)
	}
	return reviewed
}

func TestEnqueueFulfilmentRefusesReviewRequiredOrder(t *testing.T) {
	f := newOrderFixture(t)
	paid := f.paidOrderWithoutGrant(t, "review-enqueue")
	flagOrderReview(t, f, paid.ID)

	if _, err := f.st.EnqueueFulfilment(paid.ID, model.FulfilmentInitial); !errors.Is(err, ErrConflict) {
		t.Fatalf("enqueue on review order error = %v, want ErrConflict", err)
	}
	order, ok := f.st.Order(paid.ID)
	if !ok || order.Fulfilment != nil || !order.ReviewRequired || order.State != model.OrderPaid {
		t.Fatalf("review order after enqueue = %+v, ok=%t", order, ok)
	}
}

func TestBeginFulfilmentRefusesReviewRequiredOrder(t *testing.T) {
	f := newOrderFixture(t)
	paid := f.paidOrderWithoutGrant(t, "review-begin")
	enqueueInitial(t, f.st, paid.ID)
	flagOrderReview(t, f, paid.ID)

	if _, err := f.st.BeginFulfilment(paid.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("begin on review order error = %v, want ErrConflict", err)
	}
	job := fulfilmentJobOf(t, f.st, paid.ID)
	if job.State != model.FulfilmentQueued || job.Attempts != 0 || job.Blocked {
		t.Fatalf("review order job changed: %+v", job)
	}
}

func TestFulfilmentExpiredPaidTermDeadLetters(t *testing.T) {
	f := newOrderFixture(t)
	paid := f.paidOrderWithoutGrant(t, "expired-queued")
	queued := enqueueInitial(t, f.st, paid.ID)
	wantExpires, ok := orderFulfilmentExpiry(queued, model.FulfilmentInitial)
	if !ok || queued.Fulfilment.EntitlementExpiresAt != iso(wantExpires) {
		t.Fatalf("job expiry = %q, want %q", queued.Fulfilment.EntitlementExpiresAt, iso(wantExpires))
	}

	*f.now = f.now.Add(time.Duration(f.plan.LeaseTTLSeconds)*time.Second + time.Second)
	if _, err := f.st.BeginFulfilment(paid.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("begin after paid-term expiry error = %v, want ErrConflict", err)
	}
	job := fulfilmentJobOf(t, f.st, paid.ID)
	if job.State != model.FulfilmentDeadLetter || !job.Blocked || job.Attempts != 0 ||
		job.AttemptLeaseID != "" || job.NextAttemptAt != nil ||
		job.LastReason != model.FulfilmentReasonEntitlementExpired {
		t.Fatalf("expired job = %+v", job)
	}
	if due := f.st.DueFulfilments(f.now.Add(time.Hour)); len(due) != 0 {
		t.Fatalf("expired dead letter is due: %+v", due)
	}
	event := findAudit(t, f.st, "fulfilment.dead_letter", func(e model.AuditEvent) bool {
		return e.Meta["orderId"] == paid.ID
	})
	if event.Severity != model.SeverityCritical ||
		event.Meta["reason"] != string(model.FulfilmentReasonEntitlementExpired) {
		t.Fatalf("dead-letter audit = %+v", event)
	}
	// The payment and its order state survive: only the work is dead.
	order, ok := f.st.Order(paid.ID)
	if !ok || order.State != model.OrderPaid || order.PaidAt == nil || order.Grant != nil {
		t.Fatalf("expired order = %+v, ok=%t", order, ok)
	}

	// An initial job enqueued after the term already ended records the dead
	// letter directly instead of dropping the liability.
	f2 := newOrderFixture(t)
	late := f2.paidOrderWithoutGrant(t, "expired-enqueue")
	*f2.now = f2.now.Add(time.Duration(f2.plan.LeaseTTLSeconds)*time.Second + time.Second)
	expired, err := f2.st.EnqueueFulfilment(late.ID, model.FulfilmentInitial)
	if err != nil {
		t.Fatalf("EnqueueFulfilment(expired): %v", err)
	}
	lateJob := *expired.Fulfilment
	if lateJob.State != model.FulfilmentDeadLetter || !lateJob.Blocked || lateJob.Attempts != 0 ||
		lateJob.LastReason != model.FulfilmentReasonEntitlementExpired {
		t.Fatalf("late enqueue job = %+v", lateJob)
	}
	if _, err := f2.st.BeginFulfilment(late.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("begin on late enqueue error = %v, want ErrConflict", err)
	}

	// A queued reissue whose paid term ends is dead-lettered the same way.
	f3 := newOrderFixture(t)
	order3 := f3.createOrder(t, "expired-reissue")
	active := f3.apply(t, f3.settleEvent(order3.ID, "evt-expired-reissue", "receipt-expired-reissue"))
	if active.Grant == nil {
		t.Fatalf("settled order has no grant")
	}
	lease, ok := f3.st.Lease(active.Grant.LeaseID)
	if !ok {
		t.Fatalf("grant lease missing")
	}
	if _, _, _, err := f3.st.DemoteNode(lease.NodeID); err != nil {
		t.Fatalf("DemoteNode: %v", err)
	}
	if _, err := f3.st.EnqueueFulfilment(order3.ID, model.FulfilmentReissue); err != nil {
		t.Fatalf("EnqueueFulfilment(reissue): %v", err)
	}
	*f3.now = f3.now.Add(time.Duration(f3.plan.LeaseTTLSeconds)*time.Second + time.Second)
	if _, err := f3.st.BeginFulfilment(order3.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("begin after reissue expiry error = %v, want ErrConflict", err)
	}
	reissueJob := fulfilmentJobOf(t, f3.st, order3.ID)
	if reissueJob.State != model.FulfilmentDeadLetter || !reissueJob.Blocked ||
		reissueJob.Attempts != 0 || reissueJob.LastReason != model.FulfilmentReasonEntitlementExpired {
		t.Fatalf("expired reissue job = %+v", reissueJob)
	}
}

func TestAbortFulfilmentRejectsUnknownReasonAndSanitizesDetail(t *testing.T) {
	f := newOrderFixture(t)
	paid := f.paidOrderWithoutGrant(t, "sanitize")
	enqueueInitial(t, f.st, paid.ID)
	attempt, err := f.st.BeginFulfilment(paid.ID)
	if err != nil {
		t.Fatalf("BeginFulfilment: %v", err)
	}

	// Free text, a reference or a credential is not a reason code.
	for _, raw := range []string{
		"no_capacity",
		"202610261234567890",
		"https://pay.example/notify?token=SECRET",
	} {
		if _, err := f.st.AbortFulfilment(paid.ID, attempt.LeaseID,
			model.FulfilmentReason(raw), "", true); !errors.Is(err, ErrInvalid) {
			t.Fatalf("abort reason %q error = %v, want ErrInvalid", raw, err)
		}
	}
	if job := fulfilmentJobOf(t, f.st, paid.ID); job.State != model.FulfilmentPublishing || job.Attempts != 1 {
		t.Fatalf("rejected abort changed the job: %+v", job)
	}

	sensitive := "token=SECRET password hunter2 trade 20261026 api_key=abcDEF123 " +
		"receipt no. ABCD-1234 " +
		"https://pay.example/notify?trade=202610261234567890 token=sk_live_SECRET1234567890 " +
		"mail ops@example.com"
	aborted, err := f.st.AbortFulfilment(paid.ID, attempt.LeaseID,
		model.FulfilmentReasonPublishFailed, sensitive, true)
	if err != nil {
		t.Fatalf("AbortFulfilment(sensitive detail): %v", err)
	}
	job := *aborted.Fulfilment
	if job.State != model.FulfilmentQueued || job.LastReason != model.FulfilmentReasonPublishFailed {
		t.Fatalf("aborted job = %+v", job)
	}
	for _, needle := range []string{
		"http", "202610261234567890", "20261026", "SECRET", "sk_live",
		"hunter2", "abcDEF123", "ABCD-1234", "@", "example.com",
	} {
		if strings.Contains(job.LastError, needle) {
			t.Fatalf("last error kept sensitive text %q: %q", needle, job.LastError)
		}
	}
	if len([]rune(job.LastError)) > maxFulfilmentDetailLen {
		t.Fatalf("last error length = %d, want <= %d",
			len([]rune(job.LastError)), maxFulfilmentDetailLen)
	}
	for _, event := range f.st.Audit(50) {
		for key, value := range event.Meta {
			for _, needle := range []string{"SECRET", "hunter2", "abcDEF123", "ABCD-1234", "202610261234567890"} {
				if strings.Contains(value, needle) {
					t.Fatalf("audit meta %s kept sensitive text %q: %q", key, needle, value)
				}
			}
		}
		for _, needle := range []string{"SECRET", "hunter2", "abcDEF123", "ABCD-1234", "202610261234567890"} {
			if strings.Contains(event.Detail, needle) {
				t.Fatalf("audit detail kept sensitive text %q: %q", needle, event.Detail)
			}
		}
	}

	// A long but harmless detail is bounded, not rejected.
	second, err := f.st.BeginFulfilment(paid.ID)
	if err != nil {
		t.Fatalf("BeginFulfilment(second): %v", err)
	}
	long := strings.Repeat("publication retry scheduled ", 20)
	bounded, err := f.st.AbortFulfilment(paid.ID, second.LeaseID,
		model.FulfilmentReasonTimeout, long, true)
	if err != nil {
		t.Fatalf("AbortFulfilment(long detail): %v", err)
	}
	if got := len([]rune(bounded.Fulfilment.LastError)); got > maxFulfilmentDetailLen {
		t.Fatalf("bounded detail length = %d, want <= %d", got, maxFulfilmentDetailLen)
	}
}

func TestFulfilmentJobJSONRoundtrip(t *testing.T) {
	next := "2026-10-01T12:00:30Z"
	cases := []struct {
		name string
		job  model.FulfilmentJob
	}{
		{"queued retry", model.FulfilmentJob{
			OrderID: "ord-001", Mode: model.FulfilmentInitial, State: model.FulfilmentQueued,
			Attempts: 2, EntitlementExpiresAt: "2026-10-01T13:00:00Z",
			NextAttemptAt: &next, LastReason: model.FulfilmentReasonCapacity,
			LastError: "no free slot", UpdatedAt: "2026-10-01T12:00:00Z",
		}},
		{"publishing", model.FulfilmentJob{
			OrderID: "ord-001", Mode: model.FulfilmentReissue, State: model.FulfilmentPublishing,
			Attempts: 1, AttemptLeaseID: "lease-ord-001-a1",
			EntitlementExpiresAt: "2026-10-01T13:00:00Z",
			UpdatedAt:            "2026-10-01T12:00:00Z",
		}},
		{"dead letter", model.FulfilmentJob{
			OrderID: "ord-001", Mode: model.FulfilmentInitial, State: model.FulfilmentDeadLetter,
			Attempts: MaxFulfilmentAttempts, EntitlementExpiresAt: "2026-10-01T13:00:00Z",
			LastReason: model.FulfilmentReasonPublishFailed, LastError: "gateway refused",
			Blocked: true, UpdatedAt: "2026-10-01T12:00:00Z",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(model.Order{ID: "ord-001", Fulfilment: &tc.job})
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			var decoded model.Order
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if decoded.Fulfilment == nil || !fulfilmentJobsEqual(*decoded.Fulfilment, tc.job) {
				t.Fatalf("roundtrip job = %+v, want %+v", decoded.Fulfilment, tc.job)
			}
		})
	}

	// A nil job keeps the pre-fulfilment shape.
	plain, err := json.Marshal(model.Order{ID: "ord-002"})
	if err != nil {
		t.Fatalf("Marshal(nil job): %v", err)
	}
	if bytes.Contains(plain, []byte(`"fulfilment"`)) {
		t.Fatalf("nil job serialized: %s", plain)
	}

	// Optional fields stay omitted; the frozen paid term is always present.
	queued, err := json.Marshal(model.FulfilmentJob{
		OrderID: "ord-003", Mode: model.FulfilmentInitial, State: model.FulfilmentQueued,
		EntitlementExpiresAt: "2026-10-01T13:00:00Z", UpdatedAt: "2026-10-01T12:00:00Z",
	})
	if err != nil {
		t.Fatalf("Marshal(queued): %v", err)
	}
	for _, absent := range []string{
		`"attemptLeaseId"`, `"nextAttemptAt"`, `"lastReason"`, `"lastError"`, `"blocked"`,
	} {
		if bytes.Contains(queued, []byte(absent)) {
			t.Fatalf("optional field %s serialized: %s", absent, queued)
		}
	}
	if !bytes.Contains(queued, []byte(`"entitlementExpiresAt"`)) {
		t.Fatalf("required entitlement expiry omitted: %s", queued)
	}
}

func TestFulfilmentReadsAreIsolatedCopies(t *testing.T) {
	f := newOrderFixture(t)
	paid := f.paidOrderWithoutGrant(t, "clone")
	queued := enqueueInitial(t, f.st, paid.ID)
	if queued.Fulfilment == nil || queued.Fulfilment.NextAttemptAt == nil {
		t.Fatalf("enqueued job = %+v", queued.Fulfilment)
	}

	assertStored := func(stage string) {
		t.Helper()
		stored := fulfilmentJobOf(t, f.st, paid.ID)
		if stored.Attempts != 0 || stored.AttemptLeaseID != "" ||
			stored.NextAttemptAt == nil || *stored.NextAttemptAt != iso(*f.now) {
			t.Fatalf("%s: store job was mutated through a copy: %+v", stage, stored)
		}
	}
	// Enqueue returns a copy.
	*queued.Fulfilment.NextAttemptAt = "1999-01-01T00:00:00Z"
	queued.Fulfilment.Attempts = 99
	queued.Fulfilment.AttemptLeaseID = "lease-tampered"
	assertStored("enqueue")

	// Order, Orders and DueFulfilments return independent copies.
	read, ok := f.st.Order(paid.ID)
	if !ok || read.Fulfilment == nil {
		t.Fatalf("Order = %+v, ok=%t", read, ok)
	}
	*read.Fulfilment.NextAttemptAt = "1999-01-01T00:00:00Z"
	read.Fulfilment.Attempts = 7
	assertStored("order")

	all := f.st.Orders()
	if len(all) != 1 || all[0].Fulfilment == nil {
		t.Fatalf("Orders = %+v", all)
	}
	*all[0].Fulfilment.NextAttemptAt = "1999-01-01T00:00:00Z"
	all[0].Fulfilment.Attempts = 6
	assertStored("orders")

	due := f.st.DueFulfilments(*f.now)
	if len(due) != 1 || due[0].Fulfilment == nil {
		t.Fatalf("DueFulfilments = %+v", due)
	}
	*due[0].Fulfilment.NextAttemptAt = "1999-01-01T00:00:00Z"
	due[0].Fulfilment.Attempts = 5
	assertStored("due")

	// Abort returns a copy too.
	attempt, err := f.st.BeginFulfilment(paid.ID)
	if err != nil {
		t.Fatalf("BeginFulfilment: %v", err)
	}
	aborted, err := f.st.AbortFulfilment(paid.ID, attempt.LeaseID,
		model.FulfilmentReasonCapacity, "", true)
	if err != nil {
		t.Fatalf("AbortFulfilment: %v", err)
	}
	if aborted.Fulfilment == nil || aborted.Fulfilment.NextAttemptAt == nil {
		t.Fatalf("aborted job = %+v", aborted.Fulfilment)
	}
	wantNext := f.now.Add(5 * time.Second)
	*aborted.Fulfilment.NextAttemptAt = "1999-01-01T00:00:00Z"
	aborted.Fulfilment.Attempts = 4
	stored := fulfilmentJobOf(t, f.st, paid.ID)
	if stored.Attempts != 1 || stored.NextAttemptAt == nil || *stored.NextAttemptAt != iso(wantNext) {
		t.Fatalf("abort: store job was mutated through a copy: %+v", stored)
	}
}

func TestSanitizeFulfilmentDetail(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		gone []string
		keep []string
	}{
		{"keyed token", "token=SECRET", []string{"SECRET"}, []string{"token"}},
		{"password value", "password hunter2", []string{"hunter2"}, []string{"password"}},
		{"password filler", "password is hunter2", []string{"hunter2"}, []string{"password"}},
		{"trade number", "trade 20261026", []string{"20261026"}, []string{"trade"}},
		{"trade no", "trade no 20261026", []string{"20261026"}, []string{"trade no"}},
		{"receipt reference", "receipt no. ABCD-1234", []string{"ABCD-1234"}, []string{"receipt no"}},
		{"api key", "api_key=abcDEF123", []string{"abcDEF123"}, []string{"api_key"}},
		{"signed url", "https://pay.example/notify?token=SECRET",
			[]string{"http", "SECRET", "pay.example"}, nil},
		{"email", "ops@example.com", []string{"ops", "example.com", "@"}, nil},
		{"long token", "sk_live_SECRET1234567890", []string{"SECRET", "sk_live"}, nil},
		{"standalone number", "receipt 202610261234567890",
			[]string{"202610261234567890"}, []string{"receipt"}},
		{"safe prose", "no free slot for this order", nil,
			[]string{"no free slot for this order"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeFulfilmentDetail(tc.raw)
			for _, needle := range tc.gone {
				if strings.Contains(got, needle) {
					t.Fatalf("sanitized %q still contains %q", got, needle)
				}
			}
			for _, needle := range tc.keep {
				if !strings.Contains(got, needle) {
					t.Fatalf("sanitized %q dropped %q", got, needle)
				}
			}
		})
	}

	long := strings.Repeat("publication retry scheduled ", 20)
	if got := len([]rune(sanitizeFulfilmentDetail(long))); got > maxFulfilmentDetailLen {
		t.Fatalf("bounded detail length = %d, want <= %d", got, maxFulfilmentDetailLen)
	}
}

func TestRestoreKeepsZeroAttemptDeadLetter(t *testing.T) {
	f := newOrderFixture(t)
	paid := f.paidOrderWithoutGrant(t, "expired-persist")
	*f.now = f.now.Add(time.Duration(f.plan.LeaseTTLSeconds)*time.Second + time.Second)

	expired, err := f.st.EnqueueFulfilment(paid.ID, model.FulfilmentInitial)
	if err != nil {
		t.Fatalf("EnqueueFulfilment(expired): %v", err)
	}
	before := *expired.Fulfilment
	if before.State != model.FulfilmentDeadLetter || before.Attempts != 0 || !before.Blocked {
		t.Fatalf("expired job = %+v", before)
	}

	data, err := f.st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	restored := newEmptyWithClock(func() time.Time { return *f.now }, nil)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	after := fulfilmentJobOf(t, restored, paid.ID)
	if !fulfilmentJobsEqual(before, after) {
		t.Fatalf("restored dead letter = %+v, want %+v", after, before)
	}
	if after.Attempts != 0 || !after.Blocked ||
		after.LastReason != model.FulfilmentReasonEntitlementExpired {
		t.Fatalf("restored dead letter = %+v", after)
	}
	// It stays an operator item: never due, never automatically retried.
	if due := restored.DueFulfilments(*f.now); len(due) != 0 {
		t.Fatalf("restored dead letter is due: %+v", due)
	}
	if _, err := restored.BeginFulfilment(paid.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("begin on restored dead letter error = %v, want ErrConflict", err)
	}
}

func TestRestoreScrubsFulfilmentDetail(t *testing.T) {
	cases := []struct {
		name   string
		detail string
		gone   []string
	}{
		{"url token", "https://pay.example/notify?token=SECRET", []string{"pay.example", "SECRET"}},
		{"receipt reference", "receipt no. ABCD-1234", []string{"ABCD-1234"}},
		{"keyed secret", "password hunter2", []string{"hunter2"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newOrderFixture(t)
			paid := f.paidOrderWithoutGrant(t, "scrub-detail")
			enqueueInitial(t, f.st, paid.ID)
			data, err := f.st.Snapshot()
			if err != nil {
				t.Fatalf("Snapshot: %v", err)
			}
			var snap Snapshot
			if err := json.Unmarshal(data, &snap); err != nil {
				t.Fatalf("decode snapshot: %v", err)
			}
			if len(snap.Orders) != 1 || snap.Orders[0].Fulfilment == nil {
				t.Fatalf("snapshot orders = %+v", snap.Orders)
			}
			snap.Orders[0].Fulfilment.LastError = tc.detail
			corrupt, err := json.Marshal(&snap)
			if err != nil {
				t.Fatalf("encode snapshot: %v", err)
			}

			restored := newEmptyWithClock(func() time.Time { return *f.now }, nil)
			if err := restored.Restore(corrupt); err != nil {
				t.Fatalf("Restore: %v", err)
			}
			order, ok := restored.Order(paid.ID)
			if !ok || order.Fulfilment == nil {
				t.Fatalf("restored order = %+v, ok=%t; job must survive a scrubbed detail", order, ok)
			}
			if order.Fulfilment.State != model.FulfilmentQueued || order.Fulfilment.Attempts != 0 {
				t.Fatalf("restored job = %+v", order.Fulfilment)
			}
			for _, needle := range tc.gone {
				if strings.Contains(order.Fulfilment.LastError, needle) {
					t.Fatalf("restored detail kept %q: %q", needle, order.Fulfilment.LastError)
				}
			}
			if got := len([]rune(order.Fulfilment.LastError)); got > maxFulfilmentDetailLen {
				t.Fatalf("restored detail length = %d, want <= %d", got, maxFulfilmentDetailLen)
			}
			// A later save must not resurrect the sensitive text either.
			resaved, err := restored.Snapshot()
			if err != nil {
				t.Fatalf("Snapshot(restored): %v", err)
			}
			for _, needle := range tc.gone {
				if bytes.Contains(resaved, []byte(needle)) {
					t.Fatalf("re-saved snapshot kept %q", needle)
				}
			}
		})
	}
}
