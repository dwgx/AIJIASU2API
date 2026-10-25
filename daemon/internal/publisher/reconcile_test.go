package publisher

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testReconcileLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func mustReconciler(t *testing.T, cfg ReconcileConfig) *Reconciler {
	t.Helper()
	if cfg.Logger == nil {
		cfg.Logger = testReconcileLogger()
	}
	r, err := NewReconciler(cfg)
	if err != nil {
		t.Fatalf("NewReconciler: %v", err)
	}
	return r
}

// waitRecorder replaces the loop's timer so a test drives every interval by
// hand. Each call is recorded before it blocks, so next() is a deterministic
// "the loop reached its wait" signal.
type waitRecorder struct {
	mu       sync.Mutex
	waits    []time.Duration
	recorded chan time.Duration
	resume   chan struct{}
}

func newWaitRecorder() *waitRecorder {
	return &waitRecorder{
		recorded: make(chan time.Duration, 32),
		resume:   make(chan struct{}),
	}
}

func (w *waitRecorder) wait(ctx context.Context, d time.Duration, trigger <-chan struct{}) error {
	w.mu.Lock()
	w.waits = append(w.waits, d)
	w.mu.Unlock()
	w.recorded <- d
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-trigger:
		return nil
	case <-w.resume:
		return nil
	}
}

func (w *waitRecorder) next(t *testing.T) time.Duration {
	t.Helper()
	select {
	case d := <-w.recorded:
		return d
	case <-time.After(5 * time.Second):
		t.Fatal("reconciler did not reach its wait")
		return 0
	}
}

func (w *waitRecorder) recordedWaits() []time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]time.Duration(nil), w.waits...)
}

func TestDesiredStateHashTracksNodesLeasesAccounts(t *testing.T) {
	base := DesiredState{
		Nodes:    []Node{{Tag: "us-ca-01", GatePort: 13001}, {Tag: "jp-13-02", GatePort: 13002}},
		Leases:   []LeaseID{{PlatformID: "linux", Account: "acc-01"}},
		Accounts: []string{"acc-01", "acc-02"},
	}
	hash := base.Hash()
	if len(hash) != 64 {
		t.Fatalf("hash = %q, want 64 hex characters", hash)
	}
	if again := base.Hash(); again != hash {
		t.Fatalf("hash is not deterministic: %q != %q", again, hash)
	}

	clone := func() DesiredState {
		return DesiredState{
			Nodes:    append([]Node(nil), base.Nodes...),
			Leases:   append([]LeaseID(nil), base.Leases...),
			Accounts: append([]string(nil), base.Accounts...),
		}
	}
	reordered := clone()
	reordered.Nodes[0], reordered.Nodes[1] = reordered.Nodes[1], reordered.Nodes[0]
	reordered.Accounts[0], reordered.Accounts[1] = reordered.Accounts[1], reordered.Accounts[0]
	if got := reordered.Hash(); got != hash {
		t.Fatalf("slice order changed the hash: %q != %q", got, hash)
	}

	cases := []struct {
		name   string
		mutate func(*DesiredState)
	}{
		{"node port", func(s *DesiredState) { s.Nodes[0].GatePort++ }},
		{"node tag", func(s *DesiredState) { s.Nodes[0].Tag = "us-ca-99" }},
		{"node removed", func(s *DesiredState) { s.Nodes = s.Nodes[:1] }},
		{"lease added", func(s *DesiredState) {
			s.Leases = append(s.Leases, LeaseID{PlatformID: "android", Account: "acc-02"})
		}},
		{"lease removed", func(s *DesiredState) { s.Leases = nil }},
		{"account added", func(s *DesiredState) { s.Accounts = append(s.Accounts, "acc-03") }},
		{"account removed", func(s *DesiredState) { s.Accounts = s.Accounts[:1] }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := clone()
			tc.mutate(&state)
			if got := state.Hash(); got == hash {
				t.Fatalf("%s did not change the hash", tc.name)
			}
		})
	}
}

func TestNewReconcilerDefaultsAndValidation(t *testing.T) {
	snapshot := func() DesiredState { return DesiredState{} }
	reconcile := func(context.Context) error { return nil }

	r, err := NewReconciler(ReconcileConfig{Snapshot: snapshot, Reconcile: reconcile})
	if err != nil {
		t.Fatalf("NewReconciler: %v", err)
	}
	if r.cfg.Interval != DefaultReconcileInterval {
		t.Fatalf("Interval = %s, want %s", r.cfg.Interval, DefaultReconcileInterval)
	}
	if r.cfg.BackoffInitial != time.Minute || r.cfg.BackoffMax != 15*time.Minute {
		t.Fatalf("backoff = %s/%s, want 1m/15m", r.cfg.BackoffInitial, r.cfg.BackoffMax)
	}
	if r.cfg.Jitter != DefaultReconcileJitter {
		t.Fatalf("Jitter = %s, want %s", r.cfg.Jitter, DefaultReconcileJitter)
	}
	if r.cfg.Audit == nil || r.cfg.wait == nil || r.cfg.rand == nil || r.cfg.Logger == nil {
		t.Fatal("NewReconciler left a nil dependency")
	}

	// A short interval caps the default jitter so the loop still starts
	// within its own cadence.
	r, err = NewReconciler(ReconcileConfig{Interval: time.Second, Snapshot: snapshot, Reconcile: reconcile})
	if err != nil {
		t.Fatalf("NewReconciler: %v", err)
	}
	if r.cfg.Jitter != time.Second {
		t.Fatalf("Jitter = %s, want the 1s interval", r.cfg.Jitter)
	}

	// A max below the initial delay is clamped instead of producing a
	// schedule that shrinks.
	r, err = NewReconciler(ReconcileConfig{
		Snapshot: snapshot, Reconcile: reconcile,
		BackoffInitial: 10 * time.Minute, BackoffMax: time.Minute,
	})
	if err != nil {
		t.Fatalf("NewReconciler: %v", err)
	}
	if r.cfg.BackoffMax != 10*time.Minute {
		t.Fatalf("BackoffMax = %s, want the 10m initial", r.cfg.BackoffMax)
	}

	if _, err := NewReconciler(ReconcileConfig{Reconcile: reconcile}); err == nil {
		t.Fatal("NewReconciler accepted a nil Snapshot")
	}
	if _, err := NewReconciler(ReconcileConfig{Snapshot: snapshot}); err == nil {
		t.Fatal("NewReconciler accepted a nil Reconcile")
	}
}

func TestReconcilerPublishesOnlyOnStateChange(t *testing.T) {
	state := DesiredState{Nodes: []Node{{Tag: "us-ca-01", GatePort: 13001}}}
	var calls int
	r := mustReconciler(t, ReconcileConfig{
		Snapshot:  func() DesiredState { return state },
		Reconcile: func(context.Context) error { calls++; return nil },
	})
	ctx := context.Background()

	if err := r.ReconcileNow(ctx); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	if calls != 1 {
		t.Fatalf("first reconcile calls = %d, want 1", calls)
	}

	if err := r.ReconcileNow(ctx); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if calls != 1 {
		t.Fatalf("unchanged state published again: calls = %d, want 1", calls)
	}

	state = DesiredState{Nodes: []Node{
		{Tag: "us-ca-01", GatePort: 13001},
		{Tag: "jp-13-02", GatePort: 13002},
	}}
	if err := r.ReconcileNow(ctx); err != nil {
		t.Fatalf("changed reconcile: %v", err)
	}
	if calls != 2 {
		t.Fatalf("changed state calls = %d, want 2", calls)
	}
	if r.lastHash != state.Hash() {
		t.Fatalf("lastHash = %q, want the published state hash", r.lastHash)
	}
}

func TestReconcilerRetriesFailureWithoutAdvancingHash(t *testing.T) {
	state := DesiredState{Accounts: []string{"acc-01"}}
	fail := true
	var calls int
	r := mustReconciler(t, ReconcileConfig{
		Snapshot: func() DesiredState { return state },
		Reconcile: func(context.Context) error {
			calls++
			if fail {
				return errors.New("resin unavailable")
			}
			return nil
		},
	})
	ctx := context.Background()

	if err := r.ReconcileNow(ctx); err == nil {
		t.Fatal("failed reconcile returned nil")
	}
	if r.lastHash != "" {
		t.Fatalf("lastHash advanced on failure: %q", r.lastHash)
	}
	if err := r.ReconcileNow(ctx); err == nil {
		t.Fatal("failed reconcile returned nil on retry")
	}
	if calls != 2 {
		t.Fatalf("failed state was not retried: calls = %d, want 2", calls)
	}

	fail = false
	if err := r.ReconcileNow(ctx); err != nil {
		t.Fatalf("recovery reconcile: %v", err)
	}
	if calls != 3 {
		t.Fatalf("recovery calls = %d, want 3", calls)
	}
	if r.lastHash != state.Hash() {
		t.Fatalf("lastHash = %q, want the recovered state hash", r.lastHash)
	}
	if err := r.ReconcileNow(ctx); err != nil {
		t.Fatalf("no-op reconcile: %v", err)
	}
	if calls != 3 {
		t.Fatalf("recovered state published again: calls = %d, want 3", calls)
	}
}

func TestReconcilerBackoffSchedule(t *testing.T) {
	r := mustReconciler(t, ReconcileConfig{
		Snapshot:  func() DesiredState { return DesiredState{} },
		Reconcile: func(context.Context) error { return nil },
	})
	cases := []struct {
		failures int
		want     time.Duration
	}{
		{0, 0},
		{1, time.Minute},
		{2, 2 * time.Minute},
		{3, 4 * time.Minute},
		{4, 8 * time.Minute},
		{5, 15 * time.Minute},
		{6, 15 * time.Minute},
		{100, 15 * time.Minute},
	}
	for _, tc := range cases {
		if got := r.backoff(tc.failures); got != tc.want {
			t.Fatalf("backoff(%d) = %s, want %s", tc.failures, got, tc.want)
		}
	}

	custom := mustReconciler(t, ReconcileConfig{
		Snapshot:       func() DesiredState { return DesiredState{} },
		Reconcile:      func(context.Context) error { return nil },
		BackoffInitial: 2 * time.Second,
		BackoffMax:     5 * time.Second,
	})
	for failures, want := range map[int]time.Duration{
		1: 2 * time.Second, 2: 4 * time.Second, 3: 5 * time.Second, 9: 5 * time.Second,
	} {
		if got := custom.backoff(failures); got != want {
			t.Fatalf("custom backoff(%d) = %s, want %s", failures, got, want)
		}
	}
}

func TestReconcilerAuditsFailureTransitionAndRecovery(t *testing.T) {
	state := DesiredState{Accounts: []string{"acc-01"}}
	fail := true
	var actions, details []string
	r := mustReconciler(t, ReconcileConfig{
		Snapshot: func() DesiredState { return state },
		Reconcile: func(context.Context) error {
			if fail {
				return errors.New("resin down")
			}
			return nil
		},
		Audit: func(action, detail string) {
			actions = append(actions, action)
			details = append(details, detail)
		},
	})
	ctx := context.Background()

	if err := r.ReconcileNow(ctx); err == nil {
		t.Fatal("first reconcile returned nil")
	}
	if err := r.ReconcileNow(ctx); err == nil {
		t.Fatal("retry returned nil")
	}
	if len(actions) != 1 || actions[0] != AuditReconcileFailed {
		t.Fatalf("actions after two failures = %v, want one %q", actions, AuditReconcileFailed)
	}
	if !strings.Contains(details[0], "resin down") || !strings.Contains(details[0], "retry_in=1m0s") {
		t.Fatalf("failure detail = %q, want the error and the 1m retry", details[0])
	}

	fail = false
	if err := r.ReconcileNow(ctx); err != nil {
		t.Fatalf("recovery reconcile: %v", err)
	}
	if len(actions) != 2 || actions[1] != AuditReconcileRecovered {
		t.Fatalf("actions after recovery = %v, want %q", actions, AuditReconcileRecovered)
	}
	if !strings.Contains(details[1], "after 2 failed attempt") {
		t.Fatalf("recovery detail = %q, want the failure count", details[1])
	}

	// A second episode emits a second pair, not a running failure log.
	state = DesiredState{Accounts: []string{"acc-02"}}
	fail = true
	if err := r.ReconcileNow(ctx); err == nil {
		t.Fatal("second episode reconcile returned nil")
	}
	fail = false
	if err := r.ReconcileNow(ctx); err != nil {
		t.Fatalf("second recovery reconcile: %v", err)
	}
	want := []string{AuditReconcileFailed, AuditReconcileRecovered, AuditReconcileFailed, AuditReconcileRecovered}
	if len(actions) != len(want) {
		t.Fatalf("actions = %v, want %v", actions, want)
	}
	for i := range want {
		if actions[i] != want[i] {
			t.Fatalf("actions = %v, want %v", actions, want)
		}
	}
}

func TestReconcilerRevertClosesFailureEpisode(t *testing.T) {
	state := DesiredState{Accounts: []string{"acc-01"}}
	fail := false
	var calls int
	var actions, details []string
	r := mustReconciler(t, ReconcileConfig{
		Snapshot: func() DesiredState { return state },
		Reconcile: func(context.Context) error {
			calls++
			if fail {
				return errors.New("resin down")
			}
			return nil
		},
		Audit: func(action, detail string) {
			actions = append(actions, action)
			details = append(details, detail)
		},
	})
	ctx := context.Background()

	// Publish acc-01 successfully, then fail on acc-02 and revert to acc-01.
	if err := r.ReconcileNow(ctx); err != nil {
		t.Fatalf("baseline reconcile: %v", err)
	}
	fail = true
	state = DesiredState{Accounts: []string{"acc-02"}}
	if err := r.ReconcileNow(ctx); err == nil {
		t.Fatal("failed reconcile returned nil")
	}
	fail = false
	state = DesiredState{Accounts: []string{"acc-01"}}
	if err := r.ReconcileNow(ctx); err != nil {
		t.Fatalf("reverted reconcile: %v", err)
	}
	if calls != 2 {
		t.Fatalf("revert called Reconcile: calls = %d, want 2", calls)
	}
	if len(actions) != 2 || actions[0] != AuditReconcileFailed || actions[1] != AuditReconcileRecovered {
		t.Fatalf("actions = %v, want failed then recovered", actions)
	}
	if !strings.Contains(details[1], "matches the last published hash") {
		t.Fatalf("recovery detail = %q, want the hash-match reason", details[1])
	}
}

func TestReconcilerRunsOnIntervalWithJitteredStart(t *testing.T) {
	wait := newWaitRecorder()
	var calls atomic.Int32
	r := mustReconciler(t, ReconcileConfig{
		Interval: 5 * time.Minute,
		Jitter:   10 * time.Second,
		Snapshot: func() DesiredState {
			return DesiredState{Accounts: []string{"acc-01"}}
		},
		Reconcile: func(context.Context) error { calls.Add(1); return nil },
	})
	r.cfg.wait = wait.wait
	r.cfg.rand = func() float64 { return 0.5 }

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Run(ctx)
	}()

	if got := wait.next(t); got != 5*time.Second {
		t.Fatalf("start delay = %s, want half of the 10s jitter", got)
	}
	wait.resume <- struct{}{}
	if got := wait.next(t); got != 5*time.Minute {
		t.Fatalf("interval wait = %s, want %s", got, 5*time.Minute)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", calls.Load())
	}

	// The next tick sees the same hash and must not publish.
	wait.resume <- struct{}{}
	if got := wait.next(t); got != 5*time.Minute {
		t.Fatalf("second interval wait = %s, want %s", got, 5*time.Minute)
	}
	if calls.Load() != 1 {
		t.Fatalf("unchanged tick published: calls = %d, want 1", calls.Load())
	}

	cancel()
	<-done
}

func TestReconcilerBacksOffAcrossFailures(t *testing.T) {
	wait := newWaitRecorder()
	outcomes := []error{
		errors.New("resin down"),
		errors.New("resin down"),
		errors.New("resin down"),
		errors.New("resin down"),
		errors.New("resin down"),
		nil,
	}
	var calls atomic.Int32
	var actions []string
	r := mustReconciler(t, ReconcileConfig{
		Interval: 5 * time.Minute,
		Snapshot: func() DesiredState {
			return DesiredState{Accounts: []string{"acc-01"}}
		},
		Reconcile: func(context.Context) error {
			err := outcomes[calls.Load()]
			calls.Add(1)
			return err
		},
		Audit: func(action, _ string) { actions = append(actions, action) },
	})
	r.cfg.wait = wait.wait
	r.cfg.rand = func() float64 { return 0 }

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Run(ctx)
	}()

	if got := wait.next(t); got != 0 {
		t.Fatalf("start delay = %s, want 0 with rand=0", got)
	}
	for _, want := range []time.Duration{
		time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 15 * time.Minute,
	} {
		wait.resume <- struct{}{}
		if got := wait.next(t); got != want {
			t.Fatalf("retry wait = %s, want %s", got, want)
		}
	}
	wait.resume <- struct{}{} // the success
	if got := wait.next(t); got != 5*time.Minute {
		t.Fatalf("post-recovery wait = %s, want the 5m interval", got)
	}
	if calls.Load() != 6 {
		t.Fatalf("calls = %d, want 6", calls.Load())
	}

	cancel()
	<-done
	want := []string{AuditReconcileFailed, AuditReconcileRecovered}
	if len(actions) != len(want) {
		t.Fatalf("actions = %v, want %v", actions, want)
	}
	for i := range want {
		if actions[i] != want[i] {
			t.Fatalf("actions = %v, want %v", actions, want)
		}
	}
}

func TestReconcilerStopsOnContextCancel(t *testing.T) {
	wait := newWaitRecorder()
	var calls atomic.Int32
	r := mustReconciler(t, ReconcileConfig{
		Snapshot:  func() DesiredState { return DesiredState{} },
		Reconcile: func(context.Context) error { calls.Add(1); return nil },
	})
	r.cfg.wait = wait.wait

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Run(ctx)
	}()

	wait.next(t)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop on context cancel")
	}
	if calls.Load() != 0 {
		t.Fatalf("calls = %d, want 0", calls.Load())
	}
}

func TestReconcilerCancelDuringReconcileIsNotAFailure(t *testing.T) {
	entered := make(chan struct{})
	var calls, audits atomic.Int32
	r := mustReconciler(t, ReconcileConfig{
		Snapshot: func() DesiredState {
			return DesiredState{Accounts: []string{"acc-01"}}
		},
		Reconcile: func(ctx context.Context) error {
			calls.Add(1)
			close(entered)
			<-ctx.Done()
			return ctx.Err()
		},
		Audit: func(string, string) { audits.Add(1) },
	})
	r.cfg.rand = func() float64 { return 0 } // zero start delay

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Run(ctx)
	}()

	<-entered
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop when the reconcile observed the cancel")
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", calls.Load())
	}
	if audits.Load() != 0 {
		t.Fatalf("cancel emitted %d audit rows, want 0", audits.Load())
	}
}

func TestReconcilerTriggersCoalesceAndNeverOverlap(t *testing.T) {
	wait := newWaitRecorder()
	var seq, calls, active, maxActive atomic.Int32
	release := make(chan struct{})
	entered := make(chan struct{}, 16)

	r := mustReconciler(t, ReconcileConfig{
		Interval: 5 * time.Minute,
		Snapshot: func() DesiredState {
			// A unique hash per call makes every run publish, so the test
			// counts loop iterations instead of hash skips.
			return DesiredState{Accounts: []string{strconv.Itoa(int(seq.Add(1)))}}
		},
		Reconcile: func(ctx context.Context) error {
			n := active.Add(1)
			for {
				max := maxActive.Load()
				if n <= max || maxActive.CompareAndSwap(max, n) {
					break
				}
			}
			calls.Add(1)
			entered <- struct{}{}
			select {
			case <-release:
				active.Add(-1)
				return nil
			case <-ctx.Done():
				active.Add(-1)
				return ctx.Err()
			}
		},
	})
	r.cfg.wait = wait.wait
	r.cfg.rand = func() float64 { return 0 }

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Run(ctx)
	}()

	wait.next(t)
	wait.resume <- struct{}{} // first reconcile starts
	<-entered

	// Ten concurrent triggers while a run is in flight queue exactly one
	// extra run.
	for range 10 {
		r.Trigger()
	}
	release <- struct{}{} // finish the first run
	<-entered             // the coalesced trigger runs

	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2 (one run plus one coalesced trigger)", calls.Load())
	}
	if maxActive.Load() != 1 {
		t.Fatalf("max concurrent reconciles = %d, want 1", maxActive.Load())
	}

	release <- struct{}{} // finish the second run
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
	if calls.Load() != 2 {
		t.Fatalf("calls after shutdown = %d, want 2", calls.Load())
	}
}

func TestReconcilerJitterIsBounded(t *testing.T) {
	r := mustReconciler(t, ReconcileConfig{
		Jitter:    10 * time.Second,
		Snapshot:  func() DesiredState { return DesiredState{} },
		Reconcile: func(context.Context) error { return nil },
	})
	cases := []struct {
		rand float64
		want time.Duration
	}{
		{-1, 0},
		{0, 0},
		{0.25, 2500 * time.Millisecond},
		{0.999, 9990 * time.Millisecond},
		{1, 10 * time.Second},
		{2, 10 * time.Second},
	}
	for _, tc := range cases {
		r.cfg.rand = func() float64 { return tc.rand }
		if got := r.startDelay(); got != tc.want {
			t.Fatalf("startDelay(rand=%v) = %s, want %s", tc.rand, got, tc.want)
		}
	}
}
