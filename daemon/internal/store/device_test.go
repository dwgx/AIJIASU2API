package store_test

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"ajsaccountd/internal/store"
)

// linuxDevice is the identity the daemon stores for the Linux 4.2.3.0 profile:
// a 20-hex uid/cuid, cseid=linux and the captured OS claims.
func linuxDevice(uid string) store.DeviceIdentity {
	return store.DeviceIdentity{
		UID:     uid,
		OSDID:   "0123456789abcdef",
		CUID:    uid,
		CSEID:   "linux",
		OSP:     "linux",
		OSD:     "x86_64",
		OSV:     "Linux version 6.18.33.2",
		DIM:     "lid=REDACTED,8e415e603088426aa42fed420212150c",
		Version: "4.2.3.0",
		Site:    "ajs",
		Lang:    "en",
	}
}

func TestEnsureAccountDeviceOnce(t *testing.T) {
	st := store.New()
	device := linuxDevice("6ada95c900de9bbb3a82")

	if _, ok := st.AccountDevice("acc-01"); ok {
		t.Fatal("seeded store has a device identity")
	}
	stored, err := st.EnsureAccountDevice("acc-01", device)
	if err != nil {
		t.Fatalf("EnsureAccountDevice: %v", err)
	}
	if stored != device {
		t.Fatalf("stored = %+v, want %+v", stored, device)
	}

	// A retry that generated a different identity must not replace the first.
	retry := linuxDevice("ffffffffffffffffffff")
	again, err := st.EnsureAccountDevice("acc-01", retry)
	if err != nil {
		t.Fatalf("second EnsureAccountDevice: %v", err)
	}
	if again != device {
		t.Fatalf("second EnsureAccountDevice = %+v, want the first identity %+v", again, device)
	}
	current, ok := st.AccountDevice("acc-01")
	if !ok || current != device {
		t.Fatalf("AccountDevice = %+v (present %v), want %+v", current, ok, device)
	}

	if _, err := st.EnsureAccountDevice("missing", device); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown account error = %v, want ErrNotFound", err)
	}
	if _, err := st.EnsureAccountDevice("acc-01", store.DeviceIdentity{UID: "only-a-uid"}); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("incomplete device error = %v, want ErrInvalid", err)
	}

	if err := st.DeleteAccount("acc-01"); err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}
	if _, ok := st.AccountDevice("acc-01"); ok {
		t.Fatal("device identity survived DeleteAccount")
	}
}

func TestEnsureAccountDeviceConcurrent(t *testing.T) {
	st := store.New()
	devices := []store.DeviceIdentity{
		linuxDevice("aaaaaaaaaaaaaaaaaaaa"),
		linuxDevice("bbbbbbbbbbbbbbbbbbbb"),
	}
	results := make([]store.DeviceIdentity, len(devices))
	errs := make([]error, len(devices))

	var wg sync.WaitGroup
	for i, device := range devices {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = st.EnsureAccountDevice("acc-02", device)
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("EnsureAccountDevice %d: %v", i, err)
		}
	}
	if results[0] != results[1] {
		t.Fatalf("concurrent first logins stored different identities: %+v and %+v", results[0], results[1])
	}
}

func TestDeviceSnapshotRoundtrip(t *testing.T) {
	st := store.New()
	device := linuxDevice("6ada95c900de9bbb3a82")
	if _, err := st.EnsureAccountDevice("acc-02", device); err != nil {
		t.Fatalf("EnsureAccountDevice: %v", err)
	}

	data, err := st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	var snap store.Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("Unmarshal snapshot: %v", err)
	}
	if got, ok := snap.Devices["acc-02"]; !ok || got != device {
		t.Fatalf("snapshot device = %+v (present %v), want %+v", got, ok, device)
	}

	fresh := store.New()
	if err := fresh.Restore(data); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	got, ok := fresh.AccountDevice("acc-02")
	if !ok || got != device {
		t.Fatalf("restored device = %+v (present %v), want %+v", got, ok, device)
	}

	// A document written before the field existed restores with no devices.
	legacy := store.Snapshot{Version: store.SnapshotVersion}
	legacyData, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("Marshal legacy snapshot: %v", err)
	}
	older := store.New()
	if err := older.Restore(legacyData); err != nil {
		t.Fatalf("Restore legacy snapshot: %v", err)
	}
	if _, ok := older.AccountDevice("acc-02"); ok {
		t.Fatal("legacy snapshot restored a device identity")
	}
}

func TestDeviceSnapshotValidation(t *testing.T) {
	device := linuxDevice("6ada95c900de9bbb3a82")
	cases := map[string]store.Snapshot{
		"unknown account": {
			Version: store.SnapshotVersion,
			Devices: map[string]store.DeviceIdentity{"acc-99": device},
		},
		"incomplete device": {
			Version: store.SnapshotVersion,
			Devices: map[string]store.DeviceIdentity{"acc-01": {UID: device.UID}},
		},
	}
	for name, snap := range cases {
		t.Run(name, func(t *testing.T) {
			st := store.New()
			before := len(st.Accounts())
			data, err := json.Marshal(snap)
			if err != nil {
				t.Fatalf("Marshal snapshot: %v", err)
			}
			if err := st.Restore(data); err == nil {
				t.Fatal("Restore accepted an invalid device record")
			}
			if got := len(st.Accounts()); got != before {
				t.Fatalf("accounts = %d after a rejected restore, want %d", got, before)
			}
			if _, ok := st.AccountDevice("acc-01"); ok {
				t.Fatal("rejected restore stored a device identity")
			}
		})
	}
}
