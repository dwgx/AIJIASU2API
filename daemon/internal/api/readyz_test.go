package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	stdruntime "runtime"
	"strings"
	"testing"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/payment"
	"ajsaccountd/internal/runtime"
	"ajsaccountd/internal/store"
)

// readyTestServer builds an API server with the given config and returns a
// live test server. The caller supplies a writable state path in the happy
// path so /readyz is not exercising the state check by accident.
func readyTestServer(t *testing.T, cfg Config) *httptest.Server {
	t.Helper()
	if cfg.AdminToken == "" {
		cfg.AdminToken = testToken
	}
	server := New(cfg, store.New(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)
	return ts
}

func getReadyz(t *testing.T, ts *httptest.Server) (int, readinessResponse) {
	t.Helper()
	resp, body := doRequest(t, ts, newRequest(t, http.MethodGet, ts.URL+"/readyz", nil))
	var payload readinessResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode /readyz: %v (body %s)", err, body)
	}
	return resp.StatusCode, payload
}

func readyCheck(t *testing.T, payload readinessResponse, name string) readinessCheckView {
	t.Helper()
	for _, check := range payload.Checks {
		if check.Name == name {
			return check
		}
	}
	t.Fatalf("check %q missing from %+v", name, payload.Checks)
	return readinessCheckView{}
}

func TestReadyzReadyWhenStateWritable(t *testing.T) {
	dir := t.TempDir()
	ts := readyTestServer(t, Config{StatePath: filepath.Join(dir, "state.json")})

	code, payload := getReadyz(t, ts)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %+v)", code, payload)
	}
	if payload.Status != "ready" {
		t.Fatalf("status field = %q, want ready", payload.Status)
	}
	if payload.Service != "ajs-accountd" || payload.Version == "" {
		t.Fatalf("service/version missing: %+v", payload)
	}
	if len(payload.Reasons) != 0 {
		t.Fatalf("reasons = %v, want none", payload.Reasons)
	}
	state := readyCheck(t, payload, "state")
	if !state.OK || !state.Required {
		t.Fatalf("state check = %+v, want required and ok", state)
	}
	if storeCheck := readyCheck(t, payload, "store"); !storeCheck.OK {
		t.Fatalf("store check = %+v, want ok", storeCheck)
	}
}

func TestReadyzUsesStateDirFallback(t *testing.T) {
	dir := t.TempDir()
	ts := readyTestServer(t, Config{StateDir: dir})
	code, payload := getReadyz(t, ts)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %+v)", code, payload)
	}
	if check := readyCheck(t, payload, "state"); !check.OK {
		t.Fatalf("state check = %+v, want ok", check)
	}
}

func TestReadyzNotReadyWhenStateUnwritable(t *testing.T) {
	base := t.TempDir()
	blocker := filepath.Join(base, "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	statePath := filepath.Join(blocker, "state.json")
	ts := readyTestServer(t, Config{StatePath: statePath})

	code, payload := getReadyz(t, ts)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body %+v)", code, payload)
	}
	if payload.Status != "not_ready" {
		t.Fatalf("status field = %q, want not_ready", payload.Status)
	}
	state := readyCheck(t, payload, "state")
	if state.OK || !state.Required {
		t.Fatalf("state check = %+v, want required and failing", state)
	}
	if !strings.Contains(state.Reason, "not writable") {
		t.Fatalf("state reason = %q, want a writability reason", state.Reason)
	}
	if len(payload.Reasons) != 1 || !strings.Contains(payload.Reasons[0], "state:") {
		t.Fatalf("reasons = %v, want one state reason", payload.Reasons)
	}
	// The public body must not leak the operator's filesystem layout.
	if strings.Contains(strings.Join(payload.Reasons, " "), base) {
		t.Fatalf("reasons leak the state path: %v", payload.Reasons)
	}
}

func TestReadyzNotReadyWhenStateDirReadOnly(t *testing.T) {
	if stdruntime.GOOS == "windows" {
		t.Skip("Windows does not expose POSIX directory write bits")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatalf("chmod state dir: %v", err)
	}
	defer func() { _ = os.Chmod(dir, 0o755) }()
	ts := readyTestServer(t, Config{StatePath: filepath.Join(dir, "state.json")})

	code, payload := getReadyz(t, ts)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body %+v)", code, payload)
	}
	state := readyCheck(t, payload, "state")
	if state.OK || !strings.Contains(state.Reason, "not writable") {
		t.Fatalf("state check = %+v, want not writable", state)
	}
}

func TestReadyzNotReadyWhenStateNotConfigured(t *testing.T) {
	ts := readyTestServer(t, Config{})
	code, payload := getReadyz(t, ts)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", code)
	}
	if reason := readyCheck(t, payload, "state").Reason; !strings.Contains(reason, "not configured") {
		t.Fatalf("state reason = %q, want not configured", reason)
	}
}

func TestReadyzHealthzStaysLiveWhenNotReady(t *testing.T) {
	base := t.TempDir()
	blocker := filepath.Join(base, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	ts := readyTestServer(t, Config{StatePath: filepath.Join(blocker, "state.json")})

	resp, body := doRequest(t, ts, newRequest(t, http.MethodGet, ts.URL+"/healthz", nil))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/healthz status = %d, want 200 even when /readyz is 503 (body %s)", resp.StatusCode, body)
	}
	var health healthResponse
	if err := json.Unmarshal(body, &health); err != nil {
		t.Fatalf("decode /healthz: %v", err)
	}
	if health.Status != "ok" {
		t.Fatalf("health status = %q, want ok", health.Status)
	}
}

func TestReadyzConfiguredEpayMustBeEnabled(t *testing.T) {
	dir := t.TempDir()
	// A gateway and PID without a key is a partially configured adapter: the
	// operator asked for epay, so readiness must not silently ignore it.
	epay := payment.NewEpay(payment.EpayConfig{
		Gateway: "https://pay.example.com/",
		PID:     "1001",
	})
	if !epay.Configured() || epay.Enabled() {
		t.Fatalf("test epay state = configured:%v enabled:%v, want configured and disabled", epay.Configured(), epay.Enabled())
	}
	ts := readyTestServer(t, Config{
		StatePath: filepath.Join(dir, "state.json"),
		Epay:      epay,
	})

	code, payload := getReadyz(t, ts)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", code)
	}
	check := readyCheck(t, payload, "epay")
	if check.OK || !check.Required {
		t.Fatalf("epay check = %+v, want required and failing", check)
	}
	if !strings.Contains(check.Reason, "disabled") {
		t.Fatalf("epay reason = %q, want disabled", check.Reason)
	}
}

// fakePanel is the minimal runtime.Panel used to pin the Resin readiness row.
type fakePanel struct {
	topologyErr error
}

func (f *fakePanel) Topology(context.Context) (runtime.Topology, error) {
	return runtime.Topology{}, f.topologyErr
}
func (f *fakePanel) Reconcile(context.Context) (runtime.Topology, error) {
	return runtime.Topology{}, f.topologyErr
}
func (f *fakePanel) PublishLease(context.Context, model.Lease) error { return nil }
func (f *fakePanel) WithdrawLease(context.Context, string) error     { return nil }
func (f *fakePanel) RotateLease(context.Context, string) (model.Lease, error) {
	return model.Lease{}, nil
}
func (f *fakePanel) ReleaseLease(context.Context, string) error { return nil }
func (f *fakePanel) DrainNode(context.Context, string) (model.ProxyNode, error) {
	return model.ProxyNode{}, nil
}
func (f *fakePanel) UndrainNode(context.Context, string) (model.ProxyNode, error) {
	return model.ProxyNode{}, nil
}

func TestReadyzResinMustBeStarted(t *testing.T) {
	dir := t.TempDir()
	ts := readyTestServer(t, Config{
		StatePath: filepath.Join(dir, "state.json"),
		Panel:     &fakePanel{topologyErr: runtime.ErrNotStarted},
	})
	code, payload := getReadyz(t, ts)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", code)
	}
	check := readyCheck(t, payload, "resin")
	if check.OK || !check.Required {
		t.Fatalf("resin check = %+v, want required and failing", check)
	}
	if !strings.Contains(check.Reason, "reconcile") {
		t.Fatalf("resin reason = %q, want reconcile", check.Reason)
	}

	healthy := readyTestServer(t, Config{
		StatePath: filepath.Join(dir, "state.json"),
		Panel:     &fakePanel{},
	})
	code, payload = getReadyz(t, healthy)
	if code != http.StatusOK {
		t.Fatalf("healthy panel status = %d, want 200 (body %+v)", code, payload)
	}
	if check := readyCheck(t, payload, "resin"); !check.OK {
		t.Fatalf("resin check = %+v, want ok", check)
	}
}

func TestReadyzCustomChecks(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")

	requiredFail := readyTestServer(t, Config{
		StatePath: statePath,
		ReadyChecks: []ReadyCheck{{
			Name:     "vendor",
			Required: true,
			Check:    func(context.Context) error { return errors.New("vendor down") },
		}},
	})
	code, payload := getReadyz(t, requiredFail)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("required failure status = %d, want 503", code)
	}
	if check := readyCheck(t, payload, "vendor"); check.OK || !check.Required {
		t.Fatalf("vendor check = %+v, want required and failing", check)
	}
	if len(payload.Reasons) != 1 || !strings.Contains(payload.Reasons[0], "vendor:") {
		t.Fatalf("reasons = %v, want one vendor reason", payload.Reasons)
	}

	optionalFail := readyTestServer(t, Config{
		StatePath: statePath,
		ReadyChecks: []ReadyCheck{{
			Name:     "telemetry",
			Required: false,
			Check:    func(context.Context) error { return errors.New("not sampling") },
		}},
	})
	code, payload = getReadyz(t, optionalFail)
	if code != http.StatusOK {
		t.Fatalf("optional failure status = %d, want 200", code)
	}
	check := readyCheck(t, payload, "telemetry")
	if check.OK || check.Required {
		t.Fatalf("telemetry check = %+v, want optional and failing", check)
	}
	if len(payload.Reasons) != 0 {
		t.Fatalf("optional failure leaked into reasons: %v", payload.Reasons)
	}
}
