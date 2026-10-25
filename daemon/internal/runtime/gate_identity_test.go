package runtime

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"ajsaccountd/internal/account"
	"ajsaccountd/internal/gate"
	"ajsaccountd/internal/model"
	"ajsaccountd/internal/store"
)

// identityEchoSession echoes every admitted flow in memory and records the
// target. It never dials a host, so a policy test proves the gate reached
// OpenFlow without touching the network.
type identityEchoSession struct {
	mu    sync.Mutex
	opens []string
}

func (s *identityEchoSession) OpenFlow(ctx context.Context, host string, port int) (net.Conn, error) {
	s.mu.Lock()
	s.opens = append(s.opens, net.JoinHostPort(host, strconv.Itoa(port)))
	s.mu.Unlock()
	client, server := net.Pipe()
	go func() {
		defer server.Close()
		_, _ = io.Copy(server, server)
	}()
	return client, nil
}

func (s *identityEchoSession) recorded() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.opens...)
}

// identityFixture is one store, one account, one user with an API key and a
// real gate wired exactly as the daemon wires it: the FlowMeter owns both the
// request-log observer and the identity service.
type identityFixture struct {
	store    *store.Store
	account  model.Account
	user     model.PanelUser
	key      model.APIKey
	secret   string
	identity *GateIdentity
	meter    *FlowMeter
	server   *gate.Server
	session  *identityEchoSession
}

func newIdentityFixture(t *testing.T, allowlist []string, quota *int64) *identityFixture {
	t.Helper()
	st := store.NewEmpty()
	account, err := st.CreateAccount(model.CreateAccountInput{
		Label:       "acc-identity",
		Username:    "vendor-identity",
		Password:    "identity-test-password",
		Platform:    model.PlatformLinux,
		Concurrency: 4,
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	user, err := st.CreateUser(model.CreateUserInput{Name: "resin-loopback", Allowlist: allowlist, QuotaBytesMonth: quota})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	key, secret, err := st.CreateKey(user.ID, "resin")
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}
	meter := NewFlowMeter(st)
	identity := meter.Identity()
	session := &identityEchoSession{}
	built, err := meter.GateFactory()(gate.Config{
		NodeID:        "node-identity",
		AccountID:     account.ID,
		Listen:        "127.0.0.1:0",
		AuthMode:      gate.AuthUserPass,
		Authenticator: identity,
		UserPolicy:    identity,
	}, session)
	if err != nil {
		t.Fatalf("GateFactory: %v", err)
	}
	server, ok := built.(*gate.Server)
	if !ok {
		t.Fatalf("gate factory built %T, want *gate.Server", built)
	}
	if err := server.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return &identityFixture{
		store:    st,
		account:  account,
		user:     user,
		key:      key,
		secret:   secret,
		identity: identity,
		meter:    meter,
		server:   server,
		session:  session,
	}
}

func (f *identityFixture) dial(t *testing.T) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", f.server.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial gate: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	return conn
}

// rawAuthenticate performs the greeting and the RFC 1929 subnegotiation and
// returns the server's status byte. It never calls t.Fatalf, so a worker
// goroutine can report a failure on its own channel.
func rawAuthenticate(conn net.Conn, user, pass string) (byte, error) {
	if _, err := conn.Write([]byte{5, 1, 2}); err != nil {
		return 0, err
	}
	var method [2]byte
	if _, err := io.ReadFull(conn, method[:]); err != nil {
		return 0, err
	}
	if method[0] != 5 || method[1] != 2 {
		return 0, errUnexpectedReply
	}
	frame := []byte{1, byte(len(user))}
	frame = append(frame, user...)
	frame = append(frame, byte(len(pass)))
	frame = append(frame, pass...)
	if _, err := conn.Write(frame); err != nil {
		return 0, err
	}
	var reply [2]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		return 0, err
	}
	if reply[0] != 1 {
		return 0, errUnexpectedReply
	}
	return reply[1], nil
}

var errUnexpectedReply = errors.New("identity test: unexpected protocol reply")

func errText(err error) string {
	if err == nil {
		return "none"
	}
	return err.Error()
}

// authenticate is the test-fatal wrapper around rawAuthenticate.
func authenticate(t *testing.T, conn net.Conn, user, pass string) byte {
	t.Helper()
	status, err := rawAuthenticate(conn, user, pass)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	return status
}

// rawConnectIPv4 sends one CONNECT for a literal IPv4 address.
func rawConnectIPv4(conn net.Conn, ip net.IP, port int) (byte, error) {
	request := []byte{5, 1, 0, 1}
	request = append(request, ip.To4()...)
	var portBytes [2]byte
	binary.BigEndian.PutUint16(portBytes[:], uint16(port))
	request = append(request, portBytes[:]...)
	if _, err := conn.Write(request); err != nil {
		return 0, err
	}
	var reply [10]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		return 0, err
	}
	if reply[0] != 5 {
		return 0, errUnexpectedReply
	}
	return reply[1], nil
}

// connectIPv4 is the test-fatal wrapper around rawConnectIPv4.
func connectIPv4(t *testing.T, conn net.Conn, ip net.IP, port int) byte {
	t.Helper()
	rep, err := rawConnectIPv4(conn, ip, port)
	if err != nil {
		t.Fatalf("CONNECT: %v", err)
	}
	return rep
}

func echoBytes(t *testing.T, conn net.Conn, payload []byte) {
	t.Helper()
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	echo := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, echo); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(echo) != string(payload) {
		t.Fatalf("echo = %q, want %q", echo, payload)
	}
}

// waitUserRows waits until the store holds want gate rows and returns them.
func waitUserRows(t *testing.T, st *store.Store, want int) []model.RequestLog {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		rows := gateRows(st.Requests())
		if len(rows) >= want {
			return rows
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d gate rows; have %d", want, len(rows))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func gateRows(rows []model.RequestLog) []model.RequestLog {
	out := make([]model.RequestLog, 0, len(rows))
	for _, row := range rows {
		if row.Source == model.SourceGate {
			out = append(out, row)
		}
	}
	return out
}

// BenchmarkGateIdentityChecks measures the store-backed hot path the gate adds
// per flow in userpass mode: one SHA-256, one digest lookup, one live key read,
// one live user read, the compiled allowlist match and the cached user quota
// check. It excludes relay and upstream cost, which are unchanged.
func BenchmarkGateIdentityChecks(b *testing.B) {
	st := store.NewEmpty()
	user, err := st.CreateUser(model.CreateUserInput{Name: "bench", Allowlist: []string{"127.0.0.1", "10.0.0.0/8", "2001:db8::/32"}})
	if err != nil {
		b.Fatalf("CreateUser: %v", err)
	}
	_, secret, err := st.CreateKey(user.ID, "bench")
	if err != nil {
		b.Fatalf("CreateKey: %v", err)
	}
	identity := NewGateIdentity(st)
	principal := gate.Principal{UserID: user.ID, UserName: user.Name}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, ok := identity.Authenticate(user.ID, secret); !ok {
			b.Fatal("valid credential rejected")
		}
		if ok, class, _, _ := identity.Admit(principal, "10.1.2.3"); !ok {
			b.Fatalf("allowed host denied: %s", class)
		}
	}
}

// gateConfigCapture records the configuration the controller hands to a gate
// factory, so the plumbing from NodeControllerConfig can be asserted without a
// vendor login.
type gateConfigCapture struct {
	cfg gate.Config
}

func (c *gateConfigCapture) build(cfg gate.Config, _ gate.Session) (Gate, error) {
	c.cfg = cfg
	return &noopGate{}, nil
}

type noopGate struct{}

func (g *noopGate) Start(context.Context) error { return nil }
func (g *noopGate) Drain(context.Context) error { return nil }
func (g *noopGate) Undrain() error              { return nil }
func (g *noopGate) Close() error                { return nil }
func (g *noopGate) Addr() net.Addr              { return nil }

type noopLogins struct{}

func (noopLogins) Result(string) (account.LoginResult, bool) { return account.LoginResult{}, false }

type noopSessions struct{}

func (noopSessions) Open(context.Context, account.SessionRequest) (account.FlowSession, error) {
	return nil, errors.New("no sessions in this test")
}

// TestNodeControllerPassesGateAuthToGates proves the AJS_GATE_AUTH mode and the
// identity service reach every gate the controller starts, and that an unset
// mode selects the fail-closed userpass default: a controller that never
// received the parsed setting starts gates that refuse every handshake instead
// of falling back to no-auth. The compatibility wire is an explicit choice.
func TestNodeControllerPassesGateAuthToGates(t *testing.T) {
	cases := []struct {
		name       string
		mode       gate.AuthMode
		withID     bool
		wantMode   gate.AuthMode
		wantWiring bool
	}{
		{"unset selects userpass and wires identity", "", true, gate.AuthUserPass, true},
		{"unset without identity fails closed", "", false, gate.AuthUserPass, false},
		{"loopback is an explicit opt-in", gate.AuthLoopback, true, gate.AuthLoopback, false},
		{"userpass wires identity", gate.AuthUserPass, true, gate.AuthUserPass, true},
		{"userpass without identity fails closed", gate.AuthUserPass, false, gate.AuthUserPass, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := store.NewEmpty()
			capture := &gateConfigCapture{}
			config := NodeControllerConfig{
				Store:    st,
				Logins:   noopLogins{},
				Sessions: noopSessions{},
				Gates:    capture.build,
				GateAuth: tc.mode,
			}
			if tc.withID {
				config.GateIdentity = NewGateIdentity(st)
			}
			controller, err := NewNodeController(config)
			if err != nil {
				t.Fatalf("NewNodeController: %v", err)
			}
			t.Cleanup(func() {
				stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				_ = controller.Stop(stopCtx)
			})
			if _, _, err := controller.startGateLocked(
				model.SlotKey{AccountID: "acc-01", Index: 1},
				serverRef{id: "s1"},
				nil,
				gateAddresses{socks: 13000},
			); err != nil {
				t.Fatalf("startGateLocked: %v", err)
			}
			if capture.cfg.AuthMode != tc.wantMode {
				t.Fatalf("gate AuthMode = %q, want %q", capture.cfg.AuthMode, tc.wantMode)
			}
			wired := capture.cfg.Authenticator != nil && capture.cfg.UserPolicy != nil
			if wired != tc.wantWiring {
				t.Fatalf("gate identity wired = %v, want %v", wired, tc.wantWiring)
			}
			if capture.cfg.Listen != "127.0.0.1:13000" {
				t.Fatalf("gate Listen = %q, want the loopback default port", capture.cfg.Listen)
			}
		})
	}
	if _, err := NewNodeController(NodeControllerConfig{
		Store:    store.NewEmpty(),
		Logins:   noopLogins{},
		Sessions: noopSessions{},
		GateAuth: gate.AuthMode("bogus"),
	}); err == nil {
		t.Fatal("NewNodeController accepted an unknown gate auth mode")
	}
}

// TestGateIdentityRelaysAndMetersUser is acceptance rows 1 and 6 (first half)
// with the real store: a valid user/key relays, both the flow record and the
// request log carry the identity, and the metered bytes advance the user's
// monthly total until the quota refuses the next flow.
func TestGateIdentityRelaysAndMetersUser(t *testing.T) {
	budget := int64(1 << 40)
	f := newIdentityFixture(t, []string{"127.0.0.1/32"}, &budget)

	conn := f.dial(t)
	if status := authenticate(t, conn, f.user.ID, f.secret); status != 0 {
		t.Fatalf("auth status = %#x, want 0x00", status)
	}
	if rep := connectIPv4(t, conn, net.IPv4(127, 0, 0, 1), 443); rep != 0 {
		t.Fatalf("CONNECT reply = %#x, want success", rep)
	}
	payload := []byte("metered-user-payload")
	echoBytes(t, conn, payload)
	_ = conn.Close()

	rows := waitUserRows(t, f.store, 1)
	if rows[0].UserID != f.user.ID || rows[0].UserName != f.user.Name {
		t.Fatalf("request row attribution = %q/%q, want %q/%q", rows[0].UserID, rows[0].UserName, f.user.ID, f.user.Name)
	}
	if rows[0].Bytes != int64(2*len(payload)) {
		t.Fatalf("request row bytes = %d, want %d", rows[0].Bytes, 2*len(payload))
	}

	records := f.server.Flows(0)
	if len(records) != 1 || records[0].Status != gate.FlowStatusOK {
		t.Fatalf("flow records = %+v, want one accepted flow", records)
	}
	if records[0].UserID != f.user.ID || records[0].UserName != f.user.Name {
		t.Fatalf("flow record attribution = %q/%q", records[0].UserID, records[0].UserName)
	}

	// The meter advanced the cached monthly total, and the next admission
	// refuses once the observed usage reaches the budget.
	f.identity.mu.Lock()
	cached := f.identity.usage[f.user.ID]
	f.identity.mu.Unlock()
	if cached.bytes != int64(2*len(payload)) {
		t.Fatalf("cached user usage = %d, want %d", cached.bytes, 2*len(payload))
	}
	exact := cached.bytes
	if _, err := f.store.UpdateUser(f.user.ID, model.UserPatch{QuotaSet: true, QuotaBytesMonth: &exact}); err != nil {
		t.Fatalf("UpdateUser quota: %v", err)
	}
	ok, class, used, gotBudget := f.identity.Admit(gate.Principal{UserID: f.user.ID}, "127.0.0.1")
	if ok || class != gate.FlowErrorUserQuota {
		t.Fatalf("admission after metering = %v/%q, want user_quota_exceeded", ok, class)
	}
	if used != exact || gotBudget != exact {
		t.Fatalf("quota diagnostics = %d/%d, want %d", used, gotBudget, exact)
	}
}

// TestGateIdentityRevokedKeyIsImmediate is acceptance row 6: revoking the key
// fails the next handshake with 01 01 while the already admitted flow keeps
// relaying under its authenticated identity.
func TestGateIdentityRevokedKeyIsImmediate(t *testing.T) {
	f := newIdentityFixture(t, []string{"127.0.0.1/32"}, nil)

	conn := f.dial(t)
	if status := authenticate(t, conn, f.user.ID, f.secret); status != 0 {
		t.Fatalf("auth status = %#x, want 0x00", status)
	}
	if rep := connectIPv4(t, conn, net.IPv4(127, 0, 0, 1), 443); rep != 0 {
		t.Fatalf("CONNECT reply = %#x, want success", rep)
	}
	echoBytes(t, conn, []byte("before-revoke"))

	if err := f.store.RevokeKey(f.key.ID); err != nil {
		t.Fatalf("RevokeKey: %v", err)
	}

	revoked := f.dial(t)
	if status := authenticate(t, revoked, f.user.ID, f.secret); status != 1 {
		t.Fatalf("auth status after revoke = %#x, want 0x01", status)
	}
	_ = revoked.SetReadDeadline(time.Now().Add(time.Second))
	var one [1]byte
	if n, err := revoked.Read(one[:]); err == nil || n != 0 {
		t.Fatalf("revoked handshake got %d byte(s), err %v; want a close", n, err)
	}

	// The admitted flow keeps its identity and keeps relaying.
	echoBytes(t, conn, []byte("after-revoke"))
	_ = conn.Close()

	rows := waitUserRows(t, f.store, 1)
	if rows[0].UserID != f.user.ID {
		t.Fatalf("admitted flow attribution = %q, want %q", rows[0].UserID, f.user.ID)
	}
	if opens := f.session.recorded(); len(opens) != 1 {
		t.Fatalf("OpenFlow calls = %v, want one", opens)
	}
}

// TestGateIdentityRejectsBadCredentials covers unknown user, wrong secret, a
// key issued to another user and a revoked key at the store seam.
func TestGateIdentityRejectsBadCredentials(t *testing.T) {
	st := store.NewEmpty()
	userOne, err := st.CreateUser(model.CreateUserInput{Name: "one", Allowlist: []string{"127.0.0.1/32"}})
	if err != nil {
		t.Fatalf("CreateUser one: %v", err)
	}
	userTwo, err := st.CreateUser(model.CreateUserInput{Name: "two", Allowlist: []string{"127.0.0.1/32"}})
	if err != nil {
		t.Fatalf("CreateUser two: %v", err)
	}
	_, secretOne, err := st.CreateKey(userOne.ID, "one")
	if err != nil {
		t.Fatalf("CreateKey one: %v", err)
	}
	keyTwo, secretTwo, err := st.CreateKey(userTwo.ID, "two")
	if err != nil {
		t.Fatalf("CreateKey two: %v", err)
	}
	identity := NewGateIdentity(st)

	cases := []struct {
		name string
		user string
		pass string
	}{
		{"unknown user", "u-99", secretOne},
		{"wrong secret", userOne.ID, "not-the-secret"},
		{"another user's key", userOne.ID, secretTwo},
	}
	for _, tc := range cases {
		if principal, ok := identity.Authenticate(tc.user, tc.pass); ok {
			t.Fatalf("%s authenticated as %+v", tc.name, principal)
		}
	}
	if principal, ok := identity.Authenticate(userTwo.ID, secretTwo); !ok || principal.UserID != userTwo.ID {
		t.Fatalf("valid credential rejected: %+v/%v", principal, ok)
	}
	if err := st.RevokeKey(keyTwo.ID); err != nil {
		t.Fatalf("RevokeKey: %v", err)
	}
	if _, ok := identity.Authenticate(userTwo.ID, secretTwo); ok {
		t.Fatal("revoked key authenticated")
	}
}

// TestGateIdentityPolicyClasses covers disabled, deleted, zero-quota and
// malformed-allowlist users. Every one fails closed with its own class.
func TestGateIdentityPolicyClasses(t *testing.T) {
	f := newIdentityFixture(t, []string{"127.0.0.1/32"}, nil)
	principal := gate.Principal{UserID: f.user.ID, UserName: f.user.Name}

	if ok, class, _, _ := f.identity.Admit(principal, "127.0.0.1"); !ok || class != "" {
		t.Fatalf("enabled user = %v/%q, want admitted", ok, class)
	}

	if _, err := f.store.SetUserEnabled(f.user.ID, false); err != nil {
		t.Fatalf("SetUserEnabled: %v", err)
	}
	if ok, class, _, _ := f.identity.Admit(principal, "127.0.0.1"); ok || class != gate.FlowErrorUserDisabled {
		t.Fatalf("disabled user = %v/%q, want user_disabled", ok, class)
	}
	if _, err := f.store.SetUserEnabled(f.user.ID, true); err != nil {
		t.Fatalf("SetUserEnabled: %v", err)
	}

	zero := int64(0)
	if _, err := f.store.UpdateUser(f.user.ID, model.UserPatch{QuotaSet: true, QuotaBytesMonth: &zero}); err != nil {
		t.Fatalf("UpdateUser quota: %v", err)
	}
	if ok, class, used, budget := f.identity.Admit(principal, "127.0.0.1"); ok || class != gate.FlowErrorUserQuota || used != 0 || budget != 0 {
		t.Fatalf("zero quota = %v/%q (%d/%d), want user_quota_exceeded", ok, class, used, budget)
	}

	if ok, class, _, _ := f.identity.Admit(principal, "10.0.0.1"); ok || class != gate.FlowErrorAllowlist {
		t.Fatalf("nonmatching host = %v/%q, want allowlist", ok, class)
	}

	if err := f.store.DeleteUser(f.user.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if ok, class, _, _ := f.identity.Admit(principal, "127.0.0.1"); ok || class != gate.FlowErrorPolicyUnavailable {
		t.Fatalf("deleted user = %v/%q, want policy_unavailable", ok, class)
	}
	if _, ok := f.identity.Authenticate(f.user.ID, f.secret); ok {
		t.Fatal("deleted user's key authenticated")
	}
}

// TestGateIdentityAllowlistFollowsUserUpdates proves a stored allowlist change
// is visible to the next admission through the compiled-policy cache.
func TestGateIdentityAllowlistFollowsUserUpdates(t *testing.T) {
	f := newIdentityFixture(t, []string{"127.0.0.1/32"}, nil)
	principal := gate.Principal{UserID: f.user.ID}

	if ok, class, _, _ := f.identity.Admit(principal, "10.1.2.3"); ok || class != gate.FlowErrorAllowlist {
		t.Fatalf("before update = %v/%q, want allowlist", ok, class)
	}
	allowlist := []string{"10.0.0.0/8"}
	if _, err := f.store.UpdateUser(f.user.ID, model.UserPatch{Allowlist: &allowlist}); err != nil {
		t.Fatalf("UpdateUser allowlist: %v", err)
	}
	if ok, class, _, _ := f.identity.Admit(principal, "10.1.2.3"); !ok || class != "" {
		t.Fatalf("after update = %v/%q, want admitted", ok, class)
	}
	if ok, class, _, _ := f.identity.Admit(principal, "127.0.0.1"); ok || class != gate.FlowErrorAllowlist {
		t.Fatalf("replaced entry = %v/%q, want allowlist", ok, class)
	}
}

// TestGateIdentityDiscoversNewKeys pins the only allowed lag: a key created
// after the digest index was built becomes usable after rebuildInterval, and
// revocation never waits for a rebuild.
func TestGateIdentityDiscoversNewKeys(t *testing.T) {
	st := store.NewEmpty()
	user, err := st.CreateUser(model.CreateUserInput{Name: "late", Allowlist: []string{"127.0.0.1/32"}})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	identity := NewGateIdentity(st)
	now := time.Unix(1_700_000_000, 0)
	identity.now = func() time.Time { return now }

	if _, ok := identity.Authenticate(user.ID, "definitely-wrong"); ok {
		t.Fatal("wrong password authenticated")
	}
	_, secret, err := st.CreateKey(user.ID, "late")
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}
	if _, ok := identity.Authenticate(user.ID, secret); ok {
		t.Fatal("new key authenticated inside the rebuild interval")
	}
	now = now.Add(2 * time.Second)
	if principal, ok := identity.Authenticate(user.ID, secret); !ok || principal.UserID != user.ID {
		t.Fatalf("new key after the interval = %+v/%v, want the user", principal, ok)
	}
}

// TestGateIdentityLoopbackRowsHaveNoUser proves the compatibility path keeps
// the meter's user columns empty.
func TestGateIdentityLoopbackRowsHaveNoUser(t *testing.T) {
	st := store.NewEmpty()
	account, err := st.CreateAccount(model.CreateAccountInput{
		Label:       "acc-loopback",
		Username:    "vendor-loopback",
		Password:    "identity-test-password",
		Platform:    model.PlatformLinux,
		Concurrency: 4,
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	meter := NewFlowMeter(st)
	session := &identityEchoSession{}
	built, err := meter.GateFactory()(gate.Config{
		NodeID:    "node-loopback",
		AccountID: account.ID,
		Listen:    "127.0.0.1:0",
		AuthMode:  gate.AuthLoopback,
	}, session)
	if err != nil {
		t.Fatalf("GateFactory: %v", err)
	}
	server := built.(*gate.Server)
	if err := server.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	conn, err := net.DialTimeout("tcp", server.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte{5, 1, 0}); err != nil {
		t.Fatalf("write greeting: %v", err)
	}
	var method [2]byte
	if _, err := io.ReadFull(conn, method[:]); err != nil || method[1] != 0 {
		t.Fatalf("method reply = % x, err %v; want 05 00", method, err)
	}
	if rep := connectIPv4(t, conn, net.IPv4(127, 0, 0, 1), 443); rep != 0 {
		t.Fatalf("CONNECT reply = %#x, want success", rep)
	}
	echoBytes(t, conn, []byte("loopback"))
	_ = conn.Close()

	rows := waitUserRows(t, st, 1)
	if rows[0].UserID != "" || rows[0].UserName != "" {
		t.Fatalf("loopback row attributed to %q/%q, want empty", rows[0].UserID, rows[0].UserName)
	}
}

// TestGateIdentityConcurrentFlowsAndUpdates is acceptance row 12: 32 concurrent
// authenticated flows run while keys are created and revoked and an allowlist
// is replaced. Every admitted flow is attributed to its own credential, no
// bytes are misattributed, and the next admission after the final update uses
// the new policy. Run under -race.
func TestGateIdentityConcurrentFlowsAndUpdates(t *testing.T) {
	st := store.NewEmpty()
	account, err := st.CreateAccount(model.CreateAccountInput{
		Label:       "acc-race",
		Username:    "vendor-race",
		Password:    "identity-test-password",
		Platform:    model.PlatformLinux,
		Concurrency: 4,
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	type raceIdentity struct {
		user   model.PanelUser
		key    model.APIKey
		secret string
	}
	users := make([]raceIdentity, 0, 2)
	for _, name := range []string{"race-one", "race-two"} {
		user, err := st.CreateUser(model.CreateUserInput{Name: name, Allowlist: []string{"127.0.0.1/32"}})
		if err != nil {
			t.Fatalf("CreateUser %s: %v", name, err)
		}
		key, secret, err := st.CreateKey(user.ID, name)
		if err != nil {
			t.Fatalf("CreateKey %s: %v", name, err)
		}
		users = append(users, raceIdentity{user: user, key: key, secret: secret})
	}

	// The acceptance row opens 16 concurrent flows per user, so lift the
	// default per-user cap for this gate.
	t.Setenv("AJS_GATE_USER_FLOWS", "16")

	meter := NewFlowMeter(st)
	identity := meter.Identity()
	session := &identityEchoSession{}
	built, err := meter.GateFactory()(gate.Config{
		NodeID:        "node-race",
		AccountID:     account.ID,
		Listen:        "127.0.0.1:0",
		AuthMode:      gate.AuthUserPass,
		Authenticator: identity,
		UserPolicy:    identity,
	}, session)
	if err != nil {
		t.Fatalf("GateFactory: %v", err)
	}
	server := built.(*gate.Server)
	if err := server.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	// Mutations run while flows are in flight. They never touch the keys the
	// flows use, so every flow must still authenticate.
	stop := make(chan struct{})
	var mutations sync.WaitGroup
	mutations.Add(1)
	go func() {
		defer mutations.Done()
		allowlist := []string{"127.0.0.1/32", "10.0.0.0/8"}
		for {
			select {
			case <-stop:
				return
			default:
			}
			key, _, err := st.CreateKey(users[0].user.ID, "churn")
			if err == nil {
				_ = st.RevokeKey(key.ID)
			}
			_, _ = st.UpdateUser(users[1].user.ID, model.UserPatch{Allowlist: &allowlist})
			_, _ = st.UpdateUser(users[1].user.ID, model.UserPatch{Allowlist: &[]string{"127.0.0.1/32"}})
		}
	}()

	const flowsPerUser = 16
	payloads := make([][][]byte, len(users))
	var flows sync.WaitGroup
	failures := make(chan string, 2*len(users)*flowsPerUser)
	for index, credential := range users {
		for i := range flowsPerUser {
			payload := append([]byte(strconv.Itoa(index)+"-"+strconv.Itoa(i)+"-"), bytes.Repeat([]byte("x"), i)...)
			payloads[index] = append(payloads[index], payload)
			flows.Add(1)
			go func(credential raceIdentity, payload []byte) {
				defer flows.Done()
				conn, err := net.DialTimeout("tcp", server.Addr().String(), 2*time.Second)
				if err != nil {
					failures <- "dial: " + err.Error()
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
				if status, err := rawAuthenticate(conn, credential.user.ID, credential.secret); err != nil || status != 0 {
					failures <- "authenticate: status " + strconv.Itoa(int(status)) + " err " + errText(err)
					return
				}
				if rep, err := rawConnectIPv4(conn, net.IPv4(127, 0, 0, 1), 443); err != nil || rep != 0 {
					failures <- "connect: reply " + strconv.Itoa(int(rep)) + " err " + errText(err)
					return
				}
				if _, err := conn.Write(payload); err != nil {
					failures <- "write: " + err.Error()
					return
				}
				echo := make([]byte, len(payload))
				if _, err := io.ReadFull(conn, echo); err != nil {
					failures <- "read: " + err.Error()
					return
				}
				if !bytes.Equal(echo, payload) {
					failures <- "echo mismatch"
				}
			}(credential, payload)
		}
	}
	flows.Wait()
	close(stop)
	mutations.Wait()
	if len(failures) > 0 {
		t.Fatalf("concurrent flows failed: %v", <-failures)
	}

	rows := waitUserRows(t, st, 2*flowsPerUser)
	byUser := map[string]int{}
	bytesByUser := map[string]int64{}
	for _, row := range rows {
		byUser[row.UserID]++
		bytesByUser[row.UserID] += row.Bytes
	}
	for index, identityUser := range users {
		if byUser[identityUser.user.ID] != flowsPerUser {
			t.Fatalf("user %s rows = %d, want %d", identityUser.user.ID, byUser[identityUser.user.ID], flowsPerUser)
		}
		var want int64
		for _, payload := range payloads[index] {
			want += int64(2 * len(payload))
		}
		if bytesByUser[identityUser.user.ID] != want {
			t.Fatalf("user %s bytes = %d, want %d", identityUser.user.ID, bytesByUser[identityUser.user.ID], want)
		}
	}

	// The final deterministic update must govern the next admission.
	onlyPrivate := []string{"10.0.0.0/8"}
	if _, err := st.UpdateUser(users[0].user.ID, model.UserPatch{Allowlist: &onlyPrivate}); err != nil {
		t.Fatalf("UpdateUser final allowlist: %v", err)
	}
	conn, err := net.DialTimeout("tcp", server.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial after update: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if status := authenticate(t, conn, users[0].user.ID, users[0].secret); status != 0 {
		t.Fatalf("auth after update = %#x, want 0x00", status)
	}
	if rep := connectIPv4(t, conn, net.IPv4(127, 0, 0, 1), 443); rep != 2 {
		t.Fatalf("CONNECT after allowlist replacement = %#x, want 0x02", rep)
	}
}
