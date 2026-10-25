package gate

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// startHTTPGate builds a server with both listeners on ephemeral loopback
// ports. The HTTP listener is enabled by setting HTTPListen.
func startHTTPGate(t *testing.T, cfg Config, sess Session) *Server {
	t.Helper()
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:0"
	}
	if cfg.HTTPListen == "" {
		cfg.HTTPListen = "127.0.0.1:0"
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

// dialHTTPGate dials the HTTP CONNECT listener and returns the connection plus
// the reader that owns it for the rest of the test.
func dialHTTPGate(t *testing.T, srv *Server) (net.Conn, *bufio.Reader) {
	t.Helper()
	addr := srv.HTTPAddr()
	if addr == nil {
		t.Fatal("HTTP listener is not bound")
	}
	conn, err := net.DialTimeout("tcp", addr.String(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial HTTP gate: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	return conn, bufio.NewReader(conn)
}

// basicAuthHeader renders one Basic Proxy-Authorization value.
func basicAuthHeader(user, pass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

// writeConnect sends a CONNECT request head. An empty auth omits the header.
func writeConnect(t *testing.T, w io.Writer, authority, auth string) {
	t.Helper()
	var request strings.Builder
	request.WriteString("CONNECT ")
	request.WriteString(authority)
	request.WriteString(" HTTP/1.1\r\nHost: ")
	request.WriteString(authority)
	request.WriteString("\r\n")
	if auth != "" {
		request.WriteString("Proxy-Authorization: ")
		request.WriteString(auth)
		request.WriteString("\r\n")
	}
	request.WriteString("\r\n")
	if _, err := io.WriteString(w, request.String()); err != nil {
		t.Fatalf("write CONNECT: %v", err)
	}
}

// readHTTPHead parses one response head. It never touches the tunnel bytes
// after the blank line; the caller keeps reading from the same reader.
func readHTTPHead(t *testing.T, r *bufio.Reader) (int, map[string]string) {
	t.Helper()
	line, err := r.ReadString('\n')
	if err != nil {
		t.Fatalf("read status line: %v", err)
	}
	fields := strings.SplitN(strings.TrimRight(line, "\r\n"), " ", 3)
	if len(fields) < 2 {
		t.Fatalf("malformed status line %q", line)
	}
	status, err := strconv.Atoi(fields[1])
	if err != nil {
		t.Fatalf("status line %q: %v", line, err)
	}
	headers := make(map[string]string)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("read header: %v", err)
		}
		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed == "" {
			return status, headers
		}
		name, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			t.Fatalf("malformed header %q", trimmed)
		}
		headers[strings.ToLower(name)] = strings.TrimSpace(value)
	}
}

// requireHTTPClosed proves the gate closed the connection after a refusal
// instead of keeping it open for another request.
func requireHTTPClosed(t *testing.T, r *bufio.Reader) {
	t.Helper()
	if _, err := r.ReadByte(); err == nil {
		t.Fatal("gate kept the connection open after a refusal")
	}
}

// waitServerState polls the counter snapshot until cond holds.
func waitServerState(t *testing.T, srv *Server, cond func(State) bool) State {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		state := srv.State()
		if cond(state) {
			return state
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for gate state; last = %+v", state)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// dialSession stands in for the vendor session: OpenFlow dials the configured
// loopback upstream and records every target the gate asked for.
type dialSession struct {
	addr string
	err  error

	mu    sync.Mutex
	opens []string
}

func (d *dialSession) OpenFlow(ctx context.Context, host string, port int) (net.Conn, error) {
	d.mu.Lock()
	d.opens = append(d.opens, net.JoinHostPort(host, strconv.Itoa(port)))
	err := d.err
	d.mu.Unlock()
	if err != nil {
		return nil, err
	}
	var dialer net.Dialer
	return dialer.DialContext(ctx, "tcp", d.addr)
}

func (d *dialSession) recorded() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.opens...)
}

// startTLSEcho starts a loopback TLS echo server with an ephemeral self-signed
// certificate and returns its address.
func startTLSEcho(t *testing.T) string {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{selfSignedCertificate(t)},
	})
	if err != nil {
		t.Fatalf("listen TLS echo: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return ln.Addr().String()
}

func selfSignedCertificate(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "gate-http-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"example.com"},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// TestHTTPConnectRelaysTLSAndMeters is the end-to-end acceptance row: valid
// Basic credentials, an allowlisted host, a real TLS handshake and echo through
// the tunnel, then one flow record and counter update with protocol "http".
func TestHTTPConnectRelaysTLSAndMeters(t *testing.T) {
	upstream := startTLSEcho(t)
	session := &dialSession{addr: upstream}
	policy := &fakePolicy{allowlist: CompileAllowlist([]string{"example.com"})}
	srv := startHTTPGate(t, Config{
		NodeID:        "acc-01:srv-01",
		AccountID:     "acc-01",
		AuthMode:      AuthUserPass,
		Authenticator: fakeAuthenticator{users: map[string]string{"u-01": "s3cret"}},
		UserPolicy:    policy,
	}, session)

	conn, reader := dialHTTPGate(t, srv)
	writeConnect(t, conn, "example.com:443", basicAuthHeader("u-01", "s3cret"))
	status, headers := readHTTPHead(t, reader)
	if status != 200 {
		t.Fatalf("CONNECT status = %d, want 200 (headers %v)", status, headers)
	}
	if _, ok := headers["proxy-authenticate"]; ok {
		t.Fatalf("success response carried a challenge: %v", headers)
	}

	tunnel := tls.Client(&bufferedClient{Conn: conn, r: reader}, &tls.Config{
		ServerName:         "example.com",
		InsecureSkipVerify: true,
	})
	if err := tunnel.Handshake(); err != nil {
		t.Fatalf("TLS handshake through CONNECT: %v", err)
	}
	payload := []byte("tls-bytes-through-the-gate")
	if _, err := tunnel.Write(payload); err != nil {
		t.Fatalf("write through tunnel: %v", err)
	}
	echo := make([]byte, len(payload))
	if _, err := io.ReadFull(tunnel, echo); err != nil {
		t.Fatalf("read through tunnel: %v", err)
	}
	if !bytes.Equal(echo, payload) {
		t.Fatalf("tunnel echo = %q, want %q", echo, payload)
	}
	_ = tunnel.Close()

	records := waitFlowRecords(t, srv, func(records []FlowRecord) bool {
		return len(records) == 1 && records[0].Status == FlowStatusOK
	})
	record := records[0]
	if record.Protocol != FlowProtocolHTTP {
		t.Fatalf("record protocol = %q, want %q", record.Protocol, FlowProtocolHTTP)
	}
	if record.Method != "CONNECT" || record.TargetHost != "example.com" || record.TargetPort != 443 {
		t.Fatalf("record target = %q %s:%d, want CONNECT example.com:443", record.Method, record.TargetHost, record.TargetPort)
	}
	if record.UserID != "u-01" || record.UserName != "name-u-01" {
		t.Fatalf("record attribution = %q/%q, want u-01/name-u-01", record.UserID, record.UserName)
	}
	if record.BytesIn < int64(len(payload)) || record.BytesOut < int64(len(payload)) {
		t.Fatalf("record bytes = %d/%d, want at least %d each way", record.BytesIn, record.BytesOut, len(payload))
	}
	if opens := session.recorded(); len(opens) != 1 || opens[0] != "example.com:443" {
		t.Fatalf("OpenFlow targets = %v, want [example.com:443]", opens)
	}
	state := srv.State()
	if state.Accepted != 1 || state.Rejected != 0 {
		t.Fatalf("state accepted/rejected = %d/%d, want 1/0", state.Accepted, state.Rejected)
	}
	if state.IngressBytes < uint64(len(payload)) || state.EgressBytes < uint64(len(payload)) {
		t.Fatalf("state bytes = %d/%d, want at least %d each way", state.IngressBytes, state.EgressBytes, len(payload))
	}
}

// TestHTTPConnectRequiresCredentials covers the 407 challenge: no header, a
// wrong secret, an unknown user, a malformed value and a non-Basic scheme all
// answer the same challenge, consume no policy call and open no flow.
func TestHTTPConnectRequiresCredentials(t *testing.T) {
	cases := []struct {
		name string
		auth string
	}{
		{"no credentials", ""},
		{"wrong password", basicAuthHeader("u-01", "wrong")},
		{"unknown user", basicAuthHeader("u-02", "s3cret")},
		{"not base64", "Basic !!!"},
		{"digest scheme", `Digest username="u-01"`},
		{"empty password", basicAuthHeader("u-01", "")},
		{"empty user", basicAuthHeader("", "s3cret")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			session := &fakeSession{}
			policy := &fakePolicy{allowlist: CompileAllowlist([]string{"example.com"})}
			srv := startHTTPGate(t, Config{
				AuthMode:      AuthUserPass,
				Authenticator: fakeAuthenticator{users: map[string]string{"u-01": "s3cret"}},
				UserPolicy:    policy,
			}, session)

			conn, reader := dialHTTPGate(t, srv)
			writeConnect(t, conn, "example.com:443", tc.auth)
			status, headers := readHTTPHead(t, reader)
			if status != 407 {
				t.Fatalf("status = %d, want 407", status)
			}
			if got := headers["proxy-authenticate"]; got != `Basic realm="apc"` {
				t.Fatalf("Proxy-Authenticate = %q, want %q", got, `Basic realm="apc"`)
			}
			requireHTTPClosed(t, reader)
			if calls := policy.count(); calls != 0 {
				t.Fatalf("policy calls = %d, want 0", calls)
			}
			if opens := session.recorded(); len(opens) != 0 {
				t.Fatalf("OpenFlow targets = %v, want none", opens)
			}
			records := waitFlowRecords(t, srv, func(records []FlowRecord) bool { return len(records) == 1 })
			if records[0].ErrorClass != FlowErrorAuthFailed || records[0].Protocol != FlowProtocolHTTP {
				t.Fatalf("record = %+v, want auth_failed over http", records[0])
			}
			if state := srv.State(); state.Active != 0 || state.Accepted != 0 {
				t.Fatalf("state = %+v, want no active or accepted flow", state)
			}
		})
	}
}

// TestHTTPConnectAllowlistDenied covers the 403 row: the authenticated user's
// policy refuses the destination before a slot or an upstream flow.
func TestHTTPConnectAllowlistDenied(t *testing.T) {
	session := &fakeSession{}
	srv := startHTTPGate(t, Config{
		AuthMode:      AuthUserPass,
		Authenticator: fakeAuthenticator{users: map[string]string{"u-01": "s3cret"}},
		UserPolicy:    &fakePolicy{allowlist: CompileAllowlist([]string{"example.com"})},
	}, session)

	conn, reader := dialHTTPGate(t, srv)
	writeConnect(t, conn, "denied.example:443", basicAuthHeader("u-01", "s3cret"))
	status, _ := readHTTPHead(t, reader)
	if status != 403 {
		t.Fatalf("status = %d, want 403", status)
	}
	requireHTTPClosed(t, reader)
	if opens := session.recorded(); len(opens) != 0 {
		t.Fatalf("OpenFlow targets = %v, want none", opens)
	}
	records := waitFlowRecords(t, srv, func(records []FlowRecord) bool { return len(records) == 1 })
	if records[0].ErrorClass != FlowErrorAllowlist || records[0].TargetHost != "denied.example" {
		t.Fatalf("record = %+v, want allowlist denial for denied.example", records[0])
	}
}

// TestHTTPConnectQuotaExhausted covers the 429 row for both quota stages. The
// Retry-After points at the next UTC month with a pinned clock.
func TestHTTPConnectQuotaExhausted(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	monthReset := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	wantRetry := strconv.Itoa(int(monthReset.Sub(now) / time.Second))

	cases := []struct {
		name   string
		policy UserPolicy
		quota  func(string) (bool, int64, int64)
		class  string
	}{
		{
			name:   "user quota",
			policy: &fakePolicy{class: FlowErrorUserQuota, used: 100, budget: 100},
			class:  FlowErrorUserQuota,
		},
		{
			name:   "account quota",
			policy: &fakePolicy{allowlist: CompileAllowlist([]string{"example.com"})},
			quota:  func(string) (bool, int64, int64) { return false, 50, 50 },
			class:  FlowErrorQuota,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			session := &fakeSession{}
			srv := startHTTPGate(t, Config{
				AccountID:     "acc-01",
				Now:           func() time.Time { return now },
				AuthMode:      AuthUserPass,
				Authenticator: fakeAuthenticator{users: map[string]string{"u-01": "s3cret"}},
				UserPolicy:    tc.policy,
				QuotaCheck:    tc.quota,
			}, session)

			conn, reader := dialHTTPGate(t, srv)
			writeConnect(t, conn, "example.com:443", basicAuthHeader("u-01", "s3cret"))
			status, headers := readHTTPHead(t, reader)
			if status != 429 {
				t.Fatalf("status = %d, want 429", status)
			}
			if got := headers["retry-after"]; got != wantRetry {
				t.Fatalf("Retry-After = %q, want %q", got, wantRetry)
			}
			requireHTTPClosed(t, reader)
			if opens := session.recorded(); len(opens) != 0 {
				t.Fatalf("OpenFlow targets = %v, want none", opens)
			}
			records := waitFlowRecords(t, srv, func(records []FlowRecord) bool { return len(records) == 1 })
			if records[0].ErrorClass != tc.class {
				t.Fatalf("record class = %q, want %q", records[0].ErrorClass, tc.class)
			}
			if state := srv.State(); state.Active != 0 || state.Accepted != 0 {
				t.Fatalf("state = %+v, want no active or accepted flow", state)
			}
		})
	}
}

// TestHTTPConnectRejectsOtherMethodsAndMalformedRequests covers 405 and 400,
// including the Allow header and the parser bounds.
func TestHTTPConnectRejectsOtherMethodsAndMalformedRequests(t *testing.T) {
	cases := []struct {
		name    string
		request string
		status  int
		allow   bool
	}{
		{"GET", "GET http://example.com/ HTTP/1.1\r\nHost: example.com\r\n\r\n", 405, true},
		{"POST", "POST / HTTP/1.1\r\nHost: example.com\r\n\r\n", 405, true},
		{"missing port", "CONNECT example.com HTTP/1.1\r\nHost: example.com\r\n\r\n", 400, false},
		{"empty host", "CONNECT :443 HTTP/1.1\r\nHost: example.com\r\n\r\n", 400, false},
		{"zero port", "CONNECT example.com:0 HTTP/1.1\r\nHost: example.com\r\n\r\n", 400, false},
		{"non-numeric port", "CONNECT example.com:https HTTP/1.1\r\nHost: example.com\r\n\r\n", 400, false},
		{"URL authority", "CONNECT http://example.com:443 HTTP/1.1\r\nHost: example.com\r\n\r\n", 400, false},
		{"userinfo authority", "CONNECT user@example.com:443 HTTP/1.1\r\nHost: example.com\r\n\r\n", 400, false},
		{"HTTP/2.0", "CONNECT example.com:443 HTTP/2.0\r\nHost: example.com\r\n\r\n", 400, false},
		{"empty request line", "\r\n\r\n", 400, false},
		{"garbage", "NONSENSE\r\n\r\n", 400, false},
		{"header without colon", "CONNECT example.com:443 HTTP/1.1\r\nBroken\r\n\r\n", 400, false},
		{"folded header", "CONNECT example.com:443 HTTP/1.1\r\nX-A: b\r\n c\r\n\r\n", 400, false},
		{"space before colon", "CONNECT example.com:443 HTTP/1.1\r\nX-A : b\r\n\r\n", 400, false},
		{"duplicate auth", "CONNECT example.com:443 HTTP/1.1\r\nProxy-Authorization: Basic eA==\r\nProxy-Authorization: Basic eA==\r\n\r\n", 400, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			session := &fakeSession{}
			srv := startHTTPGate(t, Config{}, session)
			conn, reader := dialHTTPGate(t, srv)
			if _, err := io.WriteString(conn, tc.request); err != nil {
				t.Fatalf("write request: %v", err)
			}
			status, headers := readHTTPHead(t, reader)
			if status != tc.status {
				t.Fatalf("status = %d, want %d", status, tc.status)
			}
			if tc.allow && headers["allow"] != "CONNECT" {
				t.Fatalf("Allow = %q, want CONNECT", headers["allow"])
			}
			requireHTTPClosed(t, reader)
			if opens := session.recorded(); len(opens) != 0 {
				t.Fatalf("OpenFlow targets = %v, want none", opens)
			}
		})
	}

	t.Run("oversized header", func(t *testing.T) {
		srv := startHTTPGate(t, Config{}, &fakeSession{})
		conn, reader := dialHTTPGate(t, srv)
		request := "CONNECT example.com:443 HTTP/1.1\r\nX-Filler: " +
			strings.Repeat("a", httpMaxHeaderBytes) + "\r\n\r\n"
		if _, err := io.WriteString(conn, request); err != nil {
			t.Fatalf("write request: %v", err)
		}
		if status, _ := readHTTPHead(t, reader); status != 400 {
			t.Fatalf("status = %d, want 400", status)
		}
	})
}

// TestHTTPConnectOpenFlowFailure covers 502: the upstream could not be opened,
// so the client never sees 200 and the record names open_flow.
func TestHTTPConnectOpenFlowFailure(t *testing.T) {
	session := &dialSession{err: errors.New("upstream refused")}
	srv := startHTTPGate(t, Config{
		AuthMode:      AuthUserPass,
		Authenticator: fakeAuthenticator{users: map[string]string{"u-01": "s3cret"}},
		UserPolicy:    &fakePolicy{allowlist: CompileAllowlist([]string{"example.com"})},
	}, session)

	conn, reader := dialHTTPGate(t, srv)
	writeConnect(t, conn, "example.com:443", basicAuthHeader("u-01", "s3cret"))
	if status, _ := readHTTPHead(t, reader); status != 502 {
		t.Fatalf("status = %d, want 502", status)
	}
	records := waitFlowRecords(t, srv, func(records []FlowRecord) bool { return len(records) == 1 })
	if records[0].ErrorClass != FlowErrorOpenFlow || records[0].Status != FlowStatusRejected {
		t.Fatalf("record = %+v, want an open_flow rejection", records[0])
	}
	// finishFlow runs after the record is written, so wait for the slot to be
	// released before asserting the counter snapshot.
	state := waitServerState(t, srv, func(state State) bool { return state.Active == 0 })
	if state.Accepted != 0 {
		t.Fatalf("state = %+v, want no accepted flow", state)
	}
}

// TestHTTPConnectRelaysPipelinedBytes proves the bytes a client sends behind
// the request head (the TLS ClientHello in practice) reach the tunnel.
func TestHTTPConnectRelaysPipelinedBytes(t *testing.T) {
	srv := startHTTPGate(t, Config{}, &fakeSession{})
	conn, reader := dialHTTPGate(t, srv)
	payload := []byte("pipelined-payload")
	request := "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com\r\n\r\n"
	if _, err := io.WriteString(conn, request+string(payload)); err != nil {
		t.Fatalf("write pipelined request: %v", err)
	}
	if status, _ := readHTTPHead(t, reader); status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	echo := make([]byte, len(payload))
	if _, err := io.ReadFull(reader, echo); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if !bytes.Equal(echo, payload) {
		t.Fatalf("echo = %q, want %q", echo, payload)
	}
}

// TestHTTPConnectLoopbackModeNeedsNoCredentials pins the compatibility path:
// without userpass the listener trusts a loopback client and ignores a
// supplied Proxy-Authorization, leaving attribution empty.
func TestHTTPConnectLoopbackModeNeedsNoCredentials(t *testing.T) {
	srv := startHTTPGate(t, Config{}, &fakeSession{})
	for _, auth := range []string{"", basicAuthHeader("ignored", "ignored")} {
		conn, reader := dialHTTPGate(t, srv)
		writeConnect(t, conn, "example.com:443", auth)
		if status, _ := readHTTPHead(t, reader); status != 200 {
			t.Fatalf("status = %d, want 200", status)
		}
		if _, err := conn.Write([]byte("ping")); err != nil {
			t.Fatalf("write: %v", err)
		}
		echo := make([]byte, 4)
		if _, err := io.ReadFull(reader, echo); err != nil {
			t.Fatalf("read echo: %v", err)
		}
		if string(echo) != "ping" {
			t.Fatalf("echo = %q, want ping", echo)
		}
		_ = conn.Close()
	}
	records := waitFlowRecords(t, srv, func(records []FlowRecord) bool { return len(records) == 2 })
	for _, record := range records {
		if record.UserID != "" || record.Protocol != FlowProtocolHTTP || record.Status != FlowStatusOK {
			t.Fatalf("record = %+v, want an unattributed http success", record)
		}
	}
}

// TestHTTPConnectDrainingAnswers503 proves Drain refuses new HTTP flows while
// the listener stays bound.
func TestHTTPConnectDrainingAnswers503(t *testing.T) {
	srv := startHTTPGate(t, Config{}, &fakeSession{})
	if err := srv.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	conn, reader := dialHTTPGate(t, srv)
	writeConnect(t, conn, "example.com:443", "")
	if status, _ := readHTTPHead(t, reader); status != 503 {
		t.Fatalf("status = %d, want 503", status)
	}
	records := waitFlowRecords(t, srv, func(records []FlowRecord) bool { return len(records) == 1 })
	if records[0].ErrorClass != FlowErrorDraining {
		t.Fatalf("record class = %q, want %q", records[0].ErrorClass, FlowErrorDraining)
	}
}

// TestHTTPConnectCloseStopsListenerAndTunnel covers shutdown: Close unbinds the
// HTTP port and force-closes an active tunnel after the grace.
func TestHTTPConnectCloseStopsListenerAndTunnel(t *testing.T) {
	srv := startHTTPGate(t, Config{DrainGrace: 50 * time.Millisecond}, &fakeSession{})
	addr := srv.HTTPAddr().String()

	conn, reader := dialHTTPGate(t, srv)
	writeConnect(t, conn, "example.com:443", "")
	if status, _ := readHTTPHead(t, reader); status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	if _, err := conn.Write([]byte("x")); err != nil {
		t.Fatalf("write: %v", err)
	}
	one := make([]byte, 1)
	if _, err := io.ReadFull(reader, one); err != nil {
		t.Fatalf("read echo: %v", err)
	}

	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := reader.ReadByte(); err == nil {
		t.Fatal("tunnel survived Close")
	}
	if c, err := net.DialTimeout("tcp", addr, 500*time.Millisecond); err == nil {
		_ = c.Close()
		t.Fatal("HTTP listener still accepted a connection after Close")
	}
}

// TestHTTPConnectProbeCounted proves a dial-and-close on the HTTP port is a
// probe, not a rejection.
func TestHTTPConnectProbeCounted(t *testing.T) {
	srv := startHTTPGate(t, Config{}, &fakeSession{})
	conn, _ := dialHTTPGate(t, srv)
	_ = conn.Close()
	waitServerState(t, srv, func(state State) bool { return state.Probes == 1 && state.Rejected == 0 })
}

// TestHTTPNonLoopbackPeerRejected refuses an injected non-loopback peer before
// it can send a request head.
func TestHTTPNonLoopbackPeerRejected(t *testing.T) {
	srv := startHTTPGate(t, Config{}, &fakeSession{})
	client, server := net.Pipe()
	defer client.Close()
	go srv.handleHTTP(&peerConn{
		Conn:   server,
		remote: &net.TCPAddr{IP: net.IPv4(203, 0, 113, 9), Port: 40000},
	})
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	var one [1]byte
	if n, err := client.Read(one[:]); err == nil || n != 0 {
		t.Fatalf("non-loopback peer received %d byte(s), err = %v", n, err)
	}
	records := waitFlowRecords(t, srv, func(records []FlowRecord) bool { return len(records) == 1 })
	if records[0].ErrorClass != FlowErrorNonLoopbackPeer || records[0].Protocol != FlowProtocolHTTP {
		t.Fatalf("record = %+v, want non_loopback_peer over http", records[0])
	}
}

// TestHTTPListenValidation pins the loopback-only rule for the second listener.
func TestHTTPListenValidation(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:0", ":0", "example.com:0", "[::]:0"} {
		if _, err := NewServer(Config{HTTPListen: addr}, &fakeSession{}); err == nil {
			t.Fatalf("NewServer accepted HTTPListen %q, want a loopback error", addr)
		}
	}
	srv, err := NewServer(Config{HTTPListen: "127.0.0.1:0"}, &fakeSession{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if srv.HTTPAddr() != nil {
		t.Fatal("HTTPAddr is set before Start")
	}
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = srv.Close() }()
	bound, ok := srv.HTTPAddr().(*net.TCPAddr)
	if !ok || !bound.IP.IsLoopback() || bound.Port == 0 {
		t.Fatalf("HTTPAddr = %v, want a bound loopback port", srv.HTTPAddr())
	}
}

// TestHTTPStartFailsAtomically proves a failed HTTP bind releases the SOCKS5
// port instead of leaving a half-bound node.
func TestHTTPStartFailsAtomically(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy HTTP port: %v", err)
	}
	defer taken.Close()

	srv, err := NewServer(Config{Listen: "127.0.0.1:0", HTTPListen: taken.Addr().String()}, &fakeSession{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if err := srv.Start(context.Background()); err == nil {
		_ = srv.Close()
		t.Fatal("Start succeeded with an occupied HTTP port")
	}
	if srv.Addr() != nil || srv.HTTPAddr() != nil {
		t.Fatalf("a failed Start left listeners bound: socks=%v http=%v", srv.Addr(), srv.HTTPAddr())
	}
}

// TestHTTPConnectNeverLogsCredentials proves a rejected Proxy-Authorization
// value never reaches a log line: a malformed header is logged by name only,
// and a wrong secret is logged as one generic auth failure.
func TestHTTPConnectNeverLogsCredentials(t *testing.T) {
	logs := &captureHandler{}
	srv := startHTTPGate(t, Config{
		AuthMode:      AuthUserPass,
		Authenticator: fakeAuthenticator{users: map[string]string{"u-01": "s3cret"}},
		UserPolicy:    &fakePolicy{allowlist: CompileAllowlist([]string{"example.com"})},
		Logger:        slog.New(logs),
	}, &fakeSession{})

	secret := "sup3r-secret-value"
	encoded := base64.StdEncoding.EncodeToString([]byte("u-01:" + secret))
	headers := []string{
		"Proxy-Authorization: Basic " + encoded,    // wrong secret
		"Proxy-Authorization Basic " + encoded,     // malformed: no colon
		"Proxy-Authorization: Basic !!!" + encoded, // malformed base64
	}
	for _, header := range headers {
		conn, reader := dialHTTPGate(t, srv)
		request := "CONNECT example.com:443 HTTP/1.1\r\n" + header + "\r\n\r\n"
		if _, err := io.WriteString(conn, request); err != nil {
			t.Fatalf("write request: %v", err)
		}
		if status, _ := readHTTPHead(t, reader); status != 400 && status != 407 {
			t.Fatalf("status = %d, want 400 or 407", status)
		}
	}

	deadline := time.Now().Add(3 * time.Second)
	for len(logs.snapshot()) < len(headers) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	records := logs.snapshot()
	if len(records) < len(headers) {
		t.Fatalf("captured %d log records, want at least %d", len(records), len(headers))
	}
	for _, record := range records {
		var line bytes.Buffer
		if err := slog.NewTextHandler(&line, nil).Handle(context.Background(), record); err != nil {
			t.Fatalf("render log record: %v", err)
		}
		for _, leaked := range []string{secret, encoded, "Basic"} {
			if strings.Contains(line.String(), leaked) {
				t.Fatalf("log line leaked %q: %s", leaked, line.String())
			}
		}
	}
}

// TestHTTPConnectRefusesSocks5Bytes proves the HTTP listener does not answer a
// SOCKS5 greeting: it is a separate protocol surface.
func TestHTTPConnectRefusesSocks5Bytes(t *testing.T) {
	srv := startHTTPGate(t, Config{}, &fakeSession{})
	conn, reader := dialHTTPGate(t, srv)
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00, '\r', '\n', '\r', '\n'}); err != nil {
		t.Fatalf("write greeting: %v", err)
	}
	if status, _ := readHTTPHead(t, reader); status != 400 {
		t.Fatalf("status = %d, want 400", status)
	}
}
