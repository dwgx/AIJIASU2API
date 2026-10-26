// Account import/export and bulk-delete HTTP handlers.
//
// The handlers only validate the strict wrapper, hold the account-IO mutex and
// translate results. Formats, validation and the atomic store apply live in
// internal/store/account_io.go.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/store"
)

const (
	exportFormatEncrypted = "encrypted"
	exportFormatJSON      = "json"
	exportFormatCSV       = "csv"
	plaintextConfirm      = "EXPORT"
)

type exportFilterRequest struct {
	State    string `json:"state,omitempty"`
	Platform string `json:"platform,omitempty"`
	Q        string `json:"q,omitempty"`
}

type exportRequest struct {
	IDs              []string             `json:"ids,omitempty"`
	Filter           *exportFilterRequest `json:"filter,omitempty"`
	Format           string               `json:"format,omitempty"`
	Passphrase       string               `json:"passphrase,omitempty"`
	IncludeDevice    *bool                `json:"includeDevice,omitempty"`
	ConfirmPlaintext string               `json:"confirmPlaintext,omitempty"`
}

// handleAccountIOCapabilities reports the limits and gates the dashboard
// needs before opening a dialog. It never counts accounts or exposes a secret.
func (s *Server) handleAccountIOCapabilities(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, store.AccountIOCapabilities{
		EncryptedExport:    true,
		PlaintextExport:    s.cfg.AllowPlaintextExport,
		MaxImportRows:      store.MaxImportRows,
		MaxExportAccounts:  store.MaxExportAccounts,
		MaxDeleteIDs:       store.MaxDeleteIDs,
		MaxBodyBytes:       s.cfg.MaxBodyBytes,
		MaxImportBodyBytes: store.MaxImportBodyBytes,
	})
}

// handleExportAccounts builds the complete file in memory before writing any
// header, so an encryption failure cannot leave a truncated credential file.
func (s *Server) handleExportAccounts(w http.ResponseWriter, r *http.Request) {
	var req exportRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	format := req.Format
	if format == "" {
		format = exportFormatEncrypted
	}
	switch format {
	case exportFormatEncrypted, exportFormatJSON, exportFormatCSV:
	default:
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "format must be encrypted, json or csv")
		return
	}
	ids := dedupeStrings(req.IDs)
	if len(ids) > store.MaxExportAccounts {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument,
			fmt.Sprintf("at most %d accounts can be exported at once", store.MaxExportAccounts))
		return
	}
	if len(ids) > 0 && req.Filter != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "ids and filter are mutually exclusive")
		return
	}
	includeDevice := false
	switch format {
	case exportFormatEncrypted:
		if req.ConfirmPlaintext != "" {
			writeError(w, http.StatusBadRequest, CodeInvalidArgument, "confirmPlaintext is only valid for plaintext exports")
			return
		}
		if req.Passphrase == "" {
			writeError(w, http.StatusBadRequest, CodeInvalidArgument, "passphrase is required for encrypted export")
			return
		}
		includeDevice = true
		if req.IncludeDevice != nil {
			includeDevice = *req.IncludeDevice
		}
	case exportFormatJSON, exportFormatCSV:
		if !s.cfg.AllowPlaintextExport {
			s.logger.Warn("account_export_failed", "reason", "plaintext_disabled")
			writeError(w, http.StatusConflict, CodeConflict,
				"plaintext export is disabled; set AJS_ALLOW_PLAINTEXT_EXPORT=1")
			return
		}
		if req.Passphrase != "" {
			writeError(w, http.StatusBadRequest, CodeInvalidArgument, "passphrase is only valid for encrypted export")
			return
		}
		if req.ConfirmPlaintext != plaintextConfirm {
			writeError(w, http.StatusBadRequest, CodeInvalidArgument, `confirmPlaintext must be "EXPORT"`)
			return
		}
		if req.IncludeDevice != nil && *req.IncludeDevice {
			if format == exportFormatCSV {
				writeError(w, http.StatusBadRequest, CodeInvalidArgument, "CSV export cannot include device identity")
				return
			}
			includeDevice = true
		}
	}
	query := store.ExportQuery{IDs: ids}
	if req.Filter != nil {
		if len(req.Filter.Q) > 80 {
			writeError(w, http.StatusBadRequest, CodeInvalidArgument, "filter q must be at most 80 bytes")
			return
		}
		query.State = req.Filter.State
		query.Platform = req.Filter.Platform
		query.Q = req.Filter.Q
	}
	if !s.accountIOMu.TryLock() {
		writeError(w, http.StatusConflict, CodeConflict, "another account import/export is running")
		return
	}
	defer s.accountIOMu.Unlock()

	start := time.Now()
	rows, err := s.store.ExportAccounts(query)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	defer clearExportPasswords(rows)

	doc := store.BuildExport(rows, store.ExportOptions{
		IncludeDevice: includeDevice,
		SecretsSealed: s.store.SecretsEnabled(),
		DaemonVersion: s.cfg.Version,
		Now:           time.Now(),
	})
	missingSecrets := 0
	devices := 0
	passwords := 0
	for i := range rows {
		if rows[i].HasPassword {
			passwords++
		} else {
			missingSecrets++
		}
		if includeDevice && rows[i].Device != nil {
			devices++
		}
	}

	var payload []byte
	extension := ".ajsx"
	contentType := "application/vnd.ajs.accounts+json; charset=utf-8"
	switch format {
	case exportFormatEncrypted:
		envelope, err := store.SealExport(doc, req.Passphrase, nil)
		if err != nil {
			s.logger.Warn("account_export_failed", "reason", "encode")
			writeError(w, http.StatusBadRequest, CodeInvalidArgument, err.Error())
			return
		}
		payload, err = json.Marshal(envelope)
		if err != nil {
			s.logger.Warn("account_export_failed", "reason", "encode")
			writeError(w, http.StatusInternalServerError, CodeInternal, "internal error")
			return
		}
		// The envelope holds ciphertext only; drop the plaintext document and
		// passphrase references as soon as they are no longer needed.
		doc = store.AccountExport{}
		req.Passphrase = ""
	case exportFormatJSON:
		extension = ".json"
		contentType = "application/json; charset=utf-8"
		payload, err = json.Marshal(doc)
		if err != nil {
			s.logger.Warn("account_export_failed", "reason", "encode")
			writeError(w, http.StatusInternalServerError, CodeInternal, "internal error")
			return
		}
	case exportFormatCSV:
		extension = ".csv"
		contentType = "text/csv; charset=utf-8"
		payload, err = store.BuildExportCSV(doc)
		if err != nil {
			if errors.Is(err, store.ErrConflict) {
				writeError(w, http.StatusConflict, CodeConflict, err.Error())
				return
			}
			s.logger.Warn("account_export_failed", "reason", "encode")
			writeError(w, http.StatusInternalServerError, CodeInternal, "internal error")
			return
		}
	}

	s.store.RecordAudit(model.SeverityInfo, s.auditActor(r), "account.export", "accounts",
		fmt.Sprintf("format=%s accounts=%d passwords=%d devices=%d", format, len(rows), passwords, devices))
	s.logger.Info("account_export",
		"format", format,
		"accounts", len(rows),
		"passwords", passwords,
		"devices", devices,
		"duration_ms", time.Since(start).Milliseconds(),
	)

	filename := "ajs-accounts-" + time.Now().UTC().Format("20060102-150405") + extension
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-AJS-Export-Count", strconv.Itoa(len(rows)))
	w.Header().Set("X-AJS-Export-Missing-Secrets", strconv.Itoa(missingSecrets))
	w.Header().Set("X-AJS-Export-Devices", strconv.Itoa(devices))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(payload)
}

func clearExportPasswords(rows []store.ExportAccount) {
	for i := range rows {
		clear(rows[i].Password)
		rows[i].Password = nil
	}
}

func clearImportPlan(plan *store.ImportPlan) {
	for i := range plan.Creates {
		clear(plan.Creates[i].Password)
		plan.Creates[i].Password = nil
	}
	for i := range plan.Updates {
		clear(plan.Updates[i].Password)
		plan.Updates[i].Password = nil
	}
}

func clearExportDoc(doc *store.AccountExport) {
	for i := range doc.Accounts {
		doc.Accounts[i].Password = nil
	}
}

// handleImportAccounts parses, plans and (unless dryRun or an atomic failure)
// applies one import under the account-IO mutex.
func (s *Server) handleImportAccounts(w http.ResponseWriter, r *http.Request) {
	var req store.AccountImportRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	options := req.Options.Normalized()
	if err := options.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, err.Error())
		return
	}
	if !s.accountIOMu.TryLock() {
		writeError(w, http.StatusConflict, CodeConflict, "another account import/export is running")
		return
	}
	defer s.accountIOMu.Unlock()

	doc, format, warnings, err := decodeImportSource(req.Source)
	req.Source.Passphrase = ""
	if err != nil {
		reason := "parse"
		if req.Source.Kind == "encrypted" {
			reason = "envelope"
		}
		s.logger.Warn("account_import_failed", "reason", reason)
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, err.Error())
		return
	}
	if len(doc.Accounts) > store.MaxImportRows {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument,
			fmt.Sprintf("an import can contain at most %d accounts", store.MaxImportRows))
		return
	}
	plan, results, planWarnings := store.PlanImport(doc, options, s.store.Accounts(), s.store.SecretsEnabled())
	clearExportDoc(&doc)
	warnings = append(warnings, planWarnings...)
	plan.Format = format
	plan.Mode = options.Mode

	result := store.AccountImportResult{
		Format:   store.ImportResultFormat,
		Version:  1,
		DryRun:   options.DryRun,
		Mode:     options.Mode,
		Results:  results,
		Warnings: warnings,
	}
	for _, row := range results {
		result.Counts.Total++
		switch row.Action {
		case "create":
			result.Counts.Created++
		case "rename":
			result.Counts.Created++
			result.Counts.Renamed++
		case "update":
			result.Counts.Updated++
		case "skip":
			result.Counts.Skipped++
		case "fail":
			result.Counts.Failed++
		}
	}
	plan.Skipped = result.Counts.Skipped
	plan.Renamed = result.Counts.Renamed
	plan.Failed = result.Counts.Failed
	if options.DryRun {
		s.logger.Info("account_import_dry_run",
			"format", format,
			"mode", options.Mode,
			"total", result.Counts.Total,
			"created", result.Counts.Created,
			"updated", result.Counts.Updated,
			"skipped", result.Counts.Skipped,
			"failed", result.Counts.Failed,
		)
		writeImportResult(w, result)
		return
	}
	if options.Mode == "atomic" && result.Counts.Failed > 0 {
		writeImportResult(w, result)
		return
	}
	if len(plan.Creates) == 0 && len(plan.Updates) == 0 {
		writeImportResult(w, result)
		return
	}
	applied, err := s.store.ImportAccounts(plan, s.auditActor(r))
	clearImportPlan(&plan)
	if err != nil {
		s.logger.Warn("account_import_failed", "reason", "store")
		switch {
		case errors.Is(err, store.ErrInvalid):
			writeError(w, http.StatusBadRequest, CodeInvalidArgument, err.Error())
		case errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrConflict):
			writeError(w, http.StatusConflict, CodeConflict, err.Error())
		default:
			writeError(w, http.StatusInternalServerError, CodeInternal, "internal error")
		}
		return
	}
	assignCreatedIDs(result.Results, applied.Created)
	if s.cfg.LoginService != nil {
		for _, account := range applied.Updated {
			s.cfg.LoginService.Forget(account.ID)
		}
	}
	result.Applied = len(applied.Created)+len(applied.Updated) > 0
	s.persistAccountIO()
	s.logger.Info("account_import",
		"format", format,
		"mode", options.Mode,
		"total", result.Counts.Total,
		"created", result.Counts.Created,
		"updated", result.Counts.Updated,
		"skipped", result.Counts.Skipped,
		"failed", result.Counts.Failed,
	)
	writeImportResult(w, result)
}

func writeImportResult(w http.ResponseWriter, result store.AccountImportResult) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, result)
}

func assignCreatedIDs(results []store.AccountImportRowResult, created []model.Account) {
	index := 0
	for i := range results {
		if results[i].Action != "create" && results[i].Action != "rename" {
			continue
		}
		if index >= len(created) {
			return
		}
		results[i].ID = new(created[index].ID)
		index++
	}
}

// decodeImportSource unwraps the strict source block. The embedded document
// is decoded permissively; format and version stay exact.
func decodeImportSource(source store.AccountImportSource) (store.AccountExport, string, []store.WarningCode, error) {
	switch source.Kind {
	case "json":
		if source.Passphrase != "" {
			return store.AccountExport{}, "", nil, fmt.Errorf("passphrase is only valid for encrypted imports")
		}
		doc, err := store.ParseExportJSON(source.Content)
		return doc, "json", nil, err
	case "csv":
		if source.Passphrase != "" {
			return store.AccountExport{}, "", nil, fmt.Errorf("passphrase is only valid for encrypted imports")
		}
		var text string
		if err := json.Unmarshal(source.Content, &text); err != nil {
			return store.AccountExport{}, "", nil, fmt.Errorf("CSV content must be a JSON string")
		}
		doc, warnings, err := store.ParseExportCSV([]byte(text))
		if err != nil {
			return store.AccountExport{}, "", nil, err
		}
		if len(doc.Accounts) == 0 {
			return store.AccountExport{}, "", nil, fmt.Errorf("file has no accounts")
		}
		if len(doc.Accounts) > store.MaxImportRows {
			return store.AccountExport{}, "", nil, fmt.Errorf("an import can contain at most %d accounts", store.MaxImportRows)
		}
		return doc, "csv", warnings, nil
	case "encrypted":
		if source.Passphrase == "" {
			return store.AccountExport{}, "", nil, fmt.Errorf("passphrase is required for an encrypted import")
		}
		var envelope store.EncryptedExport
		if err := json.Unmarshal(source.Content, &envelope); err != nil {
			return store.AccountExport{}, "", nil, fmt.Errorf("encrypted export could not be opened")
		}
		doc, err := store.OpenExport(envelope, source.Passphrase)
		if err != nil {
			return store.AccountExport{}, "", nil, err
		}
		if len(doc.Accounts) == 0 {
			return store.AccountExport{}, "", nil, fmt.Errorf("file has no accounts")
		}
		if len(doc.Accounts) > store.MaxImportRows {
			return store.AccountExport{}, "", nil, fmt.Errorf("an import can contain at most %d accounts", store.MaxImportRows)
		}
		return doc, "encrypted", nil, nil
	default:
		return store.AccountExport{}, "", nil, fmt.Errorf("source.kind must be json, csv or encrypted")
	}
}

// handleBulkDeleteAccounts deletes ids sequentially, reconciles the panel once
// and returns per-item results. A per-item failure never fails the request.
func (s *Server) handleBulkDeleteAccounts(w http.ResponseWriter, r *http.Request) {
	var req store.BulkDeleteRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	ids := dedupeStrings(req.IDs)
	switch {
	case len(ids) == 0:
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "ids must contain at least one account id")
		return
	case len(ids) > store.MaxDeleteIDs:
		writeError(w, http.StatusBadRequest, CodeInvalidArgument,
			fmt.Sprintf("at most %d accounts can be deleted at once", store.MaxDeleteIDs))
		return
	}
	mode := req.Mode
	if mode == "" {
		mode = "continue"
	}
	if mode != "continue" && mode != "stop" {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "mode must be continue or stop")
		return
	}
	if !s.accountIOMu.TryLock() {
		writeError(w, http.StatusConflict, CodeConflict, "another account import/export is running")
		return
	}
	defer s.accountIOMu.Unlock()

	result := store.BulkDeleteResult{
		Format:  store.BulkDeleteResultFormat,
		Version: 1,
		DryRun:  req.DryRun,
		Mode:    mode,
		Results: make([]store.BulkDeleteItem, 0, len(ids)),
	}
	stopped := false
	for _, id := range ids {
		item := store.BulkDeleteItem{ID: id}
		if stopped {
			item.Status = "skipped"
			item.Code = new(CodeConflict)
			item.Message = new("skipped after an earlier failure")
			result.Counts.Skipped++
			result.Results = append(result.Results, item)
			continue
		}
		label, nodes, leases, ok := s.store.AccountDeleteStats(id)
		if !ok {
			item.Status = "not_found"
			item.Code = new(CodeNotFound)
			item.Message = new("account not found")
			result.Counts.NotFound++
			result.Results = append(result.Results, item)
			continue
		}
		item.Label = new(label)
		item.Nodes = nodes
		item.Leases = leases
		if req.DryRun {
			item.Status = "would_delete"
			result.Counts.WouldDelete++
			result.Results = append(result.Results, item)
			continue
		}
		// Join the account-wide stop claim, exactly like DELETE
		// /v1/accounts/{id}: a login that lands after the fence waits for the
		// claim, then re-checks the deleted state instead of installing a gate
		// behind the cascade.
		release, stopErr := s.stopAccountForMutation(r.Context(), id, s.cfg.AccountDeleteHook)
		if stopErr != nil {
			if release != nil {
				release()
			}
			s.logger.Error("account_delete_hook_error", "account", id, "error", stopErr)
			item.Status = "failed"
			item.Code = new(CodeInternal)
			item.Message = new("account delete hook failed")
			result.Counts.Failed++
			result.Results = append(result.Results, item)
			if mode == "stop" {
				stopped = true
			}
			continue
		}
		if err := s.store.DeleteAccount(id, s.auditActor(r)); err != nil {
			if release != nil {
				release()
			}
			item.Status = "failed"
			switch {
			case errors.Is(err, store.ErrNotFound):
				item.Status = "not_found"
				item.Code = new(CodeNotFound)
				item.Message = new("account not found")
				result.Counts.NotFound++
			case errors.Is(err, store.ErrUpstream):
				item.Code = new(CodeInternal)
				item.Message = new("sealed secret store unavailable")
				result.Counts.Failed++
			default:
				item.Code = new(CodeInternal)
				item.Message = new("account could not be deleted")
				result.Counts.Failed++
			}
			if mode == "stop" && item.Status == "failed" {
				stopped = true
			}
			result.Results = append(result.Results, item)
			continue
		}
		item.Status = "deleted"
		result.Counts.Deleted++
		if s.cfg.LoginService != nil {
			s.cfg.LoginService.Forget(id)
		}
		if release != nil {
			release()
		}
		result.Results = append(result.Results, item)
	}
	result.Counts.Total = len(result.Results)

	if !req.DryRun {
		if result.Counts.Deleted > 0 {
			if s.cfg.Panel != nil {
				if _, err := s.cfg.Panel.Reconcile(r.Context()); err != nil {
					s.logger.Warn("resin_reconcile_failed", "error", err)
				}
			}
			s.persistAccountIO()
		}
		s.store.RecordAudit(model.SeverityInfo, s.auditActor(r), "account.bulk_delete", "accounts",
			fmt.Sprintf("requested=%d deleted=%d not_found=%d failed=%d skipped=%d",
				len(ids), result.Counts.Deleted, result.Counts.NotFound, result.Counts.Failed, result.Counts.Skipped))
		s.logger.Info("account_bulk_delete",
			"requested", len(ids),
			"deleted", result.Counts.Deleted,
			"not_found", result.Counts.NotFound,
			"failed", result.Counts.Failed,
			"skipped", result.Counts.Skipped,
		)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) persistAccountIO() {
	if s.cfg.Persist == nil {
		return
	}
	s.cfg.Persist()
}

func dedupeStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
