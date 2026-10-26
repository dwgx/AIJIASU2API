package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"ajsaccountd/internal/model"
)

// Operation retention (DESIGN-INTEGRATION §3.4): terminal records are bounded
// to the newest 200 and 24 h so a polling script can cross a restart without
// letting the snapshot grow. In-flight records are never pruned; the restore
// path turns them into failed/OPERATION_UNKNOWN instead.
const (
	OperationRetention   = 200
	OperationMaxAge      = 24 * time.Hour
	OperationRecordLimit = 1000
	operationIDPrefix    = "op-"
)

// OperationUnknownCode is the error code a restored in-flight operation
// carries: the daemon cannot prove whether the vendor call happened, so the
// record is failed, never retried and never reported as succeeded.
const OperationUnknownCode = "OPERATION_UNKNOWN"

// OperationFilter is the GET /v1/operations query. Empty fields do not filter;
// Limit 0 means no limit (the HTTP layer supplies the 50/200 bounds).
type OperationFilter struct {
	AccountID string
	State     model.OperationState
	Type      string
	Limit     int
}

// BeginOperation appends one queued operation and allocates its op-NNNN id.
// It refuses while another operation for the same account is queued or
// running, returning that operation so the API can answer 409 ACCOUNT_BUSY
// with its id. The check and the append share one lock, so two concurrent
// selects cannot both win.
func (s *Store) BeginOperation(in model.Operation) (model.Operation, *model.Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.accountIndexLocked(in.AccountID) < 0 {
		return model.Operation{}, nil, notFoundf("account %q not found", in.AccountID)
	}
	if strings.TrimSpace(in.Type) == "" {
		return model.Operation{}, nil, invalidf("operation type is required")
	}
	for i := range s.operations {
		op := s.operations[i]
		if op.AccountID != in.AccountID || op.State.Terminal() {
			continue
		}
		busy := cloneOperation(op)
		return model.Operation{}, &busy, nil
	}

	now := s.now()
	s.operationSeq++
	in.ID = fmt.Sprintf("%s%04d", operationIDPrefix, s.operationSeq)
	in.State = model.OperationQueued
	in.CreatedAt = iso(now)
	in.StartedAt = ""
	in.FinishedAt = ""
	in.Result = nil
	in.Error = nil
	if in.Steps == nil {
		in.Steps = []model.OperationStep{}
	}
	op := cloneOperation(in)
	s.operations = append([]model.Operation{op}, s.operations...)
	if s.operationWait == nil {
		s.operationWait = make(map[string]chan struct{})
	}
	s.operationWait[op.ID] = make(chan struct{})
	s.addAuditLocked(model.SeverityInfo, auditActor(in.RequestedBy), "operation.create", op.ID,
		"Operation queued", map[string]string{
			"type":      op.Type,
			"accountId": op.AccountID,
			"serverId":  op.Target.ServerID,
		})
	s.pruneOperationsLocked(now)
	if err := s.commitLocked(); err != nil {
		return model.Operation{}, nil, err
	}
	return cloneOperation(op), nil, nil
}

// MarkOperationRunning performs queued → running. It returns false when the
// operation was canceled before the executor started, in which case the
// executor must not touch the vendor.
func (s *Store) MarkOperationRunning(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	op := s.operationByIDLocked(id)
	if op == nil || op.State != model.OperationQueued {
		return false, nil
	}
	op.State = model.OperationRunning
	op.StartedAt = iso(s.now())
	if err := s.commitLocked(); err != nil {
		return false, err
	}
	return true, nil
}

// FinishOperation performs running → succeeded|failed. It is a no-op when the
// record is already terminal, so a late executor cannot overwrite a cancel.
func (s *Store) FinishOperation(id string, state model.OperationState, steps []model.OperationStep, result *model.SelectNodeResult, opErr *model.OperationError) (model.Operation, bool, error) {
	if !state.Terminal() || state == model.OperationCanceled {
		return model.Operation{}, false, nil
	}
	// A failed row without an error code cannot be restored (validation
	// rejects it) and a succeeded row must not carry one.
	if state == model.OperationFailed && opErr == nil {
		return model.Operation{}, false, nil
	}
	if state == model.OperationSucceeded && opErr != nil {
		return model.Operation{}, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	op := s.operationByIDLocked(id)
	if op == nil || op.State != model.OperationRunning {
		return model.Operation{}, false, nil
	}
	now := s.now()
	op.State = state
	op.FinishedAt = iso(now)
	if steps != nil {
		op.Steps = append([]model.OperationStep{}, steps...)
	}
	op.Result = cloneSelectNodeResult(result)
	op.Error = cloneOperationError(opErr)
	s.closeOperationWaitLocked(id)
	severity := model.SeverityInfo
	detail := "Operation succeeded"
	meta := map[string]string{
		"type":      op.Type,
		"accountId": op.AccountID,
		"state":     string(state),
	}
	if opErr != nil {
		severity = model.SeverityWarn
		detail = "Operation failed · " + opErr.Code
		meta["errorCode"] = opErr.Code
	}
	s.addAuditLocked(severity, "system", "operation.finish", op.ID, detail, meta)
	s.pruneOperationsLocked(now)
	if err := s.commitLocked(); err != nil {
		return model.Operation{}, false, err
	}
	return cloneOperation(*op), true, nil
}

// CancelOperation is the best-effort DELETE. A queued operation is canceled
// before any vendor call. A running one answers ErrConflict: the guarded
// switch is one executor step that includes opening the vendor session, so
// there is no safe step boundary to stop at without risking a half-open
// session. A terminal record is also ErrConflict.
func (s *Store) CancelOperation(id string) (model.Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	op := s.operationByIDLocked(id)
	if op == nil {
		return model.Operation{}, notFoundf("operation %q not found", id)
	}
	switch op.State {
	case model.OperationQueued:
		now := s.now()
		op.State = model.OperationCanceled
		op.FinishedAt = iso(now)
		op.Steps = append(op.Steps, model.OperationStep{
			Name: "dispatch", State: model.OperationStepCanceled,
		})
		s.closeOperationWaitLocked(id)
		s.addAuditLocked(model.SeverityInfo, "system", "operation.cancel", op.ID,
			"Operation canceled before dispatch", map[string]string{
				"type":      op.Type,
				"accountId": op.AccountID,
			})
		if err := s.commitLocked(); err != nil {
			return model.Operation{}, err
		}
		return cloneOperation(*op), nil
	case model.OperationRunning:
		return cloneOperation(*op), conflictf("operation %s is running and cannot be canceled at a step boundary", id)
	default:
		return cloneOperation(*op), conflictf("operation %s already finished", id)
	}
}

// Operation returns one record by id.
func (s *Store) Operation(id string) (model.Operation, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	op := s.operationByIDLocked(id)
	if op == nil {
		return model.Operation{}, false
	}
	return cloneOperation(*op), true
}

// Operations returns a newest-first copy filtered by account, state and type.
func (s *Store) Operations(filter OperationFilter) []model.Operation {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.Operation, 0, len(s.operations))
	for _, op := range s.operations {
		if filter.AccountID != "" && op.AccountID != filter.AccountID {
			continue
		}
		if filter.State != "" && op.State != filter.State {
			continue
		}
		if filter.Type != "" && op.Type != filter.Type {
			continue
		}
		out = append(out, cloneOperation(op))
		if filter.Limit > 0 && len(out) >= filter.Limit {
			break
		}
	}
	return out
}

// WaitOperation blocks until the record is terminal, the context expires or
// the id disappears. A terminal record returns immediately, so a waiter that
// arrives after the executor finished never misses the result.
func (s *Store) WaitOperation(ctx context.Context, id string) (model.Operation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	op := s.operationByIDLocked(id)
	if op == nil {
		s.mu.Unlock()
		return model.Operation{}, notFoundf("operation %q not found", id)
	}
	if op.State.Terminal() {
		done := cloneOperation(*op)
		s.mu.Unlock()
		return done, nil
	}
	if s.operationWait == nil {
		s.operationWait = make(map[string]chan struct{})
	}
	wait := s.operationWait[id]
	if wait == nil {
		wait = make(chan struct{})
		s.operationWait[id] = wait
	}
	s.mu.Unlock()

	select {
	case <-wait:
	case <-ctx.Done():
		return model.Operation{}, ctx.Err()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	op = s.operationByIDLocked(id)
	if op == nil {
		return model.Operation{}, notFoundf("operation %q not found", id)
	}
	return cloneOperation(*op), nil
}

func (s *Store) operationByIDLocked(id string) *model.Operation {
	for i := range s.operations {
		if s.operations[i].ID == id {
			return &s.operations[i]
		}
	}
	return nil
}

func (s *Store) closeOperationWaitLocked(id string) {
	if wait, ok := s.operationWait[id]; ok {
		close(wait)
		delete(s.operationWait, id)
	}
}

// pruneOperationsLocked keeps every in-flight record and the newest
// OperationRetention terminal ones, dropping terminal records older than
// OperationMaxAge.
func (s *Store) pruneOperationsLocked(now time.Time) {
	if len(s.operations) == 0 {
		return
	}
	kept := make([]model.Operation, 0, len(s.operations))
	terminal := 0
	for _, op := range s.operations {
		if !op.State.Terminal() {
			kept = append(kept, op)
			continue
		}
		terminal++
		if terminal > OperationRetention {
			continue
		}
		if finished, err := time.Parse(time.RFC3339, op.FinishedAt); err == nil && now.Sub(finished) > OperationMaxAge {
			continue
		}
		kept = append(kept, op)
	}
	s.operations = kept
}

// validateOperations rejects a snapshot whose operation rows cannot be loaded
// as-is. The restore path repairs in-flight rows after this check; it never
// silently drops a malformed terminal row. An operation may outlive its
// account (delete does not cascade the history), so account existence is not
// required here.
func (snap *Snapshot) validateOperations() error {
	if len(snap.Operations) > OperationRecordLimit {
		return fmt.Errorf("state snapshot has %d operations (max %d)", len(snap.Operations), OperationRecordLimit)
	}
	ids := make(map[string]struct{}, len(snap.Operations))
	for _, op := range snap.Operations {
		if strings.TrimSpace(op.ID) == "" {
			return fmt.Errorf("state snapshot has an operation without an id")
		}
		if !strings.HasPrefix(op.ID, operationIDPrefix) {
			return fmt.Errorf("state snapshot operation %q has an unexpected id prefix", op.ID)
		}
		if _, ok := ids[op.ID]; ok {
			return fmt.Errorf("state snapshot has duplicate operation %q", op.ID)
		}
		ids[op.ID] = struct{}{}
		if !op.State.Valid() {
			return fmt.Errorf("state snapshot operation %q has unknown state %q", op.ID, op.State)
		}
		if strings.TrimSpace(op.Type) == "" {
			return fmt.Errorf("state snapshot operation %q has no type", op.ID)
		}
		if strings.TrimSpace(op.CreatedAt) == "" {
			return fmt.Errorf("state snapshot operation %q has no createdAt", op.ID)
		}
		for _, stamp := range []string{op.CreatedAt, op.StartedAt, op.FinishedAt} {
			if stamp == "" {
				continue
			}
			if _, err := time.Parse(time.RFC3339, stamp); err != nil {
				return fmt.Errorf("state snapshot operation %q has invalid timestamp %q", op.ID, stamp)
			}
		}
		if op.State == model.OperationFailed && op.Error == nil {
			return fmt.Errorf("state snapshot operation %q failed without an error", op.ID)
		}
		if op.Error != nil && strings.TrimSpace(op.Error.Code) == "" {
			return fmt.Errorf("state snapshot operation %q has an error without a code", op.ID)
		}
	}
	return nil
}

// normalizeOperations raises the sequence counter, fills nil step slices and
// fails any record that was still in flight when the document was written.
// It returns the interrupted ids so Restore can audit them.
func (snap *Snapshot) normalizeOperations(now time.Time) []string {
	if snap.Operations == nil {
		snap.Operations = []model.Operation{}
	}
	interrupted := make([]string, 0)
	for i := range snap.Operations {
		op := &snap.Operations[i]
		snap.OperationSeq = max(snap.OperationSeq, sequenceOf(op.ID, operationIDPrefix))
		if op.Steps == nil {
			op.Steps = []model.OperationStep{}
		}
		if op.State.Terminal() {
			continue
		}
		op.State = model.OperationFailed
		op.FinishedAt = iso(now)
		op.Result = nil
		op.Error = &model.OperationError{
			Code:    OperationUnknownCode,
			Message: "daemon restarted before the operation finished",
		}
		op.Steps = append(op.Steps, model.OperationStep{
			Name: "interrupted", State: model.OperationStepFailed,
		})
		interrupted = append(interrupted, op.ID)
	}
	snap.pruneOperations(now)
	return interrupted
}

func (snap *Snapshot) pruneOperations(now time.Time) {
	if len(snap.Operations) == 0 {
		return
	}
	kept := make([]model.Operation, 0, len(snap.Operations))
	terminal := 0
	for _, op := range snap.Operations {
		if !op.State.Terminal() {
			kept = append(kept, op)
			continue
		}
		terminal++
		if terminal > OperationRetention {
			continue
		}
		if finished, err := time.Parse(time.RFC3339, op.FinishedAt); err == nil && now.Sub(finished) > OperationMaxAge {
			continue
		}
		kept = append(kept, op)
	}
	snap.Operations = kept
}

func cloneOperation(op model.Operation) model.Operation {
	op.Steps = append([]model.OperationStep{}, op.Steps...)
	op.Result = cloneSelectNodeResult(op.Result)
	op.Error = cloneOperationError(op.Error)
	return op
}

func cloneSelectNodeResult(result *model.SelectNodeResult) *model.SelectNodeResult {
	if result == nil {
		return nil
	}
	cloned := *result
	cloned.Warnings = append([]string{}, result.Warnings...)
	return &cloned
}

func cloneOperationError(opErr *model.OperationError) *model.OperationError {
	if opErr == nil {
		return nil
	}
	cloned := *opErr
	if opErr.Details != nil {
		cloned.Details = make(map[string]string, len(opErr.Details))
		for key, value := range opErr.Details {
			cloned.Details[key] = value
		}
	}
	return &cloned
}
