package api

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"ajsaccountd/internal/model"
)

func TestTenantInviteOnboardingFlow(t *testing.T) {
	ts, st := newPortalTestServer(t)

	req := newRequest(t, http.MethodPost, ts.URL+"/v1/admin/tenants/invites", model.TenantInviteCreateRequest{
		Email: "  New.Customer@Example.COM ", DisplayName: "New Customer",
	})
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set(adminCSRFHeader, adminCSRFValue)
	req.Header.Set(adminOperatorHeader, "alice")
	resp, body := doRequest(t, ts, req)
	requireStatus(t, resp, http.StatusCreated)
	created := decodeBody[model.TenantInviteCreated](t, body)
	if len(created.Code) != 64 {
		t.Fatalf("code length = %d, want 64", len(created.Code))
	}
	if created.Invite.Email != "new.customer@example.com" || created.Invite.Status != model.TenantInvitePending {
		t.Fatalf("created invite = %+v", created.Invite)
	}
	if !strings.HasPrefix(created.Invite.ExpiresAt, "20") {
		t.Fatalf("expiresAt = %q", created.Invite.ExpiresAt)
	}

	// The raw code is not in the snapshot; only its digest is.
	snapshot, err := st.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if bytes.Contains(snapshot, []byte(created.Code)) {
		t.Fatal("snapshot contains the raw invite code")
	}

	// Preview is public and returns the invitee-facing fields only.
	resp, body = portalCall(t, ts, http.MethodGet, "/v1/portal/invites/"+created.Code, nil, "", "")
	requireStatus(t, resp, http.StatusOK)
	view := decodeBody[model.TenantInviteView](t, body)
	if view.Email != "new.customer@example.com" || view.DisplayName != "New Customer" {
		t.Fatalf("preview = %+v", view)
	}

	// Activation sets the password and creates a pending tenant only.
	resp, body = portalCall(t, ts, http.MethodPost, "/v1/portal/invites/"+created.Code+"/activate",
		model.TenantInviteActivateRequest{Password: portalTestPassword}, "", "")
	requireStatus(t, resp, http.StatusOK)
	tenant := decodeBody[model.Tenant](t, body)
	if tenant.Status != model.TenantPending || tenant.UserID != nil || tenant.Email != "new.customer@example.com" {
		t.Fatalf("activated tenant = %+v", tenant)
	}

	// The new tenant cannot log in until an operator approves it.
	resp, body = portalCall(t, ts, http.MethodPost, "/v1/portal/login", model.TenantLoginInput{
		Email: "new.customer@example.com", Password: portalTestPassword,
	}, "", "")
	requireStatus(t, resp, http.StatusForbidden)
	requireErrorCode(t, body, CodeForbidden)

	// The same code cannot be replayed.
	resp, body = portalCall(t, ts, http.MethodPost, "/v1/portal/invites/"+created.Code+"/activate",
		model.TenantInviteActivateRequest{Password: portalTestPassword}, "", "")
	requireStatus(t, resp, http.StatusConflict)
	requireErrorCode(t, body, CodeInviteRedeemed)
	if got := len(st.Tenants()); got != 1 {
		t.Fatalf("replay created another tenant: %d", got)
	}

	// Both audit rows carry the operator who issued the invite.
	createdRow, ok := findAuditAction(st.Audit(30), "invite.created")
	if !ok {
		t.Fatal("missing invite.created audit")
	}
	redeemedRow, ok := findAuditAction(st.Audit(30), "invite.redeemed")
	if !ok {
		t.Fatal("missing invite.redeemed audit")
	}
	if createdRow.Actor != "alice" || redeemedRow.Actor != "alice" {
		t.Fatalf("invite actors = %q / %q, want alice", createdRow.Actor, redeemedRow.Actor)
	}
	if redeemedRow.Target != "new.customer@example.com" {
		t.Fatalf("invite.redeemed target = %q", redeemedRow.Target)
	}
}

func TestTenantInviteWrongCodeAndOrigin(t *testing.T) {
	ts, st := newPortalTestServer(t)

	// A wrong code is a uniform not-found, both for preview and activation.
	resp, body := portalCall(t, ts, http.MethodGet, "/v1/portal/invites/not-a-real-code", nil, "", "")
	requireStatus(t, resp, http.StatusNotFound)
	requireErrorCode(t, body, CodeNotFound)
	resp, body = portalCall(t, ts, http.MethodPost, "/v1/portal/invites/not-a-real-code/activate",
		model.TenantInviteActivateRequest{Password: portalTestPassword}, "", "")
	requireStatus(t, resp, http.StatusNotFound)
	requireErrorCode(t, body, CodeNotFound)
	if got := len(st.Tenants()); got != 0 {
		t.Fatalf("wrong code created %d tenants", got)
	}

	// Activation is unsafe and must carry the configured Origin.
	created := decodeBody[model.TenantInviteCreated](t, func() []byte {
		resp, body := apiCall(t, ts, http.MethodPost, "/v1/admin/tenants/invites", model.TenantInviteCreateRequest{
			Email: "origin@example.com", DisplayName: "Origin",
		})
		requireStatus(t, resp, http.StatusCreated)
		return body
	}())
	req := newRequest(t, http.MethodPost, ts.URL+"/v1/portal/invites/"+created.Code+"/activate", model.TenantInviteActivateRequest{
		Password: portalTestPassword,
	})
	resp, body = doRequest(t, ts, req)
	requireStatus(t, resp, http.StatusForbidden)
	requireErrorCode(t, body, CodeForbidden)
	if got := len(st.Tenants()); got != 0 {
		t.Fatalf("missing-origin activation created %d tenants", got)
	}
}

func TestTenantInviteRateLimited(t *testing.T) {
	ts, _ := newPortalTestServer(t)
	for i := 0; i < portalInviteCodeLimit; i++ {
		resp, _ := portalCall(t, ts, http.MethodGet, "/v1/portal/invites/wrong-code", nil, "", "")
		requireStatus(t, resp, http.StatusNotFound)
	}
	resp, body := portalCall(t, ts, http.MethodGet, "/v1/portal/invites/wrong-code", nil, "", "")
	requireStatus(t, resp, http.StatusTooManyRequests)
	requireErrorCode(t, body, CodeRateLimited)
	if resp.Header.Get("Retry-After") == "" {
		t.Fatal("rate-limited invitation check has no Retry-After")
	}
}
