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

func accountIOStore(t *testing.T) (*store.Store, *secret.Store, string) {
	t.Helper()
	sealer, path := newSealer(t)
	return store.NewEmptyWithSealer(sealer), sealer, path
}

func createIOAccount(t *testing.T, st *store.Store, label, username, password string) model.Account {
	t.Helper()
	account, err := st.CreateAccount(model.CreateAccountInput{
		Label:       label,
		Username:    username,
		Password:    password,
		Platform:    model.PlatformLinux,
		Concurrency: 2,
		Regions:     []string{"cn-zj", "hk"},
	})
	if err != nil {
		t.Fatalf("CreateAccount(%q): %v", label, err)
	}
	return account
}

func exportRow(label string, password *string) store.AccountExportRow {
	return store.AccountExportRow{
		Label:       label,
		Username:    "user-" + label,
		Password:    password,
		Platform:    model.PlatformLinux,
		Concurrency: 1,
		Regions:     []string{},
		State:       model.AccountPaused,
	}
}

func TestAccountIOExportAccountsPasswordAndDevice(t *testing.T) {
	st, _, _ := accountIOStore(t)
	account := createIOAccount(t, st, "northwind", "vendor-northwind", testPassword)
	device := linuxDevice("6ada95c900de9bbb3a82")
	if _, err := st.EnsureAccountDevice(account.ID, device); err != nil {
		t.Fatalf("EnsureAccountDevice: %v", err)
	}

	rows, err := st.ExportAccounts(store.ExportQuery{IDs: []string{account.ID}})
	if err != nil {
		t.Fatalf("ExportAccounts: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if !rows[0].HasPassword || string(rows[0].Password) != testPassword {
		t.Fatalf("password not exported: has=%v value=%q", rows[0].HasPassword, rows[0].Password)
	}
	if rows[0].Device == nil || rows[0].Device.UID != device.UID {
		t.Fatalf("device = %+v, want %+v", rows[0].Device, device)
	}
}

func TestAccountIOExportAccountsMissingSecret(t *testing.T) {
	st := store.NewEmpty()
	account := createIOAccount(t, st, "plain", "vendor-plain", testPassword)
	rows, err := st.ExportAccounts(store.ExportQuery{IDs: []string{account.ID}})
	if err != nil {
		t.Fatalf("ExportAccounts: %v", err)
	}
	if rows[0].HasPassword {
		t.Fatal("store without a sealer reported a password")
	}
}

type brokenOpenSealer struct{}

func (brokenOpenSealer) Seal(accountID string, plaintext []byte) ([]byte, error) {
	return []byte("sealed-" + accountID), nil
}
func (brokenOpenSealer) Open(accountID string, sealed []byte) ([]byte, error) {
	return nil, errors.New("cannot open")
}
func (brokenOpenSealer) Delete(accountID string) error { return nil }

func TestAccountIOExportAccountsCorruptSecret(t *testing.T) {
	st := store.NewEmptyWithSealer(brokenOpenSealer{})
	account := createIOAccount(t, st, "corrupt", "vendor-corrupt", testPassword)
	_, err := st.ExportAccounts(store.ExportQuery{IDs: []string{account.ID}})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("ExportAccounts error = %v, want ErrConflict", err)
	}
}

func TestAccountIOExportAccountsFilterAndUnknownID(t *testing.T) {
	st, _, _ := accountIOStore(t)
	createIOAccount(t, st, "Alpha", "vendor-alpha", testPassword)
	createIOAccount(t, st, "Beta", "vendor-beta", testPassword)

	rows, err := st.ExportAccounts(store.ExportQuery{Q: "alph"})
	if err != nil {
		t.Fatalf("filtered ExportAccounts: %v", err)
	}
	if len(rows) != 1 || rows[0].Account.Label != "Alpha" {
		t.Fatalf("filtered rows = %+v, want Alpha", rows)
	}
	if _, err := st.ExportAccounts(store.ExportQuery{IDs: []string{"acc-99"}}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown id error = %v, want ErrNotFound", err)
	}
	if _, err := st.ExportAccounts(store.ExportQuery{State: "bogus"}); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("bad state error = %v, want ErrInvalid", err)
	}
}

func TestAccountIOSealOpenExportRoundTrip(t *testing.T) {
	password := testPassword
	doc := store.AccountExport{
		Format:     store.AccountExportFormat,
		Version:    1,
		ExportedAt: "2026-10-24T09:00:00Z",
		Counts:     store.AccountExportCounts{Accounts: 1, Passwords: 1, Devices: 1},
		Accounts: []store.AccountExportRow{{
			ID:          "acc-07",
			Label:       "northwind",
			Username:    "vendor-northwind",
			Password:    &password,
			Platform:    model.PlatformLinux,
			Concurrency: 1,
			Regions:     []string{"cn-zj"},
			State:       model.AccountPaused,
			ExpiresAt:   "2026-11-22T16:35:33Z",
			Device:      &store.DeviceIdentity{UID: "uid", OSDID: "osdid", CUID: "cuid", CSEID: "linux"},
		}},
	}
	passphrase := "correct horse battery staple"
	envelope, err := store.SealExport(doc, passphrase, bytes.NewReader(make([]byte, 28)))
	if err != nil {
		t.Fatalf("SealExport: %v", err)
	}
	if envelope.Format != store.EncryptedExportFormat || envelope.Version != 1 {
		t.Fatalf("envelope header = %+v", envelope)
	}
	if envelope.KDF.Iterations != store.ExportPBKDF2Iterations {
		t.Fatalf("iterations = %d", envelope.KDF.Iterations)
	}
	opened, err := store.OpenExport(envelope, passphrase)
	if err != nil {
		t.Fatalf("OpenExport: %v", err)
	}
	if len(opened.Accounts) != 1 || opened.Accounts[0].Label != "northwind" {
		t.Fatalf("opened = %+v", opened)
	}
	if opened.Accounts[0].Password == nil || *opened.Accounts[0].Password != testPassword {
		t.Fatalf("password did not round-trip")
	}
	if opened.Accounts[0].Device == nil || opened.Accounts[0].Device.UID != "uid" {
		t.Fatalf("device did not round-trip")
	}
}

func TestAccountIOOpenExportRejectsWrongPassphraseAndTamper(t *testing.T) {
	password := testPassword
	doc := store.AccountExport{
		Format:   store.AccountExportFormat,
		Version:  1,
		Accounts: []store.AccountExportRow{exportRow("northwind", &password)},
	}
	envelope, err := store.SealExport(doc, "correct horse battery staple", bytes.NewReader(make([]byte, 28)))
	if err != nil {
		t.Fatalf("SealExport: %v", err)
	}
	if _, err := store.OpenExport(envelope, "wrong passphrase value"); err == nil {
		t.Fatal("wrong passphrase opened the envelope")
	}

	tampered := envelope
	tampered.KDF.Iterations++
	if _, err := store.OpenExport(tampered, "correct horse battery staple"); err == nil {
		t.Fatal("tampered iterations opened the envelope")
	}
	tampered = envelope
	tampered.KDF.Salt = "AQEBAQEBAQEBAQEBAQEBAQ=="
	if _, err := store.OpenExport(tampered, "correct horse battery staple"); err == nil {
		t.Fatal("tampered salt opened the envelope")
	}
	tampered = envelope
	ciphertext, _ := json.Marshal(tampered.Ciphertext)
	tampered.Ciphertext = string(ciphertext) + "x"
	if _, err := store.OpenExport(tampered, "correct horse battery staple"); err == nil {
		t.Fatal("tampered ciphertext opened the envelope")
	}
	tampered = envelope
	tampered.KDF.Iterations = store.MaxPBKDF2Iterations + 1
	if _, err := store.OpenExport(tampered, "correct horse battery staple"); err == nil {
		t.Fatal("out-of-range iterations opened the envelope")
	}
}

func TestAccountIOBuildCSVExactAndParse(t *testing.T) {
	password := `p"a,ss`
	doc := store.AccountExport{
		Format:  store.AccountExportFormat,
		Version: 1,
		Accounts: []store.AccountExportRow{{
			Label:       "Northwind Ops",
			Username:    "user,one",
			Password:    &password,
			Platform:    model.PlatformLinux,
			Concurrency: 2,
			Regions:     []string{"cn-zj", "hk"},
			State:       model.AccountPaused,
			ExpiresAt:   "2026-11-22T16:35:33Z",
		}},
	}
	raw, err := store.BuildExportCSV(doc)
	if err != nil {
		t.Fatalf("BuildExportCSV: %v", err)
	}
	text := string(raw)
	if !strings.HasPrefix(text, "label,username,password,platform,concurrency,regions,state,expiresAt\n") {
		t.Fatalf("header = %q", strings.SplitN(text, "\n", 2)[0])
	}
	if !strings.Contains(text, `"user,one"`) || !strings.Contains(text, `"p""a,ss"`) {
		t.Fatalf("CSV did not quote correctly:\n%s", text)
	}
	parsed, warnings, err := store.ParseExportCSV(raw)
	if err != nil {
		t.Fatalf("ParseExportCSV: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v", warnings)
	}
	if len(parsed.Accounts) != 1 || parsed.Accounts[0].Username != "user,one" {
		t.Fatalf("parsed = %+v", parsed.Accounts)
	}
	if parsed.Accounts[0].Password == nil || *parsed.Accounts[0].Password != password {
		t.Fatalf("password did not round-trip")
	}
	if len(parsed.Accounts[0].Regions) != 2 {
		t.Fatalf("regions = %v", parsed.Accounts[0].Regions)
	}
}

func TestAccountIOBuildCSVRejectsRegionSeparator(t *testing.T) {
	doc := store.AccountExport{
		Format:  store.AccountExportFormat,
		Version: 1,
		Accounts: []store.AccountExportRow{{
			Label:    "northwind",
			Username: "user",
			Platform: model.PlatformLinux,
			Regions:  []string{"cn|zj"},
			State:    model.AccountPaused,
		}},
	}
	if _, err := store.BuildExportCSV(doc); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("BuildExportCSV error = %v, want ErrConflict", err)
	}
}

func TestAccountIOParseCSVWarningsAndRowError(t *testing.T) {
	raw := "\ufefflabel,username,password,platform,extra\n" +
		"northwind,user,secret,linux,ignored\n" +
		"broken,row\n"
	doc, warnings, err := store.ParseExportCSV([]byte(raw))
	if err != nil {
		t.Fatalf("ParseExportCSV: %v", err)
	}
	if len(warnings) != 1 || warnings[0] != store.WarningUnknownColumn {
		t.Fatalf("warnings = %v", warnings)
	}
	if len(doc.Accounts) != 2 {
		t.Fatalf("rows = %d", len(doc.Accounts))
	}
	_, results, _ := store.PlanImport(doc, store.AccountImportOptions{}.Normalized(), nil, true)
	if len(results) != 2 || results[1].Action != "fail" || *results[1].Code != store.CodeInvalidArgument {
		t.Fatalf("results = %+v", results)
	}
}

func TestAccountIOParseJSONVersionAndRowCount(t *testing.T) {
	valid := `{"format":"ajs-accounts","version":1,"accounts":[{"label":"a","username":"u","password":"p","platform":"linux"}]}`
	if _, err := store.ParseExportJSON([]byte(valid)); err != nil {
		t.Fatalf("valid document: %v", err)
	}
	badVersion := strings.Replace(valid, `"version":1`, `"version":2`, 1)
	if _, err := store.ParseExportJSON([]byte(badVersion)); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("bad version error = %v", err)
	}
	empty := `{"format":"ajs-accounts","version":1,"accounts":[]}`
	if _, err := store.ParseExportJSON([]byte(empty)); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("empty accounts error = %v", err)
	}
}

func TestAccountIOPlanMergeMatrix(t *testing.T) {
	st, _, _ := accountIOStore(t)
	first := createIOAccount(t, st, "Northwind Ops", "vendor-northwind", testPassword)
	createIOAccount(t, st, "Field Lab", "vendor-field", testPassword)
	existing := st.Accounts()
	password := testPassword

	doc := store.AccountExport{
		Format:  store.AccountExportFormat,
		Version: 1,
		Accounts: []store.AccountExportRow{
			exportRow("Northwind Ops", &password),
			exportRow("Field Lab", nil),
			exportRow("Northwind Ops", &password),
		},
	}
	opts := store.AccountImportOptions{OnConflict: "update"}.Normalized()
	_, results, _ := store.PlanImport(doc, opts, existing, true)
	if results[0].Action != "update" || results[0].ExistingID == nil || *results[0].ExistingID != first.ID {
		t.Fatalf("row 1 = %+v, want update %s", results[0], first.ID)
	}
	if results[1].Action != "update" || len(results[1].Warnings) != 1 || results[1].Warnings[0] != store.WarningPasswordKept {
		t.Fatalf("row 2 = %+v, want update with password_kept", results[1])
	}
	if results[2].Action != "fail" || *results[2].Code != store.CodeDuplicateRow {
		t.Fatalf("row 3 = %+v, want DUPLICATE_ROW", results[2])
	}
	renameDoc := store.AccountExport{
		Format:   store.AccountExportFormat,
		Version:  1,
		Accounts: []store.AccountExportRow{exportRow("Northwind Ops", &password)},
	}
	_, renamed, _ := store.PlanImport(renameDoc, store.AccountImportOptions{OnConflict: "rename"}.Normalized(), existing, true)
	if renamed[0].Action != "rename" || renamed[0].RenamedTo == nil || *renamed[0].RenamedTo != "Northwind Ops (imported)" {
		t.Fatalf("rename result = %+v", renamed[0])
	}
	_, skipped, _ := store.PlanImport(renameDoc, store.AccountImportOptions{OnConflict: "skip"}.Normalized(), existing, true)
	if skipped[0].Action != "skip" {
		t.Fatalf("skip result = %+v", skipped[0])
	}
	_, failed, _ := store.PlanImport(renameDoc, store.AccountImportOptions{OnConflict: "fail"}.Normalized(), existing, true)
	if failed[0].Action != "fail" || *failed[0].Code != store.CodeConflict {
		t.Fatalf("fail result = %+v", failed[0])
	}
}

func TestAccountIOPlanUsernamePlatformAmbiguous(t *testing.T) {
	st, _, _ := accountIOStore(t)
	createIOAccount(t, st, "One", "shared-user", testPassword)
	createIOAccount(t, st, "Two", "shared-user", testPassword)
	password := testPassword
	row := exportRow("Three", &password)
	row.Username = "shared-user"
	doc := store.AccountExport{Format: store.AccountExportFormat, Version: 1, Accounts: []store.AccountExportRow{row}}
	_, results, _ := store.PlanImport(doc, store.AccountImportOptions{MatchBy: "username_platform"}.Normalized(), st.Accounts(), true)
	if results[0].Action != "fail" || *results[0].Code != store.CodeConflict {
		t.Fatalf("result = %+v, want CONFLICT", results[0])
	}
}

func TestAccountIOPlanIDMatchRequiresID(t *testing.T) {
	password := testPassword
	doc := store.AccountExport{Format: store.AccountExportFormat, Version: 1, Accounts: []store.AccountExportRow{exportRow("No ID", &password)}}
	_, results, _ := store.PlanImport(doc, store.AccountImportOptions{MatchBy: "id"}.Normalized(), nil, true)
	if results[0].Action != "fail" || *results[0].Code != store.CodeInvalidArgument {
		t.Fatalf("result = %+v", results[0])
	}
}

func TestAccountIOPlanDeviceReplaceGuard(t *testing.T) {
	st, _, _ := accountIOStore(t)
	account := createIOAccount(t, st, "Live", "vendor-live", testPassword)
	if _, err := st.UpdateAccount(account.ID, model.AccountPatch{State: new(model.AccountPaused)}); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if _, err := st.PutNode(model.ProxyNode{
		ID: "node-live", AccountID: account.ID, Label: "node-live", Region: "hk",
		State: model.NodeLive, Capacity: 1,
	}); err != nil {
		t.Fatalf("PutNode: %v", err)
	}
	password := testPassword
	row := exportRow("Live", &password)
	row.Device = &store.DeviceIdentity{UID: "uid", OSDID: "osdid", CUID: "cuid", CSEID: "linux"}
	doc := store.AccountExport{Format: store.AccountExportFormat, Version: 1, Accounts: []store.AccountExportRow{row}}
	_, results, _ := store.PlanImport(doc, store.AccountImportOptions{OnConflict: "update", DevicePolicy: "replace"}.Normalized(), st.Accounts(), true)
	if results[0].Action != "fail" || *results[0].Code != store.CodeDeviceInUse {
		t.Fatalf("result = %+v, want DEVICE_IN_USE", results[0])
	}
}

func TestAccountIOPlanRenameCollision(t *testing.T) {
	st, _, _ := accountIOStore(t)
	createIOAccount(t, st, "Northwind Ops", "vendor-one", testPassword)
	createIOAccount(t, st, "Northwind Ops (imported)", "vendor-two", testPassword)
	password := testPassword
	doc := store.AccountExport{Format: store.AccountExportFormat, Version: 1, Accounts: []store.AccountExportRow{exportRow("Northwind Ops", &password)}}
	_, results, _ := store.PlanImport(doc, store.AccountImportOptions{OnConflict: "rename"}.Normalized(), st.Accounts(), true)
	if results[0].RenamedTo == nil || *results[0].RenamedTo != "Northwind Ops (imported 2)" {
		t.Fatalf("renamedTo = %v", results[0].RenamedTo)
	}
}

func TestAccountIOImportRoundTripRestoresPasswordAndDevice(t *testing.T) {
	st, sealer, _ := accountIOStore(t)
	account := createIOAccount(t, st, "northwind", "vendor-northwind", testPassword)
	device := linuxDevice("6ada95c900de9bbb3a82")
	if _, err := st.EnsureAccountDevice(account.ID, device); err != nil {
		t.Fatalf("EnsureAccountDevice: %v", err)
	}
	rows, err := st.ExportAccounts(store.ExportQuery{IDs: []string{account.ID}})
	if err != nil {
		t.Fatalf("ExportAccounts: %v", err)
	}
	doc := store.BuildExport(rows, store.ExportOptions{IncludeDevice: true, SecretsSealed: true})
	if err := st.DeleteAccount(account.ID); err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}
	if ids := sealer.IDs(); len(ids) != 0 {
		t.Fatalf("secrets after delete = %v", ids)
	}
	plan, results, _ := store.PlanImport(doc, store.AccountImportOptions{}.Normalized(), st.Accounts(), true)
	if len(results) != 1 || results[0].Action != "create" {
		t.Fatalf("results = %+v", results)
	}
	applied, err := st.ImportAccounts(plan)
	if err != nil {
		t.Fatalf("ImportAccounts: %v", err)
	}
	if len(applied.Created) != 1 {
		t.Fatalf("created = %d", len(applied.Created))
	}
	restored := applied.Created[0]
	if restored.Label != account.Label || restored.Username != account.Username || restored.ExpiresAt != account.ExpiresAt {
		t.Fatalf("restored = %+v, want %+v", restored, account)
	}
	password, err := st.AccountSecret(restored.ID)
	if err != nil {
		t.Fatalf("AccountSecret: %v", err)
	}
	if string(password) != testPassword {
		t.Fatalf("password = %q", password)
	}
	storedDevice, ok := st.AccountDevice(restored.ID)
	if !ok || storedDevice != device {
		t.Fatalf("device = %+v, want %+v", storedDevice, device)
	}
	if restored.State != model.AccountPaused {
		t.Fatalf("state = %q, want paused", restored.State)
	}
}

func TestAccountIOImportSealsUnderLocalKey(t *testing.T) {
	sourceSealer, _ := newSealer(t)
	source := store.NewEmptyWithSealer(sourceSealer)
	account := createIOAccount(t, source, "northwind", "vendor-northwind", testPassword)
	rows, err := source.ExportAccounts(store.ExportQuery{IDs: []string{account.ID}})
	if err != nil {
		t.Fatalf("ExportAccounts: %v", err)
	}
	doc := store.BuildExport(rows, store.ExportOptions{SecretsSealed: true})

	otherKey := []byte("abcdef0123456789abcdef0123456789")
	targetSealer, err := secret.New(otherKey, filepath.Join(t.TempDir(), "secrets.json"))
	if err != nil {
		t.Fatalf("secret.New: %v", err)
	}
	target := store.NewEmptyWithSealer(targetSealer)
	plan, _, _ := store.PlanImport(doc, store.AccountImportOptions{}.Normalized(), nil, true)
	applied, err := target.ImportAccounts(plan)
	if err != nil {
		t.Fatalf("ImportAccounts: %v", err)
	}
	password, err := target.AccountSecret(applied.Created[0].ID)
	if err != nil {
		t.Fatalf("AccountSecret: %v", err)
	}
	if string(password) != testPassword {
		t.Fatalf("password = %q", password)
	}
	if ids := targetSealer.IDs(); len(ids) != 1 {
		t.Fatalf("target sealed ids = %v", ids)
	}
}

func TestAccountIOImportAtomicRollback(t *testing.T) {
	st, sealer, _ := accountIOStore(t)
	account := createIOAccount(t, st, "existing", "vendor-existing", testPassword)
	beforeAccounts := len(st.Accounts())
	beforeAudit := len(st.Audit(100))
	badState := model.AccountState("bogus")
	plan := store.ImportPlan{
		Format: "json",
		Creates: []store.ImportCreate{{
			Label: "new", Username: "vendor-new", Password: []byte(testPassword),
			Platform: model.PlatformLinux, Concurrency: 1, State: model.AccountPaused,
		}},
		Updates: []store.ImportUpdate{{
			ID:    account.ID,
			Patch: model.AccountPatch{State: &badState},
		}},
	}
	if _, err := st.ImportAccounts(plan); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("ImportAccounts error = %v, want ErrInvalid", err)
	}
	if len(st.Accounts()) != beforeAccounts {
		t.Fatalf("accounts after rollback = %d, want %d", len(st.Accounts()), beforeAccounts)
	}
	if _, ok := st.Account("acc-02"); ok {
		t.Fatal("created account survived rollback")
	}
	if len(st.Audit(100)) != beforeAudit {
		t.Fatalf("audit after rollback = %d, want %d", len(st.Audit(100)), beforeAudit)
	}
	ids := sealer.IDs()
	if len(ids) != 1 || ids[0] != account.ID {
		t.Fatalf("sealed ids after rollback = %v, want [%s]", ids, account.ID)
	}
	if password, err := st.AccountSecret(account.ID); err != nil || string(password) != testPassword {
		t.Fatalf("original secret after rollback = %q, %v", password, err)
	}
}

func TestAccountIOImportPreservesIDsAndBumpsSequence(t *testing.T) {
	st, _, _ := accountIOStore(t)
	plan := store.ImportPlan{
		Format: "json",
		Creates: []store.ImportCreate{{
			ID: "acc-99", Label: "restored", Username: "vendor-restored", Password: []byte(testPassword),
			Platform: model.PlatformLinux, Concurrency: 1, State: model.AccountPaused,
		}},
	}
	applied, err := st.ImportAccounts(plan)
	if err != nil {
		t.Fatalf("ImportAccounts: %v", err)
	}
	if applied.Created[0].ID != "acc-99" {
		t.Fatalf("id = %q, want acc-99", applied.Created[0].ID)
	}
	next := createIOAccount(t, st, "next", "vendor-next", testPassword)
	if next.ID != "acc-100" {
		t.Fatalf("next id = %q, want acc-100", next.ID)
	}
}

func TestAccountIOImportNoSealerWithPassword(t *testing.T) {
	st := store.NewEmpty()
	password := testPassword
	doc := store.AccountExport{Format: store.AccountExportFormat, Version: 1, Accounts: []store.AccountExportRow{exportRow("new", &password)}}
	_, results, _ := store.PlanImport(doc, store.AccountImportOptions{}.Normalized(), nil, false)
	if results[0].Action != "fail" || *results[0].Code != store.CodeSecretStorageOff {
		t.Fatalf("result = %+v, want SECRET_STORAGE_DISABLED", results[0])
	}
	plan := store.ImportPlan{Creates: []store.ImportCreate{{
		Label: "new", Username: "vendor-new", Password: []byte(password),
		Platform: model.PlatformLinux, Concurrency: 1, State: model.AccountPaused,
	}}}
	if _, err := st.ImportAccounts(plan); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("ImportAccounts error = %v, want ErrInvalid", err)
	}
	if len(st.Accounts()) != 0 {
		t.Fatalf("accounts = %d, want 0", len(st.Accounts()))
	}
}

func TestAccountIOSecretBulkOperations(t *testing.T) {
	sealer, path := newSealer(t)
	if _, err := sealer.Seal("acc-01", []byte("first-password")); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := sealer.Seal("acc-02", []byte("second-password")); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	sealed, err := sealer.SealMany(map[string][]byte{
		"acc-03": []byte("third-password"),
		"acc-04": []byte("fourth-password"),
	})
	if err != nil {
		t.Fatalf("SealMany: %v", err)
	}
	if len(sealed) != 2 {
		t.Fatalf("SealMany returned %d envelopes", len(sealed))
	}
	for id, want := range map[string]string{
		"acc-03": "third-password",
		"acc-04": "fourth-password",
	} {
		got, err := sealer.Open(id, sealed[id])
		if err != nil || string(got) != want {
			t.Fatalf("Open(%s) = %q, %v", id, got, err)
		}
	}
	if ids := sealer.IDs(); len(ids) != 4 {
		t.Fatalf("IDs = %v", ids)
	}
	previousThird, err := sealer.Seal("acc-03", []byte("first-password"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	previousFourth, err := sealer.Seal("acc-04", []byte("second-password"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if err := sealer.RestoreMany(map[string][]byte{"acc-03": previousThird, "acc-04": previousFourth}); err != nil {
		t.Fatalf("RestoreMany: %v", err)
	}
	if got, err := sealer.Open("acc-03", previousThird); err != nil || string(got) != "first-password" {
		t.Fatalf("restored acc-03 = %q, %v", got, err)
	}
	if err := sealer.RemoveMany([]string{"acc-03", "acc-04", "missing"}); err != nil {
		t.Fatalf("RemoveMany: %v", err)
	}
	if ids := sealer.IDs(); len(ids) != 2 || ids[0] != "acc-01" || ids[1] != "acc-02" {
		t.Fatalf("IDs after remove = %v", ids)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if strings.Contains(string(raw), "acc-03") || strings.Contains(string(raw), "acc-04") {
		t.Fatal("removed ids remain in secrets.json")
	}
}

func TestAccountIOImportAuditSummary(t *testing.T) {
	st, _, _ := accountIOStore(t)
	password := testPassword
	doc := store.AccountExport{Format: store.AccountExportFormat, Version: 1, Accounts: []store.AccountExportRow{
		exportRow("one", &password),
		exportRow("two", &password),
	}}
	plan, _, _ := store.PlanImport(doc, store.AccountImportOptions{}.Normalized(), nil, true)
	if _, err := st.ImportAccounts(plan); err != nil {
		t.Fatalf("ImportAccounts: %v", err)
	}
	events := st.Audit(10)
	actions := make(map[string]int)
	for _, event := range events {
		actions[event.Action]++
	}
	if actions["account.import"] != 1 || actions["account.create"] != 2 {
		t.Fatalf("audit actions = %v", actions)
	}
	for _, event := range events {
		if strings.Contains(event.Detail, testPassword) {
			t.Fatalf("audit leaked a password: %+v", event)
		}
	}
}

func TestAccountIOImportRowsOrder(t *testing.T) {
	// The CSV parser must preserve file order; the result row number is the
	// 1-based data row, not the array index after any skipped blank lines.
	raw := "label,username,password,platform\none,u1,p1,linux\n\ntwo,u2,p2,linux\n"
	doc, _, err := store.ParseExportCSV([]byte(raw))
	if err != nil {
		t.Fatalf("ParseExportCSV: %v", err)
	}
	if len(doc.Accounts) != 2 {
		t.Fatalf("rows = %d, want 2", len(doc.Accounts))
	}
	_, results, _ := store.PlanImport(doc, store.AccountImportOptions{}.Normalized(), nil, true)
	if results[0].Row != 1 || results[1].Row != 2 {
		t.Fatalf("row numbers = %d, %d", results[0].Row, results[1].Row)
	}
}

func TestAccountIOCSVHeaderDuplicateAndMissing(t *testing.T) {
	if _, _, err := store.ParseExportCSV([]byte("label,label,username,password,platform\n")); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("duplicate header error = %v", err)
	}
	if _, _, err := store.ParseExportCSV([]byte("label,username,platform\n")); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("missing password error = %v", err)
	}
}

func TestAccountIOImportRollbackRestoresPreviousSealed(t *testing.T) {
	st, sealer, _ := accountIOStore(t)
	first := createIOAccount(t, st, "one", "vendor-one", testPassword)
	second := createIOAccount(t, st, "two", "vendor-two", testPassword)
	badState := model.AccountState("bogus")
	plan := store.ImportPlan{
		Format: "json",
		Mode:   "atomic",
		Updates: []store.ImportUpdate{
			{ID: first.ID, Password: []byte("replacement-password")},
			{ID: second.ID, Patch: model.AccountPatch{State: &badState}},
		},
	}
	if _, err := st.ImportAccounts(plan); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("ImportAccounts error = %v, want ErrInvalid", err)
	}
	password, err := st.AccountSecret(first.ID)
	if err != nil {
		t.Fatalf("AccountSecret: %v", err)
	}
	if string(password) != testPassword {
		t.Fatalf("password after rollback = %q, want the previous value", password)
	}
	if ids := sealer.IDs(); len(ids) != 2 {
		t.Fatalf("sealed ids after rollback = %v", ids)
	}
}

func TestAccountIOImportDeviceReplace(t *testing.T) {
	st, _, _ := accountIOStore(t)
	account := createIOAccount(t, st, "device", "vendor-device", testPassword)
	original := linuxDevice("6ada95c900de9bbb3a82")
	if _, err := st.EnsureAccountDevice(account.ID, original); err != nil {
		t.Fatalf("EnsureAccountDevice: %v", err)
	}
	if _, err := st.UpdateAccount(account.ID, model.AccountPatch{State: new(model.AccountPaused)}); err != nil {
		t.Fatalf("pause: %v", err)
	}
	replacement := linuxDevice("ffffffffffffffffffff")
	plan := store.ImportPlan{
		Format: "json",
		Updates: []store.ImportUpdate{{
			ID:            account.ID,
			Device:        &replacement,
			ReplaceDevice: true,
		}},
	}
	if _, err := st.ImportAccounts(plan); err != nil {
		t.Fatalf("ImportAccounts: %v", err)
	}
	stored, ok := st.AccountDevice(account.ID)
	if !ok || stored != replacement {
		t.Fatalf("device = %+v, want replacement", stored)
	}

	// A later import without ReplaceDevice keeps the stored identity.
	ignored := linuxDevice("aaaaaaaaaaaaaaaaaaaa")
	plan = store.ImportPlan{
		Format:  "json",
		Updates: []store.ImportUpdate{{ID: account.ID, Device: &ignored}},
	}
	if _, err := st.ImportAccounts(plan); err != nil {
		t.Fatalf("second ImportAccounts: %v", err)
	}
	stored, ok = st.AccountDevice(account.ID)
	if !ok || stored != replacement {
		t.Fatalf("device after keep = %+v, want the replacement", stored)
	}
}

func TestAccountIOSealManyPersistFailureRollsBack(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.json")
	sealer, err := secret.New(testKey(), path)
	if err != nil {
		t.Fatalf("secret.New: %v", err)
	}
	t.Cleanup(func() { _ = sealer.Close() })
	original, err := sealer.Seal("acc-01", []byte("old-password"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	// Replace the state directory with a regular file so the next rewrite
	// cannot create its temporary file.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	if err := os.WriteFile(dir, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := sealer.SealMany(map[string][]byte{
		"acc-01": []byte("new-password"),
		"acc-02": []byte("second-password"),
	}); err == nil {
		t.Fatal("SealMany succeeded with an unusable state directory")
	}
	password, err := sealer.Open("acc-01", original)
	if err != nil || string(password) != "old-password" {
		t.Fatalf("Open after failed SealMany = %q, %v", password, err)
	}
	if ids := sealer.IDs(); len(ids) != 1 || ids[0] != "acc-01" {
		t.Fatalf("ids after failed SealMany = %v", ids)
	}
}

func TestAccountIORemoveManyPersistFailureRollsBack(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.json")
	sealer, err := secret.New(testKey(), path)
	if err != nil {
		t.Fatalf("secret.New: %v", err)
	}
	t.Cleanup(func() { _ = sealer.Close() })
	first, err := sealer.Seal("acc-01", []byte("first-password"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	second, err := sealer.Seal("acc-02", []byte("second-password"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	if err := os.WriteFile(dir, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := sealer.RemoveMany([]string{"acc-01", "acc-02"}); err == nil {
		t.Fatal("RemoveMany succeeded with an unusable state directory")
	}
	if ids := sealer.IDs(); len(ids) != 2 {
		t.Fatalf("ids after failed RemoveMany = %v", ids)
	}
	if password, err := sealer.Open("acc-01", first); err != nil || string(password) != "first-password" {
		t.Fatalf("Open acc-01 after failed RemoveMany = %q, %v", password, err)
	}
	if password, err := sealer.Open("acc-02", second); err != nil || string(password) != "second-password" {
		t.Fatalf("Open acc-02 after failed RemoveMany = %q, %v", password, err)
	}
}
