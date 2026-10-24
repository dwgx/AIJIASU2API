package backup

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"testing"
	"time"
)

const testPassphrase = "correct horse battery staple"

var testNow = time.Date(2026, 10, 24, 9, 30, 0, 0, time.UTC)

func testBlobs() map[string][]byte {
	binary := make([]byte, 256)
	for i := range binary {
		binary[i] = byte(i)
	}
	return map[string][]byte{
		BlobState:         []byte(`{"accounts":[{"id":"acc-01"}]}`),
		BlobSecrets:       []byte(`{"token":"PLAINTEXT-SECRET-MARKER"}`),
		BlobDeviceSecrets: binary,
	}
}

func mustCreate(t *testing.T, blobs map[string][]byte) ([]byte, Manifest) {
	t.Helper()
	envelope, manifest, err := Create(blobs, testPassphrase, testNow)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return envelope, manifest
}

type tarEntry struct {
	name string
	data []byte
}

func craftPayload(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for _, entry := range entries {
		if err := writeTarEntry(writer, entry.name, entry.data); err != nil {
			t.Fatalf("writeTarEntry(%q): %v", entry.name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	return buffer.Bytes()
}

func sealCrafted(t *testing.T, payload []byte) []byte {
	t.Helper()
	envelope, err := sealPayload(payload, testPassphrase, testNow, nil)
	if err != nil {
		t.Fatalf("sealPayload: %v", err)
	}
	return envelope
}

func TestRoundTrip(t *testing.T) {
	blobs := testBlobs()
	envelope, createdManifest := mustCreate(t, blobs)

	if len(createdManifest.Entries) != 3 {
		t.Fatalf("manifest entries = %d, want 3", len(createdManifest.Entries))
	}
	for i, entry := range createdManifest.Entries {
		if i > 0 && createdManifest.Entries[i-1].Name >= entry.Name {
			t.Fatalf("manifest is not sorted: %q before %q", createdManifest.Entries[i-1].Name, entry.Name)
		}
		sum := sha256.Sum256(blobs[entry.Name])
		if entry.SHA256 != hex.EncodeToString(sum[:]) {
			t.Fatalf("%s sha256 = %s, want %s", entry.Name, entry.SHA256, hex.EncodeToString(sum[:]))
		}
		if entry.Size != int64(len(blobs[entry.Name])) {
			t.Fatalf("%s size = %d, want %d", entry.Name, entry.Size, len(blobs[entry.Name]))
		}
		if want := entry.Name == BlobState; entry.Required != want {
			t.Fatalf("%s required = %v, want %v", entry.Name, entry.Required, want)
		}
	}

	opened, openedManifest, err := Open(envelope, testPassphrase)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !reflect.DeepEqual(createdManifest, openedManifest) {
		t.Fatalf("manifest mismatch:\ncreated %+v\nopened  %+v", createdManifest, openedManifest)
	}
	if len(opened) != len(blobs) {
		t.Fatalf("opened %d blobs, want %d", len(opened), len(blobs))
	}
	for name, want := range blobs {
		if !bytes.Equal(opened[name], want) {
			t.Fatalf("%s did not round-trip", name)
		}
	}
}

func TestWrongPassphrase(t *testing.T) {
	envelope, _ := mustCreate(t, testBlobs())

	_, _, err := Open(envelope, "this is not the passphrase")
	if !errors.Is(err, ErrAuth) {
		t.Fatalf("Open wrong passphrase = %v, want ErrAuth", err)
	}

	_, _, err = Open(envelope, "short")
	if !errors.Is(err, ErrPassphrase) {
		t.Fatalf("Open short passphrase = %v, want ErrPassphrase", err)
	}

	if _, _, err := Create(testBlobs(), "", testNow); !errors.Is(err, ErrPassphrase) {
		t.Fatalf("Create empty passphrase = %v, want ErrPassphrase", err)
	}
	if _, _, err := Create(testBlobs(), "elevenchars", testNow); !errors.Is(err, ErrPassphrase) {
		t.Fatalf("Create 11-byte passphrase = %v, want ErrPassphrase", err)
	}
	if _, _, err := Create(testBlobs(), "long enough passphrase\x00", testNow); !errors.Is(err, ErrPassphrase) {
		t.Fatalf("Create NUL passphrase = %v, want ErrPassphrase", err)
	}
}

func TestTamper(t *testing.T) {
	envelope, _ := mustCreate(t, testBlobs())

	var header Header
	if err := json.Unmarshal(envelope, &header); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}

	t.Run("payload bit flip", func(t *testing.T) {
		tampered := header
		tampered.Payload = bytes.Clone(header.Payload)
		tampered.Payload[len(tampered.Payload)/2] ^= 0x01
		raw, err := json.Marshal(tampered)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if _, _, err := Open(raw, testPassphrase); !errors.Is(err, ErrAuth) {
			t.Fatalf("Open tampered payload = %v, want ErrAuth", err)
		}
	})

	t.Run("header timestamp", func(t *testing.T) {
		tampered := header
		tampered.CreatedAt = header.CreatedAt.Add(time.Second)
		raw, err := json.Marshal(tampered)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if _, _, err := Open(raw, testPassphrase); !errors.Is(err, ErrAuth) {
			t.Fatalf("Open tampered header = %v, want ErrAuth", err)
		}
	})

	t.Run("kdf iterations", func(t *testing.T) {
		tampered := header
		tampered.KDF.Iter = Iterations - 1
		raw, err := json.Marshal(tampered)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if _, _, err := Open(raw, testPassphrase); !errors.Is(err, ErrEnvelope) {
			t.Fatalf("Open altered KDF = %v, want ErrEnvelope", err)
		}
	})

	t.Run("version", func(t *testing.T) {
		tampered := header
		tampered.Version = Version + 1
		raw, err := json.Marshal(tampered)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if _, _, err := Open(raw, testPassphrase); !errors.Is(err, ErrVersion) {
			t.Fatalf("Open future version = %v, want ErrVersion", err)
		}
		info, err := Inspect(raw)
		if err != nil {
			t.Fatalf("Inspect future version: %v", err)
		}
		if info.Version != Version+1 {
			t.Fatalf("Inspect version = %d, want %d", info.Version, Version+1)
		}
	})
}

func TestBlobValidation(t *testing.T) {
	t.Run("unknown name", func(t *testing.T) {
		blobs := testBlobs()
		blobs["extra.txt"] = []byte("nope")
		if _, _, err := Create(blobs, testPassphrase, testNow); !errors.Is(err, ErrBlobName) {
			t.Fatalf("Create unknown blob = %v, want ErrBlobName", err)
		}
	})

	t.Run("no blobs", func(t *testing.T) {
		if _, _, err := Create(map[string][]byte{}, testPassphrase, testNow); !errors.Is(err, ErrMissingBlob) {
			t.Fatalf("Create empty = %v, want ErrMissingBlob", err)
		}
	})

	fullManifest := func(t *testing.T) (Manifest, map[string][]byte) {
		t.Helper()
		_, manifest, err := buildPayload(testBlobs())
		if err != nil {
			t.Fatalf("buildPayload: %v", err)
		}
		return manifest, testBlobs()
	}

	t.Run("missing required blob", func(t *testing.T) {
		manifest, blobs := fullManifest(t)
		raw, err := json.Marshal(manifest)
		if err != nil {
			t.Fatalf("marshal manifest: %v", err)
		}
		payload := craftPayload(t, []tarEntry{
			{name: manifestName, data: raw},
			{name: BlobSecrets, data: blobs[BlobSecrets]},
		})
		if _, _, err := Open(sealCrafted(t, payload), testPassphrase); !errors.Is(err, ErrMissingBlob) {
			t.Fatalf("Open missing required = %v, want ErrMissingBlob", err)
		}
	})

	t.Run("missing optional blob", func(t *testing.T) {
		manifest, blobs := fullManifest(t)
		raw, err := json.Marshal(manifest)
		if err != nil {
			t.Fatalf("marshal manifest: %v", err)
		}
		payload := craftPayload(t, []tarEntry{
			{name: manifestName, data: raw},
			{name: BlobState, data: blobs[BlobState]},
		})
		if _, _, err := Open(sealCrafted(t, payload), testPassphrase); !errors.Is(err, ErrManifest) {
			t.Fatalf("Open missing optional = %v, want ErrManifest", err)
		}
	})

	t.Run("extra blob", func(t *testing.T) {
		blobs := map[string][]byte{BlobSecrets: []byte("only-secret")}
		_, manifest, err := buildPayload(blobs)
		if err != nil {
			t.Fatalf("buildPayload: %v", err)
		}
		raw, err := json.Marshal(manifest)
		if err != nil {
			t.Fatalf("marshal manifest: %v", err)
		}
		extra := craftPayload(t, []tarEntry{
			{name: manifestName, data: raw},
			{name: BlobSecrets, data: blobs[BlobSecrets]},
			{name: BlobState, data: []byte(`{}`)},
		})
		if _, _, err := Open(sealCrafted(t, extra), testPassphrase); !errors.Is(err, ErrManifest) {
			t.Fatalf("Open extra blob = %v, want ErrManifest", err)
		}
	})

	t.Run("hash mismatch", func(t *testing.T) {
		_, manifest, err := buildPayload(map[string][]byte{BlobSecrets: []byte("AAAA")})
		if err != nil {
			t.Fatalf("buildPayload: %v", err)
		}
		raw, err := json.Marshal(manifest)
		if err != nil {
			t.Fatalf("marshal manifest: %v", err)
		}
		payload := craftPayload(t, []tarEntry{
			{name: manifestName, data: raw},
			{name: BlobSecrets, data: []byte("BBBB")},
		})
		if _, _, err := Open(sealCrafted(t, payload), testPassphrase); !errors.Is(err, ErrHash) {
			t.Fatalf("Open hash mismatch = %v, want ErrHash", err)
		}
	})

	t.Run("required flag", func(t *testing.T) {
		manifest, _ := fullManifest(t)
		for i := range manifest.Entries {
			if manifest.Entries[i].Name == BlobSecrets {
				manifest.Entries[i].Required = true
			}
		}
		raw, err := json.Marshal(manifest)
		if err != nil {
			t.Fatalf("marshal manifest: %v", err)
		}
		payload := craftPayload(t, []tarEntry{{name: manifestName, data: raw}})
		if _, _, err := Open(sealCrafted(t, payload), testPassphrase); !errors.Is(err, ErrManifest) {
			t.Fatalf("Open bad required flag = %v, want ErrManifest", err)
		}
	})
}

func TestDeterminism(t *testing.T) {
	blobs := testBlobs()

	first, firstManifest, err := buildPayload(blobs)
	if err != nil {
		t.Fatalf("buildPayload: %v", err)
	}
	second, secondManifest, err := buildPayload(blobs)
	if err != nil {
		t.Fatalf("buildPayload: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("two builds of the same blobs produced different tar bytes")
	}
	if !reflect.DeepEqual(firstManifest, secondManifest) {
		t.Fatal("two builds of the same blobs produced different manifests")
	}

	envelopeA, _, err := Create(blobs, testPassphrase, testNow)
	if err != nil {
		t.Fatalf("Create A: %v", err)
	}
	envelopeB, _, err := Create(blobs, testPassphrase, testNow)
	if err != nil {
		t.Fatalf("Create B: %v", err)
	}
	headerA, err := parseHeader(envelopeA)
	if err != nil {
		t.Fatalf("parseHeader A: %v", err)
	}
	headerB, err := parseHeader(envelopeB)
	if err != nil {
		t.Fatalf("parseHeader B: %v", err)
	}
	if headerA.Version != headerB.Version ||
		!headerA.CreatedAt.Equal(headerB.CreatedAt) ||
		headerA.App != headerB.App ||
		headerA.KDF.Algo != headerB.KDF.Algo ||
		headerA.KDF.Iter != headerB.KDF.Iter ||
		headerA.Cipher != headerB.Cipher {
		t.Fatal("envelope structure differs for identical inputs and fixed now")
	}
	if bytes.Equal(headerA.KDF.Salt, headerB.KDF.Salt) {
		t.Fatal("salt was reused across envelopes")
	}
	if bytes.Equal(headerA.Nonce, headerB.Nonce) {
		t.Fatal("nonce was reused across envelopes")
	}
	if bytes.Equal(headerA.Payload, headerB.Payload) {
		t.Fatal("ciphertext was reused across envelopes")
	}

	headerA.KDF.Salt, headerA.Nonce, headerA.Payload = nil, nil, nil
	headerB.KDF.Salt, headerB.Nonce, headerB.Payload = nil, nil, nil
	if !reflect.DeepEqual(headerA, headerB) {
		t.Fatal("envelopes differ outside salt, nonce and payload")
	}

	// With an injected reader the whole envelope is byte-identical.
	random := bytes.Repeat([]byte{0x5a}, saltSize+nonceSize)
	sealedA, err := sealPayload(first, testPassphrase, testNow, bytes.NewReader(random))
	if err != nil {
		t.Fatalf("sealPayload A: %v", err)
	}
	sealedB, err := sealPayload(first, testPassphrase, testNow, bytes.NewReader(random))
	if err != nil {
		t.Fatalf("sealPayload B: %v", err)
	}
	if !bytes.Equal(sealedA, sealedB) {
		t.Fatal("sealPayload is not deterministic with a fixed reader")
	}
}

func TestPayloadHeaders(t *testing.T) {
	blobs := testBlobs()
	payload, manifest, err := buildPayload(blobs)
	if err != nil {
		t.Fatalf("buildPayload: %v", err)
	}

	reader := tar.NewReader(bytes.NewReader(payload))
	var names []string
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("tar Next: %v", err)
		}
		names = append(names, header.Name)
		if header.ModTime.Unix() != 0 {
			t.Fatalf("%s mtime = %v, want the Unix epoch", header.Name, header.ModTime)
		}
		if header.Mode != 0o600 || header.Uid != 0 || header.Gid != 0 || header.Uname != "" || header.Gname != "" {
			t.Fatalf("%s header is not canonical: %+v", header.Name, header)
		}
		if header.Name == manifestName {
			raw, err := io.ReadAll(reader)
			if err != nil {
				t.Fatalf("read manifest: %v", err)
			}
			var embedded Manifest
			if err := json.Unmarshal(raw, &embedded); err != nil {
				t.Fatalf("decode embedded manifest: %v", err)
			}
			if !reflect.DeepEqual(embedded, manifest) {
				t.Fatal("embedded manifest differs from the returned manifest")
			}
		}
	}

	want := []string{manifestName, BlobDeviceSecrets, BlobSecrets, BlobState}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("tar order = %v, want %v", names, want)
	}
}

func TestDiff(t *testing.T) {
	current := map[string][]byte{
		BlobState:         []byte("same"),
		BlobSecrets:       []byte("old-secret"),
		BlobDeviceSecrets: []byte("removed-bytes"),
	}
	restored := map[string][]byte{
		BlobState:    []byte("same"),
		BlobSecrets:  []byte("new-secret!"),
		"added.json": []byte("added"),
	}
	report := Diff(current, restored)

	if report.Added != 1 || report.Removed != 1 || report.Changed != 1 || report.Same != 1 {
		t.Fatalf("counts = added %d removed %d changed %d same %d, want 1/1/1/1",
			report.Added, report.Removed, report.Changed, report.Same)
	}
	if report.AddedBytes != 5 || report.RemovedBytes != 13 || report.ChangedBytes != 11 || report.SameBytes != 4 {
		t.Fatalf("bytes = added %d removed %d changed %d same %d, want 5/13/11/4",
			report.AddedBytes, report.RemovedBytes, report.ChangedBytes, report.SameBytes)
	}
	if !reflect.DeepEqual(report.AddedNames, []string{"added.json"}) ||
		!reflect.DeepEqual(report.RemovedNames, []string{BlobDeviceSecrets}) ||
		!reflect.DeepEqual(report.ChangedNames, []string{BlobSecrets}) ||
		!reflect.DeepEqual(report.SameNames, []string{BlobState}) {
		t.Fatalf("names = %+v", report)
	}

	empty := Diff(nil, nil)
	if !reflect.DeepEqual(empty, DiffReport{}) {
		t.Fatalf("empty diff = %+v, want zero value", empty)
	}
}

func TestSizeCap(t *testing.T) {
	oversized := map[string][]byte{
		BlobState:   []byte("{}"),
		BlobSecrets: make([]byte, MaxBlobBytes),
	}
	if _, _, err := Create(oversized, testPassphrase, testNow); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Create over cap = %v, want ErrTooLarge", err)
	}

	half := MaxBlobBytes/2 + 1
	twoHalves := map[string][]byte{
		BlobSecrets:       make([]byte, half),
		BlobDeviceSecrets: make([]byte, half),
	}
	if _, _, err := Create(twoHalves, testPassphrase, testNow); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Create combined over cap = %v, want ErrTooLarge", err)
	}
}

func TestNoPlaintextInEnvelope(t *testing.T) {
	blobs := testBlobs()
	markers := [][]byte{
		[]byte("PLAINTEXT-SECRET-MARKER"),
		[]byte("acc-01"),
		[]byte(testPassphrase),
	}
	envelope, _ := mustCreate(t, blobs)
	for _, marker := range markers {
		if bytes.Contains(envelope, marker) {
			t.Fatalf("envelope contains plaintext %q", marker)
		}
	}
	if bytes.Contains(envelope, []byte(BlobState)) {
		t.Fatal("envelope contains a blob name outside base64 of the payload")
	}
}

func TestInspect(t *testing.T) {
	envelope, _ := mustCreate(t, testBlobs())
	info, err := Inspect(envelope)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if info.Version != Version || info.App != AppName || !info.CreatedAt.Equal(testNow) {
		t.Fatalf("Inspect = %+v", info)
	}
	if !reflect.DeepEqual(info.KDF, KDFParams{}) || info.Cipher != "" || info.Nonce != nil || info.Payload != nil {
		t.Fatalf("Inspect leaked fields beyond version/createdAt/app: %+v", info)
	}

	for _, malformed := range [][]byte{nil, []byte(""), []byte("{"), []byte(`{"version":`)} {
		if _, err := Inspect(malformed); !errors.Is(err, ErrEnvelope) {
			t.Fatalf("Inspect(%q) = %v, want ErrEnvelope", malformed, err)
		}
	}
}
