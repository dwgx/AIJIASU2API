//go:build unix

package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"ajsaccountd/internal/backup"
	"ajsaccountd/internal/model"
	"ajsaccountd/internal/store"
)

// daemonChildEnv turns the test binary into the real daemon (TestMain below),
// so this test needs no separate build step.
const daemonChildEnv = "AJS_TEST_DAEMON_CHILD"

// TestMain re-executes the test binary as the daemon for the SIGTERM test.
// Every other test runs normally.
func TestMain(m *testing.M) {
	if os.Getenv(daemonChildEnv) == "1" {
		os.Args = []string{os.Args[0]}
		flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ExitOnError)
		if err := run(); err != nil {
			fmt.Fprintln(os.Stderr, "ajs-accountd:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// syncBuffer is the child's stdout/stderr sink. exec copies from the pipe on
// its own goroutine, so reads must be synchronized with those writes.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestGracefulSIGTERMKeepsRestoredState is the process-level proof for hazard
// H1 (RESTORE-DRILL.md): after POST /v1/backup/restore applies a panel, a
// graceful SIGTERM must not write the daemon's stale in-memory panel over the
// restored state.json.
func TestGracefulSIGTERMKeepsRestoredState(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")

	// The panel the daemon loads at startup: empty.
	if err := store.NewEmpty().Save(statePath); err != nil {
		t.Fatalf("save startup state: %v", err)
	}

	// The panel the restore writes: one account, so its bytes differ from the
	// startup panel and from anything a stale in-memory save could produce.
	restored := store.NewEmpty()
	if _, err := restored.CreateAccount(model.CreateAccountInput{
		Label:    "restored-h1",
		Username: "restored-h1",
		Password: "not-a-real-secret",
		Platform: model.PlatformWindows,
	}); err != nil {
		t.Fatalf("build restored panel: %v", err)
	}
	restoredBytes, err := restored.Snapshot()
	if err != nil {
		t.Fatalf("snapshot restored panel: %v", err)
	}
	envelope, _, err := backup.Create(map[string][]byte{backup.BlobState: restoredBytes},
		"correct horse battery staple", time.Now())
	if err != nil {
		t.Fatalf("seal restore envelope: %v", err)
	}

	token := randomToken(t)
	addr := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(daemonTestEnv(),
		daemonChildEnv+"=1",
		"AJS_ADMIN_TOKEN="+token,
		"AJS_LISTEN="+addr,
		"AJS_STATE_DIR="+dir,
		"AJS_STATE_FILE="+statePath,
		"AJS_BACKUP_ALLOW_RESTORE=1",
		// Keep the interval ticker out of the test: the shutdown save is the
		// writer under test.
		"AJS_SNAPSHOT_INTERVAL=1h",
	)
	var logs syncBuffer
	cmd.Stdout = &logs
	cmd.Stderr = &logs
	if err := cmd.Start(); err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})

	client := &http.Client{Timeout: 10 * time.Second}
	waitReady(t, client, addr, token, &logs)

	payload, err := json.Marshal(map[string]any{
		"envelope":   json.RawMessage(envelope),
		"passphrase": "correct horse battery staple",
		"dryRun":     false,
		"confirm":    "RESTORE",
	})
	if err != nil {
		t.Fatalf("encode restore request: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, "http://"+addr+"/v1/backup/restore", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("build restore request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-AJS-CSRF", "1")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("apply restore: %v\nlogs:\n%s", err, logs.String())
	}
	var view struct {
		Applied         bool `json:"applied"`
		RestartRequired bool `json:"restartRequired"`
		ExitRecommended bool `json:"exitRecommended"`
	}
	decodeErr := json.NewDecoder(resp.Body).Decode(&view)
	_ = resp.Body.Close()
	if decodeErr != nil {
		t.Fatalf("decode restore response: %v\nlogs:\n%s", decodeErr, logs.String())
	}
	if resp.StatusCode != http.StatusOK || !view.Applied || !view.RestartRequired || !view.ExitRecommended {
		t.Fatalf("restore response = %d %+v, want 200 applied restartRequired exitRecommended\nlogs:\n%s",
			resp.StatusCode, view, logs.String())
	}

	applied, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read applied state: %v", err)
	}
	if !bytes.Equal(applied, restoredBytes) {
		t.Fatalf("apply wrote sha256 %x, want %x", sha256.Sum256(applied), sha256.Sum256(restoredBytes))
	}

	// The hazard: a graceful stop after the apply used to save the store that
	// was loaded at startup (plus the restore's audit row) over the restored
	// file.
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal daemon: %v", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("daemon did not stop cleanly: %v\nlogs:\n%s", err, logs.String())
		}
	case <-time.After(20 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("daemon did not exit after SIGTERM\nlogs:\n%s", logs.String())
	}

	after, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state after SIGTERM: %v", err)
	}
	if !bytes.Equal(after, restoredBytes) {
		t.Fatalf("graceful SIGTERM overwrote the restored state.json: sha256 %x, want %x\nlogs:\n%s",
			sha256.Sum256(after), sha256.Sum256(restoredBytes), logs.String())
	}
	if !strings.Contains(logs.String(), "state_save_suppressed_after_restore") {
		t.Fatalf("shutdown save was not suppressed after the restore\nlogs:\n%s", logs.String())
	}
}

// daemonTestEnv inherits the test environment without any AJS_* variable, so a
// developer's daemon settings cannot steer the child.
func daemonTestEnv() []string {
	env := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "AJS_") {
			continue
		}
		env = append(env, entry)
	}
	return env
}

func waitReady(t *testing.T, client *http.Client, addr, token string, logs *syncBuffer) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		req, err := http.NewRequest(http.MethodGet, "http://"+addr+"/healthz", nil)
		if err != nil {
			t.Fatalf("build health request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
			lastErr = fmt.Errorf("healthz answered %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("daemon did not become ready: %v\nlogs:\n%s", lastErr, logs.String())
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer func() { _ = listener.Close() }()
	return listener.Addr().(*net.TCPAddr).Port
}

func randomToken(t *testing.T) string {
	t.Helper()
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		t.Fatalf("random token: %v", err)
	}
	return hex.EncodeToString(buf[:])
}
