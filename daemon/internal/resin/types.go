// Package resin is a stdlib-only client for the Resin v1.3.0 admin API.
//
// Only the routes ajs-accountd needs are implemented. The proxy token is never
// used here: the admin client authenticates with the admin bearer token, and
// no request carries credentials in a URL, query string or log line.
package resin

import "time"

// SystemInfo is the GET /api/v1/system/info response.
type SystemInfo struct {
	Version   string `json:"version"`
	GitCommit string `json:"git_commit"`
	BuildTime string `json:"build_time"`
	StartedAt string `json:"started_at"`
}

// Subscription is a local subscription as returned by Resin. Content is the
// exact text last accepted by Resin, which the publisher compares against its
// rendered topology to decide whether a PATCH is needed.
type Subscription struct {
	ID                      string `json:"id"`
	Name                    string `json:"name"`
	SourceType              string `json:"source_type"`
	URL                     string `json:"url"`
	Content                 string `json:"content"`
	UpdateInterval          string `json:"update_interval"`
	NodeCount               int    `json:"node_count"`
	HealthyNodeCount        int    `json:"healthy_node_count"`
	Ephemeral               bool   `json:"ephemeral"`
	IncrementalAliveNodes   bool   `json:"incremental_alive_nodes"`
	EphemeralNodeEvictDelay string `json:"ephemeral_node_evict_delay"`
	Enabled                 bool   `json:"enabled"`
	CreatedAt               string `json:"created_at"`
	LastChecked             string `json:"last_checked,omitempty"`
	LastUpdated             string `json:"last_updated,omitempty"`
	LastError               string `json:"last_error,omitempty"`
}

// SubscriptionPatch is the PATCH /api/v1/subscriptions/{id} body. Nil fields
// are omitted so a content-only publish never touches the interval.
type SubscriptionPatch struct {
	Content        *string `json:"content,omitempty"`
	UpdateInterval *string `json:"update_interval,omitempty"`
}

// CreateSubscriptionRequest is the POST /api/v1/subscriptions body for a
// local, ephemeral subscription.
type CreateSubscriptionRequest struct {
	Name           string `json:"name"`
	SourceType     string `json:"source_type"`
	Content        string `json:"content"`
	UpdateInterval string `json:"update_interval,omitempty"`
	Ephemeral      bool   `json:"ephemeral"`
}

// Platform is a Resin platform. RegexFilters are matched against
// "<subscription>/<tag>"; a leading "!" negates a pattern.
type Platform struct {
	ID                               string   `json:"id"`
	Name                             string   `json:"name"`
	StickyTTL                        string   `json:"sticky_ttl"`
	RegexFilters                     []string `json:"regex_filters"`
	RegionFilters                    []string `json:"region_filters"`
	RoutableNodeCount                int      `json:"routable_node_count"`
	ReverseProxyMissAction           string   `json:"reverse_proxy_miss_action"`
	ReverseProxyEmptyAccountBehavior string   `json:"reverse_proxy_empty_account_behavior"`
	ReverseProxyFixedAccountHeader   string   `json:"reverse_proxy_fixed_account_header"`
	AllocationPolicy                 string   `json:"allocation_policy"`
	PassiveCircuitBreakerDisabled    bool     `json:"passive_circuit_breaker_disabled"`
	UpdatedAt                        string   `json:"updated_at"`
}

// PlatformSpec is the desired platform state. Nil RegionFilters and an empty
// AllocationPolicy mean "do not manage this field".
type PlatformSpec struct {
	Name                          string
	StickyTTL                     string
	RegexFilters                  []string
	RegionFilters                 []string
	AllocationPolicy              string
	PassiveCircuitBreakerDisabled *bool
}

// CreatePlatformRequest is the POST /api/v1/platforms body. Fields are omitted
// when the spec leaves them unmanaged so Resin applies its own defaults.
type CreatePlatformRequest struct {
	Name                          string   `json:"name"`
	StickyTTL                     string   `json:"sticky_ttl,omitempty"`
	RegexFilters                  []string `json:"regex_filters,omitempty"`
	RegionFilters                 []string `json:"region_filters,omitempty"`
	AllocationPolicy              string   `json:"allocation_policy,omitempty"`
	PassiveCircuitBreakerDisabled *bool    `json:"passive_circuit_breaker_disabled,omitempty"`
}

// UpdatePlatformRequest is the PATCH /api/v1/platforms/{id} body. Nil fields
// are omitted.
type UpdatePlatformRequest struct {
	StickyTTL                     *string   `json:"sticky_ttl,omitempty"`
	RegexFilters                  *[]string `json:"regex_filters,omitempty"`
	RegionFilters                 *[]string `json:"region_filters,omitempty"`
	AllocationPolicy              *string   `json:"allocation_policy,omitempty"`
	PassiveCircuitBreakerDisabled *bool     `json:"passive_circuit_breaker_disabled,omitempty"`
}

// Node is a Resin node summary. CircuitOpenSince is empty while the circuit is
// closed and an RFC 3339 timestamp once it opens; Resin emits JSON null for the
// closed case, which decodes into the zero value.
type Node struct {
	NodeHash                         string    `json:"node_hash"`
	CreatedAt                        string    `json:"created_at"`
	Enabled                          bool      `json:"enabled"`
	DisplayTag                       string    `json:"display_tag,omitempty"`
	HasOutbound                      bool      `json:"has_outbound"`
	LastError                        string    `json:"last_error,omitempty"`
	CircuitOpenSince                 string    `json:"circuit_open_since"`
	FailureCount                     int       `json:"failure_count"`
	EgressIP                         string    `json:"egress_ip,omitempty"`
	Region                           string    `json:"region,omitempty"`
	LastEgressUpdate                 string    `json:"last_egress_update,omitempty"`
	LastLatencyProbeAttempt          string    `json:"last_latency_probe_attempt,omitempty"`
	LastAuthorityLatencyProbeAttempt string    `json:"last_authority_latency_probe_attempt,omitempty"`
	ReferenceLatencyMs               *float64  `json:"reference_latency_ms,omitempty"`
	Tags                             []NodeTag `json:"tags"`
}

// CircuitOpen reports whether Resin has tripped the node's circuit breaker.
func (n Node) CircuitOpen() bool { return n.CircuitOpenSince != "" }

// NodeTag is one subscription tag attached to a node.
type NodeTag struct {
	SubscriptionID   string `json:"subscription_id"`
	SubscriptionName string `json:"subscription_name"`
	Tag              string `json:"tag"`
}

// Lease is a (Platform, Account) lease anchored on the node's egress IP.
type Lease struct {
	PlatformID   string `json:"platform_id"`
	Account      string `json:"account"`
	NodeHash     string `json:"node_hash"`
	NodeTag      string `json:"node_tag"`
	EgressIP     string `json:"egress_ip"`
	Expiry       string `json:"expiry"`
	LastAccessed string `json:"last_accessed"`
}

// RequestLogQuery is the GET /api/v1/request-logs filter. Offset is sent only
// when non-zero; Resin v1.3.0 rejects it in favour of Cursor, so prefer Cursor
// for pagination and leave Offset zero.
type RequestLogQuery struct {
	PlatformID string
	Account    string
	From       time.Time
	To         time.Time
	Limit      int
	Offset     int
	Cursor     string
}

// RequestLog is one Resin request log row.
type RequestLog struct {
	ID                  string `json:"id"`
	Ts                  string `json:"ts"`
	ProxyType           int    `json:"proxy_type"`
	ClientIP            string `json:"client_ip"`
	PlatformID          string `json:"platform_id"`
	PlatformName        string `json:"platform_name"`
	Account             string `json:"account"`
	TargetHost          string `json:"target_host"`
	TargetURL           string `json:"target_url"`
	NodeHash            string `json:"node_hash"`
	NodeTag             string `json:"node_tag"`
	EgressIP            string `json:"egress_ip"`
	DurationMs          int64  `json:"duration_ms"`
	FirstByteDurationMs int64  `json:"first_byte_duration_ms"`
	NetOK               bool   `json:"net_ok"`
	HTTPMethod          string `json:"http_method"`
	HTTPStatus          int    `json:"http_status"`
	ResinError          string `json:"resin_error"`
	UpstreamStage       string `json:"upstream_stage"`
	UpstreamErrKind     string `json:"upstream_err_kind"`
	UpstreamErrno       string `json:"upstream_errno"`
	UpstreamErrMsg      string `json:"upstream_err_msg"`
	IngressBytes        int64  `json:"ingress_bytes"`
	EgressBytes         int64  `json:"egress_bytes"`
}

// RequestLogPage is the GET /api/v1/request-logs envelope.
type RequestLogPage struct {
	Items      []RequestLog `json:"items"`
	Limit      int          `json:"limit"`
	HasMore    bool         `json:"has_more"`
	NextCursor string       `json:"next_cursor,omitempty"`
}

// Page is the paginated list envelope used by subscriptions, platforms and
// nodes.
type Page[T any] struct {
	Items  []T `json:"items"`
	Total  int `json:"total"`
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

// SystemConfigPatch is the PATCH /api/v1/system/config body. Value is any
// runtime-config value; Resin validates durations itself.
type SystemConfigPatch map[string]any
