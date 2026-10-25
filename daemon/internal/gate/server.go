// Package gate implements the per-node proxy front end that Resin dials.
//
// One Server owns one node's loopback listeners: the SOCKS5 gate and an
// optional HTTP CONNECT listener that share one admission pipeline (per-user
// policy, account quota, one slot limit), one set of byte counters, one drain
// state machine and one bounded flow ring. It accepts the SOCKS5 CONNECT
// command after negotiating either the no-auth method (AuthLoopback) or RFC
// 1929 username/password (AuthUserPass), or the HTTP CONNECT method with Basic
// proxy credentials, admits at most Limit concurrent flows across both, relays
// bytes between the client and one Session flow, and exposes counters plus a
// drain/undrain state machine for the node supervisor.
//
// The Session seam is deliberately one method (OpenFlow), so an
// account.FlowSession or a test fake satisfies it structurally. The gate never
// opens a vendor control connection, never keeps a credential past the
// handshake and never logs the target beyond host:port. Authentication only
// attributes a local admission; it never selects or creates a vendor session.
package gate

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// SOCKS5 constants (RFC 1928) plus the RFC 1929 username/password
// subnegotiation. A gate serves exactly one method per AuthMode.
const (
	socks5Version = 0x05

	authNone         = 0x00
	authUserPass     = 0x02
	authNoAcceptable = 0xFF

	// RFC 1929: the client sends version 1, a length-prefixed username and a
	// length-prefixed password; the server answers version 1 plus a status.
	authVersion      = 0x01
	authStatusOK     = 0x00
	authStatusFailed = 0x01

	cmdConnect = 0x01

	atypIPv4   = 0x01
	atypDomain = 0x03
	atypIPv6   = 0x04

	repSucceeded          = 0x00
	repGeneralFailure     = 0x01
	repNotAllowed         = 0x02
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

	// defaultAuthTimeout bounds the RFC 1929 subnegotiation after the method
	// reply, so a client that stops mid-frame cannot pin a handler for the
	// whole handshake budget.
	defaultAuthTimeout = 3 * time.Second
)

var (
	// ErrClosed is returned by Drain, Undrain and Start after Close.
	ErrClosed = errors.New("gate: server is closed")
	// ErrAlreadyStarted is returned by a second Start.
	ErrAlreadyStarted = errors.New("gate: server already started")

	errAdmissionTimeout = errors.New("gate: admission timeout")
	errDraining         = errors.New("gate: node is draining")
	errQuotaStopped     = errors.New("gate: flow quota exceeded")
)

// ProgressChunk is the in-flight accounting granularity. The relay reports
// bytes to Config.MeterProgress once per chunk per direction, so a flow can
// overshoot a monthly budget by less than one chunk before the meter refuses
// it. Smaller chunks bound the overrun more tightly but cost one store update
// per chunk.
const ProgressChunk = 256 << 10

// FlowErrorUserFlowLimit is the refusal class for the per-user concurrent-flow
// cap. It is a transient capacity refusal, not a byte quota: the HTTP listener
// answers 429 with a one-second retry hint rather than the monthly reset.
const FlowErrorUserFlowLimit = "user_flow_limit"

// Config is the per-node gate configuration. Zero values select the defaults.
type Config struct {
	// NodeID and AccountID label State and log lines. They are not used for
	// routing.
	NodeID    string
	AccountID string
	// Slot and Generation attribute every flow record to the account slot and
	// the session generation that served it. Zero is the legacy standalone
	// gate; the runtime always sets both.
	Slot       int
	Generation uint64
	// Listen is the TCP listen address. Default 127.0.0.1:0 (ephemeral
	// loopback port); the operator may widen it explicitly.
	Listen string
	// HTTPListen is the optional HTTP CONNECT listener. Empty disables it.
	// The runtime derives it from the SOCKS5 gate port plus
	// AJS_GATE_HTTP_OFFSET; both listeners share admission, counters and the
	// flow ring. Like Listen it must name a literal loopback address.
	HTTPListen string
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
	// OnFlow observes every finished flow record, accepted or rejected, after
	// it enters the bounded ring. It runs synchronously in the flow's own
	// goroutine, so an implementation must be fast and must not block: the
	// metering seam appends one small request-log row. Nil is a no-op.
	OnFlow func(FlowRecord)
	// QuotaCheck is the per-account admission policy. It runs after the
	// CONNECT request parses and before an admission slot is taken or any
	// vendor flow opens, with the gate's AccountID. A false result records a
	// quota rejection (error class FlowErrorQuota), answers SOCKS5 0x02 and
	// never calls Session.OpenFlow; used and budget are diagnostics only.
	// Nil disables quota admission.
	QuotaCheck func(accountID string) (ok bool, used, budget int64)
	// MeterProgress reports relayed bytes while an admitted flow is in flight.
	// The relay calls it at most once per ProgressChunk bytes per direction
	// (and once more for the trailing remainder). It advances the durable
	// monthly counters and returns false when a budget is exhausted; the gate
	// then closes both sides of the tunnel and records FlowErrorQuota. Nil
	// disables in-flight accounting.
	MeterProgress func(userID, accountID string, n int64) bool
	// UserFlowCheck reserves one concurrent-flow slot for an authenticated
	// user. It runs at the same post-CONNECT admission point as the quota
	// checks; a false result refuses the flow with FlowErrorUserFlowLimit
	// before a slot or vendor flow is taken. UserFlowRelease must return the
	// slot after the flow finishes. Both nil disables the per-user cap.
	UserFlowCheck   func(userID string) bool
	UserFlowRelease func(userID string)
	// AuthMode selects the SOCKS5 authentication method. An empty value keeps
	// AuthLoopback. AuthUserPass requires Authenticator and UserPolicy; a nil
	// one fails closed (01 01 / policy_unavailable) rather than falling back
	// to no-auth.
	AuthMode AuthMode
	// AuthTimeout bounds the RFC 1929 subnegotiation. Default 3s.
	AuthTimeout time.Duration
	// Authenticator resolves RFC 1929 credentials in AuthUserPass mode. Nil
	// makes every handshake fail with auth_unavailable.
	Authenticator Authenticator
	// UserPolicy is the post-CONNECT per-user policy in AuthUserPass mode.
	// Nil answers SOCKS5 0x02 with policy_unavailable.
	UserPolicy UserPolicy
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
	// Probes counts connections that closed before sending any handshake
	// byte: a health sampler dial-and-close. A probe is not a rejection and
	// never enters the flow ring.
	Probes uint64 `json:"probes"`
	// IngressBytes is client -> target payload; EgressBytes is target ->
	// client. They exclude the protocol handshakes. Both listeners feed the
	// same counters: one node has one traffic total, whichever front end a
	// client speaks.
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
	httpLn    net.Listener
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

	flowRing *flowRing
	flowSeq  atomic.Uint64

	accepted atomic.Uint64
	rejected atomic.Uint64
	probes   atomic.Uint64
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
	if err := validateListen(cfg.Listen); err != nil {
		return nil, err
	}
	if cfg.HTTPListen != "" {
		if err := validateListen(cfg.HTTPListen); err != nil {
			return nil, err
		}
	}
	mode, err := ParseAuthMode(string(cfg.AuthMode))
	if err != nil {
		return nil, err
	}
	cfg.AuthMode = mode
	if cfg.Limit <= 0 {
		cfg.Limit = defaultLimit
	}
	if cfg.AdmissionTimeout <= 0 {
		cfg.AdmissionTimeout = defaultAdmissionTimeout
	}
	if cfg.AuthTimeout <= 0 {
		cfg.AuthTimeout = defaultAuthTimeout
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
		flowRing:  newFlowRing(FlowRingCapacity),
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
	// Recheck the bound address: the configuration was validated in
	// NewServer, but a listener must never come up public even if that check
	// is bypassed or the OS resolved something unexpected.
	if err := validateBoundLoopback(ln.Addr()); err != nil {
		_ = ln.Close()
		s.mu.Unlock()
		return err
	}
	// The HTTP CONNECT listener is all-or-nothing with the SOCKS5 one: a node
	// that advertises both must not come up half-bound, so a failed HTTP bind
	// releases the SOCKS5 port and fails Start (the controller then retries
	// the next port pair).
	var httpLn net.Listener
	if s.cfg.HTTPListen != "" {
		httpLn, err = net.Listen("tcp", s.cfg.HTTPListen)
		if err != nil {
			_ = ln.Close()
			s.mu.Unlock()
			return fmt.Errorf("gate: listen http %s: %w", s.cfg.HTTPListen, err)
		}
		if err := validateBoundLoopback(httpLn.Addr()); err != nil {
			_ = httpLn.Close()
			_ = ln.Close()
			s.mu.Unlock()
			return err
		}
	}
	s.ln = ln
	s.httpLn = httpLn
	s.started = true
	s.startedAt = s.cfg.Now()
	s.mu.Unlock()

	go s.serve(ln)
	if httpLn != nil {
		go s.serveHTTP(httpLn)
	}
	go s.watchContext(ctx)
	s.logger.Info("gate listening",
		"node_id", s.cfg.NodeID,
		"account_id", s.cfg.AccountID,
		"addr", ln.Addr().String(),
		"limit", s.cfg.Limit,
	)
	if httpLn != nil {
		s.logger.Info("gate http listening",
			"node_id", s.cfg.NodeID,
			"account_id", s.cfg.AccountID,
			"addr", httpLn.Addr().String(),
		)
	}
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

// HTTPAddr returns the bound HTTP CONNECT listener address, or nil when the
// listener is disabled or the server has not started.
func (s *Server) HTTPAddr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.httpLn == nil {
		return nil
	}
	return s.httpLn.Addr()
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
	st.Probes = s.probes.Load()
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
		httpLn := s.httpLn
		active := s.active
		zero := s.zeroCh
		s.mu.Unlock()

		if ln != nil {
			_ = ln.Close()
		}
		if httpLn != nil {
			_ = httpLn.Close()
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

// serve accepts SOCKS5 connections until the listener closes.
func (s *Server) serve(ln net.Listener) {
	defer close(s.serveDone)
	s.accept(ln, s.handle)
}

// serveHTTP accepts HTTP CONNECT connections until the listener closes. It
// shares the admission slots, counters, drain state and flow ring with the
// SOCKS5 listener: one node has one capacity, whichever front end a client
// speaks.
func (s *Server) serveHTTP(ln net.Listener) {
	s.accept(ln, s.handleHTTP)
}

// accept runs one listener's accept loop. A transient Accept error (EMFILE,
// ECONNABORTED, a reset backlog) must not end the loop: the listener stays
// bound and the port keeps completing handshakes, so returning would leave a
// dead listener that looks healthy to a readiness dial. Only a closed listener
// ends the loop.
func (s *Server) accept(ln net.Listener, handle func(net.Conn)) {
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
			handle(conn)
		}()
	}
}

// handle serves one client connection: greeting, CONNECT, admission, OpenFlow
// and relay. Every exit path closes the client connection.
func (s *Server) handle(client net.Conn) {
	s.track(client)
	defer s.untrack(client)
	defer client.Close()

	started := s.cfg.Now()

	// A connection queued in the accept backlog can be handed to a handler
	// after Close took its client snapshot; refuse it here.
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		s.rejected.Add(1)
		s.recordReject(started, target{}, Principal{}, FlowErrorClosed)
		return
	}

	// A gate is a local front end: refuse a non-loopback peer before reading
	// one greeting byte. Authentication never makes a remote peer acceptable.
	if !loopbackPeer(client.RemoteAddr()) {
		s.rejected.Add(1)
		s.recordReject(started, target{}, Principal{}, FlowErrorNonLoopbackPeer)
		s.logger.Warn("gate peer rejected",
			"node_id", s.cfg.NodeID,
			"reason", FlowErrorNonLoopbackPeer,
		)
		return
	}

	_ = client.SetDeadline(time.Now().Add(handshakeTimeout))

	// Count what the peer sends so an idle probe (dial and close) can be told
	// apart from a real handshake that was truncated mid-frame.
	handshake := &countingConn{Conn: client}
	principal, err := s.negotiate(handshake)
	if err != nil {
		if handshake.read == 0 && (errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)) {
			s.probes.Add(1)
			s.logger.Debug("gate probe closed", "node_id", s.cfg.NodeID)
			return
		}
		errorClass := FlowErrorHandshake
		var refusal *handshakeError
		if errors.As(err, &refusal) {
			errorClass = refusal.Class
		}
		s.rejected.Add(1)
		s.recordReject(started, target{}, principal, errorClass)
		s.logger.Warn("gate handshake rejected",
			"node_id", s.cfg.NodeID,
			"reason", errorClass,
			"error", err,
		)
		return
	}
	// The RFC 1929 subnegotiation used its own short deadline; restore the
	// full handshake budget for the CONNECT request.
	_ = client.SetDeadline(time.Now().Add(handshakeTimeout))

	target, err := readConnect(handshake)
	if err != nil {
		s.rejected.Add(1)
		s.recordReject(started, target, principal, FlowErrorHandshake)
		s.logger.Warn("gate handshake rejected", "node_id", s.cfg.NodeID, "error", err)
		return
	}
	_ = client.SetDeadline(time.Time{})

	// The shared admission pipeline runs at the one post-parse point: the
	// per-user policy (enabled state, allowlist, user quota) and then the
	// account quota. Both precede the admission slot and every vendor call, so
	// a refusal never consumes capacity or opens upstream traffic. 0x02 is
	// SOCKS5's "connection not allowed by ruleset".
	verdict := s.admit(principal, target)
	if !verdict.ok {
		s.rejected.Add(1)
		s.recordReject(started, target, principal, verdict.errorClass)
		_ = writeReply(client, repNotAllowed)
		s.logger.Warn("gate flow rejected",
			"node_id", s.cfg.NodeID,
			"account_id", s.cfg.AccountID,
			"user_id", principal.UserID,
			"target", target.String(),
			"reason", verdict.errorClass,
			"used_bytes", verdict.used,
			"budget_bytes", verdict.budget,
		)
		return
	}
	if verdict.release != nil {
		defer verdict.release()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	release, err := s.acquireFlow(ctx)
	if err != nil {
		s.rejected.Add(1)
		rep := byte(repConnectionRefused)
		errorClass := FlowErrorClosed
		switch {
		case errors.Is(err, errAdmissionTimeout):
			rep = repTTLExpired
			errorClass = FlowErrorAdmissionTimeout
		case errors.Is(err, errDraining):
			errorClass = FlowErrorDraining
		}
		s.recordReject(started, target, principal, errorClass)
		_ = writeReply(client, rep)
		s.logger.Warn("gate flow rejected",
			"node_id", s.cfg.NodeID,
			"user_id", principal.UserID,
			"target", target.String(),
			"reason", err,
		)
		return
	}
	defer release()

	f, errorClass := s.beginFlow(client, cancel)
	if errorClass != "" {
		s.rejected.Add(1)
		s.recordReject(started, target, principal, errorClass)
		_ = writeReply(client, repConnectionRefused)
		return
	}
	defer s.finishFlow(f)

	remote, err := s.session.OpenFlow(ctx, target.host, target.port)
	if err != nil {
		s.rejected.Add(1)
		s.recordReject(started, target, principal, FlowErrorOpenFlow)
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
		s.recordReject(started, target, principal, FlowErrorReply)
		s.logger.Warn("gate reply failed",
			"node_id", s.cfg.NodeID,
			"target", target.String(),
			"error", err,
		)
		return
	}
	s.accepted.Add(1)

	in, out, quotaStopped := relay(client, remote, &s.ingress, &s.egress, s.meterProgress(principal.UserID))
	record := FlowRecord{
		StartedAt:  started,
		UserID:     principal.UserID,
		UserName:   principal.UserName,
		Method:     "CONNECT",
		Protocol:   FlowProtocolSOCKS5,
		TargetHost: target.host,
		TargetPort: target.port,
		Status:     FlowStatusOK,
		BytesIn:    in,
		BytesOut:   out,
	}
	if quotaStopped {
		record.ErrorClass = FlowErrorQuota
	}
	s.recordFlow(record)
	s.logger.Info("gate flow closed",
		"node_id", s.cfg.NodeID,
		"protocol", FlowProtocolSOCKS5,
		"user_id", principal.UserID,
		"target", target.String(),
		"ingress_bytes", in,
		"egress_bytes", out,
		"duration", s.cfg.Now().Sub(started),
		"error_class", record.ErrorClass,
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

// acquireFlow takes one admission slot and returns its idempotent release.
// Both listeners use it, so the slot accounting is identical for SOCKS5 and
// HTTP CONNECT.
func (s *Server) acquireFlow(ctx context.Context) (func(), error) {
	if err := s.acquire(ctx); err != nil {
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(func() { <-s.slots }) }, nil
}

// beginFlow registers one admitted flow and bumps Active. It returns the
// credential-free FlowError class when the server closed or started draining
// while the flow waited for its slot; the caller answers with its protocol's
// refusal.
func (s *Server) beginFlow(client net.Conn, cancel context.CancelFunc) (*flow, string) {
	f := &flow{client: client, cancel: cancel}
	s.mu.Lock()
	closed := s.closed
	draining := s.draining
	if closed || draining {
		s.mu.Unlock()
		if closed {
			return nil, FlowErrorClosed
		}
		return nil, FlowErrorDraining
	}
	s.flows[f] = struct{}{}
	s.beginFlowLocked()
	s.mu.Unlock()
	return f, ""
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

// countingConn counts the bytes read from the peer during the handshake. The
// greeting reader owns the connection for the whole handshake, so the plain
// int is race-free; writes still go through the embedded net.Conn.
type countingConn struct {
	net.Conn
	read int
}

func (c *countingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.read += n
	return n, err
}

// handshakeError is a handshake refusal that already answered the client.
// Class is the credential-free FlowRecord class; Err never carries a username,
// a password or buffered frame bytes.
type handshakeError struct {
	Class string
	Err   error
}

func (e *handshakeError) Error() string { return e.Err.Error() }

func (e *handshakeError) Unwrap() error { return e.Err }

// negotiate performs the SOCKS5 greeting and, in AuthUserPass mode, the RFC
// 1929 subnegotiation. It writes the protocol reply for every failure it can
// answer (0xFF when the required method is absent, 01 01 for a bad credential
// or a complete but invalid auth frame) and returns a handshakeError naming
// the FlowRecord class. A truncated frame returns the read error without
// inventing a reply.
func (s *Server) negotiate(conn net.Conn) (Principal, error) {
	var greeting [2]byte
	if _, err := io.ReadFull(conn, greeting[:]); err != nil {
		return Principal{}, fmt.Errorf("read greeting: %w", err)
	}
	if greeting[0] != socks5Version {
		return Principal{}, &handshakeError{
			Class: FlowErrorHandshake,
			Err:   fmt.Errorf("greeting version %d, want 5", greeting[0]),
		}
	}
	methods := make([]byte, int(greeting[1]))
	if _, err := io.ReadFull(conn, methods); err != nil {
		return Principal{}, &handshakeError{
			Class: FlowErrorHandshake,
			Err:   fmt.Errorf("read methods: %w", err),
		}
	}

	wanted := byte(authNone)
	if s.cfg.AuthMode == AuthUserPass {
		wanted = authUserPass
	}
	offered := false
	for _, method := range methods {
		if method == wanted {
			offered = true
			break
		}
	}
	if !offered {
		_ = writeMethodReply(conn, authNoAcceptable)
		return Principal{}, &handshakeError{
			Class: FlowErrorAuthMethod,
			Err:   errors.New("no acceptable authentication method"),
		}
	}
	if err := writeMethodReply(conn, wanted); err != nil {
		return Principal{}, &handshakeError{
			Class: FlowErrorHandshake,
			Err:   fmt.Errorf("write method reply: %w", err),
		}
	}
	if wanted == authNone {
		return Principal{}, nil
	}

	// The credential frame gets its own short deadline instead of the whole
	// handshake budget: a client that stops mid-frame must not pin a handler.
	_ = conn.SetDeadline(time.Now().Add(s.cfg.AuthTimeout))
	return s.readUserPass(conn)
}

// readUserPass reads one RFC 1929 frame and verifies it. Every rejection
// answers the same 01 01 status, so the client cannot tell an unknown user
// from a wrong secret, a revoked key or a key issued to another user.
func (s *Server) readUserPass(conn net.Conn) (Principal, error) {
	var head [2]byte
	if _, err := io.ReadFull(conn, head[:]); err != nil {
		return Principal{}, &handshakeError{
			Class: FlowErrorAuthFailed,
			Err:   fmt.Errorf("read auth frame: %w", err),
		}
	}
	if head[0] != authVersion {
		_ = writeAuthReply(conn, authStatusFailed)
		return Principal{}, &handshakeError{
			Class: FlowErrorAuthFailed,
			Err:   errors.New("auth frame version is not RFC 1929"),
		}
	}
	if head[1] == 0 {
		_ = writeAuthReply(conn, authStatusFailed)
		return Principal{}, &handshakeError{
			Class: FlowErrorAuthFailed,
			Err:   errors.New("auth frame has an empty username"),
		}
	}
	user := make([]byte, int(head[1]))
	defer clear(user)
	if _, err := io.ReadFull(conn, user); err != nil {
		return Principal{}, &handshakeError{
			Class: FlowErrorAuthFailed,
			Err:   fmt.Errorf("read auth username: %w", err),
		}
	}

	var passLen [1]byte
	if _, err := io.ReadFull(conn, passLen[:]); err != nil {
		return Principal{}, &handshakeError{
			Class: FlowErrorAuthFailed,
			Err:   fmt.Errorf("read auth password length: %w", err),
		}
	}
	if passLen[0] == 0 {
		_ = writeAuthReply(conn, authStatusFailed)
		return Principal{}, &handshakeError{
			Class: FlowErrorAuthFailed,
			Err:   errors.New("auth frame has an empty password"),
		}
	}
	pass := make([]byte, int(passLen[0]))
	defer clear(pass)
	if _, err := io.ReadFull(conn, pass); err != nil {
		return Principal{}, &handshakeError{
			Class: FlowErrorAuthFailed,
			Err:   fmt.Errorf("read auth password: %w", err),
		}
	}

	if s.cfg.Authenticator == nil {
		_ = writeAuthReply(conn, authStatusFailed)
		return Principal{}, &handshakeError{
			Class: FlowErrorAuthUnavailable,
			Err:   errors.New("no authenticator is configured"),
		}
	}
	principal, ok := s.cfg.Authenticator.Authenticate(string(user), string(pass))
	if !ok || !principal.Authenticated() {
		_ = writeAuthReply(conn, authStatusFailed)
		return Principal{}, &handshakeError{
			Class: FlowErrorAuthFailed,
			Err:   errors.New("authentication failed"),
		}
	}
	if err := writeAuthReply(conn, authStatusOK); err != nil {
		return Principal{}, &handshakeError{
			Class: FlowErrorHandshake,
			Err:   fmt.Errorf("write auth reply: %w", err),
		}
	}
	return principal, nil
}

// readConnect parses one SOCKS5 CONNECT request. It writes every reply the
// protocol defines for the failure it finds: 0x07 for a non-CONNECT command,
// 0x08 for an unknown address type and 0x01 for a malformed request. A wrong
// version or truncated frame closes without a reply.
func readConnect(conn net.Conn) (target, error) {
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
		t.host = CanonicalHost(net.IP(ip).String())
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
		host := CanonicalHost(string(name))
		if host == "" {
			_ = writeReply(conn, repGeneralFailure)
			return target{}, errors.New("empty domain name")
		}
		t.host = host
	case atypIPv6:
		ip := make([]byte, net.IPv6len)
		if _, err := io.ReadFull(conn, ip); err != nil {
			return target{}, fmt.Errorf("read IPv6 address: %w", err)
		}
		t.host = CanonicalHost(net.IP(ip).String())
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

// admitUser evaluates the authenticated user's policy at the single
// post-CONNECT admission point. A nil policy fails closed.
func (s *Server) admitUser(principal Principal, host string) (bool, string, int64, int64) {
	if s.cfg.UserPolicy == nil {
		return false, FlowErrorPolicyUnavailable, 0, 0
	}
	ok, errorClass, used, budget := s.cfg.UserPolicy.Admit(principal, host)
	if ok {
		return true, "", used, budget
	}
	if errorClass == "" {
		errorClass = FlowErrorPolicyUnavailable
	}
	return false, errorClass, used, budget
}

// admission is the shared policy verdict for one parsed destination. A
// refusal names the credential-free FlowRecord class; used and budget are
// diagnostics for the rejection log only. release is non-nil when the verdict
// reserved a per-user flow slot; the caller must defer it.
type admission struct {
	ok         bool
	errorClass string
	used       int64
	budget     int64
	release    func()
}

// admit evaluates the policy stages both listeners share, in the order the
// design fixes: the per-user policy (userpass mode only: enabled state,
// allowlist, user quota), then the account quota, then the per-user active-flow
// cap. It runs after the destination parses and before an admission slot is
// taken or any vendor flow opens, so a refusal never consumes capacity or opens
// upstream traffic.
func (s *Server) admit(principal Principal, target target) admission {
	if s.cfg.AuthMode == AuthUserPass {
		ok, errorClass, used, budget := s.admitUser(principal, target.host)
		if !ok {
			return admission{errorClass: errorClass, used: used, budget: budget}
		}
	}
	if s.cfg.QuotaCheck != nil {
		if ok, used, budget := s.cfg.QuotaCheck(s.cfg.AccountID); !ok {
			return admission{errorClass: FlowErrorQuota, used: used, budget: budget}
		}
	}
	// The cap needs both hooks: acquiring without a release would leak the
	// slot, so an incomplete pair is treated as disabled.
	if s.cfg.AuthMode == AuthUserPass && principal.UserID != "" &&
		s.cfg.UserFlowCheck != nil && s.cfg.UserFlowRelease != nil {
		if !s.cfg.UserFlowCheck(principal.UserID) {
			return admission{errorClass: FlowErrorUserFlowLimit}
		}
		userID := principal.UserID
		return admission{ok: true, release: func() { s.cfg.UserFlowRelease(userID) }}
	}
	return admission{ok: true}
}

// loopbackPeer reports whether addr is a literal loopback TCP peer. Any other
// address, including a non-TCP one, is refused: the listener is TCP, so an
// unknown transport cannot be the local client this gate serves.
func loopbackPeer(addr net.Addr) bool {
	tcp, ok := addr.(*net.TCPAddr)
	return ok && tcp.IP.IsLoopback()
}

// validateListen refuses anything but a literal loopback host. An empty host
// (":13000"), a wildcard, a LAN or public IP and a hostname are all rejected:
// the gate is a local front end and authentication never makes a public bind
// acceptable.
func validateListen(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("gate: listen address %q: %w", addr, err)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("gate: listen host %q is not a literal IP address", host)
	}
	if !ip.IsLoopback() {
		return fmt.Errorf("gate: listen host %q is not loopback", host)
	}
	return nil
}

// validateBoundLoopback rechecks the address the OS actually bound.
func validateBoundLoopback(addr net.Addr) error {
	tcp, ok := addr.(*net.TCPAddr)
	if !ok || !tcp.IP.IsLoopback() {
		return fmt.Errorf("gate: listener bound to %s, which is not loopback", addr)
	}
	return nil
}

func writeMethodReply(w io.Writer, method byte) error {
	_, err := w.Write([]byte{socks5Version, method})
	return err
}

// writeAuthReply answers one RFC 1929 subnegotiation: version 1 plus the
// status byte.
func writeAuthReply(w io.Writer, status byte) error {
	_, err := w.Write([]byte{authVersion, status})
	return err
}

func writeReply(w io.Writer, rep byte) error {
	_, err := w.Write([]byte{socks5Version, rep, 0x00, atypIPv4, 0, 0, 0, 0, 0, 0})
	return err
}

// meterProgress returns the per-flow progress callback for one principal. Nil
// means the gate was built without in-flight accounting.
func (s *Server) meterProgress(userID string) func(int64) bool {
	if s.cfg.MeterProgress == nil {
		return nil
	}
	accountID := s.cfg.AccountID
	return func(n int64) bool { return s.cfg.MeterProgress(userID, accountID, n) }
}

// relay copies bytes in both directions until both copies end, then closes
// both connections. ingress counts client -> target, egress target -> client.
// progress, when non-nil, is called with every ProgressChunk bytes written in
// either direction plus the trailing remainder; a false result closes both
// sides immediately and sets quotaStopped so the caller can record the class.
func relay(a, b net.Conn, ingress, egress *atomic.Uint64, progress func(int64) bool) (in, out int64, quotaStopped bool) {
	var wg sync.WaitGroup
	var once sync.Once
	var stopped atomic.Bool
	stop := func() {
		once.Do(func() {
			stopped.Store(true)
			_ = a.Close()
			_ = b.Close()
		})
	}
	wg.Add(2)
	go func() {
		defer wg.Done()
		in = copyFlow(b, a, ingress, progress, stop)
	}()
	go func() {
		defer wg.Done()
		out = copyFlow(a, b, egress, progress, stop)
	}()
	wg.Wait()
	_ = a.Close()
	_ = b.Close()
	return in, out, stopped.Load()
}

// copyFlow copies one direction and half-closes the destination. A connection
// without CloseWrite (net.Pipe, for example) is closed instead, matching the
// emulator's relay behaviour.
func copyFlow(dst, src net.Conn, counter *atomic.Uint64, progress func(int64) bool, stop func()) int64 {
	writer := &meteredWriter{w: dst, n: counter, progress: progress}
	n, _ := io.Copy(writer, src)
	if writer.flush() {
		stop()
	}
	if cw, ok := dst.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	} else {
		_ = dst.Close()
	}
	return n
}

// meteredWriter adds every successful write to counter and reports the bytes
// to the progress callback in ProgressChunk batches. Counters are updated live,
// so State is exact while a flow is still running.
type meteredWriter struct {
	w        io.Writer
	n        *atomic.Uint64
	progress func(int64) bool
	pending  int64
	refused  bool
}

func (m *meteredWriter) Write(p []byte) (int, error) {
	n, err := m.w.Write(p)
	if n > 0 {
		m.n.Add(uint64(n))
		m.pending += int64(n)
	}
	if m.progress != nil && m.pending >= ProgressChunk {
		// Clear pending before reporting: the refused chunk must reach the
		// meter exactly once. Leaving it set lets flush report the same bytes
		// again when io.Copy stops on errQuotaStopped.
		pending := m.pending
		m.pending = 0
		if !m.progress(pending) {
			m.refused = true
			return n, errQuotaStopped
		}
	}
	return n, err
}

// flush reports the trailing bytes below one chunk and returns true when the
// meter refused the flow. Pending is cleared before the callback so a refused
// remainder is never reported twice.
func (m *meteredWriter) flush() bool {
	if m.progress != nil && m.pending > 0 {
		pending := m.pending
		m.pending = 0
		if !m.progress(pending) {
			m.refused = true
		}
	}
	return m.refused
}
