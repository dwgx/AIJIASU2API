package account

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync/atomic"

	"ajsproxy/apifacade"
	"ajsproxy/fvfacade"
)

// VersionProfile is one FV client profile: the version claims sent in the
// control API and the FV auth body, plus the carrier the profile uses. A
// Factory is fixed to one profile at construction.
type VersionProfile struct {
	// Name identifies the profile, e.g. "linux-4.2.3.0-binary".
	Name string
	// ClientVersion is _v: the control API client version claim.
	ClientVersion string
	// CoreVersion is cv: the core build stamp sent in the FV auth body.
	CoreVersion string
	// ProtocolVersion is the client's protocolVersion: 2 selects the binary
	// carrier, values below 2 the legacy text frame.
	ProtocolVersion int
	// OAEPHash is the RSA-OAEP digest name: "SHA-1" or "SHA-256".
	OAEPHash string
	// SigningMode is the control API signing mode: "none" or "openajs". The
	// FV auth body carries no signature, so the session factory does not use
	// it.
	SigningMode string
	// Transport is the carrier label recorded for the profile. The Linux
	// 4.2.3.0 capture names plain TCP with no HTTP request line ("rawtcp").
	// The concrete transport is injected into NewFactory, because the
	// protocol-2 binary carrier is implemented by fvfacade.HTTPUpgrade.
	Transport string
}

// Linux423Binary is the default profile: the shipping Linux 4.2.3.0 client on
// its protocolVersion=2 binary carrier, OAEP-SHA1, no request signing, plain
// TCP. ClientVersion and CoreVersion come from PROTOCOL.md §Version matrix;
// the rest from the AJS-07 capture.
var Linux423Binary = VersionProfile{
	Name:            "linux-4.2.3.0-binary",
	ClientVersion:   LinuxClientVersion,
	CoreVersion:     "20241105-1",
	ProtocolVersion: 2,
	OAEPHash:        "SHA-1",
	SigningMode:     "none",
	Transport:       "rawtcp",
}

// SessionRequest is one FV session to open: the account, the server that
// carries it, the device identity, the login's connect password and the
// profile.
//
// Profile is optional. A zero value selects the factory's profile; any other
// value must match it, so two profiles cannot disagree at runtime.
type SessionRequest struct {
	AccountID       string
	Server          apifacade.Server
	Device          apifacade.Device
	UserName        string
	ConnectPassword Secret
	Profile         VersionProfile
}

// FlowSession is one authenticated FV session and its data channels. It is
// safe for concurrent use: every OpenFlow opens an independent data
// connection.
type FlowSession interface {
	// SID is the vendor session id, empty when the server did not issue one.
	SID() string
	// Fields returns the merged auth response fields. The map belongs to the
	// session and must not be modified.
	Fields() map[string]string
	// OpenFlow opens one proxied TCP flow to host:port.
	OpenFlow(ctx context.Context, host string, port int) (net.Conn, error)
	// Healthy reports whether the session carries a SID. It is a passive
	// check and performs no I/O.
	Healthy() bool
	// Close closes the control connection. It is idempotent.
	Close() error
}

// Factory opens FV sessions for one version profile through one transport.
// The profile and transport are fixed at construction. A Factory holds no
// per-session state and is safe for concurrent use.
type Factory struct {
	profile   VersionProfile
	transport fvfacade.Transport
	opts      fvfacade.AuthOptions
}

// NewFactory builds a session factory. transport is the carrier
// implementation (for the protocol-2 binary carrier, &fvfacade.HTTPUpgrade{});
// opts overrides the crypto and timeout defaults. An OAEPHash in opts wins
// over the profile's hash; leave it zero to use the profile.
func NewFactory(profile VersionProfile, transport fvfacade.Transport, opts fvfacade.AuthOptions) *Factory {
	return &Factory{profile: profile, transport: transport, opts: opts}
}

// Profile returns the profile the factory opens sessions for.
func (f *Factory) Profile() VersionProfile {
	if f == nil {
		return VersionProfile{}
	}
	return f.profile
}

// Open authenticates one FV session against req.Server's FV endpoint and
// returns it wrapped as a FlowSession. The connect password is put on the wire
// and is never logged.
func (f *Factory) Open(ctx context.Context, req SessionRequest) (FlowSession, error) {
	if f == nil || f.transport == nil {
		return nil, errors.New("account: session factory has no transport")
	}
	if req.Profile != (VersionProfile{}) && req.Profile != f.profile {
		return nil, fmt.Errorf("account: request profile %q does not match factory profile %q",
			req.Profile.Name, f.profile.Name)
	}
	if f.profile.ClientVersion == "" {
		return nil, errors.New("account: profile has no client version")
	}
	addr := req.Server.Addr()
	if addr == "" {
		return nil, fmt.Errorf("account: server %q has no FV endpoint (svr/par.fv.p)", req.Server.ID)
	}
	if req.UserName == "" {
		return nil, errors.New("account: session request has no user name")
	}
	if req.ConnectPassword == "" {
		return nil, errors.New("account: session request has no connect password")
	}
	if req.Device.DeviceID == "" {
		return nil, errors.New("account: session request has no device id")
	}
	hash, err := fvfacade.HashByName(f.profile.OAEPHash)
	if err != nil {
		return nil, fmt.Errorf("account: profile %q: %w", f.profile.Name, err)
	}
	opts := f.opts
	if opts.OAEPHash == 0 {
		opts.OAEPHash = hash
	}
	site := req.Device.Site
	if site == "" {
		site = "ajs"
	}
	cfg := fvfacade.AuthConfig{
		User:        req.UserName,
		Password:    req.ConnectPassword.Reveal(),
		DeviceID:    req.Device.DeviceID,
		Site:        site,
		AppVersion:  f.profile.ClientVersion,
		CoreVersion: f.profile.CoreVersion,
		OSP:         req.Device.Platform,
		OSD:         req.Device.OSDevice,
		OSV:         req.Device.OSVersion,
		Dim:         req.Device.OSDim,
	}
	sess, err := fvfacade.Authenticate(ctx, cfg, addr, f.transport, opts)
	if err != nil {
		return nil, err
	}
	return &flowSession{sess: sess}, nil
}

// flowSession adapts *fvfacade.Session to FlowSession.
type flowSession struct {
	sess   *fvfacade.Session
	closed atomic.Bool
}

// Compile-time proof that the adapter satisfies the shared interface.
var _ FlowSession = (*flowSession)(nil)

func (s *flowSession) SID() string {
	if s == nil || s.sess == nil {
		return ""
	}
	return s.sess.SID
}

func (s *flowSession) Fields() map[string]string {
	if s == nil || s.sess == nil {
		return nil
	}
	return s.sess.Fields
}

func (s *flowSession) OpenFlow(ctx context.Context, host string, port int) (net.Conn, error) {
	if s == nil || s.sess == nil {
		return nil, errors.New("account: nil flow session")
	}
	if s.closed.Load() {
		return nil, errors.New("account: flow session is closed")
	}
	return s.sess.OpenStreamContext(ctx, host, port)
}

// Healthy reports whether the session carries a SID. It is a passive check
// with no I/O; a closed session keeps its SID.
func (s *flowSession) Healthy() bool { return s.SID() != "" }

func (s *flowSession) Close() error {
	if s == nil || s.sess == nil {
		return nil
	}
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	return s.sess.Close()
}
