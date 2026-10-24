package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/secret"
)

// testDevicePassphrase is a fixed local placeholder, not a credential. It is
// long enough to satisfy the 12-byte minimum.
const testDevicePassphrase = "device-pool-passphrase"

const testLinuxProfile = "linux-4.2.3.0-binary"

type poolClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *poolClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *poolClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type poolFixture struct {
	st      *Store
	devices *secret.Store
	dir     string
	clock   *poolClock
}

func newPoolFixture(t *testing.T) *poolFixture {
	t.Helper()
	dir := t.TempDir()
	passwords, err := secret.New(bytes.Repeat([]byte{0x11}, secret.KeySize), filepath.Join(dir, "secrets.json"))
	if err != nil {
		t.Fatalf("password sealer: %v", err)
	}
	devices, err := secret.New(bytes.Repeat([]byte{0x22}, secret.KeySize), filepath.Join(dir, "device-secrets.json"))
	if err != nil {
		t.Fatalf("device sealer: %v", err)
	}
	st := NewEmptyWithSealers(passwords, devices)
	clock := &poolClock{now: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)}
	st.now = clock.Now
	return &poolFixture{st: st, devices: devices, dir: dir, clock: clock}
}

func (f *poolFixture) createAccount(t *testing.T, label, username string) model.Account {
	t.Helper()
	account, err := f.st.CreateAccount(model.CreateAccountInput{
		Label: label, Username: username, Password: "pool-test-password",
		Platform: model.PlatformLinux, Concurrency: 1,
	})
	if err != nil {
		t.Fatalf("CreateAccount(%q): %v", label, err)
	}
	return account
}

func (f *poolFixture) material(t *testing.T, deviceID string) DeviceMaterial {
	t.Helper()
	raw, err := f.devices.OpenID(deviceID)
	if err != nil {
		t.Fatalf("OpenID(%q): %v", deviceID, err)
	}
	var material DeviceMaterial
	if err := json.Unmarshal(raw, &material); err != nil {
		t.Fatalf("decode material %q: %v", deviceID, err)
	}
	return material
}

// reload saves the snapshot and loads it into a fresh store with the same
// sealed files, which is the restart drill.
func (f *poolFixture) reload(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(f.dir, "state.json")
	if err := f.st.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	passwords, err := secret.New(bytes.Repeat([]byte{0x11}, secret.KeySize), filepath.Join(f.dir, "secrets.json"))
	if err != nil {
		t.Fatalf("password sealer reload: %v", err)
	}
	devices, err := secret.New(bytes.Repeat([]byte{0x22}, secret.KeySize), filepath.Join(f.dir, "device-secrets.json"))
	if err != nil {
		t.Fatalf("device sealer reload: %v", err)
	}
	fresh := NewEmptyWithSealers(passwords, devices)
	fresh.now = f.clock.Now
	if err := fresh.Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}
	return fresh
}

func TestDevicePoolGenerateSealsAndPersists(t *testing.T) {
	f := newPoolFixture(t)
	linuxRecords, err := f.st.GenerateDevices(DeviceGenerateInput{
		Count: 2, Profile: testLinuxProfile, LabelPrefix: "linux",
	})
	if err != nil {
		t.Fatalf("GenerateDevices linux: %v", err)
	}
	androidRecords, err := f.st.GenerateDevices(DeviceGenerateInput{
		Count: 1, Profile: "openajs-4.11.10.0-rawtcp", LabelPrefix: "android",
	})
	if err != nil {
		t.Fatalf("GenerateDevices android: %v", err)
	}
	all := append(linuxRecords, androidRecords...)
	if len(all) != 3 {
		t.Fatalf("generated = %d, want 3", len(all))
	}
	for _, rec := range all {
		if rec.State != DeviceAvailable {
			t.Fatalf("new record %q state = %q, want available", rec.ID, rec.State)
		}
		if !deviceIDPattern.MatchString(rec.ID) {
			t.Fatalf("device id %q does not match dev_[0-9a-f]{16}", rec.ID)
		}
		if !isSHA256Hex(rec.Fingerprint) {
			t.Fatalf("fingerprint %q is not sha256:64hex", rec.Fingerprint)
		}
		for _, key := range []string{"uid", "osdid", "cuid", "cseid", "machine"} {
			if !strings.HasPrefix(rec.Hashes[key], "sha256:") {
				t.Fatalf("record %q hash %q = %q, want sha256 prefix", rec.ID, key, rec.Hashes[key])
			}
		}
		if rec.Claims["cseid"] != "" {
			t.Fatalf("record %q leaks cseid as a claim", rec.ID)
		}
		if !f.st.DeviceMaterialPresent(rec.ID) {
			t.Fatalf("record %q has no loaded material", rec.ID)
		}
		material := f.material(t, rec.ID)
		if material.Fingerprint != rec.Fingerprint {
			t.Fatalf("material fingerprint = %q, record = %q", material.Fingerprint, rec.Fingerprint)
		}
		if got := deviceHash(material.Identity.UID); got != rec.Hashes["uid"] {
			t.Fatalf("uid hash = %q, want %q", rec.Hashes["uid"], got)
		}
	}

	data, err := f.st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if SnapshotVersion != 2 {
		t.Fatalf("SnapshotVersion = %d, want 2", SnapshotVersion)
	}
	if snap.Version != SnapshotVersion {
		t.Fatalf("snapshot version = %d, want %d", snap.Version, SnapshotVersion)
	}
	if len(snap.DevicePool) != 3 {
		t.Fatalf("snapshot pool = %d, want 3", len(snap.DevicePool))
	}
	if len(snap.Devices) != 0 {
		t.Fatalf("snapshot carries %d plaintext devices", len(snap.Devices))
	}
	for _, rec := range all {
		material := f.material(t, rec.ID)
		rawValues := []string{
			material.MachineCode, material.Identity.UID, material.Identity.OSDID,
			material.Identity.CUID, material.Identity.DIM,
		}
		// cseid is "linux" for the Linux family: a low-entropy constant that
		// legitimately appears as the platform name. Android cseid equals
		// osdid and is already covered.
		if material.Identity.CSEID != "linux" {
			rawValues = append(rawValues, material.Identity.CSEID)
		}
		for _, raw := range rawValues {
			if raw != "" && bytes.Contains(data, []byte(raw)) {
				t.Fatalf("snapshot leaks raw device value %q", raw)
			}
		}
	}

	fresh := f.reload(t)
	if got := len(fresh.DevicePoolRecords()); got != 3 {
		t.Fatalf("records after reload = %d, want 3", got)
	}
	if !fresh.DeviceMaterialPresent(all[0].ID) {
		t.Fatal("material was not reconciled after reload")
	}
}

func TestDevicePoolHMACVector(t *testing.T) {
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = byte(i)
	}
	generator, ok := DeviceGeneratorByName(DeviceAlgorithmHMAC)
	if !ok {
		t.Fatal("hmac-sha256/v1 is not registered")
	}
	linuxProfile, _ := DeviceProfileByName(testLinuxProfile)
	material, err := generator.Generate(DeviceGenerateSpec{
		Profile: linuxProfile, Index: 0, Seed: seed, DimMode: "derived",
	})
	if err != nil {
		t.Fatalf("hmac generate: %v", err)
	}
	want := map[string]string{
		"machine": "2469a4b3aa09bcce057b4eb268e4c7a9299ebbd49f501ffcc3e93f9a7d54dc56",
		"osdid":   "c21b6396f5dd8c79",
		"uid":     "b44dbcc3a6a4a7a4a875",
		"dim":     "lid=54636fb3f2e4474362c978957a4627ca,08de4ac080e248160462460c4e4a7915",
	}
	if material.MachineCode != want["machine"] {
		t.Fatalf("machine code = %q, want %q", material.MachineCode, want["machine"])
	}
	if material.Identity.OSDID != want["osdid"] || material.Identity.UID != want["uid"] {
		t.Fatalf("identity = %+v, want osdid %q uid %q", material.Identity, want["osdid"], want["uid"])
	}
	if material.Identity.DIM != want["dim"] {
		t.Fatalf("dim = %q, want %q", material.Identity.DIM, want["dim"])
	}

	androidProfile, _ := DeviceProfileByName("openajs-4.11.10.0-rawtcp")
	android, err := generator.Generate(DeviceGenerateSpec{
		Profile: androidProfile, Index: 0, Seed: seed,
	})
	if err != nil {
		t.Fatalf("hmac android generate: %v", err)
	}
	if android.Identity.UID != "96dd0e3cffdbc9657a01de" {
		t.Fatalf("android uid = %q, want the pinned vector", android.Identity.UID)
	}
	if android.Identity.OSDID != want["osdid"] || android.Identity.CUID != want["osdid"] || android.Identity.CSEID != want["osdid"] {
		t.Fatalf("android identity ids = %+v, want osdid on cuid/cseid", android.Identity)
	}
	if android.Identity.DIM != `{"asid":"c21b6396f5dd8c79"}` {
		t.Fatalf("android dim = %q", android.Identity.DIM)
	}
}

func TestDevicePoolRandomGenerationIsUnique(t *testing.T) {
	f := newPoolFixture(t)
	records, err := f.st.GenerateDevices(DeviceGenerateInput{Count: 100, Profile: testLinuxProfile})
	if err != nil {
		t.Fatalf("GenerateDevices: %v", err)
	}
	fingerprints := make(map[string]bool, len(records))
	machines := make(map[string]bool, len(records))
	for _, rec := range records {
		if fingerprints[rec.Fingerprint] {
			t.Fatalf("duplicate fingerprint %q", rec.Fingerprint)
		}
		fingerprints[rec.Fingerprint] = true
		if machines[rec.Hashes["machine"]] {
			t.Fatalf("duplicate machine hash %q", rec.Hashes["machine"])
		}
		machines[rec.Hashes["machine"]] = true
	}
}

func TestDevicePoolAssignReleaseQuarantine(t *testing.T) {
	f := newPoolFixture(t)
	accountA := f.createAccount(t, "alpha", "vendor-alpha")
	accountB := f.createAccount(t, "beta", "vendor-beta")
	records, err := f.st.GenerateDevices(DeviceGenerateInput{Count: 2, Profile: testLinuxProfile})
	if err != nil {
		t.Fatalf("GenerateDevices: %v", err)
	}

	assigned, err := f.st.AssignDevice(records[0].ID, accountA.ID)
	if err != nil {
		t.Fatalf("AssignDevice: %v", err)
	}
	if assigned.State != DeviceAssigned || assigned.AssignedAccountID != accountA.ID || assigned.PresentedAt != nil {
		t.Fatalf("assigned record = %+v", assigned)
	}
	if assigned.AccountKey == "" || !isSHA256Hex(assigned.AccountKey) {
		t.Fatalf("assigned record account key = %q", assigned.AccountKey)
	}
	if material := f.material(t, assigned.ID); material.AccountKey != assigned.AccountKey {
		t.Fatalf("sealed account key = %q, record = %q", material.AccountKey, assigned.AccountKey)
	}
	if _, err := f.st.AssignDevice(records[0].ID, accountB.ID); !errors.Is(err, ErrConflict) ||
		DeviceErrorReason(err) != DeviceReasonStateInvalid {
		t.Fatalf("assign assigned record error = %v, want DEVICE_STATE_INVALID", err)
	}
	if _, err := f.st.AssignDevice(records[1].ID, accountA.ID); !errors.Is(err, ErrConflict) ||
		DeviceErrorReason(err) != DeviceReasonAlreadyBound {
		t.Fatalf("assign second record error = %v, want DEVICE_ALREADY_BOUND", err)
	}

	released, err := f.st.ReleaseDevice(records[0].ID)
	if err != nil {
		t.Fatalf("ReleaseDevice: %v", err)
	}
	if released.State != DeviceAvailable || released.PresentedAt != nil {
		t.Fatalf("release before presentation = %+v, want available", released)
	}

	material, err := f.st.BindForLogin(accountA.ID, testLinuxProfile)
	if err != nil {
		t.Fatalf("BindForLogin: %v", err)
	}
	presented, err := f.st.ReleaseDevice(material.DeviceID)
	if err != nil {
		t.Fatalf("ReleaseDevice after presentation: %v", err)
	}
	if presented.State != DeviceRetired || presented.RetiredReason != "released_after_presentation" {
		t.Fatalf("release after presentation = %+v, want retired", presented)
	}
	if _, err := f.st.AssignDevice(material.DeviceID, accountB.ID); !errors.Is(err, ErrConflict) ||
		DeviceErrorReason(err) != DeviceReasonStateInvalid {
		t.Fatalf("assign presented tombstone to another account = %v, want conflict", err)
	}

	quarantined, err := f.st.QuarantineDevice(records[1].ID, "suspected exposure")
	if err != nil {
		t.Fatalf("QuarantineDevice: %v", err)
	}
	if quarantined.State != DeviceQuarantined {
		t.Fatalf("quarantine state = %q", quarantined.State)
	}
	if _, err := f.st.AssignDevice(records[1].ID, accountB.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("assign quarantined = %v, want conflict", err)
	}
	if _, err := f.st.RetireDevice(records[1].ID, "x"); !errors.Is(err, ErrConflict) {
		t.Fatalf("retire quarantined = %v, want conflict", err)
	}
	if err := f.st.PurgeDevice(records[1].ID); err != nil {
		t.Fatalf("PurgeDevice: %v", err)
	}
	if _, ok := f.st.DeviceRecord(records[1].ID); ok {
		t.Fatal("purged record is still listed")
	}
	if _, err := f.devices.OpenID(records[1].ID); err == nil {
		t.Fatal("purged record still has sealed material")
	}
}

func TestDevicePoolDeleteRetiresAndReAddRestores(t *testing.T) {
	f := newPoolFixture(t)
	account := f.createAccount(t, "alpha", "vendor-alpha")
	first, err := f.st.BindForLogin(account.ID, testLinuxProfile)
	if err != nil {
		t.Fatalf("BindForLogin: %v", err)
	}
	if _, err := f.st.MarkDeviceSeen(account.ID, "vendor-alpha"); err != nil {
		t.Fatalf("MarkDeviceSeen: %v", err)
	}
	before, ok := f.st.DeviceRecord(first.DeviceID)
	if !ok || before.FirstSeenAt == nil || before.LoginCount != 1 {
		t.Fatalf("record before delete = %+v", before)
	}

	if err := f.st.DeleteAccount(account.ID); err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}
	retired, ok := f.st.DeviceRecord(first.DeviceID)
	if !ok {
		t.Fatal("delete erased the device record instead of retiring it")
	}
	if retired.State != DeviceRetired || retired.RetiredReason != "account_deleted" ||
		retired.AssignedAccountID != "" || retired.RetiredAt == nil {
		t.Fatalf("retired record = %+v", retired)
	}
	if !f.st.DeviceMaterialPresent(first.DeviceID) {
		t.Fatal("delete erased the sealed material")
	}
	if _, err := f.devices.OpenID(first.DeviceID); err != nil {
		t.Fatalf("sealed material after delete: %v", err)
	}
	if _, ok := f.st.AccountDevice(account.ID); ok {
		t.Fatal("deleted account still reports a device")
	}

	again := f.createAccount(t, "alpha-2", "vendor-alpha")
	restored, err := f.st.BindForLogin(again.ID, testLinuxProfile)
	if err != nil {
		t.Fatalf("BindForLogin after re-add: %v", err)
	}
	if restored.DeviceID != first.DeviceID {
		t.Fatalf("re-add bound %q, want the retired %q", restored.DeviceID, first.DeviceID)
	}
	if restored.Identity != first.Identity {
		t.Fatalf("restored identity = %+v, want %+v", restored.Identity, first.Identity)
	}
	rec, _ := f.st.DeviceRecord(first.DeviceID)
	if rec.State != DeviceAssigned || rec.AssignedAccountID != again.ID {
		t.Fatalf("restored record = %+v", rec)
	}
	if rec.FirstSeenAt == nil || *rec.FirstSeenAt != *before.FirstSeenAt || rec.LoginCount != 1 {
		t.Fatalf("restore lost first-seen counters: %+v", rec)
	}
	if got := len(f.st.DevicePoolRecords()); got != 1 {
		t.Fatalf("pool after restore = %d records, want 1 (no silent regeneration)", got)
	}

	// Expiry: delete again, advance past 90 days, GC must remove the tombstone
	// and a later re-add must generate a new identity.
	if err := f.st.DeleteAccount(again.ID); err != nil {
		t.Fatalf("second DeleteAccount: %v", err)
	}
	f.clock.Advance(89 * 24 * time.Hour)
	if removed, err := f.st.GCDevices(f.clock.Now(), DeviceRetentionDays); err != nil || removed != 0 {
		t.Fatalf("GC before expiry = %d, %v; want 0", removed, err)
	}
	f.clock.Advance(2 * 24 * time.Hour)
	removed, err := f.st.GCDevices(f.clock.Now(), DeviceRetentionDays)
	if err != nil {
		t.Fatalf("GCDevices: %v", err)
	}
	if removed != 1 {
		t.Fatalf("GC removed %d, want 1", removed)
	}
	if _, ok := f.st.DeviceRecord(first.DeviceID); ok {
		t.Fatal("expired record survived GC")
	}
	if _, err := f.devices.OpenID(first.DeviceID); err == nil {
		t.Fatal("expired material survived GC")
	}
	third := f.createAccount(t, "alpha-3", "vendor-alpha")
	generated, err := f.st.BindForLogin(third.ID, testLinuxProfile)
	if err != nil {
		t.Fatalf("BindForLogin after expiry: %v", err)
	}
	if generated.DeviceID == first.DeviceID || generated.Fingerprint == first.Fingerprint {
		t.Fatal("expired tombstone was resurrected instead of generating a new identity")
	}
}

func TestDevicePoolRestoreAmbiguity(t *testing.T) {
	f := newPoolFixture(t)
	cfg := DefaultDevicePoolConfig()
	cfg.RestoreMode = "manual"
	if err := f.st.ConfigureDevicePool(cfg); err != nil {
		t.Fatalf("ConfigureDevicePool: %v", err)
	}

	firstAccount := f.createAccount(t, "dup-1", "vendor-dup")
	first, err := f.st.BindForLogin(firstAccount.ID, testLinuxProfile)
	if err != nil {
		t.Fatalf("first BindForLogin: %v", err)
	}
	if _, err := f.st.MarkDeviceSeen(firstAccount.ID, "vendor-dup"); err != nil {
		t.Fatalf("MarkDeviceSeen: %v", err)
	}
	if err := f.st.DeleteAccount(firstAccount.ID); err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}

	secondAccount := f.createAccount(t, "dup-2", "vendor-dup")
	second, err := f.st.BindForLogin(secondAccount.ID, testLinuxProfile)
	if err != nil {
		t.Fatalf("second BindForLogin: %v", err)
	}
	if second.DeviceID == first.DeviceID {
		t.Fatal("manual restore mode auto-restored a tombstone")
	}
	if _, err := f.st.MarkDeviceSeen(secondAccount.ID, "vendor-dup"); err != nil {
		t.Fatalf("MarkDeviceSeen: %v", err)
	}
	if err := f.st.DeleteAccount(secondAccount.ID); err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}
	if got := len(f.st.DevicePoolRecords()); got != 2 {
		t.Fatalf("tombstones = %d, want 2", got)
	}

	cfg.RestoreMode = "auto"
	if err := f.st.ConfigureDevicePool(cfg); err != nil {
		t.Fatalf("ConfigureDevicePool auto: %v", err)
	}
	thirdAccount := f.createAccount(t, "dup-3", "vendor-dup")
	if _, err := f.st.BindForLogin(thirdAccount.ID, testLinuxProfile); !errors.Is(err, ErrConflict) ||
		DeviceErrorReason(err) != DeviceReasonRestoreAmbiguous {
		t.Fatalf("ambiguous restore error = %v, want DEVICE_RESTORE_AMBIGUOUS", err)
	}
	if _, ok := f.st.AccountDevice(thirdAccount.ID); ok {
		t.Fatal("ambiguous restore bound a device")
	}
	candidates, err := f.st.RestoreCandidates(thirdAccount.ID, testLinuxProfile)
	if err != nil {
		t.Fatalf("RestoreCandidates: %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("candidates = %d, want 2", len(candidates))
	}

	if _, err := f.st.RestoreDevice(second.DeviceID, thirdAccount.ID); err != nil {
		t.Fatalf("RestoreDevice: %v", err)
	}
	restored, err := f.st.BindForLogin(thirdAccount.ID, testLinuxProfile)
	if err != nil {
		t.Fatalf("BindForLogin after manual restore: %v", err)
	}
	if restored.DeviceID != second.DeviceID {
		t.Fatalf("bind after manual restore = %q, want %q", restored.DeviceID, second.DeviceID)
	}
}

func TestDevicePoolMissingMaterialFailsClosed(t *testing.T) {
	f := newPoolFixture(t)
	account := f.createAccount(t, "alpha", "vendor-alpha")
	material, err := f.st.BindForLogin(account.ID, testLinuxProfile)
	if err != nil {
		t.Fatalf("BindForLogin: %v", err)
	}
	if err := f.devices.Delete(material.DeviceID); err != nil {
		t.Fatalf("delete sealed material: %v", err)
	}
	fresh := f.reload(t)
	if _, err := fresh.AccountDeviceMaterial(account.ID); !errors.Is(err, ErrDeviceMaterialMissing) {
		t.Fatalf("AccountDeviceMaterial = %v, want ErrDeviceMaterialMissing", err)
	}
	if _, ok := fresh.AccountDevice(account.ID); ok {
		t.Fatal("AccountDevice reported a missing identity")
	}
	before := len(fresh.DevicePoolRecords())
	if _, err := fresh.BindForLogin(account.ID, testLinuxProfile); !errors.Is(err, ErrDeviceMaterialMissing) {
		t.Fatalf("BindForLogin = %v, want ErrDeviceMaterialMissing", err)
	}
	if got := len(fresh.DevicePoolRecords()); got != before {
		t.Fatalf("pool grew from %d to %d after a fail-closed login", before, got)
	}
	warnings := fresh.DevicePoolWarnings()
	if len(warnings) == 0 {
		t.Fatal("missing material produced no load warning")
	}
	found := false
	for _, event := range fresh.Audit(100) {
		if event.Action == "device.material_missing" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("missing material was not audited")
	}
}

func TestDevicePoolMigratesV1Snapshot(t *testing.T) {
	identity := DeviceIdentity{
		UID: "aaaaaaaaaaaaaaaaaaaa", OSDID: "0123456789abcdef",
		CUID: "aaaaaaaaaaaaaaaaaaaa", CSEID: "linux",
		OSP: "linux", OSD: "x86_64", OSV: "Linux version 6.18.33.2",
		DIM:     "lid=REDACTED,8e415e603088426aa42fed420212150c",
		Version: "4.2.3.0", Site: "ajs", Lang: "en",
	}
	legacy := Snapshot{
		Version: 1,
		Accounts: []model.Account{{
			ID: "acc-01", Label: "legacy", Username: "vendor-legacy",
			Platform: model.PlatformLinux, LastLoginAt: "2026-10-20T10:00:00Z",
		}},
		Devices: map[string]DeviceIdentity{"acc-01": identity},
	}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("marshal v1 snapshot: %v", err)
	}

	f := newPoolFixture(t)
	if err := f.st.Restore(data); err != nil {
		t.Fatalf("Restore v1: %v", err)
	}
	records := f.st.DevicePoolRecords()
	if len(records) != 1 {
		t.Fatalf("migrated records = %d, want 1", len(records))
	}
	rec := records[0]
	sum := sha256.Sum256([]byte("legacy|acc-01|" + identity.UID))
	wantID := "dev_" + hex.EncodeToString(sum[:])[:16]
	if rec.ID != wantID {
		t.Fatalf("migrated id = %q, want deterministic %q", rec.ID, wantID)
	}
	if rec.State != DeviceAssigned || rec.Algorithm != DeviceAlgorithmLegacy || rec.Profile != testLinuxProfile {
		t.Fatalf("migrated record = %+v", rec)
	}
	if rec.PresentedAt == nil || *rec.PresentedAt != "2026-10-20T10:00:00Z" {
		t.Fatalf("migrated presentedAt = %v, want the account last login", rec.PresentedAt)
	}
	if got, ok := f.st.AccountDevice("acc-01"); !ok || got != identity {
		t.Fatalf("migrated account device = %+v (present %v)", got, ok)
	}
	if material := f.material(t, rec.ID); material.Identity != identity {
		t.Fatalf("sealed identity = %+v, want %+v", material.Identity, identity)
	}

	snapshot, err := f.st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if bytes.Contains(snapshot, []byte(identity.UID)) ||
		bytes.Contains(snapshot, []byte(identity.OSDID)) ||
		bytes.Contains(snapshot, []byte(identity.DIM)) {
		t.Fatal("migrated snapshot still carries raw device values")
	}
	var snap Snapshot
	if err := json.Unmarshal(snapshot, &snap); err != nil {
		t.Fatalf("decode migrated snapshot: %v", err)
	}
	if snap.Version != 2 || len(snap.Devices) != 0 || len(snap.DeviceBindings) != 1 {
		t.Fatalf("migrated snapshot = version %d, devices %d, bindings %d", snap.Version, len(snap.Devices), len(snap.DeviceBindings))
	}

	// Determinism: the same v1 document migrates to the same id and
	// fingerprint in a fresh store.
	other := newPoolFixture(t)
	if err := other.st.Restore(data); err != nil {
		t.Fatalf("second Restore v1: %v", err)
	}
	otherRecords := other.st.DevicePoolRecords()
	if len(otherRecords) != 1 || otherRecords[0].ID != rec.ID || otherRecords[0].Fingerprint != rec.Fingerprint {
		t.Fatalf("migration is not deterministic: %+v vs %+v", otherRecords, rec)
	}
	if err := f.st.MigrateDevices(); err != nil {
		t.Fatalf("MigrateDevices after migration: %v", err)
	}
}

func TestDevicePoolPersistsBindingAndMaterial(t *testing.T) {
	f := newPoolFixture(t)
	account := f.createAccount(t, "alpha", "vendor-alpha")
	material, err := f.st.BindForLogin(account.ID, testLinuxProfile)
	if err != nil {
		t.Fatalf("BindForLogin: %v", err)
	}
	if _, err := f.st.MarkDeviceSeen(account.ID, "vendor-alpha"); err != nil {
		t.Fatalf("MarkDeviceSeen: %v", err)
	}
	fresh := f.reload(t)
	rec, ok := fresh.DeviceRecord(material.DeviceID)
	if !ok || rec.State != DeviceAssigned || rec.AssignedAccountID != account.ID || rec.LoginCount != 1 {
		t.Fatalf("record after reload = %+v (present %v)", rec, ok)
	}
	restored, err := fresh.AccountDeviceMaterial(account.ID)
	if err != nil {
		t.Fatalf("AccountDeviceMaterial after reload: %v", err)
	}
	if restored.Identity != material.Identity || restored.MachineCode != material.MachineCode {
		t.Fatalf("material after reload = %+v, want %+v", restored, material)
	}
	identity, ok := fresh.AccountDevice(account.ID)
	if !ok || identity != material.Identity {
		t.Fatalf("AccountDevice after reload = %+v (present %v)", identity, ok)
	}
	data, err := fresh.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if snap.DeviceBindings[account.ID] != material.DeviceID {
		t.Fatalf("snapshot binding = %q, want %q", snap.DeviceBindings[account.ID], material.DeviceID)
	}
	for _, event := range fresh.Audit(100) {
		for _, raw := range []string{material.Identity.UID, material.Identity.OSDID, material.Identity.DIM, material.MachineCode} {
			if raw != "" && (strings.Contains(event.Detail, raw) || strings.Contains(event.Target, raw)) {
				t.Fatalf("audit event leaks a raw device value: %+v", event)
			}
		}
	}
}

func TestDevicePoolExportImportRoundtrip(t *testing.T) {
	source := newPoolFixture(t)
	alpha := source.createAccount(t, "alpha", "vendor-alpha")
	beta := source.createAccount(t, "beta", "vendor-beta")
	alphaMaterial, err := source.st.BindForLogin(alpha.ID, testLinuxProfile)
	if err != nil {
		t.Fatalf("alpha BindForLogin: %v", err)
	}
	betaMaterial, err := source.st.BindForLogin(beta.ID, testLinuxProfile)
	if err != nil {
		t.Fatalf("beta BindForLogin: %v", err)
	}
	if _, err := source.st.MarkDeviceSeen(alpha.ID, "vendor-alpha"); err != nil {
		t.Fatalf("MarkDeviceSeen: %v", err)
	}
	if err := source.st.DeleteAccount(beta.ID); err != nil {
		t.Fatalf("DeleteAccount beta: %v", err)
	}
	available, err := source.st.GenerateDevices(DeviceGenerateInput{Count: 1, Profile: testLinuxProfile})
	if err != nil {
		t.Fatalf("GenerateDevices available: %v", err)
	}

	envelope, err := source.st.ExportDevicePool(DeviceExportOptions{
		Scope: "all", Passphrase: testDevicePassphrase,
	})
	if err != nil {
		t.Fatalf("ExportDevicePool: %v", err)
	}
	if envelope.DeviceCount != 3 || envelope.Format != DeviceFormat || envelope.Version != DeviceFormatVersion {
		t.Fatalf("envelope = %+v", envelope)
	}
	rawEnvelope, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	for _, raw := range []string{alphaMaterial.Identity.UID, betaMaterial.Identity.UID, alphaMaterial.MachineCode} {
		if raw != "" && bytes.Contains(rawEnvelope, []byte(raw)) {
			t.Fatalf("envelope leaks raw device value %q", raw)
		}
	}

	target := newPoolFixture(t)
	targetAlpha := target.createAccount(t, "alpha", "vendor-alpha")
	targetBeta := target.createAccount(t, "beta", "vendor-beta")
	dry, err := target.st.ImportDevicePool(rawEnvelope, DeviceImportOptions{
		Passphrase: testDevicePassphrase, DryRun: true, Bind: true,
	})
	if err != nil {
		t.Fatalf("dry-run import: %v", err)
	}
	if !dry.DryRun || dry.Added != 3 || dry.Bound != 2 {
		t.Fatalf("dry-run result = %+v", dry)
	}
	if got := len(target.st.DevicePoolRecords()); got != 0 {
		t.Fatalf("dry-run mutated the pool: %d records", got)
	}

	result, err := target.st.ImportDevicePool(rawEnvelope, DeviceImportOptions{
		Passphrase: testDevicePassphrase,
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.Added != 3 || result.Skipped != 0 || result.Bound != 0 {
		t.Fatalf("import result = %+v", result)
	}
	if _, err := target.st.AccountDeviceMaterial(targetAlpha.ID); !errors.Is(err, ErrDeviceMaterialMissing) {
		t.Fatalf("unbound imported account material = %v, want ErrDeviceMaterialMissing", err)
	}

	bound, err := target.st.ImportDevicePool(rawEnvelope, DeviceImportOptions{
		Passphrase: testDevicePassphrase, Bind: true,
	})
	if err != nil {
		t.Fatalf("bind re-import: %v", err)
	}
	if bound.Added != 0 || bound.Skipped != 3 || bound.Bound != 2 {
		t.Fatalf("bind re-import = %+v, want added 0 skipped 3 bound 2", bound)
	}
	alphaRestored, err := target.st.AccountDeviceMaterial(targetAlpha.ID)
	if err != nil {
		t.Fatalf("target alpha material: %v", err)
	}
	if alphaRestored.DeviceID != alphaMaterial.DeviceID || alphaRestored.Identity != alphaMaterial.Identity ||
		alphaRestored.MachineCode != alphaMaterial.MachineCode {
		t.Fatalf("alpha roundtrip mismatch: %+v vs %+v", alphaRestored, alphaMaterial)
	}
	betaRestored, err := target.st.AccountDeviceMaterial(targetBeta.ID)
	if err != nil {
		t.Fatalf("target beta material: %v", err)
	}
	if betaRestored.DeviceID != betaMaterial.DeviceID || betaRestored.Identity != betaMaterial.Identity {
		t.Fatalf("beta roundtrip mismatch: %+v vs %+v", betaRestored, betaMaterial)
	}
	importedAvailable, ok := target.st.DeviceRecord(available[0].ID)
	if !ok || importedAvailable.State != DeviceAvailable || importedAvailable.PresentedAt != nil {
		t.Fatalf("available record after import = %+v (present %v)", importedAvailable, ok)
	}

	again, err := target.st.ImportDevicePool(rawEnvelope, DeviceImportOptions{
		Passphrase: testDevicePassphrase, Bind: true,
	})
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if again.Added != 0 || again.Skipped != 3 || again.Bound != 0 {
		t.Fatalf("idempotent import = %+v, want added 0 skipped 3 bound 0", again)
	}
	if len(again.BindingConflicts) != 2 {
		t.Fatalf("binding conflicts = %+v, want two record_assigned", again.BindingConflicts)
	}
	if got := len(target.st.DevicePoolRecords()); got != 3 {
		t.Fatalf("pool after second import = %d, want 3", got)
	}

	if _, err := target.st.ImportDevicePool(rawEnvelope, DeviceImportOptions{
		Passphrase: "wrong-passphrase-1",
	}); !errors.Is(err, ErrInvalid) || DeviceErrorReason(err) != DeviceReasonImportDecrypt {
		t.Fatalf("wrong passphrase error = %v, want DEVICE_IMPORT_DECRYPT", err)
	}

	var tampered DeviceExportEnvelope
	if err := json.Unmarshal(rawEnvelope, &tampered); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	ciphertext, err := base64.StdEncoding.DecodeString(tampered.Ciphertext)
	if err != nil {
		t.Fatalf("decode ciphertext: %v", err)
	}
	ciphertext[0] ^= 0xff
	tampered.Ciphertext = base64.StdEncoding.EncodeToString(ciphertext)
	tamperedRaw, err := json.Marshal(tampered)
	if err != nil {
		t.Fatalf("marshal tampered envelope: %v", err)
	}
	if _, err := target.st.ImportDevicePool(tamperedRaw, DeviceImportOptions{
		Passphrase: testDevicePassphrase,
	}); !errors.Is(err, ErrInvalid) || DeviceErrorReason(err) != DeviceReasonImportDecrypt {
		t.Fatalf("tampered envelope error = %v, want DEVICE_IMPORT_DECRYPT", err)
	}

	// Fingerprint conflict: the same id with a different fingerprint aborts the
	// whole import and mutates nothing.
	target.st.mu.Lock()
	target.st.devicePool[alphaMaterial.DeviceID].Fingerprint = "sha256:" + strings.Repeat("a", 64)
	target.st.mu.Unlock()
	before := len(target.st.DevicePoolRecords())
	_, err = target.st.ImportDevicePool(rawEnvelope, DeviceImportOptions{Passphrase: testDevicePassphrase})
	if !errors.Is(err, ErrConflict) || DeviceErrorReason(err) != DeviceReasonImportConflict {
		t.Fatalf("conflict import error = %v, want DEVICE_IMPORT_CONFLICT", err)
	}
	if got := len(target.st.DevicePoolRecords()); got != before {
		t.Fatalf("conflicting import mutated the pool: %d -> %d", before, got)
	}
}

func TestDevicePoolConcurrentBind(t *testing.T) {
	f := newPoolFixture(t)
	account := f.createAccount(t, "alpha", "vendor-alpha")
	const logins = 50
	results := make([]DeviceMaterial, logins)
	errs := make([]error, logins)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = f.st.BindForLogin(account.ID, testLinuxProfile)
		}()
	}
	wg.Wait()
	for i := range results {
		if errs[i] != nil {
			t.Fatalf("login %d: %v", i, errs[i])
		}
		if results[i].DeviceID != results[0].DeviceID || results[i].Identity != results[0].Identity {
			t.Fatalf("concurrent login %d got a different identity", i)
		}
	}
	if got := len(f.st.DevicePoolRecords()); got != 1 {
		t.Fatalf("concurrent first logins created %d records, want 1", got)
	}
}

func TestDevicePoolValidationAndLimits(t *testing.T) {
	f := newPoolFixture(t)
	if _, err := f.st.GenerateDevices(DeviceGenerateInput{
		Count: 1, Platform: model.PlatformAndroid, Profile: testLinuxProfile,
	}); !errors.Is(err, ErrInvalid) || DeviceErrorReason(err) != DeviceReasonProfileMismatch {
		t.Fatalf("platform mismatch error = %v, want DEVICE_PROFILE_MISMATCH", err)
	}
	if _, err := f.st.GenerateDevices(DeviceGenerateInput{
		Count: 1, Profile: testLinuxProfile, Algorithm: "bogus/v9",
	}); !errors.Is(err, ErrInvalid) || DeviceErrorReason(err) != DeviceReasonAlgorithmUnknown {
		t.Fatalf("unknown algorithm error = %v, want DEVICE_ALGORITHM_UNKNOWN", err)
	}
	if _, err := f.st.GenerateDevices(DeviceGenerateInput{
		Count: 1, Profile: testLinuxProfile, Algorithm: DeviceAlgorithmHMAC, Seed: []byte("short"),
	}); !errors.Is(err, ErrInvalid) || DeviceErrorReason(err) != DeviceReasonSeedInvalid {
		t.Fatalf("short seed error = %v, want DEVICE_SEED_INVALID", err)
	}

	cfg := DefaultDevicePoolConfig()
	cfg.Max = 2
	if err := f.st.ConfigureDevicePool(cfg); err != nil {
		t.Fatalf("ConfigureDevicePool: %v", err)
	}
	if _, err := f.st.GenerateDevices(DeviceGenerateInput{Count: 3, Profile: testLinuxProfile}); !errors.Is(err, ErrConflict) ||
		DeviceErrorReason(err) != DeviceReasonPoolFull {
		t.Fatalf("pool full error = %v, want DEVICE_POOL_FULL", err)
	}
	if _, err := f.st.GenerateDevices(DeviceGenerateInput{Count: 2, Profile: testLinuxProfile}); err != nil {
		t.Fatalf("GenerateDevices at cap: %v", err)
	}
	if _, err := f.st.GenerateDevices(DeviceGenerateInput{Count: 1, Profile: testLinuxProfile}); !errors.Is(err, ErrConflict) {
		t.Fatalf("GenerateDevices past cap = %v, want conflict", err)
	}

	// A repeated deterministic batch is a duplicate, not a second identity.
	other := newPoolFixture(t)
	cfg = DefaultDevicePoolConfig()
	cfg.Algorithm = DeviceAlgorithmHMAC
	cfg.Seed = bytes.Repeat([]byte{0x42}, 32)
	if err := other.st.ConfigureDevicePool(cfg); err != nil {
		t.Fatalf("ConfigureDevicePool hmac: %v", err)
	}
	if _, err := other.st.GenerateDevices(DeviceGenerateInput{Count: 1, Profile: testLinuxProfile}); err != nil {
		t.Fatalf("first deterministic generate: %v", err)
	}
	if _, err := other.st.GenerateDevices(DeviceGenerateInput{Count: 1, Profile: testLinuxProfile}); !errors.Is(err, ErrConflict) ||
		DeviceErrorReason(err) != DeviceReasonDuplicate {
		t.Fatalf("duplicate deterministic generate = %v, want DEVICE_DUPLICATE", err)
	}
}

func TestDevicePoolPreallocatedStrict(t *testing.T) {
	f := newPoolFixture(t)
	cfg := DefaultDevicePoolConfig()
	cfg.Mode = "preallocated"
	cfg.Strict = true
	if err := f.st.ConfigureDevicePool(cfg); err != nil {
		t.Fatalf("ConfigureDevicePool: %v", err)
	}
	records, err := f.st.GenerateDevices(DeviceGenerateInput{Count: 1, Profile: testLinuxProfile})
	if err != nil {
		t.Fatalf("GenerateDevices: %v", err)
	}
	first := f.createAccount(t, "alpha", "vendor-alpha")
	material, err := f.st.BindForLogin(first.ID, testLinuxProfile)
	if err != nil {
		t.Fatalf("BindForLogin preallocated: %v", err)
	}
	if material.DeviceID != records[0].ID {
		t.Fatalf("preallocated login used %q, want %q", material.DeviceID, records[0].ID)
	}
	second := f.createAccount(t, "beta", "vendor-beta")
	if _, err := f.st.BindForLogin(second.ID, testLinuxProfile); !errors.Is(err, ErrConflict) ||
		DeviceErrorReason(err) != DeviceReasonPoolEmpty {
		t.Fatalf("strict empty pool error = %v, want DEVICE_POOL_EMPTY", err)
	}
}

func TestDevicePoolDisabled(t *testing.T) {
	st := NewEmpty()
	if st.DevicePoolEnabled() {
		t.Fatal("NewEmpty enabled the device pool")
	}
	if _, err := st.GenerateDevices(DeviceGenerateInput{Count: 1, Profile: testLinuxProfile}); !errors.Is(err, ErrConflict) ||
		DeviceErrorReason(err) != DeviceReasonPoolDisabled {
		t.Fatalf("disabled generate error = %v, want DEVICE_POOL_DISABLED", err)
	}
	view := st.ListDevicePool(DevicePoolQuery{})
	if view.Enabled || len(view.Devices) != 0 {
		t.Fatalf("disabled view = %+v", view)
	}
}

func TestDevicePoolOrphanMaterialRecovered(t *testing.T) {
	f := newPoolFixture(t)
	profile, _ := DeviceProfileByName(testLinuxProfile)
	generator, _ := DeviceGeneratorByName(DeviceAlgorithmRandom)
	material, err := generator.Generate(DeviceGenerateSpec{Profile: profile})
	if err != nil {
		t.Fatalf("generate material: %v", err)
	}
	material.DeviceID = "dev_0123456789abcdef"
	material.CreatedAt = iso(f.clock.Now())
	// Seal without inserting an index row: the crash window after the seal and
	// before the snapshot write.
	if err := f.st.sealDeviceMaterial(material); err != nil {
		t.Fatalf("seal orphan material: %v", err)
	}

	fresh := f.reload(t)
	rec, ok := fresh.DeviceRecord(material.DeviceID)
	if !ok {
		t.Fatal("orphan sealed material was not recovered")
	}
	if rec.State != DeviceRetired || rec.RetiredReason != "recovered" || rec.AssignedAccountID != "" {
		t.Fatalf("recovered record = %+v", rec)
	}
	if rec.Fingerprint != material.Fingerprint || rec.PresentedAt == nil {
		t.Fatalf("recovered record lost identity metadata: %+v", rec)
	}
	if !fresh.DeviceMaterialPresent(material.DeviceID) {
		t.Fatal("recovered material is not loaded")
	}
	found := false
	for _, event := range fresh.Audit(100) {
		if event.Action == "device.recovered" && event.Target == material.DeviceID {
			found = true
		}
	}
	if !found {
		t.Fatal("orphan recovery was not audited")
	}
}

func TestDevicePoolDropsMaterialLessTombstone(t *testing.T) {
	f := newPoolFixture(t)
	account := f.createAccount(t, "alpha", "vendor-alpha")
	material, err := f.st.BindForLogin(account.ID, testLinuxProfile)
	if err != nil {
		t.Fatalf("BindForLogin: %v", err)
	}
	if err := f.st.DeleteAccount(account.ID); err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}
	if err := f.devices.Delete(material.DeviceID); err != nil {
		t.Fatalf("delete material: %v", err)
	}
	fresh := f.reload(t)
	if _, ok := fresh.DeviceRecord(material.DeviceID); ok {
		t.Fatal("retired record without material survived the load")
	}
	warnings := strings.Join(fresh.DevicePoolWarnings(), " ")
	if !strings.Contains(warnings, "dropped") {
		t.Fatalf("warnings = %q, want a dropped-record warning", warnings)
	}
}

func TestDevicePoolListAndUpdate(t *testing.T) {
	f := newPoolFixture(t)
	account := f.createAccount(t, "alpha", "vendor-alpha")
	records, err := f.st.GenerateDevices(DeviceGenerateInput{Count: 3, Profile: testLinuxProfile, LabelPrefix: "linux"})
	if err != nil {
		t.Fatalf("GenerateDevices: %v", err)
	}
	if _, err := f.st.AssignDevice(records[0].ID, account.ID); err != nil {
		t.Fatalf("AssignDevice: %v", err)
	}
	view := f.st.ListDevicePool(DevicePoolQuery{State: DeviceAssigned})
	if view.Total != 3 || view.Counts[string(DeviceAssigned)] != 1 || view.Counts[string(DeviceAvailable)] != 2 {
		t.Fatalf("view = %+v", view)
	}
	if len(view.Devices) != 1 || view.Devices[0].ID != records[0].ID {
		t.Fatalf("assigned page = %+v", view.Devices)
	}
	label := "renamed"
	notes := "operator note"
	updated, err := f.st.UpdateDevice(records[1].ID, &label, &notes)
	if err != nil {
		t.Fatalf("UpdateDevice: %v", err)
	}
	if updated.Label != label || updated.Notes != notes {
		t.Fatalf("updated record = %+v", updated)
	}
	view = f.st.ListDevicePool(DevicePoolQuery{Query: "renamed"})
	if view.Total != 3 || len(view.Devices) != 1 || view.Devices[0].ID != records[1].ID {
		t.Fatalf("query page = %+v", view)
	}
}
