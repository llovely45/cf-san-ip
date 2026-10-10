package menu

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestManagerStartsStopsAndRejectsDuplicateRun(t *testing.T) {
	dir := t.TempDir()
	manager := Manager{
		Executable: "/bin/sleep",
		Arguments:  []string{"30"},
		RunDir:     filepath.Join(dir, "run"),
		LogDir:     filepath.Join(dir, "log"),
	}
	if err := manager.Start(); err != nil {
		t.Fatal(err)
	}
	if running, err := manager.Status(); err != nil || !running {
		t.Fatalf("Status() = (%v, %v), want running", running, err)
	}
	if err := manager.Start(); err == nil {
		t.Fatal("Start() allowed a duplicate scan process")
	}
	if err := manager.Stop(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		running, err := manager.Status()
		if err != nil {
			t.Fatal(err)
		}
		if !running {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("scanner is still running after pause signal")
}

func TestManagerRemovesMalformedOrStalePIDFile(t *testing.T) {
	runDir := t.TempDir()
	manager := Manager{Executable: "/usr/local/bin/cf-san", RunDir: runDir, LogDir: t.TempDir()}
	pidPath := filepath.Join(runDir, "cf-san.pid")
	if err := os.WriteFile(pidPath, []byte("not-a-pid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	running, err := manager.Status()
	if err != nil || running {
		t.Fatalf("Status() = (%v, %v), want stopped", running, err)
	}
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Fatalf("stale PID file still exists: %v", err)
	}
}
