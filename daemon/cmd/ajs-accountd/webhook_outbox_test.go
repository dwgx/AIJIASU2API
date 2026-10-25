package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/store"
	"ajsaccountd/internal/webhook"
)

// outboxAuditRows returns only the rows the outbox cap writes, so a test does
// not depend on unrelated startup audits.
func outboxAuditRows(st *store.Store) []model.AuditEvent {
	rows := make([]model.AuditEvent, 0, 1)
	for _, event := range st.Audit(store.AuditRetention) {
		if event.Action == "webhook.outbox_dropped" {
			rows = append(rows, event)
		}
	}
	return rows
}

// recordingReceiver counts the deliveries it saw per event id. The first
// request of blockFirst waits on release, which pins it in flight while the
// test queues another event behind it.
type recordingReceiver struct {
	mu         sync.Mutex
	counts     map[string]int
	blockFirst bool
	arrived    chan struct{}
	arriveOne  sync.Once
	release    chan struct{}
	unblock    func()
}

func newRecordingReceiver(blockFirst bool) *recordingReceiver {
	r := &recordingReceiver{
		counts:  make(map[string]int),
		arrived: make(chan struct{}),
		release: make(chan struct{}),
	}
	var once sync.Once
	r.unblock = func() { once.Do(func() { close(r.release) }) }
	r.blockFirst = blockFirst
	return r
}

func (r *recordingReceiver) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var payload webhook.Payload
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Errorf("decode delivery: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		r.mu.Lock()
		r.counts[payload.ID]++
		first := r.blockFirst && r.counts[payload.ID] == 1 && payload.ID == "evt-a"
		r.mu.Unlock()
		if first {
			r.arriveOne.Do(func() { close(r.arrived) })
			<-r.release
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (r *recordingReceiver) count(id string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.counts[id]
}

// TestStartWebhooksOutboxSurvivesRestart proves the release-audit gap is
// closed end to end: an event queued but not attempted, and an attempt cut off
// by shutdown, both survive the state snapshot. The queued event reaches the
// receiver exactly once after the restart; the interrupted one is delivered at
// least once.
func TestStartWebhooksOutboxSurvivesRestart(t *testing.T) {
	// The receiver is a loopback httptest server; the lab opt-out is the
	// documented way to allow a private destination.
	t.Setenv("AJS_WEBHOOK_ALLOW_PRIVATE", "1")
	receiver := newRecordingReceiver(true)
	server := httptest.NewServer(receiver.handler(t))
	defer server.Close()
	// Registered after Close so it runs first: a blocked handler must be
	// released before the server waits for it.
	defer receiver.unblock()

	st := store.NewEmpty()
	enabled := true
	endpoint, err := st.CreateWebhookEndpoint(model.WebhookEndpointInput{URL: server.URL, Enabled: &enabled})
	if err != nil {
		t.Fatalf("create endpoint: %v", err)
	}
	secrets := memoryWebhookSecrets{endpoint.ID: "test-signing-secret"}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := webhook.Config{Backoff: []time.Duration{time.Millisecond}}

	first, _ := startWebhooksWithConfig(st, logger, secrets, cfg)
	if err := first.Dispatch(webhook.Event{ID: "evt-a", EndpointID: endpoint.ID, Type: webhook.EventAccountLogin}); err != nil {
		t.Fatalf("dispatch evt-a: %v", err)
	}
	select {
	case <-receiver.arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("the receiver never saw the first attempt")
	}
	// evt-b is accepted while evt-a occupies the endpoint's single delivery
	// slot, so it is queued but never attempted before the shutdown. Its data
	// carries a credential-shaped field: the persisted body must already be
	// scrubbed.
	if err := first.Dispatch(webhook.Event{
		ID:         "evt-b",
		EndpointID: endpoint.ID,
		Type:       webhook.EventNodeConnected,
		Data:       map[string]any{"nodeId": "node-1", "token": "tok-must-not-persist"},
	}); err != nil {
		t.Fatalf("dispatch evt-b: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close the first dispatcher: %v", err)
	}
	receiver.unblock()

	if got := st.WebhookOutboxLen(); got != 2 {
		t.Fatalf("pending outbox after shutdown = %d, want 2: %+v", got, st.WebhookOutbox())
	}
	for _, entry := range st.WebhookOutbox() {
		if entry.EndpointID != endpoint.ID {
			t.Fatalf("outbox entry for %q, want %q", entry.EndpointID, endpoint.ID)
		}
		if entry.ID == "evt-b" && entry.Attempts != 0 {
			t.Fatalf("queued event attempted before shutdown: %+v", entry)
		}
	}

	// The snapshot is the durability boundary: reload it into a fresh store and
	// start the dispatcher the way a restart does.
	data, err := st.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if !strings.Contains(string(data), `"webhookOutbox"`) {
		t.Fatal("snapshot does not carry the pending outbox")
	}
	if strings.Contains(string(data), "test-signing-secret") {
		t.Fatal("snapshot leaked the signing secret")
	}
	if strings.Contains(string(data), "tok-must-not-persist") {
		t.Fatal("snapshot leaked an event credential into the outbox")
	}
	if !strings.Contains(string(data), webhook.Redacted) {
		t.Fatal("snapshot outbox does not carry the scrubbed event data")
	}
	restarted := store.NewEmpty()
	if err := restarted.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	second, _ := startWebhooksWithConfig(restarted, logger, secrets, cfg)
	t.Cleanup(func() { _ = second.Close() })

	waitForWebhook(t, "the restored deliveries to finish", func() bool {
		return second.Pending() == 0 && restarted.WebhookOutboxLen() == 0
	})
	if got := receiver.count("evt-b"); got != 1 {
		t.Fatalf("queued event delivered %d times after the restart, want exactly 1", got)
	}
	if got := receiver.count("evt-a"); got < 1 {
		t.Fatalf("interrupted event delivered %d times, want at least 1", got)
	}
}

// TestStartWebhooksAuditsOutboxDrop proves the composition root reports an
// eviction: one warning webhook.outbox_dropped row names the endpoint, the
// event and the attempts spent, and never the URL or the signing secret.
func TestStartWebhooksAuditsOutboxDrop(t *testing.T) {
	t.Setenv("AJS_WEBHOOK_ALLOW_PRIVATE", "1")
	receiver := newRecordingReceiver(true)
	server := httptest.NewServer(receiver.handler(t))
	defer server.Close()
	// Registered after Close so it runs first: a blocked handler must be
	// released before the server waits for it.
	defer receiver.unblock()

	st := store.NewEmpty()
	enabled := true
	endpoint, err := st.CreateWebhookEndpoint(model.WebhookEndpointInput{URL: server.URL, Enabled: &enabled})
	if err != nil {
		t.Fatalf("create endpoint: %v", err)
	}
	secrets := memoryWebhookSecrets{endpoint.ID: "test-signing-secret"}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dispatcher, _ := startWebhooksWithConfig(st, logger, secrets, webhook.Config{
		OutboxLimit: 1,
		Workers:     1,
	})
	t.Cleanup(func() { _ = dispatcher.Close() })

	if err := dispatcher.Dispatch(webhook.Event{ID: "evt-a", EndpointID: endpoint.ID, Type: webhook.EventAccountLogin}); err != nil {
		t.Fatalf("dispatch evt-a: %v", err)
	}
	select {
	case <-receiver.arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("the receiver never saw the first attempt")
	}
	if err := dispatcher.Dispatch(webhook.Event{ID: "evt-b", EndpointID: endpoint.ID, Type: webhook.EventNodeConnected}); err != nil {
		t.Fatalf("dispatch evt-b: %v", err)
	}
	waitForWebhook(t, "the outbox drop audit", func() bool { return len(outboxAuditRows(st)) == 1 })

	rows := outboxAuditRows(st)
	row := rows[0]
	if row.Severity != model.SeverityWarn || row.Actor != "system" {
		t.Fatalf("audit row = %+v, want a system warning", row)
	}
	if row.Target != endpoint.ID {
		t.Fatalf("audit target = %q, want %q", row.Target, endpoint.ID)
	}
	if row.Detail != "account.login dropped after 0 attempts" {
		t.Fatalf("audit detail = %q, want the event and attempt count", row.Detail)
	}
	if strings.Contains(row.Detail, "http") || strings.Contains(row.Detail, "test-signing-secret") {
		t.Fatalf("audit detail leaked the URL or the signing secret: %q", row.Detail)
	}
	if got := st.WebhookOutboxLen(); got != 1 {
		t.Fatalf("persisted outbox len = %d, want the cap 1", got)
	}
	if entries := st.WebhookOutbox(); entries[0].ID != "evt-b" {
		t.Fatalf("persisted outbox = %+v, want the surviving evt-b", entries)
	}
}
