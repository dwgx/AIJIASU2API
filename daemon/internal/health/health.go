// Package health measures gate health for the console.
//
// The loopback Sampler's probe is one TCP handshake to 127.0.0.1:<gate port>
// followed by an immediate close: no payload, no proxy protocol, no credential
// and no vendor traffic. It keeps one bounded ring per node key, folds the
// window into p50/p95/min/max, success rate and an ok|degraded|down state, and
// never talks to a vendor host.
//
// The opt-in EgressSampler measures real egress instead: one HTTP request
// through the node's own SOCKS5 gate to a configured target, kept in a bounded
// per-node ring with the same state vocabulary. It only samples a watched live
// gate and is off unless the daemon enables it.
//
// Both samplers own every goroutine they start. Start launches one per watched
// node; Stop cancels and joins all of them, so a stopped sampler leaves
// nothing running. The package is a pure library: it registers no route and
// reads no environment variable.
package health

import (
	"context"
	"errors"
	"net"
	"os"
	"strconv"
	"sync"
	"time"
)

// Defaults applied by New when a Config field is zero.
const (
	// DefaultInterval is the probe period. 120 samples cover one hour.
	DefaultInterval = 30 * time.Second
	// DefaultTimeout bounds one TCP dial.
	DefaultTimeout = 3 * time.Second
	// DefaultCapacity is the per-node ring size.
	DefaultCapacity = 120
	// DefaultMaxNodes bounds the number of retained node rings. The daemon
	// overrides it from AJS_HEALTH_MAX_NODES, which is clamped to 1..64 at
	// that boundary, so the default stays inside the accepted range.
	DefaultMaxNodes = 16
	// DefaultMaxConcurrent caps simultaneous probes.
	DefaultMaxConcurrent = 8
)

// Probe failure classes recorded in Sample.ErrorClass. A successful sample
// carries the empty string.
const (
	ErrorTimeout     = "timeout"
	ErrorRefused     = "refused"
	ErrorUnreachable = "unreachable"
	ErrorOther       = "other"
)

// Node health states reported by Stats.State.
const (
	StateOK       = "ok"
	StateDegraded = "degraded"
	StateDown     = "down"
)

// State thresholds. They are deliberately not configurable: the console
// contract is one state machine for every node.
const (
	downAfterFailures    = 3
	downBelowSuccess     = 0.5
	degradedAboveP95Ms   = 800
	degradedBelowSuccess = 0.9
)

// loopbackHost is the only address the sampler ever dials. The gate binds
// loopback, so a probe cannot leave the host.
const loopbackHost = "127.0.0.1"

// Watch errors.
var (
	// ErrInvalidKey is returned by Watch for an empty node key.
	ErrInvalidKey = errors.New("health: key is required")
	// ErrInvalidPort is returned by Watch for a port outside 1-65535.
	ErrInvalidPort = errors.New("health: port must be 1-65535")
	// ErrStopped is returned by Watch after Stop. A stopped sampler cannot
	// be restarted.
	ErrStopped = errors.New("health: sampler stopped")
)

// Sample is one probe result. At is the instant the probe started; LatencyMs
// is the connect+close duration (or the time to failure) in milliseconds.
// ErrorClass is empty when OK is true.
type Sample struct {
	At         time.Time
	LatencyMs  float64
	OK         bool
	ErrorClass string
}

// Stats is the aggregate over the retained window of one node. Percentiles,
// MinMs and MaxMs are computed over successful samples only, because a failed
// probe's duration measures the failure path, not gate latency. SuccessRate
// is OK/Count over the whole window, and Last is the newest sample regardless
// of outcome.
type Stats struct {
	Key                 string
	Count               int
	OK                  int
	SuccessRate         float64
	P50Ms               float64
	P95Ms               float64
	MinMs               float64
	MaxMs               float64
	Last                Sample
	ConsecutiveFailures int
	State               string
}

// Config configures a Sampler. Zero values select the defaults above; the
// constructor never reads the environment.
type Config struct {
	// Interval is the probe period. Default DefaultInterval.
	Interval time.Duration
	// Timeout bounds one dial. Default DefaultTimeout.
	Timeout time.Duration
	// Capacity is the per-node ring size. Default DefaultCapacity.
	Capacity int
	// MaxNodes bounds the number of node rings; the least recently used
	// ring is evicted beyond it. Default DefaultMaxNodes.
	MaxNodes int
	// MaxConcurrent caps simultaneous probes. Default DefaultMaxConcurrent.
	MaxConcurrent int
	// Now is the clock used for Sample.At and latency. Nil uses time.Now.
	// Tests pin it.
	Now func() time.Time
	// Dial opens one TCP connection. Nil uses a net.Dialer. Tests inject it
	// to exercise the timeout path deterministically; production always uses
	// the loopback dialer.
	Dial func(ctx context.Context, network, address string) (net.Conn, error)
}

// nodeState is one node's ring plus its watch bookkeeping. lastUsed is a
// monotonically increasing stamp for LRU eviction; it is touched by Watch and
// by every recorded sample.
type nodeState struct {
	ring     ringBuffer
	port     int
	lastUsed uint64
	// cancel and done are non-nil exactly while a probe goroutine runs for
	// this node.
	cancel context.CancelFunc
	done   chan struct{}
}

// evictionRecord is one entry of the bounded evicted-key log. The stamp
// identifies the record so a Watch that clears a key cannot resurrect an old
// record when the log is pruned.
type evictionRecord struct {
	key   string
	stamp uint64
}

// Sampler probes watched nodes on a fixed interval and retains a bounded
// window per node. All exported methods are safe for concurrent use.
type Sampler struct {
	cfg Config

	mu    sync.Mutex
	nodes map[string]*nodeState
	// evicted records the keys whose watched ring the LRU cap reclaimed, so
	// the daemon can re-arm a gate that still runs. A Watch clears the entry;
	// evictedOrder and evictedLimit keep the record bounded.
	evicted      map[string]uint64
	evictedOrder []evictionRecord
	evictedSeq   uint64
	evictedLimit int

	seq     uint64
	started bool
	stopped bool
	ctx     context.Context
	cancel  context.CancelFunc

	sem chan struct{}
	wg  sync.WaitGroup
}

// New returns a stopped sampler. It starts no goroutine, dials nothing and
// reads no environment variable.
func New(cfg Config) *Sampler {
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultInterval
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.Capacity <= 0 {
		cfg.Capacity = DefaultCapacity
	}
	if cfg.MaxNodes <= 0 {
		cfg.MaxNodes = DefaultMaxNodes
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = DefaultMaxConcurrent
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Dial == nil {
		cfg.Dial = dialTCP
	}
	// The record only needs to outlive one reconcile sweep, but a daemon may
	// run several gates per ring slot; four slots per node covers that with a
	// floor so a tiny MaxNodes still keeps a useful history.
	evictedLimit := 4 * cfg.MaxNodes
	if evictedLimit < 16 {
		evictedLimit = 16
	}
	return &Sampler{
		cfg:          cfg,
		nodes:        make(map[string]*nodeState),
		evicted:      make(map[string]uint64),
		evictedLimit: evictedLimit,
		sem:          make(chan struct{}, cfg.MaxConcurrent),
	}
}

// dialTCP is the production dialer. It connects and lets the caller close
// immediately; no bytes are exchanged.
func dialTCP(ctx context.Context, network, address string) (net.Conn, error) {
	var dialer net.Dialer
	return dialer.DialContext(ctx, network, address)
}

// Start launches one probe goroutine per watched node and is idempotent: a
// second call is a no-op. The sampler derives its lifetime from ctx, so a
// cancelled parent stops the probes as well; Stop still joins them. Start
// after Stop is a no-op because Stop is final.
func (s *Sampler) Start(ctx context.Context) {
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

// Stop cancels every probe, waits for all probe goroutines to return and
// marks the sampler final. It is idempotent and safe to call without Start.
func (s *Sampler) Stop() {
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

// Watch registers a node target and, once Start has been called, launches its
// probe goroutine. key is conventionally "accountID:serverID"; port is the
// loopback gate port. Watching an already-watched key is a no-op and keeps
// the original port. Watching a key whose history was retained but whose
// probe was stopped re-arms it with the new port. At capacity the least
// recently used node ring is evicted; an unwatched ring is preferred over a
// watched one, and a watched victim is recorded for Evicted so the caller can
// re-arm it. Watching a key always clears its Evicted record.
func (s *Sampler) Watch(key string, port int) error {
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
	// Watching a key again re-arms it: the window is fresh, so it is no
	// longer a missed ring the reconciler has to heal.
	delete(s.evicted, key)
	if node, ok := s.nodes[key]; ok {
		node.lastUsed = s.touchLocked()
		if node.cancel == nil {
			node.port = port
			if s.started {
				s.startLocked(key, node)
			}
		}
		return nil
	}
	if len(s.nodes) >= s.cfg.MaxNodes {
		s.evictLocked()
	}
	node := &nodeState{
		ring:     newRing(s.cfg.Capacity),
		port:     port,
		lastUsed: s.touchLocked(),
	}
	s.nodes[key] = node
	if s.started {
		s.startLocked(key, node)
	}
	return nil
}

// Unwatch stops the probe goroutine for key and waits for it to return. The
// retained window is kept so the console can still render the last hour; the
// LRU bound reclaims it. Unwatch of an unknown or already unwatched key is a
// no-op.
func (s *Sampler) Unwatch(key string) {
	s.mu.Lock()
	node, ok := s.nodes[key]
	if !ok {
		s.mu.Unlock()
		return
	}
	cancel, done := node.cancel, node.done
	node.cancel, node.done = nil, nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}

// Stats returns the aggregate for key. ok is false when the key was never
// watched or was evicted; Evicted tells the two apart. A watched node with no
// sample yet reports StateOK and Count 0.
func (s *Sampler) Stats(key string) (Stats, bool) {
	s.mu.Lock()
	node, ok := s.nodes[key]
	if !ok {
		s.mu.Unlock()
		return Stats{}, false
	}
	samples := node.ring.samples()
	s.mu.Unlock()
	return aggregate(key, samples), true
}

// touchLocked advances the LRU stamp. Callers hold s.mu.
func (s *Sampler) touchLocked() uint64 {
	s.seq++
	return s.seq
}

// startLocked launches the probe goroutine for one node. Callers hold s.mu
// and have checked started and node.cancel == nil.
func (s *Sampler) startLocked(key string, node *nodeState) {
	ctx, cancel := context.WithCancel(s.ctx)
	done := make(chan struct{})
	port := node.port
	node.cancel = cancel
	node.done = done
	s.wg.Add(1)
	go s.loop(ctx, key, node, port, done)
}

// evictLocked drops the least recently used node ring, preferring an
// unwatched one. A watched victim is cancelled here; its goroutine observes
// the cancellation and exits without waiting (Stop joins it). A watched
// victim is also recorded for Evicted: its gate may still be running, so the
// caller can re-arm the probe. Callers hold s.mu.
func (s *Sampler) evictLocked() {
	var victimKey string
	var victim *nodeState
	for key, node := range s.nodes {
		if node.cancel != nil {
			continue
		}
		if victim == nil || node.lastUsed < victim.lastUsed {
			victimKey, victim = key, node
		}
	}
	if victim == nil {
		for key, node := range s.nodes {
			if victim == nil || node.lastUsed < victim.lastUsed {
				victimKey, victim = key, node
			}
		}
	}
	if victim == nil {
		return
	}
	delete(s.nodes, victimKey)
	if victim.cancel != nil {
		victim.cancel()
		// Only a watched victim lost a live probe; reclaiming an unwatched
		// ring is the intended outcome and nothing needs healing.
		s.markEvictedLocked(victimKey)
	}
}

// markEvictedLocked records key as evicted and prunes the oldest records past
// evictedLimit. A record for a key that was watched again in the meantime is
// already gone and must not be resurrected, hence the stamp comparison.
// Callers hold s.mu.
func (s *Sampler) markEvictedLocked(key string) {
	s.evictedSeq++
	s.evicted[key] = s.evictedSeq
	s.evictedOrder = append(s.evictedOrder, evictionRecord{key: key, stamp: s.evictedSeq})
	for len(s.evictedOrder) > s.evictedLimit {
		oldest := s.evictedOrder[0]
		s.evictedOrder = s.evictedOrder[1:]
		if s.evicted[oldest.key] == oldest.stamp {
			delete(s.evicted, oldest.key)
		}
	}
}

// Evicted reports whether key's watched ring was reclaimed by the LRU cap and
// the key has not been watched since. The daemon re-arms such a key: the gate
// is still running, only its retained window is gone. The record is bounded,
// so a very old eviction may have been forgotten and report false.
func (s *Sampler) Evicted(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.evicted[key]
	return ok
}

// loop probes immediately, then once per interval, until ctx is cancelled.
// port is captured when the goroutine starts so a re-arm cannot change the
// target under a running probe.
func (s *Sampler) loop(ctx context.Context, key string, node *nodeState, port int, done chan struct{}) {
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

// probe runs one connect+close sample. It takes a concurrency slot, dials
// with the probe timeout and records the result unless the sampler itself is
// shutting down (a cancelled probe is not a health signal).
func (s *Sampler) probe(ctx context.Context, key string, node *nodeState, port int) {
	select {
	case s.sem <- struct{}{}:
	case <-ctx.Done():
		return
	}
	defer func() { <-s.sem }()

	at := s.cfg.Now()
	dialCtx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	conn, err := s.cfg.Dial(dialCtx, "tcp", net.JoinHostPort(loopbackHost, strconv.Itoa(port)))
	if err != nil {
		cancel()
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			return
		}
		s.record(key, node, Sample{
			At:         at,
			LatencyMs:  milliseconds(s.cfg.Now().Sub(at)),
			ErrorClass: classifyError(err),
		})
		return
	}
	// Connect+close only: the gate sees an accepted TCP connection and EOF,
	// never a proxy handshake or a credential.
	_ = conn.Close()
	cancel()
	s.record(key, node, Sample{
		At:        at,
		LatencyMs: milliseconds(s.cfg.Now().Sub(at)),
		OK:        true,
	})
}

// record appends a sample only if node is still the live entry for key, so a
// probe that outlives an eviction cannot write into a replacement ring.
func (s *Sampler) record(key string, node *nodeState, sample Sample) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nodes[key] != node {
		return
	}
	node.ring.append(sample)
	node.lastUsed = s.touchLocked()
}

// classifyError maps a dial error onto the four console classes. A timeout is
// reported by the context deadline or by net.Error.Timeout; refused and
// unreachable come from the platform socket error.
func classifyError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return ErrorTimeout
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return ErrorTimeout
	}
	if class := platformClass(err); class != "" {
		return class
	}
	return ErrorOther
}

// milliseconds converts a duration to fractional milliseconds.
func milliseconds(d time.Duration) float64 {
	return float64(d) / float64(time.Millisecond)
}
