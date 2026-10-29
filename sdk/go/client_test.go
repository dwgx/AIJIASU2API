package ajs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testToken = "test-token-not-a-secret"

func newTestClient(t *testing.T, handler http.HandlerFunc, opts ...Option) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	all := []Option{WithRetryPolicy(RetryPolicy{
		MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond,
		RetryAfterCap: time.Second, Jitter: 0,
	})}
	all = append(all, opts...)
	client, err := New(srv.URL, testToken, all...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client
}

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func TestListAccountsSendsAuthAndQuery(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/accounts" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+testToken {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("X-AJS-CSRF"); got != "" {
			t.Errorf("GET sent X-AJS-CSRF = %q, want none", got)
		}
		if got := r.Header.Get("User-Agent"); !strings.HasPrefix(got, "ajs-sdk-go/") {
			t.Errorf("User-Agent = %q", got)
		}
		q := r.URL.Query()
		if q.Get("state") != "active" || q.Get("limit") != "5" || q.Get("offset") != "10" {
			t.Errorf("query = %v", q)
		}
		writeJSON(w, 200, `[{"id":"acc-1","label":"primary","username":"u1","platform":"linux",
			"state":"active","concurrency":1,"useCount":1,"effectiveSlots":1,"regions":["cn"],
			"nodeCount":2,"liveNodeCount":1,"usageBytes":42,"trafficBudgetBytes":null,
			"expiresAt":"","lastLoginAt":"","createdAt":"2026-10-01T00:00:00Z"}]`)
	})
	accounts, err := client.ListAccounts(context.Background(), ListAccountsOptions{
		State: AccountActive, Limit: 5, Offset: 10,
	})
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	if len(accounts) != 1 || accounts[0].ID != "acc-1" || accounts[0].UsageBytes != 42 {
		t.Fatalf("accounts = %+v", accounts)
	}
	if accounts[0].TrafficBudgetBytes != nil {
		t.Fatalf("TrafficBudgetBytes = %v, want nil", accounts[0].TrafficBudgetBytes)
	}
}

func TestAPIErrorIsTypedAndLeaksNoToken(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusConflict, `{"error":{"code":"CONFLICT","message":"duplicate label",
			"reason":"duplicate_label","details":{"field":"label"}}}`)
	})
	_, err := client.CreateAccount(context.Background(), CreateAccountInput{
		Label: "dup", Username: "u", Password: "p", Platform: PlatformLinux, Concurrency: 1,
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error type = %T, want *APIError", err)
	}
	if apiErr.StatusCode != http.StatusConflict || apiErr.Code != "CONFLICT" ||
		apiErr.Reason != "duplicate_label" || apiErr.Details["field"] != "label" {
		t.Fatalf("APIError = %+v", apiErr)
	}
	if apiErr.Retryable() {
		t.Fatal("409 must not be retryable")
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatalf("error leaked the token: %s", err)
	}
	if IsCode(err, "CONFLICT") == false {
		t.Fatal("IsCode(CONFLICT) = false")
	}
}

func TestAPIErrorRedactsReflectedBearer(t *testing.T) {
	// A misbehaving server reflects the credential it received in the error
	// envelope, in both message and reason. Error() must not carry it onward.
	const token = "ajs_sk_reflected_secret"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusUnauthorized, `{"error":{"code":"UNAUTHORIZED","message":"rejected `+
			r.Header.Get("Authorization")+`","reason":"bad Bearer `+token+`"}}`)
	}))
	t.Cleanup(srv.Close)
	client, err := New(srv.URL, token)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = client.ListAccounts(context.Background(), ListAccountsOptions{})
	if err == nil {
		t.Fatal("expected an error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error type = %T, want *APIError", err)
	}
	text := err.Error()
	if strings.Contains(text, token) {
		t.Fatalf("error leaked the reflected token: %s", text)
	}
	if got := strings.Count(text, "Bearer [REDACTED]"); got != 2 {
		t.Fatalf("redactions = %d, want 2 (message and reason): %s", got, text)
	}
	// The rest of the error survives: code, status, path and the surrounding
	// words are untouched.
	for _, want := range []string{"UNAUTHORIZED", "HTTP 401", "/v1/accounts", "rejected Bearer [REDACTED]", "(bad Bearer [REDACTED])"} {
		if !strings.Contains(text, want) {
			t.Errorf("error missing %q: %s", want, text)
		}
	}
	if apiErr.Code != "UNAUTHORIZED" || apiErr.Reason != "bad Bearer "+token {
		t.Errorf("the typed fields must stay unmodified: %+v", apiErr)
	}
}

func TestRetriesServerErrorThenSucceeds(t *testing.T) {
	var attempts atomic.Int32
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) < 3 {
			writeJSON(w, http.StatusInternalServerError, `{"error":{"code":"INTERNAL","message":"boom"}}`)
			return
		}
		writeJSON(w, 200, `[]`)
	})
	if _, err := client.ListAccounts(context.Background(), ListAccountsOptions{}); err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	if got := attempts.Load(); got != 3 {
		t.Fatalf("attempts = %d, want 3", got)
	}
}

func TestRetryAfterIsHonored(t *testing.T) {
	var attempts atomic.Int32
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			writeJSON(w, http.StatusTooManyRequests, `{"error":{"code":"RATE_LIMITED","message":"slow down"}}`)
			return
		}
		writeJSON(w, 200, `[]`)
	})
	start := time.Now()
	if _, err := client.ListAccounts(context.Background(), ListAccountsOptions{}); err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Retry-After 0 should not sleep, took %s", elapsed)
	}
}

func TestNoRetryOnClientError(t *testing.T) {
	var attempts atomic.Int32
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		writeJSON(w, http.StatusBadRequest, `{"error":{"code":"INVALID_ARGUMENT","message":"bad"}}`)
	})
	_, err := client.ListAccounts(context.Background(), ListAccountsOptions{})
	if err == nil || !IsCode(err, "INVALID_ARGUMENT") {
		t.Fatalf("err = %v", err)
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("attempts = %d, want 1", got)
	}
}

func TestPostWithoutIdempotencyKeyIsNotRetried(t *testing.T) {
	var attempts atomic.Int32
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		writeJSON(w, http.StatusInternalServerError, `{"error":{"code":"INTERNAL","message":"boom"}}`)
	})
	_, err := client.CreateAccount(context.Background(), CreateAccountInput{
		Label: "a", Username: "u", Password: "p", Platform: PlatformLinux, Concurrency: 1,
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("attempts = %d, want 1", got)
	}
}

func TestCreateLeaseGeneratesMatchingIdempotencyKey(t *testing.T) {
	var attempts atomic.Int32
	var headerKey, bodyKey string
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/leases" || r.Method != http.MethodPost {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("X-AJS-CSRF"); got != "1" {
			t.Errorf("X-AJS-CSRF = %q, want 1", got)
		}
		headerKey = r.Header.Get("Idempotency-Key")
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		bodyKey, _ = body["idempotencyKey"].(string)
		if attempts.Add(1) == 1 {
			writeJSON(w, http.StatusInternalServerError, `{"error":{"code":"INTERNAL","message":"boom"}}`)
			return
		}
		writeJSON(w, http.StatusCreated, `{"lease":{"id":"lease-1","userId":"usr-1","userName":"u",
			"accountId":"acc-1","accountLabel":"primary","platform":"linux","nodeId":"acc-1:srv-1",
			"nodeLabel":"Tokyo 1","region":"jp","egressIp":"203.0.113.9","protocol":"socks5",
			"gatePort":19001,"ttlSeconds":3600,"expiresAt":"2026-10-26T01:00:00Z","requests":0,
			"bytes":0,"sticky":true,"status":"active","idempotencyKey":"`+headerKey+`",
			"createdAt":"2026-10-26T00:00:00Z"},"replayed":false}`)
	})
	result, err := client.CreateLease(context.Background(), CreateLeaseInput{
		UserID: "usr-1", Platform: PlatformLinux,
	})
	if err != nil {
		t.Fatalf("CreateLease: %v", err)
	}
	if attempts.Load() != 2 {
		t.Fatalf("attempts = %d, want 2 (idempotency key makes POST retryable)", attempts.Load())
	}
	if headerKey == "" || bodyKey == "" {
		t.Fatalf("missing idempotency key: header=%q body=%q", headerKey, bodyKey)
	}
	if headerKey != bodyKey {
		t.Fatalf("header key %q != body key %q", headerKey, bodyKey)
	}
	if !strings.HasPrefix(headerKey, "sdk-") {
		t.Fatalf("generated key = %q", headerKey)
	}
	if result.Lease.ID != "lease-1" || result.Lease.GatePort != 19001 {
		t.Fatalf("result = %+v", result)
	}
}

func TestPatchAccountEncodesExplicitNullBudget(t *testing.T) {
	var body map[string]json.RawMessage
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/v1/accounts/acc-1" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
		}
		writeJSON(w, 200, `{"id":"acc-1"}`)
	})
	label := "renamed"
	if _, err := client.PatchAccount(context.Background(), "acc-1", AccountPatch{
		Label: &label, TrafficBudgetBytes: NullInt64(),
	}); err != nil {
		t.Fatalf("PatchAccount: %v", err)
	}
	if string(body["trafficBudgetBytes"]) != "null" {
		t.Fatalf("trafficBudgetBytes = %s, want null", body["trafficBudgetBytes"])
	}
	var gotLabel string
	_ = json.Unmarshal(body["label"], &gotLabel)
	if gotLabel != "renamed" {
		t.Fatalf("label = %q", gotLabel)
	}
}

func TestDeleteAccountAcceptsNoContent(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/v1/accounts/acc-1" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	if err := client.DeleteAccount(context.Background(), "acc-1"); err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}
}

func TestListAccountServersQuery(t *testing.T) {
	selected := true
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/v1/accounts/acc-1/servers" || q.Get("q") != "tokyo" ||
			q.Get("region") != "jp" || q.Get("status") != "ok" ||
			q.Get("selected") != "true" || q.Get("sort") != "title" ||
			q.Get("offset") != "50" || q.Get("limit") != "50" {
			t.Errorf("path/query = %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		writeJSON(w, 200, `{"enabled":true,"accountId":"acc-1","fetchedAt":"2026-10-25T00:00:00Z",
			"stale":false,"total":1,"offset":50,"limit":50,"nextOffset":0,"hasMore":false,
			"regions":[{"code":"jp","count":1}],
			"servers":[{"id":"srv-1","title":"Tokyo 1","region":"jp","host":"h","port":443,
			"status":"ok","nodeId":"","nodeState":"","selected":false,"skippedByLimit":false,
			"memberOnly":null,"fullLoad":false,"blockingMarks":[],"hasPingSecret":true,
			"latencyMs":null}]}`)
	})
	view, err := client.ListAccountServers(context.Background(), "acc-1", ServersOptions{
		Query: "tokyo", Region: "jp", Status: "ok", Selected: &selected,
		Offset: 50, Limit: 50, Sort: "title",
	})
	if err != nil {
		t.Fatalf("ListAccountServers: %v", err)
	}
	if view.Total != 1 || len(view.Servers) != 1 || view.Servers[0].ID != "srv-1" {
		t.Fatalf("view = %+v", view)
	}
	if view.Servers[0].MemberOnly != nil || view.Servers[0].LatencyMs != nil {
		t.Fatalf("nullable fields decoded incorrectly: %+v", view.Servers[0])
	}
}

func TestClientFormattingRedactsToken(t *testing.T) {
	client, err := New("http://127.0.0.1:18080", testToken)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%v %+v", client, client); strings.Contains(got, testToken) {
		t.Fatalf("formatted client leaked the token: %s", got)
	}
}

func TestNewValidatesInput(t *testing.T) {
	if _, err := New("", testToken); err == nil {
		t.Fatal("empty base URL accepted")
	}
	if _, err := New("ftp://example.com", testToken); err == nil {
		t.Fatal("ftp base URL accepted")
	}
	if _, err := New("http://example.com", "  "); err == nil {
		t.Fatal("empty token accepted")
	}
	if _, err := New("http://user:pass@example.com", testToken); err == nil {
		t.Fatal("userinfo base URL accepted")
	}
	if _, err := New("http://example.com?x=1", testToken); err == nil {
		t.Fatal("query base URL accepted")
	}
	// A bare trailing "?" carries no RawQuery but still forces one, which would
	// turn "http://host:2055?" into "http://host:2055?/healthz".
	if _, err := New("http://example.com?", testToken); err == nil {
		t.Fatal("bare question-mark base URL accepted")
	}
	if _, err := New("http://example.com#readyz", testToken); err == nil {
		t.Fatal("fragment base URL accepted")
	}
}

func TestTransportErrorIsTyped(t *testing.T) {
	// Point the client at a closed listener.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	closed, err := New(srv.URL, testToken, WithRetryPolicy(RetryPolicy{
		MaxAttempts: 1, BaseDelay: time.Millisecond, Jitter: 0,
	}))
	if err != nil {
		t.Fatal(err)
	}
	srv.Close()
	_, err = closed.ListAccounts(context.Background(), ListAccountsOptions{})
	var netErr *NetworkError
	if !errors.As(err, &netErr) {
		t.Fatalf("error = %T %v, want *NetworkError", err, err)
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatal("transport error leaked the token")
	}
}

func TestContextCancellationStopsRetry(t *testing.T) {
	var attempts atomic.Int32
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		time.Sleep(200 * time.Millisecond)
		writeJSON(w, http.StatusServiceUnavailable, `{"error":{"code":"UPSTREAM_UNAVAILABLE","message":"down"}}`)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := client.ListAccounts(ctx, ListAccountsOptions{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context deadline", err)
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("attempts = %d, want 1", got)
	}
}

func TestURLPathEscaping(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/v1/accounts/acc%2F1" {
			t.Errorf("escaped path = %q", r.URL.EscapedPath())
		}
		writeJSON(w, 200, `{"id":"acc/1"}`)
	})
	if _, err := client.GetAccount(context.Background(), "acc/1"); err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
}
