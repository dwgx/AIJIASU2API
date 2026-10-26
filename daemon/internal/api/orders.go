package api

import (
	"net/http"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/webhook"
)

// reissueEndedOrderLease commits one replacement lease for an existing order
// and emits the customer-visible lease.reissued event after the store commit.
// The store owns the single-grant/no-second-charge guarantees; this helper only
// joins the event emission to the successful transaction.
func (s *Server) reissueEndedOrderLease(orderID, actor string) (model.Order, error) {
	order, lease, err := s.store.ReissueOrderLease(orderID, actor)
	if err != nil {
		return model.Order{}, err
	}
	data := webhook.LeaseEventData(
		lease.ID, lease.UserID, string(lease.Platform), lease.AccountID, lease.ExpiresAt)
	data["orderId"] = order.ID
	s.emit(webhook.EventLeaseReissued, data)
	return order, nil
}

// registerOrderReissueRoutes wires the explicit re-issue actions. The admin
// route is always present; the portal route follows the tenant portal switch
// like every other /v1/portal route.
func (s *Server) registerOrderReissueRoutes(rt *router) {
	rt.handle(http.MethodPost, "/v1/admin/orders/{id}/reissue", s.handleReissueOrder)
	if s.cfg.TenantPortal {
		rt.handle(http.MethodPost, "/v1/portal/orders/{id}/reissue", s.handlePortalReissueOrder)
	}
}

// handlePortalReissueOrder restores service for one own active order whose
// grant lease has ended (for example after a daemon restart). It creates a
// replacement lease for the same grant, never a new order, grant or payment,
// and returns the updated order. A live lease is refused so a retry cannot
// allocate a second one.
func (s *Server) handlePortalReissueOrder(w http.ResponseWriter, r *http.Request) {
	principal, ok := portalPrincipalFrom(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, CodeUnauthorized, "portal session required")
		return
	}
	if !s.portalLimit(w, "portal:orders:"+principal.Tenant.ID, portalOrderWriteLimitPerMin,
		"too many order writes for this tenant") {
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
	after, err := s.reissueEndedOrderLease(order.ID, "tenant:"+principal.Tenant.ID)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, after)
}

// handleReissueOrder is the admin counterpart of the portal re-issue route.
// It is registered even when the tenant portal is off, like every other order
// administration route, so an operator can restore a paid entitlement after a
// restart without a customer session.
func (s *Server) handleReissueOrder(w http.ResponseWriter, r *http.Request) {
	after, err := s.reissueEndedOrderLease(pathParam(r, "id"), "operator")
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, after)
}
