package account

import "log/slog"

// redacted replaces a Secret in every formatted and serialized form.
const redacted = "[redacted]"

// Secret is an account credential: a login token, a connect password or a
// plaintext account password.
//
// The zero value is an empty secret. Formatting a Secret through fmt with any
// verb (including %#v), logging it through slog, or encoding it as JSON yields
// "[redacted]", so a Secret can live in a struct that is logged or marshalled
// without leaking its value. Reveal is the only way out and must be called only
// where the credential is put on the wire.
type Secret string

// Reveal returns the credential. Callers must not log, print or persist the
// result.
func (s Secret) Reveal() string { return string(s) }

// String implements fmt.Stringer.
func (s Secret) String() string { return redacted }

// GoString implements fmt.GoStringer, so %#v is redacted too.
func (s Secret) GoString() string { return redacted }

// MarshalJSON encodes the placeholder instead of the credential. Secrets are
// not part of the daemon's JSON wire contract.
func (s Secret) MarshalJSON() ([]byte, error) { return []byte(`"[redacted]"`), nil }

// LogValue implements slog.LogValuer.
func (s Secret) LogValue() slog.Value { return slog.StringValue(redacted) }
