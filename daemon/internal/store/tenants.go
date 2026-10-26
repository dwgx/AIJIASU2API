package store

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"sort"
	"strings"
	"sync"
	"time"

	"ajsaccountd/internal/model"

	"golang.org/x/crypto/argon2"
)

// Portal errors. They are deliberately separate from ErrInvalid/ErrNotFound so
// the tenant auth path can answer a uniform 401 and a status refusal with 403
// without inventing a reason an attacker could use to enumerate accounts.
var (
	ErrUnauthorized   = errors.New("unauthorized")
	ErrForbidden      = errors.New("forbidden")
	ErrInviteExpired  = errors.New("invite expired")
	ErrInviteRedeemed = errors.New("invite already redeemed")
)

// Portal session and tenant bounds. The absolute expiry is the cookie lifetime;
// the idle expiry is re-checked on every request. The key cap counts active
// (non-revoked) keys only.
const (
	PortalSessionAbsoluteTTL = 8 * time.Hour
	PortalSessionIdleTTL     = 30 * time.Minute
	MaxTenantSessions        = 8
	MaxTenantKeys            = 10
	MaxPortalSessions        = 1000

	MinTenantPasswordLen = 12
	MaxTenantPasswordLen = 256
	MaxTenantEmailLen    = 254
	MaxTenantNameLen     = 80

	// TenantInviteTTL is the fixed lifetime of an onboarding code. The code is
	// single-use and stored only as a SHA-256 digest; the admin UI shows it
	// once at creation because the daemon cannot recover it later.
	TenantInviteTTL = 72 * time.Hour
	// MaxTenantInvites bounds the retained invitation history. Redemption
	// requires scanning this slice, so an unbounded list would turn an admin
	// action into a linear scan of arbitrary size.
	MaxTenantInvites = 1000
)

// Argon2id parameters. The encoded hash carries them, so a future change
// verifies old rows with their original cost. Bounds in parsePasswordHash keep
// a hand-edited snapshot from requesting an unbounded allocation.
const (
	tenantArgonMemory  = 64 * 1024 // KiB
	tenantArgonTime    = 3
	tenantArgonThreads = 2
	tenantArgonKeyLen  = 32
	tenantArgonSaltLen = 16
)

// tenantHashConcurrency bounds simultaneous Argon2 calls. Hashing is the most
// expensive work a portal request can trigger, so a burst must not pin every
// core or allocate 64 MiB per concurrent signup/login.
const tenantHashConcurrency = 2

// tenantRecord is the store-only tenant row. The embedded model.Tenant is the
// wire contract; PasswordHash and invalid never reach a response. invalid is
// process-only: a snapshot is re-validated on every restore.
type tenantRecord struct {
	model.Tenant
	PasswordHash string `json:"passwordHash"`
	invalid      string
}

// tenantInviteRecord is the store-only invitation row. CodeHash is the
// SHA-256 digest of the one-time activation code; the raw code exists only in
// the create response. invalid is process-only like tenantRecord.invalid.
type tenantInviteRecord struct {
	model.TenantInvite
	CodeHash string `json:"codeHash"`
	invalid  string
}

// SignupTenant stores a pending tenant. A duplicate canonical email is
// accepted and produces no change; the caller returns the same 202 either way.
// Password hashing happens outside the store lock, and a duplicate is
// detected again after hashing so two concurrent signups cannot both insert.
func (s *Store) SignupTenant(in model.TenantSignupInput) error {
	email, err := canonicalTenantEmail(in.Email)
	if err != nil {
		return err
	}
	if err := validateTenantPassword(in.Password); err != nil {
		return err
	}
	name, err := canonicalTenantName(in.DisplayName)
	if err != nil {
		return err
	}

	s.mu.RLock()
	exists := s.tenantEmailIndexLocked(email) >= 0
	s.mu.RUnlock()
	if exists {
		// Equal work for a duplicate: an attacker must not learn from timing
		// that the address is already registered.
		s.dummyTenantVerify(in.Password)
		return nil
	}

	hash, err := s.hashTenantPassword(in.Password)
	if err != nil {
		return err
	}

	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tenantEmailIndexLocked(email) >= 0 {
		// The hash above already paid the same cost as a dummy verify.
		return nil
	}
	s.tenantSeq++
	rec := tenantRecord{
		Tenant: model.Tenant{
			ID:          fmt.Sprintf("t-%02d", s.tenantSeq),
			Email:       email,
			DisplayName: name,
			Status:      model.TenantPending,
			CreatedAt:   iso(now),
		},
		PasswordHash: hash,
	}
	s.tenants = append([]tenantRecord{rec}, s.tenants...)
	s.addAuditLocked(model.SeverityInfo, "portal", "tenant.signup", email, "pending approval")
	return nil
}

// CreateTenantInvite stores one single-use onboarding code for an email. The
// raw code is returned exactly once; only its SHA-256 digest is persisted. A
// tenant that already exists and an unexpired pending invitation for the same
// address are both refused so a second code cannot race the first one.
func (s *Store) CreateTenantInvite(in model.TenantInviteCreateRequest, actor ...string) (model.TenantInviteCreated, error) {
	email, err := canonicalTenantEmail(in.Email)
	if err != nil {
		return model.TenantInviteCreated{}, err
	}
	name := ""
	if strings.TrimSpace(in.DisplayName) != "" {
		name, err = canonicalTenantName(in.DisplayName)
		if err != nil {
			return model.TenantInviteCreated{}, err
		}
	}
	code, codeHash, err := newPortalToken()
	if err != nil {
		return model.TenantInviteCreated{}, fmt.Errorf("generate tenant invite: %w", err)
	}

	now := s.now()
	expires := now.Add(TenantInviteTTL)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tenantEmailIndexLocked(email) >= 0 {
		return model.TenantInviteCreated{}, conflictf("email %q is already registered", email)
	}
	for i := range s.tenantInvites {
		rec := &s.tenantInvites[i]
		if rec.Email != email || rec.invalid != "" {
			continue
		}
		if tenantInviteStatus(*rec, now) == model.TenantInvitePending {
			return model.TenantInviteCreated{}, conflictf("an active invitation already exists for %q", email)
		}
	}

	s.tenantInviteSeq++
	rec := tenantInviteRecord{
		TenantInvite: model.TenantInvite{
			ID:          fmt.Sprintf("inv-%02d", s.tenantInviteSeq),
			Email:       email,
			DisplayName: name,
			Status:      model.TenantInvitePending,
			CreatedAt:   iso(now),
			ExpiresAt:   iso(expires),
			CreatedBy:   auditActorArg(actor),
		},
		CodeHash: codeHash,
	}
	s.tenantInvites = append([]tenantInviteRecord{rec}, s.tenantInvites...)
	if len(s.tenantInvites) > MaxTenantInvites {
		s.tenantInvites = s.tenantInvites[:MaxTenantInvites]
	}
	s.addAuditLocked(model.SeverityInfo, rec.CreatedBy, "invite.created", email,
		"one-time activation code issued, expires "+rec.ExpiresAt,
		map[string]string{"inviteId": rec.ID, "expiresAt": rec.ExpiresAt})
	return model.TenantInviteCreated{Invite: cloneTenantInvite(rec.TenantInvite), Code: code}, nil
}

// TenantInvites returns every invitation, newest first. Status is derived at
// read time so an expired row cannot keep a stale "pending" label.
func (s *Store) TenantInvites() []model.TenantInvite {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := s.now()
	out := make([]model.TenantInvite, len(s.tenantInvites))
	for i, rec := range s.tenantInvites {
		view := cloneTenantInvite(rec.TenantInvite)
		view.Status = tenantInviteStatus(rec, now)
		out[i] = view
	}
	return out
}

// PreviewTenantInvite validates a code without consuming it. Wrong codes are
// indistinguishable from unknown ones; an expired or redeemed code reports a
// stable reason so the activation page can explain the failure.
func (s *Store) PreviewTenantInvite(code string) (model.TenantInviteView, error) {
	code = strings.TrimSpace(code)
	if code == "" || len(code) > 128 {
		return model.TenantInviteView{}, notFoundf("invitation not found")
	}
	hash := portalHash(code)
	s.mu.RLock()
	defer s.mu.RUnlock()
	idx := s.tenantInviteIndexLocked(hash)
	if idx < 0 || s.tenantInvites[idx].invalid != "" {
		return model.TenantInviteView{}, notFoundf("invitation not found")
	}
	rec := &s.tenantInvites[idx]
	switch tenantInviteStatus(*rec, s.now()) {
	case model.TenantInviteRedeemed:
		return model.TenantInviteView{}, ErrInviteRedeemed
	case model.TenantInviteExpired:
		return model.TenantInviteView{}, ErrInviteExpired
	}
	return model.TenantInviteView{
		Email:       rec.Email,
		DisplayName: rec.DisplayName,
		ExpiresAt:   rec.ExpiresAt,
	}, nil
}

// RedeemTenantInvite consumes one code and creates a pending tenant with the
// chosen password. Hashing happens outside the store lock; the single-use
// check is repeated after re-acquiring it so two concurrent redemptions cannot
// both create a tenant.
func (s *Store) RedeemTenantInvite(code string, in model.TenantInviteActivateRequest) (model.Tenant, error) {
	code = strings.TrimSpace(code)
	if code == "" || len(code) > 128 {
		return model.Tenant{}, notFoundf("invitation not found")
	}
	if err := validateTenantPassword(in.Password); err != nil {
		return model.Tenant{}, err
	}

	s.mu.RLock()
	idx := s.tenantInviteIndexLocked(portalHash(code))
	if idx < 0 || s.tenantInvites[idx].invalid != "" {
		s.mu.RUnlock()
		return model.Tenant{}, notFoundf("invitation not found")
	}
	rec := s.tenantInvites[idx]
	switch tenantInviteStatus(rec, s.now()) {
	case model.TenantInviteRedeemed:
		s.mu.RUnlock()
		return model.Tenant{}, ErrInviteRedeemed
	case model.TenantInviteExpired:
		s.mu.RUnlock()
		return model.Tenant{}, ErrInviteExpired
	}
	s.mu.RUnlock()

	name := strings.TrimSpace(in.DisplayName)
	if name == "" {
		name = rec.DisplayName
	}
	name, err := canonicalTenantName(name)
	if err != nil {
		return model.Tenant{}, err
	}
	hash, err := s.hashTenantPassword(in.Password)
	if err != nil {
		return model.Tenant{}, err
	}

	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	idx = s.tenantInviteIndexLocked(portalHash(code))
	if idx < 0 || s.tenantInvites[idx].invalid != "" {
		return model.Tenant{}, notFoundf("invitation not found")
	}
	recPtr := &s.tenantInvites[idx]
	switch tenantInviteStatus(*recPtr, now) {
	case model.TenantInviteRedeemed:
		return model.Tenant{}, ErrInviteRedeemed
	case model.TenantInviteExpired:
		return model.Tenant{}, ErrInviteExpired
	}
	if s.tenantEmailIndexLocked(recPtr.Email) >= 0 {
		return model.Tenant{}, conflictf("email %q is already registered", recPtr.Email)
	}

	s.tenantSeq++
	tenant := tenantRecord{
		Tenant: model.Tenant{
			ID:          fmt.Sprintf("t-%02d", s.tenantSeq),
			Email:       recPtr.Email,
			DisplayName: name,
			Status:      model.TenantPending,
			CreatedAt:   iso(now),
		},
		PasswordHash: hash,
	}
	s.tenants = append([]tenantRecord{tenant}, s.tenants...)
	recPtr.RedeemedAt = ptrString(iso(now))
	recPtr.TenantID = ptrString(tenant.ID)
	recPtr.Status = model.TenantInviteRedeemed
	actor := strings.TrimSpace(recPtr.CreatedBy)
	if actor == "" {
		actor = "operator"
	}
	s.addAuditLocked(model.SeverityInfo, actor, "invite.redeemed", recPtr.Email,
		"tenant "+tenant.ID+" created pending approval",
		map[string]string{"inviteId": recPtr.ID, "tenantId": tenant.ID})
	return cloneTenant(tenant.Tenant), nil
}

// AuthenticateTenant verifies a password. The bool is false for an unknown
// email, a wrong password and a quarantined row; the caller answers a uniform
// 401. A verified pending or suspended tenant returns true so the caller can
// answer 403 with the status and issue no session.
func (s *Store) AuthenticateTenant(emailRaw, password string) (model.Tenant, bool) {
	email, err := canonicalTenantEmail(emailRaw)
	if err != nil {
		s.dummyTenantVerify(password)
		return model.Tenant{}, false
	}

	s.mu.RLock()
	idx := s.tenantEmailIndexLocked(email)
	hash := ""
	if idx >= 0 {
		hash = s.tenants[idx].PasswordHash
	}
	s.mu.RUnlock()
	if idx < 0 {
		s.dummyTenantVerify(password)
		return model.Tenant{}, false
	}
	if !s.verifyTenantPassword(hash, password) {
		return model.Tenant{}, false
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	idx = s.tenantEmailIndexLocked(email)
	if idx < 0 || s.tenants[idx].invalid != "" {
		return model.Tenant{}, false
	}
	return cloneTenant(s.tenants[idx].Tenant), true
}

// CreatePortalSession issues a new opaque session for an active tenant. Only
// the SHA-256 of the cookie value is stored; the raw cookie and CSRF token are
// returned once. The previous sessions of the tenant are bounded, and expired
// or revoked rows are pruned first.
func (s *Store) CreatePortalSession(tenantID string) (cookie string, csrf string, err error) {
	cookie, idHash, err := newPortalToken()
	if err != nil {
		return "", "", fmt.Errorf("generate portal session: %w", err)
	}
	csrf, csrfHash, err := newPortalToken()
	if err != nil {
		return "", "", fmt.Errorf("generate portal csrf token: %w", err)
	}

	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.tenantIndexLocked(tenantID)
	if idx < 0 {
		return "", "", ErrUnauthorized
	}
	rec := &s.tenants[idx]
	if rec.invalid != "" {
		return "", "", ErrUnauthorized
	}
	if rec.Status != model.TenantActive {
		return "", "", ErrForbidden
	}
	if rec.UserID == nil {
		return "", "", ErrForbidden
	}
	userIdx := s.userIndexLocked(*rec.UserID)
	if userIdx < 0 || !s.users[userIdx].Enabled {
		return "", "", ErrForbidden
	}

	s.pruneTenantSessionsLocked(tenantID, now)
	session := model.PortalSession{
		IDHash:     idHash,
		TenantID:   tenantID,
		CSRFHash:   csrfHash,
		CSRFToken:  csrf,
		CreatedAt:  iso(now),
		LastSeenAt: iso(now),
		ExpiresAt:  iso(now.Add(PortalSessionAbsoluteTTL)),
	}
	s.portalSessions = append([]model.PortalSession{session}, s.portalSessions...)
	if len(s.portalSessions) > MaxPortalSessions {
		s.portalSessions = s.portalSessions[:MaxPortalSessions]
	}
	return cookie, csrf, nil
}

// AuthenticatePortalSession re-checks every property on every request: hash,
// absolute and idle expiry, revocation, tenant status, the linked user and the
// user's kill switch. Any failure revokes the session when it still exists and
// returns ErrUnauthorized (unknown/expired) or ErrForbidden (a real session
// whose tenant or user is no longer allowed).
func (s *Store) AuthenticatePortalSession(cookie string, now time.Time) (model.PortalSession, model.Tenant, model.PanelUser, error) {
	if cookie == "" || len(cookie) > 128 {
		return model.PortalSession{}, model.Tenant{}, model.PanelUser{}, ErrUnauthorized
	}
	idHash := portalHash(cookie)

	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.sessionIndexLocked(idHash)
	if idx < 0 {
		return model.PortalSession{}, model.Tenant{}, model.PanelUser{}, ErrUnauthorized
	}
	session := &s.portalSessions[idx]
	revoke := func() {
		if session.RevokedAt == nil {
			session.RevokedAt = ptrString(iso(now))
		}
	}
	if session.RevokedAt != nil || portalSessionExpired(*session, now) {
		revoke()
		return model.PortalSession{}, model.Tenant{}, model.PanelUser{}, ErrUnauthorized
	}
	tenantIdx := s.tenantIndexLocked(session.TenantID)
	if tenantIdx < 0 {
		revoke()
		return model.PortalSession{}, model.Tenant{}, model.PanelUser{}, ErrUnauthorized
	}
	rec := &s.tenants[tenantIdx]
	if rec.invalid != "" {
		revoke()
		return model.PortalSession{}, model.Tenant{}, model.PanelUser{}, ErrUnauthorized
	}
	if rec.Status != model.TenantActive {
		revoke()
		return model.PortalSession{}, model.Tenant{}, model.PanelUser{}, ErrForbidden
	}
	if rec.UserID == nil {
		revoke()
		return model.PortalSession{}, model.Tenant{}, model.PanelUser{}, ErrForbidden
	}
	userIdx := s.userIndexLocked(*rec.UserID)
	if userIdx < 0 || !s.users[userIdx].Enabled {
		revoke()
		return model.PortalSession{}, model.Tenant{}, model.PanelUser{}, ErrForbidden
	}

	session.LastSeenAt = iso(now)
	out := *session
	return out, cloneTenant(rec.Tenant), s.withDerivedUsage(cloneUser(s.users[userIdx])), nil
}

// EnsurePortalCSRF returns the raw CSRF token for a live session. The token is
// process-only, so after a restart the first call rotates one and updates the
// stored hash; an old token then fails closed until the client re-reads
// GET /v1/portal/session.
func (s *Store) EnsurePortalCSRF(idHash string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.sessionIndexLocked(idHash)
	if idx < 0 {
		return "", ErrUnauthorized
	}
	session := &s.portalSessions[idx]
	if session.RevokedAt != nil {
		return "", ErrUnauthorized
	}
	if session.CSRFToken != "" {
		return session.CSRFToken, nil
	}
	raw, hash, err := newPortalToken()
	if err != nil {
		return "", fmt.Errorf("generate portal csrf token: %w", err)
	}
	session.CSRFToken = raw
	session.CSRFHash = hash
	return raw, nil
}

// RevokePortalSession marks one session revoked. It is idempotent and never
// reveals whether the value existed.
func (s *Store) RevokePortalSession(idHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.sessionIndexLocked(idHash)
	if idx < 0 {
		return nil
	}
	if s.portalSessions[idx].RevokedAt == nil {
		s.portalSessions[idx].RevokedAt = ptrString(iso(s.now()))
	}
	return nil
}

// Tenants returns every tenant, newest first.
func (s *Store) Tenants() []model.Tenant {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.Tenant, len(s.tenants))
	for i, rec := range s.tenants {
		out[i] = cloneTenant(rec.Tenant)
	}
	return out
}

// Tenant returns one tenant by id.
func (s *Store) Tenant(id string) (model.Tenant, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	idx := s.tenantIndexLocked(id)
	if idx < 0 {
		return model.Tenant{}, false
	}
	return cloneTenant(s.tenants[idx].Tenant), true
}

// ApproveTenant atomically binds exactly one user and activates the tenant.
// Either userID names an existing PanelUser or create supplies a validated
// CreateUserInput; the new user, the link, the status change and the audit row
// commit under one lock, so a failed bind leaves the tenant pending and creates
// nothing.
func (s *Store) ApproveTenant(tenantID, userID string, create *model.CreateUserInput, actor ...string) (model.Tenant, error) {
	tenantID = strings.TrimSpace(tenantID)
	userID = strings.TrimSpace(userID)
	if (userID == "") == (create == nil) {
		return model.Tenant{}, invalidf("supply exactly one of userId or user")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.tenantIndexLocked(tenantID)
	if idx < 0 {
		return model.Tenant{}, notFoundf("tenant %q not found", tenantID)
	}
	rec := &s.tenants[idx]
	if rec.invalid != "" {
		return model.Tenant{}, conflictf("tenant %q is quarantined: %s", rec.ID, rec.invalid)
	}
	if rec.Status != model.TenantPending {
		return model.Tenant{}, conflictf("tenant %q is already %s", rec.ID, rec.Status)
	}

	if create != nil {
		user, err := s.createUserLocked(*create, actor...)
		if err != nil {
			return model.Tenant{}, err
		}
		userID = user.ID
	} else if s.userIndexLocked(userID) < 0 {
		return model.Tenant{}, notFoundf("user %q not found", userID)
	}
	for i := range s.tenants {
		if i == idx || s.tenants[i].UserID == nil {
			continue
		}
		if *s.tenants[i].UserID == userID {
			return model.Tenant{}, conflictf("user %q is already linked to tenant %q", userID, s.tenants[i].ID)
		}
	}

	now := iso(s.now())
	rec.UserID = ptrString(userID)
	rec.Status = model.TenantActive
	rec.ApprovedAt = ptrString(now)
	rec.SuspendedAt = nil
	s.addAuditLocked(model.SeverityInfo, auditActorArg(actor), "tenant.approve", rec.Email,
		"linked user "+userID)
	return cloneTenant(rec.Tenant), nil
}

// SuspendTenant disables the linked user and revokes every session and API key
// the tenant owns in one commit. Suspending an already suspended tenant is a
// no-op so an admin retry is safe.
func (s *Store) SuspendTenant(id string, actor ...string) (model.Tenant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.tenantIndexLocked(strings.TrimSpace(id))
	if idx < 0 {
		return model.Tenant{}, notFoundf("tenant %q not found", id)
	}
	rec := &s.tenants[idx]
	if rec.invalid != "" {
		return model.Tenant{}, conflictf("tenant %q is quarantined: %s", rec.ID, rec.invalid)
	}
	switch rec.Status {
	case model.TenantSuspended:
		return cloneTenant(rec.Tenant), nil
	case model.TenantPending:
		return model.Tenant{}, conflictf("tenant %q is pending; approve it before suspending", rec.ID)
	}
	if rec.UserID == nil {
		return model.Tenant{}, conflictf("tenant %q has no linked user", rec.ID)
	}

	now := iso(s.now())
	if userIdx := s.userIndexLocked(*rec.UserID); userIdx >= 0 {
		s.users[userIdx].Enabled = false
	}
	sessionsRevoked := 0
	for i := range s.portalSessions {
		if s.portalSessions[i].TenantID == rec.ID && s.portalSessions[i].RevokedAt == nil {
			s.portalSessions[i].RevokedAt = ptrString(now)
			sessionsRevoked++
		}
	}
	keysRevoked := 0
	for i := range s.keys {
		if s.keys[i].UserID == *rec.UserID && s.keys[i].RevokedAt == nil {
			s.keys[i].RevokedAt = ptrString(now)
			keysRevoked++
		}
	}
	// Scoped keys are admin-issued service credentials, but they are still
	// owned by the linked user. Suspension revokes them too: a disabled
	// customer must not keep a live credential, and resume never un-revokes.
	for i := range s.scopedKeys {
		if s.scopedKeys[i].UserID == *rec.UserID && s.scopedKeys[i].RevokedAt == nil {
			s.scopedKeys[i].RevokedAt = ptrString(now)
			keysRevoked++
		}
	}
	rec.Status = model.TenantSuspended
	rec.SuspendedAt = ptrString(now)
	s.addAuditLocked(model.SeverityWarn, auditActorArg(actor), "tenant.suspend", rec.Email,
		fmt.Sprintf("%d sessions revoked · %d keys revoked", sessionsRevoked, keysRevoked))
	return cloneTenant(rec.Tenant), nil
}

// ResumeTenant re-enables the linked user. Keys revoked by the suspension stay
// revoked; only a new issue restores service credentials.
func (s *Store) ResumeTenant(id string, actor ...string) (model.Tenant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.tenantIndexLocked(strings.TrimSpace(id))
	if idx < 0 {
		return model.Tenant{}, notFoundf("tenant %q not found", id)
	}
	rec := &s.tenants[idx]
	if rec.invalid != "" {
		return model.Tenant{}, conflictf("tenant %q is quarantined: %s", rec.ID, rec.invalid)
	}
	switch rec.Status {
	case model.TenantActive:
		return cloneTenant(rec.Tenant), nil
	case model.TenantPending:
		return model.Tenant{}, conflictf("tenant %q is pending; approve it first", rec.ID)
	}
	if rec.UserID == nil {
		return model.Tenant{}, conflictf("tenant %q has no linked user", rec.ID)
	}
	if userIdx := s.userIndexLocked(*rec.UserID); userIdx >= 0 {
		s.users[userIdx].Enabled = true
	}
	rec.Status = model.TenantActive
	rec.SuspendedAt = nil
	s.addAuditLocked(model.SeverityInfo, auditActorArg(actor), "tenant.resume", rec.Email, "tenant reactivated")
	return cloneTenant(rec.Tenant), nil
}

// TenantKeys lists one user's API keys, newest first, including revoked rows so
// the portal can render their history.
func (s *Store) TenantKeys(userID string) []model.APIKey {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.APIKey, 0, len(s.keys))
	for _, key := range s.keys {
		if key.UserID == userID {
			out = append(out, cloneKey(key))
		}
	}
	return out
}

// CreateTenantKey issues one key for the linked user under the active-key cap.
// It reuses the same secret generation and hashing as the admin path.
func (s *Store) CreateTenantKey(userID, label string) (model.APIKey, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	active := 0
	for _, key := range s.keys {
		if key.UserID == userID && key.RevokedAt == nil {
			active++
		}
	}
	if active >= MaxTenantKeys {
		return model.APIKey{}, "", &Error{Kind: ErrQuota,
			Message: fmt.Sprintf("a tenant may hold at most %d active keys", MaxTenantKeys)}
	}
	return s.createKeyLocked(userID, label)
}

// RevokeTenantKey revokes one key only when it belongs to userID. A foreign or
// unknown id reports ErrNotFound, so an id probe cannot distinguish the two.
func (s *Store) RevokeTenantKey(userID, keyID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.keyIndexLocked(strings.TrimSpace(keyID))
	if idx < 0 || s.keys[idx].UserID != userID {
		return notFoundf("key %q not found", keyID)
	}
	key := &s.keys[idx]
	if key.RevokedAt != nil {
		return nil
	}
	key.RevokedAt = ptrString(iso(s.now()))
	s.addAuditLocked(model.SeverityInfo, "portal", "key.revoke", key.UserName,
		"Key prefix "+key.Prefix+" revoked")
	return nil
}

func (s *Store) tenantIndexLocked(id string) int {
	for i := range s.tenants {
		if s.tenants[i].ID == id {
			return i
		}
	}
	return -1
}

func (s *Store) tenantEmailIndexLocked(email string) int {
	for i := range s.tenants {
		if s.tenants[i].Email == email {
			return i
		}
	}
	return -1
}

// tenantInviteIndexLocked finds an invitation by code digest. Invitations are
// newest first, so the first match is also the most recent row.
func (s *Store) tenantInviteIndexLocked(codeHash string) int {
	for i := range s.tenantInvites {
		if s.tenantInvites[i].CodeHash == codeHash {
			return i
		}
	}
	return -1
}

// tenantInviteStatus derives pending/redeemed/expired from the row. A row with
// an unparseable expiry is treated as expired: fail closed rather than keeping
// a malformed code redeemable.
func tenantInviteStatus(rec tenantInviteRecord, now time.Time) model.TenantInviteStatus {
	if rec.RedeemedAt != nil {
		return model.TenantInviteRedeemed
	}
	expires, err := time.Parse(time.RFC3339Nano, rec.ExpiresAt)
	if err != nil || !now.Before(expires) {
		return model.TenantInviteExpired
	}
	return model.TenantInvitePending
}

func cloneTenantInvite(invite model.TenantInvite) model.TenantInvite {
	invite.RedeemedAt = copyStringPtr(invite.RedeemedAt)
	invite.TenantID = copyStringPtr(invite.TenantID)
	return invite
}

func (s *Store) sessionIndexLocked(idHash string) int {
	for i := range s.portalSessions {
		if s.portalSessions[i].IDHash == idHash {
			return i
		}
	}
	return -1
}

// pruneTenantSessionsLocked drops one tenant's revoked and expired sessions and
// then trims the tenant to MaxTenantSessions-1 rows, leaving room for the
// caller's new session. Rows are newest first.
func (s *Store) pruneTenantSessionsLocked(tenantID string, now time.Time) {
	kept := s.portalSessions[:0]
	for _, session := range s.portalSessions {
		if session.TenantID == tenantID &&
			(session.RevokedAt != nil || portalSessionExpired(session, now)) {
			continue
		}
		kept = append(kept, session)
	}
	s.portalSessions = kept

	count := 0
	capped := s.portalSessions[:0]
	for _, session := range s.portalSessions {
		if session.TenantID == tenantID {
			if count >= MaxTenantSessions-1 {
				continue
			}
			count++
		}
		capped = append(capped, session)
	}
	s.portalSessions = capped
}

func portalSessionExpired(session model.PortalSession, now time.Time) bool {
	expires, err := time.Parse(time.RFC3339Nano, session.ExpiresAt)
	if err != nil || !now.Before(expires) {
		return true
	}
	lastSeen, err := time.Parse(time.RFC3339Nano, session.LastSeenAt)
	if err != nil || now.Sub(lastSeen) > PortalSessionIdleTTL {
		return true
	}
	return false
}

func (s *Store) hashTenantPassword(password string) (string, error) {
	s.tenantHashSem <- struct{}{}
	defer func() { <-s.tenantHashSem }()

	salt := make([]byte, tenantArgonSaltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, tenantArgonTime, tenantArgonMemory, tenantArgonThreads, tenantArgonKeyLen)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		tenantArgonMemory, tenantArgonTime, tenantArgonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

func (s *Store) verifyTenantPassword(encoded, password string) bool {
	params, ok := parsePasswordHash(encoded)
	if !ok {
		return false
	}
	s.tenantHashSem <- struct{}{}
	defer func() { <-s.tenantHashSem }()
	key := argon2.IDKey([]byte(password), params.salt, params.time, params.memory, params.threads, uint32(len(params.hash)))
	return subtle.ConstantTimeCompare(key, params.hash) == 1
}

// tenantDummyOnce builds one real hash so an unknown email costs the same as a
// wrong password. It is built lazily because the parameters may change between
// builds and a hash is never accepted as a credential.
var tenantDummyOnce sync.Once

var tenantDummyHash string

func (s *Store) dummyTenantVerify(password string) {
	tenantDummyOnce.Do(func() {
		tenantDummyHash, _ = s.hashTenantPassword("dummy-portal-credential")
	})
	_ = s.verifyTenantPassword(tenantDummyHash, password)
}

type argonParams struct {
	memory  uint32
	time    uint32
	threads uint8
	salt    []byte
	hash    []byte
}

// parsePasswordHash decodes the PHC string this store writes. Bounds keep a
// hand-edited snapshot from turning a login into a multi-gigabyte allocation;
// a row outside them is quarantined by the snapshot validator.
func parsePasswordHash(encoded string) (argonParams, bool) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v=19" {
		return argonParams{}, false
	}
	var memory uint32
	var iterations uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &threads); err != nil {
		return argonParams{}, false
	}
	if parts[3] != fmt.Sprintf("m=%d,t=%d,p=%d", memory, iterations, threads) {
		return argonParams{}, false
	}
	if memory < 8*1024 || memory > 1024*1024 ||
		iterations < 1 || iterations > 10 ||
		threads < 1 || threads > 64 {
		return argonParams{}, false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < 8 || len(salt) > 64 {
		return argonParams{}, false
	}
	hash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(hash) < 16 || len(hash) > 64 {
		return argonParams{}, false
	}
	return argonParams{memory: memory, time: iterations, threads: threads, salt: salt, hash: hash}, true
}

func canonicalTenantEmail(raw string) (string, error) {
	email := strings.TrimSpace(raw)
	if email == "" {
		return "", invalidf("email is required")
	}
	if len(email) > MaxTenantEmailLen {
		return "", invalidf("email must be at most %d characters", MaxTenantEmailLen)
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Name != "" || addr.Address != email {
		return "", invalidf("email must be a plain address without a display name")
	}
	return strings.ToLower(email), nil
}

func validateTenantPassword(password string) error {
	if len(password) < MinTenantPasswordLen {
		return invalidf("password must be at least %d characters", MinTenantPasswordLen)
	}
	if len(password) > MaxTenantPasswordLen {
		return invalidf("password must be at most %d bytes", MaxTenantPasswordLen)
	}
	return nil
}

func canonicalTenantName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", invalidf("displayName is required")
	}
	if len([]rune(name)) > MaxTenantNameLen {
		return "", invalidf("displayName must be at most %d characters", MaxTenantNameLen)
	}
	return name, nil
}

func newPortalToken() (raw, hash string, err error) {
	buf := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", "", err
	}
	raw = hex.EncodeToString(buf)
	return raw, portalHash(raw), nil
}

func portalHash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func cloneTenant(tenant model.Tenant) model.Tenant {
	tenant.UserID = copyStringPtr(tenant.UserID)
	tenant.ApprovedAt = copyStringPtr(tenant.ApprovedAt)
	tenant.SuspendedAt = copyStringPtr(tenant.SuspendedAt)
	return tenant
}

func copyStringPtr(value *string) *string {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

// validateTenants rejects a document whose tenant rows cannot be identified
// and quarantines every row whose fields or links are broken. Duplicate ids are
// structural (no row could be addressed) and fail the load; duplicate emails or
// user links quarantine every row involved.
func (snap *Snapshot) validateTenants(users map[string]struct{}) error {
	ids := make(map[string]struct{}, len(snap.Tenants))
	emailCounts := make(map[string]int, len(snap.Tenants))
	userCounts := make(map[string]int, len(snap.Tenants))
	for i := range snap.Tenants {
		rec := &snap.Tenants[i]
		if strings.TrimSpace(rec.ID) == "" {
			return errors.New("state snapshot has a tenant without an id")
		}
		if _, ok := ids[rec.ID]; ok {
			return fmt.Errorf("state snapshot has duplicate tenant %q", rec.ID)
		}
		ids[rec.ID] = struct{}{}
		emailCounts[rec.Email]++
		if rec.UserID != nil {
			userCounts[*rec.UserID]++
		}
	}
	for i := range snap.Tenants {
		rec := &snap.Tenants[i]
		rec.invalid = tenantInvalidReason(rec, users, emailCounts, userCounts)
	}
	return nil
}

func tenantInvalidReason(rec *tenantRecord, users map[string]struct{}, emailCounts, userCounts map[string]int) string {
	if !model.ValidTenantStatus(rec.Status) {
		return "unknown status " + string(rec.Status)
	}
	canonical, err := canonicalTenantEmail(rec.Email)
	if err != nil || canonical != rec.Email {
		return "email is not canonical"
	}
	if emailCounts[rec.Email] > 1 {
		return "duplicate email"
	}
	if !validRFC3339(rec.CreatedAt) {
		return "createdAt is not RFC3339"
	}
	if _, ok := parsePasswordHash(rec.PasswordHash); !ok {
		return "password hash is malformed"
	}
	switch rec.Status {
	case model.TenantPending:
		if rec.UserID != nil {
			return "pending tenant is already linked"
		}
		if rec.ApprovedAt != nil || rec.SuspendedAt != nil {
			return "pending tenant carries approval timestamps"
		}
	case model.TenantActive, model.TenantSuspended:
		if rec.UserID == nil {
			return "tenant has no linked user"
		}
		if _, ok := users[*rec.UserID]; !ok {
			return "linked user is missing"
		}
		if userCounts[*rec.UserID] > 1 {
			return "user is linked to multiple tenants"
		}
		if rec.ApprovedAt == nil || !validRFC3339(*rec.ApprovedAt) {
			return "approvedAt is missing or invalid"
		}
		if rec.Status == model.TenantSuspended {
			if rec.SuspendedAt == nil || !validRFC3339(*rec.SuspendedAt) {
				return "suspendedAt is missing or invalid"
			}
		} else if rec.SuspendedAt != nil {
			return "active tenant carries suspendedAt"
		}
	}
	return ""
}

// validateTenantInvites drops rows that cannot be redeemed safely. An
// invitation is additive state: dropping one only invalidates its code, which
// is the fail-closed direction, while a malformed row must never keep a code
// alive or point a redeemed invite at a foreign tenant.
func (snap *Snapshot) validateTenantInvites() {
	tenants := make(map[string]*tenantRecord, len(snap.Tenants))
	for i := range snap.Tenants {
		tenants[snap.Tenants[i].ID] = &snap.Tenants[i]
	}
	seenIDs := make(map[string]struct{}, len(snap.TenantInvites))
	seenCodes := make(map[string]struct{}, len(snap.TenantInvites))
	valid := make([]tenantInviteRecord, 0, len(snap.TenantInvites))
	for _, rec := range snap.TenantInvites {
		rec.invalid = tenantInviteInvalidReason(rec, tenants)
		if rec.invalid != "" {
			continue
		}
		if _, ok := seenIDs[rec.ID]; ok {
			continue
		}
		if _, ok := seenCodes[rec.CodeHash]; ok {
			continue
		}
		seenIDs[rec.ID] = struct{}{}
		seenCodes[rec.CodeHash] = struct{}{}
		valid = append(valid, rec)
	}
	sort.SliceStable(valid, func(i, j int) bool { return valid[i].CreatedAt > valid[j].CreatedAt })
	if len(valid) > MaxTenantInvites {
		valid = valid[:MaxTenantInvites]
	}
	snap.TenantInvites = valid
}

func tenantInviteInvalidReason(rec tenantInviteRecord, tenants map[string]*tenantRecord) string {
	if strings.TrimSpace(rec.ID) == "" {
		return "invitation has no id"
	}
	canonical, err := canonicalTenantEmail(rec.Email)
	if err != nil || canonical != rec.Email {
		return "invitation email is not canonical"
	}
	if !isLowerHex(rec.CodeHash, 64) {
		return "invitation code hash is malformed"
	}
	if !validRFC3339(rec.CreatedAt) || !validRFC3339(rec.ExpiresAt) {
		return "invitation timestamps are invalid"
	}
	created, err := time.Parse(time.RFC3339Nano, rec.CreatedAt)
	if err != nil {
		return "invitation createdAt is invalid"
	}
	expires, err := time.Parse(time.RFC3339Nano, rec.ExpiresAt)
	if err != nil || !expires.After(created) {
		return "invitation expiry is invalid"
	}
	if rec.RedeemedAt != nil {
		if !validRFC3339(*rec.RedeemedAt) {
			return "invitation redeemedAt is invalid"
		}
		if rec.TenantID == nil {
			return "redeemed invitation has no tenant"
		}
		tenant, ok := tenants[*rec.TenantID]
		if !ok || tenant.Email != rec.Email || tenant.invalid != "" {
			return "redeemed invitation points at a missing tenant"
		}
	} else if rec.TenantID != nil {
		return "pending invitation already names a tenant"
	}
	return ""
}

// validatePortalSessions drops every session whose digests, tenant or
// timestamps are unusable, then keeps the newest MaxTenantSessions per tenant.
// Dropping a row denies the cookie just as an explicit revocation does.
func (snap *Snapshot) validatePortalSessions() {
	tenants := make(map[string]*tenantRecord, len(snap.Tenants))
	for i := range snap.Tenants {
		tenants[snap.Tenants[i].ID] = &snap.Tenants[i]
	}
	seen := make(map[string]struct{}, len(snap.PortalSessions))
	valid := make([]model.PortalSession, 0, len(snap.PortalSessions))
	for _, session := range snap.PortalSessions {
		session.CSRFToken = ""
		if !isLowerHex(session.IDHash, 64) || !isLowerHex(session.CSRFHash, 64) {
			continue
		}
		if _, ok := seen[session.IDHash]; ok {
			continue
		}
		rec, ok := tenants[session.TenantID]
		if !ok || rec.invalid != "" {
			continue
		}
		if !validRFC3339(session.CreatedAt) || !validRFC3339(session.LastSeenAt) || !validRFC3339(session.ExpiresAt) {
			continue
		}
		if session.RevokedAt != nil && !validRFC3339(*session.RevokedAt) {
			continue
		}
		created, err := time.Parse(time.RFC3339Nano, session.CreatedAt)
		if err != nil {
			continue
		}
		expires, err := time.Parse(time.RFC3339Nano, session.ExpiresAt)
		if err != nil || expires.Before(created) {
			continue
		}
		seen[session.IDHash] = struct{}{}
		valid = append(valid, session)
	}
	// Newest first: RFC 3339 with the same UTC layout sorts chronologically as
	// a string, and a hand-edited document may be in any order.
	sort.SliceStable(valid, func(i, j int) bool { return valid[i].CreatedAt > valid[j].CreatedAt })
	perTenant := make(map[string]int, len(tenants))
	kept := valid[:0]
	for _, session := range valid {
		if perTenant[session.TenantID] >= MaxTenantSessions {
			continue
		}
		perTenant[session.TenantID]++
		kept = append(kept, session)
	}
	if len(kept) > MaxPortalSessions {
		kept = kept[:MaxPortalSessions]
	}
	snap.PortalSessions = kept
}
