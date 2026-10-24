package gate

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"ajsaccountd/internal/account"
)

// Compile-time proof that the account session seam fits: FlowSession has
// OpenFlow plus extra methods, so it satisfies Session structurally.
var _ Session = account.FlowSession(nil)

// fakeSession opens an in-memory echo flow and records every target. It never
// touches the network.
type fakeSession struct {
	mu    sync.Mutex
	opens []string
	err   error
}

func (f *fakeSession) OpenFlow(ctx context.Context, host string, port int) (net.Conn, error) {
	f.mu.Lock()
	f.opens = append(f.opens, net.JoinHostPort(host, strconv.Itoa(port)))
	err := f.err
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	client, server := net.Pipe()
	go func() {
		defer server.Close()
		_, _ = io.Copy(server, server)
	}()
	return client, nil
}

func (f *fakeSession) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.opens...)
}

// blockingSession stays inside OpenFlow until the flow context is cancelled.
type blockingSession struct {
	once    sync.Once
	started chan struct{}
}

func newBlockingSession() *blockingSession {
	return &blockingSession{started: make(chan struct{})}
}

func (b *blockingSession) OpenFlow(ctx context.Context, host string, port int) (net.Conn, error) {
	b.once.Do(func() { close(b.started) })
	<-ctx.Done()
	return nil, ctx.Err()
}

func startServer(t *testing.T, cfg Config, sess Session) *Server {
	t.Helper()
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:0"
	}
	srv, err := NewServer(cfg, sess)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

func dialGate(t *testing.T, srv *Server) net.Conn {
	t.Helper()
	c, err := net.DialTimeout("tcp", srv.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial gate: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// greet sends the greeting and returns the selected method.
func greet(t *testing.T, c net.Conn, methods ...byte) byte {
	t.Helper()
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := c.Write(append([]byte{socks5Version, byte(len(methods))}, methods...)); err != nil {
		t.Fatalf("write greeting: %v", err)
	}
	var reply [2]byte
	if _, err := io.ReadFull(c, reply[:]); err != nil {
		t.Fatalf("read method reply: %v", err)
	}
	if reply[0] != socks5Version {
		t.Fatalf("method reply version = %d, want 5", reply[0])
	}
	return reply[1]
}

// connectRequest writes a CONNECT frame. addr is the raw address field (the
// domain length prefix included for ATYP 3).
func connectRequest(t *testing.T, c net.Conn, cmd, atyp byte, addr []byte, port int) {
	t.Helper()
	req := []byte{socks5Version, cmd, 0x00, atyp}
	req = append(req, addr...)
	var p [2]byte
	binary.BigEndian.PutUint16(p[:], uint16(port))
	req = append(req, p[:]...)
	if _, err := c.Write(req); err != nil {
		t.Fatalf("write request: %v", err)
	}
}

func readReply(t *testing.T, c net.Conn) byte {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	var reply [10]byte
	if _, err := io.ReadFull(c, reply[:]); err != nil {
		t.Fatalf("read SOCKS5 reply: %v", err)
	}
	if reply[0] != socks5Version {
		t.Fatalf("reply version = %d, want 5", reply[0])
	}
	return reply[1]
}

// echoOnce writes payload and reads the same number of bytes back.
func echoOnce(t *testing.T, c net.Conn, payload []byte) {
	t.Helper()
	if _, err := c.Write(payload); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(c, got); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("echo = %q, want %q", got, payload)
	}
}

// connectOK performs greeting + CONNECT and requires a success reply.
func connectOK(t *testing.T, c net.Conn, atyp byte, addr []byte, port int) {
	t.Helper()
	if method := greet(t, c, authNone); method != authNone {
		t.Fatalf("selected method = %#x, want 0x00", method)
	}
	connectRequest(t, c, cmdConnect, atyp, addr, port)
	if rep := readReply(t, c); rep != repSucceeded {
		t.Fatalf("CONNECT reply = %#x, want 0x00", rep)
	}
}

func waitState(t *testing.T, srv *Server, cond func(State) bool) State {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		st := srv.State()
		if cond(st) {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for state; last = %+v", st)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestConnectAddressTypes(t *testing.T) {
	tests := []struct {
		name     string
		atyp     byte
		addr     []byte
		wantHost string
		port     int
	}{
		{
			name:     "domain",
			atyp:     atypDomain,
			addr:     append([]byte{11}, []byte("example.com")...),
			wantHost: "example.com",
			port:     443,
		},
		{
			name:     "ipv4",
			atyp:     atypIPv4,
			addr:     net.IPv4(192, 0, 2, 10).To4(),
			wantHost: "192.0.2.10",
			port:     8080,
		},
		{
			name:     "ipv6",
			atyp:     atypIPv6,
			addr:     net.ParseIP("2001:db8::1").To16(),
			wantHost: "2001:db8::1",
			port:     53,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sess := &fakeSession{}
			srv := startServer(t, Config{NodeID: "node-1", AccountID: "acc-1", Limit: 4}, sess)
			c := dialGate(t, srv)
			connectOK(t, c, tc.atyp, tc.addr, tc.port)
			echoOnce(t, c, []byte("ping"))
			_ = c.Close()

			st := waitState(t, srv, func(st State) bool { return st.Active == 0 })
			if st.Accepted != 1 || st.Rejected != 0 {
				t.Fatalf("accepted/rejected = %d/%d, want 1/0", st.Accepted, st.Rejected)
			}
			if st.IngressBytes != 4 || st.EgressBytes != 4 {
				t.Fatalf("bytes = %d/%d, want 4/4", st.IngressBytes, st.EgressBytes)
			}
			opens := sess.recorded()
			want := net.JoinHostPort(tc.wantHost, strconv.Itoa(tc.port))
			if len(opens) != 1 || opens[0] != want {
				t.Fatalf("OpenFlow targets = %v, want [%s]", opens, want)
			}
		})
	}
}

func TestAdmissionLimit(t *testing.T) {
	sess := &fakeSession{}
	srv := startServer(t, Config{
		NodeID:           "node-1",
		Limit:            1,
		AdmissionTimeout: 150 * time.Millisecond,
	}, sess)

	first := dialGate(t, srv)
	connectOK(t, first, atypDomain, append([]byte{8}, []byte("first.io")...), 80)
	echoOnce(t, first, []byte("a"))

	second := dialGate(t, srv)
	if method := greet(t, second, authNone); method != authNone {
		t.Fatalf("selected method = %#x, want 0x00", method)
	}
	connectRequest(t, second, cmdConnect, atypDomain, append([]byte{9}, []byte("second.io")...), 80)
	start := time.Now()
	rep := readReply(t, second)
	elapsed := time.Since(start)
	if rep != repTTLExpired {
		t.Fatalf("busy CONNECT reply = %#x, want 0x06", rep)
	}
	if elapsed < 100*time.Millisecond {
		t.Fatalf("rejection after %s, want at least the 150ms admission timeout", elapsed)
	}
	if st := srv.State(); st.Rejected != 1 || st.Accepted != 1 {
		t.Fatalf("accepted/rejected = %d/%d, want 1/1", st.Accepted, st.Rejected)
	}
	_ = second.Close()

	_ = first.Close()
	waitState(t, srv, func(st State) bool { return st.Active == 0 })

	third := dialGate(t, srv)
	connectOK(t, third, atypDomain, append([]byte{5}, []byte("third")...), 80)
	echoOnce(t, third, []byte("b"))
	_ = third.Close()

	st := waitState(t, srv, func(st State) bool { return st.Active == 0 })
	if st.Accepted != 2 || st.Rejected != 1 {
		t.Fatalf("accepted/rejected = %d/%d, want 2/1", st.Accepted, st.Rejected)
	}
}

func TestDrainWaitsForActiveAndUndrain(t *testing.T) {
	sess := &fakeSession{}
	srv := startServer(t, Config{NodeID: "node-1", Limit: 4, DrainGrace: 2 * time.Second}, sess)

	active := dialGate(t, srv)
	connectOK(t, active, atypDomain, append([]byte{6}, []byte("active")...), 80)
	echoOnce(t, active, []byte("x"))

	drained := make(chan error, 1)
	go func() { drained <- srv.Drain(context.Background()) }()
	waitState(t, srv, func(st State) bool { return st.Draining })

	rejected := dialGate(t, srv)
	if method := greet(t, rejected, authNone); method != authNone {
		t.Fatalf("selected method = %#x, want 0x00", method)
	}
	connectRequest(t, rejected, cmdConnect, atypDomain, append([]byte{3}, []byte("new")...), 80)
	if rep := readReply(t, rejected); rep != repConnectionRefused {
		t.Fatalf("draining CONNECT reply = %#x, want 0x05", rep)
	}
	_ = rejected.Close()

	select {
	case err := <-drained:
		t.Fatalf("Drain returned before the active flow ended: %v", err)
	default:
	}

	_ = active.Close()
	select {
	case err := <-drained:
		if err != nil {
			t.Fatalf("Drain: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Drain did not return after the active flow ended")
	}

	st := srv.State()
	if !st.Draining || st.Active != 0 || st.Rejected != 1 || st.Accepted != 1 {
		t.Fatalf("state after drain = %+v", st)
	}

	if err := srv.Undrain(); err != nil {
		t.Fatalf("Undrain: %v", err)
	}
	if st := srv.State(); st.Draining {
		t.Fatal("State.Draining is true after Undrain")
	}

	again := dialGate(t, srv)
	connectOK(t, again, atypDomain, append([]byte{5}, []byte("again")...), 80)
	echoOnce(t, again, []byte("y"))
	_ = again.Close()

	st = waitState(t, srv, func(st State) bool { return st.Active == 0 })
	if st.Accepted != 2 {
		t.Fatalf("Accepted = %d, want 2", st.Accepted)
	}
}

func TestDrainGraceLeavesFlowRunning(t *testing.T) {
	sess := &fakeSession{}
	srv := startServer(t, Config{NodeID: "node-1", Limit: 2, DrainGrace: 100 * time.Millisecond}, sess)

	c := dialGate(t, srv)
	connectOK(t, c, atypDomain, append([]byte{4}, []byte("slow")...), 80)

	start := time.Now()
	if err := srv.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 80*time.Millisecond {
		t.Fatalf("Drain returned after %s, want to wait for the 100ms grace", elapsed)
	}
	if st := srv.State(); st.Active != 1 || !st.Draining {
		t.Fatalf("state after grace = %+v, want one active draining flow", st)
	}

	// The in-flight flow still relays after Drain returned.
	echoOnce(t, c, []byte("still here"))
	_ = c.Close()
	waitState(t, srv, func(st State) bool { return st.Active == 0 })
}

func TestMalformedHandshakes(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T, c net.Conn)
	}{
		{
			name: "wrong greeting version",
			run: func(t *testing.T, c net.Conn) {
				_ = c.SetDeadline(time.Now().Add(3 * time.Second))
				if _, err := c.Write([]byte{0x04, 0x01, authNone}); err != nil {
					t.Fatalf("write greeting: %v", err)
				}
				if _, err := c.Read(make([]byte, 1)); err == nil {
					t.Fatal("read succeeded, want the server to close without a reply")
				}
			},
		},
		{
			name: "no acceptable method",
			run: func(t *testing.T, c net.Conn) {
				if method := greet(t, c, 0x02); method != authNoAcceptable {
					t.Fatalf("selected method = %#x, want 0xFF", method)
				}
			},
		},
		{
			name: "zero methods",
			run: func(t *testing.T, c net.Conn) {
				if method := greet(t, c); method != authNoAcceptable {
					t.Fatalf("selected method = %#x, want 0xFF", method)
				}
			},
		},
		{
			name: "unsupported command",
			run: func(t *testing.T, c net.Conn) {
				if method := greet(t, c, authNone); method != authNone {
					t.Fatalf("selected method = %#x, want 0x00", method)
				}
				connectRequest(t, c, 0x02, atypIPv4, net.IPv4(127, 0, 0, 1).To4(), 80)
				if rep := readReply(t, c); rep != repCommandUnsupported {
					t.Fatalf("reply = %#x, want 0x07", rep)
				}
			},
		},
		{
			name: "unsupported address type",
			run: func(t *testing.T, c net.Conn) {
				if method := greet(t, c, authNone); method != authNone {
					t.Fatalf("selected method = %#x, want 0x00", method)
				}
				if _, err := c.Write([]byte{socks5Version, cmdConnect, 0x00, 0x09}); err != nil {
					t.Fatalf("write request: %v", err)
				}
				if rep := readReply(t, c); rep != repAddrUnsupported {
					t.Fatalf("reply = %#x, want 0x08", rep)
				}
			},
		},
		{
			name: "reserved byte set",
			run: func(t *testing.T, c net.Conn) {
				if method := greet(t, c, authNone); method != authNone {
					t.Fatalf("selected method = %#x, want 0x00", method)
				}
				if _, err := c.Write([]byte{socks5Version, cmdConnect, 0x01, atypIPv4, 127, 0, 0, 1, 0, 80}); err != nil {
					t.Fatalf("write request: %v", err)
				}
				if rep := readReply(t, c); rep != repGeneralFailure {
					t.Fatalf("reply = %#x, want 0x01", rep)
				}
			},
		},
		{
			name: "empty domain",
			run: func(t *testing.T, c net.Conn) {
				if method := greet(t, c, authNone); method != authNone {
					t.Fatalf("selected method = %#x, want 0x00", method)
				}
				if _, err := c.Write([]byte{socks5Version, cmdConnect, 0x00, atypDomain, 0x00, 0x00, 80}); err != nil {
					t.Fatalf("write request: %v", err)
				}
				if rep := readReply(t, c); rep != repGeneralFailure {
					t.Fatalf("reply = %#x, want 0x01", rep)
				}
			},
		},
		{
			name: "wrong request version",
			run: func(t *testing.T, c net.Conn) {
				if method := greet(t, c, authNone); method != authNone {
					t.Fatalf("selected method = %#x, want 0x00", method)
				}
				_ = c.SetDeadline(time.Now().Add(3 * time.Second))
				if _, err := c.Write([]byte{0x04, cmdConnect, 0x00, atypIPv4, 127, 0, 0, 1, 0, 80}); err != nil {
					t.Fatalf("write request: %v", err)
				}
				if _, err := c.Read(make([]byte, 1)); err == nil {
					t.Fatal("read succeeded, want the server to close without a reply")
				}
			},
		},
		{
			name: "truncated request",
			run: func(t *testing.T, c net.Conn) {
				if method := greet(t, c, authNone); method != authNone {
					t.Fatalf("selected method = %#x, want 0x00", method)
				}
				if _, err := c.Write([]byte{socks5Version, cmdConnect, 0x00, atypIPv4, 127}); err != nil {
					t.Fatalf("write partial request: %v", err)
				}
				_ = c.Close()
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sess := &fakeSession{}
			srv := startServer(t, Config{NodeID: "node-1", Limit: 2}, sess)
			c := dialGate(t, srv)
			tc.run(t, c)
			st := waitState(t, srv, func(st State) bool { return st.Rejected >= 1 })
			if st.Accepted != 0 {
				t.Fatalf("Accepted = %d, want 0", st.Accepted)
			}
			if opens := sess.recorded(); len(opens) != 0 {
				t.Fatalf("OpenFlow called for a rejected handshake: %v", opens)
			}
		})
	}
}

// captureHandler records every log record under a mutex, so a handler
// goroutine can log while the test inspects the slice.
type captureHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}

func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

func (h *captureHandler) snapshot() []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]slog.Record(nil), h.records...)
}

// waitLog waits for one record with the given message and returns it.
func waitLog(t *testing.T, h *captureHandler, msg string) slog.Record {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		for _, r := range h.snapshot() {
			if r.Message == msg {
				return r
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for log %q; records = %v", msg, h.snapshot())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// warnRecords returns every WARN record seen so far.
func warnRecords(h *captureHandler) []slog.Record {
	var out []slog.Record
	for _, r := range h.snapshot() {
		if r.Level == slog.LevelWarn {
			out = append(out, r)
		}
	}
	return out
}

// TestProbeConnectionIsNotRejected pins the health-sampler fix: a dial and
// close with no handshake byte counts as a probe, logs at debug, and leaves
// Rejected and the flow ring untouched.
func TestProbeConnectionIsNotRejected(t *testing.T) {
	logs := &captureHandler{}
	srv := startServer(t, Config{NodeID: "node-probe", Logger: slog.New(logs)}, &fakeSession{})

	c := dialGate(t, srv)
	_ = c.Close()

	record := waitLog(t, logs, "gate probe closed")
	if record.Level != slog.LevelDebug {
		t.Fatalf("probe log level = %v, want DEBUG", record.Level)
	}
	st := waitState(t, srv, func(st State) bool { return st.Probes == 1 })
	if st.Accepted != 0 || st.Rejected != 0 {
		t.Fatalf("accepted/rejected/probes = %d/%d/%d, want 0/0/1", st.Accepted, st.Rejected, st.Probes)
	}
	if records := srv.Flows(0); len(records) != 0 {
		t.Fatalf("probe wrote flow records: %+v", records)
	}
	if warns := warnRecords(logs); len(warns) != 0 {
		t.Fatalf("probe logged warnings: %v", warns)
	}
}

// TestZeroByteWriteThenCloseIsProbe covers a sampler that writes an empty
// buffer before closing: still zero handshake bytes, still a probe.
func TestZeroByteWriteThenCloseIsProbe(t *testing.T) {
	logs := &captureHandler{}
	srv := startServer(t, Config{NodeID: "node-probe", Logger: slog.New(logs)}, &fakeSession{})

	c := dialGate(t, srv)
	if n, err := c.Write([]byte{}); err != nil || n != 0 {
		t.Fatalf("zero-length write = %d, %v; want 0, nil", n, err)
	}
	_ = c.Close()

	waitLog(t, logs, "gate probe closed")
	st := waitState(t, srv, func(st State) bool { return st.Probes == 1 })
	if st.Accepted != 0 || st.Rejected != 0 {
		t.Fatalf("accepted/rejected/probes = %d/%d/%d, want 0/0/1", st.Accepted, st.Rejected, st.Probes)
	}
	if records := srv.Flows(0); len(records) != 0 {
		t.Fatalf("probe wrote flow records: %+v", records)
	}
	if warns := warnRecords(logs); len(warns) != 0 {
		t.Fatalf("probe logged warnings: %v", warns)
	}
}

// TestPartialGreetingIsRejected proves a truncated real handshake keeps the
// old semantics: one consumed byte is not a probe.
func TestPartialGreetingIsRejected(t *testing.T) {
	logs := &captureHandler{}
	srv := startServer(t, Config{NodeID: "node-truncated", Logger: slog.New(logs)}, &fakeSession{})

	c := dialGate(t, srv)
	if _, err := c.Write([]byte{socks5Version}); err != nil {
		t.Fatalf("write partial greeting: %v", err)
	}
	_ = c.Close()

	record := waitLog(t, logs, "gate handshake rejected")
	if record.Level != slog.LevelWarn {
		t.Fatalf("truncated handshake log level = %v, want WARN", record.Level)
	}
	st := waitState(t, srv, func(st State) bool { return st.Rejected == 1 })
	if st.Probes != 0 || st.Accepted != 0 {
		t.Fatalf("accepted/rejected/probes = %d/%d/%d, want 0/1/0", st.Accepted, st.Rejected, st.Probes)
	}
	records := waitFlowRecords(t, srv, func(records []FlowRecord) bool { return len(records) == 1 })
	if records[0].Status != FlowStatusRejected || records[0].ErrorClass != FlowErrorHandshake {
		t.Fatalf("record = %+v, want rejected/handshake", records[0])
	}
}

// TestProbeThenValidHandshake proves classification does not disturb normal
// service: after a probe, a full CONNECT still relays.
func TestProbeThenValidHandshake(t *testing.T) {
	logs := &captureHandler{}
	sess := &fakeSession{}
	srv := startServer(t, Config{NodeID: "node-probe", Logger: slog.New(logs)}, sess)

	probe := dialGate(t, srv)
	_ = probe.Close()
	waitLog(t, logs, "gate probe closed")

	c := dialGate(t, srv)
	connectOK(t, c, atypDomain, append([]byte{11}, []byte("example.com")...), 443)
	echoOnce(t, c, []byte("ping"))
	_ = c.Close()

	st := waitState(t, srv, func(st State) bool { return st.Accepted == 1 && st.Active == 0 })
	if st.Probes != 1 || st.Rejected != 0 {
		t.Fatalf("accepted/rejected/probes = %d/%d/%d, want 1/0/1", st.Accepted, st.Rejected, st.Probes)
	}
	if opens := sess.recorded(); len(opens) != 1 {
		t.Fatalf("OpenFlow calls = %v, want one", opens)
	}
}

func TestOpenFlowErrorRepliesRefused(t *testing.T) {
	sess := &fakeSession{err: errors.New("upstream unavailable")}
	srv := startServer(t, Config{NodeID: "node-1"}, sess)
	c := dialGate(t, srv)

	if method := greet(t, c, authNone); method != authNone {
		t.Fatalf("selected method = %#x, want 0x00", method)
	}
	connectRequest(t, c, cmdConnect, atypDomain, append([]byte{4}, []byte("fail")...), 80)
	if rep := readReply(t, c); rep != repConnectionRefused {
		t.Fatalf("reply = %#x, want 0x05", rep)
	}
	st := waitState(t, srv, func(st State) bool { return st.Rejected == 1 && st.Active == 0 })
	if st.Accepted != 0 || st.IngressBytes != 0 || st.EgressBytes != 0 {
		t.Fatalf("state after open failure = %+v", st)
	}
}

func TestStateSnapshot(t *testing.T) {
	srv := startServer(t, Config{NodeID: "node-7", AccountID: "acc-7", Limit: 3}, &fakeSession{})

	st := srv.State()
	if st.NodeID != "node-7" || st.AccountID != "acc-7" {
		t.Fatalf("ids = %q/%q", st.NodeID, st.AccountID)
	}
	if st.Limit != 3 || st.Active != 0 || st.Draining {
		t.Fatalf("initial state = %+v", st)
	}
	if st.Accepted != 0 || st.Rejected != 0 || st.IngressBytes != 0 || st.EgressBytes != 0 {
		t.Fatalf("initial counters = %+v", st)
	}
	if st.StartedAt.IsZero() {
		t.Fatal("StartedAt is zero after Start")
	}

	addr, ok := srv.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("Addr = %T, want *net.TCPAddr", srv.Addr())
	}
	if !addr.IP.IsLoopback() {
		t.Fatalf("listener IP = %s, want loopback", addr.IP)
	}

	if err := srv.Undrain(); err != nil {
		t.Fatalf("Undrain on a serving server: %v", err)
	}
}

func TestCloseStopsListenerAndActiveFlows(t *testing.T) {
	sess := &fakeSession{}
	srv := startServer(t, Config{NodeID: "node-1", Limit: 2, DrainGrace: 100 * time.Millisecond}, sess)
	addr := srv.Addr().String()

	c := dialGate(t, srv)
	connectOK(t, c, atypDomain, append([]byte{4}, []byte("open")...), 80)
	echoOnce(t, c, []byte("z"))

	start := time.Now()
	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 80*time.Millisecond {
		t.Fatalf("Close returned after %s, want to wait for the 100ms grace", elapsed)
	}

	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatal("active client still readable after Close")
	}

	waitState(t, srv, func(st State) bool { return st.Active == 0 })
	select {
	case <-srv.serveDone:
	case <-time.After(2 * time.Second):
		t.Fatal("accept loop still running after Close")
	}

	done := make(chan struct{})
	go func() {
		srv.handlers.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler goroutines did not finish after Close")
	}

	srv.mu.Lock()
	clients, flows := len(srv.clients), len(srv.flows)
	srv.mu.Unlock()
	if clients != 0 || flows != 0 {
		t.Fatalf("tracked clients/flows = %d/%d after Close, want 0/0", clients, flows)
	}

	if _, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
		t.Fatal("listener still accepting after Close")
	}
	if err := srv.Drain(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("Drain after Close = %v, want ErrClosed", err)
	}
	if err := srv.Undrain(); !errors.Is(err, ErrClosed) {
		t.Fatalf("Undrain after Close = %v, want ErrClosed", err)
	}
	if err := srv.Start(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("Start after Close = %v, want ErrClosed", err)
	}
}

func TestCloseCancelsBlockedOpenFlow(t *testing.T) {
	sess := newBlockingSession()
	srv := startServer(t, Config{NodeID: "node-1", Limit: 1, DrainGrace: 50 * time.Millisecond}, sess)

	c := dialGate(t, srv)
	if method := greet(t, c, authNone); method != authNone {
		t.Fatalf("selected method = %#x, want 0x00", method)
	}
	connectRequest(t, c, cmdConnect, atypDomain, append([]byte{4}, []byte("hang")...), 80)
	select {
	case <-sess.started:
	case <-time.After(2 * time.Second):
		t.Fatal("OpenFlow was not called")
	}

	start := time.Now()
	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 40*time.Millisecond {
		t.Fatalf("Close returned after %s, want to wait for the 50ms grace", elapsed)
	}

	done := make(chan struct{})
	go func() {
		srv.handlers.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("blocked OpenFlow handler did not finish after Close")
	}
	waitState(t, srv, func(st State) bool { return st.Active == 0 })
}

func TestFlowLogCarriesNodeTargetAndBytes(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	sess := &fakeSession{}
	srv := startServer(t, Config{NodeID: "node-log", AccountID: "acc-log", Logger: logger}, sess)

	c := dialGate(t, srv)
	connectOK(t, c, atypDomain, append([]byte{14}, []byte("secret.example")...), 443)
	echoOnce(t, c, []byte("hi"))
	_ = c.Close()
	waitState(t, srv, func(st State) bool { return st.Active == 0 })

	line := buf.String()
	for _, want := range []string{"gate flow closed", "node_id=node-log", "secret.example", "ingress_bytes=2", "egress_bytes=2", "duration="} {
		if !strings.Contains(line, want) {
			t.Fatalf("log line %q does not contain %q", line, want)
		}
	}
	lower := strings.ToLower(line)
	for _, banned := range []string{"password", "connectpassword", "token"} {
		if strings.Contains(lower, banned) {
			t.Fatalf("log line leaks %q: %s", banned, line)
		}
	}
}

// flakyListener injects Accept failures before delegating to a real listener.
type flakyListener struct {
	net.Listener

	mu   sync.Mutex
	errs []error
}

func (l *flakyListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	if len(l.errs) > 0 {
		err := l.errs[0]
		l.errs = l.errs[1:]
		l.mu.Unlock()
		return nil, err
	}
	l.mu.Unlock()
	return l.Listener.Accept()
}

func (l *flakyListener) injected() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.errs)
}

// TestAcceptErrorKeepsServing proves a transient Accept failure does not end
// the loop: the next connection is still served.
func TestAcceptErrorKeepsServing(t *testing.T) {
	base, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ln := &flakyListener{Listener: base, errs: []error{errors.New("accept: too many open files")}}

	sess := &fakeSession{}
	srv, err := NewServer(Config{NodeID: "node-1", AccountID: "acc-1", Limit: 4}, sess)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	srv.mu.Lock()
	srv.ln = ln
	srv.started = true
	srv.startedAt = time.Now()
	srv.mu.Unlock()
	go srv.serve(ln)
	t.Cleanup(func() { _ = srv.Close() })

	c, err := net.DialTimeout("tcp", base.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial gate: %v", err)
	}
	defer c.Close()
	connectOK(t, c, atypDomain, append([]byte{11}, []byte("example.com")...), 443)
	echoOnce(t, c, []byte("ping"))
	_ = c.Close()

	st := waitState(t, srv, func(st State) bool { return st.Accepted == 1 && st.Active == 0 })
	if st.Rejected != 0 {
		t.Fatalf("rejected = %d, want 0", st.Rejected)
	}
	if opens := sess.recorded(); len(opens) != 1 {
		t.Fatalf("OpenFlow calls = %v, want one", opens)
	}
	if remaining := ln.injected(); remaining != 0 {
		t.Fatalf("injected Accept errors remaining = %d", remaining)
	}
}
