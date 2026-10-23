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
	// ServerPar is the par object of a VpnServer.
	ServerPar = api.ServerPar
	// ServerLoc is one entry of VpnServer.loc.
	ServerLoc = api.ServerLoc
)

// ErrNoLoginToken is returned by authenticated calls before a successful
// login. It is the same value as api.ErrNoLoginToken.
var ErrNoLoginToken = api.ErrNoLoginToken

// Functions. Variables rather than wrappers so the facade stays allocation-free
// and the signatures remain the tested ones.
var (
	// NewClient returns a client for the given API base URLs.
	NewClient = api.NewClient
	// DefaultDevice returns the OpenAJS device descriptor with empty IDs.
	DefaultDevice = api.DefaultDevice
	// NewDeviceID returns 16 random lowercase hex characters.
	NewDeviceID = api.NewDeviceID
	// NewUID builds the 22-character _uid.
	NewUID = api.NewUID
	// BuildQuery serializes params as a signed request body.
	BuildQuery = api.BuildQuery
	// Sign returns the X-Sig header value for body and timestamp.
	Sign = api.Sign
	// DualHashHex returns the dual hash of data as 16 lowercase hex characters.
	DualHashHex = api.DualHashHex
	// URLEncode percent-encodes a query value the way FVSign.urlEncode does.
	URLEncode = api.URLEncode
)
