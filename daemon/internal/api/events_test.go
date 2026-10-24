package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ajsaccountd/internal/events"
	"ajsaccountd/internal/model"
)

// newEventsServer builds a test server whose replay ring has the given
// capacity. The returned ring is the same one the handlers read.
func newEventsServer(t *testing.T, capacity int) (*httptest.Server, *events.Ring) {
	t.Helper()
	ring := events.NewRing(capacity)
	t.Cleanup(ring.Close)
	ts, _ := newPlatformServer(t, Config{EventRing: ring})
	return ts, ring
}

// openEventsStream performs the request and returns once the response headers
// arrive. The caller owns resp.Body.
func openEventsStream(t *testing.T, ts *httptest.Server, path, secret string, header map[string]string) *http.Response {
	t.Helper()
	req := newRequest(t, http.MethodGet, ts.URL+path, nil)
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	for name, value := range header {
		req.Header.Set(name, value)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

type sseFrame struct {
	id    string
	event string
	data  string
}

// readSSEFrame reads one id/event/data frame, skipping comment lines.
func readSSEFrame(t *testing.T, reader *bufio.Reader) sseFrame {
	t.Helper()
	frame := sseFrame{}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read SSE frame: %v", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if frame != (sseFrame{}) {
				return frame
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "id":
			frame.id = value
		case "event":
			frame.event = value
		case "data":
			frame.data = value
		}
	}
}

func decodeFrameEvent(t *testing.T, frame sseFrame) events.Event {
	t.Helper()
	var event events.Event
	if err := json.Unmarshal([]byte(frame.data), &event); err != nil {
		t.Fatalf("decode SSE data %q: %v", frame.data, err)
	}
	if frame.id != event.ID || frame.event != event.Type {
		t.Fatalf("frame id/event = %q/%q, data carries %q/%q", frame.id, frame.event, event.ID, event.Type)
	}
	return event
}

func TestListEventsReplaysOrderedWithCursor(t *testing.T) {
	ts, ring := newEventsServer(t, 8)
	for _, eventType := range []string{"account.login", "node.connected", "schedule.fired"} {
		ring.Publish(events.Event{Type: eventType})
	}

	resp, body := apiCall(t, ts, http.MethodGet, "/v1/events?limit=2", nil)
	requireStatus(t, resp, http.StatusOK)
	page := decodeBody[eventsPage](t, body)
	if len(page.Events) != 2 {
		t.Fatalf("events = %d, want 2 (body %s)", len(page.Events), body)
	}
	if page.Events[0].ID != "evt-000001" || page.Events[1].ID != "evt-000002" {
		t.Fatalf("page ids = %q, %q", page.Events[0].ID, page.Events[1].ID)
	}
	if page.Events[0].Type != "account.login" || page.Events[1].Type != "node.connected" {
		t.Fatalf("page order = %q, %q", page.Events[0].Type, page.Events[1].Type)
	}
	if page.NextCursor != "evt-000002" {
		t.Fatalf("nextCursor = %q, want evt-000002", page.NextCursor)
	}
	if page.OldestCursor != "evt-000001" {
		t.Fatalf("oldestCursor = %q, want evt-000001", page.OldestCursor)
	}
	if page.Dropped != 0 {
		t.Fatalf("dropped = %d, want 0", page.Dropped)
	}

	// The returned cursor resumes exactly where the page stopped.
	resp, body = apiCall(t, ts, http.MethodGet, "/v1/events?since="+page.NextCursor, nil)
	requireStatus(t, resp, http.StatusOK)
	next := decodeBody[eventsPage](t, body)
	if len(next.Events) != 1 || next.Events[0].ID != "evt-000003" {
		t.Fatalf("resume page = %+v, want evt-000003", next.Events)
	}
	if next.NextCursor != "evt-000003" {
		t.Fatalf("resume nextCursor = %q", next.NextCursor)
	}

	// A current cursor answers an empty, non-null page with the cursor kept.
	resp, body = apiCall(t, ts, http.MethodGet, "/v1/events?since=evt-000003", nil)
	requireStatus(t, resp, http.StatusOK)
	current := decodeBody[eventsPage](t, body)
	if current.Events == nil || len(current.Events) != 0 {
		t.Fatalf("current page events = %#v, want []", current.Events)
	}
	if current.NextCursor != "evt-000003" {
		t.Fatalf("current nextCursor = %q, want evt-000003", current.NextCursor)
	}
	if !strings.Contains(string(body), `"events":[]`) {
		t.Fatalf("empty page is not an empty array: %s", body)
	}

	// limit is clamped to the maximum instead of being refused.
	resp, body = apiCall(t, ts, http.MethodGet, "/v1/events?limit=999", nil)
	requireStatus(t, resp, http.StatusOK)
	if got := len(decodeBody[eventsPage](t, body).Events); got != 3 {
		t.Fatalf("clamped limit returned %d events, want 3", got)
	}
}

func TestListEventsRejectsBadQuery(t *testing.T) {
	ts, _ := newEventsServer(t, 8)
	for _, path := range []string{
		"/v1/events?limit=0",
		"/v1/events?limit=-1",
		"/v1/events?limit=abc",
		"/v1/events?wait=soon",
		"/v1/events?wait=-1s",
	} {
		resp, body := apiCall(t, ts, http.MethodGet, path, nil)
		requireStatus(t, resp, http.StatusBadRequest)
		requireErrorCode(t, body, CodeInvalidArgument)
	}
}

func TestListEventsCursorErrors(t *testing.T) {
	ts, ring := newEventsServer(t, 2)
	for range 3 {
		ring.Publish(events.Event{Type: "account.login"})
	}

	// evt-000001 was evicted, so it is well formed but no longer replayable.
	resp, body := apiCall(t, ts, http.MethodGet, "/v1/events?since=evt-000001", nil)
	requireStatus(t, resp, http.StatusGone)
	var expired errorResponse
	if err := json.Unmarshal(body, &expired); err != nil {
		t.Fatalf("decode 410 body %q: %v", body, err)
	}
	if expired.Error.Code != codeCursorExpired {
		t.Fatalf("code = %q, want %q", expired.Error.Code, codeCursorExpired)
	}
	if got := expired.Error.Details["oldestCursor"]; got != "evt-000002" {
		t.Fatalf("details.oldestCursor = %q, want evt-000002", got)
	}

	for _, cursor := range []string{"not-a-cursor", "evt-", "evt-12x", "evt-999999"} {
		resp, body := apiCall(t, ts, http.MethodGet, "/v1/events?since="+cursor, nil)
		want := http.StatusGone
		if cursor == "not-a-cursor" || cursor == "evt-" || cursor == "evt-12x" {
			want = http.StatusBadRequest
		}
		requireStatus(t, resp, want)
		if want == http.StatusBadRequest {
			requireErrorCode(t, body, CodeInvalidArgument)
		}
	}
}

func TestListEventsLongPollReturnsOnNewEvent(t *testing.T) {
	ts, ring := newEventsServer(t, 8)

	type result struct {
		status int
		body   []byte
		err    error
	}
	done := make(chan result, 1)
	start := time.Now()
	go func() {
		req := newRequest(t, http.MethodGet, ts.URL+"/v1/events?wait=10s", nil)
		req.Header.Set("Authorization", "Bearer "+testToken)
		resp, err := ts.Client().Do(req)
		if err != nil {
			done <- result{err: err}
			return
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		done <- result{status: resp.StatusCode, body: body, err: err}
	}()

	// Publish after the poll has had time to subscribe; if it publishes
	// earlier the handler's replay already sees the event, which is also a
	// correct early return.
	time.Sleep(100 * time.Millisecond)
	ring.Publish(events.Event{Type: "account.login", Data: map[string]any{"accountId": "acc-01"}})

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("long poll: %v", got.err)
		}
		if got.status != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %s)", got.status, got.body)
		}
		page := decodeBody[eventsPage](t, got.body)
		if len(page.Events) != 1 || page.Events[0].Type != "account.login" {
			t.Fatalf("events = %+v, want the published account.login", page.Events)
		}
		if page.NextCursor != page.Events[0].ID {
			t.Fatalf("nextCursor = %q, want %q", page.NextCursor, page.Events[0].ID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("long poll did not return after a new event")
	}
	if elapsed := time.Since(start); elapsed >= 5*time.Second {
		t.Fatalf("long poll returned after %s; it must answer on the event, not the budget", elapsed)
	}
}

func TestListEventsLongPollTimesOut(t *testing.T) {
	ts, ring := newEventsServer(t, 8)
	ring.Publish(events.Event{Type: "account.login"})

	start := time.Now()
	resp, body := apiCall(t, ts, http.MethodGet, "/v1/events?since=evt-000001&wait=150ms", nil)
	elapsed := time.Since(start)
	requireStatus(t, resp, http.StatusOK)
	page := decodeBody[eventsPage](t, body)
	if len(page.Events) != 0 {
		t.Fatalf("timed-out poll returned %+v, want an empty page", page.Events)
	}
	if page.NextCursor != "evt-000001" {
		t.Fatalf("nextCursor = %q, want the unchanged cursor", page.NextCursor)
	}
	if elapsed < 100*time.Millisecond {
		t.Fatalf("poll returned after %s, before the wait budget", elapsed)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("poll returned after %s, far beyond the wait budget", elapsed)
	}

	// Without wait, a current cursor answers immediately.
	start = time.Now()
	resp, _ = apiCall(t, ts, http.MethodGet, "/v1/events?since=evt-000001", nil)
	requireStatus(t, resp, http.StatusOK)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("plain poll took %s; wait must be opt-in", elapsed)
	}
}

func TestEventsStreamReplaysThenStreamsLive(t *testing.T) {
	ts, ring := newEventsServer(t, 8)
	ring.Publish(events.Event{Type: "account.login", Data: map[string]any{"accountId": "acc-01"}})
	ring.Publish(events.Event{Type: "node.connected", Data: map[string]any{"nodeId": "node-01"}})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := newRequest(t, http.MethodGet, ts.URL+"/v1/events/stream", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("stream status = %d, want 200 (body %s)", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}
	if got := resp.Header.Get("X-Accel-Buffering"); got != "no" {
		t.Fatalf("X-Accel-Buffering = %q, want no", got)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache", got)
	}

	reader := bufio.NewReader(resp.Body)
	first := decodeFrameEvent(t, readSSEFrame(t, reader))
	if first.ID != "evt-000001" || first.Type != "account.login" {
		t.Fatalf("first frame = %+v, want the replayed account.login", first)
	}
	if got := first.Data["accountId"]; got != "acc-01" {
		t.Fatalf("first frame data accountId = %v", got)
	}
	second := decodeFrameEvent(t, readSSEFrame(t, reader))
	if second.ID != "evt-000002" || second.Type != "node.connected" {
		t.Fatalf("second frame = %+v, want the replayed node.connected", second)
	}

	// A live event after the replay is streamed once, not duplicated.
	ring.Publish(events.Event{Type: "schedule.fired", Data: map[string]any{"scheduleId": "sch-01"}})
	third := decodeFrameEvent(t, readSSEFrame(t, reader))
	if third.ID != "evt-000003" || third.Type != "schedule.fired" {
		t.Fatalf("third frame = %+v, want the live schedule.fired", third)
	}

	// Cancelling the request must unsubscribe: if the handler were still
	// attached, this burst would overflow its channel and increment Dropped.
	cancel()
	time.Sleep(100 * time.Millisecond)
	for range 200 {
		ring.Publish(events.Event{Type: "schedule.fired"})
	}
	if got := ring.Dropped(); got != 0 {
		t.Fatalf("dropped = %d after cancel, want 0 (handler did not close)", got)
	}
}

// TestEventsStreamClosesOnContextCancel drives the handler directly: the
// stream must return as soon as the request context is cancelled, not wait for
// the heartbeat or a new event.
func TestEventsStreamClosesOnContextCancel(t *testing.T) {
	ring := events.NewRing(8)
	t.Cleanup(ring.Close)
	ring.Publish(events.Event{Type: "account.login"})
	server := New(Config{AdminToken: testToken, EventRing: ring}, nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/v1/events/stream", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		server.ServeHTTP(rec, req)
		close(done)
	}()

	// Let the handler write the replay frame and enter its wait loop.
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream handler did not return after the client cancelled")
	}
	if got := rec.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "id: evt-000001") || !strings.Contains(body, "event: account.login") {
		t.Fatalf("stream body = %q, want the replay frame", body)
	}
}

// TestEventsStreamClosesWhenRingCloses pins the shutdown path: main registers
// Ring.Close with http.Server.RegisterOnShutdown so an open stream does not
// hold Shutdown until its deadline.
func TestEventsStreamClosesWhenRingCloses(t *testing.T) {
	ring := events.NewRing(8)
	ring.Publish(events.Event{Type: "account.login"})
	server := New(Config{AdminToken: testToken, EventRing: ring}, nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	req := httptest.NewRequest(http.MethodGet, "/v1/events/stream", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		server.ServeHTTP(rec, req)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	ring.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream handler did not return after the ring closed")
	}
	if body := rec.Body.String(); !strings.Contains(body, "id: evt-000001") {
		t.Fatalf("stream body = %q, want the replay frame", body)
	}
}

func TestEventsStreamResumesFromLastEventIDAndSince(t *testing.T) {
	ts, ring := newEventsServer(t, 8)
	for range 3 {
		ring.Publish(events.Event{Type: "account.login"})
	}

	// The reconnect header wins over the query.
	header := openEventsStream(t, ts, "/v1/events/stream?since=evt-000003", testToken,
		map[string]string{"Last-Event-ID": "evt-000001"})
	reader := bufio.NewReader(header.Body)
	if frame := decodeFrameEvent(t, readSSEFrame(t, reader)); frame.ID != "evt-000002" {
		t.Fatalf("Last-Event-ID resume frame = %q, want evt-000002", frame.ID)
	}
	header.Body.Close()

	query := openEventsStream(t, ts, "/v1/events/stream?since=evt-000002", testToken, nil)
	reader = bufio.NewReader(query.Body)
	if frame := decodeFrameEvent(t, readSSEFrame(t, reader)); frame.ID != "evt-000003" {
		t.Fatalf("since resume frame = %q, want evt-000003", frame.ID)
	}
}

func TestEventsStreamCursorErrorsStayJSON(t *testing.T) {
	ts, ring := newEventsServer(t, 2)
	for range 3 {
		ring.Publish(events.Event{Type: "account.login"})
	}

	resp, body := apiCall(t, ts, http.MethodGet, "/v1/events/stream?since=bogus", nil)
	requireStatus(t, resp, http.StatusBadRequest)
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("Content-Type = %q, want JSON before the stream starts", got)
	}
	requireErrorCode(t, body, CodeInvalidArgument)

	resp, body = apiCall(t, ts, http.MethodGet, "/v1/events/stream?since=evt-000001", nil)
	requireStatus(t, resp, http.StatusGone)
	var expired errorResponse
	if err := json.Unmarshal(body, &expired); err != nil {
		t.Fatalf("decode 410 body %q: %v", body, err)
	}
	if expired.Error.Code != codeCursorExpired || expired.Error.Details["oldestCursor"] != "evt-000002" {
		t.Fatalf("410 body = %+v, want CURSOR_EXPIRED with oldestCursor evt-000002", expired.Error)
	}
}

func TestEventsRoutesRequireConfiguredRing(t *testing.T) {
	ts, _ := newPlatformServer(t, Config{})
	for _, path := range []string{"/v1/events", "/v1/events/stream"} {
		resp, body := apiCall(t, ts, http.MethodGet, path, nil)
		requireStatus(t, resp, http.StatusServiceUnavailable)
		requireErrorCode(t, body, CodeUpstreamUnavailable)
	}
}

// TestScopedEventsScopeMatrix pins events:read: it reaches exactly the two
// event routes, every other scope is refused, a restricted key cannot read the
// global stream, and admin routes stay closed to it.
func TestScopedEventsScopeMatrix(t *testing.T) {
	ts, ring := newEventsServer(t, 8)
	ring.Publish(events.Event{Type: "account.login"})

	reader, _ := mustCreateScopedKey(t, ts, map[string]any{
		"userId": "u-01",
		"label":  "event reader",
		"scopes": []string{string(model.ScopeEventsRead)},
	})
	resp, body := scopedCall(t, ts, reader.Secret, http.MethodGet, "/v1/events", nil)
	requireStatus(t, resp, http.StatusOK)
	if got := len(decodeBody[eventsPage](t, body).Events); got != 1 {
		t.Fatalf("scoped replay returned %d events, want 1", got)
	}

	stream := openEventsStream(t, ts, "/v1/events/stream", reader.Secret, nil)
	if stream.StatusCode != http.StatusOK {
		t.Fatalf("scoped stream status = %d, want 200", stream.StatusCode)
	}
	if got := decodeFrameEvent(t, readSSEFrame(t, bufio.NewReader(stream.Body))); got.ID != "evt-000001" {
		t.Fatalf("scoped stream first frame = %q", got.ID)
	}
	stream.Body.Close()

	// Another read scope must not reach the ring.
	other, _ := mustCreateScopedKey(t, ts, map[string]any{
		"userId": "u-01",
		"label":  "account reader",
		"scopes": []string{string(model.ScopeAccountsRead)},
	})
	for _, path := range []string{"/v1/events", "/v1/events/stream"} {
		resp, body := scopedCall(t, ts, other.Secret, http.MethodGet, path, nil)
		requireStatus(t, resp, http.StatusForbidden)
		requireErrorCode(t, body, CodeForbidden)
	}

	// A key restricted to specific accounts cannot read the global ring.
	restricted, _ := mustCreateScopedKey(t, ts, map[string]any{
		"userId":     "u-01",
		"label":      "restricted event reader",
		"scopes":     []string{string(model.ScopeEventsRead)},
		"accountIds": []string{"acc-01"},
	})
	for _, path := range []string{"/v1/events", "/v1/events/stream"} {
		resp, body := scopedCall(t, ts, restricted.Secret, http.MethodGet, path, nil)
		requireStatus(t, resp, http.StatusForbidden)
		requireErrorCode(t, body, CodeForbidden)
	}

	// The admin token and an admin-scoped key bypass the table.
	resp, _ = apiCall(t, ts, http.MethodGet, "/v1/events", nil)
	requireStatus(t, resp, http.StatusOK)
	admin, _ := mustCreateScopedKey(t, ts, map[string]any{
		"userId": "u-01",
		"label":  "admin key",
		"scopes": []string{"admin"},
	})
	resp, _ = scopedCall(t, ts, admin.Secret, http.MethodGet, "/v1/events", nil)
	requireStatus(t, resp, http.StatusOK)

	// Admin-only routes stay closed to events:read.
	resp, body = scopedCall(t, ts, reader.Secret, http.MethodGet, "/v1/webhooks", nil)
	requireStatus(t, resp, http.StatusForbidden)
	requireErrorCode(t, body, CodeForbidden)
}
