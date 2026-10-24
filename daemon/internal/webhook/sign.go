package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"
)

// Signed headers. Receivers must verify the signature before parsing the body.
const (
	HeaderSignature = "X-APC-Signature"
	HeaderTimestamp = "X-APC-Timestamp"
	HeaderEvent     = "X-APC-Event"
	HeaderDelivery  = "X-APC-Delivery"
)

// SignaturePrefix is prepended to the hex digest in X-APC-Signature.
const SignaturePrefix = "sha256="

// Sign returns the lowercase hex HMAC-SHA256 of "timestamp.body" keyed with
// secret. timestamp is the exact X-APC-Timestamp header value, so a receiver
// can recompute it without parsing.
func Sign(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// Signature computes the full header value for a request signed at at.
func Signature(secret string, at time.Time, body []byte) (signature, timestamp string) {
	timestamp = strconv.FormatInt(at.Unix(), 10)
	return SignaturePrefix + Sign(secret, timestamp, body), timestamp
}

// VerifySignature reports whether signature matches body for the given
// timestamp. It is constant-time with respect to the secret and digest.
func VerifySignature(secret, timestamp string, body []byte, signature string) bool {
	if len(signature) < len(SignaturePrefix) || signature[:len(SignaturePrefix)] != SignaturePrefix {
		return false
	}
	expected := SignaturePrefix + Sign(secret, timestamp, body)
	return hmac.Equal([]byte(expected), []byte(signature))
}
