package api

import (
	"compress/flate"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DefaultUserAgent is the agent string OpenAJS sends (AjsClient.java:76).
const DefaultUserAgent = "okhttp/4.9.3"

// ErrNoLoginToken is returned by authenticated calls before a successful login.
var ErrNoLoginToken = errors.New("api: request requires a login token")

// Client is a control API client for POST <host>/client.php.
//
// It never performs TLS pinning or certificate relaxation; the caller decides
// which hosts are contacted and with which transport. OpenAJS disables TLS
// verification, but an emulator should not copy that.
type Client struct {
	Hosts     []string
	HTTP      *http.Client
	Device    Device
	Secret    string
	UserAgent string
	Now       func() time.Time

	// Session state filled by Login / LoginSync.
	LoginToken      string
	UserName        string
	ConnectPassword string
}

// NewClient returns a client for the given API base URLs. The device must have
// IDs generated already; use Device.EnsureIDs.
func NewClient(hosts []string, device Device) *Client {
	return &Client{
		Hosts:     append([]string(nil), hosts...),
		HTTP:      &http.Client{Timeout: 35 * time.Second},
		Device:    device,
		Secret:    DefaultSecret,
		UserAgent: DefaultUserAgent,
		Now:       time.Now,
	}
}

// LoginResult is the Login object of a ClientApiLogin/Login response.
type LoginResult struct {
	AuthResult       string `json:"AuthResult"`
	AuthMessage      string `json:"AuthMessage"`
	LoginToken       string `json:"LoginToken"`
	UserName         string `json:"UserName"`
	ConnectPassword  string `json:"ConnectPassword"`
	Email            string `json:"Email"`
	PhoneNumber      string `json:"PhoneNumber"`
	MembershipStatus string `json:"MembershipStatus"`
	GroupTitle       string `json:"GroupTitle"`
	ExpireTime       int64  `json:"ExpireTime"`
	Expired          bool   `json:"Expired"`
	UseCount         int    `json:"UseCount"`
}

// OK reports whether AuthResult is exactly "OK" (case-sensitive, like
// LoginResult.isOk in OpenAJS).
func (l *LoginResult) OK() bool { return l != nil && l.AuthResult == "OK" }

// ServerFV is the par.fv object of a VpnServer.
//
// OpenAJS ignores u and ps and authenticates with Login.UserName /
// Login.ConnectPassword; their purpose is [UNKNOWN].
type ServerFV struct {
	U  int    `json:"u"`
	P  int    `json:"p"`
	PS string `json:"ps"`
}

// ServerPar is the par object of a VpnServer.
type ServerPar struct {
	FV ServerFV `json:"fv"`
}

// ServerLoc is one entry of VpnServer.loc.
type ServerLoc struct {
	C string `json:"c"`
	N string `json:"n"`
	O int    `json:"o"`
}

// Server is one entry of the Servers map.
type Server struct {
	ID   string      `json:"id"`
	Vsid int         `json:"vsid"`
	Ctry string      `json:"ctry"`
	Svr  string      `json:"svr"`
	Tit  string      `json:"tit"`
	Cmt  string      `json:"cmt"`
	Sts  string      `json:"sts"`
	Ord  string      `json:"ord"`
	Cat  []string    `json:"cat"`
	Par  ServerPar   `json:"par"`
	Loc  []ServerLoc `json:"loc"`
}

// Port returns par.fv.p, the FV TCP port.
func (s Server) Port() int { return s.Par.FV.P }

// Addr returns the FV endpoint as host:port.
func (s Server) Addr() string {
	if s.Svr == "" || s.Port() == 0 {
		return ""
	}
	return net.JoinHostPort(s.Svr, strconv.Itoa(s.Port()))
}

// OK reports whether the server is marked usable (sts == "ok").
func (s Server) OK() bool { return s.Sts == "ok" }

// Post sends one signed /client.php request. auth adds _client_login and
// username as callAuth does. Failover to the next host happens only on a
// transport error, matching AjsClient.call; an HTTP error status or invalid
// JSON is returned to the caller.
func (c *Client) post(ctx context.Context, cmd string, extra map[string]string, auth bool) ([]byte, error) {
	if len(c.Hosts) == 0 {
		return nil, errors.New("api: no hosts configured")
	}
	params := c.Device.CommonParams()
	params["cmd"] = cmd
	for k, v := range extra {
		params[k] = v
	}
	if auth {
		if c.LoginToken == "" {
			return nil, ErrNoLoginToken
		}
		params["_client_login"] = c.LoginToken
		params["username"] = c.authUserName()
	}
	body := BuildQuery(params)
	timestamp := strconv.FormatInt(c.Now().UnixMilli(), 10)
	sig := Sign(c.Secret, body, timestamp)

	var lastErr error
	for _, host := range c.Hosts {
		url := strings.TrimRight(host, "/") + "/client.php"
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("api: build request: %w", err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept-Encoding", "gzip")
		req.Header.Set("User-Agent", c.UserAgent)
		req.Header.Set("X-Timestamp", timestamp)
		req.Header.Set("X-Sig", sig)

		resp, err := c.HTTP.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		data, readErr := decodeBody(resp)
		resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("api: read response: %w", readErr)
		}
		return data, nil
	}
	return nil, fmt.Errorf("api: all hosts failed: %w", lastErr)
}

func (c *Client) authUserName() string {
	if c.UserName != "" {
		return c.UserName
	}
	if i := strings.IndexByte(c.LoginToken, '|'); i > 0 {
		return c.LoginToken[:i]
	}
	return c.LoginToken
}

func decodeBody(resp *http.Response) ([]byte, error) {
	var r io.Reader = resp.Body
	switch strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding"))) {
	case "gzip":
		zr, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		r = zr
	case "deflate":
		fr := flate.NewReader(resp.Body)
		defer fr.Close()
		r = fr
	}
	return io.ReadAll(r)
}

// Login performs ClientApiLogin/Login and stores the session credentials on
// success. A non-OK AuthResult is returned as a LoginResult, not as an error.
func (c *Client) Login(ctx context.Context, username, password string) (*LoginResult, error) {
	data, err := c.post(ctx, "ClientApiLogin/Login", map[string]string{
		"username": username,
		"password": password,
		"trigger":  "login",
	}, false)
	if err != nil {
		return nil, err
	}
	var env struct {
		Login *LoginResult `json:"Login"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("api: login: invalid JSON: %w", err)
	}
	if env.Login == nil {
		return nil, errors.New("api: login: response has no Login object")
	}
	if env.Login.OK() {
		c.LoginToken = env.Login.LoginToken
		c.UserName = env.Login.UserName
		c.ConnectPassword = env.Login.ConnectPassword
	}
	return env.Login, nil
}

// LoginSync refreshes ConnectPassword and returns the Servers map, using the
// stored login token. The Servers map is empty when the field is absent.
func (c *Client) LoginSync(ctx context.Context) (map[string]Server, error) {
	data, err := c.post(ctx, "ClientApiLogin/Login", map[string]string{
		"trigger": "login",
	}, true)
	if err != nil {
		return nil, err
	}
	var env struct {
		Login   *LoginResult      `json:"Login"`
		Servers map[string]Server `json:"Servers"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("api: loginSync: invalid JSON: %w", err)
	}
	if env.Login != nil {
		if env.Login.ConnectPassword != "" {
			c.ConnectPassword = env.Login.ConnectPassword
		}
		if env.Login.UserName != "" {
			c.UserName = env.Login.UserName
		}
	}
	if env.Servers == nil {
		env.Servers = map[string]Server{}
	}
	return env.Servers, nil
}

// Servers calls LoginSync and returns the server map.
func (c *Client) Servers(ctx context.Context) (map[string]Server, error) {
	return c.LoginSync(ctx)
}
