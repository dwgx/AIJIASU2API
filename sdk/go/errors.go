package ajs

import (
	"errors"
	"fmt"
	"net/http"
	"time"
)

// APIError is a non-2xx answer from the daemon. It carries the machine-readable
// code, the optional stable reason and the non-secret details map. The request
// method and path are recorded; headers and bodies are never included, so an
// error can be logged without leaking the bearer token.
type APIError struct {
	StatusCode int
	Code       string
	Message    string
	Reason     string
	Details    map[string]string
	RetryAfter time.Duration
	Method     string
	Path       string
}

// Error implements error.
func (e *APIError) Error() string {
	msg := e.Code
	if msg == "" {
		msg = http.StatusText(e.StatusCode)
	}
	out := fmt.Sprintf("ajs: %s %s: HTTP %d %s", e.Method, e.Path, e.StatusCode, msg)
	if e.Message != "" && e.Message != msg {
		out += ": " + e.Message
	}
	if e.Reason != "" {
		out += " (" + e.Reason + ")"
	}
	return out
}

// Retryable reports whether the status class is transient.
func (e *APIError) Retryable() bool {
	switch e.StatusCode {
	case http.StatusTooManyRequests, http.StatusInternalServerError,
		http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

// IsCode reports whether err is an *APIError with the given code.
func IsCode(err error, code string) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Code == code
}

// Scoped-key denial reasons carried by APIError.Reason. The daemon emits only
// these two values on a scoped-key 403; the message is not part of the contract.
const (
	// ReasonScopeRequired means the key lacks the route's scope.
	ReasonScopeRequired = "scope_required"
	// ReasonScopedKeyRestricted means the key's accountIds/regions grant
	// excludes the target, or a lease/user_id belongs to another user.
	ReasonScopedKeyRestricted = "scoped_key_restricted"
)

// HasReason reports whether err is an *APIError carrying the given reason.
func HasReason(err error, reason string) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Reason == reason
}

// IsScopeRequired reports whether err is a scoped-key 403 for a missing route
// scope (error.reason=scope_required).
func IsScopeRequired(err error) bool { return HasReason(err, ReasonScopeRequired) }

// IsScopedKeyRestricted reports whether err is a scoped-key 403 for an
// account/region grant or lease/user ownership (error.reason=
// scoped_key_restricted).
func IsScopedKeyRestricted(err error) bool { return HasReason(err, ReasonScopedKeyRestricted) }

// NetworkError is a transport failure (DNS, connect, TLS, reset). It wraps the
// underlying error, so errors.Is(err, context.Canceled) still works.
type NetworkError struct {
	Method string
	Path   string
	Err    error
}

// Error implements error.
func (e *NetworkError) Error() string {
	return fmt.Sprintf("ajs: %s %s: %v", e.Method, e.Path, e.Err)
}

// Unwrap exposes the transport cause.
func (e *NetworkError) Unwrap() error { return e.Err }

// OperationFailure is a terminal failed or canceled operation. OperationID is
// always set so a caller can fetch the durable record.
type OperationFailure struct {
	OperationID string
	State       OperationState
	Code        string
	Message     string
	Details     map[string]string
}

// Error implements error.
func (e *OperationFailure) Error() string {
	code := e.Code
	if code == "" {
		code = string(e.State)
	}
	return fmt.Sprintf("ajs: operation %s %s: %s", e.OperationID, e.State, code)
}

// OperationTimeoutError means the local poll budget expired before the
// operation reached a terminal state. The operation may still be running.
type OperationTimeoutError struct {
	OperationID string
	Timeout     time.Duration
}

// Error implements error.
func (e *OperationTimeoutError) Error() string {
	return fmt.Sprintf("ajs: operation %s did not finish within %s", e.OperationID, e.Timeout)
}
