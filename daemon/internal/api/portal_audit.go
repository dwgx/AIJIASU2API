package api

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/store"
)

const (
	// portalAuditDefaultLimit is the page size when ?limit is absent.
	portalAuditDefaultLimit = 50
	// portalAuditMaxLimit caps one page. ?limit=0 means "no limit" on the
	// dashboard lists; here the cap wins so a tenant can never pull the whole
	// retained ring in one response.
	portalAuditMaxLimit = 200
)

// portalAuditURL matches a scheme-qualified URL in a detail. The customer view
// never needs one, and a future writer must not be able to turn the activity
// tab into a link surface (or leak a signed URL) by putting one in a detail.
var portalAuditURL = regexp.MustCompile(`(?i)\bhttps?://[^\s,;]+`)

// portalAuditDetail scrubs credentials and full URLs from one detail.
func portalAuditDetail(detail string) string {
	return portalAuditURL.ReplaceAllString(sanitizeAuditDetail(detail), "[redacted-url]")
}

// handlePortalAudit serves GET /v1/portal/audit. It filters the retained audit
// ring to rows attributable to the signed-in tenant: the linked user, that
// user's leases, orders and keys, the tenant lifecycle, and portal sessions.
//
// The ownership filter runs before the cursor/limit window, so a page boundary
// can never expose a foreign row. The response carries no meta block: the
// identifiers an operator needs are not part of the customer contract, and
// dropping them keeps a future meta field from leaking by default.
func (s *Server) handlePortalAudit(w http.ResponseWriter, r *http.Request) {
	principal, ok := portalPrincipalFrom(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, CodeUnauthorized, "portal session required")
		return
	}
	limit, beforeSeq, ok := portalAuditWindow(w, r)
	if !ok {
		return
	}

	scope := s.portalAuditScope(principal)
	own := make([]model.AuditEvent, 0, limit)
	for _, event := range s.store.Audit(store.AuditRetention) {
		if !scope.owns(event) {
			continue
		}
		if beforeSeq >= 0 && auditSequence(event.ID) >= beforeSeq {
			continue
		}
		event.Detail = portalAuditDetail(event.Detail)
		event.Meta = nil
		own = append(own, event)
		if len(own) == limit {
			break
		}
	}
	writeJSON(w, http.StatusOK, own)
}

// portalAuditWindow parses ?limit=&before=. A malformed value answers 400
// instead of silently returning a different page. beforeSeq -1 means "from the
// newest row"; otherwise only rows with a strictly older audit sequence are
// returned, which keeps the cursor stable when the ring evicts the exact row a
// client last saw.
func portalAuditWindow(w http.ResponseWriter, r *http.Request) (limit int, beforeSeq int64, ok bool) {
	limit = portalAuditDefaultLimit
	beforeSeq = -1
	query := r.URL.Query()
	if raw := query.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			writeError(w, http.StatusBadRequest, CodeInvalidArgument,
				"limit must be a non-negative integer")
			return 0, -1, false
		}
		if parsed == 0 || parsed > portalAuditMaxLimit {
			parsed = portalAuditMaxLimit
		}
		limit = parsed
	}
	if raw := query.Get("before"); raw != "" {
		seq := auditSequence(raw)
		if seq < 0 {
			writeError(w, http.StatusBadRequest, CodeInvalidArgument,
				"before must be an audit event id")
			return 0, -1, false
		}
		beforeSeq = seq
	}
	return limit, beforeSeq, true
}

// auditSequence extracts N from an "audit-N" id. Seed and runtime ids share
// the shape, so the numeric value is a stable newest-first cursor even when a
// retention sweep has already dropped the row the client last saw.
func auditSequence(id string) int64 {
	const prefix = "audit-"
	if !strings.HasPrefix(id, prefix) {
		return -1
	}
	seq, err := strconv.ParseInt(strings.TrimPrefix(id, prefix), 10, 64)
	if err != nil || seq < 0 {
		return -1
	}
	return seq
}

// portalAuditScope is the immutable ownership map for one request. It is
// rebuilt per call: a lease can expire and a key can be issued between reads,
// and a cached set would then attribute a row to the wrong principal.
type portalAuditScope struct {
	tenantID string
	userID   string
	userName string
	email    string
	orderIDs map[string]struct{}
	leaseIDs map[string]struct{}
	keyIDs   map[string]struct{}
}

func (s *Server) portalAuditScope(principal portalPrincipal) portalAuditScope {
	scope := portalAuditScope{
		tenantID: principal.Tenant.ID,
		userID:   principal.User.ID,
		userName: principal.User.Name,
		email:    strings.ToLower(strings.TrimSpace(principal.Tenant.Email)),
		orderIDs: make(map[string]struct{}),
		leaseIDs: make(map[string]struct{}),
		keyIDs:   make(map[string]struct{}),
	}
	for _, order := range s.store.OrdersForTenant(principal.Tenant.ID) {
		if order.UserID == principal.User.ID {
			scope.orderIDs[order.ID] = struct{}{}
		}
	}
	for _, lease := range s.store.Leases() {
		if lease.UserID == principal.User.ID {
			scope.leaseIDs[lease.ID] = struct{}{}
		}
	}
	for _, key := range s.store.TenantKeys(principal.User.ID) {
		scope.keyIDs[key.ID] = struct{}{}
	}
	return scope
}

// owns reports whether one audit row belongs to the tenant. The order matters:
// a row that names an owner in meta is decided by that owner alone, so a
// mismatched tenant/user can never fall through to a weaker target match.
func (scope portalAuditScope) owns(event model.AuditEvent) bool {
	if meta := event.Meta; len(meta) > 0 {
		owned := false
		if userID := strings.TrimSpace(meta["userId"]); userID != "" {
			if userID != scope.userID {
				return false
			}
			owned = true
		}
		if tenantID := strings.TrimSpace(meta["tenantId"]); tenantID != "" {
			if tenantID != scope.tenantID {
				return false
			}
			owned = true
		}
		if owned {
			// An owner id is necessary but not sufficient: the action must be
			// one a customer can see. A future admin row that happens to
			// carry a userId stays hidden until its family is listed here,
			// which is the fail-closed direction for a privacy surface.
			return portalTenantAction(event.Action)
		}
		if orderID := strings.TrimSpace(meta["orderId"]); orderID != "" {
			if _, ok := scope.orderIDs[orderID]; ok {
				return true
			}
		}
		if leaseID := strings.TrimSpace(meta["leaseId"]); leaseID != "" {
			if _, ok := scope.leaseIDs[leaseID]; ok {
				return true
			}
		}
		if keyID := strings.TrimSpace(meta["keyId"]); keyID != "" {
			if _, ok := scope.keyIDs[keyID]; ok {
				return true
			}
		}
	}

	if event.Target == scope.userID || event.Target == scope.userName {
		return portalTenantAction(event.Action)
	}
	if scope.email != "" && strings.EqualFold(event.Target, scope.email) {
		return strings.HasPrefix(event.Action, "tenant.") || strings.HasPrefix(event.Action, "invite.")
	}
	if _, ok := scope.orderIDs[event.Target]; ok {
		return true
	}
	if _, ok := scope.leaseIDs[event.Target]; ok {
		return true
	}
	if _, ok := scope.keyIDs[event.Target]; ok {
		return true
	}
	return event.Actor == scope.userID || event.Actor == "tenant:"+scope.tenantID
}

// portalTenantAction lists the action families whose target is a user name.
// Admin rows that merely mention a user (for example account.test) are not
// customer activity and stay hidden even when the target matches.
func portalTenantAction(action string) bool {
	for _, prefix := range []string{
		"key.", "lease.", "order.", "payment.", "entitlement.",
		"refund.", "chargeback.", "webhook.", "user.", "session.",
	} {
		if strings.HasPrefix(action, prefix) {
			return true
		}
	}
	return false
}
