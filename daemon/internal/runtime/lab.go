package runtime

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"ajsaccountd/internal/account"
	"ajsaccountd/internal/gate"
	"ajsaccountd/internal/lablog"
	"ajsaccountd/internal/model"
	"ajsaccountd/internal/store"
	"ajsproxy/apifacade"
)

// Disabled reasons are part of the Lab contract: a disabled mode answers 200
// with the exact switch that is off, never a 500 or an unexplained empty list.
const (
	reasonLoginDisabled = "AJS_ACCOUNT_LOGIN is not 1"
	reasonLogsDisabled  = "the runtime log ring is not configured"
)

// LabConfig is the effective daemon configuration the read-only Lab view
// reports. Every dependency is optional: a nil Controller or Logins yields the
// corresponding enabled:false body, which is how AJS_ACCOUNT_LOGIN=0 stays
// honest instead of failing.
type LabConfig struct {
	Version   string
	Commit    string
	GoVersion string
	Started   time.Time
	// Now defaults to time.Now; tests pin it.
	Now func() time.Time

	Store      *store.Store
	Logins     LoginResults
	Controller *NodeController
	LogRing    *lablog.Ring
	Profile    account.VersionProfile
	NodeLimit  int

	AccountLogin bool
	Resin        bool
	UpdateCheck  bool
	Secrets      bool
	CertPin      string
	StatePath    string
	Flags        []model.FlagView

	// EgressCheckURL is the HTTPS echo the check reads through the node's
	// gate. Empty selects https://ifconfig.me/ip; tests point it at an
	// httptest server.
	EgressCheckURL string
	// EgressCheckTimeout bounds one check end to end. Zero selects 10s.
	EgressCheckTimeout time.Duration
	// EgressCheckInterval is the per-node cooldown between checks. Zero
	// selects 5s.
	EgressCheckInterval time.Duration
	// EgressTLSConfig is the TLS client configuration for the echo request.
	// Nil verifies against the system roots; tests trust an httptest server.
	EgressTLSConfig *tls.Config
}

// LabView implements the Lab telemetry. GETs are read-only and contact no
// vendor; CheckEgress is the one explicit outbound probe, and it uses the
// node's own gate. The view never mutates the store and never serializes a
// credential: sessions expose a redacted SID, devices expose hashes, and flags
// expose secret presence only.
type LabView struct {
	cfg   LabConfig
	store *store.Store

	// egressMu guards egressLast, the per-node check cooldown. A check
	// reserves its slot before dialing, so concurrent calls for one node
	// cannot both pass the interval.
	egressMu   sync.Mutex
	egressLast map[string]time.Time
}

// NewLabView builds the view. Nil Store falls back to an empty store, a zero
// Started uses the current time, and an empty CertPin reads as "default".
func NewLabView(cfg LabConfig) *LabView {
	if cfg.Store == nil {
		cfg.Store = store.New()
	}
	if cfg.Started.IsZero() {
		cfg.Started = time.Now()
	}
	if cfg.CertPin == "" {
		cfg.CertPin = "default"
	}
	if cfg.Flags == nil {
		cfg.Flags = []model.FlagView{}
	}
	if cfg.EgressCheckURL == "" {
		cfg.EgressCheckURL = defaultEgressCheckURL
	}
	if cfg.EgressCheckTimeout <= 0 {
		cfg.EgressCheckTimeout = defaultEgressCheckTimeout
	}
	if cfg.EgressCheckInterval <= 0 {
		cfg.EgressCheckInterval = defaultEgressCheckInterval
	}
	return &LabView{cfg: cfg, store: cfg.Store, egressLast: make(map[string]time.Time)}
}

func (v *LabView) now() time.Time {
	if v.cfg.Now != nil {
		return v.cfg.Now()
	}
	return time.Now()
}

func (v *LabView) snapshot() []NodeRuntime {
	if v.cfg.Controller == nil {
		return nil
	}
	return v.cfg.Controller.Snapshot()
}

// Overview serves GET /v1/lab/overview: version, uptime, effective modes,
// aggregate counts and the warnings that explain a disabled or risky mode.
func (v *LabView) Overview(context.Context) model.LabOverview {
	now := v.now()
	accounts := v.store.Accounts()
	nodes := v.store.Nodes()
	live := 0
	for _, node := range nodes {
		if node.State == model.NodeLive {
			live++
		}
	}
	snapshot := v.snapshot()
	sessions := 0
	for _, node := range snapshot {
		if node.SessionPresent {
			sessions++
		}
	}
	uptime := int64(now.Sub(v.cfg.Started).Seconds())
	if uptime < 0 {
		uptime = 0
	}
	return model.LabOverview{
		Enabled:     true,
		GeneratedAt: formatTime(now),
		Version:     v.cfg.Version,
		Commit:      v.cfg.Commit,
		UptimeSec:   uptime,
		Modes: model.LabModes{
			AccountLogin: v.cfg.AccountLogin,
			Resin:        v.cfg.Resin,
			UpdateCheck:  v.cfg.UpdateCheck,
			Secrets:      v.cfg.Secrets,
			CertPin:      v.cfg.CertPin,
		},
		Counts: model.LabCounts{
			Accounts:  len(accounts),
			Nodes:     len(nodes),
			LiveNodes: live,
			Sessions:  sessions,
			Gates:     len(snapshot),
			Leases:    len(v.store.Leases()),
		},
		Warnings: v.warnings(accounts, nodes),
	}
}

// warnings explains every disabled integration and the configurations the
// live report proved risky. Messages name flags and ids only, never values.
func (v *LabView) warnings(accounts []model.Account, nodes []model.ProxyNode) []model.LabWarning {
	out := make([]model.LabWarning, 0, 8)
	if !v.cfg.AccountLogin {
		out = append(out, model.LabWarning{
			Code: "account_login_disabled", Severity: model.SeverityInfo,
			Message: reasonLoginDisabled + "; vendor login, sessions and gates are disabled",
		})
	}
	if !v.cfg.Resin {
		out = append(out, model.LabWarning{
			Code: "resin_disabled", Severity: model.SeverityInfo,
			Message: "AJS_RESIN_ADMIN_TOKEN is not set; topology uses the store view and no subscription is published",
		})
	}
	if !v.cfg.UpdateCheck {
		out = append(out, model.LabWarning{
			Code: "update_check_disabled", Severity: model.SeverityInfo,
			Message: "AJS_UPDATE_REPO is not set; the update check is disabled",
		})
	}
	if !v.cfg.Secrets {
		out = append(out, model.LabWarning{
			Code: "secrets_disabled", Severity: model.SeverityWarn,
			Message: "AJS_MASTER_KEY is not set; account passwords are discarded and vendor login answers 409",
		})
	}
	if v.cfg.CertPin != "pinned" {
		out = append(out, model.LabWarning{
			Code: "cert_pin_default", Severity: model.SeverityWarn,
			Message: "AJS_CONTROL_CERT_SHA256 is not set; control TLS uses default verification",
		})
	}
	if v.cfg.NodeLimit > 1 {
		out = append(out, model.LabWarning{
			Code: "node_limit_gt_1", Severity: model.SeverityWarn,
			Message: fmt.Sprintf("AJS_NODE_LIMIT=%d; the vendor allows one FV session per account", v.cfg.NodeLimit),
		})
	}
	if v.cfg.NodeLimit == 0 {
		out = append(out, model.LabWarning{
			Code: "node_limit_zero", Severity: model.SeverityWarn,
			Message: "AJS_NODE_LIMIT=0; every usable server starts a session, which is not live-safe",
		})
	}
	if v.cfg.LogRing == nil {
		out = append(out, model.LabWarning{
			Code: "log_ring_disabled", Severity: model.SeverityInfo,
			Message: reasonLogsDisabled,
		})
	}
	if len(accounts) > 1 {
		out = append(out, model.LabWarning{
			Code: "multi_account", Severity: model.SeverityWarn,
			Message: fmt.Sprintf("%d accounts share one daemon; the vendor allows one FV session per account", len(accounts)),
		})
	}
	liveByAccount := make(map[string]bool, len(nodes))
	for _, node := range nodes {
		if node.State == model.NodeLive {
			liveByAccount[node.AccountID] = true
		}
	}
	for _, account := range accounts {
		if account.Concurrency > 1 {
			out = append(out, model.LabWarning{
				Code: "account_concurrency_gt_1", Severity: model.SeverityWarn,
				Message: fmt.Sprintf("account %s has concurrency %d; the vendor allows one active session", account.ID, account.Concurrency),
			})
		}
		if account.State == model.AccountPaused && liveByAccount[account.ID] {
			out = append(out, model.LabWarning{
				Code: "paused_account_with_live_nodes", Severity: model.SeverityWarn,
				Message: fmt.Sprintf("account %s is paused but owns LIVE nodes; pause does not stop gates", account.ID),
			})
		}
	}
	return out
}

// Flags serves GET /v1/lab/flags. The slice is a copy of the startup view; a
// secret flag never carried a value in the first place.
func (v *LabView) Flags() model.FlagsView {
	flags := make([]model.FlagView, len(v.cfg.Flags))
	copy(flags, v.cfg.Flags)
	return model.FlagsView{Enabled: true, Flags: flags}
}

// Gates serves GET /v1/lab/gates. The second return value is the disabled
// reason; a non-empty reason means the caller must answer enabled:false.
func (v *LabView) Gates() ([]model.GateView, string) {
	if v.cfg.Controller == nil {
		return nil, reasonLoginDisabled
	}
	snapshot := v.snapshot()
	out := make([]model.GateView, 0, len(snapshot))
	for _, running := range snapshot {
		node, hasNode := v.store.Node(running.NodeID)
		view := model.GateView{
			NodeID:    running.NodeID,
			AccountID: running.AccountID,
			Port:      running.Port,
			Draining:  running.Draining,
		}
		if hasNode {
			view.State = node.State
			if view.Port == 0 {
				view.Port = node.GatePort
			}
			view.Limit = node.Capacity
			view.Active = node.InUse
		}
		if running.GateKnown {
			state := running.Gate
			view.Active = state.Active
			if state.Limit > 0 {
				view.Limit = state.Limit
			}
			view.Accepted = state.Accepted
			view.Rejected = state.Rejected
			view.IngressBytes = state.IngressBytes
			view.EgressBytes = state.EgressBytes
			view.Draining = state.Draining || running.Draining
			view.StartedAt = formatTime(state.StartedAt)
		}
		out = append(out, view)
	}
	return out, ""
}

// labFlowMaxLimit is one gate's ring capacity: /v1/lab/flows never asks for
// more than a single gate can retain.
const labFlowMaxLimit = 500

// Flows serves GET /v1/lab/flows: the newest finished flows across every
// running gate, joined with the node and account identity. The session SID is
// redacted, and a record never carries a URL, header or credential. The second
// return value is the disabled reason; a non-empty reason means the caller
// must answer enabled:false.
func (v *LabView) Flows(limit int) (model.FlowsView, string) {
	if v.cfg.Controller == nil {
		return model.FlowsView{}, reasonLoginDisabled
	}
	if limit <= 0 || limit > labFlowMaxLimit {
		limit = labFlowMaxLimit
	}
	type joinedFlow struct {
		record    gate.FlowRecord
		nodeID    string
		accountID string
		sid       string
	}
	joined := make([]joinedFlow, 0, limit)
	var dropped uint64
	for _, running := range v.snapshot() {
		if running.FlowRing == nil {
			continue
		}
		dropped += running.FlowRing.Dropped()
		sid := redactSID(running.SessionSID)
		for _, record := range running.FlowRing.Flows(limit) {
			joined = append(joined, joinedFlow{
				record:    record,
				nodeID:    running.NodeID,
				accountID: running.AccountID,
				sid:       sid,
			})
		}
	}
	sort.SliceStable(joined, func(i, j int) bool {
		if !joined[i].record.StartedAt.Equal(joined[j].record.StartedAt) {
			return joined[i].record.StartedAt.After(joined[j].record.StartedAt)
		}
		return joined[i].record.ID > joined[j].record.ID
	})
	if len(joined) > limit {
		joined = joined[:limit]
	}
	flows := make([]model.FlowView, 0, len(joined))
	for _, row := range joined {
		record := row.record
		flows = append(flows, model.FlowView{
			ID:         record.ID,
			NodeID:     row.nodeID,
			AccountID:  row.accountID,
			SID:        row.sid,
			StartedAt:  formatTime(record.StartedAt),
			DurationMs: record.DurationMs,
			Method:     record.Method,
			TargetHost: record.TargetHost,
			TargetPort: record.TargetPort,
			Status:     record.Status,
			BytesIn:    record.BytesIn,
			BytesOut:   record.BytesOut,
			ErrorClass: record.ErrorClass,
		})
	}
	return model.FlowsView{Enabled: true, Flows: flows, Dropped: dropped}, ""
}

// Sessions serves GET /v1/lab/sessions. SID is a prefix plus length; the full
// value never leaves the process.
func (v *LabView) Sessions() ([]model.SessionView, string) {
	if v.cfg.Controller == nil {
		return nil, reasonLoginDisabled
	}
	now := v.now()
	snapshot := v.snapshot()
	out := make([]model.SessionView, 0, len(snapshot))
	for _, running := range snapshot {
		age := int64(0)
		if !running.StartedAt.IsZero() {
			age = int64(now.Sub(running.StartedAt).Seconds())
			if age < 0 {
				age = 0
			}
		}
		out = append(out, model.SessionView{
			NodeID:        running.NodeID,
			AccountID:     running.AccountID,
			ServerID:      running.ServerID,
			SID:           redactSID(running.SessionSID),
			Healthy:       running.SessionHealthy,
			AgeSec:        age,
			LastFlowAt:    nil,
			EgressClaimed: running.EgressIP,
			Keepalive:     model.LabKeepalive{Supported: false},
		})
	}
	return out, ""
}

// Egress serves GET /v1/lab/egress: the vendor's claimed exit address. The
// first slice performs no check, so verified/match/checkedAt stay empty.
func (v *LabView) Egress() ([]model.EgressView, string) {
	if v.cfg.Controller == nil {
		return nil, reasonLoginDisabled
	}
	snapshot := v.snapshot()
	out := make([]model.EgressView, 0, len(snapshot))
	for _, running := range snapshot {
		source := ""
		if running.EgressIP != "" {
			source = "fv-claim"
		}
		out = append(out, model.EgressView{
			NodeID:  running.NodeID,
			Claimed: running.EgressIP,
			Source:  source,
		})
	}
	return out, ""
}

// Profiles serves GET /v1/lab/profiles: the active profile, the supported
// profiles and the shipped-client baselines from the research matrix.
func (v *LabView) Profiles() model.ProfilesView {
	active := ""
	reason := ""
	if v.cfg.AccountLogin {
		active = v.cfg.Profile.Name
	} else {
		reason = reasonLoginDisabled + "; no profile is active"
	}
	linux430 := profileView(account.Linux430Binary)
	linux430.Note = "4.3.0.1 is statically derived; unsigned live acceptance is unverified"
	return model.ProfilesView{
		Enabled: true,
		Reason:  reason,
		Active:  active,
		Supported: []model.ProfileView{
			profileView(account.Linux423Binary),
			linux430,
			profileView(account.OpenAJSAndroid),
		},
		Samples: []model.ProfileSample{
			{Platform: "windows", Version: "4.9.6.0", Core: "20250708-1", Source: "research/40-protocol/PROTOCOL.md:532-542"},
			{Platform: "linux", Version: "4.3.0.1", Core: "20260525-1", Source: "research/30-linux-static/REPORT.md"},
			{Platform: "linux", Version: "4.2.3.0", Core: "20241105-1", Source: "research/40-protocol/PROTOCOL.md:532-542"},
			{Platform: "android", Version: "4.12.2.0", Core: "20260525-1", Source: "apktool.yml versionName; TRIAGE.md:362-395"},
			{Platform: "macos", Version: "4.9.6", Core: "20250708-1", Source: "Info.plist; TRIAGE.md:362-395"},
		},
	}
}

func profileView(profile account.VersionProfile) model.ProfileView {
	return model.ProfileView{
		Name:            profile.Name,
		ClientVersion:   profile.ClientVersion,
		CoreVersion:     profile.CoreVersion,
		ProtocolVersion: profile.ProtocolVersion,
		OAEPHash:        profile.OAEPHash,
		SigningMode:     profile.SigningMode,
		RequestShape:    requestShapeName(profile.RequestShape),
		Transport:       profile.Transport,
	}
}

func requestShapeName(shape apifacade.RequestShape) string {
	if shape == apifacade.ShapeLinux423 {
		return "linux423"
	}
	return "openajs"
}

// Logs serves GET /v1/lab/logs from the bounded ring.
func (v *LabView) Logs(query lablog.Query) model.LogsView {
	if v.cfg.LogRing == nil {
		return model.LogsView{Enabled: false, Reason: reasonLogsDisabled, Entries: []model.LogEntry{}, Dropped: 0}
	}
	entries, dropped := v.cfg.LogRing.Entries(query.Since, query.Level, query.Component, query.Limit)
	out := make([]model.LogEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, model.LogEntry{
			TS:         formatTime(entry.Time),
			Level:      entry.Level.String(),
			Component:  entry.Component,
			Msg:        entry.Message,
			AccountID:  entry.AccountID,
			NodeID:     entry.NodeID,
			ServerID:   entry.ServerID,
			ErrorClass: entry.ErrorClass,
			DurationMS: entry.DurationMS,
			Status:     entry.Status,
			Bytes:      entry.Bytes,
		})
	}
	return model.LogsView{Enabled: true, Entries: out, Dropped: dropped}
}

// Diagnostics serves GET /v1/lab/diagnostics: readiness, the state-file
// summary, build info and the same warnings as the overview.
func (v *LabView) Diagnostics() model.DiagnosticsView {
	health := map[string]string{
		"api":        "ok",
		"store":      "ok",
		"secrets":    "disabled",
		"certPin":    v.cfg.CertPin,
		"resin":      "disabled",
		"update":     "disabled",
		"controller": "disabled",
		"logRing":    "disabled",
		"pcap":       "unavailable",
	}
	if v.cfg.Secrets {
		health["secrets"] = "ok"
	}
	if v.cfg.Resin {
		health["resin"] = "enabled"
	}
	if v.cfg.UpdateCheck {
		health["update"] = "enabled"
	}
	if v.cfg.Controller != nil {
		health["controller"] = "running"
	}
	if v.cfg.LogRing != nil {
		health["logRing"] = "ok"
	}
	goVersion := v.cfg.GoVersion
	if goVersion == "" {
		goVersion = goruntime.Version()
	}
	return model.DiagnosticsView{
		Enabled:  true,
		Health:   health,
		State:    v.stateView(),
		Build:    model.LabBuildView{Version: v.cfg.Version, GoVersion: goVersion, Commit: v.cfg.Commit},
		Warnings: v.warnings(v.store.Accounts(), v.store.Nodes()),
	}
}

// stateView reports the snapshot path as a basename plus its size and mtime.
// A missing file is reported as zeros instead of an error: the daemon runs
// before the first save too.
func (v *LabView) stateView() model.LabStateView {
	view := model.LabStateView{}
	if v.cfg.StatePath == "" {
		return view
	}
	view.Path = filepath.Base(v.cfg.StatePath)
	info, err := os.Stat(v.cfg.StatePath)
	if err != nil {
		return view
	}
	view.LastSavedAt = formatTime(info.ModTime())
	view.SizeBytes = info.Size()
	return view
}

// Servers serves GET /v1/accounts/{id}/servers from the retained login
// result. Without one (fresh restart) the catalog is empty and stale:true
// rather than fabricated.
//
// The current apifacade.Server payload carries no marks[] or ServerLoad, so
// blockingMarks is always empty, fullLoad is always false, and memberOnly and
// latencyMs stay null: the view reports only what the daemon actually knows.
func (v *LabView) Servers(_ context.Context, accountID string) (model.ServersView, error) {
	if _, ok := v.store.Account(accountID); !ok {
		return model.ServersView{}, &store.Error{Kind: store.ErrNotFound, Message: fmt.Sprintf("account %q not found", accountID)}
	}
	view := model.ServersView{
		Enabled:   true,
		AccountID: accountID,
		Servers:   []model.ServerView{},
	}
	if v.cfg.Logins == nil {
		view.Enabled = false
		view.Reason = reasonLoginDisabled
		view.Stale = true
		return view, nil
	}
	result, ok := v.cfg.Logins.Result(accountID)
	if !ok {
		view.Stale = true
		view.Reason = "no retained login result; log in to refresh the catalog"
		return view, nil
	}
	view.FetchedAt = formatTime(result.FetchedAt)
	catalog := catalogServers(result.Servers)
	usableIndex := 0
	for _, entry := range catalog {
		usable := entry.server.OK()
		skipped := false
		if usable {
			if v.cfg.NodeLimit > 0 && usableIndex >= v.cfg.NodeLimit {
				skipped = true
			}
			usableIndex++
		}
		id := nodeID(accountID, entry.id)
		node, hasNode := v.store.Node(id)
		item := model.ServerView{
			ID:             entry.id,
			Title:          entry.server.Tit,
			Order:          strings.TrimSpace(entry.server.Ord),
			Region:         nodeRegion(entry.server),
			Host:           entry.server.Svr,
			Port:           entry.server.Port(),
			Status:         entry.server.Sts,
			NodeID:         id,
			Selected:       hasNode,
			SkippedByLimit: skipped,
			BlockingMarks:  []string{},
			HasPingSecret:  strings.TrimSpace(entry.server.Par.FV.PS) != "",
		}
		if hasNode {
			item.NodeState = node.State
		}
		view.Servers = append(view.Servers, item)
	}
	return view, nil
}

// Device serves GET /v1/accounts/{id}/device with hashed identifiers. The raw
// _uid/_os_did/cuid and the dim fingerprint stay out of the default response
// (DASHBOARD-GAPS §8.2); the guarded reveal path is not part of this slice.
func (v *LabView) Device(accountID string) (model.DeviceView, error) {
	if _, ok := v.store.Account(accountID); !ok {
		return model.DeviceView{}, &store.Error{Kind: store.ErrNotFound, Message: fmt.Sprintf("account %q not found", accountID)}
	}
	view := model.DeviceView{
		Enabled:   true,
		AccountID: accountID,
		Hashes:    map[string]string{},
		Claims:    map[string]string{},
	}
	identity, ok := v.store.AccountDevice(accountID)
	if !ok {
		return view, nil
	}
	view.Present = true
	view.Stable = true
	for key, value := range map[string]string{
		"uid":   identity.UID,
		"osdid": identity.OSDID,
		"cuid":  identity.CUID,
	} {
		if hashed := hashIdentifier(value); hashed != "" {
			view.Hashes[key] = hashed
		}
	}
	for key, value := range map[string]string{
		"cseid": identity.CSEID,
		"osp":   identity.OSP,
		"osd":   identity.OSD,
		"osv":   identity.OSV,
	} {
		if value = strings.TrimSpace(value); value != "" {
			view.Claims[key] = value
		}
	}
	return view, nil
}

// hashIdentifier returns sha256(value) truncated to 16 hex characters with a
// "sha256:" prefix, as the device contract specifies.
func hashIdentifier(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])[:16]
}

// redactSID keeps the first four characters and the length, never the full
// session id: "a1b2…(32)".
func redactSID(sid string) string {
	if sid == "" {
		return ""
	}
	prefix := sid
	if len(prefix) > 4 {
		prefix = prefix[:4]
	}
	return fmt.Sprintf("%s…(%d)", prefix, len(sid))
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// catalogServers returns every vendor server sorted by its stable id. The map
// key is the fallback when the server object carries no id, matching the node
// controller's keying rule.
func catalogServers(servers map[string]apifacade.Server) []serverRef {
	keys := make([]string, 0, len(servers))
	for key := range servers {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	out := make([]serverRef, 0, len(servers))
	seen := make(map[string]struct{}, len(servers))
	for _, key := range keys {
		server := servers[key]
		id := strings.TrimSpace(server.ID)
		if id == "" {
			id = strings.TrimSpace(key)
		}
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, serverRef{id: id, server: server})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

// FlagValues is the effective startup configuration the flags view reports.
// Secret values are accepted only so the builder can report presence; they are
// never copied into a FlagView.
type FlagValues struct {
	Listen            string
	LogLevel          string
	AccountProfile    string
	NodeLimit         string
	SnapshotInterval  string
	ControlCertSHA256 string
	UpdateRepo        string
	ResinURL          string
	SubscriptionName  string
	AdminToken        string
	MasterKey         string
	ResinAdminToken   string
	AllowInsecure     bool
	AccountLogin      bool
	AllowPlaintext    bool
}

// Flags builds the effective flag list. Non-secret keys expose value and
// provenance (env/default/flag); secret keys expose set/redacted only.
func Flags(values FlagValues) []model.FlagView {
	boolValue := func(set bool) string {
		if set {
			return "1"
		}
		return "0"
	}
	specs := []struct {
		key    string
		value  string
		def    string
		secret bool
	}{
		{"AJS_LISTEN", values.Listen, "127.0.0.1:2261", false},
		{"AJS_ADMIN_TOKEN", values.AdminToken, "", true},
		{"AJS_ALLOW_INSECURE", boolValue(values.AllowInsecure), "0", false},
		{"AJS_ALLOW_PLAINTEXT", boolValue(values.AllowPlaintext), "0", false},
		{"AJS_LOG_LEVEL", values.LogLevel, "info", false},
		{"AJS_ACCOUNT_LOGIN", boolValue(values.AccountLogin), "0", false},
		{"AJS_ACCOUNT_PROFILE", values.AccountProfile, "linux-4.2.3.0", false},
		{"AJS_NODE_LIMIT", values.NodeLimit, "1", false},
		{"AJS_CONTROL_CERT_SHA256", values.ControlCertSHA256, "", false},
		{"AJS_RESIN_URL", values.ResinURL, "http://127.0.0.1:2260", false},
		{"AJS_RESIN_ADMIN_TOKEN", values.ResinAdminToken, "", true},
		{"AJS_SUBSCRIPTION_NAME", values.SubscriptionName, DefaultSubscriptionName, false},
		{"AJS_MASTER_KEY", values.MasterKey, "", true},
		{"AJS_SNAPSHOT_INTERVAL", values.SnapshotInterval, "30s", false},
		{"AJS_UPDATE_REPO", values.UpdateRepo, "", false},
	}
	out := make([]model.FlagView, 0, len(specs))
	for _, spec := range specs {
		view := model.FlagView{Key: spec.key, Secret: spec.secret, RestartRequired: true}
		if spec.secret {
			set := spec.value != ""
			view.Set = &set
			view.Redacted = true
			view.Source = flagSource(spec.key, set)
			out = append(out, view)
			continue
		}
		view.Value = spec.value
		view.Source = flagSource(spec.key, spec.value != spec.def)
		out = append(out, view)
	}
	return out
}

// flagSource reports where a value came from. An environment variable wins;
// otherwise a value that differs from the default was supplied by a CLI flag.
func flagSource(key string, differsFromDefault bool) string {
	if strings.TrimSpace(os.Getenv(key)) != "" {
		return "env"
	}
	if differsFromDefault {
		return "flag"
	}
	return "default"
}
