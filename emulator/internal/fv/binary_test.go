package fv

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func captureBytes(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join("..", "..", "..", "research", "60-dynamic", "captures", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read capture %s: %v", path, err)
	}
	return data
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("hex: %v", err)
	}
	return b
}

// TestBinaryCarrierCaptureVectors checks the empirical Linux 4.2.3.0 wire
// bytes: the auth and data requests from fvmock-1/2 and the response/auth-map
// vectors logged by the mock.
func TestBinaryCarrierCaptureVectors(t *testing.T) {
	sessionKey := mustHex(t, "252a3a233e1cd2e781a9709e377d2cb8")

	authPacket := captureBytes(t, "fvmock-1.bin")
	key, text, body, err := decodeBinaryRequest(authPacket)
	if err != nil {
		t.Fatalf("decode auth capture: %v", err)
	}
	if got := hex.EncodeToString(key); got != "1f5914aac3e1f3590ca811d4f378aa05" {
		t.Fatalf("auth key = %s", got)
	}
	fields := ParseFields(text)
	if fields["s"] != "" || fields["n"] != "0e03c6770574fa0a95bd703b733baea7" || fields["l"] != "425" {
		t.Fatalf("auth text fields = %v", fields)
	}
	iv := mustHex(t, fields["n"])
	authBody, err := AESCTR(sessionKey, iv, body)
	if err != nil {
		t.Fatalf("decrypt auth body: %v", err)
	}
	if !strings.HasPrefix(string(authBody), "c=au\n") ||
		!strings.Contains(string(authBody), "u=test\n") ||
		!strings.Contains(string(authBody), "p=test\n") ||
		!strings.Contains(string(authBody), "v=4.2.3.0\n") {
		t.Fatalf("auth body = %q", authBody)
	}

	respText, err := decodeBinaryResponse(
		mustHex(t, "eff58ae86dbcae22e178a5e4b82bb269af55ef6b17ae44b4e064aa8e68f44619884e"),
		key,
	)
	if err != nil {
		t.Fatalf("decode auth response: %v", err)
	}
	if respText != "code=200\nr=ok\ns=test\nsid=test\n" {
		t.Fatalf("auth response text = %q", respText)
	}
	authMap, err := AESCTR(sessionKey, iv, mustHex(t, "f2f4f0940944b7fd72669b85eb0360c982012d3d8cb79806d42262088d99d48b1492e5da53c94bbba46c6e01fd0070ef5897d5e6481bfa4d7ff2d459564886f6c97b7f67869ff8f8344def657f214a6e58b1bd88e73714e79ef4ec8936de7914816d345e43d920c5a2cc60e50e3866be6012e6f8c2bf7380656215ce105714928252bf928ea2b9122bf8de97861235e09fb580f4ffb6d0401b0a4c597d08ee5fa5221119c2192cc718d744786d3dbf051c4c33f26a846dc67274d4"))
	if err != nil {
		t.Fatalf("decrypt auth map: %v", err)
	}
	mapFields := ParseFields(string(authMap))
	if mapFields["r"] != "ok" || mapFields["sid"] != "test" || mapFields["sip"] != "10.0.0.1" {
		t.Fatalf("auth map = %v", mapFields)
	}

	dataPacket := captureBytes(t, "fvmock-2.bin")
	key2, text2, body2, err := decodeBinaryRequest(dataPacket)
	if err != nil {
		t.Fatalf("decode data capture: %v", err)
	}
	if got := hex.EncodeToString(key2); got != "d258388e2d5120b6a475fc1ffc98dd81" {
		t.Fatalf("data key = %s", got)
	}
	dataFields := ParseFields(text2)
	if dataFields["s"] != "test" || dataFields["n"] != "180a5b9318eafe1814908cafb9800eae" || dataFields["k"] != "" || dataFields["l"] != "" {
		t.Fatalf("data text fields = %v", dataFields)
	}
	div := mustHex(t, dataFields["n"])
	socks, err := AESCTR(sessionKey, div, body2)
	if err != nil {
		t.Fatalf("decrypt SOCKS body: %v", err)
	}
	if got := hex.EncodeToString(socks); got != "050100030b6578616d706c652e636f6d0050" {
		t.Fatalf("SOCKS body = %s", got)
	}
	respText2, err := decodeBinaryResponse(
		mustHex(t, "0e1c189aaf59fc698d3ab4145577cfe07994fb08cfafbc1759717ab0283d2c424761"),
		key2,
	)
	if err != nil {
		t.Fatalf("decode data response: %v", err)
	}
	if respText2 != "code=200\nr=ok\ns=test\nsid=test\n" {
		t.Fatalf("data response text = %q", respText2)
	}
}

// TestBinaryCarrierHandshake exercises the protocolVersion=2 carrier end to
// end against an in-process mock: binary auth, auth map + half-close, data
// channel, SOCKS5 request, reply, and relay.
func TestBinaryCarrierHandshake(t *testing.T) {
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
		// Auth connection.
		conn, err := ln.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		key, fields, err := readCarrierText(conn)
		if err != nil {
			conn.Close()
			serverErr <- err
			return
		}
		block, err := base64.StdEncoding.DecodeString(fields["k"])
		if err != nil || len(block) < 4 || string(block[:4]) != OAEPBlockMagic {
			conn.Close()
			serverErr <- fmt.Errorf("k field = %q", fields["k"])
			return
		}
		sessionKey, err := rsa.DecryptOAEP(sha1.New(), rand.Reader, priv, block[4:], nil)
		if err != nil {
			conn.Close()
			serverErr <- fmt.Errorf("OAEP: %w", err)
			return
		}
		bodyLen, _ := strconv.Atoi(fields["l"])
		body := make([]byte, bodyLen)
		if _, err := io.ReadFull(conn, body); err != nil {
			conn.Close()
			serverErr <- err
			return
		}
		iv, _ := hex.DecodeString(fields["n"])
		authBody, err := AESCTR(sessionKey, iv, body)
		if err != nil {
			conn.Close()
			serverErr <- err
			return
		}
		if !strings.Contains(string(authBody), "u=alice\n") || !strings.Contains(string(authBody), "p=pw\n") {
			conn.Close()
			serverErr <- fmt.Errorf("auth body = %q", authBody)
			return
		}
		respText := "code=200\nr=ok\ns=test\nsid=test\n"
		respPlain := make([]byte, 4+len(respText))
		binary.BigEndian.PutUint32(respPlain[:4], uint32(len(respText)))
		copy(respPlain[4:], respText)
		resp, _ := AESCTR(key, zeroIV, respPlain)
		if _, err := conn.Write(resp); err != nil {
			conn.Close()
			serverErr <- err
			return
		}
		authMapPlain := []byte("c=aur\nr=ok\nsid=test\ns=test\nok=1\nsip=10.0.0.1\ncip=10.0.0.2\n")
		authMap, _ := AESCTR(sessionKey, iv, authMapPlain)
		if _, err := conn.Write(authMap); err != nil {
			conn.Close()
			serverErr <- err
			return
		}
		if tcp, ok := conn.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		} else {
			conn.Close()
		}

		// Data connection.
		conn, err = ln.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		defer conn.Close()
		key2, dataFields, err := readCarrierText(conn)
		if err != nil {
			serverErr <- err
			return
		}
		if dataFields["s"] != "test" || dataFields["k"] != "" || dataFields["l"] != "" {
			serverErr <- fmt.Errorf("data fields = %v", dataFields)
			return
		}
		div, _ := hex.DecodeString(dataFields["n"])
		socksBody := make([]byte, len(expectedSocks))
		if _, err := io.ReadFull(conn, socksBody); err != nil {
			serverErr <- err
			return
		}
		recv, _ := NewCTR(sessionKey, div)
		socksPlain := make([]byte, len(socksBody))
		recv.XORKeyStream(socksPlain, socksBody)
		if !bytes.Equal(socksPlain, expectedSocks) {
			serverErr <- fmt.Errorf("SOCKS = %x, want %x", socksPlain, expectedSocks)
			return
		}
		respText = "code=200\n"
		respPlain = make([]byte, 4+len(respText))
		binary.BigEndian.PutUint32(respPlain[:4], uint32(len(respText)))
		copy(respPlain[4:], respText)
		resp, _ = AESCTR(key2, zeroIV, respPlain)
		if _, err := conn.Write(resp); err != nil {
			serverErr <- err
			return
		}
		send, _ := NewCTR(sessionKey, div)
		reply := make([]byte, 10)
		send.XORKeyStream(reply, []byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		if _, err := conn.Write(reply); err != nil {
			serverErr <- err
			return
		}
		echo := make([]byte, 4)
		if _, err := io.ReadFull(conn, echo); err != nil {
			serverErr <- err
			return
		}
		recv.XORKeyStream(echo, echo)
		enc := make([]byte, len(echo))
		send.XORKeyStream(enc, echo)
		if _, err := conn.Write(enc); err != nil {
			serverErr <- err
			return
		}
		serverErr <- nil
	}()

	cfg := DefaultAuthConfig("alice", "pw", "0123456789abcdef")
	sess, err := Authenticate(context.Background(), cfg, ln.Addr().String(), &HTTPUpgrade{}, AuthOptions{
		PublicKey:   &priv.PublicKey,
		IdleTimeout: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	defer sess.Close()
	if sess.SID != "test" || sess.Field("sip") != "10.0.0.1" {
		t.Fatalf("session = sid %q sip %q", sess.SID, sess.Field("sip"))
	}
	stream, err := sess.OpenStream("example.com", 80)
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	if _, err := stream.Write([]byte("ping")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got := make([]byte, 4)
	if _, err := io.ReadFull(stream, got); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(got) != "ping" {
		t.Fatalf("echo = %q", got)
	}
	stream.Close()
	select {
	case err := <-serverErr:
		if err != nil {
			t.Fatalf("server: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("mock server did not finish")
	}
}

// readCarrierText reads [16B K][CTR(K,0)(4B len || text)] and returns the key
// and parsed text. The body is left unread for the caller.
func readCarrierText(conn net.Conn) ([]byte, map[string]string, error) {
	key := make([]byte, IVSize)
	if _, err := io.ReadFull(conn, key); err != nil {
		return nil, nil, err
	}
	head := make([]byte, HeaderSize)
	if _, err := io.ReadFull(conn, head); err != nil {
		return nil, nil, err
	}
	plainHead, err := AESCTR(key, zeroIV, head)
	if err != nil {
		return nil, nil, err
	}
	if plainHead[0] != 0 || plainHead[1] != 0 {
		return nil, nil, fmt.Errorf("length prefix %x", plainHead)
	}
	textLen := int(binary.BigEndian.Uint16(plainHead[2:4]))
	textCT := make([]byte, textLen)
	if _, err := io.ReadFull(conn, textCT); err != nil {
		return nil, nil, err
	}
	plain, err := AESCTR(key, zeroIV, append(head, textCT...))
	if err != nil {
		return nil, nil, err
	}
	return key, ParseFields(string(plain[4:])), nil
}
