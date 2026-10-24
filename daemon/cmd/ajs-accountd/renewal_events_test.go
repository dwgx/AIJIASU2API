package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/schedule"
	"ajsaccountd/internal/store"
	"ajsaccountd/internal/webhook"
)

type renewalSinkCall struct {
	event webhook.EventType
	data  map[string]any
}

// renewalRecordingSink is the fake api.EventSink used to pin the mapping.
type renewalRecordingSink struct {
	mu    sync.Mutex
	calls []renewalSinkCall
}

func (s *renewalRecordingSink) Emit(event webhook.EventType, data map[string]any) {
	copied := make(map[string]any, len(data))
	for key, value := range data {
		copied[key] = value
	}
	s.mu.Lock()
	s.calls = append(s.calls, renewalSinkCall{event: event, data: copied})
	s.mu.Unlock()
}

func (s *renewalRecordingSink) snapshot() []renewalSinkCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]renewalSinkCall(nil), s.calls...)
}

// TestEmitRenewalEventMapsOntoWebhookCatalogue pins the two names, the exact
// payload (ids and dates only, never a credential) and the drop/ nil rules.
func TestEmitRenewalEventMapsOntoWebhookCatalogue(t *testing.T) {
	sink := &renewalRecordingSink{}
	emitRenewalEvent(sink, schedule.ReminderEvent{
		AccountID: "acc-due", Kind: schedule.ReminderKindRenewalDue,
		Threshold: 3, ExpiresAt: "2026-11-01T00:00:00Z", DaysLeft: 3,
	})
	emitRenewalEvent(sink, schedule.ReminderEvent{
		AccountID: "acc-expired", Kind: schedule.ReminderKindRenewalOverdue,
		Threshold: 0, ExpiresAt: "2026-10-01T00:00:00Z", DaysLeft: -2,
	})
	emitRenewalEvent(sink, schedule.ReminderEvent{AccountID: "acc-unknown", Kind: "renewal_soon"})
	emitRenewalEvent(nil, schedule.ReminderEvent{
		AccountID: "acc-nil", Kind: schedule.ReminderKindRenewalDue,
	})

	want := []renewalSinkCall{
		{event: webhook.EventAccountRenewalDue, data: map[string]any{
			"accountId": "acc-due", "expiresAt": "2026-11-01T00:00:00Z", "daysLeft": 3, "threshold": 3,
		}},
		{event: webhook.EventAccountExpired, data: map[string]any{
			"accountId": "acc-expired", "expiresAt": "2026-10-01T00:00:00Z", "daysLeft": -2, "threshold": 0,
		}},
	}
	got := sink.snapshot()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %+v, want %+v", got, want)
	}
}

// TestRenewalSweepReachesWebhookSink drives the same hook startScheduler wires:
// one real sweep over a real store must reach the sink exactly once per mark.
func TestRenewalSweepReachesWebhookSink(t *testing.T) {
	now := time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	st := store.NewEmpty()
	account, err := st.CreateAccount(model.CreateAccountInput{
		Label: "Renewal Wire", Username: "user", Password: "secret",
		Platform: model.PlatformLinux, Concurrency: 1, Regions: []string{"cn"},
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	expiresAt := now.Add(3 * 24 * time.Hour).UTC().Format(time.RFC3339)
	updated, err := st.UpdateAccount(account.ID, model.AccountPatch{ExpiresAt: &expiresAt})
	if err != nil {
		t.Fatalf("UpdateAccount: %v", err)
	}
	sink := &renewalRecordingSink{}
	engine, err := schedule.NewEngine(schedule.Config{
		Store:           st,
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		Tick:            time.Second,
		RenewalEnabled:  true,
		RenewalLeadDays: []int{3},
		RenewalHour:     9,
		RenewalTimezone: "UTC",
		OnReminder: func(reminder schedule.ReminderEvent) {
			emitRenewalEvent(sink, reminder)
		},
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	engine.RenewalSweepNow(now)
	engine.RenewalSweepNow(now.Add(time.Hour)) // repeated sweep must not duplicate

	want := []renewalSinkCall{{event: webhook.EventAccountRenewalDue, data: map[string]any{
		"accountId": account.ID, "expiresAt": updated.ExpiresAt, "daysLeft": 3, "threshold": 3,
	}}}
	got := sink.snapshot()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %+v, want %+v", got, want)
	}
}

// TestWebhookSinkReceivesRenewalEvents runs the real dispatcher against an
// httptest receiver: the mapping must survive the sink and reach the wire.
func TestWebhookSinkReceivesRenewalEvents(t *testing.T) {
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
		ID: "ep-renewal", URL: server.URL, Secret: "0123456789abcdef",
		Enabled: true, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("AddEndpoint: %v", err)
	}
	sink := &webhookSink{dispatcher: dispatcher, logger: logger}

	emitRenewalEvent(sink, schedule.ReminderEvent{
		AccountID: "acc-due", Kind: schedule.ReminderKindRenewalDue,
		Threshold: 3, ExpiresAt: "2026-11-01T00:00:00Z", DaysLeft: 3,
	})
	emitRenewalEvent(sink, schedule.ReminderEvent{
		AccountID: "acc-expired", Kind: schedule.ReminderKindRenewalOverdue,
		Threshold: 0, ExpiresAt: "2026-10-01T00:00:00Z", DaysLeft: -2,
	})

	// JSON numbers decode as float64, so the wire payload is compared in that
	// shape; the key set is exact, which is the no-credential assertion.
	want := map[webhook.EventType]map[string]any{
		webhook.EventAccountRenewalDue: {
			"accountId": "acc-due", "expiresAt": "2026-11-01T00:00:00Z",
			"daysLeft": float64(3), "threshold": float64(3),
		},
		webhook.EventAccountExpired: {
			"accountId": "acc-expired", "expiresAt": "2026-10-01T00:00:00Z",
			"daysLeft": float64(-2), "threshold": float64(0),
		},
	}
	for range 2 {
		select {
		case payload := <-received:
			expected, ok := want[payload.Event]
			if !ok {
				t.Fatalf("unexpected event %q", payload.Event)
			}
			if !reflect.DeepEqual(payload.Data, expected) {
				t.Fatalf("payload for %s = %+v, want %+v", payload.Event, payload.Data, expected)
			}
			delete(want, payload.Event)
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for deliveries; missing %v", want)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing deliveries: %v", want)
	}
}
