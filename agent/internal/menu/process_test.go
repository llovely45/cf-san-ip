package menu

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestProcessMatchesUsesExecutableInsteadOfArgvZero(t *testing.T) {
	command := exec.Command("/bin/sleep", "30")
	command.Args[0] = "renamed-agent"
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	})
	if !processMatches(command.Process.Pid, command.Path) {
		t.Fatal("processMatches rejected a process whose argv[0] differs from its executable")
	}
}

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

func TestManagerWaitStoppedAfterSIGTERM(t *testing.T) {
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
	t.Cleanup(func() {
		_ = manager.Stop()
	})

	if err := manager.Stop(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.WaitStopped(ctx, 10*time.Millisecond); err != nil {
		t.Fatalf("WaitStopped() error = %v, want nil after SIGTERM", err)
	}
}

func TestManagerWaitStoppedReturnsNilWhenAlreadyStopped(t *testing.T) {
	manager := Manager{RunDir: filepath.Join(t.TempDir(), "run")}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := manager.WaitStopped(ctx, 10*time.Millisecond); err != nil {
		t.Fatalf("WaitStopped() error = %v, want nil for an already-stopped process", err)
	}
}

func TestManagerWaitStoppedHonorsContextWhenProcessIgnoresSIGTERM(t *testing.T) {
	dir := t.TempDir()
	readyPath := filepath.Join(dir, "ready")
	manager := Manager{
		Executable: "/bin/sh",
		Arguments:  []string{"-c", "trap '' TERM; touch \"$1\"; while :; do sleep 1; done", "ignore-term-helper", readyPath},
		RunDir:     filepath.Join(dir, "run"),
		LogDir:     filepath.Join(dir, "log"),
	}
	if err := manager.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		data, err := os.ReadFile(manager.pidPath())
		if err == nil {
			var record pidRecord
			if err := json.Unmarshal(data, &record); err == nil && record.PID > 0 {
				_ = syscall.Kill(-record.PID, syscall.SIGKILL)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = manager.WaitStopped(ctx, 5*time.Millisecond)
	})
	if running, err := manager.Status(); err != nil || !running {
		t.Fatalf("Status() = (%v, %v), want the SIGTERM-ignoring process running", running, err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(readyPath); err == nil {
			break
		} else if !os.IsNotExist(err) {
			t.Fatalf("checking helper readiness: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("SIGTERM-ignoring helper did not become ready")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := manager.Stop(); err != nil {
		t.Fatal(err)
	}
	if running, err := manager.Status(); err != nil || !running {
		t.Fatalf("Status() after SIGTERM = (%v, %v), want the helper process still running", running, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := manager.WaitStopped(ctx, 10*time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitStopped() error = %v, want context deadline exceeded", err)
	}
}
