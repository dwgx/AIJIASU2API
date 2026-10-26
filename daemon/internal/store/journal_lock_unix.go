//go:build unix

package store

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// Unix exclusive flock over state.lock. The lock is held for the process
// lifetime and released by Close or by the kernel when the process exits, so a
// crash leaves the file (reused by the next opener) but not the lock.
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
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, fmt.Errorf("%w: %s", ErrStateLocked, path)
		}
		return nil, fmt.Errorf("lock state directory %s: %w", path, err)
	}
	return &journalLock{file: file}, nil
}

func (l *journalLock) release() error {
	if l == nil || l.file == nil {
		return nil
	}
	_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	err := l.file.Close()
	l.file = nil
	return err
}
