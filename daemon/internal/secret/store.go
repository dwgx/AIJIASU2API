// Package secret is a file-backed AES-256-GCM store for credentials at rest.
//
// Every sealed blob is bound to its account id as additional authenticated
// data, so ciphertext cannot be moved between accounts. The file is a JSON
// document of base64 envelopes, written with mode 0600 through a temporary
// file and an atomic rename.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// KeySize is the required AES-256 key length.
const KeySize = 32

const (
	nonceSize   = 12
	fileVersion = 1
	version     = "v1"
)

// Errors are matched with errors.Is. No error carries plaintext or key
// material.
var (
	ErrKeySize  = errors.New("secret: key must be 32 bytes")
	ErrClosed   = errors.New("secret: store is closed")
	ErrEnvelope = errors.New("secret: malformed or unauthenticated envelope")
)

// fileEnvelope is the on-disk JSON document. Entries map an account id to the
// base64 of its sealed envelope.
type fileEnvelope struct {
	Version int               `json:"version"`
	Entries map[string]string `json:"entries"`
}

// Store seals and opens secrets with AES-256-GCM. Entries live in memory and,
// when path is not empty, in a 0600 JSON file rewritten atomically on every
// seal, delete or rotation. All exported methods are safe for concurrent use.
type Store struct {
	mu      sync.Mutex
	aead    cipher.AEAD
	path    string
	entries map[string][]byte
	closed  bool
}

// New returns a store using key, which must be exactly 32 bytes. An empty path
// keeps every entry in memory; a missing file starts an empty store.
func New(key []byte, path string) (*Store, error) {
	if len(key) != KeySize {
		return nil, ErrKeySize
	}
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	s := &Store{aead: aead, path: path, entries: make(map[string][]byte)}
	if path == "" {
		return s, nil
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secret: cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secret: gcm: %w", err)
	}
	return aead, nil
}

// Seal encrypts plaintext for accountID and records the result. The returned
// envelope is version || nonce || ciphertext+tag. A file-backed store rewrites
// its file before returning, so a returned envelope is durable.
func (s *Store) Seal(accountID string, plaintext []byte) ([]byte, error) {
	if accountID == "" {
		return nil, errors.New("secret: account id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	sealed, err := sealWith(s.aead, accountID, plaintext)
	if err != nil {
		return nil, err
	}
	previous, hadPrevious := s.entries[accountID]
	s.entries[accountID] = sealed
	if err := s.persistLocked(); err != nil {
		if hadPrevious {
			s.entries[accountID] = previous
		} else {
			delete(s.entries, accountID)
		}
		return nil, err
	}
	return sealed, nil
}

// Delete removes the sealed entry for accountID and rewrites the file before
// returning, so a completed delete is durable. A missing entry is not an
// error: a retried account deletion converges instead of failing on the second
// attempt. When the rewrite fails the entry is restored, so a failed delete
// never leaves the in-memory map and the file disagreeing.
func (s *Store) Delete(accountID string) error {
	if accountID == "" {
		return errors.New("secret: account id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	previous, ok := s.entries[accountID]
	if !ok {
		return nil
	}
	delete(s.entries, accountID)
	if err := s.persistLocked(); err != nil {
		s.entries[accountID] = previous
		return err
	}
	return nil
}

// Open decrypts a sealed envelope for accountID. The account id must match the
// one used by Seal; a mismatch fails authentication.
func (s *Store) Open(accountID string, sealed []byte) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	return s.openLocked(accountID, sealed)
}

// Rotate re-seals every entry under newKey and rewrites the file. The old key
// stays active when re-sealing or the write fails, so rotation is all or
// nothing.
func (s *Store) Rotate(newKey []byte) error {
	if len(newKey) != KeySize {
		return ErrKeySize
	}
	next, err := newAEAD(newKey)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	rotated := make(map[string][]byte, len(s.entries))
	for accountID, sealed := range s.entries {
		plaintext, err := s.openLocked(accountID, sealed)
		if err != nil {
			return fmt.Errorf("secret: rotate account %q: %w", accountID, err)
		}
		resealed, err := sealWith(next, accountID, plaintext)
		// Zero the old key's plaintext as soon as it is no longer needed,
		// including when re-sealing fails: this package exists to minimise
		// plaintext lifetime.
		clear(plaintext)
		if err != nil {
			return fmt.Errorf("secret: rotate account %q: %w", accountID, err)
		}
		rotated[accountID] = resealed
	}
	previousAEAD, previousEntries := s.aead, s.entries
	s.aead, s.entries = next, rotated
	if err := s.persistLocked(); err != nil {
		s.aead, s.entries = previousAEAD, previousEntries
		return err
	}
	return nil
}

// Close rejects further use. Every write is already flushed, so Close only
// marks the store closed; it is safe to call more than once.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func sealWith(aead cipher.AEAD, accountID string, plaintext []byte) ([]byte, error) {
	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("secret: nonce: %w", err)
	}
	sealed := make([]byte, 0, len(version)+nonceSize+len(plaintext)+aead.Overhead())
	sealed = append(sealed, version...)
	sealed = append(sealed, nonce...)
	return aead.Seal(sealed, nonce, plaintext, []byte(accountID)), nil
}

func (s *Store) openLocked(accountID string, sealed []byte) ([]byte, error) {
	if len(sealed) < len(version)+nonceSize+s.aead.Overhead() || string(sealed[:len(version)]) != version {
		return nil, ErrEnvelope
	}
	nonce := sealed[len(version) : len(version)+nonceSize]
	plaintext, err := s.aead.Open(nil, nonce, sealed[len(version)+nonceSize:], []byte(accountID))
	if err != nil {
		return nil, fmt.Errorf("secret: account %q: %w", accountID, ErrEnvelope)
	}
	return plaintext, nil
}

func (s *Store) load() error {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("secret: read %s: %w", s.path, err)
	}
	var doc fileEnvelope
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("secret: parse %s: %w", s.path, err)
	}
	if doc.Version != fileVersion {
		return fmt.Errorf("secret: %s: unsupported version %d", s.path, doc.Version)
	}
	entries := make(map[string][]byte, len(doc.Entries))
	for accountID, encoded := range doc.Entries {
		sealed, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return fmt.Errorf("secret: %s: entry %q: %w", s.path, accountID, err)
		}
		if len(sealed) < len(version)+nonceSize+s.aead.Overhead() || string(sealed[:len(version)]) != version {
			return fmt.Errorf("secret: %s: entry %q: %w", s.path, accountID, ErrEnvelope)
		}
		entries[accountID] = sealed
	}
	s.entries = entries
	return nil
}

// persistLocked rewrites the file when the store is file-backed. The write
// goes to a 0600 temporary file in the same directory and is renamed over the
// target, so readers never observe a partial document.
func (s *Store) persistLocked() error {
	if s.path == "" {
		return nil
	}
	doc := fileEnvelope{Version: fileVersion, Entries: make(map[string]string, len(s.entries))}
	for accountID, sealed := range s.entries {
		doc.Entries[accountID] = base64.StdEncoding.EncodeToString(sealed)
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("secret: encode: %w", err)
	}
	raw = append(raw, '\n')

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("secret: state dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".secrets-*.tmp")
	if err != nil {
		return fmt.Errorf("secret: temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("secret: chmod: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("secret: write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("secret: sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("secret: close temp: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("secret: rename: %w", err)
	}
	return nil
}
