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
