package usage

import (
	"net"
	"net/url"
	"strings"
)

// hostLabel reduces a request target to a host-only label. It deliberately
// strips the scheme, userinfo, port, path, query string and fragment: usage
// summaries are customer-facing and must never echo a full URL or a token that
// leaked into one. Hostnames are lowercased because DNS names are
// case-insensitive. A target with no usable host is grouped under
// UnknownTarget.
func hostLabel(target string) string {
	raw := strings.TrimSpace(target)
	if raw == "" {
		return UnknownTarget
	}
	if strings.Contains(raw, "://") || strings.HasPrefix(raw, "//") {
		candidate := raw
		if strings.HasPrefix(candidate, "//") {
			candidate = "http:" + candidate
		}
		parsed, err := url.Parse(candidate)
		if err != nil || parsed.Hostname() == "" {
			return UnknownTarget
		}
		return normalizeHost(parsed.Hostname())
	}

	// Authority-like target: drop everything from the first path, query or
	// fragment delimiter, then drop userinfo.
	if i := strings.IndexAny(raw, "/?#"); i >= 0 {
		raw = raw[:i]
	}
	if i := strings.LastIndex(raw, "@"); i >= 0 {
		raw = raw[i+1:]
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return UnknownTarget
	}
	if host, _, err := net.SplitHostPort(raw); err == nil {
		raw = host
	} else if strings.HasPrefix(raw, "[") && strings.HasSuffix(raw, "]") {
		raw = raw[1 : len(raw)-1]
	}
	return normalizeHost(raw)
}

func normalizeHost(host string) string {
	host = strings.TrimSpace(host)
	host = strings.TrimSuffix(host, ".")
	host = strings.ToLower(host)
	if host == "" {
		return UnknownTarget
	}
	return host
}
