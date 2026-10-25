package account

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync/atomic"

	"ajsaccountd/internal/model"
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
	// RequestShape selects the control request field set: ShapeLinux423 for
	// the captured Linux 4.2.3.0 form, ShapeOpenAJS for OpenAJS/Android.
	RequestShape apifacade.RequestShape
	// Transport is the registry name of the carrier that speaks this
	// profile's wire format. Linux 4.2.3.0 runs protocolVersion=2, the binary
	// carrier implemented by fvfacade.HTTPUpgrade ("httpupgrade"). OpenAJS
	// uses its own frame carrier, fvfacade.RawTCP ("rawtcp").
	Transport string
}

// Linux423Binary is the default profile: the shipping Linux 4.2.3.0 client on
// its protocolVersion=2 binary carrier (the HTTPUpgrade implementation),
// OAEP-SHA1, no request signing. ClientVersion and CoreVersion come from
// PROTOCOL.md §Version matrix; the request shape and device claims from the
// AJS-07 captures.
var Linux423Binary = VersionProfile{
	Name:            "linux-4.2.3.0-binary",
	ClientVersion:   LinuxClientVersion,
	CoreVersion:     "20241105-1",
	ProtocolVersion: 2,
	OAEPHash:        "SHA-1",
	SigningMode:     "none",
	RequestShape:    apifacade.ShapeLinux423,
	Transport:       "httpupgrade",
}

// Linux430Binary is the Linux 4.3.0.1 profile. The static analysis
// (research/84-ajs-496/LINUX.md §3, §4.4, §5) found the same protocolVersion=2
// binary carrier, the same OAEP-SHA1 key wrap and the same control endpoint as
// 4.2.3.0, with new version claims. 4.3.0.1 changed X-Sig to a 16-hex dual
// hash, but the canonical field order is [UNKNOWN], so the profile sends
// unsigned requests like Linux423Binary; live acceptance is [UNVERIFIED].
var Linux430Binary = VersionProfile{
	Name:            "linux-4.3.0.1-binary",
	ClientVersion:   Linux430ClientVersion,
	CoreVersion:     "20260525-1",
	ProtocolVersion: 2,
	OAEPHash:        "SHA-1",
	SigningMode:     "none",
	RequestShape:    apifacade.ShapeLinux423,
	Transport:       "httpupgrade",
}

// OpenAJSAndroid is the OpenAJS 4.11.10.0 profile: the OpenAJS raw-TCP frame
// carrier, OAEP-SHA1 and the OpenAJS signing secret. The daemon does not wire
// it (AJS_ACCOUNT_PROFILE accepts only the Linux profiles), but the factory
// and the emulator keep it available as the separate Android profile.
var OpenAJSAndroid = VersionProfile{
	Name:            "openajs-4.11.10.0-rawtcp",
	ClientVersion:   OpenAJSClientVersion,
	CoreVersion:     "20260525-1",
	ProtocolVersion: 1,
	OAEPHash:        "SHA-1",
	SigningMode:     "openajs",
	RequestShape:    apifacade.ShapeOpenAJS,
	Transport:       "rawtcp",
}

// SessionRequest is one FV session to open: the account, the server that
// carries it, the device identity, the login's connect password and the
// profile.
//
// Profile is optional. A zero value selects the factory's profile; any other
// value must match it, so two profiles cannot disagree at runtime.
type SessionRequest struct {
	AccountID string
	// Slot is the account slot the session belongs to. It is optional: the
	// zero key means slot 1, and a non-zero invalid key fails Open.
	Slot            model.SlotKey
	Server          apifacade.Server
	Device          apifacade.Device
	UserName        string
	ConnectPassword Secret
	Profile         VersionProfile
}

// SlotUserName is the vendor tunnel name of one slot: base for slot 1, base#N
// otherwise. An empty base returns "" (the caller falls back to the account
// username first).
func SlotUserName(base string, index int) string {
	base = strings.TrimSpace(base)
	if base == "" {
		return ""
	}
	if index <= 1 {
		return base
	}
	return base + "#" + strconv.Itoa(index)
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
	if req.Slot.Index != 0 && !req.Slot.Valid() {
		return nil, fmt.Errorf("account: session request slot %q is invalid", req.Slot.String())
	}
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
		CUID:        req.Device.CUID,
		CSEID:       req.Device.CSEID,
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

// Liveness is the optional active-liveness view of a FlowSession. Healthy is
// passive: it only reports that the vendor issued a SID, and a closed or kicked
// session keeps it. Liveness answers whether the session can still carry flows,
// which is what the runtime's kick containment needs. The runtime type-asserts,
// so a session implementation without it keeps the passive behaviour.
type Liveness interface {
	Alive() bool
}

// vendorKickMarker is the vendor's data-channel refusal for an account that
// another connection took over: "fv: data channel rejected ... fm=disconnected
// by another connection" (README §AJS_NODE_LIMIT, live 2026-10-23). It is a
// session loss, not a per-flow failure: the vendor no longer keeps this
// identity, so every later flow of the same session fails the same way.
const vendorKickMarker = "disconnected by another connection"

// isVendorKick reports whether one data-channel error is the vendor telling us
// that another connection owns the account.
func isVendorKick(err error) bool {
	return err != nil && strings.Contains(err.Error(), vendorKickMarker)
}

// flowSession adapts *fvfacade.Session to FlowSession.
type flowSession struct {
	sess   *fvfacade.Session
	closed atomic.Bool
	kicked atomic.Bool
}

// Compile-time proof that the adapter satisfies the shared interfaces.
var (
	_ FlowSession = (*flowSession)(nil)
	_ Liveness    = (*flowSession)(nil)
)

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
	conn, err := s.sess.OpenStreamContext(ctx, host, port)
	if isVendorKick(err) {
		s.kicked.Store(true)
	}
	return conn, err
}

// Healthy reports whether the session carries a SID. It is a passive check
// with no I/O; a closed session keeps its SID.
func (s *flowSession) Healthy() bool { return s.SID() != "" }

// Alive reports whether the session can still carry flows. It is a passive
// check with no I/O: it turns false once the session is closed or the vendor
// refused a data channel because another connection took the account. The
// runtime polls it, so a vendor kick leaves the slot out of service within one
// liveness window even though the SID and the local listener survive.
func (s *flowSession) Alive() bool {
	if s == nil || s.sess == nil {
		return false
	}
	return !s.closed.Load() && !s.kicked.Load()
}

func (s *flowSession) Close() error {
	if s == nil || s.sess == nil {
		return nil
	}
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	return s.sess.Close()
}
