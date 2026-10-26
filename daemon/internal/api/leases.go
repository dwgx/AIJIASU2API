package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"ajsaccountd/internal/account"
	"ajsaccountd/internal/model"
	"ajsaccountd/internal/store"
	"ajsaccountd/internal/webhook"
)

// LeaseAction identifies the lease mutation passed to Config.LeasePublishHook:
// LeaseActionCreated before a create is answered (fail-closed, so an error
// rolls the lease back) and LeaseActionRevoked after a revoke committed (a
// warning, because the row is already gone).
type LeaseAction string

const (
	LeaseActionCreated LeaseAction = "created"
	LeaseActionRevoked LeaseAction = "revoked"
)

// maxLeaseIdempotencyKeyLen matches the OpenAPI header/schema bound.
const maxLeaseIdempotencyKeyLen = 128

// leaseCreateRequest is the POST /v1/leases body. TTLSeconds 0 asks the store
// for the user's sticky TTL, then the panel lease TTL.
type leaseCreateRequest struct {
	UserID         string         `json:"userId"`
	Platform       model.Platform `json:"platform"`
	AccountID      string         `json:"accountId"`
	TTLSeconds     int            `json:"ttlSeconds"`
	IdempotencyKey string         `json:"idempotencyKey"`
}

// leaseCreateResult is the 201 body. Warning carries the honest slot-capacity
// note, not a publication failure: a lease whose publication failed is rolled
// back and answered with an error envelope instead of a 201. Replayed is true
// when an Idempotency-Key returned the original lease instead of allocating
// again.
type leaseCreateResult struct {
	Lease    model.Lease `json:"lease"`
	Warning  string      `json:"warning,omitempty"`
	Replayed bool        `json:"replayed,omitempty"`
}

// leaseReasonPublishFailed is the machine reason of a create whose publication
// failed. The store row was rolled back before the error was written, so a
// client may retry with the same Idempotency-Key and allocate again.
const leaseReasonPublishFailed = "lease_publish_failed"

// leaseRevokeRequest is the DELETE /v1/leases/{id} body. The confirm flag is
// required so a bare DELETE cannot free a customer's node by accident.
type leaseRevokeRequest struct {
	Confirm bool `json:"confirm"`
}

// leaseSlotWarning returns the honest display-capacity warning for a lease on
// an account whose effective slot capacity is above one. Admission counts
// verified, listener-bound serving slots; the live account has UseCount=1 and
// two-slot coexistence is unproven, so the warning is retained for API
// compatibility.
func (s *Server) leaseSlotWarning(accountID string) string {
	acct, ok := s.store.Account(accountID)
	if !ok {
		return ""
	}
	return account.MultiSessionWarningFor(account.EffectiveSlots(acct.UseCount, account.ConfiguredAccountSlots()))
}

// handleCreateLease allocates a free node to a user. The Idempotency-Key
// header (or the body's idempotencyKey) makes a retried create return the
// original lease; a second create for the same user and platform is a 409,
// an exhausted user quota or account concurrency is a 429, and a missing user
// or account is a 404.
//
// Publication is fail-closed: a lease is only reported created after the panel
// (and the optional hook) accepted it. A publication failure rolls the store
// row back and answers 409/503 with reason lease_publish_failed, so a caller
// never receives a lease the external control plane cannot serve.
func (s *Server) handleCreateLease(w http.ResponseWriter, r *http.Request) {
	var body leaseCreateRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	// A customer scoped key is bound to its own user subject: an empty userId
	// becomes the key's user, and any other id is refused before the store can
	// allocate a lease for someone else. Admin callers keep the explicit
	// userId contract.
	subject, customer := customerKey(r)
	if customer {
		if requested := strings.TrimSpace(body.UserID); requested != "" && requested != subject.UserID {
			writeErrorReason(w, http.StatusForbidden, CodeForbidden, ReasonScopedKeyRestricted,
				"scoped key is bound to its own userId; userId must match it", nil)
			return
		}
		body.UserID = subject.UserID
	}
	// A restricted key must name an account it may use: an empty accountId
	// would let the store allocate on any free account, which the key's
	// accountIds/regions cannot cover.
	if s.denyScopedKeyAccountTarget(w, r, body.AccountID) {
		return
	}
	headerKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	bodyKey := strings.TrimSpace(body.IdempotencyKey)
	if headerKey != "" && bodyKey != "" && headerKey != bodyKey {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument,
			"Idempotency-Key header and idempotencyKey body field disagree")
		return
	}
	if len(headerKey) > maxLeaseIdempotencyKeyLen {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument,
			"Idempotency-Key must be at most 128 characters")
		return
	}
	key := headerKey
	if key == "" {
		key = bodyKey
	}

	lease, replayed, err := s.store.CreateLease(model.CreateLeaseInput{
		UserID:         body.UserID,
		Platform:       body.Platform,
		AccountID:      body.AccountID,
		TTLSeconds:     body.TTLSeconds,
		IdempotencyKey: key,
	}, s.auditActor(r))
	if err != nil {
		s.writeLeaseCreateError(w, err)
		return
	}
	// The store answers an idempotent replay before it looks at the requested
	// accountId, so the lease that actually came back is checked again: a
	// customer key must neither see nor republish a lease for another user or
	// on an account it may not use. A fresh create is already pinned to the
	// checked subject and account, so this can only fire on a replay, where
	// nothing was mutated.
	if replayed && customer && lease.UserID != subject.UserID {
		writeErrorReason(w, http.StatusForbidden, CodeForbidden, ReasonScopedKeyRestricted,
			"scoped key is bound to its own userId; the replayed lease belongs to another user", nil)
		return
	}
	if replayed && !s.scopedKeyAllowsAccount(r, lease.AccountID) {
		writeErrorReason(w, http.StatusForbidden, CodeForbidden, ReasonScopedKeyRestricted,
			"scoped key is restricted to named accounts; the replayed lease belongs to another account", nil)
		return
	}

	result := leaseCreateResult{Lease: lease, Replayed: replayed}
	// A replay returned an existing lease; the original create already
	// published it and emitted lease.created, so a retry must not fan out a
	// second event or a second publication.
	if !replayed {
		// Fail closed: a lease is only reported created after every configured
		// control plane accepted it. The rollback frees the node, so the retry
		// the client is about to make allocates cleanly instead of replaying a
		// row the panel cannot serve.
		if err := s.publishLease(r.Context(), lease); err != nil {
			s.rollbackUnpublishedLease(lease, err)
			s.writeLeasePublishError(w, lease, err)
			return
		}
		s.emitLeaseEvent(webhook.EventLeaseCreated, lease)
	}
	result.Warning = s.leaseSlotWarning(lease.AccountID)
	if result.Warning != "" {
		w.Header().Set("Warning", warningHeaderValue(result.Warning))
	}
	if replayed {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeJSON(w, http.StatusCreated, result)
}

// handleRevokeLease frees a lease after an explicit confirm. The lease is
// resolved and bound to the caller first, so a customer key cannot revoke
// another user's lease and never reaches the panel or the store with one. In
// panel mode the Resin lease is deleted before the store row: a Resin failure
// answers 503 and leaves the store row in place instead of reporting a revoke
// that did not happen.
func (s *Server) handleRevokeLease(w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	lease, denied := s.authorizeLeaseMutation(w, r, id)
	if denied {
		return
	}
	var body leaseRevokeRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	if !body.Confirm {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument,
			"confirm must be true to revoke a lease")
		return
	}

	var revoked model.Lease
	if s.cfg.Panel != nil {
		// The row was read before the panel deletes it: the publish hook needs
		// the account/node it addressed even though the store row is gone
		// after.
		revoked = lease
		if err := s.cfg.Panel.ReleaseLease(r.Context(), id); err != nil {
			s.writePanelError(w, err)
			return
		}
		revoked.Status = model.LeaseRevoked
	} else {
		revokedLease, err := s.store.RevokeLease(id, s.auditActor(r))
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		revoked = revokedLease
	}

	if revoked.ID != "" {
		s.emitLeaseEvent(webhook.EventLeaseRevoked, revoked)
		if warning := s.runLeaseRevokeHook(r, revoked); warning != "" {
			w.Header().Set("Warning", warningHeaderValue(warning))
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// emitLeaseEvent publishes one committed lease transition. The payload is the
// shared lease contract (ids, platform, expiry) and never carries a key or
// secret; s.emit is a no-op when no sink is configured. The same transition is
// mirrored into the real-time debug trace, where the lease id, node, slot and
// generation are attribution, not a credential.
func (s *Server) emitLeaseEvent(event webhook.EventType, lease model.Lease) {
	kind := ""
	switch event {
	case webhook.EventLeaseCreated:
		kind = model.DebugKindLeaseCreate
	case webhook.EventLeaseRevoked:
		kind = model.DebugKindLeaseRevoke
	}
	if kind != "" {
		s.cfg.Debug.Record(model.DebugTrace{
			Kind:       kind,
			AccountID:  lease.AccountID,
			NodeID:     lease.NodeID,
			Slot:       lease.Slot,
			Generation: lease.Generation,
			State:      string(lease.Status),
			Detail:     "lease " + lease.ID,
		})
	}
	s.emit(event, webhook.LeaseEventData(
		lease.ID, lease.UserID, string(lease.Platform), lease.AccountID, lease.ExpiresAt))
}

// publishLease publishes one committed lease to every configured control
// plane. It is the fail-closed half of create: the caller rolls the lease back
// when this returns an error. The panel is the Resin publication; the hook is
// the seam for a deployment that publishes leases elsewhere. Both must succeed.
func (s *Server) publishLease(ctx context.Context, lease model.Lease) error {
	if s.cfg.Panel != nil {
		if err := s.cfg.Panel.PublishLease(ctx, lease); err != nil {
			return err
		}
	}
	if s.cfg.LeasePublishHook != nil {
		if err := s.cfg.LeasePublishHook(ctx, LeaseActionCreated, lease); err != nil {
			return fmt.Errorf("publish hook: %w", err)
		}
	}
	return nil
}

// PublishReservedLease publishes one lease an order fulfilment attempt reserved
// and is the fulfilment twin of publishLease: the same Panel and
// LeasePublishHook boundary, with the store row as the idempotence check. A
// retry after an interrupted attempt may present the same reservation again,
// so an already-active row is inspected and reported published, and a row that
// is gone (released by recovery or an abort) is refused instead of published
// for a reservation that no longer exists. Only a pending_publication row is
// actually published. It satisfies fulfilment.Publisher.
func (s *Server) PublishReservedLease(ctx context.Context, lease model.Lease) error {
	current, ok := s.store.Lease(lease.ID)
	if !ok {
		return fmt.Errorf("%w: reserved lease %q was released; nothing to publish",
			store.ErrConflict, lease.ID)
	}
	switch current.Status {
	case model.LeaseActive:
		// A previous attempt already promoted this exact row. Publishing
		// again would duplicate external work for a lease that already
		// serves.
		return nil
	case model.LeasePendingPublication:
		return s.publishLease(ctx, current)
	default:
		return fmt.Errorf("%w: reserved lease %q is %s, want %s",
			store.ErrConflict, lease.ID, current.Status, model.LeasePendingPublication)
	}
}

// LeaseCreated publishes the serving lease a committed initial fulfilment
// created. The worker calls it after the store's commit, never before.
func (s *Server) LeaseCreated(lease model.Lease) {
	s.emitLeaseEvent(webhook.EventLeaseCreated, lease)
}

// LeaseReissued publishes the replacement lease a committed reissue created.
// previousLeaseID names the ended lease the customer was moved off; the shared
// lease payload carries ids and expiry, never a key.
func (s *Server) LeaseReissued(lease model.Lease, _ string) {
	s.emitLeaseEvent(webhook.EventLeaseReissued, lease)
}

// WithdrawReservedLease withdraws a published reservation that will never
// commit. Resin has no per-lease create for a reservation — publication is a
// reconcile of the node's subscription, and the panel's account lease belongs
// to serving rows — so the publish hook is the only external object a
// deployment can hold. It receives the revoke action; the store row itself is
// released by the worker's dead-letter path. An active row is refused: a
// fulfilment cleanup must never remove service.
func (s *Server) WithdrawReservedLease(ctx context.Context, lease model.Lease) error {
	current, ok := s.store.Lease(lease.ID)
	if ok {
		if current.Status != model.LeasePendingPublication {
			return fmt.Errorf("%w: lease %q is %s; refusing to withdraw a serving row",
				store.ErrConflict, lease.ID, current.Status)
		}
		lease = current
	} else if strings.TrimSpace(lease.AccountID) == "" {
		// The row is gone and the caller has no account identity: only the
		// hook can still withdraw an external object.
		if s.cfg.LeasePublishHook != nil {
			if err := s.cfg.LeasePublishHook(ctx, LeaseActionRevoked, lease); err != nil {
				return fmt.Errorf("withdraw hook: %w", err)
			}
		}
		return nil
	}
	// The panel deletes the account's external lease. That deletion is only
	// identity-safe when no other lease row uses the account: a newer serving
	// lease must never be removed by a stale reservation's cleanup.
	if s.cfg.Panel != nil && s.reservationOwnsAccount(lease) {
		if err := s.cfg.Panel.WithdrawLease(ctx, lease.AccountID); err != nil {
			return fmt.Errorf("withdraw panel lease: %w", err)
		}
	}
	if s.cfg.LeasePublishHook != nil {
		if err := s.cfg.LeasePublishHook(ctx, LeaseActionRevoked, lease); err != nil {
			return fmt.Errorf("withdraw hook: %w", err)
		}
	}
	return nil
}

// reservationOwnsAccount reports whether the reservation is the only lease row
// for its account. Account-level external deletion is safe only then; with a
// concurrent serving lease the panel must not delete the account's lease.
func (s *Server) reservationOwnsAccount(lease model.Lease) bool {
	for _, other := range s.store.Leases() {
		if other.AccountID == lease.AccountID && other.ID != lease.ID {
			return false
		}
	}
	return true
}

// rollbackUnpublishedLease is the compensating step of a failed publication:
// the store row disappears, the node's slot is free, and one error audit row
// records why. The debug trace carries the same refusal for the live console.
// No lease.created event is emitted for a lease the caller never received. The
// log line carries only a bounded class: the panel/hook error can embed a URL,
// token or provider reference, and the sanitized operator detail is the store
// audit row written by RollbackLease.
func (s *Server) rollbackUnpublishedLease(lease model.Lease, cause error) {
	if _, err := s.store.RollbackLease(lease.ID, cause.Error()); err != nil {
		s.logger.Error("lease_rollback_failed", "lease", lease.ID, "error", err)
	}
	s.logger.Warn("lease_publish_failed",
		"lease", lease.ID,
		"account_id", lease.AccountID,
		"node_id", lease.NodeID,
		"error", publishFailureClass(cause))
	s.cfg.Debug.Record(model.DebugTrace{
		Kind:       model.DebugKindLeaseCreate,
		AccountID:  lease.AccountID,
		NodeID:     lease.NodeID,
		Slot:       lease.Slot,
		Generation: lease.Generation,
		State:      "failed",
		ErrorClass: leaseReasonPublishFailed,
		Detail:     "lease " + lease.ID + " · " + cause.Error(),
	})
}

// writeLeasePublishError maps a publication failure onto the error envelope.
// The lease no longer exists, so the answer says "not created": 409 when the
// panel refused the configuration (no platform yet, a node that is not
// published) and 503 when Resin itself failed. The reason is stable so a client
// can retry deliberately instead of parsing the message.
func (s *Server) writeLeasePublishError(w http.ResponseWriter, lease model.Lease, cause error) {
	status, code := http.StatusServiceUnavailable, CodeUpstreamUnavailable
	if errors.Is(cause, store.ErrConflict) {
		status, code = http.StatusConflict, CodeConflict
	}
	writeJSON(w, status, errorResponse{Error: errorDetail{
		Code:    code,
		Message: fmt.Sprintf("lease %s was not created: publication failed", lease.ID),
		Reason:  leaseReasonPublishFailed,
	}})
}

// publishFailureClass reduces an external publication error to a bounded,
// credential-free label for logs and responses. The sanitized operator detail
// is the store audit row written by RollbackLease.
func publishFailureClass(cause error) string {
	switch {
	case errors.Is(cause, store.ErrConflict):
		return "conflict"
	case errors.Is(cause, store.ErrUpstream):
		return "upstream"
	default:
		return "publish_failed"
	}
}

// runLeaseRevokeHook calls the optional hook after a revoke committed. Unlike
// create it is not fail-closed: the row is already gone, so a hook failure is
// logged and reported in the Warning header. The Resin revoke itself is
// fail-closed before the store mutation (handleRevokeLease releases the panel
// lease first), which is the path that must never report a revoke that did not
// happen.
func (s *Server) runLeaseRevokeHook(r *http.Request, lease model.Lease) string {
	if s.cfg.LeasePublishHook == nil {
		return ""
	}
	if err := s.cfg.LeasePublishHook(r.Context(), LeaseActionRevoked, lease); err != nil {
		s.logger.Warn("lease_publish_hook_failed",
			"action", string(LeaseActionRevoked), "lease", lease.ID, "error", err)
		return fmt.Sprintf("lease %s, but the publish hook failed: %v", LeaseActionRevoked, err)
	}
	return ""
}

// writeLeaseCreateError maps a store refusal onto the error envelope, keeping
// the machine-readable reason the dashboard uses to explain the refusal.
func (s *Server) writeLeaseCreateError(w http.ResponseWriter, err error) {
	var leaseErr *store.LeaseError
	if !errors.As(err, &leaseErr) {
		s.writeStoreError(w, err)
		return
	}
	status, code := http.StatusInternalServerError, CodeInternal
	switch leaseErr.Kind {
	case store.ErrInvalid:
		status, code = http.StatusBadRequest, CodeInvalidArgument
	case store.ErrNotFound:
		status, code = http.StatusNotFound, CodeNotFound
	case store.ErrConflict:
		status, code = http.StatusConflict, CodeConflict
	case store.ErrQuota:
		status, code = http.StatusTooManyRequests, CodeQuotaExceeded
	}
	writeJSON(w, status, errorResponse{Error: errorDetail{
		Code:    code,
		Message: leaseErr.Message,
		Reason:  leaseErr.Reason,
	}})
}

// warningHeaderValue renders an RFC 7234 Warning value with the header's
// quoting rules; newlines would split the header, so they become spaces. The
// truncation counts runes so a multi-byte message cannot leave invalid UTF-8.
func warningHeaderValue(message string) string {
	message = strings.NewReplacer("\r", " ", "\n", " ", `"`, "'").Replace(message)
	if runes := []rune(message); len(runes) > 300 {
		message = string(runes[:300])
	}
	return `299 ajs-accountd "` + message + `"`
}
