package account

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ErrCertPin reports that the peer certificate of a control API host does not
// match the configured pin. The error carries only the expected fingerprint,
// which is public operator configuration, never a credential.
var ErrCertPin = fmt.Errorf("account: certificate pin mismatch")

// ParseCertFingerprint normalizes a SHA-256 certificate fingerprint for
// PinnedHTTPClient. Colons and surrounding whitespace are stripped and the
// remainder must decode to exactly 32 bytes, so "AA:BB:…" and "aabb…" forms
// are accepted. The return value is lowercase hex.
func ParseCertFingerprint(fingerprint string) (string, error) {
	cleaned := strings.NewReplacer(":", "", " ", "").Replace(fingerprint)
	raw, err := hex.DecodeString(cleaned)
	if err != nil {
		return "", fmt.Errorf("account: certificate fingerprint %q is not valid hex", fingerprint)
	}
	if len(raw) != sha256.Size {
		return "", fmt.Errorf("account: certificate fingerprint %q is not a %d-byte SHA-256 value", fingerprint, sha256.Size)
	}
	return hex.EncodeToString(raw), nil
}

// PinnedHTTPClient returns an *http.Client whose TLS verification accepts
// exactly one leaf certificate, the one whose SHA-256 fingerprint matches.
// Every control host currently presents the same self-signed
// CN=localhost certificate, so the pin replaces chain and hostname
// verification: InsecureSkipVerify is set on this client only and
// VerifyPeerCertificate enforces the pin instead. All other HTTPS traffic of
// the daemon keeps the default verification. A mismatch fails the handshake
// with ErrCertPin, which callers can match with errors.Is; fingerprint
// accepts the AA:BB:… or raw-hex form of a SHA-256 value. The error from a
// malformed fingerprint carries the input (public configuration), never a
// credential. timeout becomes the client's Timeout and should be the login
// transport timeout.
func PinnedHTTPClient(fingerprint string, timeout time.Duration) (*http.Client, error) {
	want, err := ParseCertFingerprint(fingerprint)
	if err != nil {
		return nil, err
	}
	wantRaw, _ := hex.DecodeString(want)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{
		MinVersion: tls.VersionTLS12,
		// Intentional: the pin below replaces chain and hostname
		// verification for this client only; see the function comment.
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return fmt.Errorf("%w: server sent no certificate", ErrCertPin)
			}
			sum := sha256.Sum256(rawCerts[0])
			if !hmac.Equal(sum[:], wantRaw) {
				return fmt.Errorf("%w: want sha256 %s", ErrCertPin, want)
			}
			return nil
		},
	}
	return &http.Client{Transport: transport, Timeout: timeout}, nil
}
