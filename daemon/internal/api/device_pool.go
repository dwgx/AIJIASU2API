// Device pool HTTP handlers (DESIGN-DEVICE-POOL §8.1/§8.2).
//
// The handlers only validate request shapes, translate store errors into the
// stable reason envelope and project records through deviceRecordView, which
// has no raw-identity fields: only sha256 hashes, public claims and lifecycle
// metadata leave the daemon.
package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/store"
)

// deviceRecordView is the §8.2 pool view. machineCode and identity are
// deliberately absent; Hashes carries the sha256 prefixes instead.
type deviceRecordView struct {
	ID              string            `json:"id"`
	Label           string            `json:"label"`
	Notes           string            `json:"notes"`
	State           store.DeviceState `json:"state"`
	Platform        model.Platform    `json:"platform"`
	Profile         string            `json:"profile"`
	Algorithm       string            `json:"algorithm"`
	Fingerprint     string            `json:"fingerprint"`
	Hashes          map[string]string `json:"hashes"`
	Claims          map[string]string `json:"claims"`
	AccountID       string            `json:"accountId,omitempty"`
	AccountLabel    string            `json:"accountLabel,omitempty"`
	AccountKey      string            `json:"accountKey,omitempty"`
	CreatedAt       string            `json:"createdAt"`
	AssignedAt      *string           `json:"assignedAt"`
	PresentedAt     *string           `json:"presentedAt"`
	FirstSeenAt     *string           `json:"firstSeenAt"`
	LastSeenAt      *string           `json:"lastSeenAt"`
	LoginCount      int               `json:"loginCount"`
	RetiredAt       *string           `json:"retiredAt"`
	RetiredReason   string            `json:"retiredReason"`
	MaterialPresent bool              `json:"materialPresent"`
	Warnings        []string          `json:"warnings"`
}

// devicePoolView is the §8.2 list envelope. Reason is only set when the pool
// is disabled, so a client can render the hint without a second request.
type devicePoolView struct {
	Enabled  bool               `json:"enabled"`
	Reason   string             `json:"reason,omitempty"`
	Total    int                `json:"total"`
	Counts   map[string]int     `json:"counts"`
	Devices  []deviceRecordView `json:"devices"`
	Warnings []string           `json:"warnings,omitempty"`
}

type deviceGenerateRequest struct {
	Count       int    `json:"count"`
	Platform    string `json:"platform"`
	Profile     string `json:"profile"`
	Algorithm   string `json:"algorithm"`
	LabelPrefix string `json:"labelPrefix"`
}

type deviceGenerateResponse struct {
	Devices []deviceRecordView `json:"devices"`
}

type devicePatchRequest struct {
	Label *string `json:"label,omitempty"`
	Notes *string `json:"notes,omitempty"`
}

type deviceAssignRequest struct {
	AccountID string `json:"accountId"`
}

type deviceConfirmRequest struct {
	Confirm string `json:"confirm"`
}

type deviceReasonConfirmRequest struct {
	Reason  string `json:"reason,omitempty"`
	Confirm string `json:"confirm"`
}

type deviceExportRequest struct {
	Passphrase         string   `json:"passphrase"`
	Scope              string   `json:"scope,omitempty"`
	DeviceIDs          []string `json:"deviceIds,omitempty"`
	IncludeAccountKeys *bool    `json:"includeAccountKeys,omitempty"`
	IncludeLabels      *bool    `json:"includeLabels,omitempty"`
	Confirm            string   `json:"confirm,omitempty"`
}

type deviceImportRequest struct {
	Passphrase string          `json:"passphrase"`
	Envelope   json.RawMessage `json:"envelope"`
	DryRun     bool            `json:"dryRun"`
	Bind       bool            `json:"bind"`
	Confirm    string          `json:"confirm,omitempty"`
}

// deviceImportConflictResponse is the §8.4 hard-conflict body: the standard
// error envelope plus the per-record conflicts. No record was mutated.
type deviceImportConflictResponse struct {
	Error     errorDetail                  `json:"error"`
	Conflicts []store.DeviceImportConflict `json:"conflicts"`
}

// shortDeviceHash truncates a sha256 value to the 16-hex display convention
// used by the rest of the panel. Values that are not sha256 hashes pass
// through unchanged.
func shortDeviceHash(value string) string {
	const prefix = "sha256:"
	if strings.HasPrefix(value, prefix) && len(value) > len(prefix)+16 {
		return value[:len(prefix)+16]
	}
	return value
}

// deviceView projects one store record. It never reads sealed material: only
// the presence bit and the already-hashed index fields are exposed.
func (s *Server) deviceView(rec store.DeviceRecord) deviceRecordView {
	hashes := rec.Hashes
	if hashes == nil {
		hashes = map[string]string{}
	}
	claims := rec.Claims
	if claims == nil {
		claims = map[string]string{}
	}
	present := s.store.DeviceMaterialPresent(rec.ID)
	warnings := make([]string, 0, 1)
	if !present {
		warnings = append(warnings, "sealed material is missing; restore it from an export")
	}
	accountLabel := ""
	if rec.AssignedAccountID != "" {
		accountLabel = s.store.DeviceAccountLabel(rec.AssignedAccountID)
	}
	return deviceRecordView{
		ID:              rec.ID,
		Label:           rec.Label,
		Notes:           rec.Notes,
		State:           rec.State,
		Platform:        rec.Platform,
		Profile:         rec.Profile,
		Algorithm:       rec.Algorithm,
		Fingerprint:     rec.Fingerprint,
		Hashes:          hashes,
		Claims:          claims,
		AccountID:       rec.AssignedAccountID,
		AccountLabel:    accountLabel,
		AccountKey:      shortDeviceHash(rec.AccountKey),
		CreatedAt:       rec.CreatedAt,
		AssignedAt:      rec.AssignedAt,
		PresentedAt:     rec.PresentedAt,
		FirstSeenAt:     rec.FirstSeenAt,
		LastSeenAt:      rec.LastSeenAt,
		LoginCount:      rec.LoginCount,
		RetiredAt:       rec.RetiredAt,
		RetiredReason:   rec.RetiredReason,
		MaterialPresent: present,
		Warnings:        warnings,
	}
}

func (s *Server) deviceViews(records []store.DeviceRecord) []deviceRecordView {
	views := make([]deviceRecordView, 0, len(records))
	for _, rec := range records {
		views = append(views, s.deviceView(rec))
	}
	return views
}

// deviceNotFound writes the standard 404 with the stable reason.
func deviceNotFound(w http.ResponseWriter, id string) {
	writeErrorReason(w, http.StatusNotFound, CodeNotFound, store.DeviceReasonNotFound,
		"device "+strconv.Quote(id)+" not found", nil)
}

// requireDeviceConfirm enforces an exact confirm token before a destructive
// lifecycle action. A missing or different token is a 400, never a silent
// no-op.
func requireDeviceConfirm(w http.ResponseWriter, got, want string) bool {
	if got != want {
		writeErrorReason(w, http.StatusBadRequest, CodeInvalidArgument,
			store.DeviceReasonConfirmRequired, `confirm must be "`+want+`"`, nil)
		return false
	}
	return true
}

// writeDeviceStoreError maps a device-pool store failure onto the HTTP
// contract. Import conflicts keep their per-record detail; every other
// failure is the standard envelope with the store's stable reason.
func (s *Server) writeDeviceStoreError(w http.ResponseWriter, err error) {
	var conflicts *store.DeviceImportConflictError
	if errors.As(err, &conflicts) {
		writeJSON(w, http.StatusConflict, deviceImportConflictResponse{
			Error: errorDetail{
				Code: CodeConflict, Reason: store.DeviceReasonImportConflict,
				Message: err.Error(),
			},
			Conflicts: conflicts.Conflicts,
		})
		return
	}
	switch {
	case errors.Is(err, store.ErrInvalid):
		writeErrorReason(w, http.StatusBadRequest, CodeInvalidArgument, store.DeviceErrorReason(err), err.Error(), nil)
	case errors.Is(err, store.ErrNotFound):
		writeErrorReason(w, http.StatusNotFound, CodeNotFound, store.DeviceErrorReason(err), err.Error(), nil)
	case errors.Is(err, store.ErrConflict):
		writeErrorReason(w, http.StatusConflict, CodeConflict, store.DeviceErrorReason(err), err.Error(), nil)
	default:
		s.logger.Error("device_store_error", "error", err)
		writeError(w, http.StatusInternalServerError, CodeInternal, "internal error")
	}
}

// parseDevicePoolQuery reads the §8.1 filters. Store-side clamping keeps the
// page at 50 by default and 200 at most.
func parseDevicePoolQuery(w http.ResponseWriter, r *http.Request) (store.DevicePoolQuery, bool) {
	query := r.URL.Query()
	q := store.DevicePoolQuery{
		State:     store.DeviceState(query.Get("state")),
		Platform:  model.Platform(query.Get("platform")),
		Profile:   query.Get("profile"),
		AccountID: query.Get("accountId"),
		Query:     query.Get("q"),
		Limit:     50,
	}
	if raw := query.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			writeError(w, http.StatusBadRequest, CodeInvalidArgument, "limit must be a non-negative integer")
			return store.DevicePoolQuery{}, false
		}
		q.Limit = parsed
	}
	if raw := query.Get("offset"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			writeError(w, http.StatusBadRequest, CodeInvalidArgument, "offset must be a non-negative integer")
			return store.DevicePoolQuery{}, false
		}
		q.Offset = parsed
	}
	return q, true
}

// handleListDevicePool serves GET /v1/device-pool. A disabled pool is a 200
// with enabled:false and the reason, never an error: the dashboard must be
// able to render the setup hint.
func (s *Server) handleListDevicePool(w http.ResponseWriter, r *http.Request) {
	q, ok := parseDevicePoolQuery(w, r)
	if !ok {
		return
	}
	view := s.store.ListDevicePool(q)
	out := devicePoolView{
		Enabled:  view.Enabled,
		Total:    view.Total,
		Counts:   view.Counts,
		Devices:  s.deviceViews(view.Devices),
		Warnings: view.Warnings,
	}
	if !view.Enabled {
		out.Reason = store.DeviceReasonPoolDisabled
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGenerateDevices(w http.ResponseWriter, r *http.Request) {
	var req deviceGenerateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	records, err := s.store.GenerateDevices(store.DeviceGenerateInput{
		Count:       req.Count,
		Platform:    model.Platform(req.Platform),
		Profile:     req.Profile,
		Algorithm:   req.Algorithm,
		LabelPrefix: req.LabelPrefix,
		Actor:       "operator",
	})
	if err != nil {
		s.writeDeviceStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, deviceGenerateResponse{Devices: s.deviceViews(records)})
}

func (s *Server) handleGetDevicePoolRecord(w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	rec, ok := s.store.DeviceRecord(id)
	if !ok {
		deviceNotFound(w, id)
		return
	}
	writeJSON(w, http.StatusOK, s.deviceView(rec))
}

func (s *Server) handlePatchDevicePoolRecord(w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	var req devicePatchRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	rec, err := s.store.UpdateDevice(id, req.Label, req.Notes)
	if err != nil {
		s.writeDeviceStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.deviceView(rec))
}

func (s *Server) handleAssignDevicePoolRecord(w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	var req deviceAssignRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.AccountID) == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "accountId is required")
		return
	}
	rec, err := s.store.AssignDevice(id, req.AccountID)
	if err != nil {
		s.writeDeviceStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.deviceView(rec))
}

func (s *Server) handleReleaseDevicePoolRecord(w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	var req deviceConfirmRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if !requireDeviceConfirm(w, req.Confirm, "RELEASE") {
		return
	}
	rec, err := s.store.ReleaseDevice(id)
	if err != nil {
		s.writeDeviceStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.deviceView(rec))
}

func (s *Server) handleRetireDevicePoolRecord(w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	var req deviceReasonConfirmRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if !requireDeviceConfirm(w, req.Confirm, "RETIRE") {
		return
	}
	rec, err := s.store.RetireDevice(id, req.Reason)
	if err != nil {
		s.writeDeviceStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.deviceView(rec))
}

func (s *Server) handleQuarantineDevicePoolRecord(w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	var req deviceReasonConfirmRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if !requireDeviceConfirm(w, req.Confirm, "QUARANTINE") {
		return
	}
	rec, err := s.store.QuarantineDevice(id, req.Reason)
	if err != nil {
		s.writeDeviceStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.deviceView(rec))
}

func (s *Server) handlePurgeDevicePoolRecord(w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	var req deviceConfirmRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if !requireDeviceConfirm(w, req.Confirm, "PURGE") {
		return
	}
	if err := s.store.PurgeDevice(id); err != nil {
		s.writeDeviceStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleExportDevicePool returns the passphrase-encrypted envelope. The
// confirm field is accepted for contract compatibility; when supplied it
// must be exactly "EXPORT".
func (s *Server) handleExportDevicePool(w http.ResponseWriter, r *http.Request) {
	var req deviceExportRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Confirm != "" && req.Confirm != "EXPORT" {
		writeErrorReason(w, http.StatusBadRequest, CodeInvalidArgument,
			store.DeviceReasonConfirmRequired, `confirm must be "EXPORT" when supplied`, nil)
		return
	}
	opts := store.DeviceExportOptions{
		Scope:      req.Scope,
		DeviceIDs:  req.DeviceIDs,
		Passphrase: req.Passphrase,
	}
	if req.IncludeAccountKeys != nil {
		opts.ExcludeAccountKeys = !*req.IncludeAccountKeys
	}
	if req.IncludeLabels != nil {
		opts.ExcludeLabels = !*req.IncludeLabels
	}
	envelope, err := s.store.ExportDevicePool(opts)
	if err != nil {
		s.writeDeviceStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, envelope)
}

// handleImportDevicePool decrypts, validates and (unless dryRun) commits an
// envelope. The store owns the all-or-nothing guarantee; this handler only
// enforces the request size and shape.
func (s *Server) handleImportDevicePool(w http.ResponseWriter, r *http.Request) {
	var req deviceImportRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if len(req.Envelope) == 0 || string(req.Envelope) == "null" {
		writeErrorReason(w, http.StatusBadRequest, CodeInvalidArgument,
			store.DeviceReasonImportFormat, "envelope is required", nil)
		return
	}
	result, err := s.store.ImportDevicePool([]byte(req.Envelope), store.DeviceImportOptions{
		Passphrase: req.Passphrase,
		DryRun:     req.DryRun,
		Bind:       req.Bind,
	})
	if err != nil {
		s.writeDeviceStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
