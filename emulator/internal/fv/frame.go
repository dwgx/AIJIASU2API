package fv

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	// HeaderSize is the encrypted fixed header: 2 reserved bytes followed by
	// the big-endian uint16 text length.
	HeaderSize = 4
	// MaxFrameText is the largest frame text the 16-bit length can carry.
	MaxFrameText = 0xFFFF
)

var zeroIV = make([]byte, IVSize)

// EncryptHeaderText returns the encrypted 4-byte header plus text, without the
// 16-byte cleartext IV prefix:
//
//	plain = 00 00 <text_len:uint16 BE> <text UTF-8>
//	out   = AES-CTR(key=headerIV, iv=0, plain)
func EncryptHeaderText(headerIV []byte, text string) ([]byte, error) {
	textBytes := []byte(text)
	if len(textBytes) > MaxFrameText {
		return nil, fmt.Errorf("fv: frame text is %d bytes; the uint16 length allows at most %d", len(textBytes), MaxFrameText)
	}
	plain := make([]byte, HeaderSize+len(textBytes))
	binary.BigEndian.PutUint16(plain[2:4], uint16(len(textBytes)))
	copy(plain[HeaderSize:], textBytes)
	return AESCTR(headerIV, zeroIV, plain)
}

// EncodeFrame builds a client-to-server frame: the cleartext 16-byte header IV
// followed by the encrypted header and text. The caller appends the encrypted
// body, if any, and writes the result in one write.
func EncodeFrame(headerIV []byte, text string) ([]byte, error) {
	if len(headerIV) != IVSize {
		return nil, fmt.Errorf("%w: header IV is %d bytes, want %d", ErrBadKeySize, len(headerIV), IVSize)
	}
	encrypted, err := EncryptHeaderText(headerIV, text)
	if err != nil {
		return nil, err
	}
	frame := make([]byte, 0, IVSize+len(encrypted))
	frame = append(frame, headerIV...)
	frame = append(frame, encrypted...)
	return frame, nil
}

// DecodeHeader decrypts a server-form frame header (no IV prefix) and returns
// the text and the remaining ciphertext (the body).
func DecodeHeader(headerIV, encrypted []byte) (text string, rest []byte, err error) {
	if len(headerIV) != IVSize {
		return "", nil, fmt.Errorf("%w: header IV is %d bytes, want %d", ErrBadKeySize, len(headerIV), IVSize)
	}
	if len(encrypted) < HeaderSize {
		return "", nil, fmt.Errorf("fv: frame is %d bytes, want at least %d", len(encrypted), HeaderSize)
	}
	plain, err := AESCTR(headerIV, zeroIV, encrypted)
	if err != nil {
		return "", nil, err
	}
	textLen := int(binary.BigEndian.Uint16(plain[2:4]))
	if len(encrypted) < HeaderSize+textLen {
		return "", nil, fmt.Errorf("fv: frame declares %d text bytes but only %d remain", textLen, len(encrypted)-HeaderSize)
	}
	return string(plain[HeaderSize : HeaderSize+textLen]), encrypted[HeaderSize+textLen:], nil
}

// ParseFields parses newline-delimited key=value lines. Lines without "=" or
// with "=" at position 0 are ignored, as in OpenAJS's parser.
func ParseFields(text string) map[string]string {
	fields := make(map[string]string)
	for _, line := range strings.Split(text, "\n") {
		i := strings.IndexByte(line, '=')
		if i <= 0 {
			continue
		}
		fields[line[:i]] = line[i+1:]
	}
	return fields
}

// ParseResponse decodes an FV response frame and returns its merged header and
// body fields, body values winning on duplicate keys.
//
// A server-form response begins directly with the encrypted 4-byte header and
// reuses the client's header IV at CTR position 0. For convenience a
// client-form response (starting with the header IV) is also accepted and the
// 16-byte prefix is stripped. The body, if any, is decrypted with sessionKey
// and bodyIV at CTR position 0.
func ParseResponse(response, headerIV, sessionKey, bodyIV []byte) (map[string]string, error) {
	if len(headerIV) != IVSize || len(sessionKey) != SessionKeySize || len(bodyIV) != IVSize {
		return nil, fmt.Errorf("%w: bad IV or key length", ErrBadKeySize)
	}
	payload := response
	if len(payload) >= IVSize && bytes.Equal(payload[:IVSize], headerIV) {
		payload = payload[IVSize:]
	}
	if len(payload) < HeaderSize {
		return map[string]string{}, nil
	}
	plain, err := AESCTR(headerIV, zeroIV, payload)
	if err != nil {
		return nil, err
	}
	textLen := int(binary.BigEndian.Uint16(plain[2:4]))
	if len(payload) < HeaderSize+textLen {
		return nil, fmt.Errorf("fv: response declares %d text bytes but only %d remain", textLen, len(payload)-HeaderSize)
	}
	fields := ParseFields(string(plain[HeaderSize : HeaderSize+textLen]))
	body := payload[HeaderSize+textLen:]
	if len(body) > 0 {
		bodyPlain, err := AESCTR(sessionKey, bodyIV, body)
		if err != nil {
			return nil, err
		}
		for k, v := range ParseFields(string(bodyPlain)) {
			fields[k] = v
		}
	}
	return fields, nil
}

// BuildAuthText returns the authentication frame text:
//
//	s=
//	n=<body IV, 32 lowercase hex>
//	k=<base64("OAEP" || RSA-OAEP(session key))>
//	l=<body ciphertext length>
//	_=<random padding>
//
// There is no trailing newline.
func BuildAuthText(bodyIV, oaepBlock []byte, bodyLen int, pad string) string {
	var b strings.Builder
	b.WriteString("s=\nn=")
	b.WriteString(hex.EncodeToString(bodyIV))
	b.WriteString("\nk=")
	b.WriteString(base64.StdEncoding.EncodeToString(oaepBlock))
	b.WriteString("\nl=")
	b.WriteString(strconv.Itoa(bodyLen))
	b.WriteString("\n_=")
	b.WriteString(pad)
	return b.String()
}

// BuildDataText returns the data-channel frame text:
//
//	s=<sid>
//	n=<payload IV, 32 lowercase hex>
//	k=
//	_=<random padding>
func BuildDataText(sid string, div []byte, pad string) string {
	var b strings.Builder
	b.WriteString("s=")
	b.WriteString(sid)
	b.WriteString("\nn=")
	b.WriteString(hex.EncodeToString(div))
	b.WriteString("\nk=\n_=")
	b.WriteString(pad)
	return b.String()
}

// RandomPad returns 0 to 199 lowercase ASCII letters for the _ field.
func RandomPad() (string, error) {
	var lb [1]byte
	if _, err := rand.Read(lb[:]); err != nil {
		return "", fmt.Errorf("fv: random pad: %w", err)
	}
	n := int(lb[0]) % 200
	if n == 0 {
		return "", nil
	}
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("fv: random pad: %w", err)
	}
	for i := range buf {
		buf[i] = 'a' + buf[i]%26
	}
	return string(buf), nil
}

// Socks5ConnectRequest returns the SOCKS5 CONNECT request carried inside the
// encrypted data channel:
//
//	05 01 00 03 <host_len:uint8> <host UTF-8> <port:uint16 BE>
//
// Only ATYP 3 (domain name) is produced, matching the OpenAJS data-channel
// opener.
func Socks5ConnectRequest(host string, port int) ([]byte, error) {
	if host == "" {
		return nil, errors.New("fv: empty SOCKS5 host")
	}
	if len(host) > 255 {
		return nil, fmt.Errorf("fv: SOCKS5 host is %d bytes, the length field allows 255", len(host))
	}
	if port <= 0 || port > 0xFFFF {
		return nil, fmt.Errorf("fv: SOCKS5 port %d is out of range", port)
	}
	req := make([]byte, 0, 7+len(host))
	req = append(req, 0x05, 0x01, 0x00, 0x03, byte(len(host)))
	req = append(req, host...)
	req = append(req, byte(port>>8), byte(port))
	return req, nil
}
