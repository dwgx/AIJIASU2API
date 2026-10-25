package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"ajsaccountd/internal/backup"
	"ajsaccountd/internal/store"
)

const (
	backupTestPassphrase = "correct horse battery staple"
	// backupPasswordMarker and backupDeviceMarker are plaintext strings the
	// fixture writes into the sealed-secret files. A backup that leaks either
	// one is storing plaintext.
	backupPasswordMarker = "MARKER-password-123456"
	backupDeviceMarker   = "MARKER-device-654321"
)

// backupFixture is one daemon state directory plus the bytes it held when the
// fixture was built, which is what a restore must reproduce.
type backupFixture struct {
	dir       string
	statePath string
	server    *Server
	ts        *httptest.Server
	st        *store.Store
	restore   *RestoreFlag
	original  map[string][]byte
}

// newBackupFixture builds a state directory the way the daemon leaves one:
// state.json written by the store, and the two sealed-secret files. The files
// carry marker strings instead of real sealed entries because the backup layer
// treats them as opaque bytes.
func newBackupFixture(t *testing.T, allowRestore bool) *backupFixture {
	t.Helper()
	return newBackupFixtureWithStore(t, allowRestore, store.NewEmpty())
}

// newBackupFixtureWithStore is newBackupFixture with a caller-supplied store,
// used where the test needs the seeded demo users.
func newBackupFixtureWithStore(t *testing.T, allowRestore bool, st *store.Store) *backupFixture {
	t.Helper()
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	if err := st.Save(statePath); err != nil {
		t.Fatalf("save state: %v", err)
	}
	files := map[string][]byte{
		"state.json":          nil,
		"secrets.json":        []byte(`{"version":1,"entries":{"acc-01":"c2VhbGVk"},"password":"` + backupPasswordMarker + `"}`),
		"device-secrets.json": []byte(`{"version":1,"entries":{"dev-01":"c2VhbGVk"},"password":"` + backupDeviceMarker + `"}`),
	}
	for name, data := range files {
		if data == nil {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	original := make(map[string][]byte, 3)
	for _, name := range backupBlobNames {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		original[name] = data
	}

	restore := &RestoreFlag{}
	server := New(Config{
		AdminToken:         testToken,
		StateDir:           dir,
		StatePath:          statePath,
		AllowBackupRestore: allowRestore,
		Restore:            restore,
	}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)
	return &backupFixture{
		dir:       dir,
		statePath: statePath,
		server:    server,
		ts:        ts,
		st:        st,
		restore:   restore,
		original:  original,
	}
}

// envelope creates a backup through the HTTP route and returns the download.
func (f *backupFixture) envelope(t *testing.T) []byte {
	t.Helper()
	resp, body := apiCall(t, f.ts, http.MethodPost, "/v1/backup",
		map[string]any{"passphrase": backupTestPassphrase})
	requireStatus(t, resp, http.StatusOK)
	if len(body) == 0 {
		t.Fatal("backup download is empty")
	}
	return body
}

// writeLiveState replaces state.json with a valid but different snapshot, so a
// dry run has something to report.
func (f *backupFixture) writeLiveState(t *testing.T, nodeState string) {
	t.Helper()
	doc := `{"version":2,"nodes":[{"id":"node-01","accountId":"acc-01","state":"` + nodeState +
		`","egressIp":"203.0.113.9","protocol":"http","gatePort":18080}]}`
	if err := os.WriteFile(f.statePath, []byte(doc), 0o600); err != nil {
		t.Fatalf("write live state: %v", err)
	}
}

func TestBackupRoundTripOverHTTP(t *testing.T) {
	f := newBackupFixture(t, true)

	// 1. Create: an octet-stream download whose bytes hold no plaintext.
	resp, envelope := apiCall(t, f.ts, http.MethodPost, "/v1/backup",
		map[string]any{"passphrase": backupTestPassphrase})
	requireStatus(t, resp, http.StatusOK)
	if got := resp.Header.Get("Content-Type"); got != "application/octet-stream" {
		t.Fatalf("Content-Type = %q, want application/octet-stream", got)
	}
	if got := resp.Header.Get("Content-Disposition"); !strings.HasPrefix(got, `attachment; filename="ajs-backup-`) {
		t.Fatalf("Content-Disposition = %q, want an attachment filename", got)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if got := resp.Header.Get("X-AJS-Backup-Blobs"); got != "3" {
		t.Fatalf("X-AJS-Backup-Blobs = %q, want 3", got)
	}
	for _, marker := range []string{`"password"`, backupPasswordMarker, backupDeviceMarker, `"entries"`} {
		if bytes.Contains(envelope, []byte(marker)) {
			t.Fatalf("envelope contains the plaintext marker %q", marker)
		}
	}
	header, err := backup.Inspect(envelope)
	if err != nil {
		t.Fatalf("inspect sealed envelope: %v", err)
	}
	if header.Version != backup.Version || header.App != backup.AppName {
		t.Fatalf("header = %+v, want version %d app %q", header, backup.Version, backup.AppName)
	}

	// 2. Inspect without a passphrase: header only, no manifest.
	resp, body := apiCall(t, f.ts, http.MethodPost, "/v1/backup/inspect",
		map[string]any{"envelope": json.RawMessage(envelope)})
	requireStatus(t, resp, http.StatusOK)
	headless := decodeBody[backupInspectView](t, body)
	if !headless.Supported || headless.Version != backup.Version {
		t.Fatalf("inspect = %+v, want a supported envelope", headless)
	}
	if headless.Manifest != nil {
		t.Fatal("inspect without a passphrase returned a manifest")
	}
	if headless.EnvelopeBytes != len(envelope) {
		t.Fatalf("envelopeBytes = %d, want %d", headless.EnvelopeBytes, len(envelope))
	}
	if bytes.Contains(body, []byte(backupPasswordMarker)) {
		t.Fatal("inspect response contains a plaintext marker")
	}

	// 3. Inspect with the passphrase: names, sizes and hashes only.
	resp, body = apiCall(t, f.ts, http.MethodPost, "/v1/backup/inspect",
		map[string]any{"envelope": json.RawMessage(envelope), "passphrase": backupTestPassphrase})
	requireStatus(t, resp, http.StatusOK)
	full := decodeBody[backupInspectView](t, body)
	if full.Manifest == nil || len(full.Manifest.Entries) != 3 {
		t.Fatalf("manifest = %+v, want three entries", full.Manifest)
	}
	wantOrder := []string{"device-secrets.json", "secrets.json", "state.json"}
	for i, entry := range full.Manifest.Entries {
		if entry.Name != wantOrder[i] {
			t.Fatalf("manifest[%d].name = %q, want %q", i, entry.Name, wantOrder[i])
		}
		if entry.Size != int64(len(f.original[entry.Name])) {
			t.Fatalf("%s size = %d, want %d", entry.Name, entry.Size, len(f.original[entry.Name]))
		}
		if len(entry.SHA256) != 64 {
			t.Fatalf("%s sha256 = %q, want a hex digest", entry.Name, entry.SHA256)
		}
		if !entry.Required && entry.Name == "state.json" {
			t.Fatal("state.json is not marked required")
		}
	}
	if bytes.Contains(body, f.original["secrets.json"]) {
		t.Fatal("inspect response contains the sealed-secrets payload")
	}

	// 4. Dry run: default when dryRun is omitted, and it must not write.
	f.writeLiveState(t, "READY")
	resp, body = apiCall(t, f.ts, http.MethodPost, "/v1/backup/restore",
		map[string]any{"envelope": json.RawMessage(envelope), "passphrase": backupTestPassphrase})
	requireStatus(t, resp, http.StatusOK)
	dry := decodeBody[backupRestoreView](t, body)
	if !dry.DryRun || dry.Applied {
		t.Fatalf("dry run = %+v, want dryRun and no apply", dry)
	}
	if dry.Diff.Changed != 1 || len(dry.Diff.ChangedNames) != 1 || dry.Diff.ChangedNames[0] != "state.json" {
		t.Fatalf("diff = %+v, want state.json changed", dry.Diff)
	}
	if dry.Diff.Same != 2 || dry.Diff.Added != 0 || dry.Diff.Removed != 0 {
		t.Fatalf("diff = %+v, want two unchanged blobs", dry.Diff)
	}
	if len(dry.Files) != 0 || dry.RestartRequired {
		t.Fatalf("dry run = %+v, want no files and no restart", dry)
	}
	if dry.ExitRecommended {
		t.Fatalf("dry run = %+v, want no exit recommendation", dry)
	}
	if f.restore.Restored() {
		t.Fatal("a dry run marked the panel restored")
	}
	if live, err := os.ReadFile(f.statePath); err != nil || !bytes.Contains(live, []byte("READY")) {
		t.Fatalf("dry run rewrote state.json (err %v)", err)
	}

	// 5. Apply: confirm plus the passphrase replaces the panel files.
	resp, body = apiCall(t, f.ts, http.MethodPost, "/v1/backup/restore", map[string]any{
		"envelope":   json.RawMessage(envelope),
		"passphrase": backupTestPassphrase,
		"dryRun":     false,
		"confirm":    "RESTORE",
	})
	requireStatus(t, resp, http.StatusOK)
	applied := decodeBody[backupRestoreView](t, body)
	if applied.DryRun || !applied.Applied || !applied.RestartRequired {
		t.Fatalf("apply = %+v, want applied with a restart", applied)
	}
	if !applied.ExitRecommended {
		t.Fatalf("apply = %+v, want exitRecommended:true", applied)
	}
	if !f.restore.Restored() {
		t.Fatal("apply did not mark the shared restore flag: the snapshot writer would overwrite the restored state.json")
	}
	if len(applied.Files) != 3 {
		t.Fatalf("files = %v, want all three blobs", applied.Files)
	}
	if applied.Warning == "" {
		t.Fatal("apply response carries no restart warning")
	}

	// 6. The restored bytes are the backed-up bytes, and state.json still
	// loads as a snapshot.
	for name, want := range f.original {
		got, err := os.ReadFile(filepath.Join(f.dir, name))
		if err != nil {
			t.Fatalf("read restored %s: %v", name, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("restored %s differs from the backup", name)
		}
		if runtime.GOOS != "windows" {
			info, err := os.Stat(filepath.Join(f.dir, name))
			if err != nil {
				t.Fatalf("stat restored %s: %v", name, err)
			}
			if perm := info.Mode().Perm(); perm != 0o600 {
				t.Fatalf("%s mode = %o, want 600", name, perm)
			}
		}
	}
	fresh := store.NewEmpty()
	if err := fresh.Load(f.statePath); err != nil {
		t.Fatalf("restored state.json does not load: %v", err)
	}
	if nodes := fresh.Nodes(); len(nodes) != 0 {
		t.Fatalf("restored store has %d nodes, want the backed-up empty panel", len(nodes))
	}
}

func TestBackupRestoreRefusals(t *testing.T) {
	t.Run("live node", func(t *testing.T) {
		f := newBackupFixture(t, true)
		envelope := f.envelope(t)
		// A node is LIVE: the state file says so and the running store loaded
		// the same document, which is what the daemon does at startup.
		f.writeLiveState(t, "LIVE")
		if err := f.st.Load(f.statePath); err != nil {
			t.Fatalf("load LIVE node: %v", err)
		}
		before, err := os.ReadFile(f.statePath)
		if err != nil {
			t.Fatalf("read state: %v", err)
		}
		resp, body := apiCall(t, f.ts, http.MethodPost, "/v1/backup/restore", map[string]any{
			"envelope":   json.RawMessage(envelope),
			"passphrase": backupTestPassphrase,
			"dryRun":     false,
			"confirm":    "RESTORE",
		})
		requireStatus(t, resp, http.StatusConflict)
		requireErrorCode(t, body, CodeBackupLiveNode)
		if f.restore.Restored() {
			t.Fatal("a refused restore marked the panel restored")
		}
		if !bytes.Contains(body, []byte(`"liveNodes":"1"`)) {
			t.Fatalf("refusal does not name the LIVE node count: %s", body)
		}
		after, err := os.ReadFile(f.statePath)
		if err != nil {
			t.Fatalf("read state: %v", err)
		}
		if !bytes.Equal(before, after) {
			t.Fatal("a refused restore rewrote state.json")
		}
		// A dry run still works with a LIVE node: it changes nothing.
		resp, _ = apiCall(t, f.ts, http.MethodPost, "/v1/backup/restore",
			map[string]any{"envelope": json.RawMessage(envelope), "passphrase": backupTestPassphrase})
		requireStatus(t, resp, http.StatusOK)
	})

	t.Run("daemon running", func(t *testing.T) {
		f := newBackupFixture(t, false)
		envelope := f.envelope(t)
		resp, body := apiCall(t, f.ts, http.MethodPost, "/v1/backup/restore", map[string]any{
			"envelope":   json.RawMessage(envelope),
			"passphrase": backupTestPassphrase,
			"dryRun":     false,
			"confirm":    "RESTORE",
		})
		requireStatus(t, resp, http.StatusConflict)
		requireErrorCode(t, body, CodeBackupDaemonRunning)
	})

	t.Run("confirm required", func(t *testing.T) {
		f := newBackupFixture(t, true)
		envelope := f.envelope(t)
		for _, confirm := range []string{"", "restore", "RESTORE "} {
			resp, body := apiCall(t, f.ts, http.MethodPost, "/v1/backup/restore", map[string]any{
				"envelope":   json.RawMessage(envelope),
				"passphrase": backupTestPassphrase,
				"dryRun":     false,
				"confirm":    confirm,
			})
			requireStatus(t, resp, http.StatusBadRequest)
			requireErrorCode(t, body, CodeInvalidArgument)
		}
	})

	t.Run("wrong passphrase", func(t *testing.T) {
		f := newBackupFixture(t, true)
		envelope := f.envelope(t)
		resp, body := apiCall(t, f.ts, http.MethodPost, "/v1/backup/restore", map[string]any{
			"envelope":   json.RawMessage(envelope),
			"passphrase": "not the right passphrase",
			"dryRun":     false,
			"confirm":    "RESTORE",
		})
		requireStatus(t, resp, http.StatusBadRequest)
		requireErrorCode(t, body, CodeInvalidArgument)
		if bytes.Contains(body, []byte(backupPasswordMarker)) {
			t.Fatal("refusal leaks a plaintext marker")
		}
		// A dry run refuses the same passphrase instead of reporting a diff.
		resp, body = apiCall(t, f.ts, http.MethodPost, "/v1/backup/restore",
			map[string]any{"envelope": json.RawMessage(envelope), "passphrase": "not the right passphrase"})
		requireStatus(t, resp, http.StatusBadRequest)
		requireErrorCode(t, body, CodeInvalidArgument)
	})

	t.Run("weak passphrase", func(t *testing.T) {
		f := newBackupFixture(t, true)
		resp, body := apiCall(t, f.ts, http.MethodPost, "/v1/backup", map[string]any{"passphrase": "short"})
		requireStatus(t, resp, http.StatusBadRequest)
		requireErrorCode(t, body, CodeInvalidArgument)
		if !bytes.Contains(body, []byte("passphrase must be 12 to 1024 bytes")) {
			t.Fatalf("weak passphrase message = %s", body)
		}
		resp, body = apiCall(t, f.ts, http.MethodPost, "/v1/backup",
			map[string]any{"passphrase": strings.Repeat("a", backup.MaxPassphraseBytes+1)})
		requireStatus(t, resp, http.StatusBadRequest)
		requireErrorCode(t, body, CodeInvalidArgument)
	})

	t.Run("busy", func(t *testing.T) {
		f := newBackupFixture(t, true)
		envelope := f.envelope(t)
		f.server.backupMu.Lock()
		defer f.server.backupMu.Unlock()

		resp, body := apiCall(t, f.ts, http.MethodPost, "/v1/backup",
			map[string]any{"passphrase": backupTestPassphrase})
		requireStatus(t, resp, http.StatusConflict)
		requireErrorCode(t, body, CodeBackupBusy)

		resp, body = apiCall(t, f.ts, http.MethodPost, "/v1/backup/restore",
			map[string]any{"envelope": json.RawMessage(envelope), "passphrase": backupTestPassphrase})
		requireStatus(t, resp, http.StatusConflict)
		requireErrorCode(t, body, CodeBackupBusy)

		// Inspect only reads the request body, so it stays available.
		resp, _ = apiCall(t, f.ts, http.MethodPost, "/v1/backup/inspect",
			map[string]any{"envelope": json.RawMessage(envelope)})
		requireStatus(t, resp, http.StatusOK)
	})

	t.Run("missing state blob", func(t *testing.T) {
		f := newBackupFixture(t, true)
		secretsOnly, _, err := backup.Create(map[string][]byte{
			backup.BlobSecrets: []byte(`{"version":1,"entries":{}}`),
		}, backupTestPassphrase, time.Now())
		if err != nil {
			t.Fatalf("create partial envelope: %v", err)
		}
		resp, body := apiCall(t, f.ts, http.MethodPost, "/v1/backup/restore", map[string]any{
			"envelope":   json.RawMessage(secretsOnly),
			"passphrase": backupTestPassphrase,
			"dryRun":     false,
			"confirm":    "RESTORE",
		})
		requireStatus(t, resp, http.StatusBadRequest)
		requireErrorCode(t, body, CodeInvalidArgument)
	})

	t.Run("restore never deletes", func(t *testing.T) {
		f := newBackupFixture(t, true)
		// A backup without secrets.json must leave the live sealed secrets in
		// place rather than deleting the file.
		stateOnly, _, err := backup.Create(map[string][]byte{
			backup.BlobState: f.original["state.json"],
		}, backupTestPassphrase, time.Now())
		if err != nil {
			t.Fatalf("create state-only envelope: %v", err)
		}
		resp, body := apiCall(t, f.ts, http.MethodPost, "/v1/backup/restore", map[string]any{
			"envelope":   json.RawMessage(stateOnly),
			"passphrase": backupTestPassphrase,
			"dryRun":     false,
			"confirm":    "RESTORE",
		})
		requireStatus(t, resp, http.StatusOK)
		view := decodeBody[backupRestoreView](t, body)
		if len(view.Files) != 1 || view.Files[0] != "state.json" {
			t.Fatalf("files = %v, want only state.json", view.Files)
		}
		if view.Diff.Removed != 2 || len(view.Diff.RemovedNames) != 2 {
			t.Fatalf("diff = %+v, want the two omitted blobs reported as removed", view.Diff)
		}
		got, err := os.ReadFile(filepath.Join(f.dir, "secrets.json"))
		if err != nil {
			t.Fatalf("secrets.json was deleted: %v", err)
		}
		if !bytes.Equal(got, f.original["secrets.json"]) {
			t.Fatal("secrets.json was rewritten by a backup that does not carry it")
		}
	})
}

func TestBackupInspectValidation(t *testing.T) {
	f := newBackupFixture(t, true)
	envelope := f.envelope(t)

	t.Run("empty", func(t *testing.T) {
		resp, body := apiCall(t, f.ts, http.MethodPost, "/v1/backup/inspect", map[string]any{})
		requireStatus(t, resp, http.StatusBadRequest)
		requireErrorCode(t, body, CodeInvalidArgument)
	})

	t.Run("not an envelope", func(t *testing.T) {
		resp, body := apiCall(t, f.ts, http.MethodPost, "/v1/backup/inspect",
			map[string]any{"envelope": "not-json"})
		requireStatus(t, resp, http.StatusBadRequest)
		requireErrorCode(t, body, CodeInvalidArgument)
	})

	t.Run("foreign app", func(t *testing.T) {
		resp, body := apiCall(t, f.ts, http.MethodPost, "/v1/backup/inspect",
			map[string]any{"envelope": map[string]any{"version": 1, "app": "other"}})
		requireStatus(t, resp, http.StatusBadRequest)
		requireErrorCode(t, body, CodeInvalidArgument)
	})

	t.Run("newer version is reported unsupported", func(t *testing.T) {
		var doc map[string]any
		if err := json.Unmarshal(envelope, &doc); err != nil {
			t.Fatalf("decode envelope: %v", err)
		}
		doc["version"] = backup.Version + 1
		newer, err := json.Marshal(doc)
		if err != nil {
			t.Fatalf("encode envelope: %v", err)
		}
		resp, body := apiCall(t, f.ts, http.MethodPost, "/v1/backup/inspect",
			map[string]any{"envelope": json.RawMessage(newer)})
		requireStatus(t, resp, http.StatusOK)
		view := decodeBody[backupInspectView](t, body)
		if view.Supported {
			t.Fatalf("inspect = %+v, want supported:false for a newer envelope", view)
		}
		// Open still refuses it, so restore answers 400 rather than writing.
		resp, body = apiCall(t, f.ts, http.MethodPost, "/v1/backup/restore",
			map[string]any{"envelope": json.RawMessage(newer), "passphrase": backupTestPassphrase})
		requireStatus(t, resp, http.StatusBadRequest)
		requireErrorCode(t, body, CodeInvalidArgument)
	})

	t.Run("wrong passphrase", func(t *testing.T) {
		resp, body := apiCall(t, f.ts, http.MethodPost, "/v1/backup/inspect",
			map[string]any{"envelope": json.RawMessage(envelope), "passphrase": "not the right passphrase"})
		requireStatus(t, resp, http.StatusBadRequest)
		requireErrorCode(t, body, CodeInvalidArgument)
	})
}

func TestBackupUnavailableWithoutStateDir(t *testing.T) {
	ts, _ := newTestServer(t)
	envelope := []byte(`{"version":1,"app":"apc"}`)
	calls := []struct {
		path string
		body map[string]any
	}{
		{"/v1/backup", map[string]any{"passphrase": backupTestPassphrase}},
		{"/v1/backup/restore", map[string]any{"envelope": json.RawMessage(envelope), "passphrase": backupTestPassphrase}},
	}
	for _, call := range calls {
		resp, body := apiCall(t, ts, http.MethodPost, call.path, call.body)
		requireStatus(t, resp, http.StatusServiceUnavailable)
		requireErrorCode(t, body, CodeBackupUnavailable)
	}
	// Inspect reads only the request body, so it keeps working without a state
	// directory: an operator can still read a backup header on a store-only
	// daemon.
	resp, body := apiCall(t, ts, http.MethodPost, "/v1/backup/inspect",
		map[string]any{"envelope": json.RawMessage(envelope)})
	requireStatus(t, resp, http.StatusOK)
	if view := decodeBody[backupInspectView](t, body); !view.Supported {
		t.Fatalf("inspect = %+v, want the header", view)
	}
}

func TestBackupCreateRequiresSnapshot(t *testing.T) {
	dir := t.TempDir()
	server := New(Config{AdminToken: testToken, StateDir: dir}, store.NewEmpty(),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)

	resp, body := apiCall(t, ts, http.MethodPost, "/v1/backup",
		map[string]any{"passphrase": backupTestPassphrase})
	requireStatus(t, resp, http.StatusConflict)
	requireErrorCode(t, body, CodeConflict)
	if !bytes.Contains(body, []byte("state.json is missing")) {
		t.Fatalf("message = %s, want the missing-snapshot hint", body)
	}
}

func TestBackupRoutesAreAdminOnly(t *testing.T) {
	f := newBackupFixtureWithStore(t, true, store.New())
	envelope := f.envelope(t)
	// Every non-admin scope: a scoped key cannot reach the sealed panel files
	// by collecting all of them.
	created, _ := mustCreateScopedKey(t, f.ts, map[string]any{
		"userId": "u-01",
		"label":  "everything but admin",
		"scopes": []string{
			"accounts:read", "accounts:write", "catalog:read", "catalog:write",
			"leases:read", "leases:write", "requests:read", "audit:read",
			"metrics:read", "lab:read", "settings:read", "settings:write",
		},
	})
	calls := []struct {
		path string
		body map[string]any
	}{
		{"/v1/backup", map[string]any{"passphrase": backupTestPassphrase}},
		{"/v1/backup/inspect", map[string]any{"envelope": json.RawMessage(envelope)}},
		{"/v1/backup/restore", map[string]any{"envelope": json.RawMessage(envelope), "passphrase": backupTestPassphrase}},
	}
	for _, call := range calls {
		resp, body := scopedCall(t, f.ts, created.Secret, http.MethodPost, call.path, call.body)
		requireStatus(t, resp, http.StatusForbidden)
		requireErrorCode(t, body, CodeForbidden)
	}
}
