package ajs

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ListEventsOptions filters GET /v1/events. Since is the id of the last event
// the caller saw; Wait is a long-poll budget capped at 25s by the daemon.
type ListEventsOptions struct {
	Since string
	Limit int
	Wait  time.Duration
}

// StreamEventsOptions controls GET /v1/events/stream. LastEventID wins over
// Since, mirroring a browser reconnect.
type StreamEventsOptions struct {
	Since       string
	LastEventID string
}

// ListEvents replays the retained event ring from a cursor. A cursor whose
// event was evicted answers 410 with an *APIError whose Code is
// CURSOR_EXPIRED and Details["oldestCursor"] names the restart point.
func (c *Client) ListEvents(ctx context.Context, opts ListEventsOptions) (*EventsPage, error) {
	q := url.Values{}
	setString(q, "since", opts.Since)
	setInt(q, "limit", opts.Limit)
	if opts.Wait > 0 {
		q.Set("wait", opts.Wait.String())
	}
	var out EventsPage
	if _, err := c.do(ctx, http.MethodGet, "/v1/events", q, nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// EventStream is one server-sent event stream. Next blocks until the next
// event, io.EOF when the daemon closes the stream, or another error. Close the
// stream when done; closing the request context also ends it.
type EventStream struct {
	resp    *http.Response
	scanner *bufio.Scanner
	closed  bool
}

// StreamEvents opens the retained-plus-live SSE stream. The response headers
// are validated here; a cursor error is returned as *APIError before any event
// is read.
func (c *Client) StreamEvents(ctx context.Context, opts StreamEventsOptions) (*EventStream, error) {
	q := url.Values{}
	setString(q, "since", opts.Since)
	headers := http.Header{}
	headers.Set("Accept", "text/event-stream")
	if opts.LastEventID != "" {
		headers.Set("Last-Event-ID", opts.LastEventID)
	}
	resp, err := c.send(ctx, http.MethodGet, "/v1/events/stream", q, nil, headers)
	if err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	return &EventStream{resp: resp, scanner: scanner}, nil
}

// Next returns the next event frame.
func (s *EventStream) Next() (Event, error) {
	if s.closed {
		return Event{}, io.EOF
	}
	var (
		id    string
		name  string
		data  []string
		event Event
	)
	dispatch := func() (Event, bool, error) {
		if len(data) == 0 {
			id, name, data = "", "", nil
			return Event{}, false, nil
		}
		payload := strings.Join(data, "\n")
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			return Event{}, false, err
		}
		if event.ID == "" {
			event.ID = id
		}
		if event.Type == "" {
			event.Type = name
		}
		id, name, data = "", "", nil
		return event, true, nil
	}
	for s.scanner.Scan() {
		line := strings.TrimSuffix(s.scanner.Text(), "\r")
		if line == "" {
			if ev, ok, err := dispatch(); ok || err != nil {
				return ev, err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue // keepalive comment
		}
		field, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "id":
			id = value
		case "event":
			name = value
		case "data":
			data = append(data, value)
		}
	}
	if err := s.scanner.Err(); err != nil {
		return Event{}, err
	}
	// An unterminated frame at EOF is discarded, per the SSE spec.
	return Event{}, io.EOF
}

// Close releases the response body. It is safe to call more than once.
func (s *EventStream) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	return s.resp.Body.Close()
}

// FollowEvents consumes a stream until ctx is done or the daemon closes it,
// calling fn for every event. A nil error means a clean end of stream.
func (c *Client) FollowEvents(ctx context.Context, opts StreamEventsOptions, fn func(Event) error) error {
	stream, err := c.StreamEvents(ctx, opts)
	if err != nil {
		return err
	}
	defer stream.Close()
	for {
		event, err := stream.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if fn != nil {
			if err := fn(event); err != nil {
				return err
			}
		}
	}
}
