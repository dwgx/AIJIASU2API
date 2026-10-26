package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/runtime"
	"ajsaccountd/internal/store"
)

// Select-node request bounds (DESIGN-INTEGRATION §3.2). DrainSeconds and
// Rollback are validated for the contract but are advisory: the current
// guarded switch fixes its own drain grace and performs metadata-only
// containment, so this layer never invents a parallel switch path.
const (
	defaultSelectDrainSeconds   = 30
	maxSelectDrainSeconds       = 60
	defaultSelectTimeoutSeconds = 60
	minSelectTimeoutSeconds     = 10
	maxSelectTimeoutSeconds     = 120

	defaultOperationLimit = 50
	maxOperationLimit     = 200
)

// CatalogServerLister is the optional retained-catalog read behind region
// targets. *runtime.NodeController implements it; a connector that does not
// answer 503 for a region target instead of guessing a server.
type CatalogServerLister interface {
	CatalogServers(accountID string) ([]runtime.CatalogServer, bool)
}

// NodeRuntimeSnapshotter is the optional runtime view used to report
// sessionHealthy honestly. *runtime.NodeController implements it. A connector
// without it falls back to the store's LIVE state.
type NodeRuntimeSnapshotter interface {
	Snapshot() []runtime.NodeRuntime
}

// selectTarget is the resolved form of a SelectNodeRequest target. Exactly one
// request form is recorded; serverID is always the vendor server the guarded
// switch will use.
type selectTarget struct {
	serverID string
	nodeID   string
	region   string
	strategy string
}

// selectNodeError is a client-safe refusal raised before an operation exists.
// Once an operation is queued, failures are recorded on the operation instead.
type selectNodeError struct {
	status  int
	code    string
	reason  string
	message string
	details map[string]string
}

func (e *selectNodeError) Error() string { return e.message }

func badSelectRequest(message string) *selectNodeError {
	return &selectNodeError{
		status: http.StatusBadRequest, code: CodeInvalidArgument,
		reason: "INVALID_ARGUMENT", message: message,
	}
}

// handleSelectNode serves POST /v1/accounts/{id}/actions/select-node. It
// validates and resolves the target synchronously (a bad target never creates
// a durable record), then queues the same guarded switch the synchronous
// connect route uses. wait:true blocks for the terminal result; wait:false
// answers 202 with the queued operation.
func (s *Server) handleSelectNode(w http.ResponseWriter, r *http.Request) {
	accountID := pathParam(r, "id")
	account, ok := s.store.Account(accountID)
	if !ok {
		writeError(w, http.StatusNotFound, CodeNotFound, "account not found")
		return
	}
	// A scoped key restricted to other accounts never reaches this handler:
	// scopedKeyDenial refuses the account path in the auth middleware with 403
	// scoped_key_restricted, before any operation is queued.
	if s.cfg.Catalog == nil {
		writeErrorReason(w, http.StatusConflict, CodeConflict, runtime.ReasonLoginDisabled,
			"account login is disabled (AJS_ACCOUNT_LOGIN=0)", nil)
		return
	}
	var body model.SelectNodeRequest
	if !decodeOptionalJSON(w, r, &body) {
		return
	}
	if message := applySelectNodeDefaults(&body); message != "" {
		writeErrorReason(w, http.StatusBadRequest, CodeInvalidArgument, "INVALID_ARGUMENT", message, nil)
		return
	}

	validateStart := time.Now()
	target, err := s.resolveSelectTarget(account, body)
	if err != nil {
		s.writeSelectNodeError(w, err)
		return
	}
	current, hasCurrent := s.currentSelectNode(accountID)
	runtimeHealthy := true
	if hasCurrent {
		runtimeHealthy = s.sessionHealthy(current.ID, true)
	}
	if hasCurrent && runtimeHealthy && current.ServerID == target.serverID {
		switch body.IfCurrent {
		case "noop":
			writeJSON(w, http.StatusOK, &model.SelectNodeResult{
				Changed:        false,
				AccountID:      accountID,
				NodeID:         current.ID,
				ServerID:       target.serverID,
				GatePort:       current.GatePort,
				EgressIP:       current.EgressIP,
				SessionHealthy: true,
				DurationMs:     time.Since(validateStart).Milliseconds(),
			})
			return
		case "error":
			writeErrorReason(w, http.StatusConflict, CodeConflict, "ALREADY_CURRENT",
				"account is already on the requested server",
				map[string]string{"serverId": target.serverID})
			return
		default: // reselect
			target, err = s.reselectTarget(accountID, current)
			if err != nil {
				s.writeSelectNodeError(w, err)
				return
			}
		}
	}

	// A same-server target that is not a healthy live session is a reopen, not
	// a switch: a DRAINING row (a drain that still owns the gate), a STALE row
	// (startup recovery demoted a persisted node), and a LIVE row whose runtime
	// session died. ConnectServer takes the already-connected shortcut when the
	// slot's gate still runs and leaves the node as it is; ReconnectSlot always
	// reopens. The trigger alone also skips the shortcut, so the same-server
	// case is a real reopen even when the reconnect executor is not chosen.
	sameSlotTarget := false
	if node, ok := s.slotNode(accountID); ok && node.ServerID == target.serverID {
		sameSlotTarget = true
	}
	// ReconnectAccount derives its server from the runtime (gate, then
	// selection), so use it only when every target it could pick is the
	// requested server; otherwise ConnectServer names the target explicitly.
	reconnect := false
	if sameSlotTarget {
		if served, ok := s.reconnectServerID(accountID); ok && served == target.serverID {
			reconnect = true
		}
	}
	operationType := model.OperationTypeConnect
	trigger := "manual"
	switch {
	case hasCurrent && runtimeHealthy:
		operationType = model.OperationTypeSwitch
	case sameSlotTarget:
		trigger = "reconnect"
	}
	operation, busy, err := s.store.BeginOperation(model.Operation{
		Type:        operationType,
		AccountID:   accountID,
		Target:      model.OperationTarget{ServerID: target.serverID, NodeID: target.nodeID, Region: target.region, Strategy: target.strategy},
		RequestedBy: requestPrincipal(r),
		Steps: []model.OperationStep{{
			Name:       "validate",
			State:      model.OperationStepOK,
			DurationMs: time.Since(validateStart).Milliseconds(),
		}},
	})
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if busy != nil {
		writeErrorReason(w, http.StatusConflict, CodeConflict, "ACCOUNT_BUSY",
			"another operation is in progress for this account",
			map[string]string{"currentOperationId": busy.ID})
		return
	}

	timeout := time.Duration(body.TimeoutSeconds) * time.Second
	go s.runSelectNode(operation, target.serverID, trigger, reconnect, timeout)
	if body.Wait {
		s.waitSelectNode(w, r, operation, timeout)
		return
	}
	w.Header().Set("Location", "/v1/operations/"+operation.ID)
	w.Header().Set("Retry-After", "1")
	writeJSON(w, http.StatusAccepted, operation)
}

// runSelectNode is the async executor. It marks the durable record running
// before touching the vendor, then runs one guarded switch: ReconnectAccount
// (runtime.ReconnectSlot) when the target is a same-server reopen, so the
// already-connected shortcut is never taken, or ConnectServer when the target
// is a different server. Both paths keep the cooldown, the one-session break
// before make, the slot lock and metadata-only containment.
func (s *Server) runSelectNode(operation model.Operation, serverID, trigger string, reconnect bool, timeout time.Duration) {
	started, err := s.store.MarkOperationRunning(operation.ID)
	if err != nil {
		// The record could not be made durable: the store is poisoned and the
		// daemon is terminating, so the vendor must not be touched.
		s.logger.Error("operation_start_persist_failed", "operation_id", operation.ID, "error", err)
		return
	}
	if !started {
		// Canceled before dispatch: never touch the vendor.
		return
	}
	// A panic in the executor must not crash the daemon or leave the durable
	// record running forever. A terminal record already finished is left
	// untouched by FinishOperation.
	defer func() {
		if recovered := recover(); recovered != nil {
			s.logger.Error("operation_panic", "operation_id", operation.ID, "panic", recovered)
			if _, _, finishErr := s.store.FinishOperation(operation.ID, model.OperationFailed, []model.OperationStep{
				{Name: "switch", State: model.OperationStepFailed},
			}, nil, &model.OperationError{Code: "INTERNAL", Message: "internal error"}); finishErr != nil {
				s.logger.Error("operation_finish_persist_failed", "operation_id", operation.ID, "error", finishErr)
			}
			s.persistAfterOperation()
		}
	}()
	validateMs := int64(0)
	for _, step := range operation.Steps {
		if step.Name == "validate" {
			validateMs = step.DurationMs
			break
		}
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var result runtime.SwitchResult
	if reconnect {
		result, err = s.cfg.Catalog.ReconnectAccount(ctx, operation.AccountID, "", trigger)
	} else {
		result, err = s.cfg.Catalog.ConnectServer(ctx, operation.AccountID, serverID, "", trigger)
	}
	switchMs := time.Since(start).Milliseconds()
	steps := []model.OperationStep{
		{Name: "validate", State: model.OperationStepOK, DurationMs: validateMs},
		{Name: "switch", State: model.OperationStepOK, DurationMs: switchMs},
	}
	if err != nil {
		steps[1].State = model.OperationStepFailed
		var partial *model.SelectNodeResult
		if result.AccountID != "" || result.ServerID != "" || result.Rollback != "" {
			partial = s.selectNodeResultFromSwitch(result)
			partial.Changed = false
			partial.SessionHealthy = false
		}
		if _, _, finishErr := s.store.FinishOperation(operation.ID, model.OperationFailed, steps, partial,
			operationErrorFrom(err, ctx)); finishErr != nil {
			s.logger.Error("operation_finish_persist_failed", "operation_id", operation.ID, "error", finishErr)
		}
		s.persistAfterOperation()
		return
	}
	if _, _, finishErr := s.store.FinishOperation(operation.ID, model.OperationSucceeded, steps,
		s.selectNodeResultFromSwitch(result), nil); finishErr != nil {
		s.logger.Error("operation_finish_persist_failed", "operation_id", operation.ID, "error", finishErr)
	}
	// Follow the new gate only after the durable record is terminal, so a
	// wait:true caller is not blocked by the health sampler.
	s.observeConnect(result)
	s.persistAfterOperation()
}

// waitSelectNode blocks a wait:true caller until the operation is terminal.
// The extra grace lets the executor's own timeout land as a failed operation
// before the HTTP wait expires.
func (s *Server) waitSelectNode(w http.ResponseWriter, r *http.Request, operation model.Operation, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(r.Context(), timeout+2*time.Second)
	defer cancel()
	final, err := s.store.WaitOperation(ctx, operation.ID)
	if err != nil {
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			writeErrorReason(w, http.StatusGatewayTimeout, "OPERATION_TIMEOUT", "OPERATION_TIMEOUT",
				"operation did not finish within timeoutSeconds",
				map[string]string{"operationId": operation.ID})
		case errors.Is(err, context.Canceled):
			// The client went away; there is no response to write.
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, CodeNotFound, "operation not found")
		default:
			s.writeStoreError(w, err)
		}
		return
	}
	switch final.State {
	case model.OperationSucceeded:
		if final.Result == nil {
			writeJSON(w, http.StatusOK, final)
			return
		}
		writeJSON(w, http.StatusOK, final.Result)
	case model.OperationCanceled:
		writeErrorReason(w, http.StatusConflict, CodeConflict, "OPERATION_CANCELED",
			"operation was canceled", map[string]string{"operationId": final.ID})
	default:
		s.writeOperationFailure(w, final)
	}
}

// writeOperationFailure maps a terminal failed operation onto the HTTP
// contract. The operation id is always present so a caller can poll it.
func (s *Server) writeOperationFailure(w http.ResponseWriter, operation model.Operation) {
	if operation.Error == nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, "internal error")
		return
	}
	status, envelope := operationErrorStatus(operation.Error.Code)
	details := make(map[string]string, len(operation.Error.Details)+1)
	for key, value := range operation.Error.Details {
		details[key] = value
	}
	details["operationId"] = operation.ID
	if status == http.StatusTooManyRequests {
		if retry := details["retryAfterSeconds"]; retry != "" {
			w.Header().Set("Retry-After", retry)
		}
	}
	writeErrorReason(w, status, envelope, operation.Error.Code, operation.Error.Message, details)
}

// handleListOperations serves GET /v1/operations. Both accountId (DESIGN) and
// account (ticket shorthand) are accepted. The list is newest first; the
// default limit is 50 and the maximum is 200.
func (s *Server) handleListOperations(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	accountID := strings.TrimSpace(query.Get("accountId"))
	if accountID == "" {
		accountID = strings.TrimSpace(query.Get("account"))
	}
	state := strings.TrimSpace(query.Get("state"))
	operationType := strings.TrimSpace(query.Get("type"))
	if state != "" && !model.OperationState(state).Valid() {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument,
			"state must be one of queued, running, succeeded, failed, canceled")
		return
	}
	limit := defaultOperationLimit
	if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > maxOperationLimit {
			writeError(w, http.StatusBadRequest, CodeInvalidArgument,
				"limit must be an integer between 1 and 200")
			return
		}
		limit = parsed
	}

	visible, restricted := s.visibleAccountIDs(r)
	if accountID != "" && restricted {
		if _, ok := visible[accountID]; !ok {
			writeError(w, http.StatusNotFound, CodeNotFound, "account not found")
			return
		}
	}
	fetchLimit := limit
	if restricted {
		// Filter before truncating so a restricted key still receives a full
		// page of the operations it may see.
		fetchLimit = 0
	}
	operations := s.store.Operations(store.OperationFilter{
		AccountID: accountID,
		State:     model.OperationState(state),
		Type:      operationType,
		Limit:     fetchLimit,
	})
	if restricted && accountID == "" {
		filtered := make([]model.Operation, 0, len(operations))
		for _, operation := range operations {
			if _, ok := visible[operation.AccountID]; ok {
				filtered = append(filtered, operation)
			}
		}
		operations = filtered
	}
	if restricted && len(operations) > limit {
		operations = operations[:limit]
	}
	if operations == nil {
		operations = []model.Operation{}
	}
	writeJSON(w, http.StatusOK, operations)
}

// handleGetOperation serves GET /v1/operations/{id}. A record outside a
// restricted key's account allowlist answers 404, not 403, so the key cannot
// enumerate other accounts' operations.
func (s *Server) handleGetOperation(w http.ResponseWriter, r *http.Request) {
	operation, ok := s.store.Operation(pathParam(r, "id"))
	if !ok {
		writeError(w, http.StatusNotFound, CodeNotFound, "operation not found")
		return
	}
	if !s.operationVisible(r, operation) {
		writeError(w, http.StatusNotFound, CodeNotFound, "operation not found")
		return
	}
	writeJSON(w, http.StatusOK, operation)
}

// handleCancelOperation serves DELETE /v1/operations/{id}. A queued operation
// is canceled before dispatch; a running one answers 409 because the guarded
// switch is one executor step that may already hold an open vendor session.
// The account restriction is enforced in the auth middleware, which resolves
// the operation's account before this handler runs.
func (s *Server) handleCancelOperation(w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	operation, ok := s.store.Operation(id)
	if !ok {
		writeError(w, http.StatusNotFound, CodeNotFound, "operation not found")
		return
	}
	canceled, err := s.store.CancelOperation(id)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, CodeNotFound, "operation not found")
		case errors.Is(err, store.ErrConflict):
			writeErrorReason(w, http.StatusConflict, CodeConflict, "NOT_CANCELABLE",
				err.Error(), map[string]string{"operationId": id, "state": string(operation.State)})
		default:
			s.writeStoreError(w, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, canceled)
}

// operationVisible enforces the scoped-key account allowlist on one record.
func (s *Server) operationVisible(r *http.Request, operation model.Operation) bool {
	visible, restricted := s.visibleAccountIDs(r)
	if !restricted {
		return true
	}
	_, ok := visible[operation.AccountID]
	return ok
}

// applySelectNodeDefaults validates the closed vocabulary and fills the
// documented defaults. A non-empty return value is a client error message.
func applySelectNodeDefaults(body *model.SelectNodeRequest) string {
	body.IfCurrent = strings.ToLower(strings.TrimSpace(body.IfCurrent))
	if body.IfCurrent == "" {
		body.IfCurrent = "noop"
	}
	switch body.IfCurrent {
	case "noop", "error", "reselect":
	default:
		return "ifCurrent must be one of noop, error, reselect"
	}
	if body.DrainSeconds == 0 {
		body.DrainSeconds = defaultSelectDrainSeconds
	}
	if body.DrainSeconds < 0 || body.DrainSeconds > maxSelectDrainSeconds {
		return "drainSeconds must be between 0 and 60"
	}
	if body.TimeoutSeconds == 0 {
		body.TimeoutSeconds = defaultSelectTimeoutSeconds
	}
	if body.TimeoutSeconds < minSelectTimeoutSeconds || body.TimeoutSeconds > maxSelectTimeoutSeconds {
		return "timeoutSeconds must be between 10 and 120"
	}
	return ""
}

// resolveSelectTarget turns the request target (or the account's persisted
// selection) into one validated server id. It performs no vendor I/O.
func (s *Server) resolveSelectTarget(account model.Account, body model.SelectNodeRequest) (selectTarget, error) {
	if emptyOperationTarget(body.Target) {
		selection, ok := s.store.Selection(account.ID)
		if !ok || strings.TrimSpace(selection.ServerID) == "" {
			return selectTarget{}, badSelectRequest(
				"target is required when the account has no selection")
		}
		return s.resolveServerTarget(account.ID, selection.ServerID, "", "", "")
	}
	target := body.Target
	forms := 0
	if strings.TrimSpace(target.ServerID) != "" {
		forms++
	}
	if strings.TrimSpace(target.NodeID) != "" {
		forms++
	}
	if strings.TrimSpace(target.Region) != "" {
		forms++
	}
	if forms != 1 {
		return selectTarget{}, badSelectRequest(
			"target must name exactly one of serverId, nodeId, region")
	}
	if strings.TrimSpace(target.Region) == "" && strings.TrimSpace(target.Strategy) != "" {
		return selectTarget{}, badSelectRequest("strategy is only valid with region")
	}
	switch {
	case strings.TrimSpace(target.ServerID) != "":
		return s.resolveServerTarget(account.ID, target.ServerID, "", "", "")
	case strings.TrimSpace(target.NodeID) != "":
		serverID, belongs, malformed := nodeTargetServerID(account.ID, target.NodeID)
		if malformed {
			return selectTarget{}, badSelectRequest(
				"nodeId must be <account>:<serverId> or <account>#<slot>:<serverId>")
		}
		if !belongs {
			return selectTarget{}, &selectNodeError{
				status: http.StatusNotFound, code: CodeNotFound, reason: "NOT_FOUND",
				message: "node is not in the account",
			}
		}
		return s.resolveServerTarget(account.ID, serverID, strings.TrimSpace(target.NodeID), "", "")
	default:
		return s.resolveRegionTarget(account.ID, target.Region, target.Strategy)
	}
}

// resolveServerTarget validates one server against the retained catalog.
func (s *Server) resolveServerTarget(accountID, serverID, nodeID, region, strategy string) (selectTarget, error) {
	serverID = strings.TrimSpace(serverID)
	if serverID == "" {
		return selectTarget{}, badSelectRequest("serverId is required")
	}
	lookup := s.cfg.Catalog.LookupServer(accountID, serverID)
	if !lookup.LoginAvailable {
		return selectTarget{}, &selectNodeError{
			status: http.StatusConflict, code: CodeConflict, reason: runtime.ReasonLoginRequired,
			message: "no retained login result; log in again",
		}
	}
	if !lookup.Found {
		return selectTarget{}, &selectNodeError{
			status: http.StatusNotFound, code: CodeNotFound, reason: runtime.ReasonUnknownServer,
			message: "server is not in the retained catalog",
		}
	}
	if !lookup.Eligible {
		details := map[string]string{}
		if lookup.IneligibleReason != "" {
			details["ineligibleReason"] = lookup.IneligibleReason
		}
		return selectTarget{}, &selectNodeError{
			status: http.StatusConflict, code: CodeConflict, reason: "SERVER_UNAVAILABLE",
			message: "server is not eligible", details: details,
		}
	}
	return selectTarget{serverID: serverID, nodeID: nodeID, region: region, strategy: strategy}, nil
}

// resolveRegionTarget deterministically picks one eligible server in a region,
// excluding the current live node. "next" continues after the current server
// id; "first" takes the lowest id.
func (s *Server) resolveRegionTarget(accountID, region, strategy string) (selectTarget, error) {
	region = strings.ToLower(strings.TrimSpace(region))
	if region == "" {
		return selectTarget{}, badSelectRequest("region is required")
	}
	strategy = strings.ToLower(strings.TrimSpace(strategy))
	if strategy == "" {
		strategy = "next"
	}
	if strategy != "next" && strategy != "first" {
		return selectTarget{}, badSelectRequest("strategy must be next or first")
	}
	lister, ok := s.cfg.Catalog.(CatalogServerLister)
	if !ok {
		return selectTarget{}, &selectNodeError{
			status: http.StatusServiceUnavailable, code: CodeUpstreamUnavailable,
			reason: "UPSTREAM_UNAVAILABLE", message: "catalog listing is not available",
		}
	}
	servers, ok := lister.CatalogServers(accountID)
	if !ok {
		return selectTarget{}, &selectNodeError{
			status: http.StatusConflict, code: CodeConflict, reason: runtime.ReasonLoginRequired,
			message: "no retained login result; log in again",
		}
	}
	current, hasCurrent := s.currentSelectNode(accountID)
	candidates := make([]runtime.CatalogServer, 0, len(servers))
	for _, server := range servers {
		if !server.Eligible {
			continue
		}
		if strings.ToLower(strings.TrimSpace(server.Region)) != region {
			continue
		}
		if hasCurrent && server.ID == current.ServerID {
			continue
		}
		candidates = append(candidates, server)
	}
	if len(candidates) == 0 {
		return selectTarget{}, &selectNodeError{
			status: http.StatusConflict, code: CodeConflict, reason: "NO_ALTERNATE_SERVER",
			message: "no other eligible server exists in the requested region",
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })
	chosen := candidates[0]
	if strategy == "next" && hasCurrent {
		for _, candidate := range candidates {
			if candidate.ID > current.ServerID {
				chosen = candidate
				break
			}
		}
	}
	return selectTarget{serverID: chosen.ID, region: region, strategy: strategy}, nil
}

// reselectTarget picks the next eligible server in the current node's region.
// When the current server is no longer in the retained catalog, it falls back
// to the first eligible server that is not the current one.
func (s *Server) reselectTarget(accountID string, current model.ProxyNode) (selectTarget, error) {
	lister, ok := s.cfg.Catalog.(CatalogServerLister)
	if !ok {
		return selectTarget{}, &selectNodeError{
			status: http.StatusServiceUnavailable, code: CodeUpstreamUnavailable,
			reason: "UPSTREAM_UNAVAILABLE", message: "catalog listing is not available",
		}
	}
	servers, ok := lister.CatalogServers(accountID)
	if !ok {
		return selectTarget{}, &selectNodeError{
			status: http.StatusConflict, code: CodeConflict, reason: runtime.ReasonLoginRequired,
			message: "no retained login result; log in again",
		}
	}
	region := ""
	for _, server := range servers {
		if server.ID == current.ServerID {
			region = strings.ToLower(strings.TrimSpace(server.Region))
			break
		}
	}
	if region != "" {
		return s.resolveRegionTarget(accountID, region, "next")
	}
	sorted := append([]runtime.CatalogServer{}, servers...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	for _, server := range sorted {
		if server.Eligible && server.ID != current.ServerID {
			return selectTarget{serverID: server.ID, strategy: "first"}, nil
		}
	}
	return selectTarget{}, &selectNodeError{
		status: http.StatusConflict, code: CodeConflict, reason: "NO_ALTERNATE_SERVER",
		message: "no other eligible server exists",
	}
}

// currentSelectNode returns the account's live slot-1 node. Legacy/seeded
// rows without a slot count as slot 1, so the account-level route keeps the
// pre-multislot meaning. A higher slot's live node is never selected here.
func (s *Server) currentSelectNode(accountID string) (model.ProxyNode, bool) {
	var fallback model.ProxyNode
	hasFallback := false
	for _, node := range s.store.Nodes() {
		if node.AccountID != accountID || node.State != model.NodeLive {
			continue
		}
		if node.Slot == 1 {
			return node, true
		}
		if node.Slot == 0 && !hasFallback {
			fallback, hasFallback = node, true
		}
	}
	return fallback, hasFallback
}

// slotNode returns the account's slot-1 node row in any state that can still
// own an endpoint: LIVE, DRAINING (a drain that has not removed the gate yet)
// or STALE (demoted by startup recovery). DOWN is excluded because a confirmed
// disconnect has nothing to reopen; selecting that server is a fresh connect.
// Legacy rows without a slot count as slot 1, matching currentSelectNode.
func (s *Server) slotNode(accountID string) (model.ProxyNode, bool) {
	var fallback model.ProxyNode
	hasFallback := false
	for _, node := range s.store.Nodes() {
		if node.AccountID != accountID {
			continue
		}
		switch node.State {
		case model.NodeLive, model.NodeDraining, model.NodeStale:
		default:
			continue
		}
		if node.Slot == 1 {
			return node, true
		}
		if node.Slot == 0 && !hasFallback {
			fallback, hasFallback = node, true
		}
	}
	return fallback, hasFallback
}

// reconnectServerID reports the server runtime.ReconnectSlot would reopen for
// slot 1, but only when the answer cannot change underneath the call: the
// persisted selection must exist and name that server, and any running slot-1
// gate must serve the same one. ReconnectSlot reads the gate first and the
// selection second, so a gate that closes between this read and the call would
// otherwise redirect the reopen to a different selection. The slot row is
// deliberately not consulted: ReconnectSlot only falls back to it for higher
// slots. ok=false means the caller must use the explicit ConnectServer path.
func (s *Server) reconnectServerID(accountID string) (string, bool) {
	selection, ok := s.store.Selection(accountID)
	if !ok {
		return "", false
	}
	serverID := strings.TrimSpace(selection.ServerID)
	if serverID == "" {
		return "", false
	}
	if snapshotter, ok := s.cfg.Catalog.(NodeRuntimeSnapshotter); ok {
		for _, running := range snapshotter.Snapshot() {
			if running.AccountID != accountID || running.Slot != 1 || running.Port <= 0 {
				continue
			}
			if strings.TrimSpace(running.ServerID) != serverID {
				return "", false
			}
		}
	}
	return serverID, true
}

// nodeTargetServerID extracts the vendor server from a node id that must
// belong to accountID. belongs=false means another account's id (answer 404);
// malformed=true means the id names this account but cannot be parsed (400).
func nodeTargetServerID(accountID, nodeID string) (serverID string, belongs, malformed bool) {
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		return "", false, true
	}
	switch {
	case strings.HasPrefix(nodeID, accountID+"#"):
		rest := nodeID[len(accountID):]
		separator := strings.Index(rest, ":")
		if separator < 0 {
			return "", false, true
		}
		serverID = strings.TrimSpace(rest[separator+1:])
		if serverID == "" {
			return "", false, true
		}
		return serverID, true, false
	case strings.HasPrefix(nodeID, accountID+":"):
		serverID = strings.TrimSpace(nodeID[len(accountID)+1:])
		if serverID == "" {
			return "", false, true
		}
		return serverID, true, false
	default:
		return "", false, false
	}
}

func emptyOperationTarget(target *model.OperationTarget) bool {
	if target == nil {
		return true
	}
	return strings.TrimSpace(target.ServerID) == "" &&
		strings.TrimSpace(target.NodeID) == "" &&
		strings.TrimSpace(target.Region) == ""
}

// requestPrincipal names the caller on the durable record. It never carries a
// secret: a bare admin token is "admin", an operator authenticated by the
// production proxy is its username, and a scoped key is its display prefix.
func requestPrincipal(r *http.Request) string {
	identity, ok := authIdentityFrom(r)
	if !ok {
		return "admin"
	}
	if identity.admin {
		if identity.operator != "" {
			return identity.operator
		}
		return "admin"
	}
	return "key:" + identity.key.Prefix
}

func (s *Server) selectNodeResultFromSwitch(result runtime.SwitchResult) *model.SelectNodeResult {
	sessionHealthy := result.NodeState == model.NodeLive
	if result.NodeID != "" {
		sessionHealthy = s.sessionHealthy(result.NodeID, sessionHealthy)
	}
	return &model.SelectNodeResult{
		Changed:        !result.AlreadyConnected,
		AccountID:      result.AccountID,
		NodeID:         result.NodeID,
		ServerID:       result.ServerID,
		GatePort:       result.GatePort,
		EgressIP:       result.EgressIP,
		SessionHealthy: sessionHealthy,
		PreviousNodeID: result.PrevNodeID,
		Rollback:       result.Rollback,
		DurationMs:     result.DurationMs,
		Warnings:       result.Warnings,
	}
}

// sessionHealthy reports whether the runtime still holds an open session for
// the node. A connector without Snapshot falls back to the store's LIVE state,
// so the daemon never claims a session it cannot see.
func (s *Server) sessionHealthy(nodeID string, fallback bool) bool {
	snapshotter, ok := s.cfg.Catalog.(NodeRuntimeSnapshotter)
	if !ok {
		return fallback
	}
	for _, running := range snapshotter.Snapshot() {
		if running.NodeID == nodeID {
			return running.SessionPresent && running.SessionHealthy
		}
	}
	return false
}

// operationErrorFrom classifies a guarded-switch failure into the durable
// operation error. The wrapped cause stays in the runtime log.
func operationErrorFrom(err error, ctx context.Context) *model.OperationError {
	if ctx != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &model.OperationError{
			Code: "OPERATION_TIMEOUT", Message: "operation exceeded timeoutSeconds",
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &model.OperationError{
			Code: "OPERATION_TIMEOUT", Message: "operation exceeded timeoutSeconds",
		}
	}
	if errors.Is(err, context.Canceled) {
		return &model.OperationError{
			Code: "OPERATION_CANCELED", Message: "operation context was canceled",
		}
	}
	if errors.Is(err, runtime.ErrControllerStopped) {
		return &model.OperationError{
			Code: "UPSTREAM_UNAVAILABLE", Message: "node controller is stopped",
		}
	}
	var switchErr *runtime.SwitchError
	if errors.As(err, &switchErr) {
		code := operationErrorCode(switchErr.Reason)
		message := switchErr.Message
		if message == "" {
			message = "operation failed"
		}
		details := switchErr.Details
		if switchErr.RetryAfter > 0 {
			copied := make(map[string]string, len(details)+1)
			for key, value := range details {
				copied[key] = value
			}
			seconds := int(math.Ceil(switchErr.RetryAfter.Seconds()))
			if seconds < 1 {
				seconds = 1
			}
			copied["retryAfterSeconds"] = strconv.Itoa(seconds)
			details = copied
		}
		return &model.OperationError{Code: code, Message: message, Details: details}
	}
	var storeErr *store.Error
	if errors.As(err, &storeErr) {
		if errors.Is(storeErr.Kind, store.ErrNotFound) {
			return &model.OperationError{Code: "NOT_FOUND", Message: storeErr.Message}
		}
		return &model.OperationError{Code: "INTERNAL", Message: "internal error"}
	}
	return &model.OperationError{Code: "INTERNAL", Message: "internal error"}
}

// operationErrorCode maps a guarded-switch reason onto the stable operation
// error vocabulary. These codes are the machine-readable class a script polls;
// the HTTP envelope keeps its existing CONFLICT/RATE_LIMITED shape.
func operationErrorCode(reason string) string {
	switch reason {
	case runtime.ReasonLoginRequired:
		return "LOGIN_REQUIRED"
	case runtime.ReasonLoginDisabled:
		return "LOGIN_DISABLED"
	case runtime.ReasonAccountState:
		return "ACCOUNT_STATE"
	case runtime.ReasonUnknownServer:
		return "NOT_FOUND"
	case runtime.ReasonSnapshotChanged:
		return "SNAPSHOT_CHANGED"
	case runtime.ReasonServerIneligible:
		return "SERVER_UNAVAILABLE"
	case runtime.ReasonSwitchInProgress:
		return "ACCOUNT_BUSY"
	case runtime.ReasonConnectCooldown:
		return "RATE_LIMITED"
	case runtime.ReasonSlotMultipleSessions:
		return "SLOT_MULTIPLE_SESSIONS"
	case runtime.ReasonPortUnavailable:
		return "PORT_UNAVAILABLE"
	case runtime.ReasonSessionOpen, runtime.ReasonGateStart, runtime.ReasonSessionUnverified:
		return "UPSTREAM_UNAVAILABLE"
	case runtime.ReasonStoreFailed:
		return "INTERNAL"
	case runtime.ReasonNoTarget:
		return "NO_ALTERNATE_SERVER"
	default:
		return "INTERNAL"
	}
}

// operationErrorStatus maps a stable operation error code to the HTTP status
// and envelope code used by wait:true.
func operationErrorStatus(code string) (int, string) {
	switch code {
	case "NOT_FOUND":
		return http.StatusNotFound, CodeNotFound
	case "INVALID_ARGUMENT":
		return http.StatusBadRequest, CodeInvalidArgument
	case "RATE_LIMITED":
		return http.StatusTooManyRequests, CodeRateLimited
	case "FROZEN":
		return http.StatusLocked, "FROZEN"
	case "PRECONDITION_FAILED", "SNAPSHOT_CHANGED":
		return http.StatusPreconditionFailed, "PRECONDITION_FAILED"
	case "UPSTREAM_UNAVAILABLE":
		return http.StatusServiceUnavailable, CodeUpstreamUnavailable
	case "OPERATION_TIMEOUT":
		return http.StatusGatewayTimeout, "OPERATION_TIMEOUT"
	case "INTERNAL":
		return http.StatusInternalServerError, CodeInternal
	default:
		return http.StatusConflict, CodeConflict
	}
}

// writeSelectNodeError writes a synchronous pre-operation refusal.
func (s *Server) writeSelectNodeError(w http.ResponseWriter, err error) {
	var selectErr *selectNodeError
	if errors.As(err, &selectErr) {
		writeErrorReason(w, selectErr.status, selectErr.code, selectErr.reason,
			selectErr.message, selectErr.details)
		return
	}
	s.writeStoreError(w, err)
}

// persistAfterOperation makes a terminal record durable promptly instead of
// waiting for the periodic snapshot writer. It is best-effort: the record is
// already in memory and the periodic writer remains the fallback.
func (s *Server) persistAfterOperation() {
	if s.cfg.Persist != nil {
		s.cfg.Persist()
	}
}

// decodeOptionalJSON is decodeJSON with an empty body allowed (select-node may
// omit the body and use the persisted selection). Unknown fields are still
// rejected, so a typo never becomes a silent default.
func decodeOptionalJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if r.Body == nil {
		return true
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return true
		}
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, CodeInvalidArgument,
				"request body exceeds the route limit")
			return false
		}
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "invalid JSON body: "+err.Error())
		return false
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument,
			"request body must contain a single JSON value")
		return false
	}
	return true
}
