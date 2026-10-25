package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"ajsaccountd/internal/model"
)

// SnapshotVersion is the schema this build writes. Restore also accepts
// version 1 (the plaintext device map) and migrates it, so an upgrade is a
// one-way, in-place conversion. A v2 document read by a v1 binary is
// rejected; downgrade requires restoring the pre-upgrade state.json backup.
const SnapshotVersion = 2

// Snapshot is the JSON document written by Save and accepted by Restore.
//
// Secrets maps an account id to its sealed AES-256-GCM envelope; encoding/json
// renders []byte as base64. No plaintext credential enters this structure:
// account passwords exist only in CreateAccount's input and AccountSecret's
// return value. KeyHashes holds the SHA-256 of created API-key secrets, never
// the secrets themselves.
//
// This is the v0.1 stopgap for the SQLite schema in docs/PANEL.md §6: it
// snapshots the whole store in one document instead of normalised tables.
type Snapshot struct {
	Version int `json:"version"`

	Accounts   []model.Account     `json:"accounts"`
	Nodes      []model.ProxyNode   `json:"nodes"`
	Leases     []model.Lease       `json:"leases"`
	Users      []model.PanelUser   `json:"users"`
	Keys       []model.APIKey      `json:"keys"`
	ScopedKeys []model.ScopedKey   `json:"scopedKeys,omitempty"`
	Requests   []model.RequestLog  `json:"requests"`
	Audit      []model.AuditEvent  `json:"audit"`
	Settings   model.PanelSettings `json:"settings"`

	// UsageMonth, MonthlyUsage and AccountUsage are the durable gate-metered
	// monthly totals (user id and account id keyed). They are quota state, not
	// display state: unlike the retained request rows they never shrink when a
	// row is evicted, and an older snapshot without them is rebuilt from its
	// current-month gate rows on restore.
	UsageMonth   string           `json:"usageMonth,omitempty"`
	MonthlyUsage map[string]int64 `json:"monthlyUsage,omitempty"`
	AccountUsage map[string]int64 `json:"accountUsage,omitempty"`

	KeyHashes map[string]string `json:"keyHashes"`
	// ScopedKeyHashes is the scoped-key counterpart of KeyHashes. Both hold
	// only SHA-256 digests.
	ScopedKeyHashes map[string]string `json:"scopedKeyHashes,omitempty"`
	Secrets         map[string][]byte `json:"secrets"`
	// Devices is the version 1 plaintext device map. It is read only for
	// migration and written only while the device pool is disabled (no
	// AJS_MASTER_KEY), which keeps the pre-pool test/mock behaviour. A
	// production v2 document never carries it: raw identity lives in the
	// sealed material file.
	Devices map[string]DeviceIdentity `json:"devices,omitempty"`

	// DeviceBindings maps an account id to a device record id. DevicePool is
	// the non-secret record index; raw material is sealed separately.
	DeviceBindings map[string]string `json:"deviceBindings,omitempty"`
	DevicePool     []DeviceRecord    `json:"devicePool,omitempty"`

	// Selections and ServerStats are the P1 catalog state (DESIGN-CATALOG
	// §3.8). Both are additive and omitempty, so an older v1 document restores
	// with empty maps and an older build ignores them.
	Selections  map[string]model.Selection  `json:"selections,omitempty"`
	ServerStats map[string]model.ServerStat `json:"serverStats,omitempty"`

	// Scheduler state (DESIGN-SCHEDULE §6.1). Every collection is additive and
	// omitempty; the sequence counters let a restored build allocate the next
	// sch-/run- id without colliding.
	Schedules     []model.Schedule              `json:"schedules,omitempty"`
	ScheduleRuns  []model.ScheduleRun           `json:"scheduleRuns,omitempty"`
	ScheduleFires []model.ScheduleFire          `json:"scheduleFires,omitempty"`
	ReminderMarks map[string]model.ReminderMark `json:"reminderMarks,omitempty"`
	ScheduleSeq   int                           `json:"scheduleSeq,omitempty"`
	RunSeq        int                           `json:"runSeq,omitempty"`

	// WebhookEndpoints is the persisted receiver metadata. Signing secrets are
	// deliberately absent: they live in the sealed secret file (or process
	// memory when no master key is configured), never in state.json.
	WebhookEndpoints []model.WebhookEndpoint `json:"webhookEndpoints,omitempty"`
	WebhookSeq       int                     `json:"webhookSeq,omitempty"`

	// WebhookDeliveries is the persisted delivery history, newest first and
	// bounded by WebhookDeliveryRetention/WebhookDeliveriesPerEndpoint. It is
	// additive: an older build ignores it, and a document without it restores
	// with an empty history.
	WebhookDeliveries []model.WebhookDelivery `json:"webhookDeliveries,omitempty"`

	// WebhookOutbox is the pending-delivery set, oldest first and bounded by
	// WebhookOutboxLimit. It is additive and omitempty: an older build ignores
	// it, and a document without it restores with nothing pending. Each row
	// carries the already-scrubbed body, so no signing secret or credential
	// enters this document.
	WebhookOutbox []WebhookOutboxEntry `json:"webhookOutbox,omitempty"`

	// Tenants and PortalSessions are the customer portal state
	// (DESIGN-TENANT §3). Both are additive and omitempty: a pre-portal
	// snapshot loads with zero tenants and no identity backfill. Password and
	// session values are stored only as hashes; a raw cookie or CSRF token
	// never enters this document.
	Tenants        []tenantRecord        `json:"tenants,omitempty"`
	PortalSessions []model.PortalSession `json:"portalSessions,omitempty"`
	TenantSeq      int                   `json:"tenantSeq,omitempty"`

	AccountSeq   int `json:"accountSeq"`
	UserSeq      int `json:"userSeq"`
	KeySeq       int `json:"keySeq"`
	ScopedKeySeq int `json:"scopedKeySeq,omitempty"`
	MutationSeq  int `json:"mutationSeq"`
	AuditSeq     int `json:"auditSeq"`
}

// Snapshot serializes every collection, counter, device identity and sealed
// secret as a versioned JSON document. The read lock is held only while
// copying, so a slow disk write never blocks the API; the copy itself is one
// consistent view.
func (s *Store) Snapshot() ([]byte, error) {
	snap := Snapshot{Version: SnapshotVersion}

	s.mu.RLock()
	snap.Accounts = make([]model.Account, len(s.accounts))
	for i, account := range s.accounts {
		snap.Accounts[i] = cloneAccount(account)
	}
	snap.Nodes = append([]model.ProxyNode(nil), s.nodes...)
	snap.Leases = append([]model.Lease(nil), s.leases...)
	snap.Users = make([]model.PanelUser, len(s.users))
	for i, user := range s.users {
		snap.Users[i] = cloneUser(user)
		// UsedBytes is derived from the gate rows on every read, never stored
		// state: persisting it would freeze one month's total into the next.
		snap.Users[i].UsedBytes = 0
	}
	snap.Keys = make([]model.APIKey, len(s.keys))
	for i, key := range s.keys {
		snap.Keys[i] = cloneKey(key)
	}
	snap.ScopedKeys = make([]model.ScopedKey, len(s.scopedKeys))
	for i, key := range s.scopedKeys {
		snap.ScopedKeys[i] = cloneScopedKey(key)
	}
	snap.Requests = append([]model.RequestLog(nil), s.requests...)
	snap.UsageMonth, snap.MonthlyUsage, snap.AccountUsage = s.usageSnapshotLocked()
	snap.Audit = append([]model.AuditEvent(nil), s.audit...)
	snap.Settings = s.settings
	snap.KeyHashes = make(map[string]string, len(s.keyHashes))
	for id, hash := range s.keyHashes {
		snap.KeyHashes[id] = hash
	}
	snap.ScopedKeyHashes = make(map[string]string, len(s.scopedKeyHashes))
	for id, hash := range s.scopedKeyHashes {
		snap.ScopedKeyHashes[id] = hash
	}
	snap.Secrets = make(map[string][]byte, len(s.secrets))
	for id, sealed := range s.secrets {
		snap.Secrets[id] = append([]byte(nil), sealed...)
	}
	snap.Devices = make(map[string]DeviceIdentity, len(s.devices))
	if s.deviceSealer == nil && len(s.devicePool) == 0 {
		for id, dev := range s.devices {
			snap.Devices[id] = dev
		}
	}
	snap.DeviceBindings = make(map[string]string, len(s.deviceBindings))
	for accountID, deviceID := range s.deviceBindings {
		snap.DeviceBindings[accountID] = deviceID
	}
	snap.DevicePool = s.devicePoolLocked()
	snap.Selections = make(map[string]model.Selection, len(s.selections))
	for id, selection := range s.selections {
		snap.Selections[id] = selection
	}
	snap.ServerStats = make(map[string]model.ServerStat, len(s.serverStats))
	for key, stat := range s.serverStats {
		snap.ServerStats[key] = cloneServerStat(stat)
	}
	snap.Schedules = make([]model.Schedule, len(s.schedules))
	for i, schedule := range s.schedules {
		snap.Schedules[i] = cloneSchedule(schedule)
	}
	snap.ScheduleRuns = append([]model.ScheduleRun(nil), s.scheduleRuns...)
	snap.ScheduleFires = append([]model.ScheduleFire(nil), s.scheduleFires...)
	snap.ReminderMarks = make(map[string]model.ReminderMark, len(s.reminderMarks))
	for key, mark := range s.reminderMarks {
		snap.ReminderMarks[key] = mark
	}
	snap.ScheduleSeq = s.scheduleSeq
	snap.RunSeq = s.runSeq
	snap.WebhookEndpoints = make([]model.WebhookEndpoint, len(s.webhookEndpoints))
	for i, endpoint := range s.webhookEndpoints {
		snap.WebhookEndpoints[i] = cloneWebhookEndpoint(endpoint)
	}
	snap.WebhookSeq = s.webhookSeq
	snap.WebhookDeliveries = append([]model.WebhookDelivery(nil), s.webhookDeliveries...)
	snap.WebhookOutbox = make([]WebhookOutboxEntry, len(s.webhookOutbox))
	for i, entry := range s.webhookOutbox {
		snap.WebhookOutbox[i] = cloneWebhookOutboxEntry(entry)
	}
	sortWebhookOutbox(snap.WebhookOutbox)
	snap.Tenants = make([]tenantRecord, len(s.tenants))
	for i, rec := range s.tenants {
		snap.Tenants[i] = rec
		snap.Tenants[i].Tenant = cloneTenant(rec.Tenant)
	}
	snap.PortalSessions = make([]model.PortalSession, len(s.portalSessions))
	for i, session := range s.portalSessions {
		session.CSRFToken = ""
		session.RevokedAt = copyStringPtr(session.RevokedAt)
		snap.PortalSessions[i] = session
	}
	snap.TenantSeq = s.tenantSeq
	snap.AccountSeq = s.accountSeq
	snap.UserSeq = s.userSeq
	snap.KeySeq = s.keySeq
	snap.ScopedKeySeq = s.scopedKeySeq
	snap.MutationSeq = s.mutationSeq
	snap.AuditSeq = s.auditSeq
	s.mu.RUnlock()

	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode state snapshot: %w", err)
	}
	return append(data, '\n'), nil
}

// Restore replaces every collection, counter, device identity and sealed
// secret from data. The document is decoded and validated before the write
// lock is taken, so an empty, corrupt or unknown-version file leaves the store
// untouched.
//
// The configured sealer and clock are preserved: a snapshot only carries the
// sealed blobs, never key material, and restored timestamps come from the
// document.
func (s *Store) Restore(data []byte) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return errors.New("state snapshot is empty")
	}
	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return fmt.Errorf("decode state snapshot: %w", err)
	}
	if snap.Version != 1 && snap.Version != SnapshotVersion {
		return fmt.Errorf("unsupported state snapshot version %d (want 1 or %d)", snap.Version, SnapshotVersion)
	}
	if s.deviceSealer != nil && len(snap.Devices) > 0 {
		// v1 plaintext identities become sealed pool records before the
		// document is validated or installed. A sealing failure fails the
		// load rather than serving raw ids.
		if err := s.migrateLegacySnapshotDevices(&snap); err != nil {
			return err
		}
	}
	if err := snap.validate(); err != nil {
		return err
	}
	snap.normalize()

	s.mu.Lock()
	s.accounts = snap.Accounts
	s.nodes = snap.Nodes
	s.leases = snap.Leases
	s.users = snap.Users
	s.keys = snap.Keys
	s.scopedKeys = snap.ScopedKeys
	s.requests = snap.Requests
	s.audit = snap.Audit
	s.settings = snap.Settings
	s.keyHashes = snap.KeyHashes
	s.scopedKeyHashes = snap.ScopedKeyHashes
	s.scopedKeyUse = make(map[string]int64)
	if s.scopedKeys == nil {
		s.scopedKeys = []model.ScopedKey{}
	}
	if s.scopedKeyHashes == nil {
		s.scopedKeyHashes = make(map[string]string)
	}
	s.secrets = snap.Secrets
	s.devices = snap.Devices
	if s.devices == nil {
		s.devices = make(map[string]DeviceIdentity)
	}
	s.deviceBindings = snap.DeviceBindings
	s.devicePool = make(map[string]*DeviceRecord, len(snap.DevicePool))
	for i := range snap.DevicePool {
		rec := snap.DevicePool[i]
		s.devicePool[rec.ID] = &rec
	}
	s.deviceMaterial = make(map[string]DeviceMaterial)
	s.deviceWarnings = nil
	s.selections = snap.Selections
	s.serverStats = snap.ServerStats
	s.schedules = snap.Schedules
	s.scheduleRuns = snap.ScheduleRuns
	s.scheduleFires = snap.ScheduleFires
	s.reminderMarks = snap.ReminderMarks
	if s.schedules == nil {
		s.schedules = []model.Schedule{}
	}
	if s.scheduleRuns == nil {
		s.scheduleRuns = []model.ScheduleRun{}
	}
	if s.scheduleFires == nil {
		s.scheduleFires = []model.ScheduleFire{}
	}
	if s.reminderMarks == nil {
		s.reminderMarks = make(map[string]model.ReminderMark)
	}
	s.scheduleSeq = snap.ScheduleSeq
	s.runSeq = snap.RunSeq
	s.webhookEndpoints = snap.WebhookEndpoints
	if s.webhookEndpoints == nil {
		s.webhookEndpoints = []model.WebhookEndpoint{}
	}
	s.webhookSeq = snap.WebhookSeq
	s.webhookDeliveries = snap.WebhookDeliveries
	if s.webhookDeliveries == nil {
		s.webhookDeliveries = []model.WebhookDelivery{}
	}
	s.webhookOutbox = snap.WebhookOutbox
	if s.webhookOutbox == nil {
		s.webhookOutbox = []WebhookOutboxEntry{}
	}
	s.accountSeq = snap.AccountSeq
	s.userSeq = snap.UserSeq
	s.keySeq = snap.KeySeq
	s.scopedKeySeq = snap.ScopedKeySeq
	s.mutationSeq = snap.MutationSeq
	s.auditSeq = snap.AuditSeq
	// Portal state is installed after validation quarantined broken rows, so a
	// malformed tenant or session denies access instead of bricking the panel.
	s.tenants = snap.Tenants
	if s.tenants == nil {
		s.tenants = []tenantRecord{}
	}
	s.portalSessions = snap.PortalSessions
	if s.portalSessions == nil {
		s.portalSessions = []model.PortalSession{}
	}
	s.tenantSeq = snap.TenantSeq
	// Both histories are newest first: a snapshot written by an older build
	// (or edited by hand) is cut to the retention window, never the reverse.
	if len(s.requests) > RequestRetention {
		s.requests = s.requests[:RequestRetention]
	}
	if len(s.audit) > AuditRetention {
		s.audit = s.audit[:AuditRetention]
	}
	// The durable monthly counters are installed with the rows. A snapshot
	// written by this build carries them; an older snapshot (or one written in
	// a previous month) is rebuilt from the restored current-month gate rows.
	month, start := currentUsageMonth(s.now())
	s.usageMu.Lock()
	s.usageMonth = month
	if snap.UsageMonth == month {
		s.monthlyUsage = copyUsage(snap.MonthlyUsage)
		s.accountUsage = copyUsage(snap.AccountUsage)
		// A hand-edited snapshot can carry a counter for a deleted row; drop
		// it instead of failing the whole restore.
		for id := range s.monthlyUsage {
			if s.userIndexLocked(id) < 0 {
				delete(s.monthlyUsage, id)
			}
		}
		for id := range s.accountUsage {
			if s.accountIndexLocked(id) < 0 {
				delete(s.accountUsage, id)
			}
		}
	} else {
		s.monthlyUsage, s.accountUsage = monthlyUsageTotals(s.requests, s.accounts, start)
	}
	s.usageMu.Unlock()
	s.syncAccountCountsLocked()
	s.mu.Unlock()
	// Material presence is checked after the index is installed: a corrupt
	// sealed file degrades the affected records, it does not brick the panel.
	_ = s.ReconcileDeviceMaterials()
	return nil
}

// migrateLegacySnapshotDevices converts a version 1 plaintext device map into
// deterministic pool records. Deterministic ids and machine codes make a
// repeated migration of the same document idempotent.
func (s *Store) migrateLegacySnapshotDevices(snap *Snapshot) error {
	if len(snap.Devices) == 0 {
		return nil
	}
	if len(snap.DevicePool) > 0 {
		return invalidf("state snapshot has both devicePool and a legacy device map")
	}
	records, bindings, _, err := buildLegacyPool(snap.Accounts, snap.Devices, s.sealDeviceMaterial, s.now())
	if err != nil {
		return err
	}
	snap.DevicePool = records
	snap.DeviceBindings = bindings
	snap.Devices = nil
	return nil
}

// Save writes the snapshot to path through a 0600 temporary file in the same
// directory followed by an atomic rename, so readers observe either the old or
// the new document and never a partial one. The directory is created when
// missing.
func (s *Store) Save(path string) error {
	data, err := s.Snapshot()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("state directory %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".state-*.tmp")
	if err != nil {
		return fmt.Errorf("state temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	discard := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}
	if err := tmp.Chmod(0o600); err != nil {
		discard()
		return fmt.Errorf("state temp file mode: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		discard()
		return fmt.Errorf("state temp file write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		discard()
		return fmt.Errorf("state temp file sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("state temp file close: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("state file rename %s: %w", path, err)
	}
	// Best effort: make the rename durable on filesystems that support a
	// directory sync. Windows rejects opening a directory, so the error is
	// intentionally ignored.
	if handle, err := os.Open(dir); err == nil {
		_ = handle.Sync()
		_ = handle.Close()
	}
	return nil
}

// Load reads and restores a snapshot. A missing file reports os.ErrNotExist so
// the caller can keep the seeded store; any other read or decode failure is
// returned without mutating the store.
func (s *Store) Load(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("state file %s: %w", path, os.ErrNotExist)
		}
		return fmt.Errorf("read state file %s: %w", path, err)
	}
	if err := s.Restore(data); err != nil {
		return fmt.Errorf("state file %s: %w", path, err)
	}
	return nil
}

// validate rejects a document whose identifiers cannot form the store's
// lookup tables. It runs before any field is replaced.
func (snap *Snapshot) validate() error {
	accounts := make(map[string]struct{}, len(snap.Accounts))
	for _, account := range snap.Accounts {
		if account.ID == "" {
			return errors.New("state snapshot has an account without an id")
		}
		if _, ok := accounts[account.ID]; ok {
			return fmt.Errorf("state snapshot has duplicate account %q", account.ID)
		}
		if account.TrafficBudgetBytes != nil && *account.TrafficBudgetBytes < 0 {
			return fmt.Errorf("state snapshot account %q has a negative traffic budget", account.ID)
		}
		accounts[account.ID] = struct{}{}
	}
	nodes := make(map[string]struct{}, len(snap.Nodes))
	for _, node := range snap.Nodes {
		if node.ID == "" {
			return errors.New("state snapshot has a node without an id")
		}
		if _, ok := nodes[node.ID]; ok {
			return fmt.Errorf("state snapshot has duplicate node %q", node.ID)
		}
		nodes[node.ID] = struct{}{}
	}
	leases := make(map[string]struct{}, len(snap.Leases))
	for _, lease := range snap.Leases {
		if lease.ID == "" {
			return errors.New("state snapshot has a lease without an id")
		}
		if _, ok := leases[lease.ID]; ok {
			return fmt.Errorf("state snapshot has duplicate lease %q", lease.ID)
		}
		leases[lease.ID] = struct{}{}
	}
	users := make(map[string]struct{}, len(snap.Users))
	for _, user := range snap.Users {
		if user.ID == "" {
			return errors.New("state snapshot has a user without an id")
		}
		if _, ok := users[user.ID]; ok {
			return fmt.Errorf("state snapshot has duplicate user %q", user.ID)
		}
		users[user.ID] = struct{}{}
	}
	keys := make(map[string]struct{}, len(snap.Keys))
	for _, key := range snap.Keys {
		if key.ID == "" {
			return errors.New("state snapshot has an API key without an id")
		}
		if _, ok := keys[key.ID]; ok {
			return fmt.Errorf("state snapshot has duplicate API key %q", key.ID)
		}
		keys[key.ID] = struct{}{}
	}
	scopedKeys := make(map[string]struct{}, len(snap.ScopedKeys))
	for _, key := range snap.ScopedKeys {
		if key.ID == "" {
			return errors.New("state snapshot has a scoped key without an id")
		}
		if _, ok := scopedKeys[key.ID]; ok {
			return fmt.Errorf("state snapshot has duplicate scoped key %q", key.ID)
		}
		scopedKeys[key.ID] = struct{}{}
	}
	for id, dev := range snap.Devices {
		if _, ok := accounts[id]; !ok {
			return fmt.Errorf("state snapshot has a device for unknown account %q", id)
		}
		if !dev.valid() {
			return fmt.Errorf("state snapshot has an incomplete device for account %q", id)
		}
	}
	if err := snap.validateDevicePool(accounts); err != nil {
		return err
	}
	for id, selection := range snap.Selections {
		if _, ok := accounts[id]; !ok {
			return fmt.Errorf("state snapshot has a selection for unknown account %q", id)
		}
		if selection.AccountID != id {
			return fmt.Errorf("state snapshot selection %q names account %q", id, selection.AccountID)
		}
		if strings.TrimSpace(selection.ServerID) == "" {
			return fmt.Errorf("state snapshot selection for account %q has no server id", id)
		}
	}
	if err := snap.validateSchedules(accounts); err != nil {
		return err
	}
	if err := snap.validateWebhooks(); err != nil {
		return err
	}
	// Tenant and session problems are quarantined per row, not rejected: a
	// hand-edited or partially migrated snapshot must not brick the panel, and
	// a quarantined tenant is denied at authentication time.
	if err := snap.validateTenants(users); err != nil {
		return err
	}
	snap.validatePortalSessions()
	if snap.UsageMonth != "" {
		if _, err := time.Parse("2006-01", snap.UsageMonth); err != nil {
			return fmt.Errorf("state snapshot has an invalid usage month %q", snap.UsageMonth)
		}
	}
	for id, bytes := range snap.MonthlyUsage {
		if bytes < 0 {
			return fmt.Errorf("state snapshot has a negative monthly usage for user %q", id)
		}
	}
	for id, bytes := range snap.AccountUsage {
		if bytes < 0 {
			return fmt.Errorf("state snapshot has a negative monthly usage for account %q", id)
		}
	}
	if err := snap.validateWebhookDeliveries(); err != nil {
		return err
	}
	return snap.validateWebhookOutbox()
}

// validateWebhooks rejects a document whose receiver rows cannot be loaded.
// The URL scheme and event vocabulary stay the dispatcher's contract; the
// snapshot only needs unique ids, a URL and a timestamp.
func (snap *Snapshot) validateWebhooks() error {
	if len(snap.WebhookEndpoints) > MaxWebhookEndpoints {
		return fmt.Errorf("state snapshot has %d webhook endpoints (max %d)", len(snap.WebhookEndpoints), MaxWebhookEndpoints)
	}
	ids := make(map[string]struct{}, len(snap.WebhookEndpoints))
	for _, endpoint := range snap.WebhookEndpoints {
		if strings.TrimSpace(endpoint.ID) == "" {
			return errors.New("state snapshot has a webhook endpoint without an id")
		}
		if _, ok := ids[endpoint.ID]; ok {
			return fmt.Errorf("state snapshot has duplicate webhook endpoint %q", endpoint.ID)
		}
		ids[endpoint.ID] = struct{}{}
		if strings.TrimSpace(endpoint.URL) == "" {
			return fmt.Errorf("state snapshot webhook endpoint %q has no url", endpoint.ID)
		}
	}
	return nil
}

// validateSchedules rejects a document whose scheduler rows cannot be loaded
// as-is. It checks identity, the closed action/trigger vocabularies and the
// retention caps; a single invalid schedule is never silently dropped.
func (snap *Snapshot) validateSchedules(accounts map[string]struct{}) error {
	if len(snap.Schedules) > MaxSchedules {
		return fmt.Errorf("state snapshot has %d schedules (max %d)", len(snap.Schedules), MaxSchedules)
	}
	ids := make(map[string]struct{}, len(snap.Schedules))
	names := make(map[string]struct{}, len(snap.Schedules))
	for _, schedule := range snap.Schedules {
		if strings.TrimSpace(schedule.ID) == "" {
			return errors.New("state snapshot has a schedule without an id")
		}
		if _, ok := ids[schedule.ID]; ok {
			return fmt.Errorf("state snapshot has duplicate schedule %q", schedule.ID)
		}
		ids[schedule.ID] = struct{}{}
		name := strings.ToLower(strings.TrimSpace(schedule.Name))
		if name == "" {
			return fmt.Errorf("state snapshot schedule %q has no name", schedule.ID)
		}
		if _, ok := names[name]; ok {
			return fmt.Errorf("state snapshot has duplicate schedule name %q", schedule.Name)
		}
		names[name] = struct{}{}
		if !validScheduleAction(schedule.Action) {
			return fmt.Errorf("state snapshot schedule %q has unknown action %q", schedule.ID, schedule.Action)
		}
		if !validTriggerKind(schedule.Trigger.Kind) {
			return fmt.Errorf("state snapshot schedule %q has unknown trigger %q", schedule.ID, schedule.Trigger.Kind)
		}
		if schedule.Revision < 0 {
			return fmt.Errorf("state snapshot schedule %q has a negative revision", schedule.ID)
		}
		if schedule.Target.Kind == "account" {
			if _, ok := accounts[schedule.Target.ID]; !ok {
				return fmt.Errorf("state snapshot schedule %q targets unknown account %q", schedule.ID, schedule.Target.ID)
			}
		}
	}
	if len(snap.ScheduleRuns) > ScheduleRunRetention {
		return fmt.Errorf("state snapshot has %d schedule runs (max %d)", len(snap.ScheduleRuns), ScheduleRunRetention)
	}
	runIDs := make(map[string]struct{}, len(snap.ScheduleRuns))
	perSchedule := make(map[string]int, len(snap.Schedules))
	for _, run := range snap.ScheduleRuns {
		if strings.TrimSpace(run.ID) == "" {
			return errors.New("state snapshot has a schedule run without an id")
		}
		if _, ok := runIDs[run.ID]; ok {
			return fmt.Errorf("state snapshot has duplicate schedule run %q", run.ID)
		}
		runIDs[run.ID] = struct{}{}
		if strings.TrimSpace(run.ScheduleID) == "" {
			return fmt.Errorf("state snapshot schedule run %q has no schedule id", run.ID)
		}
		perSchedule[run.ScheduleID]++
		if perSchedule[run.ScheduleID] > ScheduleRunsPerSchedule {
			return fmt.Errorf("state snapshot schedule %q has more than %d runs", run.ScheduleID, ScheduleRunsPerSchedule)
		}
	}
	if len(snap.ScheduleFires) > ScheduleFireRetention {
		return fmt.Errorf("state snapshot has %d schedule fires (max %d)", len(snap.ScheduleFires), ScheduleFireRetention)
	}
	fireKeys := make(map[string]struct{}, len(snap.ScheduleFires))
	for _, fire := range snap.ScheduleFires {
		if strings.TrimSpace(fire.Key) == "" {
			return errors.New("state snapshot has a schedule fire without a key")
		}
		if _, ok := fireKeys[fire.Key]; ok {
			return fmt.Errorf("state snapshot has duplicate schedule fire %q", fire.Key)
		}
		fireKeys[fire.Key] = struct{}{}
	}
	if len(snap.ReminderMarks) > ReminderMarkRetention {
		return fmt.Errorf("state snapshot has %d reminder marks (max %d)", len(snap.ReminderMarks), ReminderMarkRetention)
	}
	for key, mark := range snap.ReminderMarks {
		if _, ok := accounts[mark.AccountID]; !ok {
			return fmt.Errorf("state snapshot reminder mark %q names unknown account %q", key, mark.AccountID)
		}
		if key != ReminderMarkKey(mark.AccountID, mark.Kind, mark.Threshold, mark.ExpiresAt) {
			return fmt.Errorf("state snapshot reminder mark %q does not match its fields", key)
		}
	}
	return nil
}

// validateDevicePool rejects an index whose bindings or records cannot form a
// consistent pool. Material presence is deliberately not checked here: a
// corrupt sealed file degrades the affected record instead of rejecting the
// whole document (DESIGN-DEVICE-POOL §5.5).
func (snap *Snapshot) validateDevicePool(accounts map[string]struct{}) error {
	if len(snap.Devices) > 0 && len(snap.DevicePool) > 0 {
		return errors.New("state snapshot has both a device pool and a legacy device map")
	}
	if len(snap.DevicePool) == 0 {
		if len(snap.DeviceBindings) > 0 {
			return errors.New("state snapshot has device bindings without a device pool")
		}
		return nil
	}
	records := make(map[string]*DeviceRecord, len(snap.DevicePool))
	fingerprints := make(map[string]string, len(snap.DevicePool))
	machineHashes := make(map[string]string, len(snap.DevicePool))
	for i := range snap.DevicePool {
		rec := &snap.DevicePool[i]
		if !deviceIDPattern.MatchString(rec.ID) {
			return fmt.Errorf("state snapshot has a malformed device id %q", rec.ID)
		}
		if _, ok := records[rec.ID]; ok {
			return fmt.Errorf("state snapshot has duplicate device %q", rec.ID)
		}
		records[rec.ID] = rec
		if !validDeviceState(rec.State) {
			return fmt.Errorf("state snapshot device %q has unknown state %q", rec.ID, rec.State)
		}
		if rec.Platform != model.PlatformLinux && rec.Platform != model.PlatformAndroid {
			return fmt.Errorf("state snapshot device %q has unsupported platform %q", rec.ID, rec.Platform)
		}
		profile, known := DeviceProfileByName(rec.Profile)
		if !known && rec.Profile != "legacy/unknown" {
			return fmt.Errorf("state snapshot device %q has unknown profile %q", rec.ID, rec.Profile)
		}
		if known && profile.Family != rec.Platform {
			return fmt.Errorf("state snapshot device %q profile family does not match its platform", rec.ID)
		}
		if !validDeviceAlgorithm(rec.Algorithm) {
			return fmt.Errorf("state snapshot device %q has unknown algorithm %q", rec.ID, rec.Algorithm)
		}
		if rec.Fingerprint != "" {
			if !isSHA256Hex(rec.Fingerprint) {
				return fmt.Errorf("state snapshot device %q has a malformed fingerprint", rec.ID)
			}
			if other, ok := fingerprints[rec.Fingerprint]; ok {
				return fmt.Errorf("state snapshot devices %q and %q share a fingerprint", other, rec.ID)
			}
			fingerprints[rec.Fingerprint] = rec.ID
		}
		if machine := rec.Hashes["machine"]; machine != "" {
			if other, ok := machineHashes[machine]; ok {
				return fmt.Errorf("state snapshot devices %q and %q share a machine hash", other, rec.ID)
			}
			machineHashes[machine] = rec.ID
		}
		if rec.LoginCount < 0 {
			return fmt.Errorf("state snapshot device %q has a negative login count", rec.ID)
		}
		if len(rec.History) > DeviceHistoryCap {
			return fmt.Errorf("state snapshot device %q has more than %d history events", rec.ID, DeviceHistoryCap)
		}
		for _, stamp := range []*string{rec.AssignedAt, rec.PresentedAt, rec.FirstSeenAt, rec.LastSeenAt, rec.RetiredAt} {
			if stamp != nil && !validRFC3339(*stamp) {
				return fmt.Errorf("state snapshot device %q has a non-RFC3339 timestamp", rec.ID)
			}
		}
		if !validRFC3339(rec.CreatedAt) {
			return fmt.Errorf("state snapshot device %q has no RFC3339 createdAt", rec.ID)
		}
		if rec.AccountKey != "" && !isSHA256Hex(rec.AccountKey) {
			return fmt.Errorf("state snapshot device %q has a malformed account key", rec.ID)
		}
		if len(rec.AccountKeyAliases) > DeviceAliasCap {
			return fmt.Errorf("state snapshot device %q has too many account key aliases", rec.ID)
		}
		for _, alias := range rec.AccountKeyAliases {
			if !isSHA256Hex(alias) {
				return fmt.Errorf("state snapshot device %q has a malformed account key alias", rec.ID)
			}
		}
		switch rec.State {
		case DeviceAssigned:
			if rec.AssignedAccountID == "" {
				return fmt.Errorf("state snapshot device %q is assigned without an account", rec.ID)
			}
			if _, ok := accounts[rec.AssignedAccountID]; !ok {
				return fmt.Errorf("state snapshot device %q is assigned to unknown account %q", rec.ID, rec.AssignedAccountID)
			}
		default:
			if rec.AssignedAccountID != "" {
				return fmt.Errorf("state snapshot device %q is %s but still names an account", rec.ID, rec.State)
			}
		}
		if rec.State == DeviceRetired && rec.RetiredAt == nil {
			return fmt.Errorf("state snapshot device %q is retired without retiredAt", rec.ID)
		}
	}
	bound := make(map[string]string, len(snap.DeviceBindings))
	for accountID, deviceID := range snap.DeviceBindings {
		if _, ok := accounts[accountID]; !ok {
			return fmt.Errorf("state snapshot binds unknown account %q", accountID)
		}
		rec, ok := records[deviceID]
		if !ok {
			return fmt.Errorf("state snapshot binds account %q to unknown device %q", accountID, deviceID)
		}
		if other, ok := bound[deviceID]; ok {
			return fmt.Errorf("state snapshot binds device %q to accounts %q and %q", deviceID, other, accountID)
		}
		bound[deviceID] = accountID
		if rec.State != DeviceAssigned {
			return fmt.Errorf("state snapshot binds account %q to %s device %q", accountID, rec.State, deviceID)
		}
		if rec.AssignedAccountID != accountID {
			return fmt.Errorf("state snapshot device %q names account %q but is bound to %q", deviceID, rec.AssignedAccountID, accountID)
		}
	}
	for _, rec := range records {
		if rec.State == DeviceAssigned {
			if _, ok := bound[rec.ID]; !ok {
				return fmt.Errorf("state snapshot device %q is assigned without a binding", rec.ID)
			}
		}
	}
	return nil
}

// normalize fills absent maps and raises every counter to at least the highest
// identifier already present. A snapshot written by an older build, or one
// whose counters were lost, therefore cannot hand out a colliding id.
func (snap *Snapshot) normalize() {
	if snap.KeyHashes == nil {
		snap.KeyHashes = make(map[string]string)
	}
	if snap.ScopedKeyHashes == nil {
		snap.ScopedKeyHashes = make(map[string]string)
	}
	if snap.ScopedKeys == nil {
		snap.ScopedKeys = []model.ScopedKey{}
	}
	if snap.Secrets == nil {
		snap.Secrets = make(map[string][]byte)
	}
	if snap.Devices == nil {
		snap.Devices = make(map[string]DeviceIdentity)
	}
	if snap.DeviceBindings == nil {
		snap.DeviceBindings = make(map[string]string)
	}
	if snap.DevicePool == nil {
		snap.DevicePool = []DeviceRecord{}
	}
	if snap.Selections == nil {
		snap.Selections = make(map[string]model.Selection)
	}
	if snap.ServerStats == nil {
		snap.ServerStats = make(map[string]model.ServerStat)
	}
	if snap.Schedules == nil {
		snap.Schedules = []model.Schedule{}
	}
	if snap.ScheduleRuns == nil {
		snap.ScheduleRuns = []model.ScheduleRun{}
	}
	if snap.ScheduleFires == nil {
		snap.ScheduleFires = []model.ScheduleFire{}
	}
	if snap.ReminderMarks == nil {
		snap.ReminderMarks = make(map[string]model.ReminderMark)
	}
	if snap.WebhookEndpoints == nil {
		snap.WebhookEndpoints = []model.WebhookEndpoint{}
	}
	if snap.Tenants == nil {
		snap.Tenants = []tenantRecord{}
	}
	if snap.PortalSessions == nil {
		snap.PortalSessions = []model.PortalSession{}
	}
	snap.WebhookDeliveries = normalizeWebhookDeliveries(snap.WebhookDeliveries)
	snap.WebhookOutbox = normalizeWebhookOutbox(snap.WebhookOutbox)
	for _, tenant := range snap.Tenants {
		snap.TenantSeq = max(snap.TenantSeq, sequenceOf(tenant.ID, "t-"))
	}
	for _, endpoint := range snap.WebhookEndpoints {
		snap.WebhookSeq = max(snap.WebhookSeq, sequenceOf(endpoint.ID, "wh-"))
	}
	for _, schedule := range snap.Schedules {
		snap.ScheduleSeq = max(snap.ScheduleSeq, sequenceOf(schedule.ID, "sch-"))
	}
	for _, run := range snap.ScheduleRuns {
		snap.RunSeq = max(snap.RunSeq, sequenceOf(run.ID, "run-"))
	}
	for _, account := range snap.Accounts {
		snap.AccountSeq = max(snap.AccountSeq, sequenceOf(account.ID, "acc-"))
	}
	for _, user := range snap.Users {
		snap.UserSeq = max(snap.UserSeq, sequenceOf(user.ID, "u-"))
	}
	for _, key := range snap.Keys {
		snap.KeySeq = max(snap.KeySeq, sequenceOf(key.ID, "key-"))
	}
	for _, key := range snap.ScopedKeys {
		snap.ScopedKeySeq = max(snap.ScopedKeySeq, sequenceOf(key.ID, "sk-"))
	}
	for _, request := range snap.Requests {
		snap.MutationSeq = max(snap.MutationSeq, sequenceOf(request.ID, "req-mut-"))
	}
	for _, event := range snap.Audit {
		snap.AuditSeq = max(snap.AuditSeq, sequenceOf(event.ID, "audit-"))
	}
}

// sequenceOf returns the numeric suffix of id when it has prefix, and 0
// otherwise. Seeded identifiers follow the "<prefix><number>" convention.
func sequenceOf(id, prefix string) int {
	if !strings.HasPrefix(id, prefix) {
		return 0
	}
	value, err := strconv.Atoi(strings.TrimPrefix(id, prefix))
	if err != nil || value < 0 {
		return 0
	}
	return value
}
