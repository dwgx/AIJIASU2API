package store

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/netip"
	"strings"
	"time"

	"ajsaccountd/internal/model"
)

// Lease credential bounds. The secret carries 256 bits of entropy, so a keyed
// HMAC is the right verifier: a plain hash or a password KDF adds nothing to a
// random bearer and would slow every SOCKS handshake.
const (
	// CredentialSecretBytes is the raw entropy of one credential secret.
	CredentialSecretBytes = 32
	// CredentialPepperBytes is the exact pepper length. A shorter key weakens
	// every stored verifier; a longer one is a configuration mistake.
	CredentialPepperBytes = 32
	// DefaultCredentialMaxConns is the per-credential session cap when the
	// issue request does not name one.
	DefaultCredentialMaxConns = 8
	// MaxCredentialMaxConns bounds a per-credential cap. The router's global
	// cap is 1024, so a larger per-lease value could never be honoured.
	MaxCredentialMaxConns = 1024
	// credentialPrefixLen is the non-secret display prefix length. It is
	// short enough that it cannot be replayed as a credential.
	credentialPrefixLen = 8
	// credentialIDPrefix is the identifier convention for credential rows.
	credentialIDPrefix = "cred-"
)

// LeaseAdmission is the immutable result of a successful Authenticate. It is
// everything the router needs to admit one session and nothing it could use to
// mint another: no secret, no hash and no caller-selected node.
type LeaseAdmission struct {
	CredentialID string
	LeaseID      string
	UserID       string
	Route        model.LeaseRoute
	SourceCIDRs  []string
	MaxConns     int
}

// SetIngressPepper installs the process-only HMAC key used to verify lease
// credential secrets. It must be called during startup, before any credential
// is issued or authenticated; a missing pepper makes both fail closed. The key
// is never persisted, logged or returned, and a changed pepper invalidates
// every stored verifier unless it is installed through
// SetIngressPreviousPepper first.
func (s *Store) SetIngressPepper(pepper []byte) error {
	if len(pepper) != CredentialPepperBytes {
		return invalidf("ingress pepper must be exactly %d bytes", CredentialPepperBytes)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ingressPepper = append([]byte(nil), pepper...)
	return nil
}

// SetIngressPreviousPepper installs the one retired pepper that Authenticate
// still accepts for an offline rehash-and-cutover. A credential verified under
// the previous pepper is rehashed with the current one before the admission is
// returned, so the previous key stops mattering after each credential's first
// successful authentication. Passing nil removes it.
func (s *Store) SetIngressPreviousPepper(pepper []byte) error {
	if len(pepper) != 0 && len(pepper) != CredentialPepperBytes {
		return invalidf("previous ingress pepper must be exactly %d bytes", CredentialPepperBytes)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(pepper) == 0 {
		s.ingressPreviousPepper = nil
		return nil
	}
	s.ingressPreviousPepper = append([]byte(nil), pepper...)
	return nil
}

// SetLeaseCredentialRevokeHook installs the sink notified when a live
// credential stops admitting sessions: an explicit revoke, a rotation, or the
// credential revocation that accompanies lease expiry, deletion or node
// invalidation. Delivery is queued under the store lock and runs after it is
// released, so the sink may close router flows (and call back into the Store)
// without deadlocking. Passing nil removes the hook.
func (s *Store) SetLeaseCredentialRevokeHook(hook func(model.LeaseCredential)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.leaseCredentialHook = hook
}

// IssueLeaseCredential creates the one credential a lease may hold and returns
// the plaintext secret exactly once together with the credential metadata
// without its stored verifier. A lease that already has a live credential is
// refused; rotation is the only replacement path. The returned secret is never
// persisted, so a lost response is recovered by rotating, never by issuing
// again.
func (s *Store) IssueLeaseCredential(in model.IssueLeaseCredentialInput, actor ...string) (model.LeaseCredential, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.requireIngressPepperLocked(); err != nil {
		return model.LeaseCredential{}, "", err
	}
	leaseID := strings.TrimSpace(in.LeaseID)
	if leaseID == "" {
		return model.LeaseCredential{}, "", invalidf("leaseId is required")
	}
	if s.liveLeaseCredentialIndexLocked(leaseID) >= 0 {
		return model.LeaseCredential{}, "", conflictf("lease %q already has a credential; rotate it instead", leaseID)
	}
	sourceCIDRs, err := normalizeSourceCIDRs(in.SourceCIDRs)
	if err != nil {
		return model.LeaseCredential{}, "", err
	}
	maxConns, err := normalizeCredentialMaxConns(in.MaxConns)
	if err != nil {
		return model.LeaseCredential{}, "", err
	}
	now := s.now()
	lease, err := s.credentialLeaseLocked(leaseID, now)
	if err != nil {
		return model.LeaseCredential{}, "", err
	}
	credential, secret, err := s.newLeaseCredentialLocked(lease, sourceCIDRs, maxConns, now)
	if err != nil {
		return model.LeaseCredential{}, "", err
	}
	s.leaseCredentials = append(s.leaseCredentials, credential)
	s.addAuditLocked(model.SeverityInfo, auditActorArg(actor), "credential.issue", lease.UserName,
		"Lease credential issued", credentialAuditMeta(credential))
	if err := s.commitLocked(); err != nil {
		return model.LeaseCredential{}, "", err
	}
	return redactLeaseCredential(credential), secret, nil
}

// RotateLeaseCredential revokes the lease's live credential and issues exactly
// one replacement under the same lock and commit. The new credential inherits
// the old source CIDRs and MaxConns but snapshots the lease's current route, so
// a same-slot generation change cannot leave the replacement pinned to the
// previous gate port. The old secret stops authenticating as soon as the
// returned replacement is valid; the revoked row is retained for audit while
// the lease exists. The returned metadata never carries the stored verifier.
func (s *Store) RotateLeaseCredential(leaseID string, actor ...string) (model.LeaseCredential, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.requireIngressPepperLocked(); err != nil {
		return model.LeaseCredential{}, "", err
	}
	leaseID = strings.TrimSpace(leaseID)
	idx := s.liveLeaseCredentialIndexLocked(leaseID)
	if idx < 0 {
		return model.LeaseCredential{}, "", notFoundf("lease %q has no credential to rotate", leaseID)
	}
	now := s.now()
	lease, err := s.credentialLeaseLocked(leaseID, now)
	if err != nil {
		return model.LeaseCredential{}, "", err
	}
	old := s.leaseCredentials[idx]
	replacement, secret, err := s.newLeaseCredentialLocked(lease, old.SourceCIDRs, old.MaxConns, now)
	if err != nil {
		return model.LeaseCredential{}, "", err
	}
	revoked := s.revokeCredentialLocked(idx, now)
	s.leaseCredentials = append(s.leaseCredentials, replacement)
	s.addAuditLocked(model.SeverityWarn, auditActorArg(actor), "credential.rotate", lease.UserName,
		"Lease credential rotated", map[string]string{
			"leaseId":             lease.ID,
			"credentialId":        replacement.ID,
			"revokedCredentialId": revoked.ID,
			"prefix":              replacement.Prefix,
			"slot":                fmt.Sprintf("%d", lease.Slot),
			"generation":          fmt.Sprintf("%d", lease.Generation),
		})
	if err := s.commitLocked(); err != nil {
		return model.LeaseCredential{}, "", err
	}
	return redactLeaseCredential(replacement), secret, nil
}

// RevokeLeaseCredential stops the lease's live credential from authenticating
// and returns the revoked row. The lease itself is untouched: revoking a
// credential is not revoking the entitlement. A second revoke is not found.
func (s *Store) RevokeLeaseCredential(leaseID string, actor ...string) (model.LeaseCredential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	leaseID = strings.TrimSpace(leaseID)
	idx := s.liveLeaseCredentialIndexLocked(leaseID)
	if idx < 0 {
		return model.LeaseCredential{}, notFoundf("lease %q has no live credential", leaseID)
	}
	revoked := s.revokeCredentialLocked(idx, s.now())
	s.addAuditLocked(model.SeverityWarn, auditActorArg(actor), "credential.revoke", leaseID,
		"Lease credential revoked", credentialAuditMeta(revoked))
	if err := s.commitLocked(); err != nil {
		return model.LeaseCredential{}, err
	}
	return redactLeaseCredential(revoked), nil
}

// LookupByLease returns the lease's live credential metadata without the
// stored verifier, and reports false when the credential is revoked or the
// lease has none. The verifier never leaves the store: no caller needs it, so
// no caller can leak it.
func (s *Store) LookupByLease(leaseID string) (model.LeaseCredential, bool) {
	leaseID = strings.TrimSpace(leaseID)
	if leaseID == "" {
		return model.LeaseCredential{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	idx := s.liveLeaseCredentialIndexLocked(leaseID)
	if idx < 0 {
		return model.LeaseCredential{}, false
	}
	return redactLeaseCredential(s.leaseCredentials[idx]), true
}

// Authenticate verifies one lease credential and returns the immutable route
// it may use. Every refusal is the same generic error: an unknown lease, a
// revoked or rotated credential, a wrong secret, an expired lease, a disabled
// user and a stale route are indistinguishable to the caller. Verification
// uses hmac.Equal over the keyed digest, so the comparison is constant time.
func (s *Store) Authenticate(leaseID, secret string) (LeaseAdmission, error) {
	leaseID = strings.TrimSpace(leaseID)
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.ingressPepper) != CredentialPepperBytes {
		return LeaseAdmission{}, ingressDisabledf("remote ingress has no credential pepper")
	}
	if leaseID == "" || secret == "" {
		return LeaseAdmission{}, unauthenticatedf()
	}
	idx := s.liveLeaseCredentialIndexLocked(leaseID)
	if idx < 0 {
		return LeaseAdmission{}, unauthenticatedf()
	}
	credential := s.leaseCredentials[idx]
	matched, rehash := s.verifyCredentialSecretLocked(credential.SecretHash, secret)
	if !matched {
		return LeaseAdmission{}, unauthenticatedf()
	}
	leaseIdx := s.leaseIndexLocked(leaseID)
	if leaseIdx < 0 {
		return LeaseAdmission{}, unauthenticatedf()
	}
	lease := s.leases[leaseIdx]
	if lease.Status != model.LeaseActive || credential.UserID != lease.UserID {
		return LeaseAdmission{}, unauthenticatedf()
	}
	if expires, ok := leaseExpiry(lease.ExpiresAt); ok && !expires.After(now) {
		return LeaseAdmission{}, unauthenticatedf()
	}
	userIdx := s.userIndexLocked(lease.UserID)
	if userIdx < 0 || !s.users[userIdx].Enabled {
		return LeaseAdmission{}, unauthenticatedf()
	}
	// A route that no longer matches the live lease is a stale entitlement: a
	// generation change or a rebind must never keep admitting the old gate.
	route, err := s.leaseRouteLocked(lease)
	if err != nil || route != credential.Route {
		return LeaseAdmission{}, unauthenticatedf()
	}
	if rehash {
		// The secret verified under the retired pepper: upgrade the verifier
		// so the next authentication uses the current key. A failed commit
		// poisons the store, and the admission fails closed with it.
		s.leaseCredentials[idx].SecretHash = s.hashCredentialSecret(secret)
		if err := s.commitLocked(); err != nil {
			return LeaseAdmission{}, unauthenticatedf()
		}
	}
	return LeaseAdmission{
		CredentialID: credential.ID,
		LeaseID:      lease.ID,
		UserID:       lease.UserID,
		Route:        route,
		SourceCIDRs:  append([]string(nil), credential.SourceCIDRs...),
		MaxConns:     credential.MaxConns,
	}, nil
}

// requireIngressPepperLocked refuses credential work until startup installed
// the HMAC key. The caller holds s.mu.
func (s *Store) requireIngressPepperLocked() error {
	if len(s.ingressPepper) != CredentialPepperBytes {
		return ingressDisabledf("remote ingress is not configured: AJS_INGRESS_PEPPER_FILE is required")
	}
	return nil
}

// liveLeaseCredentialIndexLocked returns the index of the lease's one live
// credential, or -1. The caller holds s.mu.
func (s *Store) liveLeaseCredentialIndexLocked(leaseID string) int {
	if leaseID == "" {
		return -1
	}
	for idx := range s.leaseCredentials {
		credential := s.leaseCredentials[idx]
		if credential.LeaseID == leaseID && credential.RevokedAt == nil {
			return idx
		}
	}
	return -1
}

// credentialLeaseLocked returns the lease a credential may be issued for. The
// lease must be active, unexpired and owned by an enabled user; a revoked,
// expired or suspended entitlement must not receive a new secret. The caller
// holds s.mu.
func (s *Store) credentialLeaseLocked(leaseID string, now time.Time) (model.Lease, error) {
	idx := s.leaseIndexLocked(leaseID)
	if idx < 0 {
		return model.Lease{}, notFoundf("lease %q not found", leaseID)
	}
	lease := s.leases[idx]
	if lease.Status != model.LeaseActive {
		return model.Lease{}, conflictf("lease %q is %s", leaseID, lease.Status)
	}
	if expires, ok := leaseExpiry(lease.ExpiresAt); ok && !expires.After(now) {
		return model.Lease{}, conflictf("lease %q expired at %s", leaseID, lease.ExpiresAt)
	}
	userIdx := s.userIndexLocked(lease.UserID)
	if userIdx < 0 {
		return model.Lease{}, conflictf("lease %q belongs to an unknown user", leaseID)
	}
	if !s.users[userIdx].Enabled {
		return model.Lease{}, conflictf("user %q is disabled", s.users[userIdx].Name)
	}
	return lease, nil
}

// leaseRouteLocked snapshots the route a lease currently advertises. Only a
// LIVE node with a bound listener that still matches the lease's account and
// gate port can back a credential; anything else fails closed instead of
// issuing a secret for an endpoint the router cannot dial. The caller holds
// s.mu.
func (s *Store) leaseRouteLocked(lease model.Lease) (model.LeaseRoute, error) {
	if lease.NodeID == "" || lease.AccountID == "" || lease.GatePort <= 0 {
		return model.LeaseRoute{}, conflictf("lease %q has no serving route", lease.ID)
	}
	idx := s.nodeIndexLocked(lease.NodeID)
	if idx < 0 {
		return model.LeaseRoute{}, conflictf("lease %q names a missing node", lease.ID)
	}
	node := s.nodes[idx]
	if node.AccountID != lease.AccountID || node.State != model.NodeLive ||
		node.GatePort <= 0 || node.GatePort != lease.GatePort {
		return model.LeaseRoute{}, conflictf("lease %q is not served by a live node", lease.ID)
	}
	return model.LeaseRoute{
		NodeID:     node.ID,
		AccountID:  node.AccountID,
		GatePort:   node.GatePort,
		Slot:       lease.Slot,
		Generation: lease.Generation,
	}, nil
}

// newLeaseCredentialLocked mints one credential row. The caller holds s.mu and
// has validated the lease and policy. The plaintext secret is returned to the
// caller and never enters the row.
func (s *Store) newLeaseCredentialLocked(lease model.Lease, sourceCIDRs []string, maxConns int, now time.Time) (model.LeaseCredential, string, error) {
	route, err := s.leaseRouteLocked(lease)
	if err != nil {
		return model.LeaseCredential{}, "", err
	}
	secret, err := newCredentialSecret()
	if err != nil {
		return model.LeaseCredential{}, "", fmt.Errorf("generate lease credential: %w", err)
	}
	s.credentialSeq++
	credential := model.LeaseCredential{
		ID:          fmt.Sprintf("%s%02d", credentialIDPrefix, s.credentialSeq),
		LeaseID:     lease.ID,
		UserID:      lease.UserID,
		SecretHash:  s.hashCredentialSecret(secret),
		Prefix:      secret[:credentialPrefixLen],
		CreatedAt:   iso(now),
		SourceCIDRs: sourceCIDRs,
		MaxConns:    maxConns,
		Route:       route,
	}
	return credential, secret, nil
}

// revokeCredentialLocked marks one live credential revoked, queues the router
// notification and returns a copy of the revoked row. The caller holds s.mu and
// owns the commit.
func (s *Store) revokeCredentialLocked(idx int, now time.Time) model.LeaseCredential {
	s.leaseCredentials[idx].RevokedAt = ptrString(iso(now))
	revoked := s.leaseCredentials[idx]
	s.queueLeaseCredentialHookLocked(revoked)
	return cloneLeaseCredential(revoked)
}

// revokeUserCredentialsLocked revokes every live credential of one user
// without touching the lease rows: a suspension must stop the ingress
// immediately, while the entitlements stay for a resume. A resume does not
// restore a secret that was already revoked. The caller holds s.mu and owns the
// commit.
func (s *Store) revokeUserCredentialsLocked(userID string) int {
	if userID == "" || len(s.leaseCredentials) == 0 {
		return 0
	}
	now := s.now()
	revoked := 0
	for idx := range s.leaseCredentials {
		credential := &s.leaseCredentials[idx]
		if credential.UserID != userID || credential.RevokedAt != nil {
			continue
		}
		credential.RevokedAt = ptrString(iso(now))
		s.queueLeaseCredentialHookLocked(*credential)
		revoked++
	}
	return revoked
}

// revokeLeaseCredentialsLocked revokes every live credential of a lease that is
// leaving the active collection and drops all of its credential rows. A
// credential for a removed lease can never authenticate again, so keeping its
// verifier would only retain key material for a dead entitlement. The router
// notification is queued before the rows go away, so the hook still learns the
// credential id to close. The caller holds s.mu.
func (s *Store) revokeLeaseCredentialsLocked(leaseID string) int {
	if leaseID == "" || len(s.leaseCredentials) == 0 {
		return 0
	}
	now := s.now()
	revoked := 0
	kept := s.leaseCredentials[:0]
	for _, credential := range s.leaseCredentials {
		if credential.LeaseID != leaseID {
			kept = append(kept, credential)
			continue
		}
		if credential.RevokedAt == nil {
			credential.RevokedAt = ptrString(iso(now))
			s.queueLeaseCredentialHookLocked(credential)
			revoked++
		}
	}
	s.leaseCredentials = kept
	return revoked
}

// hashCredentialSecret returns the persisted verifier: hex(HMAC-SHA256(pepper,
// secret)). The caller holds s.mu (the pepper is read under it).
func (s *Store) hashCredentialSecret(secret string) string {
	return hex.EncodeToString(credentialMAC(s.ingressPepper, secret))
}

// verifyCredentialSecretLocked compares the presented secret against the stored
// verifier in constant time. The second result reports that only the retired
// pepper matched, so the caller must rehash the row with the current key. The
// caller holds s.mu.
func (s *Store) verifyCredentialSecretLocked(hash, secret string) (matched, rehash bool) {
	expected, err := hex.DecodeString(hash)
	if err != nil || len(expected) != sha256.Size {
		return false, false
	}
	if hmac.Equal(expected, credentialMAC(s.ingressPepper, secret)) {
		return true, false
	}
	if len(s.ingressPreviousPepper) == CredentialPepperBytes &&
		hmac.Equal(expected, credentialMAC(s.ingressPreviousPepper, secret)) {
		return true, true
	}
	return false, false
}

// credentialMAC is the HMAC-SHA256 verifier of one secret under key.
func credentialMAC(key []byte, secret string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(secret))
	return mac.Sum(nil)
}

// newCredentialSecret returns 32 random bytes as unpadded base64url, the
// 43-character form the design publishes.
func newCredentialSecret() (string, error) {
	raw := make([]byte, CredentialSecretBytes)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// normalizeSourceCIDRs canonicalises the credential source allowlist. Only
// explicit CIDR prefixes are accepted: a hostname cannot be evaluated against
// a peer address, and an empty list admits any IPv4/IPv6 source.
func normalizeSourceCIDRs(in []string) ([]string, error) {
	if len(in) > maxAllowlistEntries {
		return nil, invalidf("source CIDRs must have at most %d entries", maxAllowlistEntries)
	}
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, raw := range in {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			return nil, invalidf("source CIDR entries must not be empty")
		}
		prefix, err := netip.ParsePrefix(entry)
		if err != nil {
			return nil, invalidf("source CIDR %q must be an IP prefix such as 203.0.113.0/24", entry)
		}
		canonical := canonicalPrefix(prefix)
		if _, ok := seen[canonical]; ok {
			continue
		}
		seen[canonical] = struct{}{}
		out = append(out, canonical)
	}
	return out, nil
}

// normalizeCredentialMaxConns applies the default and the ceiling. The caller
// sees a refusal rather than a silently clamped cap: the limit is a security
// control, so a value the router cannot honour must not be accepted.
func normalizeCredentialMaxConns(value int) (int, error) {
	if value == 0 {
		return DefaultCredentialMaxConns, nil
	}
	if value < 1 || value > MaxCredentialMaxConns {
		return 0, invalidf("maxConns must be between 1 and %d", MaxCredentialMaxConns)
	}
	return value, nil
}

// credentialAuditMeta is the attribution a credential audit row carries. It
// never contains the secret, the verifier or the source policy.
func credentialAuditMeta(credential model.LeaseCredential) map[string]string {
	return map[string]string{
		"leaseId":      credential.LeaseID,
		"credentialId": credential.ID,
		"prefix":       credential.Prefix,
		"nodeId":       credential.Route.NodeID,
		"slot":         fmt.Sprintf("%d", credential.Route.Slot),
		"generation":   fmt.Sprintf("%d", credential.Route.Generation),
	}
}

// cloneLeaseCredential copies a credential row so a caller cannot mutate the
// store's slice or its source CIDRs.
func cloneLeaseCredential(credential model.LeaseCredential) model.LeaseCredential {
	out := credential
	out.SourceCIDRs = append([]string(nil), credential.SourceCIDRs...)
	if credential.RevokedAt != nil {
		out.RevokedAt = ptrString(*credential.RevokedAt)
	}
	return out
}

// redactLeaseCredential returns a copy without the stored verifier. Every
// public result and hook delivery goes through it, so no caller can leak the
// keyed hash; the stored row keeps it for Authenticate and the snapshot.
func redactLeaseCredential(credential model.LeaseCredential) model.LeaseCredential {
	out := cloneLeaseCredential(credential)
	out.SecretHash = ""
	return out
}

// cloneLeaseCredentials copies every row for a snapshot.
func cloneLeaseCredentials(in []model.LeaseCredential) []model.LeaseCredential {
	if len(in) == 0 {
		return []model.LeaseCredential{}
	}
	out := make([]model.LeaseCredential, len(in))
	for i, credential := range in {
		out[i] = cloneLeaseCredential(credential)
	}
	return out
}
