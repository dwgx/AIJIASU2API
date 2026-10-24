// Package model defines the JSON wire contract shared with the dashboard.
//
// Field names and types mirror dashboard/src/lib/types.ts exactly. Timestamps
// are RFC 3339 strings, byte counts are integers, and absent optional values are
// encoded as JSON null. Credentials are deliberately absent from every response
// type.
package model

// Platform is the account platform vocabulary from types.ts.
type Platform string

const (
	PlatformAndroid Platform = "android"
	PlatformIOS     Platform = "ios"
	PlatformWindows Platform = "windows"
	PlatformLinux   Platform = "linux"
)

// AccountState is the dashboard account state vocabulary.
type AccountState string

const (
	AccountActive     AccountState = "active"
	AccountPaused     AccountState = "paused"
	AccountAuthFailed AccountState = "auth_failed"
	AccountExpired    AccountState = "expired"
)

// NodeState is the dashboard node state vocabulary (uppercase by contract).
type NodeState string

const (
	NodeLive     NodeState = "LIVE"
	NodeReady    NodeState = "READY"
	NodeStarting NodeState = "STARTING"
	NodeDraining NodeState = "DRAINING"
	NodeDown     NodeState = "DOWN"
	// NodeStale marks a persisted node whose gate did not survive the last
	// process: the row exists, but no listener is bound and no lease is open.
	// Startup recovery writes it; a later login sync moves the node back to
	// LIVE (or DRAINING). It never claims traffic, so the console shows it
	// without the LIVE badge.
	NodeStale NodeState = "STALE"
)

// ProxyProtocol is the protocol exposed by a proxy node.
type ProxyProtocol string

const (
	ProtocolSOCKS5 ProxyProtocol = "socks5"
	ProtocolHTTP   ProxyProtocol = "http"
)

// Severity is the audit event severity.
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarn     Severity = "warn"
	SeverityError    Severity = "error"
	SeverityCritical Severity = "critical"
)

// ThemeName is the dashboard theme.
type ThemeName string

const (
	ThemeDark  ThemeName = "dark"
	ThemeLight ThemeName = "light"
)

// Transport is the FV transport selected in settings.
type Transport string

const (
	TransportHTTPUpgrade Transport = "httpupgrade"
	TransportRawTCP      Transport = "rawtcp"
)

// MetricRange is the metrics window selector.
type MetricRange string

const (
	Range24h MetricRange = "24h"
	Range7d  MetricRange = "7d"
)

// RequestSource identifies where a request log row came from.
type RequestSource string

const (
	SourceProxy       RequestSource = "proxy"
	SourceMutationLab RequestSource = "mutation-lab"
	SourceGate        RequestSource = "gate"
)

// Account mirrors the dashboard Account interface.
type Account struct {
	ID            string       `json:"id"`
	Label         string       `json:"label"`
	Username      string       `json:"username"`
	Platform      Platform     `json:"platform"`
	State         AccountState `json:"state"`
	Concurrency   int          `json:"concurrency"`
	Regions       []string     `json:"regions"`
	NodeCount     int          `json:"nodeCount"`
	LiveNodeCount int          `json:"liveNodeCount"`
	UsageBytes    int64        `json:"usageBytes"`
	// TrafficBudgetBytes is the monthly gate-enforced traffic budget. Nil
	// means unlimited; zero is a real budget that refuses every byte.
	TrafficBudgetBytes *int64 `json:"trafficBudgetBytes"`
	ExpiresAt          string `json:"expiresAt"`
	LastLoginAt        string `json:"lastLoginAt"`
	CreatedAt          string `json:"createdAt"`
}

// ProxyNode mirrors the dashboard ProxyNode interface.
type ProxyNode struct {
	ID           string        `json:"id"`
	AccountID    string        `json:"accountId"`
	AccountLabel string        `json:"accountLabel"`
	Label        string        `json:"label"`
	Region       string        `json:"region"`
	Country      string        `json:"country"`
	City         string        `json:"city"`
	State        NodeState     `json:"state"`
	LatencyMs    int           `json:"latencyMs"`
	SuccessRate  float64       `json:"successRate"`
	InUse        int           `json:"inUse"`
	Capacity     int           `json:"capacity"`
	EgressIP     string        `json:"egressIp"`
	Protocol     ProxyProtocol `json:"protocol"`
	GatePort     int           `json:"gatePort"`
	LastProbeAt  string        `json:"lastProbeAt"`
	// ServerID is the vendor server this node was opened for. It is empty for
	// the seeded skeleton nodes and set by the runtime node controller, which
	// keys the gate on (account, server).
	ServerID string `json:"serverId,omitempty"`
}

// Lease mirrors the dashboard Lease interface.
type Lease struct {
	ID           string        `json:"id"`
	UserID       string        `json:"userId"`
	UserName     string        `json:"userName"`
	AccountID    string        `json:"accountId"`
	AccountLabel string        `json:"accountLabel"`
	NodeID       string        `json:"nodeId"`
	NodeLabel    string        `json:"nodeLabel"`
	Region       string        `json:"region"`
	EgressIP     string        `json:"egressIp"`
	Protocol     ProxyProtocol `json:"protocol"`
	GatePort     int           `json:"gatePort"`
	TTLSeconds   int           `json:"ttlSeconds"`
	ExpiresAt    string        `json:"expiresAt"`
	Requests     int64         `json:"requests"`
	Bytes        int64         `json:"bytes"`
	Sticky       bool          `json:"sticky"`
	CreatedAt    string        `json:"createdAt"`
}

// PanelUser mirrors the dashboard PanelUser interface. QuotaBytesMonth is null
// when the user has no monthly quota.
type PanelUser struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Platform        string   `json:"platform"`
	StickyTTL       string   `json:"stickyTtl"`
	QuotaBytesMonth *int64   `json:"quotaBytesMonth"`
	UsedBytes       int64    `json:"usedBytes"`
	Enabled         bool     `json:"enabled"`
	Allowlist       []string `json:"allowlist"`
	CreatedAt       string   `json:"createdAt"`
}

// APIKey mirrors the dashboard ApiKey interface. LastUsedAt and RevokedAt are
// null when unset.
type APIKey struct {
	ID         string  `json:"id"`
	UserID     string  `json:"userId"`
	UserName   string  `json:"userName"`
	Label      string  `json:"label"`
	Prefix     string  `json:"prefix"`
	CreatedAt  string  `json:"createdAt"`
	LastUsedAt *string `json:"lastUsedAt"`
	RevokedAt  *string `json:"revokedAt"`
}

// Scope is one permission carried by a scoped API key. The vocabulary is the
// OpenAPI Scope enum; accounts:write is part of the daemon contract even
// though the first published enum only listed accounts:read. The platform
// reads (usage, renewal, health) are separate scopes so a monitoring key never
// needs an account or settings grant. events:read reaches the replay ring and
// the SSE stream and is refused to keys restricted by accountIds/regions,
// because the ring is global and an event is not attributed to one account.
type Scope string

const (
	ScopeAccountsRead  Scope = "accounts:read"
	ScopeAccountsWrite Scope = "accounts:write"
	ScopeCatalogRead   Scope = "catalog:read"
	ScopeCatalogWrite  Scope = "catalog:write"
	ScopeLeasesRead    Scope = "leases:read"
	ScopeLeasesWrite   Scope = "leases:write"
	ScopeRequestsRead  Scope = "requests:read"
	ScopeAuditRead     Scope = "audit:read"
	ScopeMetricsRead   Scope = "metrics:read"
	ScopeLabRead       Scope = "lab:read"
	ScopeSettingsRead  Scope = "settings:read"
	ScopeSettingsWrite Scope = "settings:write"
	ScopeUsageRead     Scope = "usage:read"
	ScopeRenewalRead   Scope = "renewal:read"
	ScopeHealthRead    Scope = "health:read"
	ScopeEventsRead    Scope = "events:read"
	ScopeAdmin         Scope = "admin"
)

// DefaultScopedKeyRateLimitPerMin is the per-minute request budget assigned to
// a new scoped key when the create request omits rateLimitPerMin. Zero means
// unlimited and must be requested explicitly.
const DefaultScopedKeyRateLimitPerMin = 60

// ScopedKey is a least-privilege API key. AccountIDs and Regions are
// restrictions on the accounts the key may read; empty means every account or
// every region. The secret is never part of this type.
type ScopedKey struct {
	ID              string   `json:"id"`
	UserID          string   `json:"userId"`
	UserName        string   `json:"userName"`
	Label           string   `json:"label"`
	Prefix          string   `json:"prefix"`
	Scopes          []Scope  `json:"scopes"`
	AccountIDs      []string `json:"accountIds"`
	Regions         []string `json:"regions"`
	RateLimitPerMin int      `json:"rateLimitPerMin"`
	CreatedAt       string   `json:"createdAt"`
	ExpiresAt       *string  `json:"expiresAt"`
	LastUsedAt      *string  `json:"lastUsedAt"`
	RevokedAt       *string  `json:"revokedAt"`
}

// ScopedKeyCreateRequest is the POST /v1/scoped-keys body. ExpiresAt is an
// RFC 3339 timestamp; empty means the key does not expire. RateLimitPerMin is
// a pointer so an omitted field can take the default while an explicit 0
// selects unlimited.
type ScopedKeyCreateRequest struct {
	UserID          string   `json:"userId"`
	Label           string   `json:"label"`
	Scopes          []Scope  `json:"scopes"`
	AccountIDs      []string `json:"accountIds"`
	Regions         []string `json:"regions"`
	RateLimitPerMin *int     `json:"rateLimitPerMin"`
	ExpiresAt       string   `json:"expiresAt"`
}

// ScopedKeyCreated is the one-time create response. Secret is returned exactly
// once and only its SHA-256 is persisted.
type ScopedKeyCreated struct {
	Key    ScopedKey `json:"key"`
	Secret string    `json:"secret"`
}

// WebhookEndpoint is one persisted outbound-webhook receiver. The signing
// secret is deliberately absent: it is sealed through secret.Store (or kept in
// process memory when no master key is configured) and returned only by the
// create call.
type WebhookEndpoint struct {
	ID          string   `json:"id"`
	URL         string   `json:"url"`
	Events      []string `json:"events,omitempty"`
	Enabled     bool     `json:"enabled"`
	Description string   `json:"description,omitempty"`
	CreatedAt   string   `json:"createdAt"`
}

// WebhookEndpointInput is the POST /v1/webhooks body. An empty Events list
// subscribes to every event; an absent Enabled defaults to true.
type WebhookEndpointInput struct {
	URL         string   `json:"url"`
	Events      []string `json:"events,omitempty"`
	Description string   `json:"description,omitempty"`
	Enabled     *bool    `json:"enabled,omitempty"`
}

// WebhookDelivery is one delivery attempt persisted in the state snapshot. It
// is the restart-safe counterpart of webhook.Delivery: the dispatcher's ring
// stays in memory, this row is what survives a snapshot/reload cycle. Error
// carries only a status line or transport message — never a payload, signing
// secret or header value.
type WebhookDelivery struct {
	ID         string `json:"id"`
	EndpointID string `json:"endpointId"`
	Event      string `json:"event"`
	Attempt    int    `json:"attempt"`
	Status     string `json:"status"`
	HTTPStatus int    `json:"httpStatus,omitempty"`
	Error      string `json:"error,omitempty"`
	At         string `json:"at"`
	DurationMs int64  `json:"durationMs"`
}

// RequestLog mirrors the dashboard RequestLog interface.
type RequestLog struct {
	ID           string        `json:"id"`
	TS           string        `json:"ts"`
	UserID       string        `json:"userId"`
	UserName     string        `json:"userName"`
	AccountLabel string        `json:"accountLabel"`
	NodeLabel    string        `json:"nodeLabel"`
	Region       string        `json:"region"`
	Method       string        `json:"method"`
	Target       string        `json:"target"`
	Status       int           `json:"status"`
	Bytes        int64         `json:"bytes"`
	LatencyMs    int           `json:"latencyMs"`
	Source       RequestSource `json:"source"`
}

// AuditEvent mirrors the dashboard AuditEvent interface.
type AuditEvent struct {
	ID       string   `json:"id"`
	TS       string   `json:"ts"`
	Severity Severity `json:"severity"`
	Actor    string   `json:"actor"`
	Action   string   `json:"action"`
	Target   string   `json:"target"`
	Detail   string   `json:"detail"`
	// Meta is the small, credential-free key/value block the catalog writes
	// (server ids, node id, gate port, duration, result). Older events have
	// nil, which omitempty keeps out of the wire shape.
	Meta map[string]string `json:"meta,omitempty"`
}

// TrafficPoint is one ingress/egress sample.
type TrafficPoint struct {
	T       string `json:"t"`
	Ingress int64  `json:"ingress"`
	Egress  int64  `json:"egress"`
}

// AccountHealth is the per-account health row embedded in Metrics.
type AccountHealth struct {
	AccountID string       `json:"accountId"`
	Label     string       `json:"label"`
	Score     int          `json:"score"`
	State     AccountState `json:"state"`
}

// MetricsTotals is the aggregate block embedded in Metrics.
type MetricsTotals struct {
	Requests     int64 `json:"requests"`
	EgressBytes  int64 `json:"egressBytes"`
	IngressBytes int64 `json:"ingressBytes"`
	P95LatencyMs int   `json:"p95LatencyMs"`
}

// Metrics mirrors the dashboard Metrics interface.
type Metrics struct {
	Range         MetricRange     `json:"range"`
	Series        []TrafficPoint  `json:"series"`
	Totals        MetricsTotals   `json:"totals"`
	AccountHealth []AccountHealth `json:"accountHealth"`
	// TelemetryAvailable reports whether Series and Totals come from a real
	// traffic meter. The daemon has none yet, so both are empty/zero and the
	// dashboard can label the chart instead of drawing a synthetic curve.
	TelemetryAvailable bool   `json:"telemetryAvailable"`
	GeneratedAt        string `json:"generatedAt"`
}

// PanelSettings mirrors the dashboard PanelSettings interface.
type PanelSettings struct {
	Transport              Transport `json:"transport"`
	HealthIntervalSec      int       `json:"healthIntervalSec"`
	LeaseTTL               string    `json:"leaseTtl"`
	SubscriptionRefreshSec int       `json:"subscriptionRefreshSec"`
	Theme                  ThemeName `json:"theme"`
}

// CreateAccountInput mirrors the dashboard CreateAccountInput interface. The
// password is validated and then discarded: the skeleton stores no credential.
type CreateAccountInput struct {
	Label       string   `json:"label"`
	Username    string   `json:"username"`
	Password    string   `json:"password"`
	Platform    Platform `json:"platform"`
	Concurrency int      `json:"concurrency"`
	Regions     []string `json:"regions"`
}

// CreateKeyInput mirrors the dashboard CreateKeyInput interface.
type CreateKeyInput struct {
	UserID string `json:"userId"`
	Label  string `json:"label"`
}

// CreateKeyResult mirrors the dashboard CreateKeyResult interface. Secret is
// returned exactly once and is never persisted in plaintext.
type CreateKeyResult struct {
	Key    APIKey `json:"key"`
	Secret string `json:"secret"`
}

// MutationLabInput mirrors the dashboard MutationLabInput interface.
type MutationLabInput struct {
	Method string `json:"method"`
	Target string `json:"target"`
	NodeID string `json:"nodeId"`
	Body   string `json:"body"`
}

// CreateUserInput is the POST /v1/users body. Null quota means unlimited.
type CreateUserInput struct {
	Name            string   `json:"name"`
	Platform        string   `json:"platform"`
	StickyTTL       string   `json:"stickyTtl"`
	QuotaBytesMonth *int64   `json:"quotaBytesMonth"`
	Allowlist       []string `json:"allowlist"`
}

// AccountPatch is the partial body accepted by PATCH /v1/accounts/{id}.
// TrafficBudgetSet distinguishes an absent budget field from an explicit JSON
// null: absent leaves the budget alone, null clears it (unlimited).
type AccountPatch struct {
	Label              *string       `json:"label"`
	Username           *string       `json:"username"`
	Platform           *Platform     `json:"platform"`
	State              *AccountState `json:"state"`
	Concurrency        *int          `json:"concurrency"`
	Regions            *[]string     `json:"regions"`
	ExpiresAt          *string       `json:"expiresAt"`
	TrafficBudgetBytes *int64        `json:"trafficBudgetBytes"`
	TrafficBudgetSet   bool          `json:"-"`
}

// UserPatch is the normalised partial update used by the store. QuotaSet
// distinguishes an absent quota field from an explicit JSON null.
type UserPatch struct {
	Platform        *string
	StickyTTL       *string
	QuotaBytesMonth *int64
	QuotaSet        bool
	Allowlist       *[]string
}

// SettingsPatch is the partial body accepted by PATCH /v1/settings.
type SettingsPatch struct {
	Transport              *Transport `json:"transport"`
	HealthIntervalSec      *int       `json:"healthIntervalSec"`
	LeaseTTL               *string    `json:"leaseTtl"`
	SubscriptionRefreshSec *int       `json:"subscriptionRefreshSec"`
	Theme                  *ThemeName `json:"theme"`
}

// Topology is the GET /v1/topology response: a rendered node-to-tag map plus
// the platform names currently known to the panel.
type Topology struct {
	Subscription string            `json:"subscription"`
	Nodes        map[string]string `json:"nodes"`
	Platforms    []string          `json:"platforms"`
	GeneratedAt  string            `json:"generatedAt"`
}
