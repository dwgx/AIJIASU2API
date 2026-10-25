package api

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"strings"
)

// Environment variables of the optional HTTPS admin listener (AJS-276). The
// plaintext AJS_LISTEN listener keeps its address and behaviour; this is a
// second, opt-in socket over the same handler for remote administration.
const (
	EnvTLSCert   = "AJS_TLS_CERT"
	EnvTLSKey    = "AJS_TLS_KEY"
	EnvTLSListen = "AJS_TLS_LISTEN"
)

// DefaultTLSListen is the HTTPS admin address when AJS_TLS_LISTEN is unset.
// It differs from the plaintext default so both listeners can run on one host.
const DefaultTLSListen = "127.0.0.1:2262"

// The TLS listener sends HSTS on every response, errors included: a browser
// that sees one response without it may fall back to http:// on the next
// request. A year, without includeSubDomains — the admin API is usually one
// host name and the daemon must not pin unrelated sibling subdomains of the
// operator's domain. Preload is intentionally absent.
const (
	hstsHeader = "Strict-Transport-Security"
	hstsValue  = "max-age=31536000"
)

// TLSConfig is the optional HTTPS admin listener configuration. The zero
// value means disabled: no certificate, no socket.
type TLSConfig struct {
	// CertFile and KeyFile are the PEM certificate chain and its private key.
	// They are set together or not at all.
	CertFile string
	KeyFile  string
	// Listen is the HTTPS address; DefaultTLSListen when AJS_TLS_LISTEN is
	// unset. It is meaningless while the pair is empty.
	Listen string
}

// Enabled reports whether a certificate pair was configured. A listen address
// alone never enables TLS.
func (c TLSConfig) Enabled() bool {
	return c.CertFile != "" && c.KeyFile != ""
}

// ParseTLSConfig reads the three TLS variables through getenv (os.Getenv in
// the daemon, a map in tests). The listener is off unless both AJS_TLS_CERT
// and AJS_TLS_KEY are set; a partial configuration fails instead of silently
// serving plaintext or picking up an unrelated certificate. AJS_TLS_LISTEN
// alone is partial too: the operator asked for TLS and left out the pair.
// Surrounding whitespace is ignored, so an empty .env row reads as unset.
func ParseTLSConfig(getenv func(string) string) (TLSConfig, error) {
	cert := strings.TrimSpace(getenv(EnvTLSCert))
	key := strings.TrimSpace(getenv(EnvTLSKey))
	listen := strings.TrimSpace(getenv(EnvTLSListen))

	cfg := TLSConfig{CertFile: cert, KeyFile: key, Listen: listen}
	switch {
	case cert == "" && key == "":
		if listen != "" {
			return TLSConfig{}, fmt.Errorf("%s is set but %s and %s are not: the HTTPS listener needs both", EnvTLSListen, EnvTLSCert, EnvTLSKey)
		}
		cfg.Listen = DefaultTLSListen
		return cfg, nil
	case cert == "":
		return TLSConfig{}, fmt.Errorf("%s is set but %s is not: the HTTPS listener needs both", EnvTLSKey, EnvTLSCert)
	case key == "":
		return TLSConfig{}, fmt.Errorf("%s is set but %s is not: the HTTPS listener needs both", EnvTLSCert, EnvTLSKey)
	}
	if cfg.Listen == "" {
		cfg.Listen = DefaultTLSListen
	}
	return cfg, nil
}

// tls12CipherSuites is the TLS 1.2 suite list. TLS 1.3 suites are not
// configurable in Go and are always the AEAD set. Only ECDHE key exchange with
// AEAD ciphers is offered: no CBC, no static RSA, no RC4 or 3DES.
var tls12CipherSuites = []uint16{
	tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
	tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
	tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
	tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
	tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
	tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
}

// NewTLSServer returns the HTTPS admin listener. It serves the same handler as
// the plaintext listener — auth, routes and request logging are unchanged —
// wrapped only to add HSTS, and copies the template's timeouts so the two
// listeners cannot drift. The certificate pair is loaded here, before any
// socket is bound: an unreadable, mismatched or non-PEM pair aborts startup
// with an error that names the files, never their contents.
//
// The template is not modified: its Addr, Handler and TLSConfig stay as they
// are, so the plaintext server keeps serving loopback traffic. TLS 1.2 is the
// floor; 1.3 is preferred.
func NewTLSServer(template *http.Server, cfg TLSConfig) (*http.Server, error) {
	if !cfg.Enabled() {
		return nil, fmt.Errorf("%s and %s are not set: the HTTPS listener is disabled", EnvTLSCert, EnvTLSKey)
	}
	certificate, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("tls: load certificate pair (%s, %s): %w", cfg.CertFile, cfg.KeyFile, err)
	}
	handler := template.Handler
	if handler == nil {
		handler = http.DefaultServeMux
	}
	return &http.Server{
		Addr:              cfg.Listen,
		Handler:           withHSTS(handler),
		TLSConfig:         tlsServerConfig(certificate),
		ReadHeaderTimeout: template.ReadHeaderTimeout,
		ReadTimeout:       template.ReadTimeout,
		WriteTimeout:      template.WriteTimeout,
		IdleTimeout:       template.IdleTimeout,
		MaxHeaderBytes:    template.MaxHeaderBytes,
		ErrorLog:          template.ErrorLog,
	}, nil
}

// tlsServerConfig is the handshake policy of the admin listener. MinVersion
// keeps TLS 1.0/1.1 off the wire; the explicit suite list keeps CBC and static
// RSA out of TLS 1.2.
func tlsServerConfig(certificate tls.Certificate) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{certificate},
		MinVersion:   tls.VersionTLS12,
		CipherSuites: tls12CipherSuites,
	}
}

// withHSTS adds Strict-Transport-Security to every response of the TLS
// listener. It wraps the plaintext handler without changing routing or auth.
func withHSTS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(hstsHeader, hstsValue)
		next.ServeHTTP(w, r)
	})
}
