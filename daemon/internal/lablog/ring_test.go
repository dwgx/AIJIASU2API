package lablog

import (
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestRingRetainsBoundedWindow(t *testing.T) {
	ring := NewRing(2)
	logger := slog.New(ring.Handler(nil))
	logger.Info("gate one", "node_id", "n1")
	logger.Info("gate two", "node_id", "n2")
	logger.Info("gate three", "node_id", "n3")

	entries, dropped := ring.Entries(time.Time{}, slog.LevelDebug, "", 10)
	if len(entries) != 2 {
		t.Fatalf("retained %d entries, want 2", len(entries))
	}
	if dropped != 1 {
		t.Fatalf("dropped = %d, want 1", dropped)
	}
	if entries[0].NodeID != "n2" || entries[1].NodeID != "n3" {
		t.Fatalf("retained nodes = %q, %q; want n2, n3", entries[0].NodeID, entries[1].NodeID)
	}
	if ring.Len() != 2 || ring.Capacity() != 2 {
		t.Fatalf("Len/Capacity = %d/%d, want 2/2", ring.Len(), ring.Capacity())
	}
}

func TestRingDropsNonAllowlistedAttributes(t *testing.T) {
	ring := NewRing(10)
	logger := slog.New(ring.Handler(nil))
	logger.Warn("gate flow rejected",
		"node_id", "acc-01:srv-07",
		"account_id", "acc-01",
		"server_id", "srv-07",
		"error_class", "vendor_rejected",
		"duration", 1500*time.Millisecond,
		"status", 502,
		"bytes", int64(99),
		"password", "hunter2",
		"token", "session-token",
		"authorization", "Bearer abc",
		"target", "example.com:443",
		"error", errors.New("vendor said secret=abc"),
	)
	logger.Warn("node_session_failed", "node_id", "n2",
		"error_class", errors.New("vendor error text must not be stored"))

	entries, _ := ring.Entries(time.Time{}, slog.LevelDebug, "", 10)
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	if entries[1].ErrorClass != "" {
		t.Fatalf("non-string error_class was stored: %q", entries[1].ErrorClass)
	}
	entry := entries[0]
	if entry.NodeID != "acc-01:srv-07" || entry.AccountID != "acc-01" ||
		entry.ServerID != "srv-07" || entry.ErrorClass != "vendor_rejected" {
		t.Fatalf("allowlisted attrs missing: %+v", entry)
	}
	if entry.DurationMS == nil || *entry.DurationMS != 1500 {
		t.Fatalf("duration_ms = %v, want 1500", entry.DurationMS)
	}
	if entry.Status == nil || *entry.Status != 502 {
		t.Fatalf("status = %v, want 502", entry.Status)
	}
	if entry.Bytes == nil || *entry.Bytes != 99 {
		t.Fatalf("bytes = %v, want 99", entry.Bytes)
	}

	// The retained entry cannot carry a dropped attribute because Entry has
	// no field for one; assert the rendered form too, so a future field
	// addition cannot silently re-introduce one.
	rendered := strings.ToLower(entry.Message + " " + entry.AccountID + " " + entry.NodeID + " " +
		entry.ServerID + " " + entry.ErrorClass)
	for _, forbidden := range []string{"hunter2", "session-token", "bearer", "example.com", "secret=abc"} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("entry contains dropped value %q: %q", forbidden, rendered)
		}
	}
}

func TestRingFilters(t *testing.T) {
	ring := NewRing(10)
	logger := slog.New(ring.Handler(nil))
	logger.Debug("gate debug", "node_id", "n0")
	logger.Info("gate info", "node_id", "n1")
	logger.Warn("account_login_rejected", "account_id", "a1")
	logger.Error("node_session_failed", "node_id", "n2")

	onlyWarn, _ := ring.Entries(time.Time{}, slog.LevelWarn, "", 10)
	if len(onlyWarn) != 2 || onlyWarn[0].Message != "account_login_rejected" || onlyWarn[1].Message != "node_session_failed" {
		t.Fatalf("warn filter = %+v", onlyWarn)
	}
	gateOnly, _ := ring.Entries(time.Time{}, slog.LevelDebug, "gate", 10)
	if len(gateOnly) != 2 || gateOnly[0].Message != "gate debug" || gateOnly[1].Message != "gate info" {
		t.Fatalf("component filter = %+v", gateOnly)
	}
	lastGate, _ := ring.Entries(time.Time{}, slog.LevelDebug, "gate", 1)
	if len(lastGate) != 1 || lastGate[0].Message != "gate info" {
		t.Fatalf("limit filter = %+v", lastGate)
	}
	since := time.Now().Add(time.Hour)
	if future, _ := ring.Entries(since, slog.LevelDebug, "", 10); len(future) != 0 {
		t.Fatalf("since filter = %+v, want none", future)
	}
}

func TestRingForwardsToNextHandler(t *testing.T) {
	ring := NewRing(10)
	var lines strings.Builder
	next := slog.NewTextHandler(&lines, &slog.HandlerOptions{Level: slog.LevelWarn})
	logger := slog.New(ring.Handler(next))
	logger.Info("gate info")
	logger.Warn("gate warn")

	if got := ring.Len(); got != 2 {
		t.Fatalf("ring retained %d entries, want 2 (info must still be recorded)", got)
	}
	if strings.Contains(lines.String(), "gate info") {
		t.Fatalf("console handler ignored its level filter: %q", lines.String())
	}
	if !strings.Contains(lines.String(), "gate warn") {
		t.Fatalf("console handler missed the warn line: %q", lines.String())
	}
}

func TestRingWithAttrsAndGroups(t *testing.T) {
	ring := NewRing(10)
	logger := slog.New(ring.Handler(nil)).With("account_id", "acc-01")
	logger.WithGroup("flow").Info("gate flow closed", "node_id", "n1", "bytes", 7)

	entries, _ := ring.Entries(time.Time{}, slog.LevelDebug, "gate", 10)
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if entries[0].AccountID != "acc-01" || entries[0].NodeID != "n1" {
		t.Fatalf("grouped attrs missing: %+v", entries[0])
	}
	if entries[0].Bytes == nil || *entries[0].Bytes != 7 {
		t.Fatalf("bytes = %v, want 7", entries[0].Bytes)
	}
}

func TestComponentOf(t *testing.T) {
	cases := map[string]string{
		"gate listening":       "gate",
		"account_login":        "account",
		"http_request":         "http",
		"node_started":         "node",
		"resin request failed": "resin",
		"single":               "single",
		"":                     "",
	}
	for message, want := range cases {
		if got := componentOf(message); got != want {
			t.Fatalf("componentOf(%q) = %q, want %q", message, got, want)
		}
	}
}
