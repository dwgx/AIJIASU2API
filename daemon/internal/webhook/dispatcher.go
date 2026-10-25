package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Dispatcher defaults. The queue bounds accepted-but-unfinished deliveries;
// the worker count bounds concurrent HTTP requests.
const (
	DefaultQueueSize    = 256
	DefaultWorkers      = 4
	DefaultMaxAttempts  = 5
	DefaultTimeout      = 10 * time.Second
	DefaultDedupeWindow = 60 * time.Second
	// DefaultHookQueueSize bounds the queue between the delivery workers and
	// the OnDelivery sink. Persisting an attempt is an in-memory append, so a
	// small queue is enough; a slow sink degrades to dropped rows, never to a
	// delayed delivery.
	DefaultHookQueueSize = 256

	// deadLetterDedupeSize bounds the set of delivery ids OnDeadLetter has
	// already reported. A fixed ring keeps a long-lived daemon's memory flat;
	// after the ring wraps, an ancient id can be reported again.
	deadLetterDedupeSize = 256
)

// DefaultBackoff is the delay before attempt n+1. The last entry repeats when
// MaxAttempts exceeds the schedule length.
var DefaultBackoff = [5]time.Duration{
	1 * time.Second,
	5 * time.Second,
	30 * time.Second,
	2 * time.Minute,
	10 * time.Minute,
}

// Config configures a Dispatcher. Zero values select the defaults. Now and
// Sleep are injectable so tests run the retry schedule without real waits;
// Sleep must let simulated time pass on the same clock Now reads.
type Config struct {
	QueueSize    int
	Workers      int
	MaxAttempts  int
	Backoff      []time.Duration
	Timeout      time.Duration
	DedupeWindow time.Duration
	RingSize     int

	// AllowHTTP permits plain HTTP receivers. Loopback receivers are always
	// allowed. AJS_WEBHOOK_ALLOW_HTTP=1 in the environment has the same effect.
	AllowHTTP bool
	// AllowPrivate permits receivers on loopback and private networks. It is
	// the lab/test escape hatch; AJS_WEBHOOK_ALLOW_PRIVATE=1 has the same
	// effect. Without it a receiver may only point at a public address, and
	// cloud metadata names are rejected regardless.
	AllowPrivate bool

	Now        func() time.Time
	Sleep      func(ctx context.Context, d time.Duration) error
	Logger     *slog.Logger
	HTTPClient *http.Client

	// OnDelivery receives every recorded attempt: success, retry, dead letter
	// and abandoned-queue rows alike. It runs on one background goroutine fed
	// by a bounded queue, never on a delivery worker, so a slow sink cannot
	// delay or block a delivery. When the queue is full the attempt is dropped
	// and counted by HookDropped. Nil disables the hook.
	OnDelivery func(Delivery)
	// HookQueueSize bounds the OnDelivery queue. A non-positive value selects
	// DefaultHookQueueSize.
	HookQueueSize int

	// OnDeadLetter receives each delivery that exhausted its retries, at most
	// once per delivery id (the recent ids are kept in a fixed-size ring).
	// Unlike OnDelivery it runs synchronously on the delivery worker after the
	// final attempt is recorded, so a slow sink delays that endpoint's next
	// delivery; the operator audit write it usually drives is an in-memory
	// append. Nil disables the hook.
	OnDeadLetter func(Delivery)

	// Outbox persists the pending-delivery set so a restart resumes it instead
	// of losing every accepted event. Every entry is written before its first
	// attempt and removed only after the delivery succeeds, dead-letters or is
	// evicted by OutboxLimit. RestoreOutbox re-queues the surviving entries
	// once the receivers are registered. Nil keeps the outbox in memory only.
	Outbox OutboxStore
	// OutboxLimit bounds the pending set. A non-positive value selects
	// DefaultOutboxLimit. When the limit is reached the oldest entry is
	// dropped, counted by OutboxDropped and reported to OnOutboxDrop.
	OutboxLimit int
	// OnOutboxDrop receives each entry evicted by OutboxLimit, synchronously on
	// the dispatching goroutine. The operator audit write it usually drives is
	// an in-memory append; a slow sink delays Dispatch, so it must not block.
	// Nil disables the hook.
	OnOutboxDrop func(OutboxEntry)
}

// Dispatcher delivers events to registered endpoints. It is safe for
// concurrent use.
type Dispatcher struct {
	cfg       Config
	logger    *slog.Logger
	client    *http.Client
	allowHTTP bool
	// allowPrivate disables the loopback/private address rejection. It is
	// captured at construction so the environment cannot flip mid-flight.
	allowPrivate bool

	ctx    context.Context
	cancel context.CancelFunc
	sem    chan struct{}

	mu        sync.Mutex
	closed    bool
	endpoints map[string]Endpoint
	states    map[string]*endpointState
	pending   int
	dedupe    map[string]time.Time

	// outbox is the authoritative pending set, keyed by OutboxEntry.Key. A
	// record leaves it on success, dead letter, or eviction; cfg.Outbox mirrors
	// every transition so a restart can resume the set.
	outbox        map[string]*outboxRecord
	outboxDropped atomic.Int64

	log *DeliveryLog
	wg  sync.WaitGroup

	// hookCh feeds OnDelivery from a single background goroutine. It is nil
	// when no hook is configured; a full queue increments hookDropped instead
	// of blocking the delivery worker.
	hookCh      chan Delivery
	hookDropped atomic.Int64
	hookWG      sync.WaitGroup

	// deadLetterSeen holds the delivery ids already reported to OnDeadLetter.
	// deadLetterRing is the eviction order for that set and is preallocated at
	// deadLetterDedupeSize, so reporting a dead letter allocates nothing.
	deadLetterMu   sync.Mutex
	deadLetterSeen map[string]struct{}
	deadLetterRing []string
	deadLetterNext int
}

type endpointState struct {
	pending []job
	running bool
}

type job struct {
	endpoint Endpoint
	event    Event
	// record is the outbox entry this job delivers. It carries the exact body
	// and the attempts already spent, so a restored entry resumes where the
	// previous process stopped.
	record *outboxRecord
}

// New builds a dispatcher. Deliveries start as soon as Dispatch accepts one.
func New(cfg Config) *Dispatcher {
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = DefaultQueueSize
	}
	if cfg.Workers <= 0 {
		cfg.Workers = DefaultWorkers
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = DefaultMaxAttempts
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.DedupeWindow <= 0 {
		cfg.DedupeWindow = DefaultDedupeWindow
	}
	if cfg.Backoff == nil {
		cfg.Backoff = append([]time.Duration(nil), DefaultBackoff[:]...)
	}
	if cfg.OutboxLimit <= 0 {
		cfg.OutboxLimit = DefaultOutboxLimit
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Sleep == nil {
		cfg.Sleep = sleepContext
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	allowPrivate := cfg.AllowPrivate || os.Getenv(AllowPrivateEnv) == "1"
	client := &http.Client{
		Transport: secureTransport(allowPrivate),
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	if cfg.HTTPClient != nil {
		copied := *cfg.HTTPClient
		copied.CheckRedirect = client.CheckRedirect
		if copied.Transport == nil {
			copied.Transport = client.Transport
		}
		client = &copied
	}

	ctx, cancel := context.WithCancel(context.Background())
	if cfg.OnDelivery != nil && cfg.HookQueueSize <= 0 {
		cfg.HookQueueSize = DefaultHookQueueSize
	}
	dispatcher := &Dispatcher{
		cfg:            cfg,
		logger:         cfg.Logger,
		client:         client,
		allowHTTP:      cfg.AllowHTTP || os.Getenv("AJS_WEBHOOK_ALLOW_HTTP") == "1",
		allowPrivate:   allowPrivate,
		ctx:            ctx,
		cancel:         cancel,
		sem:            make(chan struct{}, cfg.Workers),
		endpoints:      make(map[string]Endpoint),
		states:         make(map[string]*endpointState),
		dedupe:         make(map[string]time.Time),
		outbox:         make(map[string]*outboxRecord),
		log:            NewDeliveryLog(cfg.RingSize),
		deadLetterSeen: make(map[string]struct{}, deadLetterDedupeSize),
		deadLetterRing: make([]string, deadLetterDedupeSize),
	}
	if cfg.OnDelivery != nil {
		dispatcher.hookCh = make(chan Delivery, cfg.HookQueueSize)
		dispatcher.hookWG.Add(1)
		go dispatcher.runDeliveryHook()
	}
	return dispatcher
}

// AddEndpoint registers a receiver. The URL must be HTTPS unless the receiver
// is loopback or HTTP is explicitly allowed, and the secret must be non-empty
// because every delivery is signed.
func (d *Dispatcher) AddEndpoint(ep Endpoint) error {
	if err := validateEndpoint(ep, d.allowHTTP, d.allowPrivate); err != nil {
		return err
	}
	if ep.CreatedAt.IsZero() {
		ep.CreatedAt = d.cfg.Now()
	}
	ep.Events = append([]EventType(nil), ep.Events...)

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return ErrClosed
	}
	if _, exists := d.endpoints[ep.ID]; exists {
		return fmt.Errorf("%w: %q", ErrEndpointExists, ep.ID)
	}
	d.endpoints[ep.ID] = ep
	return nil
}

// RemoveEndpoint unregisters a receiver. Queued deliveries for it still run;
// new dispatches are rejected.
func (d *Dispatcher) RemoveEndpoint(id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, exists := d.endpoints[id]; !exists {
		return false
	}
	delete(d.endpoints, id)
	return true
}

// SetEndpointEnabled toggles a receiver. Disabled endpoints reject new
// dispatches.
func (d *Dispatcher) SetEndpointEnabled(id string, enabled bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	ep, exists := d.endpoints[id]
	if !exists {
		return fmt.Errorf("%w: %q", ErrEndpointMissing, id)
	}
	ep.Enabled = enabled
	d.endpoints[id] = ep
	return nil
}

// Endpoint returns a copy of a registered endpoint.
func (d *Dispatcher) Endpoint(id string) (Endpoint, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	ep, ok := d.endpoints[id]
	return ep, ok
}

// Endpoints returns every registered endpoint sorted by id.
func (d *Dispatcher) Endpoints() []Endpoint {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]Endpoint, 0, len(d.endpoints))
	for _, ep := range d.endpoints {
		ep.Events = append([]EventType(nil), ep.Events...)
		out = append(out, ep)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Dispatch accepts an event for delivery. It never blocks: a full queue,
// duplicate event id, unknown or disabled endpoint and unsubscribed event are
// reported immediately.
//
// The payload is built and scrubbed here, once, so the outbox can persist the
// exact body before the first attempt; a body that cannot be encoded is
// rejected as an invalid event instead of being accepted and lost.
func (d *Dispatcher) Dispatch(event Event) error {
	if event.ID == "" {
		return fmt.Errorf("%w: id is required", ErrInvalidEvent)
	}
	if !event.Type.Valid() {
		return fmt.Errorf("%w: unknown event type %q", ErrInvalidEvent, event.Type)
	}
	if event.At.IsZero() {
		event.At = d.cfg.Now()
	}
	body, err := json.Marshal(Payload{
		ID:    event.ID,
		Event: event.Type,
		At:    event.At,
		Data:  Scrub(event.Data),
	})
	if err != nil {
		return fmt.Errorf("%w: encode payload: %v", ErrInvalidEvent, err)
	}

	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return ErrClosed
	}
	ep, ok := d.endpoints[event.EndpointID]
	if !ok {
		d.mu.Unlock()
		return fmt.Errorf("%w: %q", ErrUnknownEndpoint, event.EndpointID)
	}
	if !ep.Enabled {
		d.mu.Unlock()
		return fmt.Errorf("%w: %q", ErrEndpointDisabled, event.EndpointID)
	}
	if !ep.subscribes(event.Type) {
		d.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrNotSubscribed, event.Type)
	}

	now := d.cfg.Now()
	d.purgeDedupeLocked(now)
	if _, duplicate := d.dedupe[event.ID]; duplicate {
		d.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrDuplicate, event.ID)
	}
	if d.pending >= d.cfg.QueueSize {
		d.mu.Unlock()
		return ErrQueueFull
	}

	record := &outboxRecord{entry: OutboxEntry{
		ID:            event.ID,
		EndpointID:    ep.ID,
		Event:         event.Type,
		PayloadHash:   PayloadDigest(body),
		Payload:       body,
		Attempts:      0,
		NextAttemptAt: now,
		CreatedAt:     now,
	}}
	entry := record.entry
	d.outbox[record.entry.Key()] = record
	d.dedupe[event.ID] = now
	d.pending++
	state := d.states[ep.ID]
	if state == nil {
		state = &endpointState{}
		d.states[ep.ID] = state
	}
	state.pending = append(state.pending, job{endpoint: ep, event: event, record: record})
	start := !state.running
	if start {
		state.running = true
		d.wg.Add(1)
	}
	evicted := d.evictOutboxLocked(record)
	d.mu.Unlock()

	// Persist and report outside the lock: the outbox store and the drop hook
	// may reach back into the dispatcher (the audit hook dispatches events), so
	// calling them while d.mu is held could deadlock.
	//
	// The new entry is written before the drops are reported, so a re-entrant
	// Dispatch from the drop hook cannot observe a key that is in memory but
	// still missing from the store.
	d.persistLiveOutbox(record, entry)
	d.reportOutboxDrops(evicted)
	// The entry is durable before the first attempt starts.
	if start {
		go d.runEndpoint(state)
	}
	return nil
}

func (d *Dispatcher) purgeDedupeLocked(now time.Time) {
	for id, seen := range d.dedupe {
		if now.Sub(seen) >= d.cfg.DedupeWindow {
			delete(d.dedupe, id)
		}
	}
}

// Pending reports deliveries accepted but not yet finished.
func (d *Dispatcher) Pending() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.pending
}

// Log exposes the delivery ring. The returned log is safe for concurrent use.
func (d *Dispatcher) Log() *DeliveryLog { return d.log }

// Close stops the dispatcher. In-flight attempts are cancelled, queued
// deliveries are recorded as failed and no further dispatches are accepted.
// Pending outbox entries are left in place: a delivery that was queued or on the
// wire is resumed by the next process instead of being lost.
func (d *Dispatcher) Close() error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil
	}
	d.closed = true
	d.mu.Unlock()

	d.cancel()
	d.wg.Wait()

	d.mu.Lock()
	var queued []job
	for _, state := range d.states {
		queued = append(queued, state.pending...)
		state.pending = nil
	}
	d.pending = 0
	d.mu.Unlock()

	for _, leftover := range queued {
		d.record(Delivery{
			ID:         leftover.event.ID + ":0",
			EndpointID: leftover.endpoint.ID,
			Event:      leftover.event.Type,
			Status:     StatusFailed,
			Error:      "dispatcher closed",
			At:         d.cfg.Now(),
		})
	}
	// Only Close sends the close, and only after every delivery worker has
	// stopped and the leftover rows above are recorded, so no send can race
	// the close. Draining the channel lets queued attempts reach the sink
	// before Close returns.
	if d.hookCh != nil {
		close(d.hookCh)
		d.hookWG.Wait()
	}
	return nil
}

// runEndpoint drains one endpoint's FIFO. A single goroutine per endpoint is
// what guarantees one in-flight delivery per endpoint, in submission order.
func (d *Dispatcher) runEndpoint(state *endpointState) {
	defer d.wg.Done()
	for {
		d.mu.Lock()
		if d.closed || len(state.pending) == 0 {
			state.running = false
			d.mu.Unlock()
			return
		}
		queued := state.pending[0]
		state.pending = state.pending[1:]
		d.mu.Unlock()

		if !d.acquire() {
			d.record(Delivery{
				ID:         queued.event.ID + ":0",
				EndpointID: queued.endpoint.ID,
				Event:      queued.event.Type,
				Status:     StatusFailed,
				Error:      "dispatcher closed",
				At:         d.cfg.Now(),
			})
			d.finish()
			return
		}
		d.deliver(queued)
		<-d.sem
		d.finish()
	}
}

func (d *Dispatcher) acquire() bool {
	select {
	case d.sem <- struct{}{}:
		return true
	case <-d.ctx.Done():
		return false
	}
}

func (d *Dispatcher) finish() {
	d.mu.Lock()
	d.pending--
	d.mu.Unlock()
}

func (d *Dispatcher) deliver(queued job) {
	record := queued.record
	// A restored entry carries the backoff remainder from the previous
	// process: wait it out instead of hammering the receiver right after a
	// restart. A fresh entry is due immediately.
	if delay := record.entry.NextAttemptAt.Sub(d.cfg.Now()); delay > 0 {
		if err := d.cfg.Sleep(d.ctx, delay); err != nil {
			// Shutdown during the wait: the entry stays in the outbox with its
			// attempts and next-attempt time, so the next process resumes here.
			return
		}
	}

	for attempt := record.entry.Attempts + 1; attempt <= d.cfg.MaxAttempts; attempt++ {
		if d.ctx.Err() != nil {
			// Shutdown cancelled the attempt that was on the wire. Keep the
			// entry for the next process instead of spending the rest of the
			// retry budget on a dead context.
			return
		}
		if !d.outboxLive(record) {
			// Evicted by the cap while queued: the delivery was abandoned.
			return
		}
		started := d.cfg.Now()
		httpStatus, errText, retryable := d.send(queued.endpoint, queued.event, record.entry.Payload, attempt)
		delivery := Delivery{
			ID:         fmt.Sprintf("%s:%d", queued.event.ID, attempt),
			EndpointID: queued.endpoint.ID,
			Event:      queued.event.Type,
			Attempt:    attempt,
			Status:     StatusFailed,
			HTTPStatus: httpStatus,
			Error:      errText,
			At:         started,
			DurationMs: d.cfg.Now().Sub(started).Milliseconds(),
		}
		if errText == "" {
			delivery.Status = StatusSuccess
			d.completeOutbox(record)
			d.record(delivery)
			return
		}
		if !retryable || attempt == d.cfg.MaxAttempts {
			if retryable {
				delivery.Status = StatusDeadLetter
			}
			d.completeOutbox(record)
			d.record(delivery)
			return
		}
		d.record(delivery)
		// Persist the failure and the next due time before waiting, so a crash
		// during the backoff resumes the schedule instead of restarting it.
		backoff := backoffFor(d.cfg.Backoff, attempt)
		d.advanceOutbox(record, attempt, d.cfg.Now().Add(backoff))
		if err := d.cfg.Sleep(d.ctx, backoff); err != nil {
			return
		}
	}
	// Unreachable in practice: a restored entry whose retry budget is already
	// spent is finalized by RestoreOutbox. Keep the guard so a shrunk attempt
	// budget can never leave an entry pending forever.
	if d.outboxLive(record) {
		d.finalizeOutbox(record)
	}
}

// RestoreOutbox re-queues the durable pending deliveries. It must run after
// AddEndpoint, because a restored entry can only be delivered through its
// receiver. Entries whose endpoint is gone, whose payload no longer matches its
// digest, or whose retry budget is already spent are removed from the outbox
// store and counted as skipped; a delivery is never queued twice because the
// key is the endpoint id plus the event id.
//
// The restored event ids are seeded into the dedupe window, so a producer that
// re-sends an event after the restart is rejected instead of fanning out a
// duplicate.
func (d *Dispatcher) RestoreOutbox() (restored, skipped int) {
	if d.cfg.Outbox == nil {
		return 0, 0
	}
	entries := d.cfg.Outbox.Outbox()
	if len(entries) == 0 {
		return 0, 0
	}

	// unusable rows are deleted from the store; exhausted ones are deleted and
	// reported as the dead letter the previous process did not get to write.
	var unusable, exhausted []OutboxEntry
	var starts []*endpointState
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return 0, 0
	}
	now := d.cfg.Now()
	d.purgeDedupeLocked(now)
	for _, entry := range entries {
		key := entry.Key()
		if entry.ID == "" || entry.EndpointID == "" {
			unusable = append(unusable, entry)
			skipped++
			continue
		}
		if entry.PayloadHash != "" && entry.PayloadHash != PayloadDigest(entry.Payload) {
			unusable = append(unusable, entry)
			skipped++
			continue
		}
		if _, duplicate := d.outbox[key]; duplicate {
			// Already queued: a second RestoreOutbox call must not disturb the
			// live entry or its persisted row.
			skipped++
			continue
		}
		ep, ok := d.endpoints[entry.EndpointID]
		if !ok {
			unusable = append(unusable, entry)
			skipped++
			continue
		}
		if entry.Attempts >= d.cfg.MaxAttempts {
			// The previous process died after the final attempt but before it
			// removed the entry. Finalizing here reports the dead letter the
			// crash interrupted and keeps a stale entry from blocking its
			// endpoint's FIFO for one more backoff.
			exhausted = append(exhausted, entry)
			skipped++
			continue
		}
		record := &outboxRecord{entry: entry}
		d.outbox[key] = record
		d.dedupe[entry.ID] = now
		d.pending++
		state := d.states[ep.ID]
		if state == nil {
			state = &endpointState{}
			d.states[ep.ID] = state
		}
		state.pending = append(state.pending, job{endpoint: ep, event: d.eventFor(entry), record: record})
		if !state.running {
			state.running = true
			d.wg.Add(1)
			starts = append(starts, state)
		}
		restored++
	}
	// A snapshot written under a larger limit must not exceed this process's
	// bound: the cap evicts the oldest restored entries, exactly as Dispatch
	// does for a live overflow.
	evicted := d.evictOutboxLocked(nil)
	d.mu.Unlock()

	for _, entry := range append(unusable, exhausted...) {
		if err := d.cfg.Outbox.RemoveOutbox(entry.EndpointID, entry.ID); err != nil {
			d.logger.Warn("webhook_outbox_remove_failed", "endpoint", entry.EndpointID, "event", entry.Event, "error", err)
		}
	}
	for _, entry := range exhausted {
		d.record(Delivery{
			ID:         fmt.Sprintf("%s:%d", entry.ID, entry.Attempts),
			EndpointID: entry.EndpointID,
			Event:      entry.Event,
			Attempt:    entry.Attempts,
			Status:     StatusDeadLetter,
			Error:      "retry budget exhausted before restart",
			At:         d.cfg.Now(),
		})
	}
	d.reportOutboxDrops(evicted)
	for _, state := range starts {
		go d.runEndpoint(state)
	}
	return restored, skipped
}

// eventFor rebuilds the dispatcher event a restored entry was accepted with.
// Only the fields a delivery reads are restored: the payload bytes are replayed
// from the entry, not re-encoded from Data.
func (d *Dispatcher) eventFor(entry OutboxEntry) Event {
	return Event{ID: entry.ID, EndpointID: entry.EndpointID, Type: entry.Event}
}

// OutboxLen reports the pending deliveries the outbox currently holds.
func (d *Dispatcher) OutboxLen() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.outbox)
}

// OutboxDropped reports how many entries the cap evicted. It is a lifetime
// counter, not a gauge.
func (d *Dispatcher) OutboxDropped() int64 { return d.outboxDropped.Load() }

// evictOutboxLocked drops the oldest entries until the pending set fits
// OutboxLimit. keep is the entry that was just accepted and is never the
// victim. The caller reports the victims outside the lock.
func (d *Dispatcher) evictOutboxLocked(keep *outboxRecord) []*outboxRecord {
	var evicted []*outboxRecord
	for len(d.outbox) > d.cfg.OutboxLimit {
		victim := d.oldestOutboxLocked(keep)
		if victim == nil {
			break
		}
		delete(d.outbox, victim.entry.Key())
		d.removeQueuedLocked(victim)
		evicted = append(evicted, victim)
	}
	return evicted
}

// oldestOutboxLocked returns the entry with the earliest creation time, which
// is the one the cap drops first. Ties break on the key so the choice is
// deterministic.
func (d *Dispatcher) oldestOutboxLocked(except *outboxRecord) *outboxRecord {
	var oldest *outboxRecord
	for _, candidate := range d.outbox {
		if candidate == except {
			continue
		}
		if oldest == nil ||
			candidate.entry.CreatedAt.Before(oldest.entry.CreatedAt) ||
			(candidate.entry.CreatedAt.Equal(oldest.entry.CreatedAt) && candidate.entry.Key() < oldest.entry.Key()) {
			oldest = candidate
		}
	}
	return oldest
}

// removeQueuedLocked removes a job that has not started yet. An in-flight job
// is not in the queue: it notices the eviction before its next attempt.
func (d *Dispatcher) removeQueuedLocked(record *outboxRecord) {
	for _, state := range d.states {
		for i := range state.pending {
			if state.pending[i].record != record {
				continue
			}
			state.pending = append(state.pending[:i], state.pending[i+1:]...)
			d.pending--
			return
		}
	}
}

// reportOutboxDrops removes the evicted entries from the outbox store, counts
// them and hands each to OnOutboxDrop. It runs outside d.mu because the hook
// usually writes an audit row.
func (d *Dispatcher) reportOutboxDrops(evicted []*outboxRecord) {
	for _, record := range evicted {
		entry := record.entry
		// A re-entrant Dispatch may have re-accepted the same key while this
		// drop was being reported. That row is newer than the evicted one, so
		// only a key that is still absent is deleted from the store.
		if d.cfg.Outbox != nil && !d.outboxHoldsKey(entry.Key()) {
			if err := d.cfg.Outbox.RemoveOutbox(entry.EndpointID, entry.ID); err != nil {
				d.logger.Warn("webhook_outbox_remove_failed", "endpoint", entry.EndpointID, "event", entry.Event, "error", err)
			}
		}
		d.outboxDropped.Add(1)
		d.logger.Warn("webhook_outbox_dropped",
			"endpoint", entry.EndpointID,
			"event", entry.Event,
			"attempts", entry.Attempts,
			"limit", d.cfg.OutboxLimit,
		)
		d.notifyOutboxDrop(entry)
	}
}

// outboxHoldsKey reports whether any live record currently uses key.
func (d *Dispatcher) outboxHoldsKey(key string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.outbox[key]
	return ok
}

// notifyOutboxDrop isolates the sink: a panic is logged and the dispatching
// goroutine continues, so one bad audit row cannot stop a producer.
func (d *Dispatcher) notifyOutboxDrop(entry OutboxEntry) {
	if d.cfg.OnOutboxDrop == nil {
		return
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			d.logger.Error("webhook_outbox_drop_hook_panic", "panic", recovered)
		}
	}()
	d.cfg.OnOutboxDrop(entry)
}

// persistOutbox mirrors one entry into the outbox store. A persistence fault
// is logged and delivery continues: durability degrades, availability does not.
func (d *Dispatcher) persistOutbox(entry OutboxEntry) {
	if d.cfg.Outbox == nil {
		return
	}
	if err := d.cfg.Outbox.PutOutbox(entry); err != nil {
		d.logger.Warn("webhook_outbox_persist_failed",
			"endpoint", entry.EndpointID, "event", entry.Event, "error", err)
	}
}

// persistLiveOutbox writes one record's current state only while the outbox
// still holds that record. The store write happens outside d.mu, so a
// concurrent Dispatch can evict the record and delete its row before the write
// lands; the write then verifies the record is gone and removes itself again.
// Without that check a stale row would be replayed after the next restart.
func (d *Dispatcher) persistLiveOutbox(record *outboxRecord, entry OutboxEntry) {
	d.persistOutbox(entry)
	if d.cfg.Outbox == nil || d.outboxLive(record) {
		return
	}
	if err := d.cfg.Outbox.RemoveOutbox(entry.EndpointID, entry.ID); err != nil {
		d.logger.Warn("webhook_outbox_remove_failed",
			"endpoint", entry.EndpointID, "event", entry.Event, "error", err)
	}
}

// outboxLive reports whether the outbox still holds this exact record. A
// record evicted by the cap is not live even when a new entry reuses its key.
func (d *Dispatcher) outboxLive(record *outboxRecord) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	current, ok := d.outbox[record.entry.Key()]
	return ok && current == record
}

// advanceOutbox records the failed attempt and the next due time before the
// backoff wait, so a restart resumes the schedule instead of restarting it.
func (d *Dispatcher) advanceOutbox(record *outboxRecord, attempts int, next time.Time) {
	key := record.entry.Key()
	d.mu.Lock()
	current, ok := d.outbox[key]
	if ok && current == record {
		record.entry.Attempts = attempts
		record.entry.NextAttemptAt = next
	}
	entry := record.entry
	d.mu.Unlock()
	if !ok || current != record {
		return
	}
	d.persistLiveOutbox(record, entry)
}

// completeOutbox removes a finished delivery from the outbox and its store.
// It is idempotent: an entry already evicted by the cap is not re-removed.
func (d *Dispatcher) completeOutbox(record *outboxRecord) {
	key := record.entry.Key()
	d.mu.Lock()
	current, ok := d.outbox[key]
	if ok && current == record {
		delete(d.outbox, key)
	}
	d.mu.Unlock()
	if !ok || current != record {
		return
	}
	if d.cfg.Outbox == nil {
		return
	}
	if err := d.cfg.Outbox.RemoveOutbox(record.entry.EndpointID, record.entry.ID); err != nil {
		d.logger.Warn("webhook_outbox_remove_failed",
			"endpoint", record.entry.EndpointID, "event", record.entry.Event, "error", err)
	}
}

// finalizeOutbox removes an entry whose retry budget is already spent and
// records the dead letter a crash interrupted.
func (d *Dispatcher) finalizeOutbox(record *outboxRecord) {
	d.completeOutbox(record)
	d.record(Delivery{
		ID:         fmt.Sprintf("%s:%d", record.entry.ID, record.entry.Attempts),
		EndpointID: record.entry.EndpointID,
		Event:      record.entry.Event,
		Attempt:    record.entry.Attempts,
		Status:     StatusDeadLetter,
		Error:      "retry budget exhausted",
		At:         d.cfg.Now(),
	})
}

// send performs one signed POST. It returns the HTTP status (0 when no
// response arrived), a non-empty error text on failure, and whether the
// failure is worth retrying. Client errors other than 429 are permanent.
func (d *Dispatcher) send(ep Endpoint, event Event, body []byte, attempt int) (int, string, bool) {
	ctx, cancel := context.WithTimeout(d.ctx, d.cfg.Timeout)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, ep.URL, bytes.NewReader(body))
	if err != nil {
		return 0, err.Error(), false
	}
	signature, timestamp := Signature(ep.Secret, d.cfg.Now(), body)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(HeaderSignature, signature)
	request.Header.Set(HeaderTimestamp, timestamp)
	request.Header.Set(HeaderEvent, string(event.Type))
	request.Header.Set(HeaderDelivery, fmt.Sprintf("%s:%d", event.ID, attempt))

	response, err := d.client.Do(request)
	if err != nil {
		if errors.Is(err, ErrInvalidEndpoint) {
			// The dial guard refused a private destination: retrying the same
			// URL cannot succeed, so record it as a permanent failure.
			return 0, err.Error(), false
		}
		return 0, err.Error(), true
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return response.StatusCode, "", false
	}
	retryable := response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500
	return response.StatusCode, fmt.Sprintf("http status %d", response.StatusCode), retryable
}

func backoffFor(schedule []time.Duration, attempt int) time.Duration {
	if len(schedule) == 0 {
		return 0
	}
	index := attempt - 1
	if index >= len(schedule) {
		index = len(schedule) - 1
	}
	if index < 0 {
		index = 0
	}
	return schedule[index]
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// record stores an attempt, hands it to the persistence hook, reports a dead
// letter to OnDeadLetter and logs it without any payload, secret or header
// content.
func (d *Dispatcher) record(delivery Delivery) {
	d.log.Append(delivery)
	d.publishHook(delivery)
	if delivery.Status == StatusDeadLetter {
		d.notifyDeadLetter(delivery)
		d.logger.Warn("webhook delivery dead-lettered",
			"endpoint", delivery.EndpointID,
			"event", delivery.Event,
			"delivery", delivery.ID,
			"attempt", delivery.Attempt,
			"httpStatus", delivery.HTTPStatus,
			"error", delivery.Error,
		)
		return
	}
	d.logger.Debug("webhook delivery attempt",
		"endpoint", delivery.EndpointID,
		"event", delivery.Event,
		"delivery", delivery.ID,
		"attempt", delivery.Attempt,
		"status", delivery.Status,
		"httpStatus", delivery.HTTPStatus,
		"error", delivery.Error,
	)
}

// publishHook queues one attempt for OnDelivery without ever blocking the
// caller. A full queue drops the attempt and counts it: a slow sink costs
// history, never a delivery.
func (d *Dispatcher) publishHook(delivery Delivery) {
	if d.hookCh == nil {
		return
	}
	select {
	case d.hookCh <- delivery:
	default:
		d.hookDropped.Add(1)
	}
}

// runDeliveryHook drains the OnDelivery queue on one goroutine. A panicking
// sink is logged and the pump continues, so one bad row cannot silently stop
// persistence for the process lifetime.
func (d *Dispatcher) runDeliveryHook() {
	defer d.hookWG.Done()
	for delivery := range d.hookCh {
		d.callDeliveryHook(delivery)
	}
}

func (d *Dispatcher) callDeliveryHook(delivery Delivery) {
	defer func() {
		if recovered := recover(); recovered != nil {
			d.logger.Error("webhook_delivery_hook_panic", "panic", recovered)
		}
	}()
	d.cfg.OnDelivery(delivery)
}

// notifyDeadLetter reports one retry-exhausted delivery to OnDeadLetter at
// most once per delivery id. The id set is a fixed-size ring, so a long-lived
// process pays constant memory; once the ring wraps an ancient id may be
// reported again.
func (d *Dispatcher) notifyDeadLetter(delivery Delivery) {
	if d.cfg.OnDeadLetter == nil {
		return
	}
	d.deadLetterMu.Lock()
	_, seen := d.deadLetterSeen[delivery.ID]
	if !seen {
		if evicted := d.deadLetterRing[d.deadLetterNext]; evicted != "" {
			delete(d.deadLetterSeen, evicted)
		}
		d.deadLetterRing[d.deadLetterNext] = delivery.ID
		d.deadLetterNext = (d.deadLetterNext + 1) % len(d.deadLetterRing)
		d.deadLetterSeen[delivery.ID] = struct{}{}
	}
	d.deadLetterMu.Unlock()
	if seen {
		return
	}
	d.callDeadLetterHook(delivery)
}

// callDeadLetterHook isolates the sink: a panic is logged and the delivery
// worker continues, so one bad audit row cannot stop an endpoint.
func (d *Dispatcher) callDeadLetterHook(delivery Delivery) {
	defer func() {
		if recovered := recover(); recovered != nil {
			d.logger.Error("webhook_dead_letter_hook_panic", "panic", recovered)
		}
	}()
	d.cfg.OnDeadLetter(delivery)
}

// HookDropped reports how many attempts were dropped because the OnDelivery
// queue was full. It is a lifetime counter, not a gauge.
func (d *Dispatcher) HookDropped() int64 { return d.hookDropped.Load() }
