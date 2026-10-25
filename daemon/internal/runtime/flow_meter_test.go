package runtime_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"ajsaccountd/internal/account"
	"ajsaccountd/internal/api"
	"ajsaccountd/internal/gate"
	"ajsaccountd/internal/model"
	"ajsaccountd/internal/runtime"
	"ajsaccountd/internal/store"
	"ajsaccountd/internal/usage"
	"ajsproxy/apifacade"
)

// meterFlowSession is a fake vendor session that echoes, except for one host
// that fails before relay so the meter can prove it drops rejected flows.
type meterFlowSession struct {
	*fakeFlowSession
}

func (s *meterFlowSession) OpenFlow(ctx context.Context, host string, port int) (net.Conn, error) {
	if host == "blocked.example" {
		return nil, errors.New("meter test: open flow refused")
	}
	return s.fakeFlowSession.OpenFlow(ctx, host, port)
}

// meterSessionOpener hands every gate a fresh fake session.
type meterSessionOpener struct{}

func (meterSessionOpener) Open(context.Context, account.SessionRequest) (account.FlowSession, error) {
	return &meterFlowSession{fakeFlowSession: &fakeFlowSession{
		fields: map[string]string{"sip": "198.51.100.7"},
	}}, nil
}

type meterFixture struct {
	store      *store.Store
	account    model.Account
	controller *runtime.NodeController
}

// newMeterFixture builds an empty store, one account, a fake login and a node
// controller whose gates are metered by the runtime FlowMeter.
func newMeterFixture(t *testing.T, label string) *meterFixture {
	t.Helper()
	st := newNodeTestStore(t)
	acct := newNodeTestAccount(t, st, label)
	logins := newFakeLoginResults()
	logins.set(acct.ID, account.LoginResult{
		AuthResult:      "OK",
		UserName:        "vendor-user",
		ConnectPassword: account.Secret(testConnectPassword),
		Servers:         map[string]apifacade.Server{"s1": testServer("s1")},
	})
	meter := runtime.NewFlowMeter(st)
	controller, err := runtime.NewNodeController(runtime.NodeControllerConfig{
		Store:      st,
		Logins:     logins,
		Sessions:   meterSessionOpener{},
		Gates:      meter.GateFactory(),
		MaxServers: 1,
		PortBase:   13300,
		Logger:     discardLogger(),
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
	return &meterFixture{store: st, account: acct, controller: controller}
}

// gatePort returns the loopback port of the fixture's only node.
func (f *meterFixture) gatePort(t *testing.T) int {
	t.Helper()
	node, ok := f.store.Node(slot1NodeID(f.account.ID, "s1"))
	if !ok || node.GatePort == 0 {
		t.Fatalf("node %s:s1 has no gate port", f.account.ID)
	}
	return node.GatePort
}

// socksConnectCode performs a no-auth SOCKS5 CONNECT and returns the reply
// code, leaving the connection open on success.
func socksConnectCode(t *testing.T, addr, host string, port int) (byte, net.Conn) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial gate %s: %v", addr, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
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
	request = append(request, byte(port>>8), byte(port))
	if _, err := conn.Write(request); err != nil {
		t.Fatalf("write connect: %v", err)
	}
	var head [4]byte
	if _, err := io.ReadFull(conn, head[:]); err != nil {
		t.Fatalf("read connect reply head: %v", err)
	}
	var skip int
	switch head[3] {
	case 1:
		skip = 4 + 2
	case 4:
		skip = 16 + 2
	case 3:
		var length [1]byte
		if _, err := io.ReadFull(conn, length[:]); err != nil {
			t.Fatalf("read bound domain length: %v", err)
		}
		skip = int(length[0]) + 2
	default:
		t.Fatalf("connect reply atyp = %d", head[3])
	}
	if _, err := io.ReadFull(conn, make([]byte, skip)); err != nil {
		t.Fatalf("read connect reply tail: %v", err)
	}
	_ = conn.SetDeadline(time.Time{})
	return head[1], conn
}

// driveFlow opens one CONNECT through the gate and relays one payload.
func driveFlow(t *testing.T, port int, host string, payload []byte) {
	t.Helper()
	code, conn := socksConnectCode(t, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), host, 443)
	if code != 0 {
		t.Fatalf("CONNECT %s reply = %d, want success", host, code)
	}
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
	_ = conn.Close()
}

// waitGateRows waits until the store holds want metered rows.
func waitGateRows(t *testing.T, st *store.Store, want int) []model.RequestLog {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		rows := meteredRows(st.Requests())
		if len(rows) >= want {
			return rows
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d metered rows; have %d", want, len(rows))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func meteredRows(rows []model.RequestLog) []model.RequestLog {
	out := make([]model.RequestLog, 0, len(rows))
	for _, row := range rows {
		if row.Source == model.SourceGate {
			out = append(out, row)
		}
	}
	return out
}

// TestFlowMeterMetersAcceptedFlowsIntoUsage drives one real accepted flow and
// one rejected flow through a real gate, then asks GET /v1/usage for the
// account: only the accepted flow may count, with the documented row fields.
func TestFlowMeterMetersAcceptedFlowsIntoUsage(t *testing.T) {
	fixture := newMeterFixture(t, "meter-usage")
	port := fixture.gatePort(t)

	driveFlow(t, port, "example.com", []byte("ping"))

	// A rejected flow never reaches relay; the gate still records it in its
	// ring, but the meter must not create a usage row.
	if code, _ := socksConnectCode(t, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), "blocked.example", 443); code == 0 {
		t.Fatal("rejected flow answered success")
	}

	rows := waitGateRows(t, fixture.store, 1)
	if len(rows) != 1 {
		t.Fatalf("metered rows = %d, want 1 (rejected flow must be dropped): %+v", len(rows), rows)
	}
	row := rows[0]
	if row.Status != http.StatusOK || row.Method != "CONNECT" {
		t.Fatalf("row status/method = %d/%q, want 200/CONNECT", row.Status, row.Method)
	}
	if row.Target != "example.com:443" {
		t.Fatalf("target = %q, want host:port only", row.Target)
	}
	if row.Bytes != 8 || row.LatencyMs < 0 {
		t.Fatalf("row bytes/latency = %d/%d, want 8/>=0", row.Bytes, row.LatencyMs)
	}
	if row.AccountLabel != fixture.account.Label {
		t.Fatalf("accountLabel = %q, want %q", row.AccountLabel, fixture.account.Label)
	}
	node, ok := fixture.store.Node(slot1NodeID(fixture.account.ID, "s1"))
	if !ok || row.NodeLabel != node.Label || row.Region != node.Region {
		t.Fatalf("node attribution = %q/%q, want %q/%q", row.NodeLabel, row.Region, node.Label, node.Region)
	}
	encoded, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("marshal row: %v", err)
	}
	for _, leaked := range []string{"http://", "https://", "/", "@", "?", testConnectPassword} {
		if strings.Contains(string(encoded), leaked) {
			t.Fatalf("metered row leaked %q: %s", leaked, encoded)
		}
	}

	server := api.New(api.Config{
		AdminToken: "meter-test-token",
		Usage:      usage.New(usage.Config{}),
	}, fixture.store, discardLogger())
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)

	day := row.TS[:10]
	endpoint := ts.URL + "/v1/usage?account=" + url.QueryEscape(fixture.account.Label) +
		"&from=" + day + "&to=" + day
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		t.Fatalf("build usage request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer meter-test-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /v1/usage: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read usage body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/usage status = %d: %s", resp.StatusCode, body)
	}
	var summary usage.Summary
	if err := json.Unmarshal(body, &summary); err != nil {
		t.Fatalf("decode usage summary: %v (%s)", err, body)
	}
	if summary.Requests != 1 || summary.Bytes != 8 {
		t.Fatalf("usage = %d requests/%d bytes, want 1/8: %s", summary.Requests, summary.Bytes, body)
	}
	if len(summary.Accounts) != 1 || summary.Accounts[0].Account != fixture.account.Label {
		t.Fatalf("usage accounts = %+v, want only %q", summary.Accounts, fixture.account.Label)
	}
	if len(summary.TopTargets) != 1 || summary.TopTargets[0].Target != "example.com" {
		t.Fatalf("top targets = %+v, want host-only example.com", summary.TopTargets)
	}
	if summary.RowsRetained != usage.DefaultRetentionRows {
		t.Fatalf("rowsRetained = %d, want %d", summary.RowsRetained, usage.DefaultRetentionRows)
	}
	if summary.RetentionDays != 1 {
		t.Fatalf("retentionDays = %d, want 1 for a same-day row", summary.RetentionDays)
	}
}

// TestFlowMeterReconnectDoesNotDuplicate proves a stop/start cycle meters each
// accepted flow exactly once: the callback fires per finished flow, so a new
// gate cannot replay the previous gate's records.
func TestFlowMeterReconnectDoesNotDuplicate(t *testing.T) {
	fixture := newMeterFixture(t, "meter-reconnect")
	driveFlow(t, fixture.gatePort(t), "example.com", []byte("one"))
	waitGateRows(t, fixture.store, 1)

	if err := fixture.controller.StopAccount(context.Background(), fixture.account.ID); err != nil {
		t.Fatalf("StopAccount: %v", err)
	}
	if err := fixture.controller.SyncAccount(context.Background(), fixture.account.ID); err != nil {
		t.Fatalf("SyncAccount after reconnect: %v", err)
	}
	driveFlow(t, fixture.gatePort(t), "example.com", []byte("two"))
	rows := waitGateRows(t, fixture.store, 2)

	if len(rows) != 2 {
		t.Fatalf("metered rows after reconnect = %d, want 2 (no duplicates): %+v", len(rows), rows)
	}
	ids := map[string]struct{}{}
	for _, row := range rows {
		if _, dup := ids[row.ID]; dup {
			t.Fatalf("duplicate row id %q after reconnect: %+v", row.ID, rows)
		}
		ids[row.ID] = struct{}{}
		if row.Target != "example.com:443" || row.Status != http.StatusOK {
			t.Fatalf("reconnected row = %+v", row)
		}
	}
}

// TestFlowMeterGateFactoryKeepsFlowRing proves the metered factory returns a
// real gate with the observer attached and the Lab's GateFlows seam intact.
func TestFlowMeterGateFactoryKeepsFlowRing(t *testing.T) {
	st := newNodeTestStore(t)
	meter := runtime.NewFlowMeter(st)
	factory := meter.GateFactory()
	if factory == nil {
		t.Fatal("GateFactory returned nil")
	}
	srv, err := factory(gate.Config{
		NodeID:    "node-x",
		AccountID: "acct-x",
		Logger:    discardLogger(),
	}, &meterFlowSession{
		fakeFlowSession: &fakeFlowSession{fields: map[string]string{}},
	})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if _, ok := srv.(runtime.GateFlows); !ok {
		t.Fatalf("metered gate %T lost the GateFlows seam", srv)
	}
	_ = srv.Close()
}
