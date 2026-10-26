package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/store"
)

func (s *Server) handleListNodes(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	accountID := query.Get("account_id")
	if accountID == "" {
		accountID = query.Get("accountId")
	}
	state := query.Get("state")
	region := query.Get("region")

	nodes := s.store.Nodes()
	filtered := make([]model.ProxyNode, 0, len(nodes))
	visible, restricted := s.visibleAccountIDs(r)
	for _, node := range nodes {
		if restricted {
			if _, ok := visible[node.AccountID]; !ok {
				continue
			}
		}
		if accountID != "" && node.AccountID != accountID {
			continue
		}
		if state != "" && string(node.State) != state {
			continue
		}
		if region != "" && node.Region != region {
			continue
		}
		filtered = append(filtered, node)
	}
	start, end, ok := paginate(w, r, len(filtered))
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, filtered[start:end])
}

// runNodeDrainHook hands a drain/undrain to the node controller before the
// store or the panel is touched. A nil hook keeps the store-only behaviour of
// a daemon without AJS_ACCOUNT_LOGIN=1.
func (s *Server) runNodeDrainHook(ctx context.Context, nodeID string, drain bool) error {
	if s.cfg.NodeDrainHook == nil {
		return nil
	}
	return s.cfg.NodeDrainHook(ctx, nodeID, drain)
}

// writeNodeDrainHookError logs a controller hook failure and answers. A
// controller error that is the store's own refusal (draining a DOWN node, a
// node removed between the existence check and the hook) keeps the store
// contract instead of becoming a 500; every other failure answers 500
// INTERNAL and leaves the node as it was.
func (s *Server) writeNodeDrainHookError(w http.ResponseWriter, nodeID string, err error) {
	s.logger.Error("node_drain_hook_error", "node", nodeID, "error", err)
	switch {
	case errors.Is(err, store.ErrInvalid), errors.Is(err, store.ErrNotFound),
		errors.Is(err, store.ErrConflict), errors.Is(err, store.ErrUpstream):
		s.writeStoreError(w, err)
	default:
		writeError(w, http.StatusInternalServerError, CodeInternal, "internal error")
	}
}

func (s *Server) handleDrainNode(w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	if _, ok := s.store.Node(id); !ok {
		// A node the store does not know never reaches the controller: the
		// 404 contract is decided here, before any hook side effect.
		writeError(w, http.StatusNotFound, CodeNotFound, fmt.Sprintf("node %q not found", id))
		return
	}
	if err := s.runNodeDrainHook(r.Context(), id, true); err != nil {
		// The controller must close admission before the store says DRAINING,
		// so a hook failure leaves the node as it was.
		s.writeNodeDrainHookError(w, id, err)
		return
	}
	if s.cfg.Panel != nil {
		node, err := s.cfg.Panel.DrainNode(r.Context(), id)
		if err != nil {
			s.writePanelError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, node)
		return
	}
	node, err := s.store.DrainNode(id, s.auditActor(r))
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, node)
}

func (s *Server) handleUndrainNode(w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	if _, ok := s.store.Node(id); !ok {
		writeError(w, http.StatusNotFound, CodeNotFound, fmt.Sprintf("node %q not found", id))
		return
	}
	if err := s.runNodeDrainHook(r.Context(), id, false); err != nil {
		// Reopening admission failed, so the node stays DRAINING in the store
		// rather than advertising a node whose gate still refuses flows.
		s.writeNodeDrainHookError(w, id, err)
		return
	}
	if s.cfg.Panel != nil {
		node, err := s.cfg.Panel.UndrainNode(r.Context(), id)
		if err != nil {
			s.writePanelError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, node)
		return
	}
	node, err := s.store.UndrainNode(id, s.auditActor(r))
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, node)
}

func (s *Server) handleProbeNode(w http.ResponseWriter, r *http.Request) {
	node, err := s.store.ProbeNode(pathParam(r, "id"))
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, node)
}

// handleListLeases lists active leases. A customer scoped key is bound to its
// own subject: the list is filtered by lease.UserID == key.UserID before the
// account grant, and a query naming another user is refused without revealing
// whether that user holds a lease. Operator callers (admin token or admin-scope
// key) keep the unfiltered list and the optional user_id filter.
func (s *Server) handleListLeases(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		userID = r.URL.Query().Get("userId")
	}
	if key, ok := customerKey(r); ok {
		if userID != "" && userID != key.UserID {
			writeErrorReason(w, http.StatusForbidden, CodeForbidden, ReasonScopedKeyRestricted,
				"scoped key is bound to its own userId; user_id must match it", nil)
			return
		}
		userID = key.UserID
	}
	leases := s.store.Leases()
	filtered := make([]model.Lease, 0, len(leases))
	visible, restricted := s.visibleAccountIDs(r)
	for _, lease := range leases {
		if restricted {
			if _, ok := visible[lease.AccountID]; !ok {
				continue
			}
		}
		if userID != "" && lease.UserID != userID {
			continue
		}
		filtered = append(filtered, lease)
	}
	start, end, ok := paginate(w, r, len(filtered))
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, filtered[start:end])
}

// leaseConnectionHost is the only address a gate binds; the connection block
// of a lease detail never advertises a public endpoint.
const leaseConnectionHost = "127.0.0.1"

// handleGetLease serves GET /v1/leases/{id}. It applies the same ownership
// rule as a mutation (P0-01): a customer scoped key reads only its own subject,
// an unknown id and another user's lease answer the same 404, and a lease on an
// account outside the key's grant answers 403. The connection block is derived
// on every read from the stored row, which a switch commit rebinds, so the port
// is the current gate rather than a cached one. The scoped-key secret is never
// part of the response: only its kind is.
func (s *Server) handleGetLease(w http.ResponseWriter, r *http.Request) {
	lease, found, denied := s.authorizeLease(w, r, pathParam(r, "id"))
	if denied {
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, CodeNotFound, "lease not found")
		return
	}
	writeJSON(w, http.StatusOK, model.LeaseDetail{
		Lease: lease,
		Connection: model.LeaseConnection{
			Host:           leaseConnectionHost,
			Port:           lease.GatePort,
			Protocol:       lease.Protocol,
			Username:       lease.UserID,
			CredentialKind: model.LeaseCredentialKindScopedKey,
		},
	})
}

// authorizeLease resolves the lease a read, revoke or rotate addresses and
// binds it to the caller before any side effect. Operator callers (admin token
// or admin-scope key) keep the stored row and their existing contract; found is
// false for an unknown id so the caller keeps its own 404. A customer scoped key
// may only touch its own subject: an unknown id and another user's lease answer
// the same 404, so the id cannot be probed, and a lease on an account outside
// the key's grant answers 403 before the store or the panel is touched. It
// reports whether the response was written.
func (s *Server) authorizeLease(w http.ResponseWriter, r *http.Request, id string) (lease model.Lease, found, denied bool) {
	lease, found = s.store.Lease(id)
	key, customer := customerKey(r)
	if !customer {
		return lease, found, false
	}
	if !found || lease.UserID != key.UserID {
		writeError(w, http.StatusNotFound, CodeNotFound, "lease not found")
		return model.Lease{}, false, true
	}
	if !s.scopedKeyAllowsAccount(r, lease.AccountID) {
		writeErrorReason(w, http.StatusForbidden, CodeForbidden, ReasonScopedKeyRestricted,
			"scoped key is restricted to named accounts; the lease belongs to another account", nil)
		return model.Lease{}, false, true
	}
	return lease, true, false
}

func (s *Server) handleRotateLease(w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	_, found, denied := s.authorizeLease(w, r, id)
	if denied {
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, CodeNotFound, "lease not found")
		return
	}
	// A rotation is a Resin action. Without a panel there is no upstream to
	// move the lease, so refuse before the store is touched instead of
	// rewriting the recorded egress IP and reporting a rotation that did not
	// happen.
	if s.cfg.Panel == nil {
		writeError(w, http.StatusServiceUnavailable, CodeUpstreamUnavailable,
			"lease rotation requires a configured Resin panel")
		return
	}
	lease, err := s.cfg.Panel.RotateLease(r.Context(), id)
	if err != nil {
		s.writePanelError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, lease)
}
