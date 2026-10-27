package api

import (
	"context"
	"net/http"
	"strings"

	"ajsaccountd/internal/model"
)

// Stable error.reason values of a scoped-key 403. The OpenAPI contract
// (ErrorDetail.reason and the ScopedKeyForbidden response) names the same
// pair, so a client can branch on the reason instead of parsing the message.
const (
	// ReasonScopeRequired means the key does not carry the route's scope.
	ReasonScopeRequired = "scope_required"
	// ReasonScopedKeyRestricted means the key's accountIds/regions exclude
	// the requested surface.
	ReasonScopedKeyRestricted = "scoped_key_restricted"
	// ReasonAdminRequired means the route is operator-global: the admin token
	// or a key carrying the admin scope is required and no other scoped key is
	// served, whatever its scopes or account grant say.
	ReasonAdminRequired = "admin_required"
)

// routeScopes maps a registered route pattern to the scope a scoped key needs.
// Routes absent from the table are admin-only: a scoped key without the admin
// scope cannot reach them. Keeping the table beside the handlers makes the
// least-privilege contract auditable in one place.
var routeScopes = map[string]model.Scope{
	http.MethodGet + " /metrics":    model.ScopeMetricsRead,
	http.MethodGet + " /v1/metrics": model.ScopeMetricsRead,

	// Account export, import and bulk-delete are deliberately absent. A
	// filtered export still discloses credentials, import has no tenant-owned
	// id or region semantics, and a filtered bulk-delete is destructive with
	// partial success; so a scoped key without the admin scope is refused by
	// the router before any handler runs. Only the static capabilities read,
	// which names limits and never account data, stays available.
	http.MethodGet + " /v1/accounts":                          model.ScopeAccountsRead,
	http.MethodPost + " /v1/accounts":                         model.ScopeAccountsWrite,
	http.MethodGet + " /v1/accounts/io/capabilities":          model.ScopeAccountsRead,
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
	// select-node is the async wrapper of the same guarded switch, so it uses
	// the catalog write grant. Operation reads are catalog reads; cancel is a
	// write because it stops a queued switch. No new scope vocabulary is
	// introduced by this ticket.
	http.MethodPost + " /v1/accounts/{id}/actions/select-node": model.ScopeCatalogWrite,
	http.MethodGet + " /v1/operations":                         model.ScopeCatalogRead,
	http.MethodGet + " /v1/operations/{id}":                    model.ScopeCatalogRead,
	http.MethodDelete + " /v1/operations/{id}":                 model.ScopeCatalogWrite,

	http.MethodGet + " /v1/schedules":                   model.ScopeSettingsRead,
	http.MethodPost + " /v1/schedules":                  model.ScopeSettingsWrite,
	http.MethodGet + " /v1/schedules/preview":           model.ScopeSettingsRead,
	http.MethodPost + " /v1/schedules/preview":          model.ScopeSettingsRead,
	http.MethodGet + " /v1/schedules/{id}":              model.ScopeSettingsRead,
	http.MethodPatch + " /v1/schedules/{id}":            model.ScopeSettingsWrite,
	http.MethodDelete + " /v1/schedules/{id}":           model.ScopeSettingsWrite,
	http.MethodPost + " /v1/schedules/{id}/actions/run": model.ScopeSettingsWrite,
	http.MethodGet + " /v1/schedules/{id}/runs":         model.ScopeSettingsRead,

	// Lab telemetry is operator diagnostics, not a customer surface: every
	// /v1/lab/* route is deliberately absent, so only the admin token or a key
	// carrying the admin scope can reach it. The rows name other tenants'
	// accounts, users, targets and gate ports, and the egress check spends a
	// real vendor flow; neither belongs to a least-privilege key.

	http.MethodGet + " /v1/nodes":                       model.ScopeCatalogRead,
	http.MethodPost + " /v1/nodes/{id}/actions/drain":   model.ScopeCatalogWrite,
	http.MethodPost + " /v1/nodes/{id}/actions/undrain": model.ScopeCatalogWrite,
	http.MethodPost + " /v1/nodes/{id}/actions/probe":   model.ScopeCatalogWrite,

	http.MethodGet + " /v1/leases":                      model.ScopeLeasesRead,
	http.MethodPost + " /v1/leases":                     model.ScopeLeasesWrite,
	http.MethodGet + " /v1/leases/{id}":                 model.ScopeLeasesRead,
	http.MethodPost + " /v1/leases/{id}/actions/rotate": model.ScopeLeasesWrite,
	http.MethodDelete + " /v1/leases/{id}":              model.ScopeLeasesWrite,

	http.MethodGet + " /v1/requests": model.ScopeRequestsRead,
	// The audit log is operator-global: rows carry every tenant's actor,
	// target, email and free text, so the route scope alone is not enough. The
	// handlers require an admin principal and refuse every other scoped key
	// with admin_required; audit:read stays in the table so a key without it is
	// refused with the more specific scope_required.
	http.MethodGet + " /v1/audit":            model.ScopeAuditRead,
	http.MethodGet + " /v1/audit/export.csv": model.ScopeAuditRead,

	http.MethodGet + " /v1/settings":   model.ScopeSettingsRead,
	http.MethodPatch + " /v1/settings": model.ScopeSettingsWrite,
	http.MethodGet + " /v1/topology":   model.ScopeCatalogRead,

	// Customer-facing platform reads. Each is a separate read grant, so a
	// monitoring key can reach usage, renewal or health without an accounts,
	// settings or admin scope. A key that names accounts or regions is still
	// narrowed to them by scopedKeyDenial and the handlers.
	http.MethodGet + " /v1/usage":                 model.ScopeUsageRead,
	http.MethodGet + " /v1/renewal/plans":         model.ScopeRenewalRead,
	http.MethodGet + " /v1/renewal/accounts/{id}": model.ScopeRenewalRead,
	http.MethodGet + " /v1/health/nodes":          model.ScopeHealthRead,

	// The replay ring and its SSE stream are operator-global. The route scope
	// still requires events:read so a key without it is refused with
	// scope_required, but the handlers additionally require an admin
	// principal: every non-admin scoped key answers 403 admin_required, since
	// the ring carries every tenant's events and no per-account or per-user
	// filter can be applied honestly.
	http.MethodGet + " /v1/events":        model.ScopeEventsRead,
	http.MethodGet + " /v1/events/stream": model.ScopeEventsRead,

	// The real-time debugger trace is a catalog read: it names the same
	// slot/generation/node attribution the catalog uses, and it exposes no
	// credential. Both endpoints share the grant; a key with any other scope
	// (catalog:write included) is refused by the router.
	http.MethodGet + " /v1/debug/trace":  model.ScopeCatalogRead,
	http.MethodGet + " /v1/debug/stream": model.ScopeCatalogRead,

	// POST /v1/backup, /v1/backup/inspect and /v1/backup/restore are
	// deliberately absent: a scoped key must never read the sealed panel files
	// or replace them, so those routes stay admin-only. The same holds for
	// every webhook route, including the GET reads: a customer key has no
	// business enumerating or editing the daemon's outbound receivers.
	// Settings and schedule writes keep their existing write scopes.
}

// authIdentity is the authenticated caller stored in the request context by
// the auth middleware. The zero value is never installed; an absent identity
// means the request is unauthenticated (healthz) or insecure mode is on, and
// the router then skips scope checks.
type authIdentity struct {
	admin bool
	// operator is the token-authenticated name in strict mode. The legacy
	// proxy header remains an audit label only when RBAC is not strict.
	operator string
	role     AdminRole
	rbac     bool
	key      model.ScopedKey
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
	return hasScope(id.key.Scopes, scope)
}

func hasScope(scopes []model.Scope, scope model.Scope) bool {
	for _, candidate := range scopes {
		if candidate == scope {
			return true
		}
	}
	return false
}

// scopedKeyDenial returns the 403 message for a request a scoped key may not
// make, or "" when it may proceed.
//
// A non-admin key that carries an account, catalog or lease scope must name at
// least one account or region: creation rejects that shape, so a key that still
// has no grant was minted before the rule and fails closed on every non-admin
// route until it is reissued.
//
// Otherwise accountIds/regions narrow every account-shaped route, read or
// write. The scopes decide which verbs a key may use; this restriction decides
// which accounts those verbs may reach, so a key scoped to acc-A cannot delete,
// connect, lease, drain, rotate or cancel on acc-B. It covers the account path
// (including the action sub-routes), the renewal account path, the
// account_id/accountId query, and the node and operation paths, which are
// resolved to their account through the store. Lease create, lease reads and
// lease mutations carry their own subject/account check in the handlers, before
// any side effect, and a usage account filter is the handler's 400: the
// customer surface is per-user, not per-account.
func (s *Server) scopedKeyDenial(key model.ScopedKey, r *http.Request) string {
	if key.RequiresAccountGrant() && !key.HasAccountGrant() {
		return "scoped key has no accountIds or regions; reissue it with an explicit account grant"
	}
	if !key.HasAccountGrant() {
		return ""
	}
	// The admin grant already covers every account, so a list on an
	// admin-scoped key narrows nothing. restrictedKey applies the same rule to
	// the handlers; this keeps the middleware in agreement with them.
	if hasScope(key.Scopes, model.ScopeAdmin) {
		return ""
	}
	read := r.Method == http.MethodGet || r.Method == http.MethodHead
	// The schedule list applies its own key filter before the accountId
	// filter, so an outside or unknown account answers an empty list instead of
	// 403. Every other route keeps the query denial below.
	if r.URL.Path == "/v1/schedules" {
		return ""
	}
	for _, name := range []string{"account_id", "accountId"} {
		if value := strings.TrimSpace(r.URL.Query().Get(name)); value != "" {
			if !s.keyAllowsAccountID(key, value) {
				return "scoped key is not allowed to use this account"
			}
		}
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 2 || parts[0] != "v1" {
		return ""
	}
	if len(parts) < 3 {
		return ""
	}
	switch parts[1] {
	case "accounts":
		switch parts[2] {
		case "io", "export", "import", "bulk-delete":
			return ""
		}
		if s.deniesAccountTarget(key, parts[2], read) {
			return "scoped key is not allowed to use this account"
		}
	case "renewal":
		if len(parts) < 4 || parts[2] != "accounts" {
			return ""
		}
		if s.deniesAccountTarget(key, parts[3], read) {
			return "scoped key is not allowed to use this account"
		}
	case "nodes":
		if read {
			return ""
		}
		node, ok := s.store.Node(parts[2])
		if !ok || s.deniesAccountTarget(key, node.AccountID, false) {
			return "scoped key is not allowed to use this account"
		}
	case "operations":
		// A read keeps the handler's 404 for an operation the key cannot see;
		// a cancel is a write and is refused here.
		if read {
			return ""
		}
		operation, ok := s.store.Operation(parts[2])
		if !ok || s.deniesAccountTarget(key, operation.AccountID, false) {
			return "scoped key is not allowed to use this account"
		}
	}
	return ""
}

// deniesAccountTarget reports whether a restricted key must be refused one
// account id. A write that names an account the store cannot resolve is
// refused with the same 403 as a foreign account, so a mutation cannot be used
// to probe whether another tenant's account or node exists. A read keeps the
// handler's 404, which is the documented answer for an unknown id.
func (s *Server) deniesAccountTarget(key model.ScopedKey, accountID string, read bool) bool {
	account, ok := s.store.Account(accountID)
	if !ok {
		return !read
	}
	return !key.AllowsAccount(account)
}

// scopedKeyAllowsAccount reports whether the caller may act on one account id.
// It is true for admin callers and for a key without an account grant (its
// scopes are all global). A key with a grant must name an account the store
// resolves and the key's accountIds/regions allow.
func (s *Server) scopedKeyAllowsAccount(r *http.Request, accountID string) bool {
	key, restricted := s.restrictedKey(r)
	if !restricted {
		return true
	}
	account, ok := s.store.Account(strings.TrimSpace(accountID))
	return ok && key.AllowsAccount(account)
}

// denyScopedKeyAccountTarget answers 403 when a restricted key names an account
// it may not act on, or names none at all: a lease create without accountId
// lets the store pick any free account, which the restriction cannot cover.
// It reports whether the response was written.
func (s *Server) denyScopedKeyAccountTarget(w http.ResponseWriter, r *http.Request, accountID string) bool {
	if s.scopedKeyAllowsAccount(r, accountID) {
		return false
	}
	writeErrorReason(w, http.StatusForbidden, CodeForbidden, ReasonScopedKeyRestricted,
		"scoped key is restricted to named accounts; accountId must name an account the key may use", nil)
	return true
}

// keyAllowsAccountID resolves an account reference from a query or path and
// applies the shared model predicate. An id the store cannot resolve is
// refused only when the key names explicit ids; a region-only key keeps the
// handler's empty result, which is the pre-existing query-filter contract.
func (s *Server) keyAllowsAccountID(key model.ScopedKey, id string) bool {
	if !key.HasAccountGrant() {
		return true
	}
	account, ok := s.store.Account(id)
	if !ok {
		return len(key.AccountIDs) == 0 || containsString(key.AccountIDs, id)
	}
	return key.AllowsAccount(account)
}

// accountByIDOrLabel resolves an account reference the way the usage filter
// accepts it: an exact id first, then an exact label.
func (s *Server) accountByIDOrLabel(value string) (model.Account, bool) {
	if account, ok := s.store.Account(value); ok {
		return account, true
	}
	for _, account := range s.store.Accounts() {
		if account.Label == value {
			return account, true
		}
	}
	return model.Account{}, false
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// customerKey returns the non-admin scoped key that is the caller, if any.
// Admin tokens and keys carrying the admin scope are operator callers: they
// keep the unfiltered, unscoped-by-user view. Handlers use this to bind a
// customer request to the key's user subject (key.UserID), which is the
// resource owner for leases; accounts and nodes remain operator assets.
func customerKey(r *http.Request) (model.ScopedKey, bool) {
	key, ok := scopedKeyFromRequest(r)
	if !ok || hasScope(key.Scopes, model.ScopeAdmin) {
		return model.ScopedKey{}, false
	}
	return key, true
}

// requestsForUser keeps the rows whose exact RequestLog.UserID matches the
// subject. It is the one per-user filter shared by the usage aggregate and the
// request log: both are attributed by UserID, never by the recyclable account
// label, so a customer key is served its own rows instead of a guess.
func requestsForUser(rows []model.RequestLog, userID string) []model.RequestLog {
	// A key without a subject must match nothing: unattributed rows (loopback
	// gate mode leaves UserID empty) are not the key's own traffic.
	if userID == "" {
		return []model.RequestLog{}
	}
	filtered := make([]model.RequestLog, 0, len(rows))
	for _, row := range rows {
		if row.UserID == userID {
			filtered = append(filtered, row)
		}
	}
	return filtered
}

// restrictedKey returns the scoped key installed by the auth middleware when it
// narrows the key to named accounts or regions. Admin tokens and admin-scoped
// keys never restrict, even when they carry accountIds/regions: the admin grant
// already covers every account. The bool is false for callers without a grant,
// so handlers skip account filtering entirely; a non-admin key whose scopes are
// account-bound always has a grant (creation enforces it and the middleware
// fails closed on a legacy key without one).
func (s *Server) restrictedKey(r *http.Request) (model.ScopedKey, bool) {
	key, ok := customerKey(r)
	if !ok || !key.HasAccountGrant() {
		return model.ScopedKey{}, false
	}
	return key, true
}

// denyOperatorOnly answers 403 for any scoped key that is not an admin
// principal. Some surfaces are operator-global rather than tenant data: the
// audit log carries every tenant's actor, target and free text, and the event
// ring carries every account's order, lease and schedule metadata. Neither can
// be partitioned honestly for a customer key, so the admin token or a key
// carrying the admin scope is required and every other scoped key is refused
// with admin_required regardless of its scopes or accountIds/regions. It
// reports whether the response was written.
func (s *Server) denyOperatorOnly(w http.ResponseWriter, r *http.Request, surface string) bool {
	key, ok := scopedKeyFromRequest(r)
	if !ok || hasScope(key.Scopes, model.ScopeAdmin) {
		return false
	}
	writeErrorReason(w, http.StatusForbidden, CodeForbidden, ReasonAdminRequired,
		surface+" is operator-global and is not available to a customer key", nil)
	return true
}

// denyGlobalRead answers 403 for a restricted key on a surface that is global
// and unattributed. Filtering such a response would have to guess ownership, so
// the request is refused instead of returning another tenant's rows. It reports
// whether the response was written.
func (s *Server) denyGlobalRead(w http.ResponseWriter, r *http.Request, surface string) bool {
	if _, restricted := s.restrictedKey(r); !restricted {
		return false
	}
	writeErrorReason(w, http.StatusForbidden, CodeForbidden, ReasonScopedKeyRestricted,
		"scoped key is restricted to named accounts; "+surface+" is not available", nil)
	return true
}

// visibleAccountIDs returns the account ids a restricted key may see. The bool
// is false when the key is unrestricted, so callers skip filtering entirely.
func (s *Server) visibleAccountIDs(r *http.Request) (map[string]struct{}, bool) {
	key, ok := s.restrictedKey(r)
	if !ok {
		return nil, false
	}
	ids := make(map[string]struct{}, len(key.AccountIDs))
	for _, account := range s.store.Accounts() {
		if key.AllowsAccount(account) {
			ids[account.ID] = struct{}{}
		}
	}
	return ids, true
}

func (s *Server) filterAccountsForRequest(r *http.Request, accounts []model.Account) []model.Account {
	key, ok := s.restrictedKey(r)
	if !ok {
		return accounts
	}
	filtered := make([]model.Account, 0, len(accounts))
	for _, account := range accounts {
		if key.AllowsAccount(account) {
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
	key, secret, err := s.store.CreateScopedKey(input, s.auditActor(r))
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
	if err := s.store.RevokeScopedKey(pathParam(r, "id"), s.auditActor(r)); err != nil {
		s.writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
