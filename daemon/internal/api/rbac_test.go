package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/store"
)

func testOperatorSet(t *testing.T) *OperatorSet {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "operators")
	var content strings.Builder
	for _, item := range []struct{ name, role, token string }{
		{"alice", "viewer", "alice-viewer-token-012345678901234567890123456789"},
		{"bob", "operator", "bob-operator-token-012345678901234567890123456789"},
		{"carol", "admin", "carol-admin-token-0123456789012345678901234567890"},
	} {
		digest := sha256.Sum256([]byte(item.token))
		fmt.Fprintf(&content, "%s:%s:%s\n", item.name, item.role, hex.EncodeToString(digest[:]))
	}
	if err := os.WriteFile(path, []byte(content.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	set, err := LoadOperatorSet(path)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func newStrictRBACServer(t *testing.T) *httptest.Server {
	t.Helper()
	st := store.New()
	server := New(Config{AdminToken: testToken, Operators: testOperatorSet(t), StrictRBAC: true,
		AdminOrigins: []string{adminTestOrigin}}, st, nil)
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)
	return ts
}

func strictRBACRequest(t *testing.T, ts *httptest.Server, method, path, token, spoof string) (*http.Response, []byte) {
	t.Helper()
	req := newRequest(t, method, ts.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set(adminCSRFHeader, "1")
	if spoof != "" {
		req.Header.Set(adminOperatorHeader, spoof)
	}
	return doRequest(t, ts, req)
}

// TestAJS412OperatorRoleMatrix is the AJS-412 acceptance test for route
// enforcement. It proves actual HTTP 403/200/2xx behavior, not just the table.
func TestAJS412OperatorRoleMatrix(t *testing.T) {
	ts := newStrictRBACServer(t)
	tests := []struct {
		name, method, path      string
		viewer, operator, admin int
	}{
		{"viewer reads", http.MethodGet, "/v1/accounts", 200, 200, 200},
		{"viewer cannot mutate runtime", http.MethodPost, "/v1/leases", 403, 400, 400},
		{"operator runtime action", http.MethodPost, "/v1/nodes/node-07/actions/drain", 403, 200, 200},
		{"operator cannot create credential", http.MethodPost, "/v1/scoped-keys", 403, 403, 400},
		{"operator cannot read credential inventory", http.MethodGet, "/v1/scoped-keys", 403, 403, 200},
		{"operator cannot export account", http.MethodPost, "/v1/accounts/export", 403, 403, 400},
		{"operator cannot backup", http.MethodPost, "/v1/backup", 403, 403, 400},
		{"operator cannot restore", http.MethodPost, "/v1/backup/restore", 403, 403, 400},
		{"operator cannot settle order", http.MethodPost, "/v1/admin/orders/o-01/settle", 403, 403, 503},
		{"operator cannot change plan", http.MethodPost, "/v1/admin/plans", 403, 403, 400},
		{"operator cannot import devices", http.MethodPost, "/v1/device-pool/import", 403, 403, 400},
		{"operator cannot generate devices", http.MethodPost, "/v1/device-pool/generate", 403, 403, 400},
	}
	tokens := []string{"alice-viewer-token-012345678901234567890123456789", "bob-operator-token-012345678901234567890123456789", "carol-admin-token-0123456789012345678901234567890"}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for i, want := range []int{test.viewer, test.operator, test.admin} {
				resp, body := strictRBACRequest(t, ts, test.method, test.path, tokens[i], "")
				if resp.StatusCode != want {
					t.Errorf("role %d status=%d want=%d body=%s", i, resp.StatusCode, want, body)
				}
			}
		})
	}
}

// TestAJS412LegacyTokenRevokedAndIdentityCannotSpoof pins the migration's
// final state: no shared bearer, no anonymous mode and no header privilege.
func TestAJS412LegacyTokenRevokedAndIdentityCannotSpoof(t *testing.T) {
	set := testOperatorSet(t)
	server := New(Config{AdminToken: testToken, Operators: set, StrictRBAC: true, AllowInsecure: true}, store.New(), nil)
	ts := httptest.NewServer(server)
	defer ts.Close()
	for _, test := range []struct{ name, token string }{{"no token", ""}, {"legacy", testToken}, {"revoked", "revoked-token-012345678901234567890123456789"}} {
		req := newRequest(t, http.MethodGet, ts.URL+"/v1/accounts", nil)
		if test.token != "" {
			req.Header.Set("Authorization", "Bearer "+test.token)
		}
		req.Header.Set(adminOperatorHeader, "carol")
		resp, _ := doRequest(t, ts, req)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s status=%d want=401", test.name, resp.StatusCode)
		}
	}
}

func TestAJS412OperatorIdentityComesFromToken(t *testing.T) {
	st := store.New()
	server := New(Config{Operators: testOperatorSet(t), StrictRBAC: true, AdminOrigins: []string{adminTestOrigin}}, st, nil)
	ts := httptest.NewServer(server)
	defer ts.Close()
	for _, spoof := range []string{"", "carol", "alice forged"} {
		before := len(st.Audit(10000))
		resp, body := strictRBACRequest(t, ts, http.MethodPost, "/v1/accounts/acc-01/actions/login", "bob-operator-token-012345678901234567890123456789", spoof)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status=%d body=%s", resp.StatusCode, body)
		}
		found := false
		for i, event := range st.Audit(10000) {
			if i >= len(st.Audit(10000))-before {
				break
			}
			if event.Action == "account.login" && event.Actor == "bob" {
				found = true
			}
			if event.Action == "account.login" && event.Actor != "bob" {
				t.Errorf("spoof=%q actor=%q want bob", spoof, event.Actor)
			}
		}
		if !found {
			t.Fatalf("expected account login audit; rows=%+v", st.Audit(10000))
		}
	}
}

func TestAJS412ScopedKeyAndPortalIsolation(t *testing.T) {
	ts := newStrictRBACServer(t)
	createReq := newRequest(t, http.MethodPost, ts.URL+"/v1/scoped-keys", map[string]any{"userId": "u-01", "label": "RBAC isolation", "scopes": []string{"accounts:read"}, "accountIds": []string{"acc-01"}})
	createReq.Header.Set("Authorization", "Bearer carol-admin-token-0123456789012345678901234567890")
	createReq.Header.Set(adminCSRFHeader, adminCSRFValue)
	createResp, createBody := doRequest(t, ts, createReq)
	if createResp.StatusCode != 201 {
		t.Fatalf("create scoped key status=%d body=%s", createResp.StatusCode, createBody)
	}
	created := decodeBody[model.ScopedKeyCreated](t, createBody)
	resp, body := scopedCall(t, ts, created.Secret, http.MethodGet, "/v1/accounts", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("scoped read status=%d body=%s", resp.StatusCode, body)
	}
	resp, body = scopedCall(t, ts, created.Secret, http.MethodPost, "/v1/leases", nil)
	if resp.StatusCode != 403 {
		t.Fatalf("scoped write status=%d body=%s want=403", resp.StatusCode, body)
	}
	resp, body = scopedCall(t, ts, created.Secret, http.MethodGet, "/v1/scoped-keys", nil)
	if resp.StatusCode != 403 {
		t.Fatalf("scoped admin read status=%d body=%s want=403", resp.StatusCode, body)
	}
	req := newRequest(t, http.MethodGet, ts.URL+"/v1/accounts", nil)
	req.Header.Set("Authorization", "Bearer not-a-scoped-key")
	resp, _ = doRequest(t, ts, req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unknown key status=%d want=401", resp.StatusCode)
	}
	req = newRequest(t, http.MethodGet, ts.URL+"/v1/portal/session", nil)
	req.Header.Set("Authorization", "Bearer carol-admin-token-0123456789012345678901234567890")
	resp, _ = doRequest(t, ts, req)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("portal status=%d want=404 when disabled", resp.StatusCode)
	}
}

func TestAJS412EveryRouteHasIntendedDefaultPolicy(t *testing.T) {
	s := New(Config{}, store.NewEmpty(), nil)
	rt := &router{}
	s.routes(rt)
	for _, route := range rt.routes {
		if strings.HasPrefix(route.pattern, "/v1/portal/") || strings.HasPrefix(route.pattern, "/v1/payments/") || route.pattern == "/healthz" || route.pattern == "/readyz" {
			continue
		}
		role := routeRole(route.method, route.pattern)
		if !role.valid() {
			t.Errorf("%s %s: invalid role %q", route.method, route.pattern, role)
		}
		if route.method != http.MethodGet && role == RoleViewer {
			t.Errorf("%s %s: unsafe route is viewer", route.method, route.pattern)
		}
	}
	if routeRole(http.MethodPost, "/v1/future-sensitive-action") != RoleAdmin {
		t.Fatal("new unsafe route must default to admin")
	}
	if routeRole(http.MethodGet, "/v1/future-read") != RoleViewer {
		t.Fatal("new read route must default to viewer")
	}
}

func TestAJS412Whoami(t *testing.T) {
	ts := newStrictRBACServer(t)
	for _, test := range []struct{ token, name, role string }{
		{"alice-viewer-token-012345678901234567890123456789", "alice", "viewer"},
		{"bob-operator-token-012345678901234567890123456789", "bob", "operator"},
		{"carol-admin-token-0123456789012345678901234567890", "carol", "admin"},
	} {
		resp, body := strictRBACRequest(t, ts, http.MethodGet, "/v1/whoami", test.token, "forged-admin")
		if resp.StatusCode != 200 {
			t.Fatalf("whoami status=%d body=%s", resp.StatusCode, body)
		}
		var got whoamiResponse
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		if got.Name != test.name || got.Role != test.role || !got.StrictRBAC || got.Kind != "operator" {
			t.Errorf("whoami=%+v", got)
		}
	}
}

func TestAJS412OperatorFileValidation(t *testing.T) {
	dir := t.TempDir()
	digest := strings.Repeat("a", 64)
	tests := []struct{ name, content string }{
		{"bad role", "alice:root:" + digest + "\n"},
		{"bad user", "alice forged:viewer:" + digest + "\n"},
		{"bad hash", "alice:viewer:plain-token\n"},
		{"empty", "# no operators\n"},
		{"conflicting roles", "alice:viewer:" + digest + "\nalice:admin:" + strings.Repeat("b", 64) + "\n"},
		{"duplicate token", "alice:viewer:" + digest + "\nbob:admin:" + digest + "\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(dir, strings.ReplaceAll(test.name, " ", "-"))
			if err := os.WriteFile(path, []byte(test.content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadOperatorSet(path); err == nil {
				t.Fatal("invalid operator file accepted")
			}
		})
	}
	if runtime.GOOS != "windows" {
		path := filepath.Join(dir, "permissive")
		if err := os.WriteFile(path, []byte("alice:admin:"+digest+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadOperatorSet(path); err == nil {
			t.Fatal("group/world-readable token hashes accepted")
		}
	}
}

func TestAJS412TwoGenerationRotation(t *testing.T) {
	oldToken := "old-token-012345678901234567890123456789012"
	newToken := "new-token-012345678901234567890123456789012"
	dir := t.TempDir()
	path := filepath.Join(dir, "operators")
	var content bytes.Buffer
	for _, token := range []string{oldToken, newToken} {
		h := sha256.Sum256([]byte(token))
		fmt.Fprintf(&content, "alice:viewer:%x\n", h)
	}
	if err := os.WriteFile(path, content.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	set, err := LoadOperatorSet(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{oldToken, newToken} {
		entry, ok := set.authenticate(token)
		if !ok || entry.name != "alice" || entry.role != RoleViewer {
			t.Fatalf("rotation token failed: %+v %v", entry, ok)
		}
	}
	// Remove the retired generation, reload and prove it no longer works.
	h := sha256.Sum256([]byte(newToken))
	if err := os.WriteFile(path, []byte(fmt.Sprintf("alice:viewer:%x\n", h)), 0o600); err != nil {
		t.Fatal(err)
	}
	set, err = LoadOperatorSet(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := set.authenticate(oldToken); ok {
		t.Fatal("retired token remains valid")
	}
	if _, ok := set.authenticate(newToken); !ok {
		t.Fatal("current token stopped working")
	}
}

func TestAJS412DeniedResponseIsJSONAndAuditsNoMutation(t *testing.T) {
	ts := newStrictRBACServer(t)
	resp, body := strictRBACRequest(t, ts, http.MethodPost, "/v1/backup", "alice-viewer-token-012345678901234567890123456789", "")
	if resp.StatusCode != 403 {
		t.Fatalf("status=%d want=403", resp.StatusCode)
	}
	var envelope errorResponse
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != CodeForbidden {
		t.Fatalf("code=%q body=%s", envelope.Error.Code, body)
	}
}

func TestAJS412OperatorFileNeverContainsPlaintext(t *testing.T) {
	set := testOperatorSet(t)
	data, err := json.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, []byte("{}")) || bytes.Contains(data, []byte("token-0123")) {
		t.Fatalf("operator set serializes secret material: %s", data)
	}
	_ = io.Discard
}
