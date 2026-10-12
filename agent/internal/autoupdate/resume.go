package autoupdate

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	// DefaultResumeStatePath stores a pending automatic scanner recovery intent.
	DefaultResumeStatePath = "/var/lib/cf-san/update-resume"
	resumeStateContents    = "resume\n"
)

// SetPendingResume atomically records that an active scanner must be resumed.
func SetPendingResume(path string) error {
	path = normalizeResumePath(path)
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("cannot create resume state directory: %w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return fmt.Errorf("cannot protect resume state directory: %w", err)
	}
	temp, err := os.CreateTemp(directory, ".cf-san-resume-*")
	if err != nil {
		return fmt.Errorf("cannot create resume state: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return fmt.Errorf("cannot protect resume state: %w", err)
	}
	if _, err := io.WriteString(temp, resumeStateContents); err != nil {
		_ = temp.Close()
		return fmt.Errorf("cannot write resume state: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("cannot flush resume state: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("cannot close resume state: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("cannot install resume state: %w", err)
	}
	return nil
}

// HasPendingResume reports whether an automatic scanner recovery is pending.
func HasPendingResume(path string) (bool, error) {
	path = normalizeResumePath(path)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("cannot read resume state: %w", err)
	}
	if strings.TrimSpace(string(data)) != strings.TrimSpace(resumeStateContents) {
		return false, errors.New("resume state is invalid")
	}
	return true, nil
}

// ClearPendingResume removes a pending automatic scanner recovery intent.
func ClearPendingResume(path string) error {
	path = normalizeResumePath(path)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("cannot clear resume state: %w", err)
	}
	return nil
}

func normalizeResumePath(path string) string {
	if path == "" {
		return DefaultResumeStatePath
	}
	return path
}
