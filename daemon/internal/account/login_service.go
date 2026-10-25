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
// The account's device identity is loaded from the store and generated once on
// the first login; later logins reuse it. LoginResult values are kept in memory
// for the node controller; they are never written to the store or a log.
// A LoginService is safe for concurrent use. Close releases the idle HTTP
// connections of the controls it keeps and is idempotent.
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
	// started records when the retained login attempt began. The node
	// controller compares it with a stop fence: a login that was already in
	// flight when the account was stopped completes with a fresh FetchedAt but
	// is not an operator reopen.
	started map[string]time.Time
	closed  bool

	// accountLocks serialize one account's login attempt against Forget. A
	// logout or delete must not return while an in-flight login can still
	// retain a result for it, so Forget waits for that login and then drops
	// whatever it retained.
	accountLocks map[string]*sync.Mutex
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
		store:        st,
		cfg:          cfg,
		hosts:        hosts,
		profile:      profile,
		noSign:       cfg.NoSigning || profile.SigningMode == "none",
		logger:       logger,
		results:      make(map[string]LoginResult),
		controls:     make(map[string]*Control),
		started:      make(map[string]time.Time),
		accountLocks: make(map[string]*sync.Mutex),
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
	// The attempt starts now, before it waits for another login of the same
	// account: a stop fence installed after this instant must refuse the
	// result even when the vendor call completes later.
	startedAt := s.now()
	unlock := s.lockAccount(accountID)
	defer unlock()
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

	identity, err := s.deviceForSlot(model.SlotKey{AccountID: accountID, Index: 1})
	if err != nil {
		return model.Account{}, err
	}
	control := NewControl(s.hosts, deviceFromIdentity(identity, s.profile.ClientVersion), Options{
		HTTPClient:   s.cfg.HTTPClient,
		NoSigning:    s.noSign,
		RequestShape: s.profile.RequestShape,
		Now:          s.cfg.Now,
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

	updated, err := s.store.MarkLogin(accountID, result.UserName, result.MembershipStatus, result.Expired, result.UseCount)
	if err != nil {
		control.Close()
		return model.Account{}, fmt.Errorf("account: record login for %q: %w", accountID, err)
	}
	// The Login response carries credentials only. Every shipping client
	// follows it with loginSync to fetch the server list, and the node
	// controller needs that list to open one gate per server. A sync failure
	// does not fail the login: the account is already marked active and the
	// next login retries the sync. A failed or empty sync is "unknown", not
	// "the account has no servers"; remember keeps the previously retained
	// list, because replacing it with an empty map would make the node
	// controller tear down every gate and node of the account.
	if synced, syncErr := control.LoginSync(ctx); syncErr != nil {
		s.logger.Warn("account_login_sync_failed", "account", accountID, "error", syncErr)
	} else if len(synced.Servers) == 0 {
		s.logger.Warn("account_login_sync_empty", "account", accountID)
	} else {
		if synced.UserName != "" {
			result.UserName = synced.UserName
		}
		if synced.ConnectPassword != "" {
			result.ConnectPassword = synced.ConnectPassword
		}
		result.Servers = synced.Servers
	}
	if !s.remember(accountID, control, result, startedAt) {
		control.Close()
		return model.Account{}, ErrClosed
	}
	s.logger.Info("account_login", "account", accountID,
		"state", updated.State, "membership", result.MembershipStatus)
	return updated, nil
}

// deviceFor returns slot 1's device identity. It is the compatibility entry
// point used by tests and by callers that still speak in account ids.
func (s *LoginService) deviceFor(accountID string) (store.DeviceIdentity, error) {
	return s.deviceForSlot(model.SlotKey{AccountID: accountID, Index: 1})
}

// deviceForSlot returns one slot's device identity for login. With the device
// pool enabled it binds that slot to a sealed pool record through
// BindForLoginSlot: a retry or a concurrent login gets the same material
// because the binding is first-writer-wins per slot, and a missing or corrupt
// entry fails closed instead of regenerating an identity. Without the pool
// (tests and mock mode) only slot 1 has the legacy in-memory path.
func (s *LoginService) deviceForSlot(key model.SlotKey) (store.DeviceIdentity, error) {
	if s.store.DevicePoolEnabled() {
		material, err := s.store.BindForLoginSlot(key, s.profile.Name)
		if err != nil {
			return store.DeviceIdentity{}, fmt.Errorf("account: bind device for %s: %w", key.String(), err)
		}
		return material.Identity, nil
	}
	if key.Index != 1 {
		return store.DeviceIdentity{}, fmt.Errorf("account: device pool is disabled for %s", key.String())
	}
	if identity, ok := s.store.AccountDeviceSlot(key); ok {
		return identity, nil
	}
	device, err := NewDeviceForProfile(s.profile)
	if err != nil {
		return store.DeviceIdentity{}, fmt.Errorf("account: device for %q: %w", key.AccountID, err)
	}
	identity, err := s.store.EnsureAccountDevice(key.AccountID, Identify(device))
	if err != nil {
		return store.DeviceIdentity{}, fmt.Errorf("account: store device for %q: %w", key.AccountID, err)
	}
	return identity, nil
}

// now is the service clock: the configured Now when set, else the wall clock.
// It stamps FetchedAt and the attempt start, so a pinned clock makes the
// controller's stop-fence comparison deterministic in tests.
func (s *LoginService) now() time.Time {
	if s.cfg.Now != nil {
		return s.cfg.Now().UTC()
	}
	return time.Now().UTC()
}

// lockAccount serializes one account's login attempt against Forget. The lock
// map is guarded by mu and never shrinks: its entries are bounded by the
// number of accounts the daemon has logged in.
func (s *LoginService) lockAccount(accountID string) func() {
	s.mu.Lock()
	lock := s.accountLocks[accountID]
	if lock == nil {
		lock = &sync.Mutex{}
		s.accountLocks[accountID] = lock
	}
	s.mu.Unlock()
	lock.Lock()
	return lock.Unlock
}

// LoginStarted returns when the retained login attempt for accountID began.
// The node controller compares it with a stop fence, so a login that was
// already in flight when the account was logged out or deleted cannot clear
// the fence by completing afterwards.
func (s *LoginService) LoginStarted(accountID string) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	started, ok := s.started[accountID]
	return started, ok
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

// Forget drops the retained login result and closes its control for accountID.
// Logout and delete call it so the catalog cannot keep serving a snapshot for
// an account with no session (DESIGN-CATALOG §3.12). It performs no vendor call
// and is idempotent.
//
// It waits for an in-flight login of the same account, then drops whatever
// that login retained: a logout that committed while the vendor call was still
// running must not leave a result the controller could sync later.
func (s *LoginService) Forget(accountID string) {
	unlock := s.lockAccount(accountID)
	defer unlock()
	s.mu.Lock()
	control := s.controls[accountID]
	delete(s.controls, accountID)
	delete(s.results, accountID)
	delete(s.started, accountID)
	s.mu.Unlock()
	if control != nil {
		control.Close()
	}
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
	clear(s.started)
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
// factory. It reports false when Close won the race. startedAt is when the
// attempt began, not when it finished: the controller's stop fence uses it to
// refuse a login that was already in flight when the account was stopped.
//
// An empty server list never replaces a retained non-empty one: a failed or
// empty loginSync means "unknown", and the node controller treats an empty
// list as "delete every node". Enforcing that here also closes the race
// between two concurrent logins for the same account.
func (s *LoginService) remember(accountID string, control *Control, result LoginResult, startedAt time.Time) bool {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return false
	}
	if len(result.Servers) == 0 {
		if previous, ok := s.results[accountID]; ok && len(previous.Servers) > 0 {
			result.Servers = cloneLoginResult(previous).Servers
			if result.FetchedAt.IsZero() {
				result.FetchedAt = previous.FetchedAt
			}
		}
	}
	previous := s.controls[accountID]
	s.controls[accountID] = control
	if result.FetchedAt.IsZero() {
		result.FetchedAt = s.now()
	}
	s.results[accountID] = cloneLoginResult(result)
	s.started[accountID] = startedAt
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
