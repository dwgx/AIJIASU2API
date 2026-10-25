// Command ajs-accountd serves the panel HTTP API from an in-memory store.
package main

import (
	"context"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	_ "time/tzdata"

	"ajsaccountd/internal/account"
	"ajsaccountd/internal/api"
	eventring "ajsaccountd/internal/events"
	"ajsaccountd/internal/gate"
	"ajsaccountd/internal/health"
	"ajsaccountd/internal/lablog"
	"ajsaccountd/internal/model"
	"ajsaccountd/internal/publisher"
	"ajsaccountd/internal/renewal"
	"ajsaccountd/internal/resin"
	"ajsaccountd/internal/runtime"
	"ajsaccountd/internal/schedule"
	"ajsaccountd/internal/secret"
	"ajsaccountd/internal/store"
	"ajsaccountd/internal/usage"
	"ajsaccountd/internal/webhook"
	"ajsproxy/fvfacade"
)

func main() {
	if err := run(); err != nil {
		slog.Error("ajs-accountd failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	started := time.Now()
	listen := flag.String("listen", envOr("AJS_LISTEN", "127.0.0.1:2261"), "listen address")
	token := flag.String("token", os.Getenv("AJS_ADMIN_TOKEN"), "admin bearer token (env AJS_ADMIN_TOKEN)")
	insecure := flag.Bool("insecure", os.Getenv("AJS_ALLOW_INSECURE") == "1", "disable bearer auth (env AJS_ALLOW_INSECURE=1)")
	corsOrigins := flag.String("cors-origins", envOr("AJS_CORS_ORIGINS", strings.Join(api.DefaultCORSOrigins, ",")), "comma-separated allowed CORS origins")
	logLevel := flag.String("log-level", envOr("AJS_LOG_LEVEL", "info"), "debug, info, warn or error")
	resinURL := flag.String("resin-url", envOr(resin.EnvBaseURL, resin.DefaultBaseURL), "Resin admin API base URL (env AJS_RESIN_URL)")
	resinToken := flag.String("resin-token", os.Getenv(resin.EnvAdminToken), "Resin admin bearer token (env AJS_RESIN_ADMIN_TOKEN); empty disables the integration")
	subscriptionName := flag.String("subscription-name", envOr("AJS_SUBSCRIPTION_NAME", runtime.DefaultSubscriptionName), "Resin local subscription name (env AJS_SUBSCRIPTION_NAME)")
	platformName := flag.String("platform-name", os.Getenv("AJS_PLATFORM_NAME"), "Resin platform name (env AJS_PLATFORM_NAME; default: the subscription name)")
	updateRepo := flag.String("update-repo", os.Getenv("AJS_UPDATE_REPO"), "GitHub owner/repo for the update check (env AJS_UPDATE_REPO); empty disables the check")
	updateAPIBase := flag.String("update-api-base", envOr("AJS_UPDATE_API_BASE", api.DefaultUpdateAPIBase), "GitHub API base URL for the update check (env AJS_UPDATE_API_BASE)")
	tenantPortal := flag.Bool("tenant-portal", os.Getenv("AJS_TENANT_PORTAL") == "1", "enable the customer tenant portal (env AJS_TENANT_PORTAL=1)")
	tenantOrigin := flag.String("tenant-origin", os.Getenv("AJS_TENANT_ORIGIN"), "exact browser origin allowed for portal writes (env AJS_TENANT_ORIGIN)")
	trustedProxies := flag.String("trusted-proxies", os.Getenv("AJS_TRUSTED_PROXIES"), "comma-separated proxy IPs/CIDRs whose X-Forwarded-For is trusted (env AJS_TRUSTED_PROXIES)")
	flag.Parse()

	// The HTTPS admin listener is opt-in (AJS-276): AJS_TLS_CERT and
	// AJS_TLS_KEY together enable it on AJS_TLS_LISTEN (default
	// 127.0.0.1:2262). A partial configuration aborts here, before any state
	// is opened, instead of silently serving a different transport.
	tlsCfg, err := api.ParseTLSConfig(os.Getenv)
	if err != nil {
		return err
	}

	// S4: AJS_ALLOW_INSECURE=1 must stay on loopback. Without this guard a
	// single env var turns the loopback control plane into an unauthenticated
	// network service (state-changing POSTs are CSRF-able there).
	if *insecure {
		if err := requireLoopback(*listen); err != nil {
			return err
		}
	}
	if err := validateTLSListener(*listen, tlsCfg, *insecure); err != nil {
		return err
	}
	// S2: a placeholder token is a working admin API whose value is printed in
	// the repository — including the one .env.example ships — so startup
	// refuses those values. The insecure lab mode is the explicit opt-out.
	if err := validateAdminToken(*token, *insecure); err != nil {
		return err
	}
	// AJS_TENANT_PORTAL is the opt-in. Enabling it without an exact browser
	// origin would leave every signup/login/mutation unchecked, so that
	// partial configuration aborts before the listener starts. A configured
	// origin without the flag is ignored with a warning (see below).
	tenantOriginValue, trustedProxyValues, err := tenantPortalConfig(*tenantPortal, *tenantOrigin, *trustedProxies)
	if err != nil {
		return err
	}

	// AJS_GATE_AUTH selects the SOCKS5 gate authentication method. An unknown
	// value aborts startup rather than silently serving a weaker listener; an
	// unset value keeps the loopback compatibility mode for the rolling
	// release (DESIGN-GATE-IDENTITY §5).
	gateAuth, err := gate.ParseAuthMode(os.Getenv("AJS_GATE_AUTH"))
	if err != nil {
		return err
	}
	// AJS_GATE_HTTP=1 enables the HTTP CONNECT listener that runs beside every
	// SOCKS5 gate. It is opt-in: an upgrade must not start binding a second
	// port pair per node, and the listener shares the same admission pipeline,
	// so it adds no policy the gate does not already enforce.
	// AJS_GATE_HTTP_OFFSET places it relative to the gate port (default 1000).
	gateHTTP, err := gateHTTPEnabled(os.Getenv("AJS_GATE_HTTP"))
	if err != nil {
		return err
	}
	gateHTTPOffset, err := gateHTTPOffset(os.Getenv("AJS_GATE_HTTP_OFFSET"))
	if err != nil {
		return err
	}

	// The Lab ring is a bounded, redacted copy of the same log stream. It is
	// teed in front of the console handler so AJS_LOG_LEVEL still controls
	// stdout while /v1/lab/logs can show recent lines on demand.
	logRing := lablog.NewRing(lablog.DefaultCapacity)
	logger := slog.New(logRing.Handler(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: parseLevel(*logLevel)})))
	slog.SetDefault(logger)
	st, secrets, deviceSecrets, err := openStore(logger)
	if err != nil {
		return err
	}
	if secrets != nil {
		defer func() { _ = secrets.Close() }()
	}
	if deviceSecrets != nil {
		defer func() { _ = deviceSecrets.Close() }()
	}

	// The snapshot is the authoritative full state. It is loaded before the
	// listener starts so a corrupt or future-version file fails startup
	// instead of serving a half-restored panel.
	statePath := stateFilePath()
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		return fmt.Errorf("state directory %s: %w", filepath.Dir(statePath), err)
	}
	if err := st.Load(statePath); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("load state: %w", err)
		}
		logger.Info("state_missing", "path", statePath)
	} else {
		logger.Info("state_loaded", "path", statePath,
			"accounts", len(st.Accounts()),
			"nodes", len(st.Nodes()),
			"leases", len(st.Leases()))
	}

	// DESIGN-DEVICE-POOL §5.3: a snapshot that still carries the plaintext
	// device map is converted to sealed pool records once, before any request
	// can observe it. Restore already migrates when the sealer is wired, so
	// this is idempotent; a failure is logged and the panel still starts,
	// because the affected accounts fail closed at login rather than
	// regenerating an identity.
	migrateDevicesAtStartup(st, logger)

	// AJS_DROP_DEMO is the one-shot migration off the dashboard mock: remove
	// exactly the seed rows from the loaded snapshot, log counts only, and
	// persist the result through the normal atomic snapshot path. It runs
	// before the listener and before the snapshot ticker so no request can
	// observe the half-purged state.
	if os.Getenv("AJS_DROP_DEMO") == "1" {
		purged, err := st.PurgeDemo()
		if err != nil {
			return fmt.Errorf("purge demo data: %w", err)
		}
		logger.Info("demo_purged",
			"accounts", purged.Accounts,
			"nodes", purged.Nodes,
			"leases", purged.Leases,
			"users", purged.Users,
			"keys", purged.Keys,
			"requests", purged.Requests,
			"audit", purged.Audit)
		saveState(st, statePath, logger, "demo-purge")
	}
	pruneSecrets(secrets, st, logger)

	// A gate is a process resource, so every node the snapshot still calls
	// LIVE or DRAINING is stale until a login sync reopens it. Demote before
	// the panel's startup reconcile (which renders LIVE rows) and before the
	// listener, so neither Resin nor the console can observe the old claim.
	recovered, err := runtime.RecoverOnStartup(st, nil, logger)
	if err != nil {
		// The rows are already demoted; a per-node failure is reported and the
		// daemon still starts, because refusing to serve is worse than a row
		// that stays LIVE until the next login sync.
		logger.Error("startup_recovery_failed", "error", err)
	}
	if len(recovered.Demoted) > 0 {
		saveState(st, statePath, logger, "startup-recovery")
	}

	snapshotEvery, err := snapshotInterval()
	if err != nil {
		return err
	}
	stopSnapshots := make(chan struct{})
	snapshotsDone := make(chan struct{})
	go func() {
		defer close(snapshotsDone)
		ticker := time.NewTicker(snapshotEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				saveState(st, statePath, logger, "interval")
			case <-stopSnapshots:
				return
			}
		}
	}()
	// Runs after the HTTP server has stopped (and before secrets.Close, which
	// was deferred earlier) so the last mutations reach disk.
	defer func() {
		close(stopSnapshots)
		<-snapshotsDone
		saveState(st, statePath, logger, "shutdown")
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Platform services (AJS-205). The renewal catalogue is anonymous and
	// read-only; usage is a pure aggregator over the request log; the health
	// sampler probes only loopback gate ports; the webhook dispatcher fans
	// customer events out to the persisted receivers.
	renewalService := renewal.New(renewal.Config{})
	// RetentionRows mirrors the store's request-log cap so the summary can
	// label limited history honestly (rowsRetained/retentionDays).
	usageAggregator := usage.New(usage.Config{RetentionRows: store.RequestRetention})

	var healthSampler *health.Sampler
	var egressSampler *health.EgressSampler
	var healthView api.HealthMonitor
	var healthWatches *healthTracker
	if os.Getenv("AJS_HEALTH_PROBE") == "0" {
		logger.Info("health_probe_disabled", "reason", "AJS_HEALTH_PROBE=0")
		if egressProbeEnabled() {
			// Egress follows the health sampler's watch set: with no loopback
			// sampler there is no live-gate signal to attach to.
			logger.Warn("egress_probe_ignored", "reason", "AJS_HEALTH_PROBE=0")
		}
	} else {
		maxNodes, err := healthMaxNodes()
		if err != nil {
			return err
		}
		healthSampler = health.New(health.Config{MaxNodes: maxNodes})
		healthSampler.Start(ctx)
		// Registered before the controller's stop defer, so probes stop after
		// the gates they sample are closed.
		defer healthSampler.Stop()
		if egressProbeEnabled() {
			if gateAuth == gate.AuthUserPass {
				// The probe speaks the gate's no-auth SOCKS5 mode. In
				// userpass mode the gate refuses that greeting, so every
				// sample records a proxy failure rather than a silent pass.
				logger.Warn("egress_probe_unauthenticated",
					"reason", "AJS_GATE_AUTH=userpass refuses the no-auth probe; egress samples will record proxy failures")
			}
			egressCfg, err := egressConfig()
			if err != nil {
				return err
			}
			egressSampler, err = health.NewEgressSampler(egressCfg)
			if err != nil {
				return fmt.Errorf("AJS_EGRESS_TARGET: %w", err)
			}
			egressSampler.Start(ctx)
			// Same ordering as the loopback sampler: the gates close first.
			defer egressSampler.Stop()
			logger.Info("egress_probe_enabled",
				"interval", egressCfg.Interval.String(),
				"timeout", egressCfg.Timeout.String(),
				"target", egressLogTarget(egressCfg.Target))
		}
		healthWatches = newHealthTracker(healthSampler, egressSampler)
		healthView = healthWatches
		logger.Info("health_probe_enabled",
			"interval", health.DefaultInterval.String(),
			"timeout", health.DefaultTimeout.String(),
			"max_nodes", maxNodes)
	}

	var webhookSecrets api.WebhookSecretStore
	if secrets != nil {
		webhookSecrets = sealedWebhookSecrets{store: secrets}
	} else {
		logger.Warn("webhook_secrets_memory_only",
			"reason", "AJS_MASTER_KEY is not set; endpoint secrets do not survive a restart")
	}
	webhookDispatcher, webhookEvents := startWebhooks(st, logger, webhookSecrets)
	defer func() { _ = webhookDispatcher.Close() }()
	// One ring behind GET /v1/events and the SSE stream. Every event the
	// webhook sink receives is mirrored into it first, so the replay and the
	// outbound deliveries are the same stream and cannot drift.
	eventRing := eventring.NewRing(api.DefaultEventRingCapacity)
	defer eventRing.Close()
	events := &ringSink{ring: eventRing, next: webhookEvents}
	// Every critical audit event (webhook path excluded) reaches subscribed
	// receivers. The dispatcher fans it out; a nil receiver set is a no-op.
	st.SetAuditHook(func(ev model.AuditEvent) {
		if strings.HasPrefix(ev.Action, "webhook.") { // never re-emit the webhook path's own audit
			return
		}
		events.Emit(webhook.EventAuditCritical, map[string]any{
			"auditId": ev.ID,
			"action":  ev.Action,
			"actor":   ev.Actor,
			"target":  ev.Target,
		})
	})

	panel, err := startResinPanel(ctx, st, logger, resinOptions{
		url:              *resinURL,
		token:            *resinToken,
		subscriptionName: *subscriptionName,
		platformName:     *platformName,
	})
	if err != nil {
		return err
	}
	if resinPanel, ok := panel.(*runtime.ResinPanel); ok {
		defer func() { _ = resinPanel.Close() }()
	}
	// AJS_RESIN_RECONCILE=1 keeps the Resin view converged: the loop hashes
	// the desired LIVE nodes, leases and accounts and publishes only when the
	// hash changes. The stop func is registered after the panel's Close, so
	// LIFO runs it first and no reconcile can race the publisher's shutdown.
	stopReconcile, err := startResinReconciler(ctx, st, panel, logger)
	if err != nil {
		return err
	}
	if stopReconcile != nil {
		defer stopReconcile()
	}

	var loginService api.LoginService
	var loginResults runtime.LoginResults
	var loginHook func(context.Context, string) error
	var accountDeleteHook func(context.Context, string) error
	var accountLogoutHook func(context.Context, string) error
	var nodeDrainHook func(context.Context, string, bool) error
	var catalogConnector api.CatalogConnector
	var controller *runtime.NodeController
	var profile account.VersionProfile
	nodeLimitValue := 1
	service, profile, err := startAccountLogin(st, logger)
	if err != nil {
		return err
	}
	if service != nil {
		defer service.Close()
		loginService = service
		loginResults = service
		limit, err := nodeLimit()
		if err != nil {
			return err
		}
		nodeLimitValue = limit
		controller, err = startNodeController(st, logger, panel, service, profile, limit, gateAuth, gateHTTP, gateHTTPOffset)
		if err != nil {
			return err
		}
		catalogConnector = controller
		// Registered after the snapshot defer, so gates close and the final
		// node state is written before the last state file is saved.
		defer func() {
			stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := controller.Stop(stopCtx); err != nil {
				logger.Warn("node_controller_stop_failed", "error", err)
			}
		}()
		loginHook = controller.SyncAccount
		accountDeleteHook = controller.StopAccount
		accountLogoutHook = controller.StopAccount
		nodeDrainHook = func(ctx context.Context, nodeID string, drain bool) error {
			if drain {
				return controller.DrainNode(ctx, nodeID)
			}
			return controller.UndrainNode(ctx, nodeID)
		}
		if healthWatches != nil {
			go runHealthReconciler(ctx, controller, healthWatches, logger)
		}
		if autoLoginEnabled(os.Getenv("AJS_AUTO_LOGIN")) {
			// Off by default. The runner is serialized and makes at most one
			// attempt per account, so it never turns startup into a retry
			// loop; it runs in the background so a slow vendor cannot delay
			// the listener.
			autoLogin, err := runtime.NewAutoLogin(runtime.AutoLoginConfig{
				Store:  st,
				Logins: service,
				Sync:   controller.SyncAccount,
				Events: events,
				Logger: logger,
			})
			if err != nil {
				return err
			}
			go func() { autoLogin.Run(ctx) }()
		}
	} else if autoLoginEnabled(os.Getenv("AJS_AUTO_LOGIN")) {
		logger.Warn("auto_login_disabled", "reason", "AJS_AUTO_LOGIN=1 requires AJS_ACCOUNT_LOGIN=1")
	}

	scheduleService, stopScheduler, err := startScheduler(ctx, st, logger, statePath, controller, nodeLimitValue, events)
	if err != nil {
		return err
	}
	if stopScheduler != nil {
		defer stopScheduler()
	}

	profileFlag := envOr("AJS_ACCOUNT_PROFILE", "linux-4.2.3.0")
	nodeLimitFlag := envOr("AJS_NODE_LIMIT", "1")
	if service != nil {
		nodeLimitFlag = strconv.Itoa(nodeLimitValue)
	}
	labView := runtime.NewLabView(runtime.LabConfig{
		Version:      api.DefaultVersion,
		Commit:       buildCommit(),
		Started:      started,
		Store:        st,
		Logins:       loginResults,
		Controller:   controller,
		LogRing:      logRing,
		Profile:      profile,
		NodeLimit:    nodeLimitValue,
		AccountLogin: service != nil,
		Resin:        panel != nil,
		UpdateCheck:  *updateRepo != "",
		Secrets:      secrets != nil,
		CertPin:      pinState(),
		StatePath:    statePath,
		Flags: runtime.Flags(runtime.FlagValues{
			Listen:            *listen,
			LogLevel:          *logLevel,
			AccountProfile:    profileFlag,
			NodeLimit:         nodeLimitFlag,
			SnapshotInterval:  snapshotEvery.String(),
			ControlCertSHA256: strings.TrimSpace(os.Getenv("AJS_CONTROL_CERT_SHA256")),
			UpdateRepo:        *updateRepo,
			ResinURL:          *resinURL,
			SubscriptionName:  *subscriptionName,
			AdminToken:        *token,
			MasterKey:         os.Getenv("AJS_MASTER_KEY"),
			ResinAdminToken:   *resinToken,
			AllowInsecure:     *insecure,
			AccountLogin:      service != nil,
			AllowPlaintext:    os.Getenv("AJS_ALLOW_PLAINTEXT") == "1",
		}),
	})

	if os.Getenv("AJS_BACKUP_ALLOW_RESTORE") == "1" {
		logger.Warn("backup_restore_enabled",
			"reason", "AJS_BACKUP_ALLOW_RESTORE=1: POST /v1/backup/restore may replace the panel files while the daemon runs")
	}
	if *tenantPortal {
		if !strings.HasPrefix(tenantOriginValue, "https://") {
			logger.Warn("tenant_portal_insecure_origin",
				"origin", tenantOriginValue,
				"reason", "the portal cookie is Secure and SameSite=Strict; browsers only send it over HTTPS")
		}
		if *insecure {
			logger.Warn("tenant_portal_insecure_mode",
				"reason", "AJS_ALLOW_INSECURE=1 does not bypass the portal session or tenant-admin checks")
		}
	} else if tenantOriginValue != "" {
		logger.Warn("tenant_origin_ignored",
			"reason", "AJS_TENANT_ORIGIN is set but AJS_TENANT_PORTAL is not 1")
	}
	server := api.New(api.Config{
		AdminToken:           *token,
		AllowInsecure:        *insecure,
		CORSOrigins:          splitList(*corsOrigins),
		Version:              api.DefaultVersion,
		StateDir:             envOr("AJS_STATE_DIR", "./data"),
		StatePath:            statePath,
		AllowBackupRestore:   os.Getenv("AJS_BACKUP_ALLOW_RESTORE") == "1",
		TenantPortal:         *tenantPortal,
		TenantOrigin:         tenantOriginValue,
		TrustedProxies:       trustedProxyValues,
		Panel:                panel,
		LoginService:         loginService,
		LoginHook:            loginHook,
		AccountDeleteHook:    accountDeleteHook,
		AccountLogoutHook:    accountLogoutHook,
		NodeDrainHook:        nodeDrainHook,
		UpdateRepo:           *updateRepo,
		UpdateAPIBase:        *updateAPIBase,
		Lab:                  labView,
		Catalog:              catalogConnector,
		Schedules:            scheduleService,
		Renewal:              renewalService,
		Usage:                usageAggregator,
		Health:               healthView,
		Webhooks:             webhookDispatcher,
		WebhookSecrets:       webhookSecrets,
		Events:               events,
		EventRing:            eventRing,
		AllowPlaintextExport: os.Getenv("AJS_ALLOW_PLAINTEXT_EXPORT") == "1",
		Persist: func() {
			saveState(st, statePath, logger, "account-io")
		},
	}, st, logger)

	httpServer := &http.Server{
		Addr:              *listen,
		Handler:           server,
		ReadHeaderTimeout: 5 * time.Second,
		// ReadTimeout bounds a trickled request body (MaxBytesReader bounds
		// bytes, not time). WriteTimeout sits above the worst-case handler: a
		// reconcile is ~30s of Resin calls plus refresh backoff.
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	// An SSE handler only returns when its subscriber channel closes, and
	// Shutdown waits for active handlers, so close the ring first to release
	// every open stream instead of burning the 5 s shutdown budget.
	httpServer.RegisterOnShutdown(eventRing.Close)

	// The HTTPS listener is a second socket over the same handler, built here
	// so an unreadable or mismatched certificate pair fails startup before
	// either listener accepts a connection. It copies the plaintext server's
	// timeouts and leaves that server untouched, so loopback HTTP keeps
	// working beside it.
	var tlsServer *http.Server
	if tlsCfg.Enabled() {
		tlsServer, err = api.NewTLSServer(httpServer, tlsCfg)
		if err != nil {
			return err
		}
		tlsServer.RegisterOnShutdown(eventRing.Close)
	}

	errCh := make(chan error, 2)
	go func() { errCh <- httpServer.ListenAndServe() }()
	if tlsServer != nil {
		go func() { errCh <- tlsServer.ListenAndServeTLS("", "") }()
	}
	authMode := "bearer"
	if *insecure {
		authMode = "insecure"
	}
	logger.Info("ajs-accountd listening", "addr", *listen, "auth", authMode)
	if tlsServer != nil {
		// Address only: the certificate and key paths stay out of the log
		// along with their contents.
		logger.Info("tls_listener_started", "addr", tlsCfg.Listen, "min_version", "1.2")
	} else {
		logger.Info("tls_disabled", "reason", "AJS_TLS_CERT and AJS_TLS_KEY are not set")
	}

	select {
	case <-ctx.Done():
		logger.Info("ajs-accountd shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown: %w", err)
		}
		if tlsServer != nil {
			if err := tlsServer.Shutdown(shutdownCtx); err != nil {
				return fmt.Errorf("shutdown tls listener: %w", err)
			}
		}
		return nil
	case err := <-errCh:
		// One listener failing must not leave the daemon half-served: close
		// the sibling so the process exits instead of answering on one port.
		_ = httpServer.Close()
		if tlsServer != nil {
			_ = tlsServer.Close()
		}
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// startScheduler constructs and starts the schedule engine when
// AJS_SCHEDULES=1. The default is off, matching DESIGN-SCHEDULE §10.2: the
// read routes and the pure preview still work, but no goroutine runs and no
// mutation is accepted. A nil service means disabled.
func startScheduler(ctx context.Context, st *store.Store, logger *slog.Logger, statePath string, controller *runtime.NodeController, nodeLimit int, events api.EventSink) (api.ScheduleService, func(), error) {
	if os.Getenv("AJS_SCHEDULES") != "1" {
		logger.Info("scheduler_disabled", "reason", "AJS_SCHEDULES is not 1")
		return nil, nil, nil
	}
	tick := time.Second
	if raw := strings.TrimSpace(os.Getenv("AJS_SCHEDULER_TICK")); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			if seconds, intErr := strconv.Atoi(raw); intErr == nil {
				parsed, err = time.Duration(seconds)*time.Second, nil
			}
		}
		if err != nil || parsed < time.Second || parsed > 60*time.Second {
			return nil, nil, fmt.Errorf("AJS_SCHEDULER_TICK %q must be 1s..60s", raw)
		}
		tick = parsed
	}
	workers := 4
	if raw := strings.TrimSpace(os.Getenv("AJS_SCHEDULES_MAX_CONCURRENT")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 16 {
			return nil, nil, fmt.Errorf("AJS_SCHEDULES_MAX_CONCURRENT %q must be 1..16", raw)
		}
		workers = parsed
	}
	renewalEnabled := os.Getenv("AJS_RENEWAL_REMINDERS") != "0"
	renewalHour := 9
	if raw := strings.TrimSpace(os.Getenv("AJS_RENEWAL_HOUR")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 || parsed > 23 {
			return nil, nil, fmt.Errorf("AJS_RENEWAL_HOUR %q must be 0..23", raw)
		}
		renewalHour = parsed
	}
	leadDays := []int{30, 14, 7, 3, 1}
	if raw := strings.TrimSpace(os.Getenv("AJS_RENEWAL_LEAD_DAYS")); raw != "" {
		leadDays = nil
		for _, part := range strings.Split(raw, ",") {
			parsed, err := strconv.Atoi(strings.TrimSpace(part))
			if err != nil || parsed < 1 || parsed > 365 {
				return nil, nil, fmt.Errorf("AJS_RENEWAL_LEAD_DAYS %q must be comma-separated days 1..365", raw)
			}
			leadDays = append(leadDays, parsed)
		}
		if len(leadDays) > 6 {
			return nil, nil, errors.New("AJS_RENEWAL_LEAD_DAYS accepts at most 6 values")
		}
	}
	timezone := envOr("AJS_TIMEZONE", "UTC")
	if _, err := schedule.LoadTimezone(timezone); err != nil {
		return nil, nil, fmt.Errorf("AJS_TIMEZONE %q is not a valid IANA name", timezone)
	}
	fireLog, err := schedule.OpenFireLog(filepath.Join(filepath.Dir(statePath), "schedule-fire.log"), time.Now)
	if err != nil {
		return nil, nil, err
	}
	var catalog schedule.Catalog
	if controller != nil {
		catalog = controller
	}
	engine, err := schedule.NewEngine(schedule.Config{
		Store:             st,
		Catalog:           catalog,
		Logger:            logger,
		FireLog:           fireLog,
		Tick:              tick,
		MaxConcurrent:     workers,
		QueueSize:         100,
		StartDeadline:     60 * time.Second,
		AllowMultiSession: os.Getenv("AJS_VENDOR_ONE_SESSION") == "0",
		NodeLimit:         nodeLimit,
		RenewalEnabled:    renewalEnabled,
		RenewalLeadDays:   leadDays,
		RenewalHour:       renewalHour,
		RenewalTimezone:   timezone,
		OnRun: func(schedule model.Schedule, run model.ScheduleRun) {
			if events == nil {
				return
			}
			switch run.Result {
			case "ok":
				events.Emit(webhook.EventScheduleFired, scheduleEventData(schedule, run))
			case "error":
				events.Emit(webhook.EventScheduleFailed, scheduleEventData(schedule, run))
			}
		},
		OnReminder: func(reminder schedule.ReminderEvent) {
			emitRenewalEvent(events, reminder)
		},
	})
	if err != nil {
		_ = fireLog.Close()
		return nil, nil, err
	}
	if err := engine.Start(ctx); err != nil {
		_ = engine.Stop(context.Background())
		_ = fireLog.Close()
		return nil, nil, err
	}
	logger.Info("scheduler_started", "tick", tick.String(), "workers", workers,
		"renewal_reminders", renewalEnabled)
	stop := func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := engine.Stop(stopCtx); err != nil {
			logger.Warn("scheduler_stop_failed", "error", err)
		}
		_ = engine.Checkpoint()
		_ = fireLog.Close()
	}
	return engine, stop, nil
}

// Health sampler bounds. AJS_HEALTH_MAX_NODES overrides the retained-ring cap;
// values outside 1..64 are clamped and a non-integer fails startup, matching
// AJS_NODE_LIMIT. The default covers a sixteen-gate console before the LRU cap
// reclaims a ring.
const (
	defaultHealthMaxNodes = health.DefaultMaxNodes
	minHealthMaxNodes     = 1
	maxHealthMaxNodes     = 64
)

// healthMaxNodes parses AJS_HEALTH_MAX_NODES. Empty selects the default; a
// numeric value is clamped into the accepted range so an operator can write
// 100 without failing the daemon; a non-integer fails startup instead of
// silently running a different cap.
func healthMaxNodes() (int, error) {
	raw := strings.TrimSpace(os.Getenv("AJS_HEALTH_MAX_NODES"))
	if raw == "" {
		return defaultHealthMaxNodes, nil
	}
	nodes, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("AJS_HEALTH_MAX_NODES %q must be an integer", raw)
	}
	if nodes < minHealthMaxNodes {
		return minHealthMaxNodes, nil
	}
	if nodes > maxHealthMaxNodes {
		return maxHealthMaxNodes, nil
	}
	return nodes, nil
}

// minEgressInterval keeps a misconfigured AJS_EGRESS_INTERVAL from turning
// every sample into a vendor flow: each probe spends the account's session
// capacity and budget, so a sub-10s cadence is refused rather than clamped.
const minEgressInterval = 10 * time.Second

// egressProbeEnabled reports whether AJS_EGRESS_PROBE opts into the
// through-gate probe. Only the exact value "1" enables it; the probe is off by
// default because every sample is real egress through the vendor session.
func egressProbeEnabled() bool {
	return strings.TrimSpace(os.Getenv("AJS_EGRESS_PROBE")) == "1"
}

// egressConfig parses the optional egress knobs. An empty interval keeps the
// 60s default, a malformed or too-short one fails startup, and an empty target
// keeps https://www.gstatic.com/generate_204. NewEgressSampler validates the
// target so a typo aborts startup instead of recording every probe as a
// failure.
func egressConfig() (health.EgressConfig, error) {
	// Start from the effective defaults so the startup log reports the cadence
	// the sampler will actually use, not the zero value that New would fill.
	cfg := health.EgressConfig{
		Interval: health.DefaultEgressInterval,
		Timeout:  health.DefaultEgressTimeout,
		Target:   strings.TrimSpace(os.Getenv("AJS_EGRESS_TARGET")),
	}
	raw := strings.TrimSpace(os.Getenv("AJS_EGRESS_INTERVAL"))
	if raw == "" {
		return cfg, nil
	}
	interval, err := time.ParseDuration(raw)
	if err != nil {
		return cfg, fmt.Errorf("AJS_EGRESS_INTERVAL %q must be a duration such as 60s", raw)
	}
	if interval < minEgressInterval {
		return cfg, fmt.Errorf("AJS_EGRESS_INTERVAL %q must be at least %s", raw, minEgressInterval)
	}
	cfg.Interval = interval
	return cfg, nil
}

// egressLogTarget renders AJS_EGRESS_TARGET for the startup log without any
// userinfo, query or fragment. The target is an operator setting, but a URL is
// an easy place to paste a token; the log keeps only scheme, host and path.
func egressLogTarget(raw string) string {
	if raw == "" {
		return health.DefaultEgressTarget
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "invalid"
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.RawFragment = ""
	return parsed.String()
}

// healthReconcileInterval is how often the sampler is re-aligned with the
// controller's running gates. The API updates it immediately on
// connect/disconnect/login/logout; the sweep covers gates opened by the
// scheduler, which never passes through an API handler.
const healthReconcileInterval = 15 * time.Second

// healthRearmLogInterval throttles the re-arm log: one line per minute at
// most, so a sampler pinned at its node cap cannot emit a line every sweep.
const healthRearmLogInterval = time.Minute

// healthGateSource is the live-gate view the reconciler follows.
// *runtime.NodeController implements it.
type healthGateSource interface {
	Snapshot() []runtime.NodeRuntime
}

// healthTracker adapts *health.Sampler (and, when enabled, *health.EgressSampler)
// to api.HealthMonitor and remembers the watched keys, so the reconciler can
// unwatch gates the controller no longer runs and re-arm a key the sampler
// evicted. It serializes Watch/Unwatch because a port change needs the
// sampler's retained ring re-armed, and Unwatch waits for the probe goroutine.
type healthTracker struct {
	sampler *health.Sampler
	egress  *health.EgressSampler
	now     func() time.Time

	mu           sync.Mutex
	watched      map[string]int
	lastRearmLog time.Time
}

func newHealthTracker(sampler *health.Sampler, egress *health.EgressSampler) *healthTracker {
	return &healthTracker{
		sampler: sampler,
		egress:  egress,
		now:     time.Now,
		watched: make(map[string]int),
	}
}

func (t *healthTracker) Watch(key string, port int) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if current, ok := t.watched[key]; ok && current != port {
		t.sampler.Unwatch(key)
		if t.egress != nil {
			t.egress.Unwatch(key)
		}
		delete(t.watched, key)
	}
	if err := t.sampler.Watch(key, port); err != nil {
		return err
	}
	if t.egress != nil {
		// The egress sampler follows the same live-gate set. Its only
		// failures are the validation errors the loopback watch already
		// refused and a stopped sampler; report them so the sweep retries.
		if err := t.egress.Watch(key, port); err != nil {
			return err
		}
	}
	t.watched[key] = port
	return nil
}

func (t *healthTracker) Unwatch(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sampler.Unwatch(key)
	if t.egress != nil {
		t.egress.Unwatch(key)
	}
	delete(t.watched, key)
}

func (t *healthTracker) Stats(key string) (health.Stats, bool) {
	return t.sampler.Stats(key)
}

// EgressStats exposes the through-gate window to the API. A daemon started
// without AJS_EGRESS_PROBE has no egress sampler, and the API then reports the
// block as disabled instead of inventing an empty aggregate.
func (t *healthTracker) EgressStats(key string) (health.EgressStats, bool) {
	if t.egress == nil {
		return health.EgressStats{}, false
	}
	return t.egress.Stats(key)
}

// EgressEnabled reports whether the through-gate probe is running.
func (t *healthTracker) EgressEnabled() bool {
	return t.egress != nil
}

// Evicted reports whether the sampler dropped this key's window at its node
// cap. The reconciler re-arms such a key; the gate itself is still running.
func (t *healthTracker) Evicted(key string) bool {
	return t.sampler.Evicted(key)
}

// snapshot returns a copy of the watched key→port map.
func (t *healthTracker) snapshot() map[string]int {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]int, len(t.watched))
	for key, port := range t.watched {
		out[key] = port
	}
	return out
}

// noteRearm logs one healing sweep at most once per minute. Both lists are
// sorted by the caller so the line is stable across map iteration.
func (t *healthTracker) noteRearm(logger *slog.Logger, rearmed, failed []string) {
	if logger == nil || (len(rearmed) == 0 && len(failed) == 0) {
		return
	}
	t.mu.Lock()
	now := t.now()
	if !t.lastRearmLog.IsZero() && now.Sub(t.lastRearmLog) < healthRearmLogInterval {
		t.mu.Unlock()
		return
	}
	t.lastRearmLog = now
	t.mu.Unlock()
	if len(rearmed) > 0 {
		logger.Info("health_watch_rearmed", "count", len(rearmed), "keys", rearmed)
	}
	if len(failed) > 0 {
		logger.Warn("health_watch_rearm_failed", "count", len(failed), "keys", failed)
	}
}

// runHealthReconciler keeps the sampler aligned with the controller's live
// gates until ctx is cancelled.
func runHealthReconciler(ctx context.Context, source healthGateSource, tracker *healthTracker, logger *slog.Logger) {
	ticker := time.NewTicker(healthReconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reconcileHealthWatches(source, tracker, logger)
		}
	}
}

// reconcileHealthWatches watches every running gate, re-arms a watched key the
// sampler evicted at its cap, and unwatches keys the controller no longer
// runs. source.Snapshot is the authoritative running set; store nodes may
// outlive their gate after a restart.
//
// A re-arm is bounded to one attempt per key per sweep. The evicted set is
// captured before the first attempt, so a key evicted by a heal in this sweep
// waits for the next one: with fewer ring slots than live gates the sweep
// cannot rotate the whole watch set, and the key it healed stays watched.
func reconcileHealthWatches(source healthGateSource, tracker *healthTracker, logger *slog.Logger) {
	live := make(map[string]int)
	for _, node := range source.Snapshot() {
		if node.Port <= 0 {
			continue
		}
		live[node.NodeID] = node.Port
	}
	current := tracker.snapshot()
	pending := make(map[string]bool, len(live))
	for key := range live {
		pending[key] = tracker.Evicted(key)
	}
	rearmed := make([]string, 0, 1)
	failed := make([]string, 0, 1)
	for key, port := range live {
		if got, ok := current[key]; ok && got == port && !pending[key] {
			continue
		}
		if err := tracker.Watch(key, port); err != nil {
			failed = append(failed, key)
			continue
		}
		if pending[key] {
			rearmed = append(rearmed, key)
		}
	}
	sort.Strings(rearmed)
	sort.Strings(failed)
	tracker.noteRearm(logger, rearmed, failed)
	for key := range tracker.snapshot() {
		if _, ok := live[key]; !ok {
			tracker.Unwatch(key)
		}
	}
}

// sealedWebhookSecrets stores webhook signing secrets through secret.Store.
// The AAD label is namespaced so a webhook id can never collide with an
// account id, and pruneSecrets keeps these entries.
type sealedWebhookSecrets struct{ store *secret.Store }

func webhookSecretID(id string) string { return "webhook/" + id }

func (s sealedWebhookSecrets) Seal(id string, secret []byte) error {
	_, err := s.store.Seal(webhookSecretID(id), secret)
	return err
}

func (s sealedWebhookSecrets) Open(id string) (string, bool) {
	plaintext, err := s.store.OpenID(webhookSecretID(id))
	if err != nil {
		return "", false
	}
	return string(plaintext), true
}

func (s sealedWebhookSecrets) Delete(id string) error {
	return s.store.Delete(webhookSecretID(id))
}

// startWebhooks builds the dispatcher and registers every persisted receiver
// whose signing secret can be opened. A missing secret (no master key, or an
// entry removed by hand) is logged and skipped: the row stays visible as
// unavailable instead of delivering with an empty key.
//
// Every recorded attempt is appended to the store through OnDelivery. The
// append is memory-only — the periodic snapshot carries it to disk — and the
// dispatcher feeds the hook from its own bounded goroutine, so neither a slow
// disk nor a slow store can delay a delivery. A delivery that exhausts its
// retries additionally writes one warning audit row through OnDeadLetter, so
// an operator sees a receiver that stopped accepting events.
func startWebhooks(st *store.Store, logger *slog.Logger, secrets api.WebhookSecretStore) (*webhook.Dispatcher, api.EventSink) {
	return startWebhooksWithConfig(st, logger, secrets, webhook.Config{})
}

// startWebhooksWithConfig is startWebhooks with the dispatcher tunables the
// tests need (attempt count, backoff). Production passes the zero Config,
// which selects the dispatcher defaults; the persistence and audit hooks are
// always owned by this function.
func startWebhooksWithConfig(st *store.Store, logger *slog.Logger, secrets api.WebhookSecretStore, cfg webhook.Config) (*webhook.Dispatcher, api.EventSink) {
	cfg.Logger = logger
	cfg.OnDelivery = func(delivery webhook.Delivery) {
		if err := st.AppendWebhookDelivery(persistedDelivery(delivery)); err != nil {
			logger.Warn("webhook_delivery_persist_failed", "delivery", delivery.ID, "error", err)
		}
	}
	// The dispatcher dedupes by delivery id, so one exhausted delivery writes
	// exactly one row. The detail carries the event and the attempt count
	// only; the URL, signing secret and payload never enter the audit trail.
	cfg.OnDeadLetter = func(delivery webhook.Delivery) {
		st.RecordAudit(model.SeverityWarn, "system", "webhook.dead_letter", delivery.EndpointID,
			fmt.Sprintf("%s after %d attempts", delivery.Event, delivery.Attempt))
	}
	dispatcher := webhook.New(cfg)
	registered, unavailable := 0, 0
	for _, endpoint := range st.WebhookEndpoints() {
		secret, ok := openWebhookSecret(secrets, endpoint.ID)
		if !ok {
			unavailable++
			logger.Warn("webhook_secret_missing", "endpoint", endpoint.ID)
			continue
		}
		receiver := webhook.Endpoint{
			ID:          endpoint.ID,
			URL:         endpoint.URL,
			Secret:      secret,
			Events:      webhookEventTypes(endpoint.Events),
			Enabled:     endpoint.Enabled,
			Description: endpoint.Description,
		}
		if parsed, err := time.Parse(time.RFC3339, endpoint.CreatedAt); err == nil {
			receiver.CreatedAt = parsed
		}
		if err := dispatcher.AddEndpoint(receiver); err != nil {
			unavailable++
			logger.Warn("webhook_endpoint_invalid", "endpoint", endpoint.ID, "error", err)
			continue
		}
		if !endpoint.Enabled {
			_ = dispatcher.SetEndpointEnabled(endpoint.ID, false)
		}
		registered++
	}
	logger.Info("webhooks_loaded",
		"registered", registered,
		"unavailable", unavailable,
		"configured", len(st.WebhookEndpoints()),
		"sealed", secrets != nil)
	return dispatcher, &webhookSink{dispatcher: dispatcher, logger: logger}
}

func openWebhookSecret(secrets api.WebhookSecretStore, id string) (string, bool) {
	if secrets == nil {
		return "", false
	}
	return secrets.Open(id)
}

// persistedDelivery maps one dispatcher attempt onto its snapshot row. The
// timestamp is RFC 3339 because that is the model's wire convention; the
// nanosecond precision keeps retries of one event ordered.
func persistedDelivery(delivery webhook.Delivery) model.WebhookDelivery {
	return model.WebhookDelivery{
		ID:         delivery.ID,
		EndpointID: delivery.EndpointID,
		Event:      string(delivery.Event),
		Attempt:    delivery.Attempt,
		Status:     string(delivery.Status),
		HTTPStatus: delivery.HTTPStatus,
		Error:      delivery.Error,
		At:         delivery.At.UTC().Format(time.RFC3339Nano),
		DurationMs: delivery.DurationMs,
	}
}

// webhookEventTypes converts persisted event names to the dispatcher type.
func webhookEventTypes(events []string) []webhook.EventType {
	out := make([]webhook.EventType, 0, len(events))
	for _, event := range events {
		out = append(out, webhook.EventType(event))
	}
	return out
}

// ringSink mirrors every customer event into the replay ring before fanning it
// out to the webhook dispatcher. It is the single emission point for both
// consumers: the replay API and the SSE stream serve the same stream the
// webhook receivers see, so neither view can miss an event the other saw.
// Publishing first is safe because Ring.Publish never blocks.
type ringSink struct {
	ring *eventring.Ring
	next api.EventSink
}

func (s *ringSink) Emit(event webhook.EventType, data map[string]any) {
	s.ring.Publish(eventring.Event{Type: string(event), Data: data})
	if s.next != nil {
		s.next.Emit(event, data)
	}
}

// webhookSink fans one customer event out to every enabled endpoint.
// Dispatcher.Dispatch only enqueues, so an event never blocks the request that
// produced it. Each endpoint gets its own event id because the dispatcher's
// dedupe window is keyed by that id.
type webhookSink struct {
	dispatcher *webhook.Dispatcher
	logger     *slog.Logger
	seq        atomic.Uint64
}

func (s *webhookSink) Emit(event webhook.EventType, data map[string]any) {
	for _, endpoint := range s.dispatcher.Endpoints() {
		if !endpoint.Enabled {
			continue
		}
		id := fmt.Sprintf("%s-%d-%d", event, time.Now().UnixNano(), s.seq.Add(1))
		err := s.dispatcher.Dispatch(webhook.Event{
			ID:         id,
			EndpointID: endpoint.ID,
			Type:       event,
			Data:       data,
		})
		if err == nil ||
			errors.Is(err, webhook.ErrNotSubscribed) ||
			errors.Is(err, webhook.ErrEndpointDisabled) {
			continue
		}
		s.logger.Warn("webhook_dispatch_failed", "event", event, "endpoint", endpoint.ID, "error", err)
	}
}

// scheduleEventData is the payload shared by schedule.fired and
// schedule.failed. It carries ids and outcomes only, never a credential.
func scheduleEventData(schedule model.Schedule, run model.ScheduleRun) map[string]any {
	data := map[string]any{
		"scheduleId":   run.ScheduleID,
		"runId":        run.ID,
		"trigger":      run.Trigger,
		"scheduledFor": run.ScheduledFor,
		"startedAt":    run.StartedAt,
		"finishedAt":   run.FinishedAt,
		"result":       run.Result,
	}
	if schedule.Target.Kind == "account" {
		data["accountId"] = schedule.Target.ID
	}
	if run.Reason != "" {
		data["reason"] = run.Reason
	}
	if run.ErrorClass != "" {
		data["errorClass"] = run.ErrorClass
	}
	if run.Detail != "" {
		data["detail"] = run.Detail
	}
	return data
}

// emitRenewalEvent maps one renewal reminder onto the customer webhook
// vocabulary: a due threshold becomes account.renewal_due and an overdue
// membership becomes account.expired. Unknown kinds are dropped instead of
// inventing an event type. A nil sink is a daemon without webhooks.
func emitRenewalEvent(events api.EventSink, reminder schedule.ReminderEvent) {
	if events == nil {
		return
	}
	switch reminder.Kind {
	case schedule.ReminderKindRenewalDue:
		events.Emit(webhook.EventAccountRenewalDue, renewalEventData(reminder))
	case schedule.ReminderKindRenewalOverdue:
		events.Emit(webhook.EventAccountExpired, renewalEventData(reminder))
	}
}

// renewalEventData is the payload shared by account.renewal_due and
// account.expired. It carries the account id and the expiry only, never a
// credential.
func renewalEventData(reminder schedule.ReminderEvent) map[string]any {
	return map[string]any{
		"accountId": reminder.AccountID,
		"expiresAt": reminder.ExpiresAt,
		"daysLeft":  reminder.DaysLeft,
		"threshold": reminder.Threshold,
	}
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

// autoLoginEnabled reports whether AJS_AUTO_LOGIN turns the startup auto-login
// on. Only the exact value "1" (surrounding whitespace allowed) enables it,
// matching AJS_SCHEDULES and AJS_DROP_DEMO; every other value, including the
// unset default, keeps the daemon from contacting a vendor host on boot.
func autoLoginEnabled(raw string) bool {
	return strings.TrimSpace(raw) == "1"
}

// placeholderAdminTokens are the values the repository itself ships or prints
// as examples — deploy/.env.example included. Accepting any of them yields a
// working admin API whose secret is public (S2), so startup refuses them.
var placeholderAdminTokens = map[string]struct{}{
	"change-me": {},
	"changeme":  {},
	"change_me": {},
	"admin":     {},
	"test":      {},
	"password":  {},
	"secret":    {},
	// The literal the READMEs used as a loopback placeholder: printed in the
	// repository, so accepting it is the same exposure as `change-me`.
	"local-test-token": {},
	// The literal .env.example ships: it is an instruction, not a token.
	"replace-me-with-openssl-rand-hex-32": {},
}

// minAdminTokenLen is the shortest accepted panel token (S2). The token is the
// only authentication on the admin API and is compared byte-for-byte, so a
// short value is guessable no matter which characters it uses. 32 characters
// is what `openssl rand -hex 32` emits.
const minAdminTokenLen = 32

// tenantPortalConfig validates the three portal environment values together.
// A partial configuration is refused: enabling the portal without an exact
// origin would leave signup, login and every mutation unchecked, and a
// malformed proxy entry would silently trust the wrong peer.
func tenantPortalConfig(enabled bool, originRaw, proxyRaw string) (string, []string, error) {
	origin, err := api.NormalizeTenantOrigin(originRaw)
	if err != nil {
		return "", nil, err
	}
	if enabled && origin == "" {
		return "", nil, errors.New("AJS_TENANT_PORTAL=1 requires AJS_TENANT_ORIGIN, for example https://panel.example.com")
	}
	proxies := splitList(proxyRaw)
	if _, err := api.ParseTrustedProxies(proxies); err != nil {
		return "", nil, err
	}
	return origin, proxies, nil
}

// validateAdminToken is the startup guard for the panel bearer token (S2).
// The insecure mode keeps the old behaviour: it exists for local lab runs,
// where requireLoopback has already proved the listener is not routable.
// The token value never reaches the error text.
func validateAdminToken(token string, insecure bool) error {
	if insecure {
		return nil
	}
	// Trim only for the checks: the listener still compares the exact value
	// the operator configured. Padding a short token to the minimum length
	// must not pass, because the padding carries no entropy.
	trimmed := strings.TrimSpace(token)
	if trimmed == "" {
		return errors.New("AJS_ADMIN_TOKEN is required unless AJS_ALLOW_INSECURE=1")
	}
	if _, bad := placeholderAdminTokens[strings.ToLower(trimmed)]; bad {
		return errors.New("AJS_ADMIN_TOKEN is a documented placeholder; generate a random one (openssl rand -hex 32) or set AJS_ALLOW_INSECURE=1 for a local lab run")
	}
	if len(trimmed) < minAdminTokenLen {
		return fmt.Errorf("AJS_ADMIN_TOKEN is %d characters, below the %d-character minimum; generate one with openssl rand -hex 32 or set AJS_ALLOW_INSECURE=1 for a local lab run", len(trimmed), minAdminTokenLen)
	}
	return nil
}

// requireLoopback is the bind-address guard for AJS_ALLOW_INSECURE=1 (S4).
// Insecure mode disables bearer auth entirely, so the loopback bind is the
// only boundary left; without this check one env var publishes an open admin
// API on every interface.
func requireLoopback(listen string) error {
	return requireLoopbackEnv(listen, "AJS_LISTEN")
}

// requireLoopbackEnv is requireLoopback for a named listener variable, so the
// error tells the operator which address to change.
func requireLoopbackEnv(listen, env string) error {
	host := listen
	if parsed, _, err := net.SplitHostPort(listen); err == nil {
		host = parsed
	}
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if host == "" {
		return fmt.Errorf("AJS_ALLOW_INSECURE=1 needs a loopback %s, got %q (an empty host binds every interface)", env, listen)
	}
	lower := strings.ToLower(host)
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("AJS_ALLOW_INSECURE=1 needs a loopback %s (127.0.0.1, ::1 or localhost), got %q", env, listen)
}

// validateTLSListener is the startup guard for the optional HTTPS listener.
// Two listeners cannot share one address, and AJS_ALLOW_INSECURE=1 turns the
// admin API into an unauthenticated service: the plaintext guard pins
// AJS_LISTEN to loopback, and the TLS listener — whose whole purpose is to be
// reachable beyond loopback — must not become the way around it.
func validateTLSListener(listen string, cfg api.TLSConfig, insecure bool) error {
	if !cfg.Enabled() {
		return nil
	}
	if cfg.Listen == listen {
		return fmt.Errorf("AJS_TLS_LISTEN and AJS_LISTEN are both %q; the HTTPS listener needs its own address", listen)
	}
	if insecure {
		return requireLoopbackEnv(cfg.Listen, api.EnvTLSListen)
	}
	return nil
}

// validateLoginHosts is the scheme allowlist for AJS_API_HOSTS (S11). The
// login body carries the account password form-encoded, so an http:// host
// would send it in cleartext and leave nothing for AJS_CONTROL_CERT_SHA256 to
// pin. A local lab mock — the only reason to allow plaintext — opts in with
// AJS_ALLOW_PLAINTEXT=1.
func validateLoginHosts(hosts []string) error {
	if os.Getenv("AJS_ALLOW_PLAINTEXT") == "1" {
		return nil
	}
	for _, host := range hosts {
		u, err := url.Parse(host)
		if err != nil {
			return fmt.Errorf("AJS_API_HOSTS %q: %w", host, err)
		}
		if u.Scheme == "" || strings.EqualFold(u.Scheme, "https") {
			continue
		}
		return fmt.Errorf("AJS_API_HOSTS %q is not https: the login would send the account password in cleartext; use https or set AJS_ALLOW_PLAINTEXT=1 for a local lab mock", host)
	}
	return nil
}

// defaultSnapshotInterval is the background save cadence when
// AJS_SNAPSHOT_INTERVAL is unset.
const defaultSnapshotInterval = 30 * time.Second

// stateFilePath resolves AJS_STATE_FILE, defaulting to
// $AJS_STATE_DIR/state.json next to the sealed-secret file.
func stateFilePath() string {
	if path := strings.TrimSpace(os.Getenv("AJS_STATE_FILE")); path != "" {
		return path
	}
	return filepath.Join(envOr("AJS_STATE_DIR", "./data"), "state.json")
}

// snapshotInterval parses AJS_SNAPSHOT_INTERVAL. A bare integer means seconds
// (the bench run showed "3600" is the obvious way to write an hour); any Go
// duration keeps its meaning. An invalid or non-positive duration fails
// startup instead of silently disabling persistence.
func snapshotInterval() (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv("AJS_SNAPSHOT_INTERVAL"))
	if raw == "" {
		return defaultSnapshotInterval, nil
	}
	interval, err := time.ParseDuration(raw)
	if err != nil {
		// ParseDuration("3600s") also catches integer overflow for us.
		if _, intErr := strconv.ParseInt(raw, 10, 64); intErr == nil {
			interval, err = time.ParseDuration(raw + "s")
		}
		if err != nil {
			return 0, fmt.Errorf("AJS_SNAPSHOT_INTERVAL %q: %w (use a Go duration such as 30s, or a bare integer number of seconds)", raw, err)
		}
	}
	if interval <= 0 {
		return 0, fmt.Errorf("AJS_SNAPSHOT_INTERVAL %q must be positive", raw)
	}
	return interval, nil
}

// pruneSecrets removes sealed entries that have no account, which is how an
// older build's delete or a crash between SealMany and apply leaves the file.
// It logs a count only: the entries are sealed, but their ids are still not
// needed in a log line.
func pruneSecrets(secrets *secret.Store, st *store.Store, logger *slog.Logger) {
	if secrets == nil {
		return
	}
	known := make(map[string]struct{}, len(st.Accounts())+len(st.WebhookEndpoints()))
	for _, account := range st.Accounts() {
		known[account.ID] = struct{}{}
	}
	// Webhook signing secrets share the file under the "webhook/" namespace;
	// an entry is an orphan only when its endpoint row is gone.
	for _, endpoint := range st.WebhookEndpoints() {
		known[webhookSecretID(endpoint.ID)] = struct{}{}
	}
	orphans := make([]string, 0)
	for _, id := range secrets.IDs() {
		if _, ok := known[id]; !ok {
			orphans = append(orphans, id)
		}
	}
	if len(orphans) == 0 {
		return
	}
	if err := secrets.RemoveMany(orphans); err != nil {
		logger.Warn("secret_prune_failed", "count", len(orphans), "error", err)
		return
	}
	logger.Info("secret_pruned", "count", len(orphans))
}

// saveState writes the full snapshot and logs the outcome. Neither the log
// line nor the error carries a credential: the document only holds sealed
// envelopes and key hashes.
func saveState(st *store.Store, path string, logger *slog.Logger, reason string) {
	if err := st.Save(path); err != nil {
		logger.Error("state_save_failed", "path", path, "reason", reason, "error", err)
		return
	}
	logger.Info("state_saved", "path", path, "reason", reason)
}

// openStore builds the panel store. The daemon starts from an empty store:
// the dashboard mock seed is a test/mock fixture, never live data. AJS_MASTER_KEY
// enables at-rest sealing of account passwords; without it the daemon keeps the
// skeleton behaviour and discards them. With the key set the device pool is on
// by default (DESIGN-DEVICE-POOL §7.6) and seals device material through a
// second secret.Store whose key is derived from the master key; the password
// file and the device file stay distinct (§5.1), so a password rotation cannot
// re-encrypt identities and pruneSecrets never mistakes a dev_ entry for an
// orphan. AJS_DEVICE_POOL=0 is the test-only kill switch. The key itself is
// never logged.
func openStore(logger *slog.Logger) (*store.Store, *secret.Store, *secret.Store, error) {
	raw := strings.TrimSpace(os.Getenv("AJS_MASTER_KEY"))
	poolOn, err := devicePoolEnabled(raw != "")
	if err != nil {
		return nil, nil, nil, err
	}
	if raw == "" {
		logger.Info("secrets_disabled", "reason", "AJS_MASTER_KEY is not set")
		return store.NewEmpty(), nil, nil, nil
	}
	key, err := parseMasterKey(raw)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("AJS_MASTER_KEY: %w", err)
	}
	stateDir := envOr("AJS_STATE_DIR", "./data")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, nil, nil, fmt.Errorf("AJS_STATE_DIR %s: %w", stateDir, err)
	}
	path := filepath.Join(stateDir, "secrets.json")
	secrets, err := secret.New(key, path)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("secret store: %w", err)
	}
	logger.Info("secrets_enabled", "path", path)
	if !poolOn {
		logger.Info("device_pool_disabled", "reason", "AJS_DEVICE_POOL=0")
		return store.NewEmptyWithSealer(secrets), secrets, nil, nil
	}
	deviceKey, err := deviceSealerKey(key)
	if err != nil {
		_ = secrets.Close()
		return nil, nil, nil, err
	}
	devicePath := filepath.Join(stateDir, "device-secrets.json")
	deviceSecrets, err := secret.New(deviceKey, devicePath)
	if err != nil {
		_ = secrets.Close()
		return nil, nil, nil, fmt.Errorf("device secret store: %w", err)
	}
	logger.Info("device_pool_enabled", "path", devicePath)
	return store.NewEmptyWithSealers(secrets, deviceSecrets), secrets, deviceSecrets, nil
}

// devicePoolEnabled resolves AJS_DEVICE_POOL. DESIGN-DEVICE-POOL §7.6 makes the
// pool on by default whenever AJS_MASTER_KEY is set, because the sealer needs
// that key; AJS_DEVICE_POOL=0 is the test-only kill switch. An explicit 1
// without a master key is a configuration error, and any other value is
// refused instead of being read as "on".
func devicePoolEnabled(masterKeyPresent bool) (bool, error) {
	switch raw := strings.TrimSpace(os.Getenv("AJS_DEVICE_POOL")); raw {
	case "":
		return masterKeyPresent, nil
	case "1":
		if !masterKeyPresent {
			return false, errors.New("AJS_DEVICE_POOL=1 requires AJS_MASTER_KEY: the pool has no sealer without it")
		}
		return true, nil
	case "0":
		return false, nil
	default:
		return false, fmt.Errorf("AJS_DEVICE_POOL %q: use 1 (on) or 0 (off)", raw)
	}
}

// deviceSealerKey derives the device-material key. The device file is separate
// from the password file (DESIGN-DEVICE-POOL §5.1): the derived key keeps the
// two namespaces independent, and the sealed entries are keyed by device id.
func deviceSealerKey(masterKey []byte) ([]byte, error) {
	key, err := hkdf.Key(sha256.New, masterKey, nil, "ajs/device-pool/v1", secret.KeySize)
	if err != nil {
		return nil, fmt.Errorf("device sealer key: %w", err)
	}
	return key, nil
}

// migrateDevicesAtStartup converts a legacy plaintext device map to sealed pool
// records. Restore already performs the same conversion when the device sealer
// is wired, so the call is idempotent; it keeps the documented startup sequence
// (DESIGN-DEVICE-POOL §5.3) in one place and covers a store that was populated
// before the sealer was attached. A failure is logged and startup continues:
// the panel still serves, and a login for an affected account fails closed
// instead of regenerating an identity.
func migrateDevicesAtStartup(st *store.Store, logger *slog.Logger) {
	if st == nil || !st.DevicePoolEnabled() {
		return
	}
	before := len(st.DevicePoolRecords())
	if err := st.MigrateDevices(); err != nil {
		logger.Warn("device_migration_failed", "error", err)
		return
	}
	if after := len(st.DevicePoolRecords()); after != before {
		logger.Info("device_pool_migrated", "records", after)
	}
	if warnings := st.DevicePoolWarnings(); len(warnings) > 0 {
		logger.Warn("device_pool_warnings", "count", len(warnings))
	}
}

// parseMasterKey accepts a 32-byte key as 64 hex characters or base64, padded
// or raw. The error never echoes the input.
func parseMasterKey(value string) ([]byte, error) {
	if key, err := hex.DecodeString(value); err == nil && len(key) == secret.KeySize {
		return key, nil
	}
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding} {
		if key, err := encoding.DecodeString(value); err == nil && len(key) == secret.KeySize {
			return key, nil
		}
	}
	return nil, errors.New("must be 32 bytes encoded as hex or base64")
}

// resinOptions mirrors the Resin flags/env. An empty token disables the
// integration entirely and keeps the in-memory behaviour.
type resinOptions struct {
	url              string
	token            string
	subscriptionName string
	platformName     string
}

// startResinPanel builds and starts the Resin wiring when an admin token is
// configured. The startup reconciliation runs before the listener starts. A
// failed start is logged but does not stop the daemon: POST /v1/reconcile
// retries the same sequence, and GET /v1/topology reports 503 until it
// succeeds. A nil api.Panel means Resin is disabled.
func startResinPanel(ctx context.Context, st *store.Store, logger *slog.Logger, opts resinOptions) (api.Panel, error) {
	if opts.token == "" {
		logger.Info("resin_disabled", "reason", "AJS_RESIN_ADMIN_TOKEN is not set")
		return nil, nil
	}
	cfg := resin.ConfigFromEnv()
	cfg.BaseURL = opts.url
	cfg.AdminToken = opts.token
	cfg.Logger = logger
	client, err := resin.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("resin client: %w", err)
	}
	pub := publisher.New(client, publisher.Config{Logger: logger})
	panel, err := runtime.NewResinPanel(st, client, pub, runtime.Config{
		SubscriptionName: opts.subscriptionName,
		PlatformName:     opts.platformName,
		Logger:           logger,
	})
	if err != nil {
		return nil, fmt.Errorf("resin panel: %w", err)
	}

	startCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := panel.Start(startCtx); err != nil {
		logger.Warn("resin_start_failed", "error", err)
		return panel, nil
	}
	logger.Info("resin_connected",
		"subscription_id", panel.SubscriptionID(),
		"platform_id", panel.PlatformID())
	return panel, nil
}

// startResinReconciler starts the interval reconciler when
// AJS_RESIN_RECONCILE=1 and a Resin panel is configured. The returned stop
// function cancels the loop and waits for an in-flight reconcile; the caller
// registers it after the panel's Close defer so it runs first. A nil stop
// means the loop is disabled.
func startResinReconciler(ctx context.Context, st *store.Store, panel api.Panel, logger *slog.Logger) (func(), error) {
	enabled := strings.TrimSpace(os.Getenv("AJS_RESIN_RECONCILE")) == "1"
	if panel == nil {
		if enabled {
			logger.Warn("resin_reconcile_disabled", "reason", "AJS_RESIN_ADMIN_TOKEN is not set")
		}
		return nil, nil
	}
	if !enabled {
		logger.Info("resin_reconcile_disabled", "reason", "AJS_RESIN_RECONCILE is not 1")
		return nil, nil
	}
	interval, err := resinReconcileInterval()
	if err != nil {
		return nil, err
	}
	reconciler, err := publisher.NewReconciler(publisher.ReconcileConfig{
		Interval: interval,
		Snapshot: func() publisher.DesiredState { return desiredResinState(st) },
		Reconcile: func(ctx context.Context) error {
			_, err := panel.Reconcile(ctx)
			return err
		},
		Audit:  resinReconcileAudit(st),
		Logger: logger,
	})
	if err != nil {
		return nil, fmt.Errorf("resin reconciler: %w", err)
	}
	loopCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		reconciler.Run(loopCtx)
	}()
	logger.Info("resin_reconcile_enabled", "interval", interval.String())
	return func() {
		cancel()
		<-done
	}, nil
}

// resinReconcileAudit maps the reconciler's two transitions onto store audit
// rows. A failed publish is a warning; a recovery is informational. The detail
// is produced by the reconciler and never carries a credential.
func resinReconcileAudit(st *store.Store) func(action, detail string) {
	return func(action, detail string) {
		severity := model.SeverityInfo
		if action == publisher.AuditReconcileFailed {
			severity = model.SeverityWarn
		}
		st.RecordAudit(severity, "system", action, "resin", detail)
	}
}

// desiredResinState maps the store onto the reconciler's hash input. It uses
// the same LIVE filter and tag as runtime.ResinPanel.render; volatile counters
// (requests, bytes, latency) are deliberately absent so a busy gate does not
// look like drift.
func desiredResinState(st *store.Store) publisher.DesiredState {
	nodes := st.Nodes()
	state := publisher.DesiredState{Nodes: make([]publisher.Node, 0, len(nodes))}
	for _, node := range nodes {
		if node.State != model.NodeLive {
			continue
		}
		state.Nodes = append(state.Nodes, publisher.Node{Tag: node.Label, GatePort: node.GatePort})
	}

	leases := st.Leases()
	state.Leases = make([]publisher.LeaseID, 0, len(leases))
	for _, lease := range leases {
		state.Leases = append(state.Leases, publisher.LeaseID{
			PlatformID: string(lease.Platform),
			Account:    lease.AccountID,
		})
	}

	accounts := st.Accounts()
	state.Accounts = make([]string, 0, len(accounts))
	for _, account := range accounts {
		state.Accounts = append(state.Accounts, account.ID)
	}
	return state
}

// resinReconcileInterval parses AJS_RESIN_RECONCILE_INTERVAL. Unset selects
// publisher.DefaultReconcileInterval; a bare integer means seconds, any Go
// duration keeps its meaning. Invalid or non-positive fails startup instead
// of silently disabling the loop.
func resinReconcileInterval() (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv("AJS_RESIN_RECONCILE_INTERVAL"))
	if raw == "" {
		return publisher.DefaultReconcileInterval, nil
	}
	interval, err := time.ParseDuration(raw)
	if err != nil {
		// ParseDuration("3600s") also catches integer overflow for us.
		if _, intErr := strconv.ParseInt(raw, 10, 64); intErr == nil {
			interval, err = time.ParseDuration(raw + "s")
		}
		if err != nil {
			return 0, fmt.Errorf("AJS_RESIN_RECONCILE_INTERVAL %q: %w (use a Go duration such as 5m, or a bare integer number of seconds)", raw, err)
		}
	}
	if interval <= 0 {
		return 0, fmt.Errorf("AJS_RESIN_RECONCILE_INTERVAL %q must be positive", raw)
	}
	return interval, nil
}

// startAccountLogin builds the optional vendor login path. AJS_ACCOUNT_LOGIN=1
// is the only switch: without it the daemon logs account_login_disabled and
// the handler keeps the store-only refresh. Construction performs no I/O and
// contacts no host. AJS_CONTROL_CERT_SHA256 optionally pins the control TLS
// certificate (empty keeps the default verification). The returned
// *account.LoginService satisfies api.LoginService; a nil value means
// disabled. The profile is returned so the node controller can open sessions
// with the same claims the login used.
func startAccountLogin(st *store.Store, logger *slog.Logger) (*account.LoginService, account.VersionProfile, error) {
	if os.Getenv("AJS_ACCOUNT_LOGIN") != "1" {
		logger.Info("account_login_disabled", "reason", "AJS_ACCOUNT_LOGIN is not 1")
		return nil, account.VersionProfile{}, nil
	}
	profile, err := loginProfile(envOr("AJS_ACCOUNT_PROFILE", "linux-4.2.3.0"))
	if err != nil {
		return nil, account.VersionProfile{}, err
	}
	hosts := splitList(os.Getenv("AJS_API_HOSTS"))
	if len(hosts) == 0 {
		hosts = append([]string(nil), account.DefaultLoginHosts...)
	}
	// S11: an http:// host sends the account password in cleartext and leaves
	// nothing for the certificate pin, so startup refuses it (opt out with
	// AJS_ALLOW_PLAINTEXT=1 for a local lab mock).
	if err := validateLoginHosts(hosts); err != nil {
		return nil, account.VersionProfile{}, err
	}
	httpClient, err := controlHTTPClient(logger)
	if err != nil {
		return nil, account.VersionProfile{}, err
	}
	service, err := account.NewLoginService(st, account.LoginConfig{
		Hosts:      hosts,
		Profile:    profile,
		HTTPClient: httpClient,
		Enabled:    true,
		Logger:     logger,
	})
	if err != nil {
		return nil, account.VersionProfile{}, fmt.Errorf("account login: %w", err)
	}
	logger.Info("account_login_enabled", "hosts", len(hosts), "profile", profile.Name,
		"control_tls_pin", pinState())
	return service, profile, nil
}

// controlLoginTimeout matches the api package's 35-second control client.
const controlLoginTimeout = 35 * time.Second

// controlHTTPClient resolves AJS_CONTROL_CERT_SHA256. An empty value keeps
// the default verification (nil client); a set value builds the pinned
// transport, whose fingerprint is public configuration and the only thing
// logged for it. A malformed fingerprint fails startup instead of silently
// falling back to default verification.
func controlHTTPClient(logger *slog.Logger) (*http.Client, error) {
	pin := strings.TrimSpace(os.Getenv("AJS_CONTROL_CERT_SHA256"))
	if pin == "" {
		return nil, nil
	}
	client, err := account.PinnedHTTPClient(pin, controlLoginTimeout)
	if err != nil {
		return nil, fmt.Errorf("AJS_CONTROL_CERT_SHA256: %w", err)
	}
	logger.Info("control_tls_pin", "fingerprint", pin)
	return client, nil
}

// pinState reports the startup log label for the TLS pin: "pinned" when
// AJS_CONTROL_CERT_SHA256 is set, "default" when it is empty.
func pinState() string {
	if strings.TrimSpace(os.Getenv("AJS_CONTROL_CERT_SHA256")) == "" {
		return "default"
	}
	return "pinned"
}

// startNodeController builds the FV session factory and the node controller
// that turns a successful login into one loopback gate per usable server.
// Construction performs no I/O: gates bind only when SyncAccount runs after a
// login. A nil panel (Resin disabled) still creates nodes; only the reconcile
// step is skipped. AJS_NODE_LIMIT caps how many usable servers one account
// starts (default 1, the vendor's one active data session per account; an
// explicit 0 = all). auth selects the gate's SOCKS5 authentication mode; in
// userpass mode the meter owns the identity service, so metering must be on.
// httpEnabled adds the HTTP CONNECT listener at gatePort+httpOffset.
func startNodeController(st *store.Store, logger *slog.Logger, panel api.Panel, logins runtime.LoginResults, profile account.VersionProfile, limit int, auth gate.AuthMode, httpEnabled bool, httpOffset int) (*runtime.NodeController, error) {
	factory := account.NewFactory(profile, &fvfacade.HTTPUpgrade{}, fvfacade.AuthOptions{})
	var reconciler runtime.Reconciler
	if panel != nil {
		reconciler = panel
	}
	// Gate flow metering is on by default; AJS_FLOW_METER=0 turns it off. The
	// factory attaches one observer callback per gate, so metering costs no
	// goroutine and no extra copy of the flow.
	var gates runtime.GateFactory
	var identity *runtime.GateIdentity
	if flowMeterEnabled(os.Getenv("AJS_FLOW_METER")) {
		meter := runtime.NewFlowMeter(st)
		gates = meter.GateFactory()
		identity = meter.Identity()
		logger.Info("flow_meter_enabled", "retention_rows", store.RequestRetention)
	} else {
		logger.Info("flow_meter_disabled", "reason", "AJS_FLOW_METER=0")
	}
	// Per-user quota is metered from the gate request rows this observer
	// writes. Without it a userpass gate could only ever see a stale total, so
	// the combination is refused instead of enforcing an unlimited policy.
	if auth == gate.AuthUserPass && identity == nil {
		return nil, errors.New("AJS_GATE_AUTH=userpass requires AJS_FLOW_METER: user quota is metered from gate request rows")
	}
	controller, err := runtime.NewNodeController(runtime.NodeControllerConfig{
		Store:          st,
		Logins:         logins,
		Sessions:       factory,
		Gates:          gates,
		Reconciler:     reconciler,
		Profile:        profile,
		MaxServers:     limit,
		Logger:         logger,
		GateAuth:       auth,
		GateIdentity:   identity,
		GateHTTP:       httpEnabled,
		GateHTTPOffset: httpOffset,
	})
	if err != nil {
		return nil, fmt.Errorf("node controller: %w", err)
	}
	if limit > 0 {
		logger.Info("node_controller_enabled", "profile", profile.Name, "reconcile", reconciler != nil, "max_servers", limit, "gate_auth", auth, "gate_http", httpEnabled, "gate_http_offset", httpOffset)
	} else {
		logger.Info("node_controller_enabled", "profile", profile.Name, "reconcile", reconciler != nil, "max_servers", "all", "gate_auth", auth, "gate_http", httpEnabled, "gate_http_offset", httpOffset)
	}
	return controller, nil
}

// flowMeterEnabled parses AJS_FLOW_METER. Metering is on by default so the
// usage page reflects real traffic; only the explicit value "0" disables it,
// matching the AJS_HEALTH_PROBE convention.
func flowMeterEnabled(raw string) bool {
	return strings.TrimSpace(raw) != "0"
}

// gateHTTPEnabled parses AJS_GATE_HTTP. The HTTP CONNECT listener is opt-in:
// an upgrade must never bind an extra port pair, and an operator enables it
// deliberately. "1" enables it, unset/"0" disables it, and anything else
// aborts startup rather than silently changing the listener surface.
func gateHTTPEnabled(raw string) (bool, error) {
	switch strings.TrimSpace(raw) {
	case "", "0":
		return false, nil
	case "1":
		return true, nil
	default:
		return false, fmt.Errorf("AJS_GATE_HTTP=%q: want 1 or 0", raw)
	}
}

// gateHTTPOffset parses AJS_GATE_HTTP_OFFSET, the distance between a node's
// SOCKS5 gate port and its HTTP CONNECT port. Unset selects the runtime
// default (1000). An explicit 0 is refused: it would bind both listeners to
// the same port.
func gateHTTPOffset(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return runtime.DefaultGateHTTPOffset, nil
	}
	offset, err := strconv.Atoi(raw)
	if err != nil || offset < 1 || offset >= 65535 {
		return 0, fmt.Errorf("AJS_GATE_HTTP_OFFSET=%q: want an integer between 1 and 65534", raw)
	}
	return offset, nil
}

// nodeLimit parses AJS_NODE_LIMIT, the per-account cap on usable servers the
// node controller starts. The default 1 keeps a single FV session per
// account: the live 2026-10-23 run proved the vendor allows exactly one
// active data session, so a second session's gate answers SOCKS5 0x05 with
// "fv: data channel rejected ... fm=disconnected by another connection".
// An explicit 0 keeps every usable server for catalog/probe research and is
// not live-safe; a negative or non-integer value fails startup instead of
// silently lifting the cap.
func nodeLimit() (int, error) {
	raw := strings.TrimSpace(os.Getenv("AJS_NODE_LIMIT"))
	if raw == "" {
		return 1, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("AJS_NODE_LIMIT %q must be a non-negative integer", raw)
	}
	if limit < 0 {
		return 0, fmt.Errorf("AJS_NODE_LIMIT %q must be a non-negative integer", raw)
	}
	return limit, nil
}

// loginProfile maps AJS_ACCOUNT_PROFILE to a version profile. The default is
// the live-tested Linux 4.2.3.0 profile; linux-4.3.0.1 selects the
// static-only 4.3.0.1 profile, whose unsigned login is not live-tested. An
// unknown value fails startup instead of silently logging in as a different
// client.
func loginProfile(name string) (account.VersionProfile, error) {
	switch strings.TrimSpace(name) {
	case "", "linux-4.2.3.0", account.Linux423Binary.Name:
		return account.Linux423Binary, nil
	case "linux-4.3.0.1", account.Linux430Binary.Name:
		return account.Linux430Binary, nil
	default:
		return account.VersionProfile{}, fmt.Errorf("AJS_ACCOUNT_PROFILE %q is not supported (want linux-4.2.3.0 or linux-4.3.0.1)", name)
	}
}

func splitList(value string) []string {
	parts := strings.Split(value, ",")
	items := make([]string, 0, len(parts))
	for _, part := range parts {
		if item := strings.TrimSpace(part); item != "" {
			items = append(items, item)
		}
	}
	return items
}

// buildCommit reports the VCS revision embedded by the Go toolchain. It is
// empty for a build without VCS metadata, which the Lab overview shows as "".
func buildCommit() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" {
			return setting.Value
		}
	}
	return ""
}

func parseLevel(value string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
