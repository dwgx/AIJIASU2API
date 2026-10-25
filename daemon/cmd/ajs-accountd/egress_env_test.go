package main

import (
	"testing"
	"time"

	"ajsaccountd/internal/health"
)

// TestEgressProbeEnabled pins the off-by-default contract: only the exact
// value "1" opts into real egress through the vendor session.
func TestEgressProbeEnabled(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"", false},
		{"0", false},
		{"true", false},
		{"yes", false},
		{" 1 ", true},
		{"1", true},
	}
	for _, tc := range cases {
		t.Setenv("AJS_EGRESS_PROBE", tc.raw)
		if got := egressProbeEnabled(); got != tc.want {
			t.Fatalf("egressProbeEnabled(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

// TestEgressConfig pins the interval and target knobs. An empty interval keeps
// the sampler default; a malformed or sub-10s one fails startup because every
// sample is a vendor flow.
func TestEgressConfig(t *testing.T) {
	t.Setenv("AJS_EGRESS_INTERVAL", "")
	t.Setenv("AJS_EGRESS_TARGET", "")
	cfg, err := egressConfig()
	if err != nil {
		t.Fatalf("empty config: %v", err)
	}
	if cfg.Interval != health.DefaultEgressInterval || cfg.Timeout != health.DefaultEgressTimeout {
		t.Fatalf("empty config = %+v, want the effective sampler defaults", cfg)
	}
	if cfg.Target != "" {
		t.Fatalf("empty target = %q, want the sampler default", cfg.Target)
	}

	t.Setenv("AJS_EGRESS_INTERVAL", "90s")
	t.Setenv("AJS_EGRESS_TARGET", "https://example.test/generate_204")
	cfg, err = egressConfig()
	if err != nil {
		t.Fatalf("explicit config: %v", err)
	}
	if cfg.Interval != 90*time.Second || cfg.Target != "https://example.test/generate_204" {
		t.Fatalf("explicit config = %+v", cfg)
	}

	for _, raw := range []string{"nope", "5s", "-1m"} {
		t.Setenv("AJS_EGRESS_INTERVAL", raw)
		if _, err := egressConfig(); err == nil {
			t.Fatalf("egressConfig accepted interval %q", raw)
		}
	}

	t.Setenv("AJS_EGRESS_INTERVAL", "")
	t.Setenv("AJS_EGRESS_TARGET", "not-a-url")
	cfg, err = egressConfig()
	if err != nil {
		t.Fatalf("egressConfig rejects a target before the sampler validates it: %v", err)
	}
	if _, err := health.NewEgressSampler(cfg); err == nil {
		t.Fatal("NewEgressSampler accepted an invalid AJS_EGRESS_TARGET")
	}
}

// TestEgressLogTarget keeps a URL-shaped credential out of the startup log.
func TestEgressLogTarget(t *testing.T) {
	if got := egressLogTarget(""); got != health.DefaultEgressTarget {
		t.Fatalf("empty target log = %q, want the default", got)
	}
	if got := egressLogTarget("https://example.test/generate_204?token=secret#frag"); got != "https://example.test/generate_204" {
		t.Fatalf("query/fragment leaked into the log target: %q", got)
	}
	if got := egressLogTarget("https://user:pass@example.test/x"); got != "https://example.test/x" {
		t.Fatalf("userinfo leaked into the log target: %q", got)
	}
}

// TestHealthTrackerDrivesEgress keeps the two samplers on one watch set: the
// API only calls Watch/Unwatch once, and both windows follow the live gate.
func TestHealthTrackerDrivesEgress(t *testing.T) {
	sampler := health.New(health.Config{})
	egress, err := health.NewEgressSampler(health.EgressConfig{})
	if err != nil {
		t.Fatalf("new egress sampler: %v", err)
	}
	tracker := newHealthTracker(sampler, egress)
	if !tracker.EgressEnabled() {
		t.Fatal("tracker with an egress sampler reports disabled")
	}
	if err := tracker.Watch("acct-01:s1", 13000); err != nil {
		t.Fatalf("watch: %v", err)
	}
	if _, ok := tracker.Stats("acct-01:s1"); !ok {
		t.Fatal("loopback window missing after watch")
	}
	if stats, ok := tracker.EgressStats("acct-01:s1"); !ok || stats.Count != 0 {
		t.Fatalf("egress stats = %+v/%v, want an empty watched window", stats, ok)
	}
	tracker.Unwatch("acct-01:s1")
	if _, ok := tracker.EgressStats("acct-01:s1"); ok {
		t.Fatal("egress window survived unwatch")
	}

	plain := newHealthTracker(sampler, nil)
	if plain.EgressEnabled() {
		t.Fatal("tracker without an egress sampler reports enabled")
	}
	if _, ok := plain.EgressStats("acct-01:s1"); ok {
		t.Fatal("disabled tracker returned egress stats")
	}
}
