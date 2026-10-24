package api

import (
	"encoding/csv"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/store"
)

// readAuditCSV parses an export body with the same reader a client would use.
func readAuditCSV(t *testing.T, body []byte) [][]string {
	t.Helper()
	rows, err := csv.NewReader(strings.NewReader(string(body))).ReadAll()
	if err != nil {
		t.Fatalf("parse CSV export: %v (body %q)", err, body)
	}
	if len(rows) == 0 {
		t.Fatalf("CSV export is empty (body %q)", body)
	}
	return rows
}

func TestAuditExportCSVHeadersAndRows(t *testing.T) {
	ts, st := newTestServer(t)
	st.WriteAudit(model.SeverityInfo, "system", "catalog.switch", "acc-01:srv-a", "switched",
		map[string]string{"toServerId": "srv-b", "fromServerId": "srv-a", "result": "ok"})

	resp, body := apiCall(t, ts, http.MethodGet, "/v1/audit/export.csv", nil)
	requireStatus(t, resp, http.StatusOK)

	if got := resp.Header.Get("Content-Type"); got != "text/csv; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	disposition := resp.Header.Get("Content-Disposition")
	if !strings.HasPrefix(disposition, `attachment; filename="apc-audit-`) || !strings.HasSuffix(disposition, `.csv"`) {
		t.Fatalf("Content-Disposition = %q", disposition)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if got := resp.Header.Get("X-Truncated"); got != "" {
		t.Fatalf("X-Truncated = %q on an untruncated export", got)
	}

	rows := readAuditCSV(t, body)
	if got, want := rows[0], auditExportColumns; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("header = %v, want %v", got, want)
	}
	if want := len(st.Audit(store.AuditRetention)); len(rows)-1 != want {
		t.Fatalf("exported rows = %d, want all %d retained rows inside the default window", len(rows)-1, want)
	}

	// Rows are oldest-first and every column has a value; seed rows carry no
	// meta, which renders as an empty JSON object rather than a blank cell.
	previous := time.Time{}
	actions := map[string]string{}
	for _, row := range rows[1:] {
		if len(row) != len(auditExportColumns) {
			t.Fatalf("row has %d columns: %v", len(row), row)
		}
		stamp, err := time.Parse(time.RFC3339, row[0])
		if err != nil {
			t.Fatalf("row timestamp %q: %v", row[0], err)
		}
		if stamp.Before(previous) {
			t.Fatalf("rows are not oldest-first: %s after %s", row[0], previous.Format(time.RFC3339))
		}
		previous = stamp
		actions[row[3]] = row[6]
	}
	if got := actions["account.login"]; got != "{}" {
		t.Fatalf("seed meta = %q, want {}", got)
	}
	if got, want := actions["catalog.switch"], `{"fromServerId":"srv-a","result":"ok","toServerId":"srv-b"}`; got != want {
		t.Fatalf("catalog meta = %q, want stable sorted JSON %q", got, want)
	}
}

func TestAuditExportCSVFilters(t *testing.T) {
	ts, st := newTestServer(t)
	st.RecordAudit(model.SeverityWarn, "operator", "key.create", "build-bot", "warn marker")
	st.RecordAudit(model.SeverityError, "operator", "key.revoke", "build-bot", "error marker")

	resp, body := apiCall(t, ts, http.MethodGet, "/v1/audit/export.csv?severity=warn", nil)
	requireStatus(t, resp, http.StatusOK)
	rows := readAuditCSV(t, body)
	for _, row := range rows[1:] {
		if row[1] != string(model.SeverityWarn) {
			t.Fatalf("severity filter leaked %q: %v", row[1], row)
		}
	}
	text := string(body)
	if !strings.Contains(text, "warn marker") {
		t.Fatalf("warn row missing from export: %s", text)
	}
	if strings.Contains(text, "error marker") {
		t.Fatalf("severity filter leaked the error row: %s", text)
	}

	now := time.Now().UTC()
	from := now.Add(-time.Hour)
	to := now.Add(time.Hour)
	resp, body = apiCall(t, ts, http.MethodGet, "/v1/audit/export.csv?from="+
		url.QueryEscape(from.Format(time.RFC3339))+"&to="+url.QueryEscape(to.Format(time.RFC3339)), nil)
	requireStatus(t, resp, http.StatusOK)
	if !strings.Contains(string(body), "warn marker") {
		t.Fatalf("inclusive window dropped a fresh row: %s", body)
	}

	// A window that ends an hour ago excludes the rows just recorded.
	from = now.Add(-2 * time.Hour)
	to = now.Add(-time.Hour)
	resp, body = apiCall(t, ts, http.MethodGet, "/v1/audit/export.csv?from="+
		url.QueryEscape(from.Format(time.RFC3339))+"&to="+url.QueryEscape(to.Format(time.RFC3339)), nil)
	requireStatus(t, resp, http.StatusOK)
	if strings.Contains(string(body), "warn marker") || strings.Contains(string(body), "error marker") {
		t.Fatalf("window leaked rows outside the bounds: %s", body)
	}
	rows = readAuditCSV(t, body)
	if len(rows) == 1 {
		t.Fatalf("expected seeded rows inside the one-hour window, got none")
	}
	for _, row := range rows[1:] {
		stamp, err := time.Parse(time.RFC3339, row[0])
		if err != nil {
			t.Fatalf("row timestamp %q: %v", row[0], err)
		}
		if stamp.Before(from) || stamp.After(to) {
			t.Fatalf("row %s outside [%s, %s]", row[0], from.Format(time.RFC3339), to.Format(time.RFC3339))
		}
	}
}

func TestAuditExportCSVBoundsValidation(t *testing.T) {
	ts, _ := newTestServer(t)
	now := time.Now().UTC()
	nowValue := url.QueryEscape(now.Format(time.RFC3339))
	hourAgo := url.QueryEscape(now.Add(-time.Hour).Format(time.RFC3339))
	tooWide := url.QueryEscape(now.Add(-367 * 24 * time.Hour).Format(time.RFC3339))

	cases := []struct{ name, query string }{
		{"invalid from", "?from=yesterday"},
		{"invalid to", "?to=2026-10-24"},
		{"reversed bounds", "?from=" + nowValue + "&to=" + hourAgo},
		{"window over 366 days", "?from=" + tooWide + "&to=" + nowValue},
		{"unknown severity", "?severity=debug"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := apiCall(t, ts, http.MethodGet, "/v1/audit/export.csv"+tc.query, nil)
			requireStatus(t, resp, http.StatusBadRequest)
			requireErrorCode(t, body, CodeInvalidArgument)
		})
	}

	// Exactly 366 days is the documented maximum and stays valid.
	from := url.QueryEscape(now.Add(-366 * 24 * time.Hour).Format(time.RFC3339))
	resp, _ := apiCall(t, ts, http.MethodGet, "/v1/audit/export.csv?from="+from+"&to="+nowValue, nil)
	requireStatus(t, resp, http.StatusOK)
}

func TestAuditExportCSVTruncation(t *testing.T) {
	previous := auditExportRowCap
	auditExportRowCap = 2
	t.Cleanup(func() { auditExportRowCap = previous })

	ts, _ := newTestServer(t)
	resp, body := apiCall(t, ts, http.MethodGet, "/v1/audit/export.csv", nil)
	requireStatus(t, resp, http.StatusOK)
	if got := resp.Header.Get("X-Truncated"); got != "true" {
		t.Fatalf("X-Truncated = %q, want true", got)
	}
	rows := readAuditCSV(t, body)
	if len(rows)-1 != 2 {
		t.Fatalf("rows = %d, want the cap of 2", len(rows)-1)
	}
	// The two newest seeds survive, oldest-first.
	if rows[1][3] != "subscription.publish" || rows[2][3] != "account.login" {
		t.Fatalf("truncated rows = %v %v, want the two newest seeds oldest-first", rows[1], rows[2])
	}
}

func TestAuditExportCSVRedactsCredentials(t *testing.T) {
	ts, st := newTestServer(t)
	st.RecordAudit(model.SeverityWarn, "operator", "key.create", "build-bot",
		"token=ajs_live_SUPERSECRET password=hunter2 Authorization: Bearer abc.def.ghi")

	resp, body := apiCall(t, ts, http.MethodGet, "/v1/audit/export.csv?severity=warn", nil)
	requireStatus(t, resp, http.StatusOK)
	text := string(body)
	for _, secret := range []string{"ajs_live_SUPERSECRET", "hunter2", "abc.def.ghi", testToken} {
		if strings.Contains(text, secret) {
			t.Fatalf("export leaked %q: %s", secret, text)
		}
	}
	for _, want := range []string{"token=[redacted]", "password=[redacted]", "authorization=[redacted]"} {
		if !strings.Contains(text, want) {
			t.Fatalf("export is missing %q: %s", want, text)
		}
	}

	// The scrub is narrow: a plain mention of a token without an assignment
	// stays readable.
	resp, body = apiCall(t, ts, http.MethodGet, "/v1/audit/export.csv", nil)
	requireStatus(t, resp, http.StatusOK)
	if !strings.Contains(string(body), "token valid for 10m") {
		t.Fatalf("export over-redacted a benign detail: %s", body)
	}
}

func TestAuditExportCSVRequiresAuth(t *testing.T) {
	ts, _ := newTestServer(t)
	req := newRequest(t, http.MethodGet, ts.URL+"/v1/audit/export.csv", nil)
	resp, body := doRequest(t, ts, req)
	requireStatus(t, resp, http.StatusUnauthorized)
	requireErrorCode(t, body, CodeUnauthorized)
}

func TestAuditExportCSVScopedKey(t *testing.T) {
	ts, _ := newTestServer(t)
	created, _ := mustCreateScopedKey(t, ts, map[string]any{
		"userId": "u-01",
		"label":  "audit export",
		"scopes": []string{"audit:read"},
	})
	resp, _ := scopedCall(t, ts, created.Secret, http.MethodGet, "/v1/audit/export.csv", nil)
	requireStatus(t, resp, http.StatusOK)
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/csv") {
		t.Fatalf("Content-Type = %q", got)
	}
}

func TestSelectAuditRowsFiltersOrdersAndTruncates(t *testing.T) {
	base := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	events := []model.AuditEvent{
		{ID: "a", TS: base.Format(time.RFC3339), Severity: model.SeverityInfo},
		{ID: "b", TS: base.Add(-time.Minute).Format(time.RFC3339), Severity: model.SeverityWarn},
		{ID: "c", TS: base.Add(-2 * time.Minute).Format(time.RFC3339), Severity: model.SeverityInfo},
		{ID: "d", TS: base.Add(-3 * time.Minute).Format(time.RFC3339), Severity: model.SeverityWarn},
		{ID: "e", TS: base.Add(-4 * time.Minute).Format(time.RFC3339), Severity: model.SeverityInfo},
	}
	from := base.Add(-10 * time.Minute)
	to := base.Add(time.Minute)

	ids := func(rows []model.AuditEvent) []string {
		out := make([]string, len(rows))
		for i := range rows {
			out[i] = rows[i].ID
		}
		return out
	}

	rows, truncated := selectAuditRows(events, from, to, "", 10)
	if truncated || strings.Join(ids(rows), "") != "edcba" {
		t.Fatalf("all rows = %v truncated=%v, want edcba ascending", ids(rows), truncated)
	}

	rows, _ = selectAuditRows(events, from, to, model.SeverityWarn, 10)
	if strings.Join(ids(rows), "") != "db" {
		t.Fatalf("warn rows = %v, want db ascending", ids(rows))
	}

	// Inclusive bounds: both the -2m and -1m events match.
	rows, _ = selectAuditRows(events, base.Add(-2*time.Minute), base.Add(-time.Minute), "", 10)
	if strings.Join(ids(rows), "") != "cb" {
		t.Fatalf("window rows = %v, want cb ascending", ids(rows))
	}

	// The cap keeps the newest matches and flags the drop.
	rows, truncated = selectAuditRows(events, from, to, "", 2)
	if !truncated || strings.Join(ids(rows), "") != "ba" {
		t.Fatalf("capped rows = %v truncated=%v, want ba ascending and truncated", ids(rows), truncated)
	}
	if _, truncated = selectAuditRows(events, from, to, "", 5); truncated {
		t.Fatal("exactly maxRows rows must not report truncation")
	}

	// A malformed timestamp is skipped rather than failing the export.
	events = append(events, model.AuditEvent{ID: "bad", TS: "not-a-time", Severity: model.SeverityInfo})
	rows, _ = selectAuditRows(events, from, to, "", 10)
	if strings.Join(ids(rows), "") != "edcba" {
		t.Fatalf("malformed timestamp changed the selection: %v", ids(rows))
	}
}

func TestEncodeAuditMetaStableOrder(t *testing.T) {
	if got := encodeAuditMeta(nil); got != "{}" {
		t.Fatalf("nil meta = %q, want {}", got)
	}
	meta := map[string]string{"z": "last", "a": "first", "m": "middle"}
	if got, want := encodeAuditMeta(meta), `{"a":"first","m":"middle","z":"last"}`; got != want {
		t.Fatalf("meta = %q, want %q", got, want)
	}
}

func TestNeutralizeAuditCSVFieldMatrix(t *testing.T) {
	cases := map[string]string{
		"":            "",
		"plain":       "plain",
		"=1+1":        "'=1+1",
		"+SUM(A1)":    "'+SUM(A1)",
		"-2+3":        "'-2+3",
		"@cmd":        "'@cmd",
		"\t=1+1":      "'\t=1+1",
		"\r=1+1":      "'\r=1+1",
		"a=b":         "a=b",
		" mid=value ": " mid=value ",
		"1+1":         "1+1",
	}
	for input, want := range cases {
		if got := neutralizeAuditCSVField(input); got != want {
			t.Fatalf("neutralizeAuditCSVField(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestAuditExportCSVNeutralizesFormulaCells(t *testing.T) {
	st := store.NewEmpty()
	server := New(Config{AdminToken: testToken}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)

	st.RecordAudit(model.SeverityInfo, "=actor", "+action", "-target", "@detail")
	st.RecordAudit(model.SeverityInfo, "\tactor", "\raction", "target\t", "=1+1")
	st.RecordAudit(model.SeverityInfo, "operator", "note", "host", "he said \"hi\", ok\nsecond line")

	resp, body := apiCall(t, ts, http.MethodGet, "/v1/audit/export.csv", nil)
	requireStatus(t, resp, http.StatusOK)
	rows := readAuditCSV(t, body)
	if len(rows) != 4 {
		t.Fatalf("rows = %d, want a header plus three events: %v", len(rows), rows)
	}

	want := [][]string{
		{"'=actor", "'+action", "'-target", "'@detail", "{}"},
		{"'\tactor", "'\raction", "target\t", "'=1+1", "{}"},
		{"operator", "note", "host", "he said \"hi\", ok\nsecond line", "{}"},
	}
	for i, expected := range want {
		row := rows[i+1]
		if len(row) != len(auditExportColumns) {
			t.Fatalf("row %d has %d columns: %v", i+1, len(row), row)
		}
		if got := row[2:]; strings.Join(got, "\x00") != strings.Join(expected, "\x00") {
			t.Fatalf("row %d = %q, want %q", i+1, got, expected)
		}
	}
}
