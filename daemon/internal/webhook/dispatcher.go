package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"sync"
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

	Now        func() time.Time
	Sleep      func(ctx context.Context, d time.Duration) error
	Logger     *slog.Logger
	HTTPClient *http.Client
}

// Dispatcher delivers events to registered endpoints. It is safe for
// concurrent use.
type Dispatcher struct {
	cfg       Config
	logger    *slog.Logger
	client    *http.Client
	allowHTTP bool

	ctx    context.Context
	cancel context.CancelFunc
	sem    chan struct{}

	mu        sync.Mutex
	closed    bool
	endpoints map[string]Endpoint
	states    map[string]*endpointState
	pending   int
	dedupe    map[string]time.Time

	log *DeliveryLog
	wg  sync.WaitGroup
}

type endpointState struct {
	pending []job
	running bool
}

type job struct {
	endpoint Endpoint
	event    Event
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
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Sleep == nil {
		cfg.Sleep = sleepContext
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	if cfg.HTTPClient != nil {
		copied := *cfg.HTTPClient
		copied.CheckRedirect = client.CheckRedirect
		client = &copied
	}

	ctx, cancel := context.WithCancel(context.Background())
	return &Dispatcher{
		cfg:       cfg,
		logger:    cfg.Logger,
		client:    client,
		allowHTTP: cfg.AllowHTTP || os.Getenv("AJS_WEBHOOK_ALLOW_HTTP") == "1",
		ctx:       ctx,
		cancel:    cancel,
		sem:       make(chan struct{}, cfg.Workers),
		endpoints: make(map[string]Endpoint),
		states:    make(map[string]*endpointState),
		dedupe:    make(map[string]time.Time),
		log:       NewDeliveryLog(cfg.RingSize),
	}
}

// AddEndpoint registers a receiver. The URL must be HTTPS unless the receiver
// is loopback or HTTP is explicitly allowed, and the secret must be non-empty
// because every delivery is signed.
func (d *Dispatcher) AddEndpoint(ep Endpoint) error {
	if err := validateEndpoint(ep, d.allowHTTP); err != nil {
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

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return ErrClosed
	}
	ep, ok := d.endpoints[event.EndpointID]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownEndpoint, event.EndpointID)
	}
	if !ep.Enabled {
		return fmt.Errorf("%w: %q", ErrEndpointDisabled, event.EndpointID)
	}
	if !ep.subscribes(event.Type) {
		return fmt.Errorf("%w: %s", ErrNotSubscribed, event.Type)
	}

	now := d.cfg.Now()
	d.purgeDedupeLocked(now)
	if _, duplicate := d.dedupe[event.ID]; duplicate {
		return fmt.Errorf("%w: %s", ErrDuplicate, event.ID)
	}
	if d.pending >= d.cfg.QueueSize {
		return ErrQueueFull
	}

	d.dedupe[event.ID] = now
	d.pending++
	state := d.states[ep.ID]
	if state == nil {
		state = &endpointState{}
		d.states[ep.ID] = state
	}
	state.pending = append(state.pending, job{endpoint: ep, event: event})
	if !state.running {
		state.running = true
		d.wg.Add(1)
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
	body, err := json.Marshal(Payload{
		ID:    queued.event.ID,
		Event: queued.event.Type,
		At:    queued.event.At,
		Data:  Scrub(queued.event.Data),
	})
	if err != nil {
		d.record(Delivery{
			ID:         queued.event.ID + ":0",
			EndpointID: queued.endpoint.ID,
			Event:      queued.event.Type,
			Status:     StatusFailed,
			Error:      "encode payload: " + err.Error(),
			At:         d.cfg.Now(),
		})
		return
	}

	for attempt := 1; attempt <= d.cfg.MaxAttempts; attempt++ {
		started := d.cfg.Now()
		httpStatus, errText, retryable := d.send(queued.endpoint, queued.event, body, attempt)
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
			d.record(delivery)
			return
		}
		if !retryable || attempt == d.cfg.MaxAttempts {
			if retryable {
				delivery.Status = StatusDeadLetter
			}
			d.record(delivery)
			return
		}
		d.record(delivery)
		if err := d.cfg.Sleep(d.ctx, backoffFor(d.cfg.Backoff, attempt)); err != nil {
			return
		}
	}
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

// record stores an attempt and logs it without any payload, secret or header
// content.
func (d *Dispatcher) record(delivery Delivery) {
	d.log.Append(delivery)
	if delivery.Status == StatusDeadLetter {
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
