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
	ReasonLoginRequired    = "login_required"
	ReasonLoginDisabled    = "login_disabled"
	ReasonAccountState     = "account_state"
	ReasonUnknownServer    = "unknown_server"
	ReasonSnapshotChanged  = "snapshot_changed"
	ReasonServerIneligible = "server_ineligible"
	ReasonSwitchInProgress = "switch_in_progress"
	ReasonConnectCooldown  = "connect_cooldown"
	ReasonMultipleSessions = "multiple_sessions"
	ReasonSessionOpen      = "session_open_failed"
	ReasonGateStart        = "gate_start_failed"
	ReasonStoreFailed      = "store_failed"
	ReasonReconcileFailed  = "reconcile_failed"
	ReasonNoTarget         = "no_target"
)

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
}

// DisconnectResult is the 200 body of disconnect.
type DisconnectResult struct {
	AccountID           string   `json:"accountId"`
	ServerID            string   `json:"serverId,omitempty"`
	NodeID              string   `json:"nodeId,omitempty"`
	AlreadyDisconnected bool     `json:"alreadyDisconnected,omitempty"`
	Forced              bool     `json:"forced"`
	ActiveFlows         int      `json:"activeFlows"`
	DurationMs          int64    `json:"durationMs"`
	Warnings            []string `json:"warnings"`
	AuditID             string   `json:"auditId,omitempty"`
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

// ConnectServer is the guarded switch to serverID (DESIGN-CATALOG §3.5). It
// opens the new session before closing the old one, commits exactly one LIVE
// node, and rolls the old state back on open/gate/store failure. A manual
// connect also persists the selection; rotation and login triggers do not.
func (c *NodeController) ConnectServer(ctx context.Context, accountID, serverID, snapshotID, trigger string) (SwitchResult, error) {
	start := c.now()
	trigger = switchTrigger(trigger, "manual")
	release, err := c.beginSwitch(accountID, true)
	if err != nil {
		return SwitchResult{}, err
	}
	defer release()

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped {
		return SwitchResult{}, ErrControllerStopped
	}
	acct, result, target, err := c.resolveTarget(accountID, serverID)
	if err != nil {
		return SwitchResult{}, err
	}
	if err := c.checkSnapshot(snapshotID); err != nil {
		return SwitchResult{}, err
	}
	old, err := c.accountGateLocked(accountID)
	if err != nil {
		return SwitchResult{}, err
	}
	if old != nil && old.serverID == target.id && trigger != "reconnect" {
		id := nodeID(accountID, target.id)
		result := SwitchResult{
			AccountID:        accountID,
			ServerID:         target.id,
			NodeID:           id,
			NodeState:        model.NodeLive,
			GatePort:         old.port,
			EgressIP:         old.egressIP,
			DurationMs:       c.now().Sub(start).Milliseconds(),
			Warnings:         []string{},
			AlreadyConnected: true,
		}
		if node, ok := c.store.Node(id); ok {
			result.NodeState = node.State
			result.GatePort = node.GatePort
			result.EgressIP = node.EgressIP
		}
		return result, nil
	}
	c.noteAttempt(accountID)
	res, err := c.switchLocked(ctx, acct, result, target, old, trigger, start)
	if err != nil {
		return res, err
	}
	if reconcileErr := c.reconcileLocked(ctx); reconcileErr != nil {
		c.logger.Warn("catalog_reconcile_failed", "account_id", accountID, "trigger", trigger, "error", reconcileErr)
		res.Warnings = append(res.Warnings, ReasonReconcileFailed)
	}
	return res, nil
}

// ReconnectAccount reopens the current server, or the selection when nothing is
// running (DESIGN-CATALOG §3.5). It always performs a real reopen, so the
// same-server case does not take the already-connected shortcut.
func (c *NodeController) ReconnectAccount(ctx context.Context, accountID, snapshotID, trigger string) (SwitchResult, error) {
	start := c.now()
	trigger = switchTrigger(trigger, "reconnect")
	release, err := c.beginSwitch(accountID, true)
	if err != nil {
		return SwitchResult{}, err
	}
	defer release()

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped {
		return SwitchResult{}, ErrControllerStopped
	}
	if _, ok := c.store.Account(accountID); !ok {
		return SwitchResult{}, &store.Error{Kind: store.ErrNotFound, Message: fmt.Sprintf("account %q not found", accountID)}
	}
	old, err := c.accountGateLocked(accountID)
	if err != nil {
		return SwitchResult{}, err
	}
	serverID := ""
	if old != nil {
		serverID = old.serverID
	} else if selection, ok := c.store.Selection(accountID); ok {
		serverID = selection.ServerID
	} else {
		return SwitchResult{}, &SwitchError{Reason: ReasonNoTarget, Message: "account has no current server and no target"}
	}
	acct, result, target, err := c.resolveTarget(accountID, serverID)
	if err != nil {
		return SwitchResult{}, err
	}
	if err := c.checkSnapshot(snapshotID); err != nil {
		return SwitchResult{}, err
	}
	c.noteAttempt(accountID)
	res, err := c.switchLocked(ctx, acct, result, target, old, trigger, start)
	if err != nil {
		return res, err
	}
	if reconcileErr := c.reconcileLocked(ctx); reconcileErr != nil {
		c.logger.Warn("catalog_reconcile_failed", "account_id", accountID, "trigger", trigger, "error", reconcileErr)
		res.Warnings = append(res.Warnings, ReasonReconcileFailed)
	}
	return res, nil
}

// DisconnectAccount drains and closes every gate of one account, deletes its
// nodes and leases without the generic node.delete audit, keeps the selection,
// and writes one catalog.disconnect. A second call is a no-op.
func (c *NodeController) DisconnectAccount(ctx context.Context, accountID string) (DisconnectResult, error) {
	start := c.now()
	release, err := c.beginSwitch(accountID, false)
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
	gates := make([]string, 0, 1)
	for id, running := range c.running {
		if running.accountID == accountID {
			gates = append(gates, id)
		}
	}
	if len(gates) == 0 {
		return DisconnectResult{
			AccountID:           accountID,
			AlreadyDisconnected: true,
			DurationMs:          c.now().Sub(start).Milliseconds(),
			Warnings:            []string{},
		}, nil
	}
	sort.Strings(gates)
	result := DisconnectResult{AccountID: accountID, Warnings: []string{}}
	for _, id := range gates {
		running := c.running[id]
		if stateful, ok := running.gate.(GateState); ok {
			result.ActiveFlows += stateful.State().Active
		}
		_ = c.store.SetNodeStateQuiet(id, model.NodeDraining)
		drainCtx, cancel := context.WithTimeout(ctx, switchDrainGrace)
		drainErr := running.gate.Drain(drainCtx)
		cancel()
		if drainErr != nil {
			result.Forced = true
			c.logger.Warn("catalog_disconnect_drain_failed", "account_id", accountID, "node_id", id, "error", drainErr)
		}
		if errs := c.closeGateLocked(id, running); len(errs) > 0 {
			result.Forced = true
			result.Warnings = append(result.Warnings, "close_failed")
			c.logger.Warn("catalog_disconnect_close_failed", "account_id", accountID, "node_id", id, "error", errors.Join(errs...))
		}
		delete(c.running, id)
		if err := c.store.DeleteNodeQuiet(id); err != nil && !errors.Is(err, store.ErrNotFound) {
			result.Warnings = append(result.Warnings, "store_delete_failed")
			c.logger.Warn("catalog_disconnect_store_failed", "account_id", accountID, "node_id", id, "error", err)
		}
		result.ServerID = running.serverID
		result.NodeID = id
		c.logger.Info("catalog_disconnected", "account_id", accountID, "node_id", id, "server_id", running.serverID)
	}
	result.DurationMs = c.now().Sub(start).Milliseconds()
	result.AuditID = c.store.WriteAudit(model.SeverityInfo, "operator", "catalog.disconnect",
		result.NodeID, "Session disconnected · target kept", map[string]string{
			"nodeId":      result.NodeID,
			"serverId":    result.ServerID,
			"forced":      strconv.FormatBool(result.Forced),
			"activeFlows": strconv.Itoa(result.ActiveFlows),
			"durationMs":  strconv.FormatInt(result.DurationMs, 10),
		})
	if reconcileErr := c.reconcileLocked(ctx); reconcileErr != nil {
		c.logger.Warn("catalog_reconcile_failed", "account_id", accountID, "action", "disconnect", "error", reconcileErr)
		result.Warnings = append(result.Warnings, ReasonReconcileFailed)
	}
	return result, nil
}

// switchLocked performs steps 9-16 of DESIGN-CATALOG §3.5 with the controller
// lock held. It never reconciles; the caller reconciles once after it returns.
func (c *NodeController) switchLocked(ctx context.Context, acct model.Account, result account.LoginResult, target serverRef, old *nodeGate, trigger string, start time.Time) (SwitchResult, error) {
	newID := nodeID(acct.ID, target.id)
	oldServerID := ""
	oldNodeID := ""
	oldState := model.NodeState("")
	if old != nil {
		oldServerID = old.serverID
		oldNodeID = nodeID(acct.ID, old.serverID)
		if node, ok := c.store.Node(oldNodeID); ok {
			oldState = node.State
		}
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
	}
	restoreOld := func() {
		if old == nil || oldState == "" {
			return
		}
		if err := c.store.SetNodeStateQuiet(oldNodeID, oldState); err != nil {
			c.logger.Warn("catalog_restore_state_failed", "account_id", acct.ID, "node_id", oldNodeID, "error", err)
		}
		if err := old.gate.Undrain(); err != nil {
			c.logger.Warn("catalog_undrain_failed", "account_id", acct.ID, "node_id", oldNodeID, "error", err)
		}
		old.draining = false
	}

	identity, ok := c.store.AccountDevice(acct.ID)
	if !ok {
		restoreOld()
		return c.failSwitch(acct, target, old, trigger, start, ReasonSessionOpen,
			"session_open_failed", "unknown", "account has no device identity", errors.New("account has no device identity"))
	}
	device, err := account.DeviceForIdentity(identity)
	if err != nil {
		restoreOld()
		return c.failSwitch(acct, target, old, trigger, start, ReasonSessionOpen,
			"session_open_failed", "unknown", "account device is invalid", err)
	}
	userName := strings.TrimSpace(result.UserName)
	if userName == "" {
		userName = acct.Username
	}
	session, err := c.sessions.Open(ctx, account.SessionRequest{
		AccountID:       acct.ID,
		Server:          target.server,
		Device:          device,
		UserName:        userName,
		ConnectPassword: result.ConnectPassword,
		Profile:         c.profile,
	})
	if err != nil {
		restoreOld()
		return c.failSwitch(acct, target, old, trigger, start, ReasonSessionOpen,
			"session_open_failed", classifyConnectError(err), "could not open the new session", err)
	}
	g, port, err := c.startGateLocked(newID, acct.ID, target.id, session)
	if err != nil {
		_ = session.Close()
		restoreOld()
		return c.failSwitch(acct, target, old, trigger, start, ReasonGateStart,
			"gate_start_failed", "gate", "could not start the new gate", err)
	}
	running := &nodeGate{
		gate:      g,
		session:   session,
		accountID: acct.ID,
		serverID:  target.id,
		port:      port,
		egressIP:  sessionEgress(session),
		startedAt: c.now(),
	}
	c.running[newID] = running

	node := applyServerToNode(model.ProxyNode{
		ID:       newID,
		Capacity: gateFlowLimit,
	}, acct, target, model.NodeLive, port, running.egressIP)
	var selection *model.Selection
	if trigger == "manual" {
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
		"gatePort":   strconv.Itoa(port),
		"durationMs": strconv.FormatInt(duration, 10),
		"trigger":    trigger,
		"result":     "ok",
	}
	if old != nil {
		meta["fromServerId"] = old.serverID
		meta["toServerId"] = target.id
	}
	committed, auditID, err := c.store.CommitSwitch(store.SwitchCommit{
		Node:      node,
		Actor:     "operator",
		Action:    action,
		Target:    newID,
		Detail:    switchDetail(action),
		Meta:      meta,
		Selection: selection,
		Stat:      &model.ServerStat{AccountID: acct.ID, ServerID: target.id},
	})
	if err != nil {
		c.logger.Warn("catalog_commit_failed", "account_id", acct.ID, "node_id", newID, "error", err)
		if errs := c.closeGateLocked(newID, running); len(errs) > 0 {
			c.logger.Warn("catalog_rollback_close_failed", "account_id", acct.ID, "node_id", newID, "error", errors.Join(errs...))
		}
		delete(c.running, newID)
		restoreOld()
		return c.failSwitch(acct, target, old, trigger, start, ReasonStoreFailed,
			"store_failed", "store", "could not commit the switch", err)
	}

	warnings := []string{}
	if old != nil {
		oldID := nodeID(acct.ID, old.serverID)
		if errs := c.closeGateLocked(oldID, old); len(errs) > 0 {
			warnings = append(warnings, "orphan_gate")
			c.logger.Warn("catalog_switch_cleanup_failed", "account_id", acct.ID, "node_id", oldID, "error", errors.Join(errs...))
			c.store.WriteAudit(model.SeverityWarn, "operator", "catalog.switch_cleanup_failed",
				oldID, "Old gate did not close · switch committed", map[string]string{
					"errorClass": "gate",
				})
			// A same-server reconnect reused the key, so the map already
			// points at the new gate; a different server keeps the old entry
			// so a later Stop can retry the close. Never delete the new gate.
		} else {
			if oldID != newID {
				delete(c.running, oldID)
			}
		}
	}
	c.logger.Info("catalog_switch_succeeded", "account_id", acct.ID, "node_id", newID,
		"server_id", target.id, "from_server_id", oldServerID, "trigger", trigger, "duration_ms", duration)
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
	}, nil
}

// failSwitch records a failed attempt and returns the client-safe error. The
// rollback string is "restored" when the old session is still serving and
// "none" when there was no old session to restore.
func (c *NodeController) failSwitch(acct model.Account, target serverRef, old *nodeGate, trigger string, start time.Time, reason, message, errClass, detail string, cause error) (SwitchResult, error) {
	duration := c.now().Sub(start).Milliseconds()
	rollback := "none"
	if old != nil {
		rollback = "restored"
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
		"durationMs": strconv.FormatInt(duration, 10),
		"trigger":    trigger,
		"result":     "failed",
		"rollback":   rollback,
	}
	if old != nil {
		meta["fromServerId"] = old.serverID
		meta["toServerId"] = target.id
	}
	if _, err := c.store.RecordCatalogAttempt(store.AttemptRecord{
		AccountID:  acct.ID,
		ServerID:   target.id,
		Action:     action,
		Target:     nodeID(acct.ID, target.id),
		Detail:     detail,
		Meta:       meta,
		ErrorClass: errClass,
	}); err != nil {
		c.logger.Warn("catalog_attempt_record_failed", "account_id", acct.ID, "server_id", target.id, "error", err)
	}
	c.logger.Warn("catalog_connect_failed", "account_id", acct.ID, "server_id", target.id,
		"reason", reason, "error_class", errClass, "rollback", rollback, "error", cause)
	switchedFrom := ""
	if old != nil {
		switchedFrom = old.serverID
	}
	return SwitchResult{
			AccountID:    acct.ID,
			ServerID:     target.id,
			NodeID:       nodeID(acct.ID, target.id),
			SwitchedFrom: switchedFrom,
			DurationMs:   duration,
			Warnings:     []string{},
			Rollback:     rollback,
		}, &SwitchError{
			Reason:  reason,
			Message: message,
			Details: map[string]string{"rollback": rollback},
			Cause:   cause,
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

// accountGateLocked returns the account's only running gate, or nil. More than
// one is an invariant violation and is refused instead of guessed.
func (c *NodeController) accountGateLocked(accountID string) (*nodeGate, error) {
	var found *nodeGate
	for _, running := range c.running {
		if running.accountID != accountID {
			continue
		}
		if found != nil {
			return nil, &SwitchError{
				Reason: ReasonMultipleSessions, Message: "more than one session is running for the account",
			}
		}
		found = running
	}
	return found, nil
}

// beginSwitch claims the per-account switch slot and enforces the cooldown. The
// returned release must be called once the attempt is finished.
func (c *NodeController) beginSwitch(accountID string, cooldown bool) (func(), error) {
	c.switchMu.Lock()
	defer c.switchMu.Unlock()
	if _, busy := c.switching[accountID]; busy {
		return nil, &SwitchError{
			Reason: ReasonSwitchInProgress, Message: "a switch is already in progress for this account",
		}
	}
	if cooldown && c.cooldown > 0 {
		if last, ok := c.lastAttempt[accountID]; ok {
			if remaining := c.cooldown - c.now().Sub(last); remaining > 0 {
				return nil, &SwitchError{
					Reason: ReasonConnectCooldown, Message: "connect cooldown",
					RetryAfter: remaining,
				}
			}
		}
	}
	c.switching[accountID] = struct{}{}
	return func() {
		c.switchMu.Lock()
		delete(c.switching, accountID)
		c.switchMu.Unlock()
	}, nil
}

// noteAttempt records the cooldown start. It is called only after the
// already-connected shortcut, so an idempotent 200 does not start a cooldown.
func (c *NodeController) noteAttempt(accountID string) {
	c.switchMu.Lock()
	c.lastAttempt[accountID] = c.now()
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
