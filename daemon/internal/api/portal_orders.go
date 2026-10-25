package api

import (
	"net/http"
	"strings"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/webhook"
)

// portalOrderWriteLimitPerMin bounds order create/cancel per tenant. It is a
// separate bucket from key writes, so one busy page cannot exhaust the other.
const portalOrderWriteLimitPerMin = 20

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
			own = append(own, order)
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
	order, replayed, err := s.store.CreateOrder(
		principal.Tenant.ID,
		principal.User.ID,
		in.PlanID,
		r.Header.Get("Idempotency-Key"),
		"tenant:"+principal.Tenant.ID,
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
	writeJSON(w, status, order)
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
	writeJSON(w, http.StatusOK, order)
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
