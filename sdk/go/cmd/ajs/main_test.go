package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	ajs "ajssdk"
)

const testKey = "ajs_sk_test_key_not_a_secret"

func runCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// clearEnv removes the fallbacks so a developer's shell cannot steer a test.
func clearEnv(t *testing.T) {
	t.Helper()
	t.Setenv(envURL, "")
	t.Setenv(envKey, "")
}

// recorder captures what the CLI actually sent. The handler runs on the server
// goroutine, so every read takes the lock.
type recorder struct {
	mu    sync.Mutex
	paths []string
	auth  []string
	uas   []string
	query []url.Values
}

func (r *recorder) record(req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.paths = append(r.paths, req.URL.Path)
	r.auth = append(r.auth, req.Header.Get("Authorization"))
	r.uas = append(r.uas, req.Header.Get("User-Agent"))
	r.query = append(r.query, req.URL.Query())
}

func (r *recorder) count(path string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, p := range r.paths {
		if p == path {
			n++
		}
	}
	return n
}

// lastAuth returns the Authorization header of the most recent request to path.
func (r *recorder) lastAuth(path string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.paths) - 1; i >= 0; i-- {
		if r.paths[i] == path {
			return r.auth[i]
		}
	}
	return ""
}

func (r *recorder) lastUA(path string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.paths) - 1; i >= 0; i-- {
		if r.paths[i] == path {
			return r.uas[i]
		}
	}
	return ""
}

func (r *recorder) lastQuery(path string) url.Values {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.paths) - 1; i >= 0; i-- {
		if r.paths[i] == path {
			return r.query[i]
		}
	}
	return nil
}

func newServer(t *testing.T, rec *recorder, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func writeBody(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func healthy(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/healthz":
		writeBody(w, 200, `{"status":"ok","service":"ajs-accountd","version":"0.1.0"}`)
	case "/readyz":
		writeBody(w, 200, `{"status":"ready","service":"ajs-accountd","version":"0.1.0","checks":[]}`)
	default:
		http.NotFound(w, r)
	}
}

const nodesJSON = `[
 {"id":"n-b","accountId":"acc-1","accountLabel":"primary","label":"beta","region":"jp",
  "state":"LIVE","latencyMs":80,"successRate":0.98,"inUse":1,"capacity":3,
  "egressIp":"203.0.113.2","lastProbeAt":"2026-10-29T00:00:00Z"},
 {"id":"n-a","accountId":"acc-1","accountLabel":"primary","label":"Alpha","region":"us",
  "state":"LIVE","latencyMs":40,"successRate":0.9,"inUse":0,"capacity":2,
  "egressIp":"203.0.113.1","lastProbeAt":"2026-10-29T00:00:00Z"},
 {"id":"n-c","accountId":"acc-2","accountLabel":"secondary","label":"gamma","region":"cn",
  "state":"READY","latencyMs":0,"successRate":0,"inUse":0,"capacity":1,
  "egressIp":"","lastProbeAt":""}
]`

const leasesJSON = `[
 {"id":"lease-1","userId":"usr-1","userName":"alice","accountId":"acc-1",
  "accountLabel":"primary","platform":"linux","nodeId":"n-a","nodeLabel":"Alpha",
  "region":"us","egressIp":"203.0.113.1","protocol":"socks5","gatePort":19001,
  "ttlSeconds":3600,"expiresAt":"2026-10-30T00:00:00Z","status":"active"},
 {"id":"lease-2","userId":"usr-2","accountId":"acc-2","platform":"windows",
  "nodeId":"n-b","region":"jp","gatePort":19002,
  "expiresAt":"2026-10-30T01:00:00Z","status":"expired"}
]`

const leaseDetailJSON = `{
 "lease":{"id":"lease-1","userId":"usr-1","userName":"alice","accountId":"acc-1",
  "accountLabel":"primary","platform":"linux","nodeId":"n-a","nodeLabel":"Alpha",
  "region":"us","egressIp":"203.0.113.1","protocol":"socks5","gatePort":19001,
  "ttlSeconds":3600,"expiresAt":"2026-10-30T00:00:00Z","requests":7,"bytes":2048,
  "status":"active"},
 "connection":{"host":"127.0.0.1","port":19001,"protocol":"socks5",
  "username":"usr-1","credentialKind":"ajs_sk"}
}`

// assertOrder checks that ids appear in the given order, each after the last.
func assertOrder(t *testing.T, out string, ids []string) {
	t.Helper()
	pos := -1
	for _, id := range ids {
		i := strings.Index(out, id)
		if i < 0 {
			t.Fatalf("%q missing from output:\n%s", id, out)
		}
		if i <= pos {
			t.Fatalf("%q out of order in output:\n%s", id, out)
		}
		pos = i
	}
}

func rowFor(t *testing.T, out, id string) []string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, id) {
			return strings.Fields(line)
		}
	}
	t.Fatalf("row %q missing from output:\n%s", id, out)
	return nil
}

func TestStatusPasses(t *testing.T) {
	clearEnv(t)
	rec := &recorder{}
	srv := newServer(t, rec, healthy)

	code, stdout, stderr := runCLI(t, "status", "--url", srv.URL)
	if code != exitOK {
		t.Fatalf("exit = %d, want %d (stderr %q)", code, exitOK, stderr)
	}
	for _, want := range []string{"healthz", "readyz", "200", "PASS", "0.1.0", "endpoint " + srv.URL} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, stdout)
		}
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if rec.count("/healthz") != 1 || rec.count("/readyz") != 1 {
		t.Errorf("probes = healthz %d, readyz %d, want 1 each", rec.count("/healthz"), rec.count("/readyz"))
	}
	if got := rec.lastAuth("/healthz"); got != "" {
		t.Errorf("status sent Authorization %q, want none", got)
	}
	if got := rec.lastUA("/readyz"); !strings.HasPrefix(got, "ajs-cli/") {
		t.Errorf("User-Agent = %q", got)
	}
}

func TestStatusFailsOnNotReady(t *testing.T) {
	clearEnv(t)
	rec := &recorder{}
	srv := newServer(t, rec, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/readyz" {
			writeBody(w, 503, `{"status":"unready","reasons":["state_not_writable"]}`)
			return
		}
		writeBody(w, 200, `{"status":"ok","version":"0.1.0"}`)
	})

	code, stdout, stderr := runCLI(t, "status", "--url", srv.URL)
	if code != exitRuntime {
		t.Fatalf("exit = %d, want %d", code, exitRuntime)
	}
	for _, want := range []string{"readyz", "503", "FAIL", "state_not_writable"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, stdout)
		}
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
}

func TestStatusReadyzOutcomes(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		wantExit   int
		wantOK     bool
		wantSkip   bool
		wantResult string
	}{
		{"ready", 200, exitOK, true, false, "PASS"},
		{"not exposed", 404, exitOK, true, true, "SKIP"},
		{"server error", 500, exitRuntime, false, false, "FAIL"},
		{"unauthorized", 401, exitRuntime, false, false, "FAIL"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			rec := &recorder{}
			srv := newServer(t, rec, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/readyz" {
					writeBody(w, tc.status, `{"status":"probe"}`)
					return
				}
				writeBody(w, 200, `{"status":"ok","version":"0.1.0"}`)
			})

			code, stdout, stderr := runCLI(t, "status", "--url", srv.URL, "--json")
			if code != tc.wantExit {
				t.Fatalf("exit = %d, want %d (stderr %q)", code, tc.wantExit, stderr)
			}
			var report statusReport
			if err := json.Unmarshal([]byte(stdout), &report); err != nil {
				t.Fatalf("decode: %v\n%s", err, stdout)
			}
			if report.OK != tc.wantOK {
				t.Errorf("report.ok = %v, want %v", report.OK, tc.wantOK)
			}
			ready := report.Checks[1]
			if ready.Status != tc.status || ready.Skipped != tc.wantSkip || ready.OK != tc.wantOK {
				t.Errorf("readyz check = %+v", ready)
			}

			code, stdout, _ = runCLI(t, "status", "--url", srv.URL)
			if code != tc.wantExit {
				t.Fatalf("table exit = %d, want %d", code, tc.wantExit)
			}
			row := strings.Join(rowFor(t, stdout, "readyz"), " ")
			if !strings.Contains(row, tc.wantResult) {
				t.Errorf("readyz row = %q, want %s\n%s", row, tc.wantResult, stdout)
			}
			if tc.wantSkip != strings.Contains(stdout, "not exposed at this edge") {
				t.Errorf("skip note mismatch:\n%s", stdout)
			}
		})
	}
}

func TestStatusRequiresHealthz(t *testing.T) {
	clearEnv(t)
	rec := &recorder{}
	srv := newServer(t, rec, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			writeBody(w, 404, `{"error":{"code":"NOT_FOUND"}}`)
			return
		}
		writeBody(w, 200, `{"status":"ready"}`)
	})

	code, stdout, _ := runCLI(t, "status", "--url", srv.URL, "--json")
	if code != exitRuntime {
		t.Fatalf("exit = %d, want %d", code, exitRuntime)
	}
	var report statusReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("decode: %v\n%s", err, stdout)
	}
	if report.OK || report.Checks[0].OK || report.Checks[0].Skipped {
		t.Fatalf("a 404 on /healthz must fail: %+v", report.Checks[0])
	}
}

func TestStatusJSON(t *testing.T) {
	clearEnv(t)
	rec := &recorder{}
	srv := newServer(t, rec, healthy)

	code, stdout, stderr := runCLI(t, "status", "--url", srv.URL, "--json")
	if code != exitOK {
		t.Fatalf("exit = %d, want %d (stderr %q)", code, exitOK, stderr)
	}
	var report statusReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("decode report: %v\n%s", err, stdout)
	}
	if !report.OK || report.URL != srv.URL || report.Version != "0.1.0" {
		t.Fatalf("report = %+v", report)
	}
	if len(report.Checks) != 2 || report.Checks[0].Name != "healthz" || report.Checks[1].Name != "readyz" {
		t.Fatalf("checks = %+v", report.Checks)
	}
	if report.Checks[0].Body == nil || report.Checks[0].Status != 200 || !report.Checks[0].OK {
		t.Fatalf("healthz check = %+v", report.Checks[0])
	}
}

func TestStatusUnreachableIsNonZero(t *testing.T) {
	clearEnv(t)
	code, stdout, stderr := runCLI(t, "status", "--url", "http://127.0.0.1:1", "--json")
	if code != exitRuntime {
		t.Fatalf("exit = %d, want %d (stderr %q)", code, exitRuntime, stderr)
	}
	var report statusReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("decode report: %v\n%s", err, stdout)
	}
	if report.OK || len(report.Checks) != 2 {
		t.Fatalf("report = %+v", report)
	}
	for _, check := range report.Checks {
		if check.OK || check.Error == "" || check.Status != 0 {
			t.Errorf("check = %+v, want a transport failure", check)
		}
	}

	// The human table must survive the same failure without a panic.
	code, stdout, _ = runCLI(t, "status", "--url", "http://127.0.0.1:1")
	if code != exitRuntime {
		t.Fatalf("table exit = %d, want %d", code, exitRuntime)
	}
	if strings.Count(stdout, "FAIL") != 2 {
		t.Errorf("table did not report both failures:\n%s", stdout)
	}
}

func TestNodesTableSorts(t *testing.T) {
	clearEnv(t)
	rec := &recorder{}
	srv := newServer(t, rec, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/nodes" {
			http.NotFound(w, r)
			return
		}
		writeBody(w, 200, nodesJSON)
	})

	code, stdout, stderr := runCLI(t, "nodes", "--url", srv.URL, "--key", testKey, "--sort", "latency")
	if code != exitOK {
		t.Fatalf("exit = %d, want %d (stderr %q)", code, exitOK, stderr)
	}
	for _, header := range []string{"ID", "REGION", "LABEL", "STATE", "LATENCY", "SUCCESS", "INUSE/CAP", "EGRESS"} {
		if !strings.Contains(stdout, header) {
			t.Errorf("header missing %q:\n%s", header, stdout)
		}
	}
	assertOrder(t, stdout, []string{"n-a", "n-b", "n-c"})
	if got := rec.lastAuth("/v1/nodes"); got != "Bearer "+testKey {
		t.Errorf("Authorization = %q", got)
	}
	if strings.Contains(stdout+stderr, testKey) {
		t.Errorf("the key reached the output:\n%s%s", stdout, stderr)
	}

	measured := rowFor(t, stdout, "n-a")
	if len(measured) != 8 || measured[4] != "40ms" || measured[5] != "90%" || measured[6] != "0/2" {
		t.Errorf("n-a row = %v", measured)
	}
	unprobed := rowFor(t, stdout, "n-c")
	if len(unprobed) != 8 || unprobed[4] != "-" || unprobed[5] != "-" || unprobed[7] != "-" {
		t.Errorf("n-c row = %v", unprobed)
	}

	// label sort is case-insensitive: Alpha, beta, gamma.
	code, stdout, _ = runCLI(t, "nodes", "--url", srv.URL, "--key", testKey, "--sort", "label")
	if code != exitOK {
		t.Fatalf("label exit = %d", code)
	}
	assertOrder(t, stdout, []string{"n-a", "n-b", "n-c"})

	code, stdout, _ = runCLI(t, "nodes", "--url", srv.URL, "--key", testKey, "--sort", "region")
	if code != exitOK {
		t.Fatalf("region exit = %d", code)
	}
	assertOrder(t, stdout, []string{"n-c", "n-b", "n-a"})

	// Without --sort the daemon order is kept.
	code, stdout, _ = runCLI(t, "nodes", "--url", srv.URL, "--key", testKey)
	if code != exitOK {
		t.Fatalf("plain exit = %d", code)
	}
	assertOrder(t, stdout, []string{"n-b", "n-a", "n-c"})
}

func TestNodesJSONAndFilters(t *testing.T) {
	clearEnv(t)
	rec := &recorder{}
	srv := newServer(t, rec, func(w http.ResponseWriter, r *http.Request) {
		writeBody(w, 200, nodesJSON)
	})

	code, stdout, stderr := runCLI(t, "nodes", "--url", srv.URL, "--key", testKey,
		"--region", "jp", "--account", "acc-1", "--json")
	if code != exitOK {
		t.Fatalf("exit = %d, want %d (stderr %q)", code, exitOK, stderr)
	}
	var nodes []ajs.ProxyNode
	if err := json.Unmarshal([]byte(stdout), &nodes); err != nil {
		t.Fatalf("decode nodes: %v\n%s", err, stdout)
	}
	if len(nodes) != 3 || nodes[0].ID != "n-b" {
		t.Fatalf("nodes = %+v", nodes)
	}
	q := rec.lastQuery("/v1/nodes")
	if q.Get("region") != "jp" || q.Get("account_id") != "acc-1" {
		t.Errorf("query = %v", q)
	}
	if q.Has("limit") || q.Has("offset") || q.Has("state") {
		t.Errorf("unset filters reached the query: %v", q)
	}
}

func TestNodesRejectsUnknownSort(t *testing.T) {
	clearEnv(t)
	code, _, stderr := runCLI(t, "nodes", "--url", "http://127.0.0.1:1", "--key", testKey, "--sort", "bogus")
	if code != exitUsage {
		t.Fatalf("exit = %d, want %d", code, exitUsage)
	}
	for _, want := range []string{"bogus", "latency"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr missing %q: %s", want, stderr)
		}
	}
}

func TestNodesRequiresKey(t *testing.T) {
	clearEnv(t)
	code, _, stderr := runCLI(t, "nodes", "--url", "http://127.0.0.1:1")
	if code != exitUsage {
		t.Fatalf("exit = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, envKey) {
		t.Errorf("stderr missing %q: %s", envKey, stderr)
	}
}

func TestLeasesTableAndJSON(t *testing.T) {
	clearEnv(t)
	rec := &recorder{}
	srv := newServer(t, rec, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/leases" {
			http.NotFound(w, r)
			return
		}
		writeBody(w, 200, leasesJSON)
	})

	code, stdout, stderr := runCLI(t, "leases", "--url", srv.URL, "--key", testKey)
	if code != exitOK {
		t.Fatalf("exit = %d, want %d (stderr %q)", code, exitOK, stderr)
	}
	for _, header := range []string{"ID", "USER", "ACCOUNT", "REGION", "NODE", "GATE", "STATUS", "EXPIRES"} {
		if !strings.Contains(stdout, header) {
			t.Errorf("header missing %q:\n%s", header, stdout)
		}
	}
	first := rowFor(t, stdout, "lease-1")
	if len(first) != 8 || first[1] != "alice" || first[2] != "primary" || first[5] != "19001" {
		t.Errorf("lease-1 row = %v", first)
	}
	second := rowFor(t, stdout, "lease-2")
	if len(second) != 8 || second[1] != "usr-2" || second[2] != "acc-2" || second[4] != "n-b" {
		t.Errorf("lease-2 row = %v", second)
	}
	if got := rec.lastAuth("/v1/leases"); got != "Bearer "+testKey {
		t.Errorf("Authorization = %q", got)
	}

	code, stdout, stderr = runCLI(t, "leases", "--url", srv.URL, "--key", testKey, "--user", "usr-1", "--json")
	if code != exitOK {
		t.Fatalf("json exit = %d (stderr %q)", code, stderr)
	}
	var leases []ajs.Lease
	if err := json.Unmarshal([]byte(stdout), &leases); err != nil {
		t.Fatalf("decode leases: %v\n%s", err, stdout)
	}
	if len(leases) != 2 || leases[0].ID != "lease-1" {
		t.Fatalf("leases = %+v", leases)
	}
	if got := rec.lastQuery("/v1/leases").Get("user_id"); got != "usr-1" {
		t.Errorf("user_id = %q", got)
	}
}

func TestLeaseDetail(t *testing.T) {
	clearEnv(t)
	rec := &recorder{}
	srv := newServer(t, rec, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/leases/lease-1" {
			http.NotFound(w, r)
			return
		}
		writeBody(w, 200, leaseDetailJSON)
	})

	code, stdout, stderr := runCLI(t, "lease", "lease-1", "--url", srv.URL, "--key", testKey)
	if code != exitOK {
		t.Fatalf("exit = %d, want %d (stderr %q)", code, exitOK, stderr)
	}
	for _, want := range []string{"127.0.0.1", "19001", "socks5", "usr-1", "ajs_sk", "active"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout+stderr, testKey) {
		t.Errorf("the key reached the output:\n%s%s", stdout, stderr)
	}

	// Flags after the positional id are parsed on the second pass.
	code, stdout, stderr = runCLI(t, "lease", "lease-1", "--json", "--url", srv.URL, "--key", testKey)
	if code != exitOK {
		t.Fatalf("json exit = %d (stderr %q)", code, stderr)
	}
	var detail ajs.LeaseDetail
	if err := json.Unmarshal([]byte(stdout), &detail); err != nil {
		t.Fatalf("decode detail: %v\n%s", err, stdout)
	}
	if detail.Lease.ID != "lease-1" || detail.Connection.Host != "127.0.0.1" ||
		detail.Connection.Port != 19001 || detail.Connection.Username != "usr-1" {
		t.Fatalf("detail = %+v", detail)
	}
	if strings.Contains(stdout, "password") || strings.Contains(stdout, "secret") {
		t.Errorf("detail output mentions a credential field:\n%s", stdout)
	}
}

func TestLeaseRequiresID(t *testing.T) {
	clearEnv(t)
	code, _, stderr := runCLI(t, "lease", "--url", "http://127.0.0.1:1", "--key", testKey)
	if code != exitUsage {
		t.Fatalf("exit = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, "lease id is required") {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestDoctorPasses(t *testing.T) {
	clearEnv(t)
	rec := &recorder{}
	srv := newServer(t, rec, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/nodes" {
			writeBody(w, 200, `[{"id":"n-1","region":"jp","label":"one","state":"LIVE","latencyMs":30,
				"successRate":1,"inUse":0,"capacity":1,"egressIp":"203.0.113.9",
				"lastProbeAt":"2026-10-29T00:00:00Z"}]`)
			return
		}
		healthy(w, r)
	})

	code, stdout, stderr := runCLI(t, "doctor", "--url", srv.URL, "--key", testKey)
	if code != exitOK {
		t.Fatalf("exit = %d, want %d (stderr %q)", code, exitOK, stderr)
	}
	if got := strings.Count(stdout, "PASS"); got != 3 {
		t.Errorf("PASS count = %d, want 3:\n%s", got, stdout)
	}
	for _, want := range []string{"healthz", "readyz", "auth", "ListNodes ok (1 returned)"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, stdout)
		}
	}
	if got := rec.lastQuery("/v1/nodes").Get("limit"); got != "1" {
		t.Errorf("limit = %q, want 1", got)
	}
	if got := rec.lastAuth("/v1/nodes"); got != "Bearer "+testKey {
		t.Errorf("Authorization = %q", got)
	}
}

func TestDoctorFails(t *testing.T) {
	clearEnv(t)
	rec := &recorder{}
	srv := newServer(t, rec, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/readyz" {
			writeBody(w, 503, `{"status":"unready","reasons":["state_not_writable"]}`)
			return
		}
		if r.URL.Path == "/v1/nodes" {
			writeBody(w, 200, `[]`)
			return
		}
		writeBody(w, 200, `{"status":"ok","version":"0.1.0"}`)
	})

	code, stdout, _ := runCLI(t, "doctor", "--url", srv.URL, "--key", testKey)
	if code != exitRuntime {
		t.Fatalf("exit = %d, want %d", code, exitRuntime)
	}
	if !strings.Contains(stdout, "FAIL") || !strings.Contains(stdout, "state_not_writable") {
		t.Errorf("stdout:\n%s", stdout)
	}
	if strings.Count(stdout, "PASS") != 2 {
		t.Errorf("want two passing checks:\n%s", stdout)
	}
}

func TestDoctorSkipsUnexposedReadyz(t *testing.T) {
	clearEnv(t)
	rec := &recorder{}
	srv := newServer(t, rec, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/readyz":
			writeBody(w, 404, `{"error":{"code":"NOT_FOUND"}}`)
		case "/v1/nodes":
			writeBody(w, 200, `[]`)
		default:
			writeBody(w, 200, `{"status":"ok","version":"0.1.0"}`)
		}
	})

	code, stdout, stderr := runCLI(t, "doctor", "--url", srv.URL, "--key", testKey)
	if code != exitOK {
		t.Fatalf("exit = %d, want %d (stderr %q)", code, exitOK, stderr)
	}
	if !strings.Contains(stdout, "SKIP") || strings.Contains(stdout, "FAIL") {
		t.Errorf("stdout:\n%s", stdout)
	}
	if strings.Count(stdout, "PASS") != 2 {
		t.Errorf("want two passing checks:\n%s", stdout)
	}
	if !strings.Contains(stdout, "404, not exposed at this edge") {
		t.Errorf("stdout missing the skip detail:\n%s", stdout)
	}
}

func TestDoctorWithoutKeyFailsAuthOnly(t *testing.T) {
	clearEnv(t)
	rec := &recorder{}
	srv := newServer(t, rec, healthy)

	code, stdout, _ := runCLI(t, "doctor", "--url", srv.URL)
	if code != exitRuntime {
		t.Fatalf("exit = %d, want %d", code, exitRuntime)
	}
	if !strings.Contains(stdout, envKey) || !strings.Contains(stdout, "auth") {
		t.Errorf("stdout:\n%s", stdout)
	}
	if rec.count("/v1/nodes") != 0 {
		t.Errorf("auth check ran without a key")
	}
}

func TestAPIErrorNeverEchoesKey(t *testing.T) {
	clearEnv(t)
	rec := &recorder{}
	srv := newServer(t, rec, func(w http.ResponseWriter, r *http.Request) {
		writeBody(w, 401, `{"error":{"code":"UNAUTHORIZED","message":"bad key"}}`)
	})

	code, stdout, stderr := runCLI(t, "nodes", "--url", srv.URL, "--key", testKey)
	if code != exitRuntime {
		t.Fatalf("exit = %d, want %d", code, exitRuntime)
	}
	if !strings.Contains(stderr, "401") {
		t.Errorf("stderr missing the status: %q", stderr)
	}
	if strings.Contains(stdout+stderr, testKey) || strings.Contains(stdout+stderr, "Bearer") {
		t.Errorf("the key reached the output:\n%s%s", stdout, stderr)
	}
}

func TestURLUserinfoIsRedacted(t *testing.T) {
	clearEnv(t)
	rec := &recorder{}
	srv := newServer(t, rec, healthy)
	withCreds := "http://alice:hunter2@" + strings.TrimPrefix(srv.URL, "http://")

	code, stdout, stderr := runCLI(t, "status", "--url", withCreds)
	if code != exitOK {
		t.Fatalf("exit = %d, want %d (stderr %q)", code, exitOK, stderr)
	}
	combined := stdout + stderr
	for _, secret := range []string{"hunter2", "alice"} {
		if strings.Contains(combined, secret) {
			t.Errorf("URL userinfo %q reached the output:\n%s", secret, combined)
		}
	}
	if !strings.Contains(stderr, "ignoring credentials") {
		t.Errorf("stderr = %q, want a redaction note", stderr)
	}

	// A malformed URL must not echo the credentials it carried.
	code, stdout, stderr = runCLI(t, "status", "--url", "http://alice:hunter2@[::1")
	if code != exitUsage {
		t.Fatalf("exit = %d, want %d", code, exitUsage)
	}
	if strings.Contains(stdout+stderr, "hunter2") {
		t.Errorf("parse error echoed the password: %s%s", stdout, stderr)
	}
}

func TestEnvFallbacksAndFlagPrecedence(t *testing.T) {
	rec := &recorder{}
	srv := newServer(t, rec, func(w http.ResponseWriter, r *http.Request) {
		writeBody(w, 200, `[]`)
	})
	t.Setenv(envURL, srv.URL)
	t.Setenv(envKey, "env-key")

	code, _, stderr := runCLI(t, "nodes")
	if code != exitOK {
		t.Fatalf("exit = %d, want %d (stderr %q)", code, exitOK, stderr)
	}
	if got := rec.lastAuth("/v1/nodes"); got != "Bearer env-key" {
		t.Errorf("Authorization = %q", got)
	}

	// Both flags win over the environment; the env URL points at a closed port.
	t.Setenv(envURL, "http://127.0.0.1:1")
	code, stdout, stderr := runCLI(t, "nodes", "--url", srv.URL, "--key", "flag-key", "--json")
	if code != exitOK {
		t.Fatalf("flag exit = %d, want %d (stderr %q)", code, exitOK, stderr)
	}
	if got := rec.lastAuth("/v1/nodes"); got != "Bearer flag-key" {
		t.Errorf("Authorization = %q", got)
	}
	if strings.TrimSpace(stdout) != "[]" {
		t.Errorf("empty page = %q, want []", stdout)
	}
}

func TestVersionHelpAndUsage(t *testing.T) {
	clearEnv(t)
	code, stdout, _ := runCLI(t, "--version")
	if code != exitOK || strings.TrimSpace(stdout) != "ajs "+version {
		t.Fatalf("version = %d %q", code, stdout)
	}

	code, stdout, _ = runCLI(t, "--help")
	if code != exitOK {
		t.Fatalf("help exit = %d", code)
	}
	for _, want := range []string{"usage:", "status", "nodes", "leases", "lease", "doctor", envKey} {
		if !strings.Contains(stdout, want) {
			t.Errorf("help missing %q:\n%s", want, stdout)
		}
	}

	code, _, stderr := runCLI(t, "bogus")
	if code != exitUsage || !strings.Contains(stderr, "unknown command") {
		t.Fatalf("unknown command = %d %q", code, stderr)
	}

	code, _, stderr = runCLI(t)
	if code != exitUsage || !strings.Contains(stderr, "usage:") {
		t.Fatalf("no args = %d %q", code, stderr)
	}

	code, _, stderr = runCLI(t, "nodes", "--help")
	if code != exitOK || !strings.Contains(stderr, "usage: ajs nodes") {
		t.Fatalf("nodes --help = %d %q", code, stderr)
	}

	// A trailing flag is parsed on the second pass; --help still works and a
	// bad flag is reported once, with a usage hint.
	code, _, stderr = runCLI(t, "lease", "lease-1", "--help")
	if code != exitOK || !strings.Contains(stderr, "usage: ajs lease") {
		t.Fatalf("lease id --help = %d %q", code, stderr)
	}
	code, _, stderr = runCLI(t, "lease", "lease-1", "--bogus")
	if code != exitUsage || strings.Count(stderr, "not defined") != 1 {
		t.Fatalf("lease id --bogus = %d %q", code, stderr)
	}
}
