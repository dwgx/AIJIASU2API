package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"ajsaccountd/internal/model"
)

const tenantTestPassword = "correct horse battery staple"

func newTenantStore(t *testing.T) (*Store, *time.Time) {
	t.Helper()
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	return newEmptyWithClock(clock, nil), &now
}

func signupTestTenant(t *testing.T, st *Store, email, name string) model.Tenant {
	t.Helper()
	if err := st.SignupTenant(model.TenantSignupInput{
		Email: email, Password: tenantTestPassword, DisplayName: name,
	}); err != nil {
		t.Fatalf("signup %s: %v", email, err)
	}
	canonical, err := canonicalTenantEmail(email)
	if err != nil {
		t.Fatalf("canonical email %q: %v", email, err)
	}
	for _, tenant := range st.Tenants() {
		if tenant.Email == canonical {
			return tenant
		}
	}
	t.Fatalf("signup %s did not store a tenant", email)
	return model.Tenant{}
}

func approveTestTenant(t *testing.T, st *Store, tenantID, userID string) model.Tenant {
	t.Helper()
	tenant, err := st.ApproveTenant(tenantID, userID, nil)
	if err != nil {
		t.Fatalf("approve %s for %s: %v", tenantID, userID, err)
	}
	return tenant
}

func TestSignupTenantIsPendingAndIdempotent(t *testing.T) {
	st, _ := newTenantStore(t)
	tenant := signupTestTenant(t, st, "  Ada@Example.COM ", "Ada Lovelace")
	if tenant.Status != model.TenantPending {
		t.Fatalf("status = %q, want pending", tenant.Status)
	}
	if tenant.Email != "ada@example.com" {
		t.Fatalf("email = %q, want canonical lowercase", tenant.Email)
	}
	if tenant.UserID != nil || tenant.ApprovedAt != nil || tenant.SuspendedAt != nil {
		t.Fatalf("pending tenant carries link/timestamps: %+v", tenant)
	}
	if len(st.Users()) != 0 || len(st.Keys()) != 0 {
		t.Fatalf("signup created a user or key")
	}

	// A duplicate with different case and spacing is accepted and changes
	// nothing; the API returns the same 202 either way.
	before := len(st.Tenants())
	if err := st.SignupTenant(model.TenantSignupInput{
		Email: "ADA@example.com", Password: "another valid password", DisplayName: "Other",
	}); err != nil {
		t.Fatalf("duplicate signup: %v", err)
	}
	if got := len(st.Tenants()); got != before {
		t.Fatalf("duplicate signup added a row: %d -> %d", before, got)
	}

	// Validation failures are reported, not silently accepted.
	if err := st.SignupTenant(model.TenantSignupInput{
		Email: "short@example.com", Password: "short", DisplayName: "Short",
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("short password error = %v, want ErrInvalid", err)
	}
	if err := st.SignupTenant(model.TenantSignupInput{
		Email: "not-an-email", Password: tenantTestPassword, DisplayName: "No",
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad email error = %v, want ErrInvalid", err)
	}
}

func TestAuthenticateTenantStatuses(t *testing.T) {
	st, _ := newTenantStore(t)
	tenant := signupTestTenant(t, st, "user@example.com", "User")

	got, ok := st.AuthenticateTenant("USER@example.com", tenantTestPassword)
	if !ok || got.ID != tenant.ID {
		t.Fatalf("pending login verify = %v/%q, want the tenant", ok, got.ID)
	}
	if _, ok := st.AuthenticateTenant("user@example.com", "wrong password value"); ok {
		t.Fatalf("wrong password verified")
	}
	if _, ok := st.AuthenticateTenant("missing@example.com", tenantTestPassword); ok {
		t.Fatalf("unknown email verified")
	}
}

func TestApproveTenantBindsExactlyOneUser(t *testing.T) {
	st, _ := newTenantStore(t)
	user, err := st.CreateUser(model.CreateUserInput{Name: "bound-user"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	tenant := signupTestTenant(t, st, "one@example.com", "One")
	active := approveTestTenant(t, st, tenant.ID, user.ID)
	if active.Status != model.TenantActive || active.UserID == nil || *active.UserID != user.ID {
		t.Fatalf("approval result = %+v, want active link to %s", active, user.ID)
	}
	if active.ApprovedAt == nil || active.SuspendedAt != nil {
		t.Fatalf("approval timestamps = %+v", active)
	}

	// A second tenant cannot bind the same user, and it stays pending.
	other := signupTestTenant(t, st, "two@example.com", "Two")
	if _, err := st.ApproveTenant(other.ID, user.ID, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate bind error = %v, want ErrConflict", err)
	}
	if got, _ := st.Tenant(other.ID); got.Status != model.TenantPending || got.UserID != nil {
		t.Fatalf("failed bind mutated tenant: %+v", got)
	}

	// Creating the user and the link is one commit.
	third := signupTestTenant(t, st, "three@example.com", "Three")
	created, err := st.ApproveTenant(third.ID, "", &model.CreateUserInput{Name: "fresh-tenant"})
	if err != nil {
		t.Fatalf("approve with create: %v", err)
	}
	if created.UserID == nil {
		t.Fatalf("create approval returned no user id")
	}
	if _, ok := st.User(*created.UserID); !ok {
		t.Fatalf("created user %q missing", *created.UserID)
	}

	// A failed create leaves the tenant pending and adds no user.
	usersBefore := len(st.Users())
	fourth := signupTestTenant(t, st, "four@example.com", "Four")
	if _, err := st.ApproveTenant(fourth.ID, "", &model.CreateUserInput{Name: "bound-user"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate-name create error = %v, want ErrConflict", err)
	}
	if got, _ := st.Tenant(fourth.ID); got.Status != model.TenantPending {
		t.Fatalf("failed create activated tenant: %+v", got)
	}
	if got := len(st.Users()); got != usersBefore {
		t.Fatalf("failed create added a user: %d -> %d", usersBefore, got)
	}

	// Exactly one of userId/user is required.
	if _, err := st.ApproveTenant(fourth.ID, "", nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty approval error = %v, want ErrInvalid", err)
	}
	if _, err := st.ApproveTenant(fourth.ID, user.ID, &model.CreateUserInput{Name: "both"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("double approval error = %v, want ErrInvalid", err)
	}
}

func TestPortalSessionLifecycle(t *testing.T) {
	st, now := newTenantStore(t)
	user, err := st.CreateUser(model.CreateUserInput{Name: "session-user"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	tenant := approveTestTenant(t, st, signupTestTenant(t, st, "session@example.com", "Session").ID, user.ID)

	cookie, csrf, err := st.CreatePortalSession(tenant.ID)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if len(cookie) != 64 || len(csrf) != 64 || cookie == csrf {
		t.Fatalf("tokens are not two distinct 32-byte hex values")
	}
	session, gotTenant, gotUser, err := st.AuthenticatePortalSession(cookie, *now)
	if err != nil {
		t.Fatalf("authenticate session: %v", err)
	}
	if session.IDHash != portalHash(cookie) || gotTenant.ID != tenant.ID || gotUser.ID != user.ID {
		t.Fatalf("authenticated wrong principal")
	}
	raw, err := st.EnsurePortalCSRF(session.IDHash)
	if err != nil || raw != csrf {
		t.Fatalf("ensure csrf = %q/%v, want the login token", raw, err)
	}
	if _, _, _, err := st.AuthenticatePortalSession("deadbeef", *now); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("unknown cookie error = %v, want ErrUnauthorized", err)
	}

	// Idle expiry is 30 minutes, absolute expiry 8 hours.
	*now = now.Add(PortalSessionIdleTTL + time.Minute)
	if _, _, _, err := st.AuthenticatePortalSession(cookie, *now); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("idle-expired cookie error = %v, want ErrUnauthorized", err)
	}

	cookie, _, err = st.CreatePortalSession(tenant.ID)
	if err != nil {
		t.Fatalf("create second session: %v", err)
	}
	*now = now.Add(PortalSessionAbsoluteTTL + time.Second)
	if _, _, _, err := st.AuthenticatePortalSession(cookie, *now); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("absolute-expired cookie error = %v, want ErrUnauthorized", err)
	}
}

func TestPortalSessionRevocationSurvivesRestore(t *testing.T) {
	st, now := newTenantStore(t)
	user, _ := st.CreateUser(model.CreateUserInput{Name: "restore-user"})
	tenant := approveTestTenant(t, st, signupTestTenant(t, st, "restore@example.com", "Restore").ID, user.ID)
	cookie, csrf, err := st.CreatePortalSession(tenant.ID)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	data, err := st.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if bytes.Contains(data, []byte(cookie)) {
		t.Fatal("snapshot persisted the raw cookie")
	}
	if bytes.Contains(data, []byte(csrf)) {
		t.Fatal("snapshot persisted the raw csrf token")
	}
	if !bytes.Contains(data, []byte(portalHash(cookie))) {
		t.Fatal("snapshot did not persist the cookie hash")
	}

	restored := newEmptyWithClock(func() time.Time { return *now }, nil)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, _, _, err := restored.AuthenticatePortalSession(cookie, *now); err != nil {
		t.Fatalf("live session did not survive restore: %v", err)
	}
	// The raw CSRF token is process-only; a restore rotates it on the next
	// session read instead of accepting the old value forever.
	if _, err := restored.EnsurePortalCSRF(portalHash(cookie)); err != nil {
		t.Fatalf("ensure csrf after restore: %v", err)
	}

	if err := restored.RevokePortalSession(portalHash(cookie)); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	data, err = restored.Snapshot()
	if err != nil {
		t.Fatalf("snapshot after revoke: %v", err)
	}
	again := newEmptyWithClock(func() time.Time { return *now }, nil)
	if err := again.Restore(data); err != nil {
		t.Fatalf("restore after revoke: %v", err)
	}
	if _, _, _, err := again.AuthenticatePortalSession(cookie, *now); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("revoked cookie after restore error = %v, want ErrUnauthorized", err)
	}
}

func TestSuspendResumeRevokesSessionsAndKeys(t *testing.T) {
	st, now := newTenantStore(t)
	user, _ := st.CreateUser(model.CreateUserInput{Name: "suspend-user"})
	tenant := approveTestTenant(t, st, signupTestTenant(t, st, "suspend@example.com", "Suspend").ID, user.ID)
	cookie, _, err := st.CreatePortalSession(tenant.ID)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	key, _, err := st.CreateTenantKey(user.ID, "CI")
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	scoped, _, err := st.CreateScopedKey(model.ScopedKeyCreateRequest{
		UserID: user.ID, Label: "service", Scopes: []model.Scope{model.ScopeUsageRead},
	})
	if err != nil {
		t.Fatalf("create scoped key: %v", err)
	}

	suspended, err := st.SuspendTenant(tenant.ID)
	if err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if suspended.Status != model.TenantSuspended || suspended.SuspendedAt == nil {
		t.Fatalf("suspend result = %+v", suspended)
	}
	if got, _ := st.User(user.ID); got.Enabled {
		t.Fatalf("suspended user is still enabled")
	}
	if _, _, _, err := st.AuthenticatePortalSession(cookie, *now); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("session after suspend error = %v, want ErrUnauthorized", err)
	}
	if got, _ := st.Key(key.ID); got.RevokedAt == nil {
		t.Fatalf("suspend did not revoke key %s", key.ID)
	}
	if got, _ := st.ScopedKey(scoped.ID); got.RevokedAt == nil {
		t.Fatalf("suspend did not revoke scoped key %s", scoped.ID)
	}
	// Idempotent retry.
	if _, err := st.SuspendTenant(tenant.ID); err != nil {
		t.Fatalf("second suspend: %v", err)
	}

	resumed, err := st.ResumeTenant(tenant.ID)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if resumed.Status != model.TenantActive || resumed.SuspendedAt != nil {
		t.Fatalf("resume result = %+v", resumed)
	}
	if got, _ := st.User(user.ID); !got.Enabled {
		t.Fatalf("resumed user is still disabled")
	}
	if got, _ := st.Key(key.ID); got.RevokedAt == nil {
		t.Fatalf("resume un-revoked key %s", key.ID)
	}
	if _, _, _, err := st.AuthenticatePortalSession(cookie, *now); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("old session after resume error = %v, want ErrUnauthorized", err)
	}
	if _, _, err := st.CreatePortalSession(tenant.ID); err != nil {
		t.Fatalf("new session after resume: %v", err)
	}
}

func TestPortalSessionCapPerTenant(t *testing.T) {
	st, _ := newTenantStore(t)
	user, _ := st.CreateUser(model.CreateUserInput{Name: "session-cap"})
	tenant := approveTestTenant(t, st, signupTestTenant(t, st, "cap@example.com", "Cap").ID, user.ID)
	for range MaxTenantSessions + 3 {
		if _, _, err := st.CreatePortalSession(tenant.ID); err != nil {
			t.Fatalf("create session: %v", err)
		}
	}
	if got := len(st.portalSessions); got != MaxTenantSessions {
		t.Fatalf("sessions = %d, want the per-tenant cap %d", got, MaxTenantSessions)
	}
}

func TestTenantKeyOwnershipAndCap(t *testing.T) {
	st, _ := newTenantStore(t)
	owner, _ := st.CreateUser(model.CreateUserInput{Name: "key-owner"})
	other, _ := st.CreateUser(model.CreateUserInput{Name: "key-other"})

	key, secret, err := st.CreateTenantKey(owner.ID, "laptop")
	if err != nil {
		t.Fatalf("create tenant key: %v", err)
	}
	if !strings.HasPrefix(secret, "ajs_live_") {
		t.Fatalf("secret prefix = %q", secret)
	}
	if _, ok := st.SecretHash(key.ID); !ok {
		t.Fatalf("key hash was not stored")
	}
	if err := st.RevokeTenantKey(other.ID, key.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign revoke error = %v, want ErrNotFound", err)
	}
	if got, _ := st.Key(key.ID); got.RevokedAt != nil {
		t.Fatalf("foreign revoke changed the key")
	}
	if err := st.RevokeTenantKey(owner.ID, key.ID); err != nil {
		t.Fatalf("owner revoke: %v", err)
	}
	if got, _ := st.Key(key.ID); got.RevokedAt == nil {
		t.Fatalf("owner revoke did not mark the key")
	}

	for i := 0; i < MaxTenantKeys; i++ {
		if _, _, err := st.CreateTenantKey(owner.ID, "bulk"); err != nil {
			t.Fatalf("create key %d: %v", i, err)
		}
	}
	if _, _, err := st.CreateTenantKey(owner.ID, "over-cap"); !errors.Is(err, ErrQuota) {
		t.Fatalf("over-cap error = %v, want ErrQuota", err)
	}
}

func TestSnapshotQuarantinesBrokenTenants(t *testing.T) {
	st, now := newTenantStore(t)
	user, _ := st.CreateUser(model.CreateUserInput{Name: "quarantine-user"})
	stamp := iso(*now)
	snap := Snapshot{
		Version: SnapshotVersion,
		Users:   []model.PanelUser{{ID: user.ID, Name: user.Name, Enabled: true, CreatedAt: stamp}},
		Tenants: []tenantRecord{
			{
				Tenant: model.Tenant{
					ID: "t-01", Email: "broken@example.com", DisplayName: "Broken",
					Status: model.TenantActive, UserID: ptrString(user.ID),
					CreatedAt: stamp, ApprovedAt: ptrString(stamp),
				},
				PasswordHash: "not-a-phc-hash",
			},
			{
				Tenant: model.Tenant{
					ID: "t-02", Email: "dupe@example.com", DisplayName: "Dupe A",
					Status: model.TenantPending, CreatedAt: stamp,
				},
				PasswordHash: "$argon2id$v=19$m=65536,t=3,p=2$c2FsdHNhbHRzYWx0$" + strings.Repeat("a", 43),
			},
			{
				Tenant: model.Tenant{
					ID: "t-03", Email: "dupe@example.com", DisplayName: "Dupe B",
					Status: model.TenantPending, CreatedAt: stamp,
				},
				PasswordHash: "$argon2id$v=19$m=65536,t=3,p=2$c2FsdHNhbHRzYWx0$" + strings.Repeat("a", 43),
			},
		},
	}
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	restored := newEmptyWithClock(func() time.Time { return *now }, nil)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := len(restored.Tenants()); got != 3 {
		t.Fatalf("tenants = %d, want 3 (quarantined rows stay visible)", got)
	}
	if _, ok := restored.AuthenticateTenant("broken@example.com", tenantTestPassword); ok {
		t.Fatalf("malformed hash authenticated")
	}
	if _, ok := restored.AuthenticateTenant("dupe@example.com", tenantTestPassword); ok {
		t.Fatalf("duplicate email authenticated")
	}
	if _, _, err := restored.CreatePortalSession("t-01"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("quarantined tenant session error = %v, want ErrUnauthorized", err)
	}
}

func TestOldSnapshotLoadsWithNoTenants(t *testing.T) {
	st, _ := newTenantStore(t)
	snap := Snapshot{
		Version: SnapshotVersion,
		Users: []model.PanelUser{{
			ID: "u-01", Name: "legacy-user", Enabled: true,
			CreatedAt: "2025-01-01T00:00:00Z",
		}},
		KeyHashes: map[string]string{},
		Secrets:   map[string][]byte{},
	}
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := st.Restore(data); err != nil {
		t.Fatalf("restore old snapshot: %v", err)
	}
	if got := len(st.Tenants()); got != 0 {
		t.Fatalf("tenants = %d, want 0", got)
	}
	if _, ok := st.User("u-01"); !ok {
		t.Fatalf("existing user was not preserved")
	}
}

func createTestInvite(t *testing.T, st *Store, email, name string) model.TenantInviteCreated {
	t.Helper()
	created, err := st.CreateTenantInvite(model.TenantInviteCreateRequest{
		Email: email, DisplayName: name,
	}, "alice")
	if err != nil {
		t.Fatalf("create invite %s: %v", email, err)
	}
	return created
}

func TestTenantInviteCodeIsShownOnceAndStoredHashed(t *testing.T) {
	st, now := newTenantStore(t)
	created := createTestInvite(t, st, "  Invitee@Example.COM ", "Invitee")
	if len(created.Code) != 64 {
		t.Fatalf("code length = %d, want 64 hex characters", len(created.Code))
	}
	if created.Invite.Email != "invitee@example.com" || created.Invite.DisplayName != "Invitee" {
		t.Fatalf("invite = %+v", created.Invite)
	}
	if created.Invite.Status != model.TenantInvitePending || created.Invite.RedeemedAt != nil || created.Invite.TenantID != nil {
		t.Fatalf("new invite status = %+v", created.Invite)
	}
	expires, err := time.Parse(time.RFC3339Nano, created.Invite.ExpiresAt)
	if err != nil {
		t.Fatalf("expiresAt: %v", err)
	}
	if got := expires.Sub(*now); got != TenantInviteTTL {
		t.Fatalf("ttl = %v, want %v", got, TenantInviteTTL)
	}

	// The raw code never reaches the snapshot; only its digest does.
	data, err := st.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if bytes.Contains(data, []byte(created.Code)) {
		t.Fatal("snapshot persisted the raw invite code")
	}
	if !bytes.Contains(data, []byte(portalHash(created.Code))) {
		t.Fatal("snapshot did not persist the invite code hash")
	}

	view, err := st.PreviewTenantInvite(created.Code)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if view.Email != "invitee@example.com" || view.DisplayName != "Invitee" || view.ExpiresAt != created.Invite.ExpiresAt {
		t.Fatalf("preview = %+v", view)
	}

	row, ok := findStoreAudit(st.Audit(20), "invite.created")
	if !ok {
		t.Fatal("no invite.created audit row")
	}
	if row.Actor != "alice" || row.Target != "invitee@example.com" {
		t.Fatalf("invite.created row = %+v", row)
	}
}

func TestTenantInviteRedeemIsSingleUse(t *testing.T) {
	st, _ := newTenantStore(t)
	created := createTestInvite(t, st, "redeem@example.com", "")
	tenant, err := st.RedeemTenantInvite(created.Code, model.TenantInviteActivateRequest{
		Password: tenantTestPassword, DisplayName: "Redeemed User",
	})
	if err != nil {
		t.Fatalf("redeem: %v", err)
	}
	if tenant.Status != model.TenantPending || tenant.UserID != nil {
		t.Fatalf("redeemed tenant = %+v, want pending with no user", tenant)
	}
	if got, ok := st.AuthenticateTenant("redeem@example.com", tenantTestPassword); !ok || got.ID != tenant.ID {
		t.Fatalf("activated password did not verify: %v/%q", ok, got.ID)
	}

	invites := st.TenantInvites()
	if len(invites) != 1 || invites[0].Status != model.TenantInviteRedeemed || invites[0].TenantID == nil || *invites[0].TenantID != tenant.ID {
		t.Fatalf("invite after redeem = %+v", invites)
	}
	if _, err := st.PreviewTenantInvite(created.Code); !errors.Is(err, ErrInviteRedeemed) {
		t.Fatalf("preview after redeem = %v, want ErrInviteRedeemed", err)
	}
	if _, err := st.RedeemTenantInvite(created.Code, model.TenantInviteActivateRequest{
		Password: tenantTestPassword, DisplayName: "Second User",
	}); !errors.Is(err, ErrInviteRedeemed) {
		t.Fatalf("second redeem = %v, want ErrInviteRedeemed", err)
	}
	if got := len(st.Tenants()); got != 1 {
		t.Fatalf("second redeem created a tenant: %d", got)
	}

	row, ok := findStoreAudit(st.Audit(20), "invite.redeemed")
	if !ok {
		t.Fatal("no invite.redeemed audit row")
	}
	if row.Actor != "alice" || row.Target != "redeem@example.com" {
		t.Fatalf("invite.redeemed row = %+v", row)
	}
}

func TestTenantInviteExpiryAndWrongCode(t *testing.T) {
	st, now := newTenantStore(t)
	if _, err := st.PreviewTenantInvite("not-a-real-code"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong preview = %v, want ErrNotFound", err)
	}
	if _, err := st.RedeemTenantInvite("not-a-real-code", model.TenantInviteActivateRequest{
		Password: tenantTestPassword, DisplayName: "Nobody",
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong redeem = %v, want ErrNotFound", err)
	}

	created := createTestInvite(t, st, "expire@example.com", "Expiring")
	*now = now.Add(TenantInviteTTL)
	if _, err := st.PreviewTenantInvite(created.Code); !errors.Is(err, ErrInviteExpired) {
		t.Fatalf("expired preview = %v, want ErrInviteExpired", err)
	}
	if _, err := st.RedeemTenantInvite(created.Code, model.TenantInviteActivateRequest{
		Password: tenantTestPassword, DisplayName: "Too Late",
	}); !errors.Is(err, ErrInviteExpired) {
		t.Fatalf("expired redeem = %v, want ErrInviteExpired", err)
	}
	if got := len(st.Tenants()); got != 0 {
		t.Fatalf("expired invite created %d tenants", got)
	}
}

func TestTenantInviteRejectsDuplicateAddresses(t *testing.T) {
	st, _ := newTenantStore(t)
	created := createTestInvite(t, st, "dup@example.com", "First")
	if _, err := st.CreateTenantInvite(model.TenantInviteCreateRequest{
		Email: "DUP@example.com", DisplayName: "Second",
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("second pending invite = %v, want ErrConflict", err)
	}
	if _, err := st.RedeemTenantInvite(created.Code, model.TenantInviteActivateRequest{
		Password: tenantTestPassword, DisplayName: "First",
	}); err != nil {
		t.Fatalf("redeem: %v", err)
	}
	if _, err := st.CreateTenantInvite(model.TenantInviteCreateRequest{
		Email: "dup@example.com", DisplayName: "Third",
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("invite for existing tenant = %v, want ErrConflict", err)
	}
}

func TestTenantInviteSurvivesSnapshotAndRestore(t *testing.T) {
	st, now := newTenantStore(t)
	created := createTestInvite(t, st, "roundtrip@example.com", "Round Trip")
	data, err := st.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	restored := newEmptyWithClock(func() time.Time { return *now }, nil)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, err := restored.PreviewTenantInvite(created.Code); err != nil {
		t.Fatalf("preview after restore: %v", err)
	}
	tenant, err := restored.RedeemTenantInvite(created.Code, model.TenantInviteActivateRequest{
		Password: tenantTestPassword, DisplayName: "Round Trip",
	})
	if err != nil {
		t.Fatalf("redeem after restore: %v", err)
	}
	if tenant.Status != model.TenantPending {
		t.Fatalf("restored tenant = %+v", tenant)
	}
}

func findStoreAudit(events []model.AuditEvent, action string) (model.AuditEvent, bool) {
	for _, event := range events {
		if event.Action == action {
			return event, true
		}
	}
	return model.AuditEvent{}, false
}
