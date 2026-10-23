package api

import (
	"context"
	"errors"
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
	if err := s.store.DeleteAccount(pathParam(r, "id")); err != nil {
		s.writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
	account, err := s.store.LogoutAccount(pathParam(r, "id"))
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, account)
}
