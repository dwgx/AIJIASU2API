package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ajsaccountd/internal/account"
	"ajsaccountd/internal/lablog"
	"ajsaccountd/internal/model"
	"ajsaccountd/internal/runtime"
	"ajsaccountd/internal/store"
)

const labSecretToken = "supersecret-admin-token"

func newLabTestServer(t *testing.T, lab LabReader, st *store.Store) *httptest.Server {
	t.Helper()
	server := New(Config{AdminToken: testToken, Lab: lab}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)
	return ts
}

func labGET(t *testing.T, ts *httptest.Server, path string, auth bool) (int, []byte) {
	t.Helper()
	req := newRequest(t, http.MethodGet, ts.URL+path, nil)
	if auth {
		req.Header.Set("Authorization", "Bearer "+testToken)
	}
	resp, body := doRequest(t, ts, req)
	return resp.StatusCode, body
}

func TestLabRoutesRequireAuth(t *testing.T) {
	ts, _ := newTestServer(t)
	routes := []string{
		"/v1/lab/overview",
		"/v1/lab/flags",
		"/v1/lab/gates",
		"/v1/lab/sessions",
		"/v1/lab/egress",
		"/v1/lab/profiles",
		"/v1/lab/logs",
		"/v1/lab/diagnostics",
		"/v1/accounts/acc-01/servers",
		"/v1/accounts/acc-01/device",
	}
	for _, route := range routes {
		status, body := labGET(t, ts, route, false)
		if status != http.StatusUnauthorized {
			t.Fatalf("%s without auth = %d, want 401: %s", route, status, body)
		}
	}
}

func TestLabRoutesDisabledWithoutReader(t *testing.T) {
	ts, _ := newTestServer(t)
	cases := []struct {
		path   string
		reason bool
	}{
		{"/v1/lab/overview", true},
		{"/v1/lab/flags", true},
		{"/v1/lab/gates", true},
		{"/v1/lab/sessions", true},
		{"/v1/lab/egress", true},
		{"/v1/lab/profiles", true},
		{"/v1/lab/logs", true},
		{"/v1/lab/diagnostics", true},
		{"/v1/accounts/acc-01/servers", true},
		{"/v1/accounts/acc-01/device", true},
	}
	for _, tc := range cases {
		status, body := labGET(t, ts, tc.path, true)
		if status != http.StatusOK {
			t.Fatalf("%s = %d, want 200: %s", tc.path, status, body)
		}
		var payload struct {
			Enabled bool   `json:"enabled"`
			Reason  string `json:"reason"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("%s decode: %v (%s)", tc.path, err, body)
		}
		if payload.Enabled {
			t.Fatalf("%s enabled without a reader: %s", tc.path, body)
		}
		if tc.reason && payload.Reason == "" {
			t.Fatalf("%s has no disabled reason: %s", tc.path, body)
		}
	}
}

// newLabFixture builds one account with a device identity and a read-only Lab
// view with login/controller disabled, mirroring AJS_ACCOUNT_LOGIN=0.
func newLabFixture(t *testing.T) (*store.Store, model.Account, *runtime.LabView) {
	t.Helper()
	st := store.New()
	acct, err := st.CreateAccount(model.CreateAccountInput{
		Label:       "lab-fixture",
		Username:    "lab-user",
		Password:    "fixture-password-not-a-credential",
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
	ring := lablog.NewRing(10)
	slog.New(ring.Handler(nil)).Warn("gate flow rejected",
		"node_id", acct.ID+":srv-01", "account_id", acct.ID, "error_class", "vendor_rejected",
		"password", "ring-hunter2", "target", "example.com:443")
	lab := runtime.NewLabView(runtime.LabConfig{
		Version:      "test-version",
		Store:        st,
		LogRing:      ring,
		NodeLimit:    1,
		AccountLogin: false,
		Flags: runtime.Flags(runtime.FlagValues{
			Listen:     "127.0.0.1:2261",
			AdminToken: labSecretToken,
			MasterKey:  "master-key-material",
			LogLevel:   "info",
			NodeLimit:  "1",
		}),
	})
	return st, acct, lab
}

func TestLabDisabledBodiesWithLoginOff(t *testing.T) {
	st, acct, lab := newLabFixture(t)
	ts := newLabTestServer(t, lab, st)

	for _, route := range []string{"/v1/lab/gates", "/v1/lab/sessions", "/v1/lab/egress"} {
		status, body := labGET(t, ts, route, true)
		if status != http.StatusOK {
			t.Fatalf("%s = %d: %s", route, status, body)
		}
		var payload model.LabDisabled
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("%s decode: %v (%s)", route, err, body)
		}
		if payload.Enabled || payload.Reason == "" {
			t.Fatalf("%s = %s, want enabled:false with reason", route, body)
		}
	}

	status, body := labGET(t, ts, "/v1/accounts/"+acct.ID+"/servers", true)
	if status != http.StatusOK {
		t.Fatalf("servers = %d: %s", status, body)
	}
	var servers model.ServersView
	if err := json.Unmarshal(body, &servers); err != nil {
		t.Fatalf("servers decode: %v (%s)", err, body)
	}
	if servers.Enabled || servers.Reason == "" || !servers.Stale || servers.AccountID != acct.ID {
		t.Fatalf("servers = %+v, want disabled/stale", servers)
	}
	if servers.Servers == nil {
		t.Fatal("servers is nil, want []")
	}

	status, body = labGET(t, ts, "/v1/lab/overview", true)
	if status != http.StatusOK {
		t.Fatalf("overview = %d: %s", status, body)
	}
	var overview model.LabOverview
	if err := json.Unmarshal(body, &overview); err != nil {
		t.Fatalf("overview decode: %v (%s)", err, body)
	}
	if !overview.Enabled || overview.Modes.AccountLogin {
		t.Fatalf("overview = %+v", overview)
	}
	found := false
	for _, warning := range overview.Warnings {
		if warning.Code == "account_login_disabled" {
			found = true
		}
	}
	if !found {
		t.Fatalf("overview warnings = %+v, want account_login_disabled", overview.Warnings)
	}
}

func TestLabDeviceRedaction(t *testing.T) {
	st, acct, lab := newLabFixture(t)
	ts := newLabTestServer(t, lab, st)

	status, body := labGET(t, ts, "/v1/accounts/"+acct.ID+"/device", true)
	if status != http.StatusOK {
		t.Fatalf("device = %d: %s", status, body)
	}
	var device model.DeviceView
	if err := json.Unmarshal(body, &device); err != nil {
		t.Fatalf("device decode: %v (%s)", err, body)
	}
	if !device.Enabled || !device.Present || !device.Stable || device.RawAvailable {
		t.Fatalf("device = %+v", device)
	}
	for _, key := range []string{"uid", "osdid", "cuid"} {
		if !strings.HasPrefix(device.Hashes[key], "sha256:") {
			t.Fatalf("hash %s = %q", key, device.Hashes[key])
		}
	}
	if _, ok := device.Claims["dim"]; ok {
		t.Fatalf("device claims leaked dim: %+v", device.Claims)
	}
	identity, ok := st.AccountDevice(acct.ID)
	if !ok {
		t.Fatal("fixture lost its device identity")
	}
	for _, raw := range []string{identity.UID, identity.OSDID, identity.CUID} {
		if raw != "" && strings.Contains(string(body), raw) {
			t.Fatalf("device response leaked raw id %q: %s", raw, body)
		}
	}

	status, body = labGET(t, ts, "/v1/accounts/missing/device", true)
	if status != http.StatusNotFound {
		t.Fatalf("missing device = %d, want 404: %s", status, body)
	}
	status, body = labGET(t, ts, "/v1/accounts/missing/servers", true)
	if status != http.StatusNotFound {
		t.Fatalf("missing servers = %d, want 404: %s", status, body)
	}
}

func TestLabFlagsNeverExposeSecrets(t *testing.T) {
	st, _, lab := newLabFixture(t)
	ts := newLabTestServer(t, lab, st)

	status, body := labGET(t, ts, "/v1/lab/flags", true)
	if status != http.StatusOK {
		t.Fatalf("flags = %d: %s", status, body)
	}
	for _, secret := range []string{labSecretToken, "master-key-material"} {
		if strings.Contains(string(body), secret) {
			t.Fatalf("flags leaked %q: %s", secret, body)
		}
	}
	var flags model.FlagsView
	if err := json.Unmarshal(body, &flags); err != nil {
		t.Fatalf("flags decode: %v (%s)", err, body)
	}
	if !flags.Enabled || len(flags.Flags) == 0 {
		t.Fatalf("flags = %+v", flags)
	}
	for _, flag := range flags.Flags {
		if flag.Secret && flag.Value != "" {
			t.Fatalf("secret flag %s carries a value: %+v", flag.Key, flag)
		}
	}
}

func TestLabLogsFiltersAndRedaction(t *testing.T) {
	st, acct, lab := newLabFixture(t)
	ts := newLabTestServer(t, lab, st)

	status, body := labGET(t, ts, "/v1/lab/logs?limit=10&level=warn&component=gate", true)
	if status != http.StatusOK {
		t.Fatalf("logs = %d: %s", status, body)
	}
	if strings.Contains(string(body), "ring-hunter2") || strings.Contains(string(body), "example.com") {
		t.Fatalf("logs leaked a dropped attribute: %s", body)
	}
	var logs model.LogsView
	if err := json.Unmarshal(body, &logs); err != nil {
		t.Fatalf("logs decode: %v (%s)", err, body)
	}
	if !logs.Enabled || len(logs.Entries) != 1 {
		t.Fatalf("logs = %+v", logs)
	}
	entry := logs.Entries[0]
	if entry.Component != "gate" || entry.AccountID != acct.ID || entry.ErrorClass != "vendor_rejected" {
		t.Fatalf("entry = %+v", entry)
	}

	for _, query := range []string{
		"/v1/lab/logs?limit=0",
		"/v1/lab/logs?limit=2001",
		"/v1/lab/logs?level=trace",
		"/v1/lab/logs?since=yesterday",
	} {
		status, body := labGET(t, ts, query, true)
		if status != http.StatusBadRequest {
			t.Fatalf("%s = %d, want 400: %s", query, status, body)
		}
	}
}

// egressCheckLab overrides only CheckEgress; the embedded interface keeps the
// fake small while satisfying LabReader.
type egressCheckLab struct {
	LabReader
	result    model.EgressCheckView
	err       error
	gotNodeID string
}

func (f *egressCheckLab) CheckEgress(_ context.Context, nodeID string) (model.EgressCheckView, error) {
	f.gotNodeID = nodeID
	return f.result, f.err
}

func TestLabEgressCheckHandler(t *testing.T) {
	matched := true
	fake := &egressCheckLab{result: model.EgressCheckView{
		NodeID:    "acc-07:vvn-2076-264",
		Claimed:   "203.0.113.9",
		Verified:  "203.0.113.9",
		Match:     &matched,
		LatencyMs: 42,
		CheckedAt: "2026-10-24T12:00:00Z",
		Source:    "echo",
	}}
	ts := newLabTestServer(t, fake, store.New())

	req := newRequest(t, http.MethodPost, ts.URL+"/v1/lab/egress-check",
		map[string]string{"nodeId": "acc-07:vvn-2076-264"})
	resp, body := doRequest(t, ts, req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated check = %d, want 401: %s", resp.StatusCode, body)
	}

	resp, body = apiCall(t, ts, http.MethodPost, "/v1/lab/egress-check",
		map[string]string{"nodeId": " acc-07:vvn-2076-264 "})
	requireStatus(t, resp, http.StatusOK)
	got := decodeBody[model.EgressCheckView](t, body)
	if got.NodeID != fake.result.NodeID || got.Claimed != fake.result.Claimed ||
		got.Verified != fake.result.Verified || got.Match == nil || !*got.Match ||
		got.LatencyMs != fake.result.LatencyMs || got.CheckedAt != fake.result.CheckedAt ||
		got.Source != "echo" || got.Error != "" {
		t.Fatalf("check body = %+v", got)
	}
	if fake.gotNodeID != "acc-07:vvn-2076-264" {
		t.Fatalf("nodeId passed to runtime = %q, want trimmed id", fake.gotNodeID)
	}

	for _, tc := range []struct {
		name   string
		body   any
		status int
		code   string
	}{
		{"empty node", map[string]string{}, http.StatusBadRequest, CodeInvalidArgument},
		{"blank node", map[string]string{"nodeId": "   "}, http.StatusBadRequest, CodeInvalidArgument},
		{"unknown field", map[string]string{"nodeId": "n1", "confirm": "yes"}, http.StatusBadRequest, CodeInvalidArgument},
		{"wrong type", map[string]int{"nodeId": 1}, http.StatusBadRequest, CodeInvalidArgument},
	} {
		resp, body := apiCall(t, ts, http.MethodPost, "/v1/lab/egress-check", tc.body)
		if resp.StatusCode != tc.status {
			t.Fatalf("%s = %d, want %d: %s", tc.name, resp.StatusCode, tc.status, body)
		}
		requireErrorCode(t, body, tc.code)
	}

	fake.err = fmt.Errorf("%w: node %q", runtime.ErrEgressNoGate, "missing")
	resp, body = apiCall(t, ts, http.MethodPost, "/v1/lab/egress-check", map[string]string{"nodeId": "missing"})
	requireStatus(t, resp, http.StatusConflict)
	requireErrorCode(t, body, CodeConflict)

	fake.err = fmt.Errorf("%w: node %q, retry in 5s", runtime.ErrEgressRateLimited, "n1")
	resp, body = apiCall(t, ts, http.MethodPost, "/v1/lab/egress-check", map[string]string{"nodeId": "n1"})
	requireStatus(t, resp, http.StatusTooManyRequests)
	requireErrorCode(t, body, CodeQuotaExceeded)
}

func TestLabEgressCheckProbeFailureIsA200Record(t *testing.T) {
	fake := &egressCheckLab{result: model.EgressCheckView{
		NodeID:    "n1",
		Claimed:   "203.0.113.9",
		Verified:  "",
		LatencyMs: 10000,
		CheckedAt: "2026-10-24T12:00:00Z",
		Source:    "echo",
		Error:     "egress probe: context deadline exceeded",
	}}
	ts := newLabTestServer(t, fake, store.New())
	resp, body := apiCall(t, ts, http.MethodPost, "/v1/lab/egress-check", map[string]string{"nodeId": "n1"})
	requireStatus(t, resp, http.StatusOK)
	got := decodeBody[model.EgressCheckView](t, body)
	if got.Error == "" || got.Verified != "" || got.Match != nil || got.Source != "echo" {
		t.Fatalf("failed check body = %+v", got)
	}
}

func TestLabEgressCheckWithoutReader(t *testing.T) {
	ts, _ := newTestServer(t)
	resp, body := apiCall(t, ts, http.MethodPost, "/v1/lab/egress-check", map[string]string{"nodeId": "n1"})
	requireStatus(t, resp, http.StatusConflict)
	requireErrorCode(t, body, CodeConflict)
}

func TestLabResponsesCarryNoSecretValues(t *testing.T) {
	st, acct, lab := newLabFixture(t)
	ts := newLabTestServer(t, lab, st)

	identity, ok := st.AccountDevice(acct.ID)
	if !ok {
		t.Fatal("fixture lost its device identity")
	}
	forbidden := []string{labSecretToken, "master-key-material", "ring-hunter2"}
	if identity.UID != "" {
		forbidden = append(forbidden, identity.UID)
	}
	if identity.OSDID != "" {
		forbidden = append(forbidden, identity.OSDID)
	}
	routes := []string{
		"/v1/lab/overview",
		"/v1/lab/flags",
		"/v1/lab/gates",
		"/v1/lab/sessions",
		"/v1/lab/egress",
		"/v1/lab/profiles",
		"/v1/lab/logs",
		"/v1/lab/diagnostics",
		"/v1/accounts/" + acct.ID + "/servers",
		"/v1/accounts/" + acct.ID + "/device",
	}
	for _, route := range routes {
		status, body := labGET(t, ts, route, true)
		if status != http.StatusOK {
			t.Fatalf("%s = %d: %s", route, status, body)
		}
		for _, secret := range forbidden {
			if strings.Contains(string(body), secret) {
				t.Fatalf("%s leaked %q: %s", route, secret, body)
			}
		}
	}
}
