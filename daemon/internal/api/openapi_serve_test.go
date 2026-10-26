package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"ajsaccountd/internal/store"
)

var etagPattern = regexp.MustCompile(`^"[0-9a-f]{64}"$`)

func TestEmbeddedOpenAPISpecMatchesDocs(t *testing.T) {
	docs, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "openapi.json"))
	if err != nil {
		t.Fatalf("read docs/openapi.json: %v", err)
	}
	if !bytes.Equal(docs, embeddedOpenAPISpec) {
		t.Fatal("daemon/internal/api/openapi.json drifted from docs/openapi.json; copy the docs file over the embed copy")
	}
	var doc map[string]any
	if err := json.Unmarshal(embeddedOpenAPISpec, &doc); err != nil {
		t.Fatalf("embedded spec is not valid JSON: %v", err)
	}
	version, _ := doc["openapi"].(string)
	if !strings.HasPrefix(version, "3.1.") {
		t.Fatalf("openapi version = %q, want 3.1.x", version)
	}
}

func TestOpenAPISpecServesEmbeddedContract(t *testing.T) {
	ts, _ := newTestServer(t)
	resp, body := apiCall(t, ts, http.MethodGet, "/v1/openapi.json", nil)
	requireStatus(t, resp, http.StatusOK)

	if got := resp.Header.Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := resp.Header.Get("ETag"); !etagPattern.MatchString(got) {
		t.Fatalf("ETag = %q, want a quoted SHA-256", got)
	}
	if !bytes.Equal(body, embeddedOpenAPISpec) {
		t.Fatal("served body differs from the embedded spec")
	}

	var doc struct {
		Paths map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("decode served spec: %v", err)
	}
	for _, path := range []string{"/v1/audit/export.csv", "/v1/openapi.json"} {
		if _, ok := doc.Paths[path]; !ok {
			t.Fatalf("served spec is missing %s", path)
		}
	}
}

func TestOpenAPISpecConditionalGET(t *testing.T) {
	ts, _ := newTestServer(t)
	resp, _ := apiCall(t, ts, http.MethodGet, "/v1/openapi.json", nil)
	requireStatus(t, resp, http.StatusOK)
	etag := resp.Header.Get("ETag")

	conditional := func(value string) (*http.Response, []byte) {
		t.Helper()
		req := newRequest(t, http.MethodGet, ts.URL+"/v1/openapi.json", nil)
		req.Header.Set("Authorization", "Bearer "+testToken)
		req.Header.Set("If-None-Match", value)
		return doRequest(t, ts, req)
	}

	for _, value := range []string{etag, "W/" + etag, `"stale", ` + etag, "*"} {
		resp, body := conditional(value)
		requireStatus(t, resp, http.StatusNotModified)
		if len(body) != 0 {
			t.Fatalf("304 carried a %d-byte body", len(body))
		}
		if got := resp.Header.Get("ETag"); got != etag {
			t.Fatalf("304 ETag = %q, want %q", got, etag)
		}
	}

	resp, body := conditional(`"stale"`)
	requireStatus(t, resp, http.StatusOK)
	if !bytes.Equal(body, embeddedOpenAPISpec) {
		t.Fatal("stale validator did not receive the full document")
	}
}

func TestOpenAPISpecRequiresAuth(t *testing.T) {
	ts, _ := newTestServer(t)
	req := newRequest(t, http.MethodGet, ts.URL+"/v1/openapi.json", nil)
	resp, body := doRequest(t, ts, req)
	requireStatus(t, resp, http.StatusUnauthorized)
	requireErrorCode(t, body, CodeUnauthorized)
}

func TestOpsHeadersExposedToCORS(t *testing.T) {
	ts, _ := newTestServer(t)
	req := newRequest(t, http.MethodGet, ts.URL+"/v1/accounts", nil)
	req.Header.Set("Origin", "http://127.0.0.1:18081")
	resp, _ := doRequest(t, ts, req)
	requireStatus(t, resp, http.StatusUnauthorized)
	exposed := resp.Header.Get("Access-Control-Expose-Headers")
	// A browser cannot read a response header unless it is exposed; the rate
	// contract the document promises is only real for the console when these
	// names are on the list.
	for _, header := range []string{
		"Content-Disposition", "ETag", "X-Truncated",
		"Retry-After", "X-RateLimit-Limit", "X-RateLimit-Remaining", "X-RateLimit-Reset",
	} {
		if !strings.Contains(exposed, header) {
			t.Fatalf("expose headers = %q, missing %s", exposed, header)
		}
	}
}

// openAPIResponse is the subset of a response object the contract test reads.
type openAPIResponse struct {
	Ref     string                   `json:"$ref"`
	Headers map[string]openAPIHeader `json:"headers"`
}

type openAPIHeader struct {
	Ref string `json:"$ref"`
}

// openAPIOperation is the subset of an operation the contract test reads.
type openAPIOperation struct {
	RequiredScope string                     `json:"x-required-scope"`
	Responses     map[string]openAPIResponse `json:"responses"`
}

var openAPIMethods = map[string]bool{
	"get": true, "post": true, "put": true, "patch": true, "delete": true,
}

// rateLimitHeaders are the three headers allowScopedKeyRequest writes on every
// scoped-key response, in the order the document lists them.
var rateLimitHeaders = []string{
	"X-RateLimit-Limit", "X-RateLimit-Remaining", "X-RateLimit-Reset",
}

// TestOpenAPIContractMatchesRouteTable holds the document to the daemon:
// every registered route is documented, every documented operation is
// registered, and each route in routeScopes declares the same grant, a scoped
// 403, a 429 with Retry-After and the rate-limit headers. routeScopes is the
// least-privilege table the router enforces, so this test fails whenever the
// two drift apart in either direction.
func TestOpenAPIContractMatchesRouteTable(t *testing.T) {
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Enum []string `json:"enum"`
			} `json:"schemas"`
			Responses map[string]struct {
				Description string                   `json:"description"`
				Headers     map[string]openAPIHeader `json:"headers"`
			} `json:"responses"`
		} `json:"components"`
		Paths map[string]map[string]openAPIOperation `json:"paths"`
	}
	if err := json.Unmarshal(embeddedOpenAPISpec, &doc); err != nil {
		t.Fatalf("decode embedded spec: %v", err)
	}

	// The router is the route table: build the same handler tree the server
	// serves, so a route registered beside its handler (orders.go) is seen too.
	server := New(Config{AdminToken: testToken}, store.New(),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	rt := &router{}
	server.routes(rt)
	registered := make(map[string]bool, len(rt.routes))
	for _, r := range rt.routes {
		registered[r.method+" "+r.pattern] = true
	}

	documented := make(map[string]openAPIOperation)
	for path, item := range doc.Paths {
		for method, operation := range item {
			if !openAPIMethods[method] {
				continue
			}
			documented[strings.ToUpper(method)+" "+path] = operation
		}
	}
	for route := range registered {
		if _, ok := documented[route]; !ok {
			t.Errorf("%s is registered but not documented", route)
		}
	}
	for route := range documented {
		if registered[route] {
			continue
		}
		// The customer portal and tenant administration are registered only
		// when AJS_TENANT_PORTAL=1, so the default handler tree cannot see
		// them. Every other documented operation must be a live route.
		_, path, _ := strings.Cut(route, " ")
		if strings.HasPrefix(path, "/v1/portal/") || strings.HasPrefix(path, "/v1/admin/tenants") {
			continue
		}
		t.Errorf("%s is documented but not registered", route)
	}

	scopeEnum := make(map[string]bool)
	for _, value := range doc.Components.Schemas["Scope"].Enum {
		scopeEnum[value] = true
	}

	for route, scope := range routeScopes {
		if !scopeEnum[string(scope)] {
			t.Errorf("%s requires %q, which is absent from the Scope enum", route, scope)
		}
		operation, ok := documented[route]
		if !ok {
			t.Errorf("%s is in routeScopes but has no operation", route)
			continue
		}
		if operation.RequiredScope != string(scope) {
			t.Errorf("%s x-required-scope = %q, want %q", route, operation.RequiredScope, scope)
		}
		requireResponseRef(t, route, operation, "403", "#/components/responses/ScopedKeyForbidden")
		requireResponseRef(t, route, operation, "429", "#/components/responses/ScopedKeyRateLimited")
		successes := 0
		for status, response := range operation.Responses {
			if !strings.HasPrefix(status, "2") {
				continue
			}
			successes++
			for _, header := range rateLimitHeaders {
				want := "#/components/headers/" + rateLimitHeaderComponent(header)
				if got := response.Headers[header].Ref; got != want {
					t.Errorf("%s %s %s header = %q, want %q", route, status, header, got, want)
				}
			}
		}
		if successes == 0 {
			t.Errorf("%s documents no 2xx response", route)
		}
	}
	// The table is the whole truth: a route outside it is admin-only, so an
	// invented grant would tell a generator it can reach an operator surface.
	for route, operation := range documented {
		if operation.RequiredScope == "" {
			continue
		}
		if _, ok := routeScopes[route]; !ok {
			t.Errorf("%s declares x-required-scope %q but is absent from routeScopes",
				route, operation.RequiredScope)
		}
	}

	forbidden := doc.Components.Responses["ScopedKeyForbidden"]
	for _, reason := range []string{ReasonScopeRequired, ReasonScopedKeyRestricted} {
		if !strings.Contains(forbidden.Description, reason) {
			t.Errorf("ScopedKeyForbidden does not name %s", reason)
		}
	}
	for _, header := range rateLimitHeaders {
		want := "#/components/headers/" + rateLimitHeaderComponent(header)
		if got := forbidden.Headers[header].Ref; got != want {
			t.Errorf("ScopedKeyForbidden %s header = %q, want %q", header, got, want)
		}
	}
	limited := doc.Components.Responses["ScopedKeyRateLimited"]
	if got := limited.Headers["Retry-After"].Ref; got != "#/components/headers/RetryAfter" {
		t.Errorf("ScopedKeyRateLimited Retry-After = %q", got)
	}
	for _, header := range rateLimitHeaders {
		want := "#/components/headers/" + rateLimitHeaderComponent(header)
		if got := limited.Headers[header].Ref; got != want {
			t.Errorf("ScopedKeyRateLimited %s header = %q, want %q", header, got, want)
		}
	}
}

// rateLimitHeaderComponent maps X-RateLimit-Limit to its component name.
func rateLimitHeaderComponent(header string) string {
	return strings.ReplaceAll(strings.TrimPrefix(header, "X-"), "-", "")
}

func requireResponseRef(t *testing.T, route string, operation openAPIOperation, status, want string) {
	t.Helper()
	response, ok := operation.Responses[status]
	if !ok {
		t.Errorf("%s documents no %s response", route, status)
		return
	}
	if response.Ref != want {
		t.Errorf("%s %s ref = %q, want %q", route, status, response.Ref, want)
	}
}
