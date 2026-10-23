// Package account owns the daemon's vendor-facing account layer. This file is
// only the compile-tested seam to the emulator's FV protocol core; the account
// client and session factory land on top of it later.
package account

import "ajsproxy/fvfacade"

// Protocol is the daemon-side handle on the FV protocol surface exposed by the
// emulator module. It holds the default transport registry and the raw-TCP
// transport; it performs no I/O.
type Protocol struct {
	Registry  *fvfacade.Registry
	Transport fvfacade.Transport
}

// NewProtocol wires the default FV registry and the raw-TCP transport.
func NewProtocol() *Protocol {
	return &Protocol{
		Registry:  fvfacade.DefaultRegistry(),
		Transport: fvfacade.RawTCP{},
	}
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
