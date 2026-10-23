package supervisor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// syncBuffer is a goroutine-safe log sink; monitor goroutines log while the
// test reads.
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

// TestSupervisorHelper is not a real test: Start re-executes this test binary
// with GO_WANT_SUPERVISOR_HELPER=1 and a mode argument after "--".
func TestSupervisorHelper(t *testing.T) {
	if os.Getenv("GO_WANT_SUPERVISOR_HELPER") != "1" {
		return
	}
	args := helperArgs()
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "helper: missing mode")
		os.Exit(2)
	}
	mode := args[0]
	addr := ""
	if len(args) > 1 {
		addr = args[1]
	}

	switch mode {
	case "listen":
		serveHelper(addr, nil)
	case "stderr":
		for _, line := range []string{"helper line 1", "helper line 2", "helper line 3"} {
			fmt.Fprintln(os.Stderr, line)
		}
		serveHelper(addr, nil)
	case "env":
		fmt.Fprintln(os.Stderr, "AJS_MASTER_KEY="+os.Getenv("AJS_MASTER_KEY"))
		fmt.Fprintln(os.Stderr, "AJS_ADMIN_TOKEN="+os.Getenv("AJS_ADMIN_TOKEN"))
		fmt.Fprintln(os.Stderr, "PROBE="+os.Getenv("PROBE"))
		serveHelper(addr, nil)
	case "noready":
		// Deliberately never listens, so Start's readiness poll expires.
		time.Sleep(5 * time.Second)
		os.Exit(0)
	case "exit":
		os.Exit(3)
	default:
		fmt.Fprintln(os.Stderr, "helper: unknown mode", mode)
		os.Exit(2)
	}
}

// serveHelper listens on addr, announces READY on stdout and exits on
// SIGINT/SIGTERM.
func serveHelper(addr string, _ []byte) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper listen:", err)
		os.Exit(2)
	}
	fmt.Println("READY")
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	_ = ln.Close()
	os.Exit(0)
}

func helperArgs() []string {
	for i, arg := range os.Args {
		if arg == "--" {
			return os.Args[i+1:]
		}
	}
	return nil
}

// helperSpec builds a Spec that re-executes the test binary in the given
// helper mode.
func helperSpec(nodeID, mode, addr string) Spec {
	exe, err := os.Executable()
	if err != nil {
		panic(err)
	}
	return Spec{
		NodeID:       nodeID,
		AccountID:    "acc-test",
		Binary:       exe,
		Args:         []string{"-test.run=TestSupervisorHelper", "--", mode, addr},
		Env:          []string{"GO_WANT_SUPERVISOR_HELPER=1"},
		ReadyAddr:    addr,
		ReadyTimeout: 5 * time.Second,
		StopGrace:    2 * time.Second,
		StderrLines:  4,
	}
}

// freeAddr reserves and releases a loopback port, returning its address.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func waitEvent(t *testing.T, s *Supervisor, nodeID string, kind EventKind) Event {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev := <-s.Events():
			if ev.NodeID == nodeID && ev.Kind == kind {
				return ev
			}
		case <-timeout:
			t.Fatalf("timed out waiting for %s event for %s", kind, nodeID)
		}
	}
}

func assertClosed(t *testing.T, addr string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		t.Fatalf("port %s is still accepting connections", addr)
	}
}

func TestStartReadyStop(t *testing.T) {
	var logs syncBuffer
	sup := New(slog.New(slog.NewTextHandler(&logs, nil)))
	addr := freeAddr(t)

	spec := helperSpec("acc-test:srv-01", "listen", addr)
	spec.Env = append(spec.Env, "SUPERVISOR_HELPER_TOKEN=super-secret-value")
	h, err := sup.Start(context.Background(), spec)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if h.PID <= 0 {
		t.Fatalf("PID = %d, want > 0", h.PID)
	}
	if h.StartedAt.IsZero() {
		t.Fatal("StartedAt is zero")
	}
	got, ok := sup.Get("acc-test:srv-01")
	if !ok || got.PID != h.PID {
		t.Fatalf("Get = %v, %v; want PID %d", got, ok, h.PID)
	}
	if list := sup.List(); len(list) != 1 || list[0].NodeID != "acc-test:srv-01" {
		t.Fatalf("List = %+v, want one node", list)
	}

	started := waitEvent(t, sup, "acc-test:srv-01", KindStarted)
	if started.At.IsZero() {
		t.Fatal("started event has no time")
	}
	waitEvent(t, sup, "acc-test:srv-01", KindReady)

	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatalf("dial ready address: %v", err)
	}
	_ = conn.Close()

	if err := sup.Stop(context.Background(), "acc-test:srv-01", 2*time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	select {
	case <-h.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("handle done not closed after Stop")
	}
	waitEvent(t, sup, "acc-test:srv-01", KindExited)
	if _, ok := sup.Get("acc-test:srv-01"); ok {
		t.Fatal("Get still reports a stopped node")
	}
	if list := sup.List(); len(list) != 0 {
		t.Fatalf("List = %+v, want empty", list)
	}
	assertClosed(t, addr)

	// Idempotent: stopping the already exited node is not an error.
	if err := sup.Stop(context.Background(), "acc-test:srv-01", time.Second); err != nil {
		t.Fatalf("second Stop: %v", err)
	}

	if strings.Contains(logs.String(), "super-secret-value") {
		t.Fatal("env value leaked into the log")
	}
	if !strings.Contains(logs.String(), "SUPERVISOR_HELPER_TOKEN") {
		t.Fatal("env key missing from the log")
	}
}

func TestStartUsesEnvAllowlist(t *testing.T) {
	// The daemon environment carries credentials in production; a child must
	// never see them.
	t.Setenv("AJS_MASTER_KEY", "leaked-master-key")
	t.Setenv("AJS_ADMIN_TOKEN", "leaked-admin-token")

	sup := New(nil)
	spec := helperSpec("acc-test:srv-env", "env", freeAddr(t))
	spec.Env = []string{"GO_WANT_SUPERVISOR_HELPER=1", "PROBE=1"}
	h, err := sup.Start(context.Background(), spec)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = sup.Close(context.Background()) })

	var lines []string
	deadline := time.Now().Add(5 * time.Second)
	for len(lines) < 3 && time.Now().Before(deadline) {
		lines = h.Stderr()
		time.Sleep(10 * time.Millisecond)
	}
	want := []string{"AJS_MASTER_KEY=", "AJS_ADMIN_TOKEN=", "PROBE=1"}
	if len(lines) != len(want) {
		t.Fatalf("child stderr = %v, want %v", lines, want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("child stderr[%d] = %q, want %q (all: %v)", i, lines[i], want[i], lines)
		}
	}
}

func TestStartBadBinary(t *testing.T) {
	sup := New(slog.New(slog.NewTextHandler(&syncBuffer{}, nil)))
	spec := helperSpec("acc-test:srv-bad", "listen", freeAddr(t))
	spec.Binary = filepath.Join(t.TempDir(), "does-not-exist")
	if _, err := sup.Start(context.Background(), spec); err == nil {
		t.Fatal("Start with a missing binary succeeded")
	}
	if _, ok := sup.Get("acc-test:srv-bad"); ok {
		t.Fatal("failed node remained registered")
	}
}

func TestStartInvalidSpec(t *testing.T) {
	sup := New(nil)
	base := helperSpec("acc-test:srv-x", "listen", "127.0.0.1:1")
	cases := []struct {
		name string
		mod  func(Spec) Spec
	}{
		{"empty node id", func(s Spec) Spec { s.NodeID = ""; return s }},
		{"empty binary", func(s Spec) Spec { s.Binary = ""; return s }},
		{"empty ready addr", func(s Spec) Spec { s.ReadyAddr = ""; return s }},
		{"malformed ready addr", func(s Spec) Spec { s.ReadyAddr = "not-an-address"; return s }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := sup.Start(context.Background(), tc.mod(base))
			if !errors.Is(err, ErrInvalidSpec) {
				t.Fatalf("err = %v, want ErrInvalidSpec", err)
			}
		})
	}
	if list := sup.List(); len(list) != 0 {
		t.Fatalf("List = %+v, want empty", list)
	}
}

func TestStartDuplicate(t *testing.T) {
	sup := New(nil)
	addr := freeAddr(t)
	spec := helperSpec("acc-test:srv-dup", "listen", addr)
	h, err := sup.Start(context.Background(), spec)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = sup.Close(context.Background()) })

	second := spec
	second.ReadyAddr = freeAddr(t)
	if _, err := sup.Start(context.Background(), second); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("second Start err = %v, want ErrDuplicate", err)
	}
	got, ok := sup.Get("acc-test:srv-dup")
	if !ok || got.PID != h.PID {
		t.Fatalf("duplicate Start disturbed the running node: %v %v", got, ok)
	}
}

func TestStartReadinessTimeout(t *testing.T) {
	sup := New(nil)
	spec := helperSpec("acc-test:srv-slow", "noready", freeAddr(t))
	spec.ReadyTimeout = 300 * time.Millisecond
	spec.StopGrace = time.Second

	start, err := sup.Start(context.Background(), spec)
	if err == nil {
		t.Fatalf("Start returned a handle for an unready node: %+v", start)
	}
	if !errors.Is(err, ErrNotReady) {
		t.Fatalf("err = %v, want ErrNotReady", err)
	}
	var readyErr *ReadyError
	if !errors.As(err, &readyErr) {
		t.Fatalf("err = %T, want *ReadyError", err)
	}
	if readyErr.NodeID != "acc-test:srv-slow" || readyErr.Addr != spec.ReadyAddr {
		t.Fatalf("ReadyError = %+v", readyErr)
	}
	if _, ok := sup.Get("acc-test:srv-slow"); ok {
		t.Fatal("unready node remained registered")
	}
	waitEvent(t, sup, "acc-test:srv-slow", KindExited)
}

func TestStartExitsBeforeReady(t *testing.T) {
	sup := New(nil)
	spec := helperSpec("acc-test:srv-exit", "exit", freeAddr(t))
	spec.ReadyTimeout = 2 * time.Second
	if _, err := sup.Start(context.Background(), spec); err == nil {
		t.Fatal("Start succeeded for a child that exited immediately")
	}
	if _, ok := sup.Get("acc-test:srv-exit"); ok {
		t.Fatal("exited node remained registered")
	}
	waitEvent(t, sup, "acc-test:srv-exit", KindExited)
}

func TestStderrCapture(t *testing.T) {
	sup := New(nil)
	spec := helperSpec("acc-test:srv-err", "stderr", freeAddr(t))
	spec.StderrLines = 2
	h, err := sup.Start(context.Background(), spec)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = sup.Close(context.Background()) })

	var lines []string
	timeout := time.After(5 * time.Second)
	for len(lines) < 3 {
		select {
		case ev := <-sup.Events():
			if ev.NodeID == "acc-test:srv-err" && ev.Kind == KindStderr {
				lines = append(lines, ev.Line)
			}
		case <-timeout:
			t.Fatalf("timed out; stderr events so far: %v", lines)
		}
	}
	if lines[0] != "helper line 1" || lines[2] != "helper line 3" {
		t.Fatalf("stderr events = %v", lines)
	}

	var ring []string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ring = h.Stderr()
		if len(ring) == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(ring) != 2 || ring[0] != "helper line 2" || ring[1] != "helper line 3" {
		t.Fatalf("stderr ring = %v, want the last two lines", ring)
	}
}

func TestStopAndSignalUnknown(t *testing.T) {
	sup := New(nil)
	if err := sup.Stop(context.Background(), "acc-test:missing", time.Second); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Stop err = %v, want ErrNotFound", err)
	}
	if err := sup.Signal("acc-test:missing", syscall.SIGTERM); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Signal err = %v, want ErrNotFound", err)
	}
}

func TestSignalRunning(t *testing.T) {
	sup := New(nil)
	addr := freeAddr(t)
	h, err := sup.Start(context.Background(), helperSpec("acc-test:srv-sig", "listen", addr))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = sup.Close(context.Background()) })
	waitEvent(t, sup, "acc-test:srv-sig", KindReady)

	// SIGTERM is a no-op on Windows; the signal must either be delivered or
	// fail with a platform error, never a supervisor error.
	err = sup.Signal("acc-test:srv-sig", syscall.SIGTERM)
	if err != nil && !strings.Contains(err.Error(), "signal") {
		t.Fatalf("Signal: %v", err)
	}
	select {
	case <-h.Done():
	case <-time.After(200 * time.Millisecond):
	}
}

func TestCloseStopsAll(t *testing.T) {
	sup := New(nil)
	addrA, addrB := freeAddr(t), freeAddr(t)
	if _, err := sup.Start(context.Background(), helperSpec("acc-test:a", "listen", addrA)); err != nil {
		t.Fatalf("Start a: %v", err)
	}
	if _, err := sup.Start(context.Background(), helperSpec("acc-test:b", "listen", addrB)); err != nil {
		t.Fatalf("Start b: %v", err)
	}
	waitEvent(t, sup, "acc-test:a", KindReady)
	waitEvent(t, sup, "acc-test:b", KindReady)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := sup.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if list := sup.List(); len(list) != 0 {
		t.Fatalf("List after Close = %+v, want empty", list)
	}
	for _, id := range []string{"acc-test:a", "acc-test:b"} {
		if _, ok := sup.Get(id); ok {
			t.Fatalf("Get(%s) after Close still reports running", id)
		}
	}
	assertClosed(t, addrA)
	assertClosed(t, addrB)
	if err := sup.Close(ctx); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := sup.Start(ctx, helperSpec("acc-test:c", "listen", freeAddr(t))); !errors.Is(err, ErrClosed) {
		t.Fatalf("Start after Close err = %v, want ErrClosed", err)
	}
}
