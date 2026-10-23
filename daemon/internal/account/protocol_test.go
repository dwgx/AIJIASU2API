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
	if got := p.Transport.Name(); got != "rawtcp" {
		t.Fatalf("Transport.Name() = %q, want %q", got, "rawtcp")
	}
	if names := p.Registry.Names(); !slices.Contains(names, "rawtcp") {
		t.Fatalf("Registry.Names() = %v, want rawtcp", names)
	}
}
