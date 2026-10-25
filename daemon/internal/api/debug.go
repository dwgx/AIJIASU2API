package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/runtime"
)

const (
	// debugTraceDefaultLimit is the page size when ?limit is absent.
	debugTraceDefaultLimit = 200
	// debugTraceMaxLimit is one ring's capacity: a page never exceeds it.
	debugTraceMaxLimit = runtime.DebugRingCapacity
	// debugStreamBuffer is the live-channel depth of one SSE request. A slow
	// client loses entries into the ring's Dropped counter instead of stalling
	// the runtime.
	debugStreamBuffer = 64
	// debugStreamHeartbeat is the SSE comment interval. A proxy that sees no
	// bytes for 60 s closes an idle stream, so 15 s leaves room for one missed
	// tick.
	debugStreamHeartbeat = 15 * time.Second
)

// handleDebugTrace serves GET /v1/debug/trace?account=&limit=.
//
// The ring is the runtime's bounded in-memory trace: switch steps with
// timings, gate open/close, session open/lose, lease create/revoke and slot
// state transitions, each attributed to its account, node, slot and generation
// and ordered by a monotonic sequence. Entries are redacted before they enter
// the ring; there is no field for a SID, cookie, token or device identity.
//
// The ring survives no restart: a restarted daemon starts at sequence 1 with an
// empty window. A scoped key needs catalog:read; a key restricted by
// accountIds/regions sees only its accounts (and an ?account= outside the
// restriction is 403).
func (s *Server) handleDebugTrace(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Debug == nil {
		writeError(w, http.StatusServiceUnavailable, CodeUpstreamUnavailable,
			"debug trace is not configured")
		return
	}
	account, ok := s.debugAccountFilter(w, r)
	if !ok {
		return
	}
	limit, ok := parseDebugLimit(w, r.URL.Query().Get("limit"))
	if !ok {
		return
	}
	page := s.cfg.Debug.Snapshot(account, 0)
	visible, restricted := s.visibleAccountIDs(r)
	page.Entries = filterDebugEntries(page.Entries, account, visible, restricted)
	if limit > 0 && len(page.Entries) > limit {
		page.Entries = page.Entries[len(page.Entries)-limit:]
	}
	page.Retained = len(page.Entries)
	if page.Entries == nil {
		page.Entries = []model.DebugTrace{}
	}
	writeJSON(w, http.StatusOK, page)
}

// handleDebugStream serves GET /v1/debug/stream as server-sent events.
//
// Order is replay, subscribe, replay the gap, then stream live. Subscribing
// before the first replay means a long replay cannot overflow the subscriber
// buffer; a live event that duplicates one already replayed is skipped by
// sequence. The cursor comes from Last-Event-ID (sent by a browser on
// reconnect) or ?since; the header wins. A cursor that has been evicted, or
// that belongs to a previous process, restarts from the oldest retained entry
// and sets X-Debug-Restarted: true instead of failing the stream.
func (s *Server) handleDebugStream(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Debug == nil {
		writeError(w, http.StatusServiceUnavailable, CodeUpstreamUnavailable,
			"debug trace is not configured")
		return
	}
	account, ok := s.debugAccountFilter(w, r)
	if !ok {
		return
	}
	since, ok := parseDebugSince(w, r)
	if !ok {
		return
	}
	rc := http.NewResponseController(w)
	live, cancel := s.cfg.Debug.Subscribe(debugStreamBuffer)
	defer cancel()
	replay, restarted := s.cfg.Debug.Since(since)
	visible, restricted := s.visibleAccountIDs(r)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	// The stream outlives the server-wide WriteTimeout (120 s); clearing the
	// deadline is what lets one connection stay open for hours.
	_ = rc.SetWriteDeadline(time.Time{})
	if restarted {
		w.Header().Set("X-Debug-Restarted", "true")
	}
	w.WriteHeader(http.StatusOK)

	last := since
	if restarted {
		// A cursor ahead of the ring (previous process) would otherwise skip
		// every replayed entry; restart the client from the oldest window.
		last = 0
	}
	for _, trace := range replay {
		if trace.Seq <= last {
			continue
		}
		last = trace.Seq
		if !debugEntryAllowed(trace, account, visible, restricted) {
			continue
		}
		if err := writeDebugFrame(w, trace); err != nil {
			return
		}
	}
	if err := rc.Flush(); err != nil {
		return
	}

	heartbeat := time.NewTicker(debugStreamHeartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case trace, ok := <-live:
			if !ok {
				return
			}
			if trace.Seq <= last {
				continue
			}
			last = trace.Seq
			if !debugEntryAllowed(trace, account, visible, restricted) {
				continue
			}
			if err := writeDebugFrame(w, trace); err != nil {
				return
			}
			if err := rc.Flush(); err != nil {
				return
			}
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			if err := rc.Flush(); err != nil {
				return
			}
		}
	}
}

// debugAccountFilter resolves ?account and enforces the key restriction. An
// empty filter is allowed; a restricted key may name only an account it can
// already read, so a probe for a foreign account is 403 rather than an empty
// page.
func (s *Server) debugAccountFilter(w http.ResponseWriter, r *http.Request) (string, bool) {
	account := strings.TrimSpace(r.URL.Query().Get("account"))
	if account == "" {
		return "", true
	}
	key, ok := scopedKeyFromRequest(r)
	if ok && (len(key.AccountIDs) > 0 || len(key.Regions) > 0) {
		if !s.keyAllowsAccountID(key, account) {
			writeError(w, http.StatusForbidden, CodeForbidden,
				"scoped key is not allowed to read this account")
			return "", false
		}
	}
	return account, true
}

// filterDebugEntries applies the account filter and the key restriction to one
// retained window. An entry with no account is hidden from a restricted key:
// the key's grant names accounts, and an unattributed event is not one of them.
func filterDebugEntries(entries []model.DebugTrace, account string, visible map[string]struct{}, restricted bool) []model.DebugTrace {
	out := entries[:0]
	for _, trace := range entries {
		if debugEntryAllowed(trace, account, visible, restricted) {
			out = append(out, trace)
		}
	}
	return out
}

func debugEntryAllowed(trace model.DebugTrace, account string, visible map[string]struct{}, restricted bool) bool {
	if account != "" && trace.AccountID != account {
		return false
	}
	if !restricted {
		return true
	}
	_, ok := visible[trace.AccountID]
	return ok
}

// parseDebugLimit reads ?limit. Absent means the default; 0 means every
// retained entry; a negative or non-integer value is a client error; a value
// above the ring capacity is clamped.
func parseDebugLimit(w http.ResponseWriter, raw string) (int, bool) {
	if raw == "" {
		return debugTraceDefaultLimit, true
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 0 {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument,
			"limit must be a non-negative integer")
		return 0, false
	}
	if limit > debugTraceMaxLimit {
		limit = debugTraceMaxLimit
	}
	return limit, true
}

// parseDebugSince reads Last-Event-ID or ?since. The header wins, matching the
// customer event stream. Both the bare sequence ("42") and the SSE id form
// ("evt-000042") are accepted; an empty value starts at the oldest retained
// entry.
func parseDebugSince(w http.ResponseWriter, r *http.Request) (uint64, bool) {
	raw := strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	if raw == "" {
		raw = strings.TrimSpace(r.URL.Query().Get("since"))
	}
	if raw == "" {
		return 0, true
	}
	raw = strings.TrimPrefix(raw, "evt-")
	seq, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument,
			"since must be a non-negative sequence number")
		return 0, false
	}
	return seq, true
}

// writeDebugFrame writes one SSE frame. The data is the trace JSON object;
// json.Marshal escapes newlines, and sseField strips CR/LF from the event name
// so a malformed kind cannot inject a second field.
func writeDebugFrame(w http.ResponseWriter, trace model.DebugTrace) error {
	data, err := json.Marshal(trace)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", trace.Seq, sseField(trace.Kind), data)
	return err
}
