package store

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ajsaccountd/internal/model"
)

// testIngressPepper is fixed dummy key material, not a credential.
var testIngressPepper = bytes.Repeat([]byte{0x5a}, CredentialPepperBytes)

// credentialFixture is one lease on the standard lease fixture with the ingress
// pepper already installed.
type credentialFixture struct {
	leaseTestFixture
	lease model.Lease
}

func newCredentialFixture(t *testing.T, now *time.Time) credentialFixture {
	t.Helper()
	fixture := newLeaseFixture(t, now, model.PlatformAndroid, 2)
	if err := fixture.st.SetIngressPepper(testIngressPepper); err != nil {
		t.Fatalf("SetIngressPepper: %v", err)
	}
	lease, _, err := fixture.st.CreateLease(model.CreateLeaseInput{
		UserID: fixture.user.ID, Platform: model.PlatformAndroid, IdempotencyKey: "cred-1",
	})
	if err != nil {
		t.Fatalf("CreateLease: %v", err)
	}
	return credentialFixture{leaseTestFixture: fixture, lease: lease}
}

func (f credentialFixture) issue(t *testing.T) (model.LeaseCredential, string) {
	t.Helper()
	credential, secret, err := f.st.IssueLeaseCredential(model.IssueLeaseCredentialInput{LeaseID: f.lease.ID})
	if err != nil {
		t.Fatalf("IssueLeaseCredential: %v", err)
	}
	return credential, secret
}

// TestIssueLeaseCredentialReturnsSecretOnce pins the one-time reveal: the
// secret is 32 random bytes as unpadded base64url, the stored verifier is a
// keyed HMAC rather than the secret, and a second issue is refused.
func TestIssueLeaseCredentialReturnsSecretOnce(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	fixture := newCredentialFixture(t, &now)

	credential, secret := fixture.issue(t)
	raw, err := base64.RawURLEncoding.DecodeString(secret)
	if err != nil || len(raw) != CredentialSecretBytes || len(secret) != 43 {
		t.Fatalf("secret = %q (decoded %d bytes, err %v), want 43 base64url chars of 32 bytes",
			secret, len(raw), err)
	}
	if credential.ID != "cred-01" || credential.LeaseID != fixture.lease.ID ||
		credential.UserID != fixture.user.ID {
		t.Fatalf("credential = %+v, want cred-01 for the lease and user", credential)
	}
	if credential.Prefix != secret[:credentialPrefixLen] {
		t.Fatalf("prefix = %q, want the first %d characters of the secret", credential.Prefix, credentialPrefixLen)
	}
	if credential.MaxConns != DefaultCredentialMaxConns || len(credential.SourceCIDRs) != 0 {
		t.Fatalf("policy = maxConns %d cidrs %v, want the defaults", credential.MaxConns, credential.SourceCIDRs)
	}
	if credential.RevokedAt != nil || credential.CreatedAt != iso(now) {
		t.Fatalf("credential lifecycle = %+v, want live and created now", credential)
	}
	wantRoute := model.LeaseRoute{
		NodeID: fixture.lease.NodeID, AccountID: fixture.lease.AccountID,
		GatePort: fixture.lease.GatePort, Slot: fixture.lease.Slot, Generation: fixture.lease.Generation,
	}
	if credential.Route != wantRoute {
		t.Fatalf("route = %+v, want %+v", credential.Route, wantRoute)
	}
	stored := fixture.st.leaseCredentials[0]
	if credential.SecretHash != "" {
		t.Fatalf("issued metadata carries the verifier %q", credential.SecretHash)
	}
	mac := hmac.New(sha256.New, testIngressPepper)
	mac.Write([]byte(secret))
	if want := hex.EncodeToString(mac.Sum(nil)); stored.SecretHash != want {
		t.Fatalf("stored verifier = %q, want HMAC-SHA256(pepper, secret) %q", stored.SecretHash, want)
	}
	if strings.Contains(stored.SecretHash, secret) {
		t.Fatal("stored verifier contains the plaintext secret")
	}

	if _, _, err := fixture.st.IssueLeaseCredential(model.IssueLeaseCredentialInput{LeaseID: fixture.lease.ID}); !errors.Is(err, ErrConflict) {
		t.Fatalf("second issue = %v, want ErrConflict", err)
	}
	if got := len(fixture.st.leaseCredentials); got != 1 {
		t.Fatalf("credential rows = %d, want exactly one per lease", got)
	}
	looked, ok := fixture.st.LookupByLease(fixture.lease.ID)
	if !ok || looked.ID != credential.ID || looked.Prefix != credential.Prefix || looked.SecretHash != "" {
		t.Fatalf("LookupByLease = %+v ok=%v, want the issued metadata without the verifier", looked, ok)
	}

	var issued model.AuditEvent
	for _, event := range fixture.st.Audit(20) {
		if event.Action == "credential.issue" {
			issued = event
			break
		}
	}
	if issued.ID == "" {
		t.Fatal("no credential.issue audit row")
	}
	rendered := issued.Detail + issued.Target
	for key, value := range issued.Meta {
		rendered += key + value
	}
	if strings.Contains(rendered, secret) || strings.Contains(rendered, stored.SecretHash) {
		t.Fatalf("audit row leaks credential material: %+v", issued)
	}
}

// TestRotateLeaseCredentialRevokesOldAndIssuesNew proves one lock replaces the
// credential: the old secret stops working, the new one works, and the old row
// is retained as revoked history.
func TestRotateLeaseCredentialRevokesOldAndIssuesNew(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	fixture := newCredentialFixture(t, &now)
	old, oldSecret := fixture.issue(t)

	var notified []model.LeaseCredential
	fixture.st.SetLeaseCredentialRevokeHook(func(credential model.LeaseCredential) {
		notified = append(notified, credential)
	})

	replacement, newSecret, err := fixture.st.RotateLeaseCredential(fixture.lease.ID)
	if err != nil {
		t.Fatalf("RotateLeaseCredential: %v", err)
	}
	if replacement.ID == old.ID || newSecret == oldSecret {
		t.Fatalf("replacement = %+v secret reused=%v, want a fresh id and secret", replacement, newSecret == oldSecret)
	}
	if replacement.Prefix != newSecret[:credentialPrefixLen] {
		t.Fatalf("replacement prefix = %q, want the new secret prefix", replacement.Prefix)
	}
	if _, err := fixture.st.Authenticate(fixture.lease.ID, oldSecret); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("old secret after rotate = %v, want ErrUnauthenticated", err)
	}
	admission, err := fixture.st.Authenticate(fixture.lease.ID, newSecret)
	if err != nil {
		t.Fatalf("Authenticate(new secret): %v", err)
	}
	if admission.CredentialID != replacement.ID || admission.LeaseID != fixture.lease.ID {
		t.Fatalf("admission = %+v, want the replacement credential", admission)
	}
	if len(fixture.st.leaseCredentials) != 2 {
		t.Fatalf("credential rows = %d, want the revoked row plus the replacement", len(fixture.st.leaseCredentials))
	}
	if fixture.st.leaseCredentials[0].RevokedAt == nil {
		t.Fatal("rotated credential is still live")
	}
	if len(notified) != 1 || notified[0].ID != old.ID || notified[0].RevokedAt == nil {
		t.Fatalf("revoke hook = %+v, want the revoked old credential", notified)
	}
	if live := fixture.st.liveLeaseCredentialIndexLocked(fixture.lease.ID); live < 0 ||
		fixture.st.leaseCredentials[live].ID != replacement.ID {
		t.Fatal("rotation did not leave exactly the replacement live")
	}
	if _, _, err := fixture.st.RotateLeaseCredential("lease-99"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rotate unknown lease = %v, want ErrNotFound", err)
	}
}

// TestRevokeLeaseCredentialStopsAuthentication covers the explicit revoke, the
// idempotent not-found on a second revoke, and the no-lock hook contract: a
// sink that reads the store must not deadlock.
func TestRevokeLeaseCredentialStopsAuthentication(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	fixture := newCredentialFixture(t, &now)
	credential, secret := fixture.issue(t)

	done := make(chan struct{})
	fixture.st.SetLeaseCredentialRevokeHook(func(revoked model.LeaseCredential) {
		// Reading the store from the sink is the router's shape: it looks up
		// the credential while closing the flows. The hook runs after the
		// write lock is released, so this must not block.
		if _, ok := fixture.st.LookupByLease(revoked.LeaseID); ok {
			t.Errorf("revoked credential is still live during hook delivery")
		}
		close(done)
	})
	go func() {
		if _, err := fixture.st.RevokeLeaseCredential(fixture.lease.ID); err != nil {
			t.Errorf("RevokeLeaseCredential: %v", err)
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("revoke hook blocked on the store lock")
	}

	if _, err := fixture.st.Authenticate(fixture.lease.ID, secret); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("authenticate after revoke = %v, want ErrUnauthenticated", err)
	}
	if _, ok := fixture.st.LookupByLease(fixture.lease.ID); ok {
		t.Fatal("LookupByLease returned a revoked credential")
	}
	if _, err := fixture.st.RevokeLeaseCredential(fixture.lease.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second revoke = %v, want ErrNotFound", err)
	}
	if fixture.st.leaseCredentials[0].RevokedAt == nil {
		t.Fatal("revoked row lost its RevokedAt")
	}
	_ = credential
}

// TestLeaseCredentialRejectsExpiredLease proves both sides of expiry: an
// existing credential stops authenticating, and a new credential is refused.
func TestLeaseCredentialRejectsExpiredLease(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	fixture := newCredentialFixture(t, &now)
	_, secret := fixture.issue(t)

	now = now.Add(time.Duration(fixture.lease.TTLSeconds)*time.Second + time.Second)
	if _, err := fixture.st.Authenticate(fixture.lease.ID, secret); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("authenticate expired lease = %v, want ErrUnauthenticated", err)
	}

	expired := newCredentialFixture(t, &now)
	now = now.Add(time.Duration(expired.lease.TTLSeconds)*time.Second + time.Second)
	if _, _, err := expired.st.IssueLeaseCredential(model.IssueLeaseCredentialInput{LeaseID: expired.lease.ID}); !errors.Is(err, ErrConflict) {
		t.Fatalf("issue on expired lease = %v, want ErrConflict", err)
	}
}

// TestLeaseCredentialRejectsWrongSecret proves the generic refusal: a wrong,
// empty, other-lease or unknown-lease secret is indistinguishable.
func TestLeaseCredentialRejectsWrongSecret(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	fixture := newCredentialFixture(t, &now)
	_, secret := fixture.issue(t)

	otherUser := addLeaseUser(t, fixture.st, "other-bot", nil)
	otherLease, _, err := fixture.st.CreateLease(model.CreateLeaseInput{
		UserID: otherUser.ID, Platform: model.PlatformAndroid, IdempotencyKey: "cred-2",
	})
	if err != nil {
		t.Fatalf("CreateLease(other): %v", err)
	}
	_, otherSecret, err := fixture.st.IssueLeaseCredential(model.IssueLeaseCredentialInput{LeaseID: otherLease.ID})
	if err != nil {
		t.Fatalf("IssueLeaseCredential(other): %v", err)
	}

	cases := []struct {
		name    string
		leaseID string
		secret  string
	}{
		{"wrong secret", fixture.lease.ID, "not-the-secret"},
		{"empty secret", fixture.lease.ID, ""},
		{"truncated secret", fixture.lease.ID, secret[:len(secret)-1]},
		{"other lease secret", fixture.lease.ID, otherSecret},
		{"unknown lease", "lease-99", secret},
		{"prefix only", fixture.lease.ID, secret[:credentialPrefixLen]},
	}
	var first string
	for _, tc := range cases {
		_, err := fixture.st.Authenticate(tc.leaseID, tc.secret)
		if !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("%s = %v, want ErrUnauthenticated", tc.name, err)
		}
		if first == "" {
			first = err.Error()
		} else if err.Error() != first {
			t.Fatalf("%s error %q differs from %q; refusals must be indistinguishable", tc.name, err.Error(), first)
		}
	}
}

// TestLeaseCredentialRejectsDisabledUser proves a suspended user can neither
// authenticate an existing credential nor receive a new one, and that the
// suspension revokes the credential instead of merely hiding it: a resume does
// not resurrect the old secret.
func TestLeaseCredentialRejectsDisabledUser(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	fixture := newCredentialFixture(t, &now)
	credential, secret := fixture.issue(t)
	var notified []model.LeaseCredential
	fixture.st.SetLeaseCredentialRevokeHook(func(revoked model.LeaseCredential) {
		notified = append(notified, revoked)
	})
	if _, err := fixture.st.SetUserEnabled(fixture.user.ID, false); err != nil {
		t.Fatalf("SetUserEnabled: %v", err)
	}
	if len(notified) != 1 || notified[0].ID != credential.ID {
		t.Fatalf("suspend hook = %+v, want the user's credential", notified)
	}
	if _, ok := fixture.st.LookupByLease(fixture.lease.ID); ok {
		t.Fatal("suspend left the credential live")
	}
	if _, err := fixture.st.Authenticate(fixture.lease.ID, secret); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("authenticate disabled user = %v, want ErrUnauthenticated", err)
	}
	if _, err := fixture.st.SetUserEnabled(fixture.user.ID, true); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if _, err := fixture.st.Authenticate(fixture.lease.ID, secret); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("authenticate after resume = %v, want the revoked secret to stay dead", err)
	}

	pending := newCredentialFixture(t, &now)
	if _, err := pending.st.SetUserEnabled(pending.user.ID, false); err != nil {
		t.Fatalf("SetUserEnabled: %v", err)
	}
	if _, _, err := pending.st.IssueLeaseCredential(model.IssueLeaseCredentialInput{LeaseID: pending.lease.ID}); !errors.Is(err, ErrConflict) {
		t.Fatalf("issue for disabled user = %v, want ErrConflict", err)
	}
}

// TestLeaseCredentialRejectsStaleRoute proves Authenticate rechecks the live
// route instead of trusting the stored snapshot.
func TestLeaseCredentialRejectsStaleRoute(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	fixture := newCredentialFixture(t, &now)
	_, secret := fixture.issue(t)

	fixture.st.leases[0].Generation++
	if _, err := fixture.st.Authenticate(fixture.lease.ID, secret); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("authenticate stale generation = %v, want ErrUnauthenticated", err)
	}
}

// TestLeaseCredentialSurvivesSnapshot proves the metadata round-trips while the
// plaintext secret does not: the restored store authenticates with the same
// secret only because the same pepper is installed.
func TestLeaseCredentialSurvivesSnapshot(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	fixture := newCredentialFixture(t, &now)
	credential, secret := fixture.issue(t)

	data, err := fixture.st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	restored := newEmptyWithClock(func() time.Time { return now }, nil)
	if err := restored.SetIngressPepper(testIngressPepper); err != nil {
		t.Fatalf("SetIngressPepper: %v", err)
	}
	if err := restored.Restore(data); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	looked, ok := restored.LookupByLease(fixture.lease.ID)
	if !ok {
		t.Fatal("credential did not survive the snapshot")
	}
	if looked.ID != credential.ID || looked.SecretHash != "" ||
		looked.Prefix != credential.Prefix || looked.MaxConns != credential.MaxConns ||
		looked.Route != credential.Route {
		t.Fatalf("restored credential = %+v, want %+v", looked, credential)
	}
	if got, want := restored.leaseCredentials[0].SecretHash, fixture.st.leaseCredentials[0].SecretHash; got != want {
		t.Fatalf("restored verifier = %q, want %q", got, want)
	}
	admission, err := restored.Authenticate(fixture.lease.ID, secret)
	if err != nil {
		t.Fatalf("Authenticate after restore: %v", err)
	}
	if admission.Route != credential.Route {
		t.Fatalf("restored route = %+v, want %+v", admission.Route, credential.Route)
	}
	// The pepper is process state, never snapshot state: a store restored
	// without it must fail closed.
	unpeppered := newEmptyWithClock(func() time.Time { return now }, nil)
	if err := unpeppered.Restore(data); err != nil {
		t.Fatalf("Restore(unpeppered): %v", err)
	}
	if _, err := unpeppered.Authenticate(fixture.lease.ID, secret); !errors.Is(err, ErrIngressDisabled) {
		t.Fatalf("authenticate without pepper = %v, want ErrIngressDisabled", err)
	}
}

// TestSnapshotHasNoCredentialPlaintext scans the serialised document: the
// secret and the pepper must be absent, and only the keyed verifier present.
func TestSnapshotHasNoCredentialPlaintext(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	fixture := newCredentialFixture(t, &now)
	_, secret := fixture.issue(t)

	data, err := fixture.st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if bytes.Contains(data, []byte(secret)) {
		t.Fatal("snapshot contains the plaintext credential secret")
	}
	if bytes.Contains(data, testIngressPepper) {
		t.Fatal("snapshot contains the ingress pepper")
	}
	stored := fixture.st.leaseCredentials[0]
	if !bytes.Contains(data, []byte(stored.SecretHash)) {
		t.Fatal("snapshot is missing the credential verifier")
	}
	var decoded Snapshot
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if len(decoded.LeaseCredentials) != 1 || decoded.LeaseCredentials[0].SecretHash != stored.SecretHash {
		t.Fatalf("snapshot credentials = %+v, want one verifier row", decoded.LeaseCredentials)
	}
	if decoded.CredentialSeq != 1 {
		t.Fatalf("snapshot credentialSeq = %d, want 1", decoded.CredentialSeq)
	}
}

// TestLeaseCredentialRevokedOnLeaseRemoval covers every removal path the store
// owns: revoke, expiry sweep and node invalidation all drop the credential and
// notify the router once.
func TestLeaseCredentialRevokedOnLeaseRemoval(t *testing.T) {
	cases := map[string]func(t *testing.T, fixture credentialFixture){
		"revoke": func(t *testing.T, fixture credentialFixture) {
			if _, err := fixture.st.RevokeLease(fixture.lease.ID); err != nil {
				t.Fatalf("RevokeLease: %v", err)
			}
		},
		"expiry": func(t *testing.T, fixture credentialFixture) {
			expires, ok := leaseExpiry(fixture.lease.ExpiresAt)
			if !ok {
				t.Fatalf("lease expiresAt = %q", fixture.lease.ExpiresAt)
			}
			fixture.st.now = func() time.Time { return expires.Add(time.Second) }
			if leases := fixture.st.Leases(); len(leases) != 0 {
				t.Fatalf("leases after sweep = %+v, want none", leases)
			}
		},
		"node invalidation": func(t *testing.T, fixture credentialFixture) {
			if _, err := fixture.st.InvalidateNodeLeases(fixture.lease.NodeID, "vendor_kick"); err != nil {
				t.Fatalf("InvalidateNodeLeases: %v", err)
			}
		},
		"user delete": func(t *testing.T, fixture credentialFixture) {
			if err := fixture.st.DeleteUser(fixture.user.ID); err != nil {
				t.Fatalf("DeleteUser: %v", err)
			}
		},
	}
	for name, remove := range cases {
		t.Run(name, func(t *testing.T) {
			now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
			fixture := newCredentialFixture(t, &now)
			credential, secret := fixture.issue(t)
			var notified []model.LeaseCredential
			fixture.st.SetLeaseCredentialRevokeHook(func(revoked model.LeaseCredential) {
				notified = append(notified, revoked)
			})

			remove(t, fixture)

			if _, err := fixture.st.Authenticate(fixture.lease.ID, secret); !errors.Is(err, ErrUnauthenticated) {
				t.Fatalf("authenticate after removal = %v, want ErrUnauthenticated", err)
			}
			if len(fixture.st.leaseCredentials) != 0 {
				t.Fatalf("credential rows after removal = %+v, want none", fixture.st.leaseCredentials)
			}
			if len(notified) != 1 || notified[0].ID != credential.ID || notified[0].RevokedAt == nil {
				t.Fatalf("revoke hook = %+v, want the removed credential once", notified)
			}
		})
	}
}

// TestIssueLeaseCredentialRequiresPepper proves the store fails closed until
// startup installs a valid 32-byte pepper.
func TestIssueLeaseCredentialRequiresPepper(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	fixture := newLeaseFixture(t, &now, model.PlatformAndroid, 2)
	lease, _, err := fixture.st.CreateLease(model.CreateLeaseInput{UserID: fixture.user.ID, Platform: model.PlatformAndroid})
	if err != nil {
		t.Fatalf("CreateLease: %v", err)
	}
	if _, _, err := fixture.st.IssueLeaseCredential(model.IssueLeaseCredentialInput{LeaseID: lease.ID}); !errors.Is(err, ErrIngressDisabled) {
		t.Fatalf("issue without pepper = %v, want ErrIngressDisabled", err)
	}
	if _, err := fixture.st.Authenticate(lease.ID, "secret"); !errors.Is(err, ErrIngressDisabled) {
		t.Fatalf("authenticate without pepper = %v, want ErrIngressDisabled", err)
	}
	for _, size := range []int{0, CredentialPepperBytes - 1, CredentialPepperBytes + 1} {
		if err := fixture.st.SetIngressPepper(bytes.Repeat([]byte{1}, size)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("SetIngressPepper(%d bytes) = %v, want ErrInvalid", size, err)
		}
	}
	if err := fixture.st.SetIngressPepper(testIngressPepper); err != nil {
		t.Fatalf("SetIngressPepper: %v", err)
	}
	if _, _, err := fixture.st.IssueLeaseCredential(model.IssueLeaseCredentialInput{LeaseID: lease.ID}); err != nil {
		t.Fatalf("issue with pepper: %v", err)
	}
}

// TestIssueLeaseCredentialPolicy covers the source-CIDR and MaxConns contract:
// hostnames are refused, prefixes are canonicalised and deduplicated, and the
// admission carries the policy the router enforces.
func TestIssueLeaseCredentialPolicy(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	fixture := newCredentialFixture(t, &now)

	for _, tc := range []struct {
		name  string
		input model.IssueLeaseCredentialInput
	}{
		{"hostname", model.IssueLeaseCredentialInput{LeaseID: fixture.lease.ID, SourceCIDRs: []string{"proxy.example.com"}}},
		{"empty entry", model.IssueLeaseCredentialInput{LeaseID: fixture.lease.ID, SourceCIDRs: []string{""}}},
		{"zero maxConns", model.IssueLeaseCredentialInput{LeaseID: fixture.lease.ID, MaxConns: -1}},
		{"oversized maxConns", model.IssueLeaseCredentialInput{LeaseID: fixture.lease.ID, MaxConns: MaxCredentialMaxConns + 1}},
	} {
		if _, _, err := fixture.st.IssueLeaseCredential(tc.input); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s = %v, want ErrInvalid", tc.name, err)
		}
	}

	credential, secret, err := fixture.st.IssueLeaseCredential(model.IssueLeaseCredentialInput{
		LeaseID:     fixture.lease.ID,
		SourceCIDRs: []string{"203.0.113.7/24", "2001:db8::/32", "203.0.113.0/24"},
		MaxConns:    4,
	})
	if err != nil {
		t.Fatalf("IssueLeaseCredential: %v", err)
	}
	want := []string{"203.0.113.0/24", "2001:db8::/32"}
	if len(credential.SourceCIDRs) != len(want) || credential.SourceCIDRs[0] != want[0] || credential.SourceCIDRs[1] != want[1] {
		t.Fatalf("source CIDRs = %v, want %v", credential.SourceCIDRs, want)
	}
	if credential.MaxConns != 4 {
		t.Fatalf("maxConns = %d, want 4", credential.MaxConns)
	}
	admission, err := fixture.st.Authenticate(fixture.lease.ID, secret)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if admission.MaxConns != 4 || len(admission.SourceCIDRs) != 2 {
		t.Fatalf("admission policy = %+v, want the issued policy", admission)
	}
	// Rotation keeps the policy but replaces the secret.
	replacement, newSecret, err := fixture.st.RotateLeaseCredential(fixture.lease.ID)
	if err != nil {
		t.Fatalf("RotateLeaseCredential: %v", err)
	}
	if replacement.MaxConns != 4 || len(replacement.SourceCIDRs) != 2 || newSecret == secret {
		t.Fatalf("replacement = %+v, want the inherited policy and a new secret", replacement)
	}
}

// TestPreviousPepperRehashesCredential pins the offline cutover: a secret
// verified under the retired pepper is rehashed with the current one.
func TestPreviousPepperRehashesCredential(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	fixture := newCredentialFixture(t, &now)
	credential, secret := fixture.issue(t)

	next := bytes.Repeat([]byte{0x33}, CredentialPepperBytes)
	if err := fixture.st.SetIngressPepper(next); err != nil {
		t.Fatalf("SetIngressPepper(next): %v", err)
	}
	if _, err := fixture.st.Authenticate(fixture.lease.ID, secret); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("authenticate with only the new pepper = %v, want ErrUnauthenticated", err)
	}
	if err := fixture.st.SetIngressPreviousPepper(testIngressPepper); err != nil {
		t.Fatalf("SetIngressPreviousPepper: %v", err)
	}
	if _, err := fixture.st.Authenticate(fixture.lease.ID, secret); err != nil {
		t.Fatalf("authenticate with the previous pepper: %v", err)
	}
	rehashed := fixture.st.leaseCredentials[0]
	if rehashed.SecretHash == credential.SecretHash {
		t.Fatal("verifier was not rehashed under the current pepper")
	}
	mac := hmac.New(sha256.New, next)
	mac.Write([]byte(secret))
	if want := hex.EncodeToString(mac.Sum(nil)); rehashed.SecretHash != want {
		t.Fatalf("rehashed verifier = %q, want %q", rehashed.SecretHash, want)
	}
	if err := fixture.st.SetIngressPreviousPepper(nil); err != nil {
		t.Fatalf("clear previous pepper: %v", err)
	}
	if _, err := fixture.st.Authenticate(fixture.lease.ID, secret); err != nil {
		t.Fatalf("authenticate after cutover: %v", err)
	}
}

// TestRestoreRejectsInvalidCredentialMetadata proves load validation fails
// closed on a credential that cannot be served.
func TestRestoreRejectsInvalidCredentialMetadata(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	fixture := newCredentialFixture(t, &now)
	_, _ = fixture.issue(t)
	data, err := fixture.st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	cases := map[string]func(*Snapshot){
		"missing lease": func(snap *Snapshot) { snap.Leases = nil },
		"invalid hash":  func(snap *Snapshot) { snap.LeaseCredentials[0].SecretHash = "not-a-hash" },
		"no route":      func(snap *Snapshot) { snap.LeaseCredentials[0].Route = model.LeaseRoute{} },
		"duplicate live": func(snap *Snapshot) {
			dup := snap.LeaseCredentials[0]
			dup.ID = "cred-99"
			snap.LeaseCredentials = append(snap.LeaseCredentials, dup)
		},
		"hostname cidr": func(snap *Snapshot) { snap.LeaseCredentials[0].SourceCIDRs = []string{"proxy.example.com"} },
		"maxConns zero": func(snap *Snapshot) { snap.LeaseCredentials[0].MaxConns = 0 },
	}
	for name, corrupt := range cases {
		t.Run(name, func(t *testing.T) {
			var snap Snapshot
			if err := json.Unmarshal(data, &snap); err != nil {
				t.Fatalf("decode: %v", err)
			}
			corrupt(&snap)
			corrupted, err := encodeSnapshot(snap)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			target := newEmptyWithClock(func() time.Time { return now }, nil)
			if err := target.Restore(corrupted); err == nil {
				t.Fatal("Restore accepted invalid credential metadata")
			}
		})
	}
}

// TestRestoreDropsStaleRouteCredential proves a credential whose route no
// longer matches the restored lease is dropped instead of being kept as a
// verifier for an admission the store must refuse.
func TestRestoreDropsStaleRouteCredential(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	fixture := newCredentialFixture(t, &now)
	_, secret := fixture.issue(t)
	data, err := fixture.st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("decode: %v", err)
	}
	snap.LeaseCredentials[0].Route.Generation = 99
	stale, err := encodeSnapshot(snap)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	restored := newEmptyWithClock(func() time.Time { return now }, nil)
	if err := restored.SetIngressPepper(testIngressPepper); err != nil {
		t.Fatalf("SetIngressPepper: %v", err)
	}
	if err := restored.Restore(stale); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if _, ok := restored.LookupByLease(fixture.lease.ID); ok {
		t.Fatal("stale-route credential survived restore")
	}
	if len(restored.leaseCredentials) != 0 {
		t.Fatalf("credential rows = %+v, want none", restored.leaseCredentials)
	}
	if _, err := restored.Authenticate(fixture.lease.ID, secret); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("authenticate stale credential = %v, want ErrUnauthenticated", err)
	}
}

// TestLoadIngressPepper covers the documented file encodings and the startup
// gate: ingress enabled with no usable pepper is an error, disabled is not.
func TestLoadIngressPepper(t *testing.T) {
	dir := t.TempDir()
	pepper := bytes.Repeat([]byte{0x7e}, CredentialPepperBytes)
	write := func(name string, data []byte) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return path
	}
	rawPath := write("raw", pepper)
	hexPath := write("hex", []byte(hex.EncodeToString(pepper)+"\n"))
	urlPath := write("b64url", []byte(base64.RawURLEncoding.EncodeToString(pepper)))
	stdPath := write("b64", []byte(base64.StdEncoding.EncodeToString(pepper)))
	for name, path := range map[string]string{"raw": rawPath, "hex": hexPath, "base64url": urlPath, "base64": stdPath} {
		got, err := LoadIngressPepper(path)
		if err != nil {
			t.Fatalf("%s: LoadIngressPepper: %v", name, err)
		}
		if !bytes.Equal(got, pepper) {
			t.Fatalf("%s: pepper = %x, want %x", name, got, pepper)
		}
	}

	badPath := write("bad", []byte("too-short"))
	if _, err := LoadIngressPepper(badPath); err == nil {
		t.Fatal("LoadIngressPepper accepted a short pepper")
	}
	if _, err := LoadIngressPepper(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("LoadIngressPepper accepted a missing file")
	}

	t.Setenv(IngressEnabledEnv, "")
	t.Setenv(IngressPepperEnv, "")
	if pepper, err := IngressPepperFromEnv(); err != nil || pepper != nil {
		t.Fatalf("disabled ingress = %x, %v; want nil, nil", pepper, err)
	}
	t.Setenv(IngressEnabledEnv, "1")
	if _, err := IngressPepperFromEnv(); err == nil {
		t.Fatal("enabled ingress without a pepper file did not fail")
	}
	t.Setenv(IngressPepperEnv, badPath)
	if _, err := IngressPepperFromEnv(); err == nil {
		t.Fatal("enabled ingress with an invalid pepper file did not fail")
	}
	t.Setenv(IngressPepperEnv, rawPath)
	got, err := IngressPepperFromEnv()
	if err != nil || !bytes.Equal(got, pepper) {
		t.Fatalf("enabled ingress = %x, %v; want the file pepper", got, err)
	}
	t.Setenv(IngressEnabledEnv, "yes")
	if _, err := IngressPepperFromEnv(); err == nil {
		t.Fatal("invalid AJS_REMOTE_INGRESS was accepted")
	}
}
