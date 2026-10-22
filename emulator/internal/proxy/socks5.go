// Package proxy implements the local proxy front ends: a SOCKS5 no-auth
// CONNECT server and an HTTP proxy (CONNECT plus absolute-URI). Both open one
// FV data channel per client flow through a StreamOpener and relay bytes
// unchanged in both directions.
package proxy

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// StreamOpener opens one proxied TCP flow to host:port. *fv.Session satisfies
// this interface.
type StreamOpener interface {
	OpenStream(host string, port int) (net.Conn, error)
}

// SOCKS5 reply codes (RFC 1928).
const (
	SOCKS5Succeeded          = 0x00
	SOCKS5ConnectionRefused  = 0x05
	SOCKS5CommandUnsupported = 0x07
	SOCKS5AddrUnsupported    = 0x08
)

// handshakeTimeout bounds the local handshake before any relay starts.
const handshakeTimeout = 30 * time.Second

// SOCKS5 is a no-auth SOCKS5 CONNECT server.
type SOCKS5 struct {
	// Addr is the listen address, e.g. "127.0.0.1:1080".
	Addr string
	// Opener opens the remote flow. Required.
	Opener StreamOpener
	// Logf, when non-nil, receives per-connection diagnostics.
	Logf func(format string, args ...any)
}

// ListenAndServe listens on Addr and serves until the listener closes.
func (s *SOCKS5) ListenAndServe() error {
	ln, err := net.Listen("tcp", s.Addr)
	if err != nil {
		return fmt.Errorf("proxy: SOCKS5 listen %s: %w", s.Addr, err)
	}
	return s.Serve(ln)
}

// Serve accepts connections on ln until it is closed.
func (s *SOCKS5) Serve(ln net.Listener) error {
	if s.Opener == nil {
		return errors.New("proxy: SOCKS5 has no stream opener")
	}
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go s.handle(conn)
	}
}

func (s *SOCKS5) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
	}
}

func (s *SOCKS5) handle(client net.Conn) {
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(handshakeTimeout))

	host, port, err := readSOCKS5Request(client)
	if err != nil {
		s.logf("socks5: handshake: %v", err)
		return
	}
	stream, err := s.Opener.OpenStream(host, port)
	if err != nil {
		s.logf("socks5: open %s:%d: %v", host, port, err)
		_ = writeSOCKS5Reply(client, SOCKS5ConnectionRefused)
		return
	}
	defer stream.Close()
	if err := writeSOCKS5Reply(client, SOCKS5Succeeded); err != nil {
		return
	}
	_ = client.SetDeadline(time.Time{})
	relay(client, stream)
}

// readSOCKS5Request performs the greeting and the CONNECT request. It writes
// the method-selection reply itself, and writes the failure reply for
// unsupported commands or address types.
func readSOCKS5Request(conn net.Conn) (host string, port int, err error) {
	var greeting [2]byte
	if _, err := io.ReadFull(conn, greeting[:]); err != nil {
		return "", 0, fmt.Errorf("read greeting: %w", err)
	}
	if greeting[0] != 0x05 {
		return "", 0, fmt.Errorf("SOCKS version %d, want 5", greeting[0])
	}
	methods := make([]byte, int(greeting[1]))
	if _, err := io.ReadFull(conn, methods); err != nil {
		return "", 0, fmt.Errorf("read methods: %w", err)
	}
	if _, err := conn.Write([]byte{0x05, 0x00}); err != nil {
		return "", 0, fmt.Errorf("write method reply: %w", err)
	}

	var head [4]byte
	if _, err := io.ReadFull(conn, head[:]); err != nil {
		return "", 0, fmt.Errorf("read request: %w", err)
	}
	if head[0] != 0x05 {
		return "", 0, fmt.Errorf("request version %d, want 5", head[0])
	}
	if head[1] != 0x01 {
		_ = writeSOCKS5Reply(conn, SOCKS5CommandUnsupported)
		return "", 0, fmt.Errorf("command %d is not CONNECT", head[1])
	}
	switch head[3] {
	case 0x01:
		ip := make([]byte, net.IPv4len)
		if _, err := io.ReadFull(conn, ip); err != nil {
			return "", 0, fmt.Errorf("read IPv4 address: %w", err)
		}
		host = net.IP(ip).String()
	case 0x03:
		var length [1]byte
		if _, err := io.ReadFull(conn, length[:]); err != nil {
			return "", 0, fmt.Errorf("read domain length: %w", err)
		}
		name := make([]byte, int(length[0]))
		if _, err := io.ReadFull(conn, name); err != nil {
			return "", 0, fmt.Errorf("read domain: %w", err)
		}
		host = string(name)
	case 0x04:
		ip := make([]byte, net.IPv6len)
		if _, err := io.ReadFull(conn, ip); err != nil {
			return "", 0, fmt.Errorf("read IPv6 address: %w", err)
		}
		host = net.IP(ip).String()
	default:
		_ = writeSOCKS5Reply(conn, SOCKS5AddrUnsupported)
		return "", 0, fmt.Errorf("address type %d is not supported", head[3])
	}
	var portBytes [2]byte
	if _, err := io.ReadFull(conn, portBytes[:]); err != nil {
		return "", 0, fmt.Errorf("read port: %w", err)
	}
	return host, int(binary.BigEndian.Uint16(portBytes[:])), nil
}

func writeSOCKS5Reply(w io.Writer, rep byte) error {
	_, err := w.Write([]byte{0x05, rep, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
	return err
}

// relay copies bytes in both directions until both directions end, then
// closes both connections.
func relay(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		copyAndCloseWrite(a, b)
	}()
	go func() {
		defer wg.Done()
		copyAndCloseWrite(b, a)
	}()
	wg.Wait()
	a.Close()
	b.Close()
}

func copyAndCloseWrite(dst, src net.Conn) {
	_, _ = io.Copy(dst, src)
	if cw, ok := dst.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
		return
	}
	_ = dst.Close()
}
