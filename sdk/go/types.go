// Package ajs is a dependency-free Go client for the ajs-accountd admin API.
//
// Every type in this file mirrors a schema in docs/openapi.json. Timestamps are
// kept as the RFC 3339 strings the daemon sends (empty when unset) so a client
// never invents a zero time.
package ajs

import "encoding/json"

// AccountState is the account lifecycle state.
type AccountState string

const (
	AccountActive     AccountState = "active"
	AccountPaused     AccountState = "paused"
	AccountAuthFailed AccountState = "auth_failed"
	AccountExpired    AccountState = "expired"
)

// Platform is the vendor platform an account belongs to.
type Platform string

const (
	PlatformAndroid Platform = "android"
	PlatformIOS     Platform = "ios"
	PlatformWindows Platform = "windows"
	PlatformLinux   Platform = "linux"
)

// ProxyProtocol is the gate protocol a lease exposes.
type ProxyProtocol string

const (
	ProxySOCKS5 ProxyProtocol = "socks5"
	ProxyHTTP   ProxyProtocol = "http"
)

// NodeState is the runtime state of one proxy node.
type NodeState string

const (
	NodeLive     NodeState = "LIVE"
	NodeReady    NodeState = "READY"
	NodeStarting NodeState = "STARTING"
	NodeDraining NodeState = "DRAINING"
	NodeDown     NodeState = "DOWN"
	NodeStale    NodeState = "STALE"
)

// OperationState is the lifecycle of one async operation.
type OperationState string

const (
	OperationQueued    OperationState = "queued"
	OperationRunning   OperationState = "running"
	OperationSucceeded OperationState = "succeeded"
	OperationFailed    OperationState = "failed"
	OperationCanceled  OperationState = "canceled"
)

// Terminal reports whether no further transition can happen.
func (s OperationState) Terminal() bool {
	switch s {
	case OperationSucceeded, OperationFailed, OperationCanceled:
		return true
	default:
		return false
	}
}

// LeaseStatus is the lease lifecycle state.
type LeaseStatus string

const (
	LeaseActive  LeaseStatus = "active"
	LeaseExpired LeaseStatus = "expired"
	LeaseRevoked LeaseStatus = "revoked"
)

// Account is one stored vendor account. Secrets are never returned.
type Account struct {
	ID                 string       `json:"id"`
	Label              string       `json:"label"`
	Username           string       `json:"username"`
	Platform           Platform     `json:"platform"`
	State              AccountState `json:"state"`
	Concurrency        int          `json:"concurrency"`
	UseCount           int          `json:"useCount"`
	EffectiveSlots     int          `json:"effectiveSlots"`
	Regions            []string     `json:"regions"`
	NodeCount          int          `json:"nodeCount"`
	LiveNodeCount      int          `json:"liveNodeCount"`
	UsageBytes         int64        `json:"usageBytes"`
	TrafficBudgetBytes *int64       `json:"trafficBudgetBytes"`
	ExpiresAt          string       `json:"expiresAt"`
	LastLoginAt        string       `json:"lastLoginAt"`
	CreatedAt          string       `json:"createdAt"`
}

// CreateAccountInput is the POST /v1/accounts body. Password is write-only.
type CreateAccountInput struct {
	Label       string   `json:"label"`
	Username    string   `json:"username"`
	Password    string   `json:"password"`
	Platform    Platform `json:"platform"`
	Concurrency int      `json:"concurrency"`
	Regions     []string `json:"regions,omitempty"`
}

// NullableInt64 marshals either an integer or JSON null. A nil *NullableInt64
// in a patch struct means "leave unchanged"; NullInt64() clears a budget.
type NullableInt64 struct {
	Value int64
	Null  bool
}

// Int64Budget returns a set nullable budget.
func Int64Budget(v int64) *NullableInt64 { return &NullableInt64{Value: v} }

// NullInt64 returns an explicit JSON null (clear the budget).
func NullInt64() *NullableInt64 { return &NullableInt64{Null: true} }

// MarshalJSON implements json.Marshaler.
func (n NullableInt64) MarshalJSON() ([]byte, error) {
	if n.Null {
		return []byte("null"), nil
	}
	return json.Marshal(n.Value)
}

// AccountPatch is a partial account update. Nil fields are omitted; use
// NullInt64 to clear TrafficBudgetBytes.
type AccountPatch struct {
	Label              *string        `json:"label,omitempty"`
	Username           *string        `json:"username,omitempty"`
	Platform           *Platform      `json:"platform,omitempty"`
	State              *AccountState  `json:"state,omitempty"`
	Concurrency        *int           `json:"concurrency,omitempty"`
	Regions            *[]string      `json:"regions,omitempty"`
	ExpiresAt          *string        `json:"expiresAt,omitempty"`
	TrafficBudgetBytes *NullableInt64 `json:"trafficBudgetBytes,omitempty"`
}

// RegionCount is one catalog region aggregate.
type RegionCount struct {
	Code  string `json:"code"`
	Count int    `json:"count"`
}

// ServerView is one retained vendor catalog row.
type ServerView struct {
	ID             string    `json:"id"`
	Title          string    `json:"title"`
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

// ServersView is one page of the retained vendor catalog.
type ServersView struct {
	Enabled    bool          `json:"enabled"`
	Reason     string        `json:"reason,omitempty"`
	AccountID  string        `json:"accountId"`
	FetchedAt  string        `json:"fetchedAt"`
	Stale      bool          `json:"stale"`
	Total      int           `json:"total"`
	Offset     int           `json:"offset"`
	Limit      int           `json:"limit"`
	NextOffset int           `json:"nextOffset"`
	HasMore    bool          `json:"hasMore"`
	Regions    []RegionCount `json:"regions"`
	Servers    []ServerView  `json:"servers"`
}

// OperationTarget names what to switch to. At most one form is used.
type OperationTarget struct {
	ServerID string `json:"serverId,omitempty"`
	NodeID   string `json:"nodeId,omitempty"`
	Region   string `json:"region,omitempty"`
	Strategy string `json:"strategy,omitempty"`
}

// SelectNodeRequest is the POST /v1/accounts/{id}/actions/select-node body.
// DrainSeconds, TimeoutSeconds and Rollback are pointers because zero is a
// meaningful value and the server applies its own defaults when they are nil.
type SelectNodeRequest struct {
	Target         *OperationTarget `json:"target,omitempty"`
	DrainSeconds   *int             `json:"drainSeconds,omitempty"`
	TimeoutSeconds *int             `json:"timeoutSeconds,omitempty"`
	IfCurrent      string           `json:"ifCurrent,omitempty"`
	Rollback       *bool            `json:"rollback,omitempty"`
	Wait           bool             `json:"wait,omitempty"`
}

// SelectNodeResult is the terminal result of a select-node operation and the
// body of the idempotent 200 no-op.
type SelectNodeResult struct {
	Changed           bool     `json:"changed"`
	AccountID         string   `json:"accountId"`
	NodeID            string   `json:"nodeId,omitempty"`
	ServerID          string   `json:"serverId"`
	GatePort          int      `json:"gatePort,omitempty"`
	EgressIP          string   `json:"egressIp,omitempty"`
	SessionHealthy    bool     `json:"sessionHealthy"`
	PreviousNodeID    string   `json:"previousNodeId,omitempty"`
	RollbackAttempted bool     `json:"rollbackAttempted,omitempty"`
	Rollback          string   `json:"rollback,omitempty"`
	DurationMs        int64    `json:"durationMs,omitempty"`
	Warnings          []string `json:"warnings,omitempty"`
}

// OperationStep is one timed executor boundary.
type OperationStep struct {
	Name       string `json:"name"`
	State      string `json:"state"`
	DurationMs int64  `json:"durationMs"`
}

// OperationError is the client-safe failure recorded on an operation.
type OperationError struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Details map[string]string `json:"details,omitempty"`
}

// Operation is one durable async transition.
type Operation struct {
	ID          string            `json:"id"`
	Type        string            `json:"type"`
	State       OperationState    `json:"state"`
	AccountID   string            `json:"accountId"`
	Target      OperationTarget   `json:"target"`
	RequestedBy string            `json:"requestedBy"`
	CreatedAt   string            `json:"createdAt"`
	StartedAt   string            `json:"startedAt"`
	FinishedAt  string            `json:"finishedAt"`
	Steps       []OperationStep   `json:"steps"`
	Result      *SelectNodeResult `json:"result"`
	Error       *OperationError   `json:"error"`
}

// Lease is one idempotent assignment of a free node to a panel user.
type Lease struct {
	ID             string        `json:"id"`
	UserID         string        `json:"userId"`
	UserName       string        `json:"userName"`
	AccountID      string        `json:"accountId"`
	AccountLabel   string        `json:"accountLabel"`
	Platform       Platform      `json:"platform"`
	NodeID         string        `json:"nodeId"`
	NodeLabel      string        `json:"nodeLabel"`
	Region         string        `json:"region"`
	EgressIP       string        `json:"egressIp"`
	Protocol       ProxyProtocol `json:"protocol"`
	GatePort       int           `json:"gatePort"`
	TTLSeconds     int           `json:"ttlSeconds"`
	ExpiresAt      string        `json:"expiresAt"`
	Requests       int64         `json:"requests"`
	Bytes          int64         `json:"bytes"`
	Sticky         bool          `json:"sticky"`
	Status         LeaseStatus   `json:"status"`
	IdempotencyKey string        `json:"idempotencyKey"`
	CreatedAt      string        `json:"createdAt"`
}

// CreateLeaseInput is the POST /v1/leases body. IdempotencyKey is optional;
// CreateLease fills both the body field and the header when it is empty.
type CreateLeaseInput struct {
	UserID         string   `json:"userId"`
	Platform       Platform `json:"platform"`
	AccountID      string   `json:"accountId,omitempty"`
	TTLSeconds     int      `json:"ttlSeconds,omitempty"`
	IdempotencyKey string   `json:"idempotencyKey,omitempty"`
}

// CreateLeaseResult is the POST /v1/leases response.
type CreateLeaseResult struct {
	Lease    Lease  `json:"lease"`
	Warning  string `json:"warning,omitempty"`
	Replayed bool   `json:"replayed,omitempty"`
}

// RevokeLeaseInput is the DELETE /v1/leases/{id} body; Confirm must be true.
type RevokeLeaseInput struct {
	Confirm bool `json:"confirm"`
}

// UsageBucket is one UTC day of usage.
type UsageBucket struct {
	Day          string `json:"day"`
	Requests     int    `json:"requests"`
	Bytes        int64  `json:"bytes"`
	Errors       int    `json:"errors"`
	AvgLatencyMs int    `json:"avgLatencyMs"`
}

// UsageAccount is the per-account usage roll-up.
type UsageAccount struct {
	Account            string        `json:"account"`
	Requests           int           `json:"requests"`
	Bytes              int64         `json:"bytes"`
	Errors             int           `json:"errors"`
	AvgLatencyMs       int           `json:"avgLatencyMs"`
	Buckets            []UsageBucket `json:"buckets"`
	TrafficBudgetBytes *int64        `json:"trafficBudgetBytes"`
	UsedBytes          int64         `json:"usedBytes"`
	RemainingBytes     *int64        `json:"remainingBytes"`
}

// UsageUser is the per-user usage roll-up.
type UsageUser struct {
	UserID          string `json:"userId"`
	UserName        string `json:"userName"`
	Requests        int    `json:"requests"`
	Bytes           int64  `json:"bytes"`
	QuotaBytesMonth *int64 `json:"quotaBytesMonth"`
	UsedBytes       int64  `json:"usedBytes"`
}

// UsageTarget is one host-only target label.
type UsageTarget struct {
	Target   string `json:"target"`
	Requests int    `json:"requests"`
	Bytes    int64  `json:"bytes"`
}

// UsageSummary is the GET /v1/usage response.
type UsageSummary struct {
	From          string         `json:"from"`
	To            string         `json:"to"`
	Requests      int            `json:"requests"`
	Bytes         int64          `json:"bytes"`
	Errors        int            `json:"errors"`
	Accounts      []UsageAccount `json:"accounts"`
	Users         []UsageUser    `json:"users"`
	Days          []UsageBucket  `json:"days"`
	TopTargets    []UsageTarget  `json:"topTargets"`
	Truncated     bool           `json:"truncated"`
	RowsRetained  int            `json:"rowsRetained"`
	RetentionDays int            `json:"retentionDays"`
	QuotaEnforced bool           `json:"quotaEnforced"`
}

// Event is one retained or streamed event. Data is the scrubbed payload.
type Event struct {
	ID   string         `json:"id"`
	Type string         `json:"type"`
	TS   string         `json:"ts"`
	Data map[string]any `json:"data"`
}

// EventsPage is one page of the retained event ring.
type EventsPage struct {
	Events       []Event `json:"events"`
	NextCursor   string  `json:"nextCursor"`
	OldestCursor string  `json:"oldestCursor"`
	Dropped      int     `json:"dropped"`
}
