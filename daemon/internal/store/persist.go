package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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

	// DeviceBindings maps an account id to its slot-1 device record id. It is
	// the compatibility projection of AccountSlots[account#1]; DevicePool is
	// the non-secret record index and raw material is sealed separately.
	DeviceBindings map[string]string `json:"deviceBindings,omitempty"`
	DevicePool     []DeviceRecord    `json:"devicePool,omitempty"`

	// AccountSlots is the per-account slot contract (MULTISLOT-PLAN steps 1-2).
	// It is additive and omitempty: a pre-slot document restores with slot 1
	// derived from DeviceBindings, and an older build ignores the field. Each
	// slot's DeviceID is a reference to the record named by AssignedSlotKey;
	// it is never a regenerated identity.
	AccountSlots []model.AccountSlot `json:"accountSlots,omitempty"`

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

	// Operations is the durable async-transition history (DESIGN-INTEGRATION
	// §3.4), newest first and bounded by store.OperationRetention/MaxAge. A
	// row still queued or running when this document was written is restored
	// as failed/OPERATION_UNKNOWN, never as success. OperationSeq allocates
	// the next op-NNNN id without colliding after a restore.
	Operations   []model.Operation `json:"operations,omitempty"`
	OperationSeq int               `json:"operationSeq,omitempty"`

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

	// Plans, Orders and PaymentEvents are the order state (DESIGN-ORDERS).
	// Every field is additive and omitempty, so a pre-order snapshot restores
	// with an empty catalogue and no orders. Order rows carry only the
	// idempotency/request hashes, never the raw key; payment events carry no
	// card data, signature or raw provider payload.
	Plans         []model.Plan         `json:"plans,omitempty"`
	Orders        []orderRecord        `json:"orders,omitempty"`
	PaymentEvents []model.PaymentEvent `json:"paymentEvents,omitempty"`
	PlanSeq       int                  `json:"planSeq,omitempty"`
	OrderSeq      int                  `json:"orderSeq,omitempty"`

	// quarantinedOrders is process-only: restore records why an order was
	// failed closed so an operator can find the row, but the reason never
	// enters the document.
	quarantinedOrders []string

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
	for key, deviceID := range s.deviceSlotBindings {
		if key.Index == 1 {
			snap.DeviceBindings[key.AccountID] = deviceID
		}
	}
	for accountID, deviceID := range s.deviceBindings {
		if _, ok := snap.DeviceBindings[accountID]; !ok {
			snap.DeviceBindings[accountID] = deviceID
		}
	}
	snap.DevicePool = s.devicePoolLocked()
	snap.AccountSlots = make([]model.AccountSlot, 0, len(s.accountSlots))
	for _, slot := range s.accountSlots {
		// Slot 1 is the compatibility projection of the account-level device
		// binding. The binding may predate the slot row (existing installs),
		// so fill it on save without changing the record id.
		slot = s.slotWithBindingLocked(slot)
		snap.AccountSlots = append(snap.AccountSlots, slot)
		if slot.Index == 1 && slot.DeviceID != "" &&
			(len(s.devicePool) > 0 || s.deviceBindings[slot.AccountID] != "") {
			if snap.DeviceBindings == nil {
				snap.DeviceBindings = make(map[string]string)
			}
			snap.DeviceBindings[slot.AccountID] = slot.DeviceID
		}
	}
	sortAccountSlots(snap.AccountSlots)
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
	snap.Operations = make([]model.Operation, len(s.operations))
	for i, op := range s.operations {
		snap.Operations[i] = cloneOperation(op)
	}
	snap.OperationSeq = s.operationSeq
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
	snap.Plans = make([]model.Plan, len(s.plans))
	for i, plan := range s.plans {
		snap.Plans[i] = clonePlan(plan)
	}
	snap.Orders = make([]orderRecord, len(s.orders))
	for i, record := range s.orders {
		row := record
		row.Order = cloneOrder(record.Order)
		snap.Orders[i] = row
	}
	snap.PaymentEvents = append([]model.PaymentEvent(nil), s.paymentEvents...)
	snap.PlanSeq = s.planSeq
	snap.OrderSeq = s.orderSeq
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
	// Pre-slot snapshots have no accountSlots: derive slot 1 from the
	// account-level device binding before validation. This copies the record
	// id and never generates or reseals an identity.
	snap.migrateAccountSlots()
	// Fill AssignedSlotKey for assigned records from the slot table (or the
	// slot-1 compatibility projection). This only copies the slot reference;
	// it never reseals material or changes a record id.
	snap.migrateDeviceSlotBindings()
	// Controller-owned node ids gain the slot segment ("acc:server" ->
	// "acc#1:server"); the leases that pointed at them follow. Seeded and
	// hand-written rows keep their shape, and the rewrite is idempotent.
	if migrated := snap.migrateNodeIDs(); migrated > 0 {
		slog.Info("node_ids_migrated", "count", migrated)
	}
	if err := snap.validate(); err != nil {
		return err
	}
	// Structural validation only checks shape. A snapshot can still carry a
	// LIVE slot with no bound listener, a duplicate slot entitlement or a lease
	// stamped with an older generation; repair those before the document is
	// installed, with one audit row per repair.
	if repaired := snap.reconcileSlotClaims(s.now()); repaired > 0 {
		slog.Warn("slot_claims_reconciled", "count", repaired)
	}
	snap.normalize()
	// A queued/running row cannot be resumed after a restart: the vendor call
	// may or may not have happened. Fail it with OPERATION_UNKNOWN before the
	// document is installed and audit each interrupted id after the audit
	// collection is in place.
	interruptedOperations := snap.normalizeOperations(s.now())

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
	s.deviceBindings = make(map[string]string, len(snap.DeviceBindings))
	for accountID, deviceID := range snap.DeviceBindings {
		s.deviceBindings[accountID] = deviceID
	}
	s.deviceSlotBindings = make(map[model.SlotKey]string, len(snap.AccountSlots))
	s.accountSlots = make(map[model.SlotKey]model.AccountSlot, len(snap.AccountSlots))
	poolBacked := len(snap.DevicePool) > 0
	for _, slot := range snap.AccountSlots {
		s.accountSlots[slot.SlotKey] = slot
		if slot.DeviceID == "" || !poolBacked {
			continue
		}
		s.deviceSlotBindings[slot.SlotKey] = slot.DeviceID
		if slot.Index == 1 {
			s.deviceBindings[slot.AccountID] = slot.DeviceID
		}
	}
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
	// Operations are installed after the audit sequence so the interrupted
	// records can be recorded in the same restore. A terminal record keeps its
	// timestamps and result; a restored in-flight record was already failed by
	// normalizeOperations and is never resumed.
	s.operations = snap.Operations
	if s.operations == nil {
		s.operations = []model.Operation{}
	}
	s.operationSeq = snap.OperationSeq
	s.operationWait = make(map[string]chan struct{})
	for _, id := range interruptedOperations {
		s.addAuditLocked(model.SeverityWarn, "system", "operation.unknown", id,
			"Operation interrupted by restart", map[string]string{"code": OperationUnknownCode})
	}
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
	// Order state is installed after validation quarantined broken rows, so a
	// malformed order cannot grant an entitlement. An older snapshot restores
	// with an empty catalogue and no orders.
	s.plans = snap.Plans
	if s.plans == nil {
		s.plans = []model.Plan{}
	}
	s.orders = snap.Orders
	if s.orders == nil {
		s.orders = []orderRecord{}
	}
	s.paymentEvents = snap.PaymentEvents
	if s.paymentEvents == nil {
		s.paymentEvents = []model.PaymentEvent{}
	}
	s.planSeq = snap.PlanSeq
	s.orderSeq = snap.OrderSeq
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
	nodeSlots := make(map[string]int, len(snap.Nodes))
	nodeAccounts := make(map[string]string, len(snap.Nodes))
	for _, node := range snap.Nodes {
		if node.ID == "" {
			return errors.New("state snapshot has a node without an id")
		}
		if _, ok := nodes[node.ID]; ok {
			return fmt.Errorf("state snapshot has duplicate node %q", node.ID)
		}
		nodes[node.ID] = struct{}{}
		nodeSlots[node.ID] = node.Slot
		nodeAccounts[node.ID] = node.AccountID
		if node.Slot > 0 {
			if node.ServerID == "" {
				return fmt.Errorf("state snapshot node %q has slot %d without a server id", node.ID, node.Slot)
			}
			key := model.SlotKey{AccountID: node.AccountID, Index: node.Slot}
			if want := key.NodeID(node.ServerID); node.ID != want {
				return fmt.Errorf("state snapshot node %q does not match its slot id %q", node.ID, want)
			}
		}
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
		if lease.Slot < 0 || lease.Slot > model.MaxSlotIndex {
			return fmt.Errorf("state snapshot lease %q has slot %d outside 0..%d",
				lease.ID, lease.Slot, model.MaxSlotIndex)
		}
		if lease.Slot > 0 {
			slot, ok := nodeSlots[lease.NodeID]
			if !ok || slot != lease.Slot {
				return fmt.Errorf("state snapshot lease %q names node %q with slot %d, want slot %d",
					lease.ID, lease.NodeID, slot, lease.Slot)
			}
			// The slot is identified by (account, index), so the lease must
			// agree with the node's account: a mismatched account would pin
			// the wrong account's slot in admission and audit.
			if account := nodeAccounts[lease.NodeID]; lease.AccountID != account {
				return fmt.Errorf("state snapshot lease %q names node %q of account %q, lease account is %q",
					lease.ID, lease.NodeID, account, lease.AccountID)
			}
		}
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
	if err := snap.validateAccountSlots(accounts); err != nil {
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
	if err := snap.validateOperations(); err != nil {
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
	if err := snap.validatePlans(); err != nil {
		return err
	}
	if err := snap.validateOrders(); err != nil {
		return err
	}
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
		if rec.AssignedSlotKey != nil {
			if !rec.AssignedSlotKey.Valid() {
				return fmt.Errorf("state snapshot device %q has invalid assigned slot key %q",
					rec.ID, rec.AssignedSlotKey.String())
			}
			switch rec.State {
			case DeviceAssigned:
				if rec.AssignedSlotKey.AccountID != rec.AssignedAccountID {
					return fmt.Errorf("state snapshot device %q slot key names account %q, record names %q",
						rec.ID, rec.AssignedSlotKey.AccountID, rec.AssignedAccountID)
				}
			case DeviceRetired:
				// Retired records keep the slot they were retired from.
			default:
				return fmt.Errorf("state snapshot device %q is %s but still carries a slot key", rec.ID, rec.State)
			}
		} else if rec.State == DeviceAssigned {
			return fmt.Errorf("state snapshot device %q is assigned without an assigned slot", rec.ID)
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
		if rec.State == DeviceAssigned && rec.AssignedSlotKey == nil {
			if _, ok := bound[rec.ID]; !ok {
				return fmt.Errorf("state snapshot device %q is assigned without a binding", rec.ID)
			}
		}
	}
	return nil
}

// migrateAccountSlots derives the slot table from a pre-slot snapshot. Every
// account gets its mandatory slot 1; when the account has an account-level
// device binding, the slot references that exact record id. The function only
// copies strings: it never calls a generator, BindForLogin or the sealer, so a
// restored identity cannot change.
func (snap *Snapshot) migrateAccountSlots() {
	if snap.AccountSlots == nil {
		snap.AccountSlots = []model.AccountSlot{}
	}
	byKey := make(map[model.SlotKey]int, len(snap.AccountSlots))
	rowsPerAccount := make(map[string]int, len(snap.Accounts))
	for i := range snap.AccountSlots {
		byKey[snap.AccountSlots[i].SlotKey] = i
		rowsPerAccount[snap.AccountSlots[i].AccountID]++
	}
	for _, account := range snap.Accounts {
		key := model.SlotKey{AccountID: account.ID, Index: 1}
		binding := snap.DeviceBindings[account.ID]
		if binding == "" {
			// A version-1 snapshot without a device pool keeps its identities
			// in the plaintext Devices map. There is no DeviceRecord id in that
			// mode, so the stable UID is the only durable reference available.
			if device, ok := snap.Devices[account.ID]; ok {
				binding = device.UID
			}
		}
		if index, ok := byKey[key]; ok {
			if snap.AccountSlots[index].DeviceID == "" && binding != "" {
				snap.AccountSlots[index].DeviceID = binding
			}
			continue
		}
		if rowsPerAccount[account.ID] != 0 {
			// A hand-edited document with higher indices but no slot 1 is
			// left for validation to reject; migration only fills the gap for
			// accounts that predate the slot table entirely.
			continue
		}
		snap.AccountSlots = append(snap.AccountSlots, model.AccountSlot{
			SlotKey:  key,
			DeviceID: binding,
			State:    model.SlotProvisioning,
			Health:   model.SlotHealthUnknown,
		})
		byKey[key] = len(snap.AccountSlots) - 1
		rowsPerAccount[account.ID]++
	}
}

// slotByKey resolves the slot row a node belongs to. A legacy node (Slot 0) has
// no slot row.
func slotByKey(slots []model.AccountSlot, node model.ProxyNode) (model.AccountSlot, bool) {
	if node.Slot <= 0 {
		return model.AccountSlot{}, false
	}
	for _, slot := range slots {
		if slot.AccountID == node.AccountID && slot.Index == node.Slot {
			return slot, true
		}
	}
	return model.AccountSlot{}, false
}

// migrateNodeIDs rewrites controller-owned node ids from "account:server" to
// "account#1:server" and rewrites every lease that pointed at them. It also
// backfills Lease.Slot/Generation from the node and slot table. Seeded rows
// (id "node-01", empty ServerID) and any row that is not exactly
// account:server are untouched. Idempotent: a rewritten row no longer matches
// the pre-slot shape, and the lease rewrite is a lookup miss on the next boot.
func (snap *Snapshot) migrateNodeIDs() int {
	mapping := make(map[string]string)
	for i := range snap.Nodes {
		node := &snap.Nodes[i]
		if node.Slot != 0 || node.ServerID == "" {
			continue
		}
		if node.ID != node.AccountID+":"+node.ServerID {
			continue
		}
		key := model.SlotKey{AccountID: node.AccountID, Index: 1}
		newID := key.NodeID(node.ServerID)
		mapping[node.ID] = newID
		node.ID = newID
		node.Slot = 1
	}
	for i := range snap.Leases {
		lease := &snap.Leases[i]
		if newID, ok := mapping[lease.NodeID]; ok {
			lease.NodeID = newID
		}
		if lease.Slot == 0 {
			for _, node := range snap.Nodes {
				if node.ID == lease.NodeID && node.Slot > 0 {
					lease.Slot = node.Slot
					if slot, ok := slotByKey(snap.AccountSlots, node); ok {
						lease.Generation = slot.Generation
					}
					break
				}
			}
		}
	}
	return len(mapping)
}

// migrateDeviceSlotBindings backfills AssignedSlotKey on assigned records from
// the slot table. A pre-slot snapshot has only DeviceBindings; migrateAccountSlots
// has already copied that binding to slot 1, so the same lookup covers both
// shapes. It never calls a generator, BindForLogin or the sealer.
func (snap *Snapshot) migrateDeviceSlotBindings() {
	if len(snap.DevicePool) == 0 {
		return
	}
	// Slot 1 remains the compatibility projection. A document that carries
	// only the slot row (for example a hand-written migration fixture) gets
	// the projection filled from the same record id, never from a generator.
	if snap.DeviceBindings == nil {
		snap.DeviceBindings = make(map[string]string)
	}
	for _, slot := range snap.AccountSlots {
		if slot.Index == 1 && slot.DeviceID != "" && snap.DeviceBindings[slot.AccountID] == "" {
			snap.DeviceBindings[slot.AccountID] = slot.DeviceID
		}
	}
	byDevice := make(map[string]model.SlotKey, len(snap.AccountSlots))
	for _, slot := range snap.AccountSlots {
		if slot.DeviceID != "" {
			byDevice[slot.DeviceID] = slot.SlotKey
		}
	}
	for i := range snap.DevicePool {
		rec := &snap.DevicePool[i]
		if rec.State != DeviceAssigned || rec.AssignedSlotKey != nil {
			continue
		}
		if slot, ok := byDevice[rec.ID]; ok && slot.AccountID == rec.AssignedAccountID {
			key := slot
			rec.AssignedSlotKey = &key
			continue
		}
		if rec.AssignedAccountID != "" && snap.DeviceBindings[rec.AssignedAccountID] == rec.ID {
			key := model.SlotKey{AccountID: rec.AssignedAccountID, Index: 1}
			rec.AssignedSlotKey = &key
		}
	}
}

// reconcileSlotClaims repairs the slot/lease invariants a snapshot may violate
// without failing the whole load. It runs after structural validation and
// before the document is installed, so a restored store never serves a claim
// the mutation paths would have refused:
//
//   - a LIVE slot row needs a LIVE node of the same slot that reports the
//     slot's gate port; a READY/STALE node has no bound listener, so the row
//     is demoted to DOWN (this is the persisted shape a crash or a hand-edited
//     document can leave behind);
//   - an active slot lease must point at a LIVE node bound to the LIVE slot
//     row; otherwise the entitlement is dropped instead of advertised;
//   - at most one active lease may hold an (account, index) slot; later
//     duplicates are dropped;
//   - a surviving slot lease carries its slot's serving generation and the
//     node's mirrored endpoint fields, so a pre-fix row cannot keep an older
//     generation.
//
// Every repair writes one system audit row. A document that already satisfies
// the invariants is returned untouched, so a round trip stays byte-identical.
func (snap *Snapshot) reconcileSlotClaims(now time.Time) int {
	if len(snap.AccountSlots) == 0 && len(snap.Leases) == 0 {
		return 0
	}
	nodes := make(map[string]model.ProxyNode, len(snap.Nodes))
	for _, node := range snap.Nodes {
		nodes[node.ID] = node
	}
	slots := make(map[model.SlotKey]*model.AccountSlot, len(snap.AccountSlots))
	for i := range snap.AccountSlots {
		slots[snap.AccountSlots[i].SlotKey] = &snap.AccountSlots[i]
	}
	seq := snap.AuditSeq
	for _, event := range snap.Audit {
		seq = max(seq, sequenceOf(event.ID, "audit-"))
	}
	repaired := 0
	audit := func(action, target, detail string, meta map[string]string) {
		seq++
		snap.Audit = prependCapped(snap.Audit, model.AuditEvent{
			ID:       fmt.Sprintf("audit-%03d", seq),
			TS:       iso(now),
			Severity: model.SeverityWarn,
			Actor:    "system",
			Action:   action,
			Target:   target,
			Detail:   detail,
			Meta:     meta,
		}, AuditRetention)
	}
	// bound is the set of slots a LIVE node actually serves. A READY node may
	// still remember a port from the session that died, so state and port are
	// both required.
	bound := make(map[model.SlotKey]struct{}, len(snap.Nodes))
	for _, node := range snap.Nodes {
		if node.Slot <= 0 || node.State != model.NodeLive || node.GatePort <= 0 {
			continue
		}
		slot, ok := slots[model.SlotKey{AccountID: node.AccountID, Index: node.Slot}]
		if !ok || slot.State != model.SlotLive || slot.GatePort != node.GatePort {
			continue
		}
		bound[slot.SlotKey] = struct{}{}
	}
	for i := range snap.AccountSlots {
		slot := &snap.AccountSlots[i]
		if slot.State != model.SlotLive {
			continue
		}
		if _, ok := bound[slot.SlotKey]; ok {
			continue
		}
		slot.State = model.SlotDown
		slot.Health = model.SlotHealthDegraded
		slot.LastAttemptAt = iso(now)
		slot.LastError = "gate_not_bound_after_restore"
		audit("slot.recovered_unbound", slot.SlotKey.String(),
			"Restore demoted an unbound LIVE slot to DOWN",
			map[string]string{"slot": slot.SlotKey.String(), "state": string(slot.State)})
		repaired++
	}
	accounts := make(map[string]model.Account, len(snap.Accounts))
	for _, account := range snap.Accounts {
		accounts[account.ID] = account
	}
	// capacityFor is the ceiling the admission path would apply after this
	// document is installed. It is cached because every lease of an account
	// asks the same question.
	capacities := make(map[string]int, len(accounts))
	capacityFor := func(accountID string) int {
		if capacity, ok := capacities[accountID]; ok {
			return capacity
		}
		account, ok := accounts[accountID]
		if !ok {
			capacities[accountID] = 0
			return 0
		}
		capacity := snap.restoreCapacity(account, slots)
		capacities[accountID] = capacity
		return capacity
	}
	heldSlots := make(map[model.SlotKey]struct{}, len(snap.Leases))
	heldLegacyNodes := make(map[string]struct{}, len(snap.Leases))
	active := make(map[string]int, len(accounts))
	kept := snap.Leases[:0]
	for _, lease := range snap.Leases {
		if _, ok := accounts[lease.AccountID]; !ok {
			audit("lease.unbound_dropped", lease.ID,
				"Restore dropped an active lease for an unknown account",
				map[string]string{
					"leaseId": lease.ID, "nodeId": lease.NodeID,
					"accountId": lease.AccountID, "slot": "legacy",
					"reason": "account row is missing",
				})
			repaired++
			continue
		}
		key := model.SlotKey{AccountID: lease.AccountID, Index: lease.Slot}
		slotID := "legacy"
		if lease.Slot > 0 {
			slotID = key.String()
		}
		reason := ""
		var slot *model.AccountSlot
		var node model.ProxyNode
		if lease.Slot <= 0 {
			// A legacy (Slot 0) lease has no slot row to verify. Its node
			// must still be a LIVE, bound legacy node of the same account:
			// otherwise the row is a claim on a dead endpoint, and a legacy
			// lease pointed at a slot node would bypass the slot checks.
			var ok bool
			node, ok = nodes[lease.NodeID]
			switch {
			case !ok:
				reason = "node row is missing"
			case node.AccountID != lease.AccountID:
				reason = "node belongs to another account"
			case node.Slot != 0:
				reason = "legacy lease names a slot node"
			case node.State != model.NodeLive:
				reason = "node is " + string(node.State)
			case node.GatePort <= 0:
				reason = "no bound listener"
			}
		} else {
			var slotOK, nodeOK bool
			slot, slotOK = slots[key]
			node, nodeOK = nodes[lease.NodeID]
			switch {
			case !slotOK:
				reason = "slot row is missing"
			case slot.State != model.SlotLive:
				reason = "slot is " + string(slot.State)
			case !nodeOK:
				reason = "node row is missing"
			case node.State != model.NodeLive || node.GatePort <= 0 || node.GatePort != slot.GatePort:
				reason = "no bound listener"
			}
		}
		if reason != "" {
			audit("lease.unbound_dropped", lease.ID,
				"Restore dropped an active lease with no bound endpoint: "+reason,
				map[string]string{
					"leaseId": lease.ID, "nodeId": lease.NodeID,
					"accountId": lease.AccountID, "slot": slotID, "reason": reason,
				})
			repaired++
			continue
		}
		if lease.Slot <= 0 {
			if _, duplicate := heldLegacyNodes[lease.NodeID]; duplicate {
				audit("lease.duplicate_dropped", lease.ID,
					"Restore dropped a duplicate active lease for node "+lease.NodeID,
					map[string]string{
						"leaseId": lease.ID, "nodeId": lease.NodeID,
						"accountId": lease.AccountID, "slot": slotID,
					})
				repaired++
				continue
			}
		} else if _, duplicate := heldSlots[key]; duplicate {
			audit("lease.duplicate_dropped", lease.ID,
				"Restore dropped a duplicate active lease for slot "+key.String(),
				map[string]string{
					"leaseId": lease.ID, "nodeId": lease.NodeID,
					"accountId": lease.AccountID, "slot": key.String(),
				})
			repaired++
			continue
		}
		// Capacity is a per-account ceiling: a snapshot written under a wider
		// AJS_ACCOUNT_SLOTS (or edited by hand) may hold more active
		// entitlements than admission would now grant. The first rows in
		// document order keep their claim; later ones are dropped with an
		// attributed audit row. This applies to legacy rows too.
		capacity := capacityFor(lease.AccountID)
		if active[lease.AccountID] >= capacity {
			audit("lease.over_capacity_dropped", lease.ID,
				fmt.Sprintf("Restore dropped an active lease above the account capacity %d", capacity),
				map[string]string{
					"leaseId": lease.ID, "nodeId": lease.NodeID,
					"accountId": lease.AccountID, "slot": slotID,
					"capacity": strconv.Itoa(capacity),
					"active":   strconv.Itoa(active[lease.AccountID]),
				})
			repaired++
			continue
		}
		if lease.Slot > 0 {
			heldSlots[key] = struct{}{}
		} else {
			heldLegacyNodes[lease.NodeID] = struct{}{}
		}
		active[lease.AccountID]++
		if lease.Slot > 0 && lease.Generation != slot.Generation {
			from := lease.Generation
			lease.Slot = slot.Index
			lease.Generation = slot.Generation
			lease.NodeLabel = node.Label
			lease.Region = node.Region
			lease.EgressIP = node.EgressIP
			lease.Protocol = node.Protocol
			lease.GatePort = node.GatePort
			audit("lease.generation_repaired", lease.ID,
				fmt.Sprintf("Restore advanced lease generation %d → %d", from, slot.Generation),
				map[string]string{
					"leaseId":        lease.ID,
					"nodeId":         node.ID,
					"slot":           key.String(),
					"fromGeneration": strconv.FormatUint(from, 10),
					"toGeneration":   strconv.FormatUint(slot.Generation, 10),
				})
			repaired++
		}
		kept = append(kept, lease)
	}
	snap.Leases = kept
	return repaired
}

// restoreCapacity is the active-lease ceiling admission would apply to one
// restored account. It mirrors accountCapacityLocked: the vendor/operator slot
// allowance capped by the LIVE nodes that actually bound a listener. A READY
// node is not capacity even when it remembers a port.
func (snap *Snapshot) restoreCapacity(account model.Account, slots map[model.SlotKey]*model.AccountSlot) int {
	capacity := SlotCapacity(account.UseCount, configuredAccountSlots())
	serving := 0
	seen := make(map[int]struct{}, MaxServiceSlots)
	for _, node := range snap.Nodes {
		if node.AccountID != account.ID {
			continue
		}
		if node.State != model.NodeLive || node.GatePort <= 0 {
			continue
		}
		if node.Slot > 0 {
			if _, duplicate := seen[node.Slot]; duplicate {
				continue
			}
			slot, ok := slots[model.SlotKey{AccountID: node.AccountID, Index: node.Slot}]
			if !ok || slot.State != model.SlotLive || slot.GatePort != node.GatePort {
				continue
			}
			seen[node.Slot] = struct{}{}
		}
		serving++
	}
	if serving < capacity {
		capacity = serving
	}
	return capacity
}

// validateAccountSlots rejects a document whose slot rows cannot be loaded
// as-is. A duplicate (account,index), an index outside 1..8, an unknown state,
// a device id or gate port used twice, and a slot-1/binding mismatch are hard
// errors: silently dropping a slot would change capacity or identity.
func (snap *Snapshot) validateAccountSlots(accounts map[string]struct{}) error {
	seen := make(map[model.SlotKey]struct{}, len(snap.AccountSlots))
	slotsByKey := make(map[model.SlotKey]model.AccountSlot, len(snap.AccountSlots))
	deviceOwners := make(map[string]model.SlotKey, len(snap.AccountSlots))
	// Both listener kinds share one port namespace: a slot's SOCKS port may
	// not equal another slot's HTTP port (or the reverse), or runtime
	// reservation would refuse the pair forever.
	portOwners := make(map[int]model.SlotKey, len(snap.AccountSlots)*2)
	slotOne := make(map[string]model.AccountSlot, len(accounts))
	for _, slot := range snap.AccountSlots {
		if err := slot.Validate(); err != nil {
			return fmt.Errorf("state snapshot %v", err)
		}
		if _, ok := accounts[slot.AccountID]; !ok {
			return fmt.Errorf("state snapshot has a slot for unknown account %q", slot.AccountID)
		}
		if _, duplicate := seen[slot.SlotKey]; duplicate {
			return fmt.Errorf("state snapshot has duplicate slot %s", slot.SlotKey.String())
		}
		seen[slot.SlotKey] = struct{}{}
		slotsByKey[slot.SlotKey] = slot
		if slot.DeviceID != "" {
			if other, used := deviceOwners[slot.DeviceID]; used {
				return fmt.Errorf("state snapshot slots %s and %s share device %q",
					other.String(), slot.SlotKey.String(), slot.DeviceID)
			}
			deviceOwners[slot.DeviceID] = slot.SlotKey
		}
		if slot.GatePort != 0 {
			if other, used := portOwners[slot.GatePort]; used {
				return fmt.Errorf("state snapshot slots %s and %s share port %d",
					other.String(), slot.SlotKey.String(), slot.GatePort)
			}
			portOwners[slot.GatePort] = slot.SlotKey
		}
		if slot.HTTPPort != 0 {
			if other, used := portOwners[slot.HTTPPort]; used {
				return fmt.Errorf("state snapshot slots %s and %s share port %d",
					other.String(), slot.SlotKey.String(), slot.HTTPPort)
			}
			portOwners[slot.HTTPPort] = slot.SlotKey
		}
		if slot.Index == 1 {
			slotOne[slot.AccountID] = slot
		}
	}
	for accountID, deviceID := range snap.DeviceBindings {
		slot, ok := slotOne[accountID]
		if !ok {
			return fmt.Errorf("state snapshot account %q has a device binding but no slot 1", accountID)
		}
		if slot.DeviceID != deviceID {
			return fmt.Errorf("state snapshot slot 1 of account %q references device %q, binding is %q",
				accountID, slot.DeviceID, deviceID)
		}
	}
	if len(snap.DevicePool) == 0 {
		return nil
	}
	// With a real pool, every slot device reference and every assigned record
	// must agree in both directions. This is the invariant the mutation paths
	// maintain under one lock.
	records := make(map[string]*DeviceRecord, len(snap.DevicePool))
	for i := range snap.DevicePool {
		records[snap.DevicePool[i].ID] = &snap.DevicePool[i]
	}
	for _, slot := range snap.AccountSlots {
		if slot.DeviceID == "" {
			continue
		}
		rec, ok := records[slot.DeviceID]
		if !ok {
			return fmt.Errorf("state snapshot slot %s references unknown device %q", slot.SlotKey.String(), slot.DeviceID)
		}
		if rec.State != DeviceAssigned {
			return fmt.Errorf("state snapshot slot %s references %s device %q", slot.SlotKey.String(), rec.State, rec.ID)
		}
		if rec.AssignedAccountID != slot.AccountID {
			return fmt.Errorf("state snapshot slot %s device %q names account %q",
				slot.SlotKey.String(), rec.ID, rec.AssignedAccountID)
		}
		if rec.AssignedSlotKey == nil || *rec.AssignedSlotKey != slot.SlotKey {
			return fmt.Errorf("state snapshot slot %s device %q has slot key %v",
				slot.SlotKey.String(), rec.ID, rec.AssignedSlotKey)
		}
	}
	for _, rec := range snap.DevicePool {
		if rec.State != DeviceAssigned {
			continue
		}
		if rec.AssignedSlotKey == nil {
			return fmt.Errorf("state snapshot device %q is assigned without an assigned slot", rec.ID)
		}
		slot, ok := slotsByKey[*rec.AssignedSlotKey]
		if !ok {
			return fmt.Errorf("state snapshot device %q names missing slot %s", rec.ID, rec.AssignedSlotKey.String())
		}
		if slot.DeviceID != rec.ID {
			return fmt.Errorf("state snapshot device %q is not the device of slot %s", rec.ID, rec.AssignedSlotKey.String())
		}
	}
	return nil
}

// normalizeAccountSlots makes the persisted slice deterministic. The slice is
// always sorted by (account id, index) so two snapshots of the same state are
// byte-identical regardless of map iteration order.
func (snap *Snapshot) normalizeAccountSlots() {
	if snap.AccountSlots == nil {
		snap.AccountSlots = []model.AccountSlot{}
	}
	for i := range snap.AccountSlots {
		if snap.AccountSlots[i].State == "" {
			snap.AccountSlots[i].State = model.SlotProvisioning
		}
	}
	sortAccountSlots(snap.AccountSlots)
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
	snap.normalizeAccountSlots()
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
	if snap.Plans == nil {
		snap.Plans = []model.Plan{}
	}
	if snap.Orders == nil {
		snap.Orders = []orderRecord{}
	}
	if snap.PaymentEvents == nil {
		snap.PaymentEvents = []model.PaymentEvent{}
	}
	snap.WebhookDeliveries = normalizeWebhookDeliveries(snap.WebhookDeliveries)
	snap.WebhookOutbox = normalizeWebhookOutbox(snap.WebhookOutbox)
	for _, tenant := range snap.Tenants {
		snap.TenantSeq = max(snap.TenantSeq, sequenceOf(tenant.ID, "t-"))
	}
	for _, plan := range snap.Plans {
		snap.PlanSeq = max(snap.PlanSeq, sequenceOf(plan.ID, planIDPrefix))
	}
	for _, order := range snap.Orders {
		snap.OrderSeq = max(snap.OrderSeq, sequenceOf(order.ID, orderIDPrefix))
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
