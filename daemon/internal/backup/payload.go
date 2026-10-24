package backup

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

const (
	manifestName     = "manifest.json"
	maxManifestBytes = 1 << 20
)

// allowedBlobs is the closed set of names a backup may carry.
var allowedBlobs = map[string]bool{
	BlobState:         true,
	BlobSecrets:       true,
	BlobDeviceSecrets: true,
}

// buildPayload produces the deterministic tar and its manifest. Entries are
// sorted by name, manifest.json comes first, and every tar header carries zero
// mtime, uid/gid and user/group names.
func buildPayload(blobs map[string][]byte) ([]byte, Manifest, error) {
	if len(blobs) == 0 {
		return nil, Manifest{}, fmt.Errorf("%w: no blobs", ErrMissingBlob)
	}
	names := make([]string, 0, len(blobs))
	var total int64
	for name, data := range blobs {
		if !allowedBlobs[name] {
			return nil, Manifest{}, fmt.Errorf("%w: %q", ErrBlobName, name)
		}
		total += int64(len(data))
		if total > MaxBlobBytes {
			return nil, Manifest{}, fmt.Errorf("%w: %d bytes", ErrTooLarge, total)
		}
		names = append(names, name)
	}
	sort.Strings(names)

	manifest := Manifest{Entries: make([]ManifestEntry, len(names))}
	for i, name := range names {
		sum := sha256.Sum256(blobs[name])
		manifest.Entries[i] = ManifestEntry{
			Name:     name,
			Size:     int64(len(blobs[name])),
			SHA256:   hex.EncodeToString(sum[:]),
			Required: name == BlobState,
		}
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return nil, Manifest{}, fmt.Errorf("backup: encode manifest: %w", err)
	}

	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	if err := writeTarEntry(writer, manifestName, manifestJSON); err != nil {
		return nil, Manifest{}, err
	}
	for _, name := range names {
		if err := writeTarEntry(writer, name, blobs[name]); err != nil {
			return nil, Manifest{}, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, Manifest{}, fmt.Errorf("backup: close tar: %w", err)
	}
	return buffer.Bytes(), manifest, nil
}

func writeTarEntry(writer *tar.Writer, name string, data []byte) error {
	header := &tar.Header{
		Name:     name,
		Size:     int64(len(data)),
		Mode:     0o600,
		Typeflag: tar.TypeReg,
		ModTime:  time.Unix(0, 0).UTC(),
		Format:   tar.FormatUSTAR,
	}
	if err := writer.WriteHeader(header); err != nil {
		return fmt.Errorf("backup: write tar header: %w", err)
	}
	if _, err := writer.Write(data); err != nil {
		return fmt.Errorf("backup: write tar body: %w", err)
	}
	return nil
}

// parsePayload verifies the tar against its embedded manifest. The manifest
// must be first, entries must be sorted and unique, every blob must match its
// recorded size and sha256, and every manifest entry must be present.
func parsePayload(payload []byte) (map[string][]byte, Manifest, error) {
	reader := tar.NewReader(bytes.NewReader(payload))

	header, err := reader.Next()
	if err != nil {
		return nil, Manifest{}, fmt.Errorf("%w: missing manifest: %v", ErrManifest, err)
	}
	if header.Name != manifestName || header.Typeflag != tar.TypeReg {
		return nil, Manifest{}, fmt.Errorf("%w: first entry is %q", ErrManifest, header.Name)
	}
	if header.Size < 0 || header.Size > maxManifestBytes {
		return nil, Manifest{}, fmt.Errorf("%w: manifest is %d bytes", ErrTooLarge, header.Size)
	}
	raw, err := io.ReadAll(io.LimitReader(reader, maxManifestBytes+1))
	if err != nil || int64(len(raw)) != header.Size {
		return nil, Manifest{}, fmt.Errorf("%w: truncated manifest", ErrManifest)
	}
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, Manifest{}, fmt.Errorf("%w: %v", ErrManifest, err)
	}
	entries, err := validateManifest(manifest)
	if err != nil {
		return nil, Manifest{}, err
	}

	blobs := make(map[string][]byte, len(entries))
	var total int64
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, Manifest{}, fmt.Errorf("%w: %v", ErrManifest, err)
		}
		entry, ok := entries[header.Name]
		if !ok || header.Name == manifestName {
			return nil, Manifest{}, fmt.Errorf("%w: unexpected entry %q", ErrManifest, header.Name)
		}
		if header.Typeflag != tar.TypeReg {
			return nil, Manifest{}, fmt.Errorf("%w: %q is not a regular file", ErrManifest, header.Name)
		}
		if _, duplicate := blobs[header.Name]; duplicate {
			return nil, Manifest{}, fmt.Errorf("%w: duplicate entry %q", ErrManifest, header.Name)
		}
		if header.Size != entry.Size {
			return nil, Manifest{}, fmt.Errorf("%w: %q is %d bytes, manifest says %d", ErrManifest, header.Name, header.Size, entry.Size)
		}
		total += header.Size
		if total > MaxBlobBytes {
			return nil, Manifest{}, fmt.Errorf("%w: %d bytes", ErrTooLarge, total)
		}
		data, err := io.ReadAll(io.LimitReader(reader, entry.Size+1))
		if err != nil || int64(len(data)) != entry.Size {
			return nil, Manifest{}, fmt.Errorf("%w: truncated blob %q", ErrManifest, header.Name)
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != entry.SHA256 {
			return nil, Manifest{}, fmt.Errorf("%w: %q", ErrHash, header.Name)
		}
		blobs[header.Name] = data
	}

	for name, entry := range entries {
		if _, ok := blobs[name]; !ok && entry.Required {
			return nil, Manifest{}, fmt.Errorf("%w: %q", ErrMissingBlob, name)
		}
	}
	for name := range entries {
		if _, ok := blobs[name]; !ok {
			return nil, Manifest{}, fmt.Errorf("%w: %q missing from payload", ErrManifest, name)
		}
	}
	return blobs, manifest, nil
}

// validateManifest enforces the canonical shape produced by buildPayload:
// known names, sorted, unique, lowercase sha256, and the fixed required rule.
func validateManifest(manifest Manifest) (map[string]ManifestEntry, error) {
	if len(manifest.Entries) == 0 {
		return nil, fmt.Errorf("%w: empty", ErrManifest)
	}
	if len(manifest.Entries) > len(allowedBlobs) {
		return nil, fmt.Errorf("%w: %d entries", ErrManifest, len(manifest.Entries))
	}
	entries := make(map[string]ManifestEntry, len(manifest.Entries))
	previous := ""
	for _, entry := range manifest.Entries {
		if !allowedBlobs[entry.Name] {
			return nil, fmt.Errorf("%w: %q", ErrBlobName, entry.Name)
		}
		if entry.Name <= previous {
			return nil, fmt.Errorf("%w: entries are not sorted and unique", ErrManifest)
		}
		previous = entry.Name
		if entry.Size < 0 || entry.Size > MaxBlobBytes {
			return nil, fmt.Errorf("%w: %q is %d bytes", ErrTooLarge, entry.Name, entry.Size)
		}
		if len(entry.SHA256) != sha256.Size*2 || strings.ToLower(entry.SHA256) != entry.SHA256 {
			return nil, fmt.Errorf("%w: %q has malformed sha256", ErrManifest, entry.Name)
		}
		if _, err := hex.DecodeString(entry.SHA256); err != nil {
			return nil, fmt.Errorf("%w: %q has malformed sha256", ErrManifest, entry.Name)
		}
		if entry.Required != (entry.Name == BlobState) {
			return nil, fmt.Errorf("%w: %q has an invalid required flag", ErrManifest, entry.Name)
		}
		entries[entry.Name] = entry
	}
	return entries, nil
}
