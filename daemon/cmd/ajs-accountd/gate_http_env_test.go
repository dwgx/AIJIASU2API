package main

import (
	"testing"

	"ajsaccountd/internal/runtime"
)

// TestGateHTTPEnabled pins AJS_GATE_HTTP: the listener is opt-in, "1" enables
// it, unset/"0" keeps it off and any other value aborts startup.
func TestGateHTTPEnabled(t *testing.T) {
	cases := []struct {
		raw     string
		want    bool
		wantErr bool
	}{
		{"", false, false},
		{"0", false, false},
		{" 0 ", false, false},
		{"1", true, false},
		{" 1 ", true, false},
		{"true", false, true},
		{"yes", false, true},
		{"2", false, true},
	}
	for _, tc := range cases {
		got, err := gateHTTPEnabled(tc.raw)
		if (err != nil) != tc.wantErr {
			t.Fatalf("gateHTTPEnabled(%q) error = %v, wantErr %v", tc.raw, err, tc.wantErr)
		}
		if err == nil && got != tc.want {
			t.Fatalf("gateHTTPEnabled(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

// TestGateHTTPOffset pins AJS_GATE_HTTP_OFFSET: unset selects the runtime
// default, an explicit value is used as-is and an unusable value aborts
// startup instead of binding both listeners to one port.
func TestGateHTTPOffset(t *testing.T) {
	cases := []struct {
		raw     string
		want    int
		wantErr bool
	}{
		{"", runtime.DefaultGateHTTPOffset, false},
		{"1000", 1000, false},
		{" 2000 ", 2000, false},
		{"65534", 65534, false},
		{"0", 0, true},
		{"-1", 0, true},
		{"65535", 0, true},
		{"65536", 0, true},
		{"ten", 0, true},
	}
	for _, tc := range cases {
		got, err := gateHTTPOffset(tc.raw)
		if (err != nil) != tc.wantErr {
			t.Fatalf("gateHTTPOffset(%q) error = %v, wantErr %v", tc.raw, err, tc.wantErr)
		}
		if err == nil && got != tc.want {
			t.Fatalf("gateHTTPOffset(%q) = %d, want %d", tc.raw, got, tc.want)
		}
	}
}
