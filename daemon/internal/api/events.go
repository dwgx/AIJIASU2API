package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ajsaccountd/internal/events"
)

// DefaultEventRingCapacity is the replay depth main gives the shared ring:
// 4096 events. The ring overwrites its oldest event when full, so a client
// that was away longer than the window gets CURSOR_EXPIRED and restarts from
// oldestCursor instead of silently skipping events.
const DefaultEventRingCapacity = 4096

const (
	// defaultEventsLimit is the page size when ?limit is absent.
	defaultEventsLimit = 100
	// maxEventsLimit bounds one replay page. A larger ?limit is clamped, not
	// refused, so a client asking for "everything" still gets a valid page.
	maxEventsLimit = 500
	// maxEventsWait caps a long poll. It stays below the 120 s server
	// WriteTimeout so a poll always answers on its own.
	maxEventsWait = 25 * time.Second
	// eventsSubscriberBuffer is the live-channel depth of one request. A slow
	// long poll loses events into Dropped instead of stalling the publisher;
	// the next request replays them from the ring.
	eventsSubscriberBuffer = 64
	// eventsHeartbeat is the SSE comment interval. A proxy that sees no bytes
	// for 60 s closes an idle stream, so 15 s leaves room for one missed tick.
	eventsHeartbeat = 15 * time.Second
	// codeCursorExpired is the CURSOR_EXPIRED contract code. It lives here
	// rather than with the transport codes because only the event routes use
	// it: the cursor names a position in a bounded ring, not a bad request.
	codeCursorExpired = "CURSOR_EXPIRED"
)

// eventsPage is the GET /v1/events body. Events is never null: an empty page
// is [], so a client can iterate without a nil check.
type eventsPage struct {
	Events       []events.Event `json:"events"`
	NextCursor   string         `json:"nextCursor"`
	OldestCursor string         `json:"oldestCursor"`
	Dropped      uint64         `json:"dropped"`
}

// eventsRing returns the configured ring or answers 503. A daemon always wires
// one; a test server without it says so instead of pretending to stream.
func (s *Server) eventsRing(w http.ResponseWriter) (*events.Ring, bool) {
	if s.cfg.EventRing == nil {
		writeError(w, http.StatusServiceUnavailable, CodeUpstreamUnavailable,
			"event stream is not configured")
		return nil, false
	}
	return s.cfg.EventRing, true
}

// eventsKeyAllowed refuses every scoped key that is not an admin principal.
// The ring is global: an event is not attributed to exactly one account (an
// audit row or a schedule run can span accounts) and the stream carries every
// tenant's order, lease and schedule metadata, so a per-account filter would be
// guesswork and a per-user one would still leak operator telemetry. The stream
// is therefore operator-global: the admin token and keys carrying the admin
// scope pass, and every other scoped key answers 403 admin_required whatever
// its events:read or account grant says.
func (s *Server) eventsKeyAllowed(w http.ResponseWriter, r *http.Request) bool {
	return !s.denyOperatorOnly(w, r, "the event stream")
}

// handleListEvents serves GET /v1/events?since=&limit=&wait=.
//
// The replay is the ring's: ordered, bounded and never silently backwards. A
// cursor that is current with ?wait= holds the request until the next event or
// the wait budget, whichever comes first. The handler subscribes before it
// reads the ring, so an event published in between is on the channel and the
// long poll cannot miss the transition it is waiting for.
func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	ring, ok := s.eventsRing(w)
	if !ok {
		return
	}
	if !s.eventsKeyAllowed(w, r) {
		return
	}
	query := r.URL.Query()
	since := strings.TrimSpace(query.Get("since"))
	limit, ok := parseEventsLimit(w, query.Get("limit"))
	if !ok {
		return
	}
	wait, ok := parseEventsWait(w, query.Get("wait"))
	if !ok {
		return
	}

	live, cancel := ring.Subscribe(eventsSubscriberBuffer)
	defer cancel()

	page, err := eventsSince(ring, since, limit)
	if err != nil {
		s.writeEventsCursorError(w, ring, since, err)
		return
	}
	if len(page.Events) > 0 || wait <= 0 {
		writeJSON(w, http.StatusOK, page)
		return
	}

	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-live:
		// Re-read the ring rather than returning the live event alone: the
		// replay window and the live channel overlap by design, and the ring
		// is the single ordering authority. The cursor moved, so this call
		// returns at least one event.
		page, err = eventsSince(ring, since, limit)
		if err != nil {
			s.writeEventsCursorError(w, ring, since, err)
			return
		}
		writeJSON(w, http.StatusOK, page)
	case <-timer.C:
		// No event within the budget: an empty page with the cursor unchanged
		// tells the client to poll again.
		writeJSON(w, http.StatusOK, page)
	case <-r.Context().Done():
		// The client is gone; nothing can be written and nothing is owed.
	}
}

// handleEventsStream serves GET /v1/events/stream as server-sent events.
//
// Order is replay, subscribe, replay the gap, then stream live. Subscribing
// after the first replay means a long replay cannot overflow the subscriber
// buffer, and reading the ring again after subscribing means an event
// published during the replay is delivered exactly once: the live channel may
// repeat an event already replayed, so those IDs are skipped.
func (s *Server) handleEventsStream(w http.ResponseWriter, r *http.Request) {
	ring, ok := s.eventsRing(w)
	if !ok {
		return
	}
	if !s.eventsKeyAllowed(w, r) {
		return
	}
	// The SSE reconnect header wins over ?since: a browser sends the id it
	// actually received last.
	cursor := strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	if cursor == "" {
		cursor = strings.TrimSpace(r.URL.Query().Get("since"))
	}
	// Validate before the status line, so a cursor failure is still a JSON
	// error body the client can read.
	replay, next, err := ring.Since(cursor, 0)
	if err != nil {
		s.writeEventsCursorError(w, ring, cursor, err)
		return
	}

	rc := http.NewResponseController(w)
	// The stream outlives the server-wide WriteTimeout (120 s); clearing the
	// deadline is what lets one connection stay open for hours.
	_ = rc.SetWriteDeadline(time.Time{})
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if err := rc.Flush(); err != nil {
		return
	}

	writeFrame := func(event events.Event) error {
		payload, err := json.Marshal(event)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n",
			sseField(event.ID), sseField(event.Type), payload); err != nil {
			return err
		}
		return rc.Flush()
	}

	// skip holds the IDs already written from the replay windows. The live
	// channel repeats them, and the set is dropped once the first genuinely
	// new event arrives (publish order guarantees no replayed id follows).
	skip := make(map[string]struct{}, len(replay))
	for _, event := range replay {
		if err := writeFrame(event); err != nil {
			return
		}
		skip[event.ID] = struct{}{}
	}

	live, cancel := ring.Subscribe(eventsSubscriberBuffer)
	defer cancel()
	gap, _, err := ring.Since(next, 0)
	if err != nil {
		return
	}
	for _, event := range gap {
		if err := writeFrame(event); err != nil {
			return
		}
		skip[event.ID] = struct{}{}
	}

	heartbeat := time.NewTicker(eventsHeartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
				return
			}
			if err := rc.Flush(); err != nil {
				return
			}
		case event, open := <-live:
			if !open {
				// The ring closed (daemon shutdown); end the stream so the
				// client reconnects against the restarted daemon.
				return
			}
			if _, duplicate := skip[event.ID]; duplicate {
				continue
			}
			skip = nil
			if err := writeFrame(event); err != nil {
				return
			}
		}
	}
}

// eventsSince reads one page and decorates it with the ring's restart point
// and lag signal.
func eventsSince(ring *events.Ring, cursor string, limit int) (eventsPage, error) {
	batch, next, err := ring.Since(cursor, limit)
	if err != nil {
		return eventsPage{}, err
	}
	if batch == nil {
		batch = []events.Event{}
	}
	return eventsPage{
		Events:       batch,
		NextCursor:   next,
		OldestCursor: ring.Oldest(),
		Dropped:      ring.Dropped(),
	}, nil
}

// parseEventsLimit reads ?limit. Absent means the default; a non-positive or
// non-integer value is a client error; a value above the maximum is clamped.
func parseEventsLimit(w http.ResponseWriter, raw string) (int, bool) {
	if raw == "" {
		return defaultEventsLimit, true
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "limit must be a positive integer")
		return 0, false
	}
	if limit > maxEventsLimit {
		limit = maxEventsLimit
	}
	return limit, true
}

// parseEventsWait reads ?wait as a Go duration ("25s", "500ms"). Absent means
// no long poll; a negative or malformed value is a client error; a value above
// the cap is clamped so every poll answers before the WriteTimeout.
func parseEventsWait(w http.ResponseWriter, raw string) (time.Duration, bool) {
	if raw == "" {
		return 0, true
	}
	wait, err := time.ParseDuration(raw)
	if err != nil || wait < 0 {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument,
			"wait must be a non-negative duration such as 25s")
		return 0, false
	}
	if wait > maxEventsWait {
		wait = maxEventsWait
	}
	return wait, true
}

// writeEventsCursorError maps a ring cursor failure onto the contract: a
// malformed cursor is 400, a well-formed but evicted one is 410 CURSOR_EXPIRED
// carrying the oldest retained cursor so the client restarts without guessing.
func (s *Server) writeEventsCursorError(w http.ResponseWriter, ring *events.Ring, cursor string, err error) {
	switch {
	case errors.Is(err, events.ErrBadCursor):
		writeError(w, http.StatusBadRequest, CodeInvalidArgument,
			fmt.Sprintf("invalid cursor %q: expected a retained event id", cursor))
	case errors.Is(err, events.ErrCursorExpired):
		writeJSON(w, http.StatusGone, errorResponse{Error: errorDetail{
			Code:    codeCursorExpired,
			Message: "cursor is no longer retained; restart from oldestCursor",
			Details: map[string]string{"oldestCursor": ring.Oldest()},
		}})
	default:
		s.logger.Error("events_cursor_failed", "error", err)
		writeError(w, http.StatusInternalServerError, CodeInternal, "internal error")
	}
}

// sseField strips CR/LF from a frame field. Event ids are generated digits and
// types come from the webhook catalogue, but a caller-supplied id must never be
// able to inject a second SSE field.
func sseField(value string) string {
	if !strings.ContainsAny(value, "\r\n") {
		return value
	}
	return strings.NewReplacer("\r", "", "\n", "").Replace(value)
}
