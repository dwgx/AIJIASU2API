package account

import (
	"strings"
	"testing"
)

func TestNormalizeUseCount(t *testing.T) {
	cases := []struct {
		name string
		in   int
		want int
	}{
		{name: "absent zero defaults to one", in: 0, want: 1},
		{name: "negative defaults to one", in: -7, want: 1},
		{name: "one stays one", in: 1, want: 1},
		{name: "purchased value is preserved", in: 37, want: 37},
		{name: "published maximum stays", in: MaxAccountSlots, want: MaxAccountSlots},
		{name: "above maximum clamps", in: MaxAccountSlots + 1, want: MaxAccountSlots},
		{name: "large value clamps", in: 100_000, want: MaxAccountSlots},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeUseCount(tc.in); got != tc.want {
				t.Fatalf("NormalizeUseCount(%d) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseAccountSlots(t *testing.T) {
	valid := []struct {
		raw  string
		want int
	}{
		{raw: "", want: DefaultAccountSlots},
		{raw: "   ", want: DefaultAccountSlots},
		{raw: "1", want: 1},
		{raw: "2", want: 2},
		{raw: "2000", want: 2000},
		{raw: " 7 ", want: 7},
	}
	for _, tc := range valid {
		got, err := ParseAccountSlots(tc.raw)
		if err != nil {
			t.Fatalf("ParseAccountSlots(%q): %v", tc.raw, err)
		}
		if got != tc.want {
			t.Fatalf("ParseAccountSlots(%q) = %d, want %d", tc.raw, got, tc.want)
		}
	}

	invalid := []string{"0", "-1", "2001", "abc", "1.5", "1 2"}
	for _, raw := range invalid {
		if _, err := ParseAccountSlots(raw); err == nil {
			t.Fatalf("ParseAccountSlots(%q) accepted an invalid value", raw)
		} else if !strings.Contains(err.Error(), AccountSlotsEnv) {
			t.Fatalf("ParseAccountSlots(%q) error %q does not name %s", raw, err, AccountSlotsEnv)
		}
	}
}

func TestEffectiveSlots(t *testing.T) {
	cases := []struct {
		name       string
		useCount   int
		configured int
		want       int
	}{
		{name: "absent use count and unset cap mean one", useCount: 0, configured: 0, want: 1},
		{name: "operator default keeps one", useCount: 8, configured: 1, want: 1},
		{name: "operator cap below use count wins", useCount: 8, configured: 2, want: 2},
		{name: "use count below operator cap wins", useCount: 1, configured: 8, want: 1},
		{name: "both equal", useCount: 4, configured: 4, want: 4},
		{name: "use count clamps at 2000", useCount: 5000, configured: 5000, want: 2000},
		{name: "negative values normalize", useCount: -3, configured: -2, want: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EffectiveSlots(tc.useCount, tc.configured); got != tc.want {
				t.Fatalf("EffectiveSlots(%d, %d) = %d, want %d",
					tc.useCount, tc.configured, got, tc.want)
			}
		})
	}
}

func TestConfiguredAccountSlots(t *testing.T) {
	t.Setenv(AccountSlotsEnv, "3")
	if got := ConfiguredAccountSlots(); got != 3 {
		t.Fatalf("ConfiguredAccountSlots() = %d, want 3", got)
	}
	t.Setenv(AccountSlotsEnv, "not-a-number")
	if got := ConfiguredAccountSlots(); got != DefaultAccountSlots {
		t.Fatalf("invalid ConfiguredAccountSlots() = %d, want %d", got, DefaultAccountSlots)
	}
}

func TestMultiSessionWarningFor(t *testing.T) {
	if got := MultiSessionWarningFor(1); got != "" {
		t.Fatalf("MultiSessionWarningFor(1) = %q, want empty", got)
	}
	if got := MultiSessionWarningFor(2); got != MultiSessionWarning {
		t.Fatalf("MultiSessionWarningFor(2) = %q, want %q", got, MultiSessionWarning)
	}
}
