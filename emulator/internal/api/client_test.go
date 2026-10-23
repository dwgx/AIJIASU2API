package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
)

// capturedLinuxLoginBody is research/60-dynamic/captures/control.jsonl line 2,
// the only direct Linux 4.2.3.0 control request: a sorted query string with the
// serializer's trailing "&". Its _uid is a 20-hex value and its
// username/password are the capture's dummy "test".
const capturedLinuxLoginBody = `_lang=en&_m=linux&_os_dev=x86_64&_os_dim=lid%3dREDACTED%2c8e415e603088426aa42fed420212150c&_os_ver=Linux%20version%206.18.33.2-microsoft-standard-WSL2%20%28root%40f1bbfb02316b%29%20%28gcc%20%28GCC%29%2013.2.0%2c%20GNU%20ld%20%28GNU%20Binutils%29%202.41%29%20%231%20SMP%20PREEMPT_DYNAMIC%20Thu%20Jun%2018%2021%3a54%3a43%20UTC%202026%0a&_site=ajs&_uid=6ada94ee0278eefaedb5&cmd=ClientApiLogin%2fLogin&password=test&username=test&v=4.2.3.0&`

// Linux credentials used by the recorder tests. The capture's own values are
// the dummy "test"; substituting distinct values proves the body carries what
// the caller passed.
const (
	linuxTestUser     = "capture-user"
	linuxTestPassword = "capture-pw"
)

func parseQuery(t *testing.T, body string) url.Values {
	t.Helper()
	values, err := url.ParseQuery(body)
	if err != nil {
		t.Fatalf("parse query %q: %v", body, err)
	}
	return values
}

// singleValues flattens a parsed body to the one value per key the vendor
// protocol uses.
func singleValues(t *testing.T, values url.Values) map[string]string {
	t.Helper()
	out := make(map[string]string, len(values))
	for key, list := range values {
		if len(list) != 1 {
			t.Fatalf("field %q has %d values, want 1", key, len(list))
		}
		out[key] = list[0]
	}
	return out
}

func sortedKeys(values url.Values) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// redactLinuxBody masks the uid and password so a failure message never prints
// identity or credential values.
func redactLinuxBody(t *testing.T, body string) string {
	t.Helper()
	values, err := url.ParseQuery(strings.TrimSuffix(body, "&"))
	if err != nil {
		return "[unparsable body]"
	}
	if values.Get("_uid") != "" {
		values.Set("_uid", "[redacted]")
	}
	if values.Get("password") != "" {
		values.Set("password", "[redacted]")
	}
	out := make(map[string]string, len(values))
	for key, list := range values {
		if len(list) > 0 {
			out[key] = list[0]
		}
	}
	return BuildQuery(out)
}

func isLowerHex(value string) bool {
	for i := range len(value) {
		c := value[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// TestLinux423RequestMatchesCapture is the B1 regression: ShapeLinux423 must
// produce the captured Linux 4.2.3.0 login byte for byte, apart from the
// generated _uid and the caller's credentials.
func TestLinux423RequestMatchesCapture(t *testing.T) {
	device := Linux423Device()
	if err := device.EnsureLinuxIDs(); err != nil {
		t.Fatalf("EnsureLinuxIDs: %v", err)
	}

	var (
		body   string
		header http.Header
		closed bool
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			http.Error(w, "read error", http.StatusInternalServerError)
			return
		}
		body = string(data)
		header = r.Header.Clone()
		closed = r.Close
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"Login":{"AuthResult":"OK","LoginToken":"tok|user",`+
			`"UserName":"`+linuxTestUser+`","ConnectPassword":"cp-1"}}`)
	}))
	defer srv.Close()

	client := NewClient([]string{srv.URL}, device)
	client.HTTP = srv.Client()
	client.RequestShape = ShapeLinux423
	client.NoSign = true // the Linux capture carries no X-Sig/X-Timestamp

	if _, err := client.Login(context.Background(), linuxTestUser, linuxTestPassword); err != nil {
		t.Fatalf("Login: %v", err)
	}

	// The capture is BuildQuery output plus the trailing separator, so the
	// expected body is the capture with only _uid/username/password replaced.
	capture := strings.TrimSuffix(capturedLinuxLoginBody, "&")
	if got := BuildQuery(singleValues(t, parseQuery(t, capture))); got != capture {
		t.Fatalf("capture is not sorted BuildQuery output:\n got %s\nwant %s", got, capture)
	}
	want := singleValues(t, parseQuery(t, capture))
	want["_uid"] = device.UID
	want["username"] = linuxTestUser
	want["password"] = linuxTestPassword
	wantBody := BuildQuery(want) + "&"

	if body != wantBody {
		t.Fatalf("linux body mismatch:\n got %s\nwant %s",
			redactLinuxBody(t, body), redactLinuxBody(t, wantBody))
	}
	t.Logf("ShapeLinux423 login body (uid/password redacted): %s", redactLinuxBody(t, body))

	got := parseQuery(t, strings.TrimSuffix(body, "&"))
	if len(got) != len(want) {
		t.Fatalf("field count = %d, want %d: %v", len(got), len(want), sortedKeys(got))
	}
	for key := range want {
		if _, ok := got[key]; !ok {
			t.Fatalf("missing captured field %q: %v", key, sortedKeys(got))
		}
	}
	for _, key := range []string{"_v", "_os_hw", "_os_did", "_os_lang", "_tz", "_inst_mkt", "_vrf", "trigger", "cv"} {
		if _, ok := got[key]; ok {
			t.Fatalf("linux body carries %q, the capture does not: %v", key, sortedKeys(got))
		}
	}
	if got.Get("v") != Linux423Version {
		t.Fatalf("v = %q, want %q", got.Get("v"), Linux423Version)
	}
	if uid := got.Get("_uid"); len(uid) != 20 || !isLowerHex(uid) {
		t.Fatalf("_uid = %q, want 20 lowercase hex characters", uid)
	}
	if !strings.HasSuffix(body, "&") {
		t.Fatalf("linux body does not end with '&': %q", redactLinuxBody(t, body))
	}
	if _, ok := header["User-Agent"]; ok {
		t.Fatalf("User-Agent = %q, the Linux capture sends none", header.Get("User-Agent"))
	}
	if got := header.Get("Content-Type"); got != "application/x-www-form-urlencoded" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := header.Get("Accept"); got != "*/*" {
		t.Fatalf("Accept = %q, want */*", got)
	}
	if got := header.Get("Accept-Encoding"); got != "deflate, gzip" {
		t.Fatalf("Accept-Encoding = %q, want deflate, gzip", got)
	}
	if !closed {
		t.Fatal("Connection: close missing; the Linux capture closes the connection")
	}
	if got := header.Get("X-Sig"); got != "" {
		t.Fatalf("X-Sig = %q, want none", got)
	}
}

// TestOpenAJSRequestShapeUnchanged proves the zero RequestShape keeps the
// OpenAJS/Android body and headers.
func TestOpenAJSRequestShapeUnchanged(t *testing.T) {
	device := DefaultDevice()
	if err := device.EnsureIDs(); err != nil {
		t.Fatalf("EnsureIDs: %v", err)
	}
	var (
		body   string
		header http.Header
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			http.Error(w, "read error", http.StatusInternalServerError)
			return
		}
		body = string(data)
		header = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"Login":{"AuthResult":"OK","LoginToken":"tok|user",`+
			`"UserName":"u1","ConnectPassword":"cp-1"}}`)
	}))
	defer srv.Close()

	client := NewClient([]string{srv.URL}, device)
	client.HTTP = srv.Client()

	if _, err := client.Login(context.Background(), "user@example.com", "p@ss"); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if strings.HasSuffix(body, "&") {
		t.Fatalf("OpenAJS body ends with '&': %q", body)
	}
	values := parseQuery(t, body)
	if got := values.Get("_v"); got != device.Version {
		t.Fatalf("_v = %q, want %q", got, device.Version)
	}
	if _, ok := values["v"]; ok {
		t.Fatalf("OpenAJS body carries v: %v", sortedKeys(values))
	}
	if uid := values.Get("_uid"); len(uid) != 22 {
		t.Fatalf("_uid = %q, want 22 characters", uid)
	}
	for _, key := range []string{"_os_hw", "_os_did", "_os_lang", "_tz", "_inst_mkt", "_vrf", "trigger"} {
		if _, ok := values[key]; !ok {
			t.Fatalf("OpenAJS body is missing %q: %v", key, sortedKeys(values))
		}
	}
	if got := header.Get("User-Agent"); got != DefaultUserAgent {
		t.Fatalf("User-Agent = %q, want %q", got, DefaultUserAgent)
	}
	if got := header.Get("X-Sig"); len(got) != 16 {
		t.Fatalf("X-Sig = %q, want a 16-hex signature", got)
	}
}

// TestLinux423DeviceIdentity pins the generated Linux identity format.
func TestLinux423DeviceIdentity(t *testing.T) {
	device := Linux423Device()
	if err := device.EnsureLinuxIDs(); err != nil {
		t.Fatalf("EnsureLinuxIDs: %v", err)
	}
	if len(device.UID) != 20 || !isLowerHex(device.UID) {
		t.Fatalf("UID = %q, want 20 lowercase hex characters", device.UID)
	}
	if device.CUID != device.UID {
		t.Fatalf("CUID = %q, want the 20-hex UID %q", device.CUID, device.UID)
	}
	if device.CSEID != LinuxCSEID {
		t.Fatalf("CSEID = %q, want %q", device.CSEID, LinuxCSEID)
	}
	if len(device.DeviceID) != 16 || !isLowerHex(device.DeviceID) {
		t.Fatalf("DeviceID = %q, want 16 lowercase hex characters", device.DeviceID)
	}
	if device.Platform != "linux" || device.OSDevice != "x86_64" || device.Version != Linux423Version {
		t.Fatalf("platform/device/version = %q/%q/%q", device.Platform, device.OSDevice, device.Version)
	}

	before := device
	if err := device.EnsureLinuxIDs(); err != nil {
		t.Fatalf("second EnsureLinuxIDs: %v", err)
	}
	if device != before {
		t.Fatalf("EnsureLinuxIDs regenerated the identity: %+v -> %+v", before, device)
	}
}
