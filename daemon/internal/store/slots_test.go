package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/secret"
)

// slotModelTestDevice is the Linux 4.2.3.0 identity shape used by the device-pool
// tests. It is not a credential.
func slotModelTestDevice() DeviceIdentity {
	return DeviceIdentity{
		UID:     "6ada95c900de9bbb3a82",
		OSDID:   "0123456789abcdef",
		CUID:    "6ada95c900de9bbb3a82",
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

func TestSlotKeyValidationAndFormat(t *testing.T) {
	valid := []model.SlotKey{
		{AccountID: "acc-01", Index: 1},
		{AccountID: "acc-01", Index: 8},
	}
	for _, key := range valid {
		if !key.Valid() {
			t.Fatalf("SlotKey %+v should be valid", key)
		}
	}
	invalid := []model.SlotKey{
		{AccountID: "", Index: 1},
		{AccountID: "   ", Index: 1},
		{AccountID: "acc-01", Index: 0},
		{AccountID: "acc-01", Index: 9},
		{AccountID: "acc-01", Index: -1},
	}
	for _, key := range invalid {
		if key.Valid() {
			t.Fatalf("SlotKey %+v should be invalid", key)
		}
	}

	if got := (model.SlotKey{AccountID: "acc-01", Index: 2}).String(); got != "acc-01#2" {
		t.Fatalf("SlotKey.String() = %q, want acc-01#2", got)
	}
	key, err := model.ParseSlotKey("acc-01#8")
	if err != nil {
		t.Fatalf("ParseSlotKey: %v", err)
	}
	if key != (model.SlotKey{AccountID: "acc-01", Index: 8}) {
		t.Fatalf("ParseSlotKey = %+v", key)
	}
	for _, raw := range []string{"", "acc-01", "#1", "acc-01#", "acc-01#0", "acc-01#9", "acc-01#x"} {
		if _, err := model.ParseSlotKey(raw); err == nil {
			t.Fatalf("ParseSlotKey(%q) accepted an invalid key", raw)
		}
	}
}

func TestSlotStateValidation(t *testing.T) {
	for _, state := range []model.SlotState{
		model.SlotProvisioning, model.SlotReady, model.SlotLive,
		model.SlotDown, model.SlotQuarantined,
	} {
		if !state.Valid() {
			t.Fatalf("SlotState %q should be valid", state)
		}
	}
	for _, state := range []model.SlotState{"", "empty", "disabled", "paused", "LIVE"} {
		if state.Valid() {
			t.Fatalf("SlotState %q should be invalid", state)
		}
	}
}

func TestAccountSlotValidate(t *testing.T) {
	valid := model.AccountSlot{
		SlotKey:    model.SlotKey{AccountID: "acc-01", Index: 1},
		DeviceID:   "dev-1",
		ServerID:   "srv-1",
		GatePort:   18081,
		State:      model.SlotProvisioning,
		Generation: 1,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid slot: %v", err)
	}
	cases := []struct {
		name string
		slot model.AccountSlot
	}{
		{name: "zero index", slot: model.AccountSlot{SlotKey: model.SlotKey{AccountID: "acc-01", Index: 0}, State: model.SlotProvisioning}},
		{name: "index nine", slot: model.AccountSlot{SlotKey: model.SlotKey{AccountID: "acc-01", Index: 9}, State: model.SlotProvisioning}},
		{name: "unknown state", slot: model.AccountSlot{SlotKey: model.SlotKey{AccountID: "acc-01", Index: 1}, State: "paused"}},
		{name: "negative port", slot: model.AccountSlot{SlotKey: model.SlotKey{AccountID: "acc-01", Index: 1}, GatePort: -1, State: model.SlotProvisioning}},
		{name: "oversized port", slot: model.AccountSlot{SlotKey: model.SlotKey{AccountID: "acc-01", Index: 1}, GatePort: 70000, State: model.SlotProvisioning}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.slot.Validate(); err == nil {
				t.Fatalf("slot %+v passed validation", tc.slot)
			}
		})
	}
}

func TestSlotCapacity(t *testing.T) {
	cases := []struct {
		name       string
		useCount   int
		accountCap int
		want       int
	}{
		{name: "absent inputs mean one", useCount: 0, accountCap: 0, want: 1},
		{name: "operator cap wins", useCount: 8, accountCap: 2, want: 2},
		{name: "use count wins", useCount: 1, accountCap: 8, want: 1},
		{name: "eight is allowed", useCount: 8, accountCap: 8, want: 8},
		{name: "service cap wins over 2000", useCount: 2000, accountCap: 2000, want: 8},
		{name: "above vendor maximum", useCount: 100000, accountCap: 100000, want: 8},
		{name: "negative values normalize", useCount: -3, accountCap: -2, want: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SlotCapacity(tc.useCount, tc.accountCap); got != tc.want {
				t.Fatalf("SlotCapacity(%d, %d) = %d, want %d", tc.useCount, tc.accountCap, got, tc.want)
			}
			if got := slotCapacity(tc.useCount, tc.accountCap); got != tc.want {
				t.Fatalf("slotCapacity(%d, %d) = %d, want %d", tc.useCount, tc.accountCap, got, tc.want)
			}
		})
	}
}

// TestAdmissionSlots pins the single admission calculation: the account's slot
// allowance (vendor UseCount capped by AJS_ACCOUNT_SLOTS) further capped by the
// nodes that can actually serve. A slot node counts only while its slot row is
// live; an unavailable account admits nothing; and a pool-disabled installation
// (legacy Slot 0 nodes) keeps the pre-slot answer.
func TestAdmissionSlots(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

	newAccount := func(t *testing.T, useCount int) (*Store, model.Account) {
		t.Helper()
		st := newEmptyWithClock(func() time.Time { return now }, nil)
		created, err := st.CreateAccount(model.CreateAccountInput{
			Label: "Admission " + strconv.Itoa(useCount), Username: "admission",
			Password: "local-placeholder", Platform: model.PlatformAndroid, Concurrency: 8,
		})
		if err != nil {
			t.Fatalf("CreateAccount: %v", err)
		}
		account, err := st.SetAccountUseCount(created.ID, useCount)
		if err != nil {
			t.Fatalf("SetAccountUseCount: %v", err)
		}
		return st, account
	}
	addNode := func(t *testing.T, st *Store, account model.Account, id string, slot int, state model.NodeState) {
		t.Helper()
		if _, err := st.PutNode(model.ProxyNode{
			ID: id, AccountID: account.ID, Label: "cn-" + id, Region: "cn",
			State: state, Protocol: model.ProtocolSOCKS5, GatePort: 14000 + slot,
			ServerID: "srv-" + id, Slot: slot,
		}); err != nil {
			t.Fatalf("PutNode(%s): %v", id, err)
		}
	}
	liveSlot := func(t *testing.T, st *Store, account model.Account, index int) {
		t.Helper()
		key := model.SlotKey{AccountID: account.ID, Index: index}
		if err := st.UpsertSlot(model.AccountSlot{
			SlotKey: key, ServerID: "srv-node-" + strconv.Itoa(index),
			GatePort: 14000 + index, State: model.SlotLive, Generation: 1,
		}); err != nil {
			t.Fatalf("UpsertSlot(%s): %v", key.String(), err)
		}
	}

	t.Run("one live slot caps a wider allowance", func(t *testing.T) {
		t.Setenv("AJS_ACCOUNT_SLOTS", "8")
		st, account := newAccount(t, 8)
		addNode(t, st, account, "node-1", 1, model.NodeLive)
		liveSlot(t, st, account, 1)
		if got := st.accountCapacityLocked(account, now); got != 1 {
			t.Fatalf("capacity = %d, want 1 (one live slot)", got)
		}
	})

	t.Run("two live slots admit two", func(t *testing.T) {
		t.Setenv("AJS_ACCOUNT_SLOTS", "8")
		st, account := newAccount(t, 8)
		for _, index := range []int{1, 2} {
			id := "node-" + strconv.Itoa(index)
			addNode(t, st, account, id, index, model.NodeLive)
			liveSlot(t, st, account, index)
		}
		// A third node whose slot row is down is not capacity.
		addNode(t, st, account, "node-3", 3, model.NodeLive)
		if err := st.UpsertSlot(model.AccountSlot{
			SlotKey:  model.SlotKey{AccountID: account.ID, Index: 3},
			ServerID: "srv-node-3", GatePort: 14003, State: model.SlotDown,
		}); err != nil {
			t.Fatalf("UpsertSlot(slot 3): %v", err)
		}
		if got := st.accountCapacityLocked(account, now); got != 2 {
			t.Fatalf("capacity = %d, want 2 (two live slots)", got)
		}
	})

	t.Run("operator cap narrows the allowance", func(t *testing.T) {
		t.Setenv("AJS_ACCOUNT_SLOTS", "2")
		st, account := newAccount(t, 8)
		for index := 1; index <= 5; index++ {
			addNode(t, st, account, "node-"+strconv.Itoa(index), 0, model.NodeLive)
		}
		if got := st.accountCapacityLocked(account, now); got != 2 {
			t.Fatalf("capacity = %d, want 2 (operator cap)", got)
		}
	})

	t.Run("pool disabled keeps the legacy answer", func(t *testing.T) {
		t.Setenv("AJS_ACCOUNT_SLOTS", "8")
		st, account := newAccount(t, 4)
		for index := 1; index <= 3; index++ {
			addNode(t, st, account, "node-"+strconv.Itoa(index), 0, model.NodeLive)
		}
		if got := st.accountCapacityLocked(account, now); got != 3 {
			t.Fatalf("capacity = %d, want 3 (legacy nodes)", got)
		}
	})

	t.Run("unavailable accounts admit nothing", func(t *testing.T) {
		t.Setenv("AJS_ACCOUNT_SLOTS", "8")
		st, account := newAccount(t, 8)
		addNode(t, st, account, "node-1", 0, model.NodeLive)
		expired := now.Add(-time.Hour).UTC().Format(time.RFC3339Nano)
		updated, err := st.UpdateAccount(account.ID, model.AccountPatch{ExpiresAt: &expired})
		if err != nil {
			t.Fatalf("UpdateAccount(expired): %v", err)
		}
		if got := st.accountCapacityLocked(updated, now); got != 0 {
			t.Fatalf("capacity = %d, want 0 for an expired account", got)
		}
		paused := model.AccountPaused
		updated, err = st.UpdateAccount(account.ID, model.AccountPatch{State: &paused})
		if err != nil {
			t.Fatalf("UpdateAccount(paused): %v", err)
		}
		if got := st.accountCapacityLocked(updated, now); got != 0 {
			t.Fatalf("capacity = %d, want 0 for a paused account", got)
		}
	})
}

func TestAccountSlotCRUD(t *testing.T) {
	st := New()
	key := model.SlotKey{AccountID: "acc-01", Index: 1}
	slot := model.AccountSlot{
		SlotKey:    key,
		DeviceID:   "dev-slot-1",
		ServerID:   "srv-1",
		GatePort:   18081,
		State:      model.SlotReady,
		Generation: 1,
	}
	if err := st.UpsertSlot(slot); err != nil {
		t.Fatalf("UpsertSlot: %v", err)
	}
	got, ok := st.Slot(key)
	if !ok || !reflect.DeepEqual(got, slot) {
		t.Fatalf("Slot = %+v, %v; want %+v", got, ok, slot)
	}

	slot.State = model.SlotLive
	slot.Generation = 2
	if err := st.PutSlot(slot); err != nil {
		t.Fatalf("PutSlot: %v", err)
	}
	got, _ = st.Slot(key)
	if got.State != model.SlotLive || got.Generation != 2 {
		t.Fatalf("updated slot = %+v", got)
	}

	if slots := st.Slots("acc-01"); len(slots) != 1 || slots[0] != slot {
		t.Fatalf("Slots(acc-01) = %+v", slots)
	}
	if slots := st.Slots("missing"); slots == nil || len(slots) != 0 {
		t.Fatalf("Slots(missing) = %#v, want empty non-nil", slots)
	}
	if all := st.AllSlots(); len(all) < 1 || all[0].AccountID == "" {
		t.Fatalf("AllSlots() = %+v", all)
	}

	second := model.AccountSlot{
		SlotKey:  model.SlotKey{AccountID: "acc-01", Index: 2},
		DeviceID: "dev-slot-2",
		ServerID: "srv-2",
		GatePort: 18082,
		State:    model.SlotProvisioning,
	}
	if err := st.UpsertSlot(second); err != nil {
		t.Fatalf("second UpsertSlot: %v", err)
	}
	if err := st.DeleteSlot(second.SlotKey); err != nil {
		t.Fatalf("DeleteSlot: %v", err)
	}
	if _, ok := st.Slot(second.SlotKey); ok {
		t.Fatal("deleted slot still present")
	}
	if err := st.DeleteSlot(second.SlotKey); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second DeleteSlot = %v, want ErrNotFound", err)
	}
}

func TestAccountSlotCRUDValidation(t *testing.T) {
	st := New()
	valid := model.AccountSlot{
		SlotKey: model.SlotKey{AccountID: "acc-01", Index: 1},
		State:   model.SlotProvisioning,
	}
	if err := st.UpsertSlot(valid); err != nil {
		t.Fatalf("baseline UpsertSlot: %v", err)
	}
	if err := st.UpsertSlot(model.AccountSlot{
		SlotKey: model.SlotKey{AccountID: "missing", Index: 1},
		State:   model.SlotProvisioning,
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown account = %v, want ErrNotFound", err)
	}
	for _, key := range []model.SlotKey{
		{AccountID: "acc-01", Index: 0},
		{AccountID: "acc-01", Index: 9},
	} {
		if err := st.UpsertSlot(model.AccountSlot{SlotKey: key, State: model.SlotProvisioning}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("index %d = %v, want ErrInvalid", key.Index, err)
		}
	}
	if err := st.UpsertSlot(model.AccountSlot{
		SlotKey: model.SlotKey{AccountID: "acc-01", Index: 2},
		State:   "paused",
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown state = %v, want ErrInvalid", err)
	}

	first := model.AccountSlot{
		SlotKey:  model.SlotKey{AccountID: "acc-01", Index: 1},
		DeviceID: "dev-shared",
		GatePort: 18090,
		State:    model.SlotProvisioning,
	}
	if err := st.UpsertSlot(first); err != nil {
		t.Fatalf("first slot: %v", err)
	}
	if err := st.UpsertSlot(model.AccountSlot{
		SlotKey:  model.SlotKey{AccountID: "acc-01", Index: 2},
		DeviceID: "dev-shared",
		State:    model.SlotProvisioning,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate device = %v, want ErrConflict", err)
	}
	if err := st.UpsertSlot(model.AccountSlot{
		SlotKey:  model.SlotKey{AccountID: "acc-01", Index: 2},
		GatePort: 18090,
		State:    model.SlotProvisioning,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate port = %v, want ErrConflict", err)
	}
}

func TestEnsureAccountSlots(t *testing.T) {
	st := NewEmpty()
	account, err := st.CreateAccount(model.CreateAccountInput{
		Label: "slots", Username: "slots-user", Password: "slot-test-password",
		Platform: model.PlatformLinux, Concurrency: 1,
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	slots, err := st.EnsureAccountSlots(account.ID, 3)
	if err != nil {
		t.Fatalf("EnsureAccountSlots(3): %v", err)
	}
	if len(slots) != 3 {
		t.Fatalf("EnsureAccountSlots(3) returned %d rows", len(slots))
	}
	for i, slot := range slots {
		if slot.Index != i+1 || slot.State != model.SlotProvisioning {
			t.Fatalf("slot %d = %+v", i, slot)
		}
	}
	if again, err := st.EnsureAccountSlots(account.ID, 3); err != nil || len(again) != 3 {
		t.Fatalf("second EnsureAccountSlots = %d rows, %v", len(again), err)
	}

	// A slot carrying a device is in use: shrinking capacity must fail
	// without deleting it.
	if err := st.UpsertSlot(model.AccountSlot{
		SlotKey:  model.SlotKey{AccountID: account.ID, Index: 3},
		DeviceID: "dev-in-use",
		State:    model.SlotProvisioning,
	}); err != nil {
		t.Fatalf("mark slot 3: %v", err)
	}
	if _, err := st.EnsureAccountSlots(account.ID, 1); !errors.Is(err, ErrConflict) {
		t.Fatalf("shrink with device = %v, want ErrConflict", err)
	}
	if got := st.SlotCount(account.ID); got != 3 {
		t.Fatalf("slots after refused shrink = %d, want 3", got)
	}
	if err := st.DeleteSlot(model.SlotKey{AccountID: account.ID, Index: 3}); err != nil {
		t.Fatalf("DeleteSlot(3): %v", err)
	}
	reduced, err := st.EnsureAccountSlots(account.ID, 1)
	if err != nil || len(reduced) != 1 {
		t.Fatalf("EnsureAccountSlots(1) = %d rows, %v", len(reduced), err)
	}

	grown, err := st.EnsureAccountSlots(account.ID, 20)
	if err != nil || len(grown) != MaxServiceSlots {
		t.Fatalf("EnsureAccountSlots(20) = %d rows, %v; want %d", len(grown), err, MaxServiceSlots)
	}
	if _, err := st.EnsureAccountSlots("missing", 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("EnsureAccountSlots(missing) = %v, want ErrNotFound", err)
	}
}

func TestAccountSlotValidation(t *testing.T) {
	accounts := map[string]struct{}{"acc-01": {}}
	base := model.AccountSlot{
		SlotKey: model.SlotKey{AccountID: "acc-01", Index: 1},
		State:   model.SlotProvisioning,
	}
	valid := &Snapshot{Accounts: []model.Account{{ID: "acc-01"}}, AccountSlots: []model.AccountSlot{base}}
	if err := valid.validateAccountSlots(accounts); err != nil {
		t.Fatalf("valid snapshot: %v", err)
	}

	cases := []struct {
		name string
		snap *Snapshot
	}{
		{
			name: "duplicate key",
			snap: &Snapshot{Accounts: []model.Account{{ID: "acc-01"}}, AccountSlots: []model.AccountSlot{base, base}},
		},
		{
			name: "index zero",
			snap: &Snapshot{Accounts: []model.Account{{ID: "acc-01"}}, AccountSlots: []model.AccountSlot{{
				SlotKey: model.SlotKey{AccountID: "acc-01", Index: 0}, State: model.SlotProvisioning,
			}}},
		},
		{
			name: "index nine",
			snap: &Snapshot{Accounts: []model.Account{{ID: "acc-01"}}, AccountSlots: []model.AccountSlot{{
				SlotKey: model.SlotKey{AccountID: "acc-01", Index: 9}, State: model.SlotProvisioning,
			}}},
		},
		{
			name: "unknown state",
			snap: &Snapshot{Accounts: []model.Account{{ID: "acc-01"}}, AccountSlots: []model.AccountSlot{{
				SlotKey: base.SlotKey, State: "paused",
			}}},
		},
		{
			name: "unknown account",
			snap: &Snapshot{Accounts: []model.Account{{ID: "acc-01"}}, AccountSlots: []model.AccountSlot{{
				SlotKey: model.SlotKey{AccountID: "acc-02", Index: 1}, State: model.SlotProvisioning,
			}}},
		},
		{
			name: "duplicate device",
			snap: &Snapshot{Accounts: []model.Account{{ID: "acc-01"}}, AccountSlots: []model.AccountSlot{
				{SlotKey: base.SlotKey, DeviceID: "dev-1", State: model.SlotProvisioning},
				{SlotKey: model.SlotKey{AccountID: "acc-01", Index: 2}, DeviceID: "dev-1", State: model.SlotProvisioning},
			}},
		},
		{
			name: "duplicate gate port",
			snap: &Snapshot{Accounts: []model.Account{{ID: "acc-01"}}, AccountSlots: []model.AccountSlot{
				{SlotKey: base.SlotKey, GatePort: 18081, State: model.SlotProvisioning},
				{SlotKey: model.SlotKey{AccountID: "acc-01", Index: 2}, GatePort: 18081, State: model.SlotProvisioning},
			}},
		},
		{
			name: "binding without slot one",
			snap: &Snapshot{Accounts: []model.Account{{ID: "acc-01"}}, DeviceBindings: map[string]string{"acc-01": "dev-1"}},
		},
		{
			name: "binding mismatch",
			snap: &Snapshot{Accounts: []model.Account{{ID: "acc-01"}}, AccountSlots: []model.AccountSlot{{
				SlotKey: base.SlotKey, DeviceID: "dev-1", State: model.SlotProvisioning,
			}}, DeviceBindings: map[string]string{"acc-01": "dev-2"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.snap.validateAccountSlots(accounts); err == nil {
				t.Fatalf("snapshot %q passed validation", tc.name)
			}
		})
	}

	// The integrated Snapshot.validate path must reject the same rows.
	bad := &Snapshot{Accounts: []model.Account{{ID: "acc-01"}}, AccountSlots: []model.AccountSlot{{
		SlotKey: base.SlotKey, State: "paused",
	}}}
	if err := bad.validate(); err == nil {
		t.Fatal("Snapshot.validate accepted an unknown slot state")
	}
}

func TestAccountSlotMigrationFromV1Snapshot(t *testing.T) {
	dir := t.TempDir()
	passwords, err := secret.New(bytes.Repeat([]byte{0x11}, secret.KeySize), filepath.Join(dir, "secrets.json"))
	if err != nil {
		t.Fatalf("password sealer: %v", err)
	}
	devices, err := secret.New(bytes.Repeat([]byte{0x22}, secret.KeySize), filepath.Join(dir, "device-secrets.json"))
	if err != nil {
		t.Fatalf("device sealer: %v", err)
	}
	accountID := "acc-v1"
	device := slotModelTestDevice()
	snap := Snapshot{
		Version: 1,
		Accounts: []model.Account{{
			ID: accountID, Label: "v1", Username: "v1-user",
			Platform: model.PlatformLinux, State: model.AccountActive,
			Concurrency: 1, UseCount: 1,
		}},
		Devices:   map[string]DeviceIdentity{accountID: device},
		KeyHashes: map[string]string{},
		Secrets:   map[string][]byte{},
	}
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal v1 fixture: %v", err)
	}

	st := NewEmptyWithSealers(passwords, devices)
	if err := st.Restore(data); err != nil {
		t.Fatalf("Restore(v1): %v", err)
	}
	slots := st.Slots(accountID)
	if len(slots) != 1 {
		t.Fatalf("migrated slots = %d, want 1", len(slots))
	}
	binding := st.deviceBindings[accountID]
	if binding == "" {
		t.Fatal("v1 migration did not create a device binding")
	}
	if slots[0].Index != 1 || slots[0].State != model.SlotProvisioning {
		t.Fatalf("migrated slot = %+v", slots[0])
	}
	if slots[0].DeviceID != binding {
		t.Fatalf("slot device = %q, binding = %q; migration must copy the record id", slots[0].DeviceID, binding)
	}
	material, err := st.AccountDeviceMaterial(accountID)
	if err != nil {
		t.Fatalf("AccountDeviceMaterial: %v", err)
	}
	if material.DeviceID != binding || !reflect.DeepEqual(material.Identity, device) {
		t.Fatalf("migrated material = %+v, want device id %q and the original identity", material, binding)
	}
}

func TestAccountSlotMigrationFromV2Snapshot(t *testing.T) {
	f := newPoolFixture(t)
	account := f.createAccount(t, "slot-v2", "slot-v2-user")
	device := slotModelTestDevice()
	if _, err := f.st.EnsureAccountDevice(account.ID, device); err != nil {
		t.Fatalf("EnsureAccountDevice: %v", err)
	}
	data, err := f.st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	rawSlots, ok := doc["accountSlots"]
	if !ok {
		t.Fatal("snapshot has no accountSlots field")
	}
	var snapshotSlots []model.AccountSlot
	if err := json.Unmarshal(rawSlots, &snapshotSlots); err != nil {
		t.Fatalf("decode accountSlots: %v", err)
	}
	binding := f.st.deviceBindings[account.ID]
	if len(snapshotSlots) != 1 || snapshotSlots[0].DeviceID != binding {
		t.Fatalf("snapshot accountSlots = %+v, want slot 1 device %q", snapshotSlots, binding)
	}
	delete(doc, "accountSlots")
	stripped, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("encode pre-slot snapshot: %v", err)
	}

	path := filepath.Join(f.dir, "pre-slot-state.json")
	if err := os.WriteFile(path, stripped, 0o600); err != nil {
		t.Fatalf("write pre-slot snapshot: %v", err)
	}
	devices, err := secret.New(bytes.Repeat([]byte{0x22}, secret.KeySize), filepath.Join(f.dir, "device-secrets.json"))
	if err != nil {
		t.Fatalf("device sealer reload: %v", err)
	}
	fresh := NewEmptyWithSealers(f.st.sealer, devices)
	if err := fresh.Load(path); err != nil {
		t.Fatalf("Load(pre-slot): %v", err)
	}

	slots := fresh.Slots(account.ID)
	if len(slots) != 1 {
		t.Fatalf("migrated slots = %d, want 1", len(slots))
	}
	binding = f.st.deviceBindings[account.ID]
	if slots[0].DeviceID != binding {
		t.Fatalf("slot device = %q, want binding %q", slots[0].DeviceID, binding)
	}
	if slots[0].State != model.SlotProvisioning {
		t.Fatalf("migrated slot state = %q, want provisioning", slots[0].State)
	}
	before, err := f.st.deviceSealer.OpenID(binding)
	if err != nil {
		t.Fatalf("OpenID before: %v", err)
	}
	after, err := devices.OpenID(binding)
	if err != nil {
		t.Fatalf("OpenID after: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("migration changed the sealed device material")
	}
	if len(fresh.devicePool) != len(f.st.devicePool) {
		t.Fatalf("migration changed the device pool size: %d -> %d", len(f.st.devicePool), len(fresh.devicePool))
	}
}

func TestAccountSlotMigration(t *testing.T) {
	t.Run("v1", TestAccountSlotMigrationFromV1Snapshot)
	t.Run("v1-plaintext", TestAccountSlotMigrationFromV1PlaintextSnapshot)
	t.Run("v2", TestAccountSlotMigrationFromV2Snapshot)
}

func TestAccountSlotMigrationFromV1PlaintextSnapshot(t *testing.T) {
	accountID := "acc-v1-plain"
	device := slotModelTestDevice()
	snap := Snapshot{
		Version: 1,
		Accounts: []model.Account{{
			ID: accountID, Label: "v1-plain", Username: "v1-plain-user",
			Platform: model.PlatformLinux, State: model.AccountActive,
			Concurrency: 1, UseCount: 1,
		}},
		Devices:   map[string]DeviceIdentity{accountID: device},
		KeyHashes: map[string]string{},
		Secrets:   map[string][]byte{},
	}
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	st := NewEmpty()
	if err := st.Restore(data); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	slots := st.Slots(accountID)
	if len(slots) != 1 || slots[0].DeviceID != device.UID {
		t.Fatalf("plaintext v1 slots = %+v, want slot 1 device %q", slots, device.UID)
	}
}

func TestAccountSlotCapacityReduction(t *testing.T) {
	st := NewEmpty()
	account, err := st.CreateAccount(model.CreateAccountInput{
		Label: "reduction", Username: "reduction-user", Password: "slot-test-password",
		Platform: model.PlatformLinux, Concurrency: 1,
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if _, err := st.EnsureAccountSlots(account.ID, 3); err != nil {
		t.Fatalf("EnsureAccountSlots(3): %v", err)
	}
	// A live slot cannot be removed by a capacity reduction.
	if err := st.UpsertSlot(model.AccountSlot{
		SlotKey:  model.SlotKey{AccountID: account.ID, Index: 3},
		GatePort: 18093,
		State:    model.SlotLive,
	}); err != nil {
		t.Fatalf("mark slot 3 live: %v", err)
	}
	if _, err := st.EnsureAccountSlots(account.ID, 1); !errors.Is(err, ErrConflict) {
		t.Fatalf("reduction with live slot = %v, want ErrConflict", err)
	}
	if got := st.SlotCount(account.ID); got != 3 {
		t.Fatalf("slot count after refused reduction = %d, want 3", got)
	}
}

func TestAccountSlotSnapshotRoundtrip(t *testing.T) {
	st := New()
	key := model.SlotKey{AccountID: "acc-01", Index: 1}
	slot := model.AccountSlot{
		SlotKey:    key,
		DeviceID:   "dev-roundtrip",
		ServerID:   "srv-roundtrip",
		GatePort:   19001,
		State:      model.SlotReady,
		Generation: 3,
	}
	if err := st.UpsertSlot(slot); err != nil {
		t.Fatalf("UpsertSlot: %v", err)
	}
	first, err := st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	fresh := NewEmpty()
	if err := fresh.Restore(first); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	second, err := fresh.Snapshot()
	if err != nil {
		t.Fatalf("second Snapshot: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("snapshot round-trip is not byte-identical:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
	if got, want := fresh.AllSlots(), st.AllSlots(); !reflect.DeepEqual(got, want) {
		t.Fatalf("slots after round-trip = %+v, want %+v", got, want)
	}
}

func TestAccountSlotSnapshotRoundtripWithDevicePool(t *testing.T) {
	f := newPoolFixture(t)
	account := f.createAccount(t, "slot-roundtrip", "slot-roundtrip-user")
	if _, err := f.st.EnsureAccountDevice(account.ID, slotModelTestDevice()); err != nil {
		t.Fatalf("EnsureAccountDevice: %v", err)
	}
	first, err := f.st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	fresh := NewEmptyWithSealers(f.st.sealer, f.st.deviceSealer)
	if err := fresh.Restore(first); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	second, err := fresh.Snapshot()
	if err != nil {
		t.Fatalf("second Snapshot: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("device-pool snapshot round-trip is not byte-identical:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
	slots := fresh.Slots(account.ID)
	if len(slots) != 1 || slots[0].DeviceID == "" || slots[0].DeviceID != f.st.deviceBindings[account.ID] {
		t.Fatalf("slots after device-pool round-trip = %+v", slots)
	}
}
