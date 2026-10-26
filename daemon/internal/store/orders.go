package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"ajsaccountd/internal/model"
)

// Order retention and input bounds. Orders and payment events are the audit
// trail of what was sold and what money was recorded, so both are capped but
// never silently rewritten; the caps only keep the snapshot bounded.
const (
	MaxOrders            = 5000
	MaxPaymentEvents     = 20000
	maxOrderReferenceLen = 200
	maxOrderReasonLen    = 300
	orderIDPrefix        = "ord-"
)

// MaxFulfilmentAttempts bounds automatic publication attempts. Attempts 1..7
// are retried after fulfilmentBackoff; the eighth failure dead-letters the job
// and leaves resolution to an operator.
const MaxFulfilmentAttempts = 8

// maxFulfilmentDetailLen bounds the sanitized operator detail stored on a job.
// The reason itself is a closed code; this is the only free-form field, so it
// is short, single-line and scrubbed of anything that could be a reference or
// credential.
const maxFulfilmentDetailLen = 160

// fulfilmentBackoff is the exact delay after each failed attempt 1..7.
var fulfilmentBackoff = [...]time.Duration{
	5 * time.Second,
	15 * time.Second,
	45 * time.Second,
	2 * time.Minute,
	5 * time.Minute,
	15 * time.Minute,
	30 * time.Minute,
}

// Fulfilment detail scrubbing. A publication error can embed a signed URL, an
// email, a keyed secret or a trade reference; none of those may enter the
// durable job or an audit row. The patterns replace a whole match instead of
// truncating it, because a partially masked secret is still a secret.
var (
	fulfilmentDetailURL   = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.\-]*://\S*`)
	fulfilmentDetailEmail = regexp.MustCompile(`\S*@\S*`)
	// fulfilmentDetailSecret keeps the key for operator context and replaces
	// the value, so "token=SECRET", "password is hunter2" and "trade 20261026"
	// cannot survive.
	fulfilmentDetailSecret = regexp.MustCompile(`(?i)\b(token|password|passwd|secret|api[_-]?key|access[_-]?key|authorization|auth|bearer|cookie|session|payment[_-]?(?:id|ref)|charge[_-]?id|transaction|txn|receipt\s*no|receipt[_-]?no|receipt|trade\s*no|trade[_-]?no|trade|reference|ref|signature|sign)\b\.?\s*(?:is|was|set to|equals|[:=])?\s*\S+`)
	// fulfilmentDetailDigits matches a standalone numeric reference such as a
	// trade number that appears without a key.
	fulfilmentDetailDigits = regexp.MustCompile(`\d{8,}`)
	fulfilmentDetailRun    = regexp.MustCompile(`[A-Za-z0-9_\-]{12,}`)
)

// RefundDecision is what a RefundPolicy answers for one refund request.
type RefundDecision string

const (
	RefundAllow  RefundDecision = "allow"
	RefundDeny   RefundDecision = "deny"
	RefundReview RefundDecision = "review"
)

// RefundRequest is the evidence a RefundPolicy sees. It contains no secret and
// no raw provider payload.
type RefundRequest struct {
	Order       model.Order
	AmountMinor int64
	Reason      string
	Reference   string
	Provider    string
}

// RefundPolicy decides whether a recorded refund may take effect. Nil uses the
// default: full refunds of paid/active orders with a reference are allowed;
// partial refunds and anything else go to review. The hook is installed once
// during startup and runs with the store lock held, so it must not block or
// call back into the Store.
type RefundPolicy func(RefundRequest) RefundDecision

// orderRecord is the store-only order row. The embedded model.Order is the
// wire contract; the two hashes are idempotency state and never leave the
// process. A snapshot re-validates every row on restore.
type orderRecord struct {
	model.Order
	IdempotencyKeyHash string `json:"idempotencyKeyHash,omitempty"`
	RequestHash        string `json:"requestHash,omitempty"`
}

// SetRefundPolicy installs the refund decision hook. Passing nil restores the
// default policy.
func (s *Store) SetRefundPolicy(policy RefundPolicy) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refundPolicy = policy
}

// CreateOrder creates one awaiting_payment order from an active plan. The
// returned bool reports an idempotent replay: the same (tenant, key) already
// produced the row and nothing new was created.
//
// The order snapshots the plan's priced fields, so later plan edits and
// deactivation cannot change what this order costs or grants. One
// awaiting/paid/unexpired-active order per user is allowed; an expired active
// order permits a renewal.
func (s *Store) CreateOrder(tenantID, userID, planID, idempotencyKey, actor string) (model.Order, bool, error) {
	tenantID = strings.TrimSpace(tenantID)
	userID = strings.TrimSpace(userID)
	planID = strings.TrimSpace(planID)
	key := strings.TrimSpace(idempotencyKey)
	switch {
	case tenantID == "":
		return model.Order{}, false, invalidf("tenantId is required")
	case userID == "":
		return model.Order{}, false, invalidf("userId is required")
	case planID == "":
		return model.Order{}, false, invalidf("planId is required")
	case len(key) > maxIdempotencyKeyLen:
		return model.Order{}, false, invalidf("idempotency key must be at most %d characters", maxIdempotencyKeyLen)
	case strings.ContainsAny(key, " \t\r\n"):
		return model.Order{}, false, invalidf("idempotency key must not contain whitespace")
	}
	keyHash := ""
	requestHash := hashRequest(planID)
	if key != "" {
		keyHash = hashRequest(key)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if keyHash != "" {
		for i := range s.orders {
			record := &s.orders[i]
			if record.TenantID != tenantID || record.IdempotencyKeyHash != keyHash {
				continue
			}
			if record.RequestHash != requestHash {
				return model.Order{}, false, conflictf(
					"idempotency key %q was already used for a different order request", key)
			}
			return cloneOrder(record.Order), true, nil
		}
	}

	tenantIdx := s.tenantIndexLocked(tenantID)
	if tenantIdx < 0 {
		return model.Order{}, false, notFoundf("tenant %q not found", tenantID)
	}
	tenant := s.tenants[tenantIdx]
	if tenant.invalid != "" {
		return model.Order{}, false, conflictf("tenant %q is quarantined: %s", tenant.ID, tenant.invalid)
	}
	if tenant.Status != model.TenantActive {
		return model.Order{}, false, conflictf("tenant %q is not active", tenant.ID)
	}
	if tenant.UserID == nil || *tenant.UserID != userID {
		return model.Order{}, false, conflictf("user %q is not linked to this tenant", userID)
	}
	if s.userIndexLocked(userID) < 0 {
		return model.Order{}, false, notFoundf("user %q not found", userID)
	}

	planIdx := s.planIndexLocked(planID)
	if planIdx < 0 {
		return model.Order{}, false, notFoundf("plan %q not found", planID)
	}
	plan := s.plans[planIdx]
	if !plan.Active {
		return model.Order{}, false, conflictf("plan %q is not active", plan.ID)
	}
	if !validPlanSnapshot(planSnapshot(plan)) {
		return model.Order{}, false, conflictf("plan %q has an invalid snapshot", plan.ID)
	}

	now := s.now()
	s.sweepExpiredLocked(now)
	if err := s.checkOpenOrderLocked(userID, now); err != nil {
		return model.Order{}, false, err
	}

	nextSeq := s.orderSeq + 1
	order := model.Order{
		ID:        fmt.Sprintf("%s%03d", orderIDPrefix, nextSeq),
		TenantID:  tenant.ID,
		UserID:    userID,
		PlanID:    plan.ID,
		Plan:      planSnapshot(plan),
		Provider:  model.ProviderManual,
		State:     model.OrderAwaitingPayment,
		CreatedAt: iso(now),
	}
	var inserted bool
	s.orders, inserted = prependOrderCapped(s.orders, orderRecord{
		Order:              order,
		IdempotencyKeyHash: keyHash,
		RequestHash:        requestHash,
	}, MaxOrders)
	if !inserted {
		return model.Order{}, false, conflictf(
			"order store is full of pending epay orders; settle or cancel one first")
	}
	s.orderSeq = nextSeq
	s.addAuditLocked(model.SeverityInfo, actor, "order.created", order.ID,
		fmt.Sprintf("%s · %s · %s", plan.Name, formatMinor(plan.AmountMinor, plan.Currency), order.State),
		map[string]string{
			"orderId":  order.ID,
			"tenantId": order.TenantID,
			"userId":   order.UserID,
			"planId":   order.PlanID,
			"amount":   fmt.Sprintf("%d", plan.AmountMinor),
			"currency": plan.Currency,
		})
	return cloneOrder(order), false, nil
}

// prependOrderCapped inserts one order at the head of the newest-first slice.
// At MaxOrders it evicts the oldest evictable row, but never an
// awaiting_payment epay order: that row is the only record of money the
// gateway may still report, and dropping it would turn a later valid notify
// into "unknown order". If every row is protected, insertion fails and the
// caller answers a conflict instead of silently losing a pending payment.
func prependOrderCapped(items []orderRecord, item orderRecord, limit int) ([]orderRecord, bool) {
	if limit <= 0 {
		return items, true
	}
	for len(items) >= limit {
		evict := -1
		for i := len(items) - 1; i >= 0; i-- {
			if items[i].State == model.OrderAwaitingPayment && items[i].Provider == model.ProviderEpay {
				continue
			}
			evict = i
			break
		}
		if evict < 0 {
			return items, false
		}
		items = append(items[:evict], items[evict+1:]...)
	}
	return prependCapped(items, item, limit), true
}

// checkOpenOrderLocked enforces one open order per user. An active order whose
// grant expired no longer blocks: expiry is what permits a renewal.
func (s *Store) checkOpenOrderLocked(userID string, now time.Time) error {
	for i := range s.orders {
		order := &s.orders[i]
		if order.UserID != userID {
			continue
		}
		switch order.State {
		case model.OrderAwaitingPayment, model.OrderPaid:
			return conflictf("user %q already has an order awaiting activation", userID)
		case model.OrderActive:
			if !s.grantLeaseActiveLocked(&order.Order, now) {
				continue
			}
			return conflictf("user %q already holds an unexpired entitlement", userID)
		}
	}
	return nil
}

// grantLeaseActiveLocked reports whether an order's grant is backed by a live
// serving lease right now. The grant's ExpiresAt alone is not enough: a daemon
// restart, a node revocation or an operator revoke ends the lease before the
// paid term expires, and an order whose referenced lease is gone must not
// block a renewal or be reported as serving. A revoked grant is never active.
func (s *Store) grantLeaseActiveLocked(order *model.Order, now time.Time) bool {
	if order == nil || order.Grant == nil || order.Grant.RevokedAt != nil {
		return false
	}
	expires, ok := leaseExpiry(order.Grant.ExpiresAt)
	if !ok || !expires.After(now) {
		return false
	}
	for i := range s.leases {
		lease := &s.leases[i]
		if lease.ID == order.Grant.LeaseID && lease.UserID == order.UserID &&
			lease.ExpiresAt == order.Grant.ExpiresAt {
			return true
		}
	}
	return false
}

// ReissueCandidate returns the newest active order for tenantID, userID and
// planID whose grant lease has ended but whose paid term is unexpired. It is
// the portal create path's signal that the customer already owns service and
// should be re-issued instead of being charged for a new order.
func (s *Store) ReissueCandidate(tenantID, userID, planID string) (model.Order, bool) {
	tenantID = strings.TrimSpace(tenantID)
	userID = strings.TrimSpace(userID)
	planID = strings.TrimSpace(planID)
	if tenantID == "" || userID == "" {
		return model.Order{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := s.now()
	for i := range s.orders {
		order := &s.orders[i].Order
		if order.TenantID != tenantID || order.UserID != userID || order.State != model.OrderActive {
			continue
		}
		if planID != "" && order.PlanID != planID {
			continue
		}
		if order.Grant == nil || order.Grant.RevokedAt != nil {
			continue
		}
		expires, ok := leaseExpiry(order.Grant.ExpiresAt)
		if !ok || !expires.After(now) {
			continue
		}
		if s.grantLeaseActiveLocked(order, now) {
			continue
		}
		return cloneOrder(*order), true
	}
	return model.Order{}, false
}

// Orders returns every order, newest first. It is the admin view.
func (s *Store) Orders() []model.Order {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.Order, len(s.orders))
	for i, record := range s.orders {
		out[i] = cloneOrder(record.Order)
	}
	return out
}

// OrdersForTenant returns one tenant's orders, newest first. The filter runs
// before any pagination, so a page boundary can never expose another tenant.
func (s *Store) OrdersForTenant(tenantID string) []model.Order {
	tenantID = strings.TrimSpace(tenantID)
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.Order, 0, len(s.orders))
	for _, record := range s.orders {
		if record.TenantID == tenantID {
			out = append(out, cloneOrder(record.Order))
		}
	}
	return out
}

// Order returns one order by id.
func (s *Store) Order(id string) (model.Order, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	idx := s.orderIndexLocked(strings.TrimSpace(id))
	if idx < 0 {
		return model.Order{}, false
	}
	return cloneOrder(s.orders[idx].Order), true
}

// TenantOrder returns one order only when it belongs to tenantID. A foreign or
// unknown id reports the same ErrNotFound, so an id probe learns nothing.
func (s *Store) TenantOrder(tenantID, orderID string) (model.Order, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	idx := s.orderIndexLocked(strings.TrimSpace(orderID))
	if idx < 0 || s.orders[idx].TenantID != strings.TrimSpace(tenantID) {
		return model.Order{}, notFoundf("order %q not found", orderID)
	}
	return cloneOrder(s.orders[idx].Order), nil
}

// MarkOrderCheckout records the provider a customer chose at checkout. It is
// an intent marker, never a payment: the state stays awaiting_payment and no
// grant or payment event is created. Only an owner-scoped awaiting order may
// change provider, and a repeat of the same provider is a no-op.
func (s *Store) MarkOrderCheckout(tenantID, orderID, provider string) (model.Order, error) {
	tenantID = strings.TrimSpace(tenantID)
	orderID = strings.TrimSpace(orderID)
	provider = strings.TrimSpace(provider)
	switch provider {
	case model.ProviderManual, model.ProviderStripe, model.ProviderEpay:
	default:
		return model.Order{}, invalidf("unknown payment provider %q", provider)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.orderIndexLocked(orderID)
	if idx < 0 || s.orders[idx].TenantID != tenantID {
		return model.Order{}, notFoundf("order %q not found", orderID)
	}
	order := &s.orders[idx].Order
	if order.State != model.OrderAwaitingPayment {
		return model.Order{}, conflictf("order %q is %s; only awaiting_payment can be checked out",
			order.ID, order.State)
	}
	if order.Provider == provider {
		return cloneOrder(*order), nil
	}
	order.Provider = provider
	s.addAuditLocked(model.SeverityInfo, "tenant:"+tenantID, "order.checkout", order.ID,
		"checkout provider selected",
		map[string]string{"orderId": order.ID, "provider": provider})
	return cloneOrder(*order), nil
}

// PendingEpayOrders returns the awaiting_payment orders a customer checked out
// through epay, oldest first so the bounded reconciler batch cannot starve an
// old order behind a stream of new checkouts. An order that was never checked
// out, or one already quarantined for review, stays out of the queue.
func (s *Store) PendingEpayOrders() []model.Order {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.Order, 0)
	for i := len(s.orders) - 1; i >= 0; i-- {
		record := s.orders[i]
		if record.State == model.OrderAwaitingPayment && record.Provider == model.ProviderEpay &&
			!record.ReviewRequired {
			out = append(out, cloneOrder(record.Order))
		}
	}
	return out
}

// CancelOrder cancels an awaiting_payment order. Cancelling an already
// cancelled order returns it unchanged (the command is idempotent); every other
// state is a conflict because paid and terminal orders never resume.
func (s *Store) CancelOrder(orderID, actor string) (model.Order, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.orderIndexLocked(strings.TrimSpace(orderID))
	if idx < 0 {
		return model.Order{}, notFoundf("order %q not found", orderID)
	}
	return s.cancelOrderLocked(idx, actor)
}

// CancelTenantOrder is the owner-scoped cancel: a foreign id is ErrNotFound.
func (s *Store) CancelTenantOrder(tenantID, orderID, actor string) (model.Order, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.orderIndexLocked(strings.TrimSpace(orderID))
	if idx < 0 || s.orders[idx].TenantID != strings.TrimSpace(tenantID) {
		return model.Order{}, notFoundf("order %q not found", orderID)
	}
	return s.cancelOrderLocked(idx, actor)
}

func (s *Store) cancelOrderLocked(idx int, actor string) (model.Order, error) {
	order := &s.orders[idx].Order
	switch order.State {
	case model.OrderCancelled:
		return cloneOrder(*order), nil
	case model.OrderAwaitingPayment:
		order.State = model.OrderCancelled
		order.CancelledAt = ptrString(iso(s.now()))
		s.addAuditLocked(model.SeverityInfo, actor, "order.cancelled", order.ID,
			"awaiting payment cancelled",
			map[string]string{
				"orderId":  order.ID,
				"tenantId": order.TenantID,
				"from":     string(model.OrderAwaitingPayment),
				"to":       string(model.OrderCancelled),
			})
		return cloneOrder(*order), nil
	default:
		return model.Order{}, conflictf("order %q is %s; only awaiting_payment can be cancelled",
			order.ID, order.State)
	}
}

// RecordManualRefund records an already completed full offline refund. The
// amount comes from the immutable snapshot, never from the caller, and the
// reference is required so the reversal has evidence behind it.
func (s *Store) RecordManualRefund(orderID, reference, reason, actor string) (model.Order, error) {
	ref, err := normalizeOrderReference(reference)
	if err != nil {
		return model.Order{}, err
	}
	reason, err = normalizeOrderReason(reason)
	if err != nil {
		return model.Order{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.orderIndexLocked(strings.TrimSpace(orderID))
	if idx < 0 {
		return model.Order{}, notFoundf("order %q not found", orderID)
	}
	order := &s.orders[idx].Order
	event := model.PaymentEvent{
		Provider:    model.ProviderManual,
		ID:          manualEventID("refund", order.ID, ref),
		Kind:        model.PaymentRefunded,
		OrderID:     order.ID,
		ProviderRef: ref,
		AmountMinor: order.Plan.AmountMinor,
		Currency:    order.Plan.Currency,
		OccurredAt:  iso(s.now()),
	}
	return s.applyEventLocked(idx, event, reason, actor)
}

// RecordManualChargeback records external chargeback evidence. It never
// transfers money and always flags the order for review.
func (s *Store) RecordManualChargeback(orderID, reference, reason, actor string) (model.Order, error) {
	ref, err := normalizeOrderReference(reference)
	if err != nil {
		return model.Order{}, err
	}
	reason, err = normalizeOrderReason(reason)
	if err != nil {
		return model.Order{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.orderIndexLocked(strings.TrimSpace(orderID))
	if idx < 0 {
		return model.Order{}, notFoundf("order %q not found", orderID)
	}
	order := &s.orders[idx].Order
	event := model.PaymentEvent{
		Provider:    model.ProviderManual,
		ID:          manualEventID("chargeback", order.ID, ref),
		Kind:        model.PaymentChargedBack,
		OrderID:     order.ID,
		ProviderRef: ref,
		AmountMinor: order.Plan.AmountMinor,
		Currency:    order.Plan.Currency,
		OccurredAt:  iso(s.now()),
	}
	return s.applyEventLocked(idx, event, reason, actor)
}

// ApplyPaymentEvent applies one provider fact through the single store-level
// event path. Manual settlement events carry no amount (the provider never sees
// the order); the store stamps the snapshot amount. Webhook events must match
// the snapshot exactly. A mismatch, a late settlement or a partial refund is
// recorded for review and grants nothing.
func (s *Store) ApplyPaymentEvent(provider string, event model.PaymentEvent, actor string) (model.Order, error) {
	provider = strings.TrimSpace(provider)
	event.Provider = provider
	event.ID = strings.TrimSpace(event.ID)
	event.OrderID = strings.TrimSpace(event.OrderID)
	event.ProviderRef = strings.TrimSpace(event.ProviderRef)
	switch {
	case provider == "":
		return model.Order{}, invalidf("provider is required")
	case event.ID == "":
		return model.Order{}, invalidf("payment event id is required")
	case !model.ValidPaymentEventKind(event.Kind):
		return model.Order{}, invalidf("payment event kind must be settled, refunded or charged_back")
	case event.OrderID == "":
		return model.Order{}, invalidf("payment event orderId is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.orderIndexLocked(event.OrderID)
	if idx < 0 {
		return model.Order{}, notFoundf("order %q not found", event.OrderID)
	}
	return s.applyEventLocked(idx, event, "", actor)
}

// applyEventLocked is the locked command transaction: it validates the event
// against the immutable snapshot, records the event, then mutates the order and
// entitlement. The caller holds s.mu for writing.
func (s *Store) applyEventLocked(idx int, event model.PaymentEvent, reason, actor string) (model.Order, error) {
	order := &s.orders[idx].Order

	// Duplicate provider event IDs replay the recorded result. A settlement
	// replay on a still-paid order retries activation: payment is already
	// recorded, and the retry must be able to grant once.
	if s.paymentEventIndexLocked(event.Provider, event.ID) >= 0 {
		if event.Kind == model.PaymentSettled && order.State == model.OrderPaid {
			s.activateOrderLocked(idx, actor)
			return cloneOrder(*order), nil
		}
		return cloneOrder(*order), nil
	}

	// An epay callback may only settle an order that selected epay. A manual
	// attestation remains the operator's override for an epay checkout that
	// was actually paid offline, but an epay callback must never turn a
	// never-checked-out manual order into a payment. (Stripe keeps its
	// existing webhook path: its tests and contract do not carry a checkout
	// intent marker.)
	if event.Provider == model.ProviderEpay && order.Provider != model.ProviderEpay {
		return model.Order{}, conflictf("order %q was not checked out with epay", order.ID)
	}

	if event.OccurredAt == "" {
		event.OccurredAt = iso(s.now())
	}
	if event.Provider == model.ProviderManual && event.AmountMinor == 0 && event.Currency == "" {
		event.AmountMinor = order.Plan.AmountMinor
		event.Currency = order.Plan.Currency
	}
	// Currency is never negotiable. A refund may legitimately be partial: the
	// refund policy decides whether that is reviewable, while settlement and
	// chargeback must match the snapshot amount exactly.
	mismatch := !strings.EqualFold(event.Currency, order.Plan.Currency) ||
		(event.AmountMinor != order.Plan.AmountMinor && event.Kind != model.PaymentRefunded)
	if mismatch {
		order.ReviewRequired = true
		s.recordPaymentEventLocked(event, model.PaymentReviewOnly)
		s.addAuditLocked(model.SeverityWarn, actor, "webhook.rejected", order.ID,
			fmt.Sprintf("amount/currency mismatch: event %d %s, order %d %s",
				event.AmountMinor, event.Currency, order.Plan.AmountMinor, order.Plan.Currency),
			map[string]string{"orderId": order.ID, "provider": event.Provider, "eventId": event.ID})
		return cloneOrder(*order), nil
	}

	// A settlement reference is immutable. Manual attestation refuses a second
	// reference outright; a webhook with conflicting settlement evidence is
	// quarantined. Refund and chargeback references identify their own
	// evidence and do not rewrite the settlement reference.
	if event.Kind == model.PaymentSettled && order.ProviderRef != nil && *order.ProviderRef != event.ProviderRef {
		if event.Provider == model.ProviderManual {
			return model.Order{}, conflictf("order %q was settled with a different reference", order.ID)
		}
		order.ReviewRequired = true
		s.recordPaymentEventLocked(event, model.PaymentReviewOnly)
		s.addAuditLocked(model.SeverityWarn, actor, "webhook.rejected", order.ID,
			"conflicting provider reference",
			map[string]string{
				"orderId":  order.ID,
				"provider": event.Provider,
				"eventId":  event.ID,
				"ref":      referenceFingerprint(event.ProviderRef),
			})
		return cloneOrder(*order), nil
	}

	switch event.Kind {
	case model.PaymentSettled:
		return s.applySettlementLocked(idx, event, actor)
	case model.PaymentRefunded:
		return s.applyRefundLocked(idx, event, reason, actor)
	case model.PaymentChargedBack:
		return s.applyChargebackLocked(idx, event, reason, actor)
	default:
		return model.Order{}, invalidf("unsupported payment event kind %q", event.Kind)
	}
}

func (s *Store) applySettlementLocked(idx int, event model.PaymentEvent, actor string) (model.Order, error) {
	order := &s.orders[idx].Order
	switch order.State {
	case model.OrderAwaitingPayment:
		order.State = model.OrderPaid
		// The order's provider records how the money actually arrived. A
		// checkout intent may be epay while an admin later attests an offline
		// receipt; the settlement event is the authoritative fact.
		order.Provider = event.Provider
		order.ProviderRef = ptrString(event.ProviderRef)
		order.PaidAt = ptrString(iso(s.now()))
		s.recordPaymentEventLocked(event, model.PaymentApplied)
		s.addAuditLocked(model.SeverityInfo, actor, "payment.settled", order.ID,
			fmt.Sprintf("%s · %s · ref %s", order.Plan.Name,
				formatMinor(event.AmountMinor, event.Currency),
				referenceFingerprint(event.ProviderRef)),
			map[string]string{
				"orderId":  order.ID,
				"provider": event.Provider,
				"eventId":  event.ID,
				"amount":   fmt.Sprintf("%d", event.AmountMinor),
				"currency": event.Currency,
				"from":     string(model.OrderAwaitingPayment),
				"to":       string(model.OrderPaid),
			})
		s.activateOrderLocked(idx, actor)
		return cloneOrder(*order), nil
	case model.OrderPaid:
		if order.ProviderRef == nil {
			order.ProviderRef = ptrString(event.ProviderRef)
		}
		s.recordPaymentEventLocked(event, model.PaymentApplied)
		s.addAuditLocked(model.SeverityInfo, actor, "payment.settled", order.ID,
			"settlement replay; activation retried",
			map[string]string{"orderId": order.ID, "provider": event.Provider, "eventId": event.ID})
		s.activateOrderLocked(idx, actor)
		return cloneOrder(*order), nil
	case model.OrderActive:
		s.recordPaymentEventLocked(event, model.PaymentNoOp)
		return cloneOrder(*order), nil
	default:
		order.ReviewRequired = true
		s.recordPaymentEventLocked(event, model.PaymentReviewOnly)
		s.addAuditLocked(model.SeverityWarn, actor, "webhook.rejected", order.ID,
			"late settlement on terminal order "+string(order.State),
			map[string]string{"orderId": order.ID, "provider": event.Provider, "eventId": event.ID})
		return cloneOrder(*order), nil
	}
}

func (s *Store) applyRefundLocked(idx int, event model.PaymentEvent, reason, actor string) (model.Order, error) {
	order := &s.orders[idx].Order
	if order.State == model.OrderRefunded {
		s.recordPaymentEventLocked(event, model.PaymentDuplicate)
		return cloneOrder(*order), nil
	}
	decision := RefundReview
	if order.State == model.OrderPaid || order.State == model.OrderActive {
		decision = s.refundDecisionLocked(RefundRequest{
			Order:       *order,
			AmountMinor: event.AmountMinor,
			Reason:      reason,
			Reference:   event.ProviderRef,
			Provider:    event.Provider,
		})
	}
	if decision != RefundAllow {
		order.ReviewRequired = true
		s.recordPaymentEventLocked(event, model.PaymentReviewOnly)
		detail := "refund requires review"
		if decision == RefundDeny {
			detail = "refund denied by policy"
		} else if event.AmountMinor != order.Plan.AmountMinor {
			detail = fmt.Sprintf("partial refund %d of %d requires review",
				event.AmountMinor, order.Plan.AmountMinor)
		}
		s.addAuditLocked(model.SeverityWarn, actor, "refund.review", order.ID, detail,
			map[string]string{
				"orderId":  order.ID,
				"provider": event.Provider,
				"eventId":  event.ID,
				"amount":   fmt.Sprintf("%d", event.AmountMinor),
				"currency": event.Currency,
				"ref":      referenceFingerprint(event.ProviderRef),
			})
		return cloneOrder(*order), nil
	}
	from := order.State
	order.State = model.OrderRefunded
	order.RefundedAt = ptrString(iso(s.now()))
	revoked := s.revokeGrantLocked(idx, actor)
	s.recordPaymentEventLocked(event, model.PaymentApplied)
	s.addAuditLocked(model.SeverityInfo, actor, "refund.completed", order.ID,
		fmt.Sprintf("full offline refund · %s · grant revoked %t",
			formatMinor(event.AmountMinor, event.Currency), revoked),
		map[string]string{
			"orderId":  order.ID,
			"provider": event.Provider,
			"eventId":  event.ID,
			"amount":   fmt.Sprintf("%d", event.AmountMinor),
			"currency": event.Currency,
			"ref":      referenceFingerprint(event.ProviderRef),
			"from":     string(from),
			"to":       string(model.OrderRefunded),
		})
	return cloneOrder(*order), nil
}

func (s *Store) applyChargebackLocked(idx int, event model.PaymentEvent, reason, actor string) (model.Order, error) {
	order := &s.orders[idx].Order
	if order.State == model.OrderChargedBack {
		s.recordPaymentEventLocked(event, model.PaymentDuplicate)
		return cloneOrder(*order), nil
	}
	switch order.State {
	case model.OrderPaid, model.OrderActive, model.OrderRefunded:
		from := order.State
		order.State = model.OrderChargedBack
		order.ReviewRequired = true
		revoked := s.revokeGrantLocked(idx, actor)
		s.recordPaymentEventLocked(event, model.PaymentApplied)
		s.addAuditLocked(model.SeverityWarn, actor, "chargeback.recorded", order.ID,
			fmt.Sprintf("chargeback recorded · grant revoked %t · %s", revoked,
				referenceFingerprint(event.ProviderRef)),
			map[string]string{
				"orderId":  order.ID,
				"provider": event.Provider,
				"eventId":  event.ID,
				"amount":   fmt.Sprintf("%d", event.AmountMinor),
				"currency": event.Currency,
				"ref":      referenceFingerprint(event.ProviderRef),
				"from":     string(from),
				"to":       string(model.OrderChargedBack),
			})
		return cloneOrder(*order), nil
	default:
		order.ReviewRequired = true
		s.recordPaymentEventLocked(event, model.PaymentReviewOnly)
		s.addAuditLocked(model.SeverityWarn, actor, "chargeback.recorded", order.ID,
			"chargeback without a settled payment; review required",
			map[string]string{"orderId": order.ID, "provider": event.Provider, "eventId": event.ID})
		return cloneOrder(*order), nil
	}
}

// refundDecisionLocked applies the installed policy or the default one. The
// default allows only a full refund of a paid/active order with a reference.
func (s *Store) refundDecisionLocked(request RefundRequest) RefundDecision {
	if s.refundPolicy != nil {
		return s.refundPolicy(request)
	}
	if request.AmountMinor != request.Order.Plan.AmountMinor {
		return RefundReview
	}
	if strings.TrimSpace(request.Reference) == "" {
		return RefundReview
	}
	if request.Order.State != model.OrderPaid && request.Order.State != model.OrderActive {
		return RefundReview
	}
	return RefundAllow
}

// activateOrderLocked creates the order's entitlement exactly once. A grant is
// keyed by the unique order id, so a retry or a concurrent duplicate reuses the
// existing lease and never extends the TTL or overwrites the quota twice.
//
// Activation requires an active tenant and an enabled linked user. A blocked
// activation leaves the order paid, records payment.activation_retry and is
// retryable; the payment itself stays recorded.
func (s *Store) activateOrderLocked(idx int, actor string) bool {
	record := &s.orders[idx]
	order := &record.Order
	if order.State != model.OrderPaid {
		return order.State == model.OrderActive
	}
	now := s.now()
	s.sweepExpiredLocked(now)
	if order.Grant != nil {
		if order.Grant.RevokedAt == nil {
			if s.grantLeaseActiveLocked(order, now) {
				order.State = model.OrderActive
				if order.ActivatedAt == nil {
					order.ActivatedAt = ptrString(iso(now))
				}
				return true
			}
			// The grant is still owned by the order but its lease ended (for
			// example a daemon restart). Reactivating must not pretend the
			// ended lease is still serving; the explicit re-issue path creates
			// the replacement lease without creating a second grant.
			s.retryBlockedLocked(order, actor, "grant_lease_ended",
				"entitlement lease has ended; reissue required")
			return false
		}
		s.retryBlockedLocked(order, actor, "grant_revoked", "entitlement was already revoked")
		return false
	}

	tenantIdx := s.tenantIndexLocked(order.TenantID)
	if tenantIdx < 0 {
		s.retryBlockedLocked(order, actor, "tenant_not_found", "tenant not found")
		return false
	}
	tenant := &s.tenants[tenantIdx]
	if tenant.Status != model.TenantActive {
		s.retryBlockedLocked(order, actor, "tenant_inactive", "tenant is not active")
		return false
	}
	if tenant.UserID == nil || *tenant.UserID != order.UserID {
		s.retryBlockedLocked(order, actor, "tenant_user_mismatch", "tenant is not linked to this user")
		return false
	}
	userIdx := s.userIndexLocked(order.UserID)
	if userIdx < 0 {
		s.retryBlockedLocked(order, actor, "user_not_found", "user not found")
		return false
	}
	user := &s.users[userIdx]
	if !user.Enabled {
		s.retryBlockedLocked(order, actor, "user_suspended", "user is suspended")
		return false
	}

	accountIdx, nodeIdx, platform, err := s.pickOrderLeaseTargetLocked(*user, now)
	if err != nil {
		s.retryBlockedLocked(order, actor, activationBlockedCode(err), err.Error())
		return false
	}
	account := s.accounts[accountIdx]
	node := s.nodes[nodeIdx]

	paidAt := now
	if order.PaidAt != nil {
		if parsed, ok := leaseExpiry(*order.PaidAt); ok {
			paidAt = parsed
		}
	}
	expiresAt := paidAt.Add(time.Duration(order.Plan.LeaseTTLSeconds) * time.Second)

	lease := model.Lease{
		// The entitlement lease id is derived from the order id, which is
		// monotonic, so a later lease can never reuse the id an older grant
		// still references after expiry.
		ID:           "lease-" + order.ID,
		UserID:       user.ID,
		UserName:     user.Name,
		AccountID:    account.ID,
		AccountLabel: account.Label,
		Platform:     platform,
		NodeID:       node.ID,
		NodeLabel:    node.Label,
		Region:       node.Region,
		EgressIP:     node.EgressIP,
		Protocol:     node.Protocol,
		GatePort:     node.GatePort,
		TTLSeconds:   order.Plan.LeaseTTLSeconds,
		ExpiresAt:    iso(expiresAt),
		Sticky:       true,
		Status:       model.LeaseActive,
		CreatedAt:    iso(now),
		// An order lease is a normal entitlement lease: it carries the exact
		// slot and the generation it was issued under, so slot/generation
		// cleanup and audit can attribute it.
		Slot:       node.Slot,
		Generation: s.slotGenerationLocked(node),
	}
	s.leases = append(s.leases, lease)
	s.nodes[nodeIdx].InUse++

	previousQuota := copyInt64(user.QuotaBytesMonth)
	user.QuotaBytesMonth = ptrInt64(order.Plan.QuotaBytes)
	order.Grant = &model.EntitlementGrant{
		OrderID:            order.ID,
		LeaseID:            lease.ID,
		QuotaBytes:         order.Plan.QuotaBytes,
		PreviousQuotaBytes: previousQuota,
		ExpiresAt:          lease.ExpiresAt,
	}
	order.State = model.OrderActive
	order.ActivatedAt = ptrString(iso(now))
	s.addAuditLocked(model.SeverityInfo, actor, "lease.create", user.Name,
		fmt.Sprintf("%s · %s · node %s · ttl %s · order %s", platform, account.Label,
			node.Label, (time.Duration(order.Plan.LeaseTTLSeconds)*time.Second).String(), order.ID),
		map[string]string{
			"leaseId":    lease.ID,
			"accountId":  account.ID,
			"nodeId":     node.ID,
			"platform":   string(platform),
			"orderId":    order.ID,
			"slot":       fmt.Sprintf("%d", node.Slot),
			"generation": fmt.Sprintf("%d", lease.Generation),
		})
	s.addAuditLocked(model.SeverityInfo, actor, "entitlement.granted", order.ID,
		fmt.Sprintf("lease %s · quota %d bytes · expires %s", lease.ID, order.Plan.QuotaBytes, lease.ExpiresAt),
		map[string]string{
			"orderId":  order.ID,
			"leaseId":  lease.ID,
			"tenantId": order.TenantID,
			"userId":   order.UserID,
			"quota":    fmt.Sprintf("%d", order.Plan.QuotaBytes),
			"state":    string(model.OrderActive),
		})
	return true
}

// ReissueOrderLease creates a replacement serving lease for an existing paid
// order whose grant no longer has an active lease. A daemon restart ends the
// lease with reason daemon_restart; a node revocation or an operator revoke has
// the same effect on the grant. The operation never creates a second
// entitlement or records a second payment: the same grant is re-pointed at one
// new lease with the grant's original expiry, so a re-issue can restore
// service but cannot extend the paid term.
//
// While the grant still has an active lease the call is refused, so concurrent
// or repeated re-issues cannot allocate a second lease. A grant whose term has
// expired is refused as well: restoring an expired order would be service
// without a valid entitlement.
func (s *Store) ReissueOrderLease(orderID, actor string) (model.Order, model.Lease, error) {
	orderID = strings.TrimSpace(orderID)
	if orderID == "" {
		return model.Order{}, model.Lease{}, invalidf("orderId is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := s.orderIndexLocked(orderID)
	if idx < 0 {
		return model.Order{}, model.Lease{}, notFoundf("order %q not found", orderID)
	}
	order := &s.orders[idx].Order
	if order.State != model.OrderActive {
		return model.Order{}, model.Lease{}, conflictf(
			"order %q is %s; only active orders can reissue an ended lease", order.ID, order.State)
	}
	if order.Grant == nil {
		return model.Order{}, model.Lease{}, conflictf("order %q has no entitlement grant", order.ID)
	}
	if order.Grant.RevokedAt != nil {
		return model.Order{}, model.Lease{}, conflictf("order %q entitlement was revoked", order.ID)
	}
	now := s.now()
	s.sweepExpiredLocked(now)
	expires, ok := leaseExpiry(order.Grant.ExpiresAt)
	if !ok || !expires.After(now) {
		return model.Order{}, model.Lease{}, conflictf(
			"order %q entitlement expired at %s", order.ID, order.Grant.ExpiresAt)
	}
	if s.leaseIndexLocked(order.Grant.LeaseID) >= 0 {
		return model.Order{}, model.Lease{}, conflictf(
			"order %q entitlement lease %s is still active", order.ID, order.Grant.LeaseID)
	}

	tenantIdx := s.tenantIndexLocked(order.TenantID)
	if tenantIdx < 0 {
		return model.Order{}, model.Lease{}, conflictf("tenant %q not found", order.TenantID)
	}
	tenant := s.tenants[tenantIdx]
	if tenant.Status != model.TenantActive {
		return model.Order{}, model.Lease{}, conflictf("tenant %q is not active", tenant.ID)
	}
	if tenant.UserID == nil || *tenant.UserID != order.UserID {
		return model.Order{}, model.Lease{}, conflictf("tenant is not linked to this user")
	}
	userIdx := s.userIndexLocked(order.UserID)
	if userIdx < 0 {
		return model.Order{}, model.Lease{}, conflictf("user %q not found", order.UserID)
	}
	user := &s.users[userIdx]
	if !user.Enabled {
		return model.Order{}, model.Lease{}, conflictf("user %q is suspended", user.Name)
	}

	oldLeaseID := order.Grant.LeaseID
	var endedPlatform model.Platform
	for _, end := range s.leaseEnds {
		if end.Lease.ID == oldLeaseID {
			endedPlatform = s.leasePlatformLocked(end.Lease)
			break
		}
	}
	accountIdx, nodeIdx, platform, err := s.pickOrderLeaseTargetLocked(*user, now)
	if err != nil {
		return model.Order{}, model.Lease{}, err
	}
	// Reissue replaces only this order's ended lease. It must not allocate a
	// second lease on the selected user/platform. Nor may a saturated preferred
	// platform silently turn reissue into a different-platform lease alongside
	// an existing lease for the ended grant's platform.
	for _, existing := range s.leases {
		if existing.UserID != user.ID {
			continue
		}
		existingPlatform := s.leasePlatformLocked(existing)
		if existingPlatform == platform || (endedPlatform != "" && endedPlatform != platform) {
			return model.Order{}, model.Lease{}, leaseErrorf(ErrConflict, "lease_conflict",
				"user %q already holds an active %s lease", user.Name, existingPlatform)
		}
	}
	account := s.accounts[accountIdx]
	node := s.nodes[nodeIdx]

	remaining := int(expires.Sub(now) / time.Second)
	if remaining < 1 {
		remaining = 1
	}
	lease := model.Lease{
		ID:           s.nextOrderLeaseIDLocked(order.ID),
		UserID:       user.ID,
		UserName:     user.Name,
		AccountID:    account.ID,
		AccountLabel: account.Label,
		Platform:     platform,
		NodeID:       node.ID,
		NodeLabel:    node.Label,
		Region:       node.Region,
		EgressIP:     node.EgressIP,
		Protocol:     node.Protocol,
		GatePort:     node.GatePort,
		TTLSeconds:   remaining,
		ExpiresAt:    order.Grant.ExpiresAt,
		Sticky:       true,
		Status:       model.LeaseActive,
		CreatedAt:    iso(now),
		Slot:         node.Slot,
		Generation:   s.slotGenerationLocked(node),
	}
	s.leases = append(s.leases, lease)
	s.nodes[nodeIdx].InUse++
	order.Grant.LeaseID = lease.ID
	// order.Grant.ExpiresAt intentionally stays the original paid-term
	// expiry; the replacement lease must not outlive what the customer paid.

	s.addAuditLocked(model.SeverityInfo, actor, "lease.reissued", user.Name,
		fmt.Sprintf("%s · %s · node %s · order %s · %s → %s", platform, account.Label,
			node.Label, order.ID, oldLeaseID, lease.ID),
		map[string]string{
			"leaseId":         lease.ID,
			"previousLeaseId": oldLeaseID,
			"orderId":         order.ID,
			"tenantId":        order.TenantID,
			"userId":          order.UserID,
			"accountId":       account.ID,
			"nodeId":          node.ID,
			"slot":            fmt.Sprintf("%d", node.Slot),
			"generation":      fmt.Sprintf("%d", lease.Generation),
			"expiresAt":       lease.ExpiresAt,
			"reason":          s.leaseEndReasonLocked(oldLeaseID),
		})
	return cloneOrder(*order), lease, nil
}

// RollbackOrderReissue removes a replacement lease whose publication failed
// and restores its grant to the ended lease it was meant to replace. The order
// remains active with an unserved grant, so an explicit retry can reissue it;
// a lease.reissued response or event is not valid until publication succeeds.
// The replacement must still be present and still be the grant's current lease,
// otherwise this method refuses to alter state rather than rolling back a
// concurrently changed entitlement.
func (s *Store) RollbackOrderReissue(orderID, leaseID, previousLeaseID, reason string) error {
	orderID = strings.TrimSpace(orderID)
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := s.orderIndexLocked(orderID)
	if idx < 0 {
		return notFoundf("order %q not found", orderID)
	}
	order := &s.orders[idx].Order
	leaseIdx := s.leaseIndexLocked(leaseID)
	if leaseIdx < 0 {
		return notFoundf("lease %q not found", leaseID)
	}
	lease := s.leases[leaseIdx]
	if order.Grant == nil || order.Grant.LeaseID != leaseID ||
		lease.UserID != order.UserID || lease.ExpiresAt != order.Grant.ExpiresAt {
		return conflictf("order %q grant no longer owns replacement lease %q", orderID, leaseID)
	}
	s.leases = append(s.leases[:leaseIdx], s.leases[leaseIdx+1:]...)
	if nodeIdx := s.nodeIndexLocked(lease.NodeID); nodeIdx >= 0 && s.nodes[nodeIdx].InUse > 0 {
		s.nodes[nodeIdx].InUse--
	}
	order.Grant.LeaseID = previousLeaseID
	detail := clientMessage(reason)
	if detail == "" {
		detail = "publication failed"
	}
	s.addAuditLocked(model.SeverityError, "system", "lease.publish_failed", lease.UserName,
		fmt.Sprintf("Replacement lease on %s not reissued · %s", lease.NodeLabel, detail),
		map[string]string{
			"leaseId": lease.ID, "previousLeaseId": previousLeaseID,
			"orderId": order.ID, "accountId": lease.AccountID, "nodeId": lease.NodeID,
			"platform":   string(s.leasePlatformLocked(lease)),
			"slot":       strconv.Itoa(lease.Slot),
			"generation": strconv.FormatUint(lease.Generation, 10), "reason": detail,
		})
	return nil
}

// FulfilmentAttempt is what one BeginFulfilment call reserved: the 1-based
// attempt number and the fencing token the matching AbortFulfilment must
// present. P0-2B extends the publication attempt with the provisional lease
// it reserves.
type FulfilmentAttempt struct {
	OrderID string
	Mode    model.FulfilmentMode
	Number  int
	LeaseID string
}

// EnqueueFulfilment attaches the order's single publication job. It is
// idempotent for the same mode: a replay returns the existing job untouched,
// so a duplicate settlement cannot create a second job. A job is created only
// for an order that can use it — paid without a grant for initial, active with
// an ended unexpired grant for reissue — never for a terminal or
// review-required order, and it freezes the paid term the job must not
// outlive. An initial job enqueued after that term already ended is recorded
// directly as dead-lettered, so the payment/refund liability stays visible
// instead of being silently dropped.
func (s *Store) EnqueueFulfilment(orderID string, mode model.FulfilmentMode) (model.Order, error) {
	orderID = strings.TrimSpace(orderID)
	mode = model.FulfilmentMode(strings.TrimSpace(string(mode)))
	switch {
	case orderID == "":
		return model.Order{}, invalidf("orderId is required")
	case !model.ValidFulfilmentMode(mode):
		return model.Order{}, invalidf("fulfilment mode must be initial or reissue")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.orderIndexLocked(orderID)
	if idx < 0 {
		return model.Order{}, notFoundf("order %q not found", orderID)
	}
	order := &s.orders[idx].Order
	if terminalOrderState(order.State) {
		return model.Order{}, conflictf("order %q is %s; terminal orders never resume", order.ID, order.State)
	}
	if order.ReviewRequired {
		return model.Order{}, conflictf("order %q requires review; fulfilment is blocked", order.ID)
	}
	if order.Fulfilment != nil {
		if order.Fulfilment.Mode == mode {
			return cloneOrder(*order), nil
		}
		return model.Order{}, conflictf("order %q already has a %s fulfilment job",
			order.ID, order.Fulfilment.Mode)
	}

	now := s.now()
	s.sweepExpiredLocked(now)
	switch mode {
	case model.FulfilmentInitial:
		if order.State != model.OrderPaid {
			return model.Order{}, conflictf("order %q is %s; an initial job needs a paid order",
				order.ID, order.State)
		}
		if order.Grant != nil {
			return model.Order{}, conflictf("order %q already owns an entitlement grant", order.ID)
		}
	case model.FulfilmentReissue:
		if order.State != model.OrderActive {
			return model.Order{}, conflictf("order %q is %s; a reissue job needs an active order",
				order.ID, order.State)
		}
		if order.Grant == nil || order.Grant.RevokedAt != nil {
			return model.Order{}, conflictf("order %q has no live entitlement grant to reissue", order.ID)
		}
		expires, ok := leaseExpiry(order.Grant.ExpiresAt)
		if !ok || !expires.After(now) {
			return model.Order{}, conflictf("order %q entitlement expired at %s",
				order.ID, order.Grant.ExpiresAt)
		}
		if s.grantLeaseActiveLocked(order, now) {
			return model.Order{}, conflictf("order %q entitlement lease %s is still active",
				order.ID, order.Grant.LeaseID)
		}
	}

	expires, ok := orderFulfilmentExpiry(*order, mode)
	if !ok {
		return model.Order{}, conflictf("order %q has no valid paid term", order.ID)
	}
	job := &model.FulfilmentJob{
		OrderID:              order.ID,
		Mode:                 mode,
		State:                model.FulfilmentQueued,
		EntitlementExpiresAt: iso(expires),
		NextAttemptAt:        ptrString(iso(now)),
		UpdatedAt:            iso(now),
	}
	if !expires.After(now) {
		// An initial job whose paid term already ended can never serve.
		s.deadLetterFulfilmentLocked(order, job, model.FulfilmentReasonEntitlementExpired,
			"paid term ended before fulfilment", now)
	}
	order.Fulfilment = job
	return cloneOrder(*order), nil
}

// BeginFulfilment starts one publication attempt. It is the store's
// single-winner gate: only a queued, unblocked, non-dead-lettered job on a
// non-terminal, non-review order may begin, and the attempt count and the
// publishing state are written under the same lock, so concurrent or repeated
// calls cannot start a second attempt. A queued wait or a duplicate callback
// never increments Attempts; only this call does. A job whose frozen paid term
// has ended is dead-lettered here instead of consuming an attempt: service
// after expiry is impossible, so the job becomes an operator item.
func (s *Store) BeginFulfilment(orderID string) (FulfilmentAttempt, error) {
	orderID = strings.TrimSpace(orderID)
	if orderID == "" {
		return FulfilmentAttempt{}, invalidf("orderId is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.orderIndexLocked(orderID)
	if idx < 0 {
		return FulfilmentAttempt{}, notFoundf("order %q not found", orderID)
	}
	order := &s.orders[idx].Order
	if terminalOrderState(order.State) {
		return FulfilmentAttempt{}, conflictf("order %q is %s; terminal orders never resume",
			order.ID, order.State)
	}
	if order.ReviewRequired {
		return FulfilmentAttempt{}, conflictf("order %q requires review; fulfilment is blocked", order.ID)
	}
	job := order.Fulfilment
	switch {
	case job == nil:
		return FulfilmentAttempt{}, conflictf("order %q has no fulfilment job", order.ID)
	case job.Blocked:
		return FulfilmentAttempt{}, conflictf(
			"order %q fulfilment is blocked; an operator retry is required", order.ID)
	case job.State == model.FulfilmentPublishing:
		return FulfilmentAttempt{}, conflictf("order %q already has an attempt publishing", order.ID)
	case job.State == model.FulfilmentDeadLetter:
		return FulfilmentAttempt{}, conflictf(
			"order %q fulfilment is dead-lettered; an operator retry is required", order.ID)
	case job.State != model.FulfilmentQueued:
		return FulfilmentAttempt{}, conflictf("order %q fulfilment job is %s", order.ID, job.State)
	}
	if job.Attempts >= MaxFulfilmentAttempts {
		return FulfilmentAttempt{}, conflictf("order %q used all %d fulfilment attempts",
			order.ID, MaxFulfilmentAttempts)
	}
	switch job.Mode {
	case model.FulfilmentInitial:
		if order.State != model.OrderPaid {
			return FulfilmentAttempt{}, conflictf("order %q is %s; an initial attempt needs a paid order",
				order.ID, order.State)
		}
	case model.FulfilmentReissue:
		if order.State != model.OrderActive {
			return FulfilmentAttempt{}, conflictf("order %q is %s; a reissue attempt needs an active order",
				order.ID, order.State)
		}
	}

	now := s.now()
	expires, ok := leaseExpiry(job.EntitlementExpiresAt)
	if !ok {
		return FulfilmentAttempt{}, conflictf("order %q fulfilment job has no valid paid term", order.ID)
	}
	if !expires.After(now) {
		s.deadLetterFulfilmentLocked(order, job, model.FulfilmentReasonEntitlementExpired,
			"paid term ended at "+job.EntitlementExpiresAt, now)
		return FulfilmentAttempt{}, conflictf("order %q entitlement expired at %s",
			order.ID, job.EntitlementExpiresAt)
	}

	job.Attempts++
	job.State = model.FulfilmentPublishing
	job.Blocked = false
	job.AttemptLeaseID = fulfilmentAttemptLeaseID(order.ID, job.Attempts)
	// NextAttemptAt describes when a queued job becomes due; while an attempt
	// is in flight the abort schedules the next one.
	job.NextAttemptAt = nil
	job.UpdatedAt = iso(now)
	return FulfilmentAttempt{
		OrderID: order.ID,
		Mode:    job.Mode,
		Number:  job.Attempts,
		LeaseID: job.AttemptLeaseID,
	}, nil
}

// AbortFulfilment records a failed attempt and schedules the next one. reason
// is a closed code, never free text, so a payment reference, signed URL or
// credential cannot enter the durable job through it. detail is optional
// operator context; it is scrubbed and bounded before it is stored or audited.
// The attempt lease must match the in-flight attempt, so a late abort from an
// earlier attempt cannot overwrite the current schedule. Attempts 1..7 are
// retried after 5s/15s/45s/2m/5m/15m/30m; the eighth failure, or any
// non-retryable failure, dead-letters the job and stops automatic retries.
func (s *Store) AbortFulfilment(orderID, attemptLeaseID string, reason model.FulfilmentReason, detail string, retryable bool) (model.Order, error) {
	orderID = strings.TrimSpace(orderID)
	attemptLeaseID = strings.TrimSpace(attemptLeaseID)
	switch {
	case orderID == "":
		return model.Order{}, invalidf("orderId is required")
	case attemptLeaseID == "":
		return model.Order{}, invalidf("attemptLeaseId is required")
	case !model.ValidFulfilmentReason(reason):
		return model.Order{}, invalidf("fulfilment reason %q is not a known code", string(reason))
	}
	detail = sanitizeFulfilmentDetail(detail)

	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.orderIndexLocked(orderID)
	if idx < 0 {
		return model.Order{}, notFoundf("order %q not found", orderID)
	}
	order := &s.orders[idx].Order
	if terminalOrderState(order.State) {
		return model.Order{}, conflictf("order %q is %s; terminal orders never resume",
			order.ID, order.State)
	}
	job := order.Fulfilment
	switch {
	case job == nil:
		return model.Order{}, conflictf("order %q has no fulfilment job", order.ID)
	case job.State != model.FulfilmentPublishing:
		return model.Order{}, conflictf("order %q has no attempt publishing", order.ID)
	case job.Attempts < 1:
		return model.Order{}, conflictf("order %q attempt count is inconsistent", order.ID)
	case job.AttemptLeaseID != attemptLeaseID:
		return model.Order{}, conflictf("order %q attempt lease %q is stale", order.ID, attemptLeaseID)
	}

	now := s.now()
	if !retryable || job.Attempts >= MaxFulfilmentAttempts {
		s.deadLetterFulfilmentLocked(order, job, reason, detail, now)
		return cloneOrder(*order), nil
	}

	delay := fulfilmentBackoff[job.Attempts-1]
	next := now.Add(delay)
	job.State = model.FulfilmentQueued
	job.Blocked = false
	job.LastReason = reason
	job.LastError = detail
	job.AttemptLeaseID = ""
	job.NextAttemptAt = ptrString(iso(next))
	job.UpdatedAt = iso(now)
	text := fmt.Sprintf("attempt %d failed · retry in %s · %s", job.Attempts, delay, reason)
	if detail != "" {
		text += " · " + detail
	}
	s.addAuditLocked(model.SeverityWarn, "system", "fulfilment.retry", order.ID, text,
		map[string]string{
			"orderId":       order.ID,
			"mode":          string(job.Mode),
			"attempts":      strconv.Itoa(job.Attempts),
			"reason":        string(reason),
			"nextAttemptAt": iso(next),
		})
	return cloneOrder(*order), nil
}

// DueFulfilments returns the queued jobs whose next attempt is due, oldest
// order first so the bounded worker cannot starve an old paid order behind a
// stream of new ones. A publishing or dead-lettered job is never due, and a
// terminal order never returns to the queue.
func (s *Store) DueFulfilments(now time.Time) []model.Order {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.Order, 0)
	for i := len(s.orders) - 1; i >= 0; i-- {
		record := &s.orders[i]
		job := record.Fulfilment
		if job == nil || job.State != model.FulfilmentQueued ||
			terminalOrderState(record.State) {
			continue
		}
		if !fulfilmentDue(job.NextAttemptAt, now) {
			continue
		}
		out = append(out, cloneOrder(record.Order))
	}
	return out
}

// RecoverFulfilments requeues every attempt a restart interrupted. A
// publishing job has no durable proof that its publication completed, so it
// returns to the queue immediately with the same attempt count: only a real
// BeginFulfilment increments Attempts. Queued and dead-lettered jobs are left
// alone; a dead letter is an operator queue, never an automatic retry. A job
// left on a terminal order is stale work and is dropped.
func (s *Store) RecoverFulfilments() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for i := range s.orders {
		order := &s.orders[i].Order
		job := order.Fulfilment
		if job == nil || job.State != model.FulfilmentPublishing {
			continue
		}
		if terminalOrderState(order.State) {
			order.Fulfilment = nil
			continue
		}
		job.State = model.FulfilmentQueued
		job.Blocked = false
		job.AttemptLeaseID = ""
		job.LastReason = model.FulfilmentReasonInterrupted
		job.LastError = ""
		job.NextAttemptAt = ptrString(iso(now))
		job.UpdatedAt = iso(now)
		s.addAuditLocked(model.SeverityWarn, "system", "fulfilment.interrupted", order.ID,
			"publication interrupted by restart; attempt requeued without incrementing attempts",
			map[string]string{
				"orderId":  order.ID,
				"mode":     string(job.Mode),
				"attempts": strconv.Itoa(job.Attempts),
			})
	}
	return nil
}

// fulfilmentAttemptLeaseID derives the fencing token of one attempt. It is
// deterministic from persisted state (order id and attempt count), so a
// restart cannot mint a different token for the same in-flight attempt, and it
// is unique per attempt, so an abort from an earlier attempt is rejected. The
// publication slice replaces the token with the provisional lease it reserves.
func fulfilmentAttemptLeaseID(orderID string, attempt int) string {
	return fmt.Sprintf("lease-%s-a%d", orderID, attempt)
}

// orderFulfilmentExpiry returns the paid term a job must not outlive. Initial
// fulfilment freezes PaidAt+LeaseTTLSeconds; a reissue keeps the grant's
// original expiry so a retry can never extend the paid term.
func orderFulfilmentExpiry(order model.Order, mode model.FulfilmentMode) (time.Time, bool) {
	switch mode {
	case model.FulfilmentInitial:
		if order.PaidAt == nil {
			return time.Time{}, false
		}
		paidAt, ok := leaseExpiry(*order.PaidAt)
		if !ok {
			return time.Time{}, false
		}
		return paidAt.Add(time.Duration(order.Plan.LeaseTTLSeconds) * time.Second), true
	case model.FulfilmentReissue:
		if order.Grant == nil {
			return time.Time{}, false
		}
		return leaseExpiry(order.Grant.ExpiresAt)
	default:
		return time.Time{}, false
	}
}

// deadLetterFulfilmentLocked marks a job as requiring an operator. detail is
// sanitized and bounded here, so every caller is safe; it is repeated only in
// the audit detail, never as a meta value. The caller holds s.mu for writing.
func (s *Store) deadLetterFulfilmentLocked(order *model.Order, job *model.FulfilmentJob,
	reason model.FulfilmentReason, detail string, now time.Time) {
	detail = sanitizeFulfilmentDetail(detail)
	job.State = model.FulfilmentDeadLetter
	job.Blocked = true
	job.AttemptLeaseID = ""
	job.NextAttemptAt = nil
	job.LastReason = reason
	job.LastError = detail
	job.UpdatedAt = iso(now)
	text := fmt.Sprintf("attempt %d · no automatic retry · %s", job.Attempts, reason)
	if detail != "" {
		text += " · " + detail
	}
	s.addAuditLocked(model.SeverityCritical, "system", "fulfilment.dead_letter", order.ID, text,
		map[string]string{
			"orderId":              order.ID,
			"mode":                 string(job.Mode),
			"attempts":             strconv.Itoa(job.Attempts),
			"reason":               string(reason),
			"entitlementExpiresAt": job.EntitlementExpiresAt,
		})
}

// sanitizeFulfilmentDetail reduces an upstream publication message to one
// bounded, single-line operator detail. URLs, email-like values, query or
// fragment tails and reference/token-like runs are replaced wholesale; the
// result is capped at maxFulfilmentDetailLen runes.
func sanitizeFulfilmentDetail(raw string) string {
	text := strings.Join(strings.Fields(raw), " ")
	if text == "" {
		return ""
	}
	text = fulfilmentDetailURL.ReplaceAllString(text, "[redacted]")
	text = fulfilmentDetailEmail.ReplaceAllString(text, "[redacted]")
	text = fulfilmentDetailSecret.ReplaceAllString(text, "$1 [redacted]")
	if i := strings.IndexAny(text, "?#"); i >= 0 {
		text = strings.TrimSpace(text[:i]) + " [redacted]"
	}
	// Long runs are replaced before short numeric tails: otherwise a token
	// such as sk_live_SECRET1234567890 would lose only its digits and keep
	// the credential prefix.
	text = fulfilmentDetailRun.ReplaceAllStringFunc(text, func(run string) string {
		if strings.ContainsAny(run, "0123456789") || len(run) >= 32 {
			return "[redacted]"
		}
		return run
	})
	text = fulfilmentDetailDigits.ReplaceAllString(text, "[redacted]")
	runes := []rune(text)
	if len(runes) > maxFulfilmentDetailLen {
		text = string(runes[:maxFulfilmentDetailLen-1]) + "…"
	}
	return text
}

// fulfilmentDue reports whether a queued job's schedule has arrived. A missing
// or unparsable timestamp is due: work is never stranded because of a bad
// clock string (restore validates it, so this is a safety net).
func fulfilmentDue(next *string, now time.Time) bool {
	if next == nil {
		return true
	}
	at, ok := leaseExpiry(*next)
	if !ok {
		return true
	}
	return !at.After(now)
}

// terminalOrderState reports whether state is one that never resumes.
func terminalOrderState(state model.OrderState) bool {
	switch state {
	case model.OrderCancelled, model.OrderRefunded, model.OrderChargedBack:
		return true
	default:
		return false
	}
}

// nextOrderLeaseIDLocked returns an id for a replacement lease that cannot
// collide with the original order lease or any ended row. The original
// activation uses "lease-<orderID>"; later re-issues append -rN.
func (s *Store) nextOrderLeaseIDLocked(orderID string) string {
	base := "lease-" + orderID
	used := func(id string) bool {
		for _, lease := range s.leases {
			if lease.ID == id {
				return true
			}
		}
		for _, end := range s.leaseEnds {
			if end.Lease.ID == id {
				return true
			}
		}
		return false
	}
	if !used(base) {
		return base
	}
	for n := 1; ; n++ {
		candidate := fmt.Sprintf("%s-r%d", base, n)
		if !used(candidate) {
			return candidate
		}
	}
}

// retryBlockedLocked records why activation could not proceed. The order stays
// paid; the admin or the next settlement retry can grant it once the blocker is
// cleared. code is the stable refusal reason (the lease error's Reason, or one
// of the fixed blockers below) so an operator can tell a capacity refusal from
// a tenant/user blocker without parsing prose.
func (s *Store) retryBlockedLocked(order *model.Order, actor, code, detail string) {
	s.addAuditLocked(model.SeverityWarn, actor, "payment.activation_retry", order.ID,
		"activation blocked: "+clientMessage(detail),
		map[string]string{
			"orderId":  order.ID,
			"tenantId": order.TenantID,
			"userId":   order.UserID,
			"state":    string(order.State),
			"reason":   code,
		})
}

// activationBlockedCode returns the typed reason a target picker refused with.
// The lease path names its own refusal (no_capacity, account_unavailable, …);
// anything else is recorded as the generic blocker instead of losing the code.
func activationBlockedCode(err error) string {
	var leaseErr *LeaseError
	if errors.As(err, &leaseErr) && leaseErr.Reason != "" {
		return leaseErr.Reason
	}
	return "activation_blocked"
}

// revokeGrantLocked releases the order's lease and restores the quota the
// grant replaced. It never re-enables a suspended user or unrevokes a key. A
// missing or already revoked grant is a no-op.
func (s *Store) revokeGrantLocked(idx int, actor string) bool {
	order := &s.orders[idx].Order
	grant := order.Grant
	if grant == nil || grant.RevokedAt != nil {
		return false
	}
	now := iso(s.now())
	leaseIdx := s.leaseIndexLocked(grant.LeaseID)
	if leaseIdx >= 0 {
		lease := s.leases[leaseIdx]
		// A lease id can outlive its row only through a restored/edited
		// snapshot. Revoke only a lease that still matches this grant; an id
		// reused by a newer lease must not lose service to an older reversal.
		if lease.UserID != order.UserID || lease.ExpiresAt != grant.ExpiresAt {
			leaseIdx = -1
		}
	}
	if leaseIdx >= 0 {
		lease := s.leases[leaseIdx]
		if nodeIdx := s.nodeIndexLocked(lease.NodeID); nodeIdx >= 0 && s.nodes[nodeIdx].InUse > 0 {
			s.nodes[nodeIdx].InUse--
		}
		s.leases = append(s.leases[:leaseIdx], s.leases[leaseIdx+1:]...)
		s.addAuditLocked(model.SeverityInfo, actor, "lease.revoke", lease.UserName,
			fmt.Sprintf("Lease on %s revoked by order reversal", lease.NodeLabel),
			map[string]string{
				"leaseId":    lease.ID,
				"accountId":  lease.AccountID,
				"nodeId":     lease.NodeID,
				"slot":       strconv.Itoa(lease.Slot),
				"generation": strconv.FormatUint(lease.Generation, 10),
				"orderId":    order.ID,
			})
	}
	if userIdx := s.userIndexLocked(order.UserID); userIdx >= 0 {
		// Restore only when this grant still owns the quota. An expired older
		// order can coexist with a newer renewal; reversing the old one must
		// not clobber the quota the renewal installed.
		current := s.users[userIdx].QuotaBytesMonth
		if current != nil && *current == grant.QuotaBytes {
			s.users[userIdx].QuotaBytesMonth = copyInt64(grant.PreviousQuotaBytes)
		}
	}
	grant.RevokedAt = ptrString(now)
	s.addAuditLocked(model.SeverityInfo, actor, "entitlement.revoked", order.ID,
		fmt.Sprintf("lease %s released · previous quota restored", grant.LeaseID),
		map[string]string{
			"orderId":  order.ID,
			"leaseId":  grant.LeaseID,
			"tenantId": order.TenantID,
			"userId":   order.UserID,
			"state":    string(order.State),
		})
	return true
}

// pickOrderLeaseTargetLocked chooses the account/node an entitlement runs on.
// A user platform that names a real platform is preferred; otherwise the first
// account with spare concurrency and a free node wins. An order is a generic
// subscription, so it is not tied to the user's free-form platform label.
func (s *Store) pickOrderLeaseTargetLocked(user model.PanelUser, now time.Time) (int, int, model.Platform, error) {
	platform := model.Platform(user.Platform)
	if validPlatform(platform) {
		if accountIdx, nodeIdx, err := s.pickLeaseTargetLocked("", platform, now); err == nil {
			return accountIdx, nodeIdx, platform, nil
		}
	}
	for i := range s.accounts {
		account := s.accounts[i]
		if leaseAccountAvailable(account, now) != nil {
			continue
		}
		if s.accountLeaseCountLocked(account.ID) >= account.Concurrency {
			continue
		}
		// The fallback is not an entitlement shortcut: every account is held
		// to the same verified capacity as the primary path, legacy rows
		// included. A legacy-only account (no slot nodes) is capped by
		// UseCount/AJS_ACCOUNT_SLOTS and its serving nodes just like a
		// slot-bearing one.
		if !s.orderFallbackSlotCapacityLocked(account, now) {
			continue
		}
		nodeIdx := s.pickLeaseNodeLocked(account.ID)
		if nodeIdx < 0 {
			continue
		}
		return i, nodeIdx, account.Platform, nil
	}
	return 0, 0, "", leaseErrorf(ErrQuota, "no_capacity",
		"no account has a free node for activation")
}

// orderFallbackSlotCapacityLocked reports whether an account can take one more
// order lease without exceeding its verified slot capacity. The fallback is
// held to exactly the same denominator as the primary lease path, including a
// legacy account whose nodes carry no slot index: otherwise a paid order could
// mint a second lease on the same account after ordinary admission had already
// spent the account's single allowance. accountCapacityLocked caps the
// allowance by the nodes that can actually serve, so an empty account admits
// nothing.
func (s *Store) orderFallbackSlotCapacityLocked(account model.Account, now time.Time) bool {
	capacity := s.accountCapacityLocked(account, now)
	if capacity <= 0 {
		return false
	}
	return s.accountLeaseCountLocked(account.ID) < capacity
}

// validateOrders checks the order state after tenants/users are validated. A
// row whose identifiers cannot be addressed fails the whole load; a row whose
// snapshot, state or grant is malformed is quarantined for review and loses
// any entitlement, so a corrupt document can never grant service. Payment
// events are deduplicated and dropped when their order is unknown.
func (snap *Snapshot) validateOrders() error {
	if len(snap.Orders) > MaxOrders {
		return fmt.Errorf("state snapshot has %d orders (max %d)", len(snap.Orders), MaxOrders)
	}
	orders := make(map[string]struct{}, len(snap.Orders))
	droppedLeases := make(map[string]struct{})
	for i := range snap.Orders {
		record := &snap.Orders[i]
		id := strings.TrimSpace(record.ID)
		if id == "" {
			return errors.New("state snapshot has an order without an id")
		}
		if _, ok := orders[id]; ok {
			return fmt.Errorf("state snapshot has duplicate order %q", id)
		}
		orders[id] = struct{}{}
		record.ID = id
		quarantine := func(reason string) {
			record.ReviewRequired = true
			if record.Grant != nil {
				droppedLeases[record.Grant.LeaseID] = struct{}{}
			}
			record.Grant = nil
			record.Fulfilment = nil
			record.State = model.OrderCancelled
			if validOrderTimestamp(record.CreatedAt) {
				record.CancelledAt = ptrString(record.CreatedAt)
			} else {
				record.CancelledAt = nil
			}
			snap.quarantinedOrders = append(snap.quarantinedOrders, id+": "+reason)
		}
		if reason := orderInvalidReason(*record); reason != "" {
			quarantine(reason)
			continue
		}
		if record.Grant != nil {
			grant := record.Grant
			if grant.OrderID != record.ID || strings.TrimSpace(grant.LeaseID) == "" ||
				grant.QuotaBytes <= 0 || !validOrderTimestamp(grant.ExpiresAt) ||
				(grant.RevokedAt != nil && !validOrderTimestamp(*grant.RevokedAt)) {
				quarantine("malformed grant")
			}
		}
		if record.Fulfilment != nil {
			if terminalOrderState(record.State) {
				// A terminal order never resumes, so a restored job is stale
				// work, not an entitlement. Dropping it must not touch the
				// recorded money or the terminal state.
				record.Fulfilment = nil
			} else if reason := fulfilmentJobInvalidReason(*record, *record.Fulfilment); reason != "" {
				// A job is publication work, not money. Drop only the job and
				// flag review; the order's state, payment facts and grant are
				// preserved so a malformed job can never erase a payment.
				record.Fulfilment = nil
				record.ReviewRequired = true
				snap.quarantinedOrders = append(snap.quarantinedOrders, id+": "+reason)
			} else {
				// A detail written by an older build or a hand edit can still
				// carry a credential. Scrub it on load so the persisted job
				// never keeps a URL, reference or token verbatim; the job's
				// state, schedule and attempts stay intact.
				record.Fulfilment.LastError = sanitizeFulfilmentDetail(record.Fulfilment.LastError)
			}
		}
	}
	if len(droppedLeases) > 0 {
		// A quarantined order must not keep service through its lease: the
		// entitlement is the lease, so the row is dropped with the grant. Node
		// InUse stays fail-closed high until a sweep reconciles it.
		kept := snap.Leases[:0]
		for _, lease := range snap.Leases {
			if _, drop := droppedLeases[lease.ID]; drop {
				continue
			}
			kept = append(kept, lease)
		}
		snap.Leases = kept
	}

	if len(snap.PaymentEvents) > MaxPaymentEvents {
		snap.PaymentEvents = snap.PaymentEvents[:MaxPaymentEvents]
	}
	seen := make(map[string]struct{}, len(snap.PaymentEvents))
	kept := snap.PaymentEvents[:0]
	for _, event := range snap.PaymentEvents {
		event.Provider = strings.TrimSpace(event.Provider)
		event.ID = strings.TrimSpace(event.ID)
		event.OrderID = strings.TrimSpace(event.OrderID)
		if (event.Provider != model.ProviderManual && event.Provider != model.ProviderStripe &&
			event.Provider != model.ProviderEpay) ||
			event.ID == "" || !model.ValidPaymentEventKind(event.Kind) ||
			event.AmountMinor <= 0 || !validOrderTimestamp(event.OccurredAt) {
			continue
		}
		if _, err := normalizePlanCurrency(event.Currency); err != nil {
			continue
		}
		if _, ok := orders[event.OrderID]; !ok {
			continue
		}
		key := event.Provider + "\x00" + event.ID
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		kept = append(kept, event)
	}
	snap.PaymentEvents = kept
	return nil
}

// orderInvalidReason reports the first malformed field of a restored order.
// The returned text is a bounded reason, never a value from the row.
func orderInvalidReason(record orderRecord) string {
	order := record.Order
	if strings.TrimSpace(order.TenantID) == "" || strings.TrimSpace(order.UserID) == "" ||
		strings.TrimSpace(order.PlanID) == "" {
		return "missing owner or plan id"
	}
	if !model.ValidOrderState(order.State) {
		return "unknown state"
	}
	if !validPlanSnapshot(order.Plan) || order.Plan.ID != order.PlanID {
		return "invalid plan snapshot"
	}
	if order.Provider != model.ProviderManual && order.Provider != model.ProviderStripe &&
		order.Provider != model.ProviderEpay {
		return "unknown provider"
	}
	if !validOrderTimestamp(order.CreatedAt) {
		return "invalid createdAt"
	}
	for _, value := range []*string{order.PaidAt, order.ActivatedAt, order.CancelledAt, order.RefundedAt} {
		if value != nil && !validOrderTimestamp(*value) {
			return "invalid timestamp"
		}
	}
	switch order.State {
	case model.OrderPaid:
		if order.PaidAt == nil {
			return "paid order without paidAt"
		}
	case model.OrderActive:
		if order.PaidAt == nil || order.ActivatedAt == nil || order.Grant == nil {
			return "active order without payment, activation or grant"
		}
	case model.OrderCancelled:
		if order.CancelledAt == nil {
			return "cancelled order without cancelledAt"
		}
	case model.OrderRefunded:
		if order.PaidAt == nil || order.RefundedAt == nil {
			return "refunded order without payment or refund timestamp"
		}
	case model.OrderChargedBack:
		if order.PaidAt == nil {
			return "charged-back order without payment"
		}
	}
	return ""
}

func validOrderTimestamp(value string) bool {
	if strings.TrimSpace(value) == "" {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, value)
	return err == nil
}

// fulfilmentJobInvalidReason reports the first malformed field of a restored
// job: the closed vocabularies, attempt bounds, timestamps, the per-state
// schedule/lease/blocked rules, the frozen paid term and the job's
// compatibility with its order. The returned text is a bounded reason, never a
// value from the row. A malformed job is dropped and the order is flagged for
// review; the order state and its payment facts are never rewritten here.
func fulfilmentJobInvalidReason(record orderRecord, job model.FulfilmentJob) string {
	order := record.Order
	if job.OrderID != order.ID {
		return "fulfilment job for another order"
	}
	if !model.ValidFulfilmentMode(job.Mode) {
		return "invalid fulfilment mode"
	}
	if !model.ValidFulfilmentState(job.State) {
		return "invalid fulfilment state"
	}
	if job.Attempts < 0 || job.Attempts > MaxFulfilmentAttempts {
		return "fulfilment attempts out of range"
	}
	if !validOrderTimestamp(job.UpdatedAt) {
		return "invalid fulfilment updatedAt"
	}
	if !validOrderTimestamp(job.EntitlementExpiresAt) {
		return "invalid fulfilment entitlement expiry"
	}
	if job.LastError != "" && (len([]rune(job.LastError)) > maxFulfilmentDetailLen ||
		strings.ContainsAny(job.LastError, "\r\n\t")) {
		return "invalid fulfilment lastError"
	}
	if job.LastReason != "" && !model.ValidFulfilmentReason(job.LastReason) {
		return "unknown fulfilment reason"
	}
	if job.NextAttemptAt != nil && !validOrderTimestamp(*job.NextAttemptAt) {
		return "invalid fulfilment nextAttemptAt"
	}
	switch job.Mode {
	case model.FulfilmentInitial:
		if order.State != model.OrderPaid {
			return "initial fulfilment job on a non-paid order"
		}
		if order.Grant != nil {
			return "initial fulfilment job on a granted order"
		}
	case model.FulfilmentReissue:
		if order.State != model.OrderActive {
			return "reissue fulfilment job on a non-active order"
		}
	}
	wantExpires, ok := orderFulfilmentExpiry(order, job.Mode)
	if !ok {
		return "order has no valid paid term"
	}
	gotExpires, ok := leaseExpiry(job.EntitlementExpiresAt)
	if !ok || !gotExpires.Equal(wantExpires) {
		return "fulfilment entitlement expiry mismatch"
	}
	switch job.State {
	case model.FulfilmentQueued:
		if job.Attempts >= MaxFulfilmentAttempts {
			return "queued fulfilment job exhausted its attempts"
		}
		if job.AttemptLeaseID != "" {
			return "queued fulfilment job holds an attempt lease"
		}
		if job.NextAttemptAt == nil {
			return "queued fulfilment job without a schedule"
		}
		if job.Blocked {
			return "queued fulfilment job is blocked"
		}
	case model.FulfilmentPublishing:
		if job.Attempts < 1 {
			return "publishing fulfilment job without an attempt"
		}
		if job.AttemptLeaseID == "" {
			return "publishing fulfilment job without an attempt lease"
		}
		if job.NextAttemptAt != nil {
			return "publishing fulfilment job with a schedule"
		}
		if job.Blocked {
			return "publishing fulfilment job is blocked"
		}
	case model.FulfilmentDeadLetter:
		// Attempts may be zero: a paid term that ended before the first
		// attempt dead-letters at enqueue time, and that operator item must
		// survive a restart.
		if job.AttemptLeaseID != "" {
			return "dead-lettered fulfilment job holds an attempt lease"
		}
		if job.NextAttemptAt != nil {
			return "dead-lettered fulfilment job with a schedule"
		}
		if !job.Blocked {
			return "dead-lettered fulfilment job is not blocked"
		}
		if job.LastReason == "" {
			return "dead-lettered fulfilment job without a reason"
		}
	}
	return ""
}

// PaymentEvents returns the recorded provider events, newest first.
func (s *Store) PaymentEvents() []model.PaymentEvent {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.PaymentEvent, len(s.paymentEvents))
	copy(out, s.paymentEvents)
	return out
}

// Grants returns the recorded entitlement grants, newest order first. It is
// used by the admin view and the tests; the order embeds its own grant.
func (s *Store) Grants() []model.EntitlementGrant {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.EntitlementGrant, 0, len(s.orders))
	for _, record := range s.orders {
		if record.Grant != nil {
			out = append(out, cloneGrant(*record.Grant))
		}
	}
	return out
}

func (s *Store) orderIndexLocked(id string) int {
	for i, record := range s.orders {
		if record.ID == id {
			return i
		}
	}
	return -1
}

func (s *Store) paymentEventIndexLocked(provider, id string) int {
	for i, event := range s.paymentEvents {
		if event.Provider == provider && event.ID == id {
			return i
		}
	}
	return -1
}

func (s *Store) recordPaymentEventLocked(event model.PaymentEvent, result model.PaymentEventResult) {
	event.Result = result
	s.paymentEvents = prependCapped(s.paymentEvents, event, MaxPaymentEvents)
}

// nextLeaseIDLocked is shared with CreateLease; see leases.go.

func hashRequest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// manualEventID derives a stable provider event id from the order and the
// offline reference, so retrying the same attestation replays the recorded
// result instead of recording a second settlement.
func manualEventID(kind, orderID, reference string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + orderID + "\x00" + reference))
	return "manual-" + hex.EncodeToString(sum[:12])
}

// referenceFingerprint is the only form of a receipt reference an audit row or
// log line may carry.
func referenceFingerprint(reference string) string {
	sum := sha256.Sum256([]byte(reference))
	return hex.EncodeToString(sum[:8])
}

func normalizeOrderReference(raw string) (string, error) {
	reference := strings.TrimSpace(raw)
	switch {
	case reference == "":
		return "", invalidf("reference is required")
	case len(reference) > maxOrderReferenceLen:
		return "", invalidf("reference must be at most %d characters", maxOrderReferenceLen)
	case strings.ContainsAny(reference, "\r\n\t"):
		return "", invalidf("reference must be a single line")
	}
	return reference, nil
}

func normalizeOrderReason(raw string) (string, error) {
	reason := strings.TrimSpace(raw)
	if len(reason) > maxOrderReasonLen {
		return "", invalidf("reason must be at most %d characters", maxOrderReasonLen)
	}
	if strings.ContainsAny(reason, "\r\n\t") {
		return "", invalidf("reason must be a single line")
	}
	return reason, nil
}

func cloneOrder(order model.Order) model.Order {
	if order.Grant != nil {
		grant := cloneGrant(*order.Grant)
		order.Grant = &grant
	}
	if order.Fulfilment != nil {
		job := *order.Fulfilment
		job.NextAttemptAt = copyStringPtr(job.NextAttemptAt)
		order.Fulfilment = &job
	}
	return order
}

func cloneGrant(grant model.EntitlementGrant) model.EntitlementGrant {
	grant.PreviousQuotaBytes = copyInt64(grant.PreviousQuotaBytes)
	grant.RevokedAt = copyStringPtr(grant.RevokedAt)
	return grant
}
