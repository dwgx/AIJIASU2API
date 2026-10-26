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
		{"negative rate limit", func(in *model.ScopedKeyCreateRequest) { negative := -1; in.RateLimitPerMin = &negative }, ErrInvalid},
		{"accounts scope without grant", func(in *model.ScopedKeyCreateRequest) {
			in.AccountIDs, in.Regions = nil, nil
			in.Scopes = []model.Scope{model.ScopeAccountsRead}
		}, ErrInvalid},
		{"leases scope with empty grant lists", func(in *model.ScopedKeyCreateRequest) {
			in.AccountIDs, in.Regions = []string{}, []string{}
			in.Scopes = []model.Scope{model.ScopeLeasesWrite}
		}, ErrInvalid},
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

// TestScopedKeyGrantRequirement pins AJS-448's creation rule: an account-bound
// scope needs at least one account or region grant, while a global-only or
// admin key keeps the empty-list meaning "not narrowed by account".
func TestScopedKeyGrantRequirement(t *testing.T) {
	st := New()

	for _, scopes := range [][]model.Scope{
		{model.ScopeAccountsRead}, {model.ScopeCatalogRead}, {model.ScopeLeasesRead},
		{model.ScopeLeasesWrite}, {model.ScopeUsageRead, model.ScopeCatalogWrite},
	} {
		in := model.ScopedKeyCreateRequest{UserID: "u-01", Label: "unbounded", Scopes: scopes}
		if _, _, err := st.CreateScopedKey(in); !errors.Is(err, ErrInvalid) {
			t.Fatalf("CreateScopedKey(%v) error = %v, want ErrInvalid", scopes, err)
		}
	}

	for _, scopes := range [][]model.Scope{
		{model.ScopeUsageRead, model.ScopeRequestsRead, model.ScopeEventsRead},
		{model.ScopeAdmin},
	} {
		in := model.ScopedKeyCreateRequest{UserID: "u-01", Label: "global", Scopes: scopes}
		key, _, err := st.CreateScopedKey(in)
		if err != nil {
			t.Fatalf("CreateScopedKey(%v): %v", scopes, err)
		}
		if key.RequiresAccountGrant() || key.HasAccountGrant() {
			t.Fatalf("global key %v = %+v, want no grant requirement", scopes, key)
		}
	}

	// A single region satisfies the rule, and the predicate then matches only
	// the accounts that overlap it.
	regional, _, err := st.CreateScopedKey(model.ScopedKeyCreateRequest{
		UserID: "u-01", Label: "regional", Scopes: []model.Scope{model.ScopeLeasesRead},
		Regions: []string{"cn-zj"},
	})
	if err != nil {
		t.Fatalf("CreateScopedKey regional: %v", err)
	}
	if !regional.RequiresAccountGrant() || !regional.HasAccountGrant() {
		t.Fatalf("regional key = %+v, want an account grant", regional)
	}
	if !regional.AllowsAccount(model.Account{ID: "acc-01", Regions: []string{"cn-zj", "hk"}}) {
		t.Fatal("regional key does not allow an account with an overlapping region")
	}
	if regional.AllowsAccount(model.Account{ID: "acc-02", Regions: []string{"jp-tyo"}}) {
		t.Fatal("regional key allows an account without an overlapping region")
	}
}

func TestScopedKeyRateLimitDefaultExplicitAndPersistence(t *testing.T) {
	st := New()

	defaulted, _, err := st.CreateScopedKey(scopedKeyInput(""))
	if err != nil {
		t.Fatalf("CreateScopedKey default: %v", err)
	}
	if defaulted.RateLimitPerMin != model.DefaultScopedKeyRateLimitPerMin {
		t.Fatalf("default rateLimitPerMin = %d, want %d", defaulted.RateLimitPerMin, model.DefaultScopedKeyRateLimitPerMin)
	}

	limited := 120
	in := scopedKeyInput("")
	in.Label = "rate limited"
	in.RateLimitPerMin = &limited
	key, _, err := st.CreateScopedKey(in)
	if err != nil {
		t.Fatalf("CreateScopedKey limited: %v", err)
	}
	if key.RateLimitPerMin != 120 {
		t.Fatalf("explicit rateLimitPerMin = %d, want 120", key.RateLimitPerMin)
	}

	unlimited := 0
	in = scopedKeyInput("")
	in.Label = "unlimited"
	in.RateLimitPerMin = &unlimited
	unlimitedKey, _, err := st.CreateScopedKey(in)
	if err != nil {
		t.Fatalf("CreateScopedKey unlimited: %v", err)
	}
	if unlimitedKey.RateLimitPerMin != 0 {
		t.Fatalf("explicit zero rateLimitPerMin = %d, want 0", unlimitedKey.RateLimitPerMin)
	}

	data, err := st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	fresh := NewEmpty()
	if err := fresh.Restore(data); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	for id, want := range map[string]int{
		defaulted.ID:    model.DefaultScopedKeyRateLimitPerMin,
		key.ID:          120,
		unlimitedKey.ID: 0,
	} {
		got, ok := fresh.ScopedKey(id)
		if !ok {
			t.Fatalf("restored store has no key %q", id)
		}
		if got.RateLimitPerMin != want {
			t.Fatalf("restored %q rateLimitPerMin = %d, want %d", id, got.RateLimitPerMin, want)
		}
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

// TestScopedKeyProxyConnect pins the gate credential scope: proxy:connect is
// part of the vocabulary, it is account-bound like the account/catalog/lease
// scopes (a non-admin key must name a grant and that grant narrows the gate
// account), and only an active key resolves through AuthenticateScopedKey.
func TestScopedKeyProxyConnect(t *testing.T) {
	st := New()

	// A proxy key without a grant would mean "every account"; the create rule
	// refuses it like any other account-bound scope.
	if _, _, err := st.CreateScopedKey(model.ScopedKeyCreateRequest{
		UserID: "u-01", Label: "gate without grant", Scopes: []model.Scope{model.ScopeProxyConnect},
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("proxy:connect without a grant error = %v, want ErrInvalid", err)
	}

	key, secret, err := st.CreateScopedKey(model.ScopedKeyCreateRequest{
		UserID:     "u-01",
		Label:      "gate",
		Scopes:     []model.Scope{model.ScopeProxyConnect},
		AccountIDs: []string{"acc-01"},
	})
	if err != nil {
		t.Fatalf("CreateScopedKey: %v", err)
	}
	if !key.HasScope(model.ScopeProxyConnect) {
		t.Fatalf("scopes = %v, want proxy:connect", key.Scopes)
	}
	if key.HasScope(model.ScopeLeasesRead) {
		t.Fatal("HasScope granted a scope the key does not carry")
	}
	if !key.RequiresAccountGrant() || !key.HasAccountGrant() {
		t.Fatalf("proxy:connect key = %+v, want an account grant requirement", key)
	}
	if !key.AllowsAccount(model.Account{ID: "acc-01"}) {
		t.Fatal("proxy:connect key does not allow its granted account")
	}
	if key.AllowsAccount(model.Account{ID: "acc-02"}) {
		t.Fatal("proxy:connect key allows an account outside its grant")
	}

	active, ok := st.AuthenticateScopedKey(secret)
	if !ok || active.ID != key.ID || !active.HasScope(model.ScopeProxyConnect) {
		t.Fatalf("AuthenticateScopedKey = %+v, %v; want the active proxy key", active, ok)
	}

	// A key without proxy:connect is a valid key that simply cannot open a gate.
	reader, _, err := st.CreateScopedKey(model.ScopedKeyCreateRequest{
		UserID: "u-01", Label: "reader", Scopes: []model.Scope{model.ScopeLeasesRead},
		AccountIDs: []string{"acc-01"},
	})
	if err != nil {
		t.Fatalf("CreateScopedKey reader: %v", err)
	}
	if reader.HasScope(model.ScopeProxyConnect) {
		t.Fatal("leases:read key reports proxy:connect")
	}

	if err := st.RevokeScopedKey(key.ID); err != nil {
		t.Fatalf("RevokeScopedKey: %v", err)
	}
	if _, ok := st.AuthenticateScopedKey(secret); ok {
		t.Fatal("revoked proxy:connect key authenticated")
	}
}
