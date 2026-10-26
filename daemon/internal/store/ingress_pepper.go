package store

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"runtime"
	"strings"
)

// Environment names for the opt-in remote ingress. Ingress stays off unless
// AJS_REMOTE_INGRESS is exactly "1"; the pepper file is required only then, so
// an installation that never enables ingress starts exactly as before.
const (
	IngressEnabledEnv   = "AJS_REMOTE_INGRESS"
	IngressPepperEnv    = "AJS_INGRESS_PEPPER_FILE"
	ingressPepperPerms  = 0o077
	ingressPepperMaxLen = 128
)

// RemoteIngressEnabled resolves AJS_REMOTE_INGRESS. An empty value and "0" mean
// disabled; "1" enables the ingress. Any other value is refused instead of
// being read as "off", because a typo must not silently disable a paid path.
func RemoteIngressEnabled() (bool, error) {
	switch raw := strings.TrimSpace(os.Getenv(IngressEnabledEnv)); raw {
	case "", "0":
		return false, nil
	case "1":
		return true, nil
	default:
		return false, fmt.Errorf("%s must be 0 or 1, got %q", IngressEnabledEnv, raw)
	}
}

// IngressPepperFromEnv resolves the credential pepper for startup. With ingress
// disabled it returns nil and reads nothing. With ingress enabled a missing or
// invalid file is a startup error: the daemon must not begin serving a public
// ingress whose credential verifiers have no key.
func IngressPepperFromEnv() ([]byte, error) {
	enabled, err := RemoteIngressEnabled()
	if err != nil {
		return nil, err
	}
	if !enabled {
		return nil, nil
	}
	path := strings.TrimSpace(os.Getenv(IngressPepperEnv))
	if path == "" {
		return nil, fmt.Errorf("%s=1 requires %s", IngressEnabledEnv, IngressPepperEnv)
	}
	return LoadIngressPepper(path)
}

// LoadIngressPepper reads one 32-byte HMAC key from path. The file may hold the
// raw bytes, 64 hex characters or the base64/base64url encoding of 32 bytes;
// trailing whitespace is ignored for the encoded forms only, so a raw key whose
// last byte happens to be whitespace is not corrupted. On Unix the file must
// not be readable or writable by group or other, which is the 0600 deployment
// contract; Windows has no equivalent mode and skips that check.
func LoadIngressPepper(path string) ([]byte, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("%s is empty", IngressPepperEnv)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("ingress pepper file %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("ingress pepper file %s is not a regular file", path)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&ingressPepperPerms != 0 {
		return nil, fmt.Errorf("ingress pepper file %s must not be readable or writable by group or other (want 0600)", path)
	}
	if info.Size() > ingressPepperMaxLen {
		return nil, fmt.Errorf("ingress pepper file %s is too large (%d bytes)", path, info.Size())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read ingress pepper file %s: %w", path, err)
	}
	pepper, err := decodeIngressPepper(data)
	if err != nil {
		return nil, fmt.Errorf("ingress pepper file %s: %w", path, err)
	}
	return pepper, nil
}

// decodeIngressPepper accepts exactly 32 bytes in one of the documented
// encodings. The raw form is tried first so a binary key is never trimmed.
func decodeIngressPepper(data []byte) ([]byte, error) {
	if len(data) == CredentialPepperBytes {
		return append([]byte(nil), data...), nil
	}
	text := bytes.TrimRight(data, " \t\r\n")
	if len(text) == 2*CredentialPepperBytes {
		if decoded, err := hex.DecodeString(string(text)); err == nil {
			return decoded, nil
		}
	}
	for _, encoding := range []*base64.Encoding{
		base64.RawURLEncoding,
		base64.URLEncoding,
		base64.RawStdEncoding,
		base64.StdEncoding,
	} {
		decoded, err := encoding.DecodeString(string(text))
		if err == nil && len(decoded) == CredentialPepperBytes {
			return decoded, nil
		}
	}
	return nil, fmt.Errorf("must hold %d bytes as raw, hex or base64; got %d bytes",
		CredentialPepperBytes, len(data))
}
