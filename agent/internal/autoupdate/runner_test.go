package autoupdate

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cf-san/internal/config"
)

func TestRunnerDoesNotTouchScannerWhenReleaseIsNotNewer(t *testing.T) {
	for _, version := range []string{"v1.2.2", "v1.2.1"} {
		t.Run(version, func(t *testing.T) {
			scanner := &fakeScanner{}
			runner := newTestRunner(t, "v1.2.2", Release{Version: version}, scanner)

			if err := runner.Run(context.Background(), config.Config{}); err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if got := strings.Join(scanner.events, ","); got != "" {
				t.Fatalf("scanner events = %q, want no lifecycle calls", got)
			}
		})
	}
}

func TestRunnerDoesNotStopScannerWhenReleaseLookupFails(t *testing.T) {
	wantErr := errors.New("release service unavailable")
	scanner := &fakeScanner{running: true}
	runner := newTestRunner(t, "v1.2.2", Release{}, scanner)
	runner.Releases = fakeReleaseClient{err: wantErr}

	if err := runner.Run(context.Background(), config.Config{}); !errors.Is(err, wantErr) {
		t.Fatalf("Run() error = %v, want %v", err, wantErr)
	}
	if got := strings.Join(scanner.events, ","); got != "" {
		t.Fatalf("scanner events = %q, want no lifecycle calls", got)
	}
}

func TestRunnerStopsWaitsInstallsPinnedReleaseAndRestartsActiveScanner(t *testing.T) {
	scanner := &fakeScanner{running: true}
	var installed string
	runner := newTestRunner(t, "v1.2.2", Release{Version: "v1.2.3"}, scanner)
	runner.Install = func(_ context.Context, _ config.Config, version string, _ io.Writer) error {
		installed = version
		scanner.events = append(scanner.events, "install:"+version)
		return nil
	}

	if err := runner.Run(context.Background(), config.Config{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if installed != "v1.2.3" {
		t.Fatalf("installed version = %q, want v1.2.3", installed)
	}
	if got := strings.Join(scanner.events, ","); got != "status,stop,wait,install:v1.2.3,start" {
		t.Fatalf("events = %q, want status,stop,wait,install:v1.2.3,start", got)
	}
}

func TestRunnerLeavesStoppedScannerStopped(t *testing.T) {
	scanner := &fakeScanner{}
	runner := newTestRunner(t, "v1.2.2", Release{Version: "v1.2.3"}, scanner)
	runner.Install = func(_ context.Context, _ config.Config, version string, _ io.Writer) error {
		scanner.events = append(scanner.events, "install:"+version)
		return nil
	}

	if err := runner.Run(context.Background(), config.Config{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := strings.Join(scanner.events, ","); got != "status,install:v1.2.3" {
		t.Fatalf("events = %q, want status,install:v1.2.3", got)
	}
}

func TestRunnerResumesOnlyFormerlyActiveScannerAfterInstallFailure(t *testing.T) {
	scanner := &fakeScanner{running: true}
	wantErr := errors.New("installer failed")
	runner := newTestRunner(t, "v1.2.2", Release{Version: "v1.2.3"}, scanner)
	runner.Install = func(_ context.Context, _ config.Config, version string, _ io.Writer) error {
		scanner.events = append(scanner.events, "install:"+version)
		return wantErr
	}

	if err := runner.Run(context.Background(), config.Config{}); !errors.Is(err, wantErr) {
		t.Fatalf("Run() error = %v, want %v", err, wantErr)
	}
	if got := strings.Join(scanner.events, ","); got != "status,stop,wait,install:v1.2.3,status,start" {
		t.Fatalf("events = %q, want recovery start after checking stopped state", got)
	}
}

func TestRunnerDoesNotInstallOrRestartAfterShutdownTimeout(t *testing.T) {
	scanner := &fakeScanner{running: true, waitErr: context.DeadlineExceeded}
	installed := false
	runner := newTestRunner(t, "v1.2.2", Release{Version: "v1.2.3"}, scanner)
	runner.ShutdownTimeout = 10 * time.Millisecond
	runner.PollInterval = time.Millisecond
	runner.Install = func(_ context.Context, _ config.Config, _ string, _ io.Writer) error {
		installed = true
		return nil
	}

	if err := runner.Run(context.Background(), config.Config{}); err == nil {
		t.Fatal("Run() error = nil, want shutdown timeout")
	}
	if installed {
		t.Fatal("installer ran after shutdown timeout")
	}
	if got := strings.Join(scanner.events, ","); got != "status,stop,wait" {
		t.Fatalf("events = %q, want no install or restart after timeout", got)
	}
}

func TestRunnerCompetingLockDoesNotTouchLifecycle(t *testing.T) {
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

	scanner := &fakeScanner{running: true}
	runner := newTestRunner(t, "v1.2.2", Release{Version: "v1.2.3"}, scanner)
	runner.LockPath = path
	if err := runner.Run(context.Background(), config.Config{}); !errors.Is(err, ErrUpdateInProgress) {
		t.Fatalf("Run() error = %v, want ErrUpdateInProgress", err)
	}
	if got := strings.Join(scanner.events, ","); got != "" {
		t.Fatalf("scanner events = %q, want no lifecycle calls", got)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first lock holder error = %v", err)
	}
}

func TestRunnerRestartInsideItsLockDoesNotDeadlock(t *testing.T) {
	scanner := &fakeScanner{running: true}
	runner := newTestRunner(t, "v1.2.2", Release{Version: "v1.2.3"}, scanner)
	runner.ShutdownTimeout = time.Second
	runner.Install = func(_ context.Context, _ config.Config, _ string, _ io.Writer) error {
		scanner.events = append(scanner.events, "install:v1.2.3")
		return nil
	}

	done := make(chan error, 1)
	go func() { done <- runner.Run(context.Background(), config.Config{}) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run() deadlocked while restarting scanner under its update lock")
	}
}

func TestRunnerRejectsUnknownBuildIdentityBeforeScannerLifecycle(t *testing.T) {
	for _, identity := range []struct {
		name       string
		version    string
		repository string
	}{
		{name: "development version", version: "dev", repository: "owner/repo"},
		{name: "missing repository", version: "v1.2.3", repository: ""},
	} {
		t.Run(identity.name, func(t *testing.T) {
			scanner := &fakeScanner{running: true}
			runner := newTestRunner(t, identity.version, Release{Version: "v1.2.4"}, scanner)
			runner.Repository = identity.repository

			if err := runner.Run(context.Background(), config.Config{}); err == nil {
				t.Fatal("Run() error = nil, want unknown build identity error")
			}
			if got := strings.Join(scanner.events, ","); got != "" {
				t.Fatalf("scanner events = %q, want no lifecycle calls", got)
			}
		})
	}
}

type fakeReleaseClient struct {
	release Release
	err     error
}

func (fake fakeReleaseClient) Latest(context.Context, string) (Release, error) {
	return fake.release, fake.err
}

type fakeScanner struct {
	mu        sync.Mutex
	running   bool
	events    []string
	waitErr   error
	statusErr error
}

func (fake *fakeScanner) Status() (bool, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.events = append(fake.events, "status")
	return fake.running, fake.statusErr
}

func (fake *fakeScanner) Stop() error {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.events = append(fake.events, "stop")
	fake.running = false
	return nil
}

func (fake *fakeScanner) WaitStopped(context.Context, time.Duration) error {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.events = append(fake.events, "wait")
	return fake.waitErr
}

func (fake *fakeScanner) Start() error {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.events = append(fake.events, "start")
	fake.running = true
	return nil
}

type fakeLogger struct {
	records []UpdateRecord
}

func (fake *fakeLogger) Record(record UpdateRecord) error {
	fake.records = append(fake.records, record)
	return nil
}

func newTestRunner(t *testing.T, current string, release Release, scanner *fakeScanner) Runner {
	t.Helper()
	return Runner{
		CurrentVersion:  current,
		Repository:      "owner/repo",
		Releases:        fakeReleaseClient{release: release},
		Scanner:         scanner,
		LockPath:        filepath.Join(t.TempDir(), "update.lock"),
		ShutdownTimeout: time.Second,
		PollInterval:    time.Millisecond,
	}
}
