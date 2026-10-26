package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ajsaccountd/internal/runtime"
)

// ReadyCheck is a named readiness probe for an integration that has no health
// surface of its own. The handler applies a short deadline; Check must return
// promptly and must not log or return secrets.
type ReadyCheck struct {
	Name     string
	Required bool
	Check    func(context.Context) error
}

// readinessCheckView is one row of GET /readyz. Required checks decide the
// HTTP status; optional checks are diagnostic only.
type readinessCheckView struct {
	Name     string `json:"name"`
	OK       bool   `json:"ok"`
	Required bool   `json:"required"`
	Reason   string `json:"reason,omitempty"`
}

// readinessResponse is the GET /readyz contract. Unlike /healthz (process
// liveness), this endpoint answers 503 when a required dependency is not
// usable. Reasons are short, stable and never carry credentials or paths.
type readinessResponse struct {
	Status  string               `json:"status"`
	Service string               `json:"service"`
	Version string               `json:"version"`
	Time    string               `json:"time"`
	Checks  []readinessCheckView `json:"checks"`
	Reasons []string             `json:"reasons,omitempty"`
}

// handleReadyz serves GET /readyz: a bounded readiness probe over the state
// file and the configured integrations. It performs no vendor call of its
// own; a configured Resin panel is read from its last successful in-memory
// topology, and the optional ReadyChecks seam is expected to be bounded by
// the caller. Any failed required check answers 503 with the reason list.
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	checks := make([]readinessCheckView, 0, 2+len(s.cfg.ReadyChecks))
	checks = append(checks, s.readyStoreCheck(), s.readyStateCheck())
	checks = append(checks, s.readyIntegrationChecks(r.Context())...)

	reasons := make([]string, 0)
	for _, check := range checks {
		if check.Required && !check.OK {
			reason := check.Reason
			if reason == "" {
				reason = "check failed"
			}
			reasons = append(reasons, check.Name+": "+reason)
		}
	}

	status := "ready"
	code := http.StatusOK
	if len(reasons) > 0 {
		status = "not_ready"
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, readinessResponse{
		Status:  status,
		Service: "ajs-accountd",
		Version: s.cfg.Version,
		Time:    time.Now().UTC().Format(time.RFC3339Nano),
		Checks:  checks,
		Reasons: reasons,
	})
}

// readyStoreCheck proves the store is wired. The accessor is cheap and does
// not take a write lock; a nil store cannot serve any API route.
func (s *Server) readyStoreCheck() readinessCheckView {
	if s.store == nil {
		return readinessCheckView{Name: "store", Required: true, Reason: "store is not configured"}
	}
	_ = s.store.Accounts()
	return readinessCheckView{Name: "store", Required: true, OK: true}
}

// readyStateCheck proves the daemon can persist its snapshot. Save writes a
// temporary file in the state directory and renames it over state.json, so
// directory write access is the operation that matters. A path that is absent
// from Config is a readiness failure: a production daemon without persistence
// cannot honour its restart contract.
func (s *Server) readyStateCheck() readinessCheckView {
	path := s.stateFilePath()
	if path == "" {
		return readinessCheckView{Name: "state", Required: true, Reason: "state file is not configured"}
	}
	if err := probeStateWritable(path); err != nil {
		s.logger.Warn("readyz_state_not_writable", "path", path, "error", err)
		return readinessCheckView{Name: "state", Required: true, Reason: "state directory is not writable"}
	}
	return readinessCheckView{Name: "state", Required: true, OK: true}
}

// stateFilePath mirrors backupPath's state.json resolution: AJS_STATE_FILE
// first, then AJS_STATE_DIR/state.json.
func (s *Server) stateFilePath() string {
	if path := strings.TrimSpace(s.cfg.StatePath); path != "" {
		return path
	}
	if dir := strings.TrimSpace(s.cfg.StateDir); dir != "" {
		return filepath.Join(dir, "state.json")
	}
	return ""
}

// probeStateWritable verifies the same filesystem operation Save uses. The
// permission-bit check makes the result deterministic even when the process
// runs as root (root can bypass mode bits, but a 0555 state directory is still
// an operator error worth failing readiness for).
func probeStateWritable(path string) error {
	dir := filepath.Dir(path)
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("state directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("state directory is not a directory")
	}
	if info.Mode().Perm()&0o222 == 0 {
		return os.ErrPermission
	}
	if existing, err := os.Stat(path); err == nil && existing.IsDir() {
		return fmt.Errorf("state file path is a directory")
	}
	tmp, err := os.CreateTemp(dir, ".readyz-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	closeErr := tmp.Close()
	removeErr := os.Remove(name)
	if closeErr != nil {
		return closeErr
	}
	return removeErr
}

// readyIntegrationChecks reports the integrations this deployment has
// actually configured. Optional integrations that are absent produce no row;
// a configured Resin panel or a partially configured epay adapter is required
// and can fail readiness. Additional integrations can be supplied through
// Config.ReadyChecks.
func (s *Server) readyIntegrationChecks(ctx context.Context) []readinessCheckView {
	checks := make([]readinessCheckView, 0, 3+len(s.cfg.ReadyChecks))

	if s.cfg.Panel != nil {
		check := readinessCheckView{Name: "resin", Required: true}
		probeCtx, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
		_, err := s.cfg.Panel.Topology(probeCtx)
		cancel()
		switch {
		case err == nil:
			check.OK = true
		case errors.Is(err, runtime.ErrNotStarted):
			check.Reason = "resin panel has not completed a reconcile"
		default:
			check.Reason = "resin panel is unavailable"
		}
		checks = append(checks, check)
	}

	if s.cfg.Epay != nil && s.cfg.Epay.Configured() {
		check := readinessCheckView{Name: "epay", Required: true, OK: s.cfg.Epay.Enabled()}
		if !check.OK {
			check.Reason = "epay is configured but disabled: " + s.cfg.Epay.DisabledReason()
		}
		checks = append(checks, check)
	}

	if s.cfg.TenantPortal {
		check := readinessCheckView{
			Name:     "tenant_portal",
			Required: true,
			OK:       strings.TrimSpace(s.cfg.TenantOrigin) != "",
		}
		if !check.OK {
			check.Reason = "tenant portal origin is not configured"
		}
		checks = append(checks, check)
	}

	for _, custom := range s.cfg.ReadyChecks {
		name := strings.TrimSpace(custom.Name)
		if name == "" {
			name = "integration"
		}
		check := readinessCheckView{Name: name, Required: custom.Required}
		if custom.Check == nil {
			check.Reason = "readiness check is not configured"
			checks = append(checks, check)
			continue
		}
		probeCtx, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
		err := custom.Check(probeCtx)
		cancel()
		if err != nil {
			check.Reason = "check failed"
		} else {
			check.OK = true
		}
		checks = append(checks, check)
	}

	return checks
}
