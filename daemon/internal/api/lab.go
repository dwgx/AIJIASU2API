package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ajsaccountd/internal/lablog"
	"ajsaccountd/internal/model"
	"ajsaccountd/internal/runtime"
	"ajsaccountd/internal/store"
)

// LabReader is the read-only Lab telemetry the API serves. *runtime.LabView
// implements it; a nil Config.Lab keeps every lab route registered but answers
// an enabled:false body, so a test server or a partial build never 404s the
// dashboard.
type LabReader interface {
	Overview(ctx context.Context) model.LabOverview
	Flags() model.FlagsView
	Gates() ([]model.GateView, string)
	Flows(limit int) (model.FlowsView, string)
	Sessions() ([]model.SessionView, string)
	Egress() ([]model.EgressView, string)
	CheckEgress(ctx context.Context, nodeID string) (model.EgressCheckView, error)
	Profiles() model.ProfilesView
	Logs(query lablog.Query) model.LogsView
	Diagnostics() model.DiagnosticsView
	Servers(ctx context.Context, accountID string) (model.ServersView, error)
	Device(accountID string) (model.DeviceView, error)
}

// labLogDefaultLimit and labLogMaxLimit bound GET /v1/lab/logs.
const (
	labLogDefaultLimit = 200
	labLogMaxLimit     = 2000
)

// labFlowDefaultLimit and labFlowMaxLimit bound GET /v1/lab/flows. The max
// matches one gate's ring capacity.
const (
	labFlowDefaultLimit = 200
	labFlowMaxLimit     = 500
)

// labUnavailable is the reason every route uses when no LabReader is wired.
const labUnavailable = "lab telemetry is not configured"

func (s *Server) handleLabOverview(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Lab == nil {
		writeJSON(w, http.StatusOK, model.LabOverview{
			Enabled:     false,
			Reason:      labUnavailable,
			GeneratedAt: time.Now().UTC().Format(time.RFC3339),
			Warnings:    []model.LabWarning{},
		})
		return
	}
	writeJSON(w, http.StatusOK, s.cfg.Lab.Overview(r.Context()))
}

func (s *Server) handleLabFlags(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Lab == nil {
		writeJSON(w, http.StatusOK, model.FlagsView{Enabled: false, Reason: labUnavailable, Flags: []model.FlagView{}})
		return
	}
	view := s.cfg.Lab.Flags()
	if view.Flags == nil {
		view.Flags = []model.FlagView{}
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleLabGates(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Lab == nil {
		writeJSON(w, http.StatusOK, model.LabDisabled{Enabled: false, Reason: labUnavailable})
		return
	}
	gates, reason := s.cfg.Lab.Gates()
	if reason != "" {
		writeJSON(w, http.StatusOK, model.LabDisabled{Enabled: false, Reason: reason})
		return
	}
	if gates == nil {
		gates = []model.GateView{}
	}
	writeJSON(w, http.StatusOK, gates)
}

func (s *Server) handleLabFlows(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Lab == nil {
		writeJSON(w, http.StatusOK, model.FlowsView{
			Enabled: false,
			Reason:  labUnavailable,
			Flows:   []model.FlowView{},
		})
		return
	}
	limit, ok := parseLabFlowLimit(w, r)
	if !ok {
		return
	}
	view, reason := s.cfg.Lab.Flows(limit)
	if reason != "" {
		writeJSON(w, http.StatusOK, model.FlowsView{
			Enabled: false,
			Reason:  reason,
			Flows:   []model.FlowView{},
		})
		return
	}
	if view.Flows == nil {
		view.Flows = []model.FlowView{}
	}
	writeJSON(w, http.StatusOK, view)
}

// parseLabFlowLimit validates ?limit=. An invalid value answers 400 instead
// of silently widening the window, matching the logs endpoint.
func parseLabFlowLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return labFlowDefaultLimit, true
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 || limit > labFlowMaxLimit {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument,
			fmt.Sprintf("limit must be an integer between 1 and %d", labFlowMaxLimit))
		return 0, false
	}
	return limit, true
}

func (s *Server) handleLabSessions(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Lab == nil {
		writeJSON(w, http.StatusOK, model.LabDisabled{Enabled: false, Reason: labUnavailable})
		return
	}
	sessions, reason := s.cfg.Lab.Sessions()
	if reason != "" {
		writeJSON(w, http.StatusOK, model.LabDisabled{Enabled: false, Reason: reason})
		return
	}
	if sessions == nil {
		sessions = []model.SessionView{}
	}
	writeJSON(w, http.StatusOK, sessions)
}

func (s *Server) handleLabEgress(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Lab == nil {
		writeJSON(w, http.StatusOK, model.LabDisabled{Enabled: false, Reason: labUnavailable})
		return
	}
	egress, reason := s.cfg.Lab.Egress()
	if reason != "" {
		writeJSON(w, http.StatusOK, model.LabDisabled{Enabled: false, Reason: reason})
		return
	}
	if egress == nil {
		egress = []model.EgressView{}
	}
	writeJSON(w, http.StatusOK, egress)
}

// handleLabEgressCheck performs the guarded probe. The body is
// {"nodeId":"..."}: a missing/empty id is a client error, a node without a
// running gate is 409, and a node checked less than the cooldown ago is 429.
// A probe that cannot observe an address still answers 200 with error set,
// because the attempt itself is the record the dashboard renders.
func (s *Server) handleLabEgressCheck(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Lab == nil {
		writeError(w, http.StatusConflict, CodeConflict, labUnavailable)
		return
	}
	var body struct {
		NodeID string `json:"nodeId"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	body.NodeID = strings.TrimSpace(body.NodeID)
	if body.NodeID == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "nodeId is required")
		return
	}
	view, err := s.cfg.Lab.CheckEgress(r.Context(), body.NodeID)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, view)
	case errors.Is(err, runtime.ErrEgressNoGate):
		writeError(w, http.StatusConflict, CodeConflict, err.Error())
	case errors.Is(err, runtime.ErrEgressRateLimited):
		writeError(w, http.StatusTooManyRequests, CodeQuotaExceeded, err.Error())
	default:
		s.logger.Warn("lab_egress_check_failed", "node_id", body.NodeID, "error", err)
		writeError(w, http.StatusInternalServerError, CodeInternal, "internal error")
	}
}

func (s *Server) handleLabProfiles(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Lab == nil {
		writeJSON(w, http.StatusOK, model.ProfilesView{
			Enabled:   false,
			Reason:    labUnavailable,
			Supported: []model.ProfileView{},
			Samples:   []model.ProfileSample{},
		})
		return
	}
	view := s.cfg.Lab.Profiles()
	if view.Supported == nil {
		view.Supported = []model.ProfileView{}
	}
	if view.Samples == nil {
		view.Samples = []model.ProfileSample{}
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleLabLogs(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Lab == nil {
		writeJSON(w, http.StatusOK, model.LogsView{Enabled: false, Reason: labUnavailable, Entries: []model.LogEntry{}})
		return
	}
	query, ok := parseLabLogQuery(w, r)
	if !ok {
		return
	}
	view := s.cfg.Lab.Logs(query)
	if view.Entries == nil {
		view.Entries = []model.LogEntry{}
	}
	writeJSON(w, http.StatusOK, view)
}

// parseLabLogQuery validates ?limit=&level=&component=&since=. An invalid
// value answers 400 instead of silently widening the filter, so the operator
// never believes a filtered view is complete.
func parseLabLogQuery(w http.ResponseWriter, r *http.Request) (lablog.Query, bool) {
	values := r.URL.Query()
	query := lablog.Query{
		Level:     slog.LevelDebug,
		Component: strings.TrimSpace(values.Get("component")),
		Limit:     labLogDefaultLimit,
	}
	if raw := strings.TrimSpace(values.Get("limit")); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > labLogMaxLimit {
			writeError(w, http.StatusBadRequest, CodeInvalidArgument,
				fmt.Sprintf("limit must be an integer between 1 and %d", labLogMaxLimit))
			return lablog.Query{}, false
		}
		query.Limit = limit
	}
	if raw := strings.TrimSpace(values.Get("level")); raw != "" {
		level, ok := parseLogLevel(raw)
		if !ok {
			writeError(w, http.StatusBadRequest, CodeInvalidArgument, "level must be one of debug, info, warn, error")
			return lablog.Query{}, false
		}
		query.Level = level
	}
	if raw := strings.TrimSpace(values.Get("since")); raw != "" {
		since, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, CodeInvalidArgument, "since must be an RFC 3339 timestamp")
			return lablog.Query{}, false
		}
		query.Since = since
	}
	return query, true
}

func parseLogLevel(value string) (slog.Level, bool) {
	switch strings.ToLower(value) {
	case "debug":
		return slog.LevelDebug, true
	case "info":
		return slog.LevelInfo, true
	case "warn", "warning":
		return slog.LevelWarn, true
	case "error":
		return slog.LevelError, true
	default:
		return slog.LevelInfo, false
	}
}

func (s *Server) handleLabDiagnostics(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Lab == nil {
		writeJSON(w, http.StatusOK, model.DiagnosticsView{
			Enabled:  false,
			Reason:   labUnavailable,
			Health:   map[string]string{},
			Warnings: []model.LabWarning{},
		})
		return
	}
	view := s.cfg.Lab.Diagnostics()
	if view.Health == nil {
		view.Health = map[string]string{}
	}
	if view.Warnings == nil {
		view.Warnings = []model.LabWarning{}
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleAccountServers(w http.ResponseWriter, r *http.Request) {
	accountID := pathParam(r, "id")
	if s.cfg.Lab == nil {
		writeJSON(w, http.StatusOK, model.ServersView{
			Enabled:   false,
			Reason:    labUnavailable,
			AccountID: accountID,
			Stale:     true,
			Servers:   []model.ServerView{},
		})
		return
	}
	view, err := s.cfg.Lab.Servers(r.Context(), accountID)
	if err != nil {
		s.writeLabStoreError(w, err)
		return
	}
	if view.Servers == nil {
		view.Servers = []model.ServerView{}
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleAccountDevice(w http.ResponseWriter, r *http.Request) {
	accountID := pathParam(r, "id")
	if s.cfg.Lab == nil {
		writeJSON(w, http.StatusOK, model.DeviceView{
			Enabled:   false,
			Reason:    labUnavailable,
			AccountID: accountID,
			Hashes:    map[string]string{},
			Claims:    map[string]string{},
		})
		return
	}
	view, err := s.cfg.Lab.Device(accountID)
	if err != nil {
		s.writeLabStoreError(w, err)
		return
	}
	if view.Hashes == nil {
		view.Hashes = map[string]string{}
	}
	if view.Claims == nil {
		view.Claims = map[string]string{}
	}
	writeJSON(w, http.StatusOK, view)
}

// writeLabStoreError maps a lab read failure. Only a missing account is
// client-visible; every other failure stays a generic 500 so an internal path
// or dependency cannot leak through the response.
func (s *Server) writeLabStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		s.writeStoreError(w, err)
		return
	}
	s.logger.Warn("lab_read_failed", "error", err)
	writeError(w, http.StatusInternalServerError, CodeInternal, "internal error")
}
