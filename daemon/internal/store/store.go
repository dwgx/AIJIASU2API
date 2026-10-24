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

// Sealer encrypts account credentials at rest and removes them when an account
// is deleted. Open must reject a blob sealed for a different account id; Delete
// must be idempotent and durable before it returns. *secret.Store implements
// it; tests may use a stub.
type Sealer interface {
	Seal(accountID string, plaintext []byte) ([]byte, error)
	Open(accountID string, sealed []byte) ([]byte, error)
	Delete(accountID string) error
}

// Retention caps for the two append-only histories. An insert past the cap
// drops the oldest entry, so memory and the periodic snapshot stay bounded
// however long the daemon runs. Audit events are small and frequent; request
// rows are larger and only the mutation lab appends them.
const (
	AuditRetention   = 1000
	RequestRetention = 10000
)

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

	// auditHook receives every critical audit event after it is appended. It
	// is set once during startup (SetAuditHook) and invoked with s.mu held, so
	// it must neither block nor call back into the Store; the webhook sink only
	// enqueues. The hook is not part of the snapshot.
	auditHook func(model.AuditEvent)

	// scopedKeys are the least-privilege keys; scopedKeyHashes maps their id to
	// the SHA-256 of the secret, never the secret itself.
	scopedKeys      []model.ScopedKey
	scopedKeyHashes map[string]string

	// scopedKeyUseMu guards scopedKeyUse, which throttles lastUsedAt writes to
	// one per key per minute. It is never held while s.mu is taken.
	scopedKeyUseMu sync.Mutex
	scopedKeyUse   map[string]int64

	// keyHashes maps key id to the SHA-256 of the secret. Plaintext secrets
	// exist only in the CreateKey return value.
	keyHashes map[string]string

	// sealer encrypts account passwords when configured; secrets holds the
	// sealed blobs. Neither is part of model.Account or any JSON response.
	sealer  Sealer
	secrets map[string][]byte

	// devices holds the stable, non-secret device identity of each account.
	// It is persisted in the state snapshot and never regenerated. With the
	// device pool enabled it is a mirror of the bound records' material; the
	// pool and the sealed material file are the source of truth.
	devices map[string]DeviceIdentity

	// deviceSealer seals raw device material; nil disables the pool. deviceMu
	// serialises material I/O so a seal never blocks API reads, and pool
	// mutations so two first logins cannot bind two records.
	deviceSealer   DeviceSealer
	deviceMu       sync.Mutex
	deviceCfg      DevicePoolConfig
	devicePool     map[string]*DeviceRecord
	deviceBindings map[string]string
	deviceMaterial map[string]DeviceMaterial
	deviceWarnings []string

	// selections is the persisted operator target per account (DESIGN-CATALOG
	// §3.4). It is not a session: setting one starts no gate. serverStats is
	// keyed by account id + ":" + server id and counts switch attempts.
	selections  map[string]model.Selection
	serverStats map[string]model.ServerStat

	// schedules and their bounded histories are the scheduler state
	// (DESIGN-SCHEDULE §3, §6). Runs are newest first; fires are keyed by
	// scheduleId@scheduledUnix. reminderMarks records one emitted renewal
	// notice per account/threshold/expiresAt.
	schedules     []model.Schedule
	scheduleRuns  []model.ScheduleRun
	scheduleFires []model.ScheduleFire
	reminderMarks map[string]model.ReminderMark

	// webhookEndpoints is the persisted receiver metadata. Signing secrets are
	// not here: they live in secret.Store (or process memory) so a state.json
	// snapshot never carries a credential.
	webhookEndpoints []model.WebhookEndpoint

	// pendingLabels holds the lowercased labels of account creates that are
	// sealing their password outside the write lock. A label is reserved
	// before the slow call so a concurrent create or rename cannot take it.
	pendingLabels map[string]struct{}

	accountSeq   int
	userSeq      int
	keySeq       int
	scopedKeySeq int
	mutationSeq  int
	auditSeq     int
	scheduleSeq  int
	runSeq       int
	webhookSeq   int
}

// New returns a store seeded with the dashboard mock dataset. It exists for
// tests and mock-mode comparisons; the daemon starts from NewEmpty. Account
// passwords are validated and discarded.
func New() *Store {
	return newWithClock(time.Now, nil)
}

// NewWithSealer returns a seeded store that seals every created account's
// password through sealer instead of discarding it.
func NewWithSealer(sealer Sealer) *Store {
	return newWithClock(time.Now, sealer)
}

// NewEmpty returns a store with no accounts, nodes, leases, users, keys,
// request rows or audit events. Only the default settings are present. The
// daemon uses it so a fresh install does not present the dashboard mock as
// live data; every sequence starts at zero, so the first created account is
// acc-01.
func NewEmpty() *Store {
	return newEmptyWithClock(time.Now, nil)
}

// NewEmptyWithSealer returns an empty store that seals every created account's
// password through sealer instead of discarding it.
func NewEmptyWithSealer(sealer Sealer) *Store {
	return newEmptyWithClock(time.Now, sealer)
}

// NewEmptyWithSealers returns an empty store with both a password sealer and
// a device-material sealer. The device pool is enabled only when device is
// non-nil; the two files must be distinct (DESIGN-DEVICE-POOL §5.1).
func NewEmptyWithSealers(password Sealer, device DeviceSealer) *Store {
	st := newEmptyWithClock(time.Now, password)
	st.deviceSealer = device
	return st
}

// NewWithSealers is the seeded-store counterpart of NewEmptyWithSealers.
func NewWithSealers(password Sealer, device DeviceSealer) *Store {
	st := newWithClock(time.Now, password)
	st.deviceSealer = device
	return st
}

func newEmptyWithClock(now func() time.Time, sealer Sealer) *Store {
	return &Store{
		now:              now,
		accounts:         []model.Account{},
		nodes:            []model.ProxyNode{},
		leases:           []model.Lease{},
		users:            []model.PanelUser{},
		keys:             []model.APIKey{},
		scopedKeys:       []model.ScopedKey{},
		requests:         []model.RequestLog{},
		audit:            []model.AuditEvent{},
		settings:         defaultSettings(),
		keyHashes:        make(map[string]string),
		scopedKeyHashes:  make(map[string]string),
		scopedKeyUse:     make(map[string]int64),
		sealer:           sealer,
		secrets:          make(map[string][]byte),
		devices:          make(map[string]DeviceIdentity),
		deviceCfg:        defaultDevicePoolConfig(),
		devicePool:       make(map[string]*DeviceRecord),
		deviceBindings:   make(map[string]string),
		deviceMaterial:   make(map[string]DeviceMaterial),
		pendingLabels:    make(map[string]struct{}),
		selections:       make(map[string]model.Selection),
		serverStats:      make(map[string]model.ServerStat),
		schedules:        []model.Schedule{},
		scheduleRuns:     []model.ScheduleRun{},
		scheduleFires:    []model.ScheduleFire{},
		reminderMarks:    make(map[string]model.ReminderMark),
		webhookEndpoints: []model.WebhookEndpoint{},
	}
}

func newWithClock(now func() time.Time, sealer Sealer) *Store {
	stamp := now()
	users := buildUsers(stamp)
	nodes := buildNodes(stamp)
	s := newEmptyWithClock(now, sealer)
	s.accounts = buildAccounts(stamp)
	s.nodes = nodes
	s.leases = buildLeases(stamp)
	s.users = users
	s.keys = buildKeys(stamp)
	s.requests = buildRequests(stamp, users, nodes)
	s.audit = buildAudit(stamp)
	s.accountSeq = 6
	s.userSeq = 4
	s.keySeq = 6
	s.auditSeq = seedAudit
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
	if stored {
		// Copy before unlocking: the map value must not be read after the
		// lock is released, and a concurrent delete or restore must not
		// change what Open authenticates.
		sealed = append([]byte(nil), sealed...)
	}
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

// CreateAccount validates input, reserves the id and label, seals the password
// outside the write lock and then inserts the account. A Sealer's Seal may
// fsync, so holding the store lock across it would block every reader; the
// reservation keeps concurrent creates and renames conflict-safe. Without a
// sealer the password is validated and discarded under the lock.
func (s *Store) CreateAccount(in model.CreateAccountInput) (model.Account, error) {
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

	s.mu.Lock()
	if s.accountLabelTakenLocked(label) {
		s.mu.Unlock()
		return model.Account{}, conflictf("an account named %q already exists", label)
	}
	if s.sealer == nil {
		id := s.reserveAccountIDLocked()
		account := s.insertAccountLocked(id, label, username, in.Platform, concurrency, regions, nil)
		s.mu.Unlock()
		return cloneAccount(account), nil
	}
	id := s.reserveAccountIDLocked()
	s.pendingLabels[strings.ToLower(label)] = struct{}{}
	s.mu.Unlock()

	sealed, err := s.sealer.Seal(id, []byte(in.Password))
	if err != nil {
		s.mu.Lock()
		delete(s.pendingLabels, strings.ToLower(label))
		s.mu.Unlock()
		return model.Account{}, fmt.Errorf("seal account secret: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.pendingLabels, strings.ToLower(label))
	if s.accountLabelTakenLocked(label) {
		// Defensive: the reservation makes this unreachable while every label
		// writer honours pendingLabels.
		return model.Account{}, conflictf("an account named %q already exists", label)
	}
	account := s.insertAccountLocked(id, label, username, in.Platform, concurrency, regions, sealed)
	return cloneAccount(account), nil
}

// reserveAccountIDLocked advances the account sequence and returns the new id.
// A failed seal burns the reserved id rather than risk handing the same id to
// two concurrent creates. The caller holds the write lock.
func (s *Store) reserveAccountIDLocked() string {
	s.accountSeq++
	return fmt.Sprintf("acc-%02d", s.accountSeq)
}

// insertAccountLocked prepends the account, stores its sealed secret and
// records the audit event. The caller holds the write lock.
func (s *Store) insertAccountLocked(id, label, username string, platform model.Platform, concurrency int, regions []string, sealed []byte) model.Account {
	now := s.now()
	account := model.Account{
		ID:          id,
		Label:       label,
		Username:    username,
		Platform:    platform,
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
		fmt.Sprintf("platform %s · concurrency %d", platform, concurrency))
	return account
}

// accountLabelTakenLocked reports whether label is used by an account or
// reserved by an in-flight create. The caller holds at least the read lock.
func (s *Store) accountLabelTakenLocked(label string) bool {
	if _, ok := s.pendingLabels[strings.ToLower(label)]; ok {
		return true
	}
	for _, existing := range s.accounts {
		if strings.EqualFold(existing.Label, label) {
			return true
		}
	}
	return false
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
		if _, pending := s.pendingLabels[strings.ToLower(label)]; pending {
			return model.Account{}, conflictf("an account named %q already exists", label)
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

// DeleteAccount removes the account, its nodes, its leases, its device
// identity and its sealed secret. Requests and audit history are retained.
//
// The sealed entry lives twice: s.secrets, which the next snapshot persists,
// and the sealer's own file. The sealer is pruned first, outside the store lock
// because a sealer write may fsync; a failure there leaves the account and its
// secret untouched so the operator can retry, whereas deleting the account
// first would drop it while leaking the sealed blob on disk. The in-memory
// cascade then runs under one write lock.
func (s *Store) DeleteAccount(id string) error {
	// Serialise with the pool binders: retiring a bound record and sealing a
	// new one must not interleave.
	s.deviceMu.Lock()
	defer s.deviceMu.Unlock()
	s.mu.RLock()
	sealer := s.sealer
	exists := s.accountIndexLocked(id) >= 0
	s.mu.RUnlock()
	if !exists {
		return notFoundf("account %q not found", id)
	}
	if sealer != nil {
		if err := sealer.Delete(id); err != nil {
			// The cause can carry the sealer's file path, so it stays out of
			// the client-facing message.
			return upstreamf("account %q could not be deleted: sealed secret store unavailable", id)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.accountIndexLocked(id)
	if idx < 0 {
		// A concurrent delete won the race after this call pruned the sealer.
		// Sealer.Delete is idempotent, so this is still a clean not-found.
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
	// A bound identity is retired, not erased: re-adding the same
	// (family, username) inside the retention window restores it. The sealed
	// material and the account-key aliases stay.
	if recID, ok := s.deviceBindings[id]; ok {
		if rec := s.deviceRecordLocked(recID); rec != nil {
			now := iso(s.now())
			rec.State = DeviceRetired
			rec.AssignedAccountID = ""
			rec.AssignedAt = nil
			rec.RetiredAt = &now
			rec.RetiredReason = "account_deleted"
			s.deviceAppendEventLocked(rec, "retired", "operator", "account delete")
			s.addAuditLocked(model.SeverityInfo, "operator", "device.retire", rec.ID,
				"reason=account_deleted account="+label)
		}
		delete(s.deviceBindings, id)
	}
	delete(s.devices, id)
	// The selection and attempt counters describe a target that no longer
	// exists; catalog events/audit history stay for the retention window.
	delete(s.selections, id)
	for key := range s.serverStats {
		if strings.HasPrefix(key, id+":") {
			delete(s.serverStats, key)
		}
	}
	// A schedule without its account cannot resolve a target, so it is
	// removed with the account (DESIGN-SCHEDULE §6.4). Runs and fire records
	// stay until retention evicts them.
	keptSchedules := s.schedules[:0]
	for _, schedule := range s.schedules {
		if schedule.Target.Kind == "account" && schedule.Target.ID == id {
			continue
		}
		keptSchedules = append(keptSchedules, schedule)
	}
	s.schedules = keptSchedules
	for key, mark := range s.reminderMarks {
		if mark.AccountID == id {
			delete(s.reminderMarks, key)
		}
	}
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
	// A successful login is the only event that sets firstSeenAt and advances
	// the counters; identity stability never depends on them.
	s.markDeviceSeenLocked(id, account.Username)
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
// node that belongs to it. The sealed secret is deliberately kept: logout ends
// a session, and the account client needs the password to sign in again.
// Only DeleteAccount removes it.
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

func (s *Store) addAuditLocked(severity model.Severity, actor, action, target, detail string, meta ...map[string]string) string {
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
	if len(meta) > 0 && len(meta[0]) > 0 {
		event.Meta = make(map[string]string, len(meta[0]))
		for key, value := range meta[0] {
			event.Meta[key] = value
		}
	}
	s.audit = prependCapped(s.audit, event, AuditRetention)
	if s.auditHook != nil && severity == model.SeverityCritical {
		s.auditHook(event)
	}
	return event.ID
}

// SetAuditHook installs the sink for critical audit events. It is meant to be
// called once during startup, before requests are served. The hook runs with
// the store lock held and must not block or call back into the Store.
//
// The hook fires only for model.SeverityCritical, so the ordinary
// webhook-lifecycle rows (info/warn) never re-enter the webhook path.
func (s *Store) SetAuditHook(hook func(model.AuditEvent)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.auditHook = hook
}

// prependCapped inserts item at the front of items (newest first) and drops the
// oldest entry once limit is reached. The slice is reused, so an insert costs
// one bounded copy instead of reallocating the whole history.
func prependCapped[T any](items []T, item T, limit int) []T {
	if limit <= 0 {
		return items
	}
	items = append(items, item)
	copy(items[1:], items[:len(items)-1])
	items[0] = item
	if len(items) > limit {
		items = items[:limit]
	}
	return items
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
	user.QuotaBytesMonth = copyInt64(user.QuotaBytesMonth)
	return user
}

// copyInt64 returns a pointer to a copy of value, so a snapshot never aliases
// the store's quota field.
func copyInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
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
