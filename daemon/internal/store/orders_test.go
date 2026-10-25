package store

import (
	"encoding/json"
	"errors"
	"fmt"
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
