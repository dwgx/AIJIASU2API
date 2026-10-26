package fv

import (
	"bufio"
	"context"
	"crypto/cipher"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
)

// Transport opens the TCP connection to an FV endpoint and the per-flow data
// channel on top of it.
//
// Dial is used for the authentication connection. OpenData opens a fresh
// connection per proxied flow and returns a net.Conn carrying the relayed
// bytes; the transport owns all framing and encryption of that connection.
type Transport interface {
	// Name is the registry key, e.g. "rawtcp".
	Name() string
	// Experimental reports that the wire format is not confirmed against a
	// live service.
	Experimental() bool
	// Dial opens a TCP connection to addr.
	Dial(ctx context.Context, addr string) (net.Conn, error)
	// OpenData opens one proxied flow to host:port on session s.
	OpenData(ctx context.Context, s *Session, host string, port int) (net.Conn, error)
}

// ErrUnknownTransport is returned by Registry.Get for an unregistered name.
var ErrUnknownTransport = errors.New("fv: unknown transport")

// Registry maps transport names to implementations.
type Registry struct {
	mu     sync.RWMutex
	byName map[string]Transport
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{byName: make(map[string]Transport)}
}

// Register adds a transport under its Name, case-insensitively.
func (r *Registry) Register(t Transport) error {
	if t == nil {
		return errors.New("fv: nil transport")
	}
	name := strings.ToLower(strings.TrimSpace(t.Name()))
	if name == "" {
		return errors.New("fv: transport has an empty name")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.byName[name]; exists {
		return fmt.Errorf("fv: transport %q already registered", name)
	}
	r.byName[name] = t
	return nil
}

// Get returns the transport registered under name.
func (r *Registry) Get(name string) (Transport, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.byName[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownTransport, name)
	}
	return t, nil
}

// Names returns the registered names, sorted.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.byName))
	for name := range r.byName {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// DefaultRegistry registers the implemented transports: rawtcp (variant A,
// fully implemented) and httpupgrade (variant B, experimental).
func DefaultRegistry() *Registry {
	r := NewRegistry()
	_ = r.Register(RawTCP{})
	_ = r.Register(&HTTPUpgrade{})
	return r
}

// dialTCP opens a TCP connection with the same timeouts the clients use
// (15 s connect, 20 s handshake read).
func dialTCP(ctx context.Context, addr string) (net.Conn, error) {
	d := net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("fv: dial %s: %w", addr, err)
	}
	return conn, nil
}

// streamConn is a net.Conn whose payload is AES-CTR transformed in both
// directions. The CTR positions continue from the SOCKS5 request and reply
// bytes already exchanged by the transport opener.
type streamConn struct {
	conn net.Conn
	r    *bufio.Reader
	send cipher.Stream
	recv cipher.Stream

	wmu     sync.Mutex
	wbuf    []byte
	wclosed bool
}

func (c *streamConn) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 {
		c.recv.XORKeyStream(p[:n], p[:n])
	}
	return n, err
}

func (c *streamConn) Write(p []byte) (int, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.wclosed {
		return 0, io.ErrClosedPipe
	}
	if cap(c.wbuf) < len(p) {
		c.wbuf = make([]byte, len(p))
	}
	buf := c.wbuf[:len(p)]
	c.send.XORKeyStream(buf, p)
	return c.conn.Write(buf)
}

// CloseWrite sends a FIN on the vendor connection while keeping the read
// direction open, so the response to an already-written request still arrives.
// A connection without CloseWrite is closed instead, which is what the gate
// did before this method existed. CloseWrite is idempotent.
func (c *streamConn) CloseWrite() error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.wclosed {
		return nil
	}
	c.wclosed = true
	if cw, ok := c.conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return c.conn.Close()
}

func (c *streamConn) Close() error                       { return c.conn.Close() }
func (c *streamConn) LocalAddr() net.Addr                { return c.conn.LocalAddr() }
func (c *streamConn) RemoteAddr() net.Addr               { return c.conn.RemoteAddr() }
func (c *streamConn) SetDeadline(t time.Time) error      { return c.conn.SetDeadline(t) }
func (c *streamConn) SetReadDeadline(t time.Time) error  { return c.conn.SetReadDeadline(t) }
func (c *streamConn) SetWriteDeadline(t time.Time) error { return c.conn.SetWriteDeadline(t) }

// readFrameText reads one server-form frame header and text (no IV prefix),
// decrypting both with headerIV at CTR position 0.
func readFrameText(r io.Reader, headerIV []byte) (string, error) {
	stream, err := NewCTR(headerIV, zeroIV)
	if err != nil {
		return "", err
	}
	header := make([]byte, HeaderSize)
	if _, err := io.ReadFull(r, header); err != nil {
		return "", fmt.Errorf("fv: read frame header: %w", err)
	}
	stream.XORKeyStream(header, header)
	if header[0] != 0 || header[1] != 0 {
		return "", fmt.Errorf("fv: frame reserved bytes are %#x %#x, want 0 0", header[0], header[1])
	}
	textLen := int(binary.BigEndian.Uint16(header[2:4]))
	text := make([]byte, textLen)
	if _, err := io.ReadFull(r, text); err != nil {
		return "", fmt.Errorf("fv: read frame text: %w", err)
	}
	stream.XORKeyStream(text, text)
	return string(text), nil
}

// writeAll writes the concatenation of parts to conn in one call.
func writeAll(conn net.Conn, parts ...[]byte) error {
	total := 0
	for _, p := range parts {
		total += len(p)
	}
	buf := make([]byte, 0, total)
	for _, p := range parts {
		buf = append(buf, p...)
	}
	if _, err := conn.Write(buf); err != nil {
		return fmt.Errorf("fv: write: %w", err)
	}
	return nil
}
