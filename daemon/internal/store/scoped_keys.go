package store

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"time"

	"ajsaccountd/internal/model"
)

// CreateScopedKey issues a least-privilege secret, stores only its SHA-256 and
// returns the secret once. The account and region lists restrict reads; an
// empty list means unrestricted.
func (s *Store) CreateScopedKey(in model.ScopedKeyCreateRequest) (model.ScopedKey, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	userID := strings.TrimSpace(in.UserID)
	if userID == "" {
		return model.ScopedKey{}, "", invalidf("userId is required")
	}
	userIdx := s.userIndexLocked(userID)
	if userIdx < 0 {
		return model.ScopedKey{}, "", notFoundf("user %q not found", userID)
	}
	label := strings.TrimSpace(in.Label)
	if label == "" {
		return model.ScopedKey{}, "", invalidf("label is required")
	}
	if len(label) > 80 {
		return model.ScopedKey{}, "", invalidf("label must be at most 80 characters")
	}
	scopes, err := normalizeScopes(in.Scopes)
	if err != nil {
		return model.ScopedKey{}, "", err
	}
	accountIDs, err := normalizeStringList(in.AccountIDs, "accountIds")
	if err != nil {
		return model.ScopedKey{}, "", err
	}
	regions, err := normalizeRegions(in.Regions)
	if err != nil {
		return model.ScopedKey{}, "", err
	}
	rateLimit, err := normalizeRateLimit(in.RateLimitPerMin)
	if err != nil {
		return model.ScopedKey{}, "", err
	}
	expiresAt, err := normalizeExpiresAt(in.ExpiresAt, s.now())
	if err != nil {
		return model.ScopedKey{}, "", err
	}

	raw := make([]byte, 24)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return model.ScopedKey{}, "", fmt.Errorf("generate scoped key secret: %w", err)
	}
	secret := "ajs_sk_" + hex.EncodeToString(raw)
	sum := sha256.Sum256([]byte(secret))

	now := s.now()
	s.scopedKeySeq++
	key := model.ScopedKey{
		ID:              fmt.Sprintf("sk-%02d", s.scopedKeySeq),
		UserID:          s.users[userIdx].ID,
		UserName:        s.users[userIdx].Name,
		Label:           label,
		Prefix:          secret[:16],
		Scopes:          scopes,
		AccountIDs:      accountIDs,
		Regions:         regions,
		RateLimitPerMin: rateLimit,
		CreatedAt:       iso(now),
		ExpiresAt:       expiresAt,
	}
	s.scopedKeys = append([]model.ScopedKey{key}, s.scopedKeys...)
	s.scopedKeyHashes[key.ID] = hex.EncodeToString(sum[:])
	s.addAuditLocked(model.SeverityInfo, "operator", "key.create", key.UserName,
		"Scoped key prefix "+key.Prefix)
	return cloneScopedKey(key), secret, nil
}

// ScopedKeys returns a snapshot in creation order.
func (s *Store) ScopedKeys() []model.ScopedKey {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.ScopedKey, len(s.scopedKeys))
	for i, key := range s.scopedKeys {
		out[i] = cloneScopedKey(key)
	}
	return out
}

// ScopedKey returns one scoped key by id.
func (s *Store) ScopedKey(id string) (model.ScopedKey, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	idx := s.scopedKeyIndexLocked(id)
	if idx < 0 {
		return model.ScopedKey{}, false
	}
	return cloneScopedKey(s.scopedKeys[idx]), true
}

// RevokeScopedKey marks a scoped key revoked. Revoking an already-revoked key
// is a no-op so DELETE stays idempotent.
func (s *Store) RevokeScopedKey(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := s.scopedKeyIndexLocked(id)
	if idx < 0 {
		return notFoundf("scoped key %q not found", id)
	}
	key := &s.scopedKeys[idx]
	if key.RevokedAt != nil {
		return nil
	}
	key.RevokedAt = ptrString(iso(s.now()))
	s.addAuditLocked(model.SeverityInfo, "operator", "key.revoke", key.UserName,
		"Scoped key prefix "+key.Prefix+" revoked")
	return nil
}

// AuthenticateScopedKey resolves a plaintext secret to an active key. The
// secret is hashed, so a revoked, expired or unknown key answers false without
// exposing which condition failed.
func (s *Store) AuthenticateScopedKey(secret string) (model.ScopedKey, bool) {
	sum := sha256.Sum256([]byte(secret))
	hash := hex.EncodeToString(sum[:])

	s.mu.RLock()
	defer s.mu.RUnlock()

	id := ""
	for candidate, stored := range s.scopedKeyHashes {
		if stored == hash {
			id = candidate
			break
		}
	}
	if id == "" {
		return model.ScopedKey{}, false
	}
	idx := s.scopedKeyIndexLocked(id)
	if idx < 0 {
		return model.ScopedKey{}, false
	}
	key := s.scopedKeys[idx]
	if key.RevokedAt != nil {
		return model.ScopedKey{}, false
	}
	if key.ExpiresAt != nil {
		expires, err := time.Parse(time.RFC3339, *key.ExpiresAt)
		if err != nil || !s.now().Before(expires) {
			return model.ScopedKey{}, false
		}
	}
	return cloneScopedKey(key), true
}

// MarkScopedKeyUsed records lastUsedAt at most once per minute per key. The
// throttle map has its own mutex, so the common path takes no store write
// lock; only the first use in a minute does.
func (s *Store) MarkScopedKeyUsed(id string) {
	now := s.now()
	minute := now.Unix() / 60

	s.scopedKeyUseMu.Lock()
	if s.scopedKeyUse == nil {
		s.scopedKeyUse = make(map[string]int64)
	}
	if s.scopedKeyUse[id] == minute {
		s.scopedKeyUseMu.Unlock()
		return
	}
	s.scopedKeyUse[id] = minute
	s.scopedKeyUseMu.Unlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.scopedKeyIndexLocked(id)
	if idx < 0 || s.scopedKeys[idx].RevokedAt != nil {
		return
	}
	s.scopedKeys[idx].LastUsedAt = ptrString(iso(now))
}

func (s *Store) scopedKeyIndexLocked(id string) int {
	for i, key := range s.scopedKeys {
		if key.ID == id {
			return i
		}
	}
	return -1
}

func normalizeScopes(in []model.Scope) ([]model.Scope, error) {
	if len(in) == 0 {
		return nil, invalidf("scopes must contain at least one scope")
	}
	out := make([]model.Scope, 0, len(in))
	seen := make(map[model.Scope]struct{}, len(in))
	for _, raw := range in {
		scope := model.Scope(strings.TrimSpace(string(raw)))
		if !validScope(scope) {
			return nil, invalidf("unknown scope %q", raw)
		}
		if _, ok := seen[scope]; ok {
			continue
		}
		seen[scope] = struct{}{}
		out = append(out, scope)
	}
	return out, nil
}

func validScope(scope model.Scope) bool {
	switch scope {
	case model.ScopeAccountsRead, model.ScopeAccountsWrite,
		model.ScopeCatalogRead, model.ScopeCatalogWrite,
		model.ScopeLeasesRead, model.ScopeLeasesWrite,
		model.ScopeRequestsRead, model.ScopeAuditRead,
		model.ScopeMetricsRead, model.ScopeLabRead,
		model.ScopeSettingsRead, model.ScopeSettingsWrite,
		model.ScopeUsageRead, model.ScopeRenewalRead, model.ScopeHealthRead,
		model.ScopeAdmin:
		return true
	default:
		return false
	}
}

func normalizeStringList(in []string, field string) ([]string, error) {
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, raw := range in {
		value := strings.TrimSpace(raw)
		if value == "" {
			return nil, invalidf("%s must not contain empty values", field)
		}
		if len(value) > 128 {
			return nil, invalidf("%s entry is too long", field)
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out, nil
}

// normalizeRateLimit resolves the optional create field. Omitted means the
// default budget; an explicit 0 means unlimited; a negative value is invalid.
func normalizeRateLimit(in *int) (int, error) {
	if in == nil {
		return model.DefaultScopedKeyRateLimitPerMin, nil
	}
	if *in < 0 {
		return 0, invalidf("rateLimitPerMin must be zero (unlimited) or positive")
	}
	return *in, nil
}

func normalizeExpiresAt(value string, now time.Time) (*string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	expires, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil, invalidf("expiresAt must be an RFC 3339 timestamp")
	}
	if !expires.After(now) {
		return nil, invalidf("expiresAt must be in the future")
	}
	stamp := iso(expires)
	return &stamp, nil
}

func cloneScopedKey(key model.ScopedKey) model.ScopedKey {
	key.Scopes = append([]model.Scope{}, key.Scopes...)
	key.AccountIDs = append([]string{}, key.AccountIDs...)
	key.Regions = append([]string{}, key.Regions...)
	if key.ExpiresAt != nil {
		key.ExpiresAt = ptrString(*key.ExpiresAt)
	}
	if key.LastUsedAt != nil {
		key.LastUsedAt = ptrString(*key.LastUsedAt)
	}
	if key.RevokedAt != nil {
		key.RevokedAt = ptrString(*key.RevokedAt)
	}
	return key
}
