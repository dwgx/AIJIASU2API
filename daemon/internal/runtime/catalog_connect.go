package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"ajsaccountd/internal/account"
	"ajsaccountd/internal/model"
	"ajsaccountd/internal/store"
	"ajsproxy/apifacade"
)

// DefaultConnectCooldown is the per-account minimum interval between two
// switch attempts (DESIGN-CATALOG §3.5 step 6).
const DefaultConnectCooldown = 5 * time.Second

// switchDrainGrace bounds the drain that runs before a new session is opened.
// A timeout is logged and the switch continues; Close force-closes the flows
// after the gate's own DrainGrace.
const switchDrainGrace = 10 * time.Second

// Catalog switch reasons. They are the error.reason vocabulary of
// DESIGN-CATALOG §3.5/§3.10; the API maps them onto HTTP codes.
const (
	ReasonLoginRequired        = "login_required"
	ReasonLoginDisabled        = "login_disabled"
	ReasonAccountState         = "account_state"
	ReasonUnknownServer        = "unknown_server"
	ReasonSnapshotChanged      = "snapshot_changed"
	ReasonServerIneligible     = "server_ineligible"
	ReasonSwitchInProgress     = "switch_in_progress"
	ReasonConnectCooldown      = "connect_cooldown"
	ReasonSlotMultipleSessions = "slot_multiple_sessions"
	ReasonPortUnavailable      = "port_unavailable"
	ReasonSessionUnverified    = "session_unverified"
	ReasonSessionOpen          = "session_open_failed"
	ReasonGateStart            = "gate_start_failed"
	ReasonStoreFailed          = "store_failed"
	ReasonReconcileFailed      = "reconcile_failed"
	ReasonLeaseWithdrawFailed  = "lease_withdraw_failed"
	ReasonNoTarget             = "no_target"
)

// ReasonMetadataOnly is the rollback value of a switch whose old session was
// already closed: the metadata (node/slot rows) is consistent, but the old
// session cannot be restored without risking a vendor kick.
const ReasonMetadataOnly = "metadata_only"

// SwitchError is a guarded-connect failure with a stable reason. The message is
// client-safe; Cause stays in the log and is never serialized.
type SwitchError struct {
	Reason     string
	Message    string
	Details    map[string]string
	RetryAfter time.Duration
	Cause      error
}

// Error implements error.
func (e *SwitchError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return e.Reason
}

// Unwrap exposes the internal cause to errors.Is in logs/tests.
func (e *SwitchError) Unwrap() error { return e.Cause }

// SwitchResult is the 200 body of connect/reconnect. AlreadyConnected marks the
// idempotent "the requested server is already current" answer, which changes no
// state and writes no audit.
type SwitchResult struct {
	AccountID        string          `json:"accountId"`
	ServerID         string          `json:"serverId"`
	NodeID           string          `json:"nodeId"`
	NodeState        model.NodeState `json:"nodeState"`
	GatePort         int             `json:"gatePort"`
	EgressIP         string          `json:"egressIp"`
	SwitchedFrom     string          `json:"switchedFrom,omitempty"`
	DurationMs       int64           `json:"durationMs"`
	Warnings         []string        `json:"warnings"`
	AuditID          string          `json:"auditId,omitempty"`
	AlreadyConnected bool            `json:"alreadyConnected,omitempty"`
	Rollback         string          `json:"rollback,omitempty"`
	// Slot is the one-based account slot the switch targeted. Generation is
	// the slot generation after a successful open. PrevNodeID is the closed
	// node's exact id, never reconstructed from the account and server.
	Slot       int    `json:"slot"`
	Generation uint64 `json:"generation,omitempty"`
	PrevNodeID string `json:"prevNodeId,omitempty"`
}

// SlotDisconnectResult is one slot's outcome in a disconnect-all.
type SlotDisconnectResult struct {
	Slot        int    `json:"slot"`
	ServerID    string `json:"serverId,omitempty"`
	NodeID      string `json:"nodeId,omitempty"`
	Forced      bool   `json:"forced"`
	ActiveFlows int    `json:"activeFlows"`
}

// DisconnectResult is the 200 body of disconnect. Slot is set only when exactly
// one slot was disconnected, so a single-slot caller sees the legacy shape plus
// slot 1; a cascade reports every slot in Slots.
type DisconnectResult struct {
	AccountID           string                 `json:"accountId"`
	ServerID            string                 `json:"serverId,omitempty"`
	NodeID              string                 `json:"nodeId,omitempty"`
	AlreadyDisconnected bool                   `json:"alreadyDisconnected,omitempty"`
	Forced              bool                   `json:"forced"`
	ActiveFlows         int                    `json:"activeFlows"`
	DurationMs          int64                  `json:"durationMs"`
	Warnings            []string               `json:"warnings"`
	AuditID             string                 `json:"auditId,omitempty"`
	Slot                int                    `json:"slot,omitempty"`
	Slots               []SlotDisconnectResult `json:"slots,omitempty"`
}

// ServerLookup is the retained-catalog view the selection handler validates
// before persisting a target. It performs no vendor I/O.
type ServerLookup struct {
	Title            string
	Found            bool
	Eligible         bool
	IneligibleReason string
	// LoginAvailable is false when no retained login result exists for the
	// account; every other field is then zero.
	LoginAvailable bool
}

// CatalogServer is one retained-catalog entry as the scheduler sees it. It is
// read-only and carries no credentials.
type CatalogServer struct {
	ID       string
	Title    string
	Region   string
	Eligible bool
}

// CatalogServers returns the account's retained catalog in server-id order.
// The boolean is false when no login result is retained, which the scheduler
// treats as login_required rather than an empty catalog.
func (c *NodeController) CatalogServers(accountID string) ([]CatalogServer, bool) {
	result, ok := c.logins.Result(accountID)
	if !ok {
		return nil, false
	}
	servers := make([]CatalogServer, 0, len(result.Servers))
	for key, server := range result.Servers {
		id := strings.TrimSpace(server.ID)
		if id == "" {
			id = strings.TrimSpace(key)
		}
		if id == "" {
			continue
		}
		servers = append(servers, CatalogServer{
			ID:       id,
			Title:    strings.TrimSpace(server.Tit),
			Region:   nodeRegion(server),
			Eligible: server.OK(),
		})
	}
	sort.Slice(servers, func(i, j int) bool { return servers[i].ID < servers[j].ID })
	return servers, true
}

// LookupServer resolves one server in the account's retained login result.
func (c *NodeController) LookupServer(accountID, serverID string) ServerLookup {
	result, ok := c.logins.Result(accountID)
	if !ok {
		return ServerLookup{}
	}
	target, found := findServer(result.Servers, serverID)
	if !found {
		return ServerLookup{LoginAvailable: true}
	}
	lookup := ServerLookup{
		Title:          strings.TrimSpace(target.server.Tit),
		Found:          true,
		Eligible:       target.server.OK(),
		LoginAvailable: true,
	}
	if !lookup.Eligible {
		lookup.IneligibleReason = "vendor_status"
	}
	return lookup
}

// SnapshotID is the catalog snapshot identity: a hash of every retained login
// result's account, fetch time and server ids, sorted. It changes on login and
// on Forget, and is stable across reads. An empty catalog still has an id.
func (c *NodeController) SnapshotID() string {
	accounts := c.store.Accounts()
	parts := make([]string, 0, len(accounts))
	for _, acct := range accounts {
		result, ok := c.logins.Result(acct.ID)
		if !ok {
			continue
		}
		ids := make([]string, 0, len(result.Servers))
		for key, server := range result.Servers {
			id := strings.TrimSpace(server.ID)
			if id == "" {
				id = strings.TrimSpace(key)
			}
			if id != "" {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
		parts = append(parts, acct.ID+"|"+result.FetchedAt.UTC().Format(time.RFC3339Nano)+"|"+strings.Join(ids, ","))
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return "cat-" + hex.EncodeToString(sum[:])[:12]
}

// ConnectSlot is the guarded switch of one account slot to serverID
// (DESIGN-CATALOG §3.5, B5). It validates the target and the slot's reserved
// port pair before touching the old session, closes the old session before the
// new one opens (the vendor allows one session per identity), commits exactly
// one LIVE node and the slot row together, and reports metadata_only when a
// failure after the close leaves nothing to restore.
func (c *NodeController) ConnectSlot(ctx context.Context, key model.SlotKey, serverID, snapshotID, trigger string) (SwitchResult, error) {
	start := c.now()
	trigger = switchTrigger(trigger, "manual")
	if !key.Valid() {
		return SwitchResult{}, fmt.Errorf("runtime: slot key %q is invalid", key.String())
	}
	release, err := c.beginSwitch(key, true)
	if err != nil {
		return SwitchResult{}, err
	}
	defer release()

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped {
		return SwitchResult{}, ErrControllerStopped
	}
	acct, result, target, err := c.resolveTarget(key.AccountID, serverID)
	if err != nil {
		return SwitchResult{}, err
	}
	if err := c.checkSnapshot(snapshotID); err != nil {
		return SwitchResult{}, err
	}
	old, err := c.slotGateLocked(key)
	if err != nil {
		return SwitchResult{}, err
	}
	if old != nil && old.serverID == target.id && trigger != "reconnect" {
		id := nodeID(key, target.id)
		res := SwitchResult{
			AccountID:        key.AccountID,
			ServerID:         target.id,
			NodeID:           id,
			NodeState:        model.NodeLive,
			GatePort:         old.port,
			EgressIP:         old.egressIP,
			DurationMs:       c.now().Sub(start).Milliseconds(),
			Warnings:         []string{},
			AlreadyConnected: true,
			Slot:             key.Index,
			Generation:       old.generation,
		}
		if node, ok := c.store.Node(id); ok {
			res.NodeState = node.State
			res.GatePort = node.GatePort
			res.EgressIP = node.EgressIP
		}
		return res, nil
	}
	c.noteAttempt(key)
	res, err := c.switchSlotLocked(ctx, acct, result, target, old, key, trigger, start)
	if err != nil {
		// A break-before-make failure revoked the old endpoint; the panel must
		// stop advertising it in the same request, not on a later sync.
		if res.Rollback == ReasonMetadataOnly {
			if reconcileErr := c.reconcileLocked(ctx); reconcileErr != nil {
				c.recordReconcileLoss(&res, trigger, reconcileErr)
			}
		}
		annotateSwitchError(err, res)
		return res, err
	}
	if reconcileErr := c.reconcileLocked(ctx); reconcileErr != nil {
		c.recordReconcileLoss(&res, trigger, reconcileErr)
	}
	return res, nil
}

// ConnectServer is the slot-1 compatibility wrapper of ConnectSlot.
func (c *NodeController) ConnectServer(ctx context.Context, accountID, serverID, snapshotID, trigger string) (SwitchResult, error) {
	return c.ConnectSlot(ctx, model.SlotKey{AccountID: accountID, Index: 1}, serverID, snapshotID, trigger)
}

// ReconnectSlot reopens one slot's current server, or slot 1's selection when
// nothing is running (DESIGN-CATALOG §3.5). It always performs a real reopen,
// so the same-server case does not take the already-connected shortcut.
func (c *NodeController) ReconnectSlot(ctx context.Context, key model.SlotKey, snapshotID, trigger string) (SwitchResult, error) {
	start := c.now()
	trigger = switchTrigger(trigger, "reconnect")
	if !key.Valid() {
		return SwitchResult{}, fmt.Errorf("runtime: slot key %q is invalid", key.String())
	}
	release, err := c.beginSwitch(key, true)
	if err != nil {
		return SwitchResult{}, err
	}
	defer release()

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped {
		return SwitchResult{}, ErrControllerStopped
	}
	if _, ok := c.store.Account(key.AccountID); !ok {
		return SwitchResult{}, &store.Error{Kind: store.ErrNotFound, Message: fmt.Sprintf("account %q not found", key.AccountID)}
	}
	old, err := c.slotGateLocked(key)
	if err != nil {
		return SwitchResult{}, err
	}
	serverID := ""
	switch {
	case old != nil:
		serverID = old.serverID
	case key.Index == 1:
		if selection, ok := c.store.Selection(key.AccountID); ok {
			serverID = selection.ServerID
		}
	}
	if serverID == "" && key.Index > 1 {
		// Slot 1 keeps the legacy rule (current gate, else selection); higher
		// slots have no selection, so their row's target is the fallback.
		if slot, ok := c.store.Slot(key); ok {
			serverID = slot.ServerID
		}
	}
	if serverID == "" {
		return SwitchResult{}, &SwitchError{Reason: ReasonNoTarget, Message: "slot has no current server and no target"}
	}
	acct, result, target, err := c.resolveTarget(key.AccountID, serverID)
	if err != nil {
		return SwitchResult{}, err
	}
	if err := c.checkSnapshot(snapshotID); err != nil {
		return SwitchResult{}, err
	}
	c.noteAttempt(key)
	res, err := c.switchSlotLocked(ctx, acct, result, target, old, key, trigger, start)
	if err != nil {
		if res.Rollback == ReasonMetadataOnly {
			if reconcileErr := c.reconcileLocked(ctx); reconcileErr != nil {
				c.recordReconcileLoss(&res, trigger, reconcileErr)
			}
		}
		annotateSwitchError(err, res)
		return res, err
	}
	if reconcileErr := c.reconcileLocked(ctx); reconcileErr != nil {
		c.recordReconcileLoss(&res, trigger, reconcileErr)
	}
	return res, nil
}

// ReconnectAccount is the slot-1 compatibility wrapper of ReconnectSlot.
func (c *NodeController) ReconnectAccount(ctx context.Context, accountID, snapshotID, trigger string) (SwitchResult, error) {
	return c.ReconnectSlot(ctx, model.SlotKey{AccountID: accountID, Index: 1}, snapshotID, trigger)
}

// DisconnectSlot drains and closes one slot's gate, deletes its node and
// leases without the generic node.delete audit, marks the slot DOWN while
// keeping its target and port pair, and writes one catalog.disconnect. A second
// call is a no-op.
func (c *NodeController) DisconnectSlot(ctx context.Context, key model.SlotKey) (DisconnectResult, error) {
	start := c.now()
	if !key.Valid() {
		return DisconnectResult{}, fmt.Errorf("runtime: slot key %q is invalid", key.String())
	}
	release, err := c.beginSwitch(key, false)
	if err != nil {
		return DisconnectResult{}, err
	}
	defer release()

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped {
		return DisconnectResult{}, ErrControllerStopped
	}
	acct, ok := c.store.Account(key.AccountID)
	if !ok {
		return DisconnectResult{}, &store.Error{Kind: store.ErrNotFound, Message: fmt.Sprintf("account %q not found", key.AccountID)}
	}
	result := DisconnectResult{AccountID: acct.ID, Warnings: []string{}}
	running, err := c.slotGateLocked(key)
	if err != nil {
		return DisconnectResult{}, err
	}
	if running == nil {
		result.AlreadyDisconnected = true
		result.DurationMs = c.now().Sub(start).Milliseconds()
		return result, nil
	}
	// Capture the entitlement and generation before the node row goes away:
	// the audit must name what the disconnect released.
	leases := c.store.NodeLeases(nodeID(key, running.serverID))
	generation := uint64(0)
	if slot, ok := c.store.Slot(key); ok {
		generation = slot.Generation
	}
	entry := c.disconnectSlotLocked(ctx, key, running)
	result.Slot = entry.Slot
	result.ServerID = entry.ServerID
	result.NodeID = entry.NodeID
	result.Forced = entry.Forced
	result.ActiveFlows = entry.ActiveFlows
	result.DurationMs = c.now().Sub(start).Milliseconds()
	meta := map[string]string{
		"nodeId":      entry.NodeID,
		"serverId":    entry.ServerID,
		"slot":        key.String(),
		"generation":  strconv.FormatUint(generation, 10),
		"forced":      strconv.FormatBool(entry.Forced),
		"activeFlows": strconv.Itoa(entry.ActiveFlows),
		"durationMs":  strconv.FormatInt(result.DurationMs, 10),
	}
	if len(leases) > 0 {
		ids := make([]string, 0, len(leases))
		for _, lease := range leases {
			ids = append(ids, lease.ID)
		}
		meta["leases"] = strconv.Itoa(len(leases))
		meta["leaseIds"] = strings.Join(ids, ",")
	}
	auditID, auditErr := c.store.WriteAudit(model.SeverityInfo, "operator", "catalog.disconnect",
		entry.NodeID, "Session disconnected · target kept", meta)
	if auditErr != nil {
		c.logger.Error("catalog_audit_persist_failed", "node_id", entry.NodeID, "error", auditErr)
	}
	result.AuditID = auditID
	if reconcileErr := c.reconcileLocked(ctx); reconcileErr != nil {
		c.logger.Warn("catalog_reconcile_failed", "account_id", acct.ID, "action", "disconnect", "error", reconcileErr)
		result.Warnings = append(result.Warnings, ReasonReconcileFailed)
	}
	return result, nil
}

// DisconnectAccount drains and closes every gate of one account, highest slot
// first, and reports each slot in Slots. Slot is set only when exactly one slot
// was disconnected, so a single-slot caller keeps the legacy shape.
func (c *NodeController) DisconnectAccount(ctx context.Context, accountID string) (DisconnectResult, error) {
	start := c.now()
	release, err := c.beginAccountMutation(accountID)
	if err != nil {
		return DisconnectResult{}, err
	}
	defer release()

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped {
		return DisconnectResult{}, ErrControllerStopped
	}
	if _, ok := c.store.Account(accountID); !ok {
		return DisconnectResult{}, &store.Error{Kind: store.ErrNotFound, Message: fmt.Sprintf("account %q not found", accountID)}
	}
	result := DisconnectResult{AccountID: accountID, Warnings: []string{}}
	type slotAudit struct {
		entry      SlotDisconnectResult
		generation uint64
		leaseIDs   []string
	}
	slots := c.store.Slots(accountID)
	audits := make([]slotAudit, 0, len(slots))
	for i := len(slots) - 1; i >= 0; i-- {
		key := slots[i].SlotKey
		running, err := c.slotGateLocked(key)
		if err != nil {
			return result, err
		}
		if running == nil {
			continue
		}
		// Capture generation and entitlement before the node row is deleted:
		// the aggregate audit must identify every affected slot, node and
		// lease, or a cascade cannot be reconstructed later.
		generation := uint64(0)
		if slot, ok := c.store.Slot(key); ok {
			generation = slot.Generation
		}
		leases := c.store.NodeLeases(nodeID(key, running.serverID))
		leaseIDs := make([]string, 0, len(leases))
		for _, lease := range leases {
			leaseIDs = append(leaseIDs, lease.ID)
		}
		entry := c.disconnectSlotLocked(ctx, key, running)
		result.Slots = append(result.Slots, entry)
		result.Forced = result.Forced || entry.Forced
		result.ActiveFlows += entry.ActiveFlows
		audits = append(audits, slotAudit{entry: entry, generation: generation, leaseIDs: leaseIDs})
	}
	if len(result.Slots) == 0 {
		result.AlreadyDisconnected = true
		result.DurationMs = c.now().Sub(start).Milliseconds()
		return result, nil
	}
	if len(result.Slots) == 1 {
		result.Slot = result.Slots[0].Slot
		result.ServerID = result.Slots[0].ServerID
		result.NodeID = result.Slots[0].NodeID
	}
	result.DurationMs = c.now().Sub(start).Milliseconds()
	auditTarget := result.NodeID
	if auditTarget == "" {
		auditTarget = accountID
	}
	meta := map[string]string{
		"nodeId":      result.NodeID,
		"serverId":    result.ServerID,
		"slots":       strconv.Itoa(len(result.Slots)),
		"forced":      strconv.FormatBool(result.Forced),
		"activeFlows": strconv.Itoa(result.ActiveFlows),
		"durationMs":  strconv.FormatInt(result.DurationMs, 10),
	}
	for _, audit := range audits {
		prefix := "slot." + strconv.Itoa(audit.entry.Slot) + "."
		meta[prefix+"nodeId"] = audit.entry.NodeID
		meta[prefix+"serverId"] = audit.entry.ServerID
		meta[prefix+"generation"] = strconv.FormatUint(audit.generation, 10)
		if len(audit.leaseIDs) > 0 {
			meta[prefix+"leaseIds"] = strings.Join(audit.leaseIDs, ",")
		}
	}
	auditID, auditErr := c.store.WriteAudit(model.SeverityInfo, "operator", "catalog.disconnect",
		auditTarget, "Session disconnected · target kept", meta)
	if auditErr != nil {
		c.logger.Error("catalog_audit_persist_failed", "node_id", auditTarget, "error", auditErr)
	}
	result.AuditID = auditID
	if reconcileErr := c.reconcileLocked(ctx); reconcileErr != nil {
		c.logger.Warn("catalog_reconcile_failed", "account_id", accountID, "action", "disconnect", "error", reconcileErr)
		result.Warnings = append(result.Warnings, ReasonReconcileFailed)
	}
	return result, nil
}

// disconnectSlotLocked drains one slot, closes its gate before its session,
// deletes the node (and its leases), and marks the slot down. The slot keeps
// its DeviceID, ServerID and port pair so a manual disconnect does not move the
// customer's endpoint.
func (c *NodeController) disconnectSlotLocked(ctx context.Context, key model.SlotKey, running *nodeGate) SlotDisconnectResult {
	id := nodeID(key, running.serverID)
	entry := SlotDisconnectResult{Slot: key.Index, ServerID: running.serverID, NodeID: id}
	if stateful, ok := running.gate.(GateState); ok {
		entry.ActiveFlows = stateful.State().Active
	}
	_ = c.store.SetNodeStateQuiet(id, model.NodeDraining)
	drainCtx, cancel := context.WithTimeout(ctx, switchDrainGrace)
	drainErr := running.gate.Drain(drainCtx)
	cancel()
	if drainErr != nil {
		entry.Forced = true
		c.logger.Warn("catalog_disconnect_drain_failed", "account_id", key.AccountID, "node_id", id, "error", drainErr)
	}
	if errs := c.closeSlotLocked(key); len(errs) > 0 {
		entry.Forced = true
		c.logger.Warn("catalog_disconnect_close_failed", "account_id", key.AccountID, "node_id", id, "error", errors.Join(errs...))
	}
	if err := c.store.DeleteNodeQuiet(id); err != nil && !errors.Is(err, store.ErrNotFound) {
		c.logger.Warn("catalog_disconnect_store_failed", "account_id", key.AccountID, "node_id", id, "error", err)
	}
	if err := c.store.SetSlotState(key, model.SlotDown, model.SlotHealthUnknown, "disconnected"); err != nil {
		c.logger.Warn("catalog_disconnect_slot_failed", "account_id", key.AccountID, "slot", key.String(), "error", err)
	}
	c.logger.Info("catalog_disconnected", "account_id", key.AccountID, "node_id", id,
		"server_id", running.serverID, "slot", key.String())
	return entry
}

// switchSlotLocked performs the slot-scoped break-before-make switch with the
// controller lock held. It never reconciles; the caller reconciles once after
// it returns.
func (c *NodeController) switchSlotLocked(ctx context.Context, acct model.Account, result account.LoginResult, target serverRef, old *nodeGate, key model.SlotKey, trigger string, start time.Time) (SwitchResult, error) {
	newID := nodeID(key, target.id)
	stepStart := c.now()
	oldServerID := ""
	oldNodeID := ""
	if old != nil {
		oldServerID = old.serverID
		oldNodeID = nodeID(key, old.serverID)
	}
	slot, ok := c.store.Slot(key)
	if !ok {
		return c.failSlotSwitch(ctx, acct, key, target, old, trigger, start, ReasonStoreFailed,
			"store_failed", "store", "slot row is missing", fmt.Errorf("runtime: slot %s not found", key.String()))
	}
	// A kicked slot may not be reopened before its retry window: the vendor
	// still holds the stolen session, so an early attempt would be kicked
	// again. The check runs before the old session closes, so a refusal leaves
	// a serving slot serving.
	if wait, blocked := slotKickWindow(slot, c.now()); blocked {
		res, err := c.failSlotSwitch(ctx, acct, key, target, old, trigger, start, ReasonConnectCooldown,
			"connect cooldown", "capacity", "the slot is waiting out its vendor-kick retry window",
			fmt.Errorf("runtime: slot %s is not restartable yet", key.String()))
		// failSlotSwitch is shared, so the remaining window is attached here:
		// the API turns it into the Retry-After header of the 429 answer.
		if switchErr, ok := err.(*SwitchError); ok {
			switchErr.RetryAfter = wait
		}
		return res, err
	}
	// Validate the pair before touching the old session: a missing pair fails
	// the attempt and leaves the slot serving.
	addrs, claimed, err := c.reserveSlotPairLocked(key, slot.GatePort)
	if err != nil {
		return c.failSlotSwitch(ctx, acct, key, target, old, trigger, start, ReasonPortUnavailable,
			"port_unavailable", "capacity", "the slot port pair is unavailable", err)
	}
	release := func() {
		if claimed {
			_ = c.store.SetSlotPorts(key, 0, 0)
		}
	}
	// Debugger step: the slot row, kick window and port pair are validated.
	// The attempted generation is the one the success path will commit.
	c.debug.Record(newDebugTrace(model.DebugKindSwitchStep, key, slot.Generation+1,
		newID, target.id, "validate", model.DebugStateOK, "", "", c.now().Sub(stepStart)))
	// Break before make: the vendor allows one session per identity, so the old
	// session closes before the new one opens. A failed close is reported as an
	// orphan gate and never blocks the switch.
	warnings := []string{}
	drainStart := c.now()
	if old != nil {
		if err := c.store.SetNodeStateQuiet(oldNodeID, model.NodeDraining); err != nil {
			c.logger.Warn("catalog_drain_store_failed", "account_id", acct.ID, "node_id", oldNodeID, "error", err)
		}
		drainCtx, cancel := context.WithTimeout(ctx, switchDrainGrace)
		drainErr := old.gate.Drain(drainCtx)
		cancel()
		if drainErr != nil {
			c.logger.Warn("catalog_drain_failed", "account_id", acct.ID, "node_id", oldNodeID, "error", drainErr)
		}
		old.draining = true
		if errs := c.closeSlotLocked(key); len(errs) > 0 {
			warnings = append(warnings, "orphan_gate")
			c.logger.Warn("catalog_switch_cleanup_failed", "account_id", acct.ID, "node_id", oldNodeID, "error", errors.Join(errs...))
			if _, auditErr := c.store.WriteAudit(model.SeverityWarn, "operator", "catalog.switch_cleanup_failed",
				oldNodeID, "Old gate did not close · switch committed", map[string]string{
					"errorClass": "gate",
				}); auditErr != nil {
				c.logger.Error("catalog_audit_persist_failed", "node_id", oldNodeID, "error", auditErr)
			}
		}
		delete(c.running, oldNodeID)
		delete(c.slotGates, key)
		c.debug.Record(newDebugTrace(model.DebugKindSwitchStep, key, slot.Generation+1,
			oldNodeID, oldServerID, "drain", model.DebugStateOK, strings.Join(warnings, ","), "", c.now().Sub(drainStart)))
	}

	openStart := c.now()
	device, recordID, err := c.slotDevice(key)
	if err != nil {
		release()
		c.markSlotDownLocked(key, acct, target, "device_material_missing")
		c.debug.Record(newDebugTrace(model.DebugKindSessionOpen, key, slot.Generation+1,
			newID, target.id, "device", model.DebugStateFailed, "account has no device identity", "unknown", c.now().Sub(openStart)))
		return c.failSlotSwitch(ctx, acct, key, target, old, trigger, start, ReasonSessionOpen,
			"session_open_failed", "unknown", "account has no device identity", err)
	}
	session, err := c.sessions.Open(ctx, account.SessionRequest{
		AccountID:       acct.ID,
		Slot:            key,
		Server:          target.server,
		Device:          device,
		UserName:        account.SlotUserName(baseUserName(result, acct), key.Index),
		ConnectPassword: result.ConnectPassword,
		Profile:         c.profile,
	})
	if err != nil {
		release()
		c.markSlotDownLocked(key, acct, target, "session_open_failed")
		c.debug.Record(newDebugTrace(model.DebugKindSessionOpen, key, slot.Generation+1,
			newID, target.id, "open", model.DebugStateFailed, "could not open the new session", classifyConnectError(err), c.now().Sub(openStart)))
		return c.failSlotSwitch(ctx, acct, key, target, old, trigger, start, ReasonSessionOpen,
			"session_open_failed", classifyConnectError(err), "could not open the new session", err)
	}
	if session.SID() == "" {
		_ = session.Close()
		release()
		c.markSlotDownLocked(key, acct, target, "session_unverified")
		c.debug.Record(newDebugTrace(model.DebugKindSessionOpen, key, slot.Generation+1,
			newID, target.id, "open", model.DebugStateFailed, "the vendor session has no id", "auth", c.now().Sub(openStart)))
		return c.failSlotSwitch(ctx, acct, key, target, old, trigger, start, ReasonSessionUnverified,
			"session_unverified", "auth", "the vendor session has no id", errors.New("session has no SID"))
	}
	c.debug.Record(newDebugTrace(model.DebugKindSessionOpen, key, slot.Generation+1,
		newID, target.id, "open", model.DebugStateOpen, "", "", c.now().Sub(openStart)))
	gateStart := c.now()
	g, bound, err := c.startGateLocked(key, target, session, addrs)
	if err != nil {
		_ = session.Close()
		release()
		c.markSlotDownLocked(key, acct, target, "gate_start_failed")
		c.debug.Record(newDebugTrace(model.DebugKindSwitchStep, key, slot.Generation+1,
			newID, target.id, "start_gate", model.DebugStateFailed, "could not start the new gate", "gate", c.now().Sub(gateStart)))
		return c.failSlotSwitch(ctx, acct, key, target, old, trigger, start, ReasonGateStart,
			"gate_start_failed", "gate", "could not start the new gate", err)
	}
	c.debug.Record(newDebugTrace(model.DebugKindSwitchStep, key, slot.Generation+1,
		newID, target.id, "start_gate", model.DebugStateOK, "", "", c.now().Sub(gateStart)))
	running := &nodeGate{
		gate: g, session: session, accountID: acct.ID, serverID: target.id,
		slot: key, generation: slot.Generation + 1,
		port: bound.socks, httpPort: bound.http,
		egressIP: sessionEgress(session), startedAt: c.now(),
	}
	c.running[newID] = running

	node := applyServerToNode(model.ProxyNode{
		ID:       newID,
		Capacity: gateFlowLimit,
	}, acct, key, target, model.NodeLive, bound.socks, running.egressIP)
	slotRow := model.AccountSlot{
		SlotKey: key, DeviceID: slot.DeviceID, ServerID: target.id,
		GatePort: bound.socks, HTTPPort: bound.http,
		State: model.SlotLive, Health: model.SlotHealthHealthy,
		Generation: slot.Generation + 1,
	}
	var selection *model.Selection
	// A slot-2 manual connect must not rewrite slot 1's selection.
	if key.Index == 1 && trigger == "manual" {
		selection = &model.Selection{
			AccountID: acct.ID,
			ServerID:  target.id,
			UpdatedAt: c.now().UTC().Format(time.RFC3339Nano),
			UpdatedBy: "operator",
		}
	}
	action := "catalog.connect"
	switch {
	case trigger == "reconnect":
		action = "catalog.reconnect"
	case old != nil:
		action = "catalog.switch"
	}
	duration := c.now().Sub(start).Milliseconds()
	meta := map[string]string{
		"serverId":   target.id,
		"nodeId":     newID,
		"slot":       key.String(),
		"generation": strconv.FormatUint(slotRow.Generation, 10),
		"gatePort":   strconv.Itoa(bound.socks),
		"durationMs": strconv.FormatInt(duration, 10),
		"trigger":    trigger,
		"result":     "ok",
	}
	if old != nil {
		meta["fromServerId"] = old.serverID
		meta["toServerId"] = target.id
		meta["prevNodeId"] = oldNodeID
	}
	commitStart := c.now()
	committed, auditID, err := c.store.CommitSwitch(store.SwitchCommit{
		Node:      node,
		Slot:      key,
		SlotState: &slotRow,
		Actor:     "operator",
		Action:    action,
		Target:    newID,
		Detail:    switchDetail(action),
		Meta:      meta,
		Selection: selection,
		Stat:      &model.ServerStat{AccountID: acct.ID, ServerID: target.id},
		// The release that landed while the new session was opening must fail
		// the commit instead of publishing an unowned identity.
		DeviceRecordID: recordID,
	})
	if err != nil {
		c.debug.Record(newDebugTrace(model.DebugKindSwitchStep, key, slotRow.Generation,
			newID, target.id, "commit", model.DebugStateFailed, "could not commit the switch", "store", c.now().Sub(commitStart)))
		c.logger.Warn("catalog_commit_failed", "account_id", acct.ID, "node_id", newID, "error", err)
		if errs := c.closeGateLocked(newID, running); len(errs) > 0 {
			c.logger.Warn("catalog_rollback_close_failed", "account_id", acct.ID, "node_id", newID, "error", errors.Join(errs...))
		}
		delete(c.running, newID)
		release()
		c.markSlotDownLocked(key, acct, target, "store_failed")
		return c.failSlotSwitch(ctx, acct, key, target, old, trigger, start, ReasonStoreFailed,
			"store_failed", "store", "could not commit the switch", err)
	}
	c.debug.Record(newDebugTrace(model.DebugKindSwitchStep, key, slotRow.Generation,
		newID, target.id, "commit", model.DebugStateOK, "", "", c.now().Sub(commitStart)))
	c.debug.Record(newDebugTrace(model.DebugKindSwitch, key, slotRow.Generation,
		newID, target.id, trigger, model.DebugStateOK, "", "", time.Duration(duration)*time.Millisecond))
	c.slotGates[key] = newID
	c.logger.Info("catalog_switch_succeeded", "account_id", acct.ID, "node_id", newID,
		"server_id", target.id, "from_server_id", oldServerID, "slot", key.String(),
		"trigger", trigger, "duration_ms", duration)
	return SwitchResult{
		AccountID:    acct.ID,
		ServerID:     target.id,
		NodeID:       committed.ID,
		NodeState:    committed.State,
		GatePort:     committed.GatePort,
		EgressIP:     committed.EgressIP,
		SwitchedFrom: oldServerID,
		DurationMs:   duration,
		Warnings:     warnings,
		AuditID:      auditID,
		Slot:         key.Index,
		Generation:   slotRow.Generation,
		PrevNodeID:   oldNodeID,
	}, nil
}

// failSlotSwitch records a failed attempt and returns the client-safe error.
// The rollback string is "restored" only while the old gate is still serving;
// after the break-before-make close it is metadata_only, because the old
// session cannot be reopened without risking a vendor kick.
//
// A metadata_only failure is also the containment step: the old listener is
// gone, so its lease and its publication are revoked here, in the same call,
// instead of surviving as a dead endpoint until a later cleanup deletes the
// node. That revocation is audited with the lease ids.
func (c *NodeController) failSlotSwitch(ctx context.Context, acct model.Account, key model.SlotKey, target serverRef, old *nodeGate, trigger string, start time.Time, reason, message, errClass, detail string, cause error) (SwitchResult, error) {
	duration := c.now().Sub(start).Milliseconds()
	oldServerID := ""
	oldNodeID := ""
	rollback := "none"
	warnings := []string{}
	if old != nil {
		oldServerID = old.serverID
		oldNodeID = nodeID(key, old.serverID)
		rollback = ReasonMetadataOnly
		if _, live := c.running[oldNodeID]; live {
			rollback = "restored"
		}
	}
	if old != nil && rollback == ReasonMetadataOnly {
		warnings = c.revokeClosedSlotLocked(ctx, key, oldNodeID, oldServerID, reason)
	}
	generation := uint64(0)
	if slot, ok := c.store.Slot(key); ok {
		generation = slot.Generation
	}
	action := "catalog.connect"
	switch {
	case trigger == "reconnect":
		action = "catalog.reconnect"
	case old != nil:
		action = "catalog.switch"
	}
	meta := map[string]string{
		"serverId":   target.id,
		"slot":       key.String(),
		"durationMs": strconv.FormatInt(duration, 10),
		"trigger":    trigger,
		"result":     "failed",
		"rollback":   rollback,
	}
	if generation > 0 {
		meta["generation"] = strconv.FormatUint(generation, 10)
	}
	if old != nil {
		meta["fromServerId"] = old.serverID
		meta["toServerId"] = target.id
		meta["prevNodeId"] = oldNodeID
	}
	if _, err := c.store.RecordCatalogAttempt(store.AttemptRecord{
		AccountID:  acct.ID,
		ServerID:   target.id,
		Action:     action,
		Target:     nodeID(key, target.id),
		Detail:     detail,
		Meta:       meta,
		ErrorClass: errClass,
		Slot:       key,
	}); err != nil {
		c.logger.Warn("catalog_attempt_record_failed", "account_id", acct.ID, "server_id", target.id, "error", err)
	}
	c.logger.Warn("catalog_connect_failed", "account_id", acct.ID, "server_id", target.id,
		"slot", key.String(), "reason", reason, "error_class", errClass, "rollback", rollback, "error", cause)
	c.debug.Record(newDebugTrace(model.DebugKindSwitch, key, generation,
		nodeID(key, target.id), target.id, trigger, model.DebugStateFailed,
		reason+": "+message, errClass, time.Duration(duration)*time.Millisecond))
	result := SwitchResult{
		AccountID:    acct.ID,
		ServerID:     target.id,
		NodeID:       nodeID(key, target.id),
		SwitchedFrom: oldServerID,
		DurationMs:   duration,
		Warnings:     warnings,
		Rollback:     rollback,
		Slot:         key.Index,
		Generation:   generation,
		PrevNodeID:   oldNodeID,
	}
	return result, &SwitchError{
		Reason:  reason,
		Message: message,
		Details: map[string]string{"rollback": rollback},
		Cause:   cause,
	}
}

// revokeClosedSlotLocked is the same-step containment of a failed
// break-before-make switch. The old listener is gone, so its lease must go with
// it: every active lease on the closed node is revoked (one audit row per
// lease), the node leaves service without a port or egress (a DOWN row must not
// advertise an endpoint), and one slot.switch_failed row names the revoked
// leases. When no sibling slot still serves the account, the external Resin
// lease is deleted in the same call: Resin must not keep leasing an account
// whose only local endpoint just died. It never touches a sibling slot's
// publication or lease.
func (c *NodeController) revokeClosedSlotLocked(ctx context.Context, key model.SlotKey, oldNodeID, oldServerID, reason string) []string {
	revoked, revokeErr := c.store.InvalidateNodeLeases(oldNodeID, "switch_failed:"+reason)
	if revokeErr != nil {
		c.logger.Error("lease_revoke_persist_failed", "node_id", oldNodeID, "error", revokeErr)
	}
	if node, ok := c.store.Node(oldNodeID); ok {
		node.State = model.NodeDown
		node.GatePort = 0
		node.EgressIP = ""
		if _, err := c.store.PutNode(node); err != nil {
			c.logger.Warn("catalog_revoke_node_failed", "account_id", key.AccountID,
				"node_id", oldNodeID, "error", err)
		}
	}
	generation := uint64(0)
	if slot, ok := c.store.Slot(key); ok {
		generation = slot.Generation
	}
	meta := map[string]string{
		"slot":     key.String(),
		"nodeId":   oldNodeID,
		"serverId": oldServerID,
		"reason":   reason,
		"leases":   strconv.Itoa(len(revoked)),
	}
	if generation > 0 {
		meta["generation"] = strconv.FormatUint(generation, 10)
	}
	if len(revoked) > 0 {
		ids := make([]string, 0, len(revoked))
		for _, lease := range revoked {
			ids = append(ids, lease.ID)
		}
		meta["leaseIds"] = strings.Join(ids, ",")
	}
	if _, auditErr := c.store.WriteAudit(model.SeverityWarn, "operator", "slot.switch_failed", oldNodeID,
		"Switch failed after the old session closed · lease revoked", meta); auditErr != nil {
		c.logger.Error("catalog_audit_persist_failed", "node_id", oldNodeID, "error", auditErr)
	}
	c.logger.Warn("catalog_switch_revoked", "account_id", key.AccountID, "slot", key.String(),
		"node_id", oldNodeID, "server_id", oldServerID, "leases", len(revoked), "reason", reason)

	warnings := []string{}
	withdrawer, ok := c.reconciler.(LeaseWithdrawer)
	if !ok || c.accountServingLocked(key.AccountID) {
		return warnings
	}
	if err := withdrawer.WithdrawLease(ctx, key.AccountID); err != nil {
		warnings = append(warnings, ReasonLeaseWithdrawFailed)
		if _, auditErr := c.store.WriteAudit(model.SeverityWarn, "operator", "catalog.lease_withdraw_failed", oldNodeID,
			"Switch failed after the old session closed · external lease withdrawal failed",
			map[string]string{
				"slot": key.String(), "nodeId": oldNodeID, "serverId": oldServerID,
				"accountId": key.AccountID, "reason": reason, "errorClass": "resin",
			}); auditErr != nil {
			c.logger.Error("catalog_audit_persist_failed", "node_id", oldNodeID, "error", auditErr)
		}
		c.logger.Warn("catalog_lease_withdraw_failed", "account_id", key.AccountID,
			"slot", key.String(), "node_id", oldNodeID, "error", err)
	}
	return warnings
}

// accountServingLocked reports whether any running gate still serves the
// account. Resin leases are per account, so a failed slot must not delete the
// external lease a sibling slot is still using.
func (c *NodeController) accountServingLocked(accountID string) bool {
	for _, running := range c.running {
		if running.accountID == accountID {
			return true
		}
	}
	return false
}

// recordReconcileLoss retains a post-switch publication failure instead of
// leaving it in the log. The audit row names the slot, its generation and the
// closed node, so a two-slot trace can attribute the loss to the exact switch;
// the warning is also mirrored into the result for the caller.
func (c *NodeController) recordReconcileLoss(res *SwitchResult, trigger string, reconcileErr error) {
	c.logger.Warn("catalog_reconcile_failed", "account_id", res.AccountID,
		"slot", res.Slot, "generation", res.Generation, "trigger", trigger, "error", reconcileErr)
	res.Warnings = append(res.Warnings, ReasonReconcileFailed)
	target := res.NodeID
	if res.PrevNodeID != "" {
		target = res.PrevNodeID
	}
	meta := map[string]string{
		"trigger":    trigger,
		"result":     "failed",
		"errorClass": "reconcile",
		"nodeId":     res.NodeID,
		"serverId":   res.ServerID,
	}
	if res.Slot > 0 {
		meta["slot"] = model.SlotKey{AccountID: res.AccountID, Index: res.Slot}.String()
	}
	if res.Generation > 0 {
		meta["generation"] = strconv.FormatUint(res.Generation, 10)
	}
	if res.PrevNodeID != "" {
		meta["prevNodeId"] = res.PrevNodeID
	}
	if _, auditErr := c.store.WriteAudit(model.SeverityWarn, "operator", "catalog.reconcile_failed", target,
		"Switch left the store consistent but the Resin publication failed · a dead endpoint may stay advertised", meta); auditErr != nil {
		c.logger.Error("catalog_audit_persist_failed", "node_id", target, "error", auditErr)
	}
}

// annotateSwitchError copies the failed switch's warnings and slot/generation
// attribution into the client-visible error details. Without this the API sees
// only the reason and the caller cannot tell a clean refusal from one whose
// publication was lost.
func annotateSwitchError(err error, res SwitchResult) {
	var switchErr *SwitchError
	if !errors.As(err, &switchErr) {
		return
	}
	if switchErr.Details == nil {
		switchErr.Details = make(map[string]string, 4)
	}
	if len(res.Warnings) > 0 {
		switchErr.Details["warnings"] = strings.Join(res.Warnings, ",")
	}
	if res.Slot > 0 {
		switchErr.Details["slot"] = strconv.Itoa(res.Slot)
	}
	if res.Generation > 0 {
		switchErr.Details["generation"] = strconv.FormatUint(res.Generation, 10)
	}
	if res.PrevNodeID != "" {
		switchErr.Details["prevNodeId"] = res.PrevNodeID
	}
}

// resolveTarget validates the account, its retained login result, the server
// and its eligibility. The snapshot check is separate because reconnect may
// resolve the target before it knows the caller's snapshot id.
func (c *NodeController) resolveTarget(accountID, serverID string) (model.Account, account.LoginResult, serverRef, error) {
	acct, ok := c.store.Account(accountID)
	if !ok {
		return model.Account{}, account.LoginResult{}, serverRef{}, &store.Error{
			Kind: store.ErrNotFound, Message: fmt.Sprintf("account %q not found", accountID),
		}
	}
	if acct.State != model.AccountActive {
		return model.Account{}, account.LoginResult{}, serverRef{}, &SwitchError{
			Reason: ReasonAccountState, Message: "account is not active",
		}
	}
	result, ok := c.logins.Result(accountID)
	if !ok {
		return model.Account{}, account.LoginResult{}, serverRef{}, &SwitchError{
			Reason: ReasonLoginRequired, Message: "no retained login result; log in again",
		}
	}
	target, found := findServer(result.Servers, serverID)
	if !found {
		return model.Account{}, account.LoginResult{}, serverRef{}, &SwitchError{
			Reason: ReasonUnknownServer, Message: "server is not in the retained catalog",
		}
	}
	if !target.server.OK() {
		return model.Account{}, account.LoginResult{}, serverRef{}, &SwitchError{
			Reason: ReasonServerIneligible, Message: "server is not eligible",
			Details: map[string]string{"ineligibleReason": "vendor_status"},
		}
	}
	return acct, result, target, nil
}

// checkSnapshot compares the caller's snapshot id with the current one. An
// empty id means "current" (the read paths may omit it).
func (c *NodeController) checkSnapshot(snapshotID string) error {
	snapshotID = strings.TrimSpace(snapshotID)
	if snapshotID == "" {
		return nil
	}
	current := c.SnapshotID()
	if snapshotID != current {
		return &SwitchError{
			Reason: ReasonSnapshotChanged, Message: "catalog changed since it was read",
			Details: map[string]string{"snapshotId": current},
		}
	}
	return nil
}

// slotGateLocked returns the one gate of one slot, or nil. Two gates for one
// slot is an invariant violation and is refused instead of guessed.
func (c *NodeController) slotGateLocked(key model.SlotKey) (*nodeGate, error) {
	var found *nodeGate
	for _, running := range c.running {
		if running.slot != key {
			continue
		}
		if found != nil {
			return nil, &SwitchError{
				Reason: ReasonSlotMultipleSessions, Message: "more than one session is running for the slot",
			}
		}
		found = running
	}
	return found, nil
}

// beginSwitch claims the per-slot switch marker and enforces the per-slot
// cooldown. It refuses while an account-wide mutation holds the account or
// while the account is fenced by a stop, so a delete/logout cannot race a start
// into an orphan gate. The returned release must be called once the attempt is
// finished.
func (c *NodeController) beginSwitch(key model.SlotKey, cooldown bool) (func(), error) {
	c.switchMu.Lock()
	defer c.switchMu.Unlock()
	if _, busy := c.accountBusy[key.AccountID]; busy {
		return nil, &SwitchError{
			Reason: ReasonSwitchInProgress, Message: "an account-wide operation is already in progress",
		}
	}
	if since, fenced := c.accountStopped(key.AccountID); fenced {
		return nil, &SwitchError{
			Reason:  ReasonAccountState,
			Message: "account is stopped; log in again",
			Details: map[string]string{"stoppedSince": since.UTC().Format(time.RFC3339)},
		}
	}
	if _, busy := c.switching[key]; busy {
		return nil, &SwitchError{
			Reason: ReasonSwitchInProgress, Message: "a switch is already in progress for this slot",
		}
	}
	if cooldown && c.cooldown > 0 {
		if last, ok := c.lastAttempt[key]; ok {
			if remaining := c.cooldown - c.now().Sub(last); remaining > 0 {
				return nil, &SwitchError{
					Reason: ReasonConnectCooldown, Message: "connect cooldown",
					RetryAfter: remaining,
				}
			}
		}
	}
	done := make(chan struct{})
	c.switching[key] = done
	return func() {
		c.switchMu.Lock()
		if current, ok := c.switching[key]; ok && current == done {
			delete(c.switching, key)
			close(done)
		}
		c.switchMu.Unlock()
	}, nil
}

// beginAccountMutation claims the account for a mutation that spans slots
// (disconnect-all, login sync). It refuses while any slot of the account is
// switching, and every slot switch refuses while the account is busy, so a
// per-slot switch can never interleave with a cascade or a sync.
//
// The returned release closes the claim channel, which wakes a waiting
// beginAccountStop. It must be called exactly once.
func (c *NodeController) beginAccountMutation(accountID string) (func(), error) {
	c.switchMu.Lock()
	defer c.switchMu.Unlock()
	if _, busy := c.accountBusy[accountID]; busy {
		return nil, &SwitchError{Reason: ReasonSwitchInProgress,
			Message: "an account-wide operation is already in progress"}
	}
	for key := range c.switching {
		if key.AccountID == accountID {
			return nil, &SwitchError{Reason: ReasonSwitchInProgress,
				Message: "a slot switch is already in progress for this account"}
		}
	}
	done := make(chan struct{})
	c.accountBusy[accountID] = done
	return func() {
		c.switchMu.Lock()
		if current, ok := c.accountBusy[accountID]; ok && current == done {
			delete(c.accountBusy, accountID)
			close(done)
		}
		c.switchMu.Unlock()
	}, nil
}

// beginAccountMutationWait is beginAccountMutation for a login sync: a
// concurrent sync, slot switch or account-wide mutation is waited out and
// retried instead of refused. A stop claim also makes the caller wait; once the
// stop's store mutation commits, the sync's own account-state check refuses it,
// so the wait can never reopen a paused/deleted account.
func (c *NodeController) beginAccountMutationWait(ctx context.Context, accountID string) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		release, err := c.beginAccountMutation(accountID)
		if err == nil {
			return release, nil
		}
		var switchErr *SwitchError
		if !errors.As(err, &switchErr) || switchErr.Reason != ReasonSwitchInProgress {
			return nil, err
		}
		c.switchMu.Lock()
		wait := make([]chan struct{}, 0, 1+len(c.switching))
		if busy, ok := c.accountBusy[accountID]; ok {
			wait = append(wait, busy)
		}
		for key, done := range c.switching {
			if key.AccountID == accountID {
				wait = append(wait, done)
			}
		}
		c.switchMu.Unlock()
		if len(wait) == 0 {
			// The blocker released between the claim and the re-lock; retry.
			continue
		}
		for _, done := range wait {
			select {
			case <-done:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
}

// beginAccountStop claims the account for a stop after waiting for its
// in-flight slot switches and account mutations (a login sync or disconnect-all)
// to settle, so the caller closes a complete gate set instead of one a start is
// still building. The wait is bounded by ctx; a cancelled wait releases nothing
// and reports the context error.
//
// It is the delete/logout seam and therefore waits rather than refusing: the
// caller must not commit its store mutation while a gate is still opening.
func (c *NodeController) beginAccountStop(ctx context.Context, accountID string) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		c.switchMu.Lock()
		if busy, ok := c.accountBusy[accountID]; ok {
			c.switchMu.Unlock()
			select {
			case <-busy:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		pending := make([]chan struct{}, 0, len(c.switching))
		for key, done := range c.switching {
			if key.AccountID == accountID {
				pending = append(pending, done)
			}
		}
		if len(pending) == 0 {
			done := make(chan struct{})
			c.accountBusy[accountID] = done
			c.switchMu.Unlock()
			return func() {
				c.switchMu.Lock()
				if current, ok := c.accountBusy[accountID]; ok && current == done {
					delete(c.accountBusy, accountID)
					close(done)
				}
				c.switchMu.Unlock()
			}, nil
		}
		c.switchMu.Unlock()
		for _, done := range pending {
			select {
			case <-done:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
}

// noteAttempt records the cooldown start for one slot. It is called only after
// the already-connected shortcut, so an idempotent 200 does not start a
// cooldown.
func (c *NodeController) noteAttempt(key model.SlotKey) {
	c.switchMu.Lock()
	c.lastAttempt[key] = c.now()
	c.switchMu.Unlock()
}

// findServer resolves serverID by vendor id or by the Servers map key. The
// returned ref uses the vendor id when present.
func findServer(servers map[string]apifacade.Server, serverID string) (serverRef, bool) {
	serverID = strings.TrimSpace(serverID)
	if serverID == "" {
		return serverRef{}, false
	}
	if server, ok := servers[serverID]; ok {
		id := strings.TrimSpace(server.ID)
		if id == "" {
			id = serverID
		}
		return serverRef{id: id, server: server}, true
	}
	for key, server := range servers {
		if strings.TrimSpace(key) == serverID || strings.TrimSpace(server.ID) == serverID {
			id := strings.TrimSpace(server.ID)
			if id == "" {
				id = serverID
			}
			return serverRef{id: id, server: server}, true
		}
	}
	return serverRef{}, false
}

func switchTrigger(value, fallback string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return fallback
}

func switchDetail(action string) string {
	switch action {
	case "catalog.reconnect":
		return "Session reopened"
	case "catalog.switch":
		return "Switched · one session live"
	default:
		return "Connected · one session live"
	}
}

// classifyConnectError maps a session-open failure onto the small audit enum.
func classifyConnectError(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "timeout"
	case errors.Is(err, account.ErrAuthRejected):
		return "auth"
	default:
		return "network"
	}
}
