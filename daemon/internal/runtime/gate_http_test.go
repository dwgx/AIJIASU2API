package runtime_test

import (
	"bufio"
	"context"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"ajsaccountd/internal/account"
	"ajsaccountd/internal/gate"
	"ajsaccountd/internal/model"
	"ajsaccountd/internal/runtime"
	"ajsaccountd/internal/store"
	"ajsproxy/apifacade"
)

// newHTTPGateFixture builds a store, one account and a node controller whose
// real gates are metered. httpOffset selects the HTTP CONNECT port distance.
func newHTTPGateFixture(t *testing.T, label string, servers map[string]apifacade.Server, maxServers, portBase, httpOffset int) (*storeFixture, *runtime.NodeController) {
	t.Helper()
	st := newNodeTestStore(t)
	acct := newNodeTestAccount(t, st, label)
	logins := newFakeLoginResults()
	logins.set(acct.ID, account.LoginResult{
		AuthResult:      "OK",
		UserName:        "vendor-user",
		ConnectPassword: account.Secret(testConnectPassword),
		Servers:         servers,
	})
	meter := runtime.NewFlowMeter(st)
	controller, err := runtime.NewNodeController(runtime.NodeControllerConfig{
		Store:          st,
		Logins:         logins,
		Sessions:       meterSessionOpener{},
		Gates:          meter.GateFactory(),
		MaxServers:     maxServers,
		PortBase:       portBase,
		GateHTTP:       true,
		GateHTTPOffset: httpOffset,
		Logger:         discardLogger(),
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
	return &storeFixture{store: st, account: acct}, controller
}

// storeFixture is the minimal store/account pair the HTTP gate tests need.
type storeFixture struct {
	store   *store.Store
	account model.Account
}

// node returns one running node row.
func (f *storeFixture) node(t *testing.T, serverID string) model.ProxyNode {
	t.Helper()
	node, ok := f.store.Node(f.account.ID + ":" + serverID)
	if !ok {
		t.Fatalf("node %s:%s is missing", f.account.ID, serverID)
	}
	return node
}

// httpProxyConnect sends one CONNECT request and returns the status line plus
// the reader that owns the tunnel.
func httpProxyConnect(t *testing.T, addr, authority string) (int, net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial HTTP gate %s: %v", addr, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	request := "CONNECT " + authority + " HTTP/1.1\r\nHost: " + authority + "\r\n\r\n"
	if _, err := io.WriteString(conn, request); err != nil {
		t.Fatalf("write CONNECT: %v", err)
	}
	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
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
	for {
		header, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read header: %v", err)
		}
		if strings.TrimRight(header, "\r\n") == "" {
			return status, conn, reader
		}
	}
}

// freePortPair finds a loopback base whose HTTP offset port is also free.
func freePortPair(t *testing.T, offset int) int {
	t.Helper()
	for base := 20000; base < 30000; base++ {
		first, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(base)))
		if err != nil {
			continue
		}
		_ = first.Close()
		second, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(base+offset)))
		if err != nil {
			continue
		}
		_ = second.Close()
		return base
	}
	t.Fatal("no free loopback port pair")
	return 0
}

// TestNodeControllerHTTPConnectEndToEnd is the runtime acceptance row: a real
// node gate exposes the HTTP CONNECT listener at gatePort+offset, relays bytes
// through the session, and meters the flow with protocol "http".
func TestNodeControllerHTTPConnectEndToEnd(t *testing.T) {
	fixture, _ := newHTTPGateFixture(t, "http-gate",
		map[string]apifacade.Server{"s1": testServer("s1")}, 1, 13400,
		runtime.DefaultGateHTTPOffset)
	node := fixture.node(t, "s1")
	httpPort := node.GatePort + runtime.DefaultGateHTTPOffset
	httpAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(httpPort))

	status, conn, reader := httpProxyConnect(t, httpAddr, "example.com:443")
	if status != 200 {
		t.Fatalf("CONNECT status = %d, want 200", status)
	}
	payload := []byte("http-connect-payload")
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	echo := make([]byte, len(payload))
	if _, err := io.ReadFull(reader, echo); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(echo) != string(payload) {
		t.Fatalf("echo = %q, want %q", echo, payload)
	}
	_ = conn.Close()

	rows := waitGateRows(t, fixture.store, 1)
	if rows[0].Protocol != string(gate.FlowProtocolHTTP) {
		t.Fatalf("metered protocol = %q, want %q", rows[0].Protocol, gate.FlowProtocolHTTP)
	}
	if rows[0].Method != "CONNECT" || rows[0].Target != "example.com:443" {
		t.Fatalf("metered row = %+v, want CONNECT example.com:443", rows[0])
	}
	if rows[0].Bytes != int64(2*len(payload)) {
		t.Fatalf("metered bytes = %d, want %d", rows[0].Bytes, 2*len(payload))
	}

	// The SOCKS5 listener on the same node still works and is metered as
	// socks5, so both front ends feed one usage pipeline.
	driveFlow(t, node.GatePort, "example.com", []byte("socks"))
	rows = waitGateRows(t, fixture.store, 2)
	byProtocol := map[string]model.RequestLog{}
	for _, row := range rows {
		byProtocol[row.Protocol] = row
	}
	if _, ok := byProtocol[string(gate.FlowProtocolHTTP)]; !ok {
		t.Fatalf("no http row in %+v", rows)
	}
	socks, ok := byProtocol[string(gate.FlowProtocolSOCKS5)]
	if !ok || socks.Bytes != 10 {
		t.Fatalf("socks5 row = %+v, want 10 bytes", socks)
	}
}

// TestNodeControllerReservesHTTPPorts proves two nodes never share a port: the
// HTTP port of the first node is reserved before the second allocates.
func TestNodeControllerReservesHTTPPorts(t *testing.T) {
	const offset = 1
	fixture, _ := newHTTPGateFixture(t, "http-ports", map[string]apifacade.Server{
		"s1": testServer("s1"),
		"s2": testServer("s2"),
	}, 2, 21000, offset)

	first := fixture.node(t, "s1")
	second := fixture.node(t, "s2")
	ports := map[int]string{
		first.GatePort:           "s1 socks",
		first.GatePort + offset:  "s1 http",
		second.GatePort:          "s2 socks",
		second.GatePort + offset: "s2 http",
	}
	if len(ports) != 4 {
		t.Fatalf("ports collided: s1=%d s2=%d offset=%d", first.GatePort, second.GatePort, offset)
	}
	for port, label := range ports {
		conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), time.Second)
		if err != nil {
			t.Fatalf("%s port %d is not listening: %v", label, port, err)
		}
		_ = conn.Close()
	}
}

// TestNodeControllerRetriesPortPairOnHTTPBindFailure proves a gate whose HTTP
// port is taken moves to the next base port instead of coming up half-bound.
func TestNodeControllerRetriesPortPairOnHTTPBindFailure(t *testing.T) {
	const offset = 500
	base := freePortPair(t, offset)
	blocker, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(base+offset)))
	if err != nil {
		t.Skipf("cannot occupy the first HTTP port: %v", err)
	}
	defer blocker.Close()

	fixture, _ := newHTTPGateFixture(t, "http-retry",
		map[string]apifacade.Server{"s1": testServer("s1")}, 1, base, offset)
	node := fixture.node(t, "s1")
	if node.GatePort == base {
		t.Fatalf("gate kept port %d although its HTTP port was occupied", base)
	}
	httpAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(node.GatePort+offset))
	conn, err := net.DialTimeout("tcp", httpAddr, time.Second)
	if err != nil {
		t.Fatalf("retried node's HTTP port %s is not listening: %v", httpAddr, err)
	}
	_ = conn.Close()
}

// TestNodeControllerHTTPOffsetValidation pins the construction guard.
func TestNodeControllerHTTPOffsetValidation(t *testing.T) {
	st := newNodeTestStore(t)
	base := runtime.NodeControllerConfig{
		Store:    st,
		Logins:   newFakeLoginResults(),
		Sessions: newFakeSessionOpener(),
		Logger:   discardLogger(),
	}
	for _, offset := range []int{-1, 65535, 70000} {
		cfg := base
		cfg.GateHTTPOffset = offset
		if _, err := runtime.NewNodeController(cfg); err == nil {
			t.Fatalf("NewNodeController accepted GateHTTPOffset %d", offset)
		}
	}
}
