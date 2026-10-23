package store_test

import (
	"bytes"
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
