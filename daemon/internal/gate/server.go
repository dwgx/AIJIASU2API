// Package gate implements the per-node SOCKS5 front end that Resin dials.
//
// One Server owns one loopback listener for one node (docs/PANEL.md §1–§2).
// It accepts only the no-auth SOCKS5 CONNECT command, admits at most Limit
// concurrent flows, relays bytes between the client and one Session flow, and
// exposes counters plus a drain/undrain state machine for the node supervisor.
//
// The Session seam is deliberately one method (OpenFlow), so an
// account.FlowSession or a test fake satisfies it structurally. The gate never
// opens a vendor control connection, never holds credentials and never logs
// the target beyond host:port.
package gate

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// SOCKS5 constants (RFC 1928). Only the no-auth CONNECT method is served.
const (
	socks5Version = 0x05

	authNone         = 0x00
	authNoAcceptable = 0xFF

	cmdConnect = 0x01

	atypIPv4   = 0x01
	atypDomain = 0x03
	atypIPv6   = 0x04

	repSucceeded          = 0x00
	repGeneralFailure     = 0x01
	repConnectionRefused  = 0x05
	repTTLExpired         = 0x06
	repCommandUnsupported = 0x07
	repAddrUnsupported    = 0x08
)

const (
	defaultListen           = "127.0.0.1:0"
	defaultLimit            = 32
	defaultAdmissionTimeout = 30 * time.Second
	defaultDrainGrace       = 30 * time.Second

	// handshakeTimeout bounds the greeting and the CONNECT request. It is
	// independent of AdmissionTimeout, which bounds the semaphore wait.
	handshakeTimeout = 30 * time.Second

	// acceptBackoff is the pause after a transient Accept error before the
	// loop retries, so a persistent failure cannot spin a core.
	acceptBackoff = 50 * time.Millisecond
)

var (
	// ErrClosed is returned by Drain, Undrain and Start after Close.
	ErrClosed = errors.New("gate: server is closed")
	// ErrAlreadyStarted is returned by a second Start.
	ErrAlreadyStarted = errors.New("gate: server already started")

	errAdmissionTimeout = errors.New("gate: admission timeout")
	errDraining         = errors.New("gate: node is draining")
)

// Config is the per-node gate configuration. Zero values select the defaults.
type Config struct {
	// NodeID and AccountID label State and log lines. They are not used for
	// routing.
	NodeID    string
	AccountID string
	// Listen is the TCP listen address. Default 127.0.0.1:0 (ephemeral
	// loopback port); the operator may widen it explicitly.
	Listen string
	// Limit is the maximum number of concurrently admitted flows. Default 32.
	Limit int
	// AdmissionTimeout bounds how long a new flow waits for a slot. Default
	// 30s; expiry answers SOCKS5 reply 0x06.
	AdmissionTimeout time.Duration
	// DrainGrace bounds how long Drain waits for active flows. Default 30s.
	// Close uses the same grace before it force-closes connections.
	DrainGrace time.Duration
	// Now is the clock used for StartedAt and flow durations. Default
	// time.Now.
	Now func() time.Time
	// Logger receives one line per accepted flow plus rejection lines. Default
	// slog.Default().
	Logger *slog.Logger
}

// Session opens one proxied TCP flow. *account.FlowSession satisfies it: the
// gate only needs OpenFlow, and the account interface is a superset.
type Session interface {
	OpenFlow(ctx context.Context, host string, port int) (net.Conn, error)
}

// State is the point-in-time snapshot behind GateState. It carries no
// credentials and no target addresses.
type State struct {
	NodeID    string `json:"nodeId"`
	AccountID string `json:"accountId"`
	// Active is the number of admitted flows, including flows still opening.
	Active int `json:"active"`
	Limit  int `json:"limit"`
	// Draining is true between Drain and Undrain (or Close).
	Draining bool `json:"draining"`
	// Accepted counts flows that received a success reply and entered relay.
	Accepted uint64 `json:"accepted"`
	// Rejected counts flows refused before relay: draining, admission
	// timeout, malformed handshake, or a failed OpenFlow.
	Rejected uint64 `json:"rejected"`
	// IngressBytes is client -> target payload; EgressBytes is target ->
	// client. They exclude the SOCKS5 handshake.
	IngressBytes uint64    `json:"ingressBytes"`
	EgressBytes  uint64    `json:"egressBytes"`
	StartedAt    time.Time `json:"startedAt"`
}

// flow is one admitted client connection. client and remote are guarded by
// Server.mu because Close force-closes them from another goroutine.
type flow struct {
	client net.Conn
	remote net.Conn
	cancel context.CancelFunc
}

// Server is one node's SOCKS5 gate. It is safe for concurrent use.
type Server struct {
	cfg     Config
	session Session
	logger  *slog.Logger

	slots chan struct{}

	mu        sync.Mutex
	ln        net.Listener
	started   bool
	startedAt time.Time
	active    int
	draining  bool
	closed    bool
	drainCh   chan struct{}
	zeroCh    chan struct{}
	flows     map[*flow]struct{}
	clients   map[net.Conn]struct{}
	serveDone chan struct{}
	handlers  sync.WaitGroup

	closeOnce sync.Once

	accepted atomic.Uint64
	rejected atomic.Uint64
	ingress  atomic.Uint64
	egress   atomic.Uint64
}

// NewServer validates the configuration, applies defaults and returns a
// server that is not listening yet. A nil session is a programming error.
func NewServer(cfg Config, session Session) (*Server, error) {
	if session == nil {
		return nil, errors.New("gate: session is required")
	}
	if cfg.Listen == "" {
		cfg.Listen = defaultListen
	}
	if cfg.Limit <= 0 {
		cfg.Limit = defaultLimit
	}
	if cfg.AdmissionTimeout <= 0 {
		cfg.AdmissionTimeout = defaultAdmissionTimeout
	}
	if cfg.DrainGrace <= 0 {
		cfg.DrainGrace = defaultDrainGrace
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Server{
		cfg:       cfg,
		session:   session,
		logger:    cfg.Logger,
		slots:     make(chan struct{}, cfg.Limit),
		drainCh:   make(chan struct{}),
		zeroCh:    make(chan struct{}),
		flows:     make(map[*flow]struct{}),
		clients:   make(map[net.Conn]struct{}),
		serveDone: make(chan struct{}),
	}, nil
}

// Start binds the listener and serves in the background. Cancelling ctx is
// equivalent to Close. Start returns after the listener is bound, so Addr is
// valid on return.
func (s *Server) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	if s.started {
		s.mu.Unlock()
		return ErrAlreadyStarted
	}
	ln, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("gate: listen %s: %w", s.cfg.Listen, err)
	}
	s.ln = ln
	s.started = true
	s.startedAt = s.cfg.Now()
	s.mu.Unlock()

	go s.serve(ln)
	go s.watchContext(ctx)
	s.logger.Info("gate listening",
		"node_id", s.cfg.NodeID,
		"account_id", s.cfg.AccountID,
		"addr", ln.Addr().String(),
		"limit", s.cfg.Limit,
	)
	return nil
}

// Addr returns the bound listener address, or nil before Start.
func (s *Server) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln == nil {
		return nil
	}
	return s.ln.Addr()
}

// State returns a consistent snapshot of the counters and the drain flag.
func (s *Server) State() State {
	s.mu.Lock()
	st := State{
		NodeID:    s.cfg.NodeID,
		AccountID: s.cfg.AccountID,
		Active:    s.active,
		Limit:     s.cfg.Limit,
		Draining:  s.draining,
		StartedAt: s.startedAt,
	}
	s.mu.Unlock()
	st.Accepted = s.accepted.Load()
	st.Rejected = s.rejected.Load()
	st.IngressBytes = s.ingress.Load()
	st.EgressBytes = s.egress.Load()
	return st
}

// Drain stops admitting new flows and waits for the active ones to finish.
// In-flight flows continue to relay. It returns when active reaches zero, when
// DrainGrace elapses (the remaining flows are left running), or when ctx ends;
// only a cancelled ctx is an error. Draining an already draining server is
// idempotent.
func (s *Server) Drain(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	if !s.draining {
		s.draining = true
		close(s.drainCh)
	}
	active := s.active
	zero := s.zeroCh
	s.mu.Unlock()

	if active == 0 {
		return nil
	}
	timer := time.NewTimer(s.cfg.DrainGrace)
	defer timer.Stop()
	select {
	case <-zero:
		return nil
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Undrain returns a draining server to service. It is idempotent and fails
// once the server is closed.
func (s *Server) Undrain() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	if s.draining {
		s.draining = false
		s.drainCh = make(chan struct{})
	}
	return nil
}

// Close stops the listener, marks the server draining and waits up to
// DrainGrace for active flows. Any connection still open after the grace is
// force-closed, including handshakes that never completed. Close is
// idempotent.
func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		if !s.draining {
			s.draining = true
			close(s.drainCh)
		}
		ln := s.ln
		active := s.active
		zero := s.zeroCh
		s.mu.Unlock()

		if ln != nil {
			_ = ln.Close()
		}

		if active > 0 {
			timer := time.NewTimer(s.cfg.DrainGrace)
			select {
			case <-zero:
			case <-timer.C:
			}
			timer.Stop()
		}

		s.mu.Lock()
		for c := range s.clients {
			_ = c.Close()
		}
		for f := range s.flows {
			f.cancel()
			if f.remote != nil {
				_ = f.remote.Close()
			}
		}
		s.mu.Unlock()

		s.logger.Info("gate closed", "node_id", s.cfg.NodeID)
	})
	return nil
}

// watchContext closes the server when the Start context is cancelled. It exits
// as soon as the accept loop stops.
func (s *Server) watchContext(ctx context.Context) {
	select {
	case <-ctx.Done():
		_ = s.Close()
	case <-s.serveDone:
	}
}

// serve accepts connections until the listener closes. A transient Accept
// error (EMFILE, ECONNABORTED, a reset backlog) must not end the loop: the
// listener stays bound and the port keeps completing handshakes, so returning
// would leave a dead gate that looks healthy to a readiness dial. Only a closed
// listener ends the loop.
func (s *Server) serve(ln net.Listener) {
	defer close(s.serveDone)
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			s.logger.Warn("gate accept failed", "node_id", s.cfg.NodeID, "error", err)
			time.Sleep(acceptBackoff)
			continue
		}
		s.handlers.Add(1)
		go func() {
			defer s.handlers.Done()
			s.handle(conn)
		}()
	}
}

// handle serves one client connection: greeting, CONNECT, admission, OpenFlow
// and relay. Every exit path closes the client connection.
func (s *Server) handle(client net.Conn) {
	s.track(client)
	defer s.untrack(client)
	defer client.Close()

	// A connection queued in the accept backlog can be handed to a handler
	// after Close took its client snapshot; refuse it here.
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		s.rejected.Add(1)
		return
	}

	started := s.cfg.Now()
	_ = client.SetDeadline(time.Now().Add(handshakeTimeout))

	target, err := readRequest(client)
	if err != nil {
		s.rejected.Add(1)
		s.logger.Warn("gate handshake rejected", "node_id", s.cfg.NodeID, "error", err)
		return
	}
	_ = client.SetDeadline(time.Time{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := s.acquire(ctx); err != nil {
		s.rejected.Add(1)
		rep := byte(repConnectionRefused)
		if errors.Is(err, errAdmissionTimeout) {
			rep = repTTLExpired
		}
		_ = writeReply(client, rep)
		s.logger.Warn("gate flow rejected",
			"node_id", s.cfg.NodeID,
			"target", target.String(),
			"reason", err,
		)
		return
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { <-s.slots }) }
	defer release()

	f := &flow{client: client, cancel: cancel}
	s.mu.Lock()
	if s.closed || s.draining {
		s.mu.Unlock()
		s.rejected.Add(1)
		_ = writeReply(client, repConnectionRefused)
		return
	}
	s.flows[f] = struct{}{}
	s.beginFlowLocked()
	s.mu.Unlock()
	defer s.finishFlow(f)

	remote, err := s.session.OpenFlow(ctx, target.host, target.port)
	if err != nil {
		s.rejected.Add(1)
		_ = writeReply(client, repConnectionRefused)
		s.logger.Warn("gate open flow failed",
			"node_id", s.cfg.NodeID,
			"target", target.String(),
			"error", err,
		)
		return
	}
	s.attachRemote(f, remote)

	if err := writeReply(client, repSucceeded); err != nil {
		s.rejected.Add(1)
		s.logger.Warn("gate reply failed",
			"node_id", s.cfg.NodeID,
			"target", target.String(),
			"error", err,
		)
		return
	}
	s.accepted.Add(1)

	in, out := relay(client, remote, &s.ingress, &s.egress)
	s.logger.Info("gate flow closed",
		"node_id", s.cfg.NodeID,
		"target", target.String(),
		"ingress_bytes", in,
		"egress_bytes", out,
		"duration", s.cfg.Now().Sub(started),
	)
}

// acquire takes one admission slot. It refuses while draining and waits at
// most AdmissionTimeout (or ctx) for a slot.
func (s *Server) acquire(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	select {
	case <-s.drainCh:
		s.mu.Unlock()
		return errDraining
	default:
	}
	select {
	case s.slots <- struct{}{}:
		s.mu.Unlock()
		return nil
	default:
	}
	drainCh := s.drainCh
	s.mu.Unlock()

	timer := time.NewTimer(s.cfg.AdmissionTimeout)
	defer timer.Stop()
	select {
	case s.slots <- struct{}{}:
		s.mu.Lock()
		draining := s.draining
		s.mu.Unlock()
		if draining {
			<-s.slots
			return errDraining
		}
		return nil
	case <-drainCh:
		return errDraining
	case <-timer.C:
		return errAdmissionTimeout
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Server) track(c net.Conn) {
	s.mu.Lock()
	s.clients[c] = struct{}{}
	s.mu.Unlock()
}

func (s *Server) untrack(c net.Conn) {
	s.mu.Lock()
	delete(s.clients, c)
	s.mu.Unlock()
}

func (s *Server) beginFlowLocked() {
	if s.active == 0 {
		s.zeroCh = make(chan struct{})
	}
	s.active++
}

func (s *Server) attachRemote(f *flow, remote net.Conn) {
	s.mu.Lock()
	f.remote = remote
	s.mu.Unlock()
}

// finishFlow unregisters the flow, releases its Active slot and closes both
// sides. It is called exactly once per admitted flow.
func (s *Server) finishFlow(f *flow) {
	s.mu.Lock()
	remote := f.remote
	delete(s.flows, f)
	s.active--
	if s.active == 0 {
		close(s.zeroCh)
	}
	s.mu.Unlock()

	f.cancel()
	_ = f.client.Close()
	if remote != nil {
		_ = remote.Close()
	}
}

// target is one parsed CONNECT destination.
type target struct {
	host string
	port int
}

func (t target) String() string {
	return net.JoinHostPort(t.host, strconv.Itoa(t.port))
}

// readRequest performs the greeting and the CONNECT request. It writes every
// reply the protocol defines for the failure it finds: 0xFF for no acceptable
// method, 0x07 for a non-CONNECT command, 0x08 for an unknown address type and
// 0x01 for a malformed request. A wrong version or truncated frame closes
// without a reply.
func readRequest(conn net.Conn) (target, error) {
	var greeting [2]byte
	if _, err := io.ReadFull(conn, greeting[:]); err != nil {
		return target{}, fmt.Errorf("read greeting: %w", err)
	}
	if greeting[0] != socks5Version {
		return target{}, fmt.Errorf("greeting version %d, want 5", greeting[0])
	}
	methods := make([]byte, int(greeting[1]))
	if _, err := io.ReadFull(conn, methods); err != nil {
		return target{}, fmt.Errorf("read methods: %w", err)
	}
	offersNone := false
	for _, m := range methods {
		if m == authNone {
			offersNone = true
			break
		}
	}
	if !offersNone {
		_ = writeMethodReply(conn, authNoAcceptable)
		return target{}, errors.New("no acceptable authentication method")
	}
	if err := writeMethodReply(conn, authNone); err != nil {
		return target{}, fmt.Errorf("write method reply: %w", err)
	}

	var req [4]byte
	if _, err := io.ReadFull(conn, req[:]); err != nil {
		return target{}, fmt.Errorf("read request: %w", err)
	}
	if req[0] != socks5Version {
		return target{}, fmt.Errorf("request version %d, want 5", req[0])
	}
	if req[1] != cmdConnect {
		_ = writeReply(conn, repCommandUnsupported)
		return target{}, fmt.Errorf("command %d is not CONNECT", req[1])
	}
	if req[2] != 0x00 {
		_ = writeReply(conn, repGeneralFailure)
		return target{}, fmt.Errorf("reserved byte %#x is not zero", req[2])
	}

	var t target
	switch req[3] {
	case atypIPv4:
		ip := make([]byte, net.IPv4len)
		if _, err := io.ReadFull(conn, ip); err != nil {
			return target{}, fmt.Errorf("read IPv4 address: %w", err)
		}
		t.host = net.IP(ip).String()
	case atypDomain:
		var length [1]byte
		if _, err := io.ReadFull(conn, length[:]); err != nil {
			return target{}, fmt.Errorf("read domain length: %w", err)
		}
		if length[0] == 0 {
			_ = writeReply(conn, repGeneralFailure)
			return target{}, errors.New("empty domain name")
		}
		name := make([]byte, int(length[0]))
		if _, err := io.ReadFull(conn, name); err != nil {
			return target{}, fmt.Errorf("read domain: %w", err)
		}
		t.host = string(name)
	case atypIPv6:
		ip := make([]byte, net.IPv6len)
		if _, err := io.ReadFull(conn, ip); err != nil {
			return target{}, fmt.Errorf("read IPv6 address: %w", err)
		}
		t.host = net.IP(ip).String()
	default:
		_ = writeReply(conn, repAddrUnsupported)
		return target{}, fmt.Errorf("address type %d is not supported", req[3])
	}

	var port [2]byte
	if _, err := io.ReadFull(conn, port[:]); err != nil {
		return target{}, fmt.Errorf("read port: %w", err)
	}
	t.port = int(binary.BigEndian.Uint16(port[:]))
	return t, nil
}

func writeMethodReply(w io.Writer, method byte) error {
	_, err := w.Write([]byte{socks5Version, method})
	return err
}

func writeReply(w io.Writer, rep byte) error {
	_, err := w.Write([]byte{socks5Version, rep, 0x00, atypIPv4, 0, 0, 0, 0, 0, 0})
	return err
}

// relay copies bytes in both directions until both copies end, then closes
// both connections. ingress counts client -> target, egress target -> client.
func relay(a, b net.Conn, ingress, egress *atomic.Uint64) (in, out int64) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		in = copyFlow(b, a, ingress)
	}()
	go func() {
		defer wg.Done()
		out = copyFlow(a, b, egress)
	}()
	wg.Wait()
	_ = a.Close()
	_ = b.Close()
	return in, out
}

// copyFlow copies one direction and half-closes the destination. A connection
// without CloseWrite (net.Pipe, for example) is closed instead, matching the
// emulator's relay behaviour.
func copyFlow(dst, src net.Conn, counter *atomic.Uint64) int64 {
	n, _ := io.Copy(countingWriter{w: dst, n: counter}, src)
	if cw, ok := dst.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	} else {
		_ = dst.Close()
	}
	return n
}

// countingWriter adds every successful write to counter. Counters are updated
// live, so State is exact while a flow is still running.
type countingWriter struct {
	w io.Writer
	n *atomic.Uint64
}

func (c countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	if n > 0 {
		c.n.Add(uint64(n))
	}
	return n, err
}
