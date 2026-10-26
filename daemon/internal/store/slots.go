package store

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"ajsaccountd/internal/model"
)

// MaxServiceSlots is the design ceiling on independently operated slots per
// account. The vendor's UseCount may be as high as 2000, but the daemon serves
// at most eight concurrent FV sessions for one account (DESIGN-MULTISLOT;
// MULTISLOT-PLAN §1).
//
// This file is step 1 of MULTISLOT-PLAN: it only models and persists slots.
// It opens no session and starts no gate; slot-aware session start lives in the
// runtime.
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

// accountCapacityLocked is the one admission calculation (B1): the account's
// slot allowance further capped by the number of nodes that can actually
// serve. It uses the same membership predicate as the lease path, so capacity
// and admission cannot disagree. The caller holds at least the read lock.
func (s *Store) accountCapacityLocked(account model.Account, now time.Time) int {
	// Membership and expiry: an unavailable account admits nothing.
	if leaseAccountAvailable(account, now) != nil {
		return 0
	}
	capacity := SlotCapacity(account.UseCount, configuredAccountSlots())
	serving := 0
	seen := make(map[int]struct{}, MaxServiceSlots)
	for _, node := range s.nodes {
		if node.AccountID != account.ID {
			continue
		}
		// Only a LIVE row has a bound listener. A READY/STARTING/DRAINING/
		// DOWN/STALE row is not capacity, whether it is a slot node or a
		// legacy (Slot 0) one: counting it would let admission mint a lease
		// for a port no gate is listening on.
		if node.State != model.NodeLive || node.GatePort <= 0 {
			continue
		}
		// A slot node is capacity only while its slot row is live and the row
		// and the node agree on the bound pair. A duplicate node row for the
		// same slot counts once: the capacity denominator is a verified
		// serving slot, not a row.
		if node.Slot > 0 {
			if _, duplicate := seen[node.Slot]; duplicate {
				continue
			}
			slot, ok := s.accountSlots[model.SlotKey{AccountID: node.AccountID, Index: node.Slot}]
			if !ok || slot.State != model.SlotLive || node.GatePort <= 0 || slot.GatePort != node.GatePort {
				continue
			}
			seen[node.Slot] = struct{}{}
		}
		serving++
	}
	if serving < capacity {
		capacity = serving
	}
	return capacity
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

// slotWithBindingLocked projects the canonical device binding onto a slot.
// Existing installations may have a binding that predates the slot row; the
// read view must show the same record id the next snapshot will persist. The
// caller holds at least the read lock.
func (s *Store) slotWithBindingLocked(slot model.AccountSlot) model.AccountSlot {
	if slot.DeviceID == "" {
		if id, ok := s.slotBindingLocked(slot.SlotKey); ok {
			slot.DeviceID = id
		}
	}
	return slot
}

// UpsertSlot validates and writes one slot. It rejects an unknown account, an
// index outside 1..8, an unknown state, a device id already used by another
// slot and a gate port already owned by another slot. An existing row keeps
// its generation when the caller omits it, and a non-zero generation may never
// move backwards.
func (s *Store) UpsertSlot(slot model.AccountSlot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.upsertSlotLocked(slot)
}

// upsertSlotLocked is the body of UpsertSlot for callers that already hold the
// write lock (CommitSlotStart, CommitSwitch). It validates and writes under
// that same lock, so a node and its slot row cannot be written half-way.
func (s *Store) upsertSlotLocked(slot model.AccountSlot) error {
	if slot.State == "" {
		slot.State = model.SlotProvisioning
	}
	if err := slot.Validate(); err != nil {
		return invalidf("%v", err)
	}
	if slot.GatePort != 0 && slot.GatePort == slot.HTTPPort {
		return invalidf("slot %s gate and http ports must differ", slot.SlotKey.String())
	}
	if s.accountIndexLocked(slot.AccountID) < 0 {
		return notFoundf("account %q not found", slot.AccountID)
	}
	// When the device pool is live, a slot may only reference the exact
	// assigned record that names this slot. The pre-pool slot CRUD tests use
	// synthetic ids with an empty pool and remain valid.
	if slot.DeviceID != "" {
		if rec := s.deviceRecordLocked(slot.DeviceID); rec != nil {
			if rec.State != DeviceAssigned || rec.AssignedAccountID != slot.AccountID ||
				rec.AssignedSlotKey == nil || *rec.AssignedSlotKey != slot.SlotKey {
				return conflictf("device %q is not assigned to slot %s", slot.DeviceID, slot.SlotKey.String())
			}
		} else if len(s.devicePool) > 0 {
			return conflictf("slot %s references unknown device %q", slot.SlotKey.String(), slot.DeviceID)
		}
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
		// A slot owns a pair, so the two listener kinds share one namespace:
		// a SOCKS port may not collide with another slot's HTTP port (or the
		// reverse), or the runtime reservation would refuse the pair forever.
		if slot.GatePort != 0 && (other.GatePort == slot.GatePort || other.HTTPPort == slot.GatePort) {
			return conflictf("port %d is already used by slot %s", slot.GatePort, key.String())
		}
		if slot.HTTPPort != 0 && (other.GatePort == slot.HTTPPort || other.HTTPPort == slot.HTTPPort) {
			return conflictf("port %d is already used by slot %s", slot.HTTPPort, key.String())
		}
	}
	s.accountSlots[slot.SlotKey] = slot
	return nil
}

// PutSlot is an alias for UpsertSlot kept for the plan's vocabulary.
func (s *Store) PutSlot(slot model.AccountSlot) error { return s.UpsertSlot(slot) }

// SlotDeviceBound reports whether the slot already owns a device binding. It
// is the read that decides "bind once" in the runtime; it never generates.
func (s *Store) SlotDeviceBound(key model.SlotKey) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, bound := s.slotBindingLocked(key)
	return bound
}

// SlotStartCommit writes one started slot and its node under a single lock.
// Slot.Generation must be >= the stored generation (monotonic); Slot.State must
// be SlotLive; Slot.SlotKey must equal Node.Slot and Node.AccountID.
//
// DeviceRecordID is the device record the session was opened with. The commit
// re-checks it under the write lock, so a release that lands while a slow start
// is in flight cannot commit LIVE without an owned identity. Empty means "no
// record", which is the pool-disabled legacy mode.
type SlotStartCommit struct {
	Slot           model.AccountSlot
	Node           model.ProxyNode
	DeviceRecordID string
}

// CommitSlotStart writes one started slot and its node under a single lock, so
// a concurrent reader never sees a LIVE node without its slot row (or the
// other way round). It returns the node as written.
func (s *Store) CommitSlotStart(commit SlotStartCommit) (model.ProxyNode, error) {
	node, err := normalizeNode(commit.Node)
	if err != nil {
		return model.ProxyNode{}, err
	}
	slot := commit.Slot
	if !slot.SlotKey.Valid() {
		return model.ProxyNode{}, invalidf("slot key %q is invalid", slot.SlotKey.String())
	}
	if slot.Index != node.Slot || slot.AccountID != node.AccountID {
		return model.ProxyNode{}, invalidf("slot %s does not match node %q (account %q, slot %d)",
			slot.SlotKey.String(), node.ID, node.AccountID, node.Slot)
	}
	if slot.State != model.SlotLive {
		return model.ProxyNode{}, invalidf("slot %s start commit requires state %q",
			slot.SlotKey.String(), model.SlotLive)
	}
	if node.GatePort != slot.GatePort {
		return model.ProxyNode{}, invalidf("node %q gate port %d does not match slot %s port %d",
			node.ID, node.GatePort, slot.SlotKey.String(), slot.GatePort)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	accountIdx := s.accountIndexLocked(node.AccountID)
	if accountIdx < 0 {
		return model.ProxyNode{}, notFoundf("account %q not found", node.AccountID)
	}
	account := s.accounts[accountIdx]
	node.AccountID = account.ID
	node.AccountLabel = account.Label
	// A device released while the session was opening must fail the commit, not
	// become a LIVE node with an unowned identity.
	if err := s.requireSlotDeviceLocked(slot.SlotKey, commit.DeviceRecordID); err != nil {
		return model.ProxyNode{}, err
	}

	idx := s.nodeIndexLocked(node.ID)
	if idx >= 0 && s.nodes[idx].AccountID != node.AccountID {
		return model.ProxyNode{}, conflictf("node %q belongs to account %q", node.ID, s.nodes[idx].AccountID)
	}
	for i, other := range s.nodes {
		if i != idx && other.Label == node.Label {
			return model.ProxyNode{}, conflictf("node label %q is already used by %q", node.Label, other.ID)
		}
	}
	previousGeneration := uint64(0)
	if previous, ok := s.accountSlots[slot.SlotKey]; ok {
		previousGeneration = previous.Generation
	}
	if slot.Generation == 0 {
		slot.Generation = previousGeneration
	}
	// The slot write is the only fallible step of the commit, so it runs before
	// the node is touched: a rejected slot leaves the node table unchanged.
	if err := s.upsertSlotLocked(slot); err != nil {
		return model.ProxyNode{}, err
	}
	// A start is the lifecycle event a later audit needs to pair with a switch
	// or disconnect, and exactly one row per (slot, generation) is the rule a
	// two-slot trace needs. Re-committing the same generation (a DRAINING gate
	// returned to LIVE, or a refresh) is not a new start, so it writes nothing.
	started := idx < 0 || slot.Generation != previousGeneration
	if idx >= 0 {
		existing := s.nodes[idx]
		if node.InUse == 0 {
			node.InUse = existing.InUse
		}
		if node.SuccessRate == 0 {
			node.SuccessRate = existing.SuccessRate
		}
		if node.LastProbeAt == "" {
			node.LastProbeAt = existing.LastProbeAt
		}
		s.nodes[idx] = node
	} else {
		if node.LastProbeAt == "" {
			node.LastProbeAt = iso(s.now())
		}
		s.nodes = append(s.nodes, node)
	}
	if started {
		s.addAuditLocked(model.SeverityInfo, "system", "slot.start", slot.SlotKey.String(),
			"Slot started · one session live",
			map[string]string{
				"slot":       slot.SlotKey.String(),
				"nodeId":     node.ID,
				"serverId":   node.ServerID,
				"generation": strconv.FormatUint(slot.Generation, 10),
				"gatePort":   strconv.Itoa(node.GatePort),
			})
	}
	s.syncAccountCountsLocked()
	return node, nil
}

// SlotKickQuarantineThreshold is the number of recorded vendor kicks after
// which a slot stops retrying on its own: a repeatedly kicked identity is
// evidence the vendor no longer honours this slot, so only an operator (a
// manual connect) may reopen it.
const SlotKickQuarantineThreshold = 3

// SlotKick is one recorded vendor session loss for a slot: the server the
// session was opened against, the bounded reason and the instant before which
// the slot must not reopen.
type SlotKick struct {
	Key        model.SlotKey
	ServerID   string
	Detail     string
	RetryAfter time.Time
}

// RecordSlotKick records one vendor kick: the slot leaves LIVE, its health
// becomes vendor_kick, the kick counter advances and RetryAfter blocks an
// automatic reopen. A slot that reaches SlotKickQuarantineThreshold moves to
// SlotQuarantined instead of down. The device binding and the reserved port
// pair are kept, so a controlled reconnect reuses the same endpoint.
//
// The node and its leases are the caller's job: the runtime closes the gate,
// marks the node DOWN and revokes its leases in the same containment step.
func (s *Store) RecordSlotKick(kick SlotKick) (model.AccountSlot, error) {
	if !kick.Key.Valid() {
		return model.AccountSlot{}, invalidf("slot key %q is invalid", kick.Key.String())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	slot, ok := s.accountSlots[kick.Key]
	if !ok {
		return model.AccountSlot{}, notFoundf("slot %s not found", kick.Key.String())
	}
	slot.KickCount++
	slot.Health = model.SlotHealthVendorKick
	slot.State = model.SlotDown
	if slot.KickCount >= SlotKickQuarantineThreshold {
		slot.State = model.SlotQuarantined
	}
	if serverID := strings.TrimSpace(kick.ServerID); serverID != "" {
		slot.ServerID = serverID
	}
	detail := clientMessage(kick.Detail)
	if detail == "" {
		detail = "vendor_kick"
	}
	slot.LastAttemptAt = iso(s.now())
	slot.LastError = detail
	if !kick.RetryAfter.IsZero() {
		slot.RetryAfter = kick.RetryAfter.UTC().Format(time.RFC3339)
	}
	s.accountSlots[kick.Key] = slot
	meta := map[string]string{
		"slot":       kick.Key.String(),
		"kickCount":  strconv.Itoa(slot.KickCount),
		"state":      string(slot.State),
		"reason":     detail,
		"generation": strconv.FormatUint(slot.Generation, 10),
	}
	if slot.ServerID != "" {
		meta["serverId"] = slot.ServerID
		meta["nodeId"] = kick.Key.NodeID(slot.ServerID)
	}
	if slot.RetryAfter != "" {
		meta["retryAfter"] = slot.RetryAfter
	}
	s.addAuditLocked(model.SeverityWarn, "system", "slot.vendor_kick", kick.Key.String(),
		"Slot session lost · "+detail, meta)
	return s.slotWithBindingLocked(slot), nil
}

// demoteSlotForNodeLocked records that restart recovery demoted a node whose
// gate no longer exists. The slot row must not stay LIVE: admission and lease
// selection read the slot row, so a LIVE row over a STALE node would keep
// admitting leases for an endpoint that was never reopened. The device
// binding, target, port pair and generation are kept for the next login.
func (s *Store) demoteSlotForNodeLocked(node model.ProxyNode) (model.AccountSlot, bool) {
	if node.Slot <= 0 {
		return model.AccountSlot{}, false
	}
	key := model.SlotKey{AccountID: node.AccountID, Index: node.Slot}
	slot, ok := s.accountSlots[key]
	if !ok || slot.State != model.SlotLive {
		return model.AccountSlot{}, false
	}
	slot.State = model.SlotDown
	slot.Health = model.SlotHealthDegraded
	slot.LastAttemptAt = iso(s.now())
	slot.LastError = "gate_not_bound_after_restart"
	s.accountSlots[key] = slot
	return slot, true
}

// SetSlotFailure marks one slot down with a bounded reason and records the
// server the attempt targeted. DeviceID, ports and generation are preserved.
func (s *Store) SetSlotFailure(key model.SlotKey, serverID, reason string) error {
	if !key.Valid() {
		return invalidf("slot key %q is invalid", key.String())
	}
	detail := clientMessage(reason)
	s.mu.Lock()
	defer s.mu.Unlock()
	slot, ok := s.accountSlots[key]
	if !ok {
		return notFoundf("slot %s not found", key.String())
	}
	slot.State = model.SlotDown
	slot.Health = model.SlotHealthDegraded
	slot.LastAttemptAt = iso(s.now())
	slot.LastError = detail
	if serverID != "" {
		slot.ServerID = serverID
	}
	s.accountSlots[key] = slot
	meta := map[string]string{
		"slot":       key.String(),
		"reason":     detail,
		"generation": strconv.FormatUint(slot.Generation, 10),
	}
	if serverID != "" {
		meta["serverId"] = serverID
		meta["nodeId"] = key.NodeID(serverID)
	}
	s.addAuditLocked(model.SeverityWarn, "system", "slot.failure", key.String(),
		"Slot down · "+detail, meta)
	return nil
}

// DisableSlot releases one slot: state down, ports cleared, row kept. It
// refuses while an active lease points at the slot's node, so a capacity
// change never revokes a customer lease.
func (s *Store) DisableSlot(key model.SlotKey) error {
	if !key.Valid() {
		return invalidf("slot key %q is invalid", key.String())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	slot, ok := s.accountSlots[key]
	if !ok {
		return notFoundf("slot %s not found", key.String())
	}
	// Every node row of the slot pins it, not just the persisted target: a
	// failed target change leaves the old server's node behind, and a capacity
	// reduction must not delete that node's lease as collateral.
	slotNodes := make(map[string]struct{}, 2)
	for _, node := range s.nodes {
		if node.AccountID == key.AccountID && node.Slot == key.Index {
			slotNodes[node.ID] = struct{}{}
		}
	}
	if slot.ServerID != "" {
		slotNodes[key.NodeID(slot.ServerID)] = struct{}{}
	}
	for _, lease := range s.leases {
		if _, pinned := slotNodes[lease.NodeID]; pinned {
			return conflictf("slot_has_lease: slot %s still has an active lease", key.String())
		}
	}
	slot.State = model.SlotDown
	slot.Health = model.SlotHealthDegraded
	slot.GatePort, slot.HTTPPort = 0, 0
	slot.LastAttemptAt = iso(s.now())
	slot.LastError = "disabled"
	s.accountSlots[key] = slot
	meta := map[string]string{
		"slot":       key.String(),
		"serverId":   slot.ServerID,
		"generation": strconv.FormatUint(slot.Generation, 10),
	}
	if slot.ServerID != "" {
		meta["nodeId"] = key.NodeID(slot.ServerID)
	}
	s.addAuditLocked(model.SeverityInfo, "operator", "slot.disable", key.String(),
		"Slot disabled · capacity reduced", meta)
	return nil
}

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
		// One namespace for both listener kinds: see upsertSlotLocked.
		if socks != 0 && (other.GatePort == socks || other.HTTPPort == socks) {
			return conflictf("port %d is already used by slot %s", socks, otherKey.String())
		}
		if httpPort != 0 && (other.GatePort == httpPort || other.HTTPPort == httpPort) {
			return conflictf("port %d is already used by slot %s", httpPort, otherKey.String())
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
	if _, bound := s.boundRecordIDLocked(key); bound {
		return conflictf("slot_in_use: slot %s still owns a device binding", key.String())
	}
	if slot, ok := s.accountSlots[key]; ok && slot.DeviceID != "" {
		if rec := s.deviceRecordLocked(slot.DeviceID); rec != nil && rec.State == DeviceAssigned {
			return conflictf("slot_in_use: slot %s references assigned device %q", key.String(), slot.DeviceID)
		}
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
		if _, bound := s.boundRecordIDLocked(key); bound || !slotBare(slot) {
			return nil, conflictf("slot_in_use: slot %s must be drained before capacity can shrink", key.String())
		}
	}

	for key, slot := range s.accountSlots {
		if key.AccountID != accountID || key.Index <= capacity || !slotBare(slot) {
			continue
		}
		if _, bound := s.boundRecordIDLocked(key); bound {
			continue
		}
		delete(s.accountSlots, key)
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
