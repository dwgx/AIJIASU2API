package api

import (
	"net/http"
	"strings"

	"ajsaccountd/internal/model"
)

// handleListTenants is the admin view. It filters by the closed status
// vocabulary and paginates after filtering.
func (s *Server) handleListTenants(w http.ResponseWriter, r *http.Request) {
	status := model.TenantStatus(strings.TrimSpace(r.URL.Query().Get("status")))
	if status != "" && !model.ValidTenantStatus(status) {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument,
			"status must be pending, active or suspended")
		return
	}
	tenants := s.store.Tenants()
	filtered := make([]model.Tenant, 0, len(tenants))
	for _, tenant := range tenants {
		if status != "" && tenant.Status != status {
			continue
		}
		filtered = append(filtered, tenant)
	}
	start, end, ok := paginate(w, r, len(filtered))
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, filtered[start:end])
}

// handleApproveTenant binds exactly one existing user or creates one identity
// and binds it, activating the tenant atomically.
func (s *Server) handleApproveTenant(w http.ResponseWriter, r *http.Request) {
	var in model.TenantApproveRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	tenant, err := s.store.ApproveTenant(pathParam(r, "id"), in.UserID, in.User)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tenant)
}

// handleSuspendTenant disables the linked user and revokes the tenant's
// sessions and keys in one commit.
func (s *Server) handleSuspendTenant(w http.ResponseWriter, r *http.Request) {
	tenant, err := s.store.SuspendTenant(pathParam(r, "id"))
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tenant)
}

// handleResumeTenant re-enables the linked user. Keys revoked by the
// suspension stay revoked.
func (s *Server) handleResumeTenant(w http.ResponseWriter, r *http.Request) {
	tenant, err := s.store.ResumeTenant(pathParam(r, "id"))
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tenant)
}
