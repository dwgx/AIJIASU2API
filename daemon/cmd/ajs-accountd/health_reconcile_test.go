package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"ajsaccountd/internal/health"
	"ajsaccountd/internal/runtime"
)

// TestHealthMaxNodesEnv pins AJS_HEALTH_MAX_NODES: empty keeps the default,
// numeric values are clamped into 1..64 and a non-integer fails startup.
func TestHealthMaxNodesEnv(t *testing.T) {
	cases := []struct {
		raw     string
		want    int
		wantErr bool
	}{
		{raw: "", want: 16},
		{raw: "1", want: 1},
		{raw: "8", want: 8},
		{raw: "16", want: 16},
		{raw: "64", want: 64},
		{raw: " 32 ", want: 32},
		{raw: "0", want: 1},
		{raw: "-3", want: 1},
		{raw: "65", want: 64},
		{raw: "many", wantErr: true},
	}
	for _, tc := range cases {
		t.Setenv("AJS_HEALTH_MAX_NODES", tc.raw)
		got, err := healthMaxNodes()
		if tc.wantErr {
			if err == nil {
				t.Fatalf("healthMaxNodes(%q) = %d, want an error", tc.raw, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("healthMaxNodes(%q): %v", tc.raw, err)
		}
		if got != tc.want {
			t.Fatalf("healthMaxNodes(%q) = %d, want %d", tc.raw, got, tc.want)
		}
	}
}

// fakeHealthSource is the live-gate view the reconciler follows in tests.
type fakeHealthSource struct {
	nodes []runtime.NodeRuntime
}

func (f *fakeHealthSource) Snapshot() []runtime.NodeRuntime {
	return append([]runtime.NodeRuntime(nil), f.nodes...)
}

// newHealthTestSampler starts a sampler whose dial blocks until the probe is
// cancelled. No sample is recorded, so lastUsed stays watch-order only and the
// LRU victim of a sweep is deterministic.
func newHealthTestSampler(t *testing.T, maxNodes int) *health.Sampler {
	t.Helper()
	sampler := health.New(health.Config{
		MaxNodes: maxNodes,
		Interval: time.Hour,
		Timeout:  time.Hour,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	})
	sampler.Start(context.Background())
	t.Cleanup(sampler.Stop)
	return sampler
}

func evictedKeys(tracker *healthTracker, keys ...string) []string {
	evicted := make([]string, 0, 1)
	for _, key := range keys {
		if tracker.Evicted(key) {
			evicted = append(evicted, key)
		}
	}
	return evicted
}

// TestReconcileHealthWatchesHealsEvictedKey pins the P2-4 fix with the repro
// from the review: three live gates and a two-ring cap. One gate's window is
// evicted per sweep, and the next sweep must re-arm it within that tick.
func TestReconcileHealthWatchesHealsEvictedKey(t *testing.T) {
	sampler := newHealthTestSampler(t, 2)
	tracker := newHealthTracker(sampler)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	source := &fakeHealthSource{nodes: []runtime.NodeRuntime{
		{NodeID: "a", Port: 1001},
		{NodeID: "b", Port: 1002},
		{NodeID: "c", Port: 1003},
	}}

	// The first sweep watches all three gates; the cap keeps two rings and
	// records the third key as evicted.
	reconcileHealthWatches(source, tracker, logger)
	evicted := evictedKeys(tracker, "a", "b", "c")
	if len(evicted) != 1 {
		t.Fatalf("evicted keys = %v, want exactly one at MaxNodes 2 with three gates", evicted)
	}
	if watched := tracker.snapshot(); len(watched) != 3 {
		t.Fatalf("tracker watches %d keys, want 3 (the watch set is independent of the cap)", len(watched))
	}

	// One reconcile tick later the evicted gate is probed again.
	reconcileHealthWatches(source, tracker, logger)
	if tracker.Evicted(evicted[0]) {
		t.Fatalf("key %q is still evicted after a reconcile tick", evicted[0])
	}
	if _, ok := sampler.Stats(evicted[0]); !ok {
		t.Fatalf("key %q has no retained window after the heal", evicted[0])
	}
	if again := evictedKeys(tracker, "a", "b", "c"); len(again) != 1 {
		t.Fatalf("evicted keys after the heal = %v, want exactly one", again)
	}
}

// TestReconcileHealthWatchesThrottlesRearmLog pins the log contract: a heal is
// logged at most once per minute even though the sweep runs every 15s.
func TestReconcileHealthWatchesThrottlesRearmLog(t *testing.T) {
	sampler := newHealthTestSampler(t, 1)
	tracker := newHealthTracker(sampler)
	clock := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	tracker.now = func() time.Time { return clock }
	var log bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&log, nil))
	source := &fakeHealthSource{nodes: []runtime.NodeRuntime{
		{NodeID: "a", Port: 1001},
		{NodeID: "b", Port: 1002},
	}}

	// Sweep 1 only watches; the second gate evicts the first, and there is
	// nothing to re-arm yet.
	reconcileHealthWatches(source, tracker, logger)
	if strings.Contains(log.String(), "health_watch_rearmed") {
		t.Fatalf("first sweep logged a re-arm: %s", log.String())
	}
	// Sweep 2 heals the evicted key and logs.
	reconcileHealthWatches(source, tracker, logger)
	if got := strings.Count(log.String(), "health_watch_rearmed"); got != 1 {
		t.Fatalf("re-arm log lines after sweep 2 = %d, want 1: %s", got, log.String())
	}
	// Sweep 3 heals the newly evicted key but stays inside the throttle.
	reconcileHealthWatches(source, tracker, logger)
	if got := strings.Count(log.String(), "health_watch_rearmed"); got != 1 {
		t.Fatalf("re-arm log lines inside one minute = %d, want 1: %s", got, log.String())
	}
	// A minute later the next heal is logged again.
	clock = clock.Add(healthRearmLogInterval + time.Second)
	reconcileHealthWatches(source, tracker, logger)
	if got := strings.Count(log.String(), "health_watch_rearmed"); got != 2 {
		t.Fatalf("re-arm log lines after a minute = %d, want 2: %s", got, log.String())
	}
}

// TestReconcileHealthWatchesLogsRearmFailure keeps a stopped sampler from
// swallowing the failure: the sweep reports it instead of pretending the heal
// happened.
func TestReconcileHealthWatchesLogsRearmFailure(t *testing.T) {
	sampler := newHealthTestSampler(t, 1)
	tracker := newHealthTracker(sampler)
	source := &fakeHealthSource{nodes: []runtime.NodeRuntime{
		{NodeID: "a", Port: 1001},
		{NodeID: "b", Port: 1002},
	}}
	reconcileHealthWatches(source, tracker, nil)
	if evicted := evictedKeys(tracker, "a", "b"); len(evicted) != 1 {
		t.Fatalf("evicted keys = %v, want exactly one", evicted)
	}
	sampler.Stop()

	var log bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&log, nil))
	reconcileHealthWatches(source, tracker, logger)
	if !strings.Contains(log.String(), "health_watch_rearm_failed") {
		t.Fatalf("stopped sampler produced no failure line: %s", log.String())
	}
}

// TestReconcileHealthWatchesUnwatchesStoppedGates keeps the original sweep
// contract: a gate the controller no longer runs is unwatched, and a nil
// logger is allowed.
func TestReconcileHealthWatchesUnwatchesStoppedGates(t *testing.T) {
	sampler := newHealthTestSampler(t, 4)
	tracker := newHealthTracker(sampler)
	source := &fakeHealthSource{nodes: []runtime.NodeRuntime{
		{NodeID: "a", Port: 1001},
		{NodeID: "b", Port: 1002},
	}}
	reconcileHealthWatches(source, tracker, nil)
	if got := len(tracker.snapshot()); got != 2 {
		t.Fatalf("watched keys = %d, want 2", got)
	}

	source.nodes = []runtime.NodeRuntime{{NodeID: "a", Port: 1001}}
	reconcileHealthWatches(source, tracker, nil)
	watched := tracker.snapshot()
	if _, ok := watched["b"]; ok {
		t.Fatal("gate b is still watched after the controller stopped it")
	}
	if _, ok := watched["a"]; !ok {
		t.Fatal("gate a was unwatched although it still runs")
	}
}
