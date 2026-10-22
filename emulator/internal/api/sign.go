// Package api implements the AJiaSu control API: request signing, device
// parameters, login, loginSync, and the server list.
//
// Everything here is derived from the public-domain OpenAJS client and the
// static analysis in research/40-protocol/PROTOCOL.md. No live endpoint is
// contacted by this package; the caller supplies the HTTP client.
package api

import (
	"encoding/hex"
	"sort"
	"strings"
)

// DefaultSecret is the signing secret hard-coded in the public-domain OpenAJS
// client (AjsClient.java:59), written exactly as it appears there: 20 bytes as
// colon-separated uppercase hex, colons included.
//
// The shipping 4.9.6.0 Windows and 4.2.3.0 Linux clients do not contain this
// string (neither ASCII nor raw bytes); their secret is [UNKNOWN]. This value
// therefore authenticates an OpenAJS-compatible request, not necessarily a
// request the current vendor backend accepts.
const DefaultSecret = "84:C1:12:45:0B:68:F9:B2:06:95:00:44:74:37:55:71:60:B5:98:31"

// DualHash computes the 8-byte AJiaSu "dual hash" over data.
//
// Two unsigned 32-bit accumulators are updated once per input byte:
//
//	h2 = h2 ^ (c + 32*h2 + (h2 >> 2))
//	h1 = c + 65599*h1
//
// seeded with h1=0xCAFEBABE and h2=0xDEADBEEF. All arithmetic is modulo 2^32.
// The Java implementation unrolls the loop two bytes at a time, but the
// per-byte update is identical, so pairing has no effect on the result.
//
// Output interleaves the accumulators high byte first:
// h2>>24, h1>>24, h2>>16, h1>>16, h2>>8, h1>>8, h2, h1.
func DualHash(data []byte) [8]byte {
	h1 := uint32(0xCAFEBABE)
	h2 := uint32(0xDEADBEEF)
	for _, c := range data {
		cc := uint32(c)
		h2 = h2 ^ (cc + 32*h2 + (h2 >> 2))
		h1 = cc + 65599*h1
	}
	return [8]byte{
		byte(h2 >> 24), byte(h1 >> 24),
		byte(h2 >> 16), byte(h1 >> 16),
		byte(h2 >> 8), byte(h1 >> 8),
		byte(h2), byte(h1),
	}
}

// DualHashHex returns DualHash(data) as 16 lowercase hex characters.
func DualHashHex(data []byte) string {
	sum := DualHash(data)
	return hex.EncodeToString(sum[:])
}

// Sign returns the X-Sig header value for body and timestamp.
//
// The signed material is secret + "|" + body + "|" + timestamp + "|" + secret,
// and the signature is the lowercase hex dual hash of its UTF-8 bytes. body
// must be the exact query string sent on the wire, and timestamp is the same
// decimal epoch-millisecond value sent in X-Timestamp.
func Sign(secret, body, timestamp string) string {
	return DualHashHex([]byte(secret + "|" + body + "|" + timestamp + "|" + secret))
}

// URLEncode percent-encodes a query value the way FVSign.urlEncode does.
//
// The unreserved set is A-Z a-z 0-9 - . _ ~; every other byte is emitted as
// "%" plus two lowercase hex digits. Space becomes %20, never "+". Non-ASCII
// input is encoded byte-by-byte from its UTF-8 representation.
func URLEncode(value string) string {
	const hexDigits = "0123456789abcdef"
	var b strings.Builder
	b.Grow(len(value))
	for i := range len(value) {
		c := value[i]
		if isUnreserved(c) {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexDigits[c>>4])
		b.WriteByte(hexDigits[c&0x0f])
	}
	return b.String()
}

func isUnreserved(c byte) bool {
	switch {
	case 'A' <= c && c <= 'Z':
		return true
	case 'a' <= c && c <= 'z':
		return true
	case '0' <= c && c <= '9':
		return true
	}
	return c == '-' || c == '.' || c == '_' || c == '~'
}

// BuildQuery serializes params as a signed request body: keys sorted
// ascending, values percent-encoded, keys verbatim, joined by "&" with no
// trailing separator.
//
// The Java side sorts a TreeMap, i.e. by UTF-16 code unit. Control API keys
// are ASCII by construction, where that order equals byte order.
func BuildQuery(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
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
		b.WriteString(URLEncode(params[k]))
	}
	return b.String()
}
