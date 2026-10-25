package runtime_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"ajsaccountd/internal/account"
	"ajsaccountd/internal/gate"
	"ajsaccountd/internal/model"
	"ajsaccountd/internal/runtime"
	"ajsproxy/apifacade"
)

// flowGate exposes a fixed flow window through the optional GateFlows seam.
// It returns records newest first, as the gate contract requires, so the test
// proves the runtime merges and orders the per-node windows itself.
type flowGate struct {
	*fakeGate
	records []gate.FlowRecord
	dropped uint64
}

func (g *flowGate) Flows(limit int) []gate.FlowRecord {
	if limit <= 0 || limit > len(g.records) {
		limit = len(g.records)
	}
	out := make([]gate.FlowRecord, limit)
	copy(out, g.records[:limit])
	return out
}

func (g *flowGate) Dropped() uint64 { return g.dropped }

func TestLabViewFlowsJoinOrderRedactionAndAttribution(t *testing.T) {
	st := newNodeTestStore(t)
	acct := newNodeTestAccount(t, st, "lab-flows")
	logins := newFakeLoginResults()
	logins.set(acct.ID, account.LoginResult{
		AuthResult:      "OK",
		UserName:        "vendor-user",
		ConnectPassword: account.Secret(testConnectPassword),
		Servers: map[string]apifacade.Server{
			"s1": testServer("s1"),
			"s2": testServer("s2"),
		},
	})

	started := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	gates := map[string]*flowGate{
		"s1": {
			fakeGate: &fakeGate{},
			dropped:  7,
			records: []gate.FlowRecord{
				{
					ID: "stale#1", NodeID: "stale-node", AccountID: "stale-account",
					StartedAt: started.Add(3 * time.Second), DurationMs: 5, Method: "CONNECT",
					Protocol:   gate.FlowProtocolSOCKS5,
					UserID:     "user-7",
					UserName:   "ada.ops",
					TargetHost: "example.com", TargetPort: 443, Status: gate.FlowStatusOK,
					BytesIn: 10, BytesOut: 20,
				},
				{
					ID: "stale#2", StartedAt: started, DurationMs: 1,
					Method: "CONNECT", TargetHost: "203.0.113.9", TargetPort: 80,
					Status: gate.FlowStatusRejected, ErrorClass: gate.FlowErrorDraining,
				},
			},
		},
		"s2": {
			fakeGate: &fakeGate{},
			dropped:  4,
			records: []gate.FlowRecord{
				{
					ID: "stale#3", StartedAt: started.Add(2 * time.Second), DurationMs: 2,
					Method: "CONNECT", Protocol: gate.FlowProtocolHTTP,
					UserID:     "user-9",
					TargetHost: "198.51.100.4", TargetPort: 8080,
					Status: gate.FlowStatusOK, BytesIn: 1, BytesOut: 2,
				},
				{
					ID: "stale#4", StartedAt: started.Add(time.Second), DurationMs: 3,
					Method: "CONNECT", TargetHost: "example.net", TargetPort: 53,
					Status: gate.FlowStatusRejected, ErrorClass: gate.FlowErrorOpenFlow,
				},
			},
		},
	}
	controller, err := runtime.NewNodeController(runtime.NodeControllerConfig{
		Store:      st,
		Logins:     logins,
		Sessions:   newFakeSessionOpener(),
		MaxServers: 2,
		PortBase:   13200,
		Gates: func(cfg gate.Config, _ gate.Session) (runtime.Gate, error) {
			if cfg.NodeID == acct.ID+":s2" {
				return gates["s2"], nil
			}
			return gates["s1"], nil
		},
		Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("NewNodeController: %v", err)
	}
	t.Cleanup(func() { _ = controller.Stop(context.Background()) })
	if err := controller.SyncAccount(context.Background(), acct.ID); err != nil {
		t.Fatalf("SyncAccount: %v", err)
	}

	view := runtime.NewLabView(runtime.LabConfig{Store: st, Controller: controller, Logins: logins})
	flows, reason := view.Flows(200)
	if reason != "" {
		t.Fatalf("Flows disabled: %q", reason)
	}
	if !flows.Enabled || flows.Dropped != 11 {
		t.Fatalf("flows header = %+v, want dropped 11", flows)
	}
	if len(flows.Flows) != 4 {
		t.Fatalf("flows = %+v, want 4", flows.Flows)
	}
	wantOrder := []string{"stale#1", "stale#3", "stale#4", "stale#2"}
	for i, want := range wantOrder {
		if flows.Flows[i].ID != want {
			t.Fatalf("flow[%d] = %q, want %q (newest first across nodes)", i, flows.Flows[i].ID, want)
		}
	}
	if flows.Flows[0].Status != gate.FlowStatusOK || flows.Flows[0].BytesIn != 10 || flows.Flows[0].BytesOut != 20 {
		t.Fatalf("flow[0] = %+v", flows.Flows[0])
	}
	// The gate's protocol and authenticated user pass through unchanged.
	if got := flows.Flows[0]; got.Protocol != gate.FlowProtocolSOCKS5 ||
		got.UserID != "user-7" || got.UserName != "ada.ops" {
		t.Fatalf("flow[0] attribution = %+v, want socks5/user-7/ada.ops", got)
	}
	// A record that carries an id but no name keeps the id and leaves the name
	// empty; omitempty drops the empty key instead of inventing one.
	if got := flows.Flows[1]; got.Protocol != gate.FlowProtocolHTTP ||
		got.UserID != "user-9" || got.UserName != "" {
		t.Fatalf("flow[1] attribution = %+v, want http/user-9 with no name", got)
	}
	// stale#2 is an anonymous flow: no protocol, no user.
	if got := flows.Flows[3]; got.Protocol != "" || got.UserID != "" || got.UserName != "" {
		t.Fatalf("anonymous flow gained attribution: %+v", got)
	}
	if flows.Flows[2].ErrorClass != gate.FlowErrorOpenFlow {
		t.Fatalf("flow[2] = %+v, want the open_flow reject", flows.Flows[2])
	}
	for _, flow := range flows.Flows {
		wantNode := acct.ID + ":s1"
		if flow.ID == "stale#3" || flow.ID == "stale#4" {
			wantNode = acct.ID + ":s2"
		}
		if flow.NodeID != wantNode || flow.AccountID != acct.ID {
			t.Fatalf("flow was not joined to its node/account: %+v", flow)
		}
		if flow.SID != "fake…(8)" {
			t.Fatalf("sid = %q, want the redacted value", flow.SID)
		}
		if strings.Contains(flow.TargetHost, ":") || strings.Contains(flow.TargetHost, "/") {
			t.Fatalf("target is not host-only: %q", flow.TargetHost)
		}
	}

	encoded, err := json.Marshal(flows)
	if err != nil {
		t.Fatalf("marshal flows: %v", err)
	}
	body := string(encoded)
	if strings.Contains(body, "fake-sid") {
		t.Fatalf("flows leaked the full session id: %s", body)
	}
	if strings.Contains(body, "stale-node") || strings.Contains(body, "stale-account") {
		t.Fatalf("flows kept the gate's unjoined identity: %s", body)
	}
	for _, forbidden := range []string{"http://", "https://", "password", testConnectPassword} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("flows leaked %q: %s", forbidden, body)
		}
	}
	// Attribution reaches the wire, and an empty attribution stays off it.
	for _, want := range []string{
		`"protocol":"socks5"`, `"userId":"user-7"`, `"userName":"ada.ops"`, `"userId":"user-9"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("flows body is missing %s: %s", want, body)
		}
	}
	anonymous, err := json.Marshal(flows.Flows[3])
	if err != nil {
		t.Fatalf("marshal anonymous flow: %v", err)
	}
	for _, absent := range []string{"protocol", "userId", "userName"} {
		if strings.Contains(string(anonymous), `"`+absent+`"`) {
			t.Fatalf("anonymous flow serialized %s: %s", absent, anonymous)
		}
	}
	idOnly, err := json.Marshal(flows.Flows[1])
	if err != nil {
		t.Fatalf("marshal id-only flow: %v", err)
	}
	if strings.Contains(string(idOnly), `"userName"`) {
		t.Fatalf("id-only flow serialized an empty userName: %s", idOnly)
	}

	limited, reason := view.Flows(2)
	if reason != "" || len(limited.Flows) != 2 ||
		limited.Flows[0].ID != "stale#1" || limited.Flows[1].ID != "stale#3" {
		t.Fatalf("limit 2 = %+v reason=%q, want the two newest flows", limited, reason)
	}
}

func TestLabViewFlowsDisabledWithoutController(t *testing.T) {
	view := runtime.NewLabView(runtime.LabConfig{})
	flows, reason := view.Flows(10)
	if reason == "" || flows.Enabled {
		t.Fatalf("flows without a controller = %+v reason=%q, want disabled", flows, reason)
	}
	if flows.Flows != nil {
		t.Fatalf("disabled flows carry records: %+v", flows.Flows)
	}
}

// TestLabViewFlowsWithoutRingSkipsGates keeps the view honest for a gate that
// only implements the listener contract.
func TestLabViewFlowsWithoutRingSkipsGates(t *testing.T) {
	st := newNodeTestStore(t)
	acct := newNodeTestAccount(t, st, "lab-no-ring")
	logins := newFakeLoginResults()
	logins.set(acct.ID, account.LoginResult{
		AuthResult:      "OK",
		UserName:        "vendor-user",
		ConnectPassword: account.Secret(testConnectPassword),
		Servers:         map[string]apifacade.Server{"s1": testServer("s1")},
	})
	controller, err := runtime.NewNodeController(runtime.NodeControllerConfig{
		Store:      st,
		Logins:     logins,
		Sessions:   newFakeSessionOpener(),
		MaxServers: 1,
		PortBase:   13210,
		Gates: func(_ gate.Config, _ gate.Session) (runtime.Gate, error) {
			return &fakeGate{}, nil
		},
		Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("NewNodeController: %v", err)
	}
	t.Cleanup(func() { _ = controller.Stop(context.Background()) })
	if err := controller.SyncAccount(context.Background(), acct.ID); err != nil {
		t.Fatalf("SyncAccount: %v", err)
	}

	view := runtime.NewLabView(runtime.LabConfig{Store: st, Controller: controller, Logins: logins})
	flows, reason := view.Flows(10)
	if reason != "" || !flows.Enabled || len(flows.Flows) != 0 || flows.Dropped != 0 {
		t.Fatalf("flows without a ring = %+v reason=%q, want an empty enabled view", flows, reason)
	}
	if flows.Flows == nil {
		t.Fatal("flows is nil, want []")
	}
}

// socksConnect performs a no-auth SOCKS5 CONNECT and returns the relayed
// connection.
func socksConnect(t *testing.T, addr, host string, port int) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial gate %s: %v", addr, err)
	}
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write([]byte{5, 1, 0}); err != nil {
		t.Fatalf("write greeting: %v", err)
	}
	var method [2]byte
	if _, err := io.ReadFull(conn, method[:]); err != nil {
		t.Fatalf("read method reply: %v", err)
	}
	if method != [2]byte{5, 0} {
		t.Fatalf("method reply = %v, want [5 0]", method)
	}
	request := []byte{5, 1, 0, 3, byte(len(host))}
	request = append(request, host...)
	request = append(request, byte(port>>8), byte(port))
	if _, err := conn.Write(request); err != nil {
		t.Fatalf("write connect: %v", err)
	}
	var reply [10]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		t.Fatalf("read connect reply: %v", err)
	}
	if reply[1] != 0 {
		t.Fatalf("connect reply = %v, want success", reply)
	}
	_ = conn.SetDeadline(time.Time{})
	return conn
}

// TestLabViewFlowsFromRealGate runs one real gate.Server end to end: a SOCKS5
// client relays through the fake vendor session, the gate records the flow, and
// LabView.Flows joins it to the node and redacts the session id.
func TestLabViewFlowsFromRealGate(t *testing.T) {
	st := newNodeTestStore(t)
	acct := newNodeTestAccount(t, st, "lab-real-flow")
	logins := newFakeLoginResults()
	logins.set(acct.ID, account.LoginResult{
		AuthResult:      "OK",
		UserName:        "vendor-user",
		ConnectPassword: account.Secret(testConnectPassword),
		Servers:         map[string]apifacade.Server{"s1": testServer("s1")},
	})

	var gateServer runtime.Gate
	controller, err := runtime.NewNodeController(runtime.NodeControllerConfig{
		Store:      st,
		Logins:     logins,
		Sessions:   newFakeSessionOpener(),
		MaxServers: 1,
		PortBase:   13220,
		Gates: func(cfg gate.Config, session gate.Session) (runtime.Gate, error) {
			srv, err := gate.NewServer(cfg, session)
			if err != nil {
				return nil, err
			}
			gateServer = srv
			return srv, nil
		},
		Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("NewNodeController: %v", err)
	}
	t.Cleanup(func() { _ = controller.Stop(context.Background()) })
	if err := controller.SyncAccount(context.Background(), acct.ID); err != nil {
		t.Fatalf("SyncAccount: %v", err)
	}
	if gateServer == nil || gateServer.Addr() == nil {
		t.Fatal("controller did not start the real gate")
	}

	view := runtime.NewLabView(runtime.LabConfig{Store: st, Controller: controller, Logins: logins})
	conn := socksConnect(t, gateServer.Addr().String(), "example.com", 443)
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	echo := make([]byte, 4)
	if _, err := io.ReadFull(conn, echo); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(echo) != "ping" {
		t.Fatalf("echo = %q, want ping", echo)
	}
	_ = conn.Close()

	deadline := time.Now().Add(3 * time.Second)
	var flows model.FlowsView
	for {
		flows, _ = view.Flows(10)
		if len(flows.Flows) == 1 && flows.Flows[0].Status == gate.FlowStatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for the real gate flow; last = %+v", flows)
		}
		time.Sleep(5 * time.Millisecond)
	}
	flow := flows.Flows[0]
	if flow.NodeID != acct.ID+":s1" || flow.AccountID != acct.ID {
		t.Fatalf("flow was not joined to its node/account: %+v", flow)
	}
	if flow.TargetHost != "example.com" || flow.TargetPort != 443 || flow.Method != "CONNECT" {
		t.Fatalf("target = %s:%d method=%q, want CONNECT example.com:443", flow.TargetHost, flow.TargetPort, flow.Method)
	}
	// The real SOCKS5 listener names its protocol; the no-auth client is not
	// attributed to a panel user.
	if flow.Protocol != gate.FlowProtocolSOCKS5 {
		t.Fatalf("protocol = %q, want %q", flow.Protocol, gate.FlowProtocolSOCKS5)
	}
	if flow.UserID != "" || flow.UserName != "" {
		t.Fatalf("unauthenticated gate flow gained a user: %+v", flow)
	}
	if flow.BytesIn != 4 || flow.BytesOut != 4 || flow.DurationMs < 0 || flow.StartedAt == "" {
		t.Fatalf("flow counters = %+v", flow)
	}
	if flow.SID != "fake…(8)" {
		t.Fatalf("sid = %q, want the redacted value", flow.SID)
	}
	encoded, err := json.Marshal(flows)
	if err != nil {
		t.Fatalf("marshal flows: %v", err)
	}
	body := string(encoded)
	for _, forbidden := range []string{"fake-sid", "http://", "https://", testConnectPassword} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("real gate flows leaked %q: %s", forbidden, body)
		}
	}
}
