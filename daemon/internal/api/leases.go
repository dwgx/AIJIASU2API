package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"ajsaccountd/internal/account"
	"ajsaccountd/internal/model"
	"ajsaccountd/internal/store"
	"ajsaccountd/internal/webhook"
)

// LeaseAction identifies the committed lease mutation passed to
// Config.LeasePublishHook.
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

// leaseCreateResult is the 201 body. Warning is set when the lease committed
// but the optional publish hook failed; Replayed is true when an
// Idempotency-Key returned the original lease instead of allocating again.
type leaseCreateResult struct {
	Lease    model.Lease `json:"lease"`
	Warning  string      `json:"warning,omitempty"`
	Replayed bool        `json:"replayed,omitempty"`
}

// leaseRevokeRequest is the DELETE /v1/leases/{id} body. The confirm flag is
// required so a bare DELETE cannot free a customer's node by accident.
type leaseRevokeRequest struct {
	Confirm bool `json:"confirm"`
}

// leaseSlotWarning returns the honest capacity warning for a lease on an
// account whose effective slot cap is above one. The store has already
// enforced the cap; this only tells the caller that only the first FV session
// can actually be served until multi-session is implemented.
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
func (s *Server) handleCreateLease(w http.ResponseWriter, r *http.Request) {
	var body leaseCreateRequest
	if !decodeJSON(w, r, &body) {
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
	})
	if err != nil {
		s.writeLeaseCreateError(w, err)
		return
	}

	result := leaseCreateResult{Lease: lease, Replayed: replayed}
	// A replay returned an existing lease; the original create already
	// emitted lease.created, so a retry must not fan out a second event.
	if !replayed {
		s.emitLeaseEvent(webhook.EventLeaseCreated, lease)
	}
	result.Warning = s.leaseSlotWarning(lease.AccountID)
	if warning := s.runLeasePublishHook(r, LeaseActionCreated, lease); warning != "" {
		if result.Warning != "" {
			result.Warning += "; " + warning
		} else {
			result.Warning = warning
		}
	}
	if result.Warning != "" {
		w.Header().Set("Warning", warningHeaderValue(result.Warning))
	}
	if replayed {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeJSON(w, http.StatusCreated, result)
}

// handleRevokeLease frees a lease after an explicit confirm. In panel mode the
// Resin lease is deleted first: a Resin failure answers 503 and leaves the
// store row in place instead of reporting a revoke that did not happen.
func (s *Server) handleRevokeLease(w http.ResponseWriter, r *http.Request) {
	var body leaseRevokeRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	if !body.Confirm {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument,
			"confirm must be true to revoke a lease")
		return
	}
	id := pathParam(r, "id")

	var revoked model.Lease
	if s.cfg.Panel != nil {
		// Read the row before the panel deletes it: the publish hook needs the
		// account/node it addressed even though the store row is gone after.
		if lease, ok := s.store.Lease(id); ok {
			revoked = lease
		}
		if err := s.cfg.Panel.ReleaseLease(r.Context(), id); err != nil {
			s.writePanelError(w, err)
			return
		}
		revoked.Status = model.LeaseRevoked
	} else {
		lease, err := s.store.RevokeLease(id)
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		revoked = lease
	}

	if revoked.ID != "" {
		s.emitLeaseEvent(webhook.EventLeaseRevoked, revoked)
		if warning := s.runLeasePublishHook(r, LeaseActionRevoked, revoked); warning != "" {
			w.Header().Set("Warning", warningHeaderValue(warning))
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// emitLeaseEvent publishes one committed lease transition. The payload is the
// shared lease contract (ids, platform, expiry) and never carries a key or
// secret; s.emit is a no-op when no sink is configured.
func (s *Server) emitLeaseEvent(event webhook.EventType, lease model.Lease) {
	s.emit(event, webhook.LeaseEventData(
		lease.ID, lease.UserID, string(lease.Platform), lease.AccountID, lease.ExpiresAt))
}

// runLeasePublishHook calls the optional hook and returns a warning string.
// The store mutation already committed, so a hook failure is logged and
// reported, never turned into a rollback or a failed request.
func (s *Server) runLeasePublishHook(r *http.Request, action LeaseAction, lease model.Lease) string {
	if s.cfg.LeasePublishHook == nil {
		return ""
	}
	if err := s.cfg.LeasePublishHook(r.Context(), action, lease); err != nil {
		s.logger.Warn("lease_publish_hook_failed",
			"action", string(action), "lease", lease.ID, "error", err)
		return fmt.Sprintf("lease %s, but the publish hook failed: %v", action, err)
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
