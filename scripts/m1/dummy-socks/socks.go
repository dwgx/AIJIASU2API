// Package main implements a minimal SOCKS5 server used by the M1 spike.
//
// Two egress modes are supported:
//
//	direct   -- dial the requested target from this process
//	upstream -- relay through another SOCKS5 server (no-auth CONNECT)
//
// Only CONNECT is implemented. The harness is loopback-only by design and
// carries no credentials; it exists to give Resin two distinguishable nodes.
package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"sync"
	"time"
)

const (
	socksVersion = 0x05

	methodNoAuth = 0x00
	methodNone   = 0xFF

	cmdConnect = 0x01

	atypIPv4   = 0x01
	atypDomain = 0x03
	atypIPv6   = 0x04

	repSucceeded           = 0x00
	repGeneralFailure      = 0x01
	repCommandNotSupported = 0x07
	repAddrNotSupported    = 0x08

	handshakeTimeout = 10 * time.Second
	dialTimeout      = 15 * time.Second
)

// target is the destination requested in a SOCKS5 CONNECT.
type target struct {
	host string
	port uint16
}

func (t target) String() string {
	return net.JoinHostPort(t.host, strconv.Itoa(int(t.port)))
}

// dialFunc opens a connection to the requested target.
type dialFunc func(t target) (net.Conn, error)

// directDialer connects to the target without an intermediary.
func directDialer(t target) (net.Conn, error) {
	return net.DialTimeout("tcp", t.addr(), dialTimeout)
}

func (t target) addr() string {
	return net.JoinHostPort(t.host, strconv.Itoa(int(t.port)))
}

// upstreamDialer connects to the target through another SOCKS5 server.
func upstreamDialer(upstream string) dialFunc {
	return func(t target) (net.Conn, error) {
		return socks5Connect(upstream, t)
	}
}

// requestError carries the SOCKS5 reply code for a malformed request.
type requestError struct {
	rep byte
	err error
}

func (e *requestError) Error() string { return e.err.Error() }
func (e *requestError) Unwrap() error { return e.err }

// server is a no-auth SOCKS5 CONNECT relay.
type server struct {
	name string
	dial dialFunc
	log  *log.Logger
}

func newServer(name string, dial dialFunc, out io.Writer) *server {
	return &server{
		name: name,
		dial: dial,
		log:  log.New(out, "", log.LstdFlags|log.Lmicroseconds),
	}
}

// serve accepts connections until the listener fails.
func (s *server) serve(ln net.Listener) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		go s.handle(conn)
	}
}

func (s *server) handle(conn net.Conn) {
	defer conn.Close()

	peer := conn.RemoteAddr().String()
	if err := conn.SetDeadline(time.Now().Add(handshakeTimeout)); err != nil {
		s.log.Printf("event=handshake_fail node=%s peer=%s err=%q", s.name, peer, err)
		return
	}

	br := bufio.NewReader(conn)
	t, err := negotiate(br, conn)
	if err != nil {
		s.log.Printf("event=handshake_fail node=%s peer=%s err=%q", s.name, peer, err)
		return
	}

	dialStart := time.Now()
	upstream, err := s.dial(t)
	dialMs := time.Since(dialStart).Milliseconds()
	if err != nil {
		_, _ = conn.Write(reply(repGeneralFailure))
		s.log.Printf("event=connect node=%s target=%s ok=false dial_ms=%d err=%q",
			s.name, t, dialMs, err)
		return
	}
	defer upstream.Close()

	if _, err := conn.Write(reply(repSucceeded)); err != nil {
		s.log.Printf("event=connect node=%s target=%s ok=false dial_ms=%d err=%q",
			s.name, t, dialMs, err)
		return
	}
	s.log.Printf("event=connect node=%s target=%s ok=true dial_ms=%d", s.name, t, dialMs)

	// Handshake deadlines must not cut a long-lived relay.
	_ = conn.SetDeadline(time.Time{})
	_ = upstream.SetDeadline(time.Time{})

	start := time.Now()
	upBytes, downBytes := relay(conn, upstream)
	s.log.Printf("event=relay_done node=%s target=%s up_bytes=%d down_bytes=%d dur_ms=%d",
		s.name, t, upBytes, downBytes, time.Since(start).Milliseconds())
}

// negotiate performs the no-auth greeting and reads the CONNECT request.
func negotiate(r *bufio.Reader, w io.Writer) (target, error) {
	var head [2]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return target{}, fmt.Errorf("read greeting: %w", err)
	}
	if head[0] != socksVersion {
		return target{}, fmt.Errorf("greeting version %d", head[0])
	}
	methods := make([]byte, int(head[1]))
	if _, err := io.ReadFull(r, methods); err != nil {
		return target{}, fmt.Errorf("read methods: %w", err)
	}
	if !bytes.Contains(methods, []byte{methodNoAuth}) {
		_, _ = w.Write([]byte{socksVersion, methodNone})
		return target{}, errors.New("client did not offer no-auth")
	}
	if _, err := w.Write([]byte{socksVersion, methodNoAuth}); err != nil {
		return target{}, fmt.Errorf("write method reply: %w", err)
	}

	t, err := readRequest(r)
	if err != nil {
		var reqErr *requestError
		if errors.As(err, &reqErr) {
			_, _ = w.Write(reply(reqErr.rep))
		}
		return target{}, err
	}
	return t, nil
}

// readRequest parses a SOCKS5 CONNECT request.
func readRequest(r io.Reader) (target, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return target{}, fmt.Errorf("read request header: %w", err)
	}
	if hdr[0] != socksVersion {
		return target{}, &requestError{repGeneralFailure, fmt.Errorf("request version %d", hdr[0])}
	}
	if hdr[1] != cmdConnect {
		return target{}, &requestError{repCommandNotSupported, fmt.Errorf("command %d not supported", hdr[1])}
	}

	var host string
	switch hdr[3] {
	case atypIPv4:
		var b [4]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return target{}, fmt.Errorf("read ipv4 address: %w", err)
		}
		host = net.IP(b[:]).String()
	case atypIPv6:
		var b [16]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return target{}, fmt.Errorf("read ipv6 address: %w", err)
		}
		host = net.IP(b[:]).String()
	case atypDomain:
		var l [1]byte
		if _, err := io.ReadFull(r, l[:]); err != nil {
			return target{}, fmt.Errorf("read domain length: %w", err)
		}
		if l[0] == 0 {
			return target{}, &requestError{repGeneralFailure, errors.New("empty domain")}
		}
		b := make([]byte, int(l[0]))
		if _, err := io.ReadFull(r, b); err != nil {
			return target{}, fmt.Errorf("read domain: %w", err)
		}
		host = string(b)
	default:
		return target{}, &requestError{repAddrNotSupported, fmt.Errorf("address type %d not supported", hdr[3])}
	}

	var p [2]byte
	if _, err := io.ReadFull(r, p[:]); err != nil {
		return target{}, fmt.Errorf("read port: %w", err)
	}
	return target{host: host, port: binary.BigEndian.Uint16(p[:])}, nil
}

// reply encodes a SOCKS5 reply with an all-zero IPv4 bound address.
func reply(rep byte) []byte {
	return []byte{socksVersion, rep, 0x00, atypIPv4, 0, 0, 0, 0, 0, 0}
}

// encodeRequest builds a no-auth CONNECT request for t.
func encodeRequest(t target) ([]byte, error) {
	req := []byte{socksVersion, cmdConnect, 0x00}
	if ip := net.ParseIP(t.host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			req = append(req, atypIPv4)
			req = append(req, v4...)
		} else {
			req = append(req, atypIPv6)
			req = append(req, ip.To16()...)
		}
	} else {
		if len(t.host) == 0 || len(t.host) > 255 {
			return nil, fmt.Errorf("invalid domain %q", t.host)
		}
		req = append(req, atypDomain, byte(len(t.host)))
		req = append(req, t.host...)
	}
	return binary.BigEndian.AppendUint16(req, t.port), nil
}

// socks5Connect performs a minimal no-auth SOCKS5 CONNECT handshake and
// returns a connection ready for payload bytes.
func socks5Connect(upstream string, t target) (net.Conn, error) {
	conn, err := net.DialTimeout("tcp", upstream, dialTimeout)
	if err != nil {
		return nil, fmt.Errorf("dial upstream %s: %w", upstream, err)
	}
	ok := false
	defer func() {
		if !ok {
			_ = conn.Close()
		}
	}()

	if err := conn.SetDeadline(time.Now().Add(handshakeTimeout)); err != nil {
		return nil, err
	}
	if _, err := conn.Write([]byte{socksVersion, 0x01, methodNoAuth}); err != nil {
		return nil, fmt.Errorf("write greeting: %w", err)
	}
	var sel [2]byte
	if _, err := io.ReadFull(conn, sel[:]); err != nil {
		return nil, fmt.Errorf("read method selection: %w", err)
	}
	if sel[0] != socksVersion || sel[1] != methodNoAuth {
		return nil, fmt.Errorf("upstream selected version=%d method=%d, want 5/0", sel[0], sel[1])
	}

	req, err := encodeRequest(t)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Write(req); err != nil {
		return nil, fmt.Errorf("write connect request: %w", err)
	}

	var hdr [4]byte
	if _, err := io.ReadFull(conn, hdr[:]); err != nil {
		return nil, fmt.Errorf("read connect reply: %w", err)
	}
	if hdr[0] != socksVersion {
		return nil, fmt.Errorf("connect reply version %d", hdr[0])
	}
	if hdr[1] != repSucceeded {
		return nil, fmt.Errorf("upstream CONNECT %s failed: rep=%d", t, hdr[1])
	}
	if err := discardAddr(conn, hdr[3]); err != nil {
		return nil, err
	}
	var p [2]byte
	if _, err := io.ReadFull(conn, p[:]); err != nil {
		return nil, fmt.Errorf("read bound port: %w", err)
	}

	if err := conn.SetDeadline(time.Time{}); err != nil {
		return nil, err
	}
	ok = true
	return conn, nil
}

// discardAddr skips the bound address in a SOCKS5 reply.
func discardAddr(r io.Reader, atyp byte) error {
	switch atyp {
	case atypIPv4:
		var b [4]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return fmt.Errorf("read bound ipv4: %w", err)
		}
	case atypIPv6:
		var b [16]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return fmt.Errorf("read bound ipv6: %w", err)
		}
	case atypDomain:
		var l [1]byte
		if _, err := io.ReadFull(r, l[:]); err != nil {
			return fmt.Errorf("read bound domain length: %w", err)
		}
		b := make([]byte, int(l[0]))
		if _, err := io.ReadFull(r, b); err != nil {
			return fmt.Errorf("read bound domain: %w", err)
		}
	default:
		return fmt.Errorf("unsupported bound address type %d", atyp)
	}
	return nil
}

// relay copies bytes in both directions and half-closes each side when done.
func relay(client, upstream net.Conn) (clientToUp, upToClient int64) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		clientToUp, _ = io.Copy(upstream, client)
		closeWrite(upstream)
	}()
	go func() {
		defer wg.Done()
		upToClient, _ = io.Copy(client, upstream)
		closeWrite(client)
	}()
	wg.Wait()
	return clientToUp, upToClient
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
}
