package gate

import (
	"fmt"
	"net/netip"
	"strings"
)

// AuthMode selects the SOCKS5 authentication method one gate negotiates. An
// empty value selects DefaultAuthMode; every explicit value must parse or
// construction fails.
type AuthMode string

const (
	// AuthLoopback serves the no-auth method (0x00) and trusts every local
	// process. It is the explicit opt-in compatibility path: no user identity, no
	// user allowlist and no user quota. The loopback peer check and the account
	// quota still apply, so it is not a secure multi-user mode and a deployment
	// must select it on purpose.
	AuthLoopback AuthMode = "loopback"
	// AuthUserPass serves RFC 1929 username/password. The username is the
	// exact PanelUser.ID and the password is one of that user's API-key
	// secrets; the gate never creates or selects a vendor session.
	AuthUserPass AuthMode = "userpass"
)

// DefaultAuthMode is what an unset AJS_GATE_AUTH selects. It is the fail-closed
// default: a deployment that forgets to configure gate authentication must not
// relay for an unauthenticated same-host process, and the gate cannot
// distinguish such a process from the operator. The compatibility wire is still
// available, but only as an explicit AuthLoopback opt-in; the listener stays
// loopback-only in both modes, so the default never widens the bind.
const DefaultAuthMode = AuthUserPass

// ParseAuthMode maps the process setting (AJS_GATE_AUTH) onto a typed mode. An
// empty value selects DefaultAuthMode; every other unrecognised value is an
// error so a typo aborts construction instead of silently weakening the
// listener.
func ParseAuthMode(raw string) (AuthMode, error) {
	mode := AuthMode(strings.TrimSpace(raw))
	switch mode {
	case "":
		return DefaultAuthMode, nil
	case AuthLoopback, AuthUserPass:
		return mode, nil
	default:
		return "", fmt.Errorf("gate: unknown auth mode %q (want %q or %q)", raw, AuthLoopback, AuthUserPass)
	}
}

// Principal is the authenticated identity of one flow. It carries attribution
// only: the RFC 1929 frame and the API-key secret are erased before a
// Principal exists, so no credential can reach a FlowRecord or a log line.
//
// LeaseID is the one active lease the credential was resolved against at the
// gate's account; it is an identifier, never a secret. Admission re-reads that
// lease so a flow cannot outlive the entitlement it was authenticated for.
type Principal struct {
	UserID   string
	UserName string
	LeaseID  string
}

// Authenticated reports whether p names a panel user.
func (p Principal) Authenticated() bool {
	return p.UserID != ""
}

// Authenticator resolves one RFC 1929 credential pair. It returns ok only for
// an active key whose secret matches pass and whose UserID equals user; every
// rejection is indistinguishable to the client (one generic 01 01 reply), so
// an unknown user, a wrong secret, a revoked key and a key issued to another
// user share one path.
type Authenticator interface {
	Authenticate(user, pass string) (Principal, bool)
}

// UserPolicy is the per-user admission policy. The gate calls it after the
// CONNECT destination parses and before it takes an admission slot or opens a
// vendor flow, so a refusal never consumes capacity or upstream traffic.
//
// host is already canonical (ASCII lower case, one terminal dot removed).
// errorClass is one of the FlowError* user classes; used and budget are
// diagnostics for the rejection log only. A nil policy fails closed.
type UserPolicy interface {
	Admit(principal Principal, host string) (ok bool, errorClass string, used, budget int64)
}

// Allowlist is one user's compiled destination policy. Host entries are exact,
// case-insensitive names; a wildcard entry ("*.example.com") matches any name
// with at least one extra label below it; prefix entries match only a literal
// target IP inside that prefix. Matching never resolves a name, so a domain
// rule can never authorize an address the client reached through DNS.
type Allowlist struct {
	hosts    map[string]struct{}
	suffixes map[string]struct{}
	prefixes []netip.Prefix
	invalid  bool
}

// CompileAllowlist parses the entries once per user write. An entry that is
// neither a hostname, a "*.domain" wildcard, an IP literal nor a CIDR marks the
// whole list invalid: the gate then denies that user instead of skipping the
// bad entry, so a malformed persisted row can never widen the policy into
// allow-all. A wildcard must be exactly one leading "*." followed by a plain
// name; "*", "*example.com", "a.*.com" and "**.example.com" are all invalid.
func CompileAllowlist(entries []string) Allowlist {
	list := Allowlist{
		hosts:    make(map[string]struct{}, len(entries)),
		suffixes: make(map[string]struct{}, len(entries)),
	}
	for _, raw := range entries {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			list.invalid = true
			continue
		}
		if base, wildcard := strings.CutPrefix(entry, "*."); wildcard {
			base = CanonicalHost(base)
			// A wildcard base needs at least two labels: "*.com" would
			// authorize a whole TLD from one typo, and the store refuses it.
			if !validHostname(base) || !strings.Contains(base, ".") {
				list.invalid = true
				continue
			}
			list.suffixes[base] = struct{}{}
			continue
		}
		if strings.Contains(entry, "*") {
			list.invalid = true
			continue
		}
		if strings.Contains(entry, "/") {
			prefix, err := netip.ParsePrefix(entry)
			if err != nil {
				list.invalid = true
				continue
			}
			list.prefixes = append(list.prefixes, prefix.Masked())
			continue
		}
		if addr, err := netip.ParseAddr(entry); err == nil {
			// A zone names a local interface, not a destination; the store
			// refuses one at write time and a persisted snapshot must not
			// silently carry it.
			if addr.Zone() != "" {
				list.invalid = true
				continue
			}
			list.hosts[addr.Unmap().String()] = struct{}{}
			continue
		}
		host := CanonicalHost(entry)
		if !validHostname(host) {
			list.invalid = true
			continue
		}
		list.hosts[host] = struct{}{}
	}
	return list
}

// Match reports whether host is authorized. An empty list authorizes nothing,
// an invalid list authorizes nothing, a wildcard requires at least one label
// below its base (so "*.example.com" never matches "example.com" itself), and
// a subdomain is never implied by its parent's exact rule. A wildcard rule
// never matches an IP literal.
func (a Allowlist) Match(host string) bool {
	if a.invalid {
		return false
	}
	canonical := CanonicalHost(host)
	if canonical == "" {
		return false
	}
	if addr, err := netip.ParseAddr(canonical); err == nil {
		addr = addr.Unmap()
		if _, ok := a.hosts[addr.String()]; ok {
			return true
		}
		for _, prefix := range a.prefixes {
			if prefix.Contains(addr) {
				return true
			}
		}
		return false
	}
	if _, ok := a.hosts[canonical]; ok {
		return true
	}
	// Every dot splits the name into a candidate wildcard base; looking the
	// remainder up in the suffix set keeps one match O(labels), not O(entries).
	for i := 1; i < len(canonical); i++ {
		if canonical[i] != '.' {
			continue
		}
		if _, ok := a.suffixes[canonical[i+1:]]; ok {
			return true
		}
	}
	return false
}

// Invalid reports whether the compiled list contained an entry the gate could
// not parse. A user with an invalid list is denied every destination.
func (a Allowlist) Invalid() bool {
	return a.invalid
}

// CanonicalHost normalizes one SOCKS5 destination for policy comparison: an IP
// literal becomes its canonical text (IPv4-mapped IPv6 unmapped), a name is
// ASCII-lowercased and loses exactly one terminal dot. It never resolves and
// never invents a name for an empty input.
func CanonicalHost(host string) string {
	trimmed := strings.TrimSpace(host)
	if addr, err := netip.ParseAddr(trimmed); err == nil {
		return addr.Unmap().String()
	}
	lowered := asciiLower(trimmed)
	return strings.TrimSuffix(lowered, ".")
}

// asciiLower lowercases A-Z only. Hostnames are ASCII on the wire; leaving
// every other byte alone keeps the comparison byte-exact.
func asciiLower(value string) string {
	first := -1
	for i := range len(value) {
		if value[i] >= 'A' && value[i] <= 'Z' {
			first = i
			break
		}
	}
	if first < 0 {
		return value
	}
	out := []byte(value)
	for i := first; i < len(out); i++ {
		if out[i] >= 'A' && out[i] <= 'Z' {
			out[i] += 'a' - 'A'
		}
	}
	return string(out)
}

// validHostname reports whether a canonical allowlist entry is a plain DNS
// name. Wildcards, ports, schemes and empty labels are rejected: the design
// supports exact names and IP/CIDR rules only.
func validHostname(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		for i := range len(label) {
			c := label[i]
			switch {
			case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_':
			default:
				return false
			}
		}
	}
	return true
}
