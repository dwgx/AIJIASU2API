// Package fv implements the FV data-plane core: RSA-OAEP session-key
// transport, AES-128-CTR payload encryption, the s/n/k/l/_ frame, the auth
// exchange, and the pluggable data-channel transports.
//
// The wire format is derived from the public-domain OpenAJS client and the
// static analysis in research/40-protocol/PROTOCOL.md. Server-side behaviour
// is [INFERENCE] unless stated otherwise. No live service is contacted by this
// package; tests use in-process peers.
package fv

import (
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"math/big"
	"strings"
	"sync"
)

const (
	// SessionKeySize is the AES-128 key length used for the FV session key.
	SessionKeySize = 16
	// IVSize is the AES-CTR IV length, equal to the AES block size.
	IVSize = 16
	// OAEPBlockMagic prefixes the RSA ciphertext in the k= field.
	OAEPBlockMagic = "OAEP"
	// VendorModulusDecimal is the 1024-bit RSA modulus embedded in the
	// shipping clients and in OpenAJS (FvCrypto.java:21). It has 309 decimal
	// digits; ciphertexts are 128 bytes, not 256.
	VendorModulusDecimal = "141601187905926909444644730286409666755495300050721564398199769080746616339148556683219048017120018748016428099359153205651362664001287890722535799466700786136519566714160932079339659299698611914846217596226121056961277268141789905486380539961102053398435632146710731361922836526658451793921073088741138357721"
	// VendorExponent is the RSA public exponent (FvCrypto.java:22).
	VendorExponent = 65537
)

// ErrBadKeySize is returned when a key or IV has the wrong length.
var ErrBadKeySize = errors.New("fv: key or IV has the wrong length")

// ParseRSAPublicKey builds an RSA public key from a decimal modulus and an
// integer exponent.
func ParseRSAPublicKey(modulusDecimal string, exponent int) (*rsa.PublicKey, error) {
	n, ok := new(big.Int).SetString(strings.TrimSpace(modulusDecimal), 10)
	if !ok || n.Sign() <= 0 {
		return nil, errors.New("fv: invalid RSA modulus")
	}
	if exponent <= 0 {
		return nil, errors.New("fv: invalid RSA exponent")
	}
	return &rsa.PublicKey{N: n, E: exponent}, nil
}

var vendorKey = sync.OnceValues(func() (*rsa.PublicKey, error) {
	return ParseRSAPublicKey(VendorModulusDecimal, VendorExponent)
})

// VendorPublicKey returns the vendor RSA public key from PROTOCOL.md.
// Tests replace it with a generated 1024-bit key so an in-process server can
// decrypt; production defaults to this value.
func VendorPublicKey() (*rsa.PublicKey, error) {
	return vendorKey()
}

// HashByName maps the OAEP hash names used by the clients to crypto.Hash.
// The empty string and "SHA-1"/"SHA1" select SHA-1 (the OpenAJS default and
// the -Dfv.oaep fallback); "SHA-256"/"SHA256" select SHA-256.
func HashByName(name string) (crypto.Hash, error) {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "", "SHA-1", "SHA1":
		if !crypto.SHA1.Available() {
			return 0, errors.New("fv: SHA-1 is unavailable")
		}
		return crypto.SHA1, nil
	case "SHA-256", "SHA256":
		if !crypto.SHA256.Available() {
			return 0, errors.New("fv: SHA-256 is unavailable")
		}
		return crypto.SHA256, nil
	default:
		return 0, fmt.Errorf("fv: unsupported OAEP hash %q", name)
	}
}

// hashFunc returns the hash constructor for an OAEP hash.
func hashFunc(h crypto.Hash) (func() hash.Hash, error) {
	switch h {
	case crypto.SHA1:
		return sha1.New, nil
	case crypto.SHA256:
		return sha256.New, nil
	default:
		return nil, fmt.Errorf("fv: unsupported OAEP hash %v", h)
	}
}

// RSAOAEPEncrypt encrypts plaintext with RSA-OAEP under pub using the given
// digest and MGF1 with the same digest. The result is the modulus size:
// 128 bytes for the 1024-bit vendor key.
func RSAOAEPEncrypt(pub *rsa.PublicKey, h crypto.Hash, plaintext []byte) ([]byte, error) {
	if pub == nil {
		return nil, errors.New("fv: nil RSA public key")
	}
	if _, err := hashFunc(h); err != nil {
		return nil, err
	}
	ct, err := rsa.EncryptOAEP(h.New(), rand.Reader, pub, plaintext, nil)
	if err != nil {
		return nil, fmt.Errorf("fv: RSA-OAEP encrypt: %w", err)
	}
	return ct, nil
}

// OAEPBlock returns the k= payload: the 4 ASCII bytes "OAEP" followed by the
// RSA-OAEP ciphertext of sessionKey.
func OAEPBlock(pub *rsa.PublicKey, h crypto.Hash, sessionKey []byte) ([]byte, error) {
	ct, err := RSAOAEPEncrypt(pub, h, sessionKey)
	if err != nil {
		return nil, err
	}
	block := make([]byte, 0, len(OAEPBlockMagic)+len(ct))
	block = append(block, OAEPBlockMagic...)
	block = append(block, ct...)
	return block, nil
}

// NewCTR returns an AES-128-CTR stream. Encryption and decryption are the same
// operation; the caller must use a fresh stream per direction and position.
func NewCTR(key, iv []byte) (cipher.Stream, error) {
	if len(key) != SessionKeySize {
		return nil, fmt.Errorf("%w: AES key is %d bytes, want %d", ErrBadKeySize, len(key), SessionKeySize)
	}
	if len(iv) != IVSize {
		return nil, fmt.Errorf("%w: CTR IV is %d bytes, want %d", ErrBadKeySize, len(iv), IVSize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("fv: AES: %w", err)
	}
	return cipher.NewCTR(block, iv), nil
}

// AESCTR encrypts or decrypts data with AES-128-CTR, no padding.
func AESCTR(key, iv, data []byte) ([]byte, error) {
	stream, err := NewCTR(key, iv)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(data))
	stream.XORKeyStream(out, data)
	return out, nil
}

// RandomBytes returns n cryptographically random bytes.
func RandomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("fv: random: %w", err)
	}
	return b, nil
}

// NewSessionKey returns a fresh 16-byte AES-128 session key.
func NewSessionKey() ([]byte, error) { return RandomBytes(SessionKeySize) }

// NewIV returns a fresh 16-byte IV.
func NewIV() ([]byte, error) { return RandomBytes(IVSize) }
