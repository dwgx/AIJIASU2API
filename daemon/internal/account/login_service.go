package account

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/store"
	"ajsproxy/apifacade"
)

// DefaultLoginHosts are the control API base URLs the shipping clients carry
// (PROTOCOL.md §API hosts). They are only contacted when a LoginService is
// explicitly enabled; constructing one performs no I/O.
var DefaultLoginHosts = []string{
	"https://login.ajs-app.com",
	"https://login.mmegg.com",
	"https://login.mmegg.com:4443",
	"https://login.mbclubsh.com",
}

// Login errors are matched with errors.Is by the API layer.
var (
	// ErrDisabled reports a service constructed with LoginConfig.Enabled
	// false. The API never wires such a service, but Login still refuses.
	ErrDisabled = errors.New("account: login is disabled")
	// ErrClosed reports a Login after Close.
	ErrClosed = errors.New("account: login service is closed")
	// ErrNoSecret reports an account without a usable sealed password.
	ErrNoSecret = errors.New("account: no stored secret")
	// ErrAuthRejected reports a vendor AuthResult other than "OK".
	ErrAuthRejected = errors.New("account: authentication rejected")
	// ErrUpstream reports a transport or protocol failure. The account's
	// stored credentials are not marked bad for it.
	ErrUpstream = errors.New("account: upstream unavailable")
)

// LoginConfig configures a LoginService. The zero value is disabled and uses
// the four DefaultLoginHosts, the Linux423Binary profile, the api package's
// 35-second HTTP client, signing as the profile says, time.Now and
// slog.Default.
type LoginConfig struct {
	// Hosts are the control API base URLs, tried in order. Empty selects
	// DefaultLoginHosts. Blank entries are ignored.
	Hosts []string

	// Profile selects _v and the signing mode. A zero value selects
	// Linux423Binary. The profile's SigningMode "none" disables signing even
	// when NoSigning is false.
	Profile VersionProfile

	// HTTPClient overrides the transport. Nil keeps the api package's client.
	HTTPClient *http.Client

	// NoSigning omits X-Timestamp and X-Sig.
	NoSigning bool

	// Now overrides the clock used for X-Timestamp. Nil uses time.Now.
	Now func() time.Time

	// Enabled is the explicit switch. A service built with Enabled false
	// refuses Login with ErrDisabled.
	Enabled bool

	// Logger receives account-level events. Nil uses slog.Default.
	Logger *slog.Logger
}

// LoginService performs a fresh vendor login for one account and records the
// outcome in the store. It is the only daemon path that can contact a control
// API host, and it exists only when the operator enables it.
//
// LoginResult values are kept in memory for the future session factory; they
// are never written to the store or a log. A LoginService is safe for
// concurrent use. Close releases the idle HTTP connections of the controls it
// keeps and is idempotent.
type LoginService struct {
	store   *store.Store
	cfg     LoginConfig
	hosts   []string
	profile VersionProfile
	noSign  bool
	logger  *slog.Logger

	mu       sync.Mutex
	results  map[string]LoginResult
	controls map[string]*Control
	closed   bool
}

// NewLoginService validates cfg and returns a service. It performs no I/O and
// contacts no host: the device identity is generated and the control client is
// built inside Login.
func NewLoginService(st *store.Store, cfg LoginConfig) (*LoginService, error) {
	if st == nil {
		return nil, errors.New("account: login service requires a store")
	}
	hosts := make([]string, 0, len(cfg.Hosts))
	for _, host := range cfg.Hosts {
		if host = strings.TrimSpace(host); host != "" {
			hosts = append(hosts, host)
		}
	}
	if len(hosts) == 0 {
		hosts = append(hosts, DefaultLoginHosts...)
	}
	profile := cfg.Profile
	if profile == (VersionProfile{}) {
		profile = Linux423Binary
	}
	if strings.TrimSpace(profile.ClientVersion) == "" {
		return nil, errors.New("account: login profile has no client version")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &LoginService{
		store:    st,
		cfg:      cfg,
		hosts:    hosts,
		profile:  profile,
		noSign:   cfg.NoSigning || profile.SigningMode == "none",
		logger:   logger,
		results:  make(map[string]LoginResult),
		controls: make(map[string]*Control),
	}, nil
}

// Login authenticates accountID against the configured hosts and records the
// result. A missing account is a store.ErrNotFound; an account without a
// sealed password is ErrNoSecret; a non-OK AuthResult marks the account
// auth_failed and returns ErrAuthRejected; a transport or protocol failure
// returns ErrUpstream without touching the account state.
//
// The password is put on the wire and is never logged, returned or stored.
func (s *LoginService) Login(ctx context.Context, accountID string) (model.Account, error) {
	if !s.cfg.Enabled {
		return model.Account{}, ErrDisabled
	}
	if s.isClosed() {
		return model.Account{}, ErrClosed
	}
	account, ok := s.store.Account(accountID)
	if !ok {
		return model.Account{}, &store.Error{
			Kind:    store.ErrNotFound,
			Message: fmt.Sprintf("account %q not found", accountID),
		}
	}
	plaintext, err := s.store.AccountSecret(accountID)
	if err != nil {
		s.logger.Warn("account_secret_unavailable", "account", accountID, "error", err)
		return model.Account{}, fmt.Errorf("%w for account %q", ErrNoSecret, accountID)
	}
	password := Secret(string(plaintext))
	clear(plaintext)

	device, err := NewDevice(s.profile.ClientVersion)
	if err != nil {
		return model.Account{}, fmt.Errorf("account: device for %q: %w", accountID, err)
	}
	control := NewControl(s.hosts, device, Options{
		HTTPClient: s.cfg.HTTPClient,
		NoSigning:  s.noSign,
		Now:        s.cfg.Now,
	})

	result, err := control.Login(ctx, account.Username, password)
	if err != nil {
		var authErr *AuthError
		if errors.As(err, &authErr) {
			control.Close()
			reason := strings.TrimSpace(authErr.Message)
			if reason == "" {
				reason = strings.TrimSpace(authErr.Result)
			}
			if _, markErr := s.store.MarkAuthFailed(accountID, reason); markErr != nil {
				return model.Account{}, fmt.Errorf("account: record auth failure for %q: %w", accountID, markErr)
			}
			s.logger.Warn("account_login_rejected", "account", accountID, "result", authErr.Result)
			if reason == "" {
				return model.Account{}, ErrAuthRejected
			}
			return model.Account{}, fmt.Errorf("%w: %s", ErrAuthRejected, reason)
		}
		control.Close()
		s.logger.Warn("account_login_upstream_error", "account", accountID, "error", err)
		return model.Account{}, fmt.Errorf("%w: %v", ErrUpstream, err)
	}

	updated, err := s.store.MarkLogin(accountID, result.UserName, result.MembershipStatus, result.Expired)
	if err != nil {
		control.Close()
		return model.Account{}, fmt.Errorf("account: record login for %q: %w", accountID, err)
	}
	if !s.remember(accountID, control, result) {
		control.Close()
		return model.Account{}, ErrClosed
	}
	s.logger.Info("account_login", "account", accountID,
		"state", updated.State, "membership", result.MembershipStatus)
	return updated, nil
}

// Result returns the last successful login result for accountID. The result
// carries Secrets, which format as "[redacted]"; callers must not reveal them
// outside the wire path.
func (s *LoginService) Result(accountID string) (LoginResult, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, ok := s.results[accountID]
	if !ok {
		return LoginResult{}, false
	}
	return cloneLoginResult(result), true
}

// Close releases every idle HTTP connection and drops the retained login
// results. It performs no vendor call and is idempotent.
func (s *LoginService) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	controls := make([]*Control, 0, len(s.controls))
	for id, control := range s.controls {
		controls = append(controls, control)
		delete(s.controls, id)
	}
	clear(s.results)
	s.mu.Unlock()

	for _, control := range controls {
		control.Close()
	}
}

func (s *LoginService) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// remember stores the login result and its control for the future session
// factory. It reports false when Close won the race.
func (s *LoginService) remember(accountID string, control *Control, result LoginResult) bool {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return false
	}
	previous := s.controls[accountID]
	s.controls[accountID] = control
	s.results[accountID] = cloneLoginResult(result)
	s.mu.Unlock()

	if previous != nil && previous != control {
		previous.Close()
	}
	return true
}

// cloneLoginResult copies the result and its Servers map so a caller cannot
// mutate the retained copy.
func cloneLoginResult(in LoginResult) LoginResult {
	out := in
	if in.Servers != nil {
		out.Servers = make(map[string]apifacade.Server, len(in.Servers))
		for id, server := range in.Servers {
			server.Cat = append([]string(nil), server.Cat...)
			server.Loc = append([]apifacade.ServerLoc(nil), server.Loc...)
			out.Servers[id] = server
		}
	}
	return out
}
