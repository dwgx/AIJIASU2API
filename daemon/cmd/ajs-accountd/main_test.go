package main

import (
	"strings"
	"testing"
)

// TestNodeLimitDefaultsToOne pins the live-safe default: the 2026-10-23 live
// run proved the vendor allows one active FV data session per account (a
// second session's gate answers SOCKS5 0x05 with "fv: data channel rejected
// ... fm=disconnected by another connection"), so an unset AJS_NODE_LIMIT
// must not silently open more than one.
func TestNodeLimitDefaultsToOne(t *testing.T) {
	t.Setenv("AJS_NODE_LIMIT", "")
	limit, err := nodeLimit()
	if err != nil {
		t.Fatalf("nodeLimit: %v", err)
	}
	if limit != 1 {
		t.Fatalf("unset AJS_NODE_LIMIT: nodeLimit() = %d, want 1", limit)
	}
}

// TestNodeLimitWhitespaceDefaultsToOne pins that whitespace-only values keep
// the live-safe default, matching the TrimSpace the parser applies.
func TestNodeLimitWhitespaceDefaultsToOne(t *testing.T) {
	t.Setenv("AJS_NODE_LIMIT", "  ")
	limit, err := nodeLimit()
	if err != nil {
		t.Fatalf("nodeLimit: %v", err)
	}
	if limit != 1 {
		t.Fatalf("whitespace AJS_NODE_LIMIT: nodeLimit() = %d, want 1", limit)
	}
}

// TestNodeLimitExplicitValues covers the accepted non-default settings: an
// explicit 0 = all (catalog/probe research, not live-safe) and a positive
// cap.
func TestNodeLimitExplicitValues(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  int
	}{
		{value: "0", want: 0},
		{value: "1", want: 1},
		{value: "3", want: 3},
		{value: " 2 ", want: 2},
	} {
		t.Setenv("AJS_NODE_LIMIT", tc.value)
		limit, err := nodeLimit()
		if err != nil {
			t.Fatalf("AJS_NODE_LIMIT=%q: nodeLimit: %v", tc.value, err)
		}
		if limit != tc.want {
			t.Fatalf("AJS_NODE_LIMIT=%q: nodeLimit() = %d, want %d", tc.value, limit, tc.want)
		}
	}
}

// TestNodeLimitRejectsInvalidValues pins the startup guard: a negative or
// non-integer value fails with the exact AJS_NODE_LIMIT error instead of
// silently lifting the cap.
func TestNodeLimitRejectsInvalidValues(t *testing.T) {
	for _, value := range []string{"-1", "-3", "abc", "1.5", "1e2", "0x2", "1 2", "two"} {
		t.Setenv("AJS_NODE_LIMIT", value)
		limit, err := nodeLimit()
		if err == nil {
			t.Fatalf("AJS_NODE_LIMIT=%q: nodeLimit() = %d, want error", value, limit)
		}
		if limit != 0 {
			t.Fatalf("AJS_NODE_LIMIT=%q: limit = %d, want 0 with the error", value, limit)
		}
		if !strings.HasPrefix(err.Error(), "AJS_NODE_LIMIT ") {
			t.Fatalf("AJS_NODE_LIMIT=%q: error %q, want an AJS_NODE_LIMIT error", value, err)
		}
	}
}
