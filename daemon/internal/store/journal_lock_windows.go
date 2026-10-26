//go:build windows

package store

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// Windows exclusive byte-range lock over the first byte of state.lock. The
// lock is advisory at the OS level but mandatory for every Journal: a second
// opener's LockFileEx fails immediately with ERROR_LOCK_VIOLATION, and a
// crashed process releases the range when the handle closes.
const (
	journalLockFailImmediately = 0x00000001 // LOCKFILE_FAIL_IMMEDIATELY
	journalLockExclusive       = 0x00000002 // LOCKFILE_EXCLUSIVE_LOCK

	errorLockViolation syscall.Errno = 33
)

var (
	journalKernel32     = syscall.NewLazyDLL("kernel32.dll")
	journalLockFileEx   = journalKernel32.NewProc("LockFileEx")
	journalUnlockFileEx = journalKernel32.NewProc("UnlockFileEx")
)

type journalLock struct {
	file *os.File
}

func acquireJournalLock(path string) (*journalLock, error) {
	if err := refuseSymlink(path); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open state lock %s: %w", path, err)
	}
	var overlapped syscall.Overlapped
	ok, _, callErr := journalLockFileEx.Call(
		file.Fd(),
		uintptr(journalLockExclusive|journalLockFailImmediately),
		0,
		1,
		0,
		uintptr(unsafe.Pointer(&overlapped)),
	)
	if ok == 0 {
		_ = file.Close()
		if errors.Is(callErr, errorLockViolation) {
			return nil, fmt.Errorf("%w: %s", ErrStateLocked, path)
		}
		return nil, fmt.Errorf("lock state directory %s: %w", path, callErr)
	}
	return &journalLock{file: file}, nil
}

func (l *journalLock) release() error {
	if l == nil || l.file == nil {
		return nil
	}
	var overlapped syscall.Overlapped
	_, _, _ = journalUnlockFileEx.Call(
		l.file.Fd(),
		0,
		1,
		0,
		uintptr(unsafe.Pointer(&overlapped)),
	)
	err := l.file.Close()
	l.file = nil
	return err
}
