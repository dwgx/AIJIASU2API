//go:build !windows

package supervisor

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestStopKillsGrandchild proves the process-group kill: a child that ignores
// SIGINT and spawns a grandchild is stopped by Stop, and the grandchild (which
// never received the interrupt and holds the stderr pipe open) dies with it.
// Without Setpgid + kill(-pid), the grandchild would survive as an orphan.
func TestStopKillsGrandchild(t *testing.T) {
	sup := New(nil)
	addr := freeAddr(t)
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")

	spec := helperSpec("acc-test:srv-gc", "grandchild", addr)
	spec.Args = append(spec.Args, pidFile)
	h, err := sup.Start(context.Background(), spec)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// The child announces readiness only after the grandchild pid file
	// exists, so a ready Start means the grandchild is running.
	var grandpid int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, rerr := os.ReadFile(pidFile); rerr == nil {
			if grandpid, err = strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if grandpid == 0 {
		t.Fatalf("grandchild pid file %s never appeared; Start err: %v", pidFile, err)
	}

	if err := sup.Stop(context.Background(), "acc-test:srv-gc", 500*time.Millisecond); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	select {
	case <-h.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("child did not exit after Stop")
	}

	// Both the child and the grandchild must be gone: kill -0 fails with
	// ESRCH once the process no longer exists. A zombie would still answer
	// 0, so wait out the parent's reaping window first.
	deadline = time.Now().Add(3 * time.Second)
	var childAlive, grandAlive bool
	for time.Now().Before(deadline) {
		childAlive = syscall.Kill(h.PID, 0) == nil
		grandAlive = syscall.Kill(grandpid, 0) == nil
		if !childAlive && !grandAlive {
			t.Logf("child %d and grandchild %d are both dead", h.PID, grandpid)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	if grandAlive {
		t.Fatalf("grandchild %d survived Stop of child %d (orphaned)", grandpid, h.PID)
	}
	if childAlive {
		t.Fatalf("child %d survived Stop", h.PID)
	}
}
