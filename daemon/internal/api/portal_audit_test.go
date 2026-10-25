package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"ajsaccountd/internal/model"
)

func portalAuditRows(t *testing.T, ts *httptest.Server, cookie, query string) []model.AuditEvent {
	t.Helper()
	resp, body := portalCall(t, ts, http.MethodGet, "/v1/portal/audit"+query, nil, cookie, "")
	requireStatus(t, resp, http.StatusOK)
	return decodeBody[[]model.AuditEvent](t, body)
}

func auditHasDetail(rows []model.AuditEvent, needle string) bool {
	for _, row := range rows {
		if strings.Contains(row.Detail, needle) {
			return true
		}
	}
	return false
}

func TestPortalAuditReturnsOnlyOwnRows(t *testing.T) {
	ts, st := newPortalTestServer(t)
	tenant, cookie, _ := activePortalTenant(t, ts, st, "ada@example.com", "u-01")

	// Own rows, one per ownership signal the handler understands.
	st.WriteAudit(model.SeverityInfo, "admin", "entitlement.granted", "ord-own",
		"OWN-META-USER", map[string]string{
			"orderId": "ord-own", "tenantId": tenant.ID, "userId": "u-01",
		})
	st.WriteAudit(model.SeverityInfo, "operator", "key.create", "ada.ops", "OWN-USER-TARGET", nil)
	st.WriteAudit(model.SeverityInfo, "admin", "tenant.resume", "ada@example.com", "OWN-TENANT-EMAIL", nil)
	st.WriteAudit(model.SeverityInfo, "tenant:"+tenant.ID, "session.login", tenant.ID, "OWN-SESSION", nil)
	st.WriteAudit(model.SeverityInfo, "operator", "key.revoke", "ada.ops", "token=OWN-SECRET", nil)
	st.WriteAudit(model.SeverityInfo, "tenant:"+tenant.ID, "session.note", tenant.ID,
		"opened https://panel.example/account?token=OWN-URL-TOKEN", nil)

	// Foreign rows: another user, another tenant, admin-only work, an admin
	// row that merely mentions this user, and a credential-bearing URL.
	st.WriteAudit(model.SeverityInfo, "operator", "key.create", "build-bot", "FOREIGN-USER", nil)
	st.WriteAudit(model.SeverityInfo, "tenant:t-99", "order.created", "ord-foreign",
		"FOREIGN-TENANT", map[string]string{
			"orderId": "ord-foreign", "tenantId": "t-99", "userId": "u-02",
		})
	st.WriteAudit(model.SeverityInfo, "operator", "backup.create", "backup", "FOREIGN-ADMIN", nil)
	st.WriteAudit(model.SeverityInfo, "operator", "backup.create", "backup", "FOREIGN-ADMIN-META",
		map[string]string{"userId": "u-01", "tenantId": tenant.ID})
	st.WriteAudit(model.SeverityInfo, "operator", "account.test", "ada.ops", "FOREIGN-ADMIN-MENTION", nil)
	st.WriteAudit(model.SeverityInfo, "operator", "key.create", "build-bot",
		"https://secret.example/x?token=leak", nil)

	rows := portalAuditRows(t, ts, cookie, "")
	for _, marker := range []string{
		"OWN-META-USER", "OWN-USER-TARGET", "OWN-TENANT-EMAIL", "OWN-SESSION",
	} {
		if !auditHasDetail(rows, marker) {
			t.Fatalf("own row %q missing from %+v", marker, rows)
		}
	}
	for _, marker := range []string{
		"FOREIGN-USER", "FOREIGN-TENANT", "FOREIGN-ADMIN", "FOREIGN-ADMIN-MENTION",
		"FOREIGN-ADMIN-META", "secret.example", "OWN-SECRET", "panel.example", "OWN-URL-TOKEN",
	} {
		if auditHasDetail(rows, marker) {
			t.Fatalf("foreign or secret marker %q leaked into %+v", marker, rows)
		}
	}
	if !auditHasDetail(rows, "token=[redacted]") {
		t.Fatalf("own credential was not redacted: %+v", rows)
	}
	if !auditHasDetail(rows, "[redacted-url]") {
		t.Fatalf("own URL was not redacted: %+v", rows)
	}
	for _, row := range rows {
		if row.Meta != nil {
			t.Fatalf("portal audit leaked meta: %+v", row)
		}
	}
	for i := 1; i < len(rows); i++ {
		if auditSequence(rows[i-1].ID) <= auditSequence(rows[i].ID) {
			t.Fatalf("rows are not newest first: %q then %q", rows[i-1].ID, rows[i].ID)
		}
	}
}

func TestPortalAuditCursorAndLimitCap(t *testing.T) {
	ts, st := newPortalTestServer(t)
	_, cookie, _ := activePortalTenant(t, ts, st, "ada@example.com", "u-01")

	const seeded = 205
	for i := range seeded {
		st.WriteAudit(model.SeverityInfo, "operator", "key.create", "ada.ops",
			fmt.Sprintf("PAGED-%03d", i), nil)
	}

	// ?limit=0 keeps the dashboard meaning ("no limit") but the portal cap
	// wins, so the oldest rows are beyond one response.
	capped := portalAuditRows(t, ts, cookie, "?limit=0")
	if len(capped) != portalAuditMaxLimit {
		t.Fatalf("limit=0 returned %d rows, want cap %d", len(capped), portalAuditMaxLimit)
	}
	if !auditHasDetail(capped, "PAGED-204") {
		t.Fatalf("newest own row missing: %+v", capped)
	}
	if auditHasDetail(capped, "PAGED-000") {
		t.Fatalf("cap did not drop the oldest own row")
	}
	if over := portalAuditRows(t, ts, cookie, "?limit=999"); len(over) != portalAuditMaxLimit {
		t.Fatalf("limit=999 returned %d rows, want %d", len(over), portalAuditMaxLimit)
	}
	if defaulted := portalAuditRows(t, ts, cookie, ""); len(defaulted) != portalAuditDefaultLimit {
		t.Fatalf("default page = %d rows, want %d", len(defaulted), portalAuditDefaultLimit)
	}

	first := portalAuditRows(t, ts, cookie, "?limit=2")
	if len(first) != 2 {
		t.Fatalf("first page = %d rows, want 2", len(first))
	}
	second := portalAuditRows(t, ts, cookie, "?limit=2&before="+url.QueryEscape(first[1].ID))
	if len(second) != 2 {
		t.Fatalf("second page = %d rows, want 2", len(second))
	}
	if auditSequence(second[0].ID) >= auditSequence(first[1].ID) {
		t.Fatalf("cursor did not advance: %q then %q", first[1].ID, second[0].ID)
	}

	seen := make(map[string]struct{})
	pages := 0
	cursor := ""
	for {
		query := "?limit=3"
		if cursor != "" {
			query += "&before=" + url.QueryEscape(cursor)
		}
		page := portalAuditRows(t, ts, cookie, query)
		if len(page) == 0 {
			break
		}
		pages++
		for _, row := range page {
			if _, duplicate := seen[row.ID]; duplicate {
				t.Fatalf("cursor repeated row %q", row.ID)
			}
			seen[row.ID] = struct{}{}
		}
		cursor = page[len(page)-1].ID
	}
	if pages < 2 {
		t.Fatalf("pagination produced %d pages, want at least 2", pages)
	}
	if len(seen) <= portalAuditMaxLimit {
		t.Fatalf("pagination saw %d rows, want more than one capped page", len(seen))
	}

	for _, query := range []string{"?before=nope", "?limit=-1", "?limit=abc"} {
		resp, body := portalCall(t, ts, http.MethodGet, "/v1/portal/audit"+query, nil, cookie, "")
		requireStatus(t, resp, http.StatusBadRequest)
		requireErrorCode(t, body, CodeInvalidArgument)
	}
}

func TestPortalAuditRequiresAnActiveSession(t *testing.T) {
	ts, st := newPortalTestServer(t)

	resp, body := portalCall(t, ts, http.MethodGet, "/v1/portal/audit", nil, "", "")
	requireStatus(t, resp, http.StatusUnauthorized)
	requireErrorCode(t, body, CodeUnauthorized)

	_, cookie, _ := activePortalTenant(t, ts, st, "ada@example.com", "u-01")
	// Suspending the linked user leaves the session row alive but makes the
	// guard refuse it with 403, the same path a suspended tenant takes when
	// its session was not revoked.
	resp, _ = apiCall(t, ts, http.MethodPost, "/v1/users/u-01/suspend", nil)
	requireStatus(t, resp, http.StatusOK)

	resp, body = portalCall(t, ts, http.MethodGet, "/v1/portal/audit", nil, cookie, "")
	requireStatus(t, resp, http.StatusForbidden)
	requireErrorCode(t, body, CodeForbidden)
}
