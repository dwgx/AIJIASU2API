package webhook

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"
)

// AllowPrivateEnv lets a lab or a test install receivers on loopback and
// private networks. Production deployments leave it unset: a webhook URL is
// operator-supplied but a compromised panel could still create one, and the
// daemon must not become a probe into its own host or the cloud metadata
// service.
const AllowPrivateEnv = "AJS_WEBHOOK_ALLOW_PRIVATE"

// lookupIP resolves a receiver hostname. It is a variable so tests can prove
// the private-range rejection without depending on public DNS.
var lookupIP = net.LookupIP

// metadataHosts are rejected by name before DNS. They are not ordinary private
// addresses: 169.254.169.254 answers instance credentials, and some resolvers
// map these names onto it. The name check has no opt-out.
var metadataHosts = map[string]struct{}{
	"metadata":                 {},
	"metadata.google.internal": {},
}

// validateDestinationHost rejects receiver hosts that would make the daemon
// dial itself, a private network, a link-local address or the cloud metadata
// service. Names are resolved so a public name pointing at 127.0.0.1 or
// 169.254.169.254 is caught as well as a literal address. An unresolvable name
// is allowed: DNS may be transient, and the transport re-checks the resolved
// address in its dial guard before connecting.
//
// allowPrivate is the AJS_WEBHOOK_ALLOW_PRIVATE opt-out; it disables the IP
// checks (loopback, RFC 1918, link-local, unique-local) but never the metadata
// name block.
func validateDestinationHost(host string, allowPrivate bool) error {
	name := strings.ToLower(strings.TrimSpace(host))
	name = strings.TrimSuffix(name, ".")
	if _, blocked := metadataHosts[name]; blocked {
		return fmt.Errorf("%w: cloud metadata host %q is not allowed", ErrInvalidEndpoint, host)
	}
	// An IPv6 literal may carry a zone identifier ("fe80::1%eth0"); the zone
	// does not change the address class.
	if zone := strings.IndexByte(name, '%'); zone >= 0 {
		name = name[:zone]
	}
	if ip := net.ParseIP(name); ip != nil {
		return checkDestinationIP(host, ip, allowPrivate)
	}
	// The dial guard refuses loopback whenever private access is off, so a
	// loopback name is refused here on the name alone. Waiting for DNS would
	// admit `http://localhost` on a resolver failure and then fail at delivery
	// time with a guard error, which is the same decision reported later and
	// less clearly.
	if !allowPrivate && isLoopbackHost(name) {
		return fmt.Errorf("%w: host %q is loopback; set %s=1 to allow a private receiver",
			ErrInvalidEndpoint, host, AllowPrivateEnv)
	}
	ips, err := lookupIP(name)
	if err != nil {
		return nil
	}
	for _, ip := range ips {
		if err := checkDestinationIP(host, ip, allowPrivate); err != nil {
			return err
		}
	}
	return nil
}

func checkDestinationIP(host string, ip net.IP, allowPrivate bool) error {
	if allowPrivate || !blockedDestination(ip) {
		return nil
	}
	return fmt.Errorf("%w: host %q resolves to the private or loopback address %s",
		ErrInvalidEndpoint, host, ip)
}

// blockedDestination reports whether ip is a destination a customer webhook
// must never reach. IPv4-mapped IPv6 addresses are unwrapped first because the
// address-class predicates are stricter about the native form.
func blockedDestination(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	return ip.IsLoopback() ||
		ip.IsPrivate() || // RFC 1918 and fc00::/7
		ip.IsLinkLocalUnicast() || // 169.254.0.0/16 and fe80::/10
		ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() ||
		ip.IsUnspecified()
}

// secureTransport returns the client transport the dispatcher uses. The dial
// guard is the second half of the SSRF defence: validateEndpoint resolves the
// name when the endpoint is registered, and the guard re-checks the address
// the resolver actually produced at connect time, so a DNS rebind between the
// two moments cannot reach a private address. A caller-supplied transport is
// not wrapped; its dial path is the caller's responsibility, while
// validateEndpoint still rejects the endpoint up front.
//
// Proxy is nil on purpose: with an environment proxy the guard would inspect
// the proxy address instead of the receiver, and the proxy would resolve the
// attacker-controlled name itself, so a rebind to a private address would be
// reached through a connection the guard approved. Delivery therefore goes
// direct even when HTTP_PROXY/HTTPS_PROXY are set for the rest of the process.
func secureTransport(allowPrivate bool) *http.Transport {
	transport := &http.Transport{}
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = base.Clone()
		// Clone copies DefaultTransport's ProxyFromEnvironment; clear it.
		transport.Proxy = nil
	}
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	if !allowPrivate {
		dialer.Control = dialGuard
	}
	transport.DialContext = dialer.DialContext
	return transport
}

// dialGuard rejects a resolved address that validateEndpoint would have
// rejected. The dialer calls it with the IP:port it is about to connect to,
// after DNS resolution and before the socket is opened.
func dialGuard(network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%w: dial address %q: %v", ErrInvalidEndpoint, address, err)
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil {
		return fmt.Errorf("%w: dial address %q is not an IP", ErrInvalidEndpoint, address)
	}
	if blockedDestination(ip) {
		return fmt.Errorf("%w: refusing to dial private or loopback address %s", ErrInvalidEndpoint, ip)
	}
	return nil
}
