package webhook

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// stubLookup installs a deterministic resolver for the duration of one test.
func stubLookup(t *testing.T, resolve func(string) ([]net.IP, error)) {
	t.Helper()
	previous := lookupIP
	lookupIP = resolve
	t.Cleanup(func() { lookupIP = previous })
}

func staticLookup(mapping map[string]string) func(string) ([]net.IP, error) {
	return func(host string) ([]net.IP, error) {
		value, ok := mapping[host]
		if !ok {
			return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		}
		return []net.IP{net.ParseIP(value)}, nil
	}
}

// TestProbeTableRejectsPrivateAndMetadataHosts is the SSRF probe table. Every
// row is executed against validateDestinationHost with the resolver stubbed,
// so the result does not depend on the test host's DNS.
func TestProbeTableRejectsPrivateAndMetadataHosts(t *testing.T) {
	stubLookup(t, staticLookup(map[string]string{
		"public.example":       "93.184.216.34",
		"private.example":      "10.1.2.3",
		"loopback.example":     "127.0.0.1",
		"linklocal.example":    "169.254.169.254",
		"rebind.example":       "169.254.169.254",
		"localhost":            "127.0.0.1",
		"mixed.example":        "93.184.216.34", // second address added below
		"unresolvable.example": "",
	}))

	// mixed.example resolves to a public and a private address; the private
	// one must poison the whole name.
	mixed := lookupIP
	lookupIP = func(host string) ([]net.IP, error) {
		if host == "mixed.example" {
			return []net.IP{net.ParseIP("93.184.216.34"), net.ParseIP("192.168.1.20")}, nil
		}
		if host == "unresolvable.example" {
			return nil, &net.DNSError{Err: "temporary failure", Name: host, IsTemporary: true}
		}
		return mixed(host)
	}
	t.Cleanup(func() { lookupIP = mixed })

	probes := []struct {
		name         string
		host         string
		allowPrivate bool
		reject       bool
	}{
		{"ipv4 loopback", "127.0.0.1", false, true},
		{"ipv4 loopback alternate", "127.10.20.30", false, true},
		{"ipv6 loopback", "::1", false, true},
		{"ipv4-mapped loopback", "::ffff:127.0.0.1", false, true},
		{"rfc1918 10/8", "10.0.0.5", false, true},
		{"rfc1918 172.16/12", "172.16.9.1", false, true},
		{"rfc1918 192.168/16", "192.168.1.1", false, true},
		{"link-local metadata", "169.254.169.254", false, true},
		{"ipv6 unique-local", "fd00::1", false, true},
		{"ipv6 link-local", "fe80::1", false, true},
		{"ipv6 link-local zone", "fe80::1%eth0", false, true},
		{"unspecified v4", "0.0.0.0", false, true},
		{"unspecified v6", "::", false, true},
		{"multicast", "224.0.0.1", false, true},
		{"metadata name", "metadata", false, true},
		{"metadata name uppercase", "METADATA", false, true},
		{"gcp metadata name", "metadata.google.internal", false, true},
		{"gcp metadata trailing dot", "metadata.google.internal.", false, true},
		{"gcp metadata with lab opt-in", "metadata.google.internal", true, true},
		{"public ipv4", "8.8.8.8", false, false},
		{"public ipv4 documentation", "93.184.216.34", false, false},
		{"public name", "public.example", false, false},
		{"name resolving private", "private.example", false, true},
		{"name resolving loopback", "loopback.example", false, true},
		{"name resolving link-local", "linklocal.example", false, true},
		{"name resolving mixed", "mixed.example", false, true},
		{"dns rebind name", "rebind.example", false, true},
		{"unresolvable name is allowed", "unresolvable.example", false, false},
		{"loopback with lab opt-in", "127.0.0.1", true, false},
		{"private with lab opt-in", "10.0.0.5", true, false},
		{"localhost", "localhost", false, true},
		{"localhost with lab opt-in", "localhost", true, false},
	}
	for _, probe := range probes {
		t.Run(probe.name, func(t *testing.T) {
			err := validateDestinationHost(probe.host, probe.allowPrivate)
			if probe.reject {
				if !errors.Is(err, ErrInvalidEndpoint) {
					t.Fatalf("host %q allowPrivate=%t: err = %v, want ErrInvalidEndpoint", probe.host, probe.allowPrivate, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("host %q allowPrivate=%t rejected: %v", probe.host, probe.allowPrivate, err)
			}
		})
	}
}

// TestValidateDestinationHostRejectsLoopbackWithoutDNS proves the loopback
// decision does not depend on resolution: the dial guard refuses loopback
// whenever private access is off, so validation refuses the name even when the
// resolver answers nothing, instead of admitting the endpoint and failing at
// delivery time.
func TestValidateDestinationHostRejectsLoopbackWithoutDNS(t *testing.T) {
	stubLookup(t, func(string) ([]net.IP, error) {
		return nil, &net.DNSError{Err: "temporary failure", IsTemporary: true}
	})
	if err := validateDestinationHost("localhost", false); !errors.Is(err, ErrInvalidEndpoint) {
		t.Fatalf("localhost without DNS = %v, want ErrInvalidEndpoint", err)
	}
	if err := validateDestinationHost("localhost", true); err != nil {
		t.Fatalf("localhost with lab opt-in = %v, want nil", err)
	}
}

// TestDialGuardRejectsResolvedPrivateAddresses proves the second SSRF layer:
// the dialer re-checks the address it is about to connect to, so a DNS answer
// that changes after validation still cannot reach a private destination.
func TestDialGuardRejectsResolvedPrivateAddresses(t *testing.T) {
	probes := []struct {
		address string
		reject  bool
	}{
		{"10.0.0.5:443", true},
		{"172.16.0.1:443", true},
		{"192.168.0.10:443", true},
		{"127.0.0.1:443", true},
		{"[::1]:443", true},
		{"169.254.169.254:80", true},
		{"[fe80::1]:443", true},
		{"[fd00::1]:443", true},
		{"0.0.0.0:443", true},
		{"93.184.216.34:443", false},
		{"[2606:2800:220:1:248:1893:25c8:1946]:443", false},
		{"not-an-ip:443", true},
		{"missing-port", true},
	}
	for _, probe := range probes {
		t.Run(probe.address, func(t *testing.T) {
			err := dialGuard("tcp", probe.address, nil)
			if probe.reject && err == nil {
				t.Fatalf("dialGuard(%q) allowed a private destination", probe.address)
			}
			if !probe.reject && err != nil {
				t.Fatalf("dialGuard(%q) rejected a public destination: %v", probe.address, err)
			}
		})
	}
}

// TestSecureTransportEnforcesDialGuard exercises the transport wiring, not
// just the guard function: a loopback httptest server must be unreachable
// through the default dispatcher transport and reachable only with the lab
// opt-in.
func TestSecureTransportEnforcesDialGuard(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	blocked := secureTransport(false)
	blocked.Proxy = nil
	client := &http.Client{Transport: blocked, Timeout: 2 * time.Second}
	if _, err := client.Get(server.URL); err == nil || !strings.Contains(err.Error(), "private or loopback") {
		t.Fatalf("loopback dial error = %v, want the dial guard rejection", err)
	}

	allowed := secureTransport(true)
	allowed.Proxy = nil
	client = &http.Client{Transport: allowed, Timeout: 2 * time.Second}
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("lab opt-in dial: %v", err)
	}
	_ = resp.Body.Close()
}

// TestSecureTransportIgnoresProxyEnvironment proves webhook delivery cannot be
// routed through an HTTP proxy. With HTTP_PROXY/HTTPS_PROXY pointing at a
// fixture, the dispatcher transport must not consult them and no request may
// arrive at the fixture. The fixture is first exercised through an explicitly
// proxied transport, so a zero count below means "went direct" rather than
// "the fixture was never observable".
func TestSecureTransportIgnoresProxyEnvironment(t *testing.T) {
	var hits int32
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer fixture.Close()

	t.Setenv("HTTP_PROXY", fixture.URL)
	t.Setenv("HTTPS_PROXY", fixture.URL)
	t.Setenv("http_proxy", fixture.URL)
	t.Setenv("https_proxy", fixture.URL)
	// A NO_PROXY entry would let the loopback receiver bypass the proxy and
	// hide the regression this test exists for.
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")

	transport := secureTransport(false)
	if transport.Proxy != nil {
		t.Fatal("webhook transport consults the environment proxy")
	}

	proxyURL, err := url.Parse(fixture.URL)
	if err != nil {
		t.Fatalf("parse fixture URL: %v", err)
	}
	proxied := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
		Timeout:   2 * time.Second,
	}
	resp, err := proxied.Get("http://127.0.0.1:9/hook")
	if err != nil {
		t.Fatalf("explicitly proxied request: %v", err)
	}
	resp.Body.Close()
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("fixture proxy hits = %d, want 1: the fixture is not observable", got)
	}
	atomic.StoreInt32(&hits, 0)

	// The dispatcher transport goes direct: the dial guard refuses the loopback
	// receiver and the fixture proxy sees nothing.
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	if _, err := client.Get("http://127.0.0.1:9/hook"); err == nil {
		t.Fatal("webhook transport reached a loopback receiver")
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("webhook delivery sent %d request(s) through the environment proxy", got)
	}
}

// TestAllowPrivateEnvironmentOptIn pins the environment escape hatch at
// construction time, mirroring AJS_WEBHOOK_ALLOW_HTTP.
func TestAllowPrivateEnvironmentOptIn(t *testing.T) {
	t.Setenv(AllowPrivateEnv, "1")
	dispatcher := New(Config{Logger: discardLogger()})
	defer dispatcher.Close()
	if err := dispatcher.AddEndpoint(Endpoint{
		ID: "lab", URL: "http://127.0.0.1:9/hook", Secret: "s", Enabled: true,
	}); err != nil {
		t.Fatalf("lab endpoint rejected with %s=1: %v", AllowPrivateEnv, err)
	}
}

// TestValidateEndpointRejectsMetadataNameEvenWithHTTPOptIn makes sure the
// scheme opt-in does not widen the host policy.
func TestValidateEndpointRejectsMetadataNameEvenWithHTTPOptIn(t *testing.T) {
	err := validateEndpoint(Endpoint{
		ID: "meta", URL: "https://metadata.google.internal/hook", Secret: "s", Enabled: true,
	}, true, true)
	if err == nil {
		t.Fatal("metadata endpoint accepted")
	}
}
