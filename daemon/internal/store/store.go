// Package store is a mutex-protected in-memory implementation of the panel
// state. It is deliberately the only mutable state in the daemon: the HTTP
// layer validates and translates, nothing else.
package store

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"ajsaccountd/internal/model"
)

// Store errors are matched with errors.Is by the API layer.
var (
	ErrInvalid  = errors.New("invalid argument")
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
	ErrUpstream = errors.New("upstream unavailable")
)

// Error carries a client-safe message while keeping a matchable kind.
type Error struct {
	Kind    error
	Message string
}

func (e *Error) Error() string { return e.Message }

func (e *Error) Unwrap() error { return e.Kind }

func invalidf(format string, args ...any) error {
	return &Error{Kind: ErrInvalid, Message: fmt.Sprintf(format, args...)}
}

func notFoundf(format string, args ...any) error {
	return &Error{Kind: ErrNotFound, Message: fmt.Sprintf(format, args...)}
}

func conflictf(format string, args ...any) error {
	return &Error{Kind: ErrConflict, Message: fmt.Sprintf(format, args...)}
}

func upstreamf(format string, args ...any) error {
	return &Error{Kind: ErrUpstream, Message: fmt.Sprintf(format, args...)}
}

// Sealer encrypts account credentials at rest. Open must reject a blob sealed
// for a different account id. *secret.Store implements it; tests may use a
// stub.
type Sealer interface {
	Seal(accountID string, plaintext []byte) ([]byte, error)
	Open(accountID string, sealed []byte) ([]byte, error)
}

// Store holds every panel collection plus the sequence counters used for new
// identifiers. All exported methods are safe for concurrent use.
type Store struct {
	mu  sync.RWMutex
	now func() time.Time

	accounts []model.Account
	nodes    []model.ProxyNode
	leases   []model.Lease
	users    []model.PanelUser
	keys     []model.APIKey
	requests []model.RequestLog
	audit    []model.AuditEvent
	settings model.PanelSettings

	// keyHashes maps key id to the SHA-256 of the secret. Plaintext secrets
	// exist only in the CreateKey return value.
	keyHashes map[string]string

	// sealer encrypts account passwords when configured; secrets holds the
	// sealed blobs. Neither is part of model.Account or any JSON response.
	sealer  Sealer
	secrets map[string][]byte

	accountSeq  int
	userSeq     int
	keySeq      int
	mutationSeq int
	auditSeq    int
}

// New returns a store seeded with the dashboard mock dataset. Account
// passwords are validated and discarded.
func New() *Store {
	return newWithClock(time.Now, nil)
}

// NewWithSealer returns a store that seals every created account's password
// through sealer instead of discarding it.
func NewWithSealer(sealer Sealer) *Store {
	return newWithClock(time.Now, sealer)
}

func newWithClock(now func() time.Time, sealer Sealer) *Store {
	stamp := now()
	users := buildUsers(stamp)
	nodes := buildNodes(stamp)
	s := &Store{
		now:        now,
		accounts:   buildAccounts(stamp),
		nodes:      nodes,
		leases:     buildLeases(stamp),
		users:      users,
		keys:       buildKeys(stamp),
		requests:   buildRequests(stamp, users, nodes),
		audit:      buildAudit(stamp),
		settings:   defaultSettings(),
		keyHashes:  make(map[string]string),
		sealer:     sealer,
		secrets:    make(map[string][]byte),
		accountSeq: 6,
		userSeq:    4,
		keySeq:     6,
		auditSeq:   seedAudit,
	}
	s.syncAccountCountsLocked()
	return s
}

// Accounts returns a snapshot in seed order.
func (s *Store) Accounts() []model.Account {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.Account, len(s.accounts))
	for i, account := range s.accounts {
		out[i] = cloneAccount(account)
	}
	return out
}

// Account returns one account by id.
func (s *Store) Account(id string) (model.Account, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	idx := s.accountIndexLocked(id)
	if idx < 0 {
		return model.Account{}, false
	}
	return cloneAccount(s.accounts[idx]), true
}

// AccountSecret returns the account password in plaintext for the account
// client. It is deliberately not exposed over HTTP. A store built with New has
// no sealer and reports ErrNotFound for every account.
func (s *Store) AccountSecret(id string) ([]byte, error) {
	s.mu.RLock()
	idx := s.accountIndexLocked(id)
	sealer := s.sealer
	sealed, stored := s.secrets[id]
	s.mu.RUnlock()

	if idx < 0 {
		return nil, notFoundf("account %q not found", id)
	}
	if sealer == nil || !stored {
		return nil, notFoundf("account %q has no stored secret", id)
	}
	plaintext, err := sealer.Open(id, sealed)
	if err != nil {
		return nil, fmt.Errorf("account %q secret: %w", id, err)
	}
	return plaintext, nil
}

// Nodes returns a snapshot in seed order.
func (s *Store) Nodes() []model.ProxyNode {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.ProxyNode, len(s.nodes))
	copy(out, s.nodes)
	return out
}

// Node returns one node by id.
func (s *Store) Node(id string) (model.ProxyNode, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	idx := s.nodeIndexLocked(id)
	if idx < 0 {
		return model.ProxyNode{}, false
	}
	return s.nodes[idx], true
}

// Leases returns a snapshot in seed order.
func (s *Store) Leases() []model.Lease {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.Lease, len(s.leases))
	copy(out, s.leases)
	return out
}

// Lease returns one lease by id.
func (s *Store) Lease(id string) (model.Lease, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	idx := s.leaseIndexLocked(id)
	if idx < 0 {
		return model.Lease{}, false
	}
	return s.leases[idx], true
}

// Users returns a snapshot in seed order.
func (s *Store) Users() []model.PanelUser {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.PanelUser, len(s.users))
	for i, user := range s.users {
		out[i] = cloneUser(user)
	}
	return out
}

// User returns one panel user by id.
func (s *Store) User(id string) (model.PanelUser, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	idx := s.userIndexLocked(id)
	if idx < 0 {
		return model.PanelUser{}, false
	}
	return cloneUser(s.users[idx]), true
}

// Keys returns a snapshot in seed order.
func (s *Store) Keys() []model.APIKey {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.APIKey, len(s.keys))
	for i, key := range s.keys {
		out[i] = cloneKey(key)
	}
	return out
}

// Key returns one API key by id.
func (s *Store) Key(id string) (model.APIKey, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	idx := s.keyIndexLocked(id)
	if idx < 0 {
		return model.APIKey{}, false
	}
	return cloneKey(s.keys[idx]), true
}

// SecretHash returns the stored SHA-256 of a created key's secret. Seeded keys
// have no hash because their secrets never existed in this process.
func (s *Store) SecretHash(id string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	hash, ok := s.keyHashes[id]
	return hash, ok
}

// Requests returns request logs newest first.
func (s *Store) Requests() []model.RequestLog {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.RequestLog, len(s.requests))
	copy(out, s.requests)
	return out
}

// Audit returns up to limit events newest first.
func (s *Store) Audit(limit int) []model.AuditEvent {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit < 0 {
		limit = 0
	}
	if limit > len(s.audit) {
		limit = len(s.audit)
	}
	out := make([]model.AuditEvent, limit)
	copy(out, s.audit[:limit])
	return out
}

// Settings returns the current runtime settings.
func (s *Store) Settings() model.PanelSettings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings
}

// CreateAccount validates input, prepends the account and appends an audit
// event. When a Sealer is configured the password is sealed under the new
// account id; otherwise it is validated and discarded.
func (s *Store) CreateAccount(in model.CreateAccountInput) (model.Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	label := strings.TrimSpace(in.Label)
	username := strings.TrimSpace(in.Username)
	switch {
	case label == "":
		return model.Account{}, invalidf("label is required")
	case len(label) > 80:
		return model.Account{}, invalidf("label must be at most 80 characters")
	case username == "":
		return model.Account{}, invalidf("username is required")
	case len(username) > 80:
		return model.Account{}, invalidf("username must be at most 80 characters")
	case strings.TrimSpace(in.Password) == "":
		return model.Account{}, invalidf("password is required")
	case !validPlatform(in.Platform):
		return model.Account{}, invalidf("platform must be one of android, ios, windows, linux")
	}
	concurrency := in.Concurrency
	if concurrency == 0 {
		concurrency = 32
	}
	if concurrency < 1 || concurrency > 2000 {
		return model.Account{}, invalidf("concurrency must be between 1 and 2000")
	}
	regions, err := normalizeRegions(in.Regions)
	if err != nil {
		return model.Account{}, err
	}
	for _, existing := range s.accounts {
		if strings.EqualFold(existing.Label, label) {
			return model.Account{}, conflictf("an account named %q already exists", label)
		}
	}

	now := s.now()
	id := fmt.Sprintf("acc-%02d", s.accountSeq+1)
	var sealed []byte
	if s.sealer != nil {
		sealed, err = s.sealer.Seal(id, []byte(in.Password))
		if err != nil {
			return model.Account{}, fmt.Errorf("seal account secret: %w", err)
		}
	}
	s.accountSeq++
	account := model.Account{
		ID:          id,
		Label:       label,
		Username:    username,
		Platform:    in.Platform,
		State:       model.AccountActive,
		Concurrency: concurrency,
		Regions:     regions,
		UsageBytes:  0,
		ExpiresAt:   iso(now.Add(30 * 24 * time.Hour)),
		LastLoginAt: iso(now),
		CreatedAt:   iso(now),
	}
	if sealed != nil {
		s.secrets[id] = sealed
	}
	s.accounts = append([]model.Account{account}, s.accounts...)
	s.addAuditLocked(model.SeverityInfo, "operator", "account.create", label,
		fmt.Sprintf("platform %s · concurrency %d", in.Platform, concurrency))
	return cloneAccount(account), nil
}

// UpdateAccount applies a partial entitlement update.
func (s *Store) UpdateAccount(id string, patch model.AccountPatch) (model.Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := s.accountIndexLocked(id)
	if idx < 0 {
		return model.Account{}, notFoundf("account %q not found", id)
	}
	next := s.accounts[idx]
	changed := make([]string, 0, 6)

	if patch.Label != nil {
		label := strings.TrimSpace(*patch.Label)
		if label == "" {
			return model.Account{}, invalidf("label is required")
		}
		if len(label) > 80 {
			return model.Account{}, invalidf("label must be at most 80 characters")
		}
		for i, other := range s.accounts {
			if i != idx && strings.EqualFold(other.Label, label) {
				return model.Account{}, conflictf("an account named %q already exists", label)
			}
		}
		if label != next.Label {
			next.Label = label
			changed = append(changed, "label")
		}
	}
	if patch.Username != nil {
		username := strings.TrimSpace(*patch.Username)
		if username == "" {
			return model.Account{}, invalidf("username is required")
		}
		if username != next.Username {
			next.Username = username
			changed = append(changed, "username")
		}
	}
	if patch.Platform != nil {
		if !validPlatform(*patch.Platform) {
			return model.Account{}, invalidf("platform must be one of android, ios, windows, linux")
		}
		if *patch.Platform != next.Platform {
			next.Platform = *patch.Platform
			changed = append(changed, "platform")
		}
	}
	if patch.State != nil {
		if !validAccountState(*patch.State) {
			return model.Account{}, invalidf("state must be one of active, paused, auth_failed, expired")
		}
		if *patch.State != next.State {
			next.State = *patch.State
			changed = append(changed, "state")
		}
	}
	if patch.Concurrency != nil {
		if *patch.Concurrency < 1 || *patch.Concurrency > 2000 {
			return model.Account{}, invalidf("concurrency must be between 1 and 2000")
		}
		if *patch.Concurrency != next.Concurrency {
			next.Concurrency = *patch.Concurrency
			changed = append(changed, "concurrency")
		}
	}
	if patch.Regions != nil {
		regions, err := normalizeRegions(*patch.Regions)
		if err != nil {
			return model.Account{}, err
		}
		next.Regions = regions
		changed = append(changed, "regions")
	}
	if patch.ExpiresAt != nil {
		parsed, err := time.Parse(time.RFC3339, *patch.ExpiresAt)
		if err != nil {
			return model.Account{}, invalidf("expiresAt must be an RFC 3339 timestamp")
		}
		next.ExpiresAt = iso(parsed)
		changed = append(changed, "expiresAt")
	}
	if len(changed) == 0 {
		return cloneAccount(next), nil
	}

	s.accounts[idx] = next
	// Labels are denormalised onto nodes and leases; keep them in sync.
	for i := range s.nodes {
		if s.nodes[i].AccountID == id {
			s.nodes[i].AccountLabel = next.Label
		}
	}
	for i := range s.leases {
		if s.leases[i].AccountID == id {
			s.leases[i].AccountLabel = next.Label
		}
	}
	s.addAuditLocked(model.SeverityInfo, "operator", "account.update", next.Label, "fields: "+strings.Join(changed, ", "))
	return cloneAccount(next), nil
}

// DeleteAccount removes the account, its nodes, its leases and its sealed
// secret. Requests and audit history are retained.
func (s *Store) DeleteAccount(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := s.accountIndexLocked(id)
	if idx < 0 {
		return notFoundf("account %q not found", id)
	}
	label := s.accounts[idx].Label
	nodesRemoved := 0
	keptNodes := s.nodes[:0]
	for _, node := range s.nodes {
		if node.AccountID == id {
			nodesRemoved++
			continue
		}
		keptNodes = append(keptNodes, node)
	}
	s.nodes = keptNodes
	leasesRemoved := 0
	keptLeases := s.leases[:0]
	for _, lease := range s.leases {
		if lease.AccountID == id {
			leasesRemoved++
			continue
		}
		keptLeases = append(keptLeases, lease)
	}
	s.leases = keptLeases
	s.accounts = append(s.accounts[:idx], s.accounts[idx+1:]...)
	delete(s.secrets, id)
	s.syncAccountCountsLocked()
	s.addAuditLocked(model.SeverityInfo, "operator", "account.delete", label,
		fmt.Sprintf("%d nodes and %d leases removed", nodesRemoved, leasesRemoved))
	return nil
}

// LoginAccount refreshes the session timestamp and clears an auth failure.
func (s *Store) LoginAccount(id string) (model.Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := s.accountIndexLocked(id)
	if idx < 0 {
		return model.Account{}, notFoundf("account %q not found", id)
	}
	account := &s.accounts[idx]
	account.LastLoginAt = iso(s.now())
	if account.State == model.AccountAuthFailed {
		account.State = model.AccountActive
	}
	s.addAuditLocked(model.SeverityInfo, "operator", "account.login", account.Label, "Session refreshed")
	return cloneAccount(*account), nil
}

// MarkLogin records a successful vendor login. The state becomes active, or
// expired when the vendor reports an expired membership; LastLoginAt is now,
// the vendor's canonical user name replaces the stored one when non-empty, and
// an account.login audit event records the membership. It never stores a token
// or connect password.
func (s *Store) MarkLogin(id, userName, membership string, expired bool) (model.Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := s.accountIndexLocked(id)
	if idx < 0 {
		return model.Account{}, notFoundf("account %q not found", id)
	}
	account := &s.accounts[idx]
	account.LastLoginAt = iso(s.now())
	if userName = clientMessage(userName); userName != "" {
		account.Username = userName
	}
	detail := "Session established"
	if membership = clientMessage(membership); membership != "" {
		detail += " · membership " + membership
	}
	if expired {
		account.State = model.AccountExpired
		detail += " · expired"
	} else {
		account.State = model.AccountActive
	}
	s.addAuditLocked(model.SeverityInfo, "operator", "account.login", account.Label, detail)
	return cloneAccount(*account), nil
}

// MarkAuthFailed records a vendor authentication rejection. The state becomes
// auth_failed; message is reduced to one bounded line, so a caller must pass
// only the vendor's AuthResult/AuthMessage and never a password or token.
func (s *Store) MarkAuthFailed(id, message string) (model.Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := s.accountIndexLocked(id)
	if idx < 0 {
		return model.Account{}, notFoundf("account %q not found", id)
	}
	account := &s.accounts[idx]
	account.State = model.AccountAuthFailed
	detail := clientMessage(message)
	if detail == "" {
		detail = "Authentication rejected"
	}
	s.addAuditLocked(model.SeverityError, "operator", "account.auth_failed", account.Label, detail)
	return cloneAccount(*account), nil
}

// LogoutAccount stops the account's sessions by pausing it and draining every
// node that belongs to it.
func (s *Store) LogoutAccount(id string) (model.Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := s.accountIndexLocked(id)
	if idx < 0 {
		return model.Account{}, notFoundf("account %q not found", id)
	}
	now := s.now()
	account := &s.accounts[idx]
	account.State = model.AccountPaused
	draining := 0
	for i := range s.nodes {
		if s.nodes[i].AccountID != id || s.nodes[i].State == model.NodeDown {
			continue
		}
		s.nodes[i].State = model.NodeDraining
		s.nodes[i].LastProbeAt = iso(now)
		draining++
	}
	s.syncAccountCountsLocked()
	s.addAuditLocked(model.SeverityInfo, "operator", "account.logout", account.Label,
		fmt.Sprintf("Sessions stopped · %d nodes draining", draining))
	return cloneAccount(*account), nil
}

func (s *Store) accountIndexLocked(id string) int {
	for i, account := range s.accounts {
		if account.ID == id {
			return i
		}
	}
	return -1
}

func (s *Store) nodeIndexLocked(id string) int {
	for i, node := range s.nodes {
		if node.ID == id {
			return i
		}
	}
	return -1
}

func (s *Store) leaseIndexLocked(id string) int {
	for i, lease := range s.leases {
		if lease.ID == id {
			return i
		}
	}
	return -1
}

func (s *Store) userIndexLocked(id string) int {
	for i, user := range s.users {
		if user.ID == id {
			return i
		}
	}
	return -1
}

func (s *Store) keyIndexLocked(id string) int {
	for i, key := range s.keys {
		if key.ID == id {
			return i
		}
	}
	return -1
}

func (s *Store) syncAccountCountsLocked() {
	type counts struct{ total, live int }
	byAccount := make(map[string]counts, len(s.accounts))
	for _, node := range s.nodes {
		c := byAccount[node.AccountID]
		c.total++
		if node.State == model.NodeLive {
			c.live++
		}
		byAccount[node.AccountID] = c
	}
	for i := range s.accounts {
		c := byAccount[s.accounts[i].ID]
		s.accounts[i].NodeCount = c.total
		s.accounts[i].LiveNodeCount = c.live
	}
}

func (s *Store) addAuditLocked(severity model.Severity, actor, action, target, detail string) {
	s.auditSeq++
	event := model.AuditEvent{
		ID:       fmt.Sprintf("audit-%03d", s.auditSeq),
		TS:       iso(s.now()),
		Severity: severity,
		Actor:    actor,
		Action:   action,
		Target:   target,
		Detail:   detail,
	}
	s.audit = append([]model.AuditEvent{event}, s.audit...)
}

// clientMessage reduces a vendor-provided string to one bounded line for an
// audit detail. It removes line breaks and control spacing but does not guess
// at secrets: callers must pass only a result code or message, never a
// credential.
func clientMessage(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	const maxRunes = 200
	if runes := []rune(value); len(runes) > maxRunes {
		value = string(runes[:maxRunes]) + "…"
	}
	return value
}

func cloneAccount(account model.Account) model.Account {
	account.Regions = append([]string{}, account.Regions...)
	return account
}

func cloneUser(user model.PanelUser) model.PanelUser {
	user.Allowlist = append([]string{}, user.Allowlist...)
	return user
}

func cloneKey(key model.APIKey) model.APIKey {
	if key.LastUsedAt != nil {
		key.LastUsedAt = ptrString(*key.LastUsedAt)
	}
	if key.RevokedAt != nil {
		key.RevokedAt = ptrString(*key.RevokedAt)
	}
	return key
}

func validPlatform(platform model.Platform) bool {
	switch platform {
	case model.PlatformAndroid, model.PlatformIOS, model.PlatformWindows, model.PlatformLinux:
		return true
	default:
		return false
	}
}

func validAccountState(state model.AccountState) bool {
	switch state {
	case model.AccountActive, model.AccountPaused, model.AccountAuthFailed, model.AccountExpired:
		return true
	default:
		return false
	}
}

func normalizeRegions(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, raw := range in {
		region := strings.TrimSpace(raw)
		if region == "" {
			return nil, invalidf("regions must not contain empty values")
		}
		if len(region) > 64 {
			return nil, invalidf("region %q is too long", region)
		}
		if _, ok := seen[region]; ok {
			continue
		}
		seen[region] = struct{}{}
		out = append(out, region)
	}
	return out, nil
}
