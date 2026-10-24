// Package events keeps a bounded, redacted ring of customer-visible events and
// fans new events out to streaming subscribers.
//
// The ring is the replay source behind the console's event stream. A client
// that reconnects with Last-Event-ID subscribes for live events, then asks
// Since for the events it missed and skips any ID that also arrived live; that
// order leaves no gap. IDs are monotonic ("evt-000001") and a cursor either
// replays forward or reports ErrCursorExpired, so it never silently moves
// backwards.
//
// Publish never blocks on a subscriber. A subscriber that cannot keep up loses
// events (counted by Dropped) instead of stalling the publisher or its peers.
// The ring itself is bounded and overwrites its oldest event when full
// (counted by Evicted).
//
// The package starts no goroutines. Callers own their subscriber goroutines;
// Close closes every subscriber channel, and every cancel function is
// idempotent.
package events

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Defaults for the ring and its subscribers.
const (
	// DefaultCapacity is the number of events the ring retains.
	DefaultCapacity = 500
	// DefaultSubscriberBuffer is the channel depth selected when Subscribe
	// receives a non-positive buffer.
	DefaultSubscriberBuffer = 64
)

// idPrefix starts every generated event ID.
const idPrefix = "evt-"

// ErrBadCursor reports a malformed cursor: it is neither a retained event ID
// nor "evt-" followed by digits. Callers reset their stream instead of
// retrying the same cursor.
var ErrBadCursor = errors.New("events: bad cursor")

// ErrCursorExpired reports a well-formed cursor whose event is no longer in
// the ring. Callers restart from Oldest or from now. A restarted daemon reuses
// the same ID space, so a cursor from the previous process is only detectably
// expired when its sequence falls outside the current window.
var ErrCursorExpired = errors.New("events: cursor expired")

// Event is one customer-visible event. Type is the wire event name (for
// example "account.login"). At is the envelope's "ts" field. Data is scrubbed
// by Publish before it is stored or delivered; the stored map is shared
// read-only with every reader, so callers must not mutate it.
type Event struct {
	ID   string         `json:"id"`
	Type string         `json:"type"`
	At   time.Time      `json:"ts"`
	Data map[string]any `json:"data"`
}

// Ring is a fixed-capacity FIFO of events with fan-out to subscribers. It is
// safe for concurrent use.
type Ring struct {
	mu       sync.Mutex
	capacity int
	buf      []entry
	start    int // index of the oldest entry once the ring is full
	count    int
	seq      uint64 // sequence of the newest published event
	evicted  uint64
	dropped  uint64
	subs     map[uint64]*subscriber
	nextSub  uint64
	closed   bool
}

// entry pairs an event with the sequence number that makes cursors work even
// when a caller supplies its own ID.
type entry struct {
	seq   uint64
	event Event
}

type subscriber struct {
	ch chan Event
}

// NewRing returns a ring retaining capacity events. A non-positive capacity
// selects DefaultCapacity.
func NewRing(capacity int) *Ring {
	if capacity <= 0 {
		capacity = DefaultCapacity
	}
	return &Ring{
		capacity: capacity,
		buf:      make([]entry, capacity),
		subs:     make(map[uint64]*subscriber),
	}
}

// Capacity returns the fixed buffer size.
func (r *Ring) Capacity() int { return r.capacity }

// Len reports the number of retained events.
func (r *Ring) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.count
}

// Evicted reports how many events the ring has overwritten since it was
// created. It is not capped by the ring, so it measures lifetime replay loss.
func (r *Ring) Evicted() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.evicted
}

// Dropped reports how many subscriber deliveries were skipped because that
// subscriber's channel buffer was full. It is the lag signal for slow
// consumers and is never reset.
func (r *Ring) Dropped() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dropped
}

// Publish stamps event, appends it to the ring and fans it out. A missing ID
// becomes the next "evt-NNNNNN" value and a zero At becomes the current UTC
// time; caller-supplied values are kept. Data is scrubbed before storage and
// delivery, so a credential a caller accidentally puts in an event map never
// reaches a client.
//
// Publish never blocks. A subscriber whose buffer is full loses this event and
// Dropped increments; the other subscribers still receive it. After Close,
// Publish is a no-op that returns the zero Event.
//
// Caller-supplied IDs must be unique: a duplicate makes a cursor ambiguous.
// Generated IDs never collide with earlier ones because the sequence advances
// even when the caller supplies an ID.
func (r *Ring) Publish(event Event) Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return Event{}
	}
	r.seq++
	if event.ID == "" {
		event.ID = formatID(r.seq)
	}
	if event.At.IsZero() {
		event.At = time.Now().UTC()
	}
	event.Data = Scrub(event.Data)

	record := entry{seq: r.seq, event: event}
	if r.count < r.capacity {
		r.buf[(r.start+r.count)%r.capacity] = record
		r.count++
	} else {
		r.buf[r.start] = record
		r.start = (r.start + 1) % r.capacity
		r.evicted++
	}

	for _, sub := range r.subs {
		select {
		case sub.ch <- event:
		default:
			r.dropped++
		}
	}
	return event
}

// Since returns the retained events published after cursor, oldest first, at
// most limit of them. A zero or negative limit returns everything retained
// after the cursor. The second result is the cursor to pass next: the ID of
// the last returned event, or cursor unchanged when nothing was returned.
//
// An empty cursor starts at the oldest retained event. A malformed cursor
// returns ErrBadCursor. A well-formed cursor whose event is no longer retained
// returns ErrCursorExpired: the caller restarts from Oldest (or from now)
// rather than silently replaying an incomplete window.
func (r *Ring) Since(cursor string, limit int) ([]Event, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	offset, err := r.offsetAfterLocked(cursor)
	if err != nil {
		return nil, "", err
	}
	available := r.count - offset
	if limit <= 0 || limit > available {
		limit = available
	}
	if limit == 0 {
		return nil, cursor, nil
	}
	out := make([]Event, limit)
	for i := range limit {
		out[i] = r.buf[(r.start+offset+i)%r.capacity].event
	}
	return out, out[len(out)-1].ID, nil
}

// offsetAfterLocked maps a cursor to the index, counted from the oldest
// retained event, of the first event to return. The caller holds r.mu.
func (r *Ring) offsetAfterLocked(cursor string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	// A retained ID is authoritative: caller-supplied IDs need not parse as
	// generated ones, and an exact match can never be mistaken for a future
	// sequence.
	for i := range r.count {
		if r.buf[(r.start+i)%r.capacity].event.ID == cursor {
			return i + 1, nil
		}
	}
	seq, ok := parseID(cursor)
	if !ok {
		return 0, fmt.Errorf("%w: %q", ErrBadCursor, cursor)
	}
	if r.count == 0 {
		// Nothing is retained, so any named cursor is stale. A client starting
		// fresh sends an empty cursor.
		return 0, fmt.Errorf("%w: %q", ErrCursorExpired, cursor)
	}
	oldest := r.buf[r.start].seq
	newest := r.buf[(r.start+r.count-1)%r.capacity].seq
	if seq < oldest || seq > newest {
		return 0, fmt.Errorf("%w: %q", ErrCursorExpired, cursor)
	}
	for i := range r.count {
		if r.buf[(r.start+i)%r.capacity].seq == seq {
			return i + 1, nil
		}
	}
	// Sequences are contiguous, so this is unreachable unless the ring was
	// corrupted; report it as expired rather than replaying a wrong window.
	return 0, fmt.Errorf("%w: %q", ErrCursorExpired, cursor)
}

// Latest returns the newest retained event ID, or "" when the ring is empty.
func (r *Ring) Latest() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.count == 0 {
		return ""
	}
	return r.buf[(r.start+r.count-1)%r.capacity].event.ID
}

// Oldest returns the oldest retained event ID, or "" when the ring is empty.
// It is the cursor a caller uses to restart after ErrCursorExpired.
func (r *Ring) Oldest() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.count == 0 {
		return ""
	}
	return r.buf[r.start].event.ID
}

// Subscribe returns a channel of live events and a cancel function. A
// non-positive buffer selects DefaultSubscriberBuffer. The channel is closed
// by cancel or by Close; cancel is idempotent and safe to call after Close.
//
// Subscribing does not replay: call Since for the events published before the
// subscription. To avoid a gap, subscribe first and skip replayed events whose
// IDs already arrived on the channel.
func (r *Ring) Subscribe(buffer int) (<-chan Event, func()) {
	if buffer <= 0 {
		buffer = DefaultSubscriberBuffer
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	ch := make(chan Event, buffer)
	if r.closed {
		close(ch)
		return ch, func() {}
	}
	id := r.nextSub
	r.nextSub++
	r.subs[id] = &subscriber{ch: ch}
	cancel := func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		sub, ok := r.subs[id]
		if !ok {
			return
		}
		delete(r.subs, id)
		close(sub.ch)
	}
	return ch, cancel
}

// Close stops the ring: every subscriber channel is closed, later publishes
// are ignored and later subscriptions return a closed channel. Close is
// idempotent.
func (r *Ring) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.closed = true
	for id, sub := range r.subs {
		close(sub.ch)
		delete(r.subs, id)
	}
}

// formatID renders the monotonic wire ID. Six digits cover a million events;
// larger sequences simply grow the number instead of wrapping.
func formatID(seq uint64) string {
	return fmt.Sprintf(idPrefix+"%06d", seq)
}

// parseID accepts "evt-" followed by ASCII digits. It deliberately rejects
// signs, whitespace and other prefixes so a malformed Last-Event-ID is
// distinguishable from a cursor that was simply evicted.
func parseID(id string) (uint64, bool) {
	digits, ok := strings.CutPrefix(id, idPrefix)
	if !ok || digits == "" {
		return 0, false
	}
	for i := range len(digits) {
		if digits[i] < '0' || digits[i] > '9' {
			return 0, false
		}
	}
	seq, err := strconv.ParseUint(digits, 10, 64)
	if err != nil {
		return 0, false
	}
	return seq, true
}
