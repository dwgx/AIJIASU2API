package events

import (
	"errors"
	"runtime"
	"slices"
	"sync"
	"testing"
	"time"
)

func TestNewRingDefaults(t *testing.T) {
	for _, capacity := range []int{0, -1} {
		ring := NewRing(capacity)
		if got := ring.Capacity(); got != DefaultCapacity {
			t.Fatalf("NewRing(%d).Capacity() = %d, want %d", capacity, got, DefaultCapacity)
		}
		ring.Close()
	}
	ring := NewRing(7)
	defer ring.Close()
	if got := ring.Capacity(); got != 7 {
		t.Fatalf("NewRing(7).Capacity() = %d, want 7", got)
	}
}

func TestPublishStampsMonotonicIDs(t *testing.T) {
	ring := NewRing(8)
	defer ring.Close()

	for i := range 3 {
		got := ring.Publish(Event{Type: "account.login"})
		want := formatID(uint64(i + 1))
		if got.ID != want {
			t.Fatalf("publish %d ID = %q, want %q", i, got.ID, want)
		}
		if got.At.IsZero() {
			t.Fatalf("publish %d left At zero", i)
		}
		if got.At.Location() != time.UTC {
			t.Fatalf("publish %d At location = %v, want UTC", i, got.At.Location())
		}
		if got.Data != nil {
			t.Fatalf("publish %d invented data: %#v", i, got.Data)
		}
	}
	if got := ring.Latest(); got != "evt-000003" {
		t.Fatalf("Latest = %q, want evt-000003", got)
	}
	if got := ring.Len(); got != 3 {
		t.Fatalf("Len = %d, want 3", got)
	}
	if got := ring.Evicted(); got != 0 {
		t.Fatalf("Evicted = %d, want 0", got)
	}
	if got := ring.Dropped(); got != 0 {
		t.Fatalf("Dropped = %d, want 0", got)
	}

	// A caller-supplied ID and timestamp are kept, and the sequence still
	// advances so the next generated ID cannot collide with the custom one.
	at := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	custom := ring.Publish(Event{ID: "custom-1", Type: "audit.critical", At: at})
	if custom.ID != "custom-1" {
		t.Fatalf("custom ID = %q", custom.ID)
	}
	if !custom.At.Equal(at) {
		t.Fatalf("custom At = %v, want %v", custom.At, at)
	}
	if got := ring.Latest(); got != "custom-1" {
		t.Fatalf("Latest = %q, want custom-1", got)
	}
	next := ring.Publish(Event{Type: "account.login"})
	if next.ID != "evt-000005" {
		t.Fatalf("generated ID after custom = %q, want evt-000005", next.ID)
	}
}

func TestRingDropsOldestWhenFull(t *testing.T) {
	ring := NewRing(2)
	defer ring.Close()
	for range 3 {
		ring.Publish(Event{Type: "node.connected"})
	}

	if got := ring.Len(); got != 2 {
		t.Fatalf("Len = %d, want 2", got)
	}
	if got := ring.Evicted(); got != 1 {
		t.Fatalf("Evicted = %d, want 1", got)
	}
	if got := ring.Dropped(); got != 0 {
		t.Fatalf("Dropped = %d, want 0 (no subscribers)", got)
	}
	events, next, err := ring.Since("", 0)
	if err != nil {
		t.Fatalf("Since: %v", err)
	}
	want := []string{"evt-000002", "evt-000003"}
	if got := idsOf(events); !slices.Equal(got, want) {
		t.Fatalf("retained IDs = %v, want %v", got, want)
	}
	if next != "evt-000003" {
		t.Fatalf("next cursor = %q, want evt-000003", next)
	}
	if got := ring.Latest(); got != "evt-000003" {
		t.Fatalf("Latest = %q, want evt-000003", got)
	}
}

func TestSincePagesAndCursorEdges(t *testing.T) {
	ring := NewRing(10)
	defer ring.Close()
	for range 5 {
		ring.Publish(Event{Type: "schedule.fired"})
	}

	// Paging with limit 2 must visit every event once, in order.
	var paged []string
	cursor := ""
	for range 10 {
		batch, next, err := ring.Since(cursor, 2)
		if err != nil {
			t.Fatalf("Since(%q, 2): %v", cursor, err)
		}
		paged = append(paged, idsOf(batch)...)
		if len(batch) == 0 {
			if next != cursor {
				t.Fatalf("empty page moved cursor %q -> %q", cursor, next)
			}
			break
		}
		if next != batch[len(batch)-1].ID {
			t.Fatalf("next cursor = %q, want last ID %q", next, batch[len(batch)-1].ID)
		}
		if next == cursor {
			t.Fatalf("cursor did not advance past %q", cursor)
		}
		cursor = next
	}
	want := []string{"evt-000001", "evt-000002", "evt-000003", "evt-000004", "evt-000005"}
	if !slices.Equal(paged, want) {
		t.Fatalf("paged IDs = %v, want %v", paged, want)
	}

	// A zero or negative limit means "everything retained after the cursor".
	for _, limit := range []int{0, -1} {
		batch, next, err := ring.Since("evt-000003", limit)
		if err != nil {
			t.Fatalf("Since(limit=%d): %v", limit, err)
		}
		if got, want := idsOf(batch), []string{"evt-000004", "evt-000005"}; !slices.Equal(got, want) {
			t.Fatalf("Since(limit=%d) = %v, want %v", limit, got, want)
		}
		if next != "evt-000005" {
			t.Fatalf("Since(limit=%d) next = %q", limit, next)
		}
	}

	// A well-formed cursor that names no retained event is expired, not bad.
	for _, cursor := range []string{"evt-000000", "evt-000006"} {
		if _, _, err := ring.Since(cursor, 0); !errors.Is(err, ErrCursorExpired) {
			t.Fatalf("Since(%q) error = %v, want ErrCursorExpired", cursor, err)
		}
	}

	// A cursor at the newest event yields an empty page and stays put.
	batch, next, err := ring.Since("evt-000005", 0)
	if err != nil {
		t.Fatalf("Since(latest): %v", err)
	}
	if len(batch) != 0 || next != "evt-000005" {
		t.Fatalf("Since(latest) = %v, %q; want empty, evt-000005", idsOf(batch), next)
	}

	for _, cursor := range []string{
		"evt-",
		"evt-abc",
		"evt--1",
		"evt- 1",
		"nope",
		"evt-999999999999999999999999999999", // overflows uint64
	} {
		if _, _, err := ring.Since(cursor, 0); !errors.Is(err, ErrBadCursor) {
			t.Fatalf("Since(%q) error = %v, want ErrBadCursor", cursor, err)
		}
	}
}

func TestReplayFromLastEventID(t *testing.T) {
	ring := NewRing(16)
	defer ring.Close()

	ring.Publish(Event{Type: "account.switch.started"})
	ring.Publish(Event{Type: "account.switch.completed"})
	lastSeen := ring.Latest()
	if lastSeen != "evt-000002" {
		t.Fatalf("Last-Event-ID = %q, want evt-000002", lastSeen)
	}

	// The stream drops here; the daemon keeps publishing.
	ring.Publish(Event{Type: "node.state"})
	ring.Publish(Event{Type: "session.state"})

	batch, next, err := ring.Since(lastSeen, 0)
	if err != nil {
		t.Fatalf("Since(Last-Event-ID): %v", err)
	}
	if got, want := idsOf(batch), []string{"evt-000003", "evt-000004"}; !slices.Equal(got, want) {
		t.Fatalf("replay = %v, want %v", got, want)
	}
	if batch[0].Type != "node.state" || batch[1].Type != "session.state" {
		t.Fatalf("replay types = %q, %q", batch[0].Type, batch[1].Type)
	}
	if next != "evt-000004" {
		t.Fatalf("next cursor = %q, want evt-000004", next)
	}

	// Reconnecting again with the advanced cursor replays nothing.
	batch, next, err = ring.Since(next, 0)
	if err != nil || len(batch) != 0 || next != "evt-000004" {
		t.Fatalf("second replay = %v, %q, %v", idsOf(batch), next, err)
	}
}

func TestSinceEmptyRing(t *testing.T) {
	ring := NewRing(4)
	defer ring.Close()

	batch, next, err := ring.Since("", 0)
	if err != nil || len(batch) != 0 || next != "" {
		t.Fatalf("Since(empty) = %v, %q, %v", idsOf(batch), next, err)
	}
	for _, cursor := range []string{"evt-000000", "evt-000001"} {
		if _, _, err := ring.Since(cursor, 0); !errors.Is(err, ErrCursorExpired) {
			t.Fatalf("Since(%q) error = %v, want ErrCursorExpired", cursor, err)
		}
	}
	if _, _, err := ring.Since("nope", 0); !errors.Is(err, ErrBadCursor) {
		t.Fatalf("Since(nope) error = %v, want ErrBadCursor", err)
	}
	if ring.Latest() != "" || ring.Oldest() != "" {
		t.Fatalf("Latest/Oldest = %q/%q, want empty", ring.Latest(), ring.Oldest())
	}
}

func TestSinceReplaysAfterWrap(t *testing.T) {
	ring := NewRing(3)
	defer ring.Close()
	for range 5 {
		ring.Publish(Event{Type: "device.retired"})
	}
	if got := ring.Evicted(); got != 2 {
		t.Fatalf("Evicted = %d, want 2", got)
	}
	if got := ring.Oldest(); got != "evt-000003" {
		t.Fatalf("Oldest = %q, want evt-000003", got)
	}
	if got := ring.Latest(); got != "evt-000005" {
		t.Fatalf("Latest = %q, want evt-000005", got)
	}

	cases := []struct {
		cursor string
		want   []string
	}{
		{"", []string{"evt-000003", "evt-000004", "evt-000005"}},
		{"evt-000003", []string{"evt-000004", "evt-000005"}},
		{"evt-000004", []string{"evt-000005"}},
		{"evt-000005", nil},
	}
	for _, tc := range cases {
		batch, _, err := ring.Since(tc.cursor, 0)
		if err != nil {
			t.Fatalf("Since(%q): %v", tc.cursor, err)
		}
		if got := idsOf(batch); !slices.Equal(got, tc.want) {
			t.Fatalf("Since(%q) = %v, want %v", tc.cursor, got, tc.want)
		}
	}

	// Every cursor whose event was evicted (or that names a future event)
	// expires; the caller restarts from Oldest.
	for _, cursor := range []string{"evt-000000", "evt-000001", "evt-000002", "evt-000006"} {
		if _, _, err := ring.Since(cursor, 0); !errors.Is(err, ErrCursorExpired) {
			t.Fatalf("Since(%q) error = %v, want ErrCursorExpired", cursor, err)
		}
	}
	batch, next, err := ring.Since(ring.Oldest(), 0)
	if err != nil {
		t.Fatalf("Since(Oldest): %v", err)
	}
	if got, want := idsOf(batch), []string{"evt-000004", "evt-000005"}; !slices.Equal(got, want) {
		t.Fatalf("Since(Oldest) = %v, want %v", got, want)
	}
	if next != "evt-000005" {
		t.Fatalf("Since(Oldest) next = %q", next)
	}
}

func TestSinceAcceptsCallerSuppliedIDs(t *testing.T) {
	ring := NewRing(8)
	defer ring.Close()
	ring.Publish(Event{ID: "custom-1", Type: "account.login"})
	ring.Publish(Event{ID: "custom-2", Type: "account.logout"})
	ring.Publish(Event{Type: "account.expired"})

	cases := []struct {
		cursor string
		want   []string
	}{
		{"custom-1", []string{"custom-2", "evt-000003"}},
		{"custom-2", []string{"evt-000003"}},
		{"evt-000001", []string{"custom-2", "evt-000003"}}, // sequence fallback
	}
	for _, tc := range cases {
		batch, _, err := ring.Since(tc.cursor, 0)
		if err != nil {
			t.Fatalf("Since(%q): %v", tc.cursor, err)
		}
		if got := idsOf(batch); !slices.Equal(got, tc.want) {
			t.Fatalf("Since(%q) = %v, want %v", tc.cursor, got, tc.want)
		}
	}
	if _, _, err := ring.Since("custom-3", 0); !errors.Is(err, ErrBadCursor) {
		t.Fatalf("Since(unknown custom) error = %v, want ErrBadCursor", err)
	}
}

func TestSubscribeFanOut(t *testing.T) {
	ring := NewRing(10)
	defer ring.Close()

	channels := make([]<-chan Event, 3)
	cancels := make([]func(), 3)
	for i := range channels {
		channels[i], cancels[i] = ring.Subscribe(4)
	}
	for range 3 {
		ring.Publish(Event{Type: "node.connected"})
	}

	for i, ch := range channels {
		for j := range 3 {
			event := recvEvent(t, ch)
			want := formatID(uint64(j + 1))
			if event.ID != want {
				t.Fatalf("subscriber %d event %d ID = %q, want %q", i, j, event.ID, want)
			}
			if event.Type != "node.connected" {
				t.Fatalf("subscriber %d event %d type = %q", i, j, event.Type)
			}
		}
	}
	for _, cancel := range cancels {
		cancel()
	}
}

func TestSlowSubscriberIsIsolated(t *testing.T) {
	ring := NewRing(2000)
	defer ring.Close()

	slow, cancelSlow := ring.Subscribe(1)
	defer cancelSlow()
	fast, cancelFast := ring.Subscribe(1000)
	defer cancelFast()

	// If Publish blocked on the slow subscriber this loop would never finish.
	for range 1000 {
		ring.Publish(Event{Type: "account.login"})
	}

	if got := ring.Dropped(); got != 999 {
		t.Fatalf("Dropped = %d, want 999", got)
	}
	if got := ring.Evicted(); got != 0 {
		t.Fatalf("Evicted = %d, want 0 (capacity 2000)", got)
	}

	first := recvEvent(t, slow)
	if first.ID != "evt-000001" {
		t.Fatalf("slow subscriber got %q, want evt-000001", first.ID)
	}
	requireNoEvent(t, slow)

	for i := range 1000 {
		event := recvEvent(t, fast)
		if want := formatID(uint64(i + 1)); event.ID != want {
			t.Fatalf("fast subscriber event %d = %q, want %q", i, event.ID, want)
		}
	}
	if got := ring.Len(); got != 1000 {
		t.Fatalf("Len = %d, want 1000", got)
	}
}

func TestSubscribeCancelIsIdempotent(t *testing.T) {
	ring := NewRing(4)
	defer ring.Close()

	ch, cancel := ring.Subscribe(0)
	if got := cap(ch); got != DefaultSubscriberBuffer {
		t.Fatalf("Subscribe(0) buffer = %d, want %d", got, DefaultSubscriberBuffer)
	}
	cancel()

	ch, cancel = ring.Subscribe(1)
	ring.Publish(Event{Type: "account.login"})
	cancel()
	cancel() // second cancel must be a no-op

	event, ok := <-ch
	if !ok || event.Type != "account.login" {
		t.Fatalf("buffered event = %#v, %v; want account.login", event, ok)
	}
	if _, ok := <-ch; ok {
		t.Fatal("channel still open after cancel")
	}

	// Publishing after cancel must neither panic nor count a drop.
	ring.Publish(Event{Type: "account.logout"})
	if got := ring.Dropped(); got != 0 {
		t.Fatalf("Dropped = %d, want 0", got)
	}
	if got := ring.Len(); got != 2 {
		t.Fatalf("Len = %d, want 2", got)
	}
}

func TestCloseClosesSubscribersAndPublishIsNoop(t *testing.T) {
	ring := NewRing(4)
	first, cancelFirst := ring.Subscribe(2)
	second, _ := ring.Subscribe(2)

	ring.Publish(Event{Type: "account.login"})
	ring.Close()
	ring.Close() // idempotent

	for i, ch := range []<-chan Event{first, second} {
		event, ok := <-ch
		if !ok || event.Type != "account.login" {
			t.Fatalf("subscriber %d buffered event = %#v, %v", i, event, ok)
		}
		if _, ok := <-ch; ok {
			t.Fatalf("subscriber %d channel still open after Close", i)
		}
	}
	cancelFirst() // after Close must not panic

	got := ring.Publish(Event{Type: "account.logout"})
	if got.ID != "" || got.Type != "" || !got.At.IsZero() || got.Data != nil {
		t.Fatalf("Publish after Close = %#v, want zero Event", got)
	}
	if got := ring.Len(); got != 1 {
		t.Fatalf("Len = %d, want 1", got)
	}
	if got := ring.Latest(); got != "evt-000001" {
		t.Fatalf("Latest = %q, want evt-000001", got)
	}

	late, cancelLate := ring.Subscribe(1)
	if _, ok := <-late; ok {
		t.Fatal("Subscribe after Close returned an open channel")
	}
	cancelLate()
}

func TestCloseReleasesSubscriberGoroutines(t *testing.T) {
	before := runtime.NumGoroutine()
	ring := NewRing(32)

	var readers sync.WaitGroup
	for range 8 {
		ch, cancel := ring.Subscribe(1)
		readers.Add(1)
		go func() {
			defer readers.Done()
			defer cancel()
			for range ch {
			}
		}()
	}
	for range 4 {
		ring.Publish(Event{Type: "schedule.fired"})
	}
	ring.Close()
	readers.Wait()

	waitFor(t, 2*time.Second, "subscriber goroutines to exit", func() bool {
		return runtime.NumGoroutine() <= before
	})
}

func TestConcurrentPublishSubscribeCancel(t *testing.T) {
	ring := NewRing(64)
	const publishers = 4
	const perPublisher = 200

	var publishWG sync.WaitGroup
	for publisher := range publishers {
		publishWG.Add(1)
		go func() {
			defer publishWG.Done()
			for i := range perPublisher {
				ring.Publish(Event{
					Type: "account.login",
					Data: map[string]any{"publisher": publisher, "seq": i},
				})
			}
		}()
	}

	var readWG sync.WaitGroup
	for subscriber := range 4 {
		ch, cancel := ring.Subscribe(4)
		readWG.Add(1)
		go func() {
			defer readWG.Done()
			if subscriber%2 == 0 {
				<-ch // cancel while the publishers are still running
				cancel()
				return
			}
			for range ch {
			}
			cancel()
		}()
	}

	publishWG.Wait()
	ring.Close()
	readWG.Wait()

	if got, limit := ring.Len(), ring.Capacity(); got > limit {
		t.Fatalf("Len = %d, exceeds capacity %d", got, limit)
	}
	events, _, err := ring.Since("", 0)
	if err != nil {
		t.Fatalf("Since: %v", err)
	}
	if len(events) != ring.Len() {
		t.Fatalf("Since returned %d events, Len = %d", len(events), ring.Len())
	}
	if want := uint64(publishers*perPublisher - ring.Len()); ring.Evicted() != want {
		t.Fatalf("Evicted = %d, want %d", ring.Evicted(), want)
	}
	seen := make(map[string]struct{}, len(events))
	for _, event := range events {
		if _, dup := seen[event.ID]; dup {
			t.Fatalf("duplicate ID %q in retained ring", event.ID)
		}
		seen[event.ID] = struct{}{}
	}
}

func idsOf(events []Event) []string {
	out := make([]string, len(events))
	for i, event := range events {
		out[i] = event.ID
	}
	return out
}

func recvEvent(t *testing.T, ch <-chan Event) Event {
	t.Helper()
	select {
	case event, ok := <-ch:
		if !ok {
			t.Fatal("channel closed while waiting for an event")
		}
		return event
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for an event")
		return Event{}
	}
}

func requireNoEvent(t *testing.T, ch <-chan Event) {
	t.Helper()
	select {
	case event, ok := <-ch:
		if ok {
			t.Fatalf("unexpected extra event %#v", event)
		}
	case <-time.After(50 * time.Millisecond):
	}
}

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestFormatAndParseID(t *testing.T) {
	for _, seq := range []uint64{0, 1, 42, 999999, 1000000} {
		id := formatID(seq)
		got, ok := parseID(id)
		if !ok || got != seq {
			t.Fatalf("parseID(%q) = %d, %v; want %d, true", id, got, ok, seq)
		}
	}
	if _, ok := parseID("evt-000001x"); ok {
		t.Fatal("parseID accepted a trailing letter")
	}
	if _, ok := parseID("EVT-000001"); ok {
		t.Fatal("parseID accepted an uppercase prefix")
	}
	if got := formatID(1); got != "evt-000001" {
		t.Fatalf("formatID(1) = %q", got)
	}
	if got := formatID(1000000); got != "evt-1000000" {
		t.Fatalf("formatID(1000000) = %q", got)
	}
}
