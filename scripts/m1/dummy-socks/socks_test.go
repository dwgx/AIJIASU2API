package main

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"
)

// startEcho runs a TCP echo server for the duration of the test.
func startEcho(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen echo: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go echoLoop(ln)
	return ln
}

// startProxy runs a SOCKS5 server with the given dialer.
func startProxy(t *testing.T, name string, dial dialFunc) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen %s: %v", name, err)
	}
	t.Cleanup(func() { ln.Close() })
	go newServer(name, dial, io.Discard).serve(ln)
	return ln.Addr().String()
}

func echoPort(t *testing.T, ln net.Listener) uint16 {
	t.Helper()
	return uint16(ln.Addr().(*net.TCPAddr).Port)
}

func TestDirectConnectRelay(t *testing.T) {
	echo := startEcho(t)
	proxy := startProxy(t, "direct", directDialer)

	payload := bytes.Repeat([]byte("m1-direct-"), 1024)
	if err := roundTrip(proxy, target{host: "127.0.0.1", port: echoPort(t, echo)}, payload); err != nil {
		t.Fatalf("direct round trip: %v", err)
	}
}

func TestUpstreamChainRelay(t *testing.T) {
	echo := startEcho(t)
	upstream := startProxy(t, "upstream", directDialer)
	node := startProxy(t, "node", upstreamDialer(upstream))

	payload := bytes.Repeat([]byte("m1-upstream-"), 8192)
	if err := roundTrip(node, target{host: "127.0.0.1", port: echoPort(t, echo)}, payload); err != nil {
		t.Fatalf("upstream round trip: %v", err)
	}
}

func TestDomainTarget(t *testing.T) {
	echo := startEcho(t)
	upstream := startProxy(t, "upstream", directDialer)
	node := startProxy(t, "node", upstreamDialer(upstream))

	if err := roundTrip(node, target{host: "localhost", port: echoPort(t, echo)}, []byte("domain")); err != nil {
		t.Fatalf("domain round trip: %v", err)
	}
}

func TestCommandNotSupported(t *testing.T) {
	proxy := startProxy(t, "direct", directDialer)

	conn, err := net.DialTimeout("tcp", proxy, time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))

	if _, err := conn.Write([]byte{socksVersion, 1, methodNoAuth}); err != nil {
		t.Fatalf("write greeting: %v", err)
	}
	var sel [2]byte
	if _, err := io.ReadFull(conn, sel[:]); err != nil {
		t.Fatalf("read method selection: %v", err)
	}
	if sel != [2]byte{socksVersion, methodNoAuth} {
		t.Fatalf("method selection = %v", sel)
	}

	// UDP ASSOCIATE (0x03) must be refused.
	if _, err := conn.Write([]byte{socksVersion, 0x03, 0x00, atypIPv4, 127, 0, 0, 1, 0, 53}); err != nil {
		t.Fatalf("write request: %v", err)
	}
	var rep [4]byte
	if _, err := io.ReadFull(conn, rep[:]); err != nil {
		t.Fatalf("read reply: %v", err)
	}
	if rep[1] != repCommandNotSupported {
		t.Fatalf("reply code = %d, want %d", rep[1], repCommandNotSupported)
	}
}

func TestNoAuthRejected(t *testing.T) {
	proxy := startProxy(t, "direct", directDialer)

	conn, err := net.DialTimeout("tcp", proxy, time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))

	// Only username/password (0x02) offered: the server must answer 0xFF.
	if _, err := conn.Write([]byte{socksVersion, 1, 0x02}); err != nil {
		t.Fatalf("write greeting: %v", err)
	}
	var sel [2]byte
	if _, err := io.ReadFull(conn, sel[:]); err != nil {
		t.Fatalf("read method selection: %v", err)
	}
	if sel != [2]byte{socksVersion, methodNone} {
		t.Fatalf("method selection = %v, want [5 255]", sel)
	}
}
