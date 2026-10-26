package fv

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
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
	// The captured Linux identity: a 20-hex cuid and the literal cseid=linux.
	capturedIdentity := ParseFields(string(authBody))
	if got := capturedIdentity["cuid"]; got != "6ada95c900de9bbb3a82" {
		t.Fatalf("captured cuid = %q, want the 20-hex value", got)
	}
	if got := capturedIdentity["cseid"]; got != "linux" {
		t.Fatalf("captured cseid = %q, want linux", got)
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

	cfg := DefaultAuthConfig("alice", "pw", "0123456789abcdef")
	serverErr, _ := carrierMock(ln, priv, cfg, expectedSocks, []byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}, nil)

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

// independentCTR builds the AES-CTR stream with crypto/aes directly, so the
// mock server in these tests is not implemented with the code under test.
func independentCTR(key, iv []byte) (cipher.Stream, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewCTR(block, iv), nil
}

// readCarrierText reads [16B K][CTR(K,0)(4B len || text)] and returns the key
// and parsed text. The body is left unread for the caller.
func readCarrierText(conn net.Conn) ([]byte, map[string]string, error) {
	key := make([]byte, IVSize)
	if _, err := io.ReadFull(conn, key); err != nil {
		return nil, nil, err
	}
	stream, err := independentCTR(key, zeroIV)
	if err != nil {
		return nil, nil, err
	}
	head := make([]byte, HeaderSize)
	if _, err := io.ReadFull(conn, head); err != nil {
		return nil, nil, err
	}
	stream.XORKeyStream(head, head)
	if head[0] != 0 || head[1] != 0 {
		return nil, nil, fmt.Errorf("length prefix %x", head)
	}
	textLen := int(binary.BigEndian.Uint16(head[2:4]))
	text := make([]byte, textLen)
	if _, err := io.ReadFull(conn, text); err != nil {
		return nil, nil, err
	}
	stream.XORKeyStream(text, text)
	return key, ParseFields(string(text)), nil
}

// carrierTrace records the wire facts the mock observed. The goroutine writes
// them before it reports nil, so receiving from the error channel publishes
// them to the test.
type carrierTrace struct {
	authIV   []byte
	authBody []byte
	div      []byte
	request  []byte
}

// carrierMock runs the in-process FV server used by the binary-carrier tests:
// one auth connection (binary auth, auth map, half-close) and one data
// connection (SOCKS5 request check, reply, optional prefix payload, echo).
// reply and prefix are plaintext; the helper encrypts them at the current
// payload CTR position, as the vendor does. Every server-side operation uses
// crypto/aes directly, so a divergence in the client's IV choice fails here
// instead of being masked by the production helpers.
func carrierMock(ln net.Listener, priv *rsa.PrivateKey, cfg AuthConfig, expectedSocks, reply, prefix []byte) (<-chan error, *carrierTrace) {
	trace := &carrierTrace{}
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
		bodyStream, err := independentCTR(sessionKey, iv)
		if err != nil {
			conn.Close()
			serverErr <- err
			return
		}
		authBody := make([]byte, len(body))
		bodyStream.XORKeyStream(authBody, body)
		bodyFields := ParseFields(string(authBody))
		if bodyFields["u"] != cfg.User || bodyFields["p"] != cfg.Password || bodyFields["c"] != "au" {
			conn.Close()
			serverErr <- fmt.Errorf("auth body = %q", authBody)
			return
		}
		trace.authIV = append([]byte(nil), iv...)
		trace.authBody = append([]byte(nil), authBody...)

		respText := "code=200\nr=ok\ns=test\nsid=test\n"
		respPlain := make([]byte, 4+len(respText))
		binary.BigEndian.PutUint32(respPlain[:4], uint32(len(respText)))
		copy(respPlain[4:], respText)
		respStream, err := independentCTR(key, zeroIV)
		if err != nil {
			conn.Close()
			serverErr <- err
			return
		}
		resp := make([]byte, len(respPlain))
		respStream.XORKeyStream(resp, respPlain)
		if _, err := conn.Write(resp); err != nil {
			conn.Close()
			serverErr <- err
			return
		}
		authMapPlain := []byte("c=aur\nr=ok\nsid=test\ns=test\nok=1\nsip=10.0.0.1\ncip=10.0.0.2\n")
		mapStream, err := independentCTR(sessionKey, iv)
		if err != nil {
			conn.Close()
			serverErr <- err
			return
		}
		authMap := make([]byte, len(authMapPlain))
		mapStream.XORKeyStream(authMap, authMapPlain)
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
		recv, err := independentCTR(sessionKey, div)
		if err != nil {
			serverErr <- err
			return
		}
		socksPlain := make([]byte, len(socksBody))
		recv.XORKeyStream(socksPlain, socksBody)
		if !bytes.Equal(socksPlain, expectedSocks) {
			serverErr <- fmt.Errorf("SOCKS = %x, want %x", socksPlain, expectedSocks)
			return
		}
		trace.div = append([]byte(nil), div...)
		trace.request = append([]byte(nil), socksPlain...)

		respText = "code=200\n"
		respPlain = make([]byte, 4+len(respText))
		binary.BigEndian.PutUint32(respPlain[:4], uint32(len(respText)))
		copy(respPlain[4:], respText)
		respStream, err = independentCTR(key2, zeroIV)
		if err != nil {
			serverErr <- err
			return
		}
		resp = make([]byte, len(respPlain))
		respStream.XORKeyStream(resp, respPlain)
		if _, err := conn.Write(resp); err != nil {
			serverErr <- err
			return
		}
		send, err := independentCTR(sessionKey, div)
		if err != nil {
			serverErr <- err
			return
		}
		replyCT := make([]byte, len(reply))
		send.XORKeyStream(replyCT, reply)
		if _, err := conn.Write(replyCT); err != nil {
			serverErr <- err
			return
		}
		if len(prefix) > 0 {
			prefixCT := make([]byte, len(prefix))
			send.XORKeyStream(prefixCT, prefix)
			if _, err := conn.Write(prefixCT); err != nil {
				serverErr <- err
				return
			}
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
	return serverErr, trace
}

// TestBinaryCarrierReplyIPv6 exercises an ATYP 4 SOCKS5 reply end to end. The
// reply is 22 bytes, so a fixed 10-byte read would leave 12 bytes in the
// stream, desynchronise the CTR position, and corrupt the sentinel payload
// that follows.
func TestBinaryCarrierReplyIPv6(t *testing.T) {
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

	ipv6 := net.ParseIP("2001:db8::1").To16()
	if ipv6 == nil {
		t.Fatal("ParseIP returned nil")
	}
	reply := append([]byte{0x05, 0x00, 0x00, 0x04}, ipv6...)
	reply = append(reply, 0x00, 0x50)
	sentinel := []byte("SENTINEL")
	cfg := DefaultAuthConfig("alice", "pw", "0123456789abcdef")
	serverErr, _ := carrierMock(ln, priv, cfg, expectedSocks, reply, sentinel)

	sess, err := Authenticate(context.Background(), cfg, ln.Addr().String(), &HTTPUpgrade{}, AuthOptions{
		PublicKey:   &priv.PublicKey,
		IdleTimeout: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	defer sess.Close()
	stream, err := sess.OpenStream("example.com", 80)
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	defer stream.Close()

	gotSentinel := make([]byte, len(sentinel))
	if _, err := io.ReadFull(stream, gotSentinel); err != nil {
		t.Fatalf("read sentinel after ATYP 4 reply: %v", err)
	}
	if !bytes.Equal(gotSentinel, sentinel) {
		t.Fatalf("sentinel = %q, want %q (CTR position desynchronised)", gotSentinel, sentinel)
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
	select {
	case err := <-serverErr:
		if err != nil {
			t.Fatalf("server: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("mock server did not finish")
	}
}

// TestCarrierWrongCode checks that the code= field is parsed exactly: a longer
// code, a code hidden in another field, and a missing code all fail closed,
// and the raw text (including the vendor's fm= line) survives in the error for
// the daemon's session-loss classifier.
func TestCarrierWrongCode(t *testing.T) {
	const sessionLoss = "fm=disconnected by another connection"
	cases := []struct {
		name string
		text string
		code int
	}{
		{"longer code", "code=2001\nr=ok\n" + sessionLoss + "\n", 2001},
		{"code hidden in msg", "code=500\nmsg=code=200\n" + sessionLoss + "\n", 500},
		{"code absent", "r=ok\nmsg=code=200\n" + sessionLoss + "\n", -1},
		{"duplicate code", "code=403\ncode=200\n" + sessionLoss + "\n", -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkCarrierCode("data channel", tc.text)
			if err == nil {
				t.Fatal("response was accepted")
			}
			var carrierErr *CarrierError
			if !errors.As(err, &carrierErr) {
				t.Fatalf("error type = %T, want *CarrierError", err)
			}
			if carrierErr.Code != tc.code {
				t.Fatalf("code = %d, want %d", carrierErr.Code, tc.code)
			}
			if !strings.Contains(err.Error(), sessionLoss) {
				t.Fatalf("error %q lost the raw text (%q)", err, sessionLoss)
			}
		})
	}

	for _, text := range []string{"code=200\n", "code=200\r\nr=ok\n"} {
		if err := checkCarrierCode("data channel", text); err != nil {
			t.Fatalf("checkCarrierCode(%q) = %v, want nil", text, err)
		}
	}
	if code, ok := carrierCode("msg=code=200\n"); ok {
		t.Fatalf("carrierCode read code=%d from a msg line", code)
	}
	if _, ok := carrierCode("code=403\ncode=200\n"); ok {
		t.Fatal("duplicate code= lines were accepted")
	}
}

// TestCarrierIVReuseKnownAnswer pins the IV derivation the vendor accepts, on
// the captured vector and on the production paths. The captured vector shows
// the auth body and the 187-byte auth map share a keystream from offset 0
// under the same key and IV n, and that DefaultAuthConfig puts p= at offset
// 136 with an empty `_` pad (production offsets are 136+len(pad), so the mock's
// map overlaps only for a pad of 50 bytes or less). The integration half
// drives Authenticate and OpenStream against a mock built with crypto/aes: the
// auth body must decrypt at CTR offset 0 under n, the auth map must decode at
// offset 0 under n, the SOCKS5 request must decrypt at offset 0 under div, and
// the reply must start at offset 0 under div with the payload immediately after
// it. It fails if anyone changes the IV derivation (WP-6) without a new
// capture-backed vector.
func TestCarrierIVReuseKnownAnswer(t *testing.T) {
	sessionKey := mustHex(t, "252a3a233e1cd2e781a9709e377d2cb8")
	iv := mustHex(t, "0e03c6770574fa0a95bd703b733baea7")
	mapCT := mustHex(t, "f2f4f0940944b7fd72669b85eb0360c982012d3d8cb79806d42262088d99d48b1492e5da53c94bbba46c6e01fd0070ef5897d5e6481bfa4d7ff2d459564886f6c97b7f67869ff8f8344def657f214a6e58b1bd88e73714e79ef4ec8936de7914816d345e43d920c5a2cc60e50e3866be6012e6f8c2bf7380656215ce105714928252bf928ea2b9122bf8de97861235e09fb580f4ffb6d0401b0a4c597d08ee5fa5221119c2192cc718d744786d3dbf051c4c33f26a846dc67274d4")
	mapStream, err := independentCTR(sessionKey, iv)
	if err != nil {
		t.Fatalf("independentCTR map: %v", err)
	}
	mapPlain := make([]byte, len(mapCT))
	mapStream.XORKeyStream(mapPlain, mapCT)
	if len(mapPlain) != 187 {
		t.Fatalf("captured auth map = %d bytes, want 187", len(mapPlain))
	}

	cfg := DefaultAuthConfig("alice", "pw", "0123456789abcdef")
	body := BuildBinaryAuthBody(cfg, 1700000000, "")
	bodyStream, err := independentCTR(sessionKey, iv)
	if err != nil {
		t.Fatalf("independentCTR body: %v", err)
	}
	bodyCT := make([]byte, len(body))
	bodyStream.XORKeyStream(bodyCT, body)
	if len(body) < len(mapPlain) {
		t.Fatalf("auth body = %d bytes, want at least %d", len(body), len(mapPlain))
	}
	for i := range mapPlain {
		bodyKS := bodyCT[i] ^ body[i]
		mapKS := mapCT[i] ^ mapPlain[i]
		if bodyKS != mapKS {
			t.Fatalf("keystream byte %d differs: body %#x map %#x", i, bodyKS, mapKS)
		}
	}

	// The empty-pad body is the captured known answer. Production prepends
	// the `_` pad (sort.Strings puts `_` first), so the offset is
	// 136+len(pad) and the mock's 187-byte map overlaps only for pad <= 50.
	for _, pad := range []string{"", strings.Repeat("a", 50), strings.Repeat("a", 51)} {
		paddedBody := BuildBinaryAuthBody(cfg, 1700000000, pad)
		pOffset := strings.Index(string(paddedBody), "\np=") + 1
		if pOffset != 136+len(pad) {
			t.Fatalf("p= offset with pad %d = %d, want %d", len(pad), pOffset, 136+len(pad))
		}
		if inside := pOffset < len(mapPlain); inside != (len(pad) <= 50) {
			t.Fatalf("pad %d: p= inside the mock map = %v, want %v", len(pad), inside, len(pad) <= 50)
		}
		if !bytes.HasPrefix(paddedBody[pOffset:], []byte("p="+cfg.Password+"\n")) {
			t.Fatalf("p= line at offset %d = %q", pOffset, paddedBody[pOffset:])
		}
	}

	// Production path: the mock checks every offset against crypto/aes.
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

	reply := []byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}
	sentinel := []byte("SENTINEL")
	serverErr, trace := carrierMock(ln, priv, cfg, expectedSocks, reply, sentinel)

	sess, err := Authenticate(context.Background(), cfg, ln.Addr().String(), &HTTPUpgrade{}, AuthOptions{
		PublicKey:   &priv.PublicKey,
		IdleTimeout: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	defer sess.Close()
	// sip/cip come only from the auth map: they decode only if readAuthMap
	// used CTR offset 0 under the transmitted n.
	if sess.Field("sip") != "10.0.0.1" || sess.Field("cip") != "10.0.0.2" {
		t.Fatalf("auth map = sip %q cip %q; want offset-0 IV=n decryption", sess.Field("sip"), sess.Field("cip"))
	}

	stream, err := sess.OpenStream("example.com", 80)
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	defer stream.Close()
	gotSentinel := make([]byte, len(sentinel))
	if _, err := io.ReadFull(stream, gotSentinel); err != nil {
		t.Fatalf("read payload after reply: %v", err)
	}
	if !bytes.Equal(gotSentinel, sentinel) {
		t.Fatalf("payload after reply = %q, want %q (reply CTR offset desynchronised)", gotSentinel, sentinel)
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
	select {
	case err := <-serverErr:
		if err != nil {
			t.Fatalf("server: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("mock server did not finish")
	}

	// Wire facts observed by the mock, which rebuilt every ciphertext with
	// crypto/aes rather than the production helpers.
	bodyFields := ParseFields(string(trace.authBody))
	if bodyFields["u"] != cfg.User || bodyFields["p"] != cfg.Password {
		t.Fatalf("auth body did not decrypt at offset 0 under n: %q", trace.authBody)
	}
	if !bytes.Equal(trace.request, expectedSocks) {
		t.Fatalf("SOCKS request = %x, want %x at CTR offset 0", trace.request, expectedSocks)
	}
	if len(trace.authIV) != IVSize || len(trace.div) != IVSize {
		t.Fatalf("observed IVs = %d/%d bytes, want %d/%d", len(trace.authIV), len(trace.div), IVSize, IVSize)
	}
}
