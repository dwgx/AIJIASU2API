//go:build !windows

package health

import (
	"errors"
	"syscall"
)

// platformClass maps a platform socket error to a probe error class, or ""
// when the error is not a socket address failure.
func platformClass(err error) string {
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return ErrorRefused
	case errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH):
		return ErrorUnreachable
	}
	return ""
}
