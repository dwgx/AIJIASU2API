package api

import (
	"bytes"
	"encoding/json"
	"net/http"

	"ajsaccountd/internal/model"
)

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users := s.store.Users()
	start, end, ok := paginate(w, r, len(users))
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, users[start:end])
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var input model.CreateUserInput
	if !decodeJSON(w, r, &input) {
		return
	}
	user, err := s.store.CreateUser(input, s.auditActor(r))
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, user)
}

func (s *Server) handlePatchUser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Platform        *string         `json:"platform"`
		StickyTTL       *string         `json:"stickyTtl"`
		QuotaBytesMonth json.RawMessage `json:"quotaBytesMonth"`
		Allowlist       *[]string       `json:"allowlist"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	patch := model.UserPatch{
		Platform:  body.Platform,
		StickyTTL: body.StickyTTL,
		Allowlist: body.Allowlist,
	}
	if len(body.QuotaBytesMonth) > 0 {
		patch.QuotaSet = true
		if !bytes.Equal(bytes.TrimSpace(body.QuotaBytesMonth), []byte("null")) {
			var quota int64
			if err := json.Unmarshal(body.QuotaBytesMonth, &quota); err != nil || quota < 0 {
				writeError(w, http.StatusBadRequest, CodeInvalidArgument, "quotaBytesMonth must be a non-negative integer or null")
				return
			}
			patch.QuotaBytesMonth = &quota
		}
	}
	user, err := s.store.UpdateUser(pathParam(r, "id"), patch, s.auditActor(r))
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, user)
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteUser(pathParam(r, "id"), s.auditActor(r)); err != nil {
		s.writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSuspendUser(w http.ResponseWriter, r *http.Request) {
	user, err := s.store.SetUserEnabled(pathParam(r, "id"), false, s.auditActor(r))
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, user)
}

func (s *Server) handleResumeUser(w http.ResponseWriter, r *http.Request) {
	user, err := s.store.SetUserEnabled(pathParam(r, "id"), true, s.auditActor(r))
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, user)
}

func (s *Server) handleAllowlistUser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Allowlist *[]string `json:"allowlist"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Allowlist == nil {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "allowlist is required")
		return
	}
	user, err := s.store.SetUserAllowlist(pathParam(r, "id"), *body.Allowlist, s.auditActor(r))
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, user)
}

func (s *Server) handleListKeys(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		userID = r.URL.Query().Get("userId")
	}
	keys := s.store.Keys()
	filtered := make([]model.APIKey, 0, len(keys))
	for _, key := range keys {
		if userID != "" && key.UserID != userID {
			continue
		}
		filtered = append(filtered, key)
	}
	start, end, ok := paginate(w, r, len(filtered))
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, filtered[start:end])
}

func (s *Server) handleCreateKey(w http.ResponseWriter, r *http.Request) {
	var input model.CreateKeyInput
	if !decodeJSON(w, r, &input) {
		return
	}
	key, secret, err := s.store.CreateKey(input.UserID, input.Label, s.auditActor(r))
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, model.CreateKeyResult{Key: key, Secret: secret})
}

func (s *Server) handleDeleteKey(w http.ResponseWriter, r *http.Request) {
	if err := s.store.RevokeKey(pathParam(r, "id"), s.auditActor(r)); err != nil {
		s.writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
