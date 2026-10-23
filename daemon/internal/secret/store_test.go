package secret

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// testKey returns a deterministic dummy key. Test-only material, never a
// credential.
func testKey(seed byte) []byte {
	return bytes.Repeat([]byte{seed}, KeySize)
}

func newMemoryStore(t *testing.T, key []byte) *Store {
	t.Helper()
	s, err := New(key, "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestRoundTrip(t *testing.T) {
	s := newMemoryStore(t, testKey(0x11))
	password := []byte("correct horse battery staple")

	sealed, err := s.Seal("acc-01", password)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if string(sealed[:2]) != "v1" {
		t.Fatalf("envelope prefix = %q, want v1", sealed[:2])
	}
	if bytes.Contains(sealed, password) {
		t.Fatal("sealed envelope contains the plaintext")
	}

	opened, err := s.Open("acc-01", sealed)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(opened, password) {
		t.Fatalf("Open = %q, want %q", opened, password)
	}

	again, err := s.Seal("acc-01", password)
	if err != nil {
		t.Fatalf("Seal again: %v", err)
	}
	if bytes.Equal(sealed, again) {
		t.Fatal("two seals produced the same envelope; nonce is not random")
	}
}

func TestOpenWrongAccountID(t *testing.T) {
	s := newMemoryStore(t, testKey(0x22))
	sealed, err := s.Seal("acc-01", []byte("password-one"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := s.Open("acc-02", sealed); !errors.Is(err, ErrEnvelope) {
		t.Fatalf("Open wrong account id error = %v, want ErrEnvelope", err)
	}
	if err := s.Rotate(testKey(0x23)); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if _, err := s.Open("acc-02", sealed); !errors.Is(err, ErrEnvelope) {
		t.Fatalf("Open wrong account id after rotate error = %v, want ErrEnvelope", err)
	}
}

func TestOpenTampered(t *testing.T) {
	s := newMemoryStore(t, testKey(0x33))
	sealed, err := s.Seal("acc-01", []byte("password-one"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	cases := map[string][]byte{
		"ciphertext": append([]byte(nil), sealed...),
		"nonce":      append([]byte(nil), sealed...),
		"truncated":  append([]byte(nil), sealed[:len(sealed)-1]...),
		"version":    append([]byte(nil), sealed...),
		"short":      []byte("v1"),
	}
	cases["ciphertext"][len(cases["ciphertext"])-1] ^= 0xff
	cases["nonce"][len(version)+1] ^= 0xff
	cases["version"][0] = 'v'
	cases["version"][1] = '9'

	for name, envelope := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := s.Open("acc-01", envelope); !errors.Is(err, ErrEnvelope) {
				t.Fatalf("Open tampered %s error = %v, want ErrEnvelope", name, err)
			}
		})
	}
}

func TestOpenWrongKey(t *testing.T) {
	s := newMemoryStore(t, testKey(0x44))
	sealed, err := s.Seal("acc-01", []byte("password-one"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	other, err := New(testKey(0x45), "")
	if err != nil {
		t.Fatalf("New other key: %v", err)
	}
	t.Cleanup(func() { _ = other.Close() })
	if _, err := other.Open("acc-01", sealed); !errors.Is(err, ErrEnvelope) {
		t.Fatalf("Open with wrong key error = %v, want ErrEnvelope", err)
	}
}

func TestRotate(t *testing.T) {
	oldKey, newKey := testKey(0x55), testKey(0x56)
	path := filepath.Join(t.TempDir(), "secrets.json")
	s, err := New(oldKey, path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	first, err := s.Seal("acc-01", []byte("password-one"))
	if err != nil {
		t.Fatalf("Seal acc-01: %v", err)
	}
	second, err := s.Seal("acc-02", []byte("password-two"))
	if err != nil {
		t.Fatalf("Seal acc-02: %v", err)
	}
	before := map[string][]byte{"acc-01": first, "acc-02": second}

	if err := s.Rotate([]byte("short")); !errors.Is(err, ErrKeySize) {
		t.Fatalf("Rotate with short key error = %v, want ErrKeySize", err)
	}
	if err := s.Rotate(newKey); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	for accountID, want := range map[string][]byte{"acc-01": []byte("password-one"), "acc-02": []byte("password-two")} {
		sealed := s.entries[accountID]
		if bytes.Equal(sealed, before[accountID]) {
			t.Fatalf("rotate left %s's envelope unchanged", accountID)
		}
		opened, err := s.Open(accountID, sealed)
		if err != nil {
			t.Fatalf("Open %s after rotate: %v", accountID, err)
		}
		if !bytes.Equal(opened, want) {
			t.Fatalf("Open %s after rotate = %q, want %q", accountID, opened, want)
		}
	}
	if _, err := s.Open("acc-01", first); !errors.Is(err, ErrEnvelope) {
		t.Fatalf("old envelope after rotate error = %v, want ErrEnvelope", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := New(newKey, path)
	if err != nil {
		t.Fatalf("New with new key: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	opened, err := reopened.Open("acc-01", reopened.entries["acc-01"])
	if err != nil {
		t.Fatalf("Open after rotate and reopen: %v", err)
	}
	if string(opened) != "password-one" {
		t.Fatalf("Open after rotate and reopen = %q, want password-one", opened)
	}

	stale, err := New(oldKey, path)
	if err != nil {
		t.Fatalf("New with old key: %v", err)
	}
	t.Cleanup(func() { _ = stale.Close() })
	if _, err := stale.Open("acc-01", stale.entries["acc-01"]); !errors.Is(err, ErrEnvelope) {
		t.Fatalf("old key after rotate error = %v, want ErrEnvelope", err)
	}
}

// TestRotateTwiceKeepsEntriesUsable covers F12: zeroing the decrypted
// plaintext after each re-seal must not disturb repeated rotations.
func TestRotateTwiceKeepsEntriesUsable(t *testing.T) {
	s := newMemoryStore(t, testKey(0x71))
	first, err := s.Seal("acc-01", []byte("password-one"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	for i, key := range [][]byte{testKey(0x72), testKey(0x73)} {
		if err := s.Rotate(key); err != nil {
			t.Fatalf("Rotate %d: %v", i+1, err)
		}
		opened, err := s.Open("acc-01", s.entries["acc-01"])
		if err != nil {
			t.Fatalf("Open after rotate %d: %v", i+1, err)
		}
		if string(opened) != "password-one" {
			t.Fatalf("Open after rotate %d = %q, want password-one", i+1, opened)
		}
	}
	if _, err := s.Open("acc-01", first); !errors.Is(err, ErrEnvelope) {
		t.Fatalf("envelope from before the rotations error = %v, want ErrEnvelope", err)
	}
}

func TestFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose Unix permission bits")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.json")
	s, err := New(testKey(0x66), path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := s.Seal("acc-01", []byte("password-one")); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("file mode = %04o, want 0600", perm)
	}
	leftovers, err := filepath.Glob(filepath.Join(dir, ".secrets-*.tmp"))
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("temporary files left behind: %v", leftovers)
	}
}

func TestReopenPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	key := testKey(0x77)
	s, err := New(key, path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sealed, err := s.Seal("acc-01", []byte("password-one"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := New(key, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	stored, ok := reopened.entries["acc-01"]
	if !ok {
		t.Fatal("reopened store lost the entry")
	}
	if !bytes.Equal(stored, sealed) {
		t.Fatal("reopened store changed the envelope")
	}
	opened, err := reopened.Open("acc-01", stored)
	if err != nil {
		t.Fatalf("Open after reopen: %v", err)
	}
	if string(opened) != "password-one" {
		t.Fatalf("Open after reopen = %q, want password-one", opened)
	}
}

func TestMissingFileStartsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "secrets.json")
	s, err := New(testKey(0x88), path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if len(s.entries) != 0 {
		t.Fatalf("missing file started with %d entries, want 0", len(s.entries))
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("New created the file eagerly: %v", err)
	}
	if _, err := s.Seal("acc-01", []byte("password-one")); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Seal did not create the file: %v", err)
	}
}

func TestFileHoldsOnlySealedEnvelopes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	s, err := New(testKey(0x99), path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	password := "password-one"
	if _, err := s.Seal("acc-01", []byte(password)); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if bytes.Contains(raw, []byte(password)) {
		t.Fatal("secrets file contains the plaintext password")
	}
	var doc fileEnvelope
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if doc.Version != fileVersion {
		t.Fatalf("file version = %d, want %d", doc.Version, fileVersion)
	}
	encoded, ok := doc.Entries["acc-01"]
	if !ok {
		t.Fatal("file is missing acc-01")
	}
	sealed, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("DecodeString: %v", err)
	}
	if !strings.HasPrefix(string(sealed), version) {
		t.Fatalf("stored envelope does not start with %q", version)
	}
}

func TestKeySize(t *testing.T) {
	for _, size := range []int{0, 16, 31, 33, 64} {
		if _, err := New(make([]byte, size), ""); !errors.Is(err, ErrKeySize) {
			t.Fatalf("New with %d-byte key error = %v, want ErrKeySize", size, err)
		}
	}
}

func TestClose(t *testing.T) {
	s := newMemoryStore(t, testKey(0xaa))
	sealed, err := s.Seal("acc-01", []byte("password-one"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := s.Open("acc-01", sealed); !errors.Is(err, ErrClosed) {
		t.Fatalf("Open after Close error = %v, want ErrClosed", err)
	}
	if _, err := s.Seal("acc-02", []byte("password-two")); !errors.Is(err, ErrClosed) {
		t.Fatalf("Seal after Close error = %v, want ErrClosed", err)
	}
	if err := s.Rotate(testKey(0xab)); !errors.Is(err, ErrClosed) {
		t.Fatalf("Rotate after Close error = %v, want ErrClosed", err)
	}
}
