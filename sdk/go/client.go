package ajs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Version is the SDK version reported in User-Agent.
const Version = "0.1.0"

// maxResponseBytes bounds a unary response body; the largest daemon response is
// a paged catalog, far below this.
const maxResponseBytes = 8 << 20

// maxErrorBytes bounds the slice of an error body read for parsing.
const maxErrorBytes = 1 << 20

// RetryPolicy controls transient retries. A request is retried on a transport
// error, 429, or a 5xx answer. Non-idempotent POSTs are retried only when an
// Idempotency-Key header is present; GET, HEAD, PUT and DELETE are always safe.
type RetryPolicy struct {
	// MaxAttempts is the total number of attempts, including the first.
	MaxAttempts int
	// BaseDelay is the first backoff step; it doubles per attempt.
	BaseDelay time.Duration
	// MaxDelay caps the exponential backoff.
	MaxDelay time.Duration
	// RetryAfterCap caps a server-supplied Retry-After, so a large value cannot
	// stall a script indefinitely.
	RetryAfterCap time.Duration
	// Jitter is the fraction of the delay randomized into [1-Jitter, 1].
	Jitter float64
}

// DefaultRetryPolicy returns three attempts with 200 ms -> 5 s backoff.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxAttempts:   3,
		BaseDelay:     200 * time.Millisecond,
		MaxDelay:      5 * time.Second,
		RetryAfterCap: 30 * time.Second,
		Jitter:        0.2,
	}
}

func (p RetryPolicy) normalized() RetryPolicy {
	if p.MaxAttempts <= 0 {
		p.MaxAttempts = 1
	}
	if p.BaseDelay <= 0 {
		p.BaseDelay = 200 * time.Millisecond
	}
	if p.MaxDelay <= 0 {
		p.MaxDelay = 5 * time.Second
	}
	if p.RetryAfterCap <= 0 {
		p.RetryAfterCap = 30 * time.Second
	}
	if p.Jitter < 0 {
		p.Jitter = 0
	}
	if p.Jitter > 1 {
		p.Jitter = 1
	}
	return p
}

// Client is an ajs-accountd API client. It is safe for concurrent use.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
	retry      RetryPolicy
	userAgent  string
}

// Option customizes a Client.
type Option func(*Client)

// WithHTTPClient replaces the default http.Client.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) {
		if hc != nil {
			c.httpClient = hc
		}
	}
}

// WithRetryPolicy replaces the default retry policy.
func WithRetryPolicy(p RetryPolicy) Option {
	return func(c *Client) { c.retry = p.normalized() }
}

// WithUserAgent replaces the default User-Agent suffix.
func WithUserAgent(ua string) Option {
	return func(c *Client) {
		if ua != "" {
			c.userAgent = ua
		}
	}
}

// New builds a client for baseURL (for example http://127.0.0.1:18080) with a
// bearer token. The URL must be absolute http(s) and carry no query, fragment
// or userinfo.
func New(baseURL, token string, opts ...Option) (*Client, error) {
	raw := strings.TrimSpace(baseURL)
	if raw == "" {
		return nil, errors.New("ajs: base URL is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("ajs: invalid base URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("ajs: base URL scheme must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return nil, errors.New("ajs: base URL has no host")
	}
	if u.User != nil {
		return nil, errors.New("ajs: base URL must not carry credentials")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("ajs: base URL must not carry a query or fragment")
	}
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("ajs: bearer token is required")
	}
	c := &Client{
		baseURL: strings.TrimRight(u.String(), "/"),
		token:   token,
		// A redirect is never part of this API. Refusing it keeps the bearer
		// token from being forwarded to another origin.
		httpClient: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
		retry:     DefaultRetryPolicy(),
		userAgent: "ajs-sdk-go/" + Version,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// String redacts the bearer token, so formatting a client cannot leak it.
func (c *Client) String() string {
	return "ajs.Client{" + c.baseURL + "}"
}

// response is a fully read HTTP answer.
type response struct {
	status int
	header http.Header
	body   []byte
}

// do performs one logical request with retries and decodes a JSON body into
// out. A nil out is allowed (204 and empty bodies).
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any, headers http.Header, out any) (*response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var payload []byte
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("ajs: encode %s %s: %w", method, path, err)
		}
		payload = encoded
	}
	resp, err := c.send(ctx, method, path, query, payload, headers)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, &NetworkError{Method: method, Path: path, Err: err}
	}
	result := &response{status: resp.StatusCode, header: resp.Header, body: raw}
	if out != nil && len(raw) > 0 {
		if err := decodeBody(raw, out); err != nil {
			return result, err
		}
	}
	return result, nil
}

// decodeBody unmarshals one JSON body, tolerating an empty one.
func decodeBody(raw []byte, out any) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("ajs: decode response: %w", err)
	}
	return nil
}

// send performs the retry loop and returns a 2xx response with its body open.
// A non-2xx answer is converted to *APIError after the final attempt.
func (c *Client) send(ctx context.Context, method, path string, query url.Values, payload []byte, headers http.Header) (*http.Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	attempts := c.retry.MaxAttempts
	if attempts < 1 {
		attempts = 1
	}
	hasIdempotencyKey := headers != nil && headers.Get("Idempotency-Key") != ""
	retryable := method == http.MethodGet || method == http.MethodHead ||
		method == http.MethodPut || method == http.MethodDelete || hasIdempotencyKey
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		req, err := c.newRequest(ctx, method, path, query, payload, headers)
		if err != nil {
			return nil, err
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = &NetworkError{Method: method, Path: path, Err: err}
			if !retryable || attempt == attempts {
				return nil, lastErr
			}
			if err := c.wait(ctx, c.delay(attempt, 0)); err != nil {
				return nil, err
			}
			continue
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return resp, nil
		}
		retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		if retryable && isRetryStatus(resp.StatusCode) && attempt < attempts {
			drainAndClose(resp.Body)
			if err := c.wait(ctx, c.delay(attempt, retryAfter)); err != nil {
				return nil, err
			}
			continue
		}
		apiErr := errorFromResponse(resp, method, path)
		return nil, apiErr
	}
	return nil, lastErr
}

func (c *Client) newRequest(ctx context.Context, method, path string, query url.Values, payload []byte, headers http.Header) (*http.Request, error) {
	endpoint := c.baseURL + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	var reader io.Reader
	if payload != nil {
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, fmt.Errorf("ajs: build %s %s: %w", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	// The daemon requires a non-simple header on every admin mutation. A
	// cross-origin form cannot set it; a browser fetch must pass a CORS
	// preflight that only an allowlisted admin origin receives.
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
	default:
		req.Header.Set("X-AJS-CSRF", "1")
	}
	for name, values := range headers {
		req.Header.Del(name)
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	return req, nil
}

func isRetryStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusInternalServerError,
		http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

// delay computes the wait before the next attempt. retryAfter wins when the
// server sent one.
func (c *Client) delay(attempt int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		if retryAfter > c.retry.RetryAfterCap {
			return c.retry.RetryAfterCap
		}
		return retryAfter
	}
	delay := c.retry.BaseDelay
	for i := 1; i < attempt; i++ {
		delay *= 2
		if delay >= c.retry.MaxDelay {
			delay = c.retry.MaxDelay
			break
		}
	}
	if delay > c.retry.MaxDelay {
		delay = c.retry.MaxDelay
	}
	if c.retry.Jitter > 0 {
		delay = time.Duration(float64(delay) * (1 - c.retry.Jitter*rand.Float64()))
	}
	return delay
}

func (c *Client) wait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func drainAndClose(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, 64<<10))
	_ = body.Close()
}

// errorFromResponse parses the daemon's {"error":{...}} envelope. It never
// includes the response body verbatim, so a proxy HTML page cannot smuggle a
// token into an error string.
func errorFromResponse(resp *http.Response, method, path string) *APIError {
	apiErr := &APIError{
		StatusCode: resp.StatusCode,
		Code:       "HTTP_" + strconv.Itoa(resp.StatusCode),
		Method:     method,
		Path:       path,
		RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()),
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBytes))
	_ = resp.Body.Close()
	var envelope struct {
		Error struct {
			Code    string            `json:"code"`
			Message string            `json:"message"`
			Reason  string            `json:"reason"`
			Details map[string]string `json:"details"`
		} `json:"error"`
	}
	if len(raw) > 0 && json.Unmarshal(raw, &envelope) == nil && envelope.Error.Code != "" {
		apiErr.Code = envelope.Error.Code
		apiErr.Message = envelope.Error.Message
		apiErr.Reason = envelope.Error.Reason
		apiErr.Details = envelope.Error.Details
	}
	if apiErr.Message == "" {
		apiErr.Message = http.StatusText(resp.StatusCode)
	}
	return apiErr
}

// parseRetryAfter accepts delta-seconds or an HTTP date.
func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds < 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil {
		d := when.Sub(now)
		if d < 0 {
			return 0
		}
		return d
	}
	return 0
}
