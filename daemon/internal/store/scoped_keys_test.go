package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"ajsaccountd/internal/model"
)

func scopedKeyInput(expiresAt string) model.ScopedKeyCreateRequest {
	return model.ScopedKeyCreateRequest{
		UserID:     "u-01",
		Label:      "CI read only",
		Scopes:     []model.Scope{model.ScopeAccountsRead, model.ScopeMetricsRead},
		AccountIDs: []string{"acc-01"},
		Regions:    []string{"cn-zj"},
		ExpiresAt:  expiresAt,
	}
}

func TestScopedKeyCreateAndAuthenticate(t *testing.T) {
	st := New()
	expires := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	key, secret, err := st.CreateScopedKey(scopedKeyInput(expires))
	if err != nil {
		t.Fatalf("CreateScopedKey: %v", err)
	}
	if !strings.HasPrefix(secret, "ajs_sk_") {
		t.Fatalf("secret = %q, want the ajs_sk_ prefix", secret)
	}
	if key.ID != "sk-01" {
		t.Fatalf("id = %q, want sk-01", key.ID)
	}
	if key.UserID != "u-01" || key.UserName != "ada.ops" {
		t.Fatalf("key owner = %+v, want u-01/ada.ops", key)
	}
	if key.Prefix != secret[:16] {
		t.Fatalf("prefix = %q, want %q", key.Prefix, secret[:16])
	}
	if !reflect.DeepEqual(key.Scopes, []model.Scope{model.ScopeAccountsRead, model.ScopeMetricsRead}) {
		t.Fatalf("scopes = %v", key.Scopes)
	}
	if !reflect.DeepEqual(key.AccountIDs, []string{"acc-01"}) {
		t.Fatalf("accountIds = %v", key.AccountIDs)
	}
	if !reflect.DeepEqual(key.Regions, []string{"cn-zj"}) {
		t.Fatalf("regions = %v", key.Regions)
	}
	if key.ExpiresAt == nil || *key.ExpiresAt != expires {
		t.Fatalf("expiresAt = %v, want %q", key.ExpiresAt, expires)
	}
	if key.LastUsedAt != nil || key.RevokedAt != nil {
		t.Fatalf("new key has lastUsedAt/revokedAt set: %+v", key)
	}

	sum := sha256.Sum256([]byte(secret))
	if got, want := st.scopedKeyHashes[key.ID], hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("stored hash = %q, want %q", got, want)
	}
	if bytes.Contains([]byte(secret), []byte(st.scopedKeyHashes[key.ID])) {
		t.Fatal("stored hash contains the raw secret")
	}

	got, ok := st.AuthenticateScopedKey(secret)
	if !ok || got.ID != key.ID {
		t.Fatalf("AuthenticateScopedKey = %+v, %v; want key %q", got, ok, key.ID)
	}
	if _, ok := st.AuthenticateScopedKey("ajs_sk_not-a-real-secret"); ok {
		t.Fatal("AuthenticateScopedKey accepted an unknown secret")
	}
	if _, ok := st.AuthenticateScopedKey(""); ok {
		t.Fatal("AuthenticateScopedKey accepted an empty secret")
	}
}

func TestScopedKeyValidation(t *testing.T) {
	st := New()
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)

	cases := []struct {
		name   string
		mutate func(*model.ScopedKeyCreateRequest)
		kind   error
	}{
		{"missing user", func(in *model.ScopedKeyCreateRequest) { in.UserID = "" }, ErrInvalid},
		{"unknown user", func(in *model.ScopedKeyCreateRequest) { in.UserID = "u-99" }, ErrNotFound},
		{"missing label", func(in *model.ScopedKeyCreateRequest) { in.Label = "  " }, ErrInvalid},
		{"no scopes", func(in *model.ScopedKeyCreateRequest) { in.Scopes = nil }, ErrInvalid},
		{"unknown scope", func(in *model.ScopedKeyCreateRequest) { in.Scopes = []model.Scope{"root"} }, ErrInvalid},
		{"empty account id", func(in *model.ScopedKeyCreateRequest) { in.AccountIDs = []string{""} }, ErrInvalid},
		{"empty region", func(in *model.ScopedKeyCreateRequest) { in.Regions = []string{" "} }, ErrInvalid},
		{"malformed expiry", func(in *model.ScopedKeyCreateRequest) { in.ExpiresAt = "tomorrow" }, ErrInvalid},
		{"past expiry", func(in *model.ScopedKeyCreateRequest) { in.ExpiresAt = past }, ErrInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := scopedKeyInput(future)
			tc.mutate(&in)
			if _, _, err := st.CreateScopedKey(in); !errors.Is(err, tc.kind) {
				t.Fatalf("CreateScopedKey error = %v, want %v", err, tc.kind)
			}
		})
	}
	if got := len(st.ScopedKeys()); got != 0 {
		t.Fatalf("invalid creates left %d keys", got)
	}
}

func TestScopedKeyRevokeIsIdempotent(t *testing.T) {
	st := New()
	before := len(st.Audit(1000))
	key, secret, err := st.CreateScopedKey(scopedKeyInput(""))
	if err != nil {
		t.Fatalf("CreateScopedKey: %v", err)
	}
	if err := st.RevokeScopedKey(key.ID); err != nil {
		t.Fatalf("first RevokeScopedKey: %v", err)
	}
	if err := st.RevokeScopedKey(key.ID); err != nil {
		t.Fatalf("second RevokeScopedKey: %v", err)
	}
	if _, ok := st.AuthenticateScopedKey(secret); ok {
		t.Fatal("revoked key still authenticates")
	}
	revoked, _ := st.ScopedKey(key.ID)
	if revoked.RevokedAt == nil {
		t.Fatal("revokedAt is nil after revoke")
	}
	if err := st.RevokeScopedKey("sk-99"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown revoke error = %v, want ErrNotFound", err)
	}
	// One key.create plus one key.revoke; the second revoke is a no-op.
	if got := len(st.Audit(1000)); got != before+2 {
		t.Fatalf("audit events = %d, want %d", got, before+2)
	}
}

func TestScopedKeyExpiryUsesStoreClock(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	st := newWithClock(func() time.Time { return now }, nil)
	key, secret, err := st.CreateScopedKey(scopedKeyInput(now.Add(time.Hour).Format(time.RFC3339)))
	if err != nil {
		t.Fatalf("CreateScopedKey: %v", err)
	}
	if _, ok := st.AuthenticateScopedKey(secret); !ok {
		t.Fatal("key expired before its expiresAt")
	}
	now = now.Add(2 * time.Hour)
	if _, ok := st.AuthenticateScopedKey(secret); ok {
		t.Fatal("expired key still authenticates")
	}
	if got, _ := st.ScopedKey(key.ID); got.ID != key.ID {
		t.Fatalf("expired key disappeared from the listing: %+v", got)
	}
}

func TestScopedKeyLastUsedThrottle(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	st := newWithClock(func() time.Time { return now }, nil)
	key, _, err := st.CreateScopedKey(scopedKeyInput(""))
	if err != nil {
		t.Fatalf("CreateScopedKey: %v", err)
	}

	st.MarkScopedKeyUsed(key.ID)
	first, _ := st.ScopedKey(key.ID)
	if first.LastUsedAt == nil {
		t.Fatal("lastUsedAt is nil after first use")
	}

	now = now.Add(30 * time.Second)
	st.MarkScopedKeyUsed(key.ID)
	second, _ := st.ScopedKey(key.ID)
	if second.LastUsedAt == nil || *second.LastUsedAt != *first.LastUsedAt {
		t.Fatalf("lastUsedAt changed inside the same minute: %v -> %v", first.LastUsedAt, second.LastUsedAt)
	}

	now = now.Add(31 * time.Second)
	st.MarkScopedKeyUsed(key.ID)
	third, _ := st.ScopedKey(key.ID)
	if third.LastUsedAt == nil || *third.LastUsedAt == *first.LastUsedAt {
		t.Fatalf("lastUsedAt did not advance after a minute: %v", third.LastUsedAt)
	}
}

func TestScopedKeySnapshotRoundTrip(t *testing.T) {
	st := New()
	expires := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	key, secret, err := st.CreateScopedKey(scopedKeyInput(expires))
	if err != nil {
		t.Fatalf("CreateScopedKey: %v", err)
	}
	data, err := st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if bytes.Contains(data, []byte(secret)) {
		t.Fatal("snapshot contains the raw scoped-key secret")
	}
	if !bytes.Contains(data, []byte("scopedKeyHashes")) {
		t.Fatal("snapshot has no scopedKeyHashes section")
	}

	fresh := NewEmpty()
	if err := fresh.Restore(data); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	restored, ok := fresh.ScopedKey(key.ID)
	if !ok {
		t.Fatalf("restored store has no key %q", key.ID)
	}
	if !reflect.DeepEqual(restored, key) {
		t.Fatalf("restored key = %+v, want %+v", restored, key)
	}
	if _, ok := fresh.AuthenticateScopedKey(secret); !ok {
		t.Fatal("restored key does not authenticate")
	}
	if err := fresh.RevokeScopedKey(key.ID); err != nil {
		t.Fatalf("RevokeScopedKey after restore: %v", err)
	}
	if _, ok := fresh.AuthenticateScopedKey(secret); ok {
		t.Fatal("revoked restored key still authenticates")
	}

	// The restored counter must not hand out an existing id.
	next, _, err := fresh.CreateScopedKey(scopedKeyInput(""))
	if err != nil {
		t.Fatalf("CreateScopedKey after restore: %v", err)
	}
	if next.ID == key.ID {
		t.Fatalf("restored counter reused id %q", key.ID)
	}
}

func TestDeleteUserRevokesScopedKeys(t *testing.T) {
	st := New()
	key, secret, err := st.CreateScopedKey(scopedKeyInput(""))
	if err != nil {
		t.Fatalf("CreateScopedKey: %v", err)
	}
	if err := st.DeleteUser("u-01"); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if _, ok := st.AuthenticateScopedKey(secret); ok {
		t.Fatal("scoped key still authenticates after its user was deleted")
	}
	revoked, _ := st.ScopedKey(key.ID)
	if revoked.RevokedAt == nil {
		t.Fatal("scoped key was not revoked with its user")
	}
}
