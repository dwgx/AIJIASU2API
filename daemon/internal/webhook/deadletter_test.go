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

// deadLetterSink records OnDeadLetter callbacks for inspection.
type deadLetterSink struct {
	mu         sync.Mutex
	deliveries []Delivery
}

func (s *deadLetterSink) record(delivery Delivery) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deliveries = append(s.deliveries, delivery)
}

func (s *deadLetterSink) snapshot() []Delivery {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Delivery(nil), s.deliveries...)
}

// TestDeadLetterHookFiresOnceAfterRetriesExhausted pins the contract: one
// callback for a retry-exhausted delivery, carrying the final attempt and the
// endpoint/event an operator needs.
func TestDeadLetterHookFiresOnceAfterRetriesExhausted(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	sink := &deadLetterSink{}
	clock := newFakeClock()
	dispatcher := newTestDispatcher(Config{
		MaxAttempts:  3,
		OnDeadLetter: sink.record,
	}, clock)
	defer dispatcher.Close()
	addEndpoint(t, dispatcher, Endpoint{ID: "ep", URL: server.URL, Secret: "s", Enabled: true})

	if err := dispatcher.Dispatch(Event{ID: "evt", EndpointID: "ep", Type: EventAccountLogin}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	waitFor(t, "dead letter callback", func() bool { return len(sink.snapshot()) == 1 })
	waitFor(t, "delivery to finish", func() bool { return dispatcher.Pending() == 0 })

	if got := calls.Load(); got != 3 {
		t.Fatalf("receiver calls = %d, want 3", got)
	}
	deliveries := sink.snapshot()
	if len(deliveries) != 1 {
		t.Fatalf("dead letter callbacks = %d, want 1", len(deliveries))
	}
	delivery := deliveries[0]
	if delivery.ID != "evt:3" || delivery.EndpointID != "ep" || delivery.Event != EventAccountLogin {
		t.Fatalf("dead letter = %+v, want evt:3 for ep", delivery)
	}
	if delivery.Attempt != 3 || delivery.Status != StatusDeadLetter || delivery.HTTPStatus != http.StatusInternalServerError {
		t.Fatalf("dead letter = %+v, want the exhausted final attempt", delivery)
	}
}

// TestDeadLetterHookSkipsNonExhaustedDeliveries proves the hook is reserved for
// retry exhaustion: a success, a retry that eventually succeeds and a
// permanent client error never report a dead letter.
func TestDeadLetterHookSkipsNonExhaustedDeliveries(t *testing.T) {
	cases := []struct {
		name     string
		status   func(call int64) int
		attempts int
	}{
		{"success", func(int64) int { return http.StatusOK }, 1},
		{"retry then success", func(call int64) int {
			if call == 1 {
				return http.StatusInternalServerError
			}
			return http.StatusOK
		}, 2},
		{"permanent client error", func(int64) int { return http.StatusBadRequest }, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status(calls.Add(1)))
			}))
			defer server.Close()

			sink := &deadLetterSink{}
			clock := newFakeClock()
			dispatcher := newTestDispatcher(Config{
				MaxAttempts:  3,
				OnDeadLetter: sink.record,
			}, clock)
			defer dispatcher.Close()
			addEndpoint(t, dispatcher, Endpoint{ID: "ep", URL: server.URL, Secret: "s", Enabled: true})

			if err := dispatcher.Dispatch(Event{ID: "evt", EndpointID: "ep", Type: EventNodeConnected}); err != nil {
				t.Fatalf("Dispatch: %v", err)
			}
			waitFor(t, "delivery to finish", func() bool {
				return dispatcher.Pending() == 0 && len(dispatcher.Log().Recent()) == tc.attempts
			})
			if got := sink.snapshot(); len(got) != 0 {
				t.Fatalf("dead letter callbacks = %+v, want none", got)
			}
		})
	}
}

// TestDeadLetterHookDedupesDeliveryID proves a repeated notification for one
// delivery id reaches the sink once, including under concurrent delivery
// workers, while a different id still reports.
func TestDeadLetterHookDedupesDeliveryID(t *testing.T) {
	var mu sync.Mutex
	seen := make(map[string]int)
	dispatcher := newTestDispatcher(Config{
		OnDeadLetter: func(delivery Delivery) {
			mu.Lock()
			seen[delivery.ID]++
			mu.Unlock()
		},
	}, newFakeClock())
	defer dispatcher.Close()

	delivery := Delivery{
		ID:         "evt:5",
		EndpointID: "ep",
		Event:      EventAccountLogin,
		Attempt:    5,
		Status:     StatusDeadLetter,
		At:         time.Now(),
	}
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dispatcher.notifyDeadLetter(delivery)
		}()
	}
	wg.Wait()
	mu.Lock()
	first := seen["evt:5"]
	mu.Unlock()
	if first != 1 {
		t.Fatalf("callbacks for evt:5 = %d, want 1", first)
	}

	other := delivery
	other.ID = "evt-2:5"
	dispatcher.notifyDeadLetter(other)
	mu.Lock()
	second := seen["evt-2:5"]
	mu.Unlock()
	if second != 1 {
		t.Fatalf("callbacks for evt-2:5 = %d, want 1", second)
	}
}

// TestDeadLetterDedupeRingIsBounded proves the id set cannot grow without
// bound: the oldest id is evicted once the ring wraps, so an ancient delivery
// can be reported again instead of leaking memory.
func TestDeadLetterDedupeRingIsBounded(t *testing.T) {
	var calls atomic.Int64
	dispatcher := newTestDispatcher(Config{
		OnDeadLetter: func(Delivery) { calls.Add(1) },
	}, newFakeClock())
	defer dispatcher.Close()

	total := deadLetterDedupeSize * 2
	for i := range total {
		dispatcher.notifyDeadLetter(Delivery{
			ID:         fmt.Sprintf("evt-%d:5", i),
			EndpointID: "ep",
			Event:      EventAccountLogin,
			Attempt:    5,
			Status:     StatusDeadLetter,
			At:         time.Now(),
		})
	}
	dispatcher.deadLetterMu.Lock()
	size := len(dispatcher.deadLetterSeen)
	dispatcher.deadLetterMu.Unlock()
	if size != deadLetterDedupeSize {
		t.Fatalf("dedupe set size = %d, want %d", size, deadLetterDedupeSize)
	}
	if got := calls.Load(); got != int64(total) {
		t.Fatalf("callbacks = %d, want %d (every id was new)", got, total)
	}

	dispatcher.notifyDeadLetter(Delivery{
		ID:         "evt-0:5",
		EndpointID: "ep",
		Event:      EventAccountLogin,
		Attempt:    5,
		Status:     StatusDeadLetter,
		At:         time.Now(),
	})
	if got := calls.Load(); got != int64(total)+1 {
		t.Fatalf("callbacks after the evicted id = %d, want %d", got, total+1)
	}
}
