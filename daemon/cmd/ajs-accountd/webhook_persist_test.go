package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/store"
	"ajsaccountd/internal/webhook"
)

// memoryWebhookSecrets is the process-memory WebhookSecretStore the daemon
// uses without AJS_MASTER_KEY. It lets the test register a receiver without
// touching the sealed secret file.
type memoryWebhookSecrets map[string]string

func (m memoryWebhookSecrets) Seal(id string, secret []byte) error {
	m[id] = string(secret)
	return nil
}

func (m memoryWebhookSecrets) Open(id string) (string, bool) {
	secret, ok := m[id]
	return secret, ok
}

func (m memoryWebhookSecrets) Delete(id string) error {
	delete(m, id)
	return nil
}

// TestStartWebhooksPersistsDeliveries proves the composition root: a delivery
// the dispatcher records reaches the store through OnDelivery, with the fields
// the snapshot needs.
func TestStartWebhooksPersistsDeliveries(t *testing.T) {
	// The receiver is a loopback httptest server; the lab opt-out is the
	// documented way to allow a private destination.
	t.Setenv("AJS_WEBHOOK_ALLOW_PRIVATE", "1")
	received := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case received <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	st := store.NewEmpty()
	enabled := true
	endpoint, err := st.CreateWebhookEndpoint(model.WebhookEndpointInput{
		URL:     server.URL,
		Enabled: &enabled,
	})
	if err != nil {
		t.Fatalf("create endpoint: %v", err)
	}
	secrets := memoryWebhookSecrets{endpoint.ID: "test-signing-secret"}
	dispatcher, sink := startWebhooks(st, slog.New(slog.NewTextHandler(io.Discard, nil)), secrets)
	t.Cleanup(func() { _ = dispatcher.Close() })

	sink.Emit(webhook.EventAccountLogin, map[string]any{"accountId": "acc-01"})
	select {
	case <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("receiver was never called")
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		rows := st.WebhookDeliveries(endpoint.ID)
		if len(rows) == 1 {
			row := rows[0]
			if row.EndpointID != endpoint.ID {
				t.Fatalf("persisted endpoint = %q, want %q", row.EndpointID, endpoint.ID)
			}
			if row.Event != string(webhook.EventAccountLogin) {
				t.Fatalf("persisted event = %q", row.Event)
			}
			if row.Attempt != 1 || row.Status != string(webhook.StatusSuccess) || row.HTTPStatus != http.StatusNoContent {
				t.Fatalf("persisted attempt = %+v, want a successful first attempt", row)
			}
			if _, err := time.Parse(time.RFC3339, row.At); err != nil {
				t.Fatalf("persisted timestamp %q: %v", row.At, err)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("delivery was not persisted: %+v", st.WebhookDeliveries(endpoint.ID))
		}
		time.Sleep(5 * time.Millisecond)
	}
}
