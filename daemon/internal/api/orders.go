package api

import (
	"fmt"
	"net/http"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/store"
	"ajsaccountd/internal/webhook"
)

// reissueEndedOrderLease publishes one replacement before answering success,
// using the same fail-closed boundary as ordinary lease creation. On failure
// it restores the grant's ended lease reference so a retry remains safe, and
// emits neither lease.reissued nor an active-order response for service that
// was not published. The store owns the single-grant/no-second-charge rules.
func (s *Server) reissueEndedOrderLease(r *http.Request, orderID, actor string) (model.Order, model.Lease, error) {
	before, ok := s.store.Order(orderID)
	if !ok {
		return model.Order{}, model.Lease{}, &store.Error{
			Kind: store.ErrNotFound, Message: fmt.Sprintf("order %q not found", orderID),
		}
	}
	previousLeaseID := ""
	if before.Grant != nil {
		previousLeaseID = before.Grant.LeaseID
	}
	order, lease, err := s.store.ReissueOrderLease(orderID, actor)
	if err != nil {
		return model.Order{}, model.Lease{}, err
	}
	if err := s.publishLease(r.Context(), lease); err != nil {
		if rollbackErr := s.store.RollbackOrderReissue(orderID, lease.ID, previousLeaseID, err.Error()); rollbackErr != nil {
			s.logger.Error("order_reissue_rollback_failed", "order", orderID, "lease", lease.ID, "error", rollbackErr)
			return model.Order{}, model.Lease{}, rollbackErr
		}
		s.rollbackUnpublishedLease(lease, err)
		return model.Order{}, lease, err
	}
	data := webhook.LeaseEventData(
		lease.ID, lease.UserID, string(lease.Platform), lease.AccountID, lease.ExpiresAt)
	data["orderId"] = order.ID
	s.emit(webhook.EventLeaseReissued, data)
	return order, model.Lease{}, nil
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
	after, failedLease, err := s.reissueEndedOrderLease(r, order.ID, "tenant:"+principal.Tenant.ID)
	if err != nil {
		s.writeReissueError(w, failedLease, err)
		return
	}
	writeJSON(w, http.StatusOK, after)
}

// handleReissueOrder is the admin counterpart of the portal re-issue route.
// It is registered even when the tenant portal is off, like every other order
// administration route, so an operator can restore a paid entitlement after a
// restart without a customer session.
func (s *Server) handleReissueOrder(w http.ResponseWriter, r *http.Request) {
	after, failedLease, err := s.reissueEndedOrderLease(r, pathParam(r, "id"), "operator")
	if err != nil {
		s.writeReissueError(w, failedLease, err)
		return
	}
	writeJSON(w, http.StatusOK, after)
}

// writeReissueError keeps ordinary store refusals unchanged and gives a
// publication failure the same typed response as POST /v1/leases. The returned
// lease is nonempty only after the failed replacement was rolled back.
func (s *Server) writeReissueError(w http.ResponseWriter, failedLease model.Lease, err error) {
	if failedLease.ID != "" {
		s.writeLeasePublishError(w, failedLease, err)
		return
	}
	s.writeStoreError(w, err)
}
