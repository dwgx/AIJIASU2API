package fv

import (
	"bufio"
	"context"
	"crypto"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Binary carrier (protocolVersion >= 2), confirmed empirically by AJS-07
// against the shipping Linux 4.2.3.0 client and a mock FV server:
//
//	request  = [16B key K][AES-128-CTR(K, IV=0)(4B BE text_len || text)][body]
//	body     = AES-128-CTR(sessionKey, IV=n)(auth fields or SOCKS5 CONNECT)
//	response = [AES-128-CTR(K, IV=0)(4B BE len || text)]
//	auth map = AES-128-CTR(sessionKey, IV=n)(key=value\n...), sent after the
//	           response text; the server must half-close so the client can
//	           finish reading it.
//
// The body is outside the K stream: only the 4-byte length and the text are
// K-encrypted. This is the Linux 4.2.3.0 proxy carrier. The OPV2 frame in
// httpupgrade.go is legacy/VPN and is not on this path.

// BuildBinaryAuthBody serializes the Linux-style auth body: sorted key=value
// lines, one per line, including the peip and _ padding fields. The capture
// order is c, ce, cseid, cuid, cv, dim, osd, osp, osv, p, peip, st, t, u, v, _.
func BuildBinaryAuthBody(cfg AuthConfig, unixSeconds int64, pad string) []byte {
	fields := map[string]string{
		"c":     "au",
		"ce":    "",
		"cseid": authCSEID(cfg),
		"cuid":  authCUID(cfg),
		"cv":    cfg.CoreVersion,
		"dim":   cfg.Dim,
		"osd":   cfg.OSD,
		"osp":   cfg.OSP,
		"osv":   cfg.OSV,
		"p":     cfg.Password,
		"peip":  "",
		"st":    cfg.Site,
		"t":     strconv.FormatInt(unixSeconds, 10),
		"u":     cfg.User,
		"v":     cfg.AppVersion,
		"_":     pad,
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, key := range keys {
		b.WriteString(key)
		b.WriteByte('=')
		b.WriteString(sanitizeAuthValue(fields[key]))
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

// encodeBinaryRequest builds one binary-carrier request. The outer CTR covers
// the 4-byte big-endian text length and the text; body follows unencrypted by
// K and must already be encrypted with the session key and IV n.
func encodeBinaryRequest(key []byte, text string, body []byte) ([]byte, error) {
	if len(key) != IVSize {
		return nil, fmt.Errorf("%w: carrier key is %d bytes, want %d", ErrBadKeySize, len(key), IVSize)
	}
	textBytes := []byte(text)
	if len(textBytes) > MaxFrameText {
		return nil, fmt.Errorf("fv: carrier text is %d bytes, the uint16 length allows %d", len(textBytes), MaxFrameText)
	}
	plain := make([]byte, HeaderSize+len(textBytes))
	binary.BigEndian.PutUint32(plain[:4], uint32(len(textBytes)))
	copy(plain[4:], textBytes)
	outer, err := AESCTR(key, zeroIV, plain)
	if err != nil {
		return nil, err
	}
	packet := make([]byte, 0, len(key)+len(outer)+len(body))
	packet = append(packet, key...)
	packet = append(packet, outer...)
	packet = append(packet, body...)
	return packet, nil
}

// decodeBinaryRequest parses a complete binary-carrier request. It returns the
// clear key K, the decrypted text, and the body (still session-encrypted).
func decodeBinaryRequest(packet []byte) (key []byte, text string, body []byte, err error) {
	if len(packet) < IVSize+HeaderSize {
		return nil, "", nil, fmt.Errorf("fv: carrier packet is %d bytes, want at least %d", len(packet), IVSize+HeaderSize)
	}
	key = packet[:IVSize]
	head, err := AESCTR(key, zeroIV, packet[IVSize:IVSize+HeaderSize])
	if err != nil {
		return nil, "", nil, err
	}
	if head[0] != 0 || head[1] != 0 {
		return nil, "", nil, fmt.Errorf("fv: carrier length prefix is %#x %#x, want 0 0", head[0], head[1])
	}
	textLen := int(binary.BigEndian.Uint16(head[2:4]))
	if len(packet) < IVSize+HeaderSize+textLen {
		return nil, "", nil, fmt.Errorf("fv: carrier declares %d text bytes but only %d remain", textLen, len(packet)-IVSize-HeaderSize)
	}
	plain, err := AESCTR(key, zeroIV, packet[IVSize:IVSize+HeaderSize+textLen])
	if err != nil {
		return nil, "", nil, err
	}
	return key, string(plain[4:]), packet[IVSize+HeaderSize+textLen:], nil
}

// decodeBinaryResponse decodes a complete response block: a 4-byte length and
// the text, one continuous CTR stream from position 0 under K.
func decodeBinaryResponse(packet, key []byte) (string, error) {
	plain, err := AESCTR(key, zeroIV, packet)
	if err != nil {
		return "", err
	}
	if len(plain) < HeaderSize {
		return "", fmt.Errorf("fv: carrier response is %d bytes, want at least %d", len(plain), HeaderSize)
	}
	if plain[0] != 0 || plain[1] != 0 {
		return "", fmt.Errorf("fv: carrier response length prefix is %#x %#x, want 0 0", plain[0], plain[1])
	}
	n := int(binary.BigEndian.Uint16(plain[2:4]))
	if len(plain) < HeaderSize+n {
		return "", fmt.Errorf("fv: carrier response declares %d text bytes but only %d remain", n, len(plain)-HeaderSize)
	}
	return string(plain[HeaderSize : HeaderSize+n]), nil
}

// readBinaryResponse reads the response block from a stream. The 4-byte length
// and the text share one CTR stream, so the text is decrypted at position 4.
func readBinaryResponse(r *bufio.Reader, key []byte) (string, error) {
	stream, err := NewCTR(key, zeroIV)
	if err != nil {
		return "", err
	}
	header := make([]byte, HeaderSize)
	if _, err := io.ReadFull(r, header); err != nil {
		return "", fmt.Errorf("fv: read carrier response header: %w", err)
	}
	stream.XORKeyStream(header, header)
	if header[0] != 0 || header[1] != 0 {
		return "", fmt.Errorf("fv: carrier response length prefix is %#x %#x, want 0 0", header[0], header[1])
	}
	n := int(binary.BigEndian.Uint16(header[2:4]))
	text := make([]byte, n)
	if _, err := io.ReadFull(r, text); err != nil {
		return "", fmt.Errorf("fv: read carrier response text: %w", err)
	}
	stream.XORKeyStream(text, text)
	return string(text), nil
}

// readAuthMap reads the session-key-encrypted auth map that follows the
// response text. The server half-closes after it; an idle timeout is tolerated
// so a server that keeps the socket open still yields the map.
func readAuthMap(r *bufio.Reader, conn net.Conn, sessionKey, iv []byte, idle time.Duration) (map[string]string, error) {
	if err := conn.SetReadDeadline(time.Now().Add(idle)); err != nil {
		return nil, fmt.Errorf("fv: set auth-map deadline: %w", err)
	}
	var ciphertext []byte
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			ciphertext = append(ciphertext, buf[:n]...)
		}
		if err != nil {
			if errors.Is(err, io.EOF) || isTimeout(err) {
				break
			}
			return nil, fmt.Errorf("fv: read auth map: %w", err)
		}
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		return nil, fmt.Errorf("fv: clear auth-map deadline: %w", err)
	}
	if len(ciphertext) == 0 {
		return nil, errors.New("fv: auth map missing (the server must half-close after it)")
	}
	plain, err := AESCTR(sessionKey, iv, ciphertext)
	if err != nil {
		return nil, err
	}
	return ParseFields(string(plain)), nil
}

// timeout returns the handshake deadline.
func (h *HTTPUpgrade) timeout() time.Duration {
	if h.HandshakeTimeout > 0 {
		return h.HandshakeTimeout
	}
	return 20 * time.Second
}

// authenticate implements the optional authenticator interface. Version 2 uses
// the empirical binary exchange; version 1 keeps the raw-frame OpenAJS path.
func (h *HTTPUpgrade) authenticate(ctx context.Context, cfg AuthConfig, addr string, opts AuthOptions) (*Session, error) {
	if h.version() < 2 {
		return authenticateFrame(ctx, cfg, addr, h, opts)
	}
	return h.authenticateBinary(ctx, cfg, addr, opts)
}

func (h *HTTPUpgrade) authenticateBinary(ctx context.Context, cfg AuthConfig, addr string, opts AuthOptions) (*Session, error) {
	pub := opts.PublicKey
	if pub == nil {
		var err error
		pub, err = VendorPublicKey()
		if err != nil {
			return nil, err
		}
	}
	oaepHash := opts.OAEPHash
	if oaepHash == 0 {
		oaepHash = crypto.SHA1
	}
	idle := opts.IdleTimeout
	if idle <= 0 {
		idle = 500 * time.Millisecond
	}
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}

	sessionKey, err := NewSessionKey()
	if err != nil {
		return nil, err
	}
	iv, err := NewIV()
	if err != nil {
		return nil, err
	}
	key, err := NewIV()
	if err != nil {
		return nil, err
	}
	textPad, err := RandomPad()
	if err != nil {
		return nil, err
	}
	bodyPad, err := RandomPad()
	if err != nil {
		return nil, err
	}
	bodyPlain := BuildBinaryAuthBody(cfg, now().Unix(), bodyPad)
	bodyCT, err := AESCTR(sessionKey, iv, bodyPlain)
	if err != nil {
		return nil, err
	}
	block, err := OAEPBlock(pub, oaepHash, sessionKey)
	if err != nil {
		return nil, err
	}
	text := BuildAuthText(iv, block, len(bodyCT), textPad)
	packet, err := encodeBinaryRequest(key, text, bodyCT)
	if err != nil {
		return nil, err
	}

	conn, err := h.Dial(ctx, addr)
	if err != nil {
		return nil, err
	}
	if err := conn.SetDeadline(time.Now().Add(h.timeout())); err != nil {
		conn.Close()
		return nil, fmt.Errorf("fv: set handshake deadline: %w", err)
	}
	if err := writeAll(conn, packet); err != nil {
		conn.Close()
		return nil, err
	}
	r := bufio.NewReader(conn)
	respText, err := readBinaryResponse(r, key)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if !strings.Contains(respText, "code=200") {
		conn.Close()
		return nil, fmt.Errorf("fv: authentication rejected: %q", respText)
	}
	mapFields, err := readAuthMap(r, conn, sessionKey, iv, idle)
	if err != nil {
		conn.Close()
		return nil, err
	}
	fields := ParseFields(respText)
	for k, v := range mapFields {
		fields[k] = v
	}
	if fields["r"] != "ok" {
		conn.Close()
		return nil, &AuthError{Fields: fields}
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		conn.Close()
		return nil, fmt.Errorf("fv: clear handshake deadline: %w", err)
	}
	return &Session{
		Conn:      conn,
		Key:       sessionKey,
		HeaderIV:  append([]byte(nil), iv...),
		BodyIV:    append([]byte(nil), iv...),
		SID:       fields["sid"],
		Fields:    fields,
		Transport: h,
		Addr:      addr,
		OpenedAt:  now(),
	}, nil
}

// openBinaryData opens one proxied flow over the binary carrier.
func (h *HTTPUpgrade) openBinaryData(ctx context.Context, s *Session, host string, port int) (net.Conn, error) {
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
	if err := conn.SetDeadline(time.Now().Add(h.timeout())); err != nil {
		return nil, fmt.Errorf("fv: set data-channel deadline: %w", err)
	}
	key, err := NewIV()
	if err != nil {
		return nil, err
	}
	div, err := NewIV()
	if err != nil {
		return nil, err
	}
	pad, err := RandomPad()
	if err != nil {
		return nil, err
	}
	socksReq, err := Socks5ConnectRequest(host, port)
	if err != nil {
		return nil, err
	}
	send, err := NewCTR(s.Key, div)
	if err != nil {
		return nil, err
	}
	body := make([]byte, len(socksReq))
	send.XORKeyStream(body, socksReq)
	text := BuildDataText(s.SID, div, pad)
	packet, err := encodeBinaryRequest(key, text, body)
	if err != nil {
		return nil, err
	}
	r := bufio.NewReader(conn)
	if err := writeAll(conn, packet); err != nil {
		return nil, err
	}
	respText, err := readBinaryResponse(r, key)
	if err != nil {
		return nil, err
	}
	if !strings.Contains(respText, "code=200") {
		return nil, fmt.Errorf("fv: data channel rejected: %q", respText)
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(r, reply); err != nil {
		return nil, fmt.Errorf("fv: read SOCKS5 reply: %w", err)
	}
	recv, err := NewCTR(s.Key, div)
	if err != nil {
		return nil, err
	}
	replyPlain := make([]byte, len(reply))
	recv.XORKeyStream(replyPlain, reply)
	if replyPlain[0] != 0x05 {
		return nil, fmt.Errorf("fv: SOCKS5 reply version %d, want 5", replyPlain[0])
	}
	if replyPlain[1] != 0x00 {
		return nil, fmt.Errorf("fv: socks connect failed: rep=%d", replyPlain[1])
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return nil, fmt.Errorf("fv: clear data-channel deadline: %w", err)
	}
	ok = true
	return &streamConn{conn: conn, r: r, send: send, recv: recv}, nil
}
