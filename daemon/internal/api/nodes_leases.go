package api

import (
	"net/http"

	"ajsaccountd/internal/model"
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
	for _, node := range nodes {
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

func (s *Server) handleDrainNode(w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	if s.cfg.Panel != nil {
		node, err := s.cfg.Panel.DrainNode(r.Context(), id)
		if err != nil {
			s.writePanelError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, node)
		return
	}
	node, err := s.store.DrainNode(id)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, node)
}

func (s *Server) handleUndrainNode(w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	if s.cfg.Panel != nil {
		node, err := s.cfg.Panel.UndrainNode(r.Context(), id)
		if err != nil {
			s.writePanelError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, node)
		return
	}
	node, err := s.store.UndrainNode(id)
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
	for _, lease := range leases {
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
	lease, err := s.store.RotateLease(id)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, lease)
}

func (s *Server) handleReleaseLease(w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	if s.cfg.Panel != nil {
		if err := s.cfg.Panel.ReleaseLease(r.Context(), id); err != nil {
			s.writePanelError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := s.store.ReleaseLease(id); err != nil {
		s.writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
