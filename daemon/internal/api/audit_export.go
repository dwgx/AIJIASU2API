package api

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/store"
)

const (
	// auditExportDefaultWindow is the range used when ?from is absent: the
	// last seven days up to ?to (or now).
	auditExportDefaultWindow = 7 * 24 * time.Hour
	// auditExportMaxWindow bounds one export so a client cannot ask the
	// daemon to render years of history in a single response.
	auditExportMaxWindow = 366 * 24 * time.Hour
	// auditExportFilenameLayout is the filename-safe UTC stamp used in
	// Content-Disposition.
	auditExportFilenameLayout = "20060102T150405Z"
)

// auditExportColumns is the fixed CSV column order. A client may rely on the
// header row exactly matching this list.
var auditExportColumns = []string{"ts", "severity", "actor", "action", "target", "detail", "meta"}

// auditExportRowCap is the most rows one export returns; older matches are
// dropped and flagged with X-Truncated. It is a var only so the truncation
// path is testable without seeding 50k rows (the store retains 1000).
var auditExportRowCap = 50000

// auditExportSecretAssignment matches a credential-looking "key=value" pair in
// an audit detail. Audit rows are sanitized before they are stored, so this is
// defense in depth: a caller that ever passes a raw token cannot turn the CSV
// export into a credential leak.
var auditExportSecretAssignment = regexp.MustCompile(`(?i)\b(token|password|passwd|secret|api[_-]?key|passphrase)\s*=\s*("[^"]*"|'[^']*'|[^\s,;]+)`)

// auditExportAuthorization matches a header-like "Authorization: Bearer ..."
// value, which the generic assignment pattern only covers partially.
var auditExportAuthorization = regexp.MustCompile(`(?i)\bauthorization\s*[:=]\s*(?:bearer\s+)?[^\s,;]+`)

// handleAuditExportCSV serves GET /v1/audit/export.csv. It filters the
// retained audit history by an inclusive RFC 3339 window and an optional
// severity, then streams the matching rows oldest-first.
func (s *Server) handleAuditExportCSV(w http.ResponseWriter, r *http.Request) {
	from, to, ok := auditExportWindow(w, r)
	if !ok {
		return
	}
	severity := model.Severity(strings.TrimSpace(r.URL.Query().Get("severity")))
	switch severity {
	case "", model.SeverityInfo, model.SeverityWarn, model.SeverityError:
	default:
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "severity must be one of info, warn, error")
		return
	}

	rows, truncated := selectAuditRows(s.store.Audit(store.AuditRetention), from, to, severity, auditExportRowCap)

	filename := "apc-audit-" + from.Format(auditExportFilenameLayout) + "-" + to.Format(auditExportFilenameLayout) + ".csv"
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if truncated {
		w.Header().Set("X-Truncated", "true")
	}
	w.WriteHeader(http.StatusOK)

	cw := csv.NewWriter(w)
	if err := cw.Write(auditExportColumns); err != nil {
		s.logger.Warn("audit_export_failed", "reason", "header", "error", err)
		return
	}
	for _, event := range rows {
		if err := cw.Write([]string{
			event.TS,
			string(event.Severity),
			event.Actor,
			event.Action,
			event.Target,
			sanitizeAuditDetail(event.Detail),
			encodeAuditMeta(event.Meta),
		}); err != nil {
			s.logger.Warn("audit_export_failed", "reason", "row", "error", err)
			return
		}
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		s.logger.Warn("audit_export_failed", "reason", "flush", "error", err)
	}
}

// auditExportWindow parses and validates ?from=&to=. Both bounds are RFC 3339
// and inclusive; ?to defaults to now and ?from to seven days before ?to. An
// invalid, reversed or longer-than-366-day window answers 400.
func auditExportWindow(w http.ResponseWriter, r *http.Request) (from, to time.Time, ok bool) {
	query := r.URL.Query()
	to = time.Now().UTC()
	if raw := query.Get("to"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, CodeInvalidArgument, "to must be an RFC 3339 timestamp")
			return time.Time{}, time.Time{}, false
		}
		to = parsed.UTC()
	}
	from = to.Add(-auditExportDefaultWindow)
	if raw := query.Get("from"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, CodeInvalidArgument, "from must be an RFC 3339 timestamp")
			return time.Time{}, time.Time{}, false
		}
		from = parsed.UTC()
	}
	if from.After(to) {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "from must not be after to")
		return time.Time{}, time.Time{}, false
	}
	if to.Sub(from) > auditExportMaxWindow {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "range must not exceed 366 days")
		return time.Time{}, time.Time{}, false
	}
	return from, to, true
}

// selectAuditRows filters the newest-first store history to [from,to] and the
// optional severity, keeps at most maxRows newest matches, and returns them in
// ascending time order. truncated reports that at least one older match was
// dropped by the cap.
func selectAuditRows(events []model.AuditEvent, from, to time.Time, severity model.Severity, maxRows int) ([]model.AuditEvent, bool) {
	if maxRows <= 0 {
		return nil, false
	}
	rows := make([]model.AuditEvent, 0, min(len(events), maxRows))
	for _, event := range events {
		ts, err := time.Parse(time.RFC3339, event.TS)
		if err != nil || ts.Before(from) || ts.After(to) {
			continue
		}
		if severity != "" && event.Severity != severity {
			continue
		}
		if len(rows) == maxRows {
			reverseAuditRows(rows)
			return rows, true
		}
		rows = append(rows, event)
	}
	reverseAuditRows(rows)
	return rows, false
}

func reverseAuditRows(rows []model.AuditEvent) {
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
}

// sanitizeAuditDetail scrubs credential-looking assignments from a detail
// before it reaches the CSV. The store sanitizes at write time; this second
// pass keeps the export safe even if a future caller forgets.
func sanitizeAuditDetail(detail string) string {
	detail = auditExportAuthorization.ReplaceAllString(detail, "authorization=[redacted]")
	return auditExportSecretAssignment.ReplaceAllString(detail, "${1}=[redacted]")
}

// encodeAuditMeta renders the credential-free meta block as JSON. encoding/json
// sorts map keys, so the column is byte-stable across exports; rows without
// meta render as an empty object rather than an empty column.
func encodeAuditMeta(meta map[string]string) string {
	if len(meta) == 0 {
		return "{}"
	}
	encoded, err := json.Marshal(meta)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}
