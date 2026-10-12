package autoupdate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenLoggerCreatesPrivateLogAndRecordsSanitizedFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "update.log")
	logger, err := OpenLogger(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := logger.Record(UpdateRecord{
		Timestamp:      time.Date(2026, 10, 12, 3, 17, 5, 0, time.FixedZone("CST", 8*60*60)),
		CurrentVersion: "v1.2.2",
		LatestVersion:  "v1.2.3",
		WasRunning:     true,
		Result:         "updated",
		Error:          `request failed api_token=secret-token Authorization=Bearer another-secret`,
	}); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("log mode = %o, want 600", got)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"2026-10-12T03:17:05+08:00", "current=v1.2.2", "latest=v1.2.3", "was_running=true", "result=updated"} {
		if !strings.Contains(text, want) {
			t.Fatalf("log = %q, missing %q", text, want)
		}
	}
	for _, secret := range []string{"secret-token", "another-secret"} {
		if strings.Contains(text, secret) {
			t.Fatalf("log unexpectedly contains secret %q: %q", secret, text)
		}
	}
}

func TestUpdateLoggerRotatesAtOneMiBAndKeepsThreeBackups(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update.log")
	logger, err := OpenLogger(path)
	if err != nil {
		t.Fatal(err)
	}

	for index := 1; index <= 4; index++ {
		if err := os.WriteFile(path, []byte(strings.Repeat(string(rune('0'+index)), maxUpdateLogBytes)), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := logger.Record(UpdateRecord{CurrentVersion: "v1.2.2", LatestVersion: "v1.2.3", Result: "attempt"}); err != nil {
			t.Fatalf("Record() rotation %d error = %v", index, err)
		}
	}

	for index := 1; index <= 3; index++ {
		backup := path + "." + string(rune('0'+index))
		info, err := os.Stat(backup)
		if err != nil {
			t.Fatalf("backup %s: %v", backup, err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("backup %s mode = %o, want 600", backup, got)
		}
	}
	if _, err := os.Stat(path + ".4"); !os.IsNotExist(err) {
		t.Fatalf("unexpected fourth backup state: %v", err)
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(current) >= maxUpdateLogBytes {
		t.Fatalf("current log size = %d, want less than 1 MiB after rotation", len(current))
	}
}

func TestUpdateLoggerPropagatesRotatedBackupChmodFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update.log")
	logger, err := OpenLogger(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", maxUpdateLogBytes)), 0o600); err != nil {
		t.Fatal(err)
	}

	wantErr := errors.New("chmod failed")
	previousChmod := chmodRotatedUpdateLog
	chmodRotatedUpdateLog = func(string, os.FileMode) error { return wantErr }
	t.Cleanup(func() { chmodRotatedUpdateLog = previousChmod })

	if err := logger.Record(UpdateRecord{CurrentVersion: "v1.2.2", LatestVersion: "v1.2.3", Result: "attempt"}); !errors.Is(err, wantErr) {
		t.Fatalf("Record() error = %v, want propagated chmod failure", err)
	}
}

func TestFormatUpdateRecordLeavesUnknownScanStateUnknown(t *testing.T) {
	line := formatUpdateRecord(UpdateRecord{CurrentVersion: "v1.2.2", LatestVersion: "v1.2.3", Result: "current"})
	if !strings.Contains(line, "was_running=unknown") {
		t.Fatalf("formatted record = %q, want unknown scan state", line)
	}
	if strings.Contains(line, "was_running=false") {
		t.Fatalf("formatted record falsely claims stopped scan: %q", line)
	}
}
