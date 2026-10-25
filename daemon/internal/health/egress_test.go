package health

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
)

// fakeGate is a minimal no-auth SOCKS5 server that relays each CONNECT to the
// address the client asked for. It stands in for the node's gate so every
// egress test stays on loopback.
type fakeGate struct {
	ln       net.Listener
	stall    bool
	mu       sync.Mutex
	accepted int
	conns    map[net.Conn]struct{}
	closed   chan struct{}
	once     sync.Once
	wg       sync.WaitGroup
}

func newFakeGate(t *testing.T, stall bool) *fakeGate {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen fake gate: %v", err)
	}
	gate := &fakeGate{
		ln:     ln,
		stall:  stall,
		conns:  make(map[net.Conn]struct{}),
		closed: make(chan struct{}),
	}
	gate.wg.Add(1)
	go gate.serve()
	t.Cleanup(gate.Close)
	return gate
}

func (g *fakeGate) port() int {
	return g.ln.Addr().(*net.TCPAddr).Port
}

func (g *fakeGate) connections() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.accepted
}

func (g *fakeGate) Close() {
	g.once.Do(func() {
		close(g.closed)
		_ = g.ln.Close()
		g.mu.Lock()
		for conn := range g.conns {
			_ = conn.Close()
		}
		g.mu.Unlock()
	})
	g.wg.Wait()
}

func (g *fakeGate) serve() {
	defer g.wg.Done()
	for {
		conn, err := g.ln.Accept()
		if err != nil {
			return
		}
		g.mu.Lock()
		g.accepted++
		g.conns[conn] = struct{}{}
		g.mu.Unlock()
		g.wg.Add(1)
		go func() {
			defer g.wg.Done()
			defer func() {
				g.mu.Lock()
				delete(g.conns, conn)
				g.mu.Unlock()
				_ = conn.Close()
			}()
			g.handle(conn)
		}()
	}
}

func (g *fakeGate) handle(conn net.Conn) {
	target, err := readGateRequest(conn)
	if err != nil {
		return
	}
	if g.stall {
		// Hold the CONNECT open without a reply so the client's deadline is
		// the only way out.
		<-g.closed
		return
	}
	upstream, err := net.Dial("tcp", target)
	if err != nil {
		_, _ = conn.Write([]byte{0x05, 0x04, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	defer upstream.Close()
	if _, err := conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(upstream, conn)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(conn, upstream)
		done <- struct{}{}
	}()
	<-done
}

// readGateRequest completes the no-auth greeting and one CONNECT request, and
// returns the requested target address.
func readGateRequest(conn net.Conn) (string, error) {
	var head [2]byte
	if _, err := io.ReadFull(conn, head[:]); err != nil {
		return "", err
	}
	methods := make([]byte, int(head[1]))
	if _, err := io.ReadFull(conn, methods); err != nil {
		return "", err
	}
	if _, err := conn.Write([]byte{0x05, 0x00}); err != nil {
		return "", err
	}
	var request [4]byte
	if _, err := io.ReadFull(conn, request[:]); err != nil {
		return "", err
	}
	var host string
	switch request[3] {
	case 0x01:
		var address [4]byte
		if _, err := io.ReadFull(conn, address[:]); err != nil {
			return "", err
		}
		host = net.IP(address[:]).String()
	case 0x04:
		var address [16]byte
		if _, err := io.ReadFull(conn, address[:]); err != nil {
			return "", err
		}
		host = net.IP(address[:]).String()
	case 0x03:
		var length [1]byte
		if _, err := io.ReadFull(conn, length[:]); err != nil {
			return "", err
		}
		name := make([]byte, int(length[0]))
		if _, err := io.ReadFull(conn, name); err != nil {
			return "", err
		}
		host = string(name)
	default:
		return "", fmt.Errorf("fake gate: address type %d", request[3])
	}
	var port [2]byte
	if _, err := io.ReadFull(conn, port[:]); err != nil {
		return "", err
	}
	return net.JoinHostPort(host, strconv.Itoa(int(port[0])<<8|int(port[1]))), nil
}

// newEgressTarget serves one status over TLS. The returned config trusts the
// test server's certificate, the same seam runtime.LabView exposes.
func newEgressTarget(t *testing.T, status int) (*httptest.Server, *tls.Config) {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("egress target method = %s, want GET", r.Method)
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(server.Close)
	transport, ok := server.Client().Transport.(*http.Transport)
	if !ok {
		t.Fatal("httptest TLS client transport is not *http.Transport")
	}
	return server, transport.TLSClientConfig.Clone()
}

func startEgressSampler(t *testing.T, cfg EgressConfig) *EgressSampler {
	t.Helper()
	sampler, err := NewEgressSampler(cfg)
	if err != nil {
		t.Fatalf("new egress sampler: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sampler.Start(ctx)
	t.Cleanup(sampler.Stop)
	return sampler
}

func egressSample(t *testing.T, sampler *EgressSampler, key string) EgressStats {
	t.Helper()
	var stats EgressStats
	waitFor(t, 2*time.Second, "egress sample", func() bool {
		current, ok := sampler.Stats(key)
		if !ok || current.Count == 0 {
			return false
		}
		stats = current
		return true
	})
	return stats
}

func TestEgressDefaultsAndTargetValidation(t *testing.T) {
	sampler, err := NewEgressSampler(EgressConfig{})
	if err != nil {
		t.Fatalf("new sampler: %v", err)
	}
	if sampler.cfg.Interval != DefaultEgressInterval || sampler.cfg.Timeout != DefaultEgressTimeout {
		t.Fatalf("defaults = interval %s timeout %s", sampler.cfg.Interval, sampler.cfg.Timeout)
	}
	if sampler.cfg.Target != DefaultEgressTarget {
		t.Fatalf("default target = %q, want %q", sampler.cfg.Target, DefaultEgressTarget)
	}
	if sampler.cfg.Capacity != DefaultEgressCapacity {
		t.Fatalf("default capacity = %d, want %d", sampler.cfg.Capacity, DefaultEgressCapacity)
	}
	if sampler.cfg.MaxConcurrent != DefaultEgressMaxConcurrent {
		t.Fatalf("default concurrency = %d, want %d", sampler.cfg.MaxConcurrent, DefaultEgressMaxConcurrent)
	}

	for _, target := range []string{
		"gstatic.com/generate_204",
		"ftp://example.com/x",
		"https://",
		"https://user:secret@example.com/x",
	} {
		if _, err := NewEgressSampler(EgressConfig{Target: target}); err == nil {
			t.Fatalf("NewEgressSampler accepted invalid target %q", target)
		}
	}
	if err := ValidateEgressTarget("http://127.0.0.1:8080/health"); err != nil {
		t.Fatalf("valid target rejected: %v", err)
	}
}

func TestEgressProbeThroughGate(t *testing.T) {
	target, tlsConfig := newEgressTarget(t, http.StatusNoContent)
	gate := newFakeGate(t, false)
	sampler := startEgressSampler(t, EgressConfig{
		Interval:  time.Hour,
		Timeout:   2 * time.Second,
		Target:    target.URL,
		TLSConfig: tlsConfig,
	})
	if err := sampler.Watch("node-a", gate.port()); err != nil {
		t.Fatalf("watch: %v", err)
	}

	stats := egressSample(t, sampler, "node-a")
	if stats.Count != 1 || stats.OK != 1 || stats.State != StateOK {
		t.Fatalf("stats = %+v, want one successful sample", stats)
	}
	if stats.Last.Status != http.StatusNoContent || !stats.Last.OK || stats.Last.ErrorClass != "" {
		t.Fatalf("last = %+v, want HTTP 204 ok", stats.Last)
	}
	if stats.Last.LatencyMs <= 0 {
		t.Fatalf("latency = %f, want a measured duration", stats.Last.LatencyMs)
	}
	if stats.P50Ms <= 0 || stats.P95Ms <= 0 {
		t.Fatalf("percentiles = %f/%f, want measured values", stats.P50Ms, stats.P95Ms)
	}
}

func TestEgressNon200IsAClassifiedFailure(t *testing.T) {
	target, tlsConfig := newEgressTarget(t, http.StatusServiceUnavailable)
	gate := newFakeGate(t, false)
	sampler := startEgressSampler(t, EgressConfig{
		Interval:  time.Hour,
		Timeout:   2 * time.Second,
		Target:    target.URL,
		TLSConfig: tlsConfig,
	})
	if err := sampler.Watch("node-a", gate.port()); err != nil {
		t.Fatalf("watch: %v", err)
	}

	stats := egressSample(t, sampler, "node-a")
	if stats.Last.OK || stats.Last.ErrorClass != ErrorHTTP {
		t.Fatalf("last = %+v, want an http failure", stats.Last)
	}
	if stats.Last.Status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", stats.Last.Status)
	}
	if stats.State != StateDown {
		t.Fatalf("state = %q, want down for a zero-success window", stats.State)
	}
}

func TestEgressRedirectIsNotSuccess(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://example.com/elsewhere", http.StatusFound)
	}))
	t.Cleanup(target.Close)
	transport, ok := target.Client().Transport.(*http.Transport)
	if !ok {
		t.Fatal("httptest TLS client transport is not *http.Transport")
	}
	gate := newFakeGate(t, false)
	sampler := startEgressSampler(t, EgressConfig{
		Interval:  time.Hour,
		Timeout:   2 * time.Second,
		Target:    target.URL,
		TLSConfig: transport.TLSClientConfig.Clone(),
	})
	if err := sampler.Watch("node-a", gate.port()); err != nil {
		t.Fatalf("watch: %v", err)
	}

	stats := egressSample(t, sampler, "node-a")
	if stats.Last.OK || stats.Last.ErrorClass != ErrorHTTP || stats.Last.Status != http.StatusFound {
		t.Fatalf("last = %+v, want a 302 http failure", stats.Last)
	}
}

func TestEgressTimeoutThroughStalledGate(t *testing.T) {
	gate := newFakeGate(t, true)
	sampler := startEgressSampler(t, EgressConfig{
		Interval: time.Hour,
		Timeout:  100 * time.Millisecond,
		Target:   "https://127.0.0.1:9/generate_204",
	})
	if err := sampler.Watch("node-a", gate.port()); err != nil {
		t.Fatalf("watch: %v", err)
	}

	stats := egressSample(t, sampler, "node-a")
	if stats.Last.OK || stats.Last.ErrorClass != ErrorTimeout {
		t.Fatalf("last = %+v, want a timeout", stats.Last)
	}
	if stats.Last.Status != 0 {
		t.Fatalf("status = %d, want 0 without a response", stats.Last.Status)
	}
}

func TestEgressRefusedGate(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}

	sampler := startEgressSampler(t, EgressConfig{
		Interval: time.Hour,
		Timeout:  time.Second,
		Target:   "https://127.0.0.1:9/generate_204",
	})
	if err := sampler.Watch("node-a", port); err != nil {
		t.Fatalf("watch: %v", err)
	}

	stats := egressSample(t, sampler, "node-a")
	if stats.Last.OK || stats.Last.ErrorClass != ErrorRefused {
		t.Fatalf("last = %+v, want a refused gate", stats.Last)
	}
}

func TestEgressNoProbeWithoutWatch(t *testing.T) {
	gate := newFakeGate(t, false)
	sampler := startEgressSampler(t, EgressConfig{
		Interval: 50 * time.Millisecond,
		Timeout:  time.Second,
		Target:   "https://127.0.0.1:9/generate_204",
	})

	// Starting a sampler with no watched node must dial nothing: an egress
	// probe only exists for a live gate.
	time.Sleep(200 * time.Millisecond)
	if got := gate.connections(); got != 0 {
		t.Fatalf("gate saw %d connections without a watch, want 0", got)
	}
	if _, ok := sampler.Stats("node-a"); ok {
		t.Fatal("unwatched node reported stats")
	}
}

func TestEgressUnwatchStopsProbeAndDropsWindow(t *testing.T) {
	target, tlsConfig := newEgressTarget(t, http.StatusNoContent)
	gate := newFakeGate(t, false)
	sampler := startEgressSampler(t, EgressConfig{
		Interval:  50 * time.Millisecond,
		Timeout:   time.Second,
		Target:    target.URL,
		TLSConfig: tlsConfig,
	})
	if err := sampler.Watch("node-a", gate.port()); err != nil {
		t.Fatalf("watch: %v", err)
	}
	egressSample(t, sampler, "node-a")

	sampler.Unwatch("node-a")
	if _, ok := sampler.Stats("node-a"); ok {
		t.Fatal("unwatched node still reports an egress window")
	}
	settled := gate.connections()
	time.Sleep(250 * time.Millisecond)
	if got := gate.connections(); got != settled {
		t.Fatalf("gate saw %d connections after unwatch, want the settled %d", got, settled)
	}
}

func TestEgressRingBounds(t *testing.T) {
	sampler, err := NewEgressSampler(EgressConfig{Capacity: 3})
	if err != nil {
		t.Fatalf("new sampler: %v", err)
	}
	if err := sampler.Watch("node-a", 13000); err != nil {
		t.Fatalf("watch: %v", err)
	}
	sampler.mu.Lock()
	node := sampler.nodes["node-a"]
	sampler.mu.Unlock()
	for i := 1; i <= 5; i++ {
		sampler.record("node-a", node, EgressSample{
			At:        time.Unix(int64(i), 0),
			LatencyMs: float64(i),
			OK:        true,
			Status:    http.StatusNoContent,
		})
	}

	stats, ok := sampler.Stats("node-a")
	if !ok {
		t.Fatal("watched node has no stats")
	}
	if stats.Count != 3 || stats.OK != 3 {
		t.Fatalf("count/ok = %d/%d, want the ring capacity 3/3", stats.Count, stats.OK)
	}
	if stats.Last.LatencyMs != 5 || stats.Last.At.Unix() != 5 {
		t.Fatalf("last = %+v, want the newest sample", stats.Last)
	}
}

func TestEgressAggregateAndState(t *testing.T) {
	at := time.Unix(1_700_000_000, 0)
	mixed := aggregateEgress("node-a", []EgressSample{
		{At: at, LatencyMs: 10, OK: true, Status: 204},
		{At: at, LatencyMs: 20, OK: true, Status: 204},
		{At: at, LatencyMs: 8000, ErrorClass: ErrorTimeout},
	})
	if mixed.Count != 3 || mixed.OK != 2 {
		t.Fatalf("count/ok = %d/%d, want 3/2", mixed.Count, mixed.OK)
	}
	if !closeTo(mixed.SuccessRate, 2.0/3.0) {
		t.Fatalf("success rate = %f, want 2/3", mixed.SuccessRate)
	}
	if mixed.P50Ms != 10 || mixed.P95Ms != 20 {
		t.Fatalf("percentiles = %f/%f, want 10/20 over successful samples only", mixed.P50Ms, mixed.P95Ms)
	}
	if mixed.ConsecutiveFailures != 1 || mixed.State != StateDegraded {
		t.Fatalf("failures/state = %d/%q, want 1/degraded", mixed.ConsecutiveFailures, mixed.State)
	}

	allFailed := aggregateEgress("node-a", []EgressSample{
		{At: at, ErrorClass: ErrorHTTP, Status: 503},
		{At: at, ErrorClass: ErrorHTTP, Status: 503},
		{At: at, ErrorClass: ErrorHTTP, Status: 503},
	})
	if allFailed.State != StateDown {
		t.Fatalf("state = %q, want down after three failures", allFailed.State)
	}

	slow := aggregateEgress("node-a", []EgressSample{
		{At: at, LatencyMs: 3100, OK: true, Status: 204},
	})
	if slow.State != StateDegraded {
		t.Fatalf("state = %q, want degraded for a 3.1s p95", slow.State)
	}

	empty := aggregateEgress("node-a", nil)
	if empty.State != StateOK || empty.Count != 0 {
		t.Fatalf("empty = %+v, want ok with no samples", empty)
	}
}

func TestEgressStopJoinsProbes(t *testing.T) {
	gate := newFakeGate(t, true)
	sampler, err := NewEgressSampler(EgressConfig{
		Interval: 10 * time.Millisecond,
		Timeout:  50 * time.Millisecond,
		Target:   "https://127.0.0.1:9/generate_204",
	})
	if err != nil {
		t.Fatalf("new sampler: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	sampler.Start(ctx)
	if err := sampler.Watch("node-a", gate.port()); err != nil {
		t.Fatalf("watch: %v", err)
	}
	egressSample(t, sampler, "node-a")
	sampler.Stop()
	cancel()

	// A stopped sampler is final: Watch refuses and nothing records again.
	if err := sampler.Watch("node-b", gate.port()); err != ErrStopped {
		t.Fatalf("watch after stop = %v, want ErrStopped", err)
	}
	sampler.Stop()
}
