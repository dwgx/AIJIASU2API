package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/store"
)

// Portal cookie and header names. The __Host- prefix requires Secure, Path=/
// and no Domain attribute, so a sibling host cannot set or shadow the cookie.
const (
	portalCookieName = "__Host-ajs_portal"
	portalCSRFHeader = "X-CSRF-Token"
)

// Portal rate-limit budgets, per minute. Every bucket id is prefixed with
// "portal:" so it can never collide with a scoped-key id.
const (
	portalGlobalIPLimit        = 120
	portalSignupIPLimit        = 5
	portalSignupEmailLimit     = 3
	portalLoginIPLimit         = 10
	portalLoginEmailLimit      = 5
	portalKeyWriteLimitPerMin  = 10
	portalMaxEmailBucketKeyLen = 254
)

// portalPrincipal is the authenticated customer identity installed in the
// request context. It is never derived from an Authorization header: a cookie
// session is the only way to become a tenant, and an admin token cannot.
type portalPrincipal struct {
	Tenant        model.Tenant
	User          model.PanelUser
	SessionIDHash string
}

type portalPrincipalKey struct{}

func withPortalPrincipal(r *http.Request, principal portalPrincipal) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), portalPrincipalKey{}, principal))
}

func portalPrincipalFrom(r *http.Request) (portalPrincipal, bool) {
	principal, ok := r.Context().Value(portalPrincipalKey{}).(portalPrincipal)
	return principal, ok
}

// isPortalPath reports whether the path belongs to the customer portal chain.
// The exact chain is separate from the dashboard API: no route scope, no
// bearer fallback and no admin identity is consulted for it.
func isPortalPath(path string) bool {
	return path == "/v1/portal" || strings.HasPrefix(path, "/v1/portal/")
}

// isAdminTenantPath reports whether the path belongs to tenant administration.
// These routes require the admin token or a scoped key carrying the admin
// scope even when AJS_ALLOW_INSECURE=1: insecure mode keeps its legacy effect
// on the existing dashboard routes but never bypasses the tenant chains.
func isAdminTenantPath(path string) bool {
	return path == "/v1/admin/tenants" || strings.HasPrefix(path, "/v1/admin/tenants/")
}

// portalCookiePresent reports whether the request carries a portal session
// cookie. Tenant administration refuses that identity explicitly (403) instead
// of pretending the caller is an anonymous dashboard client (401).
func portalCookiePresent(r *http.Request) bool {
	cookie, err := r.Cookie(portalCookieName)
	return err == nil && cookie.Value != ""
}

func isUnsafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	default:
		return true
	}
}

// servePortalAuth authenticates the /v1/portal chain. Public signup/login skip
// the session check but still pass the origin and global-IP checks; every
// other route requires a live cookie session and, on an unsafe method, the
// session's CSRF token.
func (s *Server) servePortalAuth(w http.ResponseWriter, r *http.Request, next http.Handler) {
	// Portal responses carry identity state and a CSRF token; no shared cache
	// may store them, and a cache key must include the cookie.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Add("Vary", "Cookie")

	if !s.portalLimit(w, "portal:global:ip:"+s.portalClientIP(r), portalGlobalIPLimit,
		"too many portal requests from this address") {
		return
	}

	if isUnsafeMethod(r.Method) && !s.allowPortalOrigin(w, r) {
		return
	}

	if isPublicPortalPath(r.URL.Path) {
		next.ServeHTTP(w, r)
		return
	}

	cookie, err := r.Cookie(portalCookieName)
	if err != nil || cookie.Value == "" {
		writeError(w, http.StatusUnauthorized, CodeUnauthorized, "portal session required")
		return
	}
	session, tenant, user, err := s.store.AuthenticatePortalSession(cookie.Value, time.Now())
	if err != nil {
		if err == store.ErrForbidden {
			writeError(w, http.StatusForbidden, CodeForbidden, "tenant is not allowed to sign in")
			return
		}
		writeError(w, http.StatusUnauthorized, CodeUnauthorized, "portal session is invalid or expired")
		return
	}
	if isUnsafeMethod(r.Method) && !csrfTokenMatches(session, r.Header.Get(portalCSRFHeader)) {
		writeError(w, http.StatusForbidden, CodeForbidden, "invalid or missing CSRF token")
		return
	}

	principal := portalPrincipal{Tenant: tenant, User: user, SessionIDHash: session.IDHash}
	next.ServeHTTP(w, withPortalPrincipal(r, principal))
}

// isPublicPortalPath reports the two unauthenticated protocol paths. The
// method is deliberately not checked: a wrong method reaches the router, which
// answers 405, instead of failing a session check that was never required.
func isPublicPortalPath(path string) bool {
	return path == "/v1/portal/signup" || path == "/v1/portal/login"
}

// allowPortalOrigin requires the exact configured Origin on every unsafe
// portal request, and refuses a browser that declares a cross-site initiator.
// Credentialed CORS is deliberately not enabled: the portal is same-origin.
func (s *Server) allowPortalOrigin(w http.ResponseWriter, r *http.Request) bool {
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")), "cross-site") {
		writeError(w, http.StatusForbidden, CodeForbidden, "cross-site portal request refused")
		return false
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" || s.cfg.TenantOrigin == "" || !strings.EqualFold(origin, s.cfg.TenantOrigin) {
		writeError(w, http.StatusForbidden, CodeForbidden, "portal origin is not allowed")
		return false
	}
	return true
}

// portalLimit charges one token from a dedicated bucket. A refusal carries
// Retry-After set to one refill interval so a retry at that point is
// guaranteed to find a token.
func (s *Server) portalLimit(w http.ResponseWriter, key string, limit int, message string) bool {
	ok, remaining, reset := s.tenantLimiter.Allow(key, limit)
	w.Header().Set("X-RateLimit-Limit", strconv.Itoa(limit))
	w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
	if reset.IsZero() {
		w.Header().Set("X-RateLimit-Reset", "0")
	} else {
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
	}
	if ok {
		return true
	}
	retryAfter := 1
	if limit > 0 {
		retryAfter = 60 / limit
		if 60%limit != 0 {
			retryAfter++
		}
	}
	if retryAfter < 1 {
		retryAfter = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	writeError(w, http.StatusTooManyRequests, CodeRateLimited, message)
	return false
}

// portalEmailKey builds the per-email bucket key. An over-long address is
// hashed so a request cannot grow the limiter map with an unbounded key.
func portalEmailKey(raw string) string {
	email := strings.ToLower(strings.TrimSpace(raw))
	if len(email) > portalMaxEmailBucketKeyLen {
		sum := sha256.Sum256([]byte(email))
		return hex.EncodeToString(sum[:])
	}
	return email
}

// portalClientIP resolves the caller address. Forwarding headers are trusted
// only when the direct peer is a configured proxy.
func (s *Server) portalClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil {
		return host
	}
	addr = addr.Unmap()
	if !s.trustedProxy(addr) {
		return addr.String()
	}
	for _, raw := range strings.Split(r.Header.Get("X-Forwarded-For"), ",") {
		candidate := strings.TrimSpace(raw)
		if candidate == "" {
			continue
		}
		if parsed, err := netip.ParseAddr(strings.Trim(candidate, "[]")); err == nil {
			return parsed.Unmap().String()
		}
		return candidate
	}
	return addr.String()
}

func (s *Server) trustedProxy(addr netip.Addr) bool {
	for _, prefix := range s.trustedProxies {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func csrfTokenMatches(session model.PortalSession, token string) bool {
	if token == "" || len(token) > 128 || session.CSRFHash == "" {
		return false
	}
	sum := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(session.CSRFHash)) == 1
}

func setPortalCookie(w http.ResponseWriter, raw string, now time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     portalCookieName,
		Value:    raw,
		Path:     "/",
		Expires:  now.Add(store.PortalSessionAbsoluteTTL),
		MaxAge:   int(store.PortalSessionAbsoluteTTL / time.Second),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})
}

func clearPortalCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     portalCookieName,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(1, 0),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})
}

// NormalizeTenantOrigin validates AJS_TENANT_ORIGIN and reduces it to the
// scheme://host form an Origin header carries. An empty value is valid (the
// portal is then disabled or fails closed at request time); a path, query,
// fragment or userinfo is refused.
func NormalizeTenantOrigin(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("AJS_TENANT_ORIGIN %q: %w", raw, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("AJS_TENANT_ORIGIN %q must use http or https", raw)
	}
	if parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Path != "" && parsed.Path != "/") {
		return "", fmt.Errorf("AJS_TENANT_ORIGIN %q must be a bare origin such as https://panel.example.com", raw)
	}
	return strings.ToLower(parsed.Scheme) + "://" + strings.ToLower(parsed.Host), nil
}

// ParseTrustedProxies parses AJS_TRUSTED_PROXIES entries as IPs or CIDRs.
func ParseTrustedProxies(values []string) ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(values))
	for _, raw := range values {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if prefix, err := netip.ParsePrefix(raw); err == nil {
			out = append(out, prefix.Masked())
			continue
		}
		addr, err := netip.ParseAddr(strings.Trim(raw, "[]"))
		if err != nil {
			return nil, fmt.Errorf("AJS_TRUSTED_PROXIES entry %q must be an IP or CIDR prefix", raw)
		}
		addr = addr.Unmap()
		out = append(out, netip.PrefixFrom(addr, addr.BitLen()))
	}
	return out, nil
}
