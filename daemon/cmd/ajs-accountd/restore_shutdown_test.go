package main

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ajsaccountd/internal/api"
	"ajsaccountd/internal/model"
	"ajsaccountd/internal/store"
)

// snapshotFixture is one state file plus the writer and restore flag that
// run() wires together.
type snapshotFixture struct {
	saver   *stateSaver
	restore *api.RestoreFlag
	path    string
	logs    *bytes.Buffer
}

func newSnapshotFixture(t *testing.T) *snapshotFixture {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	logs := &bytes.Buffer{}
	restore := &api.RestoreFlag{}
	return &snapshotFixture{
		saver: &stateSaver{
			st:      store.NewEmpty(),
			path:    path,
			logger:  slog.New(slog.NewTextHandler(logs, nil)),
			restore: restore,
		},
		restore: restore,
		path:    path,
		logs:    logs,
	}
}

func (f *snapshotFixture) read(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatalf("read %s: %v", f.path, err)
	}
	return data
}

// addAccount mutates the in-memory panel after the baseline snapshot, so a
// stale save that slips through a restore changes the file bytes.
func (f *snapshotFixture) addAccount(t *testing.T, label string) {
	t.Helper()
	if _, err := f.saver.st.CreateAccount(model.CreateAccountInput{
		Label:    label,
		Username: label,
		Password: "not-a-real-secret",
		Platform: model.PlatformWindows,
	}); err != nil {
		t.Fatalf("create account %s: %v", label, err)
	}
}

// TestStateSaverWritesWithoutRestore pins the normal path: without a restore
// every snapshot is persisted, including the one the shutdown defer writes.
func TestStateSaverWritesWithoutRestore(t *testing.T) {
	f := newSnapshotFixture(t)
	f.saver.save("interval")
	if !strings.Contains(f.logs.String(), "state_saved") || !strings.Contains(f.logs.String(), "reason=interval") {
		t.Fatalf("interval save was not logged:\n%s", f.logs.String())
	}
	if empty := f.read(t); len(empty) == 0 {
		t.Fatal("interval save wrote an empty state.json")
	}

	// A mutation must reach disk through the same writer, and the shutdown
	// save is the last one the daemon's defer runs.
	f.addAccount(t, "normal-save")
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-stop
	}()
	stopSnapshotWriter(f.saver, stop, done)

	reloaded := store.NewEmpty()
	if err := reloaded.Load(f.path); err != nil {
		t.Fatalf("reload state: %v", err)
	}
	accounts := reloaded.Accounts()
	if len(accounts) != 1 || accounts[0].Label != "normal-save" {
		t.Fatalf("shutdown snapshot has %d accounts, want the normal-save account", len(accounts))
	}
	if !strings.Contains(f.logs.String(), "reason=shutdown") {
		t.Fatalf("shutdown save was not logged:\n%s", f.logs.String())
	}
}

// TestStateSaverSuppressedAfterRestore pins hazard H1: once the shared flag
// says a restore replaced the panel, no snapshot writer touches state.json,
// and the refusal is logged exactly once.
func TestStateSaverSuppressedAfterRestore(t *testing.T) {
	f := newSnapshotFixture(t)
	f.saver.save("baseline")
	before := f.read(t)
	savedBefore := strings.Count(f.logs.String(), "state_saved")
	f.addAccount(t, "stale-panel")

	f.restore.MarkRestored()
	f.saver.save("interval")
	f.saver.save("account-io")
	f.saver.save("shutdown")

	if got := f.read(t); !bytes.Equal(got, before) {
		t.Fatalf("a save after restore changed state.json (before %x, after %x)", before, got)
	}
	if skips := strings.Count(f.logs.String(), "state_save_suppressed_after_restore"); skips != 1 {
		t.Fatalf("suppression logged %d times, want once:\n%s", skips, f.logs.String())
	}
	if !strings.Contains(f.logs.String(), "reason=interval") {
		t.Fatalf("the suppression does not name the first refused save:\n%s", f.logs.String())
	}
	if saved := strings.Count(f.logs.String(), "state_saved"); saved != savedBefore {
		t.Fatalf("a save after restore was still logged as saved:\n%s", f.logs.String())
	}
}

// TestGracefulShutdownAfterRestoreKeepsState walks the path a SIGTERM takes:
// the process stops, the interval writer is closed and the deferred final save
// runs. The restored bytes must survive it byte for byte.
func TestGracefulShutdownAfterRestoreKeepsState(t *testing.T) {
	f := newSnapshotFixture(t)
	f.saver.save("baseline")
	before := f.read(t)
	f.addAccount(t, "stale-panel")
	f.restore.MarkRestored()

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-stop
	}()
	stopSnapshotWriter(f.saver, stop, done)

	if got := f.read(t); !bytes.Equal(got, before) {
		t.Fatal("a graceful shutdown overwrote the restored state.json")
	}
	if !strings.Contains(f.logs.String(), "state_save_suppressed_after_restore") ||
		!strings.Contains(f.logs.String(), "reason=shutdown") {
		t.Fatalf("shutdown save was not suppressed:\n%s", f.logs.String())
	}
}
