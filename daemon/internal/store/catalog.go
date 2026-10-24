package store

import (
	"strings"

	"ajsaccountd/internal/model"
)

// Catalog state: the persisted operator selection and the per-server attempt
// counters (DESIGN-CATALOG §3.4, §3.8). This file is the only writer of those
// maps; every exported mutation writes exactly one audit event, except the
// quiet node helpers used inside the switch state machine, whose single
// catalog.switch/catalog.connect audit covers the whole transition.

// SwitchCommit is one atomic switch commit: delete every other node of the
// account (and their leases), put the new node LIVE, optionally persist the
// operator selection, bump the server stat and write the catalog audit. The
// caller holds no store lock; the whole commit happens under one write lock.
type SwitchCommit struct {
	Node   model.ProxyNode
	Actor  string
	Action string
	Target string
	Detail string
	Meta   map[string]string
	// Selection is set for a manual connect/switch only. A rotation or login
	// sync passes nil so it never overwrites the operator's target.
	Selection *model.Selection
	// Stat is the per-server counter to bump on success.
	Stat *model.ServerStat
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

	// Plan the deletion of every other node of the account and its leases, and
	// validate the new row against the rows that will remain, before mutating
	// anything. A failure here must leave the old node and its lease intact.
	removed := make(map[string]struct{}, len(s.nodes))
	kept := make([]model.ProxyNode, 0, len(s.nodes))
	for _, other := range s.nodes {
		if other.AccountID == node.AccountID && other.ID != node.ID {
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

	// Everything below this line is infallible.
	s.nodes = kept
	if len(removed) > 0 {
		keptLeases := s.leases[:0]
		for _, lease := range s.leases {
			if _, drop := removed[lease.NodeID]; drop {
				continue
			}
			keptLeases = append(keptLeases, lease)
		}
		s.leases = keptLeases
	}
	if existing != nil {
		if node.InUse == 0 {
			node.InUse = existing.InUse
		}
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
	return s.addAuditLocked(model.SeverityWarn, "operator", rec.Action,
		rec.Target, rec.Detail, rec.Meta), nil
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
