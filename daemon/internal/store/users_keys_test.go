package store

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"ajsaccountd/internal/model"
)

// mustCreateUser creates one panel user in an empty store and fails the test on
// error. The store has no sealer, so no credential is involved.
func mustCreateUser(t *testing.T, st *Store, name string) model.PanelUser {
	t.Helper()
	user, err := st.CreateUser(model.CreateUserInput{Name: name})
	if err != nil {
		t.Fatalf("CreateUser(%q): %v", name, err)
	}
	return user
}

// TestNormalizeAllowlistAcceptsDestinationRules pins the accepted vocabulary:
// an exact hostname (lowercased, one terminal dot trimmed), a `*.domain`
// wildcard, an IP literal (IPv4-mapped IPv6 unmapped) and a CIDR prefix
// (masked). Two spellings of one rule collapse into one entry, and normalizing
// an already-normalized list changes nothing.
func TestNormalizeAllowlistAcceptsDestinationRules(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{"empty list", []string{}, []string{}},
		{"exact host", []string{"example.com"}, []string{"example.com"}},
		{"host is lowercased", []string{"API.Example.COM"}, []string{"api.example.com"}},
		{"one terminal dot trimmed", []string{"api.example.com."}, []string{"api.example.com"}},
		{"surrounding space trimmed", []string{"  spaced.example.com  "}, []string{"spaced.example.com"}},
		{"underscore label", []string{"_dmarc.example.com"}, []string{"_dmarc.example.com"}},
		{"wildcard", []string{"*.Example.COM."}, []string{"*.example.com"}},
		{"ipv4", []string{"192.168.1.5"}, []string{"192.168.1.5"}},
		{"ipv4-mapped ipv6 unmapped", []string{"::FFFF:192.168.1.5"}, []string{"192.168.1.5"}},
		{"ipv6 lowercased", []string{"2001:DB8::1"}, []string{"2001:db8::1"}},
		{"cidr masked", []string{"10.20.5.7/16"}, []string{"10.20.0.0/16"}},
		{"ipv6 cidr", []string{"2001:db8::/32"}, []string{"2001:db8::/32"}},
		{"ipv4-mapped cidr folded", []string{"::ffff:10.0.0.0/104"}, []string{"10.0.0.0/8"}},
		{"duplicate spellings collapse", []string{"example.com", "EXAMPLE.com.", "example.com"}, []string{"example.com"}},
		{"duplicate prefixes collapse", []string{"10.0.0.0/8", "10.1.2.3/8"}, []string{"10.0.0.0/8"}},
		{"host and its /32 stay distinct", []string{"127.0.0.1", "127.0.0.1/32"}, []string{"127.0.0.1", "127.0.0.1/32"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeAllowlist(tc.in)
			if err != nil {
				t.Fatalf("normalizeAllowlist(%v): %v", tc.in, err)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("normalizeAllowlist(%v) = %v, want %v", tc.in, got, tc.want)
			}
			// Normalizing is idempotent: the stored list is already canonical,
			// so a rewrite of the same rules is a no-op.
			again, err := normalizeAllowlist(got)
			if err != nil {
				t.Fatalf("normalizeAllowlist(%v) again: %v", got, err)
			}
			if !slices.Equal(again, got) {
				t.Fatalf("normalizeAllowlist is not idempotent: %v -> %v", got, again)
			}
		})
	}
}

// TestNormalizeAllowlistRejectsNonRules covers the entries a client must learn
// about at write time. Each one would otherwise either widen the policy (a bare
// `*`) or be rejected by the gate as uncompilable, which denies every
// destination for that user instead of just the bad entry.
func TestNormalizeAllowlistRejectsNonRules(t *testing.T) {
	tests := []struct {
		name  string
		entry string
	}{
		{"empty", ""},
		{"blank", "   "},
		{"bare wildcard", "*"},
		{"wildcard over a single label", "*.com"},
		{"wildcard without a domain", "*."},
		{"wildcard without a dot", "*example.com"},
		{"double wildcard", "**.example.com"},
		{"wildcard in the middle", "api.*.com"},
		{"scheme", "http://example.com"},
		{"scheme with path", "https://example.com/path"},
		{"path", "example.com/path"},
		{"port", "example.com:443"},
		{"userinfo", "user@example.com"},
		{"space inside", "exa mple.com"},
		{"bracketed ipv6", "[::1]"},
		{"ipv6 zone", "fe80::1%eth0"},
		{"prefix too long", "10.0.0.0/33"},
		{"two prefixes", "10.0.0.0/8/8"},
		{"empty label", "example..com"},
		{"label too long", strings.Repeat("a", 64) + ".com"},
		{"terminal dots", "example.com.."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := normalizeAllowlist([]string{tc.entry}); !errors.Is(err, ErrInvalid) {
				t.Fatalf("normalizeAllowlist(%q) = %v, %v; want ErrInvalid", tc.entry, got, err)
			}
		})
	}
}

// TestNormalizeAllowlistCaps pins the two bounds: at most 64 entries and at
// most 253 bytes per entry (the DNS name limit).
func TestNormalizeAllowlistCaps(t *testing.T) {
	// A syntactically valid 253-byte name: 63.63.63.61.
	label := strings.Repeat("a", 63)
	host253 := strings.Join([]string{label, label, label, strings.Repeat("b", 61)}, ".")
	if len(host253) != 253 {
		t.Fatalf("test fixture length = %d, want 253", len(host253))
	}
	if got, err := normalizeAllowlist([]string{host253}); err != nil || len(got) != 1 {
		t.Fatalf("253-byte host rejected: %v, %v", got, err)
	}
	if _, err := normalizeAllowlist([]string{host253 + "c"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("254-byte host accepted: %v", err)
	}

	entries := make([]string, 0, maxAllowlistEntries+1)
	for i := range maxAllowlistEntries {
		entries = append(entries, fmt.Sprintf("host-%02d.example.com", i))
	}
	if got, err := normalizeAllowlist(entries); err != nil || len(got) != maxAllowlistEntries {
		t.Fatalf("normalizeAllowlist(%d entries) = %d entries, %v; want %d, nil",
			len(entries), len(got), err, maxAllowlistEntries)
	}
	if _, err := normalizeAllowlist(append(entries, "extra.example.com")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("allowlist with %d entries accepted: %v", maxAllowlistEntries+1, err)
	}
}

// TestUserAllowlistWritePathNormalizes proves every writer of a user's
// allowlist goes through the same normalization, refuses a malformed entry
// without touching the stored list, and keeps the loopback default when the
// create body omits the field.
func TestUserAllowlistWritePathNormalizes(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	st := newEmptyWithClock(func() time.Time { return now }, nil)

	user, err := st.CreateUser(model.CreateUserInput{
		Name:      "edge-bot",
		Allowlist: []string{"API.Example.COM.", "*.example.com", "10.1.2.3/8", "api.example.com"},
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	want := []string{"api.example.com", "*.example.com", "10.0.0.0/8"}
	if !slices.Equal(user.Allowlist, want) {
		t.Fatalf("created allowlist = %v, want %v", user.Allowlist, want)
	}

	patch := []string{"192.168.1.5"}
	updated, err := st.UpdateUser(user.ID, model.UserPatch{Allowlist: &patch})
	if err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	if !slices.Equal(updated.Allowlist, []string{"192.168.1.5"}) {
		t.Fatalf("updated allowlist = %v, want [192.168.1.5]", updated.Allowlist)
	}

	replaced, err := st.SetUserAllowlist(user.ID, []string{"Example.COM", "example.com."})
	if err != nil {
		t.Fatalf("SetUserAllowlist: %v", err)
	}
	if !slices.Equal(replaced.Allowlist, []string{"example.com"}) {
		t.Fatalf("replaced allowlist = %v, want [example.com]", replaced.Allowlist)
	}

	// A malformed entry is refused and the stored list is unchanged.
	if _, err := st.SetUserAllowlist(user.ID, []string{"https://example.com"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("SetUserAllowlist(URL) = %v, want ErrInvalid", err)
	}
	stored, ok := st.User(user.ID)
	if !ok || !slices.Equal(stored.Allowlist, []string{"example.com"}) {
		t.Fatalf("stored allowlist after a refused write = %v (ok=%v), want [example.com]", stored.Allowlist, ok)
	}
	if _, err := st.UpdateUser(user.ID, model.UserPatch{Allowlist: &[]string{"example.com:443"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("UpdateUser(port) = %v, want ErrInvalid", err)
	}

	// An omitted allowlist keeps the loopback-only default.
	fresh, err := st.CreateUser(model.CreateUserInput{Name: "default-bot"})
	if err != nil {
		t.Fatalf("CreateUser(default): %v", err)
	}
	if !slices.Equal(fresh.Allowlist, []string{"127.0.0.1/32"}) {
		t.Fatalf("default allowlist = %v, want [127.0.0.1/32]", fresh.Allowlist)
	}
}
