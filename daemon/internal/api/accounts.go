package api

import (
	"net/http"

	"ajsaccountd/internal/model"
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
	account, err := s.store.LoginAccount(pathParam(r, "id"))
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, account)
}

func (s *Server) handleLogoutAccount(w http.ResponseWriter, r *http.Request) {
	account, err := s.store.LogoutAccount(pathParam(r, "id"))
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, account)
}
