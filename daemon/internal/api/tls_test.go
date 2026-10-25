package api

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"ajsaccountd/internal/store"
)

// TestTLSDisabledByDefault pins the opt-in contract: with no AJS_TLS_*
// variable the daemon starts exactly as before and binds no second socket.
func TestTLSDisabledByDefault(t *testing.T) {
	cfg, err := ParseTLSConfig(func(string) string { return "" })
	if err != nil {
		t.Fatalf("ParseTLSConfig(empty): %v", err)
	}
	if cfg.Enabled() {
		t.Fatalf("empty environment enabled TLS: %+v", cfg)
	}
	if cfg.Listen != DefaultTLSListen {
		t.Fatalf("Listen = %q, want %q", cfg.Listen, DefaultTLSListen)
	}
}

// TestParseTLSConfig pins the all-or-nothing rule: a certificate pair enables
// the listener, and every partial configuration fails instead of silently
// serving plaintext or falling back to another certificate.
func TestParseTLSConfig(t *testing.T) {
	const (
		cert = "/etc/ajs/fullchain.pem"
		key  = "/etc/ajs/privkey.pem"
	)
	cases := []struct {
		name    string
		env     map[string]string
		want    TLSConfig
		wantErr []string
	}{
		{
			name: "unset",
			env:  nil,
			want: TLSConfig{Listen: DefaultTLSListen},
		},
		{
			name: "blank rows read as unset",
			env:  map[string]string{EnvTLSCert: " ", EnvTLSKey: "\t", EnvTLSListen: ""},
			want: TLSConfig{Listen: DefaultTLSListen},
		},
		{
			name:    "cert without key",
			env:     map[string]string{EnvTLSCert: cert},
			wantErr: []string{EnvTLSCert, EnvTLSKey},
		},
		{
			name:    "key without cert",
			env:     map[string]string{EnvTLSKey: key},
			wantErr: []string{EnvTLSCert, EnvTLSKey},
		},
		{
			name:    "listen without pair",
			env:     map[string]string{EnvTLSListen: "0.0.0.0:2262"},
			wantErr: []string{EnvTLSListen, EnvTLSCert, EnvTLSKey},
		},
		{
			name: "pair uses the default address",
			env:  map[string]string{EnvTLSCert: cert, EnvTLSKey: key},
			want: TLSConfig{CertFile: cert, KeyFile: key, Listen: DefaultTLSListen},
		},
		{
			name: "pair with an explicit address",
			env:  map[string]string{EnvTLSCert: cert, EnvTLSKey: key, EnvTLSListen: "0.0.0.0:2262"},
			want: TLSConfig{CertFile: cert, KeyFile: key, Listen: "0.0.0.0:2262"},
		},
		{
			name: "pair with a blank address",
			env:  map[string]string{EnvTLSCert: cert, EnvTLSKey: key, EnvTLSListen: "  "},
			want: TLSConfig{CertFile: cert, KeyFile: key, Listen: DefaultTLSListen},
		},
		{
			name: "surrounding whitespace is trimmed",
			env:  map[string]string{EnvTLSCert: " " + cert + "\n", EnvTLSKey: "\t" + key, EnvTLSListen: " 127.0.0.1:2262 "},
			want: TLSConfig{CertFile: cert, KeyFile: key, Listen: "127.0.0.1:2262"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := ParseTLSConfig(func(name string) string { return tc.env[name] })
			if len(tc.wantErr) > 0 {
				if err == nil {
					t.Fatalf("ParseTLSConfig(%v) = %+v, want an error", tc.env, cfg)
				}
				for _, want := range tc.wantErr {
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("error %q does not name %s", err, want)
					}
				}
				if cfg != (TLSConfig{}) {
					t.Fatalf("partial configuration returned %+v, want the zero value", cfg)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseTLSConfig(%v): %v", tc.env, err)
			}
			if cfg != tc.want {
				t.Fatalf("ParseTLSConfig(%v) = %+v, want %+v", tc.env, cfg, tc.want)
			}
		})
	}
}

// TestNewTLSServerRejectsBadCertificate pins the fail-fast rule: a missing,
// mismatched or absent pair aborts before any socket is bound, and the error
// names the files without printing key material.
func TestNewTLSServerRejectsBadCertificate(t *testing.T) {
	template := &http.Server{Handler: http.NotFoundHandler()}

	if _, err := NewTLSServer(template, TLSConfig{Listen: DefaultTLSListen}); err == nil {
		t.Fatal("NewTLSServer with no certificate pair = nil error, want a refusal")
	} else if !strings.Contains(err.Error(), EnvTLSCert) || !strings.Contains(err.Error(), EnvTLSKey) {
		t.Fatalf("disabled error %q does not name both variables", err)
	}

	dir := t.TempDir()
	absent := filepath.Join(dir, "absent.pem")
	_, err := NewTLSServer(template, TLSConfig{CertFile: absent, KeyFile: absent, Listen: DefaultTLSListen})
	if err == nil {
		t.Fatal("NewTLSServer with a missing pair = nil error, want a refusal")
	}
	if !strings.Contains(err.Error(), absent) {
		t.Fatalf("missing-pair error %q does not name the path", err)
	}

	certFile, _, _ := writeTestCertificate(t)
	_, otherKey, _ := writeTestCertificate(t)
	_, err = NewTLSServer(template, TLSConfig{CertFile: certFile, KeyFile: otherKey, Listen: DefaultTLSListen})
	if err == nil {
		t.Fatal("NewTLSServer with a mismatched pair = nil error, want a refusal")
	}
	if !strings.Contains(err.Error(), "load certificate pair") {
		t.Fatalf("mismatched-pair error %q is not a load error", err)
	}
}

// TestNewTLSServerCopiesTemplate pins the two properties that keep the
// listeners in step: the HTTPS server copies the plaintext timeouts, and the
// plaintext server is left untouched.
func TestNewTLSServerCopiesTemplate(t *testing.T) {
	certFile, keyFile, _ := writeTestCertificate(t)
	mux := http.NewServeMux()
	template := &http.Server{
		Addr:              "127.0.0.1:2261",
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	srv, err := NewTLSServer(template, TLSConfig{CertFile: certFile, KeyFile: keyFile, Listen: "127.0.0.1:2262"})
	if err != nil {
		t.Fatalf("NewTLSServer: %v", err)
	}
	if srv.Addr != "127.0.0.1:2262" {
		t.Fatalf("Addr = %q, want the TLS address", srv.Addr)
	}
	if srv.ReadHeaderTimeout != template.ReadHeaderTimeout ||
		srv.ReadTimeout != template.ReadTimeout ||
		srv.WriteTimeout != template.WriteTimeout ||
		srv.IdleTimeout != template.IdleTimeout {
		t.Fatalf("timeouts = %v/%v/%v/%v, want the template's %v/%v/%v/%v",
			srv.ReadHeaderTimeout, srv.ReadTimeout, srv.WriteTimeout, srv.IdleTimeout,
			template.ReadHeaderTimeout, template.ReadTimeout, template.WriteTimeout, template.IdleTimeout)
	}
	if template.TLSConfig != nil {
		t.Fatal("NewTLSServer modified the plaintext server's TLSConfig")
	}
	if template.Handler != http.Handler(mux) || template.Addr != "127.0.0.1:2261" {
		t.Fatal("NewTLSServer modified the plaintext server's handler or address")
	}

	// TLS 1.2 floor and AEAD-only suites.
	if got := srv.TLSConfig.MinVersion; got != tls.VersionTLS12 {
		t.Fatalf("MinVersion = %#x, want TLS 1.2", got)
	}
	if len(srv.TLSConfig.CipherSuites) == 0 {
		t.Fatal("CipherSuites is empty; Go's defaults would decide the TLS 1.2 list")
	}
	for _, suite := range srv.TLSConfig.CipherSuites {
		name := tls.CipherSuiteName(suite)
		if strings.Contains(name, "CBC") || strings.Contains(name, "RC4") || strings.Contains(name, "3DES") {
			t.Fatalf("weak cipher suite %s offered", name)
		}
		if !strings.Contains(name, "GCM") && !strings.Contains(name, "CHACHA20") {
			t.Fatalf("non-AEAD cipher suite %s offered", name)
		}
	}

	// The handler is wrapped only to add HSTS; the mux still sees the request.
	hstsSeen := false
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		hstsSeen = true
		w.WriteHeader(http.StatusOK)
	})
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if !hstsSeen {
		t.Fatal("wrapped handler did not reach the plaintext handler")
	}
	if got := rec.Header().Get(hstsHeader); got != hstsValue {
		t.Fatalf("HSTS = %q, want %q", got, hstsValue)
	}
}

// TestTLSServerServesSameHandlerOverHTTPS is the acceptance row: a real HTTPS
// listener with a generated certificate serves the admin handler — /healthz
// open, /v1/* still bearer-protected — and answers byte-for-byte like the
// plaintext listener over the same store.
func TestTLSServerServesSameHandlerOverHTTPS(t *testing.T) {
	certFile, keyFile, pool := writeTestCertificate(t)
	handler := New(Config{AdminToken: testToken}, store.New(), slog.New(slog.NewTextHandler(io.Discard, nil)))

	plain := httptest.NewServer(handler)
	t.Cleanup(plain.Close)

	srv, err := NewTLSServer(&http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}, TLSConfig{
		CertFile: certFile,
		KeyFile:  keyFile,
		Listen:   "127.0.0.1:0",
	})
	if err != nil {
		t.Fatalf("NewTLSServer: %v", err)
	}
	addr, _ := startTLSServer(t, srv)
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
	}}

	resp, body := tlsRequest(t, client, http.MethodGet, "https://"+addr+"/healthz", "")
	requireStatus(t, resp, http.StatusOK)
	if got := resp.Header.Get(hstsHeader); got != hstsValue {
		t.Fatalf("HSTS on 200 = %q, want %q", got, hstsValue)
	}
	health := decodeBody[healthResponse](t, body)
	if health.Status != "ok" || health.Service != "ajs-accountd" {
		t.Fatalf("unexpected health payload over TLS: %+v", health)
	}

	// Auth still applies: the transport changed, the middleware did not.
	resp, body = tlsRequest(t, client, http.MethodGet, "https://"+addr+"/v1/accounts", "")
	requireStatus(t, resp, http.StatusUnauthorized)
	requireErrorCode(t, body, CodeUnauthorized)
	if got := resp.Header.Get(hstsHeader); got != hstsValue {
		t.Fatalf("HSTS on 401 = %q, want %q", got, hstsValue)
	}

	resp, body = tlsRequest(t, client, http.MethodGet, "https://"+addr+"/v1/accounts", testToken)
	requireStatus(t, resp, http.StatusOK)
	plainResp, plainBody := doRequest(t, plain, authedRequest(t, http.MethodGet, plain.URL+"/v1/accounts"))
	requireStatus(t, plainResp, http.StatusOK)

	var overTLS, overHTTP any
	if err := json.Unmarshal(body, &overTLS); err != nil {
		t.Fatalf("decode TLS body: %v", err)
	}
	if err := json.Unmarshal(plainBody, &overHTTP); err != nil {
		t.Fatalf("decode plaintext body: %v", err)
	}
	if !reflect.DeepEqual(overTLS, overHTTP) {
		t.Fatalf("TLS and plaintext responses differ:\nTLS:  %s\nHTTP: %s", body, plainBody)
	}
}

// TestTLSServerRejectsPlaintext proves the HTTPS port does not double as a
// plaintext one: an http:// request is answered with the standard 400 instead
// of being served.
func TestTLSServerRejectsPlaintext(t *testing.T) {
	certFile, keyFile, _ := writeTestCertificate(t)
	handler := New(Config{AdminToken: testToken}, store.New(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv, err := NewTLSServer(&http.Server{Handler: handler}, TLSConfig{CertFile: certFile, KeyFile: keyFile, Listen: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("NewTLSServer: %v", err)
	}
	addr, _ := startTLSServer(t, srv)

	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatalf("plaintext request to the TLS port: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read plaintext response: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("plaintext request status = %d, want 400 (body %s)", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "HTTPS") {
		t.Fatalf("plaintext response %q does not identify the HTTPS listener", body)
	}
}

// TestTLSServerRequiresTLS12 pins the protocol floor: TLS 1.2 completes, TLS
// 1.1 is refused during the handshake.
func TestTLSServerRequiresTLS12(t *testing.T) {
	certFile, keyFile, _ := writeTestCertificate(t)
	handler := New(Config{AdminToken: testToken}, store.New(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv, err := NewTLSServer(&http.Server{Handler: handler}, TLSConfig{CertFile: certFile, KeyFile: keyFile, Listen: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("NewTLSServer: %v", err)
	}
	addr, _ := startTLSServer(t, srv)

	conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true, MaxVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatalf("TLS 1.2 handshake: %v", err)
	}
	conn.Close()

	conn, err = tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true, MaxVersion: tls.VersionTLS11})
	if err == nil {
		conn.Close()
		t.Fatal("TLS 1.1 handshake succeeded; the listener's floor is not 1.2")
	}
}

// TestTLSServerShutdown pins the lifecycle: the listener stops accepting and
// ServeTLS reports ErrServerClosed, so main's shutdown path sees a clean exit.
func TestTLSServerShutdown(t *testing.T) {
	certFile, keyFile, pool := writeTestCertificate(t)
	handler := New(Config{AdminToken: testToken}, store.New(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv, err := NewTLSServer(&http.Server{Handler: handler}, TLSConfig{CertFile: certFile, KeyFile: keyFile, Listen: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("NewTLSServer: %v", err)
	}
	addr, errCh := startTLSServer(t, srv)
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
	}}

	resp, _ := tlsRequest(t, client, http.MethodGet, "https://"+addr+"/healthz", "")
	requireStatus(t, resp, http.StatusOK)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if err := <-errCh; !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("ServeTLS after Shutdown = %v, want http.ErrServerClosed", err)
	}

	if _, err := client.Get("https://" + addr + "/healthz"); err == nil {
		t.Fatal("request after Shutdown succeeded")
	}
}

// writeTestCertificate generates a self-signed server certificate for
// 127.0.0.1/localhost, writes the PEM pair into t.TempDir and returns the
// files plus the pool a client needs to trust it.
func writeTestCertificate(t *testing.T) (certFile, keyFile string, pool *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "ajs-tls-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}

	dir := t.TempDir()
	certFile = filepath.Join(dir, "fullchain.pem")
	keyFile = filepath.Join(dir, "privkey.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(certFile, certPEM, 0o600); err != nil {
		t.Fatalf("write certificate: %v", err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	pool = x509.NewCertPool()
	if !pool.AppendCertsFromPEM(certPEM) {
		t.Fatal("append test certificate to pool")
	}
	return certFile, keyFile, pool
}

// startTLSServer serves srv on an ephemeral loopback port and returns the
// address and the ServeTLS result channel. The listener is closed with the
// test, so a test that already shut it down still cleans up.
func startTLSServer(t *testing.T, srv *http.Server) (string, <-chan error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv.Addr = ln.Addr().String()
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ServeTLS(ln, "", "") }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().String(), errCh
}

// tlsRequest issues one request over the test client and returns the response
// with its body; token is sent as the bearer credential when non-empty.
func tlsRequest(t *testing.T, client *http.Client, method, url, token string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		resp.Body.Close()
		t.Fatalf("read response: %v", err)
	}
	resp.Body.Close()
	return resp, body
}

// authedRequest is newRequest with the admin bearer token attached.
func authedRequest(t *testing.T, method, url string) *http.Request {
	t.Helper()
	req := newRequest(t, method, url, nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	return req
}
