package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"ajsaccountd/internal/model"
)

type healthResponse struct {
	Status   string         `json:"status"`
	Service  string         `json:"service"`
	Version  string         `json:"version"`
	Time     string         `json:"time"`
	Accounts map[string]int `json:"accounts"`
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	counts := map[string]int{
		string(model.AccountActive):     0,
		string(model.AccountPaused):     0,
		string(model.AccountAuthFailed): 0,
		string(model.AccountExpired):    0,
	}
	for _, account := range s.store.Accounts() {
		counts[string(account.State)]++
	}
	writeJSON(w, http.StatusOK, healthResponse{
		Status:   "ok",
		Service:  "ajs-accountd",
		Version:  s.cfg.Version,
		Time:     time.Now().UTC().Format(time.RFC3339Nano),
		Accounts: counts,
	})
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	window := model.MetricRange(r.URL.Query().Get("range"))
	if window == "" {
		window = model.Range24h
	}
	metrics, err := s.store.Metrics(window)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, metrics)
}

func (s *Server) handleListRequests(w http.ResponseWriter, r *http.Request) {
	requests := s.store.Requests()
	start, end, ok := paginate(w, r, len(requests))
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, requests[start:end])
}

func (s *Server) handleCreateMutation(w http.ResponseWriter, r *http.Request) {
	var input model.MutationLabInput
	if !decodeJSON(w, r, &input) {
		return
	}
	entry, err := s.store.CreateMutation(input)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, entry)
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			writeError(w, http.StatusBadRequest, CodeInvalidArgument, "limit must be a non-negative integer")
			return
		}
		limit = parsed
	}
	if limit > 500 {
		limit = 500
	}
	writeJSON(w, http.StatusOK, s.store.Audit(limit))
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.store.Settings())
}

func (s *Server) handlePatchSettings(w http.ResponseWriter, r *http.Request) {
	var raw map[string]json.RawMessage
	if !decodeJSON(w, r, &raw) {
		return
	}
	allowed := map[string]struct{}{
		"transport":              {},
		"healthIntervalSec":      {},
		"leaseTtl":               {},
		"subscriptionRefreshSec": {},
		"theme":                  {},
	}
	for key := range raw {
		if _, ok := allowed[key]; !ok {
			writeError(w, http.StatusBadRequest, CodeInvalidArgument, "unknown settings field "+strconv.Quote(key))
			return
		}
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "invalid settings body")
		return
	}
	var patch model.SettingsPatch
	if err := json.Unmarshal(encoded, &patch); err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "invalid settings value: "+err.Error())
		return
	}
	settings, err := s.store.UpdateSettings(patch)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (s *Server) handleTopology(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.store.Topology())
}
