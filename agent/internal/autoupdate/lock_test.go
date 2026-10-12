package autoupdate

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWithUpdateLockCreatesPrivateParentAndLockFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "update.lock")

	if err := WithUpdateLock(path, func() error {
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("lock file mode = %o, want 600", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Fatalf("lock parent directory was not created: %v", err)
	}
}

func TestWithUpdateLockRejectsOverlappingUpdater(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update.lock")
	entered := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- WithUpdateLock(path, func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered

	var callbackRan bool
	secondErr := WithUpdateLock(path, func() error {
		callbackRan = true
		return nil
	})
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first updater error = %v", err)
	}
	if callbackRan {
		t.Fatal("overlapping updater callback ran")
	}
	if !errors.Is(secondErr, ErrUpdateInProgress) {
		t.Fatalf("second updater error = %v, want ErrUpdateInProgress", secondErr)
	}
}

func TestWithUpdateLockReleasesAfterCallbackError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update.lock")
	wantErr := errors.New("update failed")

	if err := WithUpdateLock(path, func() error { return wantErr }); !errors.Is(err, wantErr) {
		t.Fatalf("callback error = %v, want %v", err, wantErr)
	}
	if err := WithUpdateLock(path, func() error { return nil }); err != nil {
		t.Fatalf("lock was not reacquired after callback error: %v", err)
	}
}

func TestWithUpdateLockCanBeReacquiredAfterCallbackReturns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update.lock")
	for i := 0; i < 2; i++ {
		if err := WithUpdateLock(path, func() error { return nil }); err != nil {
			t.Fatalf("WithUpdateLock() call %d error = %v", i+1, err)
		}
	}
}
