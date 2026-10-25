package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"testing"
	"time"

	"ajsaccountd/internal/health"
	"ajsaccountd/internal/model"
)

// egressHealthMonitor is a loopback sampler plus the optional egress seam. It
// keeps the API test focused on the wire shape: the sampler itself is covered
// in internal/health.
type egressHealthMonitor struct {
	*health.Sampler
	enabled bool
	stats   health.EgressStats
	has     bool
}

func (m *egressHealthMonitor) EgressStats(string) (health.EgressStats, bool) {
	return m.stats, m.has
}

func (m *egressHealthMonitor) EgressEnabled() bool { return m.enabled }

// startEgressMonitor starts a loopback sampler whose dial succeeds without a
// listener, so the node is watched and the API renders a row.
func startEgressMonitor(t *testing.T, enabled bool) *egressHealthMonitor {
	t.Helper()
	sampler := health.New(health.Config{
		Interval: time.Hour,
		Dial: func(context.Context, string, string) (net.Conn, error) {
			client, server := net.Pipe()
			_ = server.Close()
			return client, nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sampler.Start(ctx)
	t.Cleanup(sampler.Stop)
	return &egressHealthMonitor{Sampler: sampler, enabled: enabled}
}

func putHealthNode(t *testing.T, monitor *egressHealthMonitor, id string) {
	t.Helper()
	if err := monitor.Watch(id, 20001); err != nil {
		t.Fatalf("watch %s: %v", id, err)
	}
	waitForSample(t, monitor.Sampler, id)
}

func TestHealthNodesEgressAggregateShape(t *testing.T) {
	monitor := startEgressMonitor(t, true)
	monitor.has = true
	at := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	monitor.stats = health.EgressStats{
		Key:                 "node-1",
		Count:               60,
		OK:                  57,
		SuccessRate:         0.95,
		P50Ms:               320,
		P95Ms:               1450,
		ConsecutiveFailures: 1,
		State:               health.StateDegraded,
		Last: health.EgressSample{
			At:         at,
			LatencyMs:  512.5,
			OK:         false,
			Status:     0,
			ErrorClass: health.ErrorTimeout,
		},
	}

	ts, st := newPlatformServer(t, Config{Health: monitor})
	if _, err := st.PutNode(model.ProxyNode{
		ID: "node-1", AccountID: "acc-01", Label: "test-node-one", Region: "test-region",
		State: model.NodeLive, GatePort: 20001, Capacity: 10, Protocol: model.ProtocolSOCKS5,
	}); err != nil {
		t.Fatalf("put node: %v", err)
	}
	putHealthNode(t, monitor, "node-1")

	resp, body := apiCall(t, ts, http.MethodGet, "/v1/health/nodes", nil)
	requireStatus(t, resp, http.StatusOK)
	payload := decodeBody[struct {
		Nodes []struct {
			Egress *struct {
				Enabled             bool    `json:"enabled"`
				Reason              string  `json:"reason"`
				State               string  `json:"state"`
				Count               int     `json:"count"`
				OK                  int     `json:"ok"`
				SuccessRate         float64 `json:"successRate"`
				P50Ms               float64 `json:"p50Ms"`
				P95Ms               float64 `json:"p95Ms"`
				ConsecutiveFailures int     `json:"consecutiveFailures"`
				Last                *struct {
					At         string  `json:"at"`
					LatencyMs  float64 `json:"latencyMs"`
					OK         bool    `json:"ok"`
					Status     int     `json:"status"`
					ErrorClass string  `json:"errorClass"`
				} `json:"last"`
			} `json:"egress"`
		} `json:"nodes"`
	}](t, body)
	if len(payload.Nodes) != 1 || payload.Nodes[0].Egress == nil {
		t.Fatalf("health payload has no egress block: %s", body)
	}
	egress := payload.Nodes[0].Egress
	if !egress.Enabled || egress.Reason != "" {
		t.Fatalf("egress enabled/reason = %v/%q", egress.Enabled, egress.Reason)
	}
	if egress.State != health.StateDegraded || egress.Count != 60 || egress.OK != 57 {
		t.Fatalf("egress aggregate = %+v", egress)
	}
	if egress.SuccessRate != 0.95 || egress.P50Ms != 320 || egress.P95Ms != 1450 {
		t.Fatalf("egress rates = %+v", egress)
	}
	if egress.ConsecutiveFailures != 1 {
		t.Fatalf("consecutive failures = %d, want 1", egress.ConsecutiveFailures)
	}
	if egress.Last == nil || egress.Last.OK || egress.Last.ErrorClass != health.ErrorTimeout {
		t.Fatalf("egress last = %+v, want a timeout", egress.Last)
	}
	if egress.Last.At != at.Format(time.RFC3339Nano) || egress.Last.LatencyMs != 512.5 {
		t.Fatalf("egress last sample = %+v", egress.Last)
	}
	// A response-less failure must not invent HTTP 0 on the wire.
	var raw struct {
		Nodes []struct {
			Egress struct {
				Last map[string]json.RawMessage `json:"last"`
			} `json:"egress"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("decode raw body: %v", err)
	}
	if _, ok := raw.Nodes[0].Egress.Last["status"]; ok {
		t.Fatalf("egress last carries status 0: %s", body)
	}
}

func TestHealthNodesEgressDisabledNamesTheSwitch(t *testing.T) {
	monitor := startEgressMonitor(t, false)
	ts, st := newPlatformServer(t, Config{Health: monitor})
	if _, err := st.PutNode(model.ProxyNode{
		ID: "node-1", AccountID: "acc-01", Label: "test-node-one", Region: "test-region",
		State: model.NodeLive, GatePort: 20001, Capacity: 10, Protocol: model.ProtocolSOCKS5,
	}); err != nil {
		t.Fatalf("put node: %v", err)
	}
	putHealthNode(t, monitor, "node-1")

	resp, body := apiCall(t, ts, http.MethodGet, "/v1/health/nodes", nil)
	requireStatus(t, resp, http.StatusOK)
	payload := decodeBody[struct {
		Nodes []struct {
			Egress *struct {
				Enabled bool   `json:"enabled"`
				Reason  string `json:"reason"`
				State   string `json:"state"`
			} `json:"egress"`
		} `json:"nodes"`
	}](t, body)
	if len(payload.Nodes) != 1 || payload.Nodes[0].Egress == nil {
		t.Fatalf("health payload has no egress block: %s", body)
	}
	egress := payload.Nodes[0].Egress
	if egress.Enabled {
		t.Fatalf("egress reported enabled without a sampler: %s", body)
	}
	if egress.Reason != reasonEgressDisabled {
		t.Fatalf("egress reason = %q, want %q", egress.Reason, reasonEgressDisabled)
	}
	if egress.State != "" {
		t.Fatalf("disabled egress carries state %q", egress.State)
	}
}

func TestHealthNodesEgressPendingWindow(t *testing.T) {
	monitor := startEgressMonitor(t, true)
	ts, st := newPlatformServer(t, Config{Health: monitor})
	if _, err := st.PutNode(model.ProxyNode{
		ID: "node-1", AccountID: "acc-01", Label: "test-node-one", Region: "test-region",
		State: model.NodeLive, GatePort: 20001, Capacity: 10, Protocol: model.ProtocolSOCKS5,
	}); err != nil {
		t.Fatalf("put node: %v", err)
	}
	putHealthNode(t, monitor, "node-1")

	resp, body := apiCall(t, ts, http.MethodGet, "/v1/health/nodes", nil)
	requireStatus(t, resp, http.StatusOK)
	payload := decodeBody[struct {
		Nodes []struct {
			Egress *struct {
				Enabled bool           `json:"enabled"`
				State   string         `json:"state"`
				Count   int            `json:"count"`
				Last    map[string]any `json:"last"`
			} `json:"egress"`
		} `json:"nodes"`
	}](t, body)
	if len(payload.Nodes) != 1 || payload.Nodes[0].Egress == nil {
		t.Fatalf("health payload has no egress block: %s", body)
	}
	egress := payload.Nodes[0].Egress
	if !egress.Enabled || egress.State != health.StateOK || egress.Count != 0 {
		t.Fatalf("pending egress = %+v, want enabled ok with no samples", egress)
	}
	if egress.Last != nil {
		t.Fatalf("pending egress carries a last sample: %s", body)
	}
}

func TestHealthNodesOmitsEgressWithoutTheSeam(t *testing.T) {
	sampler := health.New(health.Config{
		Interval: time.Hour,
		Dial: func(context.Context, string, string) (net.Conn, error) {
			client, server := net.Pipe()
			_ = server.Close()
			return client, nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sampler.Start(ctx)
	t.Cleanup(sampler.Stop)
	if err := sampler.Watch("node-1", 20001); err != nil {
		t.Fatalf("watch: %v", err)
	}
	waitForSample(t, sampler, "node-1")

	ts, st := newPlatformServer(t, Config{Health: sampler})
	if _, err := st.PutNode(model.ProxyNode{
		ID: "node-1", AccountID: "acc-01", Label: "test-node-one", Region: "test-region",
		State: model.NodeLive, GatePort: 20001, Capacity: 10, Protocol: model.ProtocolSOCKS5,
	}); err != nil {
		t.Fatalf("put node: %v", err)
	}

	resp, body := apiCall(t, ts, http.MethodGet, "/v1/health/nodes", nil)
	requireStatus(t, resp, http.StatusOK)
	var payload struct {
		Nodes []map[string]json.RawMessage `json:"nodes"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(payload.Nodes) != 1 {
		t.Fatalf("health payload nodes = %d, want 1 (%s)", len(payload.Nodes), body)
	}
	if _, ok := payload.Nodes[0]["egress"]; ok {
		t.Fatalf("loopback-only monitor emitted an egress block: %s", body)
	}
}
