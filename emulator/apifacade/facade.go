// Package apifacade re-exports the control API client implemented in
// ajsproxy/internal/api so that sibling modules (the daemon) can use it.
//
// Go's internal-package rule keeps ajsaccountd from importing
// ajsproxy/internal/api directly, so this package mirrors the public surface
// with type aliases and function variables. It carries no logic of its own:
// every symbol is the internal one, so types are identical across the module
// boundary and behaviour is exactly the tested api package.
package apifacade

import "ajsproxy/internal/api"

// DefaultSecret is the signing secret hard-coded in the public-domain OpenAJS
// client. The shipping Linux 4.2.3.0 client does not contain this string; see
// api.DefaultSecret for the evidence and caveat.
const DefaultSecret = api.DefaultSecret

// Protocol types. These are aliases, not new types: values can be passed to
// and from the internal package without conversion.
type (
	// Client is a control API client for POST <host>/client.php.
	Client = api.Client
	// Device holds the common parameters every control API request carries.
	Device = api.Device
	// LoginResult is the Login object of a login/loginSync response.
	LoginResult = api.LoginResult
	// Server is one entry of the Servers map.
	Server = api.Server
	// ServerFV is the par.fv object of a VpnServer.
	ServerFV = api.ServerFV
	// FlexInt is the numeric type the vendor sends as either a bare JSON
	// number or a decimal string (vsid, par.fv.u/p, loc.o); re-exported so
	// daemon tests can build literals against the aliased server structs.
	FlexInt = api.FlexInt
	// ServerPar is the par object of a VpnServer.
	ServerPar = api.ServerPar
	// ServerLoc is one entry of VpnServer.loc.
	ServerLoc = api.ServerLoc
)

// ErrNoLoginToken is returned by authenticated calls before a successful
// login. It is the same value as api.ErrNoLoginToken.
var ErrNoLoginToken = api.ErrNoLoginToken

// RequestShape selects the control request field set. The constants are the
// internal values, so a caller can compare and pass them without conversion.
type RequestShape = api.RequestShape

const (
	// ShapeOpenAJS is the OpenAJS/Android field set (the zero value).
	ShapeOpenAJS = api.ShapeOpenAJS
	// ShapeLinux423 is the captured Linux 4.2.3.0 field set.
	ShapeLinux423 = api.ShapeLinux423
	// Linux423Version is the Linux 4.2.3.0 client version claim.
	Linux423Version = api.Linux423Version
	// LinuxCSEID is the cseid the Linux 4.2.3.0 client sends ("linux").
	LinuxCSEID = api.LinuxCSEID
)

// Functions. Variables rather than wrappers so the facade stays allocation-free
// and the signatures remain the tested ones.
var (
	// NewClient returns a client for the given API base URLs.
	NewClient = api.NewClient
	// DefaultDevice returns the OpenAJS device descriptor with empty IDs.
	DefaultDevice = api.DefaultDevice
	// Linux423Device returns the Linux 4.2.3.0 device descriptor with empty IDs.
	Linux423Device = api.Linux423Device
	// NewDeviceID returns 16 random lowercase hex characters.
	NewDeviceID = api.NewDeviceID
	// NewUID builds the 22-character _uid.
	NewUID = api.NewUID
	// NewLinuxUID returns the 20-hex Linux 4.2.3.0 _uid/cuid.
	NewLinuxUID = api.NewLinuxUID
	// BuildQuery serializes params as a signed request body.
	BuildQuery = api.BuildQuery
	// Sign returns the X-Sig header value for body and timestamp.
	Sign = api.Sign
	// DualHashHex returns the dual hash of data as 16 lowercase hex characters.
	DualHashHex = api.DualHashHex
	// URLEncode percent-encodes a query value the way FVSign.urlEncode does.
	URLEncode = api.URLEncode
)
