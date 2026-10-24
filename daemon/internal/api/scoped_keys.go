package api

import (
	"context"
	"net/http"
	"strings"

	"ajsaccountd/internal/model"
)

// routeScopes maps a registered route pattern to the scope a scoped key needs.
// Routes absent from the table are admin-only: a scoped key without the admin
// scope cannot reach them. Keeping the table beside the handlers makes the
// least-privilege contract auditable in one place.
var routeScopes = map[string]model.Scope{
	http.MethodGet + " /metrics":    model.ScopeMetricsRead,
	http.MethodGet + " /v1/metrics": model.ScopeMetricsRead,

	http.MethodGet + " /v1/accounts":                          model.ScopeAccountsRead,
	http.MethodPost + " /v1/accounts":                         model.ScopeAccountsWrite,
	http.MethodGet + " /v1/accounts/io/capabilities":          model.ScopeAccountsRead,
	http.MethodPost + " /v1/accounts/export":                  model.ScopeAccountsRead,
	http.MethodPost + " /v1/accounts/import":                  model.ScopeAccountsWrite,
	http.MethodPost + " /v1/accounts/bulk-delete":             model.ScopeAccountsWrite,
	http.MethodGet + " /v1/accounts/{id}":                     model.ScopeAccountsRead,
	http.MethodPatch + " /v1/accounts/{id}":                   model.ScopeAccountsWrite,
	http.MethodDelete + " /v1/accounts/{id}":                  model.ScopeAccountsWrite,
	http.MethodPost + " /v1/accounts/{id}/actions/login":      model.ScopeAccountsWrite,
	http.MethodPost + " /v1/accounts/{id}/actions/logout":     model.ScopeAccountsWrite,
	http.MethodGet + " /v1/accounts/{id}/servers":             model.ScopeCatalogRead,
	http.MethodGet + " /v1/accounts/{id}/device":              model.ScopeAccountsRead,
	http.MethodGet + " /v1/accounts/{id}/selection":           model.ScopeCatalogRead,
	http.MethodPut + " /v1/accounts/{id}/selection":           model.ScopeCatalogWrite,
	http.MethodDelete + " /v1/accounts/{id}/selection":        model.ScopeCatalogWrite,
	http.MethodPost + " /v1/accounts/{id}/actions/connect":    model.ScopeCatalogWrite,
	http.MethodPost + " /v1/accounts/{id}/actions/disconnect": model.ScopeCatalogWrite,
	http.MethodPost + " /v1/accounts/{id}/actions/reconnect":  model.ScopeCatalogWrite,

	http.MethodGet + " /v1/schedules":                   model.ScopeSettingsRead,
	http.MethodPost + " /v1/schedules":                  model.ScopeSettingsWrite,
	http.MethodGet + " /v1/schedules/preview":           model.ScopeSettingsRead,
	http.MethodPost + " /v1/schedules/preview":          model.ScopeSettingsRead,
	http.MethodGet + " /v1/schedules/{id}":              model.ScopeSettingsRead,
	http.MethodPatch + " /v1/schedules/{id}":            model.ScopeSettingsWrite,
	http.MethodDelete + " /v1/schedules/{id}":           model.ScopeSettingsWrite,
	http.MethodPost + " /v1/schedules/{id}/actions/run": model.ScopeSettingsWrite,
	http.MethodGet + " /v1/schedules/{id}/runs":         model.ScopeSettingsRead,

	http.MethodGet + " /v1/lab/overview":      model.ScopeLabRead,
	http.MethodGet + " /v1/lab/flags":         model.ScopeLabRead,
	http.MethodGet + " /v1/lab/gates":         model.ScopeLabRead,
	http.MethodGet + " /v1/lab/flows":         model.ScopeLabRead,
	http.MethodGet + " /v1/lab/sessions":      model.ScopeLabRead,
	http.MethodGet + " /v1/lab/egress":        model.ScopeLabRead,
	http.MethodPost + " /v1/lab/egress-check": model.ScopeLabRead,
	http.MethodGet + " /v1/lab/profiles":      model.ScopeLabRead,
	http.MethodGet + " /v1/lab/logs":          model.ScopeLabRead,
	http.MethodGet + " /v1/lab/diagnostics":   model.ScopeLabRead,

	http.MethodGet + " /v1/nodes":                       model.ScopeCatalogRead,
	http.MethodPost + " /v1/nodes/{id}/actions/drain":   model.ScopeCatalogWrite,
	http.MethodPost + " /v1/nodes/{id}/actions/undrain": model.ScopeCatalogWrite,
	http.MethodPost + " /v1/nodes/{id}/actions/probe":   model.ScopeCatalogWrite,

	http.MethodGet + " /v1/leases":                      model.ScopeLeasesRead,
	http.MethodPost + " /v1/leases/{id}/actions/rotate": model.ScopeLeasesWrite,
	http.MethodDelete + " /v1/leases/{id}":              model.ScopeLeasesWrite,

	http.MethodGet + " /v1/requests":         model.ScopeRequestsRead,
	http.MethodGet + " /v1/audit":            model.ScopeAuditRead,
	http.MethodGet + " /v1/audit/export.csv": model.ScopeAuditRead,

	http.MethodGet + " /v1/settings":   model.ScopeSettingsRead,
	http.MethodPatch + " /v1/settings": model.ScopeSettingsWrite,
	http.MethodGet + " /v1/topology":   model.ScopeCatalogRead,
}

// authIdentity is the authenticated caller stored in the request context by
// the auth middleware. The zero value is never installed; an absent identity
// means the request is unauthenticated (healthz) or insecure mode is on, and
// the router then skips scope checks.
type authIdentity struct {
	admin bool
	key   model.ScopedKey
}

type authIdentityKey struct{}

func withAuthIdentity(r *http.Request, identity authIdentity) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), authIdentityKey{}, identity))
}

func authIdentityFrom(r *http.Request) (authIdentity, bool) {
	identity, ok := r.Context().Value(authIdentityKey{}).(authIdentity)
	return identity, ok
}

func scopedKeyFromRequest(r *http.Request) (model.ScopedKey, bool) {
	identity, ok := authIdentityFrom(r)
	if !ok || identity.admin {
		return model.ScopedKey{}, false
	}
	return identity.key, true
}

// allowsRoute reports whether the identity may call a matched route. Admin
// tokens and keys carrying the admin scope bypass the table; every other
// scoped key needs the exact scope the route declares.
func (id authIdentity) allowsRoute(method, pattern string) bool {
	if id.admin || id.hasScope(model.ScopeAdmin) {
		return true
	}
	scope, ok := routeScopes[method+" "+pattern]
	if !ok {
		return false
	}
	return id.hasScope(scope)
}

func (id authIdentity) hasScope(scope model.Scope) bool {
	for _, candidate := range id.key.Scopes {
		if candidate == scope {
			return true
		}
	}
	return false
}

// scopedKeyDeniesRequest enforces accountIds/regions on account-scoped reads.
// Writes are governed by scopes; the restriction narrows what a key may see.
func (s *Server) scopedKeyDeniesRequest(key model.ScopedKey, r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	if len(key.AccountIDs) == 0 && len(key.Regions) == 0 {
		return false
	}
	for _, name := range []string{"account_id", "accountId"} {
		if value := strings.TrimSpace(r.URL.Query().Get(name)); value != "" {
			if !s.keyAllowsAccountID(key, value) {
				return true
			}
		}
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 || parts[0] != "v1" || parts[1] != "accounts" {
		return false
	}
	switch parts[2] {
	case "io", "export", "import", "bulk-delete":
		return false
	}
	account, ok := s.store.Account(parts[2])
	if !ok {
		// The handler answers 404; an unknown id reveals nothing either way.
		return false
	}
	return !keyAllowsAccount(key, account)
}

func (s *Server) keyAllowsAccountID(key model.ScopedKey, id string) bool {
	if len(key.AccountIDs) == 0 && len(key.Regions) == 0 {
		return true
	}
	account, ok := s.store.Account(id)
	if !ok {
		return len(key.AccountIDs) == 0 || containsString(key.AccountIDs, id)
	}
	return keyAllowsAccount(key, account)
}

// keyAllowsAccount reports whether the key may read account. An account
// matches when the id list is empty or contains it, and the region list is
// empty or overlaps the account's regions.
func keyAllowsAccount(key model.ScopedKey, account model.Account) bool {
	if len(key.AccountIDs) > 0 && !containsString(key.AccountIDs, account.ID) {
		return false
	}
	if len(key.Regions) == 0 {
		return true
	}
	for _, region := range account.Regions {
		if containsString(key.Regions, region) {
			return true
		}
	}
	return false
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// visibleAccountIDs returns the account ids a restricted key may see. The bool
// is false when the key is unrestricted, so callers skip filtering entirely.
func (s *Server) visibleAccountIDs(r *http.Request) (map[string]struct{}, bool) {
	key, ok := scopedKeyFromRequest(r)
	if !ok || (len(key.AccountIDs) == 0 && len(key.Regions) == 0) {
		return nil, false
	}
	ids := make(map[string]struct{}, len(key.AccountIDs))
	for _, account := range s.store.Accounts() {
		if keyAllowsAccount(key, account) {
			ids[account.ID] = struct{}{}
		}
	}
	return ids, true
}

func (s *Server) filterAccountsForRequest(r *http.Request, accounts []model.Account) []model.Account {
	key, ok := scopedKeyFromRequest(r)
	if !ok || (len(key.AccountIDs) == 0 && len(key.Regions) == 0) {
		return accounts
	}
	filtered := make([]model.Account, 0, len(accounts))
	for _, account := range accounts {
		if keyAllowsAccount(key, account) {
			filtered = append(filtered, account)
		}
	}
	return filtered
}

func (s *Server) handleListScopedKeys(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		userID = r.URL.Query().Get("userId")
	}
	keys := s.store.ScopedKeys()
	filtered := make([]model.ScopedKey, 0, len(keys))
	for _, key := range keys {
		if userID != "" && key.UserID != userID {
			continue
		}
		filtered = append(filtered, key)
	}
	start, end, ok := paginate(w, r, len(filtered))
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, filtered[start:end])
}

func (s *Server) handleCreateScopedKey(w http.ResponseWriter, r *http.Request) {
	var input model.ScopedKeyCreateRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	key, secret, err := s.store.CreateScopedKey(input)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, model.ScopedKeyCreated{Key: key, Secret: secret})
}

func (s *Server) handleGetScopedKey(w http.ResponseWriter, r *http.Request) {
	key, ok := s.store.ScopedKey(pathParam(r, "id"))
	if !ok {
		writeError(w, http.StatusNotFound, CodeNotFound, "scoped key not found")
		return
	}
	writeJSON(w, http.StatusOK, key)
}

func (s *Server) handleDeleteScopedKey(w http.ResponseWriter, r *http.Request) {
	if err := s.store.RevokeScopedKey(pathParam(r, "id")); err != nil {
		s.writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
