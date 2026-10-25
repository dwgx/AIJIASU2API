package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"ajsaccountd/internal/model"
)

// --- shared assertions -----------------------------------------------------

func slotKey(accountID string, index int) model.SlotKey {
	return model.SlotKey{AccountID: accountID, Index: index}
}

func assertSlotBinding(t *testing.T, st *Store, key model.SlotKey, recordID string) {
	t.Helper()
	slot, ok := st.Slot(key)
	if !ok {
		t.Fatalf("slot %s is missing", key.String())
	}
	if slot.DeviceID != recordID {
		t.Fatalf("slot %s device = %q, want %q", key.String(), slot.DeviceID, recordID)
	}
	if got, ok := st.deviceSlotBindings[key]; !ok || got != recordID {
		t.Fatalf("canonical binding %s = %q (present %v), want %q", key.String(), got, ok, recordID)
	}
	if key.Index == 1 {
		if got := st.deviceBindings[key.AccountID]; got != recordID {
			t.Fatalf("slot-1 projection %s = %q, want %q", key.AccountID, got, recordID)
		}
	}
}

func assertNoSlotBinding(t *testing.T, st *Store, key model.SlotKey) {
	t.Helper()
	if slot, ok := st.Slot(key); ok && slot.DeviceID != "" {
		t.Fatalf("slot %s still references device %q", key.String(), slot.DeviceID)
	}
	if got, ok := st.deviceSlotBindings[key]; ok {
		t.Fatalf("canonical binding %s = %q, want none", key.String(), got)
	}
	if key.Index == 1 {
		if got := st.deviceBindings[key.AccountID]; got != "" {
			t.Fatalf("slot-1 projection %s = %q, want none", key.AccountID, got)
		}
	}
}

func mustSlotRecord(t *testing.T, st *Store, recordID string) DeviceRecord {
	t.Helper()
	rec, ok := st.DeviceRecord(recordID)
	if !ok {
		t.Fatalf("device %q is missing", recordID)
	}
	return rec
}

func assertRecordSlot(t *testing.T, rec DeviceRecord, key model.SlotKey) {
	t.Helper()
	if rec.AssignedSlotKey == nil {
		t.Fatalf("device %q has no AssignedSlotKey, want %s", rec.ID, key.String())
	}
	if *rec.AssignedSlotKey != key {
		t.Fatalf("device %q AssignedSlotKey = %s, want %s", rec.ID, rec.AssignedSlotKey.String(), key.String())
	}
}

// --- binding stability -----------------------------------------------------

func TestBindForLoginSlotStableAcrossRestarts(t *testing.T) {
	f := newPoolFixture(t)
	account := f.createAccount(t, "slot-stable", "slot-stable-user")
	if _, err := f.st.EnsureAccountSlots(account.ID, 2); err != nil {
		t.Fatalf("EnsureAccountSlots: %v", err)
	}
	key1 := slotKey(account.ID, 1)
	key2 := slotKey(account.ID, 2)

	first1, err := f.st.BindForLoginSlot(key1, testLinuxProfile)
	if err != nil {
		t.Fatalf("BindForLoginSlot(%s): %v", key1, err)
	}
	first2, err := f.st.BindForLoginSlot(key2, testLinuxProfile)
	if err != nil {
		t.Fatalf("BindForLoginSlot(%s): %v", key2, err)
	}
	if first1.DeviceID == first2.DeviceID {
		t.Fatal("slot 1 and slot 2 reused one device record")
	}
	if first1.Fingerprint == first2.Fingerprint || first1.MachineCode == first2.MachineCode {
		t.Fatal("slot 1 and slot 2 share a fingerprint or machine code")
	}
	assertRecordSlot(t, mustSlotRecord(t, f.st, first1.DeviceID), key1)
	assertRecordSlot(t, mustSlotRecord(t, f.st, first2.DeviceID), key2)
	if first1.AssignedSlotKey == nil || first1.AssignedSlotKey.Index != 1 {
		t.Fatalf("slot 1 material slot = %v", first1.AssignedSlotKey)
	}
	if first2.AssignedSlotKey == nil || first2.AssignedSlotKey.Index != 2 {
		t.Fatalf("slot 2 material slot = %v", first2.AssignedSlotKey)
	}

	fresh := f.reload(t)
	again1, err := fresh.BindForLoginSlot(key1, testLinuxProfile)
	if err != nil {
		t.Fatalf("BindForLoginSlot(%s) after restart: %v", key1, err)
	}
	again2, err := fresh.BindForLoginSlot(key2, testLinuxProfile)
	if err != nil {
		t.Fatalf("BindForLoginSlot(%s) after restart: %v", key2, err)
	}
	if again1.DeviceID != first1.DeviceID || again1.Identity != first1.Identity ||
		again1.MachineCode != first1.MachineCode {
		t.Fatalf("slot 1 identity changed across restart: %+v -> %+v", first1, again1)
	}
	if again2.DeviceID != first2.DeviceID || again2.Identity != first2.Identity ||
		again2.MachineCode != first2.MachineCode {
		t.Fatalf("slot 2 identity changed across restart: %+v -> %+v", first2, again2)
	}
	slots := fresh.Slots(account.ID)
	if len(slots) != 2 || slots[0].DeviceID != first1.DeviceID || slots[1].DeviceID != first2.DeviceID {
		t.Fatalf("slots after restart = %+v", slots)
	}
}

func TestSlotOneMigrationReusesExistingRecordBytes(t *testing.T) {
	f := newPoolFixture(t)
	account := f.createAccount(t, "slot-migrate", "slot-migrate-user")
	first, err := f.st.BindForLogin(account.ID, testLinuxProfile)
	if err != nil {
		t.Fatalf("BindForLogin: %v", err)
	}
	before, err := f.devices.OpenID(first.DeviceID)
	if err != nil {
		t.Fatalf("OpenID before: %v", err)
	}
	recordBefore := mustSlotRecord(t, f.st, first.DeviceID)

	fresh := f.reload(t)
	after, err := f.devices.OpenID(first.DeviceID)
	if err != nil {
		t.Fatalf("OpenID after: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("slot-1 migration rewrote the sealed material")
	}
	recordAfter := mustSlotRecord(t, fresh, first.DeviceID)
	if recordAfter.ID != recordBefore.ID || recordAfter.Fingerprint != recordBefore.Fingerprint ||
		recordAfter.CreatedAt != recordBefore.CreatedAt {
		t.Fatalf("slot-1 migration changed the record: %+v -> %+v", recordBefore, recordAfter)
	}
	assertRecordSlot(t, recordAfter, slotKey(account.ID, 1))
	again, err := fresh.BindForLogin(account.ID, testLinuxProfile)
	if err != nil {
		t.Fatalf("BindForLogin after restart: %v", err)
	}
	if again.DeviceID != first.DeviceID || again.MachineCode != first.MachineCode ||
		again.Identity != first.Identity {
		t.Fatalf("slot-1 identity changed across restart: %+v -> %+v", first, again)
	}

	// A pre-slot v2 document has neither accountSlots nor assignedSlotKey.
	// Restore must derive slot 1 and backfill the field without touching the
	// sealed bytes or generating a new identity.
	data, err := f.st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	var legacy Snapshot
	if err := json.Unmarshal(data, &legacy); err != nil {
		t.Fatalf("decode legacy snapshot: %v", err)
	}
	legacy.AccountSlots = nil
	for i := range legacy.DevicePool {
		legacy.DevicePool[i].AssignedSlotKey = nil
	}
	legacyData, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("marshal legacy snapshot: %v", err)
	}
	legacyStore := NewEmptyWithSealers(f.st.sealer, f.devices)
	if err := legacyStore.Restore(legacyData); err != nil {
		t.Fatalf("Restore legacy v2: %v", err)
	}
	legacyRec := mustSlotRecord(t, legacyStore, first.DeviceID)
	assertRecordSlot(t, legacyRec, slotKey(account.ID, 1))
	legacyBytes, err := f.devices.OpenID(first.DeviceID)
	if err != nil {
		t.Fatalf("OpenID legacy: %v", err)
	}
	if !bytes.Equal(before, legacyBytes) {
		t.Fatal("legacy migration changed the sealed material")
	}
	legacyBind, err := legacyStore.BindForLogin(account.ID, testLinuxProfile)
	if err != nil {
		t.Fatalf("BindForLogin legacy: %v", err)
	}
	if legacyBind.DeviceID != first.DeviceID {
		t.Fatalf("legacy bind = %q, want %q", legacyBind.DeviceID, first.DeviceID)
	}
}

func TestSlotTwoGetsNewRecordNeverSlotOne(t *testing.T) {
	f := newPoolFixture(t)
	account := f.createAccount(t, "slot-two", "slot-two-user")
	if _, err := f.st.EnsureAccountSlots(account.ID, 2); err != nil {
		t.Fatalf("EnsureAccountSlots: %v", err)
	}
	key1 := slotKey(account.ID, 1)
	key2 := slotKey(account.ID, 2)
	one, err := f.st.BindForLoginSlot(key1, testLinuxProfile)
	if err != nil {
		t.Fatalf("slot 1 bind: %v", err)
	}
	two, err := f.st.BindForLoginSlot(key2, testLinuxProfile)
	if err != nil {
		t.Fatalf("slot 2 bind: %v", err)
	}
	if two.DeviceID == one.DeviceID {
		t.Fatal("slot 2 got slot 1's record")
	}
	if two.Fingerprint == one.Fingerprint || two.MachineCode == one.MachineCode {
		t.Fatal("slot 2 got slot 1's identity material")
	}
	if got := len(f.st.DevicePoolRecords()); got != 2 {
		t.Fatalf("pool records = %d, want 2 (one per slot)", got)
	}
	recOne := mustSlotRecord(t, f.st, one.DeviceID)
	recTwo := mustSlotRecord(t, f.st, two.DeviceID)
	assertRecordSlot(t, recOne, key1)
	assertRecordSlot(t, recTwo, key2)
	if recOne.AssignedAccountID != account.ID || recTwo.AssignedAccountID != account.ID {
		t.Fatalf("record account ids = %q, %q", recOne.AssignedAccountID, recTwo.AssignedAccountID)
	}
}

func TestBindForLoginSlotConcurrent(t *testing.T) {
	f := newPoolFixture(t)
	account := f.createAccount(t, "slot-concurrent", "slot-concurrent-user")
	if _, err := f.st.EnsureAccountSlots(account.ID, 2); err != nil {
		t.Fatalf("EnsureAccountSlots: %v", err)
	}
	keys := []model.SlotKey{slotKey(account.ID, 1), slotKey(account.ID, 2)}
	const perSlot = 25
	type result struct {
		material DeviceMaterial
		err      error
	}
	results := make([][]result, len(keys))
	var wg sync.WaitGroup
	for i, key := range keys {
		results[i] = make([]result, perSlot)
		for j := range perSlot {
			wg.Add(1)
			go func(slot int, pos int) {
				defer wg.Done()
				results[slot][pos].material, results[slot][pos].err = f.st.BindForLoginSlot(key, testLinuxProfile)
			}(i, j)
		}
	}
	wg.Wait()
	for i := range keys {
		for j := range results[i] {
			if results[i][j].err != nil {
				t.Fatalf("slot %d bind %d: %v", i+1, j, results[i][j].err)
			}
			if results[i][j].material.DeviceID != results[i][0].material.DeviceID {
				t.Fatalf("slot %d bind %d got a different record", i+1, j)
			}
		}
		assertSlotBinding(t, f.st, keys[i], results[i][0].material.DeviceID)
	}
	if got := len(f.st.DevicePoolRecords()); got != 2 {
		t.Fatalf("pool records = %d, want 2", got)
	}
}

// --- persisted AssignedSlotKey --------------------------------------------

func TestAssignedSlotKeyPersistsAndValidates(t *testing.T) {
	f := newPoolFixture(t)
	account := f.createAccount(t, "slot-json", "slot-json-user")
	if _, err := f.st.EnsureAccountSlots(account.ID, 2); err != nil {
		t.Fatalf("EnsureAccountSlots: %v", err)
	}
	key2 := slotKey(account.ID, 2)
	material, err := f.st.BindForLoginSlot(key2, testLinuxProfile)
	if err != nil {
		t.Fatalf("BindForLoginSlot: %v", err)
	}

	data, err := f.st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if !strings.Contains(string(data), `"assignedSlotKey"`) {
		t.Fatal("snapshot does not persist assignedSlotKey")
	}
	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	var stored *DeviceRecord
	for i := range snap.DevicePool {
		if snap.DevicePool[i].ID == material.DeviceID {
			stored = &snap.DevicePool[i]
			break
		}
	}
	if stored == nil {
		t.Fatal("bound record is absent from the snapshot")
	}
	if stored.AssignedSlotKey == nil || stored.AssignedSlotKey.Index != 2 {
		t.Fatalf("persisted assignedSlotKey = %v", stored.AssignedSlotKey)
	}
	if err := stored.Validate(); err != nil {
		t.Fatalf("stored record Validate: %v", err)
	}
	badRecord := *stored
	badRecord.AssignedSlotKey = nil
	if err := badRecord.Validate(); err == nil {
		t.Fatal("DeviceRecord.Validate accepted an assigned record without a slot")
	}

	// A hand-written record with a wrong account id must be rejected.
	bad := *stored
	badKey := model.SlotKey{AccountID: "other-account", Index: 2}
	bad.AssignedSlotKey = &badKey
	badSnap := snap
	badSnap.DevicePool = append([]DeviceRecord(nil), snap.DevicePool...)
	for i := range badSnap.DevicePool {
		if badSnap.DevicePool[i].ID == bad.ID {
			badSnap.DevicePool[i] = bad
		}
	}
	badData, err := json.Marshal(badSnap)
	if err != nil {
		t.Fatalf("marshal bad snapshot: %v", err)
	}
	fresh := NewEmptyWithSealers(f.st.sealer, f.devices)
	if err := fresh.Restore(badData); err == nil {
		t.Fatal("Restore accepted a slot key that names the wrong account")
	}

	// An assigned record with no slot row or binding must be rejected rather
	// than silently migrated to a guessed slot.
	noSlot := snap
	noSlot.AccountSlots = nil
	noSlot.DeviceBindings = nil
	noSlot.DevicePool = append([]DeviceRecord(nil), snap.DevicePool...)
	for i := range noSlot.DevicePool {
		if noSlot.DevicePool[i].ID == material.DeviceID {
			noSlot.DevicePool[i].AssignedSlotKey = nil
		}
	}
	noSlotData, err := json.Marshal(noSlot)
	if err != nil {
		t.Fatalf("marshal no-slot snapshot: %v", err)
	}
	fresh = NewEmptyWithSealers(f.st.sealer, f.devices)
	if err := fresh.Restore(noSlotData); err == nil {
		t.Fatal("Restore accepted an assigned record without a slot")
	}

	// The valid document still round-trips.
	fresh = NewEmptyWithSealers(f.st.sealer, f.devices)
	if err := fresh.Restore(data); err != nil {
		t.Fatalf("valid Restore: %v", err)
	}
	rec := mustSlotRecord(t, fresh, material.DeviceID)
	assertRecordSlot(t, rec, key2)
}

// --- mutation matrix -------------------------------------------------------

func TestDeviceSlotMutationMatrix(t *testing.T) {
	t.Run("assign and release", func(t *testing.T) {
		f := newPoolFixture(t)
		account := f.createAccount(t, "matrix-assign", "matrix-assign-user")
		if _, err := f.st.EnsureAccountSlots(account.ID, 2); err != nil {
			t.Fatalf("EnsureAccountSlots: %v", err)
		}
		key2 := slotKey(account.ID, 2)
		records, err := f.st.GenerateDevices(DeviceGenerateInput{Count: 1, Profile: testLinuxProfile})
		if err != nil {
			t.Fatalf("GenerateDevices: %v", err)
		}
		assigned, err := f.st.AssignDeviceSlot(records[0].ID, key2)
		if err != nil {
			t.Fatalf("AssignDeviceSlot: %v", err)
		}
		if assigned.State != DeviceAssigned || assigned.AssignedAccountID != account.ID {
			t.Fatalf("assigned record = %+v", assigned)
		}
		assertRecordSlot(t, assigned, key2)
		assertSlotBinding(t, f.st, key2, assigned.ID)
		if material := f.material(t, assigned.ID); material.AssignedSlotKey == nil ||
			material.AssignedSlotKey.Index != 2 {
			t.Fatalf("assigned material slot = %v", material.AssignedSlotKey)
		}

		released, err := f.st.ReleaseDevice(assigned.ID)
		if err != nil {
			t.Fatalf("ReleaseDevice: %v", err)
		}
		if released.State != DeviceAvailable || released.AssignedSlotKey != nil || released.AssignedAccountID != "" {
			t.Fatalf("released record = %+v", released)
		}
		assertNoSlotBinding(t, f.st, key2)
		if material := f.material(t, assigned.ID); material.AssignedSlotKey != nil {
			t.Fatalf("released material still claims slot %v", material.AssignedSlotKey)
		}
		if _, ok := f.st.DeviceRecord(assigned.ID); !ok {
			t.Fatal("released record disappeared")
		}
		// An available record can be assigned again; its material no longer
		// carries a stale slot reference.
		reassigned, err := f.st.AssignDeviceSlot(assigned.ID, key2)
		if err != nil {
			t.Fatalf("reassign available record: %v", err)
		}
		assertRecordSlot(t, reassigned, key2)
	})

	t.Run("retire", func(t *testing.T) {
		f := newPoolFixture(t)
		account := f.createAccount(t, "matrix-retire", "matrix-retire-user")
		if _, err := f.st.EnsureAccountSlots(account.ID, 2); err != nil {
			t.Fatalf("EnsureAccountSlots: %v", err)
		}
		key2 := slotKey(account.ID, 2)
		material, err := f.st.BindForLoginSlot(key2, testLinuxProfile)
		if err != nil {
			t.Fatalf("BindForLoginSlot: %v", err)
		}
		retired, err := f.st.RetireDevice(material.DeviceID, "hardware replaced")
		if err != nil {
			t.Fatalf("RetireDevice: %v", err)
		}
		if retired.State != DeviceRetired || retired.AssignedAccountID != "" ||
			retired.AssignedSlotKey == nil || retired.AssignedSlotKey.Index != 2 {
			t.Fatalf("retired record = %+v", retired)
		}
		assertNoSlotBinding(t, f.st, key2)
	})

	t.Run("delete retires every slot and restore is slot-exact", func(t *testing.T) {
		f := newPoolFixture(t)
		account := f.createAccount(t, "matrix-delete", "matrix-delete-user")
		if _, err := f.st.EnsureAccountSlots(account.ID, 2); err != nil {
			t.Fatalf("EnsureAccountSlots: %v", err)
		}
		key1 := slotKey(account.ID, 1)
		key2 := slotKey(account.ID, 2)
		one, err := f.st.BindForLoginSlot(key1, testLinuxProfile)
		if err != nil {
			t.Fatalf("slot 1 bind: %v", err)
		}
		two, err := f.st.BindForLoginSlot(key2, testLinuxProfile)
		if err != nil {
			t.Fatalf("slot 2 bind: %v", err)
		}
		if err := f.st.DeleteAccount(account.ID); err != nil {
			t.Fatalf("DeleteAccount: %v", err)
		}
		if got := len(f.st.Slots(account.ID)); got != 0 {
			t.Fatalf("slots after delete = %d, want 0", got)
		}
		for _, id := range []string{one.DeviceID, two.DeviceID} {
			rec := mustSlotRecord(t, f.st, id)
			if rec.State != DeviceRetired || rec.AssignedAccountID != "" || rec.RetiredReason != "account_deleted" {
				t.Fatalf("record %q after delete = %+v", id, rec)
			}
		}
		if len(f.st.deviceBindings) != 0 || len(f.st.deviceSlotBindings) != 0 {
			t.Fatalf("bindings after delete: legacy=%v slot=%v", f.st.deviceBindings, f.st.deviceSlotBindings)
		}

		again := f.createAccount(t, "matrix-delete-2", "matrix-delete-user")
		if _, err := f.st.EnsureAccountSlots(again.ID, 2); err != nil {
			t.Fatalf("EnsureAccountSlots again: %v", err)
		}
		restored1, err := f.st.BindForLoginSlot(slotKey(again.ID, 1), testLinuxProfile)
		if err != nil {
			t.Fatalf("restore slot 1: %v", err)
		}
		restored2, err := f.st.BindForLoginSlot(slotKey(again.ID, 2), testLinuxProfile)
		if err != nil {
			t.Fatalf("restore slot 2: %v", err)
		}
		if restored1.DeviceID != one.DeviceID {
			t.Fatalf("slot 1 restored %q, want %q", restored1.DeviceID, one.DeviceID)
		}
		if restored2.DeviceID != two.DeviceID {
			t.Fatalf("slot 2 restored %q, want %q", restored2.DeviceID, two.DeviceID)
		}
		if restored1.DeviceID == restored2.DeviceID {
			t.Fatal("delete/re-add restored one record into both slots")
		}
	})

	t.Run("gc", func(t *testing.T) {
		f := newPoolFixture(t)
		account := f.createAccount(t, "matrix-gc", "matrix-gc-user")
		if _, err := f.st.EnsureAccountSlots(account.ID, 2); err != nil {
			t.Fatalf("EnsureAccountSlots: %v", err)
		}
		key2 := slotKey(account.ID, 2)
		material, err := f.st.BindForLoginSlot(key2, testLinuxProfile)
		if err != nil {
			t.Fatalf("BindForLoginSlot: %v", err)
		}
		if _, err := f.st.RetireDevice(material.DeviceID, "retention"); err != nil {
			t.Fatalf("RetireDevice: %v", err)
		}
		f.clock.Advance(DeviceRetentionDays*24*time.Hour + time.Hour)
		removed, err := f.st.GCDevices(f.clock.Now(), DeviceRetentionDays)
		if err != nil || removed != 1 {
			t.Fatalf("GCDevices = %d, %v; want 1", removed, err)
		}
		if _, ok := f.st.DeviceRecord(material.DeviceID); ok {
			t.Fatal("expired record survived GC")
		}
		assertNoSlotBinding(t, f.st, key2)
		if _, err := f.devices.OpenID(material.DeviceID); err == nil {
			t.Fatal("expired material survived GC")
		}
		fresh, err := f.st.BindForLoginSlot(key2, testLinuxProfile)
		if err != nil {
			t.Fatalf("bind after GC: %v", err)
		}
		if fresh.DeviceID == material.DeviceID || fresh.Fingerprint == material.Fingerprint {
			t.Fatal("GC tombstone was resurrected")
		}
	})

	t.Run("import binds the exported slot", func(t *testing.T) {
		source := newPoolFixture(t)
		sourceAccount := source.createAccount(t, "matrix-import", "matrix-import-user")
		if _, err := source.st.EnsureAccountSlots(sourceAccount.ID, 2); err != nil {
			t.Fatalf("source EnsureAccountSlots: %v", err)
		}
		one, err := source.st.BindForLoginSlot(slotKey(sourceAccount.ID, 1), testLinuxProfile)
		if err != nil {
			t.Fatalf("source slot 1: %v", err)
		}
		two, err := source.st.BindForLoginSlot(slotKey(sourceAccount.ID, 2), testLinuxProfile)
		if err != nil {
			t.Fatalf("source slot 2: %v", err)
		}
		envelope, err := source.st.ExportDevicePool(DeviceExportOptions{
			Scope: "assigned", Passphrase: testDevicePassphrase,
		})
		if err != nil {
			t.Fatalf("ExportDevicePool: %v", err)
		}
		raw, err := json.Marshal(envelope)
		if err != nil {
			t.Fatalf("marshal envelope: %v", err)
		}

		target := newPoolFixture(t)
		targetAccount := target.createAccount(t, "matrix-import-2", "matrix-import-user")
		if _, err := target.st.EnsureAccountSlots(targetAccount.ID, 2); err != nil {
			t.Fatalf("target EnsureAccountSlots: %v", err)
		}
		result, err := target.st.ImportDevicePool(raw, DeviceImportOptions{
			Passphrase: testDevicePassphrase, Bind: true,
		})
		if err != nil {
			t.Fatalf("ImportDevicePool: %v", err)
		}
		if result.Bound != 2 {
			t.Fatalf("import bound = %d, want 2", result.Bound)
		}
		gotOne, err := target.st.AccountDeviceMaterialSlot(slotKey(targetAccount.ID, 1))
		if err != nil {
			t.Fatalf("target slot 1 material: %v", err)
		}
		gotTwo, err := target.st.AccountDeviceMaterialSlot(slotKey(targetAccount.ID, 2))
		if err != nil {
			t.Fatalf("target slot 2 material: %v", err)
		}
		if gotOne.DeviceID != one.DeviceID || gotTwo.DeviceID != two.DeviceID {
			t.Fatalf("imported ids = %q, %q; want %q, %q", gotOne.DeviceID, gotTwo.DeviceID, one.DeviceID, two.DeviceID)
		}
		if gotOne.DeviceID == gotTwo.DeviceID {
			t.Fatal("import bound one record to both slots")
		}
		assertRecordSlot(t, mustSlotRecord(t, target.st, one.DeviceID), slotKey(targetAccount.ID, 1))
		assertRecordSlot(t, mustSlotRecord(t, target.st, two.DeviceID), slotKey(targetAccount.ID, 2))
	})

	t.Run("assign rejects a second record on a bound slot", func(t *testing.T) {
		f := newPoolFixture(t)
		account := f.createAccount(t, "matrix-unique", "matrix-unique-user")
		if _, err := f.st.EnsureAccountSlots(account.ID, 2); err != nil {
			t.Fatalf("EnsureAccountSlots: %v", err)
		}
		records, err := f.st.GenerateDevices(DeviceGenerateInput{Count: 2, Profile: testLinuxProfile})
		if err != nil {
			t.Fatalf("GenerateDevices: %v", err)
		}
		key2 := slotKey(account.ID, 2)
		if _, err := f.st.AssignDeviceSlot(records[0].ID, key2); err != nil {
			t.Fatalf("first AssignDeviceSlot: %v", err)
		}
		if _, err := f.st.AssignDeviceSlot(records[1].ID, key2); !errors.Is(err, ErrConflict) ||
			DeviceErrorReason(err) != DeviceReasonAlreadyBound {
			t.Fatalf("second AssignDeviceSlot = %v, want DEVICE_ALREADY_BOUND", err)
		}
		if _, err := f.st.AssignDeviceSlot(records[0].ID, key2); !errors.Is(err, ErrConflict) ||
			DeviceErrorReason(err) != DeviceReasonStateInvalid {
			t.Fatalf("reassign = %v, want DEVICE_STATE_INVALID", err)
		}
		assertSlotBinding(t, f.st, key2, records[0].ID)
	})
}

// --- ambiguity and fail-closed material -----------------------------------

func TestRestoreAmbiguityPerSlot(t *testing.T) {
	f := newPoolFixture(t)
	cfg := DefaultDevicePoolConfig()
	cfg.RestoreMode = "manual"
	if err := f.st.ConfigureDevicePool(cfg); err != nil {
		t.Fatalf("ConfigureDevicePool manual: %v", err)
	}
	// Two slot-2 tombstones for the same account key.
	for i := range 2 {
		account := f.createAccount(t, "amb-"+string(rune('a'+i)), "amb-user")
		if _, err := f.st.EnsureAccountSlots(account.ID, 2); err != nil {
			t.Fatalf("EnsureAccountSlots: %v", err)
		}
		material, err := f.st.BindForLoginSlot(slotKey(account.ID, 2), testLinuxProfile)
		if err != nil {
			t.Fatalf("BindForLoginSlot: %v", err)
		}
		if _, err := f.st.MarkDeviceSeenSlot(slotKey(account.ID, 2), "amb-user"); err != nil {
			t.Fatalf("MarkDeviceSeenSlot: %v", err)
		}
		_ = material
		if err := f.st.DeleteAccount(account.ID); err != nil {
			t.Fatalf("DeleteAccount: %v", err)
		}
	}
	cfg.RestoreMode = "auto"
	if err := f.st.ConfigureDevicePool(cfg); err != nil {
		t.Fatalf("ConfigureDevicePool auto: %v", err)
	}
	account := f.createAccount(t, "amb-new", "amb-user")
	if _, err := f.st.EnsureAccountSlots(account.ID, 2); err != nil {
		t.Fatalf("EnsureAccountSlots new: %v", err)
	}
	// Slot 1 has no candidate: it must not consume either slot-2 tombstone.
	slotOne, err := f.st.BindForLoginSlot(slotKey(account.ID, 1), testLinuxProfile)
	if err != nil {
		t.Fatalf("slot 1 bind: %v", err)
	}
	if got := len(f.st.DevicePoolRecords()); got != 3 {
		t.Fatalf("pool records = %d, want 3", got)
	}

	_, err = f.st.BindForLoginSlot(slotKey(account.ID, 2), testLinuxProfile)
	if !errors.Is(err, ErrAmbiguousRestore) {
		t.Fatalf("ambiguous bind = %v, want ErrAmbiguousRestore", err)
	}
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("ambiguous bind is not a conflict: %v", err)
	}
	if DeviceErrorReason(err) != DeviceReasonRestoreAmbiguous {
		t.Fatalf("ambiguous reason = %q", DeviceErrorReason(err))
	}
	if _, err := f.st.AccountDeviceMaterialSlot(slotKey(account.ID, 2)); !errors.Is(err, ErrDeviceMaterialMissing) {
		t.Fatalf("ambiguous slot material = %v, want ErrDeviceMaterialMissing", err)
	}
	candidates, err := f.st.RestoreCandidatesSlot(slotKey(account.ID, 2), testLinuxProfile)
	if err != nil {
		t.Fatalf("RestoreCandidatesSlot: %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("slot 2 candidates = %d, want 2", len(candidates))
	}
	// The slot-1 record is a new identity, not one of the slot-2 tombstones.
	for _, candidate := range candidates {
		if slotOne.DeviceID == candidate.DeviceID {
			t.Fatal("slot 1 consumed a slot-2 tombstone")
		}
	}
	if _, err := f.st.RestoreDeviceSlot(candidates[0].DeviceID, slotKey(account.ID, 2)); err != nil {
		t.Fatalf("RestoreDeviceSlot: %v", err)
	}
	after, err := f.st.BindForLoginSlot(slotKey(account.ID, 2), testLinuxProfile)
	if err != nil {
		t.Fatalf("bind after manual restore: %v", err)
	}
	if after.DeviceID != candidates[0].DeviceID {
		t.Fatalf("bind after manual restore = %q, want %q", after.DeviceID, candidates[0].DeviceID)
	}
}

func TestSlotMissingMaterialFailsClosed(t *testing.T) {
	f := newPoolFixture(t)
	account := f.createAccount(t, "slot-missing", "slot-missing-user")
	if _, err := f.st.EnsureAccountSlots(account.ID, 2); err != nil {
		t.Fatalf("EnsureAccountSlots: %v", err)
	}
	key1 := slotKey(account.ID, 1)
	key2 := slotKey(account.ID, 2)
	one, err := f.st.BindForLoginSlot(key1, testLinuxProfile)
	if err != nil {
		t.Fatalf("slot 1 bind: %v", err)
	}
	two, err := f.st.BindForLoginSlot(key2, testLinuxProfile)
	if err != nil {
		t.Fatalf("slot 2 bind: %v", err)
	}
	if err := f.devices.Delete(two.DeviceID); err != nil {
		t.Fatalf("delete slot-2 material: %v", err)
	}
	fresh := f.reload(t)
	before := len(fresh.DevicePoolRecords())
	if _, err := fresh.AccountDeviceMaterialSlot(key2); !errors.Is(err, ErrDeviceMaterialMissing) {
		t.Fatalf("slot 2 material = %v, want ErrDeviceMaterialMissing", err)
	}
	if _, err := fresh.BindForLoginSlot(key2, testLinuxProfile); !errors.Is(err, ErrDeviceMaterialMissing) {
		t.Fatalf("slot 2 bind = %v, want ErrDeviceMaterialMissing", err)
	}
	if got := len(fresh.DevicePoolRecords()); got != before {
		t.Fatalf("pool grew from %d to %d after a fail-closed bind", before, got)
	}
	// Slot 1 is unaffected and still uses its original record.
	again, err := fresh.BindForLoginSlot(key1, testLinuxProfile)
	if err != nil {
		t.Fatalf("slot 1 bind after slot-2 failure: %v", err)
	}
	if again.DeviceID != one.DeviceID || again.Identity != one.Identity {
		t.Fatalf("slot 1 identity changed: %+v -> %+v", one, again)
	}
}
