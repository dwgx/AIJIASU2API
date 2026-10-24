package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/store"
)

// testPoolIdentity is a synthetic, complete Linux identity for the migration
// tests. It is not a vendor credential and is never sent anywhere.
var testPoolIdentity = store.DeviceIdentity{
	UID:     "0123456789abcdef0123",
	OSDID:   "0123456789abcdef",
	CUID:    "0123456789abcdef0123",
	CSEID:   "linux",
	OSP:     "linux",
	OSD:     "x86_64",
	OSV:     "Linux version 6.18.33.2",
	DIM:     "lid=REDACTED,8e415e603088426aa42fed420212150c",
	Version: "4.2.3.0",
	Site:    "ajs",
	Lang:    "en",
}

func wireLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestDevicePoolGate pins the DESIGN-DEVICE-POOL §7.6 default: the pool is on
// when AJS_MASTER_KEY is set, AJS_DEVICE_POOL=0 is the kill switch, and any
// other explicit value is refused rather than read as "on".
func TestDevicePoolGate(t *testing.T) {
	t.Setenv("AJS_DEVICE_POOL", "")
	for _, tc := range []struct {
		name string
		key  bool
		want bool
	}{
		{name: "default with key", key: true, want: true},
		{name: "default without key", key: false, want: false},
	} {
		on, err := devicePoolEnabled(tc.key)
		if err != nil {
			t.Fatalf("%s: devicePoolEnabled: %v", tc.name, err)
		}
		if on != tc.want {
			t.Fatalf("%s: devicePoolEnabled = %v, want %v", tc.name, on, tc.want)
		}
	}

	t.Setenv("AJS_DEVICE_POOL", "1")
	if on, err := devicePoolEnabled(true); err != nil || !on {
		t.Fatalf("AJS_DEVICE_POOL=1 with key: (%v, %v), want (true, nil)", on, err)
	}
	if _, err := devicePoolEnabled(false); err == nil || !strings.Contains(err.Error(), "AJS_DEVICE_POOL") {
		t.Fatalf("AJS_DEVICE_POOL=1 without key: err = %v, want an AJS_DEVICE_POOL error", err)
	}

	t.Setenv("AJS_DEVICE_POOL", "0")
	if on, err := devicePoolEnabled(true); err != nil || on {
		t.Fatalf("AJS_DEVICE_POOL=0: (%v, %v), want (false, nil)", on, err)
	}

	t.Setenv("AJS_DEVICE_POOL", "yes")
	if _, err := devicePoolEnabled(true); err == nil || !strings.Contains(err.Error(), "AJS_DEVICE_POOL") {
		t.Fatalf("AJS_DEVICE_POOL=yes: err = %v, want an AJS_DEVICE_POOL error", err)
	}
}

// TestOpenStoreWiresDevicePool proves the daemon builds a store with the device
// sealer active and that account passwords and device material land in two
// distinct sealed files (DESIGN-DEVICE-POOL §5.1).
func TestOpenStoreWiresDevicePool(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AJS_MASTER_KEY", strings.Repeat("ab", 32))
	t.Setenv("AJS_DEVICE_POOL", "")
	t.Setenv("AJS_STATE_DIR", dir)

	st, secrets, deviceSecrets, err := openStore(wireLogger())
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	if secrets == nil {
		t.Fatal("openStore returned no password sealer")
	}
	if deviceSecrets == nil {
		t.Fatal("openStore returned no device sealer")
	}
	defer func() { _ = secrets.Close() }()
	defer func() { _ = deviceSecrets.Close() }()
	if !st.DevicePoolEnabled() {
		t.Fatal("store reports the device pool disabled")
	}

	account, err := st.CreateAccount(model.CreateAccountInput{
		Label: "wire", Username: "vendor-wire", Password: "wire-test-password",
		Platform: model.PlatformLinux, Concurrency: 1,
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	material, err := st.BindForLogin(account.ID, store.DevicePoolDefaultProfile)
	if err != nil {
		t.Fatalf("BindForLogin: %v", err)
	}
	if material.Identity.UID == "" || material.Identity.OSDID == "" {
		t.Fatalf("bound material has no identity: %+v", material.Identity)
	}

	// The two files must stay separate: the password file holds the account
	// id, the device file holds the device id, and neither sees the other.
	passwordIDs := secrets.IDs()
	if len(passwordIDs) != 1 || passwordIDs[0] != account.ID {
		t.Fatalf("password sealer ids = %v, want [%s]", passwordIDs, account.ID)
	}
	deviceIDs := deviceSecrets.IDs()
	records := st.DevicePoolRecords()
	if len(records) != 1 || len(deviceIDs) != 1 || deviceIDs[0] != records[0].ID {
		t.Fatalf("device sealer ids = %v, want the single pool record %v", deviceIDs, records)
	}
	for _, name := range []string{"secrets.json", "device-secrets.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("sealed file %s: %v", name, err)
		}
	}
}

// TestOpenStoreDevicePoolKillSwitch pins AJS_DEVICE_POOL=0: the password sealer
// stays, the device sealer is absent, and pool mutations report the documented
// disabled conflict.
func TestOpenStoreDevicePoolKillSwitch(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AJS_MASTER_KEY", strings.Repeat("cd", 32))
	t.Setenv("AJS_DEVICE_POOL", "0")
	t.Setenv("AJS_STATE_DIR", dir)

	st, secrets, deviceSecrets, err := openStore(wireLogger())
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	if secrets == nil {
		t.Fatal("openStore returned no password sealer")
	}
	defer func() { _ = secrets.Close() }()
	if deviceSecrets != nil {
		t.Fatal("AJS_DEVICE_POOL=0 returned a device sealer")
	}
	if st.DevicePoolEnabled() {
		t.Fatal("AJS_DEVICE_POOL=0 left the device pool enabled")
	}
	if _, err := os.Stat(filepath.Join(dir, "device-secrets.json")); !os.IsNotExist(err) {
		t.Fatalf("device-secrets.json exists with the pool disabled: %v", err)
	}
	if _, err := st.BindForLogin("acc-01", store.DevicePoolDefaultProfile); err == nil ||
		store.DeviceErrorReason(err) != store.DeviceReasonPoolDisabled {
		t.Fatalf("BindForLogin with the pool disabled = %v, want %s", err, store.DeviceReasonPoolDisabled)
	}
}

// TestOpenStoreRejectsPoolWithoutMasterKey pins the honest failure: an explicit
// AJS_DEVICE_POOL=1 has no sealer without the master key.
func TestOpenStoreRejectsPoolWithoutMasterKey(t *testing.T) {
	t.Setenv("AJS_MASTER_KEY", "")
	t.Setenv("AJS_DEVICE_POOL", "1")
	_, _, _, err := openStore(wireLogger())
	if err == nil || !strings.Contains(err.Error(), "AJS_DEVICE_POOL") {
		t.Fatalf("openStore = %v, want an AJS_DEVICE_POOL error", err)
	}
}

// TestStartupMigratesLegacyDevices is the daemon-level migration drill. The
// deployed snapshot can be a version 2 document that still carries the
// plaintext `devices` map (the pre-wiring build wrote that shape), so both
// envelope versions are covered. The assertions are the wiring contract:
// openStore enables the sealer, Load + the startup hook leave a pool record and
// binding, the snapshot no longer carries raw values, and a restart reopens the
// same sealed identity.
func TestStartupMigratesLegacyDevices(t *testing.T) {
	for _, version := range []int{1, 2} {
		t.Run(fmt.Sprintf("version_%d", version), func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("AJS_MASTER_KEY", strings.Repeat("ef", 32))
			t.Setenv("AJS_DEVICE_POOL", "")
			t.Setenv("AJS_STATE_DIR", dir)
			t.Setenv("AJS_STATE_FILE", "")
			path := stateFilePath()

			legacy := store.Snapshot{
				Version: version,
				Accounts: []model.Account{{
					ID: "acc-01", Label: "legacy", Username: "vendor-legacy",
					Platform: model.PlatformLinux, LastLoginAt: "2026-10-20T10:00:00Z",
				}},
				Devices: map[string]store.DeviceIdentity{"acc-01": testPoolIdentity},
			}
			data, err := json.Marshal(legacy)
			if err != nil {
				t.Fatalf("marshal legacy snapshot: %v", err)
			}
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatalf("write legacy snapshot: %v", err)
			}

			st, secrets, deviceSecrets, err := openStore(wireLogger())
			if err != nil {
				t.Fatalf("openStore: %v", err)
			}
			defer func() { _ = secrets.Close() }()
			defer func() { _ = deviceSecrets.Close() }()
			if !st.DevicePoolEnabled() {
				t.Fatal("store reports the device pool disabled")
			}
			if err := st.Load(path); err != nil {
				t.Fatalf("Load: %v", err)
			}
			migrateDevicesAtStartup(st, wireLogger())

			records := st.DevicePoolRecords()
			if len(records) != 1 {
				t.Fatalf("pool records = %d, want 1", len(records))
			}
			rec := records[0]
			if rec.State != store.DeviceAssigned || rec.Algorithm != store.DeviceAlgorithmLegacy {
				t.Fatalf("migrated record = %+v", rec)
			}
			if rec.AssignedAccountID != "acc-01" || rec.PresentedAt == nil {
				t.Fatalf("migrated binding = %+v, want assigned to acc-01 with presentedAt", rec)
			}
			if got, ok := st.AccountDevice("acc-01"); !ok || got != testPoolIdentity {
				t.Fatalf("account device = %+v (present %v), want the legacy identity", got, ok)
			}
			if material, err := st.AccountDeviceMaterial("acc-01"); err != nil || material.Identity != testPoolIdentity {
				t.Fatalf("sealed material = %+v (err %v), want the legacy identity", material.Identity, err)
			}

			// The startup hook is idempotent: a second run changes nothing.
			migrateDevicesAtStartup(st, wireLogger())
			if again := st.DevicePoolRecords(); len(again) != 1 || again[0].ID != rec.ID {
				t.Fatalf("second migration changed the pool: %+v", again)
			}

			// No plaintext identity survives in the snapshot or the sealed
			// file; the snapshot is v2 with a pool index and binding.
			snapshot, err := st.Snapshot()
			if err != nil {
				t.Fatalf("Snapshot: %v", err)
			}
			for _, raw := range []string{testPoolIdentity.UID, testPoolIdentity.OSDID, testPoolIdentity.DIM} {
				if bytes.Contains(snapshot, []byte(raw)) {
					t.Fatalf("snapshot still carries raw device value %q", raw)
				}
			}
			var decoded store.Snapshot
			if err := json.Unmarshal(snapshot, &decoded); err != nil {
				t.Fatalf("decode migrated snapshot: %v", err)
			}
			if decoded.Version != 2 || len(decoded.Devices) != 0 ||
				len(decoded.DevicePool) != 1 || len(decoded.DeviceBindings) != 1 {
				t.Fatalf("migrated snapshot = version %d, devices %d, pool %d, bindings %d",
					decoded.Version, len(decoded.Devices), len(decoded.DevicePool), len(decoded.DeviceBindings))
			}
			sealed, err := os.ReadFile(filepath.Join(dir, "device-secrets.json"))
			if err != nil {
				t.Fatalf("read device-secrets.json: %v", err)
			}
			for _, raw := range []string{testPoolIdentity.UID, testPoolIdentity.OSDID, testPoolIdentity.DIM} {
				if bytes.Contains(sealed, []byte(raw)) {
					t.Fatalf("sealed file carries raw device value %q", raw)
				}
			}

			// Restart drill: the persisted v2 snapshot plus the sealed file
			// reopen the same record and identity.
			if err := st.Save(path); err != nil {
				t.Fatalf("Save: %v", err)
			}
			restarted, restartedSecrets, restartedDeviceSecrets, err := openStore(wireLogger())
			if err != nil {
				t.Fatalf("openStore restart: %v", err)
			}
			defer func() { _ = restartedSecrets.Close() }()
			defer func() { _ = restartedDeviceSecrets.Close() }()
			if err := restarted.Load(path); err != nil {
				t.Fatalf("Load restart: %v", err)
			}
			migrateDevicesAtStartup(restarted, wireLogger())
			after := restarted.DevicePoolRecords()
			if len(after) != 1 || after[0].ID != rec.ID {
				t.Fatalf("restart pool = %+v, want the same record %q", after, rec.ID)
			}
			if got, ok := restarted.AccountDevice("acc-01"); !ok || got != testPoolIdentity {
				t.Fatalf("restart account device = %+v (present %v), want the legacy identity", got, ok)
			}
		})
	}
}
