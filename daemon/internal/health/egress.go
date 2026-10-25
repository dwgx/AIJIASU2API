package health

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"sync"
	"time"
)

// Egress probe defaults. The interval is deliberately slower than the
// loopback probe: every sample is a real flow through the node's gate and the
// vendor session behind it, so the sampler must not spend the account's
// budget or session capacity on health checks.
const (
	// DefaultEgressInterval is the egress probe period. 60 samples cover one
	// hour.
	DefaultEgressInterval = 60 * time.Second
	// DefaultEgressTimeout bounds one whole HTTP exchange through the gate.
	DefaultEgressTimeout = 8 * time.Second
	// DefaultEgressTarget answers 204 without a body, so the probe measures
	// the path and nothing else.
	DefaultEgressTarget = "https://www.gstatic.com/generate_204"
	// DefaultEgressCapacity is the per-node ring size.
	DefaultEgressCapacity = 60
	// DefaultEgressMaxConcurrent caps simultaneous egress probes.
	DefaultEgressMaxConcurrent = 4
)

// Egress failure classes recorded in EgressSample.ErrorClass. A successful
// sample carries the empty string.
const (
	// ErrorProxy marks a gate or SOCKS5 failure: the request never reached
	// the target.
	ErrorProxy = "proxy"
	// ErrorHTTP marks a target response outside 2xx. The status code is
	// recorded separately.
	ErrorHTTP = "http"
)

// Egress state thresholds. They differ from the loopback thresholds because an
// egress sample includes the gate's vendor flow and the target's TLS
// handshake; 800ms would label a healthy cross-border path degraded.
const (
	egressDownAfterFailures    = 3
	egressDownBelowSuccess     = 0.5
	egressDegradedAboveP95Ms   = 3000
	egressDegradedBelowSuccess = 0.9
)

// egressTargetUserAgent identifies the probe in the target's access log. It
// carries no node, account or credential.
const egressTargetUserAgent = "ajs-accountd-egress-health"

// EgressSample is one HTTP exchange through a node's gate. At is the instant
// the probe started; LatencyMs covers the SOCKS5 handshake, TLS and the
// response. Status is 0 when no response was read. ErrorClass is empty when OK
// is true.
type EgressSample struct {
	At         time.Time
	LatencyMs  float64
	OK         bool
	Status     int
	ErrorClass string
}

// EgressStats is the aggregate over the retained egress window of one node.
// Percentiles are computed over successful samples only: a failed probe's
// duration measures the failure path, not egress latency. SuccessRate is
// OK/Count over the whole window, and Last is the newest sample regardless of
// outcome.
type EgressStats struct {
	Key                 string
	Count               int
	OK                  int
	SuccessRate         float64
	P50Ms               float64
	P95Ms               float64
	Last                EgressSample
	ConsecutiveFailures int
	State               string
}

// EgressConfig configures an EgressSampler. Zero values select the defaults
// above; the constructor never reads the environment.
type EgressConfig struct {
	// Interval is the probe period. Default DefaultEgressInterval.
	Interval time.Duration
	// Timeout bounds one HTTP exchange end to end. Default
	// DefaultEgressTimeout.
	Timeout time.Duration
	// Target is the absolute http(s) URL fetched through the gate. Empty
	// selects DefaultEgressTarget.
	Target string
	// Capacity is the per-node ring size. Default DefaultEgressCapacity.
	Capacity int
	// MaxConcurrent caps simultaneous probes. Default
	// DefaultEgressMaxConcurrent.
	MaxConcurrent int
	// Now is the clock used for EgressSample.At. Nil uses time.Now. Tests
	// pin it.
	Now func() time.Time
	// DialGate opens the TCP connection to the node's loopback gate. Nil uses
	// the loopback dialer. Tests inject it only to exercise dial failures
	// deterministically; production always dials 127.0.0.1.
	DialGate func(ctx context.Context, network, address string) (net.Conn, error)
	// TLSConfig is the TLS client configuration for the target request. Nil
	// verifies against the system roots; tests trust an httptest server.
	TLSConfig *tls.Config
}

// ValidateEgressTarget reports whether target is an absolute http(s) URL with
// a host. An empty target is valid and selects DefaultEgressTarget.
func ValidateEgressTarget(target string) error {
	if target == "" {
		return nil
	}
	parsed, err := url.Parse(target)
	if err != nil {
		return fmt.Errorf("health: egress target %q is not a URL: %w", target, err)
	}
	if parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("health: egress target %q must be an absolute http(s) URL", target)
	}
	if parsed.User != nil {
		return fmt.Errorf("health: egress target %q must not carry credentials", target)
	}
	return nil
}

// egressNode is one node's egress ring plus its probe bookkeeping.
type egressNode struct {
	ring egressRing
	port int
	// cancel and done are non-nil exactly while a probe goroutine runs for
	// this node.
	cancel context.CancelFunc
	done   chan struct{}
}

// EgressSampler probes watched nodes through their loopback gate on a fixed
// interval and retains a bounded window per node. Unlike the loopback Sampler
// it keeps no history after Unwatch: an egress window describes a live gate,
// and the API only renders nodes the health sampler still watches. All
// exported methods are safe for concurrent use.
type EgressSampler struct {
	cfg EgressConfig

	mu      sync.Mutex
	nodes   map[string]*egressNode
	started bool
	stopped bool
	ctx     context.Context
	cancel  context.CancelFunc

	sem chan struct{}
	wg  sync.WaitGroup
}

// NewEgressSampler validates cfg and returns a stopped sampler. It starts no
// goroutine, dials nothing and reads no environment variable.
func NewEgressSampler(cfg EgressConfig) (*EgressSampler, error) {
	if err := ValidateEgressTarget(cfg.Target); err != nil {
		return nil, err
	}
	if cfg.Target == "" {
		cfg.Target = DefaultEgressTarget
	}
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultEgressInterval
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultEgressTimeout
	}
	if cfg.Capacity <= 0 {
		cfg.Capacity = DefaultEgressCapacity
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = DefaultEgressMaxConcurrent
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &EgressSampler{
		cfg:   cfg,
		nodes: make(map[string]*egressNode),
		sem:   make(chan struct{}, cfg.MaxConcurrent),
	}, nil
}

// Start launches one probe goroutine per watched node and is idempotent: a
// second call is a no-op. The sampler derives its lifetime from ctx, so a
// cancelled parent stops the probes as well; Stop still joins them. Start
// after Stop is a no-op because Stop is final.
func (s *EgressSampler) Start(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started || s.stopped {
		return
	}
	s.started = true
	s.ctx, s.cancel = context.WithCancel(ctx)
	for key, node := range s.nodes {
		if node.cancel == nil {
			s.startLocked(key, node)
		}
	}
}

// Stop cancels every probe, waits for all probe goroutines to return and marks
// the sampler final. It is idempotent and safe to call without Start.
func (s *EgressSampler) Stop() {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	cancel := s.cancel
	for _, node := range s.nodes {
		node.cancel = nil
		node.done = nil
	}
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.wg.Wait()
}

// Watch registers a live gate and, once Start has been called, launches its
// egress probe. key is conventionally "accountID:serverID"; port is the
// loopback SOCKS5 gate port. Watching an already-watched key is a no-op and
// keeps the original port. Watch is the only way an egress probe starts: a
// node with no live gate is never sampled.
func (s *EgressSampler) Watch(key string, port int) error {
	if key == "" {
		return ErrInvalidKey
	}
	if port < 1 || port > 65535 {
		return ErrInvalidPort
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return ErrStopped
	}
	if node, ok := s.nodes[key]; ok {
		if node.cancel == nil {
			node.port = port
			if s.started {
				s.startLocked(key, node)
			}
		}
		return nil
	}
	node := &egressNode{ring: newEgressRing(s.cfg.Capacity), port: port}
	s.nodes[key] = node
	if s.started {
		s.startLocked(key, node)
	}
	return nil
}

// Unwatch stops the probe goroutine for key, waits for it to return and drops
// the retained window. Unlike the loopback sampler, egress history does not
// outlive the gate it measured.
func (s *EgressSampler) Unwatch(key string) {
	s.mu.Lock()
	node, ok := s.nodes[key]
	if !ok {
		s.mu.Unlock()
		return
	}
	cancel, done := node.cancel, node.done
	delete(s.nodes, key)
	s.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}

// Stats returns the egress aggregate for key. ok is false when the key was
// never watched or has been unwatched. A watched node with no sample yet
// reports StateOK and Count 0.
func (s *EgressSampler) Stats(key string) (EgressStats, bool) {
	s.mu.Lock()
	node, ok := s.nodes[key]
	if !ok {
		s.mu.Unlock()
		return EgressStats{}, false
	}
	samples := node.ring.samples()
	s.mu.Unlock()
	return aggregateEgress(key, samples), true
}

// startLocked launches the probe goroutine for one node. Callers hold s.mu and
// have checked started and node.cancel == nil.
func (s *EgressSampler) startLocked(key string, node *egressNode) {
	ctx, cancel := context.WithCancel(s.ctx)
	done := make(chan struct{})
	port := node.port
	node.cancel = cancel
	node.done = done
	s.wg.Add(1)
	go s.loop(ctx, key, node, port, done)
}

// loop probes immediately, then once per interval, until ctx is cancelled.
// port is captured when the goroutine starts so a re-arm cannot change the
// target under a running probe.
func (s *EgressSampler) loop(ctx context.Context, key string, node *egressNode, port int, done chan struct{}) {
	defer s.wg.Done()
	defer close(done)
	s.probe(ctx, key, node, port)
	ticker := time.NewTicker(s.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.probe(ctx, key, node, port)
		}
	}
}

// probe runs one HTTP exchange through the gate. It takes a concurrency slot,
// bounds the exchange with the probe timeout and records the result unless the
// sampler itself is shutting down (a cancelled probe is not a health signal).
func (s *EgressSampler) probe(ctx context.Context, key string, node *egressNode, port int) {
	select {
	case s.sem <- struct{}{}:
	case <-ctx.Done():
		return
	}
	defer func() { <-s.sem }()

	at := s.cfg.Now()
	start := time.Now()
	status, class := s.fetch(ctx, port)
	if ctx.Err() != nil {
		return
	}
	s.record(key, node, EgressSample{
		At:         at,
		LatencyMs:  milliseconds(time.Since(start)),
		OK:         class == "",
		Status:     status,
		ErrorClass: class,
	})
}

// fetch requests the configured target through the gate on port and returns
// the HTTP status and failure class. A 2xx answer is the only success: a
// redirect or an error page means the path did not carry the probe to the
// target it names.
func (s *EgressSampler) fetch(ctx context.Context, port int) (int, string) {
	target, err := url.Parse(s.cfg.Target)
	if err != nil || target.Host == "" {
		return 0, ErrorOther
	}
	probeCtx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()

	dialer := &gateDialer{
		proxy: net.JoinHostPort(loopbackHost, strconv.Itoa(port)),
		dial:  s.cfg.DialGate,
	}
	transport := &http.Transport{
		// Never inherit an environment proxy: the only path out is the gate.
		Proxy:               nil,
		DialContext:         dialer.DialContext,
		TLSClientConfig:     s.cfg.TLSConfig,
		TLSHandshakeTimeout: s.cfg.Timeout,
		DisableKeepAlives:   true,
		ForceAttemptHTTP2:   false,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		// A redirect would measure a different host than the configured
		// target; record its status as a failure instead.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, target.String(), nil)
	if err != nil {
		return 0, ErrorOther
	}
	req.Header.Set("User-Agent", egressTargetUserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return 0, classifyEgressError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.StatusCode, ErrorHTTP
	}
	return resp.StatusCode, ""
}

// record appends a sample only if node is still the live entry for key, so a
// probe that outlives an Unwatch cannot write into a dropped ring.
func (s *EgressSampler) record(key string, node *egressNode, sample EgressSample) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nodes[key] != node {
		return
	}
	node.ring.append(sample)
}

// gateDialer dials the node's gate and issues a SOCKS5 CONNECT for the address
// the HTTP transport asked for. It implements only the no-auth CONNECT method
// the gate serves in its default AuthLoopback mode. A gate started with
// AJS_GATE_AUTH=userpass refuses the greeting, and the sample records
// ErrorProxy instead of pretending the path worked.
type gateDialer struct {
	proxy string
	dial  func(ctx context.Context, network, address string) (net.Conn, error)
}

// gateError is a SOCKS5 protocol failure. It carries a console class so the
// aggregate can distinguish "the gate refused" from "the target failed".
type gateError struct {
	class string
	err   error
}

func (e *gateError) Error() string { return e.err.Error() }
func (e *gateError) Unwrap() error { return e.err }

func (d *gateDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	switch network {
	case "tcp", "tcp4", "tcp6":
	default:
		return nil, &gateError{class: ErrorOther, err: fmt.Errorf("egress: unsupported network %q", network)}
	}
	dial := d.dial
	if dial == nil {
		dial = dialTCP
	}
	conn, err := dial(ctx, "tcp", d.proxy)
	if err != nil {
		return nil, fmt.Errorf("egress: dial gate: %w", err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		// The deadline covers the handshake and the HTTP exchange; the
		// transport closes the connection when the request context ends.
		_ = conn.SetDeadline(deadline)
	}
	if err := gateConnect(conn, address); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

// gateConnect performs the no-auth greeting and one CONNECT request on an
// established connection, then consumes the bound-address reply. Transport
// read/write errors are returned raw so the caller's classifier can report a
// timeout as such; protocol-level refusals carry ErrorProxy.
func gateConnect(conn net.Conn, address string) error {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return &gateError{class: ErrorOther, err: fmt.Errorf("egress: split target %q: %w", address, err)}
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return &gateError{class: ErrorOther, err: fmt.Errorf("egress: invalid target port %q", portText)}
	}

	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		return fmt.Errorf("egress: socks5 greeting: %w", err)
	}
	var greeting [2]byte
	if _, err := io.ReadFull(conn, greeting[:]); err != nil {
		return fmt.Errorf("egress: socks5 greeting reply: %w", err)
	}
	if greeting[0] != 0x05 || greeting[1] != 0x00 {
		return &gateError{
			class: ErrorProxy,
			err:   fmt.Errorf("egress: gate rejected no-auth method (version %d, method %d)", greeting[0], greeting[1]),
		}
	}

	request := make([]byte, 0, 262)
	request = append(request, 0x05, 0x01, 0x00)
	if ip := net.ParseIP(host); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			request = append(request, 0x01)
			request = append(request, ip4...)
		} else {
			request = append(request, 0x04)
			request = append(request, ip.To16()...)
		}
	} else {
		if host == "" || len(host) > 255 {
			return &gateError{class: ErrorOther, err: fmt.Errorf("egress: invalid target host %q", host)}
		}
		request = append(request, 0x03, byte(len(host)))
		request = append(request, host...)
	}
	request = append(request, byte(port>>8), byte(port))
	if _, err := conn.Write(request); err != nil {
		return fmt.Errorf("egress: socks5 connect request: %w", err)
	}

	var reply [4]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		return fmt.Errorf("egress: socks5 connect reply: %w", err)
	}
	if reply[0] != 0x05 {
		return &gateError{class: ErrorProxy, err: fmt.Errorf("egress: socks5 reply version %d, want 5", reply[0])}
	}
	if reply[1] != 0x00 {
		return &gateError{class: ErrorProxy, err: fmt.Errorf("egress: socks5 connect failed (reply %d)", reply[1])}
	}
	var skip int
	switch reply[3] {
	case 0x01:
		skip = 4
	case 0x04:
		skip = 16
	case 0x03:
		var length [1]byte
		if _, err := io.ReadFull(conn, length[:]); err != nil {
			return fmt.Errorf("egress: socks5 bound address: %w", err)
		}
		skip = int(length[0])
	default:
		return &gateError{class: ErrorProxy, err: fmt.Errorf("egress: socks5 reply address type %d", reply[3])}
	}
	bound := make([]byte, skip+2)
	if _, err := io.ReadFull(conn, bound); err != nil {
		return fmt.Errorf("egress: socks5 bound address: %w", err)
	}
	return nil
}

// classifyEgressError maps an HTTP client error onto the console classes. A
// gate protocol failure keeps its class; everything else uses the loopback
// classifier, which recognises timeouts, refused and unreachable sockets.
func classifyEgressError(err error) string {
	if err == nil {
		return ""
	}
	var gateErr *gateError
	if errors.As(err, &gateErr) {
		return gateErr.class
	}
	return classifyError(err)
}

// aggregateEgress folds a window (oldest-first) into EgressStats with the same
// nearest-rank percentile rule the loopback sampler uses.
func aggregateEgress(key string, samples []EgressSample) EgressStats {
	stats := EgressStats{Key: key, Count: len(samples), State: StateOK}
	if len(samples) == 0 {
		return stats
	}
	stats.Last = samples[len(samples)-1]

	latencies := make([]float64, 0, len(samples))
	for _, sample := range samples {
		if sample.OK {
			latencies = append(latencies, sample.LatencyMs)
		}
	}
	stats.OK = len(latencies)
	stats.SuccessRate = float64(stats.OK) / float64(stats.Count)
	if len(latencies) > 0 {
		sort.Float64s(latencies)
		stats.P50Ms = percentile(latencies, 0.50)
		stats.P95Ms = percentile(latencies, 0.95)
	}
	for i := len(samples) - 1; i >= 0 && !samples[i].OK; i-- {
		stats.ConsecutiveFailures++
	}
	stats.State = egressState(stats)
	return stats
}

// egressState applies the console state machine to an egress window. Down wins
// over degraded: three consecutive failures or a success rate under 50% is
// down; a p95 over 3s or a success rate under 90% is degraded. An empty window
// is ok: there is no evidence of failure yet.
func egressState(stats EgressStats) string {
	if stats.Count == 0 {
		return StateOK
	}
	if stats.ConsecutiveFailures >= egressDownAfterFailures || stats.SuccessRate < egressDownBelowSuccess {
		return StateDown
	}
	if stats.P95Ms > egressDegradedAboveP95Ms || stats.SuccessRate < egressDegradedBelowSuccess {
		return StateDegraded
	}
	return StateOK
}

// egressRing is a fixed-capacity FIFO of egress samples. It is not
// goroutine-safe; the EgressSampler guards it with its mutex.
type egressRing struct {
	buf  []EgressSample
	next int // index of the slot after the newest sample
	size int
}

func newEgressRing(capacity int) egressRing {
	if capacity < 1 {
		capacity = 1
	}
	return egressRing{buf: make([]EgressSample, capacity)}
}

// append writes one sample, overwriting the oldest when full.
func (r *egressRing) append(sample EgressSample) {
	if len(r.buf) == 0 {
		return
	}
	r.buf[r.next] = sample
	r.next = (r.next + 1) % len(r.buf)
	if r.size < len(r.buf) {
		r.size++
	}
}

// samples copies the window oldest-first.
func (r *egressRing) samples() []EgressSample {
	out := make([]EgressSample, r.size)
	start := 0
	if r.size == len(r.buf) {
		start = r.next
	}
	for i := range r.size {
		out[i] = r.buf[(start+i)%len(r.buf)]
	}
	return out
}
