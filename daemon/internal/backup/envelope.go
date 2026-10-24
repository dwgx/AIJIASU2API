package backup

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

const (
	keySize   = 32
	saltSize  = 16
	nonceSize = 12
)

// parseHeader decodes the JSON envelope and enforces the outer size cap. It
// deliberately does not judge the version, cipher or KDF: Inspect reports what
// it finds and Open rejects what it does not support.
func parseHeader(envelope []byte) (Header, error) {
	if len(envelope) == 0 {
		return Header{}, fmt.Errorf("%w: empty envelope", ErrEnvelope)
	}
	if len(envelope) > MaxEnvelopeBytes {
		return Header{}, fmt.Errorf("%w: envelope is %d bytes", ErrTooLarge, len(envelope))
	}
	var header Header
	if err := json.Unmarshal(envelope, &header); err != nil {
		return Header{}, fmt.Errorf("%w: %v", ErrEnvelope, err)
	}
	return header, nil
}

// sealPayload builds and encrypts an envelope. random may be nil, in which
// case crypto/rand is used; tests inject a deterministic reader.
func sealPayload(payload []byte, passphrase string, now time.Time, random io.Reader) ([]byte, error) {
	if random == nil {
		random = rand.Reader
	}
	salt := make([]byte, saltSize)
	nonce := make([]byte, nonceSize)
	if _, err := io.ReadFull(random, salt); err != nil {
		return nil, fmt.Errorf("backup: read salt: %w", err)
	}
	if _, err := io.ReadFull(random, nonce); err != nil {
		return nil, fmt.Errorf("backup: read nonce: %w", err)
	}
	header := Header{
		Version:   Version,
		CreatedAt: now.UTC(),
		App:       AppName,
		KDF: KDFParams{
			Algo: KDFPBKDF2SHA256,
			Iter: Iterations,
			Salt: salt,
		},
		Cipher: CipherAES256GCM,
		Nonce:  nonce,
	}
	key, err := pbkdf2.Key(sha256.New, passphrase, salt, Iterations, keySize)
	if err != nil {
		return nil, fmt.Errorf("backup: derive key: %w", err)
	}
	defer clear(key)
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	header.Payload = aead.Seal(nil, nonce, payload, headerAAD(header))
	envelope, err := json.Marshal(header)
	if err != nil {
		return nil, fmt.Errorf("backup: encode envelope: %w", err)
	}
	return envelope, nil
}

// openPayload authenticates and decrypts an envelope. A wrong passphrase and
// any modified byte are indistinguishable by design and both return ErrAuth.
func openPayload(envelope []byte, passphrase string) ([]byte, error) {
	header, err := parseHeader(envelope)
	if err != nil {
		return nil, err
	}
	if header.Version != Version {
		return nil, fmt.Errorf("%w: got %d, want %d", ErrVersion, header.Version, Version)
	}
	if header.App != AppName {
		return nil, fmt.Errorf("%w: app %q", ErrEnvelope, header.App)
	}
	if header.Cipher != CipherAES256GCM {
		return nil, fmt.Errorf("%w: cipher %q", ErrEnvelope, header.Cipher)
	}
	if header.KDF.Algo != KDFPBKDF2SHA256 || header.KDF.Iter != Iterations {
		return nil, fmt.Errorf("%w: kdf %q/%d", ErrEnvelope, header.KDF.Algo, header.KDF.Iter)
	}
	if len(header.KDF.Salt) != saltSize {
		return nil, fmt.Errorf("%w: salt is %d bytes", ErrEnvelope, len(header.KDF.Salt))
	}
	if len(header.Nonce) != nonceSize {
		return nil, fmt.Errorf("%w: nonce is %d bytes", ErrEnvelope, len(header.Nonce))
	}
	if len(header.Payload) == 0 {
		return nil, fmt.Errorf("%w: empty payload", ErrEnvelope)
	}
	key, err := pbkdf2.Key(sha256.New, passphrase, header.KDF.Salt, header.KDF.Iter, keySize)
	if err != nil {
		return nil, fmt.Errorf("backup: derive key: %w", err)
	}
	defer clear(key)
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	payload, err := aead.Open(nil, header.Nonce, header.Payload, headerAAD(header))
	if err != nil {
		return nil, fmt.Errorf("%w: authentication failed", ErrAuth)
	}
	return payload, nil
}

// headerAAD binds every header field except the payload to the ciphertext.
func headerAAD(header Header) []byte {
	return []byte(fmt.Sprintf(
		"%s|%d|%s|%s|%s|%d|%s|%s|%s",
		AppName,
		header.Version,
		header.CreatedAt.UTC().Format(time.RFC3339Nano),
		header.App,
		header.KDF.Algo,
		header.KDF.Iter,
		base64.StdEncoding.EncodeToString(header.KDF.Salt),
		header.Cipher,
		base64.StdEncoding.EncodeToString(header.Nonce),
	))
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("backup: cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("backup: gcm: %w", err)
	}
	return aead, nil
}
