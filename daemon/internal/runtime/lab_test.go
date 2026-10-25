package runtime_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"ajsaccountd/internal/account"
	"ajsaccountd/internal/gate"
	"ajsaccountd/internal/lablog"
	"ajsaccountd/internal/model"
	"ajsaccountd/internal/runtime"
	"ajsproxy/apifacade"
)

// statefulGate is the test gate that exposes counters, proving Snapshot's
// optional GateState assertion without widening the Gate interface.
type statefulGate struct {
	*fakeGate
	state gate.State
}

func (g *statefulGate) State() gate.State { return g.state }

func hasWarning(warnings []model.LabWarning, code string) bool {
	for _, warning := range warnings {
		if warning.Code == code {
			return true
		}
	}
	return false
}

func TestLabViewTelemetry(t *testing.T) {
	st := newNodeTestStore(t)
	acct := newNodeTestAccount(t, st, "lab-telemetry")
	logins := newFakeLoginResults()
	fetchedAt := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	serverOne := testServer("s1")
	serverOne.Par.FV.PS = "ping-secret-value"
	logins.set(acct.ID, account.LoginResult{
		AuthResult:      "OK",
		UserName:        "vendor-user",
		ConnectPassword: account.Secret(testConnectPassword),
		Servers: map[string]apifacade.Server{
			"s1": serverOne,
			"s2": testServer("s2"),
		},
		FetchedAt: fetchedAt,
	})

	gateStarted := time.Now().Add(-time.Minute).UTC().Truncate(time.Second)
	state := gate.State{
		Active:       2,
		Limit:        32,
		Accepted:     3,
		Rejected:     1,
		Probes:       7,
		IngressBytes: 111,
		EgressBytes:  222,
		StartedAt:    gateStarted,
	}
	controller, err := runtime.NewNodeController(runtime.NodeControllerConfig{
		Store:      st,
		Logins:     logins,
		Sessions:   newFakeSessionOpener(),
		MaxServers: 1,
		PortBase:   13100,
		Gates: func(cfg gate.Config, _ gate.Session) (runtime.Gate, error) {
			copied := state
			copied.NodeID = cfg.NodeID
			copied.AccountID = cfg.AccountID
			return &statefulGate{fakeGate: &fakeGate{}, state: copied}, nil
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

	ring := lablog.NewRing(10)
	ringLogger := slog.New(ring.Handler(nil))
	ringLogger.Warn("gate flow rejected", "node_id", slot1NodeID(acct.ID, "s1"), "account_id", acct.ID,
		"error_class", "vendor_rejected", "password", "hunter2")

	view := runtime.NewLabView(runtime.LabConfig{
		Version:      "test-version",
		Commit:       "test-commit",
		Started:      time.Now().Add(-90 * time.Second),
		Store:        st,
		Logins:       logins,
		Controller:   controller,
		LogRing:      ring,
		Profile:      account.Linux423Binary,
		NodeLimit:    1,
		AccountLogin: true,
		Resin:        true,
		UpdateCheck:  true,
		Secrets:      true,
		CertPin:      "pinned",
		Flags: runtime.Flags(runtime.FlagValues{
			Listen:         "127.0.0.1:2261",
			AdminToken:     "supersecret-admin-token",
			MasterKey:      "master-key-material",
			AccountLogin:   true,
			AccountProfile: account.Linux423Binary.Name,
			NodeLimit:      "1",
		}),
	})

	overview := view.Overview(context.Background())
	if !overview.Enabled || overview.Version != "test-version" || overview.Commit != "test-commit" {
		t.Fatalf("overview header = %+v", overview)
	}
	if overview.UptimeSec < 89 || overview.UptimeSec > 120 {
		t.Fatalf("uptime = %d, want ~90", overview.UptimeSec)
	}
	wantCounts := model.LabCounts{Accounts: 1, Nodes: 1, LiveNodes: 1, Sessions: 1, Gates: 1, Leases: 0}
	if overview.Counts != wantCounts {
		t.Fatalf("counts = %+v, want %+v", overview.Counts, wantCounts)
	}
	if overview.Modes.CertPin != "pinned" || !overview.Modes.AccountLogin {
		t.Fatalf("modes = %+v", overview.Modes)
	}

	gates, reason := view.Gates()
	if reason != "" || len(gates) != 1 {
		t.Fatalf("gates = %+v reason=%q", gates, reason)
	}
	got := gates[0]
	if got.NodeID != slot1NodeID(acct.ID, "s1") || got.AccountID != acct.ID || got.State != model.NodeLive {
		t.Fatalf("gate identity = %+v", got)
	}
	if got.Active != 2 || got.Limit != 32 || got.Accepted != 3 || got.Rejected != 1 ||
		got.Probes != 7 || got.IngressBytes != 111 || got.EgressBytes != 222 {
		t.Fatalf("gate counters = %+v", got)
	}
	gateJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal gate view: %v", err)
	}
	if !strings.Contains(string(gateJSON), `"probes":7`) {
		t.Fatalf("gate view JSON = %s, want a probes counter", gateJSON)
	}
	if got.StartedAt != gateStarted.Format(time.RFC3339) {
		t.Fatalf("gate startedAt = %q, want %q", got.StartedAt, gateStarted.Format(time.RFC3339))
	}

	sessions, reason := view.Sessions()
	if reason != "" || len(sessions) != 1 {
		t.Fatalf("sessions = %+v reason=%q", sessions, reason)
	}
	session := sessions[0]
	if session.SID != "fake…(8)" {
		t.Fatalf("sid = %q, want redacted fake…(8)", session.SID)
	}
	if strings.Contains(session.SID, "fake-sid") {
		t.Fatalf("sid leaked the full value: %q", session.SID)
	}
	if !session.Healthy || session.AgeSec < 0 || session.EgressClaimed != "198.51.100.7" {
		t.Fatalf("session = %+v", session)
	}
	if session.Keepalive.Supported || session.Keepalive.KA != nil {
		t.Fatalf("keepalive = %+v, want unsupported", session.Keepalive)
	}

	egress, reason := view.Egress()
	if reason != "" || len(egress) != 1 || egress[0].Claimed != "198.51.100.7" ||
		egress[0].Verified != "" || egress[0].Match != nil || egress[0].Source != "fv-claim" {
		t.Fatalf("egress = %+v reason=%q", egress, reason)
	}

	servers, err := view.Servers(context.Background(), acct.ID)
	if err != nil {
		t.Fatalf("Servers: %v", err)
	}
	if !servers.Enabled || servers.Stale || servers.FetchedAt != fetchedAt.Format(time.RFC3339) {
		t.Fatalf("servers header = %+v", servers)
	}
	if len(servers.Servers) != 2 {
		t.Fatalf("servers = %+v", servers.Servers)
	}
	if servers.Servers[0].ID != "s1" || !servers.Servers[0].Selected || servers.Servers[0].SkippedByLimit {
		t.Fatalf("s1 = %+v", servers.Servers[0])
	}
	if servers.Servers[1].ID != "s2" || servers.Servers[1].Selected || !servers.Servers[1].SkippedByLimit {
		t.Fatalf("s2 = %+v", servers.Servers[1])
	}
	if !servers.Servers[0].HasPingSecret || servers.Servers[1].HasPingSecret {
		t.Fatalf("ping secret presence = %+v / %+v", servers.Servers[0], servers.Servers[1])
	}
	encoded, err := json.Marshal(servers)
	if err != nil {
		t.Fatalf("marshal servers: %v", err)
	}
	if strings.Contains(string(encoded), "ping-secret-value") {
		t.Fatalf("catalog leaked par.fv.ps: %s", encoded)
	}

	device, err := view.Device(acct.ID)
	if err != nil {
		t.Fatalf("Device: %v", err)
	}
	if !device.Enabled || !device.Present || !device.Stable || device.RawAvailable {
		t.Fatalf("device header = %+v", device)
	}
	for _, key := range []string{"uid", "osdid", "cuid"} {
		if !strings.HasPrefix(device.Hashes[key], "sha256:") {
			t.Fatalf("hash %s = %q, want sha256 prefix", key, device.Hashes[key])
		}
	}
	if _, ok := device.Claims["dim"]; ok {
		t.Fatalf("device claims leaked dim: %+v", device.Claims)
	}
	identity, ok := st.AccountDevice(acct.ID)
	if !ok {
		t.Fatal("store lost the device identity")
	}
	deviceJSON, _ := json.Marshal(device)
	for _, raw := range []string{identity.UID, identity.OSDID, identity.CUID} {
		if raw != "" && strings.Contains(string(deviceJSON), raw) {
			t.Fatalf("device response leaked raw id %q: %s", raw, deviceJSON)
		}
	}

	flags := view.Flags()
	if !flags.Enabled || len(flags.Flags) == 0 {
		t.Fatalf("flags = %+v", flags)
	}
	flagJSON, _ := json.Marshal(flags)
	for _, secret := range []string{"supersecret-admin-token", "master-key-material"} {
		if strings.Contains(string(flagJSON), secret) {
			t.Fatalf("flags leaked %q: %s", secret, flagJSON)
		}
	}
	var adminFlag *model.FlagView
	for i := range flags.Flags {
		if flags.Flags[i].Key == "AJS_ADMIN_TOKEN" {
			adminFlag = &flags.Flags[i]
		}
	}
	if adminFlag == nil || adminFlag.Set == nil || !*adminFlag.Set || !adminFlag.Secret ||
		!adminFlag.Redacted || adminFlag.Value != "" {
		t.Fatalf("AJS_ADMIN_TOKEN flag = %+v", adminFlag)
	}

	logs := view.Logs(lablog.Query{Level: slog.LevelDebug, Limit: 10})
	if !logs.Enabled || len(logs.Entries) != 1 || logs.Entries[0].NodeID != slot1NodeID(acct.ID, "s1") {
		t.Fatalf("logs = %+v", logs)
	}
	logJSON, _ := json.Marshal(logs)
	if strings.Contains(string(logJSON), "hunter2") {
		t.Fatalf("logs leaked a dropped attribute: %s", logJSON)
	}

	diagnostics := view.Diagnostics()
	if diagnostics.Health["controller"] != "running" || diagnostics.Health["logRing"] != "ok" ||
		diagnostics.Health["pcap"] != "unavailable" {
		t.Fatalf("diagnostics health = %+v", diagnostics.Health)
	}
	if diagnostics.Build.Version != "test-version" || diagnostics.Build.GoVersion == "" {
		t.Fatalf("diagnostics build = %+v", diagnostics.Build)
	}
}

func TestLabViewDisabledModes(t *testing.T) {
	st := newNodeTestStore(t)
	acct := newNodeTestAccount(t, st, "lab-disabled")
	view := runtime.NewLabView(runtime.LabConfig{
		Store:        st,
		NodeLimit:    1,
		AccountLogin: false,
	})

	if gates, reason := view.Gates(); gates != nil || reason == "" {
		t.Fatalf("gates = %+v reason=%q, want disabled", gates, reason)
	}
	if sessions, reason := view.Sessions(); sessions != nil || reason == "" {
		t.Fatalf("sessions = %+v reason=%q, want disabled", sessions, reason)
	}
	if egress, reason := view.Egress(); egress != nil || reason == "" {
		t.Fatalf("egress = %+v reason=%q, want disabled", egress, reason)
	}

	servers, err := view.Servers(context.Background(), acct.ID)
	if err != nil {
		t.Fatalf("Servers: %v", err)
	}
	if servers.Enabled || servers.Reason == "" || !servers.Stale || len(servers.Servers) != 0 {
		t.Fatalf("servers = %+v, want disabled and stale", servers)
	}

	overview := view.Overview(context.Background())
	if overview.Enabled != true {
		t.Fatalf("overview enabled = %v, want true (store telemetry still works)", overview.Enabled)
	}
	if !hasWarning(overview.Warnings, "account_login_disabled") {
		t.Fatalf("overview warnings = %+v, want account_login_disabled", overview.Warnings)
	}

	if _, err := view.Servers(context.Background(), "missing"); err == nil {
		t.Fatal("Servers for a missing account returned nil error")
	}
	if _, err := view.Device("missing"); err == nil {
		t.Fatal("Device for a missing account returned nil error")
	}
}

func TestLabViewCatalogStaleWithoutLogin(t *testing.T) {
	st := newNodeTestStore(t)
	acct := newNodeTestAccount(t, st, "lab-stale")
	view := runtime.NewLabView(runtime.LabConfig{
		Store:     st,
		Logins:    newFakeLoginResults(),
		NodeLimit: 1,
	})
	servers, err := view.Servers(context.Background(), acct.ID)
	if err != nil {
		t.Fatalf("Servers: %v", err)
	}
	if !servers.Enabled || !servers.Stale || servers.Reason == "" || servers.FetchedAt != "" {
		t.Fatalf("servers = %+v, want stale catalog", servers)
	}
	if servers.Servers == nil {
		t.Fatal("servers slice is nil, want []")
	}
}

func TestLabViewWarnings(t *testing.T) {
	st := newNodeTestStore(t)
	view := runtime.NewLabView(runtime.LabConfig{
		Store:        st,
		NodeLimit:    3,
		AccountLogin: false,
		Secrets:      false,
		CertPin:      "default",
	})
	warnings := view.Overview(context.Background()).Warnings
	for _, code := range []string{"account_login_disabled", "secrets_disabled", "cert_pin_default", "node_limit_gt_1"} {
		if !hasWarning(warnings, code) {
			t.Fatalf("warnings = %+v, want %s", warnings, code)
		}
	}
	for _, warning := range warnings {
		if warning.Code == "node_limit_gt_1" && !strings.Contains(warning.Message, "AJS_NODE_LIMIT=3") {
			t.Fatalf("node_limit warning = %q", warning.Message)
		}
	}
}

func TestLabViewProfilesAndFlags(t *testing.T) {
	t.Setenv("AJS_ACCOUNT_LOGIN", "1")
	flags := runtime.Flags(runtime.FlagValues{
		Listen:         "127.0.0.1:2261",
		LogLevel:       "info",
		AccountProfile: account.Linux423Binary.Name,
		NodeLimit:      "1",
		AccountLogin:   true,
	})
	var loginFlag *model.FlagView
	for i := range flags {
		if flags[i].Key == "AJS_ACCOUNT_LOGIN" {
			loginFlag = &flags[i]
		}
	}
	if loginFlag == nil || loginFlag.Value != "1" || loginFlag.Source != "env" || loginFlag.Secret {
		t.Fatalf("AJS_ACCOUNT_LOGIN flag = %+v", loginFlag)
	}

	view := runtime.NewLabView(runtime.LabConfig{
		Store:        newNodeTestStore(t),
		AccountLogin: true,
		Profile:      account.Linux423Binary,
		Flags:        flags,
	})
	profiles := view.Profiles()
	if !profiles.Enabled || profiles.Active != account.Linux423Binary.Name || profiles.Reason != "" {
		t.Fatalf("profiles = %+v", profiles)
	}
	if len(profiles.Supported) != 3 || len(profiles.Samples) != 5 {
		t.Fatalf("profiles matrix = %+v", profiles)
	}
	if profiles.Supported[0].RequestShape != "linux423" || profiles.Supported[0].Transport != "httpupgrade" {
		t.Fatalf("active profile view = %+v", profiles.Supported[0])
	}
	linux430 := profiles.Supported[1]
	if linux430.Name != account.Linux430Binary.Name || linux430.ClientVersion != "4.3.0.1" ||
		linux430.CoreVersion != "20260525-1" || linux430.SigningMode != "none" ||
		linux430.Transport != "httpupgrade" || linux430.RequestShape != "linux423" {
		t.Fatalf("linux 4.3.0.1 profile view = %+v", linux430)
	}
	if !strings.Contains(linux430.Note, "unverified") {
		t.Fatalf("linux 4.3.0.1 note = %q, want the unverified caveat", linux430.Note)
	}
	if profiles.Supported[2].Name != account.OpenAJSAndroid.Name || profiles.Supported[2].Note != "" {
		t.Fatalf("OpenAJS profile view = %+v", profiles.Supported[2])
	}
}
