package webhook

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestDeliveryHookIsAsyncAndBounded blocks the sink and proves dispatch still
// completes: the hook runs on its own goroutine behind a small queue, so a slow
// store costs dropped history (counted) rather than a stalled delivery.
func TestDeliveryHookIsAsyncAndBounded(t *testing.T) {
	const events = 1000
	blocked := make(chan struct{})
	var mu sync.Mutex
	seen := 0
	clock := newFakeClock()
	dispatcher := newTestDispatcher(Config{
		QueueSize:     events + 8,
		Workers:       4,
		MaxAttempts:   1,
		Backoff:       []time.Duration{0},
		RingSize:      events + 8,
		HookQueueSize: 4,
		OnDelivery: func(Delivery) {
			<-blocked
			mu.Lock()
			seen++
			mu.Unlock()
		},
	}, clock)
	t.Cleanup(func() { _ = dispatcher.Close() })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	addEndpoint(t, dispatcher, Endpoint{ID: "wh-1", URL: server.URL, Secret: "test-secret", Enabled: true})

	for i := range events {
		err := dispatcher.Dispatch(Event{
			ID:         fmt.Sprintf("evt-%d", i),
			EndpointID: "wh-1",
			Type:       EventAccountLogin,
			At:         clock.Now(),
		})
		if err != nil {
			t.Fatalf("dispatch %d: %v", i, err)
		}
	}
	waitFor(t, "every attempt recorded", func() bool {
		return dispatcher.Pending() == 0 && len(dispatcher.Log().Recent()) == events
	})
	if dropped := dispatcher.HookDropped(); dropped == 0 {
		t.Fatal("a blocked sink with a four-slot queue dropped nothing")
	}
	close(blocked)
	if err := dispatcher.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	mu.Lock()
	delivered := seen
	mu.Unlock()
	if delivered == 0 {
		t.Fatal("hook never received an attempt")
	}
	if accounted := int64(delivered) + dispatcher.HookDropped(); accounted != events {
		t.Fatalf("hook saw %d and dropped %d (%d total), want %d attempts accounted for",
			delivered, dispatcher.HookDropped(), accounted, events)
	}
}

// TestDeliveryHookSeesEveryAttempt pins the callback contract: one call per
// recorded attempt, retries included, with the final success.
func TestDeliveryHookSeesEveryAttempt(t *testing.T) {
	var mu sync.Mutex
	var attempts []Delivery
	clock := newFakeClock()
	dispatcher := newTestDispatcher(Config{
		Workers:     1,
		MaxAttempts: 3,
		Backoff:     []time.Duration{0, 0},
		OnDelivery: func(delivery Delivery) {
			mu.Lock()
			attempts = append(attempts, delivery)
			mu.Unlock()
		},
	}, clock)
	t.Cleanup(func() { _ = dispatcher.Close() })

	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	addEndpoint(t, dispatcher, Endpoint{ID: "wh-1", URL: server.URL, Secret: "test-secret", Enabled: true})

	if err := dispatcher.Dispatch(Event{ID: "evt-1", EndpointID: "wh-1", Type: EventAccountLogin, At: clock.Now()}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	waitFor(t, "three hook calls", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(attempts) == 3
	})
	mu.Lock()
	defer mu.Unlock()
	for i, delivery := range attempts {
		if delivery.Attempt != i+1 {
			t.Fatalf("hook call %d carried attempt %d", i, delivery.Attempt)
		}
		if delivery.EndpointID != "wh-1" || delivery.Event != EventAccountLogin {
			t.Fatalf("hook call %d carried %+v", i, delivery)
		}
	}
	if attempts[0].Status != StatusFailed || attempts[0].HTTPStatus != http.StatusInternalServerError {
		t.Fatalf("first attempt = %+v, want a recorded 500 failure", attempts[0])
	}
	if attempts[2].Status != StatusSuccess || attempts[2].HTTPStatus != http.StatusNoContent {
		t.Fatalf("last attempt = %+v, want success", attempts[2])
	}
}
