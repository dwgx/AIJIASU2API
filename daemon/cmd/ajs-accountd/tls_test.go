package main

import (
	"strings"
	"testing"

	"ajsaccountd/internal/api"
)

// TestValidateTLSListener pins the two startup rules for the optional HTTPS
// listener: it needs its own address, and AJS_ALLOW_INSECURE=1 may not publish
// it beyond loopback. A disabled listener stays inert.
func TestValidateTLSListener(t *testing.T) {
	remote := api.TLSConfig{CertFile: "/certs/fullchain.pem", KeyFile: "/certs/privkey.pem", Listen: "0.0.0.0:2262"}
	loopback := api.TLSConfig{CertFile: "/certs/fullchain.pem", KeyFile: "/certs/privkey.pem", Listen: "127.0.0.1:2262"}

	cases := []struct {
		name     string
		listen   string
		cfg      api.TLSConfig
		insecure bool
		wantErr  []string
	}{
		{name: "disabled", listen: "0.0.0.0:2261", cfg: api.TLSConfig{}},
		{name: "disabled under insecure", listen: "127.0.0.1:2261", cfg: api.TLSConfig{}, insecure: true},
		{name: "remote tls with bearer auth", listen: "127.0.0.1:2261", cfg: remote},
		{name: "loopback tls with bearer auth", listen: "127.0.0.1:2261", cfg: loopback},
		{
			name:    "same address as the plaintext listener",
			listen:  "0.0.0.0:2262",
			cfg:     remote,
			wantErr: []string{"AJS_TLS_LISTEN", "AJS_LISTEN"},
		},
		{
			name:     "insecure mode refuses a remote tls bind",
			listen:   "127.0.0.1:2261",
			cfg:      remote,
			insecure: true,
			wantErr:  []string{"AJS_ALLOW_INSECURE=1", "AJS_TLS_LISTEN"},
		},
		{
			name:     "insecure mode accepts a loopback tls bind",
			listen:   "127.0.0.1:2261",
			cfg:      loopback,
			insecure: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateTLSListener(tc.listen, tc.cfg, tc.insecure)
			if len(tc.wantErr) == 0 {
				if err != nil {
					t.Fatalf("validateTLSListener = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateTLSListener = nil, want an error naming %v", tc.wantErr)
			}
			for _, want := range tc.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q does not name %s", err, want)
				}
			}
		})
	}
}

// TestInsecureModeStillRefusesNonLoopbackPlaintext pins the S4 guard on the
// listener that always exists: adding an HTTPS listener must not loosen the
// plaintext bind check, and its error must still name AJS_LISTEN.
func TestInsecureModeStillRefusesNonLoopbackPlaintext(t *testing.T) {
	for _, listen := range []string{"0.0.0.0:2261", ":2261", "192.168.11.10:2261", "[::]:2261", ""} {
		err := requireLoopback(listen)
		if err == nil {
			t.Fatalf("AJS_LISTEN=%q: requireLoopback = nil, want an error", listen)
		}
		if !strings.Contains(err.Error(), "AJS_LISTEN") || strings.Contains(err.Error(), "AJS_TLS_LISTEN") {
			t.Fatalf("AJS_LISTEN=%q: error %q names the wrong variable", listen, err)
		}
	}
	if err := requireLoopback("127.0.0.1:2261"); err != nil {
		t.Fatalf("loopback AJS_LISTEN: requireLoopback = %v, want nil", err)
	}
}
