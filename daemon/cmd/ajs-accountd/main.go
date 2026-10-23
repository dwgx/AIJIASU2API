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

	"ajsaccountd/internal/api"
	"ajsaccountd/internal/publisher"
	"ajsaccountd/internal/resin"
	"ajsaccountd/internal/runtime"
	"ajsaccountd/internal/secret"
	"ajsaccountd/internal/store"
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

	server := api.New(api.Config{
		AdminToken:    *token,
		AllowInsecure: *insecure,
		CORSOrigins:   splitOrigins(*corsOrigins),
		Panel:         panel,
	}, st, logger)

	httpServer := &http.Server{
		Addr:              *listen,
		Handler:           server,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
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

func splitOrigins(value string) []string {
	parts := strings.Split(value, ",")
	origins := make([]string, 0, len(parts))
	for _, part := range parts {
		if origin := strings.TrimSpace(part); origin != "" {
			origins = append(origins, origin)
		}
	}
	return origins
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
