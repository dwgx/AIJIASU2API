package api

import "net/http"

type whoamiResponse struct {
	Name       string `json:"name"`
	Role       string `json:"role"`
	StrictRBAC bool   `json:"strictRbac"`
	Kind       string `json:"kind"`
}

// handleWhoami exposes identity metadata, never the bearer or token hash.
// Legacy mode reports the historical proxy label/fallback explicitly rather
// than pretending that label is an RBAC-authenticated identity.
func (s *Server) handleWhoami(w http.ResponseWriter, r *http.Request) {
	identity, ok := authIdentityFrom(r)
	if !ok {
		writeJSON(w, http.StatusOK, whoamiResponse{Name: adminOperatorFallback, Kind: "legacy", StrictRBAC: s.cfg.StrictRBAC})
		return
	}
	if identity.operator != "" {
		writeJSON(w, http.StatusOK, whoamiResponse{Name: identity.operator, Role: string(identity.role), Kind: "operator", StrictRBAC: s.cfg.StrictRBAC})
		return
	}
	if identity.key.ID != "" {
		writeJSON(w, http.StatusOK, whoamiResponse{Name: "key:" + identity.key.UserName, Kind: "scoped-key", StrictRBAC: s.cfg.StrictRBAC})
		return
	}
	writeJSON(w, http.StatusOK, whoamiResponse{Name: adminOperatorFallback, Kind: "legacy", StrictRBAC: s.cfg.StrictRBAC})
}
