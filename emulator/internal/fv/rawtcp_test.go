package fv

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"strconv"
	"testing"
	"time"
)

// TestRawTCPOpenStream exercises variant A end to end against an in-process FV
// server: the auth exchange (OAEP under a generated 1024-bit key), the data
// channel frame, the encrypted SOCKS5 CONNECT, and the relayed echo.
func TestRawTCPOpenStream(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	expectedSocks, err := Socks5ConnectRequest("example.com", 80)
	if err != nil {
		t.Fatalf("Socks5ConnectRequest: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	serverErr := make(chan error, 1)
	go func() {
		// --- auth connection ---
		conn, err := ln.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		headerIV := make([]byte, IVSize)
		if _, err := io.ReadFull(conn, headerIV); err != nil {
			serverErr <- err
			conn.Close()
			return
		}
		text, err := readFrameText(conn, headerIV)
		if err != nil {
			serverErr <- err
			conn.Close()
			return
		}
		fields := ParseFields(text)
		bodyIV, err := hex.DecodeString(fields["n"])
		if err != nil {
			serverErr <- err
			conn.Close()
			return
		}
		bodyLen, err := strconv.Atoi(fields["l"])
		if err != nil {
			serverErr <- err
			conn.Close()
			return
		}
		block, err := base64.StdEncoding.DecodeString(fields["k"])
		if err != nil {
			serverErr <- fmt.Errorf("base64 k: %w", err)
			conn.Close()
			return
		}
		if len(block) < 4 || string(block[:4]) != OAEPBlockMagic {
			serverErr <- fmt.Errorf("OAEP block prefix %q", block)
			conn.Close()
			return
		}
		sessionKey, err := rsa.DecryptOAEP(sha1.New(), rand.Reader, priv, block[4:], nil)
		if err != nil {
			serverErr <- err
			conn.Close()
			return
		}
		bodyCT := make([]byte, bodyLen)
		if _, err := io.ReadFull(conn, bodyCT); err != nil {
			serverErr <- err
			conn.Close()
			return
		}
		bodyPlain, err := AESCTR(sessionKey, bodyIV, bodyCT)
		if err != nil {
			serverErr <- err
			conn.Close()
			return
		}
		authFields := ParseFields(string(bodyPlain))
		if authFields["c"] != "au" || authFields["u"] != "alice" || authFields["p"] != "pw" {
			serverErr <- fmt.Errorf("auth fields = %v", authFields)
			conn.Close()
			return
		}
		headerCT, err := EncryptHeaderText(headerIV, "code=200")
		if err != nil {
			serverErr <- err
			conn.Close()
			return
		}
		respBody, err := AESCTR(sessionKey, bodyIV, []byte("r=ok\nsid=abc123\nsip=10.0.0.1\n"))
		if err != nil {
			serverErr <- err
			conn.Close()
			return
		}
		if _, err := conn.Write(append(headerCT, respBody...)); err != nil {
			serverErr <- err
			conn.Close()
			return
		}
		conn.Close()

		// --- data connection ---
		conn, err = ln.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		defer conn.Close()
		dataHeaderIV := make([]byte, IVSize)
		if _, err := io.ReadFull(conn, dataHeaderIV); err != nil {
			serverErr <- fmt.Errorf("data header iv: %w", err)
			return
		}
		dataText, err := readFrameText(conn, dataHeaderIV)
		if err != nil {
			serverErr <- err
			return
		}
		dataFields := ParseFields(dataText)
		if dataFields["s"] != "abc123" {
			serverErr <- fmt.Errorf("data fields = %v", dataFields)
			return
		}
		div, err := hex.DecodeString(dataFields["n"])
		if err != nil {
			serverErr <- err
			return
		}
		recvStream, err := NewCTR(sessionKey, div)
		if err != nil {
			serverErr <- err
			return
		}
		req := make([]byte, 5)
		if _, err := io.ReadFull(conn, req); err != nil {
			serverErr <- fmt.Errorf("socks head: %w", err)
			return
		}
		recvStream.XORKeyStream(req, req)
		rest := make([]byte, int(req[4])+2)
		if _, err := io.ReadFull(conn, rest); err != nil {
			serverErr <- fmt.Errorf("socks rest (len %d): %w", len(rest), err)
			return
		}
		recvStream.XORKeyStream(rest, rest)
		req = append(req, rest...)
		if !bytes.Equal(req, expectedSocks) {
			serverErr <- fmt.Errorf("SOCKS5 request = %x, want %x", req, expectedSocks)
			return
		}
		headerCT, err = EncryptHeaderText(dataHeaderIV, "code=200")
		if err != nil {
			serverErr <- err
			return
		}
		sendStream, err := NewCTR(sessionKey, div)
		if err != nil {
			serverErr <- err
			return
		}
		reply := make([]byte, 10)
		sendStream.XORKeyStream(reply, []byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		if _, err := conn.Write(append(headerCT, reply...)); err != nil {
			serverErr <- err
			return
		}

		// Echo one relayed payload.
		echo := make([]byte, 4)
		if _, err := io.ReadFull(conn, echo); err != nil {
			serverErr <- fmt.Errorf("echo: %w", err)
			return
		}
		recvStream.XORKeyStream(echo, echo)
		enc := make([]byte, len(echo))
		sendStream.XORKeyStream(enc, echo)
		if _, err := conn.Write(enc); err != nil {
			serverErr <- err
			return
		}
		serverErr <- nil
	}()

	cfg := DefaultAuthConfig("alice", "pw", "0123456789abcdef")
	sess, err := Authenticate(context.Background(), cfg, ln.Addr().String(), RawTCP{}, AuthOptions{
		PublicKey:   &priv.PublicKey,
		IdleTimeout: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	defer sess.Close()
	if sess.SID != "abc123" || sess.Field("sip") != "10.0.0.1" {
		t.Fatalf("session = sid %q sip %q", sess.SID, sess.Field("sip"))
	}

	stream, err := sess.OpenStream("example.com", 80)
	if err != nil {
		select {
		case serverErrValue := <-serverErr:
			t.Fatalf("OpenStream: %v (server: %v)", err, serverErrValue)
		case <-time.After(200 * time.Millisecond):
			t.Fatalf("OpenStream: %v (server still running)", err)
		}
	}
	if _, err := stream.Write([]byte("ping")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got := make([]byte, 4)
	if _, err := io.ReadFull(stream, got); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(got) != "ping" {
		t.Fatalf("echo = %q, want %q", got, "ping")
	}
	stream.Close()

	select {
	case err := <-serverErr:
		if err != nil {
			t.Fatalf("server: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("mock FV server did not finish")
	}
}
