package runtime

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
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
	return dialGate(t, f.server)
}

// dialGate opens one connection to a started gate with the test deadline.
func dialGate(t *testing.T, server *gate.Server) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", server.Addr().String(), 2*time.Second)
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
			var identity *GateIdentity
			if tc.withID {
				identity = NewGateIdentity(st)
				config.GateIdentity = identity
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
			if tc.wantWiring {
				// The controller must hand the gate the identity bound to that
				// gate's account, never the bare *GateIdentity: only the
				// wrapper resolves the user's lease, so a bare identity would
				// admit a credential without one.
				authenticator, ok := capture.cfg.Authenticator.(*GateAccountIdentity)
				if !ok {
					t.Fatalf("gate authenticator = %T, want *GateAccountIdentity", capture.cfg.Authenticator)
				}
				policy, ok := capture.cfg.UserPolicy.(*GateAccountIdentity)
				if !ok {
					t.Fatalf("gate policy = %T, want *GateAccountIdentity", capture.cfg.UserPolicy)
				}
				if authenticator.accountID != "acc-01" || policy.accountID != "acc-01" {
					t.Fatalf("gate binding = %q/%q, want acc-01", authenticator.accountID, policy.accountID)
				}
				if authenticator.identity != identity || policy.identity != identity {
					t.Fatal("gate wrapper does not share the controller's identity service")
				}
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

// scopedGateFixture is one account with a live node, one user holding a scoped
// proxy:connect key plus a legacy panel key, and exactly one active lease. The
// gate is built through FlowMeter.GateFactory with no explicit authenticator or
// policy, so the production default — the account-bound wrapper — is what runs.
type scopedGateFixture struct {
	store       *store.Store
	account     model.Account
	user        model.PanelUser
	secret      string
	panelSecret string
	lease       model.Lease
	identity    *GateIdentity
	server      *gate.Server
	session     *identityEchoSession
}

func newScopedGateFixture(t *testing.T) *scopedGateFixture {
	t.Helper()
	st := store.NewEmpty()
	account, err := st.CreateAccount(model.CreateAccountInput{
		Label:       "acc-scoped",
		Username:    "vendor-scoped",
		Password:    "scoped-test-password",
		Platform:    model.PlatformLinux,
		Concurrency: 4,
		Regions:     []string{"cn-zj"},
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if _, err := st.PutNode(model.ProxyNode{
		ID: "node-scoped", AccountID: account.ID, Label: "cn-zj-01", Region: "cn-zj",
		State: model.NodeLive, EgressIP: "203.0.113.30", Protocol: model.ProtocolSOCKS5, GatePort: 13031,
	}); err != nil {
		t.Fatalf("PutNode: %v", err)
	}
	user, err := st.CreateUser(model.CreateUserInput{
		Name: "scoped-gate", Allowlist: []string{"127.0.0.1/32"},
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	_, secret, err := st.CreateScopedKey(model.ScopedKeyCreateRequest{
		UserID:     user.ID,
		Label:      "gate",
		Scopes:     []model.Scope{model.ScopeProxyConnect},
		AccountIDs: []string{account.ID},
	})
	if err != nil {
		t.Fatalf("CreateScopedKey: %v", err)
	}
	_, panelSecret, err := st.CreateKey(user.ID, "legacy")
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}
	lease, _, err := st.CreateLease(model.CreateLeaseInput{
		UserID: user.ID, Platform: model.PlatformLinux, AccountID: account.ID,
	})
	if err != nil {
		t.Fatalf("CreateLease: %v", err)
	}
	meter := NewFlowMeter(st)
	session := &identityEchoSession{}
	built, err := meter.GateFactory()(gate.Config{
		NodeID:    "node-scoped",
		AccountID: account.ID,
		Listen:    "127.0.0.1:0",
		AuthMode:  gate.AuthUserPass,
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
	return &scopedGateFixture{
		store:       st,
		account:     account,
		user:        user,
		secret:      secret,
		panelSecret: panelSecret,
		lease:       lease,
		identity:    meter.Identity(),
		server:      server,
		session:     session,
	}
}

func (f *scopedGateFixture) dial(t *testing.T) net.Conn {
	t.Helper()
	return dialGate(t, f.server)
}

// addForeignLease creates a second account with a live node, a second user and
// one active lease on that account, so a principal can name a lease that is
// real but not the gate's.
func addForeignLease(t *testing.T, st *store.Store) (model.Account, model.PanelUser, model.Lease) {
	t.Helper()
	account, err := st.CreateAccount(model.CreateAccountInput{
		Label:       "acc-foreign",
		Username:    "vendor-foreign",
		Password:    "foreign-test-password",
		Platform:    model.PlatformLinux,
		Concurrency: 2,
	})
	if err != nil {
		t.Fatalf("CreateAccount(foreign): %v", err)
	}
	if _, err := st.PutNode(model.ProxyNode{
		ID: "node-foreign", AccountID: account.ID, Label: "cn-zj-02", Region: "cn-zj",
		State: model.NodeLive, EgressIP: "203.0.113.31", Protocol: model.ProtocolSOCKS5, GatePort: 13032,
	}); err != nil {
		t.Fatalf("PutNode(foreign): %v", err)
	}
	user, err := st.CreateUser(model.CreateUserInput{
		Name: "foreign-gate", Allowlist: []string{"127.0.0.1/32"},
	})
	if err != nil {
		t.Fatalf("CreateUser(foreign): %v", err)
	}
	lease, _, err := st.CreateLease(model.CreateLeaseInput{
		UserID: user.ID, Platform: model.PlatformLinux, AccountID: account.ID,
	})
	if err != nil {
		t.Fatalf("CreateLease(foreign): %v", err)
	}
	return account, user, lease
}

// expireLeases rewrites every lease expiry into the past and restores the
// document into a fresh store, so the next read sweeps the rows as expired
// without waiting out a TTL. Restore keeps the row; the store's sweep is what
// removes it, exactly as it would after the real deadline.
func expireLeases(t *testing.T, st *store.Store) *store.Store {
	t.Helper()
	data, err := st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	leases, ok := doc["leases"].([]any)
	if !ok || len(leases) == 0 {
		t.Fatalf("snapshot carries no leases")
	}
	past := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	for _, raw := range leases {
		record, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("snapshot lease is %T, want an object", raw)
		}
		record["expiresAt"] = past
	}
	edited, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("encode snapshot: %v", err)
	}
	restored := store.NewEmpty()
	if err := restored.Restore(edited); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	return restored
}

// TestGateIdentityScopedKey is the gate half of AJS-450: a scoped key carrying
// proxy:connect authenticates at its account's gate and binds to the user's
// lease, while a key without the scope, a key issued to another user, a key
// granted another account and a revoked key are refused with the same answer.
func TestGateIdentityScopedKey(t *testing.T) {
	f := newScopedGateFixture(t)
	bound := f.identity.ForAccount(f.account.ID)

	principal, ok := bound.Authenticate(f.user.ID, f.secret)
	if !ok {
		t.Fatal("valid scoped key rejected")
	}
	if principal.UserID != f.user.ID || principal.UserName != f.user.Name {
		t.Fatalf("principal = %q/%q, want %q/%q", principal.UserID, principal.UserName, f.user.ID, f.user.Name)
	}
	if principal.LeaseID != f.lease.ID {
		t.Fatalf("principal lease = %q, want %q", principal.LeaseID, f.lease.ID)
	}

	// The same credential relays over the wire and is attributed to its user.
	conn := f.dial(t)
	if status := authenticate(t, conn, f.user.ID, f.secret); status != 0 {
		t.Fatalf("auth status = %#x, want 0x00", status)
	}
	if rep := connectIPv4(t, conn, net.IPv4(127, 0, 0, 1), 443); rep != 0 {
		t.Fatalf("CONNECT reply = %#x, want success", rep)
	}
	echoBytes(t, conn, []byte("scoped-key"))
	_ = conn.Close()
	rows := waitUserRows(t, f.store, 1)
	if rows[0].UserID != f.user.ID || rows[0].UserName != f.user.Name {
		t.Fatalf("row attribution = %q/%q, want %q/%q", rows[0].UserID, rows[0].UserName, f.user.ID, f.user.Name)
	}

	other, err := f.store.CreateUser(model.CreateUserInput{
		Name: "other-user", Allowlist: []string{"127.0.0.1/32"},
	})
	if err != nil {
		t.Fatalf("CreateUser(other): %v", err)
	}
	elsewhere, err := f.store.CreateAccount(model.CreateAccountInput{
		Label: "acc-elsewhere", Username: "vendor-elsewhere", Password: "elsewhere-test-password",
		Platform: model.PlatformLinux, Concurrency: 1,
	})
	if err != nil {
		t.Fatalf("CreateAccount(elsewhere): %v", err)
	}
	_, noScope, err := f.store.CreateScopedKey(model.ScopedKeyCreateRequest{
		UserID: f.user.ID, Label: "no gate scope",
		Scopes: []model.Scope{model.ScopeLeasesRead}, AccountIDs: []string{f.account.ID},
	})
	if err != nil {
		t.Fatalf("CreateScopedKey(no scope): %v", err)
	}
	_, elsewhereKey, err := f.store.CreateScopedKey(model.ScopedKeyCreateRequest{
		UserID: f.user.ID, Label: "other account",
		Scopes: []model.Scope{model.ScopeProxyConnect}, AccountIDs: []string{elsewhere.ID},
	})
	if err != nil {
		t.Fatalf("CreateScopedKey(elsewhere): %v", err)
	}
	revokedKey, revokedSecret, err := f.store.CreateScopedKey(model.ScopedKeyCreateRequest{
		UserID: f.user.ID, Label: "revoked",
		Scopes: []model.Scope{model.ScopeProxyConnect}, AccountIDs: []string{f.account.ID},
	})
	if err != nil {
		t.Fatalf("CreateScopedKey(revoked): %v", err)
	}
	if err := f.store.RevokeScopedKey(revokedKey.ID); err != nil {
		t.Fatalf("RevokeScopedKey: %v", err)
	}

	cases := []struct {
		name string
		user string
		pass string
	}{
		{"missing proxy:connect", f.user.ID, noScope},
		{"wrong user", other.ID, f.secret},
		{"wrong account grant", f.user.ID, elsewhereKey},
		{"revoked key", f.user.ID, revokedSecret},
		{"unknown secret", f.user.ID, "ajs_sk_not-a-real-secret"},
	}
	for _, tc := range cases {
		if principal, ok := bound.Authenticate(tc.user, tc.pass); ok {
			t.Fatalf("%s authenticated as %+v", tc.name, principal)
		}
	}

	// Every refusal is the same wire answer: 01 01.
	rejected := f.dial(t)
	if status := authenticate(t, rejected, f.user.ID, noScope); status != 1 {
		t.Fatalf("auth status for a key without proxy:connect = %#x, want 0x01", status)
	}
	if opens := f.session.recorded(); len(opens) != 1 {
		t.Fatalf("OpenFlow calls = %d, want only the admitted flow", len(opens))
	}

	// The admin grant covers every account, so an admin+proxy:connect key with
	// a nonmatching grant still reaches this gate: admin behavior is unchanged.
	_, adminSecret, err := f.store.CreateScopedKey(model.ScopedKeyCreateRequest{
		UserID: f.user.ID, Label: "admin gate",
		Scopes:     []model.Scope{model.ScopeAdmin, model.ScopeProxyConnect},
		AccountIDs: []string{elsewhere.ID},
	})
	if err != nil {
		t.Fatalf("CreateScopedKey(admin): %v", err)
	}
	adminPrincipal, adminOK := bound.Authenticate(f.user.ID, adminSecret)
	if !adminOK || adminPrincipal.LeaseID != f.lease.ID {
		t.Fatalf("admin proxy key = %+v/%v, want the lease-bound principal", adminPrincipal, adminOK)
	}
	// The admin grant never substitutes for proxy:connect.
	_, adminNoScope, err := f.store.CreateScopedKey(model.ScopedKeyCreateRequest{
		UserID: f.user.ID, Label: "admin without gate scope",
		Scopes: []model.Scope{model.ScopeAdmin},
	})
	if err != nil {
		t.Fatalf("CreateScopedKey(admin no scope): %v", err)
	}
	if principal, ok := bound.Authenticate(f.user.ID, adminNoScope); ok {
		t.Fatalf("admin key without proxy:connect authenticated as %+v", principal)
	}
}

// TestGateIdentityLeaseBinding is the entitlement half of AJS-450: a credential
// admits a flow only through exactly one active lease of (user, gate account),
// the lease is re-read at admission, and zero, ambiguous, foreign or expired
// leases all fail closed for both credential kinds.
func TestGateIdentityLeaseBinding(t *testing.T) {
	f := newScopedGateFixture(t)
	bound := f.identity.ForAccount(f.account.ID)

	// The legacy panel key is still a credential during migration, and it is
	// bound to the same lease.
	principal, ok := bound.Authenticate(f.user.ID, f.panelSecret)
	if !ok {
		t.Fatal("legacy panel key with an active lease rejected")
	}
	if principal.LeaseID != f.lease.ID {
		t.Fatalf("legacy principal lease = %q, want %q", principal.LeaseID, f.lease.ID)
	}
	if ok, class, _, _ := bound.Admit(principal, "127.0.0.1"); !ok {
		t.Fatalf("legacy admission refused: %q", class)
	}

	// A credential without a lease on this account is refused.
	noLeaseUser, err := f.store.CreateUser(model.CreateUserInput{
		Name: "no-lease", Allowlist: []string{"127.0.0.1/32"},
	})
	if err != nil {
		t.Fatalf("CreateUser(no lease): %v", err)
	}
	_, noLeaseSecret, err := f.store.CreateKey(noLeaseUser.ID, "no lease")
	if err != nil {
		t.Fatalf("CreateKey(no lease): %v", err)
	}
	if _, ok := bound.Authenticate(noLeaseUser.ID, noLeaseSecret); ok {
		t.Fatal("panel key without a lease authenticated")
	}
	_, noLeaseScoped, err := f.store.CreateScopedKey(model.ScopedKeyCreateRequest{
		UserID: noLeaseUser.ID, Label: "no lease scoped",
		Scopes: []model.Scope{model.ScopeProxyConnect}, AccountIDs: []string{f.account.ID},
	})
	if err != nil {
		t.Fatalf("CreateScopedKey(no lease): %v", err)
	}
	if principal, ok := bound.Authenticate(noLeaseUser.ID, noLeaseScoped); ok {
		t.Fatalf("scoped key without a lease authenticated as %+v", principal)
	}

	// Zero, one and many: only exactly one active lease for the pair resolves.
	if _, ok := singleActiveLease(nil, f.user.ID, f.account.ID); ok {
		t.Fatal("no lease resolved")
	}
	if got, ok := singleActiveLease([]model.Lease{f.lease}, f.user.ID, f.account.ID); !ok || got.ID != f.lease.ID {
		t.Fatalf("single lease resolved to %+v/%v", got, ok)
	}
	ambiguous := []model.Lease{
		f.lease,
		{ID: "lease-99", UserID: f.user.ID, AccountID: f.account.ID, Status: model.LeaseActive},
	}
	if _, ok := singleActiveLease(ambiguous, f.user.ID, f.account.ID); ok {
		t.Fatal("two active leases resolved to one")
	}
	foreignPair := []model.Lease{
		{ID: "lease-98", UserID: f.user.ID, AccountID: "acc-other", Status: model.LeaseActive},
		{ID: "lease-97", UserID: "u-other", AccountID: f.account.ID, Status: model.LeaseActive},
	}
	if _, ok := singleActiveLease(foreignPair, f.user.ID, f.account.ID); ok {
		t.Fatal("a lease of another user/account resolved")
	}

	// A principal naming a real lease of another user or another account is
	// refused at admission, and so is a principal with no lease id.
	_, foreignUser, foreignLease := addForeignLease(t, f.store)
	for _, p := range []gate.Principal{
		{UserID: f.user.ID, UserName: f.user.Name, LeaseID: foreignLease.ID},
		{UserID: foreignUser.ID, UserName: foreignUser.Name, LeaseID: foreignLease.ID},
		{UserID: f.user.ID, UserName: f.user.Name},
	} {
		if ok, class, _, _ := bound.Admit(p, "127.0.0.1"); ok || class != gate.FlowErrorLease {
			t.Fatalf("admission for %+v = %v/%q, want lease_inactive", p, ok, class)
		}
	}

	// The same gate account, another user's active lease: only the user half of
	// the check can refuse this, so it pins that half on its own.
	_, sameAccountLease := addSameAccountLease(t, f.store, f.account.ID)
	if sameAccountLease.AccountID != f.account.ID || sameAccountLease.UserID == f.user.ID {
		t.Fatalf("same-account lease = %+v, want another user's active lease on the gate account", sameAccountLease)
	}
	if ok, class, _, _ := bound.Admit(gate.Principal{
		UserID: f.user.ID, UserName: f.user.Name, LeaseID: sameAccountLease.ID,
	}, "127.0.0.1"); ok || class != gate.FlowErrorLease {
		t.Fatalf("admission on another user's lease of this account = %v/%q, want lease_inactive", ok, class)
	}
	// The gate user's own lease is still the one that admits.
	if principal, ok := bound.Authenticate(f.user.ID, f.secret); !ok || principal.LeaseID != f.lease.ID {
		t.Fatalf("gate user after a second account lease = %+v/%v, want %q", principal, ok, f.lease.ID)
	}

	// A lease revoked between the handshake and CONNECT refuses the flow
	// before any vendor call.
	pending := f.dial(t)
	if status := authenticate(t, pending, f.user.ID, f.secret); status != 0 {
		t.Fatalf("auth status = %#x, want 0x00", status)
	}
	if _, err := f.store.RevokeLease(f.lease.ID); err != nil {
		t.Fatalf("RevokeLease: %v", err)
	}
	if rep := connectIPv4(t, pending, net.IPv4(127, 0, 0, 1), 443); rep != 2 {
		t.Fatalf("CONNECT after lease revoke = %#x, want 0x02", rep)
	}
	if opens := f.session.recorded(); len(opens) != 0 {
		t.Fatalf("OpenFlow calls = %v, want none for a lease-less flow", opens)
	}
	records := f.server.Flows(0)
	if len(records) != 1 || records[0].Status != gate.FlowStatusRejected || records[0].ErrorClass != gate.FlowErrorLease {
		t.Fatalf("flow records = %+v, want one rejected lease_inactive record", records)
	}

	// With the lease gone, the next handshake has nothing to bind to.
	after := f.dial(t)
	if status := authenticate(t, after, f.user.ID, f.secret); status != 1 {
		t.Fatalf("auth status after lease revoke = %#x, want 0x01", status)
	}

	// An expired lease is not an entitlement either, for both credential kinds.
	g := newScopedGateFixture(t)
	expired := NewGateIdentity(expireLeases(t, g.store)).ForAccount(g.account.ID)
	if _, ok := expired.Authenticate(g.user.ID, g.secret); ok {
		t.Fatal("scoped key with an expired lease authenticated")
	}
	if _, ok := expired.Authenticate(g.user.ID, g.panelSecret); ok {
		t.Fatal("panel key with an expired lease authenticated")
	}

	// Two active leases for the same (user, account) are ambiguous: the scoped
	// credential cannot resolve one and is refused. Only a hand-edited or
	// corrupted snapshot can carry that shape.
	h := newScopedGateFixture(t)
	dup := duplicateActiveLease(t, h.store)
	duplicateIdentity := NewGateIdentity(dup).ForAccount(h.account.ID)
	if leases := dup.Leases(); len(leases) != 2 {
		t.Fatalf("restored leases = %d, want 2", len(leases))
	}
	if principal, ok := duplicateIdentity.Authenticate(h.user.ID, h.secret); ok {
		t.Fatalf("scoped key with two active leases authenticated as %+v", principal)
	}
	// Dropping the duplicate restores the exactly-one binding.
	if _, err := dup.RevokeLease("lease-99"); err != nil {
		t.Fatalf("RevokeLease(duplicate): %v", err)
	}
	principal, ok = duplicateIdentity.Authenticate(h.user.ID, h.secret)
	if !ok || principal.LeaseID != h.lease.ID {
		t.Fatalf("after dropping the duplicate = %+v/%v, want %q", principal, ok, h.lease.ID)
	}
}

// TestFlowMeterBindsAccountLease proves the production wiring: the metered
// factory installs the account-bound wrapper by default, so a scoped key relays
// only while its lease lives, the legacy panel key relays through the same
// gate, and a lease revoked after the handshake refuses the CONNECT.
func TestFlowMeterBindsAccountLease(t *testing.T) {
	f := newScopedGateFixture(t)

	conn := f.dial(t)
	if status := authenticate(t, conn, f.user.ID, f.secret); status != 0 {
		t.Fatalf("scoped auth status = %#x, want 0x00", status)
	}
	if rep := connectIPv4(t, conn, net.IPv4(127, 0, 0, 1), 443); rep != 0 {
		t.Fatalf("scoped CONNECT reply = %#x, want success", rep)
	}
	echoBytes(t, conn, []byte("scoped"))
	_ = conn.Close()
	rows := waitUserRows(t, f.store, 1)
	if rows[0].UserID != f.user.ID || rows[0].AccountLabel != f.account.Label {
		t.Fatalf("row attribution = %q/%q, want %q/%q",
			rows[0].UserID, rows[0].AccountLabel, f.user.ID, f.account.Label)
	}

	legacy := f.dial(t)
	if status := authenticate(t, legacy, f.user.ID, f.panelSecret); status != 0 {
		t.Fatalf("legacy auth status = %#x, want 0x00", status)
	}
	if rep := connectIPv4(t, legacy, net.IPv4(127, 0, 0, 1), 443); rep != 0 {
		t.Fatalf("legacy CONNECT reply = %#x, want success", rep)
	}
	echoBytes(t, legacy, []byte("legacy"))
	_ = legacy.Close()
	if rows := waitUserRows(t, f.store, 2); len(rows) != 2 {
		t.Fatalf("gate rows = %d, want 2", len(rows))
	}

	// The lease is re-read at admission: revoking it after the handshake
	// refuses the CONNECT and never calls OpenFlow.
	pending := f.dial(t)
	if status := authenticate(t, pending, f.user.ID, f.secret); status != 0 {
		t.Fatalf("pending auth status = %#x, want 0x00", status)
	}
	if _, err := f.store.RevokeLease(f.lease.ID); err != nil {
		t.Fatalf("RevokeLease: %v", err)
	}
	if rep := connectIPv4(t, pending, net.IPv4(127, 0, 0, 1), 443); rep != 2 {
		t.Fatalf("CONNECT after lease revoke = %#x, want 0x02", rep)
	}
	if opens := f.session.recorded(); len(opens) != 2 {
		t.Fatalf("OpenFlow calls = %v, want the two admitted flows only", opens)
	}
	records := f.server.Flows(0)
	refused := false
	for _, record := range records {
		if record.Status == gate.FlowStatusRejected && record.ErrorClass == gate.FlowErrorLease {
			refused = true
		}
	}
	if !refused {
		t.Fatalf("flow records = %+v, want a rejected lease_inactive record", records)
	}

	// Nothing is left to bind to, so the next handshake is refused outright.
	after := f.dial(t)
	if status := authenticate(t, after, f.user.ID, f.secret); status != 1 {
		t.Fatalf("auth status after lease revoke = %#x, want 0x01", status)
	}
}

// stripScopedKeyGrants rewrites every scoped key's accountIds/regions to empty
// and restores the document into a fresh store. Creation rejects that shape, so
// only a restored or hand-edited snapshot can carry it.
func stripScopedKeyGrants(t *testing.T, st *store.Store) *store.Store {
	t.Helper()
	data, err := st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	keys, ok := doc["scopedKeys"].([]any)
	if !ok || len(keys) == 0 {
		t.Fatalf("snapshot carries no scoped keys")
	}
	for _, raw := range keys {
		record, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("snapshot scoped key is %T, want an object", raw)
		}
		record["accountIds"] = []any{}
		record["regions"] = []any{}
	}
	edited, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("encode snapshot: %v", err)
	}
	restored := store.NewEmpty()
	if err := restored.Restore(edited); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	return restored
}

// TestGateIdentityRestoredUngrantedProxyKeyFailsClosed pins the grant-shape
// rule at the gate. Creation rejects a proxy:connect key without an account or
// region grant, so a restored snapshot that carries one must fail closed like
// the API's scopedKeyDenial: an empty grant list can never mean "every
// account" for a customer credential.
func TestGateIdentityRestoredUngrantedProxyKeyFailsClosed(t *testing.T) {
	f := newScopedGateFixture(t)
	restored := stripScopedKeyGrants(t, f.store)

	keys := restored.ScopedKeys()
	if len(keys) != 1 {
		t.Fatalf("restored scoped keys = %d, want 1", len(keys))
	}
	if !keys[0].RequiresAccountGrant() || keys[0].HasAccountGrant() {
		t.Fatalf("restored key shape = %+v, want requires-grant-with-none", keys[0])
	}
	// The secret must still resolve after the restore, so the refusal below is
	// the grant shape and not a lost hash.
	if resolved, ok := restored.AuthenticateScopedKey(f.secret); !ok || resolved.ID != keys[0].ID {
		t.Fatalf("restored secret resolved to %+v/%v, want the ungranted key", resolved, ok)
	}

	bound := NewGateIdentity(restored).ForAccount(f.account.ID)
	if principal, ok := bound.Authenticate(f.user.ID, f.secret); ok {
		t.Fatalf("restored proxy:connect key without a grant authenticated as %+v", principal)
	}

	// Control: the same restored store still admits the legacy panel key, so
	// the refusal is the missing grant, not the restore.
	principal, ok := bound.Authenticate(f.user.ID, f.panelSecret)
	if !ok || principal.LeaseID != f.lease.ID {
		t.Fatalf("legacy key after restore = %+v/%v, want the lease-bound principal", principal, ok)
	}
}

// addSameAccountLease gives accountID a second live node and one active lease
// held by another user, so a principal can name a lease that is active on the
// gate's own account but belongs to someone else. Capacity is raised for the
// test (AJS_ACCOUNT_SLOTS=2 plus a use count of 2), which is exactly what the
// second serving node would allow in production.
func addSameAccountLease(t *testing.T, st *store.Store, accountID string) (model.PanelUser, model.Lease) {
	t.Helper()
	t.Setenv("AJS_ACCOUNT_SLOTS", "2")
	if _, err := st.SetAccountUseCount(accountID, 2); err != nil {
		t.Fatalf("SetAccountUseCount: %v", err)
	}
	if _, err := st.PutNode(model.ProxyNode{
		ID: "node-scoped-2", AccountID: accountID, Label: "cn-zj-03", Region: "cn-zj",
		State: model.NodeLive, EgressIP: "203.0.113.32", Protocol: model.ProtocolSOCKS5, GatePort: 13033,
	}); err != nil {
		t.Fatalf("PutNode(second): %v", err)
	}
	user, err := st.CreateUser(model.CreateUserInput{
		Name: "same-account-other", Allowlist: []string{"127.0.0.1/32"},
	})
	if err != nil {
		t.Fatalf("CreateUser(same account): %v", err)
	}
	lease, _, err := st.CreateLease(model.CreateLeaseInput{
		UserID: user.ID, Platform: model.PlatformLinux, AccountID: accountID,
	})
	if err != nil {
		t.Fatalf("CreateLease(same account): %v", err)
	}
	return user, lease
}

// duplicateActiveLease restores a document that carries a second active lease
// for the fixture's user on a second live node of the same account. CreateLease
// refuses that shape (one lease per user and platform), so only a hand-edited
// or corrupted snapshot can hold it; the document also raises the account's
// capacity so the restore keeps both rows instead of dropping one.
func duplicateActiveLease(t *testing.T, st *store.Store) *store.Store {
	t.Helper()
	data, err := st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	accounts, ok := doc["accounts"].([]any)
	if !ok || len(accounts) != 1 {
		t.Fatalf("snapshot accounts = %v, want one", doc["accounts"])
	}
	account, ok := accounts[0].(map[string]any)
	if !ok {
		t.Fatalf("snapshot account is %T, want an object", accounts[0])
	}
	accountID, _ := account["id"].(string)
	accountLabel, _ := account["label"].(string)
	account["useCount"] = 2

	nodes, ok := doc["nodes"].([]any)
	if !ok {
		t.Fatalf("snapshot nodes = %v, want a list", doc["nodes"])
	}
	doc["nodes"] = append(nodes, map[string]any{
		"id": "node-scoped-2", "accountId": accountID, "accountLabel": accountLabel,
		"label": "cn-zj-03", "region": "cn-zj", "state": string(model.NodeLive),
		"egressIp": "203.0.113.32", "protocol": string(model.ProtocolSOCKS5), "gatePort": 13033,
	})

	leases, ok := doc["leases"].([]any)
	if !ok || len(leases) != 1 {
		t.Fatalf("snapshot leases = %v, want one", doc["leases"])
	}
	original, ok := leases[0].(map[string]any)
	if !ok {
		t.Fatalf("snapshot lease is %T, want an object", leases[0])
	}
	userID, _ := original["userId"].(string)
	userName, _ := original["userName"].(string)
	now := time.Now().UTC()
	doc["leases"] = append(leases, map[string]any{
		"id": "lease-99", "userId": userID, "userName": userName,
		"accountId": accountID, "accountLabel": accountLabel,
		"platform": string(model.PlatformLinux), "nodeId": "node-scoped-2",
		"nodeLabel": "cn-zj-03", "region": "cn-zj", "egressIp": "203.0.113.32",
		"protocol": string(model.ProtocolSOCKS5), "gatePort": 13033,
		"ttlSeconds": 3600, "expiresAt": now.Add(time.Hour).Format(time.RFC3339),
		"sticky": true, "status": string(model.LeaseActive),
		"idempotencyKey": "", "createdAt": now.Format(time.RFC3339),
	})

	edited, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("encode snapshot: %v", err)
	}
	// Restore derives capacity from AJS_ACCOUNT_SLOTS, so the second serving
	// node must count before the duplicate row is admitted.
	t.Setenv("AJS_ACCOUNT_SLOTS", "2")
	restored := store.NewEmpty()
	if err := restored.Restore(edited); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	return restored
}
