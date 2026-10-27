package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
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

// specLookup walks a decoded JSON document and reports whether the path exists.
func specLookup(root any, path ...string) (any, bool) {
	current := root
	for _, segment := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		value, ok := object[segment]
		if !ok {
			return nil, false
		}
		current = value
	}
	return current, true
}

// specDig is specLookup for a path the document must contain.
func specDig(t *testing.T, root any, path ...string) any {
	t.Helper()
	value, ok := specLookup(root, path...)
	if !ok {
		t.Fatalf("spec is missing %s", strings.Join(path, "."))
	}
	return value
}

func specMap(t *testing.T, value any, what string) map[string]any {
	t.Helper()
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%s is not an object", what)
	}
	return object
}

func specStrings(t *testing.T, value any, what string) []string {
	t.Helper()
	items, ok := value.([]any)
	if !ok {
		t.Fatalf("%s is not an array", what)
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		text, ok := item.(string)
		if !ok {
			t.Fatalf("%s contains a non-string", what)
		}
		out = append(out, text)
	}
	return out
}

// specCodeSources are the places the daemon writes an envelope code: declared
// constants, literal write sites, the operation-error status mapping and the
// event cursor constant.
var specCodeSources = []*regexp.Regexp{
	regexp.MustCompile(`(?m)^\s*(?:Code[A-Za-z0-9]+|\w*[Cc]ode\w*)\s*=\s*"([A-Z][A-Z_]{2,})"`),
	regexp.MustCompile(`write(?:Error|ErrorReason|ScheduleReason)\(\s*w\s*,\s*[^,]+,\s*"([A-Z][A-Z_]{2,})"`),
	regexp.MustCompile(`return http\.Status[A-Za-z]+,\s*"([A-Z][A-Z_]{2,})"`),
}

// TestOpenAPIDocumentsWireShapes holds the document to the shapes the handlers
// actually emit: nullable operation fields, the catalog geography summary, the
// documented sticky-TTL sentinel, the complete envelope vocabulary, the 412
// branch and the scoped-key 404s.
func TestOpenAPIDocumentsWireShapes(t *testing.T) {
	var doc map[string]any
	if err := json.Unmarshal(embeddedOpenAPISpec, &doc); err != nil {
		t.Fatalf("decode embedded spec: %v", err)
	}
	schemas := specMap(t, specDig(t, doc, "components", "schemas"), "components.schemas")

	// Operation.result/error are always serialized, null until they exist.
	operation := specMap(t, schemas["Operation"], "Operation")
	required := specStrings(t, operation["required"], "Operation.required")
	for _, name := range []string{"result", "error"} {
		if !specContainsString(required, name) {
			t.Errorf("Operation.required dropped %s; the handler always serializes it", name)
		}
		property := specMap(t, specMap(t, operation["properties"], "Operation.properties")[name], "Operation."+name)
		branches, _ := property["oneOf"].([]any)
		nullable := false
		for _, branch := range branches {
			if branchMap, ok := branch.(map[string]any); ok && branchMap["type"] == "null" {
				nullable = true
			}
		}
		if !nullable {
			t.Errorf("Operation.%s is not nullable; a queued record serializes it as null", name)
		}
	}

	// ServersView.snapshotId (omitted when empty) and the always-present geo.
	view := specMap(t, schemas["ServersView"], "ServersView")
	viewProperties := specMap(t, view["properties"], "ServersView.properties")
	if _, ok := viewProperties["snapshotId"]; !ok {
		t.Error("ServersView.snapshotId is undocumented; the guide tells clients to echo it")
	}
	if _, ok := viewProperties["geo"]; !ok {
		t.Error("ServersView.geo is undocumented")
	}
	viewRequired := specStrings(t, view["required"], "ServersView.required")
	if !specContainsString(viewRequired, "geo") {
		t.Error("ServersView.required does not name geo; the handler always sets it")
	}
	if specContainsString(viewRequired, "snapshotId") {
		t.Error("ServersView.required names snapshotId; it is omitted when no connector is configured")
	}

	for name, want := range map[string][]string{
		"CatalogGeo":         {"total", "unknown", "provinces", "current", "live"},
		"CatalogGeoProvince": {"code", "count", "connected", "cities"},
		"CatalogGeoCity":     {"code", "label", "count"},
		"CatalogGeoRow":      {"id", "title", "nodeState"},
	} {
		schema := specMap(t, schemas[name], name)
		got := specStrings(t, schema["required"], name+".required")
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s.required = %v, want %v", name, got, want)
		}
	}
	geo := specMap(t, schemas["CatalogGeo"], "CatalogGeo")
	current := specMap(t, specMap(t, geo["properties"], "CatalogGeo.properties")["current"], "CatalogGeo.current")
	if branches, _ := current["oneOf"].([]any); len(branches) == 0 {
		t.Error("CatalogGeo.current is not nullable; an account with no LIVE row serializes null")
	}

	// The catalog read accepts the province/city filters the handler parses.
	servers := specMap(t, specDig(t, doc, "paths", "/v1/accounts/{id}/servers", "get"), "servers operation")
	params := map[string]map[string]any{}
	parameters, _ := servers["parameters"].([]any)
	for _, raw := range parameters {
		param, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name, _ := param["name"].(string)
		params[name] = param
	}
	for _, name := range []string{"province", "city"} {
		if _, ok := params[name]; !ok {
			t.Errorf("GET /v1/accounts/{id}/servers does not document the %s filter", name)
		}
	}
	if description, _ := params["province"]["description"].(string); !strings.Contains(description, "00") {
		t.Error("the province filter does not name the 00 unresolved bucket")
	}

	// ttlSeconds: 0 is the sticky-TTL sentinel, 1-59 is refused.
	leaseInput := specMap(t, schemas["CreateLeaseInput"], "CreateLeaseInput")
	ttl := specMap(t, specMap(t, leaseInput["properties"], "CreateLeaseInput.properties")["ttlSeconds"], "ttlSeconds")
	branches, _ := ttl["oneOf"].([]any)
	if len(branches) != 2 {
		t.Fatalf("ttlSeconds.oneOf has %d branches, want 2", len(branches))
	}
	zero := specMap(t, branches[0], "ttlSeconds.oneOf[0]")
	window := specMap(t, branches[1], "ttlSeconds.oneOf[1]")
	if zero["const"] != float64(0) || window["minimum"] != float64(60) || window["maximum"] != float64(604800) {
		t.Errorf("ttlSeconds branches = %v / %v, want const 0 and 60..604800", zero, window)
	}

	// The envelope enum is exactly the code set the package can write.
	detail := specMap(t, schemas["ErrorDetail"], "ErrorDetail")
	codeSchema := specMap(t, specMap(t, detail["properties"], "ErrorDetail.properties")["code"], "ErrorDetail.code")
	documented := specStrings(t, codeSchema["enum"], "ErrorDetail.code.enum")
	emitted, err := emittedErrorCodes(".")
	if err != nil {
		t.Fatalf("scan package sources: %v", err)
	}
	sort.Strings(documented)
	sort.Strings(emitted)
	if !reflect.DeepEqual(documented, emitted) {
		t.Errorf("ErrorDetail.code enum drifted from the daemon\n documented: %v\n   emitted: %v", documented, emitted)
	}

	// 412 is emitted by the conditional schedule patch and the waited switch.
	for _, route := range [][2]string{
		{"/v1/schedules/{id}", "patch"},
		{"/v1/accounts/{id}/actions/select-node", "post"},
	} {
		operation := specMap(t, specDig(t, doc, "paths", route[0], route[1]), route[0])
		responses := specMap(t, operation["responses"], route[0]+".responses")
		precondition, ok := responses["412"].(map[string]any)
		if !ok || precondition["$ref"] != "#/components/responses/PreconditionFailed" {
			t.Errorf("%s %s does not document the 412 branch", route[1], route[0])
		}
	}
	if _, ok := specLookup(doc, "components", "responses", "PreconditionFailed"); !ok {
		t.Error("components.responses.PreconditionFailed is missing")
	}

	// Minting a key for an unknown user answers 404, not 400.
	createKey := specMap(t, specDig(t, doc, "paths", "/v1/scoped-keys", "post"), "createScopedKey")
	createResponses := specMap(t, createKey["responses"], "createScopedKey.responses")
	if missing, ok := createResponses["404"].(map[string]any); !ok || missing["$ref"] != "#/components/responses/NotFound" {
		t.Error("POST /v1/scoped-keys does not document the unknown-user 404")
	}
	for _, method := range []string{"get", "delete"} {
		operation := specMap(t, specDig(t, doc, "paths", "/v1/scoped-keys/{id}", method), "scoped key "+method)
		responses := specMap(t, operation["responses"], "scoped key "+method+".responses")
		if missing, ok := responses["404"].(map[string]any); !ok || missing["$ref"] != "#/components/responses/NotFound" {
			t.Errorf("%s /v1/scoped-keys/{id} does not document the 404", method)
		}
	}
}

// emittedErrorCodes derives the envelope codes from the package source so the
// document cannot silently drift from the daemon in either direction.
func emittedErrorCodes(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		source, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		for _, pattern := range specCodeSources {
			for _, match := range pattern.FindAllStringSubmatch(string(source), -1) {
				seen[match[1]] = true
			}
		}
	}
	codes := make([]string, 0, len(seen))
	for code := range seen {
		codes = append(codes, code)
	}
	return codes, nil
}

func specContainsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// TestOpenAPIJourneyExamples proves the worked examples exist and are valid
// against the schemas they illustrate, so a generator can copy them.
func TestOpenAPIJourneyExamples(t *testing.T) {
	var doc map[string]any
	if err := json.Unmarshal(embeddedOpenAPISpec, &doc); err != nil {
		t.Fatalf("decode embedded spec: %v", err)
	}

	cases := []struct {
		method string
		path   string
		where  string
	}{
		{"post", "/v1/scoped-keys", "request"},
		{"post", "/v1/scoped-keys", "201"},
		{"get", "/v1/nodes", "200"},
		{"post", "/v1/leases", "request"},
		{"post", "/v1/leases", "201"},
		{"get", "/v1/leases/{id}", "200"},
		{"post", "/v1/accounts/{id}/actions/connect", "request"},
		{"post", "/v1/accounts/{id}/actions/connect", "200"},
		{"post", "/v1/accounts/{id}/actions/select-node", "202"},
		{"get", "/v1/usage", "200"},
	}
	for _, testCase := range cases {
		operation := specMap(t, specDig(t, doc, "paths", testCase.path, testCase.method),
			testCase.method+" "+testCase.path)
		var media map[string]any
		if testCase.where == "request" {
			media = specMap(t, specDig(t, operation, "requestBody", "content", "application/json"),
				testCase.method+" "+testCase.path+" request")
		} else {
			media = specMap(t, specDig(t, operation, "responses", testCase.where, "content", "application/json"),
				testCase.method+" "+testCase.path+" "+testCase.where)
		}
		schema := specMap(t, specDig(t, media, "schema"), testCase.method+" "+testCase.path+" schema")
		examples := specMap(t, specDig(t, media, "examples"), testCase.method+" "+testCase.path+" examples")
		if len(examples) == 0 {
			t.Errorf("%s %s %s has no example", testCase.method, testCase.path, testCase.where)
			continue
		}
		for name, raw := range examples {
			example := specMap(t, raw, "example "+name)
			if summary, _ := example["summary"].(string); summary == "" {
				t.Errorf("%s %s %s example %s has no summary", testCase.method, testCase.path, testCase.where, name)
			}
			value, ok := example["value"]
			if !ok {
				t.Errorf("%s %s %s example %s has no value", testCase.method, testCase.path, testCase.where, name)
				continue
			}
			for _, violation := range validateSpecExample(doc, value, schema, "$") {
				t.Errorf("%s %s %s example %s: %s", testCase.method, testCase.path, testCase.where, name, violation)
			}
		}
	}

	// The queued operation example is the nullability contract in one object.
	queued := specMap(t, specDig(t, doc, "paths", "/v1/accounts/{id}/actions/select-node", "post",
		"responses", "202", "content", "application/json", "examples"), "select-node examples")
	for name, raw := range queued {
		value := specMap(t, specMap(t, raw, "example "+name)["value"], "example "+name+" value")
		if value["result"] != nil || value["error"] != nil {
			t.Errorf("queued example %s does not serialize result/error as null", name)
		}
	}

	// Both scoped-key denial reasons are worked examples on the shared response.
	forbidden := specMap(t, specDig(t, doc, "components", "responses", "ScopedKeyForbidden",
		"content", "application/json", "examples"), "ScopedKeyForbidden examples")
	if len(forbidden) != 2 {
		t.Fatalf("ScopedKeyForbidden has %d examples, want one per reason", len(forbidden))
	}
	reasons := map[string]bool{}
	for _, raw := range forbidden {
		value := specMap(t, specMap(t, raw, "forbidden example")["value"], "forbidden example value")
		errorDetail := specMap(t, value["error"], "error")
		reason, _ := errorDetail["reason"].(string)
		reasons[reason] = true
	}
	for _, reason := range []string{ReasonScopeRequired, ReasonScopedKeyRestricted} {
		if !reasons[reason] {
			t.Errorf("ScopedKeyForbidden has no %s example", reason)
		}
	}
}

// validateSpecExample checks one example against the JSON Schema subset the
// document uses and returns every violation.
func validateSpecExample(doc map[string]any, value any, schema map[string]any, at string) []string {
	if ref, ok := schema["$ref"].(string); ok {
		target, found := specLookup(doc, strings.Split(strings.TrimPrefix(ref, "#/"), "/")...)
		if !found {
			return []string{at + ": unresolved " + ref}
		}
		resolved, ok := target.(map[string]any)
		if !ok {
			return []string{at + ": " + ref + " is not a schema"}
		}
		return validateSpecExample(doc, value, resolved, at)
	}
	if branches, ok := schema["oneOf"].([]any); ok {
		matched := 0
		var firstViolation []string
		for _, raw := range branches {
			branch, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			violations := validateSpecExample(doc, value, branch, at)
			if len(violations) == 0 {
				matched++
				continue
			}
			if firstViolation == nil {
				firstViolation = violations
			}
		}
		if matched == 0 {
			return append([]string{at + ": matches no oneOf branch"}, firstViolation...)
		}
		return nil
	}

	var violations []string
	if kind, _ := schema["type"].(string); kind != "" {
		switch kind {
		case "object":
			object, ok := value.(map[string]any)
			if !ok {
				return []string{fmt.Sprintf("%s: want object, got %T", at, value)}
			}
			if required, ok := schema["required"].([]any); ok {
				for _, raw := range required {
					name, _ := raw.(string)
					if _, present := object[name]; !present {
						violations = append(violations, at+": missing required "+name)
					}
				}
			}
			properties, _ := schema["properties"].(map[string]any)
			for name, field := range object {
				fieldSchema, known := properties[name].(map[string]any)
				if !known {
					if additional, ok := schema["additionalProperties"].(bool); ok && !additional {
						violations = append(violations, at+": unexpected property "+name)
					}
					continue
				}
				violations = append(violations, validateSpecExample(doc, field, fieldSchema, at+"."+name)...)
			}
			return violations
		case "array":
			items, ok := value.([]any)
			if !ok {
				return []string{fmt.Sprintf("%s: want array, got %T", at, value)}
			}
			itemSchema, _ := schema["items"].(map[string]any)
			for index, item := range items {
				violations = append(violations, validateSpecExample(doc, item, itemSchema,
					fmt.Sprintf("%s[%d]", at, index))...)
			}
			return violations
		case "string":
			if _, ok := value.(string); !ok {
				return []string{fmt.Sprintf("%s: want string, got %T", at, value)}
			}
		case "boolean":
			if _, ok := value.(bool); !ok {
				return []string{fmt.Sprintf("%s: want boolean, got %T", at, value)}
			}
		case "integer":
			number, ok := value.(float64)
			if !ok || number != math.Trunc(number) {
				return []string{fmt.Sprintf("%s: want integer, got %v", at, value)}
			}
		case "number":
			if _, ok := value.(float64); !ok {
				return []string{fmt.Sprintf("%s: want number, got %T", at, value)}
			}
		case "null":
			if value != nil {
				return []string{fmt.Sprintf("%s: want null, got %v", at, value)}
			}
		}
	}
	if choices, ok := schema["enum"].([]any); ok {
		matched := false
		for _, choice := range choices {
			if reflect.DeepEqual(value, choice) {
				matched = true
				break
			}
		}
		if !matched {
			violations = append(violations, fmt.Sprintf("%s: %v is not in the enum", at, value))
		}
	}
	if expected, ok := schema["const"]; ok && !reflect.DeepEqual(value, expected) {
		violations = append(violations, fmt.Sprintf("%s: want const %v", at, expected))
	}
	if number, ok := value.(float64); ok {
		if minimum, ok := schema["minimum"].(float64); ok && number < minimum {
			violations = append(violations, fmt.Sprintf("%s: %v is below the minimum %v", at, number, minimum))
		}
		if maximum, ok := schema["maximum"].(float64); ok && number > maximum {
			violations = append(violations, fmt.Sprintf("%s: %v is above the maximum %v", at, number, maximum))
		}
	}
	return violations
}
