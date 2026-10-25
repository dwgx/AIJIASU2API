package resin

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Defaults for the admin client. The base URL is loopback because Resin must
// share a network namespace with the gates it dials (docs/PANEL.md §2).
const (
	DefaultBaseURL = "http://127.0.0.1:2260"
	DefaultTimeout = 10 * time.Second

	maxErrorBodyBytes = 1 << 20
	// maxResponseBodyBytes bounds a successful JSON document. A Resin that
	// streams an unbounded body (loopback, but reachable with the admin
	// token) must fail the call instead of growing the daemon's heap.
	maxResponseBodyBytes = 8 << 20
	maxListPageSize      = 1000
)

// EnvBaseURL and EnvAdminToken are the environment variables read by
// ConfigFromEnv.
const (
	EnvBaseURL    = "AJS_RESIN_URL"
	EnvAdminToken = "AJS_RESIN_ADMIN_TOKEN"
)

// APIError is a non-2xx response from Resin. It never contains the bearer
// token: only the method, path, status and the server-supplied code/message
// are recorded.
type APIError struct {
	Method    string
	Path      string
	Status    int
	Code      string
	Message   string
	RequestID string
}

// Error implements error.
func (e *APIError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	return fmt.Sprintf("resin %s %s: http %d %s: %s (request %s)",
		e.Method, e.Path, e.Status, e.Code, msg, e.RequestID)
}

// IsNotFound reports whether err is a Resin NOT_FOUND response.
func IsNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound
}

// IsStatus reports whether err is a Resin response with the given HTTP status.
func IsStatus(err error, status int) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == status
}

// IsUpstreamConnectFailed reports whether Resin rejected a proxied request
// because the leased node could not be dialed (M1: retry once).
func IsUpstreamConnectFailed(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Code == "UPSTREAM_CONNECT_FAILED"
}

// Config is the admin client configuration. AdminToken is the Resin admin
// bearer; the proxy token is deliberately not part of this client.
type Config struct {
	BaseURL    string
	AdminToken string
	Timeout    time.Duration
	HTTPClient *http.Client
	Logger     *slog.Logger
}

// ConfigFromEnv reads AJS_RESIN_URL (default http://127.0.0.1:2260) and
// AJS_RESIN_ADMIN_TOKEN. A missing token is not an error here: the client
// still builds, and Resin answers 401 until the operator supplies one.
func ConfigFromEnv() Config {
	base := strings.TrimSpace(os.Getenv(EnvBaseURL))
	if base == "" {
		base = DefaultBaseURL
	}
	return Config{
		BaseURL:    base,
		AdminToken: strings.TrimSpace(os.Getenv(EnvAdminToken)),
		Timeout:    DefaultTimeout,
	}
}

// Client talks to the Resin admin API. It is safe for concurrent use.
type Client struct {
	baseURL    string
	adminToken string
	timeout    time.Duration
	httpClient *http.Client
	logger     *slog.Logger
}

// New builds a client. An empty BaseURL falls back to DefaultBaseURL, a zero
// Timeout to DefaultTimeout, and a nil logger to slog.Default.
func New(cfg Config) (*Client, error) {
	base := strings.TrimSpace(cfg.BaseURL)
	if base == "" {
		base = DefaultBaseURL
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("resin: invalid base URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("resin: base URL must be http or https, got %q", parsed.Scheme)
	}
	if parsed.User != nil {
		return nil, errors.New("resin: base URL must not embed credentials; use AJS_RESIN_ADMIN_TOKEN")
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{
		baseURL:    strings.TrimRight(parsed.String(), "/"),
		adminToken: cfg.AdminToken,
		timeout:    timeout,
		httpClient: httpClient,
		logger:     logger,
	}, nil
}

// BaseURL returns the configured admin API base URL.
func (c *Client) BaseURL() string { return c.baseURL }

// SystemInfo returns the Resin build identity.
func (c *Client) SystemInfo(ctx context.Context) (SystemInfo, error) {
	var info SystemInfo
	err := c.do(ctx, http.MethodGet, "/api/v1/system/info", nil, &info)
	return info, err
}

// EnsureSubscription finds a local subscription by exact name and creates it
// when absent. When it exists and its content (or update interval) differs, it
// is PATCHed. The bool reports whether the subscription was created.
func (c *Client) EnsureSubscription(ctx context.Context, name, content, interval string) (Subscription, bool, error) {
	if strings.TrimSpace(name) == "" {
		return Subscription{}, false, errors.New("resin: subscription name is required")
	}
	existing, found, err := c.findSubscription(ctx, name)
	if err != nil {
		return Subscription{}, false, err
	}
	if !found {
		body := CreateSubscriptionRequest{
			Name:           name,
			SourceType:     "local",
			Content:        content,
			UpdateInterval: interval,
			Ephemeral:      true,
		}
		var created Subscription
		if err := c.do(ctx, http.MethodPost, "/api/v1/subscriptions", body, &created); err != nil {
			return Subscription{}, false, err
		}
		return created, true, nil
	}
	patch := SubscriptionPatch{}
	if existing.Content != content {
		patch.Content = &content
	}
	if interval != "" && existing.UpdateInterval != interval {
		patch.UpdateInterval = &interval
	}
	if patch.Content == nil && patch.UpdateInterval == nil {
		return existing, false, nil
	}
	updated, err := c.PatchSubscriptionFields(ctx, existing.ID, patch)
	if err != nil {
		return Subscription{}, false, err
	}
	return updated, false, nil
}

// PatchSubscription replaces the subscription content.
func (c *Client) PatchSubscription(ctx context.Context, id, content string) (Subscription, error) {
	return c.PatchSubscriptionFields(ctx, id, SubscriptionPatch{Content: &content})
}

// DeleteSubscription removes one subscription. It is the withdrawal path for a
// pool with no LIVE nodes: Resin rejects empty content, and a stale body would
// keep advertising dead ports. A missing subscription is a NOT_FOUND APIError.
func (c *Client) DeleteSubscription(ctx context.Context, id string) error {
	path := "/api/v1/subscriptions/" + url.PathEscape(id)
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

// PatchSubscriptionFields applies a partial subscription update, e.g. content
// plus update interval.
func (c *Client) PatchSubscriptionFields(ctx context.Context, id string, patch SubscriptionPatch) (Subscription, error) {
	var updated Subscription
	path := "/api/v1/subscriptions/" + url.PathEscape(id)
	if err := c.do(ctx, http.MethodPatch, path, patch, &updated); err != nil {
		return Subscription{}, err
	}
	return updated, nil
}

// RefreshSubscription forces a synchronous local-subscription update.
func (c *Client) RefreshSubscription(ctx context.Context, id string) error {
	path := "/api/v1/subscriptions/" + url.PathEscape(id) + "/actions/refresh"
	return c.do(ctx, http.MethodPost, path, nil, nil)
}

// FindSubscription returns the local subscription with the exact name. It is
// the restart path of the empty-render withdrawal: the previous process's id
// is not in memory, but Resin still persists the subscription by name.
func (c *Client) FindSubscription(ctx context.Context, name string) (Subscription, bool, error) {
	if strings.TrimSpace(name) == "" {
		return Subscription{}, false, errors.New("resin: subscription name is required")
	}
	return c.findSubscription(ctx, name)
}

// GetSubscription returns one subscription by id.
func (c *Client) GetSubscription(ctx context.Context, id string) (Subscription, error) {
	var sub Subscription
	path := "/api/v1/subscriptions/" + url.PathEscape(id)
	err := c.do(ctx, http.MethodGet, path, nil, &sub)
	return sub, err
}

// ListNodes returns every node Resin currently knows, following pagination.
func (c *Client) ListNodes(ctx context.Context) ([]Node, error) {
	var all []Node
	for offset := 0; ; {
		var page Page[Node]
		path := "/api/v1/nodes?limit=" + strconv.Itoa(maxListPageSize) + "&offset=" + strconv.Itoa(offset)
		if err := c.do(ctx, http.MethodGet, path, nil, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Items...)
		offset += len(page.Items)
		if len(page.Items) == 0 || offset >= page.Total {
			return all, nil
		}
	}
}

// GetNode returns one node by hash.
func (c *Client) GetNode(ctx context.Context, hash string) (Node, error) {
	var node Node
	path := "/api/v1/nodes/" + url.PathEscape(hash)
	err := c.do(ctx, http.MethodGet, path, nil, &node)
	return node, err
}

// GetPlatform returns one platform by id.
func (c *Client) GetPlatform(ctx context.Context, id string) (Platform, error) {
	var platform Platform
	path := "/api/v1/platforms/" + url.PathEscape(id)
	err := c.do(ctx, http.MethodGet, path, nil, &platform)
	return platform, err
}

// EnsurePlatform finds a platform by exact name and creates it when absent.
// Existing platforms are PATCHed only for fields the spec manages and that
// differ. The bool reports whether the platform was created.
func (c *Client) EnsurePlatform(ctx context.Context, spec PlatformSpec) (Platform, bool, error) {
	if strings.TrimSpace(spec.Name) == "" {
		return Platform{}, false, errors.New("resin: platform name is required")
	}
	existing, found, err := c.findPlatform(ctx, spec.Name)
	if err != nil {
		return Platform{}, false, err
	}
	if !found {
		body := CreatePlatformRequest{
			Name:                          spec.Name,
			StickyTTL:                     spec.StickyTTL,
			RegexFilters:                  spec.RegexFilters,
			RegionFilters:                 spec.RegionFilters,
			AllocationPolicy:              spec.AllocationPolicy,
			PassiveCircuitBreakerDisabled: spec.PassiveCircuitBreakerDisabled,
		}
		var created Platform
		if err := c.do(ctx, http.MethodPost, "/api/v1/platforms", body, &created); err != nil {
			return Platform{}, false, err
		}
		return created, true, nil
	}
	patch, changed := platformPatch(existing, spec)
	if !changed {
		return existing, false, nil
	}
	updated, err := c.PatchPlatform(ctx, existing.ID, patch)
	if err != nil {
		return Platform{}, false, err
	}
	return updated, false, nil
}

// PatchPlatform applies a partial platform update.
func (c *Client) PatchPlatform(ctx context.Context, id string, patch UpdatePlatformRequest) (Platform, error) {
	var updated Platform
	path := "/api/v1/platforms/" + url.PathEscape(id)
	if err := c.do(ctx, http.MethodPatch, path, patch, &updated); err != nil {
		return Platform{}, err
	}
	return updated, nil
}

// PatchPlatformFilters replaces the platform's regex filters. Rotation uses
// this to exclude the current tag; a full replacement keeps the call
// idempotent.
func (c *Client) PatchPlatformFilters(ctx context.Context, platformID string, filters []string) (Platform, error) {
	if filters == nil {
		filters = []string{}
	}
	return c.PatchPlatform(ctx, platformID, UpdatePlatformRequest{RegexFilters: &filters})
}

// GetLease returns the lease for (platform, account). A missing lease is a
// NOT_FOUND APIError; use IsNotFound.
func (c *Client) GetLease(ctx context.Context, platformID, account string) (Lease, error) {
	var lease Lease
	path := "/api/v1/platforms/" + url.PathEscape(platformID) + "/leases/" + url.PathEscape(account)
	err := c.do(ctx, http.MethodGet, path, nil, &lease)
	return lease, err
}

// DeleteLease releases the lease for (platform, account). A 404 means the
// lease is already gone and is treated as success, so callers can retry.
func (c *Client) DeleteLease(ctx context.Context, platformID, account string) error {
	path := "/api/v1/platforms/" + url.PathEscape(platformID) + "/leases/" + url.PathEscape(account)
	err := c.do(ctx, http.MethodDelete, path, nil, nil)
	if IsNotFound(err) {
		return nil
	}
	return err
}

// PatchSystemConfig sets one runtime-config key, e.g.
// ("max_latency_test_interval", "30s").
func (c *Client) PatchSystemConfig(ctx context.Context, key string, value any) error {
	if strings.TrimSpace(key) == "" {
		return errors.New("resin: system config key is required")
	}
	return c.do(ctx, http.MethodPatch, "/api/v1/system/config", SystemConfigPatch{key: value}, nil)
}

// ListRequestLogs queries the request log. Resin v1.3.0 pages with a cursor;
// Offset is forwarded only when non-zero (older callers) and will be rejected
// by that build, so prefer Cursor.
func (c *Client) ListRequestLogs(ctx context.Context, query RequestLogQuery) (RequestLogPage, error) {
	values := url.Values{}
	if query.PlatformID != "" {
		values.Set("platform_id", query.PlatformID)
	}
	if query.Account != "" {
		values.Set("account", query.Account)
	}
	if !query.From.IsZero() {
		values.Set("from", query.From.UTC().Format(time.RFC3339Nano))
	}
	if !query.To.IsZero() {
		values.Set("to", query.To.UTC().Format(time.RFC3339Nano))
	}
	if query.Limit > 0 {
		values.Set("limit", strconv.Itoa(query.Limit))
	}
	if query.Offset > 0 {
		values.Set("offset", strconv.Itoa(query.Offset))
	}
	if query.Cursor != "" {
		values.Set("cursor", query.Cursor)
	}
	path := "/api/v1/request-logs"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var page RequestLogPage
	if err := c.do(ctx, http.MethodGet, path, nil, &page); err != nil {
		return RequestLogPage{}, err
	}
	return page, nil
}

// findSubscription lists subscriptions matching name and returns the exact
// name match. Resin filters on id, name, url and source_type, so an exact
// comparison is still required.
func (c *Client) findSubscription(ctx context.Context, name string) (Subscription, bool, error) {
	for offset := 0; ; {
		var page Page[Subscription]
		path := "/api/v1/subscriptions?keyword=" + url.QueryEscape(name) +
			"&limit=" + strconv.Itoa(maxListPageSize) + "&offset=" + strconv.Itoa(offset)
		if err := c.do(ctx, http.MethodGet, path, nil, &page); err != nil {
			return Subscription{}, false, err
		}
		for _, sub := range page.Items {
			if sub.Name == name {
				return sub, true, nil
			}
		}
		offset += len(page.Items)
		if len(page.Items) == 0 || offset >= page.Total {
			return Subscription{}, false, nil
		}
	}
}

// findPlatform lists platforms matching name and returns the exact match.
func (c *Client) findPlatform(ctx context.Context, name string) (Platform, bool, error) {
	for offset := 0; ; {
		var page Page[Platform]
		path := "/api/v1/platforms?keyword=" + url.QueryEscape(name) +
			"&limit=" + strconv.Itoa(maxListPageSize) + "&offset=" + strconv.Itoa(offset)
		if err := c.do(ctx, http.MethodGet, path, nil, &page); err != nil {
			return Platform{}, false, err
		}
		for _, platform := range page.Items {
			if platform.Name == name {
				return platform, true, nil
			}
		}
		offset += len(page.Items)
		if len(page.Items) == 0 || offset >= page.Total {
			return Platform{}, false, nil
		}
	}
}

// platformPatch returns the minimal patch that converges an existing platform
// onto the spec, and whether anything differs.
func platformPatch(existing Platform, spec PlatformSpec) (UpdatePlatformRequest, bool) {
	var patch UpdatePlatformRequest
	changed := false
	if spec.StickyTTL != "" && !durationEqual(existing.StickyTTL, spec.StickyTTL) {
		ttl := spec.StickyTTL
		patch.StickyTTL = &ttl
		changed = true
	}
	if spec.RegexFilters != nil && !stringSetEqual(existing.RegexFilters, spec.RegexFilters) {
		filters := append([]string(nil), spec.RegexFilters...)
		patch.RegexFilters = &filters
		changed = true
	}
	if spec.RegionFilters != nil && !stringSetEqual(existing.RegionFilters, spec.RegionFilters) {
		filters := append([]string(nil), spec.RegionFilters...)
		patch.RegionFilters = &filters
		changed = true
	}
	if spec.AllocationPolicy != "" && !strings.EqualFold(existing.AllocationPolicy, spec.AllocationPolicy) {
		policy := spec.AllocationPolicy
		patch.AllocationPolicy = &policy
		changed = true
	}
	if spec.PassiveCircuitBreakerDisabled != nil && existing.PassiveCircuitBreakerDisabled != *spec.PassiveCircuitBreakerDisabled {
		disabled := *spec.PassiveCircuitBreakerDisabled
		patch.PassiveCircuitBreakerDisabled = &disabled
		changed = true
	}
	return patch, changed
}

// durationEqual compares two duration strings canonically, falling back to a
// literal comparison when either side does not parse.
func durationEqual(a, b string) bool {
	if a == b {
		return true
	}
	da, errA := time.ParseDuration(a)
	db, errB := time.ParseDuration(b)
	if errA != nil || errB != nil {
		return false
	}
	return da == db
}

// stringSetEqual compares two string slices ignoring order and nil-vs-empty.
func stringSetEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, item := range a {
		seen[item]++
	}
	for _, item := range b {
		seen[item]--
		if seen[item] < 0 {
			return false
		}
	}
	return true
}

// do performs one admin call. The caller's context bounds the request, with
// Config.Timeout applied on top when it is shorter.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	requestID := newRequestID()
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("resin: encode %s %s: %w", method, path, err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("resin: build %s %s: %w", method, path, err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Request-Id", requestID)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.adminToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.adminToken)
	}

	started := time.Now()
	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.logger.Debug("resin request failed", "method", method, "path", path,
			"request_id", requestID, "duration", time.Since(started), "error", err)
		return fmt.Errorf("resin: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	c.logger.Debug("resin request", "method", method, "path", path,
		"status", resp.StatusCode, "request_id", requestID, "duration", time.Since(started))

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return decodeAPIError(resp, method, path, requestID)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBodyBytes))
		return nil
	}
	// Read one byte past the cap so an oversized document is rejected
	// explicitly instead of surfacing as a truncated decode error.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodyBytes+1))
	if err != nil {
		return fmt.Errorf("resin: read %s %s: %w", method, path, err)
	}
	if len(raw) > maxResponseBodyBytes {
		return fmt.Errorf("resin: %s %s: response exceeds %d bytes", method, path, maxResponseBodyBytes)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("resin: decode %s %s: %w", method, path, err)
	}
	return nil
}

// decodeAPIError maps a non-2xx body onto APIError without echoing headers.
func decodeAPIError(resp *http.Response, method, path, requestID string) error {
	apiErr := &APIError{
		Method:    method,
		Path:      path,
		Status:    resp.StatusCode,
		Code:      "HTTP_" + strconv.Itoa(resp.StatusCode),
		RequestID: requestID,
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil && envelope.Error.Code != "" {
		apiErr.Code = envelope.Error.Code
		apiErr.Message = envelope.Error.Message
	} else if len(raw) > 0 && len(raw) <= 200 && !json.Valid(raw) {
		apiErr.Message = strings.TrimSpace(string(raw))
	}
	return apiErr
}

// newRequestID returns a short correlation id for one outbound call. It is
// also sent as X-Request-Id so a future Resin build can echo it.
func newRequestID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(buf[:])
}
