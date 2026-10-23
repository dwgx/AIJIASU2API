package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"ajsaccountd/internal/account"
	"ajsaccountd/internal/model"
	"ajsaccountd/internal/store"
)

func (s *Server) handleListAccounts(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	accounts := s.store.Accounts()
	filtered := make([]model.Account, 0, len(accounts))
	for _, account := range accounts {
		if state != "" && string(account.State) != state {
			continue
		}
		filtered = append(filtered, account)
	}
	start, end, ok := paginate(w, r, len(filtered))
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, filtered[start:end])
}

func (s *Server) handleGetAccount(w http.ResponseWriter, r *http.Request) {
	account, ok := s.store.Account(pathParam(r, "id"))
	if !ok {
		writeError(w, http.StatusNotFound, CodeNotFound, "account not found")
		return
	}
	writeJSON(w, http.StatusOK, account)
}

func (s *Server) handleCreateAccount(w http.ResponseWriter, r *http.Request) {
	var input model.CreateAccountInput
	if !decodeJSON(w, r, &input) {
		return
	}
	account, err := s.store.CreateAccount(input)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, account)
}

func (s *Server) handlePatchAccount(w http.ResponseWriter, r *http.Request) {
	var patch model.AccountPatch
	if !decodeJSON(w, r, &patch) {
		return
	}
	account, err := s.store.UpdateAccount(pathParam(r, "id"), patch)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, account)
}

func (s *Server) handleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	if _, ok := s.store.Account(id); !ok {
		writeError(w, http.StatusNotFound, CodeNotFound, fmt.Sprintf("account %q not found", id))
		return
	}
	if err := s.runAccountDeleteHook(r.Context(), id); err != nil {
		// A hook failure is a runtime failure, not a store contract error, so
		// it answers 500 instead of the mapped store status. The detail stays
		// in the log; the account and its nodes are untouched.
		s.logger.Error("account_delete_hook_error", "account", id, "error", err)
		writeError(w, http.StatusInternalServerError, CodeInternal, "internal error")
		return
	}
	if err := s.store.DeleteAccount(id); err != nil {
		s.writeStoreError(w, err)
		return
	}
	// The hook only stopped gates; the nodes were still LIVE when it ran, so
	// the reconcile that drops the account's tags must happen after the store
	// cascade removed them.
	s.reconcileAccountMutation(r.Context(), id, "delete")
	w.WriteHeader(http.StatusNoContent)
}

// runAccountDeleteHook stops the account's runtime gates before the store
// cascade removes its nodes. Unlike the login hook its error is returned: the
// caller aborts the delete instead of orphaning a listening gate. A nil hook
// keeps the store-only behaviour of a daemon without AJS_ACCOUNT_LOGIN=1.
func (s *Server) runAccountDeleteHook(ctx context.Context, accountID string) error {
	if s.cfg.AccountDeleteHook == nil {
		return nil
	}
	return s.cfg.AccountDeleteHook(ctx, accountID)
}

func (s *Server) handleLoginAccount(w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	if s.cfg.LoginService == nil {
		updated, err := s.store.LoginAccount(id)
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, updated)
		return
	}
	updated, err := s.cfg.LoginService.Login(r.Context(), id)
	if err != nil {
		s.writeLoginError(w, err)
		return
	}
	s.runLoginHook(r.Context(), id)
	writeJSON(w, http.StatusOK, updated)
}

// runLoginHook hands a successful login to the node controller. The login is
// already recorded and the response is already earned, so a hook failure is
// logged and swallowed: it must not turn a successful login into an error.
func (s *Server) runLoginHook(ctx context.Context, accountID string) {
	if s.cfg.LoginHook == nil {
		return
	}
	if err := s.cfg.LoginHook(ctx, accountID); err != nil {
		s.logger.Warn("account_login_hook_error", "account", accountID, "error", err)
	}
}

// writeLoginError maps the account login errors onto the dashboard contract.
// A rejected credential is 401, a missing sealed password is 409, and a
// transport/protocol failure is 503 with the detail kept in the log.
func (s *Server) writeLoginError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, account.ErrNoSecret):
		writeError(w, http.StatusConflict, CodeConflict, err.Error())
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, CodeNotFound, err.Error())
	case errors.Is(err, account.ErrAuthRejected):
		writeError(w, http.StatusUnauthorized, CodeUnauthorized, err.Error())
	case errors.Is(err, account.ErrUpstream):
		s.logger.Warn("account_login_upstream_error", "error", err)
		writeError(w, http.StatusServiceUnavailable, CodeUpstreamUnavailable, "account login upstream unavailable")
	case errors.Is(err, account.ErrDisabled), errors.Is(err, account.ErrClosed):
		writeError(w, http.StatusServiceUnavailable, CodeUpstreamUnavailable, "account login is not available")
	default:
		s.logger.Error("account_login_error", "error", err)
		writeError(w, http.StatusInternalServerError, CodeInternal, "internal error")
	}
}

func (s *Server) handleLogoutAccount(w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	if _, ok := s.store.Account(id); !ok {
		writeError(w, http.StatusNotFound, CodeNotFound, fmt.Sprintf("account %q not found", id))
		return
	}
	if err := s.runAccountLogoutHook(r.Context(), id); err != nil {
		// A hook failure is a runtime failure, not a store contract error, so
		// it answers 500 instead of the mapped store status. The detail stays
		// in the log; the account and its nodes are untouched.
		s.logger.Error("account_logout_hook_error", "account", id, "error", err)
		writeError(w, http.StatusInternalServerError, CodeInternal, "internal error")
		return
	}
	account, err := s.store.LogoutAccount(id)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	// store.LogoutAccount paused the account and drained its nodes; publish
	// that state now, because the pre-mutation hook did not reconcile.
	s.reconcileAccountMutation(r.Context(), id, "logout")
	writeJSON(w, http.StatusOK, account)
}

// runAccountLogoutHook stops the account's runtime gates before the store
// pauses the account and drains its nodes. Unlike the login hook its error is
// returned: the caller aborts the logout instead of leaving a listening gate
// behind a paused account. A nil hook keeps the store-only behaviour of a
// daemon without AJS_ACCOUNT_LOGIN=1.
func (s *Server) runAccountLogoutHook(ctx context.Context, accountID string) error {
	if s.cfg.AccountLogoutHook == nil {
		return nil
	}
	return s.cfg.AccountLogoutHook(ctx, accountID)
}

// reconcileAccountMutation republishes the panel after store.DeleteAccount or
// store.LogoutAccount committed. The pre-mutation hooks only stop gates: the
// account's nodes are still LIVE at that moment, so a reconcile there would
// re-advertise gates that no longer accept flows. This runs once the store has
// removed or drained the nodes, which is what removes their Resin tags.
//
// It is best-effort: the mutation already happened and the response is earned,
// so a panel failure is logged instead of turning a successful mutation into
// an error. Without a panel there is nothing to publish.
func (s *Server) reconcileAccountMutation(ctx context.Context, accountID, action string) {
	if s.cfg.Panel == nil {
		return
	}
	if _, err := s.cfg.Panel.Reconcile(ctx); err != nil {
		s.logger.Warn("account_reconcile_failed", "account", accountID, "action", action, "error", err)
	}
}
