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

func (s *Server) handleListLeases(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		userID = r.URL.Query().Get("userId")
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

func (s *Server) handleRotateLease(w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	if s.cfg.Panel != nil {
		lease, err := s.cfg.Panel.RotateLease(r.Context(), id)
		if err != nil {
			s.writePanelError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, lease)
		return
	}
	lease, err := s.store.RotateLease(id, s.auditActor(r))
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, lease)
}
