package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/payment"
	"ajsaccountd/internal/store"
	"ajsaccountd/internal/webhook"
)

// portalOrderWriteLimitPerMin bounds order create/cancel per tenant. It is a
// separate bucket from key writes, so one busy page cannot exhaust the other.
const portalOrderWriteLimitPerMin = 20

// orderServiceView decorates one order with the durable outcome of its grant
// lease. The order state stays truthful (`active` means the paid term is
// unexpired); when the lease that backs the grant is gone, the customer still
// needs to know why service stopped and whether the re-issue action applies.
// The projection is computed on read and never persisted on the order.
func (s *Server) orderServiceView(order model.Order) model.Order {
	if order.State != model.OrderActive || order.Grant == nil || order.Grant.RevokedAt != nil {
		return order
	}
	if lease, ok := s.store.Lease(order.Grant.LeaseID); ok &&
		lease.UserID == order.UserID && lease.ExpiresAt == order.Grant.ExpiresAt {
		return order
	}
	reason, endedAt := "lease_ended", ""
	if end, ok := s.store.LeaseEnd(order.Grant.LeaseID); ok {
		reason, endedAt = end.Reason, end.EndedAt
	}
	order.Grant.LeaseEndedReason = reason
	order.Grant.LeaseEndedAt = endedAt
	return order
}

// handlePortalPlans lists the active catalogue only. An inactive plan is admin
// configuration and must not be offered to a customer.
func (s *Server) handlePortalPlans(w http.ResponseWriter, r *http.Request) {
	if _, ok := portalPrincipalFrom(r); !ok {
		writeError(w, http.StatusUnauthorized, CodeUnauthorized, "portal session required")
		return
	}
	plans := s.store.Plans()
	active := make([]model.Plan, 0, len(plans))
	for _, plan := range plans {
		if plan.Active {
			active = append(active, plan)
		}
	}
	start, end, ok := paginate(w, r, len(active))
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, active[start:end])
}

// handlePortalListOrders lists only the linked user's orders. The ownership
// filter runs before pagination, so a page boundary cannot expose a foreign
// row.
func (s *Server) handlePortalListOrders(w http.ResponseWriter, r *http.Request) {
	principal, ok := portalPrincipalFrom(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, CodeUnauthorized, "portal session required")
		return
	}
	orders := s.store.OrdersForTenant(principal.Tenant.ID)
	own := make([]model.Order, 0, len(orders))
	for _, order := range orders {
		if order.UserID == principal.User.ID {
			own = append(own, s.orderServiceView(order))
		}
	}
	start, end, ok := paginate(w, r, len(own))
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, own[start:end])
}

// handlePortalCreateOrder creates one awaiting_payment order from an active
// plan. Creating an order never claims payment: the response state is the
// truthful server state. An Idempotency-Key replay answers 200 with the
// original order; a new order answers 201.
//
// Before creating anything, an active order for the same plan whose grant
// lease has ended is re-issued instead. That is the customer path back to
// service after a daemon restart: it returns the existing order with a
// replacement lease, so the customer is not charged a second time for an
// entitlement they already own. A different plan still creates a new order.
func (s *Server) handlePortalCreateOrder(w http.ResponseWriter, r *http.Request) {
	principal, ok := portalPrincipalFrom(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, CodeUnauthorized, "portal session required")
		return
	}
	if !s.portalLimit(w, "portal:orders:"+principal.Tenant.ID, portalOrderWriteLimitPerMin,
		"too many order writes for this tenant") {
		return
	}
	var in model.OrderCreateRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	actor := "tenant:" + principal.Tenant.ID
	if candidate, ok := s.store.ReissueCandidate(principal.Tenant.ID, principal.User.ID, in.PlanID); ok {
		after, failedLease, err := s.reissueEndedOrderLease(r, candidate.ID, actor)
		if err != nil {
			s.writeReissueError(w, failedLease, err)
			return
		}
		writeJSON(w, http.StatusOK, s.orderServiceView(after))
		return
	}
	order, replayed, err := s.store.CreateOrder(
		principal.Tenant.ID,
		principal.User.ID,
		in.PlanID,
		r.Header.Get("Idempotency-Key"),
		actor,
	)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if !replayed {
		s.emitOrderEvent(webhook.EventOrderCreated, order)
	}
	status := http.StatusCreated
	if replayed {
		status = http.StatusOK
	}
	writeJSON(w, status, s.orderServiceView(order))
}

// handlePortalGetOrder reads one own order. A foreign or unknown id answers the
// same 404.
func (s *Server) handlePortalGetOrder(w http.ResponseWriter, r *http.Request) {
	principal, ok := portalPrincipalFrom(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, CodeUnauthorized, "portal session required")
		return
	}
	order, err := s.store.TenantOrder(principal.Tenant.ID, pathParam(r, "id"))
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if order.UserID != principal.User.ID {
		writeError(w, http.StatusNotFound, CodeNotFound, "order not found")
		return
	}
	writeJSON(w, http.StatusOK, s.orderServiceView(order))
}

// handlePortalCancelOrder cancels an awaiting_payment order. A cancelled order
// is terminal; every other state answers 409.
func (s *Server) handlePortalCancelOrder(w http.ResponseWriter, r *http.Request) {
	principal, ok := portalPrincipalFrom(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, CodeUnauthorized, "portal session required")
		return
	}
	if !s.portalLimit(w, "portal:orders:"+principal.Tenant.ID, portalOrderWriteLimitPerMin,
		"too many order writes for this tenant") {
		return
	}
	orderID := pathParam(r, "id")
	before, err := s.store.TenantOrder(principal.Tenant.ID, orderID)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	order, err := s.store.CancelTenantOrder(principal.Tenant.ID, orderID, "tenant:"+principal.Tenant.ID)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if before.State != order.State {
		s.emitOrderEvent(webhook.EventOrderCancelled, order)
	}
	writeJSON(w, http.StatusOK, order)
}

// epayNotifyPath is the 易支付 server callback. It is the second /v1 route that
// is not bearer-authenticated: the MD5 signature over the form body is the
// credential, and a disabled adapter answers 503.
const epayNotifyPath = "/v1/payments/epay/notify"

// epayNotifyLimitPerMin bounds unauthenticated callback attempts per client
// address, matching the Stripe budget.
const epayNotifyLimitPerMin = 120

// epayNotifyFulfilTimeout bounds the immediate publication attempt a verified
// callback makes. It is deliberately independent of the request context: the
// gateway's connection may drop at any moment, and a publication that already
// reserved capacity must be allowed to finish or abort on its own deadline
// instead of being cancelled mid-flight.
const epayNotifyFulfilTimeout = 10 * time.Second

// orderCheckoutRequest is the POST /v1/portal/orders/{id}/checkout body. Type
// selects the gateway channel; an omitted type defaults to alipay.
type orderCheckoutRequest struct {
	Type string `json:"type"`
}

// orderCheckoutResponse is a hosted payment URL. It is never a payment fact:
// the order remains awaiting_payment until a verified notify or reconciliation
// settles it.
type orderCheckoutResponse struct {
	PayURL string `json:"payUrl"`
}

// handlePortalCheckoutOrder returns the 易支付 hosted checkout URL for one own
// awaiting_payment order. Building the URL changes no payment state; the order
// records only the chosen provider so the reconciler can find it later.
func (s *Server) handlePortalCheckoutOrder(w http.ResponseWriter, r *http.Request) {
	principal, ok := portalPrincipalFrom(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, CodeUnauthorized, "portal session required")
		return
	}
	if !s.portalLimit(w, "portal:orders:"+principal.Tenant.ID, portalOrderWriteLimitPerMin,
		"too many order writes for this tenant") {
		return
	}
	var in orderCheckoutRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	kind := strings.ToLower(strings.TrimSpace(in.Type))
	if kind == "" {
		kind = payment.EpayTypeAlipay
	}
	if kind != payment.EpayTypeAlipay && kind != payment.EpayTypeWxpay {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument,
			"type must be alipay or wxpay")
		return
	}
	order, err := s.store.TenantOrder(principal.Tenant.ID, pathParam(r, "id"))
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if order.UserID != principal.User.ID {
		writeError(w, http.StatusNotFound, CodeNotFound, "order not found")
		return
	}
	if order.State != model.OrderAwaitingPayment {
		writeError(w, http.StatusConflict, CodeConflict,
			"only awaiting_payment orders can be checked out")
		return
	}
	if s.cfg.Epay == nil || !s.cfg.Epay.Enabled() {
		writeError(w, http.StatusServiceUnavailable, CodeUpstreamUnavailable,
			"epay checkout is not configured")
		return
	}
	// The callback URLs are optional. With a tenant origin (or explicit
	// overrides) checkout advertises the public callback path; without either
	// it signs a poll-only checkout and the reconciler settles the order.
	base := strings.TrimRight(strings.TrimSpace(s.cfg.TenantOrigin), "/")
	notifyURL := strings.TrimSpace(s.cfg.Epay.NotifyURL())
	returnURL := strings.TrimSpace(s.cfg.Epay.ReturnURL())
	if notifyURL == "" && base != "" {
		notifyURL = base + epayNotifyPath
	}
	if returnURL == "" && base != "" {
		returnURL = base + "/portal"
	}
	payURL, err := s.cfg.Epay.Checkout(order, kind, notifyURL, returnURL)
	if err != nil {
		s.writePaymentError(w, err)
		return
	}
	if _, err := s.store.MarkOrderCheckout(principal.Tenant.ID, order.ID, model.ProviderEpay); err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, orderCheckoutResponse{PayURL: payURL})
}

// handleEpayNotify verifies one 易支付 form callback and applies it through the
// same store settlement path as an admin attestation. The response is the
// gateway's plain-text protocol: "success" only when the order is actually
// serving, "fail" for every rejected callback and for a payment that is
// recorded but not yet fulfilled. A malformed, badly signed or unknown
// callback changes nothing.
func (s *Server) handleEpayNotify(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Epay == nil || !s.cfg.Epay.Enabled() {
		writeEpayAck(w, http.StatusServiceUnavailable, "fail")
		return
	}
	if ok, _, _ := s.tenantLimiter.Allow("webhook:epay:"+s.portalClientIP(r), epayNotifyLimitPerMin); !ok {
		writeEpayAck(w, http.StatusTooManyRequests, "fail")
		return
	}
	// Standard 易支付 notify is a GET with query parameters; some
	// deployments POST a form body. ParseForm merges the query string and an
	// urlencoded body, so one verification path serves both transports.
	if err := r.ParseForm(); err != nil {
		if !s.rejectEpayNotify(w, "malformed form body") {
			return
		}
		writeEpayAck(w, http.StatusOK, "fail")
		return
	}
	form := r.Form
	orderID := strings.TrimSpace(form.Get("out_trade_no"))
	order, ok := s.store.Order(orderID)
	if !ok {
		if !s.rejectEpayNotify(w, "unknown order") {
			return
		}
		writeEpayAck(w, http.StatusOK, "fail")
		return
	}
	event, err := s.cfg.Epay.VerifyNotify(r.Context(), form, order)
	if err != nil {
		reason := "invalid notification"
		switch {
		case errors.Is(err, payment.ErrInvalidSignature):
			reason = "invalid signature"
		case errors.Is(err, payment.ErrUnsupportedEvent):
			reason = "unsupported trade status"
		case errors.Is(err, payment.ErrProviderMismatch):
			reason = "order provider mismatch"
		case errors.Is(err, payment.ErrUnsupportedCurrency):
			reason = "unsupported currency"
		case errors.Is(err, payment.ErrInvalidEvent):
			reason = "invalid event"
		case errors.Is(err, payment.ErrDisabled):
			writeEpayAck(w, http.StatusServiceUnavailable, "fail")
			return
		}
		if !s.rejectEpayNotify(w, reason) {
			return
		}
		writeEpayAck(w, http.StatusOK, "fail")
		return
	}
	before := order
	after, err := s.store.ApplyPaymentEvent(s.cfg.Epay.Name(), event, "system")
	if err != nil {
		if errors.Is(err, store.ErrConflict) || errors.Is(err, store.ErrInvalid) ||
			errors.Is(err, store.ErrNotFound) {
			if !s.rejectEpayNotify(w, "event rejected by the order state machine") {
				return
			}
			writeEpayAck(w, http.StatusOK, "fail")
			return
		}
		s.logger.Error("epay_notify_apply_failed", "error", err)
		writeEpayAck(w, http.StatusInternalServerError, "fail")
		return
	}
	s.emitOrderTransition(before, after)
	// The gateway's plain-text ack is a service claim, not a payment claim.
	// Only an order that is actually serving may answer success. A paid order
	// means the money is durably recorded but the entitlement is not published
	// yet, so the callback attaches the durable initial job (idempotent per
	// order) and attempts it immediately. Success is answered only when that
	// attempt (or a concurrent one) left the order active with a serving
	// grant; every other outcome answers fail, so the gateway keeps retrying
	// and no callback ever claims service that does not exist.
	current, serving := s.store.OrderActiveWithGrant(order.ID)
	if serving {
		writeEpayAck(w, http.StatusOK, "success")
		return
	}
	if current.State == model.OrderPaid && !current.ReviewRequired {
		if _, err := s.store.EnqueueFulfilment(current.ID, model.FulfilmentInitial); err != nil &&
			!errors.Is(err, store.ErrConflict) {
			// The payment is already durable; a failed enqueue must not turn
			// the acknowledgement into a payment failure or a success.
			s.logger.Warn("epay_notify_fulfilment_enqueue_failed", "order_id", current.ID, "error", err)
		}
		// The queue alone cannot answer this callback: try the job now, with a
		// deadline of its own, and let the reloaded order decide the ack. A
		// failed attempt leaves the job queued for the worker and its backoff.
		if s.cfg.Fulfiller != nil {
			fulfilCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()),
				epayNotifyFulfilTimeout)
			_, fulfilErr := s.cfg.Fulfiller.FulfilNow(fulfilCtx, current.ID)
			cancel()
			if fulfilErr != nil {
				s.logger.Warn("epay_notify_fulfilment_failed", "order_id", current.ID, "error", fulfilErr)
			}
			if _, serving := s.store.OrderActiveWithGrant(current.ID); serving {
				writeEpayAck(w, http.StatusOK, "success")
				return
			}
		}
	}
	writeEpayAck(w, http.StatusOK, "fail")
}

// writeEpayAck writes the gateway's plain-text acknowledgement. Cache-Control
// keeps an intermediary from replaying a stale success.
func writeEpayAck(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// emitOrderEvent publishes one committed order transition to the customer
// event stream. The payload carries ids, state and the priced snapshot; it
// never carries a receipt reference or any credential.
func (s *Server) emitOrderEvent(event webhook.EventType, order model.Order) {
	if s.cfg.Events == nil {
		return
	}
	data := map[string]any{
		"orderId":     order.ID,
		"tenantId":    order.TenantID,
		"userId":      order.UserID,
		"planId":      order.PlanID,
		"state":       string(order.State),
		"amountMinor": order.Plan.AmountMinor,
		"currency":    order.Plan.Currency,
		"provider":    order.Provider,
	}
	if order.Grant != nil {
		data["leaseId"] = order.Grant.LeaseID
		data["expiresAt"] = order.Grant.ExpiresAt
	}
	s.cfg.Events.Emit(event, data)
}

// emitOrderTransition publishes the events implied by a state change. A paid
// order that later activates emits only order.activated: order.paid was
// already published when payment was recorded, and a retry must not duplicate
// it.
func (s *Server) emitOrderTransition(before model.Order, after model.Order) {
	if before.State == after.State {
		return
	}
	switch after.State {
	case model.OrderPaid:
		s.emitOrderEvent(webhook.EventOrderPaid, after)
	case model.OrderActive:
		if before.State == model.OrderAwaitingPayment {
			s.emitOrderEvent(webhook.EventOrderPaid, after)
		}
		s.emitOrderEvent(webhook.EventOrderActivated, after)
	case model.OrderCancelled:
		s.emitOrderEvent(webhook.EventOrderCancelled, after)
	case model.OrderRefunded:
		s.emitOrderEvent(webhook.EventOrderRefunded, after)
	case model.OrderChargedBack:
		s.emitOrderEvent(webhook.EventOrderChargedBack, after)
	}
}

// orderStateFilter validates the admin ?state= filter.
func orderStateFilter(raw string) (model.OrderState, bool) {
	state := model.OrderState(strings.TrimSpace(raw))
	if state == "" {
		return "", true
	}
	return state, model.ValidOrderState(state)
}

// rejectEpayNotify records one rejected notification and reports whether the
// row is durable. A poisoned journal answers 503 with "fail" so the gateway
// retries instead of treating an unrecorded rejection as delivered.
func (s *Server) rejectEpayNotify(w http.ResponseWriter, detail string) bool {
	if err := s.store.RecordAudit(model.SeverityWarn, "system", "webhook.rejected", "epay", detail); err != nil {
		s.logger.Error("webhook_audit_persist_failed", "provider", "epay", "error", err)
		writeEpayAck(w, http.StatusServiceUnavailable, "fail")
		return false
	}
	return true
}
