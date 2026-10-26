package api

import (
	"net/http"
	"strings"
)

// Admin operator identity contract (AJS-405).
//
// The production admin vhost terminates per-operator HTTP Basic auth and
// forwards the authenticated username as X-AJS-Operator. The shared admin
// bearer remains the API credential: the header is an audit label, never an
// authentication factor. A caller that can set the header already needs the
// bearer, so the header cannot widen authority; it only lets the daemon name
// the operator in audit rows instead of the generic "operator" fallback.
//
// The header is sanitized rather than copied verbatim. Audit rows are exported
// as CSV and rendered in the console, so a value carrying separators, control
// characters or an unbounded length would be an injection and retention
// problem. A malformed or absent value falls back to "operator"; it never
// fails the request.
const (
	adminOperatorHeader   = "X-AJS-Operator"
	adminOperatorFallback = "operator"
	maxAdminOperatorLen   = 64
)

// requestOperator returns the sanitized operator name carried by the request.
// It is called only after the admin bearer has been verified.
func requestOperator(r *http.Request) string {
	values := r.Header.Values(adminOperatorHeader)
	if len(values) != 1 {
		// Absent or ambiguous (duplicate) headers fall back rather than letting
		// a proxy/client pair choose which value is audited.
		return ""
	}
	return sanitizeOperator(values[0])
}

// sanitizeOperator accepts the username vocabulary an htpasswd entry uses:
// letters, digits, dot, underscore, hyphen, at-sign and plus. Everything else
// (including whitespace and control bytes) makes the whole value untrusted,
// so a malformed header cannot smuggle a second audit field.
func sanitizeOperator(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" || len(value) > maxAdminOperatorLen {
		return ""
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '.', r == '_', r == '-', r == '@', r == '+':
		default:
			return ""
		}
	}
	return value
}

// auditActor names the authenticated caller for a mutation. An operator token
// (strict mode) is its authenticated username, a legacy admin bearer is the
// proxy-authenticated header or the historical "operator" fallback, and a
// scoped key is its display prefix ("key:<prefix>"): the secret is never an
// audit label, but an operator can still tell two keys apart in the trail.
// Admin audit attribution is unchanged.
func (s *Server) auditActor(r *http.Request) string {
	identity, ok := authIdentityFrom(r)
	if !ok {
		return adminOperatorFallback
	}
	if identity.rbac {
		return identity.operator
	}
	if identity.admin {
		if identity.operator != "" {
			return identity.operator
		}
		return adminOperatorFallback
	}
	return "key:" + identity.key.Prefix
}
