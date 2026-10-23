package api

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"reflect"
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

	// NoSign omits the X-Timestamp and X-Sig headers. OpenAJS signs; the Linux
	// 4.2.3.0 capture is unsigned, so a caller emulating that client must set
	// this. The zero value signs.
	NoSign bool

	// RequestShape selects the control request field set and headers. The zero
	// value is ShapeOpenAJS; ShapeLinux423 reproduces the captured Linux
	// 4.2.3.0 request (control.jsonl line 2).
	RequestShape RequestShape

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

// expireTimeLayouts are the date/time layouts the vendor may send in
// Login.ExpireTime. Layouts without an explicit zone are parsed in
// time.Local; a zone or offset inside the value wins over the local zone.
var expireTimeLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02 15:04:05",
	"2006-01-02T15:04:05",
	"2006-01-02",
}

// expireTimeShapes lists the accepted ExpireTime shapes; unknown values echo
// it so the operator can see what the decoder wants.
const expireTimeShapes = `a JSON number of Unix milliseconds, a numeric ` +
	`string, RFC3339, "2006-01-02 15:04:05", "2006-01-02T15:04:05", ` +
	`"2006-01-02", or empty`

// UnmarshalJSON decodes the Login object, accepting both vendor shapes of
// ExpireTime into the int64 field, which always holds Unix milliseconds:
//
//   - a JSON number is preserved verbatim (the offline fixtures carry
//     milliseconds, e.g. 1767225600000);
//   - a string holds either the same number as text ("1767225600000") or a
//     date/time in one of expireTimeLayouts, converted with UnixMilli;
//   - an empty string, null, or an absent field decodes to 0.
//
// Date strings without a zone are parsed in time.Local; RFC3339 values keep
// their own offset. A value matching no shape returns an error naming only
// the ExpireTime value and the accepted shapes, never the surrounding
// response or the session credentials.
func (l *LoginResult) UnmarshalJSON(data []byte) error {
	// loginResult mirrors LoginResult without its methods, so decoding the
	// other fields does not recurse into UnmarshalJSON.
	type loginResult LoginResult
	var aux struct {
		loginResult
		ExpireTime json.RawMessage `json:"ExpireTime"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	*l = LoginResult(aux.loginResult)
	ms, err := parseExpireTime(aux.ExpireTime)
	if err != nil {
		return err
	}
	l.ExpireTime = ms
	return nil
}

// parseExpireTime converts one raw ExpireTime JSON value to Unix
// milliseconds. A nil or null value means the field was absent or empty.
func parseExpireTime(raw json.RawMessage) (int64, error) {
	value := bytes.TrimSpace(raw)
	if len(value) == 0 || string(value) == "null" {
		return 0, nil
	}
	if value[0] == '"' {
		var text string
		if err := json.Unmarshal(value, &text); err != nil {
			return 0, fmt.Errorf("api: Login.ExpireTime: %w", err)
		}
		return parseExpireTimeString(text)
	}
	var ms int64
	if err := json.Unmarshal(value, &ms); err != nil {
		return 0, fmt.Errorf("api: Login.ExpireTime: unsupported value %s: want %s", value, expireTimeShapes)
	}
	return ms, nil
}

// parseExpireTimeString converts one ExpireTime string: the millisecond
// count as text, or a date/time layout (zoneless layouts in time.Local).
func parseExpireTimeString(text string) (int64, error) {
	if text == "" {
		return 0, nil
	}
	if ms, err := strconv.ParseInt(text, 10, 64); err == nil {
		return ms, nil
	}
	for _, layout := range expireTimeLayouts {
		if t, err := time.ParseInLocation(layout, text, time.Local); err == nil {
			return t.UnixMilli(), nil
		}
	}
	return 0, fmt.Errorf("api: Login.ExpireTime: unsupported value %q: want %s", text, expireTimeShapes)
}

// FlexInt is an int that decodes both shapes the vendor sends for the server
// numeric fields: a JSON number (a fraction or exponent is rejected) or a
// numeric string ("443"). null and the empty string decode to 0, so a live
// response can omit them the way the offline fixtures do. MarshalJSON is the
// default, so FlexInt still encodes as a bare JSON number.
type FlexInt int

// flexIntShapes lists the accepted FlexInt JSON shapes for error messages.
const flexIntShapes = `a JSON number (no fraction) or a numeric string`

// UnmarshalJSON decodes one FlexInt value. A value matching no shape returns
// a *json.UnmarshalTypeError, which the decoder decorates with the failing
// field path (e.g. ServerFV.Servers.par.fv.p) — the same shape as the live
// failure message. The Value names the offending value and the accepted
// shapes; no error ever echoes the per-server secret or the session
// credentials.
func (f *FlexInt) UnmarshalJSON(data []byte) error {
	value := bytes.TrimSpace(data)
	if len(value) == 0 || string(value) == "null" {
		*f = 0
		return nil
	}
	unsupported := func(v string) error {
		return &json.UnmarshalTypeError{
			Value: v + " (want " + flexIntShapes + ")",
			Type:  flexIntType,
		}
	}
	if value[0] == '"' {
		var text string
		if err := json.Unmarshal(value, &text); err != nil {
			return unsupported(fmt.Sprintf("string %s", value))
		}
		if text == "" {
			*f = 0
			return nil
		}
		n, err := strconv.ParseInt(text, 10, 0)
		if err != nil {
			return unsupported(fmt.Sprintf("string %q", text))
		}
		*f = FlexInt(n)
		return nil
	}
	// A bare number: json.Number rejects trailing garbage, then ParseInt
	// rejects fractions and exponents the default int decoder would round.
	var num json.Number
	if err := json.Unmarshal(value, &num); err != nil {
		return unsupported(string(value))
	}
	n, err := strconv.ParseInt(num.String(), 10, 0)
	if err != nil {
		return unsupported(string(value))
	}
	*f = FlexInt(n)
	return nil
}

// flexIntType is the reflect.Type UnmarshalTypeError reports for FlexInt.
var flexIntType = reflect.TypeOf(FlexInt(0))

// ServerFV is the par.fv object of a VpnServer.
//
// OpenAJS ignores u and ps and authenticates with Login.UserName /
// Login.ConnectPassword; their purpose is [UNKNOWN].
type ServerFV struct {
	U  FlexInt `json:"u"`
	P  FlexInt `json:"p"`
	PS string  `json:"ps"`
}

// ServerPar is the par object of a VpnServer.
type ServerPar struct {
	FV ServerFV `json:"fv"`
}

// ServerLoc is one entry of VpnServer.loc.
type ServerLoc struct {
	C string  `json:"c"`
	N string  `json:"n"`
	O FlexInt `json:"o"`
}

// Server is one entry of the Servers map.
type Server struct {
	ID   string      `json:"id"`
	Vsid FlexInt     `json:"vsid"`
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
func (s Server) Port() int { return int(s.Par.FV.P) }

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
	params := c.Device.ControlParams(c.RequestShape)
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
	if c.RequestShape == ShapeLinux423 {
		// The captured Linux body ends with the "&" its serializer leaves
		// behind; the signature, when present, covers the exact wire body.
		body += "&"
	}
	var timestamp, sig string
	if !c.NoSign {
		timestamp = strconv.FormatInt(c.Now().UnixMilli(), 10)
		sig = Sign(c.Secret, body, timestamp)
	}

	var lastErr error
	for _, host := range c.Hosts {
		url := strings.TrimRight(host, "/") + "/client.php"
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("api: build request: %w", err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if c.RequestShape == ShapeLinux423 {
			// Captured Linux headers: Accept: */*, "deflate, gzip" and
			// Connection: close, with no User-Agent. A nil User-Agent value
			// suppresses Go's default agent.
			req.Header.Set("Accept", "*/*")
			req.Header.Set("Accept-Encoding", "deflate, gzip")
			req.Header["User-Agent"] = nil
			req.Close = true
		} else {
			req.Header.Set("Accept-Encoding", "gzip")
			req.Header.Set("User-Agent", c.UserAgent)
		}
		if !c.NoSign {
			req.Header.Set("X-Timestamp", timestamp)
			req.Header.Set("X-Sig", sig)
		}

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
//
// ShapeLinux423 sends the captured Linux 4.2.3.0 form: v instead of _v, no
// trigger field, and the trailing "&".
func (c *Client) Login(ctx context.Context, username, password string) (*LoginResult, error) {
	extra := map[string]string{
		"username": username,
		"password": password,
	}
	if c.RequestShape != ShapeLinux423 {
		// The Linux 4.2.3.0 capture carries no trigger field; OpenAJS sends
		// trigger=login on both Login and LoginSync.
		extra["trigger"] = "login"
	}
	data, err := c.post(ctx, "ClientApiLogin/Login", extra, false)
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
//
// Only Login has a Linux 4.2.3.0 capture, so LoginSync keeps the OpenAJS
// trigger/_client_login fields under every request shape; the Linux form of
// this command is [UNVERIFIED].
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
