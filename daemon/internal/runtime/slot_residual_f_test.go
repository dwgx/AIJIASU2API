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
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ajsaccountd/internal/account"
	"ajsaccountd/internal/model"
	"ajsaccountd/internal/runtime"
	"ajsproxy/apifacade"
	"ajsproxy/fvfacade"
)

// The tests in this file close the two REVIEW-MULTISLOT-RESIDUALS findings that
// the AJS-361 re-audit left PARTIAL:
//
//  1. an idle session loss with no client flow is invisible to the passive
//     Alive view. The controller's liveness scan now performs one active
//     vendor probe per running slot, so the lost slot leaves LIVE, its listener
//     and session close, its lease is revoked, the five-minute retry window is
//     recorded and the sibling slot keeps serving — inside one 30 s window;
//  2. SyncAccount joins the account-wide mutation exclusion, and the logout
//     handler holds the claim across its store mutation, so a login that lands
//     between the fence and the pause installs no gate, session, listener or
//     LIVE node.

const (
	idleLossSID    = "sid-idle-loss-1"
	idleLossMarker = "disconnected by another connection"
)

// idleLossVendor is an in-process protocol-2 FV server for the active-probe
// test. It authenticates one session and keeps its control connection open;
// every later data connection is answered as a live session (a flow-level
// SOCKS5 refusal for the sentinel probe target) or, after kick(), as the
// vendor's idle session loss. No client flow is needed to flip the answer.
type idleLossVendor struct {
	ln   net.Listener
	priv *rsa.PrivateKey
	sid  string

	kicked atomic.Bool
	probes atomic.Int32

	mu     sync.Mutex
	errors []error
}

func startIdleLossVendor(t *testing.T, sid string) *idleLossVendor {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("generate test key: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen idle-loss vendor: %v", err)
	}
	v := &idleLossVendor{ln: ln, priv: priv, sid: sid}
	go v.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return v
}

// kick flips the vendor into the idle-loss shape: the control connection and
// the SID survive, and every later data channel is refused as a lost session.
func (v *idleLossVendor) kick() { v.kicked.Store(true) }

func (v *idleLossVendor) probeCount() int { return int(v.probes.Load()) }

func (v *idleLossVendor) addErr(err error) {
	if err == nil {
		return
	}
	v.mu.Lock()
	v.errors = append(v.errors, err)
	v.mu.Unlock()
}

// requireNoErrors fails the test on any vendor-side protocol error.
func (v *idleLossVendor) requireNoErrors(t *testing.T) {
	t.Helper()
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.errors) > 0 {
		t.Fatalf("idle-loss vendor errors: %v", v.errors)
	}
}

func (v *idleLossVendor) serve() {
	auth, err := v.ln.Accept()
	if err != nil {
		return
	}
	defer auth.Close()
	key, sessionKey, iv, err := v.authenticate(auth)
	if err != nil {
		v.addErr(err)
		return
	}
	if err := writeLossCarrierText(auth, key, "code=200\nr=ok\ns=test\nsid="+v.sid+"\n"); err != nil {
		v.addErr(err)
		return
	}
	mapText := "c=aur\nr=ok\ns=test\nsid=" + v.sid + "\nok=1\nsip=10.0.0.1\n"
	mapCT, err := fvfacade.AESCTR(sessionKey, iv, []byte(mapText))
	if err != nil {
		v.addErr(err)
		return
	}
	if _, err := auth.Write(mapCT); err != nil {
		v.addErr(fmt.Errorf("write auth map: %w", err))
		return
	}
	if err := closeLossWrite(auth); err != nil {
		v.addErr(err)
		return
	}
	for {
		conn, err := v.ln.Accept()
		if err != nil {
			return // the test closed the listener
		}
		go func() {
			defer conn.Close()
			v.addErr(v.serveData(conn, sessionKey))
		}()
	}
}

// authenticate decodes the binary-carrier auth request and returns the outer
// key, the RSA-OAEP session key and the body IV. It is the same production wire
// shape the other runtime tests use, so the real factory runs in the test.
func (v *idleLossVendor) authenticate(conn net.Conn) (key, sessionKey, iv []byte, err error) {
	key, fields, err := readLossCarrierText(conn)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read auth request: %w", err)
	}
	block, err := base64.StdEncoding.DecodeString(fields["k"])
	if err != nil {
		return nil, nil, nil, fmt.Errorf("decode k field: %w", err)
	}
	hash, err := fvfacade.HashByName(account.Linux423Binary.OAEPHash)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(block) <= len(fvfacade.OAEPBlockMagic) {
		return nil, nil, nil, fmt.Errorf("k field is not an %s block", fvfacade.OAEPBlockMagic)
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

// serveData answers one data channel. The body is the SOCKS5 CONNECT; it is
// read so the stream is clean and the sentinel target is checked. A kicked
// session is refused before any destination exchange, exactly the vendor's
// idle-loss shape.
func (v *idleLossVendor) serveData(conn net.Conn, sessionKey []byte) error {
	key, fields, err := readLossCarrierText(conn)
	if err != nil {
		return fmt.Errorf("read data request: %w", err)
	}
	if fields["s"] != v.sid {
		return fmt.Errorf("data request sid = %q, want %q", fields["s"], v.sid)
	}
	div, err := hex.DecodeString(fields["n"])
	if err != nil || len(div) != fvfacade.IVSize {
		return fmt.Errorf("data n = %q", fields["n"])
	}
	stream, err := fvfacade.NewCTR(sessionKey, div)
	if err != nil {
		return err
	}
	head := make([]byte, 5)
	if _, err := io.ReadFull(conn, head); err != nil {
		return fmt.Errorf("read socks5 head: %w", err)
	}
	stream.XORKeyStream(head, head)
	if head[0] != 0x05 || head[3] != 0x03 {
		return fmt.Errorf("socks5 head = %v, want 05 xx 00 03", head)
	}
	tail := make([]byte, int(head[4])+2)
	if _, err := io.ReadFull(conn, tail); err != nil {
		return fmt.Errorf("read socks5 tail: %w", err)
	}
	stream.XORKeyStream(tail, tail)
	host := string(tail[:head[4]])
	port := int(binary.BigEndian.Uint16(tail[head[4]:]))
	if host != "127.0.0.1" || port != 1 {
		return fmt.Errorf("probe target = %s:%d, want the 127.0.0.1:1 sentinel", host, port)
	}
	v.probes.Add(1)
	if v.kicked.Load() {
		return writeLossCarrierText(conn, key, "code=403\nfm="+idleLossMarker+"\n")
	}
	// Live session, unreachable sentinel destination: accept the data channel
	// and answer the SOCKS5 request with a flow-level refusal.
	if err := writeLossCarrierText(conn, key, "code=200\n"); err != nil {
		return err
	}
	down, err := fvfacade.NewCTR(sessionKey, div)
	if err != nil {
		return err
	}
	reply := make([]byte, 10)
	down.XORKeyStream(reply, []byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
	if _, err := conn.Write(reply); err != nil {
		return fmt.Errorf("write socks5 refusal: %w", err)
	}
	return nil
}

// TestIdleSessionLossIsContainedByActiveProbe is the reopen falsifier of
// residual 1: no client flow touches the gate, the vendor only flips its
// data-channel answer, and one liveness scan must contain exactly the target
// slot inside its 30 s window while the sibling keeps its gate, session and
// listener.
func TestIdleSessionLossIsContainedByActiveProbe(t *testing.T) {
	// The production scan runs every DefaultLivenessInterval; the requirement
	// is containment within 60 s, so one window must be shorter than that.
	if runtime.DefaultLivenessInterval <= 0 || runtime.DefaultLivenessInterval > time.Minute {
		t.Fatalf("DefaultLivenessInterval = %v, want a positive window at most 60s", runtime.DefaultLivenessInterval)
	}
	t.Setenv("AJS_ACCOUNT_SLOTS", "2")
	st := newNodeTestPoolStore(t)
	acct := newNodeTestAccount(t, st, "slot-idle-loss")
	grantSlots(t, st, acct, 2)
	vendor := startIdleLossVendor(t, idleLossSID)
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
		Store:            st,
		Logins:           logins,
		Sessions:         opener,
		Gates:            gates.build,
		MaxServers:       2,
		PortBase:         13660,
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
		t.Fatalf("slot 1 = %+v, want LIVE at generation %d", slotRow, slot1.Generation)
	}

	// A customer lease pinned to the slot that will lose its session.
	user, err := st.CreateUser(model.CreateUserInput{Name: "idle-loss-bot"})
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

	// Positive control: a live session whose probe destination is refused is
	// not a loss. The scan probes the production session once and keeps both
	// slots serving.
	if got := controller.CheckLiveness(); got != 0 {
		t.Fatalf("CheckLiveness on healthy sessions contained %d gates, want 0", got)
	}
	if got := vendor.probeCount(); got != 1 {
		t.Fatalf("active probes = %d, want 1", got)
	}
	if slotRow, ok := st.Slot(key1); !ok || slotRow.State != model.SlotLive {
		t.Fatalf("slot 1 after the healthy probe = %+v, want LIVE", slotRow)
	}
	if !lossDial(net.JoinHostPort("127.0.0.1", strconv.Itoa(slot1.Port))) {
		t.Fatalf("slot 1 listener %d stopped accepting after a healthy probe", slot1.Port)
	}

	// The vendor loses the session with no client flow. One scan must contain
	// exactly the target: the probe sees the session-loss refusal.
	vendor.kick()
	if got := controller.CheckLiveness(); got != 1 {
		t.Fatalf("CheckLiveness after the idle loss contained %d gates, want 1", got)
	}
	if got := vendor.probeCount(); got != 2 {
		t.Fatalf("active probes = %d, want 2", got)
	}

	// Target-only DOWN: the slot keeps its running generation.
	slotRow, ok = st.Slot(key1)
	if !ok || slotRow.State != model.SlotDown || slotRow.Health != model.SlotHealthVendorKick {
		t.Fatalf("slot 1 after the idle loss = %+v, want DOWN/vendor_kick", slotRow)
	}
	if slotRow.Generation != slot1.Generation {
		t.Fatalf("slot 1 generation after the loss = %d, want %d", slotRow.Generation, slot1.Generation)
	}
	wantRetry := clock.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339)
	if slotRow.RetryAfter != wantRetry {
		t.Fatalf("slot 1 retryAfter = %q, want %q", slotRow.RetryAfter, wantRetry)
	}
	node1 := nodeByServer(t, st, acct.ID, "s1")
	if node1.State != model.NodeDown || node1.GatePort != 0 || node1.EgressIP != "" {
		t.Fatalf("node s1 after the idle loss = %+v, want DOWN without port or egress", node1)
	}
	if _, ok := st.Lease(lease.ID); ok {
		t.Fatalf("lease %s survived the idle loss", lease.ID)
	}
	if lossDial(net.JoinHostPort("127.0.0.1", strconv.Itoa(slot1.Port))) {
		t.Fatalf("slot 1 listener %d still accepts after the idle loss", slot1.Port)
	}

	// The sibling is untouched: same gate, session, generation and listener.
	after := controller.Snapshot()
	if len(after) != 1 || after[0].Slot != 2 {
		t.Fatalf("snapshot after the idle loss = %+v, want only slot 2", after)
	}
	if after[0].Generation != slot2.Generation || after[0].Port != slot2.Port {
		t.Fatalf("sibling runtime = %+v, want generation %d port %d", after[0], slot2.Generation, slot2.Port)
	}
	slot2Row, ok := st.Slot(model.SlotKey{AccountID: acct.ID, Index: 2})
	if !ok || slot2Row.State != model.SlotLive || slot2Row.Generation != slot2.Generation {
		t.Fatalf("slot 2 after the sibling loss = %+v, want LIVE generation %d", slot2Row, slot2.Generation)
	}
	if node2 := nodeByServer(t, st, acct.ID, "s2"); node2.State != model.NodeLive || node2.GatePort != slot2.Port {
		t.Fatalf("node s2 after the sibling loss = %+v, want LIVE on port %d", node2, slot2.Port)
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
	vendor.requireNoErrors(t)
}

// TestSyncAccountJoinsAccountStopClaim is the reopen falsifier of residual 2 at
// controller scope: BeginAccountStop keeps the account-wide claim (and the
// fence) held across the caller's store mutation, so a login that begins after
// the fence and completes while the claim is held installs no gate, session or
// LIVE node. The sync waits for the claim and then refuses the paused account.
func TestSyncAccountJoinsAccountStopClaim(t *testing.T) {
	f := newSlotThreatFixture(t)
	f.sync(t)
	nodeID := f.key.NodeID("s1")
	if gate := f.gates.gate(nodeID); gate == nil || !gate.started.Load() {
		t.Fatalf("slot 1 gate was not started: %+v", gate)
	}

	release, err := f.controller.BeginAccountStop(context.Background(), f.acct.ID)
	if err != nil {
		t.Fatalf("BeginAccountStop: %v", err)
	}
	if release == nil {
		t.Fatal("BeginAccountStop returned a nil release")
	}
	// The claim closes the account's gates before the caller's store mutation.
	if running := f.controller.Snapshot(); len(running) != 0 {
		t.Fatalf("snapshot after the stop claim = %+v, want no gate", running)
	}
	if gate := f.gates.gate(nodeID); gate == nil || !gate.closed.Load() {
		t.Fatalf("gate after the stop claim = %+v, want it closed", gate)
	}

	// A login begins after the fence and completes while the claim is held.
	f.clock.Advance(30 * time.Second)
	f.logins.set(f.acct.ID, account.LoginResult{
		AuthResult:      "OK",
		UserName:        "vendor-user",
		ConnectPassword: account.Secret(testConnectPassword),
		Servers: map[string]apifacade.Server{
			"s1": testServer("s1"),
			"s2": testServer("s2"),
		},
		FetchedAt: f.clock.Now(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- f.controller.SyncAccount(ctx, f.acct.ID) }()
	select {
	case err := <-done:
		t.Fatalf("SyncAccount returned %v while the account stop claim was held", err)
	case <-time.After(250 * time.Millisecond):
	}
	if running := f.controller.Snapshot(); len(running) != 0 {
		t.Fatalf("snapshot while the claim is held = %+v, want no gate", running)
	}
	if got := f.gates.built(); got != 1 {
		t.Fatalf("gates built while the claim is held = %d, want only the closed one", got)
	}

	// The logout mutation commits, then the claim is released.
	if _, err := f.st.LogoutAccount(f.acct.ID); err != nil {
		t.Fatalf("LogoutAccount: %v", err)
	}
	release()
	requireSwitchReason(t, <-done, runtime.ReasonAccountState)

	// After logout returns: no gate, session, listener or LIVE node.
	if running := f.controller.Snapshot(); len(running) != 0 {
		t.Fatalf("snapshot after logout = %+v, want no gate", running)
	}
	if gate := f.gates.gate(nodeID); gate == nil || !gate.closed.Load() {
		t.Fatalf("gate after logout = %+v, want it closed", gate)
	}
	if session := f.opener.session("s1"); session == nil || !session.isClosed() {
		t.Fatalf("session after logout = %+v, want it closed", session)
	}
	for _, node := range f.st.Nodes() {
		if node.AccountID == f.acct.ID && node.State == model.NodeLive {
			t.Fatalf("node %s is LIVE after logout: %+v", node.ID, node)
		}
	}
	if acct, ok := f.st.Account(f.acct.ID); !ok || acct.State != model.AccountPaused {
		t.Fatalf("account after logout = %+v, want paused", acct)
	}

	// A later hook run cannot reopen the paused account.
	err = f.controller.SyncAccount(context.Background(), f.acct.ID)
	requireSwitchReason(t, err, runtime.ReasonAccountState)
	if running := f.controller.Snapshot(); len(running) != 0 {
		t.Fatalf("late sync left %d gates", len(running))
	}
}
