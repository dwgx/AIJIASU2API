package fv

import (
	"errors"
	"testing"
)

func TestTransportRegistry(t *testing.T) {
	reg := DefaultRegistry()

	raw, err := reg.Get("rawtcp")
	if err != nil {
		t.Fatalf("Get(rawtcp): %v", err)
	}
	if raw.Name() != "rawtcp" {
		t.Fatalf("rawtcp name = %q", raw.Name())
	}
	if raw.Experimental() {
		t.Fatal("rawtcp must not be marked experimental")
	}

	up, err := reg.Get("HTTPUpgrade")
	if err != nil {
		t.Fatalf("Get(HTTPUpgrade): %v", err)
	}
	if up.Name() != "httpupgrade" {
		t.Fatalf("httpupgrade name = %q", up.Name())
	}
	if !up.Experimental() {
		t.Fatal("httpupgrade must be marked experimental")
	}

	if _, err := reg.Get("nope"); !errors.Is(err, ErrUnknownTransport) {
		t.Fatalf("Get(nope) error = %v, want ErrUnknownTransport", err)
	}

	names := reg.Names()
	if len(names) != 2 || names[0] != "httpupgrade" || names[1] != "rawtcp" {
		t.Fatalf("Names() = %v, want [httpupgrade rawtcp]", names)
	}

	if err := reg.Register(RawTCP{}); err == nil {
		t.Fatal("duplicate registration was accepted")
	}
	if err := reg.Register(nil); err == nil {
		t.Fatal("nil transport was accepted")
	}
}
