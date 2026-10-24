package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/store"
	"ajsaccountd/internal/webhook"
)

// deadLetterAuditRows returns only the rows the dead-letter hook writes, so a
// test does not depend on unrelated startup audits.
func deadLetterAuditRows(st *store.Store) []model.AuditEvent {
	rows := make([]model.AuditEvent, 0, 1)
	for _, event := range st.Audit(store.AuditRetention) {
		if event.Action == "webhook.dead_letter" {
			rows = append(rows, event)
		}
	}
	return rows
}

func waitForWebhook(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// startDeadLetterFixture registers one loopback receiver that answers with
// status and returns the store, the endpoint id and the running dispatcher.
func startDeadLetterFixture(t *testing.T, status int, calls *atomic.Int64, attempts int) (*store.Store, string, *webhook.Dispatcher) {
	t.Helper()
	// The receiver is a loopback httptest server; the lab opt-out is the
	// documented way to allow a private destination.
	t.Setenv("AJS_WEBHOOK_ALLOW_PRIVATE", "1")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(status)
	}))
	t.Cleanup(server.Close)

	st := store.NewEmpty()
	enabled := true
	endpoint, err := st.CreateWebhookEndpoint(model.WebhookEndpointInput{URL: server.URL, Enabled: &enabled})
	if err != nil {
		t.Fatalf("create endpoint: %v", err)
	}
	secrets := memoryWebhookSecrets{endpoint.ID: "test-signing-secret"}
	dispatcher, _ := startWebhooksWithConfig(st, slog.New(slog.NewTextHandler(io.Discard, nil)), secrets, webhook.Config{
		MaxAttempts: attempts,
		Backoff:     []time.Duration{time.Millisecond},
	})
	t.Cleanup(func() { _ = dispatcher.Close() })
	return st, endpoint.ID, dispatcher
}

// TestStartWebhooksAuditsDeadLetter proves the composition root: a delivery
// that exhausts its retries writes exactly one warning audit row carrying the
// endpoint id, event and attempt count, and no URL or secret.
func TestStartWebhooksAuditsDeadLetter(t *testing.T) {
	var calls atomic.Int64
	st, endpointID, dispatcher := startDeadLetterFixture(t, http.StatusInternalServerError, &calls, 2)

	sink := &webhookSink{dispatcher: dispatcher, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	sink.Emit(webhook.EventAccountLogin, map[string]any{"accountId": "acc-01"})

	waitForWebhook(t, "the delivery to exhaust its retries", func() bool {
		return dispatcher.Pending() == 0 && calls.Load() == 2
	})
	rows := deadLetterAuditRows(st)
	if len(rows) != 1 {
		t.Fatalf("dead-letter audit rows = %+v, want exactly 1", rows)
	}
	row := rows[0]
	if row.Severity != model.SeverityWarn || row.Actor != "system" || row.Action != "webhook.dead_letter" {
		t.Fatalf("audit row = %+v, want a system warning webhook.dead_letter", row)
	}
	if row.Target != endpointID {
		t.Fatalf("audit target = %q, want the endpoint id %q", row.Target, endpointID)
	}
	if row.Detail != "account.login after 2 attempts" {
		t.Fatalf("audit detail = %q, want the event and attempt count", row.Detail)
	}
	if strings.Contains(row.Detail, "http") || strings.Contains(row.Detail, "test-signing-secret") {
		t.Fatalf("audit detail leaked the URL or the signing secret: %q", row.Detail)
	}

	// Closing records the leftover queue as failed, never as another dead
	// letter, so the row count cannot grow for the same delivery.
	if err := dispatcher.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if got := len(deadLetterAuditRows(st)); got != 1 {
		t.Fatalf("dead-letter audit rows after close = %d, want 1", got)
	}
}

// TestStartWebhooksAuditsEachDeadLetter proves two exhausted deliveries write
// two rows: the dedupe is per delivery id, not per endpoint or event.
func TestStartWebhooksAuditsEachDeadLetter(t *testing.T) {
	var calls atomic.Int64
	st, endpointID, dispatcher := startDeadLetterFixture(t, http.StatusInternalServerError, &calls, 2)
	sink := &webhookSink{dispatcher: dispatcher, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	sink.Emit(webhook.EventAccountLogin, map[string]any{"accountId": "acc-01"})
	sink.Emit(webhook.EventAccountLogin, map[string]any{"accountId": "acc-02"})

	waitForWebhook(t, "both deliveries to exhaust their retries", func() bool {
		return dispatcher.Pending() == 0 && calls.Load() == 4
	})
	rows := deadLetterAuditRows(st)
	if len(rows) != 2 {
		t.Fatalf("dead-letter audit rows = %+v, want 2", rows)
	}
	for _, row := range rows {
		if row.Target != endpointID || row.Action != "webhook.dead_letter" {
			t.Fatalf("audit row = %+v, want webhook.dead_letter for %s", row, endpointID)
		}
	}
}

// TestStartWebhooksWritesNoAuditForSuccessfulDelivery proves a receiver that
// accepts the event never produces a dead-letter row.
func TestStartWebhooksWritesNoAuditForSuccessfulDelivery(t *testing.T) {
	var calls atomic.Int64
	st, endpointID, dispatcher := startDeadLetterFixture(t, http.StatusNoContent, &calls, 2)
	sink := &webhookSink{dispatcher: dispatcher, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	sink.Emit(webhook.EventAccountLogin, map[string]any{"accountId": "acc-01"})

	waitForWebhook(t, "the successful delivery to persist", func() bool {
		rows := st.WebhookDeliveries(endpointID)
		return len(rows) == 1 && rows[0].Status == string(webhook.StatusSuccess)
	})
	if rows := deadLetterAuditRows(st); len(rows) != 0 {
		t.Fatalf("dead-letter audit rows = %+v, want none", rows)
	}
}
