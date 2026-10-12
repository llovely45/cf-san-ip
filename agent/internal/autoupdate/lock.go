package autoupdate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

var ErrUpdateInProgress = errors.New("cf-san update is already in progress")

func WithUpdateLock(path string, fn func() error) error {
	return withUpdateLock(path, fn, false)
}

// WithUpdateLockWait acquires the shared update lock in blocking mode. The
// scanner's child process uses this mode so a restart launched while the
// updater still owns the lock waits for the parent to finish instead of
// exiting with ErrUpdateInProgress.
func WithUpdateLockWait(path string, fn func() error) error {
	return withUpdateLock(path, fn, true)
}

func withUpdateLock(path string, fn func() error, wait bool) error {
	if fn == nil {
		return errors.New("update lock callback must not be nil")
	}
	if path == "" {
		return errors.New("update lock path must not be empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("cannot create update lock directory: %w", err)
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("cannot open update lock: %w", err)
	}
	defer file.Close()
	flags := syscall.LOCK_EX
	if !wait {
		flags |= syscall.LOCK_NB
	}
	if err := syscall.Flock(int(file.Fd()), flags); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return ErrUpdateInProgress
		}
		return fmt.Errorf("cannot acquire update lock: %w", err)
	}
	defer func() { _ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN) }()
	if err := file.Chmod(0o600); err != nil {
		return fmt.Errorf("cannot protect update lock: %w", err)
	}
	return fn()
}
