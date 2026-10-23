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
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"ajsaccountd/internal/account"
	"ajsaccountd/internal/api"
	"ajsaccountd/internal/lablog"
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
	flag.Parse()

	// S4: AJS_ALLOW_INSECURE=1 must stay on loopback. Without this guard a
	// single env var turns the loopback control plane into an unauthenticated
	// network service (state-changing POSTs are CSRF-able there).
	if *insecure {
		if err := requireLoopback(*listen); err != nil {
			return err
		}
	}
	// S2: a placeholder token is a working admin API whose value is printed in
	// the repository — including the one .env.example ships — so startup
	// refuses those values. The insecure lab mode is the explicit opt-out.
	if err := validateAdminToken(*token, *insecure); err != nil {
		return err
	}

	// The Lab ring is a bounded, redacted copy of the same log stream. It is
	// teed in front of the console handler so AJS_LOG_LEVEL still controls
	// stdout while /v1/lab/logs can show recent lines on demand.
	logRing := lablog.NewRing(lablog.DefaultCapacity)
	logger := slog.New(logRing.Handler(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: parseLevel(*logLevel)})))
	slog.SetDefault(logger)
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
	var loginResults runtime.LoginResults
	var loginHook func(context.Context, string) error
	var accountDeleteHook func(context.Context, string) error
	var accountLogoutHook func(context.Context, string) error
	var nodeDrainHook func(context.Context, string, bool) error
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
		controller, err = startNodeController(st, logger, panel, service, profile, limit)
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
		accountLogoutHook = controller.StopAccount
		nodeDrainHook = func(ctx context.Context, nodeID string, drain bool) error {
			if drain {
				return controller.DrainNode(ctx, nodeID)
			}
			return controller.UndrainNode(ctx, nodeID)
		}
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

	server := api.New(api.Config{
		AdminToken:        *token,
		AllowInsecure:     *insecure,
		CORSOrigins:       splitList(*corsOrigins),
		Version:           api.DefaultVersion,
		Panel:             panel,
		LoginService:      loginService,
		LoginHook:         loginHook,
		AccountDeleteHook: accountDeleteHook,
		AccountLogoutHook: accountLogoutHook,
		NodeDrainHook:     nodeDrainHook,
		UpdateRepo:        *updateRepo,
		UpdateAPIBase:     *updateAPIBase,
		Lab:               labView,
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
	host := listen
	if parsed, _, err := net.SplitHostPort(listen); err == nil {
		host = parsed
	}
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if host == "" {
		return fmt.Errorf("AJS_ALLOW_INSECURE=1 needs a loopback AJS_LISTEN, got %q (an empty host binds every interface)", listen)
	}
	lower := strings.ToLower(host)
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("AJS_ALLOW_INSECURE=1 needs a loopback AJS_LISTEN (127.0.0.1, ::1 or localhost), got %q", listen)
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
// skeleton behaviour and discards them. The key itself is never logged.
func openStore(logger *slog.Logger) (*store.Store, *secret.Store, error) {
	raw := strings.TrimSpace(os.Getenv("AJS_MASTER_KEY"))
	if raw == "" {
		logger.Info("secrets_disabled", "reason", "AJS_MASTER_KEY is not set")
		return store.NewEmpty(), nil, nil
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
	return store.NewEmptyWithSealer(secrets), secrets, nil
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
// explicit 0 = all).
func startNodeController(st *store.Store, logger *slog.Logger, panel api.Panel, logins runtime.LoginResults, profile account.VersionProfile, limit int) (*runtime.NodeController, error) {
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
		MaxServers: limit,
		Logger:     logger,
	})
	if err != nil {
		return nil, fmt.Errorf("node controller: %w", err)
	}
	if limit > 0 {
		logger.Info("node_controller_enabled", "profile", profile.Name, "reconcile", reconciler != nil, "max_servers", limit)
	} else {
		logger.Info("node_controller_enabled", "profile", profile.Name, "reconcile", reconciler != nil, "max_servers", "all")
	}
	return controller, nil
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
