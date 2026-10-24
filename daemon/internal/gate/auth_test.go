package gate

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeAuthenticator resolves the RFC 1929 pairs a wire test offers. A revoked
// entry models a key that was revoked after it was issued.
type fakeAuthenticator struct {
	users   map[string]string
	revoked map[string]bool
}

func (f fakeAuthenticator) Authenticate(user, pass string) (Principal, bool) {
	want, ok := f.users[user]
	if !ok || f.revoked[user] || want != pass {
		return Principal{}, false
	}
	return Principal{UserID: user, UserName: "name-" + user}, true
}

// fakePolicy is the per-user policy double. class forces a denial so the gate's
// ordering and reply mapping can be tested without a store.
type fakePolicy struct {
	mu        sync.Mutex
	allowlist Allowlist
	class     string
	used      int64
	budget    int64
	calls     int
}

func (p *fakePolicy) Admit(principal Principal, host string) (bool, string, int64, int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.class != "" {
		return false, p.class, p.used, p.budget
	}
	if !p.allowlist.Match(host) {
		return false, FlowErrorAllowlist, 0, 0
	}
	return true, "", p.used, p.budget
}

func (p *fakePolicy) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// authFrame sends one RFC 1929 frame and returns the server's status byte.
func authFrame(t *testing.T, c net.Conn, user, pass string) byte {
	t.Helper()
	frame := []byte{authVersion, byte(len(user))}
	frame = append(frame, user...)
	frame = append(frame, byte(len(pass)))
	frame = append(frame, pass...)
	if _, err := c.Write(frame); err != nil {
		t.Fatalf("write auth frame: %v", err)
	}
	var reply [2]byte
	if _, err := io.ReadFull(c, reply[:]); err != nil {
		t.Fatalf("read auth reply: %v", err)
	}
	if reply[0] != authVersion {
		t.Fatalf("auth reply version = %d, want 1", reply[0])
	}
	return reply[1]
}

// userPassConnect performs greeting, RFC 1929 authentication and CONNECT, and
// returns the SOCKS5 reply code.
func userPassConnect(t *testing.T, c net.Conn, user, pass string, atyp byte, addr []byte, port int) byte {
	t.Helper()
	if method := greet(t, c, authUserPass); method != authUserPass {
		t.Fatalf("selected method = %#x, want 0x02", method)
	}
	if status := authFrame(t, c, user, pass); status != authStatusOK {
		t.Fatalf("auth status = %#x, want 0x00", status)
	}
	connectRequest(t, c, cmdConnect, atyp, addr, port)
	return readReply(t, c)
}

// requireClosedWithoutReply proves the server sent nothing after its protocol
// reply: the next read is EOF, not an invented SOCKS5 frame.
func requireClosedWithoutReply(t *testing.T, c net.Conn) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	var one [1]byte
	n, err := c.Read(one[:])
	if err == nil {
		t.Fatalf("server sent %d unexpected byte(s) after the handshake reply", n)
	}
	if n != 0 {
		t.Fatalf("server sent %d byte(s) before closing", n)
	}
}

// TestParseAuthMode pins the AJS_GATE_AUTH contract: unset keeps the loopback
// compatibility mode, both documented values parse, and anything else aborts
// construction instead of silently weakening the listener.
func TestParseAuthMode(t *testing.T) {
	cases := []struct {
		raw  string
		want AuthMode
	}{
		{"", AuthLoopback},
		{" loopback ", AuthLoopback},
		{"userpass", AuthUserPass},
	}
	for _, tc := range cases {
		got, err := ParseAuthMode(tc.raw)
		if err != nil {
			t.Fatalf("ParseAuthMode(%q): %v", tc.raw, err)
		}
		if got != tc.want {
			t.Fatalf("ParseAuthMode(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
	for _, raw := range []string{"none", "no-auth", "USERPASS", "user-pass", "loopback,userpass"} {
		if _, err := ParseAuthMode(raw); err == nil {
			t.Fatalf("ParseAuthMode(%q) accepted an unknown mode", raw)
		}
	}
	if _, err := NewServer(Config{AuthMode: AuthMode("bogus")}, &fakeSession{}); err == nil {
		t.Fatal("NewServer accepted an unknown auth mode")
	}
}

// TestUserPassRelayAndAttribution is acceptance row 1: valid credential and
// allowed host negotiate 05 02 then 01 00, admit exactly one OpenFlow, relay,
// and carry the user identity plus the actual bytes on the flow record.
func TestUserPassRelayAndAttribution(t *testing.T) {
	const secret = "ajs_live_row1_secret"
	session := &fakeSession{}
	policy := &fakePolicy{allowlist: CompileAllowlist([]string{"example.com"})}
	srv := startServer(t, Config{
		NodeID:        "node-row1",
		AccountID:     "acc-01",
		AuthMode:      AuthUserPass,
		Authenticator: fakeAuthenticator{users: map[string]string{"u-01": secret}},
		UserPolicy:    policy,
		QuotaCheck:    func(string) (bool, int64, int64) { return true, 0, 0 },
	}, session)

	client := dialGate(t, srv)
	if method := greet(t, client, authUserPass, authNone); method != authUserPass {
		t.Fatalf("selected method = %#x, want 0x02", method)
	}
	if status := authFrame(t, client, "u-01", secret); status != authStatusOK {
		t.Fatalf("auth status = %#x, want 0x00", status)
	}
	// Mixed case and a terminal dot must canonicalize to the allowlist entry.
	connectRequest(t, client, cmdConnect, atypDomain, domainAddr("EXAMPLE.com."), 443)
	if rep := readReply(t, client); rep != repSucceeded {
		t.Fatalf("CONNECT reply = %#x, want success", rep)
	}
	echoOnce(t, client, []byte("row-one"))
	_ = client.Close()

	records := waitFlowRecords(t, srv, func(records []FlowRecord) bool {
		return len(records) == 1 && records[0].Status == FlowStatusOK
	})
	record := records[0]
	if record.TargetHost != "example.com" {
		t.Fatalf("target host = %q, want the canonical example.com", record.TargetHost)
	}
	if record.UserID != "u-01" || record.UserName != "name-u-01" {
		t.Fatalf("flow attribution = %q/%q, want u-01/name-u-01", record.UserID, record.UserName)
	}
	if record.BytesIn != int64(len("row-one")) || record.BytesOut != int64(len("row-one")) {
		t.Fatalf("flow bytes = %d/%d, want %d/%d", record.BytesIn, record.BytesOut,
			len("row-one"), len("row-one"))
	}
	if opens := session.recorded(); len(opens) != 1 || opens[0] != "example.com:443" {
		t.Fatalf("OpenFlow calls = %v, want one example.com:443", opens)
	}
	if policy.count() != 1 {
		t.Fatalf("user policy calls = %d, want 1", policy.count())
	}
}

// TestUserPassRejectsBadCredentials is acceptance row 2: unknown user, wrong
// secret, revoked key and another user's key all get 05 02 then 01 01 and a
// close, with no attribution, no SOCKS5 reply and no upstream.
func TestUserPassRejectsBadCredentials(t *testing.T) {
	const (
		secretOne = "ajs_live_user_one"
		secretTwo = "ajs_live_user_two"
	)
	session := &fakeSession{}
	srv := startServer(t, Config{
		NodeID:     "node-row2",
		AuthMode:   AuthUserPass,
		UserPolicy: &fakePolicy{allowlist: CompileAllowlist([]string{"example.com"})},
		Authenticator: fakeAuthenticator{
			users:   map[string]string{"u-01": secretOne, "u-02": secretTwo},
			revoked: map[string]bool{"u-02": true},
		},
	}, session)

	cases := []struct {
		name string
		user string
		pass string
	}{
		{"unknown user", "u-99", secretOne},
		{"wrong secret", "u-01", "not-the-secret"},
		{"revoked key", "u-02", secretTwo},
		{"another user's key", "u-01", secretTwo},
	}
	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := dialGate(t, srv)
			if method := greet(t, client, authUserPass); method != authUserPass {
				t.Fatalf("selected method = %#x, want 0x02", method)
			}
			if status := authFrame(t, client, tc.user, tc.pass); status != authStatusFailed {
				t.Fatalf("auth status = %#x, want 0x01", status)
			}
			requireClosedWithoutReply(t, client)

			records := waitFlowRecords(t, srv, func(records []FlowRecord) bool {
				return len(records) == index+1
			})
			record := records[0]
			if record.ErrorClass != FlowErrorAuthFailed {
				t.Fatalf("errorClass = %q, want %q", record.ErrorClass, FlowErrorAuthFailed)
			}
			if record.UserID != "" || record.UserName != "" {
				t.Fatalf("rejected flow attributed to %q/%q, want empty", record.UserID, record.UserName)
			}
			if record.Method != "" || record.TargetHost != "" {
				t.Fatalf("rejected flow invented a target: %+v", record)
			}
		})
	}
	if opens := session.recorded(); len(opens) != 0 {
		t.Fatalf("OpenFlow calls = %v, want none", opens)
	}
}

// TestUserPassRequiresUserPassMethod is acceptance row 3: a client that offers
// only no-auth gets 05 FF, a close and the auth_method class.
func TestUserPassRequiresUserPassMethod(t *testing.T) {
	session := &fakeSession{}
	srv := startServer(t, Config{
		NodeID:        "node-row3",
		AuthMode:      AuthUserPass,
		Authenticator: fakeAuthenticator{users: map[string]string{"u-01": "secret"}},
		UserPolicy:    &fakePolicy{allowlist: CompileAllowlist([]string{"example.com"})},
	}, session)

	client := dialGate(t, srv)
	if method := greet(t, client, authNone); method != authNoAcceptable {
		t.Fatalf("selected method = %#x, want 0xFF", method)
	}
	requireClosedWithoutReply(t, client)

	records := waitFlowRecords(t, srv, func(records []FlowRecord) bool { return len(records) == 1 })
	if records[0].ErrorClass != FlowErrorAuthMethod {
		t.Fatalf("errorClass = %q, want %q", records[0].ErrorClass, FlowErrorAuthMethod)
	}
	if opens := session.recorded(); len(opens) != 0 {
		t.Fatalf("OpenFlow calls = %v, want none", opens)
	}
}

// TestUserPassMalformedAuthFrames is acceptance row 4a: a complete but invalid
// RFC 1929 frame is answered with 01 01 and closed.
func TestUserPassMalformedAuthFrames(t *testing.T) {
	session := &fakeSession{}
	srv := startServer(t, Config{
		NodeID:        "node-row4a",
		AuthMode:      AuthUserPass,
		Authenticator: fakeAuthenticator{users: map[string]string{"u-01": "secret"}},
		UserPolicy:    &fakePolicy{allowlist: CompileAllowlist([]string{"example.com"})},
	}, session)

	frames := []struct {
		name  string
		frame []byte
	}{
		{"wrong version", []byte{0x05, 0x01, 'u', 0x01, 'p'}},
		{"empty username", []byte{authVersion, 0x00}},
		{"empty password", []byte{authVersion, 0x01, 'u', 0x00}},
	}
	for index, tc := range frames {
		t.Run(tc.name, func(t *testing.T) {
			client := dialGate(t, srv)
			if method := greet(t, client, authUserPass); method != authUserPass {
				t.Fatalf("selected method = %#x, want 0x02", method)
			}
			if _, err := client.Write(tc.frame); err != nil {
				t.Fatalf("write malformed frame: %v", err)
			}
			var reply [2]byte
			if _, err := io.ReadFull(client, reply[:]); err != nil {
				t.Fatalf("read auth reply: %v", err)
			}
			if reply[0] != authVersion || reply[1] != authStatusFailed {
				t.Fatalf("auth reply = % x, want 01 01", reply)
			}
			requireClosedWithoutReply(t, client)
			records := waitFlowRecords(t, srv, func(records []FlowRecord) bool {
				return len(records) == index+1
			})
			if records[0].ErrorClass != FlowErrorAuthFailed {
				t.Fatalf("errorClass = %q, want %q", records[0].ErrorClass, FlowErrorAuthFailed)
			}
		})
	}
	if opens := session.recorded(); len(opens) != 0 {
		t.Fatalf("OpenFlow calls = %v, want none", opens)
	}
}

// TestUserPassTruncatedFramesCloseWithoutReply is acceptance row 4b: a
// truncated auth frame or CONNECT request closes without an invented response.
func TestUserPassTruncatedFramesCloseWithoutReply(t *testing.T) {
	srv := startServer(t, Config{
		NodeID:        "node-row4b",
		AuthMode:      AuthUserPass,
		Authenticator: fakeAuthenticator{users: map[string]string{"u-01": "secret"}},
		UserPolicy:    &fakePolicy{allowlist: CompileAllowlist([]string{"example.com"})},
	}, &fakeSession{})

	t.Run("truncated auth frame", func(t *testing.T) {
		client := dialGate(t, srv)
		if method := greet(t, client, authUserPass); method != authUserPass {
			t.Fatalf("selected method = %#x, want 0x02", method)
		}
		if _, err := client.Write([]byte{authVersion, 0x04, 'u', 's'}); err != nil {
			t.Fatalf("write partial auth frame: %v", err)
		}
		tcp, ok := client.(*net.TCPConn)
		if !ok {
			t.Fatalf("gate dial is not TCP: %T", client)
		}
		if err := tcp.CloseWrite(); err != nil {
			t.Fatalf("close write half: %v", err)
		}
		requireClosedWithoutReply(t, client)
	})

	t.Run("truncated CONNECT", func(t *testing.T) {
		client := dialGate(t, srv)
		if method := greet(t, client, authUserPass); method != authUserPass {
			t.Fatalf("selected method = %#x, want 0x02", method)
		}
		if status := authFrame(t, client, "u-01", "secret"); status != authStatusOK {
			t.Fatalf("auth status = %#x, want 0x00", status)
		}
		if _, err := client.Write([]byte{socks5Version, cmdConnect}); err != nil {
			t.Fatalf("write partial CONNECT: %v", err)
		}
		tcp, ok := client.(*net.TCPConn)
		if !ok {
			t.Fatalf("gate dial is not TCP: %T", client)
		}
		if err := tcp.CloseWrite(); err != nil {
			t.Fatalf("close write half: %v", err)
		}
		requireClosedWithoutReply(t, client)
	})
}

// TestUserPassAuthDeadlineCloses is acceptance row 4c: a client that stops
// after the method reply is closed by the auth deadline, not held forever.
func TestUserPassAuthDeadlineCloses(t *testing.T) {
	srv := startServer(t, Config{
		NodeID:        "node-row4c",
		AuthMode:      AuthUserPass,
		AuthTimeout:   50 * time.Millisecond,
		Authenticator: fakeAuthenticator{users: map[string]string{"u-01": "secret"}},
		UserPolicy:    &fakePolicy{allowlist: CompileAllowlist([]string{"example.com"})},
	}, &fakeSession{})

	client := dialGate(t, srv)
	if method := greet(t, client, authUserPass); method != authUserPass {
		t.Fatalf("selected method = %#x, want 0x02", method)
	}
	started := time.Now()
	requireClosedWithoutReply(t, client)
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("auth deadline took %s, want about 50ms", elapsed)
	}
	records := waitFlowRecords(t, srv, func(records []FlowRecord) bool { return len(records) == 1 })
	if records[0].ErrorClass != FlowErrorAuthFailed {
		t.Fatalf("errorClass = %q, want %q", records[0].ErrorClass, FlowErrorAuthFailed)
	}
}

// TestUserPassNilAuthenticatorFailsClosed proves an unconfigured credential
// store refuses every handshake instead of falling back to no-auth.
func TestUserPassNilAuthenticatorFailsClosed(t *testing.T) {
	session := &fakeSession{}
	srv := startServer(t, Config{NodeID: "node-nil-auth", AuthMode: AuthUserPass}, session)

	client := dialGate(t, srv)
	if method := greet(t, client, authUserPass); method != authUserPass {
		t.Fatalf("selected method = %#x, want 0x02", method)
	}
	if status := authFrame(t, client, "u-01", "secret"); status != authStatusFailed {
		t.Fatalf("auth status = %#x, want 0x01", status)
	}
	requireClosedWithoutReply(t, client)
	records := waitFlowRecords(t, srv, func(records []FlowRecord) bool { return len(records) == 1 })
	if records[0].ErrorClass != FlowErrorAuthUnavailable {
		t.Fatalf("errorClass = %q, want %q", records[0].ErrorClass, FlowErrorAuthUnavailable)
	}
	if opens := session.recorded(); len(opens) != 0 {
		t.Fatalf("OpenFlow calls = %v, want none", opens)
	}
}

// TestLoopbackModeKeepsNoAuthWire is acceptance row 5: the compatibility path
// still negotiates 05 00, relays, leaves user fields empty and runs the account
// quota.
func TestLoopbackModeKeepsNoAuthWire(t *testing.T) {
	session := &fakeSession{}
	quotaCalls := 0
	srv := startServer(t, Config{
		NodeID:     "node-loopback",
		AuthMode:   AuthLoopback,
		QuotaCheck: func(string) (bool, int64, int64) { quotaCalls++; return true, 10, 100 },
	}, session)

	client := dialGate(t, srv)
	if method := greet(t, client, authNone, authUserPass); method != authNone {
		t.Fatalf("selected method = %#x, want 0x00", method)
	}
	connectRequest(t, client, cmdConnect, atypDomain, domainAddr("example.com"), 443)
	if rep := readReply(t, client); rep != repSucceeded {
		t.Fatalf("CONNECT reply = %#x, want success", rep)
	}
	echoOnce(t, client, []byte("legacy"))
	_ = client.Close()

	records := waitFlowRecords(t, srv, func(records []FlowRecord) bool {
		return len(records) == 1 && records[0].Status == FlowStatusOK
	})
	if records[0].UserID != "" || records[0].UserName != "" {
		t.Fatalf("loopback flow attributed to %q/%q, want empty", records[0].UserID, records[0].UserName)
	}
	if quotaCalls != 1 {
		t.Fatalf("account quota calls = %d, want 1", quotaCalls)
	}
	if opens := session.recorded(); len(opens) != 1 {
		t.Fatalf("OpenFlow calls = %v, want one", opens)
	}
}

// TestLoopbackModeStillEnforcesAccountQuota proves the compatibility path keeps
// the account admission check.
func TestLoopbackModeStillEnforcesAccountQuota(t *testing.T) {
	session := &fakeSession{}
	srv := startServer(t, Config{
		NodeID:     "node-loopback-quota",
		AuthMode:   AuthLoopback,
		QuotaCheck: func(string) (bool, int64, int64) { return false, 120, 100 },
	}, session)

	client := dialGate(t, srv)
	greet(t, client, authNone)
	connectRequest(t, client, cmdConnect, atypDomain, domainAddr("example.com"), 443)
	if rep := readReply(t, client); rep != repNotAllowed {
		t.Fatalf("CONNECT reply = %#x, want 0x02", rep)
	}
	records := waitFlowRecords(t, srv, func(records []FlowRecord) bool { return len(records) == 1 })
	if records[0].ErrorClass != FlowErrorQuota {
		t.Fatalf("errorClass = %q, want %q", records[0].ErrorClass, FlowErrorQuota)
	}
	if records[0].UserID != "" {
		t.Fatalf("loopback rejection carried user %q, want empty", records[0].UserID)
	}
	if opens := session.recorded(); len(opens) != 0 {
		t.Fatalf("OpenFlow calls = %v, want none", opens)
	}
}

// TestLoopbackBindOnly is acceptance row 7: only a literal loopback listen
// address is accepted, in both auth modes, and a valid control starts.
func TestLoopbackBindOnly(t *testing.T) {
	invalid := []string{
		":0",
		"0.0.0.0:0",
		"[::]:0",
		"192.168.1.10:0",
		"203.0.113.9:0",
		"localhost:0",
		"example.com:0",
		"127.0.0.1",
	}
	for _, mode := range []AuthMode{AuthLoopback, AuthUserPass} {
		for _, addr := range invalid {
			if _, err := NewServer(Config{Listen: addr, AuthMode: mode}, &fakeSession{}); err == nil {
				t.Fatalf("NewServer accepted %q in %s mode, want a bind error", addr, mode)
			}
		}
	}

	for _, addr := range []string{"127.0.0.1:0", "[::1]:0"} {
		srv, err := NewServer(Config{Listen: addr, AuthMode: AuthUserPass}, &fakeSession{})
		if err != nil {
			t.Fatalf("NewServer(%q): %v", addr, err)
		}
		if err := srv.Start(context.Background()); err != nil {
			// A host without IPv6 cannot bind ::1; the literal-loopback rule
			// is still proven by 127.0.0.1.
			if strings.HasPrefix(addr, "[::1]") {
				t.Skipf("IPv6 loopback unavailable on this host: %v", err)
			}
			t.Fatalf("Start(%q): %v", addr, err)
		}
		bound, ok := srv.Addr().(*net.TCPAddr)
		if !ok || !bound.IP.IsLoopback() {
			t.Fatalf("bound address = %v, want loopback", srv.Addr())
		}
		if err := srv.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}
}

// peerConn overrides the peer address of an injected connection.
type peerConn struct {
	net.Conn
	remote net.Addr
}

func (c *peerConn) RemoteAddr() net.Addr { return c.remote }

// TestNonLoopbackPeerRefusedBeforeGreeting is acceptance row 8: an injected
// connection whose peer is not loopback is closed before a greeting byte, while
// a loopback control proceeds to the method reply.
func TestNonLoopbackPeerRefusedBeforeGreeting(t *testing.T) {
	logs := &captureHandler{}
	session := &fakeSession{}
	srv := startServer(t, Config{NodeID: "node-peer", Logger: slog.New(logs)}, session)

	client, server := net.Pipe()
	go srv.handle(&peerConn{
		Conn:   server,
		remote: &net.TCPAddr{IP: net.IPv4(203, 0, 113, 9), Port: 40000},
	})
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	var one [1]byte
	if n, err := client.Read(one[:]); err == nil || n != 0 {
		t.Fatalf("non-loopback peer read %d byte(s), err %v; want an immediate close", n, err)
	}
	_ = client.Close()

	records := waitFlowRecords(t, srv, func(records []FlowRecord) bool { return len(records) == 1 })
	if records[0].ErrorClass != FlowErrorNonLoopbackPeer {
		t.Fatalf("errorClass = %q, want %q", records[0].ErrorClass, FlowErrorNonLoopbackPeer)
	}
	if opens := session.recorded(); len(opens) != 0 {
		t.Fatalf("OpenFlow calls = %v, want none", opens)
	}
	waitLog(t, logs, "gate peer rejected")

	control, controlServer := net.Pipe()
	go srv.handle(&peerConn{
		Conn:   controlServer,
		remote: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 40001},
	})
	if _, err := control.Write([]byte{socks5Version, 0x01, authNone}); err != nil {
		t.Fatalf("write control greeting: %v", err)
	}
	var reply [2]byte
	if _, err := io.ReadFull(control, reply[:]); err != nil {
		t.Fatalf("loopback control got no method reply: %v", err)
	}
	if reply[0] != socks5Version || reply[1] != authNone {
		t.Fatalf("loopback control method reply = % x, want 05 00", reply)
	}
	_ = control.Close()
}

// TestAllowlistMatching is acceptance rows 9 and 10 at the policy unit: exact
// hosts (case and trailing dot), exact IPs, IPv4/IPv6 CIDRs, no implicit
// subdomain, no domain-to-address authorization, and fail-closed malformed
// entries.
func TestAllowlistMatching(t *testing.T) {
	cases := []struct {
		name    string
		entries []string
		host    string
		want    bool
	}{
		{"exact host", []string{"example.com"}, "example.com", true},
		{"case insensitive", []string{"Example.COM."}, "example.com", true},
		{"terminal dot on target", []string{"example.com"}, "EXAMPLE.com.", true},
		{"implicit subdomain denied", []string{"example.com"}, "sub.example.com", false},
		{"parent not implied", []string{"sub.example.com"}, "example.com", false},
		{"exact IPv4", []string{"127.0.0.1"}, "127.0.0.1", true},
		{"other IPv4 denied", []string{"127.0.0.1"}, "127.0.0.2", false},
		{"IPv4 CIDR", []string{"10.0.0.0/8"}, "10.1.2.3", true},
		{"IPv4 CIDR miss", []string{"10.0.0.0/8"}, "11.1.2.3", false},
		{"IPv6 CIDR", []string{"2001:db8::/32"}, "2001:db8::1", true},
		{"IPv6 CIDR miss", []string{"2001:db8::/32"}, "2001:db9::1", false},
		{"IPv4-mapped target", []string{"10.0.0.0/8"}, "::ffff:10.1.2.3", true},
		{"domain rule does not authorize an address", []string{"example.com"}, "93.184.216.34", false},
		{"address rule does not authorize a name", []string{"93.184.216.34"}, "example.com", false},
		{"CIDR never resolves a name", []string{"10.0.0.0/8"}, "localhost", false},
		{"empty list denies", nil, "example.com", false},
		{"empty list denies an address", []string{}, "127.0.0.1", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			list := CompileAllowlist(tc.entries)
			if list.Invalid() {
				t.Fatalf("CompileAllowlist(%v) unexpectedly invalid", tc.entries)
			}
			if got := list.Match(tc.host); got != tc.want {
				t.Fatalf("Match(%q) = %v, want %v", tc.host, got, tc.want)
			}
		})
	}

	malformed := [][]string{
		{"bad host"},
		{"*.example.com"},
		{"example.com:443"},
		{"http://example.com"},
		{"10.0.0.0/33"},
		{"", "example.com"},
		{"example.com", "not a host"},
	}
	for _, entries := range malformed {
		list := CompileAllowlist(entries)
		if !list.Invalid() {
			t.Fatalf("CompileAllowlist(%v) accepted a malformed entry", entries)
		}
		if list.Match("example.com") {
			t.Fatalf("malformed list %v authorized example.com; must fail closed", entries)
		}
	}
}

// TestAllowlistCanonicalizesTargets is acceptance row 9 through the server:
// canonical host, IPv4, IPv6 and CIDR targets reach OpenFlow, and the record
// names the canonical destination.
func TestAllowlistCanonicalizesTargets(t *testing.T) {
	allow := CompileAllowlist([]string{"example.com", "127.0.0.1", "10.0.0.0/8", "2001:db8::/32"})
	policy := &fakePolicy{allowlist: allow}
	session := &fakeSession{}
	srv := startServer(t, Config{
		NodeID:        "node-row9",
		AuthMode:      AuthUserPass,
		Authenticator: fakeAuthenticator{users: map[string]string{"u-01": "secret"}},
		UserPolicy:    policy,
	}, session)

	ipv6 := net.ParseIP("2001:db8::1").To16()
	targets := []struct {
		name string
		atyp byte
		addr []byte
		want string
	}{
		{"host", atypDomain, domainAddr("Example.COM."), "example.com:443"},
		{"IPv4 exact", atypIPv4, net.IPv4(127, 0, 0, 1).To4(), "127.0.0.1:443"},
		{"IPv4 CIDR", atypIPv4, net.IPv4(10, 9, 8, 7).To4(), "10.9.8.7:443"},
		{"IPv6 CIDR", atypIPv6, ipv6, "2001:db8::1:443"},
	}
	for _, tc := range targets {
		t.Run(tc.name, func(t *testing.T) {
			client := dialGate(t, srv)
			if rep := userPassConnect(t, client, "u-01", "secret", tc.atyp, tc.addr, 443); rep != repSucceeded {
				t.Fatalf("CONNECT reply = %#x, want success", rep)
			}
			_ = client.Close()
		})
	}
	if opens := session.recorded(); len(opens) != len(targets) {
		t.Fatalf("OpenFlow calls = %v, want %d", opens, len(targets))
	}
}

// TestAllowlistDenialRepliesNotAllowed is acceptance row 10 through the server:
// a nonmatching host, an implicit subdomain, a domain rule against an address
// and an empty or malformed list all answer 0x02 with the allowlist class and
// never open a flow.
func TestAllowlistDenialRepliesNotAllowed(t *testing.T) {
	cases := []struct {
		name    string
		entries []string
		atyp    byte
		addr    []byte
	}{
		{"nonmatching host", []string{"allowed.example"}, atypDomain, domainAddr("other.example")},
		{"implicit subdomain", []string{"allowed.example"}, atypDomain, domainAddr("sub.allowed.example")},
		{"domain rule versus address", []string{"allowed.example"}, atypIPv4, net.IPv4(93, 184, 216, 34).To4()},
		{"empty allowlist", []string{}, atypDomain, domainAddr("allowed.example")},
		{"malformed persisted rule", []string{"allowed.example", "bad host"}, atypDomain, domainAddr("allowed.example")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			session := &fakeSession{}
			srv := startServer(t, Config{
				NodeID:        "node-row10",
				AuthMode:      AuthUserPass,
				Authenticator: fakeAuthenticator{users: map[string]string{"u-01": "secret"}},
				UserPolicy:    &fakePolicy{allowlist: CompileAllowlist(tc.entries)},
			}, session)

			client := dialGate(t, srv)
			if rep := userPassConnect(t, client, "u-01", "secret", tc.atyp, tc.addr, 443); rep != repNotAllowed {
				t.Fatalf("CONNECT reply = %#x, want 0x02", rep)
			}
			records := waitFlowRecords(t, srv, func(records []FlowRecord) bool { return len(records) == 1 })
			if records[0].ErrorClass != FlowErrorAllowlist {
				t.Fatalf("errorClass = %q, want %q", records[0].ErrorClass, FlowErrorAllowlist)
			}
			if opens := session.recorded(); len(opens) != 0 {
				t.Fatalf("OpenFlow calls = %v, want none", opens)
			}
		})
	}
}

// TestUserPolicyPrecedesAccountQuota is acceptance row 11 at the gate seam:
// disabled users, user quota and account quota each answer 0x02 with their own
// class, and a user denial never reaches the account quota, the slot or
// OpenFlow.
func TestUserPolicyPrecedesAccountQuota(t *testing.T) {
	allowAll := func(string) (bool, int64, int64) { return true, 0, 0 }
	cases := []struct {
		name          string
		policy        UserPolicy
		quota         func(string) (bool, int64, int64)
		wantClass     string
		wantQuotaHits int
	}{
		{
			name:      "disabled user",
			policy:    &fakePolicy{class: FlowErrorUserDisabled},
			quota:     allowAll,
			wantClass: FlowErrorUserDisabled,
		},
		{
			name:      "zero user quota",
			policy:    &fakePolicy{class: FlowErrorUserQuota, used: 0, budget: 0},
			quota:     allowAll,
			wantClass: FlowErrorUserQuota,
		},
		{
			name:          "exhausted account quota",
			policy:        &fakePolicy{allowlist: CompileAllowlist([]string{"example.com"})},
			quota:         func(string) (bool, int64, int64) { return false, 500, 400 },
			wantClass:     FlowErrorQuota,
			wantQuotaHits: 1,
		},
		{
			name:      "missing policy fails closed",
			policy:    nil,
			quota:     allowAll,
			wantClass: FlowErrorPolicyUnavailable,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			session := &fakeSession{}
			quotaHits := 0
			quota := tc.quota
			srv := startServer(t, Config{
				NodeID:        "node-row11",
				AuthMode:      AuthUserPass,
				Authenticator: fakeAuthenticator{users: map[string]string{"u-01": "secret"}},
				UserPolicy:    tc.policy,
				QuotaCheck: func(accountID string) (bool, int64, int64) {
					quotaHits++
					return quota(accountID)
				},
			}, session)

			client := dialGate(t, srv)
			if rep := userPassConnect(t, client, "u-01", "secret", atypDomain, domainAddr("example.com"), 443); rep != repNotAllowed {
				t.Fatalf("CONNECT reply = %#x, want 0x02", rep)
			}
			records := waitFlowRecords(t, srv, func(records []FlowRecord) bool { return len(records) == 1 })
			if records[0].ErrorClass != tc.wantClass {
				t.Fatalf("errorClass = %q, want %q", records[0].ErrorClass, tc.wantClass)
			}
			if quotaHits != tc.wantQuotaHits {
				t.Fatalf("account quota calls = %d, want %d", quotaHits, tc.wantQuotaHits)
			}
			if opens := session.recorded(); len(opens) != 0 {
				t.Fatalf("OpenFlow calls = %v, want none", opens)
			}
		})
	}
}

// TestCredentialsNeverReachRecordsOrLogs pins the credential-free vocabulary:
// the API-key secret never appears in a flow record or a log line, and a failed
// handshake never echoes the claimed username.
func TestCredentialsNeverReachRecordsOrLogs(t *testing.T) {
	const (
		secret  = "ajs_live_super_secret_value"
		claimed = "claimed-unknown-user"
	)
	logs := &captureHandler{}
	srv := startServer(t, Config{
		NodeID:        "node-secret",
		Logger:        slog.New(logs),
		AuthMode:      AuthUserPass,
		Authenticator: fakeAuthenticator{users: map[string]string{"u-01": secret}},
		UserPolicy:    &fakePolicy{allowlist: CompileAllowlist([]string{"example.com"})},
	}, &fakeSession{})

	failed := dialGate(t, srv)
	greet(t, failed, authUserPass)
	if status := authFrame(t, failed, claimed, "not-the-secret"); status != authStatusFailed {
		t.Fatalf("auth status = %#x, want 0x01", status)
	}
	requireClosedWithoutReply(t, failed)

	admitted := dialGate(t, srv)
	if rep := userPassConnect(t, admitted, "u-01", secret, atypDomain, domainAddr("example.com"), 443); rep != repSucceeded {
		t.Fatalf("CONNECT reply = %#x, want success", rep)
	}
	echoOnce(t, admitted, []byte("secret-check"))
	_ = admitted.Close()

	records := waitFlowRecords(t, srv, func(records []FlowRecord) bool {
		return len(records) == 2 && records[0].Status == FlowStatusOK
	})
	blob, err := json.Marshal(records)
	if err != nil {
		t.Fatalf("marshal flow records: %v", err)
	}
	if strings.Contains(string(blob), secret) || strings.Contains(string(blob), claimed) {
		t.Fatalf("flow records carry credential material: %s", blob)
	}
	if records[0].UserID != "u-01" {
		t.Fatalf("admitted record user = %q, want u-01 attribution", records[0].UserID)
	}

	var logged bytes.Buffer
	handler := slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug})
	for _, record := range logs.snapshot() {
		if err := handler.Handle(context.Background(), record); err != nil {
			t.Fatalf("render log record: %v", err)
		}
	}
	if strings.Contains(logged.String(), secret) {
		t.Fatalf("logs carry the API-key secret: %s", logged.String())
	}
	if strings.Contains(logged.String(), claimed) {
		t.Fatalf("logs carry the claimed username of a failed handshake: %s", logged.String())
	}
}

// BenchmarkFlowAdmission compares a loopback no-auth flow with a userpass flow
// through the real gate: full handshake, CONNECT, 1 MiB each way and one flow
// record. The <1% rollout gate from DESIGN-GATE-IDENTITY §4 is an approximate
// per-flow setup comparison, not a CI assertion.
func BenchmarkFlowAdmission(b *testing.B) {
	payload := bytes.Repeat([]byte("x"), 1<<20)
	scenarios := []struct {
		name string
		cfg  Config
	}{
		{name: "loopback", cfg: Config{}},
		{name: "userpass", cfg: Config{
			AuthMode:      AuthUserPass,
			Authenticator: fakeAuthenticator{users: map[string]string{"u-01": "secret"}},
			UserPolicy:    &fakePolicy{allowlist: CompileAllowlist([]string{"example.com"})},
		}},
	}
	for _, scenario := range scenarios {
		b.Run(scenario.name, func(b *testing.B) {
			flows := make(chan struct{}, b.N)
			cfg := scenario.cfg
			cfg.NodeID = "bench-" + scenario.name
			cfg.Listen = "127.0.0.1:0"
			cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
			cfg.OnFlow = func(record FlowRecord) {
				if record.Status == FlowStatusOK {
					flows <- struct{}{}
				}
			}
			srv, err := NewServer(cfg, &fakeSession{})
			if err != nil {
				b.Fatalf("NewServer: %v", err)
			}
			if err := srv.Start(context.Background()); err != nil {
				b.Fatalf("Start: %v", err)
			}
			defer func() { _ = srv.Close() }()
			addr := srv.Addr().String()

			b.ReportAllocs()
			b.SetBytes(int64(2 * len(payload)))
			b.ResetTimer()
			for range b.N {
				conn, err := net.Dial("tcp", addr)
				if err != nil {
					b.Fatalf("dial: %v", err)
				}
				_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
				if scenario.name == "loopback" {
					writeAll(b, conn, []byte{socks5Version, 0x01, authNone})
					readN(b, conn, 2)
				} else {
					writeAll(b, conn, []byte{socks5Version, 0x01, authUserPass})
					readN(b, conn, 2)
					frame := []byte{authVersion, 0x04}
					frame = append(frame, "u-01"...)
					frame = append(frame, 0x06)
					frame = append(frame, "secret"...)
					writeAll(b, conn, frame)
					readN(b, conn, 2)
				}
				request := []byte{socks5Version, cmdConnect, 0x00, atypDomain, byte(len("example.com"))}
				request = append(request, "example.com"...)
				request = append(request, 0x01, 0xbb)
				writeAll(b, conn, request)
				readN(b, conn, 10)
				writeAll(b, conn, payload)
				readN(b, conn, len(payload))
				_ = conn.Close()
				select {
				case <-flows:
				case <-time.After(10 * time.Second):
					b.Fatalf("flow record did not arrive")
				}
			}
		})
	}
}

func writeAll(b *testing.B, conn net.Conn, payload []byte) {
	b.Helper()
	if _, err := conn.Write(payload); err != nil {
		b.Fatalf("write: %v", err)
	}
}

func readN(b *testing.B, conn net.Conn, n int) {
	b.Helper()
	if _, err := io.ReadFull(conn, make([]byte, n)); err != nil {
		b.Fatalf("read %d bytes: %v", n, err)
	}
}
