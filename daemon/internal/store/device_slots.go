package store

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"ajsaccountd/internal/model"
)

// This file is MULTISLOT-PLAN step 2: per-slot device binding. The account
// level deviceBindings map stays as the slot-1 compatibility projection that
// older builds and the existing API read; deviceSlotBindings is the canonical
// all-slot index. Every mutation that touches a device record goes through the
// helpers below under deviceMu + s.mu, so the record, its slot row and both
// binding maps cannot disagree after an operation returns.
//
// No path in this file opens a session, starts a gate or calls a generator
// when a slot already has a DeviceID or a binding. Missing or corrupt material
// fails that slot closed; it never regenerates an identity.

// ErrAmbiguousRestore reports that a slot has more than one retired record
// matching its account key. The caller must choose one explicitly; automatic
// restore never guesses. It is also a conflict for the HTTP layer.
var ErrAmbiguousRestore = errors.New("store: device restore is ambiguous")

// AmbiguousRestoreError carries the slot and candidate ids of an ambiguous
// restore. Unwrap keeps errors.Is(err, ErrConflict) true for existing callers.
type AmbiguousRestoreError struct {
	Key        model.SlotKey
	Candidates []string
}

func (e *AmbiguousRestoreError) Error() string {
	return fmt.Sprintf("slot %s matches %d retired devices (%s); restore one explicitly",
		e.Key.String(), len(e.Candidates), strings.Join(e.Candidates, ","))
}

// Unwrap maps the error to the store's conflict kind.
func (e *AmbiguousRestoreError) Unwrap() error { return ErrConflict }

// Is makes both the typed sentinel and the conflict kind match.
func (e *AmbiguousRestoreError) Is(target error) bool {
	return target == ErrAmbiguousRestore || target == ErrConflict
}

// DeviceReason implements the stable reason interface used by the API layer.
func (e *AmbiguousRestoreError) DeviceReason() string { return DeviceReasonRestoreAmbiguous }

// cloneSlotKey copies the optional slot reference so a record can never share
// mutable storage with another record or with the caller.
func cloneSlotKey(key *model.SlotKey) *model.SlotKey {
	if key == nil {
		return nil
	}
	copied := *key
	return &copied
}

// accountSlotKey is the slot-1 compatibility key for one account.
func accountSlotKey(accountID string) model.SlotKey {
	return model.SlotKey{AccountID: accountID, Index: 1}
}

// boundRecordIDLocked returns the real device binding for a slot. Unlike
// slotBindingLocked it does not treat an arbitrary AccountSlot.DeviceID from a
// hand-written test or a partially migrated row as a binding.
func (s *Store) boundRecordIDLocked(key model.SlotKey) (string, bool) {
	if s.deviceSlotBindings != nil {
		if id := s.deviceSlotBindings[key]; id != "" {
			return id, true
		}
	}
	if key.Index == 1 {
		if id := s.deviceBindings[key.AccountID]; id != "" {
			return id, true
		}
	}
	return "", false
}

// slotBindingLocked resolves the record id a slot displays. The canonical map
// wins; the slot row and the legacy account map are fallbacks for snapshots
// written before this step.
func (s *Store) slotBindingLocked(key model.SlotKey) (string, bool) {
	if id, ok := s.boundRecordIDLocked(key); ok {
		return id, true
	}
	if slot, ok := s.accountSlots[key]; ok && slot.DeviceID != "" {
		return slot.DeviceID, true
	}
	return "", false
}

// SlotDeviceRecordID returns the device record the slot really owns. It is the
// identity a start opened its session with; the LIVE commit re-checks it so a
// release cannot slip between the material read and the write. Only a real
// binding counts, never a slot row's display copy.
func (s *Store) SlotDeviceRecordID(key model.SlotKey) (string, bool) {
	if !key.Valid() {
		return "", false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.boundRecordIDLocked(key)
}

// requireSlotDeviceLocked verifies that the slot still owns exactly the device
// record the caller opened its session with. It runs inside a commit's write
// lock, so the release it guards against cannot interleave with the LIVE write.
//
// An empty recordID is the pool-disabled legacy mode: no binding may exist,
// otherwise the caller opened its session with an identity the slot no longer
// owns. The caller holds the write lock.
func (s *Store) requireSlotDeviceLocked(key model.SlotKey, recordID string) error {
	boundID, bound := s.boundRecordIDLocked(key)
	if recordID == "" {
		if bound {
			return conflictf("slot %s is bound to device %q but the session used no device record",
				key.String(), boundID)
		}
		// Empty is the pool-disabled legacy identity, not a wildcard. A
		// pool-enabled slot that lost its binding (released after a drain, or
		// never bound) must not commit LIVE: the session in hand was opened
		// with an identity the slot no longer owns.
		if s.deviceSealer != nil {
			return conflictf("slot %s has no device binding; a pool-enabled slot cannot commit without one",
				key.String())
		}
		return nil
	}
	if !bound || boundID != recordID {
		return conflictf("device %q is no longer assigned to slot %s", recordID, key.String())
	}
	rec := s.deviceRecordLocked(recordID)
	if rec == nil || rec.State != DeviceAssigned || rec.AssignedAccountID != key.AccountID ||
		rec.AssignedSlotKey == nil || *rec.AssignedSlotKey != key {
		return conflictf("device %q is no longer assigned to slot %s", recordID, key.String())
	}
	return nil
}

// ensureSlotRowLocked materialises a provisioning row for a valid slot key.
func (s *Store) ensureSlotRowLocked(key model.SlotKey) {
	if s.accountSlots == nil {
		s.accountSlots = make(map[model.SlotKey]model.AccountSlot)
	}
	if _, ok := s.accountSlots[key]; ok {
		return
	}
	s.accountSlots[key] = model.AccountSlot{
		SlotKey: key, State: model.SlotProvisioning, Health: model.SlotHealthUnknown,
	}
}

// setSlotBindingLocked writes one binding atomically into the canonical map,
// the slot row and (for slot 1) the compatibility projection. The caller holds
// s.mu.
func (s *Store) setSlotBindingLocked(key model.SlotKey, recordID string) {
	if s.deviceSlotBindings == nil {
		s.deviceSlotBindings = make(map[model.SlotKey]string)
	}
	s.deviceSlotBindings[key] = recordID
	slot, ok := s.accountSlots[key]
	if !ok {
		slot = model.AccountSlot{SlotKey: key, State: model.SlotProvisioning, Health: model.SlotHealthUnknown}
	}
	slot.DeviceID = recordID
	s.accountSlots[key] = slot
	if key.Index == 1 {
		if s.deviceBindings == nil {
			s.deviceBindings = make(map[string]string)
		}
		s.deviceBindings[key.AccountID] = recordID
	}
}

// clearSlotBindingLocked removes one binding and the slot row's reference. It
// keeps the slot row itself; capacity reduction owns row deletion.
func (s *Store) clearSlotBindingLocked(key model.SlotKey) {
	delete(s.deviceSlotBindings, key)
	if slot, ok := s.accountSlots[key]; ok {
		slot.DeviceID = ""
		s.accountSlots[key] = slot
	}
	if key.Index == 1 {
		delete(s.deviceBindings, key.AccountID)
		delete(s.devices, key.AccountID)
	}
}

// deviceRecordBoundSlotLocked returns the slot a record is currently bound to,
// scanning the canonical map first and the compatibility projection second.
func (s *Store) deviceRecordBoundSlotLocked(recordID string) (model.SlotKey, bool) {
	for key, id := range s.deviceSlotBindings {
		if id == recordID {
			return key, true
		}
	}
	for accountID, id := range s.deviceBindings {
		if id == recordID {
			return accountSlotKey(accountID), true
		}
	}
	for key, slot := range s.accountSlots {
		if slot.DeviceID == recordID {
			return key, true
		}
	}
	return model.SlotKey{}, false
}

// deviceServingNodeSlotLocked reports whether the slot still has a node that
// can carry an authenticated session: LIVE, DRAINING (its gate may still be
// listening until the switch/cleanup closes it), or a start that has not
// committed yet. A DOWN or STALE row has no listener, so it does not pin the
// device. Slot 0 is the legacy single-slot spelling and belongs to slot 1.
func (s *Store) deviceServingNodeSlotLocked(key model.SlotKey) bool {
	for _, node := range s.nodes {
		if node.AccountID != key.AccountID {
			continue
		}
		if !slotNodeInService(node.State) {
			continue
		}
		if key.Index == 1 {
			if node.Slot == 0 || node.Slot == 1 {
				return true
			}
			continue
		}
		if node.Slot == key.Index {
			return true
		}
	}
	return false
}

// slotNodeInService reports whether a node state can still hold an
// authenticated vendor session or its listener. DRAINING is deliberately
// included: it only stops new flows and is not proof the gate closed.
func slotNodeInService(state model.NodeState) bool {
	switch state {
	case model.NodeLive, model.NodeDraining, model.NodeStarting, model.NodeReady:
		return true
	default:
		return false
	}
}

// deviceRecordMatchesSlot reports whether a retired record belongs to the
// requested slot. Legacy records without a slot key were all slot 1.
func deviceRecordMatchesSlot(rec DeviceRecord, index int) bool {
	if rec.AssignedSlotKey == nil {
		return index == 1
	}
	return rec.AssignedSlotKey.Index == index
}

// Validate checks the identity and slot-binding invariants of one record. The
// snapshot validator applies the same rules; the method exists so tests and
// future store callers can reject a bad record before writing it.
func (rec DeviceRecord) Validate() error {
	if !deviceIDPattern.MatchString(rec.ID) {
		return fmt.Errorf("device id %q is malformed", rec.ID)
	}
	if !validDeviceState(rec.State) {
		return fmt.Errorf("device %q has unknown state %q", rec.ID, rec.State)
	}
	if rec.AssignedSlotKey != nil {
		if !rec.AssignedSlotKey.Valid() {
			return fmt.Errorf("device %q has invalid assigned slot key %q", rec.ID, rec.AssignedSlotKey.String())
		}
		switch rec.State {
		case DeviceAssigned:
			if rec.AssignedAccountID == "" {
				return fmt.Errorf("device %q is assigned without an account", rec.ID)
			}
			if rec.AssignedSlotKey.AccountID != rec.AssignedAccountID {
				return fmt.Errorf("device %q slot key names account %q, record names %q",
					rec.ID, rec.AssignedSlotKey.AccountID, rec.AssignedAccountID)
			}
		case DeviceRetired:
			// A retired record keeps its last slot as restore affinity; the
			// account id inside the key may refer to the deleted account.
		default:
			return fmt.Errorf("device %q is %s but still carries a slot key", rec.ID, rec.State)
		}
	} else if rec.State == DeviceAssigned {
		return fmt.Errorf("device %q is assigned without an assigned slot", rec.ID)
	}
	if rec.State != DeviceAssigned && rec.AssignedAccountID != "" {
		return fmt.Errorf("device %q is %s but still names account %q", rec.ID, rec.State, rec.AssignedAccountID)
	}
	return nil
}

// BindForLoginSlot returns the sealed material a slot must present. It is
// first-writer-wins per slot: retries, restarts and concurrent first logins
// get the same record. A slot with a DeviceID or a binding whose record or
// material is missing fails closed; it is never regenerated.
func (s *Store) BindForLoginSlot(key model.SlotKey, profileName string) (DeviceMaterial, error) {
	if s.deviceSealer == nil {
		return DeviceMaterial{}, devicePoolDisabled()
	}
	if !key.Valid() {
		return DeviceMaterial{}, deviceInvalidf(DeviceReasonStateInvalid,
			"slot key %q is invalid", key.String())
	}
	s.deviceMu.Lock()
	defer s.deviceMu.Unlock()

	// Fast path: the slot already has a binding. Read everything under one
	// read lock, then open the material outside it (a local file read).
	s.mu.RLock()
	idx := s.accountIndexLocked(key.AccountID)
	if idx < 0 {
		s.mu.RUnlock()
		return DeviceMaterial{}, notFoundf("account %q not found", key.AccountID)
	}
	account := cloneAccount(s.accounts[idx])
	recID, bound := s.slotBindingLocked(key)
	var recCopy DeviceRecord
	var material DeviceMaterial
	var cached bool
	if bound {
		if rec := s.deviceRecordLocked(recID); rec != nil {
			recCopy = cloneDeviceRecord(*rec)
			material, cached = s.deviceMaterial[recID]
		}
	}
	cfg := s.deviceCfg
	s.mu.RUnlock()

	if bound {
		if recCopy.ID == "" {
			return DeviceMaterial{}, deviceMaterialMissingf(
				"slot %s is bound to a missing device record", key.String())
		}
		if recCopy.State != DeviceAssigned || recCopy.AssignedAccountID != key.AccountID {
			return DeviceMaterial{}, deviceMaterialMissingf(
				"slot %s record %q is not assigned to the slot's account", key.String(), recID)
		}
		if recCopy.AssignedSlotKey != nil && *recCopy.AssignedSlotKey != key {
			return DeviceMaterial{}, deviceMaterialMissingf(
				"slot %s record %q belongs to slot %s", key.String(), recID, recCopy.AssignedSlotKey.String())
		}
		if recCopy.AssignedSlotKey == nil && key.Index != 1 {
			return DeviceMaterial{}, deviceMaterialMissingf(
				"slot %s record %q has no slot binding", key.String(), recID)
		}
		if _, known := DeviceProfileByName(recCopy.Profile); !known {
			return DeviceMaterial{}, deviceInvalidf(DeviceReasonProfileMismatch,
				"device %q uses unavailable profile %q", recID, recCopy.Profile)
		}
		if cached {
			if material.AssignedSlotKey != nil && material.AssignedSlotKey.Index != key.Index {
				return DeviceMaterial{}, deviceMaterialMissingf(
					"device %q material belongs to slot %s", recID, material.AssignedSlotKey.String())
			}
			if material.AssignedSlotKey == nil && key.Index != 1 {
				return DeviceMaterial{}, deviceMaterialMissingf(
					"device %q material has no slot binding for %s", recID, key.String())
			}
		} else {
			opened, err := s.openDeviceMaterial(recID)
			if err != nil {
				return DeviceMaterial{}, deviceMaterialMissingf(
					"device %q material is missing or corrupt", recID)
			}
			if opened.Fingerprint != recCopy.Fingerprint {
				return DeviceMaterial{}, deviceMaterialMissingf(
					"device %q material fingerprint does not match the record", recID)
			}
			if opened.AssignedSlotKey != nil && opened.AssignedSlotKey.Index != key.Index {
				return DeviceMaterial{}, deviceMaterialMissingf(
					"device %q material belongs to slot %s", recID, opened.AssignedSlotKey.String())
			}
			if opened.AssignedSlotKey == nil && key.Index != 1 {
				return DeviceMaterial{}, deviceMaterialMissingf(
					"device %q material has no slot binding for %s", recID, key.String())
			}
			material = opened
		}
		if recCopy.PresentedAt == nil {
			now := iso(s.now())
			recCopy.PresentedAt = &now
			material.PresentedAt = &now
			if err := s.sealDeviceMaterial(material); err != nil {
				return DeviceMaterial{}, err
			}
		}
		s.mu.Lock()
		if current := s.deviceRecordLocked(recID); current != nil {
			boundID, bound := s.boundRecordIDLocked(key)
			if !bound {
				// The slot row was the only reference (step-1 migration or a
				// hand-edited row). Adopt it into the canonical map now.
				if slotID, ok := s.slotBindingLocked(key); ok && slotID == recID {
					s.setSlotBindingLocked(key, recID)
					boundID, bound = recID, true
				}
			}
			if bound && boundID == recID {
				if current.AssignedSlotKey == nil {
					current.AssignedSlotKey = cloneSlotKey(&key)
				}
				current.PresentedAt = cloneStringPtr(recCopy.PresentedAt)
				s.deviceMaterial[recID] = cloneDeviceMaterial(material)
			}
		}
		s.mu.Unlock()
		return cloneDeviceMaterial(material), nil
	}

	// No binding: resolve the profile and look for a tombstone that belongs to
	// this exact slot.
	resolvedName := strings.TrimSpace(profileName)
	if resolvedName == "" {
		resolvedName = DevicePoolDefaultProfile
	}
	profile, ok := DeviceProfileByName(resolvedName)
	if !ok {
		return DeviceMaterial{}, deviceInvalidf(DeviceReasonProfileMismatch,
			"unknown device profile %q", resolvedName)
	}
	keys := []string{
		deviceAccountKey(profile.Family, account.Username),
		deviceAccountKey(profile.Family, strings.ToLower(account.Username)),
	}

	s.mu.RLock()
	candidates := s.restoreCandidatesForSlotLocked(keys, profile.Family, key.Index)
	s.mu.RUnlock()

	if cfg.RestoreMode != "off" && len(candidates) > 1 {
		ids := make([]string, 0, len(candidates))
		for _, candidate := range candidates {
			ids = append(ids, candidate.ID)
		}
		s.mu.Lock()
		s.addAuditLocked(model.SeverityWarn, "system", "device.restore_ambiguous", "pool",
			fmt.Sprintf("slot=%s candidates=%d", key.String(), len(candidates)))
		s.mu.Unlock()
		return DeviceMaterial{}, &AmbiguousRestoreError{Key: key, Candidates: ids}
	}
	if cfg.RestoreMode == "auto" && len(candidates) == 1 {
		return s.bindRestoredDeviceSlot(key, candidates[0].ID, keys, account.Label)
	}

	// Preallocated mode draws an available record before generating.
	if cfg.Mode == "preallocated" {
		s.mu.RLock()
		var chosen *DeviceRecord
		for _, rec := range s.devicePool {
			if rec.State != DeviceAvailable || rec.Platform != profile.Family || rec.PresentedAt != nil {
				continue
			}
			if chosen == nil || rec.CreatedAt < chosen.CreatedAt ||
				(rec.CreatedAt == chosen.CreatedAt && rec.ID < chosen.ID) {
				chosen = rec
			}
		}
		if chosen != nil {
			copy := cloneDeviceRecord(*chosen)
			s.mu.RUnlock()
			return s.bindAvailableDeviceSlot(key, copy.ID, account.Label)
		}
		s.mu.RUnlock()
		if cfg.Strict {
			return DeviceMaterial{}, deviceConflictf(DeviceReasonPoolEmpty,
				"device pool has no available %s record", profile.Family)
		}
	}

	generator, ok := DeviceGeneratorByName(cfg.Algorithm)
	if !ok {
		return DeviceMaterial{}, deviceInvalidf(DeviceReasonAlgorithmUnknown,
			"unknown device algorithm %q", cfg.Algorithm)
	}
	generated, err := generator.Generate(DeviceGenerateSpec{
		Profile: profile, Seed: cfg.Seed, DimMode: cfg.DimMode,
	})
	if err != nil {
		return DeviceMaterial{}, err
	}
	now := iso(s.now())
	s.mu.RLock()
	generated.DeviceID = s.newDeviceIDLocked()
	s.mu.RUnlock()
	if generated.DeviceID == "" {
		return DeviceMaterial{}, fmt.Errorf("store: device id generation failed")
	}
	generated.CreatedAt = now
	generated.PresentedAt = &now
	generated.AccountKey = keys[0]
	generated.AccountKeyAliases = appendDeviceAlias(nil, keys[1])
	generated.AssignedSlotKey = cloneSlotKey(&key)
	if err := s.sealDeviceMaterial(generated); err != nil {
		return DeviceMaterial{}, err
	}

	s.mu.Lock()
	if s.accountIndexLocked(key.AccountID) < 0 {
		s.mu.Unlock()
		_ = s.deleteDeviceMaterial(generated.DeviceID)
		return DeviceMaterial{}, notFoundf("account %q not found", key.AccountID)
	}
	if _, ok := s.slotBindingLocked(key); ok {
		// Defensive: deviceMu serialises binders, so this is unreachable.
		s.mu.Unlock()
		_ = s.deleteDeviceMaterial(generated.DeviceID)
		return s.AccountDeviceMaterialSlot(key)
	}
	rec := DeviceRecord{
		ID: generated.DeviceID, State: DeviceAssigned, Platform: profile.Family,
		Profile: profile.Name, Algorithm: generated.Algorithm,
		Fingerprint: generated.Fingerprint,
		Hashes:      deviceHashes(generated.Identity, generated.MachineCode),
		Claims:      deviceClaims(generated.Identity),
		AccountKey:  keys[0], AccountKeyAliases: appendDeviceAlias(nil, keys[1]),
		AssignedAccountID: key.AccountID, AssignedSlotKey: cloneSlotKey(&key),
		AssignedAt: &now, PresentedAt: &now, CreatedAt: now, CreatedBy: "system",
	}
	if err := s.deviceRecordUniqueLocked(&rec, nil); err != nil {
		s.mu.Unlock()
		_ = s.deleteDeviceMaterial(generated.DeviceID)
		return DeviceMaterial{}, err
	}
	s.ensureSlotRowLocked(key)
	s.devicePool[rec.ID] = &rec
	s.deviceMaterial[rec.ID] = cloneDeviceMaterial(generated)
	s.setSlotBindingLocked(key, rec.ID)
	if key.Index == 1 {
		s.devices[key.AccountID] = generated.Identity
	}
	s.deviceAppendEventLocked(&rec, "created", "system",
		"algorithm="+generated.Algorithm+" profile="+profile.Name+" slot="+key.String())
	s.deviceAppendEventLocked(&rec, "assigned", "system", "source=login slot="+key.String())
	s.addAuditLocked(model.SeverityInfo, "system", "device.assign", rec.ID,
		"account="+account.Label+" source=login slot="+key.String())
	s.mu.Unlock()
	return cloneDeviceMaterial(generated), nil
}

// bindRestoredDeviceSlot binds one tombstone selected by automatic restore to
// an exact slot.
func (s *Store) bindRestoredDeviceSlot(key model.SlotKey, deviceID string, keys []string, label string) (DeviceMaterial, error) {
	s.mu.RLock()
	rec := s.deviceRecordLocked(deviceID)
	if rec == nil {
		s.mu.RUnlock()
		return DeviceMaterial{}, deviceNotFoundf("device %q not found", deviceID)
	}
	recCopy := cloneDeviceRecord(*rec)
	material, cached := s.deviceMaterial[deviceID]
	s.mu.RUnlock()
	if recCopy.AssignedSlotKey != nil && recCopy.AssignedSlotKey.Index != key.Index {
		return DeviceMaterial{}, deviceConflictf(DeviceReasonStateInvalid,
			"device %q belongs to slot %s, not %s", deviceID, recCopy.AssignedSlotKey.String(), key.String())
	}
	if recCopy.AssignedSlotKey == nil && key.Index != 1 {
		return DeviceMaterial{}, deviceConflictf(DeviceReasonStateInvalid,
			"legacy device %q has no slot affinity for %s", deviceID, key.String())
	}
	if _, known := DeviceProfileByName(recCopy.Profile); !known {
		return DeviceMaterial{}, deviceInvalidf(DeviceReasonProfileMismatch,
			"device %q uses unavailable profile %q", deviceID, recCopy.Profile)
	}
	if !cached {
		opened, err := s.openDeviceMaterial(deviceID)
		if err != nil {
			return DeviceMaterial{}, deviceMaterialMissingf("device %q material is missing or corrupt", deviceID)
		}
		if opened.Fingerprint != recCopy.Fingerprint {
			return DeviceMaterial{}, deviceMaterialMissingf("device %q material fingerprint does not match the record", deviceID)
		}
		material = opened
	}
	if material.AssignedSlotKey != nil && material.AssignedSlotKey.Index != key.Index {
		return DeviceMaterial{}, deviceConflictf(DeviceReasonStateInvalid,
			"device %q material belongs to slot %s", deviceID, material.AssignedSlotKey.String())
	}
	if material.AssignedSlotKey == nil && key.Index != 1 {
		return DeviceMaterial{}, deviceConflictf(DeviceReasonStateInvalid,
			"device %q material has no slot binding for %s", deviceID, key.String())
	}
	now := iso(s.now())
	if material.PresentedAt == nil {
		material.PresentedAt = &now
	}
	material.AssignedSlotKey = cloneSlotKey(&key)
	if err := s.sealDeviceMaterial(material); err != nil {
		return DeviceMaterial{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.deviceRecordLocked(deviceID)
	if current == nil || current.State != DeviceRetired || current.AssignedAccountID != "" {
		return DeviceMaterial{}, deviceConflictf(DeviceReasonStateInvalid,
			"device %q changed while it was being restored", deviceID)
	}
	if boundID, ok := s.boundRecordIDLocked(key); ok {
		return DeviceMaterial{}, deviceConflictf(DeviceReasonAlreadyBound,
			"slot %s is already bound to device %q", key.String(), boundID)
	}
	if !deviceRecordHasKey(current, keys) {
		return DeviceMaterial{}, deviceConflictf(DeviceReasonStateInvalid,
			"device %q was retired for a different account key", deviceID)
	}
	current.State = DeviceAssigned
	current.AssignedAccountID = key.AccountID
	current.AssignedSlotKey = cloneSlotKey(&key)
	current.AssignedAt = &now
	current.PresentedAt = cloneStringPtr(material.PresentedAt)
	current.RetiredAt = nil
	current.RetiredReason = ""
	s.ensureSlotRowLocked(key)
	s.setSlotBindingLocked(key, current.ID)
	s.deviceMaterial[current.ID] = cloneDeviceMaterial(material)
	if key.Index == 1 {
		s.devices[key.AccountID] = material.Identity
	}
	s.deviceAppendEventLocked(current, "restored", "system", "candidate=auto slot="+key.String())
	s.addAuditLocked(model.SeverityInfo, "system", "device.restore", current.ID,
		"account="+label+" candidate=auto slot="+key.String())
	return cloneDeviceMaterial(material), nil
}

// bindAvailableDeviceSlot binds a preallocated record at a slot's first login.
func (s *Store) bindAvailableDeviceSlot(key model.SlotKey, deviceID, label string) (DeviceMaterial, error) {
	s.mu.RLock()
	rec := s.deviceRecordLocked(deviceID)
	if rec == nil {
		s.mu.RUnlock()
		return DeviceMaterial{}, deviceNotFoundf("device %q not found", deviceID)
	}
	recCopy := cloneDeviceRecord(*rec)
	idx := s.accountIndexLocked(key.AccountID)
	username := ""
	if idx >= 0 {
		username = s.accounts[idx].Username
	}
	material, cached := s.deviceMaterial[deviceID]
	s.mu.RUnlock()
	if idx < 0 {
		return DeviceMaterial{}, notFoundf("account %q not found", key.AccountID)
	}
	if !cached {
		opened, err := s.openDeviceMaterial(deviceID)
		if err != nil {
			return DeviceMaterial{}, deviceMaterialMissingf("device %q material is missing or corrupt", deviceID)
		}
		material = opened
	}
	if material.Fingerprint != recCopy.Fingerprint {
		return DeviceMaterial{}, deviceMaterialMissingf("device %q material fingerprint does not match the record", deviceID)
	}
	now := iso(s.now())
	accountKey := deviceAccountKey(recCopy.Platform, username)
	alias := deviceAccountKey(recCopy.Platform, strings.ToLower(username))
	material.PresentedAt = &now
	material.AccountKey = accountKey
	material.AccountKeyAliases = appendDeviceAlias(material.AccountKeyAliases, alias)
	material.AssignedSlotKey = cloneSlotKey(&key)
	if err := s.sealDeviceMaterial(material); err != nil {
		return DeviceMaterial{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.deviceRecordLocked(deviceID)
	if current == nil || current.State != DeviceAvailable || current.PresentedAt != nil {
		return DeviceMaterial{}, deviceConflictf(DeviceReasonStateInvalid,
			"device %q changed while it was being assigned", deviceID)
	}
	if boundID, ok := s.boundRecordIDLocked(key); ok {
		return DeviceMaterial{}, deviceConflictf(DeviceReasonAlreadyBound,
			"slot %s is already bound to device %q", key.String(), boundID)
	}
	idx = s.accountIndexLocked(key.AccountID)
	if idx < 0 {
		return DeviceMaterial{}, notFoundf("account %q not found", key.AccountID)
	}
	current.State = DeviceAssigned
	current.AssignedAccountID = key.AccountID
	current.AssignedSlotKey = cloneSlotKey(&key)
	current.AssignedAt = &now
	current.PresentedAt = &now
	current.AccountKey = accountKey
	current.AccountKeyAliases = appendDeviceAlias(current.AccountKeyAliases, alias)
	s.ensureSlotRowLocked(key)
	s.setSlotBindingLocked(key, current.ID)
	s.deviceMaterial[current.ID] = cloneDeviceMaterial(material)
	if key.Index == 1 {
		s.devices[key.AccountID] = material.Identity
	}
	s.deviceAppendEventLocked(current, "assigned", "system", "source=pool slot="+key.String())
	s.addAuditLocked(model.SeverityInfo, "system", "device.assign", current.ID,
		"account="+label+" source=pool slot="+key.String())
	return cloneDeviceMaterial(material), nil
}

// AccountDeviceMaterialSlot returns the sealed identity bound to one exact
// slot. A missing record or material is ErrDeviceMaterialMissing; it never
// falls back to another slot's identity.
func (s *Store) AccountDeviceMaterialSlot(key model.SlotKey) (DeviceMaterial, error) {
	if !key.Valid() {
		return DeviceMaterial{}, deviceInvalidf(DeviceReasonStateInvalid,
			"slot key %q is invalid", key.String())
	}
	s.mu.RLock()
	recID, bound := s.slotBindingLocked(key)
	cachedMaterial, cached := s.deviceMaterial[recID]
	var recCopy DeviceRecord
	if bound {
		if rec := s.deviceRecordLocked(recID); rec != nil {
			recCopy = cloneDeviceRecord(*rec)
		}
	}
	s.mu.RUnlock()
	if !bound {
		return DeviceMaterial{}, deviceMaterialMissingf("slot %s has no device binding", key.String())
	}
	if recCopy.ID == "" {
		return DeviceMaterial{}, deviceMaterialMissingf("slot %s is bound to a missing device record", key.String())
	}
	if recCopy.State != DeviceAssigned || recCopy.AssignedAccountID != key.AccountID {
		return DeviceMaterial{}, deviceMaterialMissingf("slot %s record %q is not assigned", key.String(), recID)
	}
	if recCopy.AssignedSlotKey != nil && *recCopy.AssignedSlotKey != key {
		return DeviceMaterial{}, deviceMaterialMissingf("slot %s record %q belongs to slot %s",
			key.String(), recID, recCopy.AssignedSlotKey.String())
	}
	if recCopy.AssignedSlotKey == nil && key.Index != 1 {
		return DeviceMaterial{}, deviceMaterialMissingf("slot %s record %q has no slot binding",
			key.String(), recID)
	}
	if cached {
		if cachedMaterial.AssignedSlotKey != nil && cachedMaterial.AssignedSlotKey.Index != key.Index {
			return DeviceMaterial{}, deviceMaterialMissingf("slot %s material belongs to slot %s",
				key.String(), cachedMaterial.AssignedSlotKey.String())
		}
		if cachedMaterial.AssignedSlotKey == nil && key.Index != 1 {
			return DeviceMaterial{}, deviceMaterialMissingf("slot %s material has no slot binding",
				key.String())
		}
		return cloneDeviceMaterial(cachedMaterial), nil
	}
	opened, err := s.openDeviceMaterial(recID)
	if err != nil {
		return DeviceMaterial{}, deviceMaterialMissingf("device %q material is missing or corrupt", recID)
	}
	if opened.Fingerprint != recCopy.Fingerprint {
		return DeviceMaterial{}, deviceMaterialMissingf("device %q material fingerprint does not match the record", recID)
	}
	if opened.AssignedSlotKey != nil && opened.AssignedSlotKey.Index != key.Index {
		return DeviceMaterial{}, deviceMaterialMissingf("device %q material belongs to slot %s",
			recID, opened.AssignedSlotKey.String())
	}
	s.mu.Lock()
	s.deviceMaterial[recID] = cloneDeviceMaterial(opened)
	s.mu.Unlock()
	return cloneDeviceMaterial(opened), nil
}

// AccountDeviceSlot returns the in-memory identity of one slot. It is a
// non-secret mirror used by the login path; a missing material returns false.
func (s *Store) AccountDeviceSlot(key model.SlotKey) (DeviceIdentity, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if key.Index == 1 {
		if dev, ok := s.devices[key.AccountID]; ok {
			return dev, true
		}
	}
	if recID, ok := s.slotBindingLocked(key); ok {
		if material, ok := s.deviceMaterial[recID]; ok {
			return material.Identity, true
		}
	}
	return DeviceIdentity{}, false
}

// MarkDeviceSeenSlot records a successful login for one slot's record.
func (s *Store) MarkDeviceSeenSlot(key model.SlotKey, username string) (DeviceRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.accountIndexLocked(key.AccountID) < 0 {
		return DeviceRecord{}, notFoundf("account %q not found", key.AccountID)
	}
	recID, ok := s.slotBindingLocked(key)
	if !ok {
		return DeviceRecord{}, deviceNotFoundf("slot %s has no device binding", key.String())
	}
	rec := s.deviceRecordLocked(recID)
	if rec == nil {
		return DeviceRecord{}, deviceNotFoundf("device %q not found", recID)
	}
	s.markDeviceSeenSlotLocked(key, username)
	return cloneDeviceRecord(*rec), nil
}

// markDeviceSeenSlotLocked updates one slot's bound record. The caller holds
// the write lock.
func (s *Store) markDeviceSeenSlotLocked(key model.SlotKey, username string) {
	recID, ok := s.slotBindingLocked(key)
	if !ok {
		return
	}
	rec := s.deviceRecordLocked(recID)
	if rec == nil {
		return
	}
	now := iso(s.now())
	if rec.FirstSeenAt == nil {
		rec.FirstSeenAt = &now
	}
	rec.LastSeenAt = &now
	rec.LoginCount++
	username = strings.TrimSpace(username)
	if username == "" {
		return
	}
	exact := deviceAccountKey(rec.Platform, username)
	lower := deviceAccountKey(rec.Platform, strings.ToLower(username))
	if rec.AccountKey == "" {
		rec.AccountKey = exact
	}
	rec.AccountKeyAliases = appendDeviceAlias(rec.AccountKeyAliases, exact)
	rec.AccountKeyAliases = appendDeviceAlias(rec.AccountKeyAliases, lower)
}

// restoreCandidatesForSlotLocked returns matching tombstones for one slot
// index, newest retirement first. The account key decides ownership; the slot
// index decides which slot may restore the record.
func (s *Store) restoreCandidatesForSlotLocked(keys []string, family model.Platform, index int) []DeviceRecord {
	out := []DeviceRecord{}
	now := s.now()
	for _, rec := range s.devicePool {
		if rec.State != DeviceRetired || rec.AssignedAccountID != "" {
			continue
		}
		if rec.Platform != family || !deviceRecordHasKey(rec, keys) {
			continue
		}
		if !deviceRecordMatchesSlot(*rec, index) {
			continue
		}
		if deviceExpired(*rec, now, s.deviceCfg.RetentionDays) {
			continue
		}
		out = append(out, cloneDeviceRecord(*rec))
	}
	sort.Slice(out, func(i, j int) bool {
		left, right := "", ""
		if out[i].RetiredAt != nil {
			left = *out[i].RetiredAt
		}
		if out[j].RetiredAt != nil {
			right = *out[j].RetiredAt
		}
		if left != right {
			return left > right
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// RestoreCandidatesSlot lists retired records matching one slot. It performs
// no mutation and returns all candidates so an operator can resolve ambiguity.
func (s *Store) RestoreCandidatesSlot(key model.SlotKey, profileName string) ([]RestoreCandidate, error) {
	if !key.Valid() {
		return nil, deviceInvalidf(DeviceReasonStateInvalid, "slot key %q is invalid", key.String())
	}
	profileName = strings.TrimSpace(profileName)
	if profileName == "" {
		profileName = DevicePoolDefaultProfile
	}
	profile, ok := DeviceProfileByName(profileName)
	if !ok {
		return nil, deviceInvalidf(DeviceReasonProfileMismatch, "unknown device profile %q", profileName)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	idx := s.accountIndexLocked(key.AccountID)
	if idx < 0 {
		return nil, notFoundf("account %q not found", key.AccountID)
	}
	if _, bound := s.slotBindingLocked(key); bound {
		return nil, nil
	}
	keys := []string{
		deviceAccountKey(profile.Family, s.accounts[idx].Username),
		deviceAccountKey(profile.Family, strings.ToLower(s.accounts[idx].Username)),
	}
	records := s.restoreCandidatesForSlotLocked(keys, profile.Family, key.Index)
	out := make([]RestoreCandidate, 0, len(records))
	for _, rec := range records {
		candidate := RestoreCandidate{
			DeviceID: rec.ID, Fingerprint: rec.Fingerprint, Profile: rec.Profile,
			RetiredReason: rec.RetiredReason,
		}
		if rec.RetiredAt != nil {
			candidate.RetiredAt = *rec.RetiredAt
		}
		out = append(out, candidate)
	}
	return out, nil
}

// RestoreDeviceSlot binds a specific retired record to one exact slot. It is
// the operator's resolution for an ambiguous automatic match.
func (s *Store) RestoreDeviceSlot(deviceID string, key model.SlotKey) (DeviceRecord, error) {
	if !key.Valid() {
		return DeviceRecord{}, deviceInvalidf(DeviceReasonStateInvalid, "slot key %q is invalid", key.String())
	}
	s.deviceMu.Lock()
	defer s.deviceMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()

	rec := s.deviceRecordLocked(deviceID)
	if rec == nil {
		return DeviceRecord{}, deviceNotFoundf("device %q not found", deviceID)
	}
	if s.accountIndexLocked(key.AccountID) < 0 {
		return DeviceRecord{}, notFoundf("account %q not found", key.AccountID)
	}
	if boundID, ok := s.slotBindingLocked(key); ok {
		return DeviceRecord{}, deviceConflictf(DeviceReasonAlreadyBound,
			"slot %s is already bound to device %q", key.String(), boundID)
	}
	if rec.State != DeviceRetired {
		return DeviceRecord{}, deviceConflictf(DeviceReasonStateInvalid,
			"device %q is %s and cannot be restored", deviceID, rec.State)
	}
	if rec.AssignedSlotKey != nil && rec.AssignedSlotKey.Index != key.Index {
		return DeviceRecord{}, deviceConflictf(DeviceReasonStateInvalid,
			"device %q belongs to slot %s, not %s", deviceID, rec.AssignedSlotKey.String(), key.String())
	}
	if rec.AssignedSlotKey == nil && key.Index != 1 {
		return DeviceRecord{}, deviceConflictf(DeviceReasonStateInvalid,
			"legacy device %q has no slot affinity for %s", deviceID, key.String())
	}
	if !s.deviceAccountMatchesLocked(rec, key.AccountID) {
		return DeviceRecord{}, deviceConflictf(DeviceReasonStateInvalid,
			"device %q was retired for a different account key", deviceID)
	}
	if deviceExpired(*rec, s.now(), s.deviceCfg.RetentionDays) {
		return DeviceRecord{}, deviceConflictf(DeviceReasonStateInvalid,
			"device %q is past its retention window", deviceID)
	}
	material, err := s.deviceMaterialLocked(rec)
	if err != nil {
		return DeviceRecord{}, err
	}
	now := iso(s.now())
	material.AssignedSlotKey = cloneSlotKey(&key)
	if err := s.sealDeviceMaterial(material); err != nil {
		return DeviceRecord{}, err
	}
	rec.State = DeviceAssigned
	rec.AssignedAccountID = key.AccountID
	rec.AssignedSlotKey = cloneSlotKey(&key)
	rec.AssignedAt = &now
	rec.RetiredAt = nil
	rec.RetiredReason = ""
	s.ensureSlotRowLocked(key)
	s.setSlotBindingLocked(key, rec.ID)
	s.deviceMaterial[rec.ID] = cloneDeviceMaterial(material)
	if key.Index == 1 {
		s.devices[key.AccountID] = material.Identity
	}
	s.deviceAppendEventLocked(rec, "restored", "operator", "source=manual slot="+key.String())
	s.addAuditLocked(model.SeverityInfo, "operator", "device.restore", rec.ID,
		"account="+s.deviceAccountLabelLocked(key.AccountID)+" candidate=manual slot="+key.String())
	return cloneDeviceRecord(*rec), nil
}

// AssignDeviceSlot binds an available record, or a retired record that
// belongs to the same account key and slot, without marking it presented.
func (s *Store) AssignDeviceSlot(deviceID string, key model.SlotKey) (DeviceRecord, error) {
	if !key.Valid() {
		return DeviceRecord{}, deviceInvalidf(DeviceReasonStateInvalid, "slot key %q is invalid", key.String())
	}
	s.deviceMu.Lock()
	defer s.deviceMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()

	rec := s.deviceRecordLocked(deviceID)
	if rec == nil {
		return DeviceRecord{}, deviceNotFoundf("device %q not found", deviceID)
	}
	if s.accountIndexLocked(key.AccountID) < 0 {
		return DeviceRecord{}, notFoundf("account %q not found", key.AccountID)
	}
	if rec.State == DeviceAssigned {
		return DeviceRecord{}, deviceConflictf(DeviceReasonStateInvalid,
			"device %q is already assigned", deviceID)
	}
	if boundKey, ok := s.deviceRecordBoundSlotLocked(deviceID); ok {
		return DeviceRecord{}, deviceConflictf(DeviceReasonAlreadyBound,
			"device %q is already bound to slot %s", deviceID, boundKey.String())
	}
	if boundID, ok := s.slotBindingLocked(key); ok {
		return DeviceRecord{}, deviceConflictf(DeviceReasonAlreadyBound,
			"slot %s is already bound to device %q", key.String(), boundID)
	}
	switch rec.State {
	case DeviceAvailable:
		if rec.PresentedAt != nil {
			return DeviceRecord{}, deviceConflictf(DeviceReasonStateInvalid,
				"device %q has been presented to the vendor and cannot be reassigned", deviceID)
		}
	case DeviceRetired:
		if !s.deviceAccountMatchesLocked(rec, key.AccountID) {
			return DeviceRecord{}, deviceConflictf(DeviceReasonStateInvalid,
				"device %q was retired for a different account key", deviceID)
		}
		if rec.AssignedSlotKey != nil && rec.AssignedSlotKey.Index != key.Index {
			return DeviceRecord{}, deviceConflictf(DeviceReasonStateInvalid,
				"device %q belongs to slot %s, not %s", deviceID, rec.AssignedSlotKey.String(), key.String())
		}
		if rec.AssignedSlotKey == nil && key.Index != 1 {
			return DeviceRecord{}, deviceConflictf(DeviceReasonStateInvalid,
				"legacy device %q has no slot affinity for %s", deviceID, key.String())
		}
		if deviceExpired(*rec, s.now(), s.deviceCfg.RetentionDays) {
			return DeviceRecord{}, deviceConflictf(DeviceReasonStateInvalid,
				"device %q is past its retention window", deviceID)
		}
	default:
		return DeviceRecord{}, deviceConflictf(DeviceReasonStateInvalid,
			"device %q is %s and cannot be assigned", deviceID, rec.State)
	}
	material, err := s.deviceMaterialLocked(rec)
	if err != nil {
		return DeviceRecord{}, err
	}
	now := iso(s.now())
	idx := s.accountIndexLocked(key.AccountID)
	accountKey := deviceAccountKey(rec.Platform, s.accounts[idx].Username)
	alias := deviceAccountKey(rec.Platform, strings.ToLower(s.accounts[idx].Username))
	if material.AccountKey != accountKey || len(material.AccountKeyAliases) == 0 {
		material.AccountKey = accountKey
		material.AccountKeyAliases = appendDeviceAlias(material.AccountKeyAliases, alias)
	}
	material.AssignedSlotKey = cloneSlotKey(&key)
	if err := s.sealDeviceMaterial(material); err != nil {
		return DeviceRecord{}, err
	}
	rec.AccountKey = accountKey
	rec.AccountKeyAliases = appendDeviceAlias(rec.AccountKeyAliases, alias)
	s.deviceMaterial[rec.ID] = cloneDeviceMaterial(material)
	rec.State = DeviceAssigned
	rec.AssignedAccountID = key.AccountID
	rec.AssignedSlotKey = cloneSlotKey(&key)
	rec.AssignedAt = &now
	rec.RetiredAt = nil
	rec.RetiredReason = ""
	s.ensureSlotRowLocked(key)
	s.setSlotBindingLocked(key, rec.ID)
	if key.Index == 1 {
		s.devices[key.AccountID] = material.Identity
	}
	s.deviceAppendEventLocked(rec, "assigned", "operator", "source=manual slot="+key.String())
	s.addAuditLocked(model.SeverityInfo, "operator", "device.assign", rec.ID,
		"account="+s.deviceAccountLabelLocked(key.AccountID)+" source=manual slot="+key.String())
	return cloneDeviceRecord(*rec), nil
}

// reconcileSlotBindingsLocked repairs a missing canonical binding from the
// slot row or the record's own AssignedSlotKey. It never invents an identity,
// never reassigns a record to a different slot and never clears a binding that
// points at missing material (that must stay visible as a fail-closed state).
func (s *Store) reconcileSlotBindingsLocked() {
	if s.deviceSlotBindings == nil {
		s.deviceSlotBindings = make(map[model.SlotKey]string)
	}
	for key, slot := range s.accountSlots {
		if slot.DeviceID == "" {
			continue
		}
		rec := s.deviceRecordLocked(slot.DeviceID)
		if rec == nil || rec.State != DeviceAssigned || rec.AssignedAccountID != key.AccountID {
			continue
		}
		if rec.AssignedSlotKey == nil {
			rec.AssignedSlotKey = cloneSlotKey(&key)
		}
		if rec.AssignedSlotKey.Index != key.Index {
			continue
		}
		s.deviceSlotBindings[key] = rec.ID
		if key.Index == 1 {
			s.deviceBindings[key.AccountID] = rec.ID
		}
	}
	for key, id := range s.deviceSlotBindings {
		rec := s.deviceRecordLocked(id)
		if rec != nil && rec.State == DeviceAssigned && rec.AssignedSlotKey == nil {
			rec.AssignedSlotKey = cloneSlotKey(&key)
		}
	}
	for accountID, id := range s.deviceBindings {
		rec := s.deviceRecordLocked(id)
		if rec != nil && rec.State == DeviceAssigned && rec.AssignedSlotKey == nil {
			key := accountSlotKey(accountID)
			rec.AssignedSlotKey = cloneSlotKey(&key)
		}
	}
}

// retireAccountDeviceBindingsLocked retires every assigned record of an
// account across all slots, preserving each record's slot affinity so a
// re-added account restores the right slot. The caller holds s.mu.
func (s *Store) retireAccountDeviceBindingsLocked(accountID, label, reason string) int {
	if reason == "" {
		reason = "account_deleted"
	}
	now := iso(s.now())
	seen := make(map[string]bool)
	retired := 0
	retire := func(recID string, slotKey model.SlotKey) {
		if recID == "" || seen[recID] {
			return
		}
		seen[recID] = true
		rec := s.deviceRecordLocked(recID)
		if rec == nil || rec.State != DeviceAssigned {
			return
		}
		if rec.AssignedSlotKey == nil {
			key := slotKey
			rec.AssignedSlotKey = cloneSlotKey(&key)
		}
		rec.State = DeviceRetired
		rec.AssignedAccountID = ""
		rec.AssignedAt = nil
		rec.RetiredAt = &now
		rec.RetiredReason = reason
		s.deviceAppendEventLocked(rec, "retired", "operator", "account delete slot="+rec.AssignedSlotKey.String())
		s.addAuditLocked(model.SeverityInfo, "operator", "device.retire", rec.ID,
			"reason="+reason+" account="+label)
		retired++
	}
	for key, id := range s.deviceSlotBindings {
		if key.AccountID == accountID {
			retire(id, key)
		}
	}
	for id := range s.deviceBindings {
		if id == accountID {
			retire(s.deviceBindings[id], accountSlotKey(accountID))
		}
	}
	for _, rec := range s.devicePool {
		if rec.AssignedAccountID == accountID && rec.State == DeviceAssigned {
			key := accountSlotKey(accountID)
			if rec.AssignedSlotKey != nil {
				key = *rec.AssignedSlotKey
			}
			retire(rec.ID, key)
		}
	}
	for key := range s.deviceSlotBindings {
		if key.AccountID == accountID {
			delete(s.deviceSlotBindings, key)
		}
	}
	delete(s.deviceBindings, accountID)
	delete(s.devices, accountID)
	return retired
}

// BindForLogin is the account-level compatibility shim. It always binds slot
// 1, which is the legacy account-wide device identity.
func (s *Store) BindForLogin(accountID, profileName string) (DeviceMaterial, error) {
	return s.BindForLoginSlot(accountSlotKey(accountID), profileName)
}

// AccountDeviceMaterial is the account-level compatibility shim for slot 1.
func (s *Store) AccountDeviceMaterial(accountID string) (DeviceMaterial, error) {
	return s.AccountDeviceMaterialSlot(accountSlotKey(accountID))
}

// RestoreCandidates is the account-level compatibility shim for slot 1.
func (s *Store) RestoreCandidates(accountID, profileName string) ([]RestoreCandidate, error) {
	return s.RestoreCandidatesSlot(accountSlotKey(accountID), profileName)
}

// RestoreDevice is the account-level compatibility shim for slot 1.
func (s *Store) RestoreDevice(deviceID, accountID string) (DeviceRecord, error) {
	return s.RestoreDeviceSlot(deviceID, accountSlotKey(accountID))
}

// AssignDevice is the account-level compatibility shim for slot 1.
func (s *Store) AssignDevice(deviceID, accountID string) (DeviceRecord, error) {
	return s.AssignDeviceSlot(deviceID, accountSlotKey(accountID))
}
