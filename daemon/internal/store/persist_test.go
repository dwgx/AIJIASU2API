package store_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/store"
)

// persistPassword is a fixed local placeholder, not a credential. It is
// distinctive so a plaintext leak is easy to detect.
const persistPassword = "persist-password-not-a-credential"

func accountIDs(accounts []model.Account) map[string]struct{} {
	ids := make(map[string]struct{}, len(accounts))
	for _, account := range accounts {
		ids[account.ID] = struct{}{}
	}
	return ids
}

func TestSnapshotRoundtrip(t *testing.T) {
	sealer, _ := newSealer(t)
	st := store.NewWithSealer(sealer)

	created, err := st.CreateAccount(accountInput("persist-roundtrip", persistPassword))
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	user, err := st.CreateUser(model.CreateUserInput{
		Name:      "persist-user",
		Platform:  "ajs-persist",
		StickyTTL: "4h",
		Allowlist: []string{"127.0.0.1/32"},
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	key, _, err := st.CreateKey(user.ID, "persist key")
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}
	mutation, err := st.CreateMutation(model.MutationLabInput{
		Method: "GET",
		Target: "persist.example:443",
		NodeID: st.Nodes()[0].ID,
		Body:   "{}",
	})
	if err != nil {
		t.Fatalf("CreateMutation: %v", err)
	}
	transport := model.TransportRawTCP
	healthInterval := 45
	leaseTTL := "12h"
	theme := model.ThemeDark
	if _, err := st.UpdateSettings(model.SettingsPatch{
		Transport:         &transport,
		HealthIntervalSec: &healthInterval,
		LeaseTTL:          &leaseTTL,
		Theme:             &theme,
	}); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}

	path := filepath.Join(t.TempDir(), "state.json")
	if err := st.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	fresh := store.NewWithSealer(sealer)
	if err := fresh.Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got, want := fresh.Accounts(), st.Accounts(); !reflect.DeepEqual(got, want) {
		t.Fatalf("accounts after restore = %#v, want %#v", got, want)
	}
	if got, want := fresh.Nodes(), st.Nodes(); !reflect.DeepEqual(got, want) {
		t.Fatalf("nodes after restore = %#v, want %#v", got, want)
	}
	if got, want := fresh.Leases(), st.Leases(); !reflect.DeepEqual(got, want) {
		t.Fatalf("leases after restore = %#v, want %#v", got, want)
	}
	if got, want := fresh.Users(), st.Users(); !reflect.DeepEqual(got, want) {
		t.Fatalf("users after restore = %#v, want %#v", got, want)
	}
	if got, want := fresh.Keys(), st.Keys(); !reflect.DeepEqual(got, want) {
		t.Fatalf("keys after restore = %#v, want %#v", got, want)
	}
	if got, want := fresh.Requests(), st.Requests(); !reflect.DeepEqual(got, want) {
		t.Fatalf("requests after restore differ (got %d rows, want %d)", len(got), len(want))
	}
	if got, want := fresh.Audit(1000), st.Audit(1000); !reflect.DeepEqual(got, want) {
		t.Fatalf("audit after restore differs (got %d events, want %d)", len(got), len(want))
	}
	if got, want := fresh.Settings(), st.Settings(); got != want {
		t.Fatalf("settings after restore = %#v, want %#v", got, want)
	}
	if got := fresh.Requests()[0].ID; got != mutation.ID {
		t.Fatalf("newest request = %q, want %q", got, mutation.ID)
	}

	// The sealed secret survives the snapshot and still opens with the same
	// sealer and the same account id as additional authenticated data.
	password, err := fresh.AccountSecret(created.ID)
	if err != nil {
		t.Fatalf("AccountSecret after restore: %v", err)
	}
	if string(password) != persistPassword {
		t.Fatalf("AccountSecret after restore = %q, want the created password", password)
	}

	// Only the SHA-256 of the issued key secret is stored, and it survives.
	wantHash, ok := st.SecretHash(key.ID)
	if !ok {
		t.Fatalf("SecretHash(%q) missing before save", key.ID)
	}
	gotHash, ok := fresh.SecretHash(key.ID)
	if !ok {
		t.Fatalf("SecretHash(%q) missing after restore", key.ID)
	}
	if gotHash != wantHash {
		t.Fatalf("SecretHash after restore = %q, want %q", gotHash, wantHash)
	}

	// Restored counters must not hand out an identifier that already exists.
	existing := accountIDs(fresh.Accounts())
	next, err := fresh.CreateAccount(accountInput("persist-roundtrip-next", persistPassword))
	if err != nil {
		t.Fatalf("CreateAccount after restore: %v", err)
	}
	if _, clash := existing[next.ID]; clash {
		t.Fatalf("CreateAccount after restore reused id %q", next.ID)
	}
	nextUser, err := fresh.CreateUser(model.CreateUserInput{Name: "persist-user-next"})
	if err != nil {
		t.Fatalf("CreateUser after restore: %v", err)
	}
	if nextUser.ID == user.ID {
		t.Fatalf("CreateUser after restore reused id %q", nextUser.ID)
	}
	nextKey, _, err := fresh.CreateKey(nextUser.ID, "persist key next")
	if err != nil {
		t.Fatalf("CreateKey after restore: %v", err)
	}
	if nextKey.ID == key.ID {
		t.Fatalf("CreateKey after restore reused id %q", nextKey.ID)
	}
	nextMutation, err := fresh.CreateMutation(model.MutationLabInput{
		Method: "POST",
		Target: "persist.example:8443",
		NodeID: fresh.Nodes()[0].ID,
	})
	if err != nil {
		t.Fatalf("CreateMutation after restore: %v", err)
	}
	if nextMutation.ID == mutation.ID {
		t.Fatalf("CreateMutation after restore reused id %q", nextMutation.ID)
	}
}

func TestSaveFileModeAndNoTempLeftovers(t *testing.T) {
	st := store.New()
	dir := filepath.Join(t.TempDir(), "nested")
	path := filepath.Join(dir, "state.json")
	if err := st.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("state file mode = %04o, want 0600", perm)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, entry := range entries {
		if entry.Name() != "state.json" {
			t.Fatalf("Save left an extra file behind: %s", entry.Name())
		}
	}
}

func TestSaveOverwritesAtomically(t *testing.T) {
	st := store.New()
	path := filepath.Join(t.TempDir(), "state.json")
	if err := st.Save(path); err != nil {
		t.Fatalf("first Save: %v", err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile first: %v", err)
	}
	if _, err := st.CreateAccount(accountInput("persist-overwrite", persistPassword)); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := st.Save(path); err != nil {
		t.Fatalf("second Save: %v", err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile second: %v", err)
	}
	if bytes.Equal(first, second) {
		t.Fatal("second Save did not replace the document")
	}

	fresh := store.New()
	if err := fresh.Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := len(fresh.Accounts()), len(st.Accounts()); got != want {
		t.Fatalf("accounts after reload = %d, want %d", got, want)
	}
	if _, ok := fresh.Account("acc-07"); !ok {
		t.Fatal("created account missing after overwrite")
	}
}

func TestLoadMissingFile(t *testing.T) {
	st := store.New()
	before := st.Accounts()
	err := st.Load(filepath.Join(t.TempDir(), "absent.json"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Load missing file error = %v, want os.ErrNotExist", err)
	}
	if got := st.Accounts(); !reflect.DeepEqual(got, before) {
		t.Fatal("Load of a missing file mutated the store")
	}
}

func TestLoadCorruptFileLeavesStoreUnchanged(t *testing.T) {
	st := store.New()
	before := st.Accounts()
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"version": 1, "accounts": [`), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := st.Load(path); err == nil {
		t.Fatal("Load accepted corrupt JSON")
	}
	if got := st.Accounts(); !reflect.DeepEqual(got, before) {
		t.Fatal("Load of a corrupt file mutated the store")
	}
}

func TestRestoreRejectsWrongVersion(t *testing.T) {
	st := store.New()
	before := st.Accounts()
	for _, data := range [][]byte{
		[]byte(`{"version": 2, "accounts": []}`),
		[]byte(`{"version": 0, "accounts": []}`),
		[]byte(`{"accounts": []}`),
	} {
		err := st.Restore(data)
		if err == nil {
			t.Fatalf("Restore accepted %s", data)
		}
		if !strings.Contains(err.Error(), "version") {
			t.Fatalf("Restore error = %v, want a version error", err)
		}
		if got := st.Accounts(); !reflect.DeepEqual(got, before) {
			t.Fatal("Restore of an unknown version mutated the store")
		}
	}
}

func TestRestoreRejectsEmptyData(t *testing.T) {
	st := store.New()
	before := st.Accounts()
	for _, data := range [][]byte{nil, {}, []byte("  \n\t ")} {
		if err := st.Restore(data); err == nil {
			t.Fatalf("Restore accepted empty data %q", data)
		}
		if got := st.Accounts(); !reflect.DeepEqual(got, before) {
			t.Fatal("Restore of empty data mutated the store")
		}
	}
}

func TestRestoreReplacesEveryCollection(t *testing.T) {
	st := store.New()
	snap := store.Snapshot{
		Version:  store.SnapshotVersion,
		Accounts: []model.Account{{ID: "acc-99", Label: "replacement"}},
	}
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("Marshal snapshot: %v", err)
	}
	if err := st.Restore(data); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got := st.Accounts(); len(got) != 1 || got[0].ID != "acc-99" {
		t.Fatalf("accounts after restore = %#v, want only acc-99", got)
	}
	if got := st.Nodes(); len(got) != 0 {
		t.Fatalf("nodes after restore = %d, want 0", len(got))
	}
	if got := st.Leases(); len(got) != 0 {
		t.Fatalf("leases after restore = %d, want 0", len(got))
	}
	if got := st.Users(); len(got) != 0 {
		t.Fatalf("users after restore = %d, want 0", len(got))
	}
	if got := st.Keys(); len(got) != 0 {
		t.Fatalf("keys after restore = %d, want 0", len(got))
	}
	if got := st.Requests(); len(got) != 0 {
		t.Fatalf("requests after restore = %d, want 0", len(got))
	}
	if got := st.Audit(1000); len(got) != 0 {
		t.Fatalf("audit after restore = %d, want 0", len(got))
	}
	if got := st.Settings(); got != (model.PanelSettings{}) {
		t.Fatalf("settings after restore = %#v, want the zero value", got)
	}
	next, err := st.CreateAccount(accountInput("replacement-next", persistPassword))
	if err != nil {
		t.Fatalf("CreateAccount after restore: %v", err)
	}
	if next.ID != "acc-100" {
		t.Fatalf("new account id = %q, want acc-100", next.ID)
	}
}

func TestSaveWritesNoPlaintextPassword(t *testing.T) {
	sealer, _ := newSealer(t)
	st := store.NewWithSealer(sealer)
	created, err := st.CreateAccount(accountInput("persist-no-plaintext", persistPassword))
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	user, err := st.CreateUser(model.CreateUserInput{Name: "persist-no-plaintext-user"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	key, keySecret, err := st.CreateKey(user.ID, "persist-no-plaintext-key")
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}
	path := filepath.Join(t.TempDir(), "state.json")
	if err := st.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if bytes.Contains(raw, []byte(persistPassword)) {
		t.Fatal("state file contains the plaintext password")
	}
	if bytes.Contains(raw, []byte(keySecret)) {
		t.Fatal("state file contains the plaintext API key secret")
	}
	if bytes.Contains(bytes.ToLower(raw), []byte(`"password"`)) {
		t.Fatal("state file has a password field")
	}

	var snap store.Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("Unmarshal snapshot: %v", err)
	}
	if snap.Version != store.SnapshotVersion {
		t.Fatalf("snapshot version = %d, want %d", snap.Version, store.SnapshotVersion)
	}
	sealed, ok := snap.Secrets[created.ID]
	if !ok {
		t.Fatalf("snapshot has no sealed secret for %q", created.ID)
	}
	if bytes.Contains(sealed, []byte(persistPassword)) {
		t.Fatal("sealed secret contains the plaintext password")
	}
	if snap.KeyHashes[key.ID] == "" {
		t.Fatalf("snapshot has no key hash for %q", key.ID)
	}
}

func TestDeleteThenReloadDoesNotReuseIDs(t *testing.T) {
	sealer, _ := newSealer(t)
	st := store.NewWithSealer(sealer)
	first, err := st.CreateAccount(accountInput("persist-delete", persistPassword))
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := st.DeleteAccount(first.ID); err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}
	path := filepath.Join(t.TempDir(), "state.json")
	if err := st.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	fresh := store.NewWithSealer(sealer)
	if err := fresh.Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := fresh.Account(first.ID); ok {
		t.Fatalf("deleted account %q came back after reload", first.ID)
	}
	if _, err := fresh.AccountSecret(first.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("AccountSecret for the deleted account = %v, want ErrNotFound", err)
	}

	existing := accountIDs(fresh.Accounts())
	next, err := fresh.CreateAccount(accountInput("persist-delete-next", persistPassword))
	if err != nil {
		t.Fatalf("CreateAccount after reload: %v", err)
	}
	if _, clash := existing[next.ID]; clash {
		t.Fatalf("account id %q was reused after delete and reload", next.ID)
	}
	if next.ID == first.ID {
		t.Fatalf("account id %q was reused after delete and reload", next.ID)
	}
}

func TestRestoreRebuildsCountersFromIdentifiers(t *testing.T) {
	st := store.New()
	originalMutation, err := st.CreateMutation(model.MutationLabInput{
		Method: "GET",
		Target: "persist.example:443",
		NodeID: st.Nodes()[0].ID,
	})
	if err != nil {
		t.Fatalf("CreateMutation before snapshot: %v", err)
	}
	data, err := st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	var snap store.Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("Unmarshal snapshot: %v", err)
	}
	// Simulate a document whose counters were lost. The seeded accounts,
	// users, keys, requests and audit events still carry the highest ids.
	snap.AccountSeq, snap.UserSeq, snap.KeySeq = 0, 0, 0
	snap.MutationSeq, snap.AuditSeq = 0, 0
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("Marshal snapshot: %v", err)
	}

	fresh := store.New()
	if err := fresh.Restore(raw); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	existingAudit := make(map[string]struct{})
	for _, event := range fresh.Audit(1000) {
		existingAudit[event.ID] = struct{}{}
	}
	account, err := fresh.CreateAccount(accountInput("persist-counters", persistPassword))
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if account.ID != "acc-07" {
		t.Fatalf("new account id = %q, want acc-07", account.ID)
	}
	user, err := fresh.CreateUser(model.CreateUserInput{Name: "persist-counters-user"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if user.ID != "u-05" {
		t.Fatalf("new user id = %q, want u-05", user.ID)
	}
	key, _, err := fresh.CreateKey(user.ID, "persist counters key")
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}
	if key.ID != "key-07" {
		t.Fatalf("new key id = %q, want key-07", key.ID)
	}
	mutation, err := fresh.CreateMutation(model.MutationLabInput{
		Method: "GET",
		Target: "persist.example:443",
		NodeID: fresh.Nodes()[0].ID,
	})
	if err != nil {
		t.Fatalf("CreateMutation: %v", err)
	}
	if mutation.ID == originalMutation.ID {
		t.Fatalf("mutation id %q was reused after restore", mutation.ID)
	}
	if audit := fresh.Audit(1); len(audit) != 1 {
		t.Fatalf("audit after restore = %#v, want one event", audit)
	} else if _, clash := existingAudit[audit[0].ID]; clash {
		t.Fatalf("audit id %q was reused after restore", audit[0].ID)
	}
}
