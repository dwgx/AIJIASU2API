//go:build windows

package health

import (
	"errors"
	"syscall"
)

// Windows socket error codes. Go's syscall package exposes invented
// ECONNREFUSED/EHOSTUNREACH values that never equal the Winsock errno a dial
// actually returns, so the codes are checked directly.
const (
	wsaECONNREFUSED = syscall.Errno(10061)
	wsaEHOSTUNREACH = syscall.Errno(10065)
	wsaENETUNREACH  = syscall.Errno(10051)
)

// platformClass maps a platform socket error to a probe error class, or ""
// when the error is not a socket address failure.
func platformClass(err error) string {
	switch {
	case errors.Is(err, wsaECONNREFUSED):
		return ErrorRefused
	case errors.Is(err, wsaEHOSTUNREACH), errors.Is(err, wsaENETUNREACH):
		return ErrorUnreachable
	}
	return ""
}
