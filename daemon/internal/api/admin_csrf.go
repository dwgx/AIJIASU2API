package api

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Admin mutation CSRF contract. The admin bearer is injected by the production
// nginx for every /v1/ request, so a cross-origin HTML form would otherwise be
// authenticated by the browser. Two independent checks make that impossible:
//
//   - X-AJS-CSRF is a non-simple header. A form cannot set it, and a
//     cross-origin fetch that tries to set it must pass a CORS preflight that
//     an unknown origin never receives. Non-browser clients set the header
//     explicitly; there is no silent bypass.
//   - Origin, when present, must exactly match an allowlisted admin origin.
//     Browser unsafe requests always carry Origin, so a hostile page is
//     rejected even if it somehow obtains the custom header. Requests without
//     Origin are non-browser clients and are still required to send the
//     header.
const (
	adminCSRFHeader = "X-AJS-CSRF"
	adminCSRFValue  = "1"
)

// NormalizeAdminOrigins validates AJS_ADMIN_ORIGINS entries and reduces each
// to the bare scheme://host form an Origin header carries. Duplicates are
// removed. An empty entry is ignored; a path, query, fragment or userinfo is
// refused.
func NormalizeAdminOrigins(values []string) ([]string, error) {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		origin, err := normalizeOrigin(raw)
		if err != nil {
			return nil, fmt.Errorf("AJS_ADMIN_ORIGINS entry %q: %w", raw, err)
		}
		if _, ok := seen[origin]; ok {
			continue
		}
		seen[origin] = struct{}{}
		out = append(out, origin)
	}
	return out, nil
}

// normalizeOrigin parses one bare http(s) origin. An empty value is valid and
// returns ""; it is handled by callers that treat "not configured" as a
// fail-closed state.
func normalizeOrigin(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("must use http or https")
	}
	if parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Path != "" && parsed.Path != "/") {
		return "", fmt.Errorf("must be a bare origin such as https://admin.example.com")
	}
	return scheme + "://" + strings.ToLower(parsed.Host), nil
}

// isAdminCSRFPath reports whether an unsafe request belongs to the admin
// surface. The customer portal has its own session-bound CSRF token, and the
// payment providers authenticate a signature over the raw body; neither can
// present an admin header. Everything else under /v1/ is an admin route,
// including routes added later, so a new mutation cannot silently skip the
// check.
func isAdminCSRFPath(path string) bool {
	if !strings.HasPrefix(path, "/v1/") {
		return false
	}
	if isPortalPath(path) {
		return false
	}
	switch path {
	case stripeWebhookPath, epayNotifyPath:
		return false
	default:
		return true
	}
}

// adminCSRF enforces the non-simple header and the exact Origin allowlist on
// every unsafe admin request. It runs after auth so missing credentials still
// answer 401, and before the router so no handler can observe a rejected
// request.
func (s *Server) adminCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isUnsafeMethod(r.Method) || !isAdminCSRFPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		// Sec-Fetch-Site is browser-controlled and cannot be forged by page
		// JavaScript. A cross-site declaration is refused even if the Origin
		// value itself would otherwise match.
		if strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")), "cross-site") {
			writeError(w, http.StatusForbidden, CodeForbidden, "cross-site admin request refused")
			return
		}

		origins := r.Header.Values("Origin")
		if len(origins) > 1 {
			writeError(w, http.StatusForbidden, CodeForbidden, "admin origin is not allowed")
			return
		}
		if raw := strings.TrimSpace(r.Header.Get("Origin")); raw != "" {
			origin, err := normalizeOrigin(raw)
			if err != nil {
				writeError(w, http.StatusForbidden, CodeForbidden, "admin origin is not allowed")
				return
			}
			if _, ok := s.adminOrigins[origin]; !ok {
				writeError(w, http.StatusForbidden, CodeForbidden, "admin origin is not allowed")
				return
			}
		}

		if strings.TrimSpace(r.Header.Get(adminCSRFHeader)) != adminCSRFValue {
			writeError(w, http.StatusForbidden, CodeForbidden, "missing or invalid admin CSRF header")
			return
		}
		next.ServeHTTP(w, r)
	})
}
