package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// memoryOutbox is the OutboxStore the dispatcher tests use. It keeps the same
// keying and replacement contract as the snapshot store, so a test restart can
// hand the exact entries of the previous dispatcher to the next one.
type memoryOutbox struct {
	mu      sync.Mutex
	entries map[string]OutboxEntry
	puts    int
	removes int
}

func newMemoryOutbox() *memoryOutbox {
	return &memoryOutbox{entries: make(map[string]OutboxEntry)}
}

func (m *memoryOutbox) PutOutbox(entry OutboxEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[entry.Key()] = entry
	m.puts++
	return nil
}

func (m *memoryOutbox) RemoveOutbox(endpointID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.entries, endpointID+"\x00"+id)
	m.removes++
	return nil
}

func (m *memoryOutbox) Outbox() []OutboxEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]OutboxEntry, 0, len(m.entries))
	for _, entry := range m.entries {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].Key() < out[j].Key()
	})
	return out
}

func (m *memoryOutbox) get(endpointID, id string) (OutboxEntry, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.entries[endpointID+"\x00"+id]
	return entry, ok
}

func (m *memoryOutbox) len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.entries)
}

func TestDefaultOutboxLimitIsBounded(t *testing.T) {
	if DefaultOutboxLimit != 1000 {
		t.Fatalf("DefaultOutboxLimit = %d, want 1000", DefaultOutboxLimit)
	}
}

// TestOutboxPersistsBeforeAttemptAndRemovesOnSuccess pins the lifecycle: the
// accepted event is durable before the first attempt starts, the persisted body
// is the scrubbed wire body with a matching digest, and a successful delivery
// leaves the outbox.
func TestOutboxPersistsBeforeAttemptAndRemovesOnSuccess(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	outbox := newMemoryOutbox()
	clock := newFakeClock()
	dispatcher := newTestDispatcher(Config{Outbox: outbox}, clock)
	defer dispatcher.Close()
	addEndpoint(t, dispatcher, Endpoint{ID: "ep", URL: server.URL, Secret: "s", Enabled: true})

	if err := dispatcher.Dispatch(Event{
		ID:         "evt-1",
		EndpointID: "ep",
		Type:       EventAccountLogin,
		Data:       map[string]any{"accountId": "acc-01", "token": "tok-should-not-leak"},
	}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	// Dispatch persists synchronously before it starts the delivery worker, so
	// the entry is visible here even though the receiver has not answered.
	entry, ok := outbox.get("ep", "evt-1")
	if !ok {
		t.Fatalf("entry missing from the outbox after Dispatch: %+v", outbox.Outbox())
	}
	if entry.Attempts != 0 {
		t.Fatalf("attempts = %d, want 0", entry.Attempts)
	}
	if entry.CreatedAt != clock.Now() || !entry.NextAttemptAt.Equal(entry.CreatedAt) {
		t.Fatalf("timestamps = created %v next %v, want both %v", entry.CreatedAt, entry.NextAttemptAt, clock.Now())
	}
	if entry.PayloadHash != PayloadDigest(entry.Payload) {
		t.Fatalf("payload hash %q does not match the payload", entry.PayloadHash)
	}
	if strings.Contains(string(entry.Payload), "tok-should-not-leak") {
		t.Fatalf("persisted payload leaked a credential: %s", entry.Payload)
	}
	var payload Payload
	if err := json.Unmarshal(entry.Payload, &payload); err != nil {
		t.Fatalf("decode persisted payload: %v", err)
	}
	if payload.Data["token"] != Redacted || payload.Data["accountId"] != "acc-01" {
		t.Fatalf("persisted payload data = %#v, want the scrubbed event", payload.Data)
	}

	unblock()
	waitFor(t, "the delivery to succeed", func() bool {
		return dispatcher.Pending() == 0 && outbox.len() == 0
	})
}

// TestOutboxRestartMidBackoff proves the retry schedule survives a restart: the
// failed attempt and the next due time are persisted before the wait, and the
// new dispatcher waits out the remainder instead of retrying immediately or
// restarting the schedule.
func TestOutboxRestartMidBackoff(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	outbox := newMemoryOutbox()
	clock := newFakeClock()
	sleeping := make(chan time.Duration, 1)
	first := newTestDispatcher(Config{
		Outbox:      outbox,
		MaxAttempts: 3,
		Now:         clock.Now,
		Sleep: func(ctx context.Context, delay time.Duration) error {
			sleeping <- delay
			<-ctx.Done()
			return ctx.Err()
		},
	}, clock)
	addEndpoint(t, first, Endpoint{ID: "ep", URL: server.URL, Secret: "s", Enabled: true})

	if err := first.Dispatch(Event{ID: "evt", EndpointID: "ep", Type: EventNodeConnected}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	select {
	case delay := <-sleeping:
		if delay != time.Second {
			t.Fatalf("first backoff = %v, want 1s", delay)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("dispatcher never entered its backoff")
	}
	entry, ok := outbox.get("ep", "evt")
	if !ok {
		t.Fatalf("entry missing during backoff: %+v", outbox.Outbox())
	}
	if entry.Attempts != 1 {
		t.Fatalf("attempts during backoff = %d, want 1", entry.Attempts)
	}
	if want := clock.Now().Add(time.Second); !entry.NextAttemptAt.Equal(want) {
		t.Fatalf("nextAttemptAt = %v, want %v", entry.NextAttemptAt, want)
	}

	// Shutdown while the retry is still pending: the entry must survive with the
	// attempts and due time it had.
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, ok := outbox.get("ep", "evt"); !ok {
		t.Fatalf("entry lost on shutdown: %+v", outbox.Outbox())
	}

	second := newTestDispatcher(Config{Outbox: outbox, MaxAttempts: 3}, clock)
	defer second.Close()
	addEndpoint(t, second, Endpoint{ID: "ep", URL: server.URL, Secret: "s", Enabled: true})
	restored, skipped := second.RestoreOutbox()
	if restored != 1 || skipped != 0 {
		t.Fatalf("RestoreOutbox = %d restored, %d skipped; want 1 and 0", restored, skipped)
	}
	waitFor(t, "the restored delivery to succeed", func() bool {
		return second.Pending() == 0 && outbox.len() == 0
	})
	if got := calls.Load(); got != 2 {
		t.Fatalf("receiver calls = %d, want 2 (one failed attempt, one retry)", got)
	}
	if sleeps := clock.Sleeps(); len(sleeps) != 1 || sleeps[0] != time.Second {
		t.Fatalf("restored backoff sleeps = %v, want the remaining [1s]", sleeps)
	}
	if got := second.Log().Recent(); len(got) != 1 || got[0].Attempt != 2 || got[0].Status != StatusSuccess {
		t.Fatalf("restored attempts = %+v, want one successful attempt 2", got)
	}
}

// TestOutboxRestartMidFlightRequeues proves an attempt interrupted by shutdown
// is re-queued: the entry stays in the outbox, the next dispatcher delivers it,
// and the event id is still deduped afterwards.
func TestOutboxRestartMidFlightRequeues(t *testing.T) {
	var calls atomic.Int64
	arrived := make(chan struct{}, 1)
	// The first request stays in flight until the dispatcher is closed. A
	// channel (not the request context) releases it: the server only observes a
	// client disconnect once it reads the connection, which a blocked handler
	// never does.
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if calls.Add(1) == 1 {
			arrived <- struct{}{}
			<-release
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	outbox := newMemoryOutbox()
	clock := newFakeClock()
	first := newTestDispatcher(Config{Outbox: outbox, Backoff: []time.Duration{time.Second}}, clock)
	addEndpoint(t, first, Endpoint{ID: "ep", URL: server.URL, Secret: "s", Enabled: true})

	if err := first.Dispatch(Event{ID: "evt", EndpointID: "ep", Type: EventLeaseCreated}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("receiver never saw the first attempt")
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// The request was cancelled on the client; let the abandoned handler finish
	// so the server can accept the restarted delivery.
	unblock()
	entry, ok := outbox.get("ep", "evt")
	if !ok {
		t.Fatalf("in-flight entry lost on shutdown: %+v", outbox.Outbox())
	}
	if entry.Attempts >= first.cfg.MaxAttempts {
		t.Fatalf("shutdown spent the retry budget: attempts = %d", entry.Attempts)
	}

	second := newTestDispatcher(Config{Outbox: outbox}, clock)
	defer second.Close()
	addEndpoint(t, second, Endpoint{ID: "ep", URL: server.URL, Secret: "s", Enabled: true})
	if restored, _ := second.RestoreOutbox(); restored != 1 {
		t.Fatalf("RestoreOutbox restored = %d, want 1", restored)
	}
	waitFor(t, "the re-queued delivery to succeed", func() bool {
		return second.Pending() == 0 && outbox.len() == 0
	})
	if got := calls.Load(); got != 2 {
		t.Fatalf("receiver calls = %d, want 2 (at-least-once across the restart)", got)
	}
	// The restored event id is inside the dedupe window, so a producer retry
	// cannot fan out a second delivery.
	if err := second.Dispatch(Event{ID: "evt", EndpointID: "ep", Type: EventLeaseCreated}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("Dispatch after restore error = %v, want ErrDuplicate", err)
	}
}

// TestOutboxCapDropsOldestAndReports proves the pending set is bounded: the cap
// evicts the oldest entry, counts it, reports it to the audit hook and removes
// it from the store, while a queued victim is never delivered.
func TestOutboxCapDropsOldestAndReports(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	// A failed assertion must not leave a handler blocked: httptest.Server.Close
	// waits for every in-flight handler.
	defer unblock()
	var mu sync.Mutex
	delivered := map[string]int{}
	arrived := make(chan struct{})
	var arrivedOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload Payload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode payload: %v", err)
		}
		arrivedOnce.Do(func() { close(arrived) })
		<-release
		mu.Lock()
		delivered[payload.ID]++
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	outbox := newMemoryOutbox()
	var muDrop sync.Mutex
	var dropped []OutboxEntry
	clock := newFakeClock()
	dispatcher := newTestDispatcher(Config{
		Outbox:      outbox,
		OutboxLimit: 2,
		Workers:     1,
		OnOutboxDrop: func(entry OutboxEntry) {
			muDrop.Lock()
			dropped = append(dropped, entry)
			muDrop.Unlock()
		},
	}, clock)
	addEndpoint(t, dispatcher, Endpoint{ID: "ep", URL: server.URL, Secret: "s", Enabled: true})

	// Pin the first delivery in flight before the overflow: the cap then evicts
	// evt-0 (on the wire) and evt-1 (still queued), which proves both that an
	// in-flight attempt completes and that a queued victim is never sent.
	if err := dispatcher.Dispatch(Event{ID: "evt-0", EndpointID: "ep", Type: EventScheduleFired}); err != nil {
		t.Fatalf("Dispatch evt-0: %v", err)
	}
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("the first delivery never reached the receiver")
	}
	for i := 1; i < 4; i++ {
		if err := dispatcher.Dispatch(Event{
			ID:         fmt.Sprintf("evt-%d", i),
			EndpointID: "ep",
			Type:       EventScheduleFired,
		}); err != nil {
			t.Fatalf("Dispatch %d: %v", i, err)
		}
	}
	waitFor(t, "two evictions", func() bool { return dispatcher.OutboxDropped() == 2 })
	if got := dispatcher.OutboxLen(); got != 2 {
		t.Fatalf("outbox len = %d, want the cap 2", got)
	}
	entries := outbox.Outbox()
	if len(entries) != 2 || entries[0].ID != "evt-2" || entries[1].ID != "evt-3" {
		t.Fatalf("persisted outbox = %+v, want evt-2 and evt-3", entries)
	}
	muDrop.Lock()
	droppedIDs := make([]string, 0, len(dropped))
	for _, entry := range dropped {
		droppedIDs = append(droppedIDs, entry.ID)
	}
	muDrop.Unlock()
	if len(droppedIDs) != 2 || droppedIDs[0] != "evt-0" || droppedIDs[1] != "evt-1" {
		t.Fatalf("dropped = %v, want the two oldest (evt-0, evt-1)", droppedIDs)
	}

	unblock()
	waitFor(t, "the surviving deliveries to finish", func() bool {
		return dispatcher.Pending() == 0 && outbox.len() == 0
	})
	mu.Lock()
	defer mu.Unlock()
	if delivered["evt-0"] != 1 {
		t.Fatalf("the in-flight attempt did not complete after eviction: %v", delivered)
	}
	if delivered["evt-1"] != 0 {
		t.Fatalf("a queued entry evicted by the cap was still delivered: %v", delivered)
	}
	if delivered["evt-2"] != 1 || delivered["evt-3"] != 1 {
		t.Fatalf("surviving deliveries = %v, want each of evt-2/evt-3 once", delivered)
	}
}

// TestOutboxConcurrentOverflowKeepsStoreInSync proves the persisted set never
// drifts from the live one under concurrent dispatch at the cap: every accepted
// event is either pending or reported dropped, and the store holds exactly the
// pending rows.
func TestOutboxConcurrentOverflowKeepsStoreInSync(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	const (
		events = 100
		limit  = 10
	)
	outbox := newMemoryOutbox()
	dispatcher := newTestDispatcher(Config{
		Outbox:      outbox,
		OutboxLimit: limit,
		Workers:     4,
		QueueSize:   events + 8,
	}, newFakeClock())
	addEndpoint(t, dispatcher, Endpoint{ID: "ep", URL: server.URL, Secret: "s", Enabled: true})

	var wg sync.WaitGroup
	for i := range events {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := dispatcher.Dispatch(Event{
				ID:         fmt.Sprintf("evt-%d", i),
				EndpointID: "ep",
				Type:       EventScheduleFired,
			}); err != nil {
				t.Errorf("Dispatch %d: %v", i, err)
			}
		}()
	}
	wg.Wait()
	waitFor(t, "every event accounted for", func() bool {
		return int(dispatcher.OutboxDropped())+dispatcher.OutboxLen() == events
	})
	pending := dispatcher.OutboxLen()
	if pending != limit {
		t.Fatalf("pending = %d, want the cap %d", pending, limit)
	}
	if got := outbox.len(); got != pending {
		t.Fatalf("persisted rows = %d, want the live pending count %d", got, pending)
	}
	unblock()
	waitFor(t, "the surviving deliveries to finish", func() bool {
		return dispatcher.Pending() == 0 && outbox.len() == 0
	})
	if err := dispatcher.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestOutboxDeadLetterRemovesEntry proves an exhausted delivery leaves the
// outbox: the retry budget is spent, the row is recorded as dead_letter and
// nothing is retried after a restart.
func TestOutboxDeadLetterRemovesEntry(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	outbox := newMemoryOutbox()
	clock := newFakeClock()
	dispatcher := newTestDispatcher(Config{Outbox: outbox, MaxAttempts: 2}, clock)
	addEndpoint(t, dispatcher, Endpoint{ID: "ep", URL: server.URL, Secret: "s", Enabled: true})

	if err := dispatcher.Dispatch(Event{ID: "evt", EndpointID: "ep", Type: EventAuditCritical}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	waitFor(t, "the delivery to dead-letter", func() bool {
		return dispatcher.Pending() == 0 && outbox.len() == 0
	})
	deliveries := dispatcher.Log().Recent()
	if len(deliveries) != 2 || deliveries[1].Status != StatusDeadLetter {
		t.Fatalf("deliveries = %+v, want a dead letter on attempt 2", deliveries)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("receiver calls = %d, want 2", got)
	}
}

// TestOutboxRestoreSkipsUnusableEntries proves restore never queues a delivery
// it cannot replay: a missing receiver, a payload that no longer matches its
// digest and an already-spent retry budget are dropped from the store, and the
// spent one reports the dead letter the crash interrupted.
func TestOutboxRestoreSkipsUnusableEntries(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	outbox := newMemoryOutbox()
	clock := newFakeClock()
	now := clock.Now()

	good := OutboxEntry{
		ID:            "good",
		EndpointID:    "ep",
		Event:         EventAccountLogin,
		Payload:       json.RawMessage(`{"id":"good"}`),
		PayloadHash:   PayloadDigest([]byte(`{"id":"good"}`)),
		NextAttemptAt: now,
		CreatedAt:     now,
	}
	unknown := OutboxEntry{
		ID:            "ghost",
		EndpointID:    "missing",
		Event:         EventAccountLogin,
		Payload:       json.RawMessage(`{"id":"ghost"}`),
		PayloadHash:   PayloadDigest([]byte(`{"id":"ghost"}`)),
		NextAttemptAt: now,
		CreatedAt:     now.Add(time.Second),
	}
	corrupt := OutboxEntry{
		ID:            "corrupt",
		EndpointID:    "ep",
		Event:         EventAccountLogin,
		Payload:       json.RawMessage(`{"id":"corrupt"}`),
		PayloadHash:   "deadbeef",
		NextAttemptAt: now,
		CreatedAt:     now.Add(2 * time.Second),
	}
	exhausted := OutboxEntry{
		ID:            "spent",
		EndpointID:    "ep",
		Event:         EventAccountLogin,
		Payload:       json.RawMessage(`{"id":"spent"}`),
		PayloadHash:   PayloadDigest([]byte(`{"id":"spent"}`)),
		Attempts:      2,
		NextAttemptAt: now.Add(time.Minute),
		CreatedAt:     now.Add(3 * time.Second),
	}
	for _, entry := range []OutboxEntry{good, unknown, corrupt, exhausted} {
		if err := outbox.PutOutbox(entry); err != nil {
			t.Fatalf("seed %q: %v", entry.ID, err)
		}
	}

	var deadLetters []Delivery
	dispatcher := newTestDispatcher(Config{
		Outbox:       outbox,
		MaxAttempts:  2,
		OnDeadLetter: func(delivery Delivery) { deadLetters = append(deadLetters, delivery) },
	}, clock)
	defer dispatcher.Close()
	addEndpoint(t, dispatcher, Endpoint{ID: "ep", URL: server.URL, Secret: "s", Enabled: true})

	restored, skipped := dispatcher.RestoreOutbox()
	if restored != 1 || skipped != 3 {
		t.Fatalf("RestoreOutbox = %d restored, %d skipped; want 1 and 3", restored, skipped)
	}
	if got := dispatcher.OutboxLen(); got != 1 {
		t.Fatalf("outbox len after restore = %d, want 1", got)
	}
	if _, ok := outbox.get("ep", "good"); !ok {
		t.Fatal("the usable entry was not restored")
	}
	for _, key := range [][2]string{{"missing", "ghost"}, {"ep", "corrupt"}, {"ep", "spent"}} {
		if _, ok := outbox.get(key[0], key[1]); ok {
			t.Fatalf("unusable entry %v was not removed", key)
		}
	}
	if len(deadLetters) != 1 || deadLetters[0].ID != "spent:2" || deadLetters[0].Status != StatusDeadLetter {
		t.Fatalf("dead letters = %+v, want one spent:2", deadLetters)
	}
	waitFor(t, "the restored delivery to finish", func() bool {
		return dispatcher.Pending() == 0 && outbox.len() == 0
	})
}

// TestOutboxRestoreIsIdempotent proves a second restore does not disturb the
// live entry: it is reported as a duplicate and its persisted row survives.
func TestOutboxRestoreIsIdempotent(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	outbox := newMemoryOutbox()
	clock := newFakeClock()
	now := clock.Now()
	entry := OutboxEntry{
		ID:            "evt",
		EndpointID:    "ep",
		Event:         EventAccountLogin,
		Payload:       json.RawMessage(`{"id":"evt"}`),
		PayloadHash:   PayloadDigest([]byte(`{"id":"evt"}`)),
		NextAttemptAt: now,
		CreatedAt:     now,
	}
	if err := outbox.PutOutbox(entry); err != nil {
		t.Fatalf("seed: %v", err)
	}

	dispatcher := newTestDispatcher(Config{Outbox: outbox}, clock)
	addEndpoint(t, dispatcher, Endpoint{ID: "ep", URL: server.URL, Secret: "s", Enabled: true})
	if restored, skipped := dispatcher.RestoreOutbox(); restored != 1 || skipped != 0 {
		t.Fatalf("first restore = %d/%d, want 1/0", restored, skipped)
	}
	if restored, skipped := dispatcher.RestoreOutbox(); restored != 0 || skipped != 1 {
		t.Fatalf("second restore = %d/%d, want 0/1", restored, skipped)
	}
	if got := dispatcher.OutboxLen(); got != 1 {
		t.Fatalf("outbox len after a repeated restore = %d, want 1", got)
	}
	if _, ok := outbox.get("ep", "evt"); !ok {
		t.Fatal("a repeated restore deleted the live entry's persisted row")
	}
	unblock()
	waitFor(t, "the restored delivery to finish", func() bool {
		return dispatcher.Pending() == 0 && outbox.len() == 0
	})
	if err := dispatcher.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestOutboxRestoreHonoursSmallerLimit proves a snapshot written under a larger
// cap is trimmed on restore, so the configured bound holds from the first
// attempt.
func TestOutboxRestoreHonoursSmallerLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	outbox := newMemoryOutbox()
	clock := newFakeClock()
	now := clock.Now()
	for i := range 3 {
		id := fmt.Sprintf("evt-%d", i)
		body := json.RawMessage(`{"id":"` + id + `"}`)
		if err := outbox.PutOutbox(OutboxEntry{
			ID:            id,
			EndpointID:    "ep",
			Event:         EventAccountLogin,
			Payload:       body,
			PayloadHash:   PayloadDigest(body),
			NextAttemptAt: now,
			CreatedAt:     now.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}

	dispatcher := newTestDispatcher(Config{Outbox: outbox, OutboxLimit: 1}, clock)
	addEndpoint(t, dispatcher, Endpoint{ID: "ep", URL: server.URL, Secret: "s", Enabled: true})
	restored, skipped := dispatcher.RestoreOutbox()
	if restored != 3 || skipped != 0 {
		t.Fatalf("restore = %d/%d, want 3/0", restored, skipped)
	}
	if got := dispatcher.OutboxLen(); got != 1 {
		t.Fatalf("outbox len after restore = %d, want the cap 1", got)
	}
	if got := dispatcher.OutboxDropped(); got != 2 {
		t.Fatalf("outbox dropped = %d, want 2", got)
	}
	if entries := outbox.Outbox(); len(entries) != 1 || entries[0].ID != "evt-2" {
		t.Fatalf("persisted outbox = %+v, want the newest evt-2", entries)
	}
	if err := dispatcher.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestDispatchRejectsUnencodablePayload proves an event whose data cannot be
// encoded is refused at the door instead of being accepted and silently lost:
// the outbox persists the body, so a body that cannot be built cannot be a
// delivery.
func TestDispatchRejectsUnencodablePayload(t *testing.T) {
	outbox := newMemoryOutbox()
	dispatcher := newTestDispatcher(Config{Outbox: outbox}, newFakeClock())
	defer dispatcher.Close()
	addEndpoint(t, dispatcher, Endpoint{ID: "ep", URL: "https://example.com/hook", Secret: "s", Enabled: true})

	err := dispatcher.Dispatch(Event{
		ID:         "evt",
		EndpointID: "ep",
		Type:       EventAccountLogin,
		Data:       map[string]any{"callback": func() {}},
	})
	if !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("Dispatch error = %v, want ErrInvalidEvent", err)
	}
	if got := outbox.len(); got != 0 {
		t.Fatalf("outbox holds %d rows after a rejected event", got)
	}
	if got := dispatcher.Pending(); got != 0 {
		t.Fatalf("pending = %d after a rejected event", got)
	}
}

// TestOutboxInMemoryWithoutStore proves the outbox works as an in-memory bound
// when no store is configured: Dispatch, cap and completion all behave, and
// RestoreOutbox is a no-op.
func TestOutboxInMemoryWithoutStore(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	clock := newFakeClock()
	dispatcher := newTestDispatcher(Config{OutboxLimit: 1}, clock)
	defer dispatcher.Close()
	addEndpoint(t, dispatcher, Endpoint{ID: "ep", URL: server.URL, Secret: "s", Enabled: true})
	if restored, skipped := dispatcher.RestoreOutbox(); restored != 0 || skipped != 0 {
		t.Fatalf("RestoreOutbox without a store = %d/%d, want 0/0", restored, skipped)
	}
	if err := dispatcher.Dispatch(Event{ID: "evt", EndpointID: "ep", Type: EventNodeConnected}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	waitFor(t, "the delivery to finish", func() bool {
		return dispatcher.Pending() == 0 && dispatcher.OutboxLen() == 0
	})
}
