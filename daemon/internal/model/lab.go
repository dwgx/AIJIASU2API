package model

// Lab telemetry contracts. Every type here is an observation: the Lab exposes
// state the daemon already holds or the result of one explicit probe, and adds
// no mutation. No field in this file may carry a credential, a full session
// id, a raw device id or a vendor ping secret (DASHBOARD-GAPS §6.3 and §8).

// LabWarning is one operator-facing explanation of a disabled mode or a risky
// configuration. Severity uses the audit vocabulary (info, warn, error).
type LabWarning struct {
	Code     string   `json:"code"`
	Severity Severity `json:"severity"`
	Message  string   `json:"message"`
}

// LabModes is the effective runtime mode block of GET /v1/lab/overview.
type LabModes struct {
	AccountLogin bool `json:"accountLogin"`
	Resin        bool `json:"resin"`
	UpdateCheck  bool `json:"updateCheck"`
	Secrets      bool `json:"secrets"`
	// CertPin is "pinned" when AJS_CONTROL_CERT_SHA256 is set and "default"
	// otherwise. It names the mode, never the fingerprint.
	CertPin string `json:"certPin"`
}

// LabCounts is the aggregate count block of GET /v1/lab/overview.
type LabCounts struct {
	Accounts  int `json:"accounts"`
	Nodes     int `json:"nodes"`
	LiveNodes int `json:"liveNodes"`
	Sessions  int `json:"sessions"`
	Gates     int `json:"gates"`
	Leases    int `json:"leases"`
}

// LabOverview is the GET /v1/lab/overview response.
type LabOverview struct {
	Enabled     bool         `json:"enabled"`
	Reason      string       `json:"reason,omitempty"`
	GeneratedAt string       `json:"generatedAt"`
	Version     string       `json:"version"`
	Commit      string       `json:"commit"`
	UptimeSec   int64        `json:"uptimeSec"`
	Modes       LabModes     `json:"modes"`
	Counts      LabCounts    `json:"counts"`
	Warnings    []LabWarning `json:"warnings"`
}

// LabDisabled is the body of a list endpoint whose dependency is absent.
// Returning 200 with an explanation (instead of a 500 or an unexplained empty
// array) is the contract: a disabled mode must say why.
type LabDisabled struct {
	Enabled bool   `json:"enabled"`
	Reason  string `json:"reason"`
}

// GateView is one running gate joined to its store node. Counters come from
// gate.State; State and Port are the store/runtime view.
type GateView struct {
	NodeID       string    `json:"nodeId"`
	AccountID    string    `json:"accountId"`
	State        NodeState `json:"state"`
	Port         int       `json:"port"`
	Active       int       `json:"active"`
	Limit        int       `json:"limit"`
	Accepted     uint64    `json:"accepted"`
	Rejected     uint64    `json:"rejected"`
	IngressBytes uint64    `json:"ingressBytes"`
	EgressBytes  uint64    `json:"egressBytes"`
	Draining     bool      `json:"draining"`
	StartedAt    string    `json:"startedAt"`
}

// FlowView is one finished gate flow joined to its node. TargetHost and
// TargetPort are the only target fields: the gate records no URL, headers or
// credentials, and SID is redacted to "first4…(len)".
type FlowView struct {
	ID         string `json:"id"`
	NodeID     string `json:"nodeId"`
	AccountID  string `json:"accountId"`
	SID        string `json:"sid,omitempty"`
	StartedAt  string `json:"startedAt"`
	DurationMs int64  `json:"durationMs"`
	Method     string `json:"method"`
	TargetHost string `json:"targetHost"`
	TargetPort int    `json:"targetPort"`
	Status     string `json:"status"`
	BytesIn    int64  `json:"bytesIn"`
	BytesOut   int64  `json:"bytesOut"`
	ErrorClass string `json:"errorClass,omitempty"`
}

// FlowsView is the GET /v1/lab/flows response. Dropped counts records evicted
// from the bounded per-gate rings since process start.
type FlowsView struct {
	Enabled bool       `json:"enabled"`
	Reason  string     `json:"reason,omitempty"`
	Flows   []FlowView `json:"flows"`
	Dropped uint64     `json:"dropped"`
}

// LabKeepalive reports the keepalive fields the daemon does not implement
// yet. The object is present so the UI can state "not supported" instead of
// silently omitting the capability.
type LabKeepalive struct {
	Supported bool    `json:"supported"`
	KA        *string `json:"ka"`
	KAT       *string `json:"kat"`
	KAPF      *string `json:"kapf"`
}

// SessionView is one authenticated FV session. SID is redacted to
// "first4…(len)"; the full value never leaves the process.
type SessionView struct {
	NodeID        string       `json:"nodeId"`
	AccountID     string       `json:"accountId"`
	ServerID      string       `json:"serverId"`
	SID           string       `json:"sid"`
	Healthy       bool         `json:"healthy"`
	AgeSec        int64        `json:"ageSec"`
	LastFlowAt    *string      `json:"lastFlowAt"`
	EgressClaimed string       `json:"egressClaimed"`
	Keepalive     LabKeepalive `json:"keepalive"`
}

// EgressView is the claimed-vs-verified egress record. The first slice only
// reads the vendor claim: verified stays empty, match stays null and
// checkedAt stays null until the guarded phase-2 check exists.
type EgressView struct {
	NodeID    string  `json:"nodeId"`
	Claimed   string  `json:"claimed"`
	Verified  string  `json:"verified"`
	Match     *bool   `json:"match"`
	Source    string  `json:"source"`
	CheckedAt *string `json:"checkedAt"`
}

// EgressCheckView is the POST /v1/lab/egress-check response. Claimed is the
// vendor claim read from the running session, Verified is the address observed
// by dialing the node's own gate, and Match is null until both values exist.
// A failed probe still answers this record with Error set: the attempt, its
// latency and checkedAt are facts even when no address was observed.
type EgressCheckView struct {
	NodeID    string `json:"nodeId"`
	Claimed   string `json:"claimed"`
	Verified  string `json:"verified"`
	Match     *bool  `json:"match"`
	LatencyMs int64  `json:"latencyMs"`
	CheckedAt string `json:"checkedAt"`
	Source    string `json:"source"`
	Error     string `json:"error,omitempty"`
}

// ProfileView is one supported FV version profile.
type ProfileView struct {
	Name            string `json:"name"`
	ClientVersion   string `json:"clientVersion"`
	CoreVersion     string `json:"coreVersion"`
	ProtocolVersion int    `json:"protocolVersion"`
	OAEPHash        string `json:"oaepHash"`
	SigningMode     string `json:"signingMode"`
	RequestShape    string `json:"requestShape"`
	Transport       string `json:"transport"`
	// Note carries a profile-specific caveat for the operator, e.g. that a
	// profile is statically derived and not live-tested. Empty for profiles
	// with no caveat.
	Note string `json:"note,omitempty"`
}

// ProfileSample is one shipped client version from the research matrix. It is
// a reference baseline, not a profile the daemon can speak.
type ProfileSample struct {
	Platform string `json:"platform"`
	Version  string `json:"version"`
	Core     string `json:"core"`
	Source   string `json:"source"`
}

// ProfilesView is the GET /v1/lab/profiles response.
type ProfilesView struct {
	Enabled   bool            `json:"enabled"`
	Reason    string          `json:"reason,omitempty"`
	Active    string          `json:"active"`
	Supported []ProfileView   `json:"supported"`
	Samples   []ProfileSample `json:"samples"`
}

// ServerView is one vendor server in the per-account catalog. Vsid is
// deliberately absent, and Par.FV.PS is reduced to HasPingSecret: the secret
// itself is never serialized.
type ServerView struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// Order is the vendor ord used by ?sort=order. It is catalog metadata,
	// not part of the response shape, so it is never serialized.
	Order          string    `json:"-"`
	Region         string    `json:"region"`
	Host           string    `json:"host"`
	Port           int       `json:"port"`
	Status         string    `json:"status"`
	NodeID         string    `json:"nodeId"`
	NodeState      NodeState `json:"nodeState"`
	Selected       bool      `json:"selected"`
	SkippedByLimit bool      `json:"skippedByLimit"`
	MemberOnly     *bool     `json:"memberOnly"`
	FullLoad       bool      `json:"fullLoad"`
	BlockingMarks  []string  `json:"blockingMarks"`
	HasPingSecret  bool      `json:"hasPingSecret"`
	LatencyMs      *int      `json:"latencyMs"`
}

// RegionCount is one entry of the catalog's per-region summary.
type RegionCount struct {
	Code  string `json:"code"`
	Count int    `json:"count"`
}

// CatalogGeoCity is one resolved city inside a province bucket, labelled with
// the vendor title prefix so the city filter can show what the operator sees.
type CatalogGeoCity struct {
	Code  string `json:"code"`
	Label string `json:"label"`
	Count int    `json:"count"`
}

// CatalogGeoProvince is the map's per-province aggregate over the rows that
// match q/region/status, before the province/city filter narrows the table.
type CatalogGeoProvince struct {
	Code      string           `json:"code"`
	Count     int              `json:"count"`
	Connected int              `json:"connected"`
	Cities    []CatalogGeoCity `json:"cities"`
}

// CatalogGeoRow names one running catalog row. CityCode is empty when the
// title does not resolve.
type CatalogGeoRow struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	NodeState    NodeState `json:"nodeState"`
	CityCode     string    `json:"cityCode,omitempty"`
	ProvinceCode string    `json:"provinceCode,omitempty"`
}

// CatalogGeo is the server-side geography summary for the catalog page. It
// lets the dashboard draw the map and the city filter without downloading the
// whole retained catalog: Provinces counts the rows that match the search and
// region/status filters, while Current and Live describe every running row in
// the account regardless of those filters.
type CatalogGeo struct {
	Total     int                  `json:"total"`
	Unknown   int                  `json:"unknown"`
	Provinces []CatalogGeoProvince `json:"provinces"`
	Current   *CatalogGeoRow       `json:"current"`
	Live      []CatalogGeoRow      `json:"live"`
}

// ServersView is the GET /v1/accounts/{id}/servers response. Stale is true
// when the daemon has no in-memory login result (for example after a
// restart); the catalog is then empty rather than invented.
//
// Servers is one page of the filtered catalog. Total counts every server that
// matches the filters, Regions summarizes those same rows, and NextOffset is
// the offset to request next (offset+len(Servers), equal to Total on the last
// page). HasMore is true when another page exists.
//
// SnapshotID is the same identity the selection/connect guard compares; it is
// empty when no catalog connector is configured. Geo is the map aggregate and
// is always present (empty when the catalog is empty).
type ServersView struct {
	Enabled    bool          `json:"enabled"`
	Reason     string        `json:"reason,omitempty"`
	AccountID  string        `json:"accountId"`
	SnapshotID string        `json:"snapshotId,omitempty"`
	FetchedAt  string        `json:"fetchedAt"`
	Stale      bool          `json:"stale"`
	Total      int           `json:"total"`
	Offset     int           `json:"offset"`
	Limit      int           `json:"limit"`
	NextOffset int           `json:"nextOffset"`
	HasMore    bool          `json:"hasMore"`
	Regions    []RegionCount `json:"regions"`
	Geo        *CatalogGeo   `json:"geo,omitempty"`
	Servers    []ServerView  `json:"servers"`
}

// DeviceView is the GET /v1/accounts/{id}/device response. Hashes holds
// "sha256:"-prefixed prefixes of uid, osdid and cuid. Claims holds the
// non-fingerprint vendor claims; dim is withheld until the guarded raw-reveal
// path exists (DASHBOARD-GAPS §8.2).
type DeviceView struct {
	Enabled      bool              `json:"enabled"`
	Reason       string            `json:"reason,omitempty"`
	AccountID    string            `json:"accountId"`
	Present      bool              `json:"present"`
	Hashes       map[string]string `json:"hashes"`
	Claims       map[string]string `json:"claims"`
	Stable       bool              `json:"stable"`
	FirstSeenAt  *string           `json:"firstSeenAt"`
	RawAvailable bool              `json:"rawAvailable"`
}

// FlagView is one effective configuration flag. A secret flag carries Set and
// Redacted and never a Value.
type FlagView struct {
	Key             string `json:"key"`
	Value           string `json:"value,omitempty"`
	Set             *bool  `json:"set,omitempty"`
	Source          string `json:"source,omitempty"`
	Secret          bool   `json:"secret"`
	Redacted        bool   `json:"redacted,omitempty"`
	RestartRequired bool   `json:"restartRequired"`
}

// FlagsView is the GET /v1/lab/flags response.
type FlagsView struct {
	Enabled bool       `json:"enabled"`
	Reason  string     `json:"reason,omitempty"`
	Flags   []FlagView `json:"flags"`
}

// LogEntry is one redacted runtime log line. Only allowlisted attributes are
// populated (DASHBOARD-GAPS §8.4).
type LogEntry struct {
	TS         string `json:"ts"`
	Level      string `json:"level"`
	Component  string `json:"component"`
	Msg        string `json:"msg"`
	AccountID  string `json:"accountId,omitempty"`
	NodeID     string `json:"nodeId,omitempty"`
	ServerID   string `json:"serverId,omitempty"`
	ErrorClass string `json:"errorClass,omitempty"`
	DurationMS *int64 `json:"durationMs,omitempty"`
	Status     *int   `json:"status,omitempty"`
	Bytes      *int64 `json:"bytes,omitempty"`
}

// LogsView is the GET /v1/lab/logs response. Dropped counts entries evicted
// from the bounded ring since process start.
type LogsView struct {
	Enabled bool       `json:"enabled"`
	Reason  string     `json:"reason,omitempty"`
	Entries []LogEntry `json:"entries"`
	Dropped uint64     `json:"dropped"`
}

// LabStateView is the state-snapshot block of the diagnostics response. Path
// is a basename, never a full home directory.
type LabStateView struct {
	Path        string `json:"path"`
	LastSavedAt string `json:"lastSavedAt"`
	SizeBytes   int64  `json:"sizeBytes"`
}

// LabBuildView is the build block of the diagnostics response.
type LabBuildView struct {
	Version   string `json:"version"`
	GoVersion string `json:"goVersion"`
	Commit    string `json:"commit"`
}

// DiagnosticsView is the GET /v1/lab/diagnostics response.
type DiagnosticsView struct {
	Enabled  bool              `json:"enabled"`
	Reason   string            `json:"reason,omitempty"`
	Health   map[string]string `json:"health"`
	State    LabStateView      `json:"state"`
	Build    LabBuildView      `json:"build"`
	Warnings []LabWarning      `json:"warnings"`
}
