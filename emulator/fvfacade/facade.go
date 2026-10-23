// Package fvfacade re-exports the FV protocol core implemented in
// ajsproxy/internal/fv so that sibling modules (the daemon) can use it.
//
// Go's internal-package rule keeps ajsaccountd from importing
// ajsproxy/internal/fv directly, so this package mirrors the public surface
// with type aliases and function variables. It carries no logic of its own:
// every symbol is the internal one, so types are identical across the module
// boundary and behaviour is exactly the tested fv package.
package fvfacade

import "ajsproxy/internal/fv"

// Wire constants. Aliases of the internal values so a peer (including an
// in-process test server) can frame the carrier without duplicating magic
// numbers.
const (
	// SessionKeySize is the AES-128 key length used for the FV session key.
	SessionKeySize = fv.SessionKeySize
	// IVSize is the AES-CTR IV length, equal to the AES block size.
	IVSize = fv.IVSize
	// HeaderSize is the encrypted fixed header: 2 reserved bytes followed by
	// the big-endian uint16 text length.
	HeaderSize = fv.HeaderSize
	// OAEPBlockMagic prefixes the RSA ciphertext in the k= field.
	OAEPBlockMagic = fv.OAEPBlockMagic
)

// Protocol types. These are aliases, not new types: values can be passed to
// and from the internal package without conversion.
type (
	// Session is an authenticated FV control connection.
	Session = fv.Session
	// Transport opens FV control and per-flow data connections.
	Transport = fv.Transport
	// Registry maps transport names to implementations.
	Registry = fv.Registry
	// AuthConfig is the field set of the FV authentication body.
	AuthConfig = fv.AuthConfig
	// AuthOptions overrides the crypto and timeout defaults of Authenticate.
	AuthOptions = fv.AuthOptions
	// AuthError is a rejected session response.
	AuthError = fv.AuthError
	// RawTCP is the fully implemented OpenAJS raw-TCP transport (variant A).
	RawTCP = fv.RawTCP
	// HTTPUpgrade is the binary/legacy HTTP-upgrade transport (variant B).
	HTTPUpgrade = fv.HTTPUpgrade
	// OPV2Packet is one decoded legacy OPV2 frame.
	OPV2Packet = fv.OPV2Packet
)

// Functions. Variables rather than wrappers so the facade stays allocation-free
// and the signatures remain the tested ones.
var (
	// Authenticate performs the FV authentication exchange over a transport.
	Authenticate = fv.Authenticate
	// DefaultRegistry returns the registry with the implemented transports.
	DefaultRegistry = fv.DefaultRegistry
	// NewSessionKey returns a fresh 16-byte AES-128 session key.
	NewSessionKey = fv.NewSessionKey
	// NewIV returns a fresh 16-byte IV.
	NewIV = fv.NewIV
	// BuildAuthBody serializes the OpenAJS authentication body.
	BuildAuthBody = fv.BuildAuthBody
	// BuildBinaryAuthBody serializes the Linux binary-carrier auth body.
	BuildBinaryAuthBody = fv.BuildBinaryAuthBody
	// EncodeFrame builds a client-to-server frame.
	EncodeFrame = fv.EncodeFrame
	// DecodeHeader decrypts a server-form frame header.
	DecodeHeader = fv.DecodeHeader
	// ParseFields parses newline-delimited key=value lines.
	ParseFields = fv.ParseFields
	// ParseResponse decodes an FV response frame into merged fields.
	ParseResponse = fv.ParseResponse
	// Socks5ConnectRequest builds the SOCKS5 CONNECT request carried in a flow.
	Socks5ConnectRequest = fv.Socks5ConnectRequest
	// VendorPublicKey returns the vendor RSA public key.
	VendorPublicKey = fv.VendorPublicKey
	// HashByName maps an OAEP hash name to crypto.Hash.
	HashByName = fv.HashByName
	// RSAOAEPEncrypt encrypts with RSA-OAEP.
	RSAOAEPEncrypt = fv.RSAOAEPEncrypt
	// AESCTR encrypts or decrypts with AES-128-CTR.
	AESCTR = fv.AESCTR
	// NewCTR returns a streaming AES-128-CTR cipher.Stream. Unlike AESCTR it
	// keeps its position, so a relay can continue the keystream across reads.
	NewCTR = fv.NewCTR
	// RandomBytes returns n cryptographically random bytes.
	RandomBytes = fv.RandomBytes
	// BuildDataText builds the data-channel frame text.
	BuildDataText = fv.BuildDataText
	// BuildAuthText builds the authentication frame text.
	BuildAuthText = fv.BuildAuthText
	// RandomPad returns a random _ padding value.
	RandomPad = fv.RandomPad
	// ParseRSAPublicKey builds an RSA public key from decimal modulus and exponent.
	ParseRSAPublicKey = fv.ParseRSAPublicKey
)
