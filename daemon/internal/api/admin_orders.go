package api

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/payment"
	"ajsaccountd/internal/store"
)

// stripeWebhookPath is the provider callback. It is the one /v1 route that is
// not bearer-authenticated: the provider signature over the raw body is the
// credential, and a disabled adapter answers 503.
const stripeWebhookPath = "/v1/payments/stripe"

// handleListPlans serves the admin catalogue, including inactive rows.
func (s *Server) handleListPlans(w http.ResponseWriter, r *http.Request) {
	plans := s.store.Plans()
	start, end, ok := paginate(w, r, len(plans))
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, plans[start:end])
}

// handleCreatePlan creates one catalogue entry. The response is the stored row
// with its generated id.
func (s *Server) handleCreatePlan(w http.ResponseWriter, r *http.Request) {
	var in model.PlanInput
	if !decodeJSON(w, r, &in) {
		return
	}
	plan, err := s.store.CreatePlan(in, s.auditActor(r))
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, plan)
}

// handleGetPlan reads one catalogue entry.
func (s *Server) handleGetPlan(w http.ResponseWriter, r *http.Request) {
	plan, ok := s.store.Plan(pathParam(r, "id"))
	if !ok {
		writeError(w, http.StatusNotFound, CodeNotFound, "plan not found")
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

// handlePatchPlan applies a partial catalogue update. Existing orders keep
// their immutable snapshots.
func (s *Server) handlePatchPlan(w http.ResponseWriter, r *http.Request) {
	var patch model.PlanPatch
	if !decodeJSON(w, r, &patch) {
		return
	}
	plan, err := s.store.UpdatePlan(pathParam(r, "id"), patch, s.auditActor(r))
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

// handleDeletePlan soft-deactivates a plan. It never removes the row: orders
// snapshot the price, and an admin still needs the catalogue history.
func (s *Server) handleDeletePlan(w http.ResponseWriter, r *http.Request) {
	if _, err := s.store.DeactivatePlan(pathParam(r, "id"), s.auditActor(r)); err != nil {
		s.writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleListOrders is the admin view. Filters run before pagination.
func (s *Server) handleListOrders(w http.ResponseWriter, r *http.Request) {
	state, ok := orderStateFilter(r.URL.Query().Get("state"))
	if !ok {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument,
			"state must be awaiting_payment, paid, active, cancelled, refunded or charged_back")
		return
	}
	tenantID := strings.TrimSpace(r.URL.Query().Get("tenantId"))
	userID := strings.TrimSpace(r.URL.Query().Get("userId"))
	orders := s.store.Orders()
	filtered := make([]model.Order, 0, len(orders))
	for _, order := range orders {
		if state != "" && order.State != state {
			continue
		}
		if tenantID != "" && order.TenantID != tenantID {
			continue
		}
		if userID != "" && order.UserID != userID {
			continue
		}
		filtered = append(filtered, s.orderServiceView(order))
	}
	start, end, ok := paginate(w, r, len(filtered))
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, filtered[start:end])
}

// handleGetOrder reads one order as admin.
func (s *Server) handleGetOrder(w http.ResponseWriter, r *http.Request) {
	order, ok := s.store.Order(pathParam(r, "id"))
	if !ok {
		writeError(w, http.StatusNotFound, CodeNotFound, "order not found")
		return
	}
	writeJSON(w, http.StatusOK, s.orderServiceView(order))
}

// handleSettleOrder records an admin's offline settlement attestation. It
// requires a receipt reference and an explicit offline-funds confirmation; it
// never claims an online payment occurred. The payment is recorded even when
// activation is blocked, in which case the order stays paid and retryable.
func (s *Server) handleSettleOrder(w http.ResponseWriter, r *http.Request) {
	if s.cfg.ManualPayments == nil {
		writeError(w, http.StatusServiceUnavailable, CodeUpstreamUnavailable,
			"manual settlement is not configured")
		return
	}
	var in model.OrderSettleRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	if !in.OfflineFundsReceived {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument,
			"offlineFundsReceived must be true to attest a received offline payment")
		return
	}
	orderID := pathParam(r, "id")
	before, ok := s.store.Order(orderID)
	if !ok {
		writeError(w, http.StatusNotFound, CodeNotFound, "order not found")
		return
	}
	event, err := s.cfg.ManualPayments.AttestSettlement(r.Context(), orderID, in.Reference)
	if err != nil {
		s.writePaymentError(w, err)
		return
	}
	after, err := s.store.ApplyPaymentEvent(s.cfg.ManualPayments.Name(), event, s.auditActor(r))
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	s.emitOrderTransition(before, after)
	writeJSON(w, http.StatusOK, after)
}

// handleRefundOrder records an already completed full offline refund. It never
// transfers money and never creates a new payment.
func (s *Server) handleRefundOrder(w http.ResponseWriter, r *http.Request) {
	var in model.OrderRefundRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	orderID := pathParam(r, "id")
	before, ok := s.store.Order(orderID)
	if !ok {
		writeError(w, http.StatusNotFound, CodeNotFound, "order not found")
		return
	}
	after, err := s.store.RecordManualRefund(orderID, in.Reference, in.Reason, s.auditActor(r))
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	s.emitOrderTransition(before, after)
	writeJSON(w, http.StatusOK, after)
}

// handleChargebackOrder records external chargeback evidence. It revokes the
// entitlement immediately and flags the order for review.
func (s *Server) handleChargebackOrder(w http.ResponseWriter, r *http.Request) {
	var in model.OrderChargebackRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	orderID := pathParam(r, "id")
	before, ok := s.store.Order(orderID)
	if !ok {
		writeError(w, http.StatusNotFound, CodeNotFound, "order not found")
		return
	}
	after, err := s.store.RecordManualChargeback(orderID, in.Reference, in.Reason, s.auditActor(r))
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	s.emitOrderTransition(before, after)
	writeJSON(w, http.StatusOK, after)
}

// handleAdminCancelOrder cancels an awaiting_payment order on the admin path.
func (s *Server) handleAdminCancelOrder(w http.ResponseWriter, r *http.Request) {
	orderID := pathParam(r, "id")
	before, ok := s.store.Order(orderID)
	if !ok {
		writeError(w, http.StatusNotFound, CodeNotFound, "order not found")
		return
	}
	after, err := s.store.CancelOrder(orderID, s.auditActor(r))
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	s.emitOrderTransition(before, after)
	writeJSON(w, http.StatusOK, after)
}

// stripeWebhookLimitPerMin bounds unauthenticated webhook attempts per client
// address. A valid provider stays far below it; the cap keeps invalid
// signatures from churning the audit ring.
const stripeWebhookLimitPerMin = 120

// handleStripeWebhook verifies a Stripe-compatible callback and applies the
// resulting event. A disabled adapter answers 503 and never falls back to
// manual settlement. An unsigned, stale or malformed callback mutates nothing.
// An allowlisted event for a terminal order is acknowledged and quarantined by
// the store for review.
func (s *Server) handleStripeWebhook(w http.ResponseWriter, r *http.Request) {
	if s.cfg.StripeWebhooks == nil {
		writeError(w, http.StatusServiceUnavailable, CodeUpstreamUnavailable,
			"stripe webhook is not configured")
		return
	}
	if !s.portalLimit(w, "webhook:stripe:"+s.portalClientIP(r), stripeWebhookLimitPerMin,
		"too many webhook requests from this address") {
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "request body could not be read")
		return
	}
	event, err := s.cfg.StripeWebhooks.VerifyWebhook(r.Context(), body, r.Header)
	switch {
	case err == nil:
	case errors.Is(err, payment.ErrDisabled):
		writeError(w, http.StatusServiceUnavailable, CodeUpstreamUnavailable,
			"stripe webhook is not configured")
		return
	case errors.Is(err, payment.ErrUnsupportedEvent):
		s.store.RecordAudit(model.SeverityWarn, "system", "webhook.rejected", "stripe",
			"unsupported event type")
		writeJSON(w, http.StatusOK, webhookAck{Received: true, Ignored: true})
		return
	case errors.Is(err, payment.ErrInvalidSignature):
		s.store.RecordAudit(model.SeverityWarn, "system", "webhook.rejected", "stripe",
			"invalid signature")
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "invalid webhook signature")
		return
	case errors.Is(err, payment.ErrInvalidEvent):
		s.store.RecordAudit(model.SeverityWarn, "system", "webhook.rejected", "stripe",
			"invalid event")
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "invalid webhook event")
		return
	default:
		s.logger.Error("stripe_webhook_failed", "error", err)
		writeError(w, http.StatusServiceUnavailable, CodeUpstreamUnavailable,
			"webhook verification failed")
		return
	}

	before, ok := s.store.Order(event.OrderID)
	if !ok {
		s.store.RecordAudit(model.SeverityWarn, "system", "webhook.rejected", "stripe",
			"unknown order")
		writeJSON(w, http.StatusOK, webhookAck{Received: true, Ignored: true})
		return
	}
	after, err := s.store.ApplyPaymentEvent(s.cfg.StripeWebhooks.Name(), event, "system")
	if err != nil {
		if errors.Is(err, store.ErrConflict) || errors.Is(err, store.ErrInvalid) {
			s.store.RecordAudit(model.SeverityWarn, "system", "webhook.rejected", "stripe",
				"event rejected by the order state machine")
			writeError(w, http.StatusBadRequest, CodeInvalidArgument, "webhook event rejected")
			return
		}
		s.logger.Error("stripe_webhook_apply_failed", "error", err)
		writeError(w, http.StatusInternalServerError, CodeInternal, "webhook could not be recorded")
		return
	}
	s.emitOrderTransition(before, after)
	writeJSON(w, http.StatusOK, webhookAck{Received: true})
}

// webhookAck is the provider acknowledgement. Ignored is true for an event
// that was authenticated but could not apply (unsupported type or unknown
// order); it is acknowledged so the provider stops retrying a permanent case.
type webhookAck struct {
	Received bool `json:"received"`
	Ignored  bool `json:"ignored,omitempty"`
}

// writePaymentError maps a provider failure onto the error envelope. Provider
// messages never contain a reference or payload, and are not echoed.
func (s *Server) writePaymentError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, payment.ErrDisabled):
		writeError(w, http.StatusServiceUnavailable, CodeUpstreamUnavailable,
			"payment provider is disabled")
	case errors.Is(err, payment.ErrInvalidReference):
		writeError(w, http.StatusBadRequest, CodeInvalidArgument,
			"a nonempty receipt reference is required")
	case errors.Is(err, payment.ErrUnsupportedCurrency):
		writeError(w, http.StatusBadRequest, CodeInvalidArgument,
			"epay checkout requires a CNY plan")
	case errors.Is(err, payment.ErrInvalidEvent):
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "invalid payment event")
	default:
		s.logger.Error("payment_provider_error", "error", err)
		writeError(w, http.StatusInternalServerError, CodeInternal, "payment provider failed")
	}
}
