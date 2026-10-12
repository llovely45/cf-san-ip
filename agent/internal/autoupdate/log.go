package autoupdate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	DefaultUpdateLogPath = "/var/log/cf-san/update.log"

	maxUpdateLogBytes = 1 << 20
	updateLogBackups  = 3
)

// UpdateRecord is one sanitized automatic-update outcome.
type UpdateRecord struct {
	Timestamp      time.Time
	CurrentVersion string
	LatestVersion  string
	WasRunning     bool
	Result         string
	Error          string
}

// UpdateLogger appends bounded, root-only update diagnostics.
type UpdateLogger struct {
	path string
	mu   sync.Mutex
}

// OpenLogger creates or opens a private update log at path.
func OpenLogger(path string) (*UpdateLogger, error) {
	if path == "" {
		return nil, errors.New("update log path must not be empty")
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("cannot create update log directory: %w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return nil, fmt.Errorf("cannot protect update log directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("cannot open update log: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("cannot protect update log: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("cannot close update log: %w", err)
	}
	return &UpdateLogger{path: path}, nil
}

// Record appends one line and rotates the old log when the next write would
// exceed one MiB. Three numbered backups are retained.
func (logger *UpdateLogger) Record(record UpdateRecord) error {
	if logger == nil || logger.path == "" {
		return errors.New("update logger is not configured")
	}
	logger.mu.Lock()
	defer logger.mu.Unlock()

	line := formatUpdateRecord(record)
	if err := logger.rotateIfNeeded(int64(len(line))); err != nil {
		return err
	}
	file, err := os.OpenFile(logger.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("cannot open update log: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("cannot protect update log: %w", err)
	}
	if _, err := file.WriteString(line); err != nil {
		_ = file.Close()
		return fmt.Errorf("cannot write update log: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("cannot close update log: %w", err)
	}
	return nil
}

func (logger *UpdateLogger) rotateIfNeeded(incoming int64) error {
	info, err := os.Stat(logger.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("cannot inspect update log: %w", err)
	}
	if info.Size()+incoming <= maxUpdateLogBytes {
		return nil
	}

	if err := removeIfExists(logger.path + "." + strconv.Itoa(updateLogBackups+1)); err != nil {
		return fmt.Errorf("cannot remove excess update log backup: %w", err)
	}
	for index := updateLogBackups; index >= 1; index-- {
		source := logger.path + "." + strconv.Itoa(index)
		if index == updateLogBackups {
			if err := removeIfExists(source); err != nil {
				return fmt.Errorf("cannot remove old update log backup: %w", err)
			}
			continue
		}
		destination := logger.path + "." + strconv.Itoa(index+1)
		if err := renameIfExists(source, destination); err != nil {
			return fmt.Errorf("cannot rotate update log backup: %w", err)
		}
	}
	if err := renameIfExists(logger.path, logger.path+".1"); err != nil {
		return fmt.Errorf("cannot rotate update log: %w", err)
	}
	for index := 1; index <= updateLogBackups; index++ {
		_ = os.Chmod(logger.path+"."+strconv.Itoa(index), 0o600)
	}
	return nil
}

func formatUpdateRecord(record UpdateRecord) string {
	timestamp := record.Timestamp
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	line := fmt.Sprintf(
		"%s current=%s latest=%s was_running=%t result=%s",
		timestamp.Format(time.RFC3339),
		sanitizeLogField(record.CurrentVersion),
		sanitizeLogField(record.LatestVersion),
		record.WasRunning,
		sanitizeLogField(record.Result),
	)
	if record.Error != "" {
		line += " error=" + strconv.Quote(sanitizeErrorText(record.Error))
	}
	return line + "\n"
}

func sanitizeLogField(value string) string {
	value = strings.ReplaceAll(value, "\n", "\\n")
	value = strings.ReplaceAll(value, "\r", "\\r")
	return strings.ReplaceAll(value, " ", "_")
}

var (
	secretAssignmentPattern = regexp.MustCompile(`(?i)\b(?:api[_ -]?token|authorization|token|password|secret)\b\s*[:=]\s*(?:(?:bearer|token)\s+)?(?:"[^"]*"|'[^']*'|[^\s,;]+)`)
	bearerPattern           = regexp.MustCompile(`(?i)\bbearer\s+[^\s,;]+`)
)

func sanitizeErrorText(value string) string {
	value = secretAssignmentPattern.ReplaceAllString(value, "[REDACTED_SECRET]")
	value = bearerPattern.ReplaceAllString(value, "[REDACTED_SECRET]")
	return strings.TrimSpace(value)
}

func sanitizeErrorTextWithToken(value, token string) string {
	if token != "" {
		value = strings.ReplaceAll(value, token, "[REDACTED_SECRET]")
	}
	return sanitizeErrorText(value)
}

func removeIfExists(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func renameIfExists(source, destination string) error {
	if _, err := os.Stat(source); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if err := removeIfExists(destination); err != nil {
		return err
	}
	return os.Rename(source, destination)
}
