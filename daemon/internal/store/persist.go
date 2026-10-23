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

	"ajsaccountd/internal/model"
)

// SnapshotVersion is the only snapshot schema this build reads and writes.
// Restore rejects any other value before touching the store.
const SnapshotVersion = 1

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

	Accounts []model.Account     `json:"accounts"`
	Nodes    []model.ProxyNode   `json:"nodes"`
	Leases   []model.Lease       `json:"leases"`
	Users    []model.PanelUser   `json:"users"`
	Keys     []model.APIKey      `json:"keys"`
	Requests []model.RequestLog  `json:"requests"`
	Audit    []model.AuditEvent  `json:"audit"`
	Settings model.PanelSettings `json:"settings"`

	KeyHashes map[string]string `json:"keyHashes"`
	Secrets   map[string][]byte `json:"secrets"`
	// Devices maps an account id to its stable device identity. It is not a
	// credential; the field is additive, so SnapshotVersion stays 1 and a
	// document written by a build without it restores with no devices.
	Devices map[string]DeviceIdentity `json:"devices,omitempty"`

	AccountSeq  int `json:"accountSeq"`
	UserSeq     int `json:"userSeq"`
	KeySeq      int `json:"keySeq"`
	MutationSeq int `json:"mutationSeq"`
	AuditSeq    int `json:"auditSeq"`
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
	}
	snap.Keys = make([]model.APIKey, len(s.keys))
	for i, key := range s.keys {
		snap.Keys[i] = cloneKey(key)
	}
	snap.Requests = append([]model.RequestLog(nil), s.requests...)
	snap.Audit = append([]model.AuditEvent(nil), s.audit...)
	snap.Settings = s.settings
	snap.KeyHashes = make(map[string]string, len(s.keyHashes))
	for id, hash := range s.keyHashes {
		snap.KeyHashes[id] = hash
	}
	snap.Secrets = make(map[string][]byte, len(s.secrets))
	for id, sealed := range s.secrets {
		snap.Secrets[id] = append([]byte(nil), sealed...)
	}
	snap.Devices = make(map[string]DeviceIdentity, len(s.devices))
	for id, dev := range s.devices {
		snap.Devices[id] = dev
	}
	snap.AccountSeq = s.accountSeq
	snap.UserSeq = s.userSeq
	snap.KeySeq = s.keySeq
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
	if snap.Version != SnapshotVersion {
		return fmt.Errorf("unsupported state snapshot version %d (want %d)", snap.Version, SnapshotVersion)
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
	s.requests = snap.Requests
	s.audit = snap.Audit
	s.settings = snap.Settings
	s.keyHashes = snap.KeyHashes
	s.secrets = snap.Secrets
	s.devices = snap.Devices
	s.accountSeq = snap.AccountSeq
	s.userSeq = snap.UserSeq
	s.keySeq = snap.KeySeq
	s.mutationSeq = snap.MutationSeq
	s.auditSeq = snap.AuditSeq
	// Both histories are newest first: a snapshot written by an older build
	// (or edited by hand) is cut to the retention window, never the reverse.
	if len(s.requests) > RequestRetention {
		s.requests = s.requests[:RequestRetention]
	}
	if len(s.audit) > AuditRetention {
		s.audit = s.audit[:AuditRetention]
	}
	s.syncAccountCountsLocked()
	s.mu.Unlock()
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
	for id, dev := range snap.Devices {
		if _, ok := accounts[id]; !ok {
			return fmt.Errorf("state snapshot has a device for unknown account %q", id)
		}
		if !dev.valid() {
			return fmt.Errorf("state snapshot has an incomplete device for account %q", id)
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
	if snap.Secrets == nil {
		snap.Secrets = make(map[string][]byte)
	}
	if snap.Devices == nil {
		snap.Devices = make(map[string]DeviceIdentity)
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
