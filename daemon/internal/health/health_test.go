package health

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// listenLoopback starts a throwaway TCP listener and returns its port. The
// sampler only ever dials loopback, so every network test stays in-process.
func listenLoopback(t *testing.T) (net.Listener, int) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return listener, listener.Addr().(*net.TCPAddr).Port
}

// waitFor polls cond until it is true or timeout elapses.
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !cond() {
		t.Fatalf("timed out waiting for %s", what)
	}
}

func closeTo(got, want float64) bool {
	return math.Abs(got-want) < 1e-9
}

func TestDefaults(t *testing.T) {
	s := New(Config{})
	if s.cfg.Interval != DefaultInterval {
		t.Fatalf("Interval = %s, want %s", s.cfg.Interval, DefaultInterval)
	}
	if s.cfg.Timeout != DefaultTimeout {
		t.Fatalf("Timeout = %s, want %s", s.cfg.Timeout, DefaultTimeout)
	}
	if s.cfg.Capacity != DefaultCapacity || s.cfg.MaxNodes != DefaultMaxNodes || s.cfg.MaxConcurrent != DefaultMaxConcurrent {
		t.Fatalf("defaults = capacity %d, max nodes %d, max concurrent %d", s.cfg.Capacity, s.cfg.MaxNodes, s.cfg.MaxConcurrent)
	}
	if s.cfg.Now == nil || s.cfg.Dial == nil {
		t.Fatal("default clock or dialer is nil")
	}
	if got := cap(s.sem); got != DefaultMaxConcurrent {
		t.Fatalf("semaphore capacity = %d, want %d", got, DefaultMaxConcurrent)
	}
}

func TestWatchProbesLoopbackListener(t *testing.T) {
	_, port := listenLoopback(t)
	s := New(Config{Interval: 10 * time.Millisecond, Timeout: time.Second})
	defer s.Stop()

	if err := s.Watch("acct:1", port); err != nil {
		t.Fatalf("Watch: %v", err)
	}
	s.Start(context.Background())
	waitFor(t, 2*time.Second, "two successful samples", func() bool {
		stats, ok := s.Stats("acct:1")
		return ok && stats.Count >= 2
	})

	stats, ok := s.Stats("acct:1")
	if !ok {
		t.Fatal("Stats returned no entry for a watched key")
	}
	if stats.State != StateOK {
		t.Fatalf("State = %q, want %q", stats.State, StateOK)
	}
	if stats.OK != stats.Count {
		t.Fatalf("OK = %d of %d, want all", stats.OK, stats.Count)
	}
	if !stats.Last.OK {
		t.Fatalf("Last.OK = false, error class %q", stats.Last.ErrorClass)
	}
	if stats.Last.ErrorClass != "" {
		t.Fatalf("Last.ErrorClass = %q, want empty", stats.Last.ErrorClass)
	}
	if stats.Last.LatencyMs < 0 {
		t.Fatalf("Last.LatencyMs = %f, want >= 0", stats.Last.LatencyMs)
	}
	if stats.Last.At.IsZero() {
		t.Fatal("Last.At is zero")
	}
}

func TestProbeRefusedClass(t *testing.T) {
	listener, port := listenLoopback(t)
	_ = listener.Close()

	s := New(Config{Interval: 10 * time.Millisecond, Timeout: time.Second})
	defer s.Stop()
	if err := s.Watch("acct:1", port); err != nil {
		t.Fatalf("Watch: %v", err)
	}
	s.Start(context.Background())
	waitFor(t, 2*time.Second, "a refused sample", func() bool {
		stats, ok := s.Stats("acct:1")
		return ok && stats.Count >= 1
	})

	stats, _ := s.Stats("acct:1")
	if stats.Last.OK {
		t.Fatal("probe to a closed port reported OK")
	}
	if stats.Last.ErrorClass != ErrorRefused {
		t.Fatalf("ErrorClass = %q, want %q", stats.Last.ErrorClass, ErrorRefused)
	}
	if stats.State != StateDown {
		t.Fatalf("State = %q, want %q for a 0%% window", stats.State, StateDown)
	}
}

func TestProbeTimeoutClass(t *testing.T) {
	s := New(Config{
		Interval: time.Hour,
		Timeout:  30 * time.Millisecond,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	})
	defer s.Stop()
	if err := s.Watch("acct:1", 65000); err != nil {
		t.Fatalf("Watch: %v", err)
	}
	s.Start(context.Background())
	waitFor(t, 2*time.Second, "a timed-out sample", func() bool {
		stats, ok := s.Stats("acct:1")
		return ok && stats.Count >= 1
	})

	stats, _ := s.Stats("acct:1")
	if stats.Last.OK {
		t.Fatal("timed-out probe reported OK")
	}
	if stats.Last.ErrorClass != ErrorTimeout {
		t.Fatalf("ErrorClass = %q, want %q", stats.Last.ErrorClass, ErrorTimeout)
	}
	if stats.State != StateDown {
		t.Fatalf("State = %q, want %q", stats.State, StateDown)
	}
}

func TestProbeLatencyUsesClock(t *testing.T) {
	_, port := listenLoopback(t)
	base := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	clock := &scriptedClock{times: []time.Time{base, base.Add(5 * time.Millisecond)}}
	s := New(Config{Interval: time.Hour, Timeout: time.Second, Now: clock.Now})
	defer s.Stop()
	if err := s.Watch("acct:1", port); err != nil {
		t.Fatalf("Watch: %v", err)
	}
	s.Start(context.Background())
	waitFor(t, 2*time.Second, "one sample", func() bool {
		stats, ok := s.Stats("acct:1")
		return ok && stats.Count == 1
	})

	stats, _ := s.Stats("acct:1")
	if !stats.Last.OK {
		t.Fatalf("probe failed: %q", stats.Last.ErrorClass)
	}
	if !closeTo(stats.Last.LatencyMs, 5) {
		t.Fatalf("LatencyMs = %f, want 5", stats.Last.LatencyMs)
	}
	if !stats.Last.At.Equal(base) {
		t.Fatalf("At = %s, want %s", stats.Last.At, base)
	}
}

type scriptedClock struct {
	mu    sync.Mutex
	times []time.Time
	last  time.Time
}

func (c *scriptedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.times) == 0 {
		return c.last
	}
	now := c.times[0]
	c.times = c.times[1:]
	c.last = now
	return now
}

func TestRingCapacityAndWrap(t *testing.T) {
	ring := newRing(3)
	if got := ring.samples(); len(got) != 0 {
		t.Fatalf("empty ring returned %d samples", len(got))
	}
	for i := range 5 {
		ring.append(Sample{LatencyMs: float64(i)})
	}
	got := ring.samples()
	if len(got) != 3 {
		t.Fatalf("ring size = %d, want 3", len(got))
	}
	for i, want := range []float64{2, 3, 4} {
		if got[i].LatencyMs != want {
			t.Fatalf("sample %d = %f, want %f", i, got[i].LatencyMs, want)
		}
	}
	ring.append(Sample{LatencyMs: 9})
	got = ring.samples()
	for i, want := range []float64{3, 4, 9} {
		if got[i].LatencyMs != want {
			t.Fatalf("wrapped sample %d = %f, want %f", i, got[i].LatencyMs, want)
		}
	}
}

func TestLRUEviction(t *testing.T) {
	s := New(Config{MaxNodes: 2, Interval: time.Hour})
	for i, key := range []string{"a", "b"} {
		if err := s.Watch(key, 1000+i); err != nil {
			t.Fatalf("Watch(%q): %v", key, err)
		}
	}
	if _, ok := s.Stats("a"); !ok {
		t.Fatal("Stats(a) missing before eviction")
	}
	if err := s.Watch("c", 1002); err != nil {
		t.Fatalf("Watch(c): %v", err)
	}
	if _, ok := s.Stats("a"); ok {
		t.Fatal("least recently used node was not evicted")
	}
	for _, key := range []string{"b", "c"} {
		if _, ok := s.Stats(key); !ok {
			t.Fatalf("Stats(%q) missing after eviction", key)
		}
	}
	if _, ok := s.Stats("missing"); ok {
		t.Fatal("Stats reported an unknown key")
	}
}

func TestNodeMapBounded(t *testing.T) {
	s := New(Config{Interval: time.Hour})
	for i := range DefaultMaxNodes + 6 {
		if err := s.Watch(fmt.Sprintf("node:%d", i), 9000+i); err != nil {
			t.Fatalf("Watch(node:%d): %v", i, err)
		}
	}
	s.mu.Lock()
	nodes := len(s.nodes)
	s.mu.Unlock()
	if nodes != DefaultMaxNodes {
		t.Fatalf("retained nodes = %d, want %d", nodes, DefaultMaxNodes)
	}
	if _, ok := s.Stats("node:0"); ok {
		t.Fatal("oldest node was not evicted at the map bound")
	}
	if _, ok := s.Stats(fmt.Sprintf("node:%d", DefaultMaxNodes+5)); !ok {
		t.Fatal("newest node missing after eviction")
	}
}

func TestLRUEvictsUnwatchedFirst(t *testing.T) {
	_, port := listenLoopback(t)
	s := New(Config{MaxNodes: 2, Interval: time.Hour, Timeout: time.Second})
	defer s.Stop()
	s.Start(context.Background())
	if err := s.Watch("watched", port); err != nil {
		t.Fatalf("Watch(watched): %v", err)
	}
	if err := s.Watch("stale", port); err != nil {
		t.Fatalf("Watch(stale): %v", err)
	}
	s.Unwatch("stale")
	if err := s.Watch("new", port); err != nil {
		t.Fatalf("Watch(new): %v", err)
	}
	if _, ok := s.Stats("stale"); ok {
		t.Fatal("unwatched node was not evicted first")
	}
	if _, ok := s.Stats("watched"); !ok {
		t.Fatal("watched node was evicted before the unwatched one")
	}
}

func TestAggregateMath(t *testing.T) {
	base := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	samples := make([]Sample, 0, 10)
	for i := 1; i <= 8; i++ {
		samples = append(samples, Sample{
			At:        base.Add(time.Duration(i) * time.Second),
			LatencyMs: float64(i * 10),
			OK:        true,
		})
	}
	samples = append(samples,
		Sample{At: base.Add(9 * time.Second), LatencyMs: 500, ErrorClass: ErrorTimeout},
		Sample{At: base.Add(10 * time.Second), LatencyMs: 700, ErrorClass: ErrorRefused},
	)

	stats := aggregate("acct:1", samples)
	if stats.Key != "acct:1" || stats.Count != 10 || stats.OK != 8 {
		t.Fatalf("counts = key %q, count %d, ok %d", stats.Key, stats.Count, stats.OK)
	}
	if !closeTo(stats.SuccessRate, 0.8) {
		t.Fatalf("SuccessRate = %f, want 0.8", stats.SuccessRate)
	}
	if !closeTo(stats.MinMs, 10) || !closeTo(stats.MaxMs, 80) {
		t.Fatalf("min/max = %f/%f, want 10/80", stats.MinMs, stats.MaxMs)
	}
	if !closeTo(stats.P50Ms, 40) {
		t.Fatalf("P50Ms = %f, want 40", stats.P50Ms)
	}
	if !closeTo(stats.P95Ms, 80) {
		t.Fatalf("P95Ms = %f, want 80", stats.P95Ms)
	}
	if stats.ConsecutiveFailures != 2 {
		t.Fatalf("ConsecutiveFailures = %d, want 2", stats.ConsecutiveFailures)
	}
	if stats.Last.ErrorClass != ErrorRefused || stats.Last.At != base.Add(10*time.Second) {
		t.Fatalf("Last = %+v, want the final refused sample", stats.Last)
	}
	if stats.State != StateDegraded {
		t.Fatalf("State = %q, want %q", stats.State, StateDegraded)
	}
}

func TestAggregateAllFailures(t *testing.T) {
	stats := aggregate("k", []Sample{{LatencyMs: 5}, {LatencyMs: 6}})
	if stats.OK != 0 || stats.P50Ms != 0 || stats.P95Ms != 0 || stats.MinMs != 0 || stats.MaxMs != 0 {
		t.Fatalf("failure-only window produced latency stats: %+v", stats)
	}
	if stats.ConsecutiveFailures != 2 || stats.State != StateDown {
		t.Fatalf("ConsecutiveFailures = %d, State = %q", stats.ConsecutiveFailures, stats.State)
	}
}

func TestStateThresholds(t *testing.T) {
	cases := []struct {
		name  string
		stats Stats
		want  string
	}{
		{"empty window", Stats{}, StateOK},
		{"healthy", Stats{Count: 10, OK: 10, SuccessRate: 1, P95Ms: 100}, StateOK},
		{"p95 at threshold", Stats{Count: 10, OK: 10, SuccessRate: 1, P95Ms: 800}, StateOK},
		{"p95 over threshold", Stats{Count: 10, OK: 10, SuccessRate: 1, P95Ms: 801}, StateDegraded},
		{"success at 90%", Stats{Count: 10, OK: 9, SuccessRate: 0.9, P95Ms: 100}, StateOK},
		{"success under 90%", Stats{Count: 10, OK: 8, SuccessRate: 0.8, P95Ms: 100}, StateDegraded},
		{"success at 50%", Stats{Count: 10, OK: 5, SuccessRate: 0.5, P95Ms: 100}, StateDegraded},
		{"success under 50%", Stats{Count: 10, OK: 4, SuccessRate: 0.4, P95Ms: 100}, StateDown},
		{"two consecutive failures", Stats{Count: 10, OK: 10, SuccessRate: 1, P95Ms: 100, ConsecutiveFailures: 2}, StateOK},
		{"three consecutive failures", Stats{Count: 10, OK: 7, SuccessRate: 0.7, P95Ms: 100, ConsecutiveFailures: 3}, StateDown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := healthState(tc.stats); got != tc.want {
				t.Fatalf("healthState(%+v) = %q, want %q", tc.stats, got, tc.want)
			}
		})
	}
}

func TestWatchValidationAndIdempotency(t *testing.T) {
	_, port := listenLoopback(t)
	s := New(Config{Interval: time.Hour, Timeout: time.Second})

	if err := s.Watch("", port); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("Watch(empty key) = %v, want ErrInvalidKey", err)
	}
	if err := s.Watch("k", 0); !errors.Is(err, ErrInvalidPort) {
		t.Fatalf("Watch(port 0) = %v, want ErrInvalidPort", err)
	}
	if err := s.Watch("k", 65536); !errors.Is(err, ErrInvalidPort) {
		t.Fatalf("Watch(port 65536) = %v, want ErrInvalidPort", err)
	}
	if err := s.Watch("k", port); err != nil {
		t.Fatalf("Watch: %v", err)
	}
	s.mu.Lock()
	if s.nodes["k"].done != nil {
		s.mu.Unlock()
		t.Fatal("Watch launched a probe goroutine before Start")
	}
	s.mu.Unlock()

	s.Start(context.Background())
	defer s.Stop()
	s.mu.Lock()
	first := s.nodes["k"].done
	s.mu.Unlock()
	if first == nil {
		t.Fatal("Start did not launch the probe goroutine")
	}

	s.Start(context.Background()) // idempotent
	if err := s.Watch("k", port+1); err != nil {
		t.Fatalf("second Watch: %v", err)
	}
	s.mu.Lock()
	same := s.nodes["k"].done == first
	keptPort := s.nodes["k"].port
	s.mu.Unlock()
	if !same {
		t.Fatal("a repeated Start or Watch restarted the probe goroutine")
	}
	if keptPort != port {
		t.Fatalf("watched port = %d, want original %d", keptPort, port)
	}

	s.Stop()
	if err := s.Watch("k", port); !errors.Is(err, ErrStopped) {
		t.Fatalf("Watch after Stop = %v, want ErrStopped", err)
	}
	s.Start(context.Background()) // final: no restart
	s.mu.Lock()
	started := s.started
	s.mu.Unlock()
	if !started {
		t.Fatal("Start did not run before Stop")
	}
}

func TestUnwatchStopsProbeAndKeepsHistory(t *testing.T) {
	_, port := listenLoopback(t)
	s := New(Config{Interval: 5 * time.Millisecond, Timeout: time.Second})
	defer s.Stop()
	s.Start(context.Background())
	if err := s.Watch("k", port); err != nil {
		t.Fatalf("Watch: %v", err)
	}
	waitFor(t, 2*time.Second, "a sample", func() bool {
		stats, ok := s.Stats("k")
		return ok && stats.Count >= 1
	})
	s.Unwatch("k")

	stats, ok := s.Stats("k")
	if !ok {
		t.Fatal("Unwatch dropped the retained window")
	}
	count := stats.Count
	time.Sleep(50 * time.Millisecond)
	stats, _ = s.Stats("k")
	if stats.Count != count {
		t.Fatalf("probe continued after Unwatch: %d -> %d samples", count, stats.Count)
	}
	s.Unwatch("k") // idempotent
	s.Unwatch("missing")
}

func TestProbeConcurrencyCapped(t *testing.T) {
	var inFlight, maxInFlight atomic.Int32
	release := make(chan struct{})
	s := New(Config{
		Interval:      time.Hour,
		Timeout:       5 * time.Second,
		MaxConcurrent: 2,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			current := inFlight.Add(1)
			defer inFlight.Add(-1)
			for {
				peak := maxInFlight.Load()
				if current <= peak || maxInFlight.CompareAndSwap(peak, current) {
					break
				}
			}
			select {
			case <-release:
				return nil, errors.New("test: released")
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	})
	for i := range 5 {
		if err := s.Watch(fmt.Sprintf("node:%d", i), 7000+i); err != nil {
			t.Fatalf("Watch: %v", err)
		}
	}
	s.Start(context.Background())
	waitFor(t, 2*time.Second, "two in-flight probes", func() bool {
		return inFlight.Load() == 2
	})
	time.Sleep(50 * time.Millisecond)
	if peak := maxInFlight.Load(); peak != 2 {
		t.Fatalf("peak concurrent probes = %d, want 2", peak)
	}
	s.Stop()
	close(release)
	if peak := maxInFlight.Load(); peak > 2 {
		t.Fatalf("peak concurrent probes after Stop = %d, want <= 2", peak)
	}
}

func TestStartStopNoGoroutineLeak(t *testing.T) {
	_, port := listenLoopback(t)
	before := runtime.NumGoroutine()
	s := New(Config{Interval: 5 * time.Millisecond, Timeout: time.Second})
	for i := range 3 {
		if err := s.Watch(fmt.Sprintf("acct:%d", i), port); err != nil {
			t.Fatalf("Watch: %v", err)
		}
	}
	s.Start(context.Background())
	waitFor(t, 2*time.Second, "samples on every node", func() bool {
		for i := range 3 {
			stats, ok := s.Stats(fmt.Sprintf("acct:%d", i))
			if !ok || stats.Count == 0 {
				return false
			}
		}
		return true
	})
	s.Stop()
	s.Stop() // idempotent

	waitFor(t, 3*time.Second, "probe goroutines to exit", func() bool {
		return runtime.NumGoroutine() <= before
	})
}

func TestConcurrentWatchUnwatchStats(t *testing.T) {
	_, port := listenLoopback(t)
	s := New(Config{Interval: 5 * time.Millisecond, Timeout: time.Second})
	s.Start(context.Background())
	defer s.Stop()

	var wg sync.WaitGroup
	for worker := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			key := fmt.Sprintf("key:%d", worker%4)
			for range 50 {
				if err := s.Watch(key, port); err != nil && !errors.Is(err, ErrStopped) {
					t.Errorf("Watch: %v", err)
					return
				}
				s.Stats(key)
				s.Unwatch(key)
			}
		}()
	}
	wg.Wait()
}
