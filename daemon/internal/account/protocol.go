// Package account owns the daemon's vendor-facing account layer. This file is
// only the compile-tested seam to the emulator's FV protocol core; the account
// client and session factory land on top of it later.
package account

import (
	"fmt"
	"strings"

	"ajsproxy/fvfacade"
)

// Protocol is the daemon-side handle on the FV protocol surface exposed by the
// emulator module. It holds the default transport registry and the carrier the
// profile declares; it performs no I/O.
type Protocol struct {
	Registry  *fvfacade.Registry
	Transport fvfacade.Transport
}

// NewProtocol wires the default FV registry and the carrier the default
// profile declares: the Linux 4.2.3.0 protocol-2 binary carrier, which is
// fvfacade.HTTPUpgrade (registry name "httpupgrade"). It does not return the
// OpenAJS raw-TCP transport; use NewProtocolForProfile(OpenAJSAndroid) for the
// separate Android carrier.
func NewProtocol() *Protocol {
	protocol, err := NewProtocolForProfile(Linux423Binary)
	if err != nil {
		// The default profile is compiled in, so a lookup failure is a build
		// error. Available reports it instead of panicking at startup.
		return &Protocol{Registry: fvfacade.DefaultRegistry()}
	}
	return protocol
}

// NewProtocolForProfile returns the default registry and the carrier the
// profile's Transport field names. When that carrier is fvfacade.HTTPUpgrade,
// the profile's ProtocolVersion is applied to it, so a profile that claims the
// binary carrier cannot silently get the legacy text carrier (or the reverse).
func NewProtocolForProfile(profile VersionProfile) (*Protocol, error) {
	registry := fvfacade.DefaultRegistry()
	name := strings.TrimSpace(profile.Transport)
	if name == "" {
		return nil, fmt.Errorf("account: profile %q has no transport", profile.Name)
	}
	transport, err := registry.Get(name)
	if err != nil {
		return nil, fmt.Errorf("account: profile %q: %w", profile.Name, err)
	}
	if upgrade, ok := transport.(*fvfacade.HTTPUpgrade); ok && profile.ProtocolVersion > 0 {
		upgrade.ProtocolVersion = profile.ProtocolVersion
	}
	return &Protocol{Registry: registry, Transport: transport}, nil
}

// Available reports whether the FV facade is linked and the configured
// transport is registered. It is the seam's liveness check, not a network
// probe.
func (p *Protocol) Available() bool {
	if p == nil || p.Registry == nil || p.Transport == nil {
		return false
	}
	_, err := p.Registry.Get(p.Transport.Name())
	return err == nil
}
