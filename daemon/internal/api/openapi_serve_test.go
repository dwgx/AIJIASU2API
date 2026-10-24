package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
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
	for _, header := range []string{"Content-Disposition", "ETag", "X-Truncated"} {
		if !strings.Contains(exposed, header) {
			t.Fatalf("expose headers = %q, missing %s", exposed, header)
		}
	}
}
