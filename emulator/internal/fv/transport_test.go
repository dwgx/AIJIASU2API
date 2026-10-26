package fv

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

// TestStreamConnCloseWrite checks the half-close contract the gate relies on:
// FIN reaches the vendor, the response direction stays readable, writes after
// CloseWrite fail with io.ErrClosedPipe, and a second CloseWrite is a no-op.
func TestStreamConnCloseWrite(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	peerCh := make(chan net.Conn, 1)
	acceptErr := make(chan error, 1)
	go func() {
		peer, err := ln.Accept()
		if err != nil {
			acceptErr <- err
			return
		}
		peerCh <- peer
	}()

	raw, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	var peer net.Conn
	select {
	case peer = <-peerCh:
	case err := <-acceptErr:
		t.Fatalf("Accept: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("Accept timed out")
	}
	defer peer.Close()

	key := bytes.Repeat([]byte{0x5a}, SessionKeySize)
	iv := bytes.Repeat([]byte{0x3c}, IVSize)
	send, err := NewCTR(key, iv)
	if err != nil {
		t.Fatalf("NewCTR send: %v", err)
	}
	recv, err := NewCTR(key, iv)
	if err != nil {
		t.Fatalf("NewCTR recv: %v", err)
	}
	conn := &streamConn{conn: raw, r: bufio.NewReader(raw), send: send, recv: recv}
	defer conn.Close()

	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	peerRecv, err := NewCTR(key, iv)
	if err != nil {
		t.Fatalf("NewCTR peer recv: %v", err)
	}
	request := make([]byte, 5)
	if _, err := io.ReadFull(peer, request); err != nil {
		t.Fatalf("peer read request: %v", err)
	}
	peerRecv.XORKeyStream(request, request)
	if string(request) != "hello" {
		t.Fatalf("peer request = %q, want hello", request)
	}

	if err := conn.CloseWrite(); err != nil {
		t.Fatalf("CloseWrite: %v", err)
	}
	_ = peer.SetReadDeadline(time.Now().Add(2 * time.Second))
	if n, err := peer.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("peer read after CloseWrite = %d, %v; want EOF", n, err)
	}

	// The vendor can still answer on the same connection.
	peerSend, err := NewCTR(key, iv)
	if err != nil {
		t.Fatalf("NewCTR peer send: %v", err)
	}
	response := make([]byte, 5)
	peerSend.XORKeyStream(response, []byte("world"))
	if _, err := peer.Write(response); err != nil {
		t.Fatalf("peer write response: %v", err)
	}
	got := make([]byte, 5)
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read response after CloseWrite: %v", err)
	}
	if string(got) != "world" {
		t.Fatalf("response = %q, want world", got)
	}

	if n, err := conn.Write([]byte("x")); n != 0 || !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("Write after CloseWrite = %d, %v; want 0, io.ErrClosedPipe", n, err)
	}
	if err := conn.CloseWrite(); err != nil {
		t.Fatalf("second CloseWrite: %v", err)
	}
}

func TestTransportRegistry(t *testing.T) {
	reg := DefaultRegistry()

	raw, err := reg.Get("rawtcp")
	if err != nil {
		t.Fatalf("Get(rawtcp): %v", err)
	}
	if raw.Name() != "rawtcp" {
		t.Fatalf("rawtcp name = %q", raw.Name())
	}
	if raw.Experimental() {
		t.Fatal("rawtcp must not be marked experimental")
	}

	up, err := reg.Get("HTTPUpgrade")
	if err != nil {
		t.Fatalf("Get(HTTPUpgrade): %v", err)
	}
	if up.Name() != "httpupgrade" {
		t.Fatalf("httpupgrade name = %q", up.Name())
	}
	if !up.Experimental() {
		t.Fatal("httpupgrade must be marked experimental")
	}

	if _, err := reg.Get("nope"); !errors.Is(err, ErrUnknownTransport) {
		t.Fatalf("Get(nope) error = %v, want ErrUnknownTransport", err)
	}

	names := reg.Names()
	if len(names) != 2 || names[0] != "httpupgrade" || names[1] != "rawtcp" {
		t.Fatalf("Names() = %v, want [httpupgrade rawtcp]", names)
	}

	if err := reg.Register(RawTCP{}); err == nil {
		t.Fatal("duplicate registration was accepted")
	}
	if err := reg.Register(nil); err == nil {
		t.Fatal("nil transport was accepted")
	}
}
