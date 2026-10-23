// Package api exposes the ajs-accountd HTTP surface. It contains no business
// state: every handler validates, calls store, and writes the dashboard JSON
// contract.
package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/runtime"
	"ajsaccountd/internal/store"
)

// Error codes. The first five are the contract codes from docs/PANEL.md §3;
// UNAUTHORIZED, METHOD_NOT_ALLOWED and INTERNAL cover transport-level failures.
const (
	CodeInvalidArgument     = "INVALID_ARGUMENT"
	CodeNotFound            = "NOT_FOUND"
	CodeConflict            = "CONFLICT"
	CodeUpstreamUnavailable = "UPSTREAM_UNAVAILABLE"
	CodeQuotaExceeded       = "QUOTA_EXCEEDED"
	CodeUnauthorized        = "UNAUTHORIZED"
	CodeMethodNotAllowed    = "METHOD_NOT_ALLOWED"
	CodeInternal            = "INTERNAL"
)

const defaultMaxBodyBytes int64 = 1 << 20 // 1 MiB

// DefaultCORSOrigins are the dashboard dev and preview origins.
var DefaultCORSOrigins = []string{
	"http://127.0.0.1:18081",
	"http://127.0.0.1:5173",
}

// Panel is the optional Resin control-plane view. When nil the server serves
// the store-derived topology, POST /v1/reconcile answers 503, and node/lease
// mutations use the store directly. It is an alias of runtime.Panel so the
// mutation methods cannot drift from the implementation.
type Panel = runtime.Panel

// LoginService is the optional vendor login path. When nil the login handler
// keeps the store-only session refresh, so the daemon never contacts a vendor
// host unless AJS_ACCOUNT_LOGIN=1 wires an implementation.
type LoginService interface {
	Login(ctx context.Context, accountID string) (model.Account, error)
}

// Config is the runtime configuration of the HTTP server.
type Config struct {
	AdminToken    string
	AllowInsecure bool
	CORSOrigins   []string
	MaxBodyBytes  int64
	Version       string
	// Panel is set when AJS_RESIN_ADMIN_TOKEN is configured; it makes
	// /v1/topology report the live Resin view and enables /v1/reconcile.
	Panel Panel
	// LoginService is set when AJS_ACCOUNT_LOGIN=1; POST
	// /v1/accounts/{id}/actions/login then performs a vendor login instead
	// of the store-only session refresh.
	LoginService LoginService
	// LoginHook runs after a successful vendor login, before the response is
	// written. It is nil unless AJS_ACCOUNT_LOGIN=1 wired the node
	// controller. An error is logged and the login response is unchanged, so
	// a failed gate start never turns a successful login into an error.
	LoginHook func(ctx context.Context, accountID string) error
	// AccountDeleteHook runs before DELETE /v1/accounts/{id} removes the
	// account from the store. It is nil unless AJS_ACCOUNT_LOGIN=1 wired the
	// node controller, which uses it to stop the account's gates and FV
	// sessions. Unlike LoginHook its error aborts the delete: removing the
	// store nodes while their gates stay open is the B0 gap this seam closes.
	AccountDeleteHook func(ctx context.Context, accountID string) error
}

// Server wires the store to an http.Handler.
type Server struct {
	cfg     Config
	store   *store.Store
	logger  *slog.Logger
	origins map[string]struct{}
	handler http.Handler
}

// New builds a server. Nil store and logger fall back to a seeded store and
// slog.Default; empty CORS origins fall back to DefaultCORSOrigins.
func New(cfg Config, st *store.Store, logger *slog.Logger) *Server {
	if st == nil {
		st = store.New()
	}
	if logger == nil {
		logger = slog.Default()
	}
	if len(cfg.CORSOrigins) == 0 {
		cfg.CORSOrigins = DefaultCORSOrigins
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = defaultMaxBodyBytes
	}
	if cfg.Version == "" {
		cfg.Version = "0.1.0"
	}
	origins := make(map[string]struct{}, len(cfg.CORSOrigins))
	for _, origin := range cfg.CORSOrigins {
		origin = strings.TrimSpace(origin)
		if origin != "" {
			origins[origin] = struct{}{}
		}
	}
	s := &Server{cfg: cfg, store: st, logger: logger, origins: origins}
	s.handler = s.buildHandler()
	return s
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

func (s *Server) buildHandler() http.Handler {
	rt := &router{}
	s.routes(rt)
	var h http.Handler = rt
	h = s.auth(h)
	h = s.bodyLimit(h)
	h = s.cors(h)
	h = s.logRequests(h)
	return h
}

func (s *Server) routes(rt *router) {
	rt.handle(http.MethodGet, "/healthz", s.handleHealthz)
	rt.handle(http.MethodGet, "/v1/metrics", s.handleMetrics)

	rt.handle(http.MethodGet, "/v1/accounts", s.handleListAccounts)
	rt.handle(http.MethodPost, "/v1/accounts", s.handleCreateAccount)
	rt.handle(http.MethodGet, "/v1/accounts/{id}", s.handleGetAccount)
	rt.handle(http.MethodPatch, "/v1/accounts/{id}", s.handlePatchAccount)
	rt.handle(http.MethodDelete, "/v1/accounts/{id}", s.handleDeleteAccount)
	rt.handle(http.MethodPost, "/v1/accounts/{id}/actions/login", s.handleLoginAccount)
	rt.handle(http.MethodPost, "/v1/accounts/{id}/actions/logout", s.handleLogoutAccount)

	rt.handle(http.MethodGet, "/v1/nodes", s.handleListNodes)
	rt.handle(http.MethodPost, "/v1/nodes/{id}/actions/drain", s.handleDrainNode)
	rt.handle(http.MethodPost, "/v1/nodes/{id}/actions/undrain", s.handleUndrainNode)
	rt.handle(http.MethodPost, "/v1/nodes/{id}/actions/probe", s.handleProbeNode)

	rt.handle(http.MethodGet, "/v1/leases", s.handleListLeases)
	rt.handle(http.MethodPost, "/v1/leases/{id}/actions/rotate", s.handleRotateLease)
	rt.handle(http.MethodDelete, "/v1/leases/{id}", s.handleReleaseLease)

	rt.handle(http.MethodGet, "/v1/users", s.handleListUsers)
	rt.handle(http.MethodPost, "/v1/users", s.handleCreateUser)
	rt.handle(http.MethodPatch, "/v1/users/{id}", s.handlePatchUser)
	rt.handle(http.MethodDelete, "/v1/users/{id}", s.handleDeleteUser)
	rt.handle(http.MethodPost, "/v1/users/{id}/suspend", s.handleSuspendUser)
	rt.handle(http.MethodPost, "/v1/users/{id}/resume", s.handleResumeUser)
	rt.handle(http.MethodPatch, "/v1/users/{id}/allowlist", s.handleAllowlistUser)

	rt.handle(http.MethodGet, "/v1/apikeys", s.handleListKeys)
	rt.handle(http.MethodPost, "/v1/apikeys", s.handleCreateKey)
	rt.handle(http.MethodDelete, "/v1/apikeys/{id}", s.handleDeleteKey)

	rt.handle(http.MethodGet, "/v1/requests", s.handleListRequests)
	rt.handle(http.MethodPost, "/v1/requests/mutations", s.handleCreateMutation)
	rt.handle(http.MethodGet, "/v1/audit", s.handleAudit)

	rt.handle(http.MethodGet, "/v1/settings", s.handleGetSettings)
	rt.handle(http.MethodPatch, "/v1/settings", s.handlePatchSettings)
	rt.handle(http.MethodGet, "/v1/topology", s.handleTopologyView)
	rt.handle(http.MethodPost, "/v1/reconcile", s.handleReconcile)
}

// handleTopologyView serves the Resin panel view when configured and the
// store-derived map otherwise, so the dashboard contract is unchanged while
// Resin is disabled.
func (s *Server) handleTopologyView(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Panel == nil {
		s.handleTopology(w, r)
		return
	}
	topology, err := s.cfg.Panel.Topology(r.Context())
	if err != nil {
		s.logger.Warn("resin_topology_failed", "error", err)
		writeError(w, http.StatusServiceUnavailable, CodeUpstreamUnavailable, "resin topology unavailable")
		return
	}
	writeJSON(w, http.StatusOK, topology)
}

// handleReconcile forces a re-render, PATCH and refresh through the panel.
// Without a panel there is no Resin to reconcile against.
func (s *Server) handleReconcile(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Panel == nil {
		writeError(w, http.StatusServiceUnavailable, CodeUpstreamUnavailable,
			"resin is not configured; set AJS_RESIN_ADMIN_TOKEN")
		return
	}
	topology, err := s.cfg.Panel.Reconcile(r.Context())
	if err != nil {
		s.logger.Warn("resin_reconcile_failed", "error", err)
		writeError(w, http.StatusServiceUnavailable, CodeUpstreamUnavailable, "resin reconcile failed")
		return
	}
	writeJSON(w, http.StatusOK, topology)
}

// logRequests assigns a request id and emits one structured line per request.
// Request headers are intentionally never logged, so Authorization cannot leak.
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := newRequestID()
		w.Header().Set("X-Request-Id", requestID)
		rec := &statusRecorder{ResponseWriter: w}
		start := time.Now()
		next.ServeHTTP(rec, r)
		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}
		s.logger.Info("http_request",
			"request_id", requestID,
			"method", r.Method,
			"path", r.URL.Path,
			"status", status,
			"bytes", rec.bytes,
			"duration_ms", time.Since(start).Milliseconds(),
			"remote", r.RemoteAddr,
		)
	})
}

func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		allowed := false
		if origin != "" {
			_, allowed = s.origins[origin]
			if allowed {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Add("Vary", "Origin")
				w.Header().Set("Access-Control-Expose-Headers", "X-Request-Id")
			}
		}
		if r.Method == http.MethodOptions {
			if allowed {
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Accept")
				w.Header().Set("Access-Control-Max-Age", "600")
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) bodyLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxBodyBytes)
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1/") {
			next.ServeHTTP(w, r)
			return
		}
		if s.cfg.AllowInsecure {
			next.ServeHTTP(w, r)
			return
		}
		const prefix = "Bearer "
		header := r.Header.Get("Authorization")
		token := strings.TrimPrefix(header, prefix)
		if s.cfg.AdminToken == "" || !strings.HasPrefix(header, prefix) ||
			subtle.ConstantTimeCompare([]byte(token), []byte(s.cfg.AdminToken)) != 1 {
			writeError(w, http.StatusUnauthorized, CodeUnauthorized, "missing or invalid bearer token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func newRequestID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("req-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf[:])
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(p)
	r.bytes += n
	return n, err
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// router is a tiny exact-match mux with {param} segments. It distinguishes 404
// (no path match) from 405 (path matched, method did not) so both can be JSON.
type router struct {
	routes []route
}

type route struct {
	method  string
	pattern string
	handler http.HandlerFunc
}

func (rt *router) handle(method, pattern string, handler http.HandlerFunc) {
	rt.routes = append(rt.routes, route{method: method, pattern: pattern, handler: handler})
}

func (rt *router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	allowed := make([]string, 0, 3)
	for _, candidate := range rt.routes {
		params, ok := matchPattern(candidate.pattern, r.URL.Path)
		if !ok {
			continue
		}
		allowed = append(allowed, candidate.method)
		if candidate.method != r.Method {
			continue
		}
		ctx := context.WithValue(r.Context(), paramsKey{}, params)
		candidate.handler(w, r.WithContext(ctx))
		return
	}
	if len(allowed) > 0 {
		w.Header().Set("Allow", strings.Join(allowed, ", "))
		writeError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed,
			fmt.Sprintf("%s is not allowed on %s", r.Method, r.URL.Path))
		return
	}
	writeError(w, http.StatusNotFound, CodeNotFound, "no route for "+r.Method+" "+r.URL.Path)
}

func matchPattern(pattern, path string) (map[string]string, bool) {
	patternParts := strings.Split(pattern, "/")
	pathParts := strings.Split(path, "/")
	if len(patternParts) != len(pathParts) {
		return nil, false
	}
	params := make(map[string]string, 1)
	for i, part := range patternParts {
		if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") {
			if pathParts[i] == "" {
				return nil, false
			}
			params[part[1:len(part)-1]] = pathParts[i]
			continue
		}
		if part != pathParts[i] {
			return nil, false
		}
	}
	return params, true
}

type paramsKey struct{}

func pathParam(r *http.Request, name string) string {
	params, _ := r.Context().Value(paramsKey{}).(map[string]string)
	return params[name]
}

type errorResponse struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if value == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorResponse{Error: errorDetail{Code: code, Message: message}})
}

func (s *Server) writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrInvalid):
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, err.Error())
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, CodeNotFound, err.Error())
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, CodeConflict, err.Error())
	case errors.Is(err, store.ErrUpstream):
		writeError(w, http.StatusServiceUnavailable, CodeUpstreamUnavailable, err.Error())
	default:
		s.logger.Error("store_error", "error", err)
		writeError(w, http.StatusInternalServerError, CodeInternal, "internal error")
	}
}

// writePanelError maps a panel mutation failure onto the same error contract
// as a store failure. Missing leases are 404; a missing platform/topology tag
// or the publisher's rotation window is 409; every other failure is a Resin
// problem and answers 503 UPSTREAM_UNAVAILABLE (details stay in the log).
func (s *Server) writePanelError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrInvalid):
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, err.Error())
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, CodeNotFound, err.Error())
	case errors.Is(err, store.ErrConflict), errors.Is(err, runtime.ErrRotationRateLimited):
		writeError(w, http.StatusConflict, CodeConflict, err.Error())
	default:
		s.logger.Warn("resin_mutation_failed", "error", err)
		writeError(w, http.StatusServiceUnavailable, CodeUpstreamUnavailable, "resin mutation failed")
	}
}

// decodeJSON decodes exactly one JSON value into dst and rejects unknown
// object fields, so a typo such as "concurency" answers 400 instead of being
// silently dropped and replaced by a default. PATCH /v1/settings decodes into
// a map, where DisallowUnknownFields has no effect; that handler keeps its
// explicit allowlist.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, CodeInvalidArgument, "request body exceeds 1 MiB")
			return false
		}
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "invalid JSON body: "+err.Error())
		return false
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "request body must contain a single JSON value")
		return false
	}
	return true
}

// paginate returns the [start,end) window for ?limit=&offset=. A limit of 0
// means "no limit"; the dashboard always sends a limit for audit only.
//
// The window is clamped before adding: offset never exceeds total, so
// total-offset cannot go negative and offset+limit cannot overflow however
// large the query values are.
func paginate(w http.ResponseWriter, r *http.Request, total int) (int, int, bool) {
	limit, offset := 0, 0
	query := r.URL.Query()
	if raw := query.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			writeError(w, http.StatusBadRequest, CodeInvalidArgument, "limit must be a non-negative integer")
			return 0, 0, false
		}
		limit = parsed
	}
	if raw := query.Get("offset"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			writeError(w, http.StatusBadRequest, CodeInvalidArgument, "offset must be a non-negative integer")
			return 0, 0, false
		}
		offset = parsed
	}
	if offset > total {
		offset = total
	}
	if limit == 0 {
		return offset, total, true
	}
	if limit > total-offset {
		limit = total - offset
	}
	return offset, offset + limit, true
}
