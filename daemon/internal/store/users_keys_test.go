package store

import (
	"encoding/json"
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

// TestPanelUserUsedBytesDerivedFromGateRows pins the UsedBytes contract: the
// figure is the current UTC month's gate-metered bytes for that user, derived
// on read. Proxy-sourced rows, other users' rows, loopback rows without a user
// id, rows before the month boundary and unparseable rows never count, an
// append is visible to the next read, and a UTC month rollover resets the
// figure before rebuilding it when the clock returns.
func TestPanelUserUsedBytesDerivedFromGateRows(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	st := newEmptyWithClock(func() time.Time { return now }, nil)
	user := mustCreateUser(t, st, "usage-bot")
	other := mustCreateUser(t, st, "other-bot")

	// AppendRequest prepends, so appending oldest first keeps the log
	// newest-first exactly as production does.
	rows := []model.RequestLog{
		{TS: "2026-09-30T23:59:59Z", UserID: user.ID, Source: model.SourceGate, Bytes: 500},
		{TS: "2026-10-01T00:00:00Z", UserID: user.ID, Source: model.SourceGate, Bytes: 100},
		{TS: "2026-10-02T08:00:00Z", UserID: user.ID, Source: model.SourceGate, Bytes: 200},
		{TS: "2026-10-03T08:00:00Z", UserID: user.ID, Source: model.SourceProxy, Bytes: 900},
		{TS: "2026-10-04T08:00:00Z", UserID: other.ID, Source: model.SourceGate, Bytes: 400},
		{TS: "2026-10-05T08:00:00Z", Source: model.SourceGate, Bytes: 700},
		{TS: "not-a-timestamp", UserID: user.ID, Source: model.SourceGate, Bytes: 7},
	}
	for _, row := range rows {
		st.AppendRequest(row)
	}

	const want = 300 // 100 at the boundary + 200 on 2026-10-02
	got, ok := st.User(user.ID)
	if !ok || got.UsedBytes != want {
		t.Fatalf("User(%s).UsedBytes = %d (ok=%v), want %d", user.ID, got.UsedBytes, ok, want)
	}
	for _, listed := range st.Users() {
		if listed.ID != user.ID {
			continue
		}
		if listed.UsedBytes != want {
			t.Fatalf("Users() usedBytes = %d, want %d (list and single read must agree)", listed.UsedBytes, want)
		}
	}

	// A later append is visible to the next read without a rescan.
	st.AppendRequest(model.RequestLog{TS: "2026-10-16T09:00:00Z", UserID: user.ID, Source: model.SourceGate, Bytes: 50})
	if got, _ := st.User(user.ID); got.UsedBytes != want+50 {
		t.Fatalf("usedBytes after append = %d, want %d", got.UsedBytes, want+50)
	}

	// A UTC month rollover resets the figure even though the rows are retained.
	now = time.Date(2026, 11, 1, 0, 0, 1, 0, time.UTC)
	if got, _ := st.User(user.ID); got.UsedBytes != 0 {
		t.Fatalf("usedBytes after rollover = %d, want 0", got.UsedBytes)
	}
	// Moving the clock back rebuilds the same month from the rows.
	now = time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	if got, _ := st.User(user.ID); got.UsedBytes != want+50 {
		t.Fatalf("usedBytes after returning to October = %d, want %d", got.UsedBytes, want+50)
	}

	// A deleted user's total is forgotten.
	if err := st.DeleteUser(user.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if _, ok := st.User(user.ID); ok {
		t.Fatal("deleted user is still readable")
	}
}

// TestAppendRequestAfterRolloverDoesNotDoubleCount is the ordering regression:
// the derived totals are advanced before the row is prepended, so a rebuild
// triggered by the rollover cannot already contain the row it then adds.
func TestAppendRequestAfterRolloverDoesNotDoubleCount(t *testing.T) {
	now := time.Date(2026, 10, 31, 23, 0, 0, 0, time.UTC)
	st := newEmptyWithClock(func() time.Time { return now }, nil)
	user := mustCreateUser(t, st, "rollover-bot")

	st.AppendRequest(model.RequestLog{TS: "2026-10-31T22:00:00Z", UserID: user.ID, Source: model.SourceGate, Bytes: 500})
	if got, _ := st.User(user.ID); got.UsedBytes != 500 {
		t.Fatalf("October usedBytes = %d, want 500", got.UsedBytes)
	}

	// The cached total still belongs to October when the first November row
	// arrives.
	now = time.Date(2026, 11, 1, 0, 0, 5, 0, time.UTC)
	st.AppendRequest(model.RequestLog{TS: "2026-11-01T00:00:05Z", UserID: user.ID, Source: model.SourceGate, Bytes: 40})
	if got, _ := st.User(user.ID); got.UsedBytes != 40 {
		t.Fatalf("November usedBytes = %d, want 40 (the appended row was counted twice)", got.UsedBytes)
	}
}

// TestUsedBytesSurvivesRestoreFromRows proves the derived figure is rebuilt
// from the restored request rows rather than carried in the snapshot.
func TestUsedBytesSurvivesRestoreFromRows(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	st := newEmptyWithClock(func() time.Time { return now }, nil)
	user := mustCreateUser(t, st, "restore-bot")
	st.AppendRequest(model.RequestLog{TS: "2026-10-20T10:00:00Z", UserID: user.ID, Source: model.SourceGate, Bytes: 123})
	if got, _ := st.User(user.ID); got.UsedBytes != 123 {
		t.Fatalf("usedBytes = %d, want 123", got.UsedBytes)
	}

	data, err := st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	for _, persisted := range snap.Users {
		if persisted.UsedBytes != 0 {
			t.Fatalf("snapshot persisted usedBytes = %d, want 0 (the figure is derived)", persisted.UsedBytes)
		}
	}

	fresh := newEmptyWithClock(func() time.Time { return now }, nil)
	if err := fresh.Restore(data); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	restored, ok := fresh.User(user.ID)
	if !ok || restored.UsedBytes != 123 {
		t.Fatalf("restored usedBytes = %d (ok=%v), want 123", restored.UsedBytes, ok)
	}
}

// TestSeededPanelUserUsageIsNotTheDemoCounter proves the dashboard mock's
// per-user counters are not reported as usage: the seed's request rows are
// proxy-sourced, so a seeded user has no billable traffic.
func TestSeededPanelUserUsageIsNotTheDemoCounter(t *testing.T) {
	st := New()
	user, ok := st.User("u-01")
	if !ok {
		t.Fatal("seeded user u-01 is missing")
	}
	if user.UsedBytes != 0 {
		t.Fatalf("u-01 usedBytes = %d, want 0 (seeded rows are proxy-sourced, so the demo counter is not usage)", user.UsedBytes)
	}
}

// TestRequestRetentionEvictionKeepsDurableUsage pins the quota/retention
// separation: once the request log is full, the next append drops its oldest
// row for display, but that row's gate bytes stay in the durable monthly
// counters. Log churn must never renew a budget.
func TestRequestRetentionEvictionKeepsDurableUsage(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	st := newEmptyWithClock(func() time.Time { return now }, nil)
	user := mustCreateUser(t, st, "cap-bot")

	// The oldest row is a billable gate row; the rest only fill the log.
	st.AppendRequest(model.RequestLog{TS: "2026-10-01T00:00:00Z", UserID: user.ID, Source: model.SourceGate, Bytes: 900})
	for range RequestRetention - 1 {
		st.AppendRequest(model.RequestLog{TS: "2026-10-02T00:00:00Z", Source: model.SourceMutationLab})
	}
	if got := len(st.Requests()); got != RequestRetention {
		t.Fatalf("request log = %d rows, want %d", got, RequestRetention)
	}
	if got, _ := st.User(user.ID); got.UsedBytes != 900 {
		t.Fatalf("usedBytes before eviction = %d, want 900", got.UsedBytes)
	}

	// One more append evicts the oldest row, which was the gate row.
	st.AppendRequest(model.RequestLog{TS: "2026-10-03T00:00:00Z", Source: model.SourceMutationLab})
	if got, _ := st.User(user.ID); got.UsedBytes != 900 {
		t.Fatalf("usedBytes after eviction = %d, want 900 (eviction must not renew a quota)", got.UsedBytes)
	}
	// The counter survives a snapshot/reload even though the row is gone.
	data, err := st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	fresh := newEmptyWithClock(func() time.Time { return now }, nil)
	if err := fresh.Restore(data); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got, _ := fresh.User(user.ID); got.UsedBytes != 900 {
		t.Fatalf("restored usedBytes = %d, want 900", got.UsedBytes)
	}
}

// TestLeaseQuotaGuardUsesDerivedUsage proves lease creation refuses a user who
// exhausted the month's gate traffic, and that it reads the derived figure
// rather than the (always zero) stored counter.
func TestLeaseQuotaGuardUsesDerivedUsage(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	st := newEmptyWithClock(func() time.Time { return now }, nil)
	quota := int64(1_000)
	user, err := st.CreateUser(model.CreateUserInput{Name: "quota-bot", QuotaBytesMonth: &quota})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	st.AppendRequest(model.RequestLog{TS: "2026-10-10T10:00:00Z", UserID: user.ID, Source: model.SourceGate, Bytes: 1_000})

	if stored := st.users[0].UsedBytes; stored != 0 {
		t.Fatalf("stored usedBytes = %d, want 0: the guard must not read the stored counter", stored)
	}
	_, _, err = st.CreateLease(model.CreateLeaseInput{UserID: user.ID, Platform: model.PlatformAndroid})
	var leaseErr *LeaseError
	if !errors.As(err, &leaseErr) || !errors.Is(err, ErrQuota) || leaseErr.Reason != "quota_exceeded" {
		t.Fatalf("CreateLease = %v, want quota_exceeded", err)
	}
}
