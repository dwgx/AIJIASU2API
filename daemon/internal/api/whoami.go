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
	if identity.rbac && identity.operator != "" {
		writeJSON(w, http.StatusOK, whoamiResponse{Name: identity.operator, Role: string(identity.role), Kind: "operator", StrictRBAC: s.cfg.StrictRBAC})
		return
	}
	if identity.key.ID != "" {
		writeJSON(w, http.StatusOK, whoamiResponse{Name: "key:" + identity.key.UserName, Kind: "scoped-key", StrictRBAC: s.cfg.StrictRBAC})
		return
	}
	if identity.admin {
		// The compatibility shared bearer has full admin authority but no
		// operator entry, so its role is reported explicitly as admin while
		// the kind stays legacy. An optional audit label remains the name.
		name := identity.operator
		if name == "" {
			name = adminOperatorFallback
		}
		writeJSON(w, http.StatusOK, whoamiResponse{Name: name, Role: string(RoleAdmin), Kind: "legacy", StrictRBAC: s.cfg.StrictRBAC})
		return
	}
	writeJSON(w, http.StatusOK, whoamiResponse{Name: adminOperatorFallback, Kind: "legacy", StrictRBAC: s.cfg.StrictRBAC})
}
