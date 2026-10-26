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
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"ajsaccountd/internal/events"
	"ajsaccountd/internal/model"
	"ajsaccountd/internal/payment"
	"ajsaccountd/internal/ratelimit"
	"ajsaccountd/internal/runtime"
	"ajsaccountd/internal/store"
	"ajsaccountd/internal/usage"
	"ajsaccountd/internal/webhook"
)

// Error codes. The first five are the contract codes from docs/PANEL.md §3;
// UNAUTHORIZED, METHOD_NOT_ALLOWED and INTERNAL cover transport-level failures.
const (
	CodeInvalidArgument     = "INVALID_ARGUMENT"
	CodeNotFound            = "NOT_FOUND"
	CodeConflict            = "CONFLICT"
	CodeUpstreamUnavailable = "UPSTREAM_UNAVAILABLE"
	CodeQuotaExceeded       = "QUOTA_EXCEEDED"
	CodeRateLimited         = "RATE_LIMITED"
	CodeUnauthorized        = "UNAUTHORIZED"
	CodeForbidden           = "FORBIDDEN"
	CodeMethodNotAllowed    = "METHOD_NOT_ALLOWED"
	CodeInternal            = "INTERNAL"
)

const defaultMaxBodyBytes int64 = 1 << 20 // 1 MiB

// DefaultVersion is the daemon version reported when Config.Version is empty.
// main passes it explicitly so /healthz, the update check and the Lab overview
// cannot disagree.
const DefaultVersion = "0.1.0"

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
	// Forget drops the retained login result after logout/delete, so the
	// catalog cannot keep serving a snapshot for an account with no session.
	Forget(accountID string)
}

// Config is the runtime configuration of the HTTP server.
type Config struct {
	AdminToken    string
	AllowInsecure bool
	CORSOrigins   []string
	// AdminOrigins is the exact browser-origin allowlist for unsafe admin
	// requests (AJS_ADMIN_ORIGINS). Empty falls back to CORSOrigins; an Origin
	// header that is present but not allowlisted is refused. Requests without
	// an Origin header are non-browser clients and must still send X-AJS-CSRF.
	AdminOrigins []string
	MaxBodyBytes int64
	Version      string
	// Panel is set when AJS_RESIN_ADMIN_TOKEN is configured; it makes
	// /v1/topology report the live Resin view and enables /v1/reconcile.
	// Account delete/logout also reconcile it after the store mutation.
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
	// After the store delete succeeds the handler reconciles Panel, because
	// the nodes were still LIVE when the hook ran.
	AccountDeleteHook func(ctx context.Context, accountID string) error
	// AccountLogoutHook runs before POST /v1/accounts/{id}/actions/logout
	// pauses the account and drains its nodes. It is nil unless
	// AJS_ACCOUNT_LOGIN=1 wired the node controller, which uses it to close
	// the account's gates and FV sessions. Like AccountDeleteHook its error
	// aborts the logout: pausing the account while its gates keep listening
	// is the B0 logout gap this seam closes. The handler reconciles Panel
	// after store.LogoutAccount drained the nodes.
	AccountLogoutHook func(ctx context.Context, accountID string) error
	// NodeDrainHook runs before a drain/undrain touches the store or the
	// panel, so the node controller can stop or reopen the node's gate first.
	// It is nil unless AJS_ACCOUNT_LOGIN=1 wired the controller. An error is
	// logged and answers 500 INTERNAL with no state change; a node the store
	// does not know is answered 404 without calling the hook.
	NodeDrainHook func(ctx context.Context, nodeID string, drain bool) error
	// LeasePublishHook is the publication seam for a deployment that publishes
	// leases somewhere other than the Resin panel. Create is fail-closed on it:
	// an error rolls the lease back (store.RollbackLease writes the
	// lease.publish_failed audit row) and answers 409/503 with the reason
	// lease_publish_failed, so a lease is never reported created without its
	// publication. Revoke keeps the warning behaviour: the row is already gone
	// and the Resin revoke itself fails closed before the store mutation.
	LeasePublishHook func(ctx context.Context, action LeaseAction, lease model.Lease) error
	// UpdateRepo is the GitHub "owner/repo" checked by GET
	// /v1/system/update. Empty keeps the endpoint enabled:false and the
	// daemon makes no network call at all.
	UpdateRepo string
	// UpdateAPIBase overrides the GitHub API root for the update check
	// (default https://api.github.com). Tests point it at an httptest
	// server instead of the real API.
	UpdateAPIBase string
	// UpdateHTTP is the client used for the update check. Nil builds a
	// 10-second client per request.
	UpdateHTTP *http.Client
	// Lab is the read-only Lab telemetry view. Nil keeps every /v1/lab/*
	// route registered but answers enabled:false with a reason, so a test
	// server and a partially wired daemon degrade honestly.
	Lab LabReader
	// Catalog is the guarded selection/connect path. It is set when
	// AJS_ACCOUNT_LOGIN=1 wired the node controller; nil makes every
	// selection write and connect action answer login_disabled instead of
	// pretending to have a vendor catalog.
	Catalog CatalogConnector
	// AllowPlaintextExport gates JSON and CSV export. It is false unless
	// AJS_ALLOW_PLAINTEXT_EXPORT=1; encrypted export is always available.
	AllowPlaintextExport bool
	// Persist writes an immediate state snapshot after a bulk account
	// mutation. It is best-effort: a nil hook or a write failure never turns a
	// successful import/delete into an error.
	Persist func()
	// Schedules is the scheduler engine. Nil means AJS_SCHEDULES=0: the read
	// routes and the pure preview still work, every mutation answers 503.
	Schedules ScheduleService
	// Renewal is the cached public plan catalogue and per-account expiry
	// derivation. Nil answers 503 on both renewal routes.
	Renewal RenewalService
	// Usage aggregates the request log. Nil answers 503 on GET /v1/usage.
	Usage *usage.Aggregator
	// Health is the loopback gate sampler. Nil means AJS_HEALTH_PROBE=0 and
	// GET /v1/health/nodes reports enabled:false with a reason.
	Health HealthMonitor
	// Webhooks is the delivery dispatcher. Nil disables the webhook feature:
	// GET /v1/webhooks still lists persisted rows, every mutation answers 503.
	Webhooks *webhook.Dispatcher
	// WebhookSecrets seals endpoint signing secrets at rest. Nil keeps them in
	// process memory only, so they do not survive a restart; the daemon logs
	// and documents that caveat.
	WebhookSecrets WebhookSecretStore
	// Events receives customer-visible events (login/logout, node and schedule
	// transitions). Nil drops them.
	Events EventSink
	// EventRing is the replay ring behind GET /v1/events and
	// GET /v1/events/stream. main wires one ring and mirrors every event the
	// webhook sink receives into it, so both consumers see the same stream.
	// Nil answers 503 on both routes.
	EventRing *events.Ring
	// Debug is the bounded real-time trace ring behind GET /v1/debug/trace and
	// GET /v1/debug/stream. main wires one recorder shared with the node
	// controller, so the runtime boundaries and the API read the same ring.
	// Nil answers 503 on both routes.
	Debug *runtime.DebugRecorder
	// RateLimitClock is the clock the per-scoped-key token bucket reads. Nil
	// uses time.Now; tests inject a fake to prove refill and GC without
	// sleeping.
	RateLimitClock func() time.Time
	// StateDir is the directory holding state.json, secrets.json and
	// device-secrets.json (AJS_STATE_DIR). Empty disables the /v1/backup
	// routes with 503 BACKUP_UNAVAILABLE.
	StateDir string
	// StatePath overrides where state.json lives (AJS_STATE_FILE); it
	// defaults to StateDir/state.json, matching the daemon's own resolution,
	// so a relocated state file is backed up and restored in place.
	StatePath string
	// AllowBackupRestore lets POST /v1/backup/restore write the panel files
	// while the daemon runs (AJS_BACKUP_ALLOW_RESTORE=1). The default refusal
	// is deliberate: the running daemon keeps the replaced panel in memory and
	// cannot serve the restored one.
	AllowBackupRestore bool
	// Restore is the flag the server marks after a successful apply. The
	// daemon shares one instance between this server and its snapshot writer,
	// which stops writing state.json once the flag is set: the store still
	// holds the panel the restore replaced. Nil gives the server a private
	// flag, which is enough for a caller that never saves.
	Restore *RestoreFlag
	// TenantPortal enables the customer portal chains (/v1/portal/* and
	// /v1/admin/tenants*). It is false unless AJS_TENANT_PORTAL=1, in which
	// case the routes are not registered at all and answer 404. The feature is
	// additive: existing dashboard auth and scoped keys are unchanged.
	TenantPortal bool
	// TenantOrigin is the exact browser Origin allowed on signup, login and
	// every unsafe portal request (AJS_TENANT_ORIGIN, normalized to
	// scheme://host). Empty while the portal is enabled fails closed.
	TenantOrigin string
	// TrustedProxies lists the direct peers whose X-Forwarded-For header is
	// believed for portal rate limiting (AJS_TRUSTED_PROXIES, IPs or CIDRs).
	// Empty trusts no forwarding header.
	TrustedProxies []string
	// ManualPayments is the offline settlement provider behind the admin
	// attestation route. Nil answers 503 on POST /v1/admin/orders/{id}/settle;
	// there is no implicit fallback.
	ManualPayments payment.ManualSettlementProvider
	// StripeWebhooks verifies POST /v1/payments/stripe. Nil, or a provider that
	// reports itself disabled, answers 503; the webhook never falls back to
	// manual settlement.
	StripeWebhooks payment.WebhookProvider
	// Epay is the 易支付 redirect checkout and signed form notify adapter. Nil,
	// or an adapter with incomplete configuration, answers 503 on both the
	// portal checkout route and the notify callback.
	Epay *payment.Epay
}

// Server wires the store to an http.Handler.
type Server struct {
	cfg          Config
	store        *store.Store
	logger       *slog.Logger
	origins      map[string]struct{}
	adminOrigins map[string]struct{}
	started      time.Time // set in New; backs ajs_uptime_seconds
	handler      http.Handler

	// accountIOMu serializes export, import and bulk delete. TryLock turns a
	// concurrent bulk operation into 409 instead of queueing memory- and
	// CPU-heavy work behind it.
	accountIOMu sync.Mutex

	// webhookMu guards webhookSecrets, the in-memory fallback used when no
	// secret.Store is configured. With a sealer the map stays empty.
	webhookMu      sync.Mutex
	webhookSecrets map[string]string

	// rateLimiter enforces each scoped key's rateLimitPerMin. The admin token
	// never consults it.
	rateLimiter *ratelimit.Limiter

	// tenantLimiter enforces the portal buckets (IP, email, tenant, global).
	// It is a separate Limiter so portal traffic can never consume a scoped
	// key's budget.
	tenantLimiter *ratelimit.Limiter

	// trustedProxies are the peers whose X-Forwarded-For is believed.
	trustedProxies []netip.Prefix

	// backupMu serializes backup and restore: both read or replace the same
	// state-directory files. TryLock turns a concurrent request into 409
	// BACKUP_BUSY instead of queueing a multi-megabyte envelope behind it.
	backupMu sync.Mutex

	// restore is marked after a successful apply. It is the same instance the
	// daemon's snapshot writer reads through Config.Restore.
	restore *RestoreFlag
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
		cfg.Version = DefaultVersion
	}
	if cfg.Restore == nil {
		cfg.Restore = &RestoreFlag{}
	}
	origins := make(map[string]struct{}, len(cfg.CORSOrigins))
	for _, origin := range cfg.CORSOrigins {
		origin = strings.TrimSpace(origin)
		if origin != "" {
			origins[origin] = struct{}{}
		}
	}
	// An unset admin allowlist follows the CORS list so the loopback dashboard
	// keeps working in development. A deployment with a same-origin admin SPA
	// sets AJS_ADMIN_ORIGINS explicitly; a browser Origin that is not in this
	// map is refused even when CORS would allow it.
	adminOriginValues := cfg.AdminOrigins
	if len(adminOriginValues) == 0 {
		adminOriginValues = cfg.CORSOrigins
	}
	adminOrigins := make(map[string]struct{}, len(adminOriginValues))
	for _, raw := range adminOriginValues {
		origin, err := normalizeOrigin(raw)
		if err != nil {
			// New cannot return an error; main validates the env before calling
			// it. Fail closed here: an invalid entry is never allowed.
			logger.Error("admin_origin_invalid", "origin", raw, "error", err)
			continue
		}
		if origin != "" {
			adminOrigins[origin] = struct{}{}
		}
	}
	s := &Server{cfg: cfg, store: st, logger: logger, origins: origins, adminOrigins: adminOrigins, started: time.Now(), restore: cfg.Restore}
	if cfg.WebhookSecrets == nil {
		s.webhookSecrets = make(map[string]string)
	}
	s.rateLimiter = ratelimit.New(ratelimit.WithClock(cfg.RateLimitClock))
	s.tenantLimiter = ratelimit.New(ratelimit.WithClock(cfg.RateLimitClock))
	if normalized, err := NormalizeTenantOrigin(cfg.TenantOrigin); err != nil {
		// New cannot return an error; main validates the env before calling it.
		// Fail closed here so a hand-built Config cannot allow a malformed
		// origin.
		logger.Error("tenant_origin_invalid", "error", err)
		s.cfg.TenantOrigin = ""
	} else {
		s.cfg.TenantOrigin = normalized
	}
	trusted, err := ParseTrustedProxies(cfg.TrustedProxies)
	if err != nil {
		logger.Error("trusted_proxies_invalid", "error", err)
	}
	s.trustedProxies = trusted
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
	h = s.adminCSRF(h)
	h = s.auth(h)
	h = s.bodyLimit(h)
	h = s.cors(h)
	h = s.logRequests(h)
	return h
}

func (s *Server) routes(rt *router) {
	rt.handle(http.MethodGet, "/healthz", s.handleHealthz)
	rt.handle(http.MethodGet, "/metrics", s.handlePrometheusMetrics)
	rt.handle(http.MethodGet, "/v1/metrics", s.handleMetrics)

	rt.handle(http.MethodGet, "/v1/accounts", s.handleListAccounts)
	rt.handle(http.MethodPost, "/v1/accounts", s.handleCreateAccount)
	// Static account-IO routes must be registered before /v1/accounts/{id};
	// the router is first-match, so a later registration would be shadowed.
	rt.handle(http.MethodGet, "/v1/accounts/io/capabilities", s.handleAccountIOCapabilities)
	rt.handle(http.MethodPost, "/v1/accounts/export", s.handleExportAccounts)
	rt.handle(http.MethodPost, "/v1/accounts/import", s.handleImportAccounts)
	rt.handle(http.MethodPost, "/v1/accounts/bulk-delete", s.handleBulkDeleteAccounts)
	rt.handle(http.MethodGet, "/v1/accounts/{id}", s.handleGetAccount)
	rt.handle(http.MethodPatch, "/v1/accounts/{id}", s.handlePatchAccount)
	rt.handle(http.MethodDelete, "/v1/accounts/{id}", s.handleDeleteAccount)
	rt.handle(http.MethodPost, "/v1/accounts/{id}/actions/login", s.handleLoginAccount)
	rt.handle(http.MethodPost, "/v1/accounts/{id}/actions/logout", s.handleLogoutAccount)
	rt.handle(http.MethodGet, "/v1/accounts/{id}/servers", s.handleAccountServers)
	rt.handle(http.MethodGet, "/v1/accounts/{id}/device", s.handleAccountDevice)

	rt.handle(http.MethodGet, "/v1/device-pool", s.handleListDevicePool)
	rt.handle(http.MethodPost, "/v1/device-pool/generate", s.handleGenerateDevices)
	rt.handle(http.MethodPost, "/v1/device-pool/export", s.handleExportDevicePool)
	rt.handle(http.MethodPost, "/v1/device-pool/import", s.handleImportDevicePool)
	rt.handle(http.MethodGet, "/v1/device-pool/{id}", s.handleGetDevicePoolRecord)
	rt.handle(http.MethodPatch, "/v1/device-pool/{id}", s.handlePatchDevicePoolRecord)
	rt.handle(http.MethodPost, "/v1/device-pool/{id}/assign", s.handleAssignDevicePoolRecord)
	rt.handle(http.MethodPost, "/v1/device-pool/{id}/release", s.handleReleaseDevicePoolRecord)
	rt.handle(http.MethodPost, "/v1/device-pool/{id}/retire", s.handleRetireDevicePoolRecord)
	rt.handle(http.MethodPost, "/v1/device-pool/{id}/quarantine", s.handleQuarantineDevicePoolRecord)
	rt.handle(http.MethodPost, "/v1/device-pool/{id}/purge", s.handlePurgeDevicePoolRecord)

	rt.handle(http.MethodGet, "/v1/accounts/{id}/selection", s.handleGetSelection)
	rt.handle(http.MethodPut, "/v1/accounts/{id}/selection", s.handlePutSelection)
	rt.handle(http.MethodDelete, "/v1/accounts/{id}/selection", s.handleDeleteSelection)
	rt.handle(http.MethodPost, "/v1/accounts/{id}/actions/connect", s.handleConnectAccount)
	rt.handle(http.MethodPost, "/v1/accounts/{id}/actions/disconnect", s.handleDisconnectAccount)
	rt.handle(http.MethodPost, "/v1/accounts/{id}/actions/reconnect", s.handleReconnectAccount)
	// The async select-node executor reuses the same guarded switch as the
	// synchronous connect route; it never opens a second switch path.
	rt.handle(http.MethodPost, "/v1/accounts/{id}/actions/select-node", s.handleSelectNode)

	// Async operation records (DESIGN-INTEGRATION §3.4). List and get are
	// reads; cancel only stops a queued record before dispatch.
	rt.handle(http.MethodGet, "/v1/operations", s.handleListOperations)
	rt.handle(http.MethodGet, "/v1/operations/{id}", s.handleGetOperation)
	rt.handle(http.MethodDelete, "/v1/operations/{id}", s.handleCancelOperation)

	// Static /v1/schedules/preview must precede /v1/schedules/{id}: the
	// router is first-match and both patterns have three segments.
	rt.handle(http.MethodGet, "/v1/schedules", s.handleListSchedules)
	rt.handle(http.MethodPost, "/v1/schedules", s.handleCreateSchedule)
	rt.handle(http.MethodGet, "/v1/schedules/preview", s.handleSchedulePreview)
	rt.handle(http.MethodPost, "/v1/schedules/preview", s.handleSchedulePreview)
	rt.handle(http.MethodGet, "/v1/schedules/{id}", s.handleGetSchedule)
	rt.handle(http.MethodPatch, "/v1/schedules/{id}", s.handlePatchSchedule)
	rt.handle(http.MethodDelete, "/v1/schedules/{id}", s.handleDeleteSchedule)
	rt.handle(http.MethodPost, "/v1/schedules/{id}/actions/run", s.handleRunSchedule)
	rt.handle(http.MethodGet, "/v1/schedules/{id}/runs", s.handleScheduleRuns)

	rt.handle(http.MethodGet, "/v1/lab/overview", s.handleLabOverview)
	rt.handle(http.MethodGet, "/v1/lab/flags", s.handleLabFlags)
	rt.handle(http.MethodGet, "/v1/lab/gates", s.handleLabGates)
	rt.handle(http.MethodGet, "/v1/lab/flows", s.handleLabFlows)
	rt.handle(http.MethodGet, "/v1/lab/sessions", s.handleLabSessions)
	rt.handle(http.MethodGet, "/v1/lab/egress", s.handleLabEgress)
	rt.handle(http.MethodPost, "/v1/lab/egress-check", s.handleLabEgressCheck)
	rt.handle(http.MethodGet, "/v1/lab/profiles", s.handleLabProfiles)
	rt.handle(http.MethodGet, "/v1/lab/logs", s.handleLabLogs)
	rt.handle(http.MethodGet, "/v1/lab/diagnostics", s.handleLabDiagnostics)

	rt.handle(http.MethodGet, "/v1/nodes", s.handleListNodes)
	rt.handle(http.MethodPost, "/v1/nodes/{id}/actions/drain", s.handleDrainNode)
	rt.handle(http.MethodPost, "/v1/nodes/{id}/actions/undrain", s.handleUndrainNode)
	rt.handle(http.MethodPost, "/v1/nodes/{id}/actions/probe", s.handleProbeNode)

	rt.handle(http.MethodGet, "/v1/leases", s.handleListLeases)
	rt.handle(http.MethodPost, "/v1/leases", s.handleCreateLease)
	rt.handle(http.MethodPost, "/v1/leases/{id}/actions/rotate", s.handleRotateLease)
	rt.handle(http.MethodDelete, "/v1/leases/{id}", s.handleRevokeLease)

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

	rt.handle(http.MethodGet, "/v1/scoped-keys", s.handleListScopedKeys)
	rt.handle(http.MethodPost, "/v1/scoped-keys", s.handleCreateScopedKey)
	rt.handle(http.MethodGet, "/v1/scoped-keys/{id}", s.handleGetScopedKey)
	rt.handle(http.MethodDelete, "/v1/scoped-keys/{id}", s.handleDeleteScopedKey)

	rt.handle(http.MethodGet, "/v1/requests", s.handleListRequests)
	rt.handle(http.MethodPost, "/v1/requests/mutations", s.handleCreateMutation)
	rt.handle(http.MethodGet, "/v1/audit", s.handleAudit)
	rt.handle(http.MethodGet, "/v1/audit/export.csv", s.handleAuditExportCSV)

	rt.handle(http.MethodGet, "/v1/settings", s.handleGetSettings)
	rt.handle(http.MethodPatch, "/v1/settings", s.handlePatchSettings)
	rt.handle(http.MethodGet, "/v1/topology", s.handleTopologyView)
	rt.handle(http.MethodGet, "/v1/system/update", s.handleSystemUpdate)
	rt.handle(http.MethodGet, "/v1/openapi.json", s.handleOpenAPISpec)
	rt.handle(http.MethodPost, "/v1/reconcile", s.handleReconcile)

	// Platform routes (AJS-205). The renewal and usage routes are read-only
	// views; health reports the loopback sampler; webhooks manage the persisted
	// receivers and their delivery ring.
	rt.handle(http.MethodGet, "/v1/renewal/plans", s.handleRenewalPlans)
	rt.handle(http.MethodGet, "/v1/renewal/accounts/{id}", s.handleRenewalAccount)
	rt.handle(http.MethodGet, "/v1/usage", s.handleUsage)
	rt.handle(http.MethodGet, "/v1/health/nodes", s.handleHealthNodes)
	rt.handle(http.MethodGet, "/v1/webhooks", s.handleListWebhooks)
	rt.handle(http.MethodPost, "/v1/webhooks", s.handleCreateWebhook)
	rt.handle(http.MethodPatch, "/v1/webhooks/{id}", s.handlePatchWebhook)
	rt.handle(http.MethodDelete, "/v1/webhooks/{id}", s.handleDeleteWebhook)
	rt.handle(http.MethodGet, "/v1/webhooks/{id}/deliveries", s.handleWebhookDeliveries)

	// Event replay and streaming (AJS-232). The ring is filled by the same sink
	// that fans events out to webhooks, so the two views cannot drift.
	rt.handle(http.MethodGet, "/v1/events", s.handleListEvents)
	rt.handle(http.MethodGet, "/v1/events/stream", s.handleEventsStream)

	// Real-time debugger trace (AJS-373). The ring is in-memory, capped at
	// runtime.DebugRingCapacity (500) and requires catalog:read. It survives no
	// restart; the endpoints are diagnostics, not a durable audit.
	rt.handle(http.MethodGet, "/v1/debug/trace", s.handleDebugTrace)
	rt.handle(http.MethodGet, "/v1/debug/stream", s.handleDebugStream)

	// Panel backup routes. All three are admin-only: no scoped key may read or
	// replace the sealed panel files, so they stay absent from routeScopes.
	rt.handle(http.MethodPost, "/v1/backup", s.handleBackupCreate)
	rt.handle(http.MethodPost, "/v1/backup/inspect", s.handleBackupInspect)
	rt.handle(http.MethodPost, "/v1/backup/restore", s.handleBackupRestore)

	// Payment webhook (DESIGN-ORDERS §Provider boundary). It is registered even
	// when the customer portal is off because it is a provider callback, not a
	// customer route. It authenticates the provider signature over the raw
	// body, never a bearer token; a disabled adapter answers 503 and never
	// falls back to manual settlement.
	rt.handle(http.MethodPost, "/v1/payments/stripe", s.handleStripeWebhook)

	// 易支付 notify callback. Like the Stripe route it is a provider callback,
	// not a customer route: it is registered regardless of AJS_TENANT_PORTAL
	// and authenticates the MD5 signature over the query string/form body,
	// never a bearer. Standard 易支付 sends GET; some deployments POST.
	rt.handle(http.MethodGet, "/v1/payments/epay/notify", s.handleEpayNotify)
	rt.handle(http.MethodPost, "/v1/payments/epay/notify", s.handleEpayNotify)

	// Order administration (DESIGN-ORDERS). Plans and orders are operator
	// surfaces, not portal surfaces: they are registered even when
	// AJS_TENANT_PORTAL is off, so a deployment can price plans and audit
	// orders before the customer portal is enabled. They require the admin
	// token or an admin-scoped key (isAdminTenantPath) and stay absent from
	// routeScopes.
	rt.handle(http.MethodGet, "/v1/admin/plans", s.handleListPlans)
	rt.handle(http.MethodPost, "/v1/admin/plans", s.handleCreatePlan)
	rt.handle(http.MethodGet, "/v1/admin/plans/{id}", s.handleGetPlan)
	rt.handle(http.MethodPatch, "/v1/admin/plans/{id}", s.handlePatchPlan)
	rt.handle(http.MethodDelete, "/v1/admin/plans/{id}", s.handleDeletePlan)
	rt.handle(http.MethodGet, "/v1/admin/orders", s.handleListOrders)
	rt.handle(http.MethodGet, "/v1/admin/orders/{id}", s.handleGetOrder)
	rt.handle(http.MethodPost, "/v1/admin/orders/{id}/settle", s.handleSettleOrder)
	rt.handle(http.MethodPost, "/v1/admin/orders/{id}/refund", s.handleRefundOrder)
	rt.handle(http.MethodPost, "/v1/admin/orders/{id}/chargeback", s.handleChargebackOrder)
	rt.handle(http.MethodPost, "/v1/admin/orders/{id}/cancel", s.handleAdminCancelOrder)
	s.registerOrderReissueRoutes(rt)

	// Customer portal chains (DESIGN-TENANT). Both are opt-in: without
	// AJS_TENANT_PORTAL=1 the routes are not registered and answer 404, and
	// no identity is synthesized for them. The portal chain authenticates only
	// its own cookie; the tenant-admin chain requires the admin token or the
	// admin scope and is absent from routeScopes.
	if s.cfg.TenantPortal {
		rt.handle(http.MethodPost, "/v1/portal/signup", s.handlePortalSignup)
		rt.handle(http.MethodPost, "/v1/portal/login", s.handlePortalLogin)
		rt.handle(http.MethodGet, "/v1/portal/session", s.handlePortalSession)
		rt.handle(http.MethodPost, "/v1/portal/logout", s.handlePortalLogout)
		rt.handle(http.MethodGet, "/v1/portal/leases", s.handlePortalLeases)
		rt.handle(http.MethodGet, "/v1/portal/usage", s.handlePortalUsage)
		rt.handle(http.MethodGet, "/v1/portal/audit", s.handlePortalAudit)
		rt.handle(http.MethodGet, "/v1/portal/keys", s.handlePortalListKeys)
		rt.handle(http.MethodPost, "/v1/portal/keys", s.handlePortalCreateKey)
		rt.handle(http.MethodDelete, "/v1/portal/keys/{id}", s.handlePortalRevokeKey)
		rt.handle(http.MethodGet, "/v1/portal/plans", s.handlePortalPlans)
		rt.handle(http.MethodGet, "/v1/portal/orders", s.handlePortalListOrders)
		rt.handle(http.MethodPost, "/v1/portal/orders", s.handlePortalCreateOrder)
		rt.handle(http.MethodGet, "/v1/portal/orders/{id}", s.handlePortalGetOrder)
		rt.handle(http.MethodPost, "/v1/portal/orders/{id}/checkout", s.handlePortalCheckoutOrder)
		rt.handle(http.MethodPost, "/v1/portal/orders/{id}/cancel", s.handlePortalCancelOrder)

		rt.handle(http.MethodGet, "/v1/admin/tenants", s.handleListTenants)
		rt.handle(http.MethodPost, "/v1/admin/tenants/{id}/approve", s.handleApproveTenant)
		rt.handle(http.MethodPost, "/v1/admin/tenants/{id}/suspend", s.handleSuspendTenant)
		rt.handle(http.MethodPost, "/v1/admin/tenants/{id}/resume", s.handleResumeTenant)
	}
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
				w.Header().Set("Access-Control-Expose-Headers",
					"X-Request-Id, Retry-After, X-RateLimit-Limit, X-RateLimit-Remaining, X-RateLimit-Reset, Content-Disposition, X-AJS-Export-Count, X-AJS-Export-Missing-Secrets, X-AJS-Export-Devices, X-AJS-Backup-Blobs, X-Truncated, ETag, X-APC-Server-Time")
			}
		}
		if r.Method == http.MethodOptions {
			if allowed {
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Accept, If-Match, Idempotency-Key, X-AJS-CSRF")
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
			limit := s.cfg.MaxBodyBytes
			// Base64 and JSON overhead can inflate a 1 MiB decoded document,
			// so the import routes get their own cap.
			switch r.URL.Path {
			case "/v1/accounts/import":
				limit = store.MaxImportBodyBytes
			case "/v1/device-pool/import":
				limit = store.DeviceImportMaxBytes
			case "/v1/backup/inspect", "/v1/backup/restore":
				// Both carry a whole envelope, so the default 1 MiB cap would
				// refuse every real backup. The envelope itself is capped at
				// backup.MaxEnvelopeBytes inside the handler.
				limit = maxBackupBodyBytes
			}
			if strings.HasPrefix(r.URL.Path, "/v1/portal/") {
				// Portal bodies are a few short fields; a large body is never
				// legitimate and would only feed the Argon2 path.
				limit = 64 << 10
			}
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		next.ServeHTTP(w, r)
	})
}

// auth protects the dashboard API and the Prometheus /metrics endpoint. The
// admin bearer token is tried first in constant time; failing that, an
// ajs_sk_ scoped key is resolved by its SHA-256. Scoped keys then pass the
// account restriction check and the router enforces the per-route scope.
// /healthz stays open, and AllowInsecure disables the check entirely.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Payment webhooks are authenticated by their provider signature over
		// the raw body, not by a bearer token: a payment provider cannot send
		// one. The handlers verify the signature and answer 503 while their
		// adapter is disabled.
		if r.URL.Path == stripeWebhookPath || r.URL.Path == epayNotifyPath {
			next.ServeHTTP(w, r)
			return
		}
		// The portal chain is its own authentication world: a cookie session
		// is the only identity, and neither the admin token nor a scoped key
		// can stand in for one. When the feature is off the routes are absent,
		// so the request falls through to the router's 404.
		if isPortalPath(r.URL.Path) {
			if !s.cfg.TenantPortal {
				next.ServeHTTP(w, r)
				return
			}
			s.servePortalAuth(w, r, next)
			return
		}
		adminTenant := isAdminTenantPath(r.URL.Path)
		if !strings.HasPrefix(r.URL.Path, "/v1/") && r.URL.Path != "/metrics" {
			next.ServeHTTP(w, r)
			return
		}
		// AJS_ALLOW_INSECURE keeps its legacy effect on the dashboard routes,
		// but tenant administration is never anonymous: it needs the admin
		// token or a scoped admin principal even in insecure mode.
		if s.cfg.AllowInsecure && !adminTenant {
			next.ServeHTTP(w, r)
			return
		}
		const prefix = "Bearer "
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, prefix) {
			if adminTenant && portalCookiePresent(r) {
				writeError(w, http.StatusForbidden, CodeForbidden,
					"a portal session cannot administer this route")
				return
			}
			writeError(w, http.StatusUnauthorized, CodeUnauthorized, "missing or invalid bearer token")
			return
		}
		token := strings.TrimPrefix(header, prefix)
		if s.cfg.AdminToken != "" &&
			subtle.ConstantTimeCompare([]byte(token), []byte(s.cfg.AdminToken)) == 1 {
			next.ServeHTTP(w, withAuthIdentity(r, authIdentity{admin: true}))
			return
		}
		key, ok := s.store.AuthenticateScopedKey(token)
		if !ok {
			if adminTenant && portalCookiePresent(r) {
				writeError(w, http.StatusForbidden, CodeForbidden,
					"a portal session cannot administer this route")
				return
			}
			writeError(w, http.StatusUnauthorized, CodeUnauthorized, "missing or invalid bearer token")
			return
		}
		if adminTenant && !hasScope(key.Scopes, model.ScopeAdmin) {
			writeError(w, http.StatusForbidden, CodeForbidden,
				"tenant administration requires the admin scope")
			return
		}
		// The limit is charged before the account restriction check so a
		// refused read still carries the X-RateLimit headers and a key cannot
		// probe account ids for free.
		if !s.allowScopedKeyRequest(w, key) {
			return
		}
		if s.scopedKeyDeniesRequest(key, r) {
			writeError(w, http.StatusForbidden, CodeForbidden,
				"scoped key is not allowed to read this account")
			return
		}
		s.store.MarkScopedKeyUsed(key.ID)
		next.ServeHTTP(w, withAuthIdentity(r, authIdentity{key: key}))
	})
}

// allowScopedKeyRequest charges one request against the key's per-minute
// bucket and writes the X-RateLimit headers on every scoped-key response. A
// refused request gets 429 RATE_LIMITED plus Retry-After, set to one refill
// interval so a retry at that point is guaranteed to find a token. limit 0 is
// unlimited and still reports the headers.
func (s *Server) allowScopedKeyRequest(w http.ResponseWriter, key model.ScopedKey) bool {
	limit := key.RateLimitPerMin
	if limit < 0 {
		// Store validation rejects negatives; a snapshot written by hand may
		// still carry one, so treat it as unlimited rather than fail closed.
		limit = 0
	}
	ok, remaining, reset := s.rateLimiter.Allow(key.ID, limit)
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
	writeError(w, http.StatusTooManyRequests, CodeRateLimited, "scoped key rate limit exceeded")
	return false
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
		if identity, ok := authIdentityFrom(r); ok && !identity.allowsRoute(candidate.method, candidate.pattern) {
			writeError(w, http.StatusForbidden, CodeForbidden,
				"scoped key lacks the scope required by this route")
			return
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
	// Reason is the stable machine-readable reason of the catalog contract
	// (DESIGN-CATALOG §3.10). Existing clients ignore it.
	Reason string `json:"reason,omitempty"`
	// Details carries small, non-secret context such as the current
	// snapshotId or ineligibleReason.
	Details map[string]string `json:"details,omitempty"`
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
	case errors.Is(err, store.ErrQuota):
		writeError(w, http.StatusTooManyRequests, CodeQuotaExceeded, err.Error())
	case errors.Is(err, store.ErrUnauthorized):
		writeError(w, http.StatusUnauthorized, CodeUnauthorized, err.Error())
	case errors.Is(err, store.ErrForbidden):
		writeError(w, http.StatusForbidden, CodeForbidden, err.Error())
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
			writeError(w, http.StatusRequestEntityTooLarge, CodeInvalidArgument, "request body exceeds the route limit")
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
	return paginateWithLimit(w, r, total, 0)
}

// paginateWithLimit is paginate with a default page size for requests that
// carry no ?limit. An explicit ?limit= still wins, and ?limit=0 keeps meaning
// "every row".
func paginateWithLimit(w http.ResponseWriter, r *http.Request, total, defaultLimit int) (int, int, bool) {
	limit, offset := defaultLimit, 0
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
