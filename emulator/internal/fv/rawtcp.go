package fv

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"time"
)

// RawTCP is variant A: a plain TCP connection to svr:par.fv.p, then the
// OpenAJS frame (16-byte header IV, encrypted header/text) followed by the
// AES-CTR-encrypted SOCKS5 CONNECT request.
//
// This is the fully implemented baseline variant. It is the OpenAJS data
// channel, which the static analysis places closer to the VPN/legacy shape
// than to the shipping proxy mode (variant B).
type RawTCP struct{}

// Name implements Transport.
func (RawTCP) Name() string { return "rawtcp" }

// Experimental implements Transport. Variant A is not experimental: the
// builder is fully specified by the OpenAJS source.
func (RawTCP) Experimental() bool { return false }

// Dial implements Transport.
func (RawTCP) Dial(ctx context.Context, addr string) (net.Conn, error) {
	return dialTCP(ctx, addr)
}

// OpenData opens one proxied flow: a fresh TCP connection, the data-channel
// frame carrying s=<sid> and n=<payload IV>, then the encrypted SOCKS5 CONNECT
// request. The server's header must carry code=200 and its SOCKS5 reply must
// be 05 00; the reply length follows its ATYP.
func (RawTCP) OpenData(ctx context.Context, s *Session, host string, port int) (net.Conn, error) {
	if s == nil {
		return nil, errors.New("fv: nil session")
	}
	if s.SID == "" {
		return nil, errors.New("fv: session has no sid")
	}
	conn, err := dialTCP(ctx, s.Addr)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			conn.Close()
		}
	}()
	if err := conn.SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
		return nil, fmt.Errorf("fv: set data-channel deadline: %w", err)
	}

	headerIV, err := NewIV()
	if err != nil {
		return nil, err
	}
	payloadIV, err := NewIV()
	if err != nil {
		return nil, err
	}
	pad, err := RandomPad()
	if err != nil {
		return nil, err
	}
	text := BuildDataText(s.SID, payloadIV, pad)
	frame, err := EncodeFrame(headerIV, text)
	if err != nil {
		return nil, err
	}
	socksReq, err := Socks5ConnectRequest(host, port)
	if err != nil {
		return nil, err
	}
	send, err := NewCTR(s.Key, payloadIV)
	if err != nil {
		return nil, err
	}
	socksCT := make([]byte, len(socksReq))
	send.XORKeyStream(socksCT, socksReq)

	r := bufio.NewReader(conn)
	if err := writeAll(conn, frame, socksCT); err != nil {
		return nil, err
	}

	headerText, err := readFrameText(r, headerIV)
	if err != nil {
		return nil, err
	}
	if err := checkCarrierCode("data channel", headerText); err != nil {
		return nil, err
	}

	// The reply shares the payload CTR stream with everything the server
	// sends afterwards, so the stream is created before the reply is read.
	recv, err := NewCTR(s.Key, payloadIV)
	if err != nil {
		return nil, err
	}
	reply, err := ReadSocks5Reply(r, recv)
	if err != nil {
		return nil, err
	}
	if reply.Rep != 0x00 {
		return nil, fmt.Errorf("fv: socks connect failed: rep=%d atyp=%d bnd=%s:%d", reply.Rep, reply.Atyp, reply.BndAddr, reply.BndPort)
	}

	if err := conn.SetDeadline(time.Time{}); err != nil {
		return nil, fmt.Errorf("fv: clear data-channel deadline: %w", err)
	}
	ok = true
	return &streamConn{conn: conn, r: r, send: send, recv: recv}, nil
}
