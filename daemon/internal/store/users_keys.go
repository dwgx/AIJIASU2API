package store

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"ajsaccountd/internal/model"
)

// CreateUser validates and prepends a panel identity.
func (s *Store) CreateUser(in model.CreateUserInput) (model.PanelUser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	name := strings.TrimSpace(in.Name)
	switch {
	case name == "":
		return model.PanelUser{}, invalidf("name is required")
	case len(name) > 80:
		return model.PanelUser{}, invalidf("name must be at most 80 characters")
	}
	for _, existing := range s.users {
		if strings.EqualFold(existing.Name, name) {
			return model.PanelUser{}, conflictf("a user named %q already exists", name)
		}
	}
	platform := strings.TrimSpace(in.Platform)
	if platform == "" {
		platform = slugify(name)
	}
	if len(platform) > 64 {
		return model.PanelUser{}, invalidf("platform must be at most 64 characters")
	}
	stickyTTL := strings.TrimSpace(in.StickyTTL)
	if stickyTTL == "" {
		stickyTTL = "8h"
	}
	if !validTTL(stickyTTL) {
		return model.PanelUser{}, invalidf("stickyTtl must be a positive duration such as 1h, 8h, 24h or 7d")
	}
	if in.QuotaBytesMonth != nil && *in.QuotaBytesMonth < 0 {
		return model.PanelUser{}, invalidf("quotaBytesMonth must be zero or positive")
	}
	allowlist := in.Allowlist
	if allowlist == nil {
		allowlist = []string{"127.0.0.1/32"}
	}
	normalized, err := normalizeAllowlist(allowlist)
	if err != nil {
		return model.PanelUser{}, err
	}

	now := s.now()
	s.userSeq++
	user := model.PanelUser{
		ID:              fmt.Sprintf("u-%02d", s.userSeq),
		Name:            name,
		Platform:        platform,
		StickyTTL:       stickyTTL,
		QuotaBytesMonth: copyInt64(in.QuotaBytesMonth),
		UsedBytes:       0,
		Enabled:         true,
		Allowlist:       normalized,
		CreatedAt:       iso(now),
	}
	s.users = append([]model.PanelUser{user}, s.users...)
	s.addAuditLocked(model.SeverityInfo, "operator", "user.create", name,
		fmt.Sprintf("platform %s · sticky ttl %s", platform, stickyTTL))
	return cloneUser(user), nil
}

// UpdateUser applies a partial identity update.
func (s *Store) UpdateUser(id string, patch model.UserPatch) (model.PanelUser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := s.userIndexLocked(id)
	if idx < 0 {
		return model.PanelUser{}, notFoundf("user %q not found", id)
	}
	user := s.users[idx]
	changed := make([]string, 0, 4)

	if patch.Platform != nil {
		platform := strings.TrimSpace(*patch.Platform)
		if platform == "" || len(platform) > 64 {
			return model.PanelUser{}, invalidf("platform must be between 1 and 64 characters")
		}
		if platform != user.Platform {
			user.Platform = platform
			changed = append(changed, "platform")
		}
	}
	if patch.StickyTTL != nil {
		ttl := strings.TrimSpace(*patch.StickyTTL)
		if !validTTL(ttl) {
			return model.PanelUser{}, invalidf("stickyTtl must be a positive duration such as 1h, 8h, 24h or 7d")
		}
		if ttl != user.StickyTTL {
			user.StickyTTL = ttl
			changed = append(changed, "stickyTtl")
		}
	}
	if patch.QuotaSet {
		if patch.QuotaBytesMonth != nil && *patch.QuotaBytesMonth < 0 {
			return model.PanelUser{}, invalidf("quotaBytesMonth must be zero or positive")
		}
		user.QuotaBytesMonth = copyInt64(patch.QuotaBytesMonth)
		changed = append(changed, "quotaBytesMonth")
	}
	if patch.Allowlist != nil {
		allowlist, err := normalizeAllowlist(*patch.Allowlist)
		if err != nil {
			return model.PanelUser{}, err
		}
		user.Allowlist = allowlist
		changed = append(changed, "allowlist")
	}
	if len(changed) == 0 {
		return cloneUser(user), nil
	}
	s.users[idx] = user
	s.addAuditLocked(model.SeverityInfo, "operator", "user.update", user.Name, "fields: "+strings.Join(changed, ", "))
	return cloneUser(user), nil
}

// DeleteUser releases the user's leases, revokes their keys and removes the
// identity. Historical request rows are retained.
func (s *Store) DeleteUser(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := s.userIndexLocked(id)
	if idx < 0 {
		return notFoundf("user %q not found", id)
	}
	name := s.users[idx].Name
	leasesRemoved := 0
	keptLeases := s.leases[:0]
	for _, lease := range s.leases {
		if lease.UserID == id {
			leasesRemoved++
			if nodeIdx := s.nodeIndexLocked(lease.NodeID); nodeIdx >= 0 && s.nodes[nodeIdx].InUse > 0 {
				s.nodes[nodeIdx].InUse--
			}
			continue
		}
		keptLeases = append(keptLeases, lease)
	}
	s.leases = keptLeases
	keysRevoked := 0
	now := iso(s.now())
	for i := range s.keys {
		if s.keys[i].UserID == id && s.keys[i].RevokedAt == nil {
			s.keys[i].RevokedAt = ptrString(now)
			keysRevoked++
		}
	}
	for i := range s.scopedKeys {
		if s.scopedKeys[i].UserID == id && s.scopedKeys[i].RevokedAt == nil {
			s.scopedKeys[i].RevokedAt = ptrString(now)
			keysRevoked++
		}
	}
	s.users = append(s.users[:idx], s.users[idx+1:]...)
	s.addAuditLocked(model.SeverityInfo, "operator", "user.delete", name,
		fmt.Sprintf("%d leases released · %d keys revoked", leasesRemoved, keysRevoked))
	return nil
}

// SetUserEnabled is the kill switch behind suspend and resume.
func (s *Store) SetUserEnabled(id string, enabled bool) (model.PanelUser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := s.userIndexLocked(id)
	if idx < 0 {
		return model.PanelUser{}, notFoundf("user %q not found", id)
	}
	user := &s.users[idx]
	user.Enabled = enabled
	if enabled {
		s.addAuditLocked(model.SeverityInfo, "operator", "user.resume", user.Name, "Kill switch released")
	} else {
		s.addAuditLocked(model.SeverityWarn, "operator", "user.suspend", user.Name, "Kill switch engaged · requests fail closed")
	}
	return cloneUser(*user), nil
}

// SetUserAllowlist replaces the user's CIDR allowlist.
func (s *Store) SetUserAllowlist(id string, allowlist []string) (model.PanelUser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := s.userIndexLocked(id)
	if idx < 0 {
		return model.PanelUser{}, notFoundf("user %q not found", id)
	}
	normalized, err := normalizeAllowlist(allowlist)
	if err != nil {
		return model.PanelUser{}, err
	}
	user := &s.users[idx]
	user.Allowlist = normalized
	s.addAuditLocked(model.SeverityInfo, "operator", "user.allowlist", user.Name,
		fmt.Sprintf("%d entries", len(normalized)))
	return cloneUser(*user), nil
}

// CreateKey issues a secret, stores only its hash and returns the secret once.
func (s *Store) CreateKey(userID, label string) (model.APIKey, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	userIdx := s.userIndexLocked(userID)
	if userIdx < 0 {
		return model.APIKey{}, "", notFoundf("user %q not found", userID)
	}
	label = strings.TrimSpace(label)
	if label == "" {
		label = "Untitled key"
	}
	if len(label) > 80 {
		return model.APIKey{}, "", invalidf("label must be at most 80 characters")
	}

	raw := make([]byte, 12)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return model.APIKey{}, "", fmt.Errorf("generate key secret: %w", err)
	}
	secret := "ajs_live_" + hex.EncodeToString(raw)
	sum := sha256.Sum256([]byte(secret))

	now := s.now()
	s.keySeq++
	key := model.APIKey{
		ID:        fmt.Sprintf("key-%02d", s.keySeq),
		UserID:    s.users[userIdx].ID,
		UserName:  s.users[userIdx].Name,
		Label:     label,
		Prefix:    secret[:16],
		CreatedAt: iso(now),
	}
	s.keys = append([]model.APIKey{key}, s.keys...)
	s.keyHashes[key.ID] = hex.EncodeToString(sum[:])
	s.addAuditLocked(model.SeverityInfo, "operator", "key.create", key.UserName, "Key prefix "+key.Prefix)
	return cloneKey(key), secret, nil
}

// RevokeKey marks a key revoked. Revoking an already-revoked key is a no-op.
func (s *Store) RevokeKey(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := s.keyIndexLocked(id)
	if idx < 0 {
		return notFoundf("key %q not found", id)
	}
	key := &s.keys[idx]
	if key.RevokedAt != nil {
		return nil
	}
	key.RevokedAt = ptrString(iso(s.now()))
	s.addAuditLocked(model.SeverityInfo, "operator", "key.revoke", key.UserName, "Key prefix "+key.Prefix+" revoked")
	return nil
}

// CreateMutation appends a synthetic request log row for the mutation lab.
func (s *Store) CreateMutation(in model.MutationLabInput) (model.RequestLog, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	nodeIdx := s.nodeIndexLocked(in.NodeID)
	if nodeIdx < 0 {
		return model.RequestLog{}, notFoundf("node %q not found", in.NodeID)
	}
	method := strings.TrimSpace(in.Method)
	target := strings.TrimSpace(in.Target)
	if method == "" {
		return model.RequestLog{}, invalidf("method is required")
	}
	if len(method) > 16 {
		return model.RequestLog{}, invalidf("method is too long")
	}
	if target == "" {
		return model.RequestLog{}, invalidf("target is required")
	}
	if len(target) > 255 {
		return model.RequestLog{}, invalidf("target is too long")
	}
	node := s.nodes[nodeIdx]

	userID, userName := "u-01", "ada.ops"
	if len(s.users) > 0 {
		userID, userName = s.users[0].ID, s.users[0].Name
	}
	latency := node.LatencyMs + 12
	if latency < 18 {
		latency = 18
	}
	s.mutationSeq++
	entry := model.RequestLog{
		ID:           fmt.Sprintf("req-mut-%d", s.mutationSeq),
		TS:           iso(s.now()),
		UserID:       userID,
		UserName:     userName,
		AccountLabel: node.AccountLabel,
		NodeLabel:    node.Label,
		Region:       node.Region,
		Method:       method,
		Target:       target,
		Status:       200,
		Bytes:        1024 + int64(len(in.Body)),
		LatencyMs:    latency,
		Source:       model.SourceMutationLab,
	}
	s.requests = prependCapped(s.requests, entry, RequestRetention)
	s.addAuditLocked(model.SeverityInfo, "operator", "request.mutation", node.Label,
		fmt.Sprintf("%s %s · %d bytes", method, target, entry.Bytes))
	return entry, nil
}

// gateRequestSeq orders metered rows inside one process. The id also carries
// the append timestamp, so a row retained across a restart keeps a distinct id
// even when the sequence restarts.
var gateRequestSeq atomic.Uint64

// AppendRequest prepends one metered request-log row, stamping the id and
// timestamp when the caller leaves them empty. It applies the same
// RequestRetention cap as every other writer, so gate metering can never grow
// the snapshot beyond the documented 10,000 rows. It returns the stored row.
//
// The caller owns the row's content. The gate meter passes host:port targets
// only (never a URL, header or credential) and sets SourceGate.
func (s *Store) AppendRequest(entry model.RequestLog) model.RequestLog {
	s.mu.Lock()
	defer s.mu.Unlock()

	if entry.ID == "" {
		entry.ID = fmt.Sprintf("req-gate-%d-%d", s.now().UnixNano(), gateRequestSeq.Add(1))
	}
	if entry.TS == "" {
		entry.TS = iso(s.now())
	}
	s.requests = prependCapped(s.requests, entry, RequestRetention)
	return entry
}

// UpdateSettings applies a validated partial settings update.
func (s *Store) UpdateSettings(patch model.SettingsPatch) (model.PanelSettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	next := s.settings
	changed := make([]string, 0, 5)
	if patch.Transport != nil {
		if *patch.Transport != model.TransportHTTPUpgrade && *patch.Transport != model.TransportRawTCP {
			return model.PanelSettings{}, invalidf("transport must be httpupgrade or rawtcp")
		}
		if *patch.Transport != next.Transport {
			next.Transport = *patch.Transport
			changed = append(changed, "transport")
		}
	}
	if patch.HealthIntervalSec != nil {
		if *patch.HealthIntervalSec < 1 || *patch.HealthIntervalSec > 86400 {
			return model.PanelSettings{}, invalidf("healthIntervalSec must be between 1 and 86400")
		}
		if *patch.HealthIntervalSec != next.HealthIntervalSec {
			next.HealthIntervalSec = *patch.HealthIntervalSec
			changed = append(changed, "healthIntervalSec")
		}
	}
	if patch.LeaseTTL != nil {
		ttl := strings.TrimSpace(*patch.LeaseTTL)
		if !validTTL(ttl) {
			return model.PanelSettings{}, invalidf("leaseTtl must be a positive duration such as 8h or 7d")
		}
		if ttl != next.LeaseTTL {
			next.LeaseTTL = ttl
			changed = append(changed, "leaseTtl")
		}
	}
	if patch.SubscriptionRefreshSec != nil {
		if *patch.SubscriptionRefreshSec < 1 || *patch.SubscriptionRefreshSec > 86400 {
			return model.PanelSettings{}, invalidf("subscriptionRefreshSec must be between 1 and 86400")
		}
		if *patch.SubscriptionRefreshSec != next.SubscriptionRefreshSec {
			next.SubscriptionRefreshSec = *patch.SubscriptionRefreshSec
			changed = append(changed, "subscriptionRefreshSec")
		}
	}
	if patch.Theme != nil {
		if *patch.Theme != model.ThemeDark && *patch.Theme != model.ThemeLight {
			return model.PanelSettings{}, invalidf("theme must be dark or light")
		}
		if *patch.Theme != next.Theme {
			next.Theme = *patch.Theme
			changed = append(changed, "theme")
		}
	}
	if len(changed) == 0 {
		return next, nil
	}
	s.settings = next
	s.addAuditLocked(model.SeverityInfo, "operator", "settings.update", "runtime", "fields: "+strings.Join(changed, ", "))
	return next, nil
}

// Metrics renders the deterministic dashboard series for a window.
func (s *Store) Metrics(window model.MetricRange) (model.Metrics, error) {
	if window != model.Range24h && window != model.Range7d {
		return model.Metrics{}, invalidf("range must be 24h or 7d")
	}
	s.mu.RLock()
	accounts := make([]model.Account, len(s.accounts))
	for i, account := range s.accounts {
		accounts[i] = cloneAccount(account)
	}
	s.mu.RUnlock()
	return buildMetrics(window, s.now(), accounts), nil
}

// Topology returns the node-to-tag map used by the subscription renderer.
func (s *Store) Topology() model.Topology {
	s.mu.RLock()
	defer s.mu.RUnlock()

	nodes := make(map[string]string, len(s.nodes))
	for _, node := range s.nodes {
		nodes[node.ID] = node.Label
	}
	platformSet := make(map[string]struct{}, len(s.users))
	for _, user := range s.users {
		platformSet[user.Platform] = struct{}{}
	}
	platforms := make([]string, 0, len(platformSet))
	for platform := range platformSet {
		platforms = append(platforms, platform)
	}
	sort.Strings(platforms)
	return model.Topology{
		Subscription: "ajs-live",
		Nodes:        nodes,
		Platforms:    platforms,
		GeneratedAt:  iso(s.now()),
	}
}

func normalizeAllowlist(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, raw := range in {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			return nil, invalidf("allowlist entries must not be empty")
		}
		if _, _, err := net.ParseCIDR(entry); err != nil && net.ParseIP(entry) == nil {
			return nil, invalidf("allowlist entry %q is not an IP or CIDR", entry)
		}
		if _, ok := seen[entry]; ok {
			continue
		}
		seen[entry] = struct{}{}
		out = append(out, entry)
	}
	return out, nil
}

func validTTL(value string) bool {
	if value == "" || len(value) > 16 {
		return false
	}
	if strings.HasSuffix(value, "d") {
		days, err := strconv.Atoi(strings.TrimSuffix(value, "d"))
		return err == nil && days > 0
	}
	duration, err := time.ParseDuration(value)
	return err == nil && duration > 0
}

func slugify(value string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		return "ajs-user"
	}
	return slug
}
