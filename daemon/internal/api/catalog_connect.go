package api

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/runtime"
	"ajsaccountd/internal/store"
)

// CatalogConnector is the guarded switch path behind the selection and
// connect/disconnect/reconnect routes. *runtime.NodeController implements it;
// nil means AJS_ACCOUNT_LOGIN is not enabled, so writes answer login_disabled
// instead of pretending to have a vendor catalog (DESIGN-CATALOG §3.4-3.5).
type CatalogConnector interface {
	SnapshotID() string
	LookupServer(accountID, serverID string) runtime.ServerLookup
	ConnectServer(ctx context.Context, accountID, serverID, snapshotID, trigger string) (runtime.SwitchResult, error)
	ReconnectAccount(ctx context.Context, accountID, snapshotID, trigger string) (runtime.SwitchResult, error)
	DisconnectAccount(ctx context.Context, accountID string) (runtime.DisconnectResult, error)
}

// selectionBody is the PUT /v1/accounts/{id}/selection body. snapshotId is
// optional: when sent it must match the current catalog snapshot.
type selectionBody struct {
	ServerID   string `json:"serverId"`
	SnapshotID string `json:"snapshotId,omitempty"`
}

// connectBody is the POST connect body. confirm is intentionality, not a
// security boundary (DESIGN-CATALOG §3.11).
type connectBody struct {
	ServerID   string `json:"serverId"`
	SnapshotID string `json:"snapshotId,omitempty"`
	Confirm    bool   `json:"confirm"`
}

// confirmBody is the disconnect/reconnect body.
type confirmBody struct {
	SnapshotID string `json:"snapshotId,omitempty"`
	Confirm    bool   `json:"confirm"`
}

// handleGetSelection serves GET /v1/accounts/{id}/selection. A missing target
// is 404 no_target, the same reason reconnect uses.
func (s *Server) handleGetSelection(w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	if _, ok := s.store.Account(id); !ok {
		writeError(w, http.StatusNotFound, CodeNotFound, "account not found")
		return
	}
	selection, ok := s.store.Selection(id)
	if !ok {
		writeErrorReason(w, http.StatusNotFound, CodeNotFound, runtime.ReasonNoTarget,
			"account has no selection", nil)
		return
	}
	writeJSON(w, http.StatusOK, s.selectionView(selection))
}

// handlePutSelection serves PUT /v1/accounts/{id}/selection. It persists the
// target only: no session starts and the current node does not change.
func (s *Server) handlePutSelection(w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	if _, ok := s.store.Account(id); !ok {
		writeError(w, http.StatusNotFound, CodeNotFound, "account not found")
		return
	}
	var body selectionBody
	if !decodeJSON(w, r, &body) {
		return
	}
	body.ServerID = strings.TrimSpace(body.ServerID)
	if body.ServerID == "" {
		writeErrorReason(w, http.StatusBadRequest, CodeInvalidArgument, "invalid_argument",
			"serverId is required", nil)
		return
	}
	if s.cfg.Catalog == nil {
		writeErrorReason(w, http.StatusConflict, CodeConflict, runtime.ReasonLoginDisabled,
			"account login is disabled (AJS_ACCOUNT_LOGIN=0)", nil)
		return
	}
	lookup := s.cfg.Catalog.LookupServer(id, body.ServerID)
	if !lookup.LoginAvailable {
		writeErrorReason(w, http.StatusConflict, CodeConflict, runtime.ReasonLoginRequired,
			"no retained login result; log in again", nil)
		return
	}
	if !lookup.Found {
		writeErrorReason(w, http.StatusNotFound, CodeNotFound, runtime.ReasonUnknownServer,
			"server is not in the retained catalog", nil)
		return
	}
	if !lookup.Eligible {
		details := map[string]string{}
		if lookup.IneligibleReason != "" {
			details["ineligibleReason"] = lookup.IneligibleReason
		}
		writeErrorReason(w, http.StatusConflict, CodeConflict, runtime.ReasonServerIneligible,
			"server is not eligible", details)
		return
	}
	if err := s.checkSnapshot(body.SnapshotID); err != nil {
		s.writeCatalogError(w, err)
		return
	}
	selection, err := s.store.SetSelection(id, body.ServerID, "operator")
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, model.CatalogSelection{
		AccountID: selection.AccountID,
		ServerID:  selection.ServerID,
		Title:     lookup.Title,
		UpdatedAt: selection.UpdatedAt,
		UpdatedBy: selection.UpdatedBy,
	})
}

// handleDeleteSelection serves DELETE /v1/accounts/{id}/selection. It is
// idempotent and never disconnects the current session.
func (s *Server) handleDeleteSelection(w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	if _, ok := s.store.Account(id); !ok {
		writeError(w, http.StatusNotFound, CodeNotFound, "account not found")
		return
	}
	if _, err := s.store.ClearSelection(id, "operator"); err != nil {
		s.writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleConnectAccount serves POST /v1/accounts/{id}/actions/connect.
func (s *Server) handleConnectAccount(w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	if _, ok := s.store.Account(id); !ok {
		writeError(w, http.StatusNotFound, CodeNotFound, "account not found")
		return
	}
	var body connectBody
	if !decodeJSON(w, r, &body) {
		return
	}
	if !body.Confirm {
		writeErrorReason(w, http.StatusBadRequest, CodeInvalidArgument, "invalid_argument",
			"confirm:true is required", nil)
		return
	}
	body.ServerID = strings.TrimSpace(body.ServerID)
	if body.ServerID == "" {
		writeErrorReason(w, http.StatusBadRequest, CodeInvalidArgument, "invalid_argument",
			"serverId is required", nil)
		return
	}
	if s.cfg.Catalog == nil {
		writeErrorReason(w, http.StatusConflict, CodeConflict, runtime.ReasonLoginDisabled,
			"account login is disabled (AJS_ACCOUNT_LOGIN=0)", nil)
		return
	}
	result, err := s.cfg.Catalog.ConnectServer(r.Context(), id, body.ServerID, body.SnapshotID, "manual")
	if err != nil {
		s.writeCatalogError(w, withSwitchResult(err, result))
		return
	}
	s.observeConnect(result)
	writeJSON(w, http.StatusOK, result)
}

// handleDisconnectAccount serves POST /v1/accounts/{id}/actions/disconnect.
func (s *Server) handleDisconnectAccount(w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	if _, ok := s.store.Account(id); !ok {
		writeError(w, http.StatusNotFound, CodeNotFound, "account not found")
		return
	}
	var body confirmBody
	if !decodeJSON(w, r, &body) {
		return
	}
	if !body.Confirm {
		writeErrorReason(w, http.StatusBadRequest, CodeInvalidArgument, "invalid_argument",
			"confirm:true is required", nil)
		return
	}
	if s.cfg.Catalog == nil {
		writeErrorReason(w, http.StatusConflict, CodeConflict, runtime.ReasonLoginDisabled,
			"account login is disabled (AJS_ACCOUNT_LOGIN=0)", nil)
		return
	}
	result, err := s.cfg.Catalog.DisconnectAccount(r.Context(), id)
	if err != nil {
		s.writeCatalogError(w, err)
		return
	}
	s.observeDisconnect(result)
	writeJSON(w, http.StatusOK, result)
}

// handleReconnectAccount serves POST /v1/accounts/{id}/actions/reconnect.
func (s *Server) handleReconnectAccount(w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	if _, ok := s.store.Account(id); !ok {
		writeError(w, http.StatusNotFound, CodeNotFound, "account not found")
		return
	}
	var body confirmBody
	if !decodeJSON(w, r, &body) {
		return
	}
	if !body.Confirm {
		writeErrorReason(w, http.StatusBadRequest, CodeInvalidArgument, "invalid_argument",
			"confirm:true is required", nil)
		return
	}
	if s.cfg.Catalog == nil {
		writeErrorReason(w, http.StatusConflict, CodeConflict, runtime.ReasonLoginDisabled,
			"account login is disabled (AJS_ACCOUNT_LOGIN=0)", nil)
		return
	}
	result, err := s.cfg.Catalog.ReconnectAccount(r.Context(), id, body.SnapshotID, "reconnect")
	if err != nil {
		s.writeCatalogError(w, withSwitchResult(err, result))
		return
	}
	s.observeConnect(result)
	writeJSON(w, http.StatusOK, result)
}

// selectionView enriches a stored target with the vendor title. A stale
// catalog leaves the title empty rather than inventing one.
func (s *Server) selectionView(selection model.Selection) model.CatalogSelection {
	view := model.CatalogSelection{
		AccountID: selection.AccountID,
		ServerID:  selection.ServerID,
		UpdatedAt: selection.UpdatedAt,
		UpdatedBy: selection.UpdatedBy,
	}
	if s.cfg.Catalog != nil {
		lookup := s.cfg.Catalog.LookupServer(selection.AccountID, selection.ServerID)
		if lookup.Found {
			view.Title = lookup.Title
		}
	}
	return view
}

// checkSnapshot rejects a write whose snapshot id no longer matches.
func (s *Server) checkSnapshot(snapshotID string) error {
	snapshotID = strings.TrimSpace(snapshotID)
	if snapshotID == "" {
		return nil
	}
	current := s.cfg.Catalog.SnapshotID()
	if snapshotID != current {
		return &runtime.SwitchError{
			Reason: runtime.ReasonSnapshotChanged, Message: "catalog changed since it was read",
			Details: map[string]string{"snapshotId": current},
		}
	}
	return nil
}

// withSwitchResult folds the failed switch's warnings and slot/generation
// attribution into the error details. The runtime already annotates its own
// SwitchError; this keeps a custom Catalog implementation from silently
// dropping the same fields, because the handler discards the result on error.
func withSwitchResult(err error, result runtime.SwitchResult) error {
	var switchErr *runtime.SwitchError
	if !errors.As(err, &switchErr) {
		return err
	}
	if len(result.Warnings) == 0 && result.Slot == 0 && result.Generation == 0 && result.PrevNodeID == "" {
		return err
	}
	details := make(map[string]string, len(switchErr.Details)+5)
	for key, value := range switchErr.Details {
		details[key] = value
	}
	if len(result.Warnings) > 0 {
		details["warnings"] = strings.Join(result.Warnings, ",")
	}
	if result.Slot > 0 {
		details["slot"] = strconv.Itoa(result.Slot)
	}
	if result.Generation > 0 {
		details["generation"] = strconv.FormatUint(result.Generation, 10)
	}
	if result.PrevNodeID != "" {
		details["prevNodeId"] = result.PrevNodeID
	}
	return &runtime.SwitchError{
		Reason:     switchErr.Reason,
		Message:    switchErr.Message,
		Details:    details,
		RetryAfter: switchErr.RetryAfter,
		Cause:      switchErr.Cause,
	}
}

// writeCatalogError maps the guarded-switch reasons onto the HTTP contract of
// DESIGN-CATALOG §3.10. The client-safe message is preserved; the internal
// cause stays in the log.
func (s *Server) writeCatalogError(w http.ResponseWriter, err error) {
	var switchErr *runtime.SwitchError
	if errors.As(err, &switchErr) {
		switch switchErr.Reason {
		case runtime.ReasonUnknownServer, runtime.ReasonNoTarget:
			writeErrorReason(w, http.StatusNotFound, CodeNotFound, switchErr.Reason, switchErr.Message, switchErr.Details)
		case runtime.ReasonSnapshotChanged, runtime.ReasonServerIneligible, runtime.ReasonSwitchInProgress,
			runtime.ReasonSlotMultipleSessions, runtime.ReasonPortUnavailable,
			runtime.ReasonLoginRequired, runtime.ReasonLoginDisabled,
			runtime.ReasonAccountState:
			writeErrorReason(w, http.StatusConflict, CodeConflict, switchErr.Reason, switchErr.Message, switchErr.Details)
		case runtime.ReasonConnectCooldown:
			retry := int(math.Ceil(switchErr.RetryAfter.Seconds()))
			if retry < 1 {
				retry = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(retry))
			writeErrorReason(w, http.StatusTooManyRequests, CodeRateLimited, switchErr.Reason, switchErr.Message, switchErr.Details)
		case runtime.ReasonSessionOpen, runtime.ReasonGateStart, runtime.ReasonSessionUnverified:
			s.logger.Warn("catalog_connect_failed", "reason", switchErr.Reason, "error", switchErr.Cause)
			writeErrorReason(w, http.StatusBadGateway, CodeUpstreamUnavailable, switchErr.Reason, switchErr.Message, switchErr.Details)
		case runtime.ReasonStoreFailed:
			s.logger.Error("catalog_store_failed", "error", switchErr.Cause)
			writeErrorReason(w, http.StatusInternalServerError, CodeInternal, switchErr.Reason, switchErr.Message, switchErr.Details)
		default:
			s.logger.Error("catalog_switch_error", "reason", switchErr.Reason, "error", switchErr.Cause)
			writeError(w, http.StatusInternalServerError, CodeInternal, "internal error")
		}
		return
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, CodeNotFound, err.Error())
	case errors.Is(err, runtime.ErrControllerStopped):
		writeError(w, http.StatusServiceUnavailable, CodeUpstreamUnavailable, "node controller is stopped")
	default:
		s.logger.Error("catalog_error", "error", err)
		writeError(w, http.StatusInternalServerError, CodeInternal, "internal error")
	}
}

// writeErrorReason is writeError plus the optional reason/details block.
func writeErrorReason(w http.ResponseWriter, status int, code, reason, message string, details map[string]string) {
	writeJSON(w, status, errorResponse{Error: errorDetail{
		Code:    code,
		Message: message,
		Reason:  reason,
		Details: details,
	}})
}
