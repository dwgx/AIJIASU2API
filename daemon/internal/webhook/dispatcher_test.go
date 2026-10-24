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
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClock makes the retry schedule deterministic: Sleep advances the same
// clock Now reads, so a test never waits for a real backoff.
type fakeClock struct {
	mu     sync.Mutex
	t      time.Time
	sleeps []time.Duration
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Sleep(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sleeps = append(c.sleeps, delay)
	c.t = c.t.Add(delay)
	return nil
}

func (c *fakeClock) Advance(delay time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(delay)
}

func (c *fakeClock) Sleeps() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.sleeps...)
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newTestDispatcher(cfg Config, clock *fakeClock) *Dispatcher {
	if cfg.Now == nil {
		cfg.Now = clock.Now
	}
	if cfg.Sleep == nil {
		cfg.Sleep = clock.Sleep
	}
	if cfg.Logger == nil {
		cfg.Logger = discardLogger()
	}
	// Every dispatcher test receiver is an httptest loopback server.
	cfg.AllowPrivate = true
	return New(cfg)
}

func addEndpoint(t *testing.T, d *Dispatcher, endpoint Endpoint) {
	t.Helper()
	if err := d.AddEndpoint(endpoint); err != nil {
		t.Fatalf("AddEndpoint(%q): %v", endpoint.ID, err)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
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

func waitDeliveries(t *testing.T, d *Dispatcher, count int) []Delivery {
	t.Helper()
	waitFor(t, fmt.Sprintf("%d delivery records", count), func() bool {
		return len(d.Log().Recent()) >= count
	})
	return d.Log().Recent()
}

func TestDeliverySignaturePayloadAndScrub(t *testing.T) {
	const secret = "test-signing-secret"
	var mu sync.Mutex
	var body []byte
	var headers http.Header
	received := make(chan struct{}, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		mu.Lock()
		body = raw
		headers = r.Header.Clone()
		mu.Unlock()
		received <- struct{}{}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	clock := newFakeClock()
	dispatcher := newTestDispatcher(Config{}, clock)
	defer dispatcher.Close()
	addEndpoint(t, dispatcher, Endpoint{ID: "ep-1", URL: server.URL, Secret: secret, Enabled: true})

	data := map[string]any{
		"account": "acct-1",
		"token":   "tok-should-not-leak",
		"nested":  map[string]any{"password": "pw-should-not-leak", "label": "keep"},
	}
	if err := dispatcher.Dispatch(Event{ID: "evt-1", EndpointID: "ep-1", Type: EventAccountLogin, Data: data}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	select {
	case <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("receiver was not called")
	}
	deliveries := waitDeliveries(t, dispatcher, 1)
	if deliveries[0].Status != StatusSuccess {
		t.Fatalf("status = %s, error = %s", deliveries[0].Status, deliveries[0].Error)
	}

	mu.Lock()
	capturedBody, capturedHeaders := body, headers
	mu.Unlock()

	if !VerifySignature(secret, capturedHeaders.Get(HeaderTimestamp), capturedBody, capturedHeaders.Get(HeaderSignature)) {
		t.Fatalf("invalid signature %q", capturedHeaders.Get(HeaderSignature))
	}
	if got := capturedHeaders.Get(HeaderEvent); got != string(EventAccountLogin) {
		t.Fatalf("event header = %q", got)
	}
	if got := capturedHeaders.Get(HeaderDelivery); got != "evt-1:1" {
		t.Fatalf("delivery header = %q", got)
	}
	if capturedHeaders.Get(HeaderTimestamp) == "" {
		t.Fatal("timestamp header is empty")
	}

	var payload Payload
	if err := json.Unmarshal(capturedBody, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.ID != "evt-1" || payload.Event != EventAccountLogin || payload.At.IsZero() {
		t.Fatalf("payload = %#v", payload)
	}
	if payload.Data["account"] != "acct-1" {
		t.Fatalf("benign field lost: %#v", payload.Data)
	}
	if payload.Data["token"] != Redacted {
		t.Fatalf("token not scrubbed on the wire: %#v", payload.Data["token"])
	}
	nested := payload.Data["nested"].(map[string]any)
	if nested["password"] != Redacted || nested["label"] != "keep" {
		t.Fatalf("nested data = %#v", nested)
	}
	if data["token"] != "tok-should-not-leak" {
		t.Fatal("Dispatch mutated caller data")
	}
}

func TestRetryThenSuccess(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	clock := newFakeClock()
	dispatcher := newTestDispatcher(Config{}, clock)
	defer dispatcher.Close()
	addEndpoint(t, dispatcher, Endpoint{ID: "ep", URL: server.URL, Secret: "s", Enabled: true})

	if err := dispatcher.Dispatch(Event{ID: "evt", EndpointID: "ep", Type: EventNodeConnected}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	deliveries := waitDeliveries(t, dispatcher, 2)
	if got := calls.Load(); got != 2 {
		t.Fatalf("receiver calls = %d, want 2", got)
	}
	if deliveries[0].Attempt != 1 || deliveries[0].Status != StatusFailed || deliveries[0].HTTPStatus != 500 {
		t.Fatalf("first attempt = %#v", deliveries[0])
	}
	if deliveries[1].Attempt != 2 || deliveries[1].Status != StatusSuccess || deliveries[1].HTTPStatus != 200 {
		t.Fatalf("second attempt = %#v", deliveries[1])
	}
	if sleeps := clock.Sleeps(); len(sleeps) != 1 || sleeps[0] != time.Second {
		t.Fatalf("sleeps = %v, want [1s]", sleeps)
	}
	if dispatcher.Log().Retries() != 1 {
		t.Fatalf("retries = %d, want 1", dispatcher.Log().Retries())
	}
}

func TestPermanentClientErrorIsNotRetried(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	clock := newFakeClock()
	dispatcher := newTestDispatcher(Config{}, clock)
	defer dispatcher.Close()
	addEndpoint(t, dispatcher, Endpoint{ID: "ep", URL: server.URL, Secret: "s", Enabled: true})

	if err := dispatcher.Dispatch(Event{ID: "evt", EndpointID: "ep", Type: EventDeviceRetired}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	deliveries := waitDeliveries(t, dispatcher, 1)
	if deliveries[0].Status != StatusFailed || deliveries[0].HTTPStatus != 400 {
		t.Fatalf("delivery = %#v", deliveries[0])
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("receiver calls = %d, want 1", got)
	}
	if sleeps := clock.Sleeps(); len(sleeps) != 0 {
		t.Fatalf("sleeps = %v, want none", sleeps)
	}
}

func TestRedirectIsNotFollowed(t *testing.T) {
	var targetCalls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()

	clock := newFakeClock()
	dispatcher := newTestDispatcher(Config{}, clock)
	defer dispatcher.Close()
	addEndpoint(t, dispatcher, Endpoint{ID: "ep", URL: redirector.URL, Secret: "s", Enabled: true})

	if err := dispatcher.Dispatch(Event{ID: "evt", EndpointID: "ep", Type: EventScheduleFailed}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	deliveries := waitDeliveries(t, dispatcher, 1)
	if deliveries[0].HTTPStatus != http.StatusFound || deliveries[0].Status != StatusFailed {
		t.Fatalf("delivery = %#v", deliveries[0])
	}
	if got := targetCalls.Load(); got != 0 {
		t.Fatalf("redirect target called %d times, want 0", got)
	}
}

func TestDeadLetterAfterLastAttempt(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	clock := newFakeClock()
	dispatcher := newTestDispatcher(Config{}, clock)
	defer dispatcher.Close()
	addEndpoint(t, dispatcher, Endpoint{ID: "ep", URL: server.URL, Secret: "s", Enabled: true})

	if err := dispatcher.Dispatch(Event{ID: "evt", EndpointID: "ep", Type: EventAuditCritical}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	deliveries := waitDeliveries(t, dispatcher, DefaultMaxAttempts)
	if got := calls.Load(); got != int64(DefaultMaxAttempts) {
		t.Fatalf("receiver calls = %d, want %d", got, DefaultMaxAttempts)
	}
	for i, delivery := range deliveries {
		if delivery.Attempt != i+1 {
			t.Fatalf("delivery %d attempt = %d", i, delivery.Attempt)
		}
	}
	last := deliveries[len(deliveries)-1]
	if last.Status != StatusDeadLetter {
		t.Fatalf("last status = %s, want dead_letter", last.Status)
	}
	if last.Error == "" {
		t.Fatal("dead letter carries no error")
	}
	want := []time.Duration{time.Second, 5 * time.Second, 30 * time.Second, 2 * time.Minute}
	if sleeps := clock.Sleeps(); len(sleeps) != len(want) {
		t.Fatalf("sleeps = %v, want %v", sleeps, want)
	} else {
		for i := range want {
			if sleeps[i] != want[i] {
				t.Fatalf("sleeps = %v, want %v", sleeps, want)
			}
		}
	}
}

func TestTimeoutIsRetryableAndDeadLetters(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Outlive the client timeout. The handler returns on its own so
		// httptest.Server.Close does not wait forever.
		time.Sleep(150 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	clock := newFakeClock()
	dispatcher := newTestDispatcher(Config{Timeout: 40 * time.Millisecond, MaxAttempts: 1}, clock)
	defer dispatcher.Close()
	addEndpoint(t, dispatcher, Endpoint{ID: "ep", URL: server.URL, Secret: "s", Enabled: true})

	if err := dispatcher.Dispatch(Event{ID: "evt", EndpointID: "ep", Type: EventNodeSwitched}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	deliveries := waitDeliveries(t, dispatcher, 1)
	delivery := deliveries[0]
	if delivery.Status != StatusDeadLetter {
		t.Fatalf("status = %s, want dead_letter (error %s)", delivery.Status, delivery.Error)
	}
	if !strings.Contains(delivery.Error, "deadline exceeded") {
		t.Fatalf("error = %q, want a deadline error", delivery.Error)
	}
}

func TestDedupeWindow(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	clock := newFakeClock()
	dispatcher := newTestDispatcher(Config{}, clock)
	defer dispatcher.Close()
	addEndpoint(t, dispatcher, Endpoint{ID: "ep", URL: server.URL, Secret: "s", Enabled: true})

	event := Event{ID: "evt-dup", EndpointID: "ep", Type: EventAccountRenewalDue}
	if err := dispatcher.Dispatch(event); err != nil {
		t.Fatalf("first Dispatch: %v", err)
	}
	waitDeliveries(t, dispatcher, 1)
	if err := dispatcher.Dispatch(event); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("second Dispatch error = %v, want ErrDuplicate", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("receiver calls = %d, want 1", got)
	}

	clock.Advance(DefaultDedupeWindow + time.Second)
	if err := dispatcher.Dispatch(event); err != nil {
		t.Fatalf("Dispatch after window: %v", err)
	}
	waitDeliveries(t, dispatcher, 2)
	if got := calls.Load(); got != 2 {
		t.Fatalf("receiver calls = %d, want 2", got)
	}
}

func TestQueueSaturationRejectsWithoutBlocking(t *testing.T) {
	release := make(chan struct{})
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			<-release
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	clock := newFakeClock()
	dispatcher := newTestDispatcher(Config{QueueSize: 2, Workers: 1}, clock)
	defer dispatcher.Close()
	addEndpoint(t, dispatcher, Endpoint{ID: "ep", URL: server.URL, Secret: "s", Enabled: true})

	for i := 0; i < 2; i++ {
		event := Event{ID: fmt.Sprintf("evt-%d", i), EndpointID: "ep", Type: EventScheduleFired}
		if err := dispatcher.Dispatch(event); err != nil {
			t.Fatalf("Dispatch %d: %v", i, err)
		}
	}
	if err := dispatcher.Dispatch(Event{ID: "evt-full", EndpointID: "ep", Type: EventScheduleFired}); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("Dispatch on full queue error = %v, want ErrQueueFull", err)
	}
	close(release)
	waitFor(t, "queue to drain", func() bool { return dispatcher.Pending() == 0 })
	if got := len(dispatcher.Log().Recent()); got != 2 {
		t.Fatalf("deliveries = %d, want 2", got)
	}
}

func TestPerEndpointSerializationAndFIFO(t *testing.T) {
	var mu sync.Mutex
	var order []string
	var inflight, maxInflight atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := inflight.Add(1)
		for {
			previous := maxInflight.Load()
			if current <= previous || maxInflight.CompareAndSwap(previous, current) {
				break
			}
		}
		mu.Lock()
		order = append(order, r.Header.Get(HeaderDelivery))
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		inflight.Add(-1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	clock := newFakeClock()
	dispatcher := newTestDispatcher(Config{Workers: 4}, clock)
	defer dispatcher.Close()
	addEndpoint(t, dispatcher, Endpoint{ID: "ep", URL: server.URL, Secret: "s", Enabled: true})

	for i := 0; i < 2; i++ {
		event := Event{ID: fmt.Sprintf("evt-%d", i), EndpointID: "ep", Type: EventNodeDisconnected}
		if err := dispatcher.Dispatch(event); err != nil {
			t.Fatalf("Dispatch %d: %v", i, err)
		}
	}
	waitDeliveries(t, dispatcher, 2)
	mu.Lock()
	gotOrder := append([]string(nil), order...)
	mu.Unlock()
	if len(gotOrder) != 2 || gotOrder[0] != "evt-0:1" || gotOrder[1] != "evt-1:1" {
		t.Fatalf("delivery order = %v, want FIFO", gotOrder)
	}
	if got := maxInflight.Load(); got != 1 {
		t.Fatalf("max in-flight for one endpoint = %d, want 1", got)
	}
}

func TestNoSecretInLogs(t *testing.T) {
	const secret = "super-secret-log-canary"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	var logs lockedBuffer
	clock := newFakeClock()
	dispatcher := newTestDispatcher(Config{
		MaxAttempts: 2,
		Logger:      slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}, clock)
	defer dispatcher.Close()
	addEndpoint(t, dispatcher, Endpoint{ID: "ep", URL: server.URL, Secret: secret, Enabled: true})

	if err := dispatcher.Dispatch(Event{ID: "evt", EndpointID: "ep", Type: EventAuditCritical}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	waitDeliveries(t, dispatcher, 2)
	waitFor(t, "dead-letter log line", func() bool {
		return strings.Contains(logs.String(), "dead-lettered")
	})
	if strings.Contains(logs.String(), secret) {
		t.Fatalf("secret leaked into logs: %s", logs.String())
	}
}

func TestDispatcherEndpointManagementAndValidation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	dispatcher := newTestDispatcher(Config{}, newFakeClock())
	defer dispatcher.Close()

	loopback := Endpoint{ID: "loop", URL: server.URL, Secret: "s", Enabled: true}
	addEndpoint(t, dispatcher, loopback)
	if err := dispatcher.AddEndpoint(loopback); !errors.Is(err, ErrEndpointExists) {
		t.Fatalf("duplicate AddEndpoint error = %v, want ErrEndpointExists", err)
	}
	if err := dispatcher.AddEndpoint(Endpoint{ID: "remote", URL: "http://example.com/hook", Secret: "s", Enabled: true}); !errors.Is(err, ErrInvalidEndpoint) {
		t.Fatalf("http remote endpoint error = %v, want ErrInvalidEndpoint", err)
	}

	// The environment opt-in is read at construction time.
	t.Setenv("AJS_WEBHOOK_ALLOW_HTTP", "1")
	optedIn := newTestDispatcher(Config{}, newFakeClock())
	defer optedIn.Close()
	addEndpoint(t, optedIn, Endpoint{ID: "remote", URL: "http://example.com/hook", Secret: "s", Enabled: true})

	if _, ok := dispatcher.Endpoint("loop"); !ok {
		t.Fatal("Endpoint(loop) not found")
	}
	if !dispatcher.RemoveEndpoint("loop") {
		t.Fatal("RemoveEndpoint returned false")
	}
	if dispatcher.RemoveEndpoint("loop") {
		t.Fatal("RemoveEndpoint removed an absent endpoint")
	}
	if err := dispatcher.SetEndpointEnabled("missing", true); !errors.Is(err, ErrEndpointMissing) {
		t.Fatalf("SetEndpointEnabled error = %v, want ErrEndpointMissing", err)
	}
}

func TestDispatchValidation(t *testing.T) {
	dispatcher := newTestDispatcher(Config{}, newFakeClock())
	defer dispatcher.Close()
	addEndpoint(t, dispatcher, Endpoint{
		ID:      "ep",
		URL:     "https://example.com/hook",
		Secret:  "s",
		Events:  []EventType{EventAccountLogin},
		Enabled: true,
	})

	cases := []struct {
		name  string
		event Event
		want  error
	}{
		{"missing id", Event{EndpointID: "ep", Type: EventAccountLogin}, ErrInvalidEvent},
		{"unknown type", Event{ID: "e", EndpointID: "ep", Type: "nope"}, ErrInvalidEvent},
		{"unknown endpoint", Event{ID: "e", EndpointID: "missing", Type: EventAccountLogin}, ErrUnknownEndpoint},
		{"not subscribed", Event{ID: "e", EndpointID: "ep", Type: EventNodeConnected}, ErrNotSubscribed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := dispatcher.Dispatch(tc.event); !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}

	if err := dispatcher.SetEndpointEnabled("ep", false); err != nil {
		t.Fatalf("SetEndpointEnabled: %v", err)
	}
	if err := dispatcher.Dispatch(Event{ID: "e", EndpointID: "ep", Type: EventAccountLogin}); !errors.Is(err, ErrEndpointDisabled) {
		t.Fatalf("disabled endpoint error = %v, want ErrEndpointDisabled", err)
	}
	if endpoints := dispatcher.Endpoints(); len(endpoints) != 1 || endpoints[0].ID != "ep" {
		t.Fatalf("Endpoints() = %#v", endpoints)
	}
}

func TestCloseCancelsAndRejectsFurtherDispatches(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Keep the request in flight long enough for Close to cancel it, then
		// return so httptest.Server.Close cannot block.
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	clock := newFakeClock()
	dispatcher := newTestDispatcher(Config{MaxAttempts: 5}, clock)
	addEndpoint(t, dispatcher, Endpoint{ID: "ep", URL: server.URL, Secret: "s", Enabled: true})

	if err := dispatcher.Dispatch(Event{ID: "evt", EndpointID: "ep", Type: EventNodeConnected}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if err := dispatcher.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := dispatcher.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if got := dispatcher.Pending(); got != 0 {
		t.Fatalf("pending after Close = %d, want 0", got)
	}
	if err := dispatcher.Dispatch(Event{ID: "evt-2", EndpointID: "ep", Type: EventNodeConnected}); !errors.Is(err, ErrClosed) {
		t.Fatalf("Dispatch after Close error = %v, want ErrClosed", err)
	}
	for _, delivery := range dispatcher.Log().Recent() {
		if delivery.Status == StatusSuccess {
			t.Fatalf("unexpected success after Close: %#v", delivery)
		}
	}
}

func TestConcurrentDispatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	clock := newFakeClock()
	dispatcher := newTestDispatcher(Config{}, clock)
	defer dispatcher.Close()
	addEndpoint(t, dispatcher, Endpoint{ID: "ep", URL: server.URL, Secret: "s", Enabled: true})

	const total = 64
	var accepted atomic.Int64
	var wg sync.WaitGroup
	for i := range total {
		wg.Add(1)
		go func() {
			defer wg.Done()
			event := Event{ID: fmt.Sprintf("evt-%d", i), EndpointID: "ep", Type: EventScheduleFired}
			if err := dispatcher.Dispatch(event); err == nil {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := accepted.Load(); got != total {
		t.Fatalf("accepted = %d, want %d", got, total)
	}
	waitFor(t, "all deliveries", func() bool {
		recent := dispatcher.Log().Recent()
		if len(recent) != total {
			return false
		}
		for _, delivery := range recent {
			if delivery.Status != StatusSuccess {
				return false
			}
		}
		return true
	})
	if got := dispatcher.Pending(); got != 0 {
		t.Fatalf("pending = %d, want 0", got)
	}
}
