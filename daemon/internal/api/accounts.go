package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"ajsaccountd/internal/account"
	"ajsaccountd/internal/model"
	"ajsaccountd/internal/store"
	"ajsaccountd/internal/webhook"
)

// withEffectiveSlots fills the derived display capacity of one account for a
// response. UseCount is persisted; EffectiveSlots is recomputed on every read
// so an AJS_ACCOUNT_SLOTS change applies without a new login. Lease admission
// counts verified, listener-bound serving slots separately and is not
// represented by this field.
func withEffectiveSlots(acct model.Account) model.Account {
	acct.EffectiveSlots = account.EffectiveSlots(acct.UseCount, account.ConfiguredAccountSlots())
	return acct
}

func (s *Server) handleListAccounts(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	accounts := s.filterAccountsForRequest(r, s.store.Accounts())
	filtered := make([]model.Account, 0, len(accounts))
	for _, account := range accounts {
		if state != "" && string(account.State) != state {
			continue
		}
		filtered = append(filtered, withEffectiveSlots(account))
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
	writeJSON(w, http.StatusOK, withEffectiveSlots(account))
}

func (s *Server) handleCreateAccount(w http.ResponseWriter, r *http.Request) {
	var input model.CreateAccountInput
	if !decodeJSON(w, r, &input) {
		return
	}
	account, err := s.store.CreateAccount(input, s.auditActor(r))
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, withEffectiveSlots(account))
}

func (s *Server) handlePatchAccount(w http.ResponseWriter, r *http.Request) {
	// The budget is decoded as raw JSON so an explicit null (clear it) is
	// distinguishable from an absent field (leave it alone).
	var body struct {
		Label              *string             `json:"label"`
		Username           *string             `json:"username"`
		Platform           *model.Platform     `json:"platform"`
		State              *model.AccountState `json:"state"`
		Concurrency        *int                `json:"concurrency"`
		Regions            *[]string           `json:"regions"`
		ExpiresAt          *string             `json:"expiresAt"`
		TrafficBudgetBytes json.RawMessage     `json:"trafficBudgetBytes"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	patch := model.AccountPatch{
		Label:       body.Label,
		Username:    body.Username,
		Platform:    body.Platform,
		State:       body.State,
		Concurrency: body.Concurrency,
		Regions:     body.Regions,
		ExpiresAt:   body.ExpiresAt,
	}
	if len(body.TrafficBudgetBytes) > 0 {
		patch.TrafficBudgetSet = true
		if !bytes.Equal(bytes.TrimSpace(body.TrafficBudgetBytes), []byte("null")) {
			var budget int64
			if err := json.Unmarshal(body.TrafficBudgetBytes, &budget); err != nil || budget < 0 {
				writeError(w, http.StatusBadRequest, CodeInvalidArgument,
					"trafficBudgetBytes must be a non-negative integer or null")
				return
			}
			patch.TrafficBudgetBytes = &budget
		}
	}
	account, err := s.store.UpdateAccount(pathParam(r, "id"), patch, s.auditActor(r))
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, withEffectiveSlots(account))
}

func (s *Server) handleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	if _, ok := s.store.Account(id); !ok {
		writeError(w, http.StatusNotFound, CodeNotFound, fmt.Sprintf("account %q not found", id))
		return
	}
	// The store cascade removes the node rows, so the sampler keys must be
	// captured before the mutation.
	nodeIDs := s.accountNodeIDs(id)
	release, err := s.stopAccountForMutation(r.Context(), id, s.cfg.AccountDeleteHook)
	if err != nil {
		if release != nil {
			release()
		}
		// A hook failure is a runtime failure, not a store contract error, so
		// it answers 500 instead of the mapped store status. The detail stays
		// in the log; the account and its nodes are untouched.
		s.logger.Error("account_delete_hook_error", "account", id, "error", err)
		writeError(w, http.StatusInternalServerError, CodeInternal, "internal error")
		return
	}
	defer release()
	if err := s.store.DeleteAccount(id, s.auditActor(r)); err != nil {
		s.writeStoreError(w, err)
		return
	}
	if s.cfg.LoginService != nil {
		s.cfg.LoginService.Forget(id)
	}
	for _, nodeID := range nodeIDs {
		s.unwatchHealth(nodeID)
	}
	// The hook only stopped gates; the nodes were still LIVE when it ran, so
	// the reconcile that drops the account's tags must happen after the store
	// cascade removed them.
	s.reconcileAccountMutation(r.Context(), id, "delete")
	w.WriteHeader(http.StatusNoContent)
}

// AccountStopClaimer is the optional account-wide stop claim *runtime.NodeController
// implements. When the catalog provides it, the logout and delete handlers hold
// the claim across their store mutation and release it afterwards. A SyncAccount
// for the account waits for the claim and then re-checks the committed state,
// so a login that lands between the fence and the pause/delete commit cannot
// install a gate, session or listener behind it.
type AccountStopClaimer interface {
	// BeginAccountStop closes the account's gates and fences it, and returns
	// the release that ends the claim. The release is non-nil whenever the
	// claim was taken, even when closing a gate failed; the caller must call
	// it once the mutation (or its abort) is done.
	BeginAccountStop(ctx context.Context, accountID string) (release func(), err error)
}

// stopAccountForMutation runs the pre-mutation stop for logout/delete. The
// runtime controller's claim is preferred because it stays held until the
// returned release runs, which is what covers the store mutation. A plain hook
// (tests or a custom wiring) is called and returns a no-op release.
func (s *Server) stopAccountForMutation(ctx context.Context, accountID string, hook func(context.Context, string) error) (func(), error) {
	if claimer, ok := s.cfg.Catalog.(AccountStopClaimer); ok {
		return claimer.BeginAccountStop(ctx, accountID)
	}
	if hook == nil {
		return func() {}, nil
	}
	return func() {}, hook(ctx, accountID)
}

func (s *Server) handleLoginAccount(w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	if s.cfg.LoginService == nil {
		updated, err := s.store.LoginAccount(id, s.auditActor(r))
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		s.emit(webhook.EventAccountLogin, map[string]any{
			"accountId": updated.ID,
			"label":     updated.Label,
			"state":     string(updated.State),
		})
		writeJSON(w, http.StatusOK, withEffectiveSlots(updated))
		return
	}
	updated, err := s.cfg.LoginService.Login(r.Context(), id)
	if err != nil {
		s.writeLoginError(w, err)
		return
	}
	s.runLoginHook(r.Context(), id)
	// The login hook opens one gate per usable server, outside the connect
	// handler; register those gates with the sampler now.
	s.watchAccountHealth(id)
	s.emit(webhook.EventAccountLogin, map[string]any{
		"accountId": updated.ID,
		"label":     updated.Label,
		"state":     string(updated.State),
	})
	writeJSON(w, http.StatusOK, withEffectiveSlots(updated))
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
	release, err := s.stopAccountForMutation(r.Context(), id, s.cfg.AccountLogoutHook)
	if err != nil {
		if release != nil {
			release()
		}
		// A hook failure is a runtime failure, not a store contract error, so
		// it answers 500 instead of the mapped store status. The detail stays
		// in the log; the account and its nodes are untouched.
		s.logger.Error("account_logout_hook_error", "account", id, "error", err)
		writeError(w, http.StatusInternalServerError, CodeInternal, "internal error")
		return
	}
	defer release()
	account, err := s.store.LogoutAccount(id, s.auditActor(r))
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if s.cfg.LoginService != nil {
		s.cfg.LoginService.Forget(id)
	}
	// store.LogoutAccount paused the account and drained its nodes; publish
	// that state now, because the pre-mutation hook did not reconcile.
	s.reconcileAccountMutation(r.Context(), id, "logout")
	// The drained nodes no longer serve traffic; stop probing them.
	s.unwatchAccountHealth(id)
	s.emit(webhook.EventAccountLogout, map[string]any{
		"accountId": account.ID,
		"label":     account.Label,
		"state":     string(account.State),
	})
	writeJSON(w, http.StatusOK, withEffectiveSlots(account))
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
