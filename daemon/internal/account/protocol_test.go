package account

import (
	"slices"
	"testing"

	"ajsproxy/fvfacade"
)

// Compile-time proof that the facade's aliases satisfy the shared interface.
var _ fvfacade.Transport = fvfacade.RawTCP{}

func TestProtocolSeam(t *testing.T) {
	p := NewProtocol()
	if p == nil {
		t.Fatal("NewProtocol returned nil")
	}
	if !p.Available() {
		t.Fatal("Available() = false, want true")
	}
	if got := p.Transport.Name(); got != "httpupgrade" {
		t.Fatalf("Transport.Name() = %q, want %q", got, "httpupgrade")
	}
	if _, ok := p.Transport.(*fvfacade.HTTPUpgrade); !ok {
		t.Fatalf("Transport = %T, want the protocol-2 binary carrier", p.Transport)
	}
	names := p.Registry.Names()
	if !slices.Contains(names, "httpupgrade") || !slices.Contains(names, "rawtcp") {
		t.Fatalf("Registry.Names() = %v, want both carriers", names)
	}
}

// TestProtocolProfileCarrier is the C3 regression: the Linux 4.2.3.0 profile
// resolves to the protocol-2 binary carrier (httpupgrade), and the OpenAJS
// profile keeps the separate raw-TCP carrier.
func TestProtocolProfileCarrier(t *testing.T) {
	linux, err := NewProtocolForProfile(Linux423Binary)
	if err != nil {
		t.Fatalf("NewProtocolForProfile(Linux423Binary): %v", err)
	}
	upgrade, ok := linux.Transport.(*fvfacade.HTTPUpgrade)
	if !ok {
		t.Fatalf("Linux423Binary carrier = %T, want *fvfacade.HTTPUpgrade", linux.Transport)
	}
	if upgrade.ProtocolVersion != Linux423Binary.ProtocolVersion {
		t.Fatalf("carrier ProtocolVersion = %d, want %d",
			upgrade.ProtocolVersion, Linux423Binary.ProtocolVersion)
	}
	if got := linux.Transport.Name(); got != "httpupgrade" {
		t.Fatalf("Linux423Binary carrier name = %q, want httpupgrade", got)
	}
	if got := linux.Transport.Experimental(); !got {
		t.Fatal("binary carrier reports Experimental() = false")
	}

	openAJS, err := NewProtocolForProfile(OpenAJSAndroid)
	if err != nil {
		t.Fatalf("NewProtocolForProfile(OpenAJSAndroid): %v", err)
	}
	if _, ok := openAJS.Transport.(fvfacade.RawTCP); !ok {
		t.Fatalf("OpenAJSAndroid carrier = %T, want fvfacade.RawTCP", openAJS.Transport)
	}
	if got := openAJS.Transport.Name(); got != "rawtcp" {
		t.Fatalf("OpenAJSAndroid carrier name = %q, want rawtcp", got)
	}

	if _, err := NewProtocolForProfile(VersionProfile{Name: "bogus", Transport: "nope"}); err == nil {
		t.Fatal("NewProtocolForProfile accepted an unknown carrier")
	}
	if _, err := NewProtocolForProfile(VersionProfile{Name: "no-carrier"}); err == nil {
		t.Fatal("NewProtocolForProfile accepted a profile without a transport")
	}
}
