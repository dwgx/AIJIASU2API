package fv

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
)

// Variant B has two carriers:
//
//   - protocolVersion >= 2 (primary, the default): the binary carrier
//     confirmed empirically by AJS-07 against the shipping Linux 4.2.3.0
//     client. It is implemented in binary.go as
//     [16B K][AES-CTR(K,0)(4B BE text_len || text)][session-encrypted body].
//     No OPV2 magic, no keyA^keyB prefix, and no HTTP request line.
//   - protocolVersion < 2 (legacy/VPN): the AJS-06 text HTTP-upgrade carrier
//     with an OPV2 packet body, kept for comparison. It is not the Linux
//     proxy path; tag semantics, s/n generation, and first-packet placement
//     remain [INFERENCE].
//
// The OPV2 frame layout itself is statically confirmed; only its role on the
// Linux proxy path was overturned by AJS-07. Live server compatibility is
// still unverified for both carriers.
const (
	// OPV2Magic is the cleartext-inside-CTR magic at offset 36.
	OPV2Magic = "OPV2"
	// opv2PrefixLen is keyA^keyB (16) + keyB (16) + crc32 (4).
	opv2PrefixLen = 36
	// OPV2MaxChunk is the largest tag-1 payload this emulator writes per
	// packet. The shipping field map is budgeted to 800 bytes; the tunnel
	// payload length field allows 65535.
	OPV2MaxChunk = 1400
	// OPV2TagPayload carries the tunnel bytes (SOCKS5 request, then relayed
	// data). [INFERENCE]
	OPV2TagPayload = 0x01
	// OPV2TagFields carries the AES-128-CTR(sessionKey, IV=0) field map.
	OPV2TagFields = 0x02
	// OPV2TagEnd terminates the TLV stream.
	OPV2TagEnd = 0x00
)

// OPV2Packet is one decoded OPV2 frame. KeyA is recovered as
// (keyA^keyB) XOR keyB; both halves travel in the clear, so the outer layer is
// obfuscation, not confidentiality.
type OPV2Packet struct {
	KeyA    []byte
	KeyB    []byte
	Payload []byte // tag 0x01, cleartext inside the outer CTR layer
	Body    []byte // tag 0x02 ciphertext, AES-CTR(sessionKey, IV=0)
}

// SerializeFieldMap renders fields as sorted key=value pairs joined by "&",
// with newlines inside values replaced by spaces. The shipping
// serialize_field_map emits the same shape; whether it appends a trailing "&"
// is [INFERENCE], so this implementation does not.
func SerializeFieldMap(fields map[string]string) []byte {
	if len(fields) == 0 {
		return nil
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(sanitizeAuthValue(fields[k]))
	}
	return []byte(b.String())
}

// ParseFieldMap parses a "k=v&k=v" map, ignoring empty pairs and pairs without
// "=". Values keep everything after the first "=".
func ParseFieldMap(data []byte) map[string]string {
	fields := make(map[string]string)
	for _, pair := range strings.Split(string(data), "&") {
		i := strings.IndexByte(pair, '=')
		if i <= 0 {
			continue
		}
		fields[pair[:i]] = pair[i+1:]
	}
	return fields
}

// BuildOPV2 assembles one OPV2 packet:
//
//	[16] keyA XOR keyB
//	[16] keyB
//	[ 4] crc32(keyA XOR keyB), little-endian
//	[ 4] "OPV2"
//	[ 1] tag 0x01
//	[ 2] payload length, big-endian
//	[n] payload
//	[ 1] tag 0x02
//	[ 2] body length, big-endian
//	[m] AES-128-CTR(sessionKey, IV=0, field map)
//	[ 1] tag 0x00
//
// Bytes from offset 36 to the end are then encrypted in place with
// AES-128-CTR(keyA, IV=0).
func BuildOPV2(keyA, keyB, sessionKey, payload []byte, fields map[string]string) ([]byte, error) {
	if len(keyA) != IVSize || len(keyB) != IVSize {
		return nil, fmt.Errorf("%w: OPV2 keys are %d/%d bytes, want %d", ErrBadKeySize, len(keyA), len(keyB), IVSize)
	}
	if len(sessionKey) != SessionKeySize {
		return nil, fmt.Errorf("%w: session key is %d bytes, want %d", ErrBadKeySize, len(sessionKey), SessionKeySize)
	}
	if len(payload) > MaxFrameText {
		return nil, fmt.Errorf("fv: OPV2 payload is %d bytes, the uint16 length allows %d", len(payload), MaxFrameText)
	}
	bodyPlain := SerializeFieldMap(fields)
	bodyCT, err := AESCTR(sessionKey, zeroIV, bodyPlain)
	if err != nil {
		return nil, err
	}
	if len(bodyCT) > MaxFrameText {
		return nil, fmt.Errorf("fv: OPV2 body is %d bytes, the uint16 length allows %d", len(bodyCT), MaxFrameText)
	}

	xor := make([]byte, IVSize)
	for i := range xor {
		xor[i] = keyA[i] ^ keyB[i]
	}
	packet := make([]byte, 0, opv2PrefixLen+8+len(payload)+len(bodyCT))
	packet = append(packet, xor...)
	packet = append(packet, keyB...)
	var crc [4]byte
	binary.LittleEndian.PutUint32(crc[:], crc32.ChecksumIEEE(xor))
	packet = append(packet, crc[:]...)

	tail := make([]byte, 0, 4+1+2+len(payload)+1+2+len(bodyCT)+1)
	tail = append(tail, OPV2Magic...)
	tail = append(tail, OPV2TagPayload, byte(len(payload)>>8), byte(len(payload)))
	tail = append(tail, payload...)
	tail = append(tail, OPV2TagFields, byte(len(bodyCT)>>8), byte(len(bodyCT)))
	tail = append(tail, bodyCT...)
	tail = append(tail, OPV2TagEnd)

	encrypted, err := AESCTR(keyA, zeroIV, tail)
	if err != nil {
		return nil, err
	}
	return append(packet, encrypted...), nil
}

// ParseOPV2 decodes a complete OPV2 packet.
func ParseOPV2(packet []byte) (*OPV2Packet, error) {
	if len(packet) < opv2PrefixLen+5 {
		return nil, fmt.Errorf("fv: OPV2 packet is %d bytes, want at least %d", len(packet), opv2PrefixLen+5)
	}
	xor := packet[:IVSize]
	keyB := packet[IVSize : 2*IVSize]
	if got, want := binary.LittleEndian.Uint32(packet[32:36]), crc32.ChecksumIEEE(xor); got != want {
		return nil, fmt.Errorf("fv: OPV2 crc32 = %#x, want %#x", got, want)
	}
	keyA := make([]byte, IVSize)
	for i := range keyA {
		keyA[i] = xor[i] ^ keyB[i]
	}
	plain, err := AESCTR(keyA, zeroIV, packet[opv2PrefixLen:])
	if err != nil {
		return nil, err
	}
	pkt := &OPV2Packet{KeyA: keyA, KeyB: append([]byte(nil), keyB...)}
	if err := pkt.parseTail(plain); err != nil {
		return nil, err
	}
	return pkt, nil
}

// ReadOPV2 reads one OPV2 packet from r. The prefix is read in the clear; the
// TLV tail is decrypted incrementally so no length is needed up front.
func ReadOPV2(r io.Reader) (*OPV2Packet, error) {
	prefix := make([]byte, opv2PrefixLen)
	if _, err := io.ReadFull(r, prefix); err != nil {
		return nil, fmt.Errorf("fv: read OPV2 prefix: %w", err)
	}
	xor := prefix[:IVSize]
	keyB := prefix[IVSize : 2*IVSize]
	if got, want := binary.LittleEndian.Uint32(prefix[32:36]), crc32.ChecksumIEEE(xor); got != want {
		return nil, fmt.Errorf("fv: OPV2 crc32 = %#x, want %#x", got, want)
	}
	keyA := make([]byte, IVSize)
	for i := range keyA {
		keyA[i] = xor[i] ^ keyB[i]
	}
	stream, err := NewCTR(keyA, zeroIV)
	if err != nil {
		return nil, err
	}
	readN := func(n int) ([]byte, error) {
		b := make([]byte, n)
		if _, err := io.ReadFull(r, b); err != nil {
			return nil, fmt.Errorf("fv: read OPV2 body: %w", err)
		}
		stream.XORKeyStream(b, b)
		return b, nil
	}
	magic, err := readN(4)
	if err != nil {
		return nil, err
	}
	if string(magic) != OPV2Magic {
		return nil, fmt.Errorf("fv: OPV2 magic = %q, want %q", magic, OPV2Magic)
	}
	pkt := &OPV2Packet{KeyA: keyA, KeyB: append([]byte(nil), keyB...)}
	for {
		tag, err := readN(1)
		if err != nil {
			return nil, err
		}
		if tag[0] == OPV2TagEnd {
			return pkt, nil
		}
		lenBytes, err := readN(2)
		if err != nil {
			return nil, err
		}
		n := int(binary.BigEndian.Uint16(lenBytes))
		data, err := readN(n)
		if err != nil {
			return nil, err
		}
		switch tag[0] {
		case OPV2TagPayload:
			pkt.Payload = data
		case OPV2TagFields:
			pkt.Body = data
		default:
			return nil, fmt.Errorf("fv: unknown OPV2 tag %#x", tag[0])
		}
	}
}

// parseTail decodes the decrypted tail starting at the "OPV2" magic.
func (p *OPV2Packet) parseTail(plain []byte) error {
	if len(plain) < 4 || string(plain[:4]) != OPV2Magic {
		return errors.New("fv: OPV2 magic missing")
	}
	i := 4
	for {
		if i >= len(plain) {
			return errors.New("fv: OPV2 packet ended before the terminator")
		}
		tag := plain[i]
		i++
		if tag == OPV2TagEnd {
			if i != len(plain) {
				return fmt.Errorf("fv: %d trailing bytes after OPV2 terminator", len(plain)-i)
			}
			return nil
		}
		if i+2 > len(plain) {
			return errors.New("fv: truncated OPV2 length")
		}
		n := int(binary.BigEndian.Uint16(plain[i : i+2]))
		i += 2
		if i+n > len(plain) {
			return fmt.Errorf("fv: OPV2 tag %#x declares %d bytes, only %d remain", tag, n, len(plain)-i)
		}
		data := plain[i : i+n]
		i += n
		switch tag {
		case OPV2TagPayload:
			p.Payload = append([]byte(nil), data...)
		case OPV2TagFields:
			p.Body = append([]byte(nil), data...)
		default:
			return fmt.Errorf("fv: unknown OPV2 tag %#x", tag)
		}
	}
}

// Fields decrypts the tag-2 body with sessionKey and parses the field map.
func (p *OPV2Packet) Fields(sessionKey []byte) (map[string]string, error) {
	if len(p.Body) == 0 {
		return map[string]string{}, nil
	}
	plain, err := AESCTR(sessionKey, zeroIV, p.Body)
	if err != nil {
		return nil, err
	}
	return ParseFieldMap(plain), nil
}

// HTTPUpgrade is variant B. ProtocolVersion >= 2 (the default) is the
// empirically confirmed binary carrier; ProtocolVersion 1 is the legacy text
// HTTP-upgrade + OPV2 carrier.
type HTTPUpgrade struct {
	// ProtocolVersion selects the carrier: values below 2 use the legacy text
	// HTTP-upgrade + OPV2 form; 2 and above use the binary carrier confirmed
	// against the Linux 4.2.3.0 mock. Zero means 2.
	ProtocolVersion int
	// HandshakeTimeout bounds the exchange. Default 20 s.
	HandshakeTimeout time.Duration
}

// Name implements Transport.
func (h *HTTPUpgrade) Name() string { return "httpupgrade" }

// Experimental implements Transport.
func (h *HTTPUpgrade) Experimental() bool { return true }

// Dial implements Transport.
func (h *HTTPUpgrade) Dial(ctx context.Context, addr string) (net.Conn, error) {
	return dialTCP(ctx, addr)
}

func (h *HTTPUpgrade) version() int {
	if h.ProtocolVersion <= 0 {
		return 2
	}
	return h.ProtocolVersion
}

// OpenData opens one proxied flow. Protocol version 2 (the default) uses the
// empirical binary carrier; version 1 uses the legacy text/OPV2 carrier.
func (h *HTTPUpgrade) OpenData(ctx context.Context, s *Session, host string, port int) (net.Conn, error) {
	if s == nil {
		return nil, errors.New("fv: nil session")
	}
	if s.SID == "" {
		return nil, errors.New("fv: session has no sid")
	}
	if len(s.Key) != SessionKeySize {
		return nil, fmt.Errorf("%w: session key is %d bytes, want %d", ErrBadKeySize, len(s.Key), SessionKeySize)
	}
	if h.version() >= 2 {
		return h.openBinaryData(ctx, s, host, port)
	}
	return h.openLegacyText(ctx, s, host, port)
}

// openLegacyText is the AJS-06 text HTTP-upgrade + OPV2 carrier. It is kept for
// VPN/legacy comparison; the Linux 4.2.3.0 proxy path does not use it. The
// first OPV2 packet carries the SOCKS5 CONNECT request and the server's first
// packet must carry the 10-byte SOCKS5 reply (05 00).
func (h *HTTPUpgrade) openLegacyText(ctx context.Context, s *Session, host string, port int) (net.Conn, error) {
	if s == nil {
		return nil, errors.New("fv: nil session")
	}
	if s.SID == "" {
		return nil, errors.New("fv: session has no sid")
	}
	if len(s.Key) != SessionKeySize {
		return nil, fmt.Errorf("%w: session key is %d bytes, want %d", ErrBadKeySize, len(s.Key), SessionKeySize)
	}
	timeout := h.HandshakeTimeout
	if timeout <= 0 {
		timeout = 20 * time.Second
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
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return nil, fmt.Errorf("fv: set upgrade deadline: %w", err)
	}

	nonce, err := NewIV()
	if err != nil {
		return nil, err
	}
	wsKey, err := RandomPad()
	if err != nil {
		return nil, err
	}
	socksReq, err := Socks5ConnectRequest(host, port)
	if err != nil {
		return nil, err
	}
	keyA, err := NewIV()
	if err != nil {
		return nil, err
	}
	keyB, err := NewIV()
	if err != nil {
		return nil, err
	}
	nonceHex := hex.EncodeToString(nonce)
	fields := map[string]string{"s": s.SID, "n": nonceHex, "k": wsKey}
	firstPacket, err := BuildOPV2(keyA, keyB, s.Key, socksReq, fields)
	if err != nil {
		return nil, err
	}

	request := append(buildTextUpgradeRequest(s.SID, nonceHex, wsKey, len(firstPacket)), firstPacket...)
	if err := writeAll(conn, request); err != nil {
		return nil, err
	}

	r := bufio.NewReader(conn)
	if err := readUpgradeResponse(r); err != nil {
		return nil, err
	}

	pkt, err := ReadOPV2(r)
	if err != nil {
		return nil, err
	}
	if len(pkt.Payload) < 10 {
		return nil, fmt.Errorf("fv: OPV2 SOCKS5 reply is %d bytes, want 10", len(pkt.Payload))
	}
	if pkt.Payload[0] != 0x05 {
		return nil, fmt.Errorf("fv: SOCKS5 reply version %d, want 5", pkt.Payload[0])
	}
	if pkt.Payload[1] != 0x00 {
		return nil, fmt.Errorf("fv: socks connect failed: rep=%d", pkt.Payload[1])
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return nil, fmt.Errorf("fv: clear upgrade deadline: %w", err)
	}
	ok = true
	return &opv2Conn{
		conn:       conn,
		r:          r,
		sessionKey: s.Key,
		readBuf:    append([]byte(nil), pkt.Payload[10:]...),
	}, nil
}

// buildTextUpgradeRequest builds the protocolVersion < 2 request. There is no
// Host header; Content-Length is emitted only when contentLength >= 0.
func buildTextUpgradeRequest(sid, nonceHex, wsKey string, contentLength int) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "POST /s?s=%s&n=%s HTTP/1.1\r\n", sid, nonceHex)
	b.WriteString("Upgrade: websocket\r\n")
	b.WriteString("Connection: Upgrade\r\n")
	if contentLength >= 0 {
		fmt.Fprintf(&b, "Content-Length: %d\r\n", contentLength)
	}
	fmt.Fprintf(&b, "Sec-WebSocket-Key: %s\r\n", wsKey)
	b.WriteString("\r\n")
	return []byte(b.String())
}

// readUpgradeResponse reads the protocolVersion < 2 response headers and
// requires the two tokens the shipping parser searches for.
func readUpgradeResponse(r *bufio.Reader) error {
	var b strings.Builder
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return fmt.Errorf("fv: read upgrade response: %w", err)
		}
		b.WriteString(line)
		if b.Len() > 64<<10 {
			return errors.New("fv: upgrade response header is too large")
		}
		if line == "\r\n" || line == "\n" {
			break
		}
	}
	head := b.String()
	if !strings.Contains(head, "200 OK") {
		return fmt.Errorf("fv: HTTP upgrade rejected: %s", strings.TrimSpace(head))
	}
	if !strings.Contains(head, "Upgrade: websocket") {
		return errors.New("fv: HTTP upgrade response lacks \"Upgrade: websocket\"")
	}
	return nil
}

// opv2Conn is a net.Conn carrying relayed bytes in OPV2 tag-1 payloads.
type opv2Conn struct {
	conn       net.Conn
	r          *bufio.Reader
	sessionKey []byte
	readBuf    []byte
	wmu        sync.Mutex
	wclosed    bool
}

func (c *opv2Conn) Read(p []byte) (int, error) {
	for len(c.readBuf) == 0 {
		pkt, err := ReadOPV2(c.r)
		if err != nil {
			return 0, err
		}
		if len(pkt.Payload) > 0 {
			c.readBuf = pkt.Payload
		}
		// Packets with only a field map are control traffic; keep reading.
	}
	n := copy(p, c.readBuf)
	c.readBuf = c.readBuf[n:]
	return n, nil
}

func (c *opv2Conn) Write(p []byte) (int, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.wclosed {
		return 0, io.ErrClosedPipe
	}
	written := 0
	for len(p) > 0 {
		n := min(len(p), OPV2MaxChunk)
		keyA, err := NewIV()
		if err != nil {
			return written, err
		}
		keyB, err := NewIV()
		if err != nil {
			return written, err
		}
		packet, err := BuildOPV2(keyA, keyB, c.sessionKey, p[:n], nil)
		if err != nil {
			return written, err
		}
		if _, err := c.conn.Write(packet); err != nil {
			return written, err
		}
		written += n
		p = p[n:]
	}
	return written, nil
}

// CloseWrite half-closes the underlying connection. OPV2 has no in-band FIN,
// so the TCP FIN is the only half-close the legacy carrier can express. A
// connection without CloseWrite is closed, matching the previous behaviour.
// CloseWrite is idempotent.
func (c *opv2Conn) CloseWrite() error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.wclosed {
		return nil
	}
	c.wclosed = true
	if cw, ok := c.conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return c.conn.Close()
}

func (c *opv2Conn) Close() error                       { return c.conn.Close() }
func (c *opv2Conn) LocalAddr() net.Addr                { return c.conn.LocalAddr() }
func (c *opv2Conn) RemoteAddr() net.Addr               { return c.conn.RemoteAddr() }
func (c *opv2Conn) SetDeadline(t time.Time) error      { return c.conn.SetDeadline(t) }
func (c *opv2Conn) SetReadDeadline(t time.Time) error  { return c.conn.SetReadDeadline(t) }
func (c *opv2Conn) SetWriteDeadline(t time.Time) error { return c.conn.SetWriteDeadline(t) }

// Compile-time check that the OPV2 carrier satisfies net.Conn.
var _ net.Conn = (*opv2Conn)(nil)
