// Package backup builds and opens passphrase-encrypted full-panel backups.
//
// An envelope is a JSON document whose payload is AES-256-GCM ciphertext over
// a deterministic tar. The tar starts with a manifest.json describing every
// blob (name, size, sha256, required) and then carries the blobs in sorted
// order with zero modification times. The complete header is authenticated as
// additional data, so a changed version, timestamp, KDF parameter or cipher
// fails authentication exactly like a corrupted ciphertext.
//
// state.json is marked required whenever it is supplied; a manifest entry
// flagged required must be present in the payload when the backup is opened.
// secrets.json and device-secrets.json are optional because secrets can be
// disabled on a panel.
//
// This package has no routes and no store access: callers hand in blobs and
// receive blobs back.
package backup

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Envelope constants. The header values are part of the authenticated data and
// cannot change without a version bump.
const (
	Version         = 1
	AppName         = "apc"
	CipherAES256GCM = "aes-256-gcm"
	KDFPBKDF2SHA256 = "pbkdf2-sha256"
	Iterations      = 600000

	// MinPassphraseBytes and MaxPassphraseBytes bound the passphrase length.
	// The minimum matches the rest of the daemon's encrypted exports.
	MinPassphraseBytes = 12
	MaxPassphraseBytes = 1024

	// MaxBlobBytes caps the sum of all blob sizes in one backup.
	MaxBlobBytes = 64 << 20
	// MaxEnvelopeBytes caps the JSON envelope, including base64 expansion.
	MaxEnvelopeBytes = 96 << 20
)

// Blob names accepted in a backup. No other names are stored.
const (
	BlobState         = "state.json"
	BlobSecrets       = "secrets.json"
	BlobDeviceSecrets = "device-secrets.json"
)

// Typed errors are matched with errors.Is. No error carries blob contents or
// key material.
var (
	ErrVersion     = errBackup("unsupported envelope version")
	ErrPassphrase  = errBackup("invalid passphrase")
	ErrEnvelope    = errBackup("malformed envelope")
	ErrAuth        = errBackup("wrong passphrase or corrupted envelope")
	ErrBlobName    = errBackup("unknown blob name")
	ErrMissingBlob = errBackup("missing required blob")
	ErrTooLarge    = errBackup("backup exceeds size cap")
	ErrManifest    = errBackup("malformed or inconsistent manifest")
	ErrHash        = errBackup("blob hash mismatch")
)

type errBackup string

func (e errBackup) Error() string { return "backup: " + string(e) }

// KDFParams describes the key derivation applied to the passphrase.
type KDFParams struct {
	Algo string `json:"algo"`
	Iter int    `json:"iter"`
	Salt []byte `json:"salt"`
}

// Header is the JSON envelope. Payload holds the AES-256-GCM ciphertext.
type Header struct {
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"createdAt"`
	App       string    `json:"app"`
	KDF       KDFParams `json:"kdf"`
	Cipher    string    `json:"cipher"`
	Nonce     []byte    `json:"nonce"`
	Payload   []byte    `json:"payload"`
}

// ManifestEntry records one blob's size and digest inside the encrypted
// payload.
type ManifestEntry struct {
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
	Required bool   `json:"required"`
}

// Manifest lists the blobs in a backup. Entries are sorted by name.
type Manifest struct {
	Entries []ManifestEntry `json:"entries"`
}

// DiffReport is the dry-run comparison of a live panel against a restored
// backup. It reports counts and byte sizes only; blob contents never leave the
// maps. AddedBytes and ChangedBytes sum the restored sizes, RemovedBytes and
// SameBytes sum the live sizes. Names are sorted.
type DiffReport struct {
	Added   int
	Removed int
	Changed int
	Same    int

	AddedBytes   int64
	RemovedBytes int64
	ChangedBytes int64
	SameBytes    int64

	AddedNames   []string
	RemovedNames []string
	ChangedNames []string
	SameNames    []string
}

// Create seals blobs into an encrypted envelope and returns the envelope along
// with the manifest that was embedded in it. state.json is marked required
// when supplied. now is converted to UTC and authenticated.
func Create(blobs map[string][]byte, passphrase string, now time.Time) ([]byte, Manifest, error) {
	if err := checkPassphrase(passphrase); err != nil {
		return nil, Manifest{}, err
	}
	payload, manifest, err := buildPayload(blobs)
	if err != nil {
		return nil, Manifest{}, err
	}
	defer clear(payload)
	envelope, err := sealPayload(payload, passphrase, now, nil)
	if err != nil {
		return nil, Manifest{}, err
	}
	return envelope, manifest, nil
}

// Open decrypts and verifies an envelope. It refuses unsupported versions,
// unknown or missing blobs, size overruns, hash mismatches and any tampering
// with the header or ciphertext.
func Open(envelope []byte, passphrase string) (map[string][]byte, Manifest, error) {
	if err := checkPassphrase(passphrase); err != nil {
		return nil, Manifest{}, err
	}
	payload, err := openPayload(envelope, passphrase)
	if err != nil {
		return nil, Manifest{}, err
	}
	defer clear(payload)
	return parsePayload(payload)
}

// Inspect parses an envelope without decrypting it. Only Version, CreatedAt
// and App are populated; KDF, Cipher, Nonce and Payload are zero. The version
// is returned as found, so callers can report backups written by a newer
// daemon; Open is what refuses them.
func Inspect(envelope []byte) (Header, error) {
	header, err := parseHeader(envelope)
	if err != nil {
		return Header{}, err
	}
	return Header{Version: header.Version, CreatedAt: header.CreatedAt, App: header.App}, nil
}

// Diff compares the current panel blobs with the blobs a backup would restore.
// A blob present only in restored is added; only in current is removed; in
// both with different bytes is changed; byte-identical is same.
func Diff(current, restored map[string][]byte) DiffReport {
	var report DiffReport
	for name, live := range current {
		restoredData, ok := restored[name]
		switch {
		case !ok:
			report.Removed++
			report.RemovedBytes += int64(len(live))
			report.RemovedNames = append(report.RemovedNames, name)
		case bytes.Equal(live, restoredData):
			report.Same++
			report.SameBytes += int64(len(live))
			report.SameNames = append(report.SameNames, name)
		default:
			report.Changed++
			report.ChangedBytes += int64(len(restoredData))
			report.ChangedNames = append(report.ChangedNames, name)
		}
	}
	for name, restoredData := range restored {
		if _, ok := current[name]; !ok {
			report.Added++
			report.AddedBytes += int64(len(restoredData))
			report.AddedNames = append(report.AddedNames, name)
		}
	}
	sort.Strings(report.AddedNames)
	sort.Strings(report.RemovedNames)
	sort.Strings(report.ChangedNames)
	sort.Strings(report.SameNames)
	return report
}

func checkPassphrase(passphrase string) error {
	switch {
	case len(passphrase) < MinPassphraseBytes:
		return fmt.Errorf("%w: passphrase must be at least %d bytes", ErrPassphrase, MinPassphraseBytes)
	case len(passphrase) > MaxPassphraseBytes:
		return fmt.Errorf("%w: passphrase must be at most %d bytes", ErrPassphrase, MaxPassphraseBytes)
	case strings.ContainsRune(passphrase, 0):
		return fmt.Errorf("%w: passphrase must not contain NUL", ErrPassphrase)
	}
	return nil
}
