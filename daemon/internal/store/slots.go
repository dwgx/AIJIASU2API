package store

import (
	"sort"

	"ajsaccountd/internal/model"
)

// MaxServiceSlots is the design ceiling on independently operated slots per
// account once the multi-slot runtime lands. The vendor's UseCount may be as
// high as 2000, but the daemon will never serve more than eight concurrent FV
// sessions for one account (DESIGN-MULTISLOT; MULTISLOT-PLAN §1).
//
// This file is step 1 of MULTISLOT-PLAN: it only models and persists slots.
// It opens no session and starts no gate, so the runtime's current one-gate
// invariant is unchanged. Slot-aware session start is a later step.
const MaxServiceSlots = 8

// maxServiceSlots is the package-local spelling used by tests and display
// callers; the exported name is the public contract.
const maxServiceSlots = MaxServiceSlots

// SlotCapacity returns the display capacity for one account:
//
//	min(normalize(useCount), normalize(accountSlots), MaxServiceSlots)
//
// A missing UseCount (zero) and an unset operator cap both mean one slot, so
// an existing installation never gains capacity by upgrading. Values above
// 2000 are clamped to the vendor's published maximum before the eight-slot
// service ceiling is applied.
func SlotCapacity(useCount, accountSlots int) int {
	capacity := normalizeAccountUseCount(useCount)
	if limit := normalizeOperatorSlotCap(accountSlots); limit < capacity {
		capacity = limit
	}
	if capacity > MaxServiceSlots {
		capacity = MaxServiceSlots
	}
	return capacity
}

// slotCapacity is the package-local spelling used by display/tests.
func slotCapacity(useCount, accountSlots int) int { return SlotCapacity(useCount, accountSlots) }

// ConfiguredSlotCapacity applies the process-wide AJS_ACCOUNT_SLOTS value.
func ConfiguredSlotCapacity(useCount int) int {
	return SlotCapacity(useCount, configuredAccountSlots())
}

// MaxAdmissionSlots is the hard ceiling on active leases per account until
// the multi-slot runtime can start one gate per slot. Display capacity may be
// higher: SlotCapacity reports the purchased allowance for the UI, while
// AdmissionSlots is what the lease guard actually admits.
const MaxAdmissionSlots = 1

// AdmissionSlots returns the lease-admission capacity for one account:
//
//	min(SlotCapacity(useCount, accountSlots), MaxAdmissionSlots)
//
// Until the multi-slot runtime lands this is always one, even when the vendor
// UseCount or the operator's AJS_ACCOUNT_SLOTS asks for more. Keeping this
// helper separate from SlotCapacity lets the account API keep showing the
// purchased display capacity while the store refuses a second lease.
func AdmissionSlots(useCount, accountSlots int) int {
	capacity := SlotCapacity(useCount, accountSlots)
	if capacity > MaxAdmissionSlots {
		capacity = MaxAdmissionSlots
	}
	return capacity
}

// ConfiguredAdmissionSlots applies the process-wide AJS_ACCOUNT_SLOTS value
// to the admission cap. It is the helper the lease guard uses.
func ConfiguredAdmissionSlots(useCount int) int {
	return AdmissionSlots(useCount, configuredAccountSlots())
}

// admissionSlotCapacity is the package-local spelling used by the lease guard.
func admissionSlotCapacity(useCount, accountSlots int) int {
	return AdmissionSlots(useCount, accountSlots)
}

// normalizeOperatorSlotCap keeps an operator-supplied cap in the vendor's
// accepted 1..2000 range. Zero and negative select the safe default of one.
func normalizeOperatorSlotCap(slots int) int {
	if slots < 1 {
		return defaultAccountSlots
	}
	if slots > maxAccountSlots {
		return maxAccountSlots
	}
	return slots
}

// normalizeSlotCapacity clamps an explicit per-account slot count to the
// service range 1..MaxServiceSlots. It is used when materialising rows, not
// when deriving capacity from vendor and operator limits.
func normalizeSlotCapacity(slots int) int {
	if slots < 1 {
		return 1
	}
	if slots > MaxServiceSlots {
		return MaxServiceSlots
	}
	return slots
}

// Slot returns one account slot by key.
func (s *Store) Slot(key model.SlotKey) (model.AccountSlot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	slot, ok := s.accountSlots[key]
	if !ok {
		return model.AccountSlot{}, false
	}
	return s.slotWithBindingLocked(slot), true
}

// Slots returns one account's slots ordered by index. An unknown account has
// no slots and returns an empty, non-nil slice.
func (s *Store) Slots(accountID string) []model.AccountSlot {
	s.mu.RLock()
	out := make([]model.AccountSlot, 0, MaxServiceSlots)
	for key, slot := range s.accountSlots {
		if key.AccountID == accountID {
			out = append(out, s.slotWithBindingLocked(slot))
		}
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	return out
}

// AllSlots returns every account slot ordered by account id and then index.
// The order is stable so a snapshot written twice from the same state is
// byte-identical.
func (s *Store) AllSlots() []model.AccountSlot {
	s.mu.RLock()
	out := make([]model.AccountSlot, 0, len(s.accountSlots))
	for _, slot := range s.accountSlots {
		out = append(out, s.slotWithBindingLocked(slot))
	}
	s.mu.RUnlock()
	sortAccountSlots(out)
	return out
}

// slotWithBindingLocked projects the account-level device binding onto slot
// 1. Existing installations may have a binding that predates the slot row;
// the read view must show the same record id the next snapshot will persist.
// The caller holds at least the read lock.
func (s *Store) slotWithBindingLocked(slot model.AccountSlot) model.AccountSlot {
	if slot.Index == 1 && slot.DeviceID == "" {
		slot.DeviceID = s.deviceBindings[slot.AccountID]
	}
	return slot
}

// UpsertSlot validates and writes one slot. It rejects an unknown account, an
// index outside 1..8, an unknown state, a device id already used by another
// slot and a gate port already owned by another slot. An existing row keeps
// its generation when the caller omits it, and a non-zero generation may never
// move backwards.
func (s *Store) UpsertSlot(slot model.AccountSlot) error {
	if slot.State == "" {
		slot.State = model.SlotProvisioning
	}
	if err := slot.Validate(); err != nil {
		return invalidf("%v", err)
	}
	if slot.GatePort != 0 && slot.GatePort == slot.HTTPPort {
		return invalidf("slot %s gate and http ports must differ", slot.SlotKey.String())
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.accountIndexLocked(slot.AccountID) < 0 {
		return notFoundf("account %q not found", slot.AccountID)
	}
	previous, exists := s.accountSlots[slot.SlotKey]
	if exists {
		if slot.Generation == 0 {
			slot.Generation = previous.Generation
		}
		if previous.Generation > 0 && slot.Generation < previous.Generation {
			return invalidf("slot %s generation must not decrease", slot.SlotKey.String())
		}
	}
	for key, other := range s.accountSlots {
		if key == slot.SlotKey {
			continue
		}
		if slot.DeviceID != "" && other.DeviceID == slot.DeviceID {
			return conflictf("device %q is already used by slot %s", slot.DeviceID, key.String())
		}
		if slot.GatePort != 0 && other.GatePort == slot.GatePort {
			return conflictf("gate port %d is already used by slot %s", slot.GatePort, key.String())
		}
		if slot.HTTPPort != 0 && other.HTTPPort == slot.HTTPPort {
			return conflictf("http port %d is already used by slot %s", slot.HTTPPort, key.String())
		}
	}
	s.accountSlots[slot.SlotKey] = slot
	return nil
}

// PutSlot is an alias for UpsertSlot kept for the plan's vocabulary.
func (s *Store) PutSlot(slot model.AccountSlot) error { return s.UpsertSlot(slot) }

// SetSlotState updates the lifecycle and health of one existing slot. Detail
// is reduced to one bounded line and kept only as the slot's last error, so a
// caller must pass a reason, never a credential.
func (s *Store) SetSlotState(key model.SlotKey, state model.SlotState, health model.SlotHealth, detail string) error {
	if !key.Valid() {
		return invalidf("slot key %q is invalid", key.String())
	}
	if !state.Valid() {
		return invalidf("slot %s has unknown state %q", key.String(), state)
	}
	if !health.Valid() {
		return invalidf("slot %s has unknown health %q", key.String(), health)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	slot, ok := s.accountSlots[key]
	if !ok {
		return notFoundf("slot %s not found", key.String())
	}
	slot.State = state
	slot.Health = health
	slot.LastAttemptAt = iso(s.now())
	if detail != "" {
		slot.LastError = clientMessage(detail)
	}
	s.accountSlots[key] = slot
	return nil
}

// SetSlotPorts assigns the SOCKS5 and HTTP listener ports owned by one slot.
// Port 0 means "not assigned". A port already owned by another slot is a
// conflict; the runtime must allocate a free pair first.
func (s *Store) SetSlotPorts(key model.SlotKey, socks, httpPort int) error {
	if !key.Valid() {
		return invalidf("slot key %q is invalid", key.String())
	}
	if socks < 0 || socks > 65535 || httpPort < 0 || httpPort > 65535 {
		return invalidf("slot %s has an invalid port pair (%d, %d)", key.String(), socks, httpPort)
	}
	if socks != 0 && socks == httpPort {
		return invalidf("slot %s gate and http ports must differ", key.String())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	slot, ok := s.accountSlots[key]
	if !ok {
		return notFoundf("slot %s not found", key.String())
	}
	for otherKey, other := range s.accountSlots {
		if otherKey == key {
			continue
		}
		if socks != 0 && other.GatePort == socks {
			return conflictf("gate port %d is already used by slot %s", socks, otherKey.String())
		}
		if httpPort != 0 && other.HTTPPort == httpPort {
			return conflictf("http port %d is already used by slot %s", httpPort, otherKey.String())
		}
	}
	slot.GatePort = socks
	slot.HTTPPort = httpPort
	s.accountSlots[key] = slot
	return nil
}

// DeleteSlot removes one slot row. It does not touch a device record or any
// session: the runtime must drain the slot first.
func (s *Store) DeleteSlot(key model.SlotKey) error {
	if !key.Valid() {
		return invalidf("slot key %q is invalid", key.String())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.accountSlots[key]; !ok {
		return notFoundf("slot %s not found", key.String())
	}
	delete(s.accountSlots, key)
	return nil
}

// EnsureAccountSlots materialises indices 1..capacity for one account without
// touching existing rows. When capacity shrinks, bare provisioning rows above
// the new cap are removed; a row that carries a device, server, port or
// generation is considered in use and refuses the reduction with a conflict so
// the runtime can drain it first.
func (s *Store) EnsureAccountSlots(accountID string, capacity int) ([]model.AccountSlot, error) {
	capacity = normalizeSlotCapacity(capacity)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.accountIndexLocked(accountID) < 0 {
		return nil, notFoundf("account %q not found", accountID)
	}

	// Check the reduction before mutating anything: a failed call must leave
	// the slot table exactly as it was.
	for key, slot := range s.accountSlots {
		if key.AccountID != accountID || key.Index <= capacity {
			continue
		}
		if !slotBare(slot) {
			return nil, conflictf("slot_in_use: slot %s must be drained before capacity can shrink", key.String())
		}
	}

	for key, slot := range s.accountSlots {
		if key.AccountID == accountID && key.Index > capacity && slotBare(slot) {
			delete(s.accountSlots, key)
		}
	}
	for index := 1; index <= capacity; index++ {
		key := model.SlotKey{AccountID: accountID, Index: index}
		if _, ok := s.accountSlots[key]; ok {
			continue
		}
		s.accountSlots[key] = model.AccountSlot{SlotKey: key, State: model.SlotProvisioning, Health: model.SlotHealthUnknown}
	}

	out := make([]model.AccountSlot, 0, capacity)
	for key, slot := range s.accountSlots {
		if key.AccountID == accountID {
			out = append(out, s.slotWithBindingLocked(slot))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	return out, nil
}

// SlotCount returns how many slot rows an account currently has.
func (s *Store) SlotCount(accountID string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	count := 0
	for key := range s.accountSlots {
		if key.AccountID == accountID {
			count++
		}
	}
	return count
}

// slotBare reports whether a slot row has never been provisioned. Such a row
// can be removed safely when the operator lowers capacity.
func slotBare(slot model.AccountSlot) bool {
	return slot.State == model.SlotProvisioning &&
		slot.DeviceID == "" && slot.ServerID == "" &&
		slot.GatePort == 0 && slot.HTTPPort == 0 && slot.Generation == 0 &&
		slot.KickCount == 0 && slot.LastError == ""
}

// sortAccountSlots orders slots by account id and then index. It is the one
// ordering used by snapshots and AllSlots, so a map iteration cannot leak into
// the persisted document.
func sortAccountSlots(slots []model.AccountSlot) {
	sort.Slice(slots, func(i, j int) bool {
		if slots[i].AccountID != slots[j].AccountID {
			return slots[i].AccountID < slots[j].AccountID
		}
		return slots[i].Index < slots[j].Index
	})
}

// ensureAccountSlotRowLocked creates the mandatory slot-1 row when it is
// absent. The caller holds the write lock (or is building a fresh store before
// it is published).
func (s *Store) ensureAccountSlotRowLocked(accountID string) {
	key := model.SlotKey{AccountID: accountID, Index: 1}
	if _, ok := s.accountSlots[key]; ok {
		return
	}
	s.accountSlots[key] = model.AccountSlot{SlotKey: key, State: model.SlotProvisioning, Health: model.SlotHealthUnknown}
}
