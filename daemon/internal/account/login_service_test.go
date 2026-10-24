package account

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/store"
	"ajsproxy/apifacade"
)

// plainSealer is an identity Sealer: the tests exercise the login path, not
// encryption at rest. It never sees a real credential.
type plainSealer struct{}

func (plainSealer) Seal(_ string, plaintext []byte) ([]byte, error) {
	return append([]byte(nil), plaintext...), nil
}

func (plainSealer) Open(_ string, sealed []byte) ([]byte, error) {
	return append([]byte(nil), sealed...), nil
}

// Delete satisfies store.Sealer: this stub keeps nothing, so there is nothing
// to remove.
func (plainSealer) Delete(string) error { return nil }

func newLoginAccount(t *testing.T, st *store.Store) model.Account {
	t.Helper()
	account, err := st.CreateAccount(model.CreateAccountInput{
		Label:       "Login Test",
		Username:    "user@example.com",
		Password:    testPassword,
		Platform:    model.PlatformLinux,
		Concurrency: 8,
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	return account
}

func newTestLoginService(t *testing.T, st *store.Store, url string, logger *slog.Logger) *LoginService {
	t.Helper()
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	service, err := NewLoginService(st, LoginConfig{
		Hosts:   []string{url},
		Enabled: true,
		Logger:  logger,
	})
	if err != nil {
		t.Fatalf("NewLoginService: %v", err)
	}
	t.Cleanup(service.Close)
	return service
}

func TestLoginServiceSuccess(t *testing.T) {
	st := store.NewWithSealer(plainSealer{})
	account := newLoginAccount(t, st)
	server, requests := newFake(t, func(capturedRequest) (int, string) {
		return http.StatusOK, loginReply
	})
	service := newTestLoginService(t, st, server.URL, nil)
	// The Windows wall clock can return the same instant for back-to-back
	// calls; give MarkLogin a later tick than CreateAccount.
	time.Sleep(20 * time.Millisecond)

	updated, err := service.Login(context.Background(), account.ID)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	request := <-requests
	if got := request.form.Get("username"); got != account.Username {
		t.Fatalf("username = %q, want %q", got, account.Username)
	}
	if got := request.form.Get("password"); got != testPassword {
		t.Fatalf("password = %q, want the sealed password", got)
	}

	if updated.State != model.AccountActive {
		t.Fatalf("state = %q, want active", updated.State)
	}
	if updated.LastLoginAt == account.LastLoginAt {
		t.Fatalf("LastLoginAt = %q, want a fresh timestamp", updated.LastLoginAt)
	}
	if updated.Username != "user" {
		t.Fatalf("Username = %q, want the vendor user name", updated.Username)
	}

	audit := st.Audit(1)
	if len(audit) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(audit))
	}
	if audit[0].Action != "account.login" || audit[0].Target != account.Label {
		t.Fatalf("audit = %+v, want account.login for %q", audit[0], account.Label)
	}
	if !strings.Contains(audit[0].Detail, "vip") {
		t.Fatalf("audit detail = %q, want the membership", audit[0].Detail)
	}

	result, ok := service.Result(account.ID)
	if !ok {
		t.Fatal("Result: no stored login result")
	}
	if result.AuthResult != "OK" || result.UserName != "user" {
		t.Fatalf("result = (%q, %q), want (OK, user)", result.AuthResult, result.UserName)
	}
	if got := result.Token.Reveal(); got != testToken {
		t.Fatalf("Token = %q, want %q", got, testToken)
	}
	if got := result.ConnectPassword.Reveal(); got != testConnect {
		t.Fatalf("ConnectPassword = %q, want %q", got, testConnect)
	}
	if result.MembershipStatus != "vip" || result.Expired {
		t.Fatalf("membership = (%q, %v), want (vip, false)", result.MembershipStatus, result.Expired)
	}
}

// TestLoginServiceRetainsServers proves the follow-up loginSync reaches the
// retained result: the node controller reads Servers and the refreshed connect
// password from there.
func TestLoginServiceRetainsServers(t *testing.T) {
	st := store.NewWithSealer(plainSealer{})
	account := newLoginAccount(t, st)
	server, _ := newFake(t, func(request capturedRequest) (int, string) {
		if request.form.Get("_client_login") == "" {
			return http.StatusOK, loginReply
		}
		return http.StatusOK, syncReply
	})
	service := newTestLoginService(t, st, server.URL, nil)

	if _, err := service.Login(context.Background(), account.ID); err != nil {
		t.Fatalf("Login: %v", err)
	}
	result, ok := service.Result(account.ID)
	if !ok {
		t.Fatal("Result: no stored login result")
	}
	entry, ok := result.Servers["s1"]
	if !ok || entry.ID != "s1" || !entry.OK() {
		t.Fatalf("Servers = %+v, want the usable s1 entry", result.Servers)
	}
	if got := result.ConnectPassword.Reveal(); got != "cp-2" {
		t.Fatalf("ConnectPassword = %q, want the loginSync refresh cp-2", got)
	}
	if result.AuthResult != "OK" || result.UserName != "user" {
		t.Fatalf("result = (%q, %q), want the login identity", result.AuthResult, result.UserName)
	}
}

// TestLoginServiceKeepsRetainedServersOnSyncFailure is the P1-2 regression: a
// failed or empty follow-up loginSync is not evidence that the account has no
// servers. Before the fix, the empty result replaced the retained list and the
// node controller deleted every node on the next SyncAccount.
func TestLoginServiceKeepsRetainedServersOnSyncFailure(t *testing.T) {
	const (
		vendorErrorReply = `{"Login":{"AuthResult":"ERR","AuthMessage":"system busy"}}`
		emptySyncReply   = `{"Login":{"ConnectPassword":"cp-3"}}`
	)
	for _, tc := range []struct {
		name       string
		secondSync string
	}{
		{name: "vendor error envelope", secondSync: vendorErrorReply},
		{name: "empty server list", secondSync: emptySyncReply},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := store.NewWithSealer(plainSealer{})
			account := newLoginAccount(t, st)
			syncs := 0
			server, _ := newFake(t, func(request capturedRequest) (int, string) {
				if request.form.Get("_client_login") == "" {
					return http.StatusOK, loginReply
				}
				syncs++
				if syncs == 1 {
					return http.StatusOK, syncReply
				}
				return http.StatusOK, tc.secondSync
			})
			service := newTestLoginService(t, st, server.URL, nil)
			ctx := context.Background()

			if _, err := service.Login(ctx, account.ID); err != nil {
				t.Fatalf("first Login: %v", err)
			}
			before, ok := service.Result(account.ID)
			if !ok || len(before.Servers) != 1 {
				t.Fatalf("after the good sync: servers = %d, want 1", len(before.Servers))
			}

			if _, err := service.Login(ctx, account.ID); err != nil {
				t.Fatalf("second Login: %v", err)
			}
			after, ok := service.Result(account.ID)
			if !ok {
				t.Fatal("second Login dropped the retained result")
			}
			if len(after.Servers) != 1 {
				t.Fatalf("after the failed sync: servers = %d (%v), want the retained s1",
					len(after.Servers), after.Servers)
			}
			entry, ok := after.Servers["s1"]
			if !ok || entry.ID != "s1" || !entry.OK() {
				t.Fatalf("retained Servers = %+v, want the usable s1 entry", after.Servers)
			}
		})
	}
}

func TestLoginServiceExpiredMembership(t *testing.T) {
	const expiredReply = `{"Login":{"AuthResult":"OK","LoginToken":"tok-1|user","UserName":"user",` +
		`"ConnectPassword":"cp-1","MembershipStatus":"basic","ExpireTime":1767225600000,"Expired":true}}`
	st := store.NewWithSealer(plainSealer{})
	account := newLoginAccount(t, st)
	server, _ := newFake(t, func(capturedRequest) (int, string) {
		return http.StatusOK, expiredReply
	})
	service := newTestLoginService(t, st, server.URL, nil)

	updated, err := service.Login(context.Background(), account.ID)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if updated.State != model.AccountExpired {
		t.Fatalf("state = %q, want expired", updated.State)
	}
	if detail := st.Audit(1)[0].Detail; !strings.Contains(detail, "expired") {
		t.Fatalf("audit detail = %q, want the expired marker", detail)
	}
}

func TestLoginServiceAuthRejected(t *testing.T) {
	const rejectReply = `{"Login":{"AuthResult":"WrongPassword","AuthMessage":"bad password\nsecond line"}}`
	st := store.NewWithSealer(plainSealer{})
	account := newLoginAccount(t, st)
	server, _ := newFake(t, func(capturedRequest) (int, string) {
		return http.StatusOK, rejectReply
	})
	service := newTestLoginService(t, st, server.URL, nil)

	_, err := service.Login(context.Background(), account.ID)
	if !errors.Is(err, ErrAuthRejected) {
		t.Fatalf("Login error = %v, want ErrAuthRejected", err)
	}
	if !strings.Contains(err.Error(), "bad password") {
		t.Fatalf("Login error = %q, want the vendor message", err)
	}
	if strings.Contains(err.Error(), testPassword) {
		t.Fatalf("Login error leaks the password: %v", err)
	}

	current, ok := st.Account(account.ID)
	if !ok {
		t.Fatal("account disappeared")
	}
	if current.State != model.AccountAuthFailed {
		t.Fatalf("state = %q, want auth_failed", current.State)
	}
	if current.LastLoginAt != account.LastLoginAt {
		t.Fatalf("LastLoginAt = %q, want %q", current.LastLoginAt, account.LastLoginAt)
	}

	audit := st.Audit(1)[0]
	if audit.Action != "account.auth_failed" || audit.Target != account.Label {
		t.Fatalf("audit = %+v, want account.auth_failed", audit)
	}
	if strings.Contains(audit.Detail, "\n") {
		t.Fatalf("audit detail is not one line: %q", audit.Detail)
	}
	if audit.Detail != "bad password second line" {
		t.Fatalf("audit detail = %q, want the sanitized vendor message", audit.Detail)
	}
	if _, ok := service.Result(account.ID); ok {
		t.Fatal("Result returned a login result after a rejection")
	}
}

func TestLoginServiceTransportError(t *testing.T) {
	st := store.NewWithSealer(plainSealer{})
	account := newLoginAccount(t, st)
	server, _ := newFake(t, func(capturedRequest) (int, string) {
		return http.StatusOK, loginReply
	})
	url := server.URL
	server.Close()
	service := newTestLoginService(t, st, url, nil)
	auditBefore := len(st.Audit(1000))

	_, err := service.Login(context.Background(), account.ID)
	if !errors.Is(err, ErrUpstream) {
		t.Fatalf("Login error = %v, want ErrUpstream", err)
	}

	current, ok := st.Account(account.ID)
	if !ok {
		t.Fatal("account disappeared")
	}
	if current.State != model.AccountActive {
		t.Fatalf("state = %q, want active (unchanged)", current.State)
	}
	if current.LastLoginAt != account.LastLoginAt {
		t.Fatalf("LastLoginAt = %q, want %q", current.LastLoginAt, account.LastLoginAt)
	}
	if got := len(st.Audit(1000)); got != auditBefore {
		t.Fatalf("audit rows = %d, want %d: a transport error must not mark the account", got, auditBefore)
	}
}

func TestLoginServiceMissingSecret(t *testing.T) {
	st := store.New()
	server, requests := newFake(t, func(capturedRequest) (int, string) {
		return http.StatusOK, loginReply
	})
	service := newTestLoginService(t, st, server.URL, nil)

	_, err := service.Login(context.Background(), "acc-01")
	if !errors.Is(err, ErrNoSecret) {
		t.Fatalf("Login error = %v, want ErrNoSecret", err)
	}
	select {
	case request := <-requests:
		t.Fatalf("fake server received %q, want no request", request.raw)
	default:
	}
}

func TestLoginServiceUnknownAccount(t *testing.T) {
	st := store.NewWithSealer(plainSealer{})
	server, requests := newFake(t, func(capturedRequest) (int, string) {
		return http.StatusOK, loginReply
	})
	service := newTestLoginService(t, st, server.URL, nil)

	_, err := service.Login(context.Background(), "missing")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Login error = %v, want store.ErrNotFound", err)
	}
	select {
	case request := <-requests:
		t.Fatalf("fake server received %q, want no request", request.raw)
	default:
	}
}

func TestLoginServiceDisabled(t *testing.T) {
	st := store.NewWithSealer(plainSealer{})
	account := newLoginAccount(t, st)
	server, requests := newFake(t, func(capturedRequest) (int, string) {
		return http.StatusOK, loginReply
	})
	service, err := NewLoginService(st, LoginConfig{
		Hosts:  []string{server.URL},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("NewLoginService: %v", err)
	}
	t.Cleanup(service.Close)

	if _, err := service.Login(context.Background(), account.ID); !errors.Is(err, ErrDisabled) {
		t.Fatalf("Login error = %v, want ErrDisabled", err)
	}
	select {
	case request := <-requests:
		t.Fatalf("fake server received %q, want no request", request.raw)
	default:
	}
}

func TestLoginServiceRedaction(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	st := store.NewWithSealer(plainSealer{})
	account := newLoginAccount(t, st)
	server, _ := newFake(t, func(capturedRequest) (int, string) {
		return http.StatusOK, loginReply
	})
	service := newTestLoginService(t, st, server.URL, logger)

	result, err := service.Login(context.Background(), account.ID)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if _, err := service.Login(context.Background(), account.ID); err != nil {
		t.Fatalf("second Login: %v", err)
	}
	stored, ok := service.Result(account.ID)
	if !ok {
		t.Fatal("Result: no stored login result")
	}

	formatted := fmt.Sprintf("%v|%+v|%#v|%v", result, result, result, stored)
	for _, leak := range []string{testPassword, testToken, testConnect} {
		if strings.Contains(formatted, leak) {
			t.Fatalf("formatted result leaks %q: %s", leak, formatted)
		}
		if strings.Contains(logs.String(), leak) {
			t.Fatalf("logs leak %q: %s", leak, logs.String())
		}
	}
	if !strings.Contains(formatted, "[redacted]") {
		t.Fatalf("formatted result has no redaction marker: %s", formatted)
	}
	if !strings.Contains(logs.String(), "account_login") {
		t.Fatalf("logs = %q, want an account_login event", logs.String())
	}
}

func TestLoginServiceDefaults(t *testing.T) {
	st := store.NewWithSealer(plainSealer{})
	service, err := NewLoginService(st, LoginConfig{})
	if err != nil {
		t.Fatalf("NewLoginService: %v", err)
	}
	t.Cleanup(service.Close)

	if len(service.hosts) != len(DefaultLoginHosts) {
		t.Fatalf("hosts = %v, want the %d defaults", service.hosts, len(DefaultLoginHosts))
	}
	if service.profile != Linux423Binary {
		t.Fatalf("profile = %+v, want Linux423Binary", service.profile)
	}
	if !service.noSign {
		t.Fatal("noSign = false, want true for the Linux profile")
	}
}

// TestLoginServiceDeviceStable is the B5 regression: two logins for one
// account send the same _uid, and the device identity is generated once,
// persisted in the snapshot and reused instead of regenerated.
func TestLoginServiceDeviceStable(t *testing.T) {
	st := store.NewWithSealer(plainSealer{})
	account := newLoginAccount(t, st)
	server, requests := newFake(t, func(capturedRequest) (int, string) {
		return http.StatusOK, loginReply
	})
	service := newTestLoginService(t, st, server.URL, nil)

	if _, ok := st.AccountDevice(account.ID); ok {
		t.Fatal("a device identity exists before the first login")
	}
	var devices [2]store.DeviceIdentity
	for run := 1; run <= 2; run++ {
		if _, err := service.Login(context.Background(), account.ID); err != nil {
			t.Fatalf("Login %d: %v", run, err)
		}
		device, ok := st.AccountDevice(account.ID)
		if !ok {
			t.Fatalf("no device identity persisted after login %d", run)
		}
		devices[run-1] = device
	}
	// Two logins, each followed by loginSync: keep the login requests only.
	var loginRequests []capturedRequest
	for range 4 {
		request := <-requests
		if request.form.Get("_client_login") == "" {
			loginRequests = append(loginRequests, request)
		}
	}
	if len(loginRequests) != 2 {
		t.Fatalf("login requests = %d, want 2", len(loginRequests))
	}
	first, second := loginRequests[0], loginRequests[1]

	uid := first.form.Get("_uid")
	if len(uid) != 20 {
		t.Fatalf("_uid = %q, want the captured 20-hex form", uid)
	}
	if got := second.form.Get("_uid"); got != uid {
		t.Fatalf("second login _uid = %q, want the first login's %q", got, uid)
	}
	if _, ok := second.form["_os_did"]; ok {
		t.Fatalf("linux body carries _os_did: %v", second.raw)
	}
	if got := second.form.Get("v"); got != LinuxClientVersion {
		t.Fatalf("v = %q, want %q", got, LinuxClientVersion)
	}

	if devices[0] != devices[1] {
		t.Fatalf("device identity changed between logins: %+v -> %+v", devices[0], devices[1])
	}
	device := devices[0]
	if device.UID != uid {
		t.Fatalf("stored UID = %q, want the wire _uid %q", device.UID, uid)
	}
	if len(device.OSDID) != 16 {
		t.Fatalf("stored OSDID = %q, want 16 characters", device.OSDID)
	}
	if device.CUID != device.UID {
		t.Fatalf("stored CUID = %q, want the 20-hex UID %q", device.CUID, device.UID)
	}
	if device.CSEID != "linux" {
		t.Fatalf("stored CSEID = %q, want linux", device.CSEID)
	}
	if device.OSP != "linux" || device.OSD != "x86_64" || device.DIM != devDim {
		t.Fatalf("stored device = %+v, want the captured Linux claims", device)
	}
	if !strings.Contains(device.OSV, "Linux version 6.18.33.2") {
		t.Fatalf("stored OSV = %q, want the captured uname", device.OSV)
	}

	// The identity survives the state snapshot, so a restart keeps it too.
	data, err := st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	restored := store.New()
	if err := restored.Restore(data); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	after, ok := restored.AccountDevice(account.ID)
	if !ok || after != device {
		t.Fatalf("restored device = %+v (present %v), want %+v", after, ok, device)
	}
}

// TestLoginServiceDeviceSurvivesRejection proves the identity is persisted
// before the vendor call, so a rejected login followed by a retry sends the
// same _uid instead of looking like a new device.
func TestLoginServiceDeviceSurvivesRejection(t *testing.T) {
	const rejectReply = `{"Login":{"AuthResult":"WrongPassword","AuthMessage":"bad password"}}`
	st := store.NewWithSealer(plainSealer{})
	account := newLoginAccount(t, st)
	server, requests := newFake(t, func(capturedRequest) (int, string) {
		return http.StatusOK, rejectReply
	})
	service := newTestLoginService(t, st, server.URL, nil)

	for run := 1; run <= 2; run++ {
		if _, err := service.Login(context.Background(), account.ID); !errors.Is(err, ErrAuthRejected) {
			t.Fatalf("Login %d error = %v, want ErrAuthRejected", run, err)
		}
	}
	first, second := <-requests, <-requests

	device, ok := st.AccountDevice(account.ID)
	if !ok {
		t.Fatal("a rejected login did not persist the device identity")
	}
	if device.UID != first.form.Get("_uid") {
		t.Fatalf("stored UID = %q, want the wire _uid %q", device.UID, first.form.Get("_uid"))
	}
	if got := second.form.Get("_uid"); got != first.form.Get("_uid") {
		t.Fatalf("retry _uid = %q, want %q", got, first.form.Get("_uid"))
	}
	if current, ok := st.AccountDevice(account.ID); !ok || current != device {
		t.Fatalf("device identity changed across the retry: %+v", current)
	}
}

func TestLoginServiceForget(t *testing.T) {
	st := store.NewWithSealer(plainSealer{})
	account := newLoginAccount(t, st)
	service := newTestLoginService(t, st, "http://127.0.0.1:1", nil)

	if !service.remember(account.ID, nil, LoginResult{
		AuthResult: "OK",
		Servers:    map[string]apifacade.Server{"s1": {ID: "s1", Sts: "ok"}},
		FetchedAt:  time.Now().UTC(),
	}) {
		t.Fatal("remember refused a live result")
	}
	if _, ok := service.Result(account.ID); !ok {
		t.Fatal("Result did not see the retained login")
	}
	service.Forget(account.ID)
	if _, ok := service.Result(account.ID); ok {
		t.Fatal("Forget kept the retained login")
	}
	service.Forget(account.ID) // idempotent
}
