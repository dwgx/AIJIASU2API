// Command ajs-accountd serves the panel HTTP API from an in-memory store.
package main

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"ajsaccountd/internal/account"
	"ajsaccountd/internal/api"
	"ajsaccountd/internal/publisher"
	"ajsaccountd/internal/resin"
	"ajsaccountd/internal/runtime"
	"ajsaccountd/internal/secret"
	"ajsaccountd/internal/store"
	"ajsproxy/fvfacade"
)

func main() {
	if err := run(); err != nil {
		slog.Error("ajs-accountd failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	listen := flag.String("listen", envOr("AJS_LISTEN", "127.0.0.1:2261"), "listen address")
	token := flag.String("token", os.Getenv("AJS_ADMIN_TOKEN"), "admin bearer token (env AJS_ADMIN_TOKEN)")
	insecure := flag.Bool("insecure", os.Getenv("AJS_ALLOW_INSECURE") == "1", "disable bearer auth (env AJS_ALLOW_INSECURE=1)")
	corsOrigins := flag.String("cors-origins", envOr("AJS_CORS_ORIGINS", strings.Join(api.DefaultCORSOrigins, ",")), "comma-separated allowed CORS origins")
	logLevel := flag.String("log-level", envOr("AJS_LOG_LEVEL", "info"), "debug, info, warn or error")
	resinURL := flag.String("resin-url", envOr(resin.EnvBaseURL, resin.DefaultBaseURL), "Resin admin API base URL (env AJS_RESIN_URL)")
	resinToken := flag.String("resin-token", os.Getenv(resin.EnvAdminToken), "Resin admin bearer token (env AJS_RESIN_ADMIN_TOKEN); empty disables the integration")
	subscriptionName := flag.String("subscription-name", envOr("AJS_SUBSCRIPTION_NAME", runtime.DefaultSubscriptionName), "Resin local subscription name (env AJS_SUBSCRIPTION_NAME)")
	platformName := flag.String("platform-name", os.Getenv("AJS_PLATFORM_NAME"), "Resin platform name (env AJS_PLATFORM_NAME; default: the subscription name)")
	flag.Parse()

	if *token == "" && !*insecure {
		return errors.New("AJS_ADMIN_TOKEN is required unless AJS_ALLOW_INSECURE=1")
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: parseLevel(*logLevel)}))
	st, secrets, err := openStore(logger)
	if err != nil {
		return err
	}
	if secrets != nil {
		defer func() { _ = secrets.Close() }()
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

	var loginService api.LoginService
	var loginHook func(context.Context, string) error
	var accountDeleteHook func(context.Context, string) error
	var nodeDrainHook func(context.Context, string, bool) error
	if service, profile, err := startAccountLogin(st, logger); err != nil {
		return err
	} else if service != nil {
		defer service.Close()
		loginService = service
		controller, err := startNodeController(st, logger, panel, service, profile)
		if err != nil {
			return err
		}
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
		nodeDrainHook = func(ctx context.Context, nodeID string, drain bool) error {
			if drain {
				return controller.DrainNode(ctx, nodeID)
			}
			return controller.UndrainNode(ctx, nodeID)
		}
	}

	server := api.New(api.Config{
		AdminToken:        *token,
		AllowInsecure:     *insecure,
		CORSOrigins:       splitList(*corsOrigins),
		Panel:             panel,
		LoginService:      loginService,
		LoginHook:         loginHook,
		AccountDeleteHook: accountDeleteHook,
		NodeDrainHook:     nodeDrainHook,
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

	errCh := make(chan error, 1)
	go func() { errCh <- httpServer.ListenAndServe() }()
	authMode := "bearer"
	if *insecure {
		authMode = "insecure"
	}
	logger.Info("ajs-accountd listening", "addr", *listen, "auth", authMode)

	select {
	case <-ctx.Done():
		logger.Info("ajs-accountd shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown: %w", err)
		}
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
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

// snapshotInterval parses AJS_SNAPSHOT_INTERVAL. An invalid or non-positive
// duration fails startup instead of silently disabling persistence.
func snapshotInterval() (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv("AJS_SNAPSHOT_INTERVAL"))
	if raw == "" {
		return defaultSnapshotInterval, nil
	}
	interval, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("AJS_SNAPSHOT_INTERVAL %q: %w", raw, err)
	}
	if interval <= 0 {
		return 0, fmt.Errorf("AJS_SNAPSHOT_INTERVAL %q must be positive", raw)
	}
	return interval, nil
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

// openStore builds the panel store. AJS_MASTER_KEY enables at-rest sealing of
// account passwords; without it the daemon keeps the skeleton behaviour and
// discards them. The key itself is never logged.
func openStore(logger *slog.Logger) (*store.Store, *secret.Store, error) {
	raw := strings.TrimSpace(os.Getenv("AJS_MASTER_KEY"))
	if raw == "" {
		logger.Info("secrets_disabled", "reason", "AJS_MASTER_KEY is not set")
		return store.New(), nil, nil
	}
	key, err := parseMasterKey(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("AJS_MASTER_KEY: %w", err)
	}
	stateDir := envOr("AJS_STATE_DIR", "./data")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, nil, fmt.Errorf("AJS_STATE_DIR %s: %w", stateDir, err)
	}
	path := filepath.Join(stateDir, "secrets.json")
	secrets, err := secret.New(key, path)
	if err != nil {
		return nil, nil, fmt.Errorf("secret store: %w", err)
	}
	logger.Info("secrets_enabled", "path", path)
	return store.NewWithSealer(secrets), secrets, nil
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

// startAccountLogin builds the optional vendor login path. AJS_ACCOUNT_LOGIN=1
// is the only switch: without it the daemon logs account_login_disabled and
// the handler keeps the store-only refresh. Construction performs no I/O and
// contacts no host. The returned *account.LoginService satisfies
// api.LoginService; a nil value means disabled. The profile is returned so the
// node controller can open sessions with the same claims the login used.
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
	service, err := account.NewLoginService(st, account.LoginConfig{
		Hosts:   hosts,
		Profile: profile,
		Enabled: true,
		Logger:  logger,
	})
	if err != nil {
		return nil, account.VersionProfile{}, fmt.Errorf("account login: %w", err)
	}
	logger.Info("account_login_enabled", "hosts", len(hosts), "profile", profile.Name)
	return service, profile, nil
}

// startNodeController builds the FV session factory and the node controller
// that turns a successful login into one loopback gate per usable server.
// Construction performs no I/O: gates bind only when SyncAccount runs after a
// login. A nil panel (Resin disabled) still creates nodes; only the reconcile
// step is skipped.
func startNodeController(st *store.Store, logger *slog.Logger, panel api.Panel, logins runtime.LoginResults, profile account.VersionProfile) (*runtime.NodeController, error) {
	factory := account.NewFactory(profile, &fvfacade.HTTPUpgrade{}, fvfacade.AuthOptions{})
	var reconciler runtime.Reconciler
	if panel != nil {
		reconciler = panel
	}
	controller, err := runtime.NewNodeController(runtime.NodeControllerConfig{
		Store:      st,
		Logins:     logins,
		Sessions:   factory,
		Reconciler: reconciler,
		Profile:    profile,
		Logger:     logger,
	})
	if err != nil {
		return nil, fmt.Errorf("node controller: %w", err)
	}
	logger.Info("node_controller_enabled", "profile", profile.Name, "reconcile", reconciler != nil)
	return controller, nil
}

// loginProfile maps AJS_ACCOUNT_PROFILE to a version profile. Only the tested
// Linux 4.2.3.0 profile is accepted; an unknown value fails startup instead of
// silently logging in as a different client.
func loginProfile(name string) (account.VersionProfile, error) {
	switch strings.TrimSpace(name) {
	case "", "linux-4.2.3.0", account.Linux423Binary.Name:
		return account.Linux423Binary, nil
	default:
		return account.VersionProfile{}, fmt.Errorf("AJS_ACCOUNT_PROFILE %q is not supported (want linux-4.2.3.0)", name)
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
