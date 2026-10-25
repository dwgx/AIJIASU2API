package account

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"ajsproxy/apifacade"
)

const (
	testPassword = "s3cret-pw"
	testToken    = "tok-1|user"
	testConnect  = "cp-1"
)

// loginReply is the Login object a successful ClientApiLogin/Login returns.
const loginReply = `{"Login":{"AuthResult":"OK","LoginToken":"tok-1|user","UserName":"user",` +
	`"ConnectPassword":"cp-1","MembershipStatus":"vip","ExpireTime":1767225600000,"Expired":false}}`

// syncReply is the loginSync/login response: refreshed ConnectPassword plus the
// Servers map.
const syncReply = `{"Login":{"ConnectPassword":"cp-2"},"Servers":{"s1":{"id":"s1","vsid":1,` +
	`"ctry":"CN","svr":"203.0.113.10","tit":"Shanghai 01","cmt":"","sts":"ok","ord":"1",` +
	`"cat":["cn"],"par":{"fv":{"u":1001,"p":443,"ps":"per-server-secret"}},` +
	`"loc":[{"c":"CN","n":"Shanghai","o":1}]}}}`

// capturedRequest is one /client.php request the fake server received.
type capturedRequest struct {
	form   url.Values
	raw    string
	header http.Header
	host   string
}

// newFake starts an httptest server that records every request and answers with
// handle's status and body. Recorded requests are delivered on the returned
// channel before the response is written, so a test that has the response has
// the request too.
func newFake(t *testing.T, handle func(capturedRequest) (int, string)) (*httptest.Server, <-chan capturedRequest) {
	t.Helper()
	requests := make(chan capturedRequest, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("fake server: read body: %v", err)
			http.Error(w, "read error", http.StatusInternalServerError)
			return
		}
		form, err := url.ParseQuery(string(body))
		if err != nil {
			t.Errorf("fake server: parse body %q: %v", body, err)
		}
		request := capturedRequest{form: form, raw: string(body), header: r.Header.Clone(), host: r.Host}
		requests <- request
		status, reply := handle(request)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(server.Close)
	return server, requests
}

// mustDevice returns an OpenAJS/Android device: the control tests below drive
// the default (ShapeOpenAJS) request shape.
func mustDevice(t *testing.T) apifacade.Device {
	t.Helper()
	device, err := NewDevice(OpenAJSClientVersion)
	if err != nil {
		t.Fatalf("NewDevice: %v", err)
	}
	return device
}

// mustLinuxDevice returns the Linux 4.2.3.0 device for ShapeLinux423 tests.
func mustLinuxDevice(t *testing.T) apifacade.Device {
	t.Helper()
	device, err := NewDevice(LinuxClientVersion)
	if err != nil {
		t.Fatalf("NewDevice(LinuxClientVersion): %v", err)
	}
	return device
}

func TestLoginSigned(t *testing.T) {
	now := time.UnixMilli(1700000000000)
	server, requests := newFake(t, func(capturedRequest) (int, string) {
		return http.StatusOK, loginReply
	})
	control := NewControl([]string{server.URL}, mustDevice(t), Options{
		HTTPClient: server.Client(),
		Now:        func() time.Time { return now },
	})

	result, err := control.Login(context.Background(), "user@example.com", Secret(testPassword))
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if result.AuthResult != "OK" {
		t.Fatalf("AuthResult = %q, want OK", result.AuthResult)
	}
	if got := result.Token.Reveal(); got != testToken {
		t.Fatalf("Token = %q, want %q", got, testToken)
	}
	if result.UserName != "user" {
		t.Fatalf("UserName = %q, want user", result.UserName)
	}
	if got := result.ConnectPassword.Reveal(); got != testConnect {
		t.Fatalf("ConnectPassword = %q, want %q", got, testConnect)
	}
	if result.MembershipStatus != "vip" || result.Expired || result.ExpireTime != 1767225600000 {
		t.Fatalf("membership = (%q, %v, %d), want (vip, false, 1767225600000)",
			result.MembershipStatus, result.Expired, result.ExpireTime)
	}

	request := <-requests
	if got := request.form.Get("cmd"); got != "ClientApiLogin/Login" {
		t.Fatalf("cmd = %q, want ClientApiLogin/Login", got)
	}
	if got := request.form.Get("username"); got != "user@example.com" {
		t.Fatalf("username = %q, want user@example.com", got)
	}
	if got := request.form.Get("password"); got != testPassword {
		t.Fatalf("password = %q, want the submitted password", got)
	}
	if got := request.form.Get("trigger"); got != "login" {
		t.Fatalf("trigger = %q, want login", got)
	}
	if got := request.form.Get("_client_login"); got != "" {
		t.Fatalf("_client_login = %q, want empty on a plain login", got)
	}
	if got := request.form.Get("_site"); got != "ajs" {
		t.Fatalf("_site = %q, want ajs", got)
	}
	if got := request.form.Get("_v"); got != OpenAJSClientVersion {
		t.Fatalf("_v = %q, want %q", got, OpenAJSClientVersion)
	}
	if got := request.form.Get("_os_did"); len(got) != 16 {
		t.Fatalf("_os_did = %q, want 16 characters", got)
	}
	if got := request.form.Get("_uid"); len(got) != 22 {
		t.Fatalf("_uid = %q, want 22 characters", got)
	}

	timestamp := request.header.Get("X-Timestamp")
	if timestamp != "1700000000000" {
		t.Fatalf("X-Timestamp = %q, want 1700000000000", timestamp)
	}
	if got, want := request.header.Get("X-Sig"), apifacade.Sign(apifacade.DefaultSecret, request.raw, timestamp); got != want {
		t.Fatalf("X-Sig = %q, want %q", got, want)
	}
	if got := request.header.Get("X-Sig"); len(got) != 16 {
		t.Fatalf("X-Sig = %q, want 16 hex characters", got)
	}
	if got := request.header.Get("User-Agent"); got != "okhttp/4.9.3" {
		t.Fatalf("User-Agent = %q, want okhttp/4.9.3", got)
	}
	if got := request.header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization = %q, want empty", got)
	}

	// The credentials must not leak through any formatting verb, including the
	// whole result struct.
	formatted := fmt.Sprintf("%v|%s|%q|%+v|%#v", result.Token, result.ConnectPassword, result.ConnectPassword, result, result)
	for _, leak := range []string{testToken, testConnect, testPassword} {
		if strings.Contains(formatted, leak) {
			t.Fatalf("formatted login result leaks %q: %s", leak, formatted)
		}
	}
	if !strings.Contains(formatted, "[redacted]") {
		t.Fatalf("formatted login result has no redaction marker: %s", formatted)
	}
}

func TestLoginUnsigned(t *testing.T) {
	server, requests := newFake(t, func(capturedRequest) (int, string) {
		return http.StatusOK, loginReply
	})
	control := NewControl([]string{server.URL}, mustDevice(t), Options{
		HTTPClient: server.Client(),
		NoSigning:  true,
	})

	result, err := control.Login(context.Background(), "user@example.com", Secret(testPassword))
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if got := result.Token.Reveal(); got != testToken {
		t.Fatalf("Token = %q, want %q", got, testToken)
	}

	request := <-requests
	if got := request.header.Get("X-Sig"); got != "" {
		t.Fatalf("X-Sig = %q, want no signature header", got)
	}
	if got := request.header.Get("X-Timestamp"); got != "" {
		t.Fatalf("X-Timestamp = %q, want no timestamp header", got)
	}
	if got := request.form.Get("password"); got != testPassword {
		t.Fatalf("password = %q, want the submitted password", got)
	}
	if got := request.form.Get("cmd"); got != "ClientApiLogin/Login" {
		t.Fatalf("cmd = %q, want ClientApiLogin/Login", got)
	}
}

func TestLoginRejected(t *testing.T) {
	server, _ := newFake(t, func(capturedRequest) (int, string) {
		return http.StatusOK, `{"Login":{"AuthResult":"FAIL","AuthMessage":"bad password"}}`
	})
	control := NewControl([]string{server.URL}, mustDevice(t), Options{HTTPClient: server.Client()})

	result, err := control.Login(context.Background(), "user@example.com", Secret(testPassword))
	var authErr *AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("Login error = %v, want *AuthError", err)
	}
	if authErr.Result != "FAIL" || authErr.Message != "bad password" {
		t.Fatalf("AuthError = (%q, %q), want (FAIL, bad password)", authErr.Result, authErr.Message)
	}
	if result.AuthResult != "FAIL" {
		t.Fatalf("result.AuthResult = %q, want FAIL", result.AuthResult)
	}
	if strings.Contains(err.Error(), testPassword) {
		t.Fatalf("error leaks the password: %v", err)
	}
}

func TestLoginSyncAndServers(t *testing.T) {
	server, requests := newFake(t, func(request capturedRequest) (int, string) {
		if request.form.Get("password") != "" {
			return http.StatusOK, loginReply
		}
		return http.StatusOK, syncReply
	})
	control := NewControl([]string{server.URL}, mustDevice(t), Options{HTTPClient: server.Client()})

	if _, err := control.Login(context.Background(), "user@example.com", Secret(testPassword)); err != nil {
		t.Fatalf("Login: %v", err)
	}
	<-requests // the login request

	synced, err := control.LoginSync(context.Background())
	if err != nil {
		t.Fatalf("LoginSync: %v", err)
	}
	if got := synced.Token.Reveal(); got != testToken {
		t.Fatalf("Token = %q, want %q", got, testToken)
	}
	if synced.UserName != "user" {
		t.Fatalf("UserName = %q, want user", synced.UserName)
	}
	if got := synced.ConnectPassword.Reveal(); got != "cp-2" {
		t.Fatalf("ConnectPassword = %q, want the refreshed cp-2", got)
	}
	entry, ok := synced.Servers["s1"]
	if !ok {
		t.Fatalf("Servers = %v, want s1", synced.Servers)
	}
	if !entry.OK() {
		t.Fatalf("Server.OK() = false, sts = %q", entry.Sts)
	}
	if got := entry.Port(); got != 443 {
		t.Fatalf("Server.Port() = %d, want 443", got)
	}
	if got := entry.Addr(); got != "203.0.113.10:443" {
		t.Fatalf("Server.Addr() = %q, want 203.0.113.10:443", got)
	}
	if entry.Tit != "Shanghai 01" || len(entry.Loc) != 1 || entry.Loc[0].N != "Shanghai" {
		t.Fatalf("server metadata = %+v, want Shanghai 01/Shanghai", entry)
	}

	syncRequest := <-requests
	if got := syncRequest.form.Get("_client_login"); got != testToken {
		t.Fatalf("_client_login = %q, want %q", got, testToken)
	}
	if got := syncRequest.form.Get("username"); got != "user" {
		t.Fatalf("username = %q, want user", got)
	}
	if got := syncRequest.form.Get("password"); got != "" {
		t.Fatalf("password = %q, want none on loginSync", got)
	}
	if got := syncRequest.form.Get("trigger"); got != "login" {
		t.Fatalf("trigger = %q, want login", got)
	}

	servers, err := control.Servers(context.Background())
	if err != nil {
		t.Fatalf("Servers: %v", err)
	}
	if _, ok := servers["s1"]; !ok {
		t.Fatalf("Servers = %v, want s1", servers)
	}
	<-requests
}

func TestLoginSyncRequiresToken(t *testing.T) {
	server, _ := newFake(t, func(capturedRequest) (int, string) {
		return http.StatusOK, syncReply
	})
	control := NewControl([]string{server.URL}, mustDevice(t), Options{HTTPClient: server.Client()})

	if _, err := control.LoginSync(context.Background()); !errors.Is(err, apifacade.ErrNoLoginToken) {
		t.Fatalf("LoginSync error = %v, want ErrNoLoginToken", err)
	}
}

func TestLoginFailoverOnTransportError(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close() // the first host now refuses connections

	live, requests := newFake(t, func(capturedRequest) (int, string) {
		return http.StatusOK, loginReply
	})
	control := NewControl([]string{deadURL, live.URL}, mustDevice(t), Options{HTTPClient: live.Client()})

	result, err := control.Login(context.Background(), "user@example.com", Secret(testPassword))
	if err != nil {
		t.Fatalf("Login with a dead first host: %v", err)
	}
	if got := result.Token.Reveal(); got != testToken {
		t.Fatalf("Token = %q, want %q", got, testToken)
	}
	request := <-requests
	if got := request.form.Get("password"); got != testPassword {
		t.Fatalf("second host saw password = %q, want the submitted password", got)
	}
}

func TestNoFailoverOnHTTPError(t *testing.T) {
	broken, _ := newFake(t, func(capturedRequest) (int, string) {
		return http.StatusInternalServerError, "server exploded"
	})
	live, liveRequests := newFake(t, func(capturedRequest) (int, string) {
		return http.StatusOK, loginReply
	})
	control := NewControl([]string{broken.URL, live.URL}, mustDevice(t), Options{HTTPClient: live.Client()})

	_, err := control.Login(context.Background(), "user@example.com", Secret(testPassword))
	if err == nil {
		t.Fatal("Login with an HTTP 500 first host = nil error, want a status error")
	}
	if !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("Login error = %v, want an HTTP 500 error", err)
	}
	select {
	case request := <-liveRequests:
		t.Fatalf("second host was contacted after an HTTP 500: %v", request.form)
	default:
	}
}

func TestNoFailoverOnProtocolError(t *testing.T) {
	broken, _ := newFake(t, func(capturedRequest) (int, string) {
		return http.StatusOK, `{"Result":"Success"}`
	})
	live, liveRequests := newFake(t, func(capturedRequest) (int, string) {
		return http.StatusOK, loginReply
	})
	control := NewControl([]string{broken.URL, live.URL}, mustDevice(t), Options{HTTPClient: live.Client()})

	_, err := control.Login(context.Background(), "user@example.com", Secret(testPassword))
	if err == nil {
		t.Fatal("Login without a Login object = nil error, want a protocol error")
	}
	if !strings.Contains(err.Error(), "no Login object") {
		t.Fatalf("Login error = %v, want a missing Login object error", err)
	}
	select {
	case request := <-liveRequests:
		t.Fatalf("second host was contacted after a protocol error: %v", request.form)
	default:
	}
}

func TestNewDeviceProfiles(t *testing.T) {
	linux, err := NewDevice("")
	if err != nil {
		t.Fatalf("NewDevice(\"\"): %v", err)
	}
	if linux.Version != LinuxClientVersion {
		t.Fatalf("default Version = %q, want %q", linux.Version, LinuxClientVersion)
	}
	if linux.Platform != "linux" || linux.OSDevice != "x86_64" || linux.Site != "ajs" {
		t.Fatalf("platform/device/site = %q/%q/%q, want linux/x86_64/ajs",
			linux.Platform, linux.OSDevice, linux.Site)
	}
	if len(linux.UID) != 20 {
		t.Fatalf("UID = %q, want the captured 20-hex form", linux.UID)
	}
	if linux.CUID != linux.UID || linux.CSEID != apifacade.LinuxCSEID {
		t.Fatalf("cuid/cseid = %q/%q, want the UID and %q", linux.CUID, linux.CSEID, apifacade.LinuxCSEID)
	}
	if len(linux.DeviceID) != 16 {
		t.Fatalf("DeviceID = %q, want 16 characters", linux.DeviceID)
	}
	if linux.OSDim != "lid=REDACTED,8e415e603088426aa42fed420212150c" {
		t.Fatalf("OSDim = %q, want the captured value", linux.OSDim)
	}

	openAJS, err := NewDevice(OpenAJSClientVersion)
	if err != nil {
		t.Fatalf("NewDevice(OpenAJSClientVersion): %v", err)
	}
	if openAJS.Version != OpenAJSClientVersion {
		t.Fatalf("Version = %q, want %q", openAJS.Version, OpenAJSClientVersion)
	}
	if openAJS.Platform != "android" || openAJS.OSDevice == linux.OSDevice {
		t.Fatalf("OpenAJS platform/device = %q/%q, want the Android descriptor",
			openAJS.Platform, openAJS.OSDevice)
	}
	if len(openAJS.UID) != 22 || len(openAJS.DeviceID) != 16 {
		t.Fatalf("OpenAJS uid/device id = %q/%q, want 22/16 characters", openAJS.UID, openAJS.DeviceID)
	}
	if openAJS.DeviceID == linux.DeviceID || openAJS.UID == linux.UID {
		t.Fatalf("two devices share an identity: %q/%q", openAJS.DeviceID, openAJS.UID)
	}
	if openAJS.CUID != "" || openAJS.CSEID != "" {
		t.Fatalf("OpenAJS cuid/cseid = %q/%q, want the DeviceID fallback", openAJS.CUID, openAJS.CSEID)
	}

	identity := Identify(linux)
	if identity.Version != linux.Version || identity.OSDID != linux.DeviceID ||
		identity.UID != linux.UID || identity.TZMin != linux.TZMinutes {
		t.Fatalf("Identify = %+v, want the device's version/ids/tz", identity)
	}
	if identity.CUID != linux.UID || identity.CSEID != apifacade.LinuxCSEID ||
		identity.OSP != "linux" || identity.OSD != "x86_64" || identity.DIM != linux.OSDim {
		t.Fatalf("Identify = %+v, want the captured Linux identity", identity)
	}

	rebuilt := deviceFromIdentity(identity, linux.Version)
	if rebuilt.UID != linux.UID || rebuilt.CUID != linux.CUID || rebuilt.CSEID != linux.CSEID ||
		rebuilt.DeviceID != linux.DeviceID || rebuilt.OSVersion != linux.OSVersion ||
		rebuilt.OSDim != linux.OSDim || rebuilt.Platform != linux.Platform {
		t.Fatalf("deviceFromIdentity = %+v, want the original descriptor %+v", rebuilt, linux)
	}

	openAJSIdentity := Identify(openAJS)
	if openAJSIdentity.CUID != openAJS.DeviceID || openAJSIdentity.CSEID != openAJS.DeviceID {
		t.Fatalf("OpenAJS identity = %+v, want cuid=cseid=DeviceID", openAJSIdentity)
	}
}

// TestLoginLinuxShape is the daemon-side B1 regression: the Linux 4.2.3.0
// profile must send v (not _v), a 20-hex _uid, no Android-only fields, no
// User-Agent and the captured trailing "&".
func TestLoginLinuxShape(t *testing.T) {
	server, requests := newFake(t, func(capturedRequest) (int, string) {
		return http.StatusOK, loginReply
	})
	control := NewControl([]string{server.URL}, mustLinuxDevice(t), Options{
		HTTPClient:   server.Client(),
		NoSigning:    true,
		RequestShape: apifacade.ShapeLinux423,
	})

	if _, err := control.Login(context.Background(), "user@example.com", Secret(testPassword)); err != nil {
		t.Fatalf("Login: %v", err)
	}
	request := <-requests

	if got := request.form.Get("v"); got != LinuxClientVersion {
		t.Fatalf("v = %q, want %q", got, LinuxClientVersion)
	}
	if _, ok := request.form["_v"]; ok {
		t.Fatalf("linux body carries _v: %v", request.raw)
	}
	if uid := request.form.Get("_uid"); len(uid) != 20 {
		t.Fatalf("_uid = %q, want 20 characters", uid)
	}
	for _, key := range []string{"_os_hw", "_os_did", "_os_lang", "_tz", "_inst_mkt", "_vrf", "trigger", "cv"} {
		if _, ok := request.form[key]; ok {
			t.Fatalf("linux body carries %q: %v", key, request.raw)
		}
	}
	if !strings.HasSuffix(request.raw, "&") {
		t.Fatalf("linux body = %q, want a trailing &", request.raw)
	}
	if _, ok := request.header["User-Agent"]; ok {
		t.Fatalf("User-Agent = %q, want none", request.header.Get("User-Agent"))
	}
	if got := request.header.Get("X-Sig"); got != "" {
		t.Fatalf("X-Sig = %q, want none", got)
	}
}

func TestSecretRedaction(t *testing.T) {
	secret := Secret("hunter2-token")
	for _, formatted := range []string{
		fmt.Sprintf("%v", secret),
		fmt.Sprintf("%s", secret),
		fmt.Sprintf("%q", secret),
		fmt.Sprintf("%+v", secret),
		fmt.Sprintf("%#v", secret),
		fmt.Sprintf("%v", struct{ Token Secret }{secret}),
		fmt.Sprintf("%#v", struct{ Token Secret }{secret}),
	} {
		if strings.Contains(formatted, "hunter2") {
			t.Fatalf("formatting leaks the secret: %s", formatted)
		}
		if !strings.Contains(formatted, "[redacted]") {
			t.Fatalf("formatting has no redaction marker: %s", formatted)
		}
	}

	data, err := json.Marshal(struct {
		Token Secret `json:"token"`
	}{secret})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if string(data) != `{"token":"[redacted]"}` {
		t.Fatalf("JSON = %s, want a redacted token", data)
	}

	var log bytes.Buffer
	slog.New(slog.NewTextHandler(&log, nil)).Info("login", "token", secret)
	if strings.Contains(log.String(), "hunter2") {
		t.Fatalf("slog leaks the secret: %s", log.String())
	}
	if !strings.Contains(log.String(), "[redacted]") {
		t.Fatalf("slog has no redaction marker: %s", log.String())
	}
}

// TestConvertLoginUseCount pins the vendor capacity mapping: UseCount is the
// purchased simultaneous-connection allowance, absent means one, and values
// outside the published 1..2000 range are normalized rather than trusted.
func TestConvertLoginUseCount(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want int
	}{
		{name: "absent defaults to one", raw: `{"AuthResult":"OK"}`, want: 1},
		{name: "one stays one", raw: `{"AuthResult":"OK","UseCount":1}`, want: 1},
		{name: "purchased value is preserved", raw: `{"AuthResult":"OK","UseCount":37}`, want: 37},
		{name: "above maximum clamps", raw: `{"AuthResult":"OK","UseCount":5000}`, want: 2000},
		{name: "negative defaults to one", raw: `{"AuthResult":"OK","UseCount":-3}`, want: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var vendor apifacade.LoginResult
			if err := json.Unmarshal([]byte(tc.raw), &vendor); err != nil {
				t.Fatalf("unmarshal vendor login: %v", err)
			}
			got := convertLogin(&vendor)
			if got.UseCount != tc.want {
				t.Fatalf("convertLogin().UseCount = %d, want %d", got.UseCount, tc.want)
			}
		})
	}
}
