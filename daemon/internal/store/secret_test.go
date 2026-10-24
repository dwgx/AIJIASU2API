package store_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/secret"
	"ajsaccountd/internal/store"
)

const testPassword = "test-password-not-a-credential"

// testKey is fixed dummy key material. It is not a credential.
func testKey() []byte {
	return []byte("0123456789abcdef0123456789abcdef")
}

func newSealer(t *testing.T) (*secret.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secrets.json")
	s, err := secret.New(testKey(), path)
	if err != nil {
		t.Fatalf("secret.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, path
}

func accountInput(label, password string) model.CreateAccountInput {
	return model.CreateAccountInput{
		Label:       label,
		Username:    "user-" + label,
		Password:    password,
		Platform:    model.PlatformAndroid,
		Concurrency: 32,
	}
}

func TestCreateAccountSealsPassword(t *testing.T) {
	sealer, path := newSealer(t)
	st := store.NewWithSealer(sealer)

	account, err := st.CreateAccount(accountInput("sealed", testPassword))
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	got, err := st.AccountSecret(account.ID)
	if err != nil {
		t.Fatalf("AccountSecret: %v", err)
	}
	if string(got) != testPassword {
		t.Fatalf("AccountSecret = %q, want the created password", got)
	}

	encoded, err := json.Marshal(account)
	if err != nil {
		t.Fatalf("Marshal account: %v", err)
	}
	lower := bytes.ToLower(encoded)
	if bytes.Contains(lower, []byte("password")) || bytes.Contains(lower, []byte("secret")) {
		t.Fatalf("account JSON exposes a credential field: %s", encoded)
	}
	if bytes.Contains(encoded, []byte(testPassword)) {
		t.Fatalf("account JSON contains the password: %s", encoded)
	}

	list, err := json.Marshal(st.Accounts())
	if err != nil {
		t.Fatalf("Marshal accounts: %v", err)
	}
	if bytes.Contains(list, []byte(testPassword)) {
		t.Fatal("account list JSON contains the password")
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile secrets: %v", err)
	}
	if bytes.Contains(raw, []byte(testPassword)) {
		t.Fatal("secrets file contains the plaintext password")
	}
}

func TestAccountSecretPerAccount(t *testing.T) {
	sealer, _ := newSealer(t)
	st := store.NewWithSealer(sealer)

	first, err := st.CreateAccount(accountInput("first", "password-one"))
	if err != nil {
		t.Fatalf("CreateAccount first: %v", err)
	}
	second, err := st.CreateAccount(accountInput("second", "password-two"))
	if err != nil {
		t.Fatalf("CreateAccount second: %v", err)
	}
	for id, want := range map[string]string{first.ID: "password-one", second.ID: "password-two"} {
		got, err := st.AccountSecret(id)
		if err != nil {
			t.Fatalf("AccountSecret %s: %v", id, err)
		}
		if string(got) != want {
			t.Fatalf("AccountSecret %s = %q, want %q", id, got, want)
		}
	}
}

func TestDeleteAccountRemovesSecret(t *testing.T) {
	sealer, _ := newSealer(t)
	st := store.NewWithSealer(sealer)

	account, err := st.CreateAccount(accountInput("doomed", testPassword))
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := st.DeleteAccount(account.ID); err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}
	if _, ok := st.Account(account.ID); ok {
		t.Fatal("deleted account is still listed")
	}
	if _, err := st.AccountSecret(account.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("AccountSecret after delete error = %v, want ErrNotFound", err)
	}
}

func TestAccountSecretWithoutSealer(t *testing.T) {
	st := store.New()
	account, err := st.CreateAccount(accountInput("discarded", testPassword))
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if _, err := st.AccountSecret(account.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("AccountSecret without sealer error = %v, want ErrNotFound", err)
	}
	encoded, err := json.Marshal(account)
	if err != nil {
		t.Fatalf("Marshal account: %v", err)
	}
	if strings.Contains(string(encoded), testPassword) {
		t.Fatal("discarded password leaked into the account JSON")
	}
}

func TestAccountSecretUnknownAccount(t *testing.T) {
	sealer, _ := newSealer(t)
	st := store.NewWithSealer(sealer)
	if _, err := st.AccountSecret("acc-99"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("AccountSecret unknown account error = %v, want ErrNotFound", err)
	}
	if _, err := st.AccountSecret("acc-01"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("AccountSecret seeded account error = %v, want ErrNotFound", err)
	}
}

type failingSealer struct{ err error }

func (f failingSealer) Seal(string, []byte) ([]byte, error) { return nil, f.err }
func (f failingSealer) Open(string, []byte) ([]byte, error) { return nil, f.err }
func (f failingSealer) Delete(string) error                 { return f.err }

func TestCreateAccountSealFailure(t *testing.T) {
	sentinel := errors.New("sealer unavailable")
	st := store.NewWithSealer(failingSealer{err: sentinel})
	before := len(st.Accounts())

	_, err := st.CreateAccount(accountInput("unsealed", testPassword))
	if !errors.Is(err, sentinel) {
		t.Fatalf("CreateAccount error = %v, want the sealer failure", err)
	}
	if after := len(st.Accounts()); after != before {
		t.Fatalf("failed create changed the account count from %d to %d", before, after)
	}
}

// deleteFailSealer seals and opens normally but refuses the removal, so a test
// can prove DeleteAccount fails closed instead of leaking the sealed blob.
type deleteFailSealer struct {
	*secret.Store
}

var errDeleteUnavailable = errors.New("delete unavailable")

func (deleteFailSealer) Delete(string) error { return errDeleteUnavailable }

// sealedEntries reads the sealer's own file, so a test can assert what a
// restarted daemon would find there.
func sealedEntries(t *testing.T, path string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile secrets file: %v", err)
	}
	var doc struct {
		Version int               `json:"version"`
		Entries map[string]string `json:"entries"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("Unmarshal secrets file: %v", err)
	}
	return doc.Entries
}

func readSnapshotFile(t *testing.T, path string) store.Snapshot {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile state: %v", err)
	}
	var snap store.Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("Unmarshal state: %v", err)
	}
	return snap
}

// TestDeleteAccountRemovesPersistedSecret is the AJS-170 regression: a delete
// must be reflected by the snapshot and by the sealer's own file, not only by
// the in-memory maps, and a reload must resurrect neither the account nor its
// sealed password or device identity.
func TestDeleteAccountRemovesPersistedSecret(t *testing.T) {
	sealer, secretsPath := newSealer(t)
	st := store.NewWithSealer(sealer)
	account, err := st.CreateAccount(accountInput("delete-persisted", testPassword))
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	device := store.DeviceIdentity{
		UID:   "uid-delete-persisted",
		OSDID: "0123456789abcdef",
		CUID:  "cuid-delete-persisted",
		CSEID: "cseid-delete-persisted",
	}
	if _, err := st.EnsureAccountDevice(account.ID, device); err != nil {
		t.Fatalf("EnsureAccountDevice: %v", err)
	}
	statePath := filepath.Join(t.TempDir(), "state.json")
	if err := st.Save(statePath); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Precondition: both persisted files carry the account before the delete.
	saved := readSnapshotFile(t, statePath)
	sealed, ok := saved.Secrets[account.ID]
	if !ok {
		t.Fatalf("state file has no sealed secret for %q", account.ID)
	}
	if _, ok := saved.Devices[account.ID]; !ok {
		t.Fatalf("state file has no device identity for %q", account.ID)
	}
	if _, ok := sealedEntries(t, secretsPath)[account.ID]; !ok {
		t.Fatalf("secrets file has no sealed entry for %q", account.ID)
	}

	if err := st.DeleteAccount(account.ID); err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}

	// In-memory state.
	if _, ok := st.Account(account.ID); ok {
		t.Fatal("deleted account is still listed")
	}
	if _, err := st.AccountSecret(account.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("AccountSecret after delete error = %v, want ErrNotFound", err)
	}
	if _, ok := st.AccountDevice(account.ID); ok {
		t.Fatal("device identity survived DeleteAccount")
	}
	if _, ok := sealedEntries(t, secretsPath)[account.ID]; ok {
		t.Fatal("DeleteAccount left the sealed entry in the sealer file")
	}

	// Snapshot view, the saved document and a reload.
	raw, err := st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	var snap store.Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("Unmarshal snapshot: %v", err)
	}
	if _, ok := snap.Secrets[account.ID]; ok {
		t.Fatal("the snapshot still carries the sealed secret")
	}
	if _, ok := snap.Devices[account.ID]; ok {
		t.Fatal("the snapshot still carries the device identity")
	}
	if bytes.Contains(raw, []byte(base64.StdEncoding.EncodeToString(sealed))) {
		t.Fatal("the deleted envelope is still in the snapshot")
	}
	if err := st.Save(statePath); err != nil {
		t.Fatalf("Save after delete: %v", err)
	}
	onDisk := readSnapshotFile(t, statePath)
	if _, ok := onDisk.Secrets[account.ID]; ok {
		t.Fatal("the state file still carries the sealed secret")
	}
	if _, ok := onDisk.Devices[account.ID]; ok {
		t.Fatal("the state file still carries the device identity")
	}

	fresh := store.NewWithSealer(sealer)
	if err := fresh.Load(statePath); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := fresh.Account(account.ID); ok {
		t.Fatalf("deleted account %q came back after reload", account.ID)
	}
	if _, err := fresh.AccountSecret(account.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("AccountSecret after reload error = %v, want ErrNotFound", err)
	}
	if _, ok := fresh.AccountDevice(account.ID); ok {
		t.Fatal("device identity came back after reload")
	}
}

// TestPersistedSealerFilePrunesDeletedSecret pins the sealer file itself: the
// deleted account's envelope is gone, the survivor's envelope is untouched and
// still opens after a fresh load from disk.
func TestPersistedSealerFilePrunesDeletedSecret(t *testing.T) {
	sealer, secretsPath := newSealer(t)
	st := store.NewWithSealer(sealer)
	const survivorPassword = "survivor-password-not-a-credential"
	doomed, err := st.CreateAccount(accountInput("prune-doomed", testPassword))
	if err != nil {
		t.Fatalf("CreateAccount doomed: %v", err)
	}
	survivor, err := st.CreateAccount(accountInput("prune-survivor", survivorPassword))
	if err != nil {
		t.Fatalf("CreateAccount survivor: %v", err)
	}

	before := sealedEntries(t, secretsPath)
	doomedSealed, ok := before[doomed.ID]
	if !ok {
		t.Fatalf("secrets file has no entry for %q", doomed.ID)
	}
	survivorSealed, ok := before[survivor.ID]
	if !ok {
		t.Fatalf("secrets file has no entry for %q", survivor.ID)
	}

	if err := st.DeleteAccount(doomed.ID); err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}
	after := sealedEntries(t, secretsPath)
	if _, ok := after[doomed.ID]; ok {
		t.Fatalf("the deleted account %q is still in the sealer file", doomed.ID)
	}
	if after[survivor.ID] != survivorSealed {
		t.Fatal("the surviving account's envelope changed")
	}
	raw, err := os.ReadFile(secretsPath)
	if err != nil {
		t.Fatalf("ReadFile secrets: %v", err)
	}
	if bytes.Contains(raw, []byte(doomedSealed)) {
		t.Fatal("the deleted envelope is still in the sealer file")
	}

	// A fresh sealer loading the same file opens the survivor, so the prune did
	// not damage the remaining entry.
	reopened, err := secret.New(testKey(), secretsPath)
	if err != nil {
		t.Fatalf("secret.New: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	envelope, err := base64.StdEncoding.DecodeString(after[survivor.ID])
	if err != nil {
		t.Fatalf("DecodeString: %v", err)
	}
	plaintext, err := reopened.Open(survivor.ID, envelope)
	if err != nil {
		t.Fatalf("Open surviving entry from the file: %v", err)
	}
	if string(plaintext) != survivorPassword {
		t.Fatalf("Open surviving entry = %q, want %q", plaintext, survivorPassword)
	}
}

// TestDeleteAccountIdempotent pins the retry contract: the second delete is a
// not-found that changes nothing, and only one audit event was recorded.
func TestDeleteAccountIdempotent(t *testing.T) {
	sealer, secretsPath := newSealer(t)
	st := store.NewWithSealer(sealer)
	account, err := st.CreateAccount(accountInput("delete-idempotent", testPassword))
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	before := len(st.Accounts())
	if err := st.DeleteAccount(account.ID); err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}
	if err := st.DeleteAccount(account.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second DeleteAccount error = %v, want ErrNotFound", err)
	}
	if got := len(st.Accounts()); got != before-1 {
		t.Fatalf("account count = %d, want %d", got, before-1)
	}
	deletes := 0
	for _, event := range st.Audit(store.AuditRetention) {
		if event.Action == "account.delete" {
			deletes++
		}
	}
	if deletes != 1 {
		t.Fatalf("account.delete audit events = %d, want 1", deletes)
	}
	if _, ok := sealedEntries(t, secretsPath)[account.ID]; ok {
		t.Fatal("the retried delete restored the sealed entry")
	}
}

// TestDeleteAccountSealerFailureKeepsAccount proves the fail-closed order: when
// the sealer cannot remove the entry, the account and its secret stay put and
// the error carries neither the password nor the sealer's file path.
func TestDeleteAccountSealerFailureKeepsAccount(t *testing.T) {
	inner, secretsPath := newSealer(t)
	st := store.NewWithSealer(deleteFailSealer{Store: inner})
	account, err := st.CreateAccount(accountInput("delete-fails", testPassword))
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	err = st.DeleteAccount(account.ID)
	if !errors.Is(err, store.ErrUpstream) {
		t.Fatalf("DeleteAccount error = %v, want ErrUpstream", err)
	}
	if strings.Contains(err.Error(), testPassword) {
		t.Fatal("the error carries the password")
	}
	if strings.Contains(err.Error(), errDeleteUnavailable.Error()) || strings.Contains(err.Error(), secretsPath) {
		t.Fatalf("the error leaks the sealer detail: %v", err)
	}
	if _, ok := st.Account(account.ID); !ok {
		t.Fatal("a failed delete removed the account")
	}
	plaintext, err := st.AccountSecret(account.ID)
	if err != nil {
		t.Fatalf("AccountSecret after failed delete: %v", err)
	}
	if string(plaintext) != testPassword {
		t.Fatalf("AccountSecret after failed delete = %q, want the stored password", plaintext)
	}
	if _, ok := sealedEntries(t, secretsPath)[account.ID]; !ok {
		t.Fatal("a failed delete dropped the sealed entry")
	}
}

// TestPersistedStateKeepsLogoutSecret pins the deliberate difference between
// logout and delete: logout ends the session, so the account client still needs
// the password and both persisted copies keep it.
func TestPersistedStateKeepsLogoutSecret(t *testing.T) {
	sealer, secretsPath := newSealer(t)
	st := store.NewWithSealer(sealer)
	account, err := st.CreateAccount(accountInput("logout-keeps", testPassword))
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	loggedOut, err := st.LogoutAccount(account.ID)
	if err != nil {
		t.Fatalf("LogoutAccount: %v", err)
	}
	if loggedOut.State != model.AccountPaused {
		t.Fatalf("state after logout = %q, want %q", loggedOut.State, model.AccountPaused)
	}
	plaintext, err := st.AccountSecret(account.ID)
	if err != nil {
		t.Fatalf("AccountSecret after logout: %v", err)
	}
	if string(plaintext) != testPassword {
		t.Fatal("logout changed the stored password")
	}
	statePath := filepath.Join(t.TempDir(), "state.json")
	if err := st.Save(statePath); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, ok := readSnapshotFile(t, statePath).Secrets[account.ID]; !ok {
		t.Fatal("the state file lost the sealed secret on logout")
	}
	if _, ok := sealedEntries(t, secretsPath)[account.ID]; !ok {
		t.Fatal("the sealer file lost the sealed entry on logout")
	}
	fresh := store.NewWithSealer(sealer)
	if err := fresh.Load(statePath); err != nil {
		t.Fatalf("Load: %v", err)
	}
	plaintext, err = fresh.AccountSecret(account.ID)
	if err != nil || string(plaintext) != testPassword {
		t.Fatalf("AccountSecret after logout and reload = %q, %v", plaintext, err)
	}
}

// TestDeleteAccountSecretNotLeaked checks the audit event and the retry error
// carry no credential value.
func TestDeleteAccountSecretNotLeaked(t *testing.T) {
	sealer, _ := newSealer(t)
	st := store.NewWithSealer(sealer)
	account, err := st.CreateAccount(accountInput("delete-leak", testPassword))
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := st.DeleteAccount(account.ID); err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}
	seen := false
	for _, event := range st.Audit(store.AuditRetention) {
		if event.Action != "account.delete" {
			continue
		}
		seen = true
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("Marshal audit event: %v", err)
		}
		if bytes.Contains(encoded, []byte(testPassword)) {
			t.Fatalf("the delete audit event carries the password: %s", encoded)
		}
		if event.Detail != "0 nodes and 0 leases removed" {
			t.Fatalf("delete audit detail = %q, want counts only", event.Detail)
		}
	}
	if !seen {
		t.Fatal("DeleteAccount recorded no audit event")
	}
	err = st.DeleteAccount(account.ID)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("retried DeleteAccount error = %v, want ErrNotFound", err)
	}
	if strings.Contains(err.Error(), testPassword) {
		t.Fatal("the not-found error carries the password")
	}
}
