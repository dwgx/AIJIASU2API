package main

import (
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"ajsaccountd/internal/account"
	"ajsaccountd/internal/store"
)

// TestLoginProfile pins the accepted AJS_ACCOUNT_PROFILE values: the default
// stays the live-tested 4.2.3.0 profile, and linux-4.3.0.1 selects the new
// static-only profile. An unknown value must fail with both names.
func TestLoginProfile(t *testing.T) {
	cases := []struct {
		name string
		want account.VersionProfile
	}{
		{name: "", want: account.Linux423Binary},
		{name: "linux-4.2.3.0", want: account.Linux423Binary},
		{name: account.Linux423Binary.Name, want: account.Linux423Binary},
		{name: "linux-4.3.0.1", want: account.Linux430Binary},
		{name: account.Linux430Binary.Name, want: account.Linux430Binary},
		{name: "  linux-4.3.0.1  ", want: account.Linux430Binary},
	}
	for _, tc := range cases {
		got, err := loginProfile(tc.name)
		if err != nil {
			t.Fatalf("loginProfile(%q): %v", tc.name, err)
		}
		if got != tc.want {
			t.Fatalf("loginProfile(%q) = %+v, want %+v", tc.name, got, tc.want)
		}
	}

	_, err := loginProfile("linux-9.9.9.9")
	if err == nil {
		t.Fatal("loginProfile accepted an unknown profile")
	}
	for _, want := range []string{"linux-4.2.3.0", "linux-4.3.0.1"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("loginProfile error = %q, want it to list %q", err, want)
		}
	}
}

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

// TestAJS412StrictAllowsLegacyTokenRemoval proves the final migration state
// needs no dummy shared bearer; strict authentication never consults it.
func TestAJS412StrictAllowsLegacyTokenRemoval(t *testing.T) {
	if err := validateLegacyAdminToken("", false, true); err != nil {
		t.Fatalf("strict mode without legacy token: %v", err)
	}
	if err := validateLegacyAdminToken("", false, false); err == nil {
		t.Fatal("compatibility mode accepted missing shared bearer")
	}
	if err := validateLegacyAdminToken("", true, false); err != nil {
		t.Fatalf("legacy loopback lab: %v", err)
	}
}

// TestValidateAdminTokenRejectsPlaceholders pins the S2 guard: a documented
// placeholder is a working admin API whose secret is printed in the
// repository, so startup must refuse it. The value never reaches the error.
func TestValidateAdminTokenRejectsPlaceholders(t *testing.T) {
	for _, value := range []string{"change-me", "CHANGE-ME", "changeme", "change_me", "admin", "test", "password", "secret", " test ", "local-test-token", "LOCAL-TEST-TOKEN", "replace-me-with-openssl-rand-hex-32"} {
		if _, bad := placeholderAdminTokens[strings.TrimSpace(strings.ToLower(value))]; bad {
			continue
		}
		t.Errorf("%q: not covered by the placeholder denylist", value)
	}
	for _, value := range []string{"change-me", "CHANGE-ME", "admin", "changeme", "change_me", "Secret", "local-test-token"} {
		if err := validateAdminToken(value, false); err == nil {
			t.Fatalf("AJS_ADMIN_TOKEN=%q: validateAdminToken = nil, want an error", value)
		}
	}
	if err := validateAdminToken("", false); err == nil {
		t.Fatal("empty token with secure mode: want an error")
	}
	if err := validateAdminToken("a-random-64-hex-char-token-value", false); err != nil {
		t.Fatalf("real token: validateAdminToken = %v, want nil", err)
	}
	// The insecure lab mode keeps the old behaviour; requireLoopback is the
	// guard that applies there.
	if err := validateAdminToken("", true); err != nil {
		t.Fatalf("insecure mode with no token: validateAdminToken = %v, want nil", err)
	}
}

// TestValidateAdminTokenMinLength pins the other half of the S2 guard: the
// token is the only authentication on the admin API, so anything shorter than
// minAdminTokenLen is refused even when it is not a known placeholder. The
// checks measure the trimmed value, so padding a short token with spaces does
// not lift it over the minimum.
func TestValidateAdminTokenMinLength(t *testing.T) {
	short := strings.Repeat("a", minAdminTokenLen-1)
	if err := validateAdminToken(short, false); err == nil {
		t.Fatalf("%d-character token: validateAdminToken = nil, want an error", len(short))
	}
	padded := short + strings.Repeat(" ", minAdminTokenLen-len(short))
	if err := validateAdminToken(padded, false); err == nil {
		t.Fatal("space-padded short token: validateAdminToken = nil, want an error")
	}
	if err := validateAdminToken(strings.Repeat("a", minAdminTokenLen), false); err != nil {
		t.Fatalf("%d-character token: validateAdminToken = %v, want nil", minAdminTokenLen, err)
	}
	// 32 hex characters are the documented generator output and must pass.
	if err := validateAdminToken(strings.Repeat("0f", minAdminTokenLen/2), false); err != nil {
		t.Fatalf("openssl rand -hex 32 output: validateAdminToken = %v, want nil", err)
	}
	// The insecure lab mode skips the length rule; requireLoopback is the
	// guard that applies there.
	if err := validateAdminToken(short, true); err != nil {
		t.Fatalf("insecure mode with a short token: validateAdminToken = %v, want nil", err)
	}
}

// TestRequireLoopback pins the S4 guard: AJS_ALLOW_INSECURE=1 disables bearer
// auth entirely, so a non-loopback bind must fail startup.
func TestRequireLoopback(t *testing.T) {
	for _, listen := range []string{
		"127.0.0.1:2261", "127.0.0.2:2261", "[::1]:2261", "localhost:2261",
		"api.localhost:2261", "127.0.0.1", "localhost",
	} {
		if err := requireLoopback(listen); err != nil {
			t.Fatalf("AJS_LISTEN=%q: requireLoopback = %v, want nil", listen, err)
		}
	}
	for _, listen := range []string{"0.0.0.0:2261", ":2261", "192.168.1.10:2261", "[::]:2261", "10.0.0.5:2261", ""} {
		if err := requireLoopback(listen); err == nil {
			t.Fatalf("AJS_LISTEN=%q: requireLoopback = nil, want an error", listen)
		}
	}
}

// TestSnapshotIntervalBareSeconds is the bench regression: BENCH.md §9.9
// showed AJS_SNAPSHOT_INTERVAL=3600 refused startup ("missing unit in
// duration"). A bare integer now means seconds, while every valid duration
// keeps its meaning.
func TestSnapshotIntervalBareSeconds(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  time.Duration
	}{
		{value: "", want: defaultSnapshotInterval},
		{value: "3600", want: 3600 * time.Second},
		{value: " 30 ", want: 30 * time.Second},
		{value: "3600s", want: 3600 * time.Second},
		{value: "1h", want: time.Hour},
		{value: "1m30s", want: 90 * time.Second},
	} {
		t.Setenv("AJS_SNAPSHOT_INTERVAL", tc.value)
		got, err := snapshotInterval()
		if err != nil {
			t.Fatalf("AJS_SNAPSHOT_INTERVAL=%q: snapshotInterval: %v", tc.value, err)
		}
		if got != tc.want {
			t.Fatalf("AJS_SNAPSHOT_INTERVAL=%q: snapshotInterval() = %v, want %v", tc.value, got, tc.want)
		}
	}
}

// TestSnapshotIntervalRejectsInvalid keeps the startup guard: a value with no
// unit and no integer meaning, or a non-positive duration, still fails with a
// unit hint.
func TestSnapshotIntervalRejectsInvalid(t *testing.T) {
	for _, value := range []string{"-5", "-5s", "0", "0s", "abc", "1.5", "1 2", "1e3"} {
		t.Setenv("AJS_SNAPSHOT_INTERVAL", value)
		got, err := snapshotInterval()
		if err == nil {
			t.Fatalf("AJS_SNAPSHOT_INTERVAL=%q: snapshotInterval() = %v, want an error", value, got)
		}
		if got != 0 {
			t.Fatalf("AJS_SNAPSHOT_INTERVAL=%q: interval = %v, want 0 with the error", value, got)
		}
		if !strings.Contains(err.Error(), "AJS_SNAPSHOT_INTERVAL") {
			t.Fatalf("AJS_SNAPSHOT_INTERVAL=%q: error %q, want an AJS_SNAPSHOT_INTERVAL error", value, err)
		}
	}
}

// TestValidateLoginHosts pins the S11 guard: the login body carries the
// account password, so a non-https host is refused unless the lab opts in.
func TestValidateLoginHosts(t *testing.T) {
	https := []string{
		"https://login.ajs-app.com",
		"https://login.mmegg.com:4443",
		"login.mmegg.com", // bare host: the client prefixes https://
	}
	t.Setenv("AJS_ALLOW_PLAINTEXT", "")
	if err := validateLoginHosts(https); err != nil {
		t.Fatalf("https hosts: validateLoginHosts = %v, want nil", err)
	}
	for _, hosts := range [][]string{
		{"http://login.mmegg.com"},
		{"https://login.mmegg.com", "http://login.mmegg.com:4443"},
		{"ftp://login.mmegg.com"},
	} {
		if err := validateLoginHosts(hosts); err == nil {
			t.Fatalf("hosts %v: validateLoginHosts = nil, want an error", hosts)
		}
	}
	// The explicit lab opt-out restores the lab mock path.
	t.Setenv("AJS_ALLOW_PLAINTEXT", "1")
	if err := validateLoginHosts([]string{"http://127.0.0.1:8081"}); err != nil {
		t.Fatalf("AJS_ALLOW_PLAINTEXT=1: validateLoginHosts = %v, want nil", err)
	}
}

// TestOpenStoreStartsEmpty locks the daemon wiring: both openStore paths build
// an empty store, never the dashboard mock seed.
func TestOpenStoreStartsEmpty(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	t.Setenv("AJS_MASTER_KEY", "")
	t.Setenv("AJS_DEVICE_POOL", "")
	st, secrets, deviceSecrets, err := openStore(logger)
	if err != nil {
		t.Fatalf("openStore without a key: %v", err)
	}
	if secrets != nil {
		t.Fatal("openStore without a key returned a secret store")
	}
	if deviceSecrets != nil {
		t.Fatal("openStore without a key returned a device secret store")
	}
	assertEmptyStore(t, st)

	t.Setenv("AJS_MASTER_KEY", strings.Repeat("ab", 32))
	t.Setenv("AJS_STATE_DIR", t.TempDir())
	st, secrets, deviceSecrets, err = openStore(logger)
	if err != nil {
		t.Fatalf("openStore with a key: %v", err)
	}
	if secrets == nil {
		t.Fatal("openStore with a key returned no secret store")
	}
	if deviceSecrets == nil {
		t.Fatal("openStore with a key returned no device secret store")
	}
	defer func() { _ = secrets.Close() }()
	defer func() { _ = deviceSecrets.Close() }()
	assertEmptyStore(t, st)
}

func assertEmptyStore(t *testing.T, st *store.Store) {
	t.Helper()
	if accounts := st.Accounts(); len(accounts) != 0 {
		t.Fatalf("startup store accounts = %d, want 0", len(accounts))
	}
	if nodes := st.Nodes(); len(nodes) != 0 {
		t.Fatalf("startup store nodes = %d, want 0", len(nodes))
	}
	if leases := st.Leases(); len(leases) != 0 {
		t.Fatalf("startup store leases = %d, want 0", len(leases))
	}
}

// TestFlowMeterEnabled pins AJS_FLOW_METER: metering is on by default and only
// the explicit "0" (surrounding whitespace allowed) turns it off.
func TestFlowMeterEnabled(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{raw: "", want: true},
		{raw: "1", want: true},
		{raw: "true", want: true},
		{raw: "  ", want: true},
		{raw: "0", want: false},
		{raw: " 0 ", want: false},
	}
	for _, tc := range cases {
		if got := flowMeterEnabled(tc.raw); got != tc.want {
			t.Fatalf("flowMeterEnabled(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

// TestAutoLoginEnabled pins AJS_AUTO_LOGIN: only the exact "1" enables the
// startup auto-login, so an unset or misspelled value never contacts a vendor
// host on boot.
func TestAutoLoginEnabled(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{raw: "", want: false},
		{raw: "0", want: false},
		{raw: "true", want: false},
		{raw: "yes", want: false},
		{raw: "1", want: true},
		{raw: " 1 ", want: true},
	}
	for _, tc := range cases {
		if got := autoLoginEnabled(tc.raw); got != tc.want {
			t.Fatalf("autoLoginEnabled(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

func TestTenantPortalConfig(t *testing.T) {
	origin, proxies, err := tenantPortalConfig(false, "", "")
	if err != nil || origin != "" || len(proxies) != 0 {
		t.Fatalf("disabled config = %q/%v/%v", origin, proxies, err)
	}

	origin, proxies, err = tenantPortalConfig(true, " https://Panel.Example.COM/ ", "127.0.0.1, 10.0.0.0/8")
	if err != nil {
		t.Fatalf("valid config: %v", err)
	}
	if origin != "https://panel.example.com" {
		t.Fatalf("origin = %q, want the normalized origin", origin)
	}
	if len(proxies) != 2 {
		t.Fatalf("proxies = %v, want two entries", proxies)
	}

	if _, _, err := tenantPortalConfig(true, "", ""); err == nil {
		t.Fatal("enabled portal without an origin was accepted")
	}
	if _, _, err := tenantPortalConfig(true, "https://panel.example.com/admin", ""); err == nil {
		t.Fatal("origin with a path was accepted")
	}
	if _, _, err := tenantPortalConfig(false, "", "not-an-ip"); err == nil {
		t.Fatal("malformed trusted proxy was accepted")
	}
	// A configured origin without the flag is returned so run() can warn; it
	// does not fail startup.
	origin, _, err = tenantPortalConfig(false, "https://panel.example.com", "")
	if err != nil || origin != "https://panel.example.com" {
		t.Fatalf("disabled with origin = %q/%v", origin, err)
	}
}
