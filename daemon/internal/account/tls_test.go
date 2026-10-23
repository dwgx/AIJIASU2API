package account

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// tlsVersion12 is spelled locally so the test pins the configured floor
// without importing crypto/tls.
const tlsVersion12 = 0x0303

// certFingerprint returns the lowercase hex SHA-256 of a test certificate,
// the same value PinnedHTTPClient compares.
func certFingerprint(certBytes []byte) string {
	sum := sha256.Sum256(certBytes)
	return hex.EncodeToString(sum[:])
}

// colonsFingerprint renders a lowercase hex fingerprint as AA:BB:…, the
// second accepted input form for PinnedHTTPClient.
func colonsFingerprint(hexFingerprint string) string {
	parts := make([]string, 0, len(hexFingerprint)/2)
	for i := 0; i+1 < len(hexFingerprint); i += 2 {
		parts = append(parts, strings.ToUpper(hexFingerprint[i:i+2]))
	}
	return strings.Join(parts, ":")
}

// TestPinnedHTTPClientAcceptsCertificate proves a matching pin lets the
// self-signed httptest certificate through, in both AA:BB and raw-hex forms.
func TestPinnedHTTPClientAcceptsCertificate(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	t.Cleanup(server.Close)

	want := certFingerprint(server.Certificate().Raw)
	for name, fingerprint := range map[string]string{
		"colons": colonsFingerprint(want),
		"raw":    want,
	} {
		client, err := PinnedHTTPClient(fingerprint, 35*time.Second)
		if err != nil {
			t.Fatalf("%s: PinnedHTTPClient: %v", name, err)
		}
		resp, err := client.Get(server.URL)
		if err != nil {
			t.Fatalf("%s: GET: %v", name, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", name, resp.StatusCode)
		}
	}
}

// TestPinnedHTTPClientRejectsWrongPin proves a pin that does not match the
// server certificate fails the request with ErrCertPin.
func TestPinnedHTTPClientRejectsWrongPin(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler must not be reached with a wrong pin")
	}))
	t.Cleanup(server.Close)

	wrong := certFingerprint(server.Certificate().Raw)
	if wrong[:4] == "ffff" {
		wrong = "0000" + wrong[4:]
	} else {
		wrong = "ffff" + wrong[4:]
	}
	client, err := PinnedHTTPClient(wrong, 35*time.Second)
	if err != nil {
		t.Fatalf("PinnedHTTPClient: %v", err)
	}
	_, err = client.Get(server.URL)
	if err == nil {
		t.Fatal("GET with a wrong pin succeeded, want failure")
	}
	if !errors.Is(err, ErrCertPin) {
		t.Fatalf("GET error = %v, want ErrCertPin", err)
	}
	if !strings.Contains(err.Error(), wrong) {
		t.Fatalf("GET error %q does not carry the expected fingerprint", err)
	}
}

// TestPinnedHTTPClientRejectsMalformedFingerprint proves the constructor
// rejects non-hex and wrong-length values before any connection is made.
func TestPinnedHTTPClientRejectsMalformedFingerprint(t *testing.T) {
	for name, fingerprint := range map[string]string{
		"non-hex":    strings.Repeat("zz", 32),
		"too short":  strings.Repeat("aa", 31),
		"too long":   strings.Repeat("aa", 33),
		"empty":      "",
		"odd length": strings.Repeat("a", 63),
	} {
		if _, err := PinnedHTTPClient(fingerprint, 35*time.Second); err == nil {
			t.Fatalf("%s: PinnedHTTPClient(%q) succeeded, want error", name, fingerprint)
		}
	}
}

// TestDefaultClientRejectsSelfSigned proves the pin is opt-in: the plain
// default verification still rejects the same self-signed certificate, so
// the daemon does not skip TLS globally.
func TestDefaultClientRejectsSelfSigned(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler must not be reached without a pin")
	}))
	t.Cleanup(server.Close)

	client := &http.Client{Timeout: 35 * time.Second}
	_, err := client.Get(server.URL)
	if err == nil {
		t.Fatal("default client accepted the self-signed certificate, want x509 failure")
	}
	if !strings.Contains(err.Error(), "x509") && !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("default client error = %v, want an x509 verification failure", err)
	}
}

// TestPinnedHTTPClientMinTLS12 proves the pinned transport refuses TLS 1.0/1.1
// by its configuration, so a down-grade cannot bypass the pin.
func TestPinnedHTTPClientMinTLS12(t *testing.T) {
	client, err := PinnedHTTPClient(strings.Repeat("aa", 32), 35*time.Second)
	if err != nil {
		t.Fatalf("PinnedHTTPClient: %v", err)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T, want *http.Transport", client.Transport)
	}
	if got := transport.TLSClientConfig.MinVersion; got != tlsVersion12 {
		t.Fatalf("MinVersion = %v, want TLS 1.2", got)
	}
	if !transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("InsecureSkipVerify not set on the pinned client")
	}
	if transport.TLSClientConfig.VerifyPeerCertificate == nil {
		t.Fatal("VerifyPeerCertificate not set on the pinned client")
	}
}

// TestControlWorksWithPinnedClient wires the pinned client through Control so
// the login transport the daemon builds is proven end to end: a Control with
// the pinned client completes a login against the pinned TLS server.
func TestControlWorksWithPinnedClient(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, loginReply)
	}))
	t.Cleanup(server.Close)

	client, err := PinnedHTTPClient(certFingerprint(server.Certificate().Raw), 35*time.Second)
	if err != nil {
		t.Fatalf("PinnedHTTPClient: %v", err)
	}
	control := NewControl([]string{server.URL}, mustDevice(t), Options{
		HTTPClient: client,
		NoSigning:  true,
	})
	t.Cleanup(func() { control.Close() })

	result, err := control.Login(context.Background(), "user@example.com", Secret(testPassword))
	if err != nil {
		t.Fatalf("Login over pinned client: %v", err)
	}
	if result.AuthResult != "OK" {
		t.Fatalf("AuthResult = %q, want OK", result.AuthResult)
	}
}
