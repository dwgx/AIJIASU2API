package schedule

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/runtime"
	"ajsaccountd/internal/store"
)

// Catalog is the guarded switch surface the engine calls. It is the same
// controller the HTTP connect route uses, so the scheduler cannot bypass the
// one-session check.
type Catalog interface {
	CatalogServers(accountID string) ([]runtime.CatalogServer, bool)
	ConnectServer(ctx context.Context, accountID, serverID, snapshotID, trigger string) (runtime.SwitchResult, error)
	ReconnectAccount(ctx context.Context, accountID, snapshotID, trigger string) (runtime.SwitchResult, error)
}

func (e *Engine) performSafe(ctx context.Context, schedule model.Schedule, dryRun bool) (outcome actionOutcome) {
	defer func() {
		if recovered := recover(); recovered != nil {
			outcome = actionOutcome{
				Result: "error", Reason: "panic", ErrorClass: "internal",
				Detail: "scheduler recovered a panic", Pause: true,
			}
			e.logger.Error("schedule_panic", "schedule_id", schedule.ID, "panic", recovered)
		}
	}()
	return e.perform(ctx, schedule, dryRun)
}

func (e *Engine) perform(ctx context.Context, schedule model.Schedule, dryRun bool) actionOutcome {
	account, ok := e.store.Account(schedule.Target.ID)
	if !ok {
		return actionOutcome{Result: "error", Reason: "target_missing", ErrorClass: "permanent", Pause: true}
	}
	if account.State != model.AccountActive {
		return actionOutcome{Result: "error", Reason: "account_state", ErrorClass: "permanent", Pause: true}
	}
	if e.cfg.AllowMultiSession || e.cfg.NodeLimit > 1 {
		return actionOutcome{Result: "error", Reason: "unsupported_mode", ErrorClass: "permanent", Pause: true}
	}
	switch schedule.Action {
	case model.ActionAccountRotate:
		return e.performRotate(ctx, schedule, account, dryRun)
	case model.ActionAccountReconnect:
		return e.performReconnect(ctx, schedule, dryRun)
	default:
		return actionOutcome{Result: "error", Reason: "unsupported_action", ErrorClass: "permanent", Pause: true}
	}
}

func (e *Engine) performRotate(ctx context.Context, schedule model.Schedule, account model.Account, dryRun bool) actionOutcome {
	params, err := DecodeRotateParams(schedule.Target.Params)
	if err != nil {
		return actionOutcome{Result: "error", Reason: "invalid_params", ErrorClass: "permanent", Pause: true}
	}
	if e.catalog == nil {
		return actionOutcome{Result: "error", Reason: "login_disabled", ErrorClass: "permanent", Pause: true}
	}
	servers, ok := e.catalog.CatalogServers(account.ID)
	if !ok {
		return actionOutcome{Result: "error", Reason: "login_required", ErrorClass: "permanent", Pause: true}
	}
	if params.Strategy == "pool" {
		eligible := make(map[string]struct{}, len(servers))
		for _, server := range servers {
			if server.Eligible {
				eligible[server.ID] = struct{}{}
			}
		}
		remaining := 0
		for _, id := range params.ServerIDs {
			if _, ok := eligible[id]; ok {
				remaining++
			}
		}
		if remaining < 2 {
			return actionOutcome{Result: "error", Reason: "no_eligible_server", ErrorClass: "permanent", Pause: true}
		}
	}
	current := e.currentServer(account.ID)
	candidates := rotateCandidates(params, servers, current)
	if len(candidates) == 0 {
		return actionOutcome{Result: "error", Reason: "no_eligible_server", ErrorClass: "permanent", Pause: true}
	}
	chosen, cursor, hasCursor := pickRotateTarget(params, candidates, current, schedule.Cursor, e.randLockedIntn)
	if dryRun {
		detail := fmt.Sprintf("%s: would rotate to %s", account.ID, chosen)
		// A dry run records the attempt but must not advance the pool cursor.
		return actionOutcome{Result: "dry_run", Detail: detail}
	}
	timeout := 60 * time.Second
	if params.TimeoutSeconds > 0 {
		timeout = time.Duration(params.TimeoutSeconds) * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result, err := e.catalog.ConnectServer(runCtx, account.ID, chosen, "", "rotation")
	if err != nil {
		class, reason, pause := classifySwitchError(err, runCtx)
		return actionOutcome{Result: "error", Reason: reason, ErrorClass: class, Pause: pause,
			Detail: "rotation failed: " + reason}
	}
	if result.AlreadyConnected {
		e.recordCircuitSuccess(account.ID)
		return actionOutcome{Result: "skipped", Reason: "already_current",
			Detail: fmt.Sprintf("%s: already on %s", account.ID, chosen)}
	}
	e.recordCircuitSuccess(account.ID)
	detail := fmt.Sprintf("%s: %s -> %s", account.ID, result.SwitchedFrom, result.ServerID)
	return actionOutcome{Result: "ok", Detail: detail, AuditID: result.AuditID, Cursor: cursor, HasCursor: hasCursor}
}

func (e *Engine) performReconnect(ctx context.Context, schedule model.Schedule, dryRun bool) actionOutcome {
	params, err := DecodeReconnectParams(schedule.Target.Params)
	if err != nil {
		return actionOutcome{Result: "error", Reason: "invalid_params", ErrorClass: "permanent", Pause: true}
	}
	if e.catalog == nil {
		return actionOutcome{Result: "error", Reason: "login_disabled", ErrorClass: "permanent", Pause: true}
	}
	if e.currentServer(schedule.Target.ID) == "" {
		return actionOutcome{Result: "error", Reason: "no_target", ErrorClass: "permanent", Pause: true}
	}
	if dryRun {
		return actionOutcome{Result: "dry_run", Detail: fmt.Sprintf("%s: would reopen the current session", schedule.Target.ID)}
	}
	timeout := 60 * time.Second
	if params.TimeoutSeconds > 0 {
		timeout = time.Duration(params.TimeoutSeconds) * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result, err := e.catalog.ReconnectAccount(runCtx, schedule.Target.ID, "", "reconnect")
	if err != nil {
		class, reason, pause := classifySwitchError(err, runCtx)
		return actionOutcome{Result: "error", Reason: reason, ErrorClass: class, Pause: pause,
			Detail: "reconnect failed: " + reason}
	}
	e.recordCircuitSuccess(schedule.Target.ID)
	return actionOutcome{Result: "ok", AuditID: result.AuditID,
		Detail: fmt.Sprintf("%s: session reopened on %s", schedule.Target.ID, result.ServerID)}
}

// actionOutcome is the executor's classified result.
type actionOutcome struct {
	Result     string // ok|skipped|dry_run|error|unknown
	Reason     string
	ErrorClass string
	Detail     string
	AuditID    string
	Pause      bool
	Cursor     int
	HasCursor  bool
}

func (e *Engine) currentServer(accountID string) string {
	for _, node := range e.store.Nodes() {
		if node.AccountID == accountID && node.State == model.NodeLive {
			return node.ServerID
		}
	}
	if selection, ok := e.store.Selection(accountID); ok {
		return selection.ServerID
	}
	return ""
}

func dryRunOf(job runJob, schedule model.Schedule) bool {
	if job.dryRun != nil {
		return *job.dryRun
	}
	return schedule.Guard.DryRun
}

func fireKeyFor(scheduleID string, scheduled time.Time) string {
	return fmt.Sprintf("%s@%d", scheduleID, scheduled.Unix())
}

func triggerKind(schedule model.Schedule, manual bool) string {
	if manual {
		return "manual"
	}
	return schedule.Trigger.Kind
}

func allowsRunOnce(action model.ScheduleAction) bool {
	switch action {
	case model.ActionAccountRotate, model.ActionAccountReconnect, model.ActionAccountSwitch:
		return true
	default:
		return false
	}
}

// rotateCandidates filters the catalog to the strategy's candidate set and
// drops the current server when excludeCurrent is set.
func rotateCandidates(params model.RotateParams, servers []runtime.CatalogServer, current string) []runtime.CatalogServer {
	eligible := make(map[string]runtime.CatalogServer, len(servers))
	region := params.Region
	for _, server := range servers {
		if server.ID == current {
			region = server.Region
		}
	}
	if params.Region != "" {
		region = params.Region
	}
	for _, server := range servers {
		if !server.Eligible {
			continue
		}
		switch params.Strategy {
		case "pool":
			// membership checked below
		case "same-region":
			if region != "" && server.Region != region {
				continue
			}
		}
		eligible[server.ID] = server
	}
	candidates := make([]runtime.CatalogServer, 0, len(eligible))
	switch params.Strategy {
	case "pool":
		seen := make(map[string]struct{}, len(params.ServerIDs))
		for _, id := range params.ServerIDs {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			if server, ok := eligible[id]; ok {
				candidates = append(candidates, server)
			}
		}
	default:
		for _, server := range eligible {
			candidates = append(candidates, server)
		}
		sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })
	}
	excludeCurrent := true
	if params.ExcludeCurrent != nil {
		excludeCurrent = *params.ExcludeCurrent
	}
	if excludeCurrent && current != "" {
		filtered := candidates[:0]
		for _, server := range candidates {
			if server.ID != current {
				filtered = append(filtered, server)
			}
		}
		candidates = filtered
	}
	if len(candidates) == 0 {
		return nil
	}
	return candidates
}

func pickRotateTarget(params model.RotateParams, candidates []runtime.CatalogServer, current string, cursor int, randIntn func(int) int) (string, int, bool) {
	switch params.Strategy {
	case "pool":
		if cursor < 0 {
			cursor = 0
		}
		index := cursor % len(candidates)
		nextCursor := (index + 1) % len(candidates)
		return candidates[index].ID, nextCursor, true
	case "next-server":
		if current != "" {
			for _, server := range candidates {
				if server.ID > current {
					return server.ID, 0, false
				}
			}
		}
		return candidates[0].ID, 0, false
	case "random":
		if randIntn != nil {
			return candidates[randIntn(len(candidates))].ID, 0, false
		}
		return candidates[0].ID, 0, false
	default:
		return candidates[0].ID, 0, false
	}
}

func classifySwitchError(err error, ctx context.Context) (class, reason string, pause bool) {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || ctx.Err() != nil {
		return "unknown", "timeout", false
	}
	var switchErr *runtime.SwitchError
	if errors.As(err, &switchErr) {
		switch switchErr.Reason {
		case runtime.ReasonSwitchInProgress, runtime.ReasonConnectCooldown, runtime.ReasonPortUnavailable:
			return "conflict", switchErr.Reason, false
		case runtime.ReasonSlotMultipleSessions:
			return "invariant", switchErr.Reason, true
		case runtime.ReasonLoginRequired, runtime.ReasonLoginDisabled, runtime.ReasonAccountState,
			runtime.ReasonUnknownServer, runtime.ReasonServerIneligible, runtime.ReasonNoTarget,
			runtime.ReasonSnapshotChanged:
			return "permanent", switchErr.Reason, true
		case runtime.ReasonSessionOpen, runtime.ReasonGateStart, runtime.ReasonSessionUnverified:
			return "transient", switchErr.Reason, false
		case runtime.ReasonStoreFailed:
			return "internal", switchErr.Reason, true
		default:
			return "transient", switchErr.Reason, false
		}
	}
	var storeErr *store.Error
	if errors.As(err, &storeErr) {
		switch {
		case errors.Is(storeErr.Kind, store.ErrNotFound):
			return "permanent", "target_missing", true
		case errors.Is(storeErr.Kind, store.ErrConflict):
			return "conflict", "conflict", false
		default:
			return "transient", "store_error", false
		}
	}
	return "transient", "action_failed", false
}
