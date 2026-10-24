package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
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

// flowLab overrides only Flows; the embedded interface keeps the fake small
// while satisfying LabReader.
type flowLab struct {
	LabReader
	view     model.FlowsView
	reason   string
	gotLimit int
}

func (f *flowLab) Flows(limit int) (model.FlowsView, string) {
	f.gotLimit = limit
	return f.view, f.reason
}

func TestFlowRingAPIHandler(t *testing.T) {
	fake := &flowLab{view: model.FlowsView{
		Enabled: true,
		Dropped: 3,
		Flows: []model.FlowView{{
			ID:         "acc-01:s1#7",
			NodeID:     "acc-01:s1",
			AccountID:  "acc-01",
			SID:        "fake…(8)",
			StartedAt:  "2026-10-24T12:00:00Z",
			DurationMs: 12,
			Method:     "CONNECT",
			TargetHost: "example.com",
			TargetPort: 443,
			Status:     "ok",
			BytesIn:    10,
			BytesOut:   20,
		}},
	}}
	ts := newLabTestServer(t, fake, store.New())

	status, body := labGET(t, ts, "/v1/lab/flows", false)
	if status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated flows = %d, want 401: %s", status, body)
	}

	status, body = labGET(t, ts, "/v1/lab/flows", true)
	if status != http.StatusOK {
		t.Fatalf("flows = %d, want 200: %s", status, body)
	}
	if fake.gotLimit != labFlowDefaultLimit {
		t.Fatalf("default limit = %d, want %d", fake.gotLimit, labFlowDefaultLimit)
	}
	var view model.FlowsView
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatalf("decode flows: %v (%s)", err, body)
	}
	if !view.Enabled || view.Reason != "" || view.Dropped != 3 || len(view.Flows) != 1 {
		t.Fatalf("flows body = %+v", view)
	}
	flow := view.Flows[0]
	if flow.ID != "acc-01:s1#7" || flow.NodeID != "acc-01:s1" || flow.AccountID != "acc-01" ||
		flow.SID != "fake…(8)" || flow.StartedAt != "2026-10-24T12:00:00Z" ||
		flow.DurationMs != 12 || flow.Method != "CONNECT" ||
		flow.TargetHost != "example.com" || flow.TargetPort != 443 ||
		flow.Status != "ok" || flow.BytesIn != 10 || flow.BytesOut != 20 || flow.ErrorClass != "" {
		t.Fatalf("flow = %+v", flow)
	}
	raw := string(body)
	if strings.Contains(raw, "fake-sid") {
		t.Fatalf("flows leaked the full session id: %s", raw)
	}
	for _, forbidden := range []string{"http://", "https://", "password"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("flows leaked %q: %s", forbidden, raw)
		}
	}

	status, body = labGET(t, ts, "/v1/lab/flows?limit=7", true)
	if status != http.StatusOK || fake.gotLimit != 7 {
		t.Fatalf("limit 7 = %d limit=%d: %s", status, fake.gotLimit, body)
	}
	status, body = labGET(t, ts, "/v1/lab/flows?limit=500", true)
	if status != http.StatusOK || fake.gotLimit != labFlowMaxLimit {
		t.Fatalf("limit 500 = %d limit=%d: %s", status, fake.gotLimit, body)
	}
	status, body = labGET(t, ts, "/v1/lab/flows?limit=", true)
	if status != http.StatusOK || fake.gotLimit != labFlowDefaultLimit {
		t.Fatalf("empty limit = %d limit=%d: %s", status, fake.gotLimit, body)
	}

	for _, query := range []string{
		"/v1/lab/flows?limit=0",
		"/v1/lab/flows?limit=501",
		"/v1/lab/flows?limit=-1",
		"/v1/lab/flows?limit=abc",
		"/v1/lab/flows?limit=1.5",
	} {
		status, body := labGET(t, ts, query, true)
		if status != http.StatusBadRequest {
			t.Fatalf("%s = %d, want 400: %s", query, status, body)
		}
		requireErrorCode(t, body, CodeInvalidArgument)
	}

	fake.reason = "AJS_ACCOUNT_LOGIN is not 1"
	fake.view = model.FlowsView{}
	status, body = labGET(t, ts, "/v1/lab/flows", true)
	if status != http.StatusOK {
		t.Fatalf("disabled flows = %d: %s", status, body)
	}
	var disabled model.FlowsView
	if err := json.Unmarshal(body, &disabled); err != nil {
		t.Fatalf("decode disabled flows: %v (%s)", err, body)
	}
	if disabled.Enabled || disabled.Reason == "" {
		t.Fatalf("disabled flows = %+v, want enabled:false with a reason", disabled)
	}
	if disabled.Flows == nil || len(disabled.Flows) != 0 {
		t.Fatalf("disabled flows = %+v, want an empty array", disabled.Flows)
	}
}

// staticLoginResults serves one retained login result to the node controller.
type staticLoginResults struct {
	result account.LoginResult
}

func (s staticLoginResults) Result(string) (account.LoginResult, bool) {
	return s.result, true
}

// echoSessionOpener returns a session whose flows echo in memory.
type echoSessionOpener struct{}

func (echoSessionOpener) Open(context.Context, account.SessionRequest) (account.FlowSession, error) {
	return &echoFlowSession{}, nil
}

type echoFlowSession struct{}

func (*echoFlowSession) SID() string               { return "api-e2e-sid" }
func (*echoFlowSession) Fields() map[string]string { return nil }
func (*echoFlowSession) Healthy() bool             { return true }
func (*echoFlowSession) Close() error              { return nil }

func (*echoFlowSession) OpenFlow(context.Context, string, int) (net.Conn, error) {
	client, server := net.Pipe()
	go func() {
		defer server.Close()
		_, _ = io.Copy(server, server)
	}()
	return client, nil
}

// gateConnect performs a no-auth SOCKS5 CONNECT against a real gate.
func gateConnect(t *testing.T, addr, host string, port int) net.Conn {
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

// TestFlowRingHTTPServesRealGateRows is the end-to-end acceptance check: a
// real gate.Server records a relayed flow, runtime.LabView joins it, and the
// authenticated HTTP endpoint returns the row with a host-only target.
func TestFlowRingHTTPServesRealGateRows(t *testing.T) {
	st := store.New()
	acct, err := st.CreateAccount(model.CreateAccountInput{
		Label:       "flow-e2e",
		Username:    "flow-e2e-user",
		Password:    "flow-e2e-password-not-a-credential",
		Platform:    model.PlatformLinux,
		Concurrency: 1,
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	device, err := account.NewDeviceForProfile(account.Linux423Binary)
	if err != nil {
		t.Fatalf("NewDeviceForProfile: %v", err)
	}
	if _, err := st.EnsureAccountDevice(acct.ID, account.Identify(device)); err != nil {
		t.Fatalf("EnsureAccountDevice: %v", err)
	}
	logins := staticLoginResults{result: account.LoginResult{
		AuthResult:      "OK",
		UserName:        "vendor-user",
		ConnectPassword: account.Secret("e2e-connect-password"),
		Servers: map[string]apifacade.Server{"s1": {
			ID:   "s1",
			Svr:  "203.0.113.10",
			Tit:  "Shanghai 01",
			Ctry: "CN",
			Sts:  "ok",
			Ord:  "1",
			Par:  apifacade.ServerPar{FV: apifacade.ServerFV{P: apifacade.FlexInt(443)}},
			Loc:  []apifacade.ServerLoc{{C: "CN", N: "Shanghai", O: apifacade.FlexInt(1)}},
		}},
	}}

	var gateServer runtime.Gate
	controller, err := runtime.NewNodeController(runtime.NodeControllerConfig{
		Store:      st,
		Logins:     logins,
		Sessions:   echoSessionOpener{},
		MaxServers: 1,
		PortBase:   13300,
		Gates: func(cfg gate.Config, session gate.Session) (runtime.Gate, error) {
			srv, err := gate.NewServer(cfg, session)
			if err == nil {
				gateServer = srv
			}
			return srv, err
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
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

	lab := runtime.NewLabView(runtime.LabConfig{Store: st, Controller: controller, Logins: logins})
	ts := newLabTestServer(t, lab, st)

	// A probe is a dial-and-close with no SOCKS5 greeting. The gate must count
	// it as probes, not rejected, and GET /v1/lab/gates must carry the counter.
	probe, err := net.Dial("tcp", gateServer.Addr().String())
	if err != nil {
		t.Fatalf("dial probe: %v", err)
	}
	_ = probe.Close()
	probeDeadline := time.Now().Add(3 * time.Second)
	for {
		status, body := labGET(t, ts, "/v1/lab/gates", true)
		if status != http.StatusOK {
			t.Fatalf("gates = %d: %s", status, body)
		}
		var gates []model.GateView
		if err := json.Unmarshal(body, &gates); err != nil {
			t.Fatalf("decode gates: %v (%s)", err, body)
		}
		if len(gates) == 1 && gates[0].Probes == 1 {
			if gates[0].Rejected != 0 {
				t.Fatalf("probe counted as a rejection: %+v", gates[0])
			}
			if !strings.Contains(string(body), `"probes":1`) {
				t.Fatalf("gates body has no probes counter: %s", body)
			}
			break
		}
		if time.Now().After(probeDeadline) {
			t.Fatalf("timed out waiting for the probe counter; last = %s", body)
		}
		time.Sleep(5 * time.Millisecond)
	}

	conn := gateConnect(t, gateServer.Addr().String(), "example.com", 443)
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	echo := make([]byte, 4)
	if _, err := io.ReadFull(conn, echo); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	_ = conn.Close()

	deadline := time.Now().Add(3 * time.Second)
	var view model.FlowsView
	for {
		status, body := labGET(t, ts, "/v1/lab/flows?limit=10", true)
		if status != http.StatusOK {
			t.Fatalf("flows = %d: %s", status, body)
		}
		if err := json.Unmarshal(body, &view); err != nil {
			t.Fatalf("decode flows: %v (%s)", err, body)
		}
		if len(view.Flows) == 1 {
			if view.Flows[0].NodeID != acct.ID+":s1" || view.Flows[0].TargetHost != "example.com" ||
				view.Flows[0].TargetPort != 443 || view.Flows[0].Status != gate.FlowStatusOK ||
				view.Flows[0].BytesIn != 4 || view.Flows[0].BytesOut != 4 {
				t.Fatalf("flow = %+v", view.Flows[0])
			}
			raw := string(body)
			if !strings.Contains(raw, "api-…(11)") {
				t.Fatalf("flow does not carry the redacted sid: %s", raw)
			}
			for _, forbidden := range []string{"api-e2e-sid", "http://", "https://", "e2e-connect-password"} {
				if strings.Contains(raw, forbidden) {
					t.Fatalf("flows leaked %q: %s", forbidden, raw)
				}
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for the HTTP flow row; last = %s", body)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
