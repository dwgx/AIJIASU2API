package account

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"ajsproxy/apifacade"
)

// Options configures a Control. The zero value is usable and signs requests:
// the api package's HTTP client, the OpenAJS user agent and time.Now apply.
type Options struct {
	// HTTPClient overrides the transport. Nil keeps the api package's
	// 35-second-timeout client.
	HTTPClient *http.Client

	// UserAgent overrides the User-Agent header. Empty keeps "okhttp/4.9.3".
	UserAgent string

	// NoSigning omits X-Timestamp and X-Sig. Signing is the default (the zero
	// Options signs), matching OpenAJS. The Linux 4.2.3.0 capture is unsigned
	// and its signing secret is [UNKNOWN], so that profile sets this.
	NoSigning bool

	// RequestShape selects the control request field set. The zero value is
	// ShapeOpenAJS; the Linux 4.2.3.0 profile passes ShapeLinux423.
	RequestShape apifacade.RequestShape

	// Now overrides the clock used for X-Timestamp. Nil uses time.Now.
	Now func() time.Time
}

// Control is the daemon-side account control client: login, loginSync and the
// server list for one device identity. It wraps the emulator's tested control
// client and performs no I/O until a method is called.
//
// A Control keeps the session credentials of the last successful Login and is
// not safe for concurrent use; the session factory owns one per account.
type Control struct {
	client *apifacade.Client
}

// NewControl builds a control client for hosts. device must carry generated
// IDs; use NewDevice.
func NewControl(hosts []string, device apifacade.Device, opts Options) *Control {
	client := apifacade.NewClient(hosts, device)
	if opts.HTTPClient != nil {
		client.HTTP = opts.HTTPClient
	}
	if opts.UserAgent != "" {
		client.UserAgent = opts.UserAgent
	}
	if opts.NoSigning {
		client.NoSign = true
	}
	client.RequestShape = opts.RequestShape
	if opts.Now != nil {
		client.Now = opts.Now
	}
	return &Control{client: client}
}

// LoginResult is the credential and membership view of a login or loginSync.
//
// Token and ConnectPassword are Secrets: printing or marshalling the struct
// yields "[redacted]" for both. AuthResult is "OK" on success. LoginSync leaves
// AuthResult and AuthMessage empty because the underlying client reduces the
// sync response to credentials and servers.
type LoginResult struct {
	AuthResult       string
	AuthMessage      string
	Token            Secret
	UserName         string
	ConnectPassword  Secret
	MembershipStatus string
	ExpireTime       int64
	Expired          bool
	// UseCount is the vendor's purchased simultaneous-connection allowance
	// (同时连接数), normalized to 1..2000. A response that omits the field
	// defaults to one.
	UseCount int
	Servers  map[string]apifacade.Server
	// FetchedAt is when this result was retained. The Lab catalog uses it as
	// the snapshot timestamp; it is not a credential.
	FetchedAt time.Time
}

// AuthError reports a login response whose AuthResult is not "OK". It carries
// the server's result code and message, never credentials.
type AuthError struct {
	Result  string
	Message string
}

// Error implements error.
func (e *AuthError) Error() string {
	result := e.Result
	if result == "" {
		result = "no AuthResult"
	}
	if e.Message == "" {
		return fmt.Sprintf("account: login rejected: %s", result)
	}
	return fmt.Sprintf("account: login rejected: %s: %s", result, e.Message)
}

// Login performs a fresh ClientApiLogin/Login. On success the credentials are
// stored for LoginSync and Servers.
//
// A non-OK AuthResult is returned as an *AuthError together with the parsed
// result. Transport and protocol failures are returned as-is. Failover follows
// the api package: only a transport error advances to the next host, never an
// HTTP error status or invalid JSON.
func (c *Control) Login(ctx context.Context, username string, password Secret) (LoginResult, error) {
	result, err := c.client.Login(ctx, username, password.Reveal())
	if err != nil {
		return LoginResult{}, err
	}
	out := convertLogin(result)
	if !result.OK() {
		return out, &AuthError{Result: result.AuthResult, Message: result.AuthMessage}
	}
	return out, nil
}

// LoginSync refreshes ConnectPassword with the stored login token and returns
// the refreshed credentials plus the server map. It fails with
// apifacade.ErrNoLoginToken before a successful Login.
func (c *Control) LoginSync(ctx context.Context) (LoginResult, error) {
	servers, err := c.client.LoginSync(ctx)
	if err != nil {
		return LoginResult{}, err
	}
	return c.snapshot(servers), nil
}

// Servers returns the server map from LoginSync.
func (c *Control) Servers(ctx context.Context) (map[string]apifacade.Server, error) {
	return c.client.Servers(ctx)
}

// Close releases idle HTTP connections. It performs no vendor call and does not
// log out: the emulated API surface has no logout command.
func (c *Control) Close() {
	if c == nil || c.client == nil || c.client.HTTP == nil {
		return
	}
	c.client.HTTP.CloseIdleConnections()
}

func convertLogin(result *apifacade.LoginResult) LoginResult {
	return LoginResult{
		AuthResult:       result.AuthResult,
		AuthMessage:      result.AuthMessage,
		Token:            Secret(result.LoginToken),
		UserName:         result.UserName,
		ConnectPassword:  Secret(result.ConnectPassword),
		MembershipStatus: result.MembershipStatus,
		ExpireTime:       result.ExpireTime,
		Expired:          result.Expired,
		UseCount:         NormalizeUseCount(result.UseCount),
	}
}

func (c *Control) snapshot(servers map[string]apifacade.Server) LoginResult {
	return LoginResult{
		Token:           Secret(c.client.LoginToken),
		UserName:        c.client.UserName,
		ConnectPassword: Secret(c.client.ConnectPassword),
		UseCount:        NormalizeUseCount(0),
		Servers:         servers,
	}
}
