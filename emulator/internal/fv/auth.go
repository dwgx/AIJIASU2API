package fv

import (
	"context"
	"crypto"
	"crypto/rsa"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// AuthConfig is the field set of the FV authentication body
// (FvConnection.java:42-58).
type AuthConfig struct {
	User        string // u
	Password    string // p
	DeviceID    string // cuid and cseid, the same 16-hex device id
	Site        string // st
	AppVersion  string // v
	CoreVersion string // cv
	OSP         string // osp
	OSD         string // osd
	OSV         string // osv
	Dim         string // dim
}

// DefaultAuthConfig returns the OpenAJS-compatible auth field set.
func DefaultAuthConfig(user, password, deviceID string) AuthConfig {
	return AuthConfig{
		User:        user,
		Password:    password,
		DeviceID:    deviceID,
		Site:        "ajs",
		AppVersion:  "4.11.10.0",
		CoreVersion: "20260525-1",
		OSP:         "android",
		OSD:         "Xiaomi 23013RK75C",
		OSV:         "13",
		Dim:         "w=1080&h=2400&d=440",
	}
}

// BuildAuthBody serializes the authentication body as one key=value line per
// field, each terminated by "\n", in the OpenAJS insertion order:
// t, c, u, p, cuid, cseid, st, v, cv, osp, osd, osv, dim, ce.
// Newlines inside values become spaces; there is no other escaping.
func BuildAuthBody(cfg AuthConfig, unixSeconds int64) []byte {
	fields := []struct{ key, value string }{
		{"t", strconv.FormatInt(unixSeconds, 10)},
		{"c", "au"},
		{"u", cfg.User},
		{"p", cfg.Password},
		{"cuid", cfg.DeviceID},
		{"cseid", cfg.DeviceID},
		{"st", cfg.Site},
		{"v", cfg.AppVersion},
		{"cv", cfg.CoreVersion},
		{"osp", cfg.OSP},
		{"osd", cfg.OSD},
		{"osv", cfg.OSV},
		{"dim", cfg.Dim},
		{"ce", ""},
	}
	var b strings.Builder
	for _, f := range fields {
		b.WriteString(f.key)
		b.WriteByte('=')
		b.WriteString(sanitizeAuthValue(f.value))
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

func sanitizeAuthValue(v string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(v)
}

// AuthOptions overrides the crypto and timeout defaults of Authenticate.
type AuthOptions struct {
	// PublicKey defaults to VendorPublicKey().
	PublicKey *rsa.PublicKey
	// OAEPHash defaults to SHA-1.
	OAEPHash crypto.Hash
	// IdleTimeout bounds the wait for the response body after the header
	// text has been read. Default 500 ms.
	IdleTimeout time.Duration
	// HandshakeTimeout bounds dialing, the write, and the response header.
	// Default 20 s.
	HandshakeTimeout time.Duration
	// Now supplies the t= timestamp. Default time.Now.
	Now func() time.Time
}

// AuthError is returned when the server answers but does not accept the
// session (r != "ok").
type AuthError struct {
	Fields map[string]string
}

func (e *AuthError) Error() string {
	r := e.Fields["r"]
	if r == "" {
		r = "missing"
	}
	msg := "fv: authentication rejected by server: r=" + r
	if ft := e.Fields["ft"]; ft != "" {
		msg += " ft=" + ft
	}
	if fm := e.Fields["fm"]; fm != "" {
		msg += " fm=" + fm
	}
	return msg
}

// Session is an authenticated FV control connection plus the key material
// used to open per-flow data channels.
type Session struct {
	Conn      net.Conn
	Key       []byte // AES-128 session key
	HeaderIV  []byte
	BodyIV    []byte
	SID       string
	Fields    map[string]string // merged auth response fields
	Transport Transport
	Addr      string
	OpenedAt  time.Time
}

// Field returns one auth response field, or "".
func (s *Session) Field(name string) string { return s.Fields[name] }

// Close closes the control connection.
func (s *Session) Close() error {
	if s.Conn == nil {
		return nil
	}
	return s.Conn.Close()
}

// OpenStream opens one proxied TCP flow to host:port through the session's
// transport. The returned net.Conn carries plaintext bytes in both directions.
func (s *Session) OpenStream(host string, port int) (net.Conn, error) {
	return s.OpenStreamContext(context.Background(), host, port)
}

// OpenStreamContext is OpenStream with a caller-supplied context.
func (s *Session) OpenStreamContext(ctx context.Context, host string, port int) (net.Conn, error) {
	if s.Transport == nil {
		return nil, errors.New("fv: session has no transport")
	}
	if s.SID == "" {
		return nil, errors.New("fv: session has no sid")
	}
	return s.Transport.OpenData(ctx, s, host, port)
}

// transportAuthenticator is implemented by transports whose authentication
// exchange differs from the raw OpenAJS frame (HTTPUpgrade version 2).
type transportAuthenticator interface {
	authenticate(ctx context.Context, cfg AuthConfig, addr string, opts AuthOptions) (*Session, error)
}

// Authenticate performs the FV authentication exchange over t and returns the
// session. The response must contain r=ok; the sid may be empty, in which case
// OpenStream refuses to open flows.
//
// Transports that implement transportAuthenticator (the binary carrier) own the
// exchange; otherwise the raw OpenAJS frame is used.
func Authenticate(ctx context.Context, cfg AuthConfig, addr string, t Transport, opts AuthOptions) (*Session, error) {
	if t == nil {
		return nil, errors.New("fv: nil transport")
	}
	if a, ok := t.(transportAuthenticator); ok {
		return a.authenticate(ctx, cfg, addr, opts)
	}
	return authenticateFrame(ctx, cfg, addr, t, opts)
}

// authenticateFrame is the OpenAJS raw-frame exchange (variant A): an
// s/n/k/l/_ frame plus the session-key-encrypted auth body.
func authenticateFrame(ctx context.Context, cfg AuthConfig, addr string, t Transport, opts AuthOptions) (*Session, error) {
	if t == nil {
		return nil, errors.New("fv: nil transport")
	}
	pub := opts.PublicKey
	if pub == nil {
		var err error
		pub, err = VendorPublicKey()
		if err != nil {
			return nil, err
		}
	}
	oaepHash := opts.OAEPHash
	if oaepHash == 0 {
		oaepHash = crypto.SHA1
	}
	handshakeTimeout := opts.HandshakeTimeout
	if handshakeTimeout <= 0 {
		handshakeTimeout = 20 * time.Second
	}
	idleTimeout := opts.IdleTimeout
	if idleTimeout <= 0 {
		idleTimeout = 500 * time.Millisecond
	}
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}

	sessionKey, err := NewSessionKey()
	if err != nil {
		return nil, err
	}
	bodyIV, err := NewIV()
	if err != nil {
		return nil, err
	}
	headerIV, err := NewIV()
	if err != nil {
		return nil, err
	}
	bodyPlain := BuildAuthBody(cfg, now().Unix())
	bodyCT, err := AESCTR(sessionKey, bodyIV, bodyPlain)
	if err != nil {
		return nil, err
	}
	block, err := OAEPBlock(pub, oaepHash, sessionKey)
	if err != nil {
		return nil, err
	}
	pad, err := RandomPad()
	if err != nil {
		return nil, err
	}
	text := BuildAuthText(bodyIV, block, len(bodyCT), pad)
	frame, err := EncodeFrame(headerIV, text)
	if err != nil {
		return nil, err
	}

	conn, err := t.Dial(ctx, addr)
	if err != nil {
		return nil, err
	}
	if err := conn.SetDeadline(time.Now().Add(handshakeTimeout)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("fv: set handshake deadline: %w", err)
	}
	if err := writeAll(conn, frame, bodyCT); err != nil {
		conn.Close()
		return nil, err
	}
	fields, err := readServerFields(conn, headerIV, sessionKey, bodyIV, idleTimeout)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if fields["r"] != "ok" {
		conn.Close()
		return nil, &AuthError{Fields: fields}
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		conn.Close()
		return nil, fmt.Errorf("fv: clear handshake deadline: %w", err)
	}
	return &Session{
		Conn:      conn,
		Key:       sessionKey,
		HeaderIV:  headerIV,
		BodyIV:    bodyIV,
		SID:       fields["sid"],
		Fields:    fields,
		Transport: t,
		Addr:      addr,
		OpenedAt:  now(),
	}, nil
}

// readServerFields reads a server-form response: an encrypted 4-byte header
// and text, then any body bytes that arrive before the connection goes idle.
//
// The body length is not carried on the wire in the auth response, so the body
// is delimited by an idle timeout, the same heuristic OpenAJS uses.
func readServerFields(conn net.Conn, headerIV, sessionKey, bodyIV []byte, idle time.Duration) (map[string]string, error) {
	text, err := readFrameText(conn, headerIV)
	if err != nil {
		return nil, err
	}
	fields := ParseFields(text)

	if err := conn.SetReadDeadline(time.Now().Add(idle)); err != nil {
		return nil, fmt.Errorf("fv: set body deadline: %w", err)
	}
	var body []byte
	buf := make([]byte, 4096)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			body = append(body, buf[:n]...)
		}
		if err != nil {
			if errors.Is(err, io.EOF) || isTimeout(err) {
				break
			}
			return nil, fmt.Errorf("fv: read response body: %w", err)
		}
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		return nil, fmt.Errorf("fv: clear body deadline: %w", err)
	}
	if len(body) > 0 {
		bodyPlain, err := AESCTR(sessionKey, bodyIV, body)
		if err != nil {
			return nil, err
		}
		for k, v := range ParseFields(string(bodyPlain)) {
			fields[k] = v
		}
	}
	return fields, nil
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
