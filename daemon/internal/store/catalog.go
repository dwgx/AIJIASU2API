package store

import (
	"strconv"
	"strings"

	"ajsaccountd/internal/model"
)

// Catalog state: the persisted operator selection and the per-server attempt
// counters (DESIGN-CATALOG §3.4, §3.8). This file is the only writer of those
// maps; every exported mutation writes exactly one audit event, except the
// quiet node helpers used inside the switch state machine, whose single
// catalog.switch/catalog.connect audit covers the whole transition.

// SwitchCommit is one atomic switch commit: delete every other node of the
// same slot (and rebind their leases), put the new node LIVE, optionally
// persist the slot row, the operator selection, bump the server stat and write
// the catalog audit. The caller holds no store lock; the whole commit happens
// under one write lock.
type SwitchCommit struct {
	Node   model.ProxyNode
	Actor  string
	Action string
	Target string
	Detail string
	Meta   map[string]string
	// Slot is required; a zero Index defaults to 1 and a zero AccountID
	// defaults to the node's account. After that it must match the node
	// exactly: a commit never widens scope to the whole account.
	Slot model.SlotKey
	// SlotState is the optional slot row written atomically with the node
	// (state, ports, generation). Its SlotKey must equal Slot.
	SlotState *model.AccountSlot
	// Selection is set for a manual connect/switch only. A rotation or login
	// sync passes nil so it never overwrites the operator's target.
	Selection *model.Selection
	// Stat is the per-server counter to bump on success.
	Stat *model.ServerStat
	// DeviceRecordID is the device record the new session was opened with. The
	// commit re-checks it under the write lock, so a release that lands during
	// a slow reopen cannot commit LIVE without an owned identity. Empty means
	// "no record", which is the pool-disabled legacy mode.
	DeviceRecordID string
}

// AttemptRecord is one failed switch attempt: it updates the server stat and
// writes the audit, but changes no node. The caller supplies the catalog
// action (catalog.connect/catalog.switch/catalog.reconnect).
type AttemptRecord struct {
	AccountID  string
	ServerID   string
	Action     string
	Target     string
	Detail     string
	Meta       map[string]string
	ErrorClass string
	// Slot is the slot the attempt targeted; a zero Index defaults to 1 and a
	// zero AccountID defaults to AccountID.
	Slot model.SlotKey
}

// Selection returns the persisted target of one account.
func (s *Store) Selection(accountID string) (model.Selection, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	selection, ok := s.selections[accountID]
	return selection, ok
}

// Selections returns a copy of every persisted target, keyed by account id.
func (s *Store) Selections() map[string]model.Selection {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]model.Selection, len(s.selections))
	for id, selection := range s.selections {
		out[id] = selection
	}
	return out
}

// SetSelection persists the account's single target and writes one
// catalog.select audit with the previous server id in meta. Setting the same
// server again is a no-op (no audit): nothing changed.
func (s *Store) SetSelection(accountID, serverID, actor string) (model.Selection, error) {
	serverID = strings.TrimSpace(serverID)
	if serverID == "" {
		return model.Selection{}, invalidf("server id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := s.accountIndexLocked(accountID)
	if idx < 0 {
		return model.Selection{}, notFoundf("account %q not found", accountID)
	}
	if existing, ok := s.selections[accountID]; ok && existing.ServerID == serverID {
		return existing, nil
	}
	previous := ""
	if existing, ok := s.selections[accountID]; ok {
		previous = existing.ServerID
	}
	selection := model.Selection{
		AccountID: accountID,
		ServerID:  serverID,
		UpdatedAt: iso(s.now()),
		UpdatedBy: auditActor(actor),
	}
	s.selections[accountID] = selection
	meta := map[string]string{"serverId": serverID}
	if previous != "" {
		meta["previousServerId"] = previous
	}
	s.addAuditLocked(model.SeverityInfo, selection.UpdatedBy, "catalog.select",
		accountID+":"+serverID, "Target set · not connected", meta)
	return selection, nil
}

// ClearSelection removes the target and writes one catalog.unselect audit when
// a target existed. It is idempotent and never touches the running session.
func (s *Store) ClearSelection(accountID, actor string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := s.accountIndexLocked(accountID)
	if idx < 0 {
		return false, notFoundf("account %q not found", accountID)
	}
	existing, ok := s.selections[accountID]
	if !ok {
		return false, nil
	}
	delete(s.selections, accountID)
	s.addAuditLocked(model.SeverityInfo, auditActor(actor), "catalog.unselect",
		s.accounts[idx].Label, "Target cleared", map[string]string{"previousServerId": existing.ServerID})
	return true, nil
}

// LoseSelection clears a target that no longer resolves in the retained login
// result and audits catalog.selection_lost with the reason. Login sync calls
// it before falling back to the default desired set.
func (s *Store) LoseSelection(accountID, reason string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := s.accountIndexLocked(accountID)
	if idx < 0 {
		return false, notFoundf("account %q not found", accountID)
	}
	existing, ok := s.selections[accountID]
	if !ok {
		return false, nil
	}
	delete(s.selections, accountID)
	s.addAuditLocked(model.SeverityWarn, "system", "catalog.selection_lost",
		s.accounts[idx].Label, "Selected server is no longer usable · target cleared",
		map[string]string{"serverId": existing.ServerID, "reason": reason})
	return true, nil
}

// CommitSwitch performs the node/lease/audit/stat/selection commit of one
// successful switch under one write lock. A validation or conflict failure
// leaves every collection untouched, so the caller can roll back to the old
// session.
func (s *Store) CommitSwitch(commit SwitchCommit) (model.ProxyNode, string, error) {
	node, err := normalizeNode(commit.Node)
	if err != nil {
		return model.ProxyNode{}, "", err
	}
	if strings.TrimSpace(commit.Action) == "" {
		return model.ProxyNode{}, "", invalidf("switch commit action is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	accountIdx := s.accountIndexLocked(node.AccountID)
	if accountIdx < 0 {
		return model.ProxyNode{}, "", notFoundf("account %q not found", node.AccountID)
	}
	account := s.accounts[accountIdx]
	node.AccountID = account.ID
	node.AccountLabel = account.Label

	// Resolve and validate the slot before planning any deletion. A zero Index
	// defaults to slot 1; a zero AccountID defaults to the node's account.
	// After that the slot must match the node exactly: a caller that passes
	// slot 2 for a slot-1 node is a bug, not a request to widen scope to the
	// account. A node with Slot 0 is rejected because the store never infers a
	// slot from the id or from the account.
	slotKey := commit.Slot
	if slotKey.Index == 0 {
		slotKey.Index = 1
	}
	if slotKey.AccountID == "" {
		slotKey.AccountID = node.AccountID
	}
	if !slotKey.Valid() || slotKey.AccountID != node.AccountID || slotKey.Index != node.Slot {
		return model.ProxyNode{}, "", invalidf(
			"switch commit slot %q does not match node %q (slot %d)",
			slotKey.String(), node.ID, node.Slot)
	}
	// The old node and its lease must survive a device release that landed
	// while the new session was opening, so this runs before any mutation.
	if err := s.requireSlotDeviceLocked(slotKey, commit.DeviceRecordID); err != nil {
		return model.ProxyNode{}, "", err
	}
	var slotRow *model.AccountSlot
	if commit.SlotState != nil {
		planned := *commit.SlotState
		if planned.SlotKey != slotKey {
			return model.ProxyNode{}, "", invalidf(
				"switch commit slot %q does not match slot state %q",
				slotKey.String(), planned.SlotKey.String())
		}
		slotRow = &planned
	}

	// Plan the deletion of every other node of the same slot, and validate the
	// new row against the rows that will remain, before mutating anything. A
	// failure here must leave the old node and its lease intact. A slot-1
	// switch never touches slot 2, and a legacy/seeded row (Slot 0) is never
	// collateral.
	removed := make(map[string]struct{}, len(s.nodes))
	kept := make([]model.ProxyNode, 0, len(s.nodes))
	for _, other := range s.nodes {
		if other.AccountID == node.AccountID && other.Slot == node.Slot && other.ID != node.ID {
			removed[other.ID] = struct{}{}
			continue
		}
		kept = append(kept, other)
	}
	var existing *model.ProxyNode
	for i := range kept {
		if kept[i].ID != node.ID {
			if kept[i].Label == node.Label {
				return model.ProxyNode{}, "", conflictf("node label %q is already used by %q", node.Label, kept[i].ID)
			}
			continue
		}
		if kept[i].AccountID != node.AccountID {
			return model.ProxyNode{}, "", conflictf("node %q belongs to account %q", node.ID, kept[i].AccountID)
		}
		existing = &kept[i]
	}
	var selection *model.Selection
	if commit.Selection != nil {
		planned := *commit.Selection
		planned.AccountID = account.ID
		if strings.TrimSpace(planned.ServerID) == "" {
			return model.ProxyNode{}, "", invalidf("selection server id is required")
		}
		if planned.UpdatedAt == "" {
			planned.UpdatedAt = iso(s.now())
		}
		if planned.UpdatedBy == "" {
			planned.UpdatedBy = "operator"
		}
		selection = &planned
	}

	// The slot row is the only fallible write left, so it runs before the node
	// and lease mutations: a rejected row leaves the switch unapplied.
	if slotRow != nil {
		if err := s.upsertSlotLocked(*slotRow); err != nil {
			return model.ProxyNode{}, "", err
		}
	}
	// Everything below this line is infallible.
	s.nodes = kept
	// A lease on the replaced same-slot node is rebound, not deleted: a switch
	// inside one slot must not revoke a customer's access. A same-server
	// reconnect keeps the node id, so its lease is refreshed in place too: the
	// serving generation and the mirrored endpoint must advance with the node.
	// The generation comes from the slot row, never from a node field.
	slotGen := uint64(0)
	if slotRow != nil {
		slotGen = slotRow.Generation
	} else if current, ok := s.accountSlots[slotKey]; ok {
		slotGen = current.Generation
	}
	for i := range s.leases {
		_, drop := removed[s.leases[i].NodeID]
		if !drop && s.leases[i].NodeID != node.ID {
			continue
		}
		s.leases[i].NodeID = node.ID
		s.leases[i].NodeLabel = node.Label
		s.leases[i].Region = node.Region
		s.leases[i].EgressIP = node.EgressIP
		s.leases[i].GatePort = node.GatePort
		s.leases[i].Protocol = node.Protocol
		s.leases[i].Slot = node.Slot
		s.leases[i].Generation = slotGen
	}
	// InUse is derived, never inherited: it is the number of active leases that
	// now point at the new node. (s.leases holds active rows only.)
	node.InUse = 0
	for _, lease := range s.leases {
		if lease.NodeID == node.ID {
			node.InUse++
		}
	}
	if existing != nil {
		if node.SuccessRate == 0 {
			node.SuccessRate = existing.SuccessRate
		}
		if node.LastProbeAt == "" {
			node.LastProbeAt = existing.LastProbeAt
		}
		*existing = node
	} else {
		if node.LastProbeAt == "" {
			node.LastProbeAt = iso(s.now())
		}
		s.nodes = append(s.nodes, node)
	}

	if selection != nil {
		s.selections[account.ID] = *selection
	}
	if commit.Stat != nil {
		stat := cloneServerStat(*commit.Stat)
		stat.AccountID = account.ID
		if stat.ServerID == "" {
			stat.ServerID = node.ServerID
		}
		now := iso(s.now())
		stat.Attempts++
		stat.Successes++
		stat.LastAttemptAt = now
		stat.LastSuccessAt = now
		stat.LastErrorClass = ""
		s.serverStats[serverStatKey(stat.AccountID, stat.ServerID)] = stat
	}
	s.syncAccountCountsLocked()
	auditID := s.addAuditLocked(model.SeverityInfo, auditActor(commit.Actor), commit.Action,
		commit.Target, commit.Detail, commit.Meta)
	return node, auditID, nil
}

// RecordCatalogAttempt bumps the failed-attempt counter and writes one audit
// event. It never changes a node, so a failed switch leaves the old session in
// place.
func (s *Store) RecordCatalogAttempt(rec AttemptRecord) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.accountIndexLocked(rec.AccountID) < 0 {
		return "", notFoundf("account %q not found", rec.AccountID)
	}
	if strings.TrimSpace(rec.ServerID) == "" {
		return "", invalidf("server id is required")
	}
	if strings.TrimSpace(rec.Action) == "" {
		return "", invalidf("attempt action is required")
	}
	key := serverStatKey(rec.AccountID, rec.ServerID)
	stat := s.serverStats[key]
	stat.AccountID = rec.AccountID
	stat.ServerID = rec.ServerID
	stat.Attempts++
	stat.LastAttemptAt = iso(s.now())
	stat.LastErrorClass = rec.ErrorClass
	s.serverStats[key] = stat
	slotKey := rec.Slot
	if slotKey.Index == 0 {
		slotKey.Index = 1
	}
	if slotKey.AccountID == "" {
		slotKey.AccountID = rec.AccountID
	}
	meta := make(map[string]string, len(rec.Meta)+2)
	for name, value := range rec.Meta {
		meta[name] = value
	}
	meta["slot"] = slotKey.String()
	if slot, ok := s.accountSlots[slotKey]; ok {
		meta["generation"] = strconv.FormatUint(slot.Generation, 10)
	}
	return s.addAuditLocked(model.SeverityWarn, "operator", rec.Action,
		rec.Target, rec.Detail, meta), nil
}

// ServerStat returns one account/server attempt counter.
func (s *Store) ServerStat(accountID, serverID string) (model.ServerStat, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	stat, ok := s.serverStats[serverStatKey(accountID, serverID)]
	return cloneServerStat(stat), ok
}

// SetNodeStateQuiet updates one node's state without writing an audit event or
// touching leases. The switch state machine uses it for the DRAINING mark and
// its rollback, because the single catalog.switch audit covers the whole
// transition. It does not reopen a gate.
func (s *Store) SetNodeStateQuiet(id string, state model.NodeState) error {
	if !validNodeState(state) {
		return invalidf("node %q state %q is not supported", id, state)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.nodeIndexLocked(id)
	if idx < 0 {
		return notFoundf("node %q not found", id)
	}
	s.nodes[idx].State = state
	s.nodes[idx].LastProbeAt = iso(s.now())
	s.syncAccountCountsLocked()
	return nil
}

// DeleteNodeQuiet removes one node and its leases without the generic
// node.delete audit. Disconnect owns its single catalog.disconnect audit.
func (s *Store) DeleteNodeQuiet(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.nodeIndexLocked(id)
	if idx < 0 {
		return notFoundf("node %q not found", id)
	}
	s.nodes = append(s.nodes[:idx], s.nodes[idx+1:]...)
	kept := s.leases[:0]
	for _, lease := range s.leases {
		if lease.NodeID == id {
			continue
		}
		kept = append(kept, lease)
	}
	s.leases = kept
	s.syncAccountCountsLocked()
	return nil
}

// WriteAudit appends one audit event without touching catalog state. It is for
// events that are not an attempt (for example catalog.switch_cleanup_failed).
func (s *Store) WriteAudit(severity model.Severity, actor, action, target, detail string, meta map[string]string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addAuditLocked(severity, actor, action, target, detail, meta)
}

func serverStatKey(accountID, serverID string) string {
	return accountID + ":" + serverID
}

func auditActor(actor string) string {
	if actor = strings.TrimSpace(actor); actor != "" {
		return actor
	}
	return "operator"
}

func cloneServerStat(stat model.ServerStat) model.ServerStat {
	if stat.LastLatencyMs != nil {
		value := *stat.LastLatencyMs
		stat.LastLatencyMs = &value
	}
	return stat
}
