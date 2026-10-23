// Package supervisor owns the per-node child processes of ajs-accountd.
//
// One child process serves one (account, server) pair (docs/PANEL.md §2). The
// supervisor starts a binary from an argument array (never through a shell),
// waits for its TCP readiness address, emits lifecycle events, forwards
// signals, and stops the child with a grace period before killing it.
//
// The supervisor never holds credentials. A child receives only an explicit
// allowlist of process essentials (PATH, HOME, SystemRoot, TMPDIR, TEMP, TMP)
// plus Spec.Env; the daemon's own environment is never inherited, so
// AJS_MASTER_KEY, AJS_ADMIN_TOKEN and AJS_RESIN_ADMIN_TOKEN cannot reach a node
// process. Environment values are passed verbatim but only their names are
// logged.
package supervisor

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Defaults applied by Start when the Spec leaves a field zero.
const (
	DefaultReadyTimeout = 10 * time.Second
	DefaultStopGrace    = 5 * time.Second
	DefaultStderrLines  = 100

	readyPollInterval = 50 * time.Millisecond
	readyDialTimeout  = 200 * time.Millisecond
	eventBuffer       = 64
)

var (
	// ErrNotFound is returned by Stop and Signal for a node that is not
	// running.
	ErrNotFound = errors.New("supervisor: node not found")
	// ErrDuplicate is returned by Start when the node id is already running
	// or being started.
	ErrDuplicate = errors.New("supervisor: node already running")
	// ErrClosed is returned by Start and Signal after Close.
	ErrClosed = errors.New("supervisor: supervisor closed")
	// ErrInvalidSpec is returned by Start for a Spec that cannot be executed.
	ErrInvalidSpec = errors.New("supervisor: invalid spec")
	// ErrNotReady is wrapped by ReadyError when readiness times out.
	ErrNotReady = errors.New("supervisor: node did not become ready")
)

// EventKind is the lifecycle event type.
type EventKind string

const (
	KindStarted EventKind = "started"
	KindReady   EventKind = "ready"
	KindExited  EventKind = "exited"
	KindStderr  EventKind = "stderr"
)

// Spec is one node process. Binary, Args and Env are passed to exec directly:
// there is no shell, so no expansion or quoting applies.
type Spec struct {
	// NodeID identifies the process, conventionally "<account>:<server>".
	NodeID    string
	AccountID string
	// Binary is the executable path. It is required.
	Binary string
	// Args are the child arguments, one element per argument.
	Args []string
	// Env entries are added to the child environment as "KEY=VALUE". The
	// child does not inherit the daemon environment; only PATH, HOME,
	// SystemRoot, TMPDIR, TEMP and TMP are passed through, and a Spec.Env
	// entry overrides an allowlisted name. Values are never logged.
	Env []string
	// ReadyAddr is the "host:port" dialed until the node accepts TCP.
	ReadyAddr string
	// ReadyTimeout bounds the readiness wait. Default DefaultReadyTimeout.
	ReadyTimeout time.Duration
	// StopGrace bounds Stop's wait between the interrupt and the kill.
	// Default DefaultStopGrace.
	StopGrace time.Duration
	// StderrLines is the stderr ring capacity. Default DefaultStderrLines.
	StderrLines int
}

func (s Spec) withDefaults() Spec {
	if s.ReadyTimeout <= 0 {
		s.ReadyTimeout = DefaultReadyTimeout
	}
	if s.StopGrace <= 0 {
		s.StopGrace = DefaultStopGrace
	}
	if s.StderrLines <= 0 {
		s.StderrLines = DefaultStderrLines
	}
	return s
}

func (s Spec) validate() error {
	switch {
	case strings.TrimSpace(s.NodeID) == "":
		return fmt.Errorf("%w: node id is required", ErrInvalidSpec)
	case strings.TrimSpace(s.Binary) == "":
		return fmt.Errorf("%w: binary is required", ErrInvalidSpec)
	case strings.TrimSpace(s.ReadyAddr) == "":
		return fmt.Errorf("%w: ready address is required", ErrInvalidSpec)
	}
	if _, _, err := net.SplitHostPort(s.ReadyAddr); err != nil {
		return fmt.Errorf("%w: ready address %q: %v", ErrInvalidSpec, s.ReadyAddr, err)
	}
	return nil
}

// Event is one lifecycle change of one node. Stderr carries Line; the other
// kinds carry Err when they report a failure.
type Event struct {
	NodeID string
	Kind   EventKind
	At     time.Time
	Err    error
	Line   string
}

// ReadyError is returned by Start when the child starts but never accepts a
// TCP connection on ReadyAddr. It wraps ErrNotReady and the last dial error.
type ReadyError struct {
	NodeID  string
	Addr    string
	Timeout time.Duration
	Err     error
}

func (e *ReadyError) Error() string {
	msg := fmt.Sprintf("supervisor: node %s not ready at %s after %s", e.NodeID, e.Addr, e.Timeout)
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *ReadyError) Unwrap() []error {
	if e.Err == nil {
		return []error{ErrNotReady}
	}
	return []error{ErrNotReady, e.Err}
}

// node is the mutable process state shared by the handle and the monitor.
type node struct {
	spec      Spec
	cmd       *exec.Cmd
	done      chan struct{}
	stderrEnd chan struct{}

	mu      sync.Mutex
	exitErr error
	ring    []string
}

// Handle is the public, copyable view of one node process.
type Handle struct {
	NodeID    string
	PID       int
	StartedAt time.Time

	node *node
}

// Wait blocks until the process exits and returns its exit error (nil on a
// zero exit).
func (h *Handle) Wait() error {
	if h == nil || h.node == nil {
		return ErrNotFound
	}
	<-h.node.done
	h.node.mu.Lock()
	defer h.node.mu.Unlock()
	return h.node.exitErr
}

// Done is closed when the process exits.
func (h *Handle) Done() <-chan struct{} {
	if h == nil || h.node == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return h.node.done
}

// Stderr returns a copy of the retained stderr ring, oldest line first.
func (h *Handle) Stderr() []string {
	if h == nil || h.node == nil {
		return nil
	}
	h.node.mu.Lock()
	defer h.node.mu.Unlock()
	return append([]string(nil), h.node.ring...)
}

// Supervisor owns every child process. It is safe for concurrent use.
type Supervisor struct {
	logger *slog.Logger
	events chan Event
	now    func() time.Time

	mu       sync.Mutex
	nodes    map[string]*Handle
	reserved map[string]struct{}
	exited   map[string]struct{}
	closed   bool
}

// New returns a supervisor that is not running anything. A nil logger uses
// slog.Default().
func New(logger *slog.Logger) *Supervisor {
	if logger == nil {
		logger = slog.Default()
	}
	return &Supervisor{
		logger:   logger,
		events:   make(chan Event, eventBuffer),
		now:      time.Now,
		nodes:    make(map[string]*Handle),
		reserved: make(map[string]struct{}),
		exited:   make(map[string]struct{}),
	}
}

// Events returns the lifecycle event stream. It is buffered; a consumer that
// falls more than the buffer behind loses events (a warning is logged) rather
// than blocking a child monitor.
func (s *Supervisor) Events() <-chan Event {
	return s.events
}

// Start launches one node, waits for readiness and returns its handle.
//
// The child runs until it exits, Stop is called, ctx is cancelled, or the
// supervisor is closed. Start rejects a duplicate NodeID before touching the
// filesystem. A readiness failure kills the child and returns *ReadyError.
func (s *Supervisor) Start(ctx context.Context, spec Spec) (*Handle, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	spec = spec.withDefaults()
	if err := spec.validate(); err != nil {
		return nil, err
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, ErrClosed
	}
	if _, ok := s.nodes[spec.NodeID]; ok {
		s.mu.Unlock()
		return nil, fmt.Errorf("%w: %s", ErrDuplicate, spec.NodeID)
	}
	if _, ok := s.reserved[spec.NodeID]; ok {
		s.mu.Unlock()
		return nil, fmt.Errorf("%w: %s", ErrDuplicate, spec.NodeID)
	}
	s.reserved[spec.NodeID] = struct{}{}
	s.mu.Unlock()

	cmd := exec.CommandContext(ctx, spec.Binary, spec.Args...)
	cmd.Env = childEnv(spec.Env)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		s.release(spec.NodeID)
		return nil, fmt.Errorf("supervisor: stderr pipe for %s: %w", spec.NodeID, err)
	}

	startedAt := s.now()
	if err := cmd.Start(); err != nil {
		s.release(spec.NodeID)
		return nil, fmt.Errorf("supervisor: start %s (%s): %w", spec.NodeID, spec.Binary, err)
	}

	n := &node{
		spec:      spec,
		cmd:       cmd,
		done:      make(chan struct{}),
		stderrEnd: make(chan struct{}),
		ring:      make([]string, 0, spec.StderrLines),
	}
	h := &Handle{NodeID: spec.NodeID, PID: cmd.Process.Pid, StartedAt: startedAt, node: n}

	s.mu.Lock()
	delete(s.reserved, spec.NodeID)
	if s.closed {
		s.mu.Unlock()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, ErrClosed
	}
	s.nodes[spec.NodeID] = h
	s.mu.Unlock()

	go s.captureStderr(n, stderr)
	go s.monitor(n)

	s.logger.Info("supervisor node starting",
		"node_id", spec.NodeID,
		"account_id", spec.AccountID,
		"pid", h.PID,
		"binary", spec.Binary,
		"args", len(spec.Args),
		"env", envKeys(spec.Env),
		"ready_addr", spec.ReadyAddr,
	)
	s.emit(Event{NodeID: spec.NodeID, Kind: KindStarted, At: s.now()})

	if err := s.awaitReady(ctx, h); err != nil {
		s.killAndWait(h)
		return nil, err
	}

	s.logger.Info("supervisor node ready", "node_id", spec.NodeID, "pid", h.PID, "ready_addr", spec.ReadyAddr)
	s.emit(Event{NodeID: spec.NodeID, Kind: KindReady, At: s.now()})
	return h, nil
}

// awaitReady polls ReadyAddr until it accepts a TCP connection, the child
// exits, ctx ends or ReadyTimeout expires.
func (s *Supervisor) awaitReady(ctx context.Context, h *Handle) error {
	deadline := time.NewTimer(h.node.spec.ReadyTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(readyPollInterval)
	defer ticker.Stop()

	var lastErr error
	for {
		conn, err := net.DialTimeout("tcp", h.node.spec.ReadyAddr, readyDialTimeout)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		lastErr = err

		select {
		case <-h.node.done:
			h.node.mu.Lock()
			exitErr := h.node.exitErr
			h.node.mu.Unlock()
			if exitErr == nil {
				exitErr = errors.New("process exited before readiness")
			}
			return fmt.Errorf("supervisor: node %s exited before ready: %w", h.NodeID, exitErr)
		case <-deadline.C:
			return &ReadyError{
				NodeID:  h.NodeID,
				Addr:    h.node.spec.ReadyAddr,
				Timeout: h.node.spec.ReadyTimeout,
				Err:     lastErr,
			}
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// killAndWait kills a child that failed to start correctly and waits briefly
// for the monitor to reap it.
func (s *Supervisor) killAndWait(h *Handle) {
	_ = h.node.cmd.Process.Kill()
	select {
	case <-h.node.done:
	case <-time.After(2 * time.Second):
		s.logger.Warn("supervisor node did not exit after kill", "node_id", h.NodeID, "pid", h.PID)
	}
	s.mu.Lock()
	if cur, ok := s.nodes[h.NodeID]; ok && cur == h {
		delete(s.nodes, h.NodeID)
	}
	s.mu.Unlock()
}

// captureStderr scans the child stderr, keeps the last StderrLines lines and
// emits one stderr event per line.
func (s *Supervisor) captureStderr(n *node, r io.Reader) {
	defer close(n.stderrEnd)
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 4096), 64*1024)
	for scanner.Scan() {
		line := scanner.Text()
		n.mu.Lock()
		if len(n.ring) == n.spec.StderrLines {
			copy(n.ring, n.ring[1:])
			n.ring[len(n.ring)-1] = line
		} else {
			n.ring = append(n.ring, line)
		}
		n.mu.Unlock()
		s.emit(Event{NodeID: n.spec.NodeID, Kind: KindStderr, At: s.now(), Line: line})
	}
	if err := scanner.Err(); err != nil {
		s.logger.Warn("supervisor stderr read failed", "node_id", n.spec.NodeID, "error", err)
	}
}

// monitor reaps the child, publishes the exit event and removes it from the
// running set.
func (s *Supervisor) monitor(n *node) {
	err := n.cmd.Wait()
	<-n.stderrEnd
	n.mu.Lock()
	n.exitErr = err
	n.mu.Unlock()

	s.mu.Lock()
	if cur, ok := s.nodes[n.spec.NodeID]; ok && cur.node == n {
		delete(s.nodes, n.spec.NodeID)
		s.exited[n.spec.NodeID] = struct{}{}
	}
	s.mu.Unlock()

	s.emit(Event{NodeID: n.spec.NodeID, Kind: KindExited, At: s.now(), Err: err})
	close(n.done)

	if err != nil {
		s.logger.Warn("supervisor node exited", "node_id", n.spec.NodeID, "pid", n.cmd.Process.Pid, "error", err)
		return
	}
	s.logger.Info("supervisor node exited", "node_id", n.spec.NodeID, "pid", n.cmd.Process.Pid)
}

// Stop interrupts the node, waits up to grace for it to exit, then kills it.
// It is idempotent: stopping an already exited node that this supervisor
// started returns nil. An unknown node is ErrNotFound.
func (s *Supervisor) Stop(ctx context.Context, nodeID string, grace time.Duration) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	h, running := s.nodes[nodeID]
	_, alreadyExited := s.exited[nodeID]
	s.mu.Unlock()
	if !running {
		if alreadyExited {
			// Idempotent: the node this supervisor started is already gone.
			return nil
		}
		return fmt.Errorf("%w: %s", ErrNotFound, nodeID)
	}
	if grace <= 0 {
		grace = h.node.spec.StopGrace
	}
	return s.stopHandle(ctx, h, grace)
}

// stopHandle performs the interrupt/grace/kill sequence for one running node.
func (s *Supervisor) stopHandle(ctx context.Context, h *Handle, grace time.Duration) error {
	if err := h.node.cmd.Process.Signal(os.Interrupt); err != nil {
		// Platforms without a catchable interrupt (Windows) go straight to
		// the kill path.
		_ = h.node.cmd.Process.Kill()
	}

	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-h.node.done:
		return nil
	case <-ctx.Done():
		_ = h.node.cmd.Process.Kill()
		return ctx.Err()
	case <-timer.C:
	}

	s.logger.Warn("supervisor node stop grace elapsed; killing", "node_id", h.NodeID, "pid", h.PID, "grace", grace)
	_ = h.node.cmd.Process.Kill()
	select {
	case <-h.node.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Signal sends sig to a running node.
func (s *Supervisor) Signal(nodeID string, sig os.Signal) error {
	h, err := s.lookup(nodeID)
	if err != nil {
		return err
	}
	if err := h.node.cmd.Process.Signal(sig); err != nil {
		return fmt.Errorf("supervisor: signal %s: %w", nodeID, err)
	}
	return nil
}

// Get returns the running handle for nodeID.
func (s *Supervisor) Get(nodeID string) (*Handle, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.nodes[nodeID]
	return h, ok
}

// List returns a snapshot of the running nodes.
func (s *Supervisor) List() []Handle {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Handle, 0, len(s.nodes))
	for _, h := range s.nodes {
		out = append(out, *h)
	}
	return out
}

// Close stops every running node, each with its own StopGrace, and marks the
// supervisor closed. It is idempotent. The events channel stays open so late
// readers never panic; no events are emitted after Close returns.
func (s *Supervisor) Close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	handles := make([]*Handle, 0, len(s.nodes))
	for _, h := range s.nodes {
		handles = append(handles, h)
	}
	s.mu.Unlock()

	var wg sync.WaitGroup
	errs := make([]error, len(handles))
	for i, h := range handles {
		wg.Add(1)
		go func(i int, h *Handle) {
			defer wg.Done()
			errs[i] = s.stopHandle(ctx, h, h.node.spec.StopGrace)
		}(i, h)
	}
	wg.Wait()
	return errors.Join(errs...)
}

func (s *Supervisor) lookup(nodeID string) (*Handle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	h, ok := s.nodes[nodeID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, nodeID)
	}
	return h, nil
}

func (s *Supervisor) release(nodeID string) {
	s.mu.Lock()
	delete(s.reserved, nodeID)
	s.mu.Unlock()
}

func (s *Supervisor) emit(ev Event) {
	select {
	case s.events <- ev:
	default:
		s.logger.Warn("supervisor event buffer full; dropping event", "node_id", ev.NodeID, "kind", ev.Kind)
	}
}

// childEnv builds the environment of one node process: an explicit allowlist
// of process essentials plus extra. The daemon environment is deliberately not
// inherited, so credentials such as AJS_MASTER_KEY never reach a child. An
// extra entry wins over an allowlisted name.
func childEnv(extra []string) []string {
	allowlist := [...]string{"PATH", "HOME", "SystemRoot", "TMPDIR", "TEMP", "TMP"}
	env := make([]string, 0, len(allowlist)+len(extra))
	for _, key := range allowlist {
		value, ok := os.LookupEnv(key)
		if !ok || envHasKey(extra, key) {
			continue
		}
		env = append(env, key+"="+value)
	}
	return append(env, extra...)
}

// envHasKey reports whether env defines key. Names are compared
// case-insensitively because Windows treats them that way.
func envHasKey(env []string, key string) bool {
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if strings.EqualFold(name, key) {
			return true
		}
	}
	return false
}

// envKeys renders the environment variable names for logs, never the values.
func envKeys(env []string) []string {
	if len(env) == 0 {
		return nil
	}
	keys := make([]string, 0, len(env))
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		keys = append(keys, key)
	}
	return keys
}
