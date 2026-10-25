package runtime_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ajsaccountd/internal/account"
	"ajsaccountd/internal/gate"
	"ajsaccountd/internal/model"
	"ajsaccountd/internal/runtime"
	"ajsaccountd/internal/store"
	"ajsproxy/apifacade"
	"ajsproxy/fvfacade"
)

// The tests in this file are the AJS-355 regression proofs for the two
// REVIEW-MULTISLOT-FIXES.md residuals:
//
//  1. an authenticated-SID refusal with the vendor's session-expiry text (not
//     the exact takeover marker) is a session loss: the production adapter
//     marks it, and containment takes the generation-matched slot DOWN,
//     revokes its lease and closes its listener while a sibling keeps serving;
//  2. a login that was already in flight when a logout ran cannot install a
//     gate or listener after the logout returns, and a paused account cannot
//     sync LIVE.

const (
	lossTestSID           = "sid-loss-1"
	lossConnectPassword   = "cp-loss-not-a-credential"
	lossSessionRefusal    = "session expired; authenticate again"
	lossControlLoginReply = `{"Login":{"AuthResult":"OK","LoginToken":"tok-1|user",` +
		`"UserName":"vendor-user","ConnectPassword":"cp-1","MembershipStatus":"vip"}}`
	lossControlSyncReply = `{"Login":{"ConnectPassword":"cp-2"},"Servers":{"s1":{"id":"s1",` +
		`"vsid":1,"ctry":"CN","svr":"203.0.113.10","tit":"Shanghai 01","cmt":"",` +
		`"sts":"ok","ord":"1","cat":["cn"],"par":{"fv":{"u":1001,"p":443}},"loc":[{"c":"CN","n":"Shanghai","o":1}]}}}`
)

var lossZeroIV = make([]byte, fvfacade.IVSize)

// kickVendor is an in-process protocol-2 FV server that authenticates one
// session and refuses its data channel with a 403 text. It is the production
// wire shape, so the factory's own classifier runs in the test.
type kickVendor struct {
	ln      net.Listener
	priv    *rsa.PrivateKey
	refusal string
	errs    chan error
}

func startKickVendor(t *testing.T, refusal string) *kickVendor {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("generate test key: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen kick vendor: %v", err)
	}
	v := &kickVendor{ln: ln, priv: priv, refusal: refusal, errs: make(chan error, 1)}
	go func() { v.errs <- v.serve() }()
	t.Cleanup(func() { _ = ln.Close() })
	return v
}

func (v *kickVendor) wait(t *testing.T) {
	t.Helper()
	select {
	case err := <-v.errs:
		if err != nil {
			t.Fatalf("kick vendor: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("kick vendor did not finish")
	}
}

func (v *kickVendor) serve() error {
	auth, err := v.ln.Accept()
	if err != nil {
		return fmt.Errorf("accept auth connection: %w", err)
	}
	defer auth.Close()
	key, sessionKey, iv, err := v.authenticate(auth)
	if err != nil {
		return err
	}
	if err := writeLossCarrierText(auth, key, "code=200\nr=ok\ns=test\nsid="+lossTestSID+"\n"); err != nil {
		return err
	}
	mapText := "c=aur\nr=ok\ns=test\nsid=" + lossTestSID + "\nok=1\nsip=10.0.0.1\n"
	mapCT, err := fvfacade.AESCTR(sessionKey, iv, []byte(mapText))
	if err != nil {
		return err
	}
	if _, err := auth.Write(mapCT); err != nil {
		return fmt.Errorf("write auth map: %w", err)
	}
	if err := closeLossWrite(auth); err != nil {
		return err
	}
	data, err := v.ln.Accept()
	if err != nil {
		return fmt.Errorf("accept data connection: %w", err)
	}
	defer data.Close()
	dataKey, fields, err := readLossCarrierText(data)
	if err != nil {
		return err
	}
	if fields["s"] != lossTestSID {
		return fmt.Errorf("data request sid = %q, want %q", fields["s"], lossTestSID)
	}
	return writeLossCarrierText(data, dataKey, "code=403\nfm="+v.refusal+"\n")
}

// authenticate decodes the binary-carrier auth request and returns the outer
// key, the RSA-OAEP session key and the body IV. It proves the production
// factory, not a stub, sent a complete authenticated request.
func (v *kickVendor) authenticate(conn net.Conn) (key, sessionKey, iv []byte, err error) {
	key, fields, err := readLossCarrierText(conn)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read auth request: %w", err)
	}
	block, err := base64.StdEncoding.DecodeString(fields["k"])
	if err != nil || len(block) <= len(fvfacade.OAEPBlockMagic) {
		return nil, nil, nil, fmt.Errorf("k field is not an %s block", fvfacade.OAEPBlockMagic)
	}
	hash, err := fvfacade.HashByName(account.Linux423Binary.OAEPHash)
	if err != nil {
		return nil, nil, nil, err
	}
	sessionKey, err = rsa.DecryptOAEP(hash.New(), rand.Reader, v.priv, block[len(fvfacade.OAEPBlockMagic):], nil)
	if err != nil || len(sessionKey) != fvfacade.SessionKeySize {
		return nil, nil, nil, fmt.Errorf("decrypt session key: %v", err)
	}
	bodyLen, err := strconv.Atoi(fields["l"])
	if err != nil || bodyLen <= 0 {
		return nil, nil, nil, fmt.Errorf("l field = %q", fields["l"])
	}
	body := make([]byte, bodyLen)
	if _, err := io.ReadFull(conn, body); err != nil {
		return nil, nil, nil, fmt.Errorf("read auth body: %w", err)
	}
	iv, err = hex.DecodeString(fields["n"])
	if err != nil || len(iv) != fvfacade.IVSize {
		return nil, nil, nil, fmt.Errorf("n field = %q", fields["n"])
	}
	plain, err := fvfacade.AESCTR(sessionKey, iv, body)
	if err != nil {
		return nil, nil, nil, err
	}
	auth := fvfacade.ParseFields(string(plain))
	if auth["u"] == "" || auth["p"] == "" {
		return nil, nil, nil, fmt.Errorf("auth body has no user/password")
	}
	return key, sessionKey, iv, nil
}

// readLossCarrierText reads [16B K][AES-CTR(K,0)(4B len || text)] and returns
// the key and parsed text, leaving any body for the caller.
func readLossCarrierText(conn net.Conn) ([]byte, map[string]string, error) {
	key := make([]byte, fvfacade.IVSize)
	if _, err := io.ReadFull(conn, key); err != nil {
		return nil, nil, fmt.Errorf("read carrier key: %w", err)
	}
	head := make([]byte, fvfacade.HeaderSize)
	if _, err := io.ReadFull(conn, head); err != nil {
		return nil, nil, fmt.Errorf("read carrier header: %w", err)
	}
	plainHead, err := fvfacade.AESCTR(key, lossZeroIV, head)
	if err != nil {
		return nil, nil, err
	}
	textLen := int(binary.BigEndian.Uint16(plainHead[2:4]))
	textCT := make([]byte, textLen)
	if _, err := io.ReadFull(conn, textCT); err != nil {
		return nil, nil, fmt.Errorf("read carrier text: %w", err)
	}
	plain, err := fvfacade.AESCTR(key, lossZeroIV, append(head, textCT...))
	if err != nil {
		return nil, nil, err
	}
	return key, fvfacade.ParseFields(string(plain[fvfacade.HeaderSize:])), nil
}

func writeLossCarrierText(conn net.Conn, key []byte, text string) error {
	plain := make([]byte, fvfacade.HeaderSize+len(text))
	binary.BigEndian.PutUint16(plain[2:4], uint16(len(text)))
	copy(plain[fvfacade.HeaderSize:], text)
	ct, err := fvfacade.AESCTR(key, lossZeroIV, plain)
	if err != nil {
		return err
	}
	if _, err := conn.Write(ct); err != nil {
		return fmt.Errorf("write carrier text: %w", err)
	}
	return nil
}

func closeLossWrite(conn net.Conn) error {
	if tcp, ok := conn.(*net.TCPConn); ok {
		return tcp.CloseWrite()
	}
	return conn.Close()
}

// lossServer points one login-result server entry at a vendor listener.
func lossServer(id string, ln net.Listener) apifacade.Server {
	host, portText, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portText)
	return apifacade.Server{
		ID: id, Svr: host, Sts: "ok", Ord: "1",
		Par: apifacade.ServerPar{FV: apifacade.ServerFV{P: apifacade.FlexInt(port)}},
	}
}

// lossOpener routes one server to the production factory and the sibling to a
// fake session, so a test can prove one slot's loss leaves the other serving.
type lossOpener struct {
	factory *account.Factory
	healthy *fakeSessionOpener

	mu    sync.Mutex
	opens map[string]int
}

func newLossOpener(factory *account.Factory, healthy *fakeSessionOpener) *lossOpener {
	return &lossOpener{factory: factory, healthy: healthy, opens: make(map[string]int)}
}

func (o *lossOpener) Open(ctx context.Context, req account.SessionRequest) (account.FlowSession, error) {
	o.mu.Lock()
	o.opens[req.Server.ID]++
	o.mu.Unlock()
	if req.Server.ID == "s1" {
		return o.factory.Open(ctx, req)
	}
	return o.healthy.Open(ctx, req)
}

func (o *lossOpener) openCount(serverID string) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.opens[serverID]
}

// lossGateFactory builds real loopback gates and counts them.
type lossGateFactory struct {
	built atomic.Int32
}

func (f *lossGateFactory) build(cfg gate.Config, session gate.Session) (runtime.Gate, error) {
	srv, err := gate.NewServer(cfg, session)
	if err != nil {
		return nil, err
	}
	f.built.Add(1)
	return srv, nil
}

func (f *lossGateFactory) count() int { return int(f.built.Load()) }

// lossDial dials a loopback gate and reports whether a listener accepted.
func lossDial(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 250*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// lossSocksConnect performs one no-auth SOCKS5 CONNECT and returns the reply
// code. The gate answers 0x05 when the vendor refuses the flow.
func lossSocksConnect(t *testing.T, port int, host string) byte {
	t.Helper()
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 2*time.Second)
	if err != nil {
		t.Fatalf("dial gate %d: %v", port, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte{5, 1, 0}); err != nil {
		t.Fatalf("write greeting: %v", err)
	}
	var method [2]byte
	if _, err := io.ReadFull(conn, method[:]); err != nil {
		t.Fatalf("read method reply: %v", err)
	}
	request := []byte{5, 1, 0, 3, byte(len(host))}
	request = append(request, host...)
	request = append(request, 0, 80)
	if _, err := conn.Write(request); err != nil {
		t.Fatalf("write connect: %v", err)
	}
	var reply [10]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		t.Fatalf("read connect reply: %v", err)
	}
	return reply[1]
}

// TestEquivalentSessionLossContainsGenerationMatchedSlot proves finding 1's
// reopen falsifier at runtime scope: the production factory receives the
// vendor's "session expired; authenticate again" refusal, the slot that owned
// that session leaves LIVE at its running generation, its lease is revoked and
// its listener stops accepting, while the sibling slot keeps its gate, session
// and generation. A sync inside the retry window does not reopen it.
func TestEquivalentSessionLossContainsGenerationMatchedSlot(t *testing.T) {
	t.Setenv("AJS_ACCOUNT_SLOTS", "2")
	st := newNodeTestPoolStore(t)
	acct := newNodeTestAccount(t, st, "slot-loss")
	grantSlots(t, st, acct, 2)
	vendor := startKickVendor(t, lossSessionRefusal)
	healthy := newFakeSessionOpener()
	logins := newFakeLoginResults()
	logins.set(acct.ID, account.LoginResult{
		AuthResult:      "OK",
		UserName:        "vendor-user",
		ConnectPassword: account.Secret(lossConnectPassword),
		Servers: map[string]apifacade.Server{
			"s1": lossServer("s1", vendor.ln),
			"s2": testServer("s2"),
		},
		FetchedAt: time.Date(2026, 10, 26, 11, 0, 0, 0, time.UTC),
	})
	factory := account.NewFactory(account.Linux423Binary,
		&fvfacade.HTTPUpgrade{HandshakeTimeout: 5 * time.Second},
		fvfacade.AuthOptions{PublicKey: &vendor.priv.PublicKey, IdleTimeout: 200 * time.Millisecond})
	opener := newLossOpener(factory, healthy)
	gates := &lossGateFactory{}
	clock := newSwitchClock()
	controller, err := runtime.NewNodeController(runtime.NodeControllerConfig{
		GateAuth:         gate.AuthLoopback,
		Store:            st,
		Logins:           logins,
		Sessions:         opener,
		Gates:            gates.build,
		MaxServers:       2,
		PortBase:         13600,
		Now:              clock.Now,
		LivenessInterval: -1,
		Logger:           discardLogger(),
	})
	if err != nil {
		t.Fatalf("NewNodeController: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = controller.Stop(stopCtx)
	})

	if err := controller.SyncAccount(context.Background(), acct.ID); err != nil {
		t.Fatalf("SyncAccount: %v", err)
	}
	before := controller.Snapshot()
	if len(before) != 2 {
		t.Fatalf("snapshot = %+v, want two running slots", before)
	}
	var slot1, slot2 runtime.NodeRuntime
	for _, running := range before {
		switch running.Slot {
		case 1:
			slot1 = running
		case 2:
			slot2 = running
		}
	}
	if slot1.Port == 0 || slot2.Port == 0 {
		t.Fatalf("running ports = %d/%d, want both bound", slot1.Port, slot2.Port)
	}
	key1 := model.SlotKey{AccountID: acct.ID, Index: 1}
	slotRow, ok := st.Slot(key1)
	if !ok || slotRow.State != model.SlotLive || slotRow.Generation != slot1.Generation {
		t.Fatalf("slot 1 = %+v, want LIVE at the running generation %d", slotRow, slot1.Generation)
	}

	// One customer lease pinned to the slot that will lose its session.
	user, err := st.CreateUser(model.CreateUserInput{Name: "loss-bot"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	lease, _, err := st.CreateLease(model.CreateLeaseInput{
		UserID: user.ID, Platform: model.PlatformLinux, AccountID: acct.ID,
	})
	if err != nil {
		t.Fatalf("CreateLease: %v", err)
	}
	if lease.Slot != 1 || lease.Generation != slot1.Generation {
		t.Fatalf("lease = %+v, want slot 1 generation %d", lease, slot1.Generation)
	}

	// A real SOCKS5 flow reaches the production factory; the vendor refuses
	// the authenticated SID, so the gate refuses the flow and the session
	// reports the loss.
	if code := lossSocksConnect(t, slot1.Port, "example.com"); code == 0 {
		t.Fatal("the gate accepted a flow the vendor refused")
	}
	if got := controller.CheckLiveness(); got != 1 {
		t.Fatalf("CheckLiveness contained %d gates, want 1", got)
	}

	// Generation-matched DOWN transition: the slot keeps the generation that
	// was running, not a bumped one.
	slotRow, ok = st.Slot(key1)
	if !ok || slotRow.State != model.SlotDown || slotRow.Health != model.SlotHealthVendorKick {
		t.Fatalf("slot 1 after loss = %+v, want DOWN/vendor_kick", slotRow)
	}
	if slotRow.Generation != slot1.Generation {
		t.Fatalf("slot 1 generation after loss = %d, want the running generation %d",
			slotRow.Generation, slot1.Generation)
	}
	node1 := nodeByServer(t, st, acct.ID, "s1")
	if node1.State != model.NodeDown || node1.GatePort != 0 || node1.EgressIP != "" {
		t.Fatalf("node s1 after loss = %+v, want DOWN without port or egress", node1)
	}

	// The lease is revoked explicitly and the listener is gone.
	if _, ok := st.Lease(lease.ID); ok {
		t.Fatalf("lease %s survived the session loss", lease.ID)
	}
	if lossDial(net.JoinHostPort("127.0.0.1", strconv.Itoa(slot1.Port))) {
		t.Fatalf("listener %d still accepts after the loss", slot1.Port)
	}
	if session := healthy.session("s1"); session != nil {
		t.Fatalf("s1 must not have a fake session: %+v", session)
	}

	// The sibling is untouched: same gate, session, generation and listener.
	after := controller.Snapshot()
	if len(after) != 1 || after[0].Slot != 2 {
		t.Fatalf("snapshot after loss = %+v, want only slot 2", after)
	}
	if after[0].Generation != slot2.Generation || after[0].Port != slot2.Port {
		t.Fatalf("sibling runtime = %+v, want generation %d port %d", after[0], slot2.Generation, slot2.Port)
	}
	slot2Row, ok := st.Slot(model.SlotKey{AccountID: acct.ID, Index: 2})
	if !ok || slot2Row.State != model.SlotLive || slot2Row.Generation != slot2.Generation {
		t.Fatalf("slot 2 after sibling loss = %+v, want LIVE generation %d", slot2Row, slot2.Generation)
	}
	if node2 := nodeByServer(t, st, acct.ID, "s2"); node2.State != model.NodeLive || node2.GatePort != slot2.Port {
		t.Fatalf("node s2 after sibling loss = %+v, want LIVE on port %d", node2, slot2.Port)
	}
	if !lossDial(net.JoinHostPort("127.0.0.1", strconv.Itoa(slot2.Port))) {
		t.Fatalf("sibling listener %d stopped accepting", slot2.Port)
	}

	// The five-minute retry window blocks an automatic reopen of the lost slot.
	opens := opener.openCount("s1")
	if err := controller.SyncAccount(context.Background(), acct.ID); err != nil {
		t.Fatalf("SyncAccount inside the retry window: %v", err)
	}
	if got := opener.openCount("s1"); got != opens {
		t.Fatalf("session opens for the lost slot inside the retry window = %d, want %d", got, opens)
	}
	if slotRow, ok = st.Slot(key1); !ok || slotRow.State != model.SlotDown {
		t.Fatalf("lost slot after a blocked sync = %+v, want DOWN", slotRow)
	}
	vendor.wait(t)
}

// TestPausedAccountCannotSyncLive proves the state half of finding 2: a login
// hook that lands while the account is paused installs nothing, while a real
// re-login (which marks the account active again) still opens the slot.
func TestPausedAccountCannotSyncLive(t *testing.T) {
	f := newSlotThreatFixture(t)
	if _, err := f.st.LogoutAccount(f.acct.ID); err != nil {
		t.Fatalf("LogoutAccount: %v", err)
	}
	err := f.controller.SyncAccount(context.Background(), f.acct.ID)
	requireSwitchReason(t, err, runtime.ReasonAccountState)
	if got := f.gates.built(); got != 0 {
		t.Fatalf("gates built for a paused account = %d, want 0", got)
	}
	if running := f.controller.Snapshot(); len(running) != 0 {
		t.Fatalf("paused account sync left %d running gates", len(running))
	}

	// A successful login marks the account active before its hook runs, so the
	// refusal above is not a permanent block.
	if _, err := f.st.MarkLogin(f.acct.ID, "", "", false); err != nil {
		t.Fatalf("MarkLogin: %v", err)
	}
	if err := f.controller.SyncAccount(context.Background(), f.acct.ID); err != nil {
		t.Fatalf("SyncAccount after a successful re-login: %v", err)
	}
	if running := f.controller.Snapshot(); len(running) != 1 {
		t.Fatalf("snapshot after re-login = %+v, want one running gate", running)
	}
}

// blockingControlServer is the control API for one account. The first login
// completes immediately; the test can make the next login block inside the
// vendor call so a logout can run while it is in flight.
type blockingControlServer struct {
	server  *httptest.Server
	block   atomic.Bool
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func newBlockingControlServer(t *testing.T) *blockingControlServer {
	t.Helper()
	c := &blockingControlServer{entered: make(chan struct{}), release: make(chan struct{})}
	c.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read error", http.StatusInternalServerError)
			return
		}
		form, err := url.ParseQuery(string(body))
		if err != nil {
			http.Error(w, "parse error", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if form.Get("_client_login") != "" {
			_, _ = io.WriteString(w, lossControlSyncReply)
			return
		}
		if c.block.Load() {
			c.once.Do(func() { close(c.entered) })
			<-c.release
		}
		_, _ = io.WriteString(w, lossControlLoginReply)
	}))
	t.Cleanup(c.server.Close)
	return c
}

func (c *blockingControlServer) waitEntered(t *testing.T) {
	t.Helper()
	select {
	case <-c.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the second login never reached the vendor call")
	}
}

// TestLoginCompletingDuringLogoutCannotInstallListener repeats the AJS-353
// interleaving with the production LoginService: a login is in flight when the
// logout hook fences and closes the account, it completes afterwards with a
// fresh FetchedAt, and the logout then pauses the account. The login hook must
// install no gate, session or listener, and the retained result must not
// outlive Forget.
func TestLoginCompletingDuringLogoutCannotInstallListener(t *testing.T) {
	clock := newSwitchClock()
	st := store.NewWithSealer(testSealer{})
	if err := st.Restore([]byte(`{"version":1}`)); err != nil {
		t.Fatalf("Restore empty snapshot: %v", err)
	}
	acct := newNodeTestAccount(t, st, "logout-race")
	control := newBlockingControlServer(t)
	service, err := account.NewLoginService(st, account.LoginConfig{
		Hosts:      []string{control.server.URL},
		Enabled:    true,
		HTTPClient: control.server.Client(),
		Now:        clock.Now,
		Logger:     discardLogger(),
	})
	if err != nil {
		t.Fatalf("NewLoginService: %v", err)
	}
	t.Cleanup(service.Close)
	opener := newFakeSessionOpener()
	gates := &lossGateFactory{}
	controller, err := runtime.NewNodeController(runtime.NodeControllerConfig{
		GateAuth:         gate.AuthLoopback,
		Store:            st,
		Logins:           service,
		Sessions:         opener,
		Gates:            gates.build,
		MaxServers:       1,
		PortBase:         13610,
		Now:              clock.Now,
		LivenessInterval: -1,
		Logger:           discardLogger(),
	})
	if err != nil {
		t.Fatalf("NewNodeController: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = controller.Stop(stopCtx)
	})
	ctx := context.Background()

	// The first login opens the account's only gate.
	if _, err := service.Login(ctx, acct.ID); err != nil {
		t.Fatalf("first Login: %v", err)
	}
	if err := controller.SyncAccount(ctx, acct.ID); err != nil {
		t.Fatalf("first SyncAccount: %v", err)
	}
	before := controller.Snapshot()
	if len(before) != 1 {
		t.Fatalf("snapshot after the first login = %+v, want one gate", before)
	}
	port := before[0].Port
	if !lossDial(net.JoinHostPort("127.0.0.1", strconv.Itoa(port))) {
		t.Fatalf("the first login's listener %d does not accept", port)
	}

	// A second login is in flight when the logout hook runs.
	clock.Advance(30 * time.Second)
	control.block.Store(true)
	loginErr := make(chan error, 1)
	go func() {
		_, err := service.Login(ctx, acct.ID)
		loginErr <- err
	}()
	control.waitEntered(t)
	if err := controller.StopAccount(ctx, acct.ID); err != nil {
		t.Fatalf("StopAccount: %v", err)
	}
	if running := controller.Snapshot(); len(running) != 0 {
		t.Fatalf("snapshot after StopAccount = %+v, want no gate", running)
	}
	if lossDial(net.JoinHostPort("127.0.0.1", strconv.Itoa(port))) {
		t.Fatalf("listener %d still accepts after StopAccount", port)
	}
	if session := opener.session("s1"); session == nil || !session.isClosed() {
		t.Fatalf("session after StopAccount = %+v, want it closed", session)
	}

	// The login completes after the fence with a fresh FetchedAt, and the
	// logout's store mutation then pauses the account.
	clock.Advance(time.Second)
	close(control.release)
	if err := <-loginErr; err != nil {
		t.Fatalf("second Login: %v", err)
	}
	if _, err := st.LogoutAccount(acct.ID); err != nil {
		t.Fatalf("LogoutAccount: %v", err)
	}

	// The login hook is the API's SyncAccount. It must not install a listener:
	// the attempt began before the stop, so its fresh result is not a reopen.
	err = controller.SyncAccount(ctx, acct.ID)
	requireSwitchReason(t, err, runtime.ReasonAccountState)
	if running := controller.Snapshot(); len(running) != 0 {
		t.Fatalf("login hook left %d gates after logout", len(running))
	}
	if got := gates.count(); got != 1 {
		t.Fatalf("gates built = %d, want only the first login's gate", got)
	}
	if lossDial(net.JoinHostPort("127.0.0.1", strconv.Itoa(port))) {
		t.Fatalf("listener %d accepts after the logout", port)
	}

	// Logout's Forget drops the late result, so no later hook can use it.
	service.Forget(acct.ID)
	if err := controller.SyncAccount(ctx, acct.ID); err == nil {
		t.Fatal("SyncAccount after Forget succeeded")
	}
	if running := controller.Snapshot(); len(running) != 0 {
		t.Fatalf("late sync left %d gates", len(running))
	}
	after, ok := st.Account(acct.ID)
	if !ok || after.State != model.AccountPaused {
		t.Fatalf("account after logout = %+v, want paused", after)
	}
	if lossDial(net.JoinHostPort("127.0.0.1", strconv.Itoa(port))) {
		t.Fatalf("listener %d accepts after the late sync", port)
	}
}
