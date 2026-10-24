package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	eventring "ajsaccountd/internal/events"
	"ajsaccountd/internal/webhook"
)

// TestRingSinkMirrorsEventsToRingAndWebhooks pins the single emission point:
// one Emit call feeds the replay ring and the webhook dispatcher, both see the
// same event, and the ring scrubs a credential the caller put in the payload.
func TestRingSinkMirrorsEventsToRingAndWebhooks(t *testing.T) {
	received := make(chan webhook.Payload, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload webhook.Payload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "bad payload", http.StatusBadRequest)
			return
		}
		received <- payload
		w.WriteHeader(http.StatusOK)
	}))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dispatcher := webhook.New(webhook.Config{
		Logger:       logger,
		AllowPrivate: true, // the receiver is an httptest loopback URL
	})
	t.Cleanup(func() {
		_ = dispatcher.Close()
		server.Close()
	})
	if err := dispatcher.AddEndpoint(webhook.Endpoint{
		ID: "ep-events", URL: server.URL, Secret: "0123456789abcdef",
		Enabled: true, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("AddEndpoint: %v", err)
	}

	ring := eventring.NewRing(8)
	t.Cleanup(ring.Close)
	sink := &ringSink{ring: ring, next: &webhookSink{dispatcher: dispatcher, logger: logger}}

	sink.Emit(webhook.EventAccountLogin, map[string]any{
		"accountId": "acc-01",
		"password":  "hunter2", // must be scrubbed before it reaches the ring
	})
	sink.Emit(webhook.EventNodeConnected, map[string]any{"nodeId": "node-01"})

	replay, next, err := ring.Since("", 0)
	if err != nil {
		t.Fatalf("ring.Since: %v", err)
	}
	if len(replay) != 2 {
		t.Fatalf("ring holds %d events, want 2", len(replay))
	}
	if replay[0].ID != "evt-000001" || replay[1].ID != "evt-000002" {
		t.Fatalf("ring ids = %q, %q", replay[0].ID, replay[1].ID)
	}
	if replay[0].Type != string(webhook.EventAccountLogin) || replay[1].Type != string(webhook.EventNodeConnected) {
		t.Fatalf("ring types = %q, %q", replay[0].Type, replay[1].Type)
	}
	if next != "evt-000002" {
		t.Fatalf("next cursor = %q, want evt-000002", next)
	}
	if got := replay[0].Data["password"]; got != eventring.Redacted {
		t.Fatalf("ring password = %v, want %q", got, eventring.Redacted)
	}
	if got := replay[0].Data["accountId"]; got != "acc-01" {
		t.Fatalf("ring accountId = %v, want acc-01", got)
	}

	// The webhook side receives the same two events.
	seen := make(map[webhook.EventType]bool, 2)
	for range 2 {
		select {
		case payload := <-received:
			seen[payload.Event] = true
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for webhook deliveries; seen %v", seen)
		}
	}
	if !seen[webhook.EventAccountLogin] || !seen[webhook.EventNodeConnected] {
		t.Fatalf("webhook deliveries = %v, want both events", seen)
	}
}
