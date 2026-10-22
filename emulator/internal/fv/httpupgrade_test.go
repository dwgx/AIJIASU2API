package fv

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"fmt"
	"io"
	"maps"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestOPV2Frame(t *testing.T) {
	keyA, _ := hex.DecodeString("000102030405060708090a0b0c0d0e0f")
	keyB, _ := hex.DecodeString("f0e1d2c3b4a5968778695a4b3c2d1e0f")
	sessionKey, _ := hex.DecodeString("00112233445566778899aabbccddeeff")
	payload, _ := hex.DecodeString("050100030b6578616d706c652e636f6d0050")
	fields := map[string]string{
		"s": "sid123",
		"n": "00112233445566778899aabbccddeeff",
		"k": "ws-key",
	}

	// Independently generated vector: the documented OPV2 layout, assembled by
	// a separate Python implementation from the AJS-06 report.
	want := "f0e0d0c0b0a090807060504030201000" +
		"f0e1d2c3b4a5968778695a4b3c2d1e0f" +
		"f27a5409" +
		"89f16d05868f49876e4f8269c4b0b914032a76bbf6afd91e1979bdd7f32da1d72eb4020a5002b92a4c3bc871d6664a6906d7563063be5cc6ab3fcbe6e02cb90216146d6ef009d641c91d703e78b7ac7b9b"

	packet, err := BuildOPV2(keyA, keyB, sessionKey, payload, fields)
	if err != nil {
		t.Fatalf("BuildOPV2: %v", err)
	}
	if got := hex.EncodeToString(packet); got != want {
		t.Fatalf("OPV2 packet:\n got %s\nwant %s", got, want)
	}

	pkt, err := ParseOPV2(packet)
	if err != nil {
		t.Fatalf("ParseOPV2: %v", err)
	}
	if !bytes.Equal(pkt.KeyA, keyA) || !bytes.Equal(pkt.KeyB, keyB) {
		t.Fatalf("recovered keys do not match")
	}
	if !bytes.Equal(pkt.Payload, payload) {
		t.Fatalf("payload = %x, want %x", pkt.Payload, payload)
	}
	gotFields, err := pkt.Fields(sessionKey)
	if err != nil {
		t.Fatalf("Fields: %v", err)
	}
	if !maps.Equal(gotFields, fields) {
		t.Fatalf("fields = %v, want %v", gotFields, fields)
	}

	// The stream reader must accept the same bytes.
	streamPkt, err := ReadOPV2(bytes.NewReader(packet))
	if err != nil {
		t.Fatalf("ReadOPV2: %v", err)
	}
	if !bytes.Equal(streamPkt.Payload, payload) {
		t.Fatalf("stream payload = %x, want %x", streamPkt.Payload, payload)
	}

	// A corrupted prefix must fail the crc32 check.
	bad := append([]byte(nil), packet...)
	bad[32] ^= 0xff
	if _, err := ParseOPV2(bad); err == nil {
		t.Fatal("ParseOPV2 accepted a corrupted crc32")
	}
}

// TestHTTPUpgradeHandshake covers the legacy text/OPV2 carrier (ProtocolVersion
// 1); the primary binary carrier is covered by TestBinaryCarrierHandshake.
func TestHTTPUpgradeHandshake(t *testing.T) {
	sessionKey := bytes.Repeat([]byte{0x11}, SessionKeySize)
	expectedSocks, err := Socks5ConnectRequest("example.com", 80)
	if err != nil {
		t.Fatalf("Socks5ConnectRequest: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	serverDone := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		defer conn.Close()
		br := bufio.NewReader(conn)

		line, err := br.ReadString('\n')
		if err != nil {
			serverDone <- err
			return
		}
		if !strings.HasPrefix(line, "POST /s?s=sess-1&n=") || !strings.HasSuffix(line, " HTTP/1.1\r\n") {
			serverDone <- fmt.Errorf("request line = %q", line)
			return
		}

		contentLength := -1
		sawKey, sawHost := false, false
		for {
			h, err := br.ReadString('\n')
			if err != nil {
				serverDone <- err
				return
			}
			if h == "\r\n" {
				break
			}
			name, value, ok := strings.Cut(h, ":")
			if !ok {
				continue
			}
			value = strings.TrimSpace(value)
			switch name {
			case "Upgrade":
				if value != "websocket" {
					serverDone <- fmt.Errorf("Upgrade = %q", value)
					return
				}
			case "Connection":
				if value != "Upgrade" {
					serverDone <- fmt.Errorf("Connection = %q", value)
					return
				}
			case "Content-Length":
				contentLength, _ = strconv.Atoi(value)
			case "Sec-WebSocket-Key":
				sawKey = true
			case "Host":
				sawHost = true
			}
		}
		if !sawKey {
			serverDone <- fmt.Errorf("missing Sec-WebSocket-Key")
			return
		}
		if sawHost {
			serverDone <- fmt.Errorf("unexpected Host header")
			return
		}
		if contentLength < 0 {
			serverDone <- fmt.Errorf("missing Content-Length")
			return
		}

		body := make([]byte, contentLength)
		if _, err := io.ReadFull(br, body); err != nil {
			serverDone <- err
			return
		}
		pkt, err := ParseOPV2(body)
		if err != nil {
			serverDone <- fmt.Errorf("ParseOPV2: %w", err)
			return
		}
		if !bytes.Equal(pkt.Payload, expectedSocks) {
			serverDone <- fmt.Errorf("SOCKS5 request = %x, want %x", pkt.Payload, expectedSocks)
			return
		}
		fields, err := pkt.Fields(sessionKey)
		if err != nil {
			serverDone <- err
			return
		}
		if fields["s"] != "sess-1" || fields["n"] == "" || fields["k"] == "" {
			serverDone <- fmt.Errorf("OPV2 fields = %v", fields)
			return
		}

		if _, err := io.WriteString(conn, "HTTP/1.1 200 OK\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n"); err != nil {
			serverDone <- err
			return
		}
		reply, err := BuildOPV2(
			bytes.Repeat([]byte{0x22}, IVSize),
			bytes.Repeat([]byte{0x33}, IVSize),
			sessionKey,
			[]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0},
			map[string]string{"code": "200"},
		)
		if err != nil {
			serverDone <- err
			return
		}
		if _, err := conn.Write(reply); err != nil {
			serverDone <- err
			return
		}

		for {
			pkt, err := ReadOPV2(br)
			if err != nil {
				serverDone <- nil
				return
			}
			if len(pkt.Payload) == 0 {
				continue
			}
			keyA, _ := NewIV()
			keyB, _ := NewIV()
			out, err := BuildOPV2(keyA, keyB, sessionKey, pkt.Payload, nil)
			if err != nil {
				serverDone <- err
				return
			}
			if _, err := conn.Write(out); err != nil {
				serverDone <- err
				return
			}
		}
	}()

	sess := &Session{
		Key:       sessionKey,
		SID:       "sess-1",
		Addr:      ln.Addr().String(),
		Transport: &HTTPUpgrade{ProtocolVersion: 1},
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
		t.Fatalf("echo = %q, want %q", got, "ping")
	}
	stream.Close()

	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("server: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("mock server did not finish")
	}
}
