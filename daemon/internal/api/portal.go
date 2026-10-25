package api

import (
	"net/http"
	"time"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/usage"
)

// handlePortalSignup accepts a canonical email, password and display name and
// stores a pending tenant only. A new and a duplicate address produce the same
// 202 body; the store performs the dummy hash on the duplicate path so the
// timing does not distinguish them either.
func (s *Server) handlePortalSignup(w http.ResponseWriter, r *http.Request) {
	ip := s.portalClientIP(r)
	if !s.portalLimit(w, "portal:signup:ip:"+ip, portalSignupIPLimit,
		"too many signup attempts from this address") {
		return
	}
	var in model.TenantSignupInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if !s.portalLimit(w, "portal:signup:email:"+portalEmailKey(in.Email), portalSignupEmailLimit,
		"too many signup attempts for this address") {
		return
	}
	if err := s.store.SignupTenant(in); err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, struct {
		Status string `json:"status"`
	}{Status: string(model.TenantPending)})
}

// handlePortalLogin verifies the password and, for an active tenant, issues a
// rotated session cookie plus a CSRF token. Bad credentials answer a uniform
// 401; a verified pending or suspended tenant answers 403 and receives no
// session.
func (s *Server) handlePortalLogin(w http.ResponseWriter, r *http.Request) {
	ip := s.portalClientIP(r)
	if !s.portalLimit(w, "portal:login:ip:"+ip, portalLoginIPLimit,
		"too many login attempts from this address") {
		return
	}
	var in model.TenantLoginInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if !s.portalLimit(w, "portal:login:email:"+portalEmailKey(in.Email), portalLoginEmailLimit,
		"too many login attempts for this address") {
		return
	}

	tenant, ok := s.store.AuthenticateTenant(in.Email, in.Password)
	if !ok {
		writeError(w, http.StatusUnauthorized, CodeUnauthorized, "invalid email or password")
		return
	}
	switch tenant.Status {
	case model.TenantActive:
	case model.TenantPending:
		writeError(w, http.StatusForbidden, CodeForbidden, "tenant is pending approval")
		return
	case model.TenantSuspended:
		writeError(w, http.StatusForbidden, CodeForbidden, "tenant is suspended")
		return
	default:
		writeError(w, http.StatusForbidden, CodeForbidden, "tenant is not active")
		return
	}

	cookie, csrf, err := s.store.CreatePortalSession(tenant.ID)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if tenant.UserID == nil {
		writeError(w, http.StatusForbidden, CodeForbidden, "tenant has no linked user")
		return
	}
	user, ok := s.store.User(*tenant.UserID)
	if !ok {
		writeError(w, http.StatusForbidden, CodeForbidden, "tenant has no linked user")
		return
	}
	setPortalCookie(w, cookie, time.Now())
	writeJSON(w, http.StatusOK, model.TenantSessionView{
		Tenant:    tenant,
		User:      &user,
		CSRFToken: csrf,
	})
}

// handlePortalSession returns the tenant/user metadata and the session's CSRF
// token. The token is process-only, so a restart rotates it here; a client
// that reloads the page always fetches this route before mutating.
func (s *Server) handlePortalSession(w http.ResponseWriter, r *http.Request) {
	principal, ok := portalPrincipalFrom(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, CodeUnauthorized, "portal session required")
		return
	}
	csrf, err := s.store.EnsurePortalCSRF(principal.SessionIDHash)
	if err != nil {
		writeError(w, http.StatusUnauthorized, CodeUnauthorized, "portal session is invalid or expired")
		return
	}
	user := principal.User
	writeJSON(w, http.StatusOK, model.TenantSessionView{
		Tenant:    principal.Tenant,
		User:      &user,
		CSRFToken: csrf,
	})
}

// handlePortalLogout revokes this session and clears the cookie. It requires
// the CSRF token because it is an unsafe method.
func (s *Server) handlePortalLogout(w http.ResponseWriter, r *http.Request) {
	principal, ok := portalPrincipalFrom(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, CodeUnauthorized, "portal session required")
		return
	}
	_ = s.store.RevokePortalSession(principal.SessionIDHash)
	clearPortalCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

// handlePortalLeases lists only the linked user's leases. The ownership filter
// runs before pagination, so a page boundary can never expose another user's
// row.
func (s *Server) handlePortalLeases(w http.ResponseWriter, r *http.Request) {
	principal, ok := portalPrincipalFrom(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, CodeUnauthorized, "portal session required")
		return
	}
	leases := s.store.Leases()
	own := make([]model.Lease, 0, len(leases))
	for _, lease := range leases {
		if lease.UserID == principal.User.ID {
			own = append(own, lease)
		}
	}
	start, end, ok := paginate(w, r, len(own))
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, own[start:end])
}

// handlePortalUsage aggregates only rows whose immutable UserID equals the
// linked user. The client cannot supply another owner or account: those query
// parameters are ignored. Account budgets are admin configuration and stay
// null; the user row carries the quota and the derived monthly total.
func (s *Server) handlePortalUsage(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Usage == nil {
		writeError(w, http.StatusServiceUnavailable, CodeUpstreamUnavailable,
			"usage aggregation is not configured")
		return
	}
	principal, ok := portalPrincipalFrom(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, CodeUnauthorized, "portal session required")
		return
	}
	query := r.URL.Query()
	from, ok := parseUsageTime(w, query.Get("from"), "from", false)
	if !ok {
		return
	}
	to, ok := parseUsageTime(w, query.Get("to"), "to", true)
	if !ok {
		return
	}

	rows := s.store.Requests()
	own := make([]model.RequestLog, 0, len(rows))
	for _, row := range rows {
		if row.UserID == principal.User.ID {
			own = append(own, row)
		}
	}
	quota := principal.User.QuotaBytesMonth
	summary, err := s.cfg.Usage.Aggregate(own, usage.Query{
		From:   from,
		To:     to,
		UserID: principal.User.ID,
		Quotas: map[string]*int64{principal.User.ID: quota},
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, err.Error())
		return
	}
	// The aggregator only emits users it saw in a row. A customer with no
	// traffic still needs their own row to render a quota.
	found := false
	for i := range summary.Users {
		if summary.Users[i].UserID != principal.User.ID {
			continue
		}
		found = true
		summary.Users[i].UserName = principal.User.Name
		summary.Users[i].QuotaBytesMonth = quota
		summary.Users[i].UsedBytes = principal.User.UsedBytes
	}
	if !found {
		summary.Users = append(summary.Users, usage.UserUsage{
			UserID:          principal.User.ID,
			UserName:        principal.User.Name,
			QuotaBytesMonth: quota,
			UsedBytes:       principal.User.UsedBytes,
		})
	}
	summary.QuotaEnforced = false
	writeJSON(w, http.StatusOK, summary)
}

// handlePortalListKeys lists the linked user's key metadata, newest first.
func (s *Server) handlePortalListKeys(w http.ResponseWriter, r *http.Request) {
	principal, ok := portalPrincipalFrom(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, CodeUnauthorized, "portal session required")
		return
	}
	keys := s.store.TenantKeys(principal.User.ID)
	start, end, ok := paginate(w, r, len(keys))
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, keys[start:end])
}

// handlePortalCreateKey issues one key for the linked user and returns the
// secret exactly once. Only its SHA-256 is stored.
func (s *Server) handlePortalCreateKey(w http.ResponseWriter, r *http.Request) {
	principal, ok := portalPrincipalFrom(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, CodeUnauthorized, "portal session required")
		return
	}
	if !s.portalLimit(w, "portal:keys:"+principal.Tenant.ID, portalKeyWriteLimitPerMin,
		"too many key writes for this tenant") {
		return
	}
	var in model.TenantKeyCreateRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	key, secret, err := s.store.CreateTenantKey(principal.User.ID, in.Label)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, model.CreateKeyResult{Key: key, Secret: secret})
}

// handlePortalRevokeKey atomically checks ownership and revokes one key. A
// foreign id answers 404 exactly like an unknown one.
func (s *Server) handlePortalRevokeKey(w http.ResponseWriter, r *http.Request) {
	principal, ok := portalPrincipalFrom(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, CodeUnauthorized, "portal session required")
		return
	}
	if !s.portalLimit(w, "portal:keys:"+principal.Tenant.ID, portalKeyWriteLimitPerMin,
		"too many key writes for this tenant") {
		return
	}
	if err := s.store.RevokeTenantKey(principal.User.ID, pathParam(r, "id")); err != nil {
		s.writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
