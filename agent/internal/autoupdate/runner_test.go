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
			logger := &fakeLogger{}
			runner.Logger = logger

			if err := runner.Run(context.Background(), config.Config{}); err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if got := strings.Join(scanner.events, ","); got != "" {
				t.Fatalf("scanner events = %q, want no lifecycle calls", got)
			}
			if len(logger.records) != 1 || logger.records[0].WasRunningKnown {
				t.Fatalf("no-update record = %#v, want unknown scan state", logger.records)
			}
		})
	}
}

func TestRunnerDoesNotStopScannerWhenReleaseLookupFails(t *testing.T) {
	wantErr := errors.New("release service unavailable")
	scanner := &fakeScanner{running: true}
	runner := newTestRunner(t, "v1.2.2", Release{}, scanner)
	runner.Releases = fakeReleaseClient{err: wantErr}
	logger := &fakeLogger{}
	runner.Logger = logger

	if err := runner.Run(context.Background(), config.Config{}); !errors.Is(err, wantErr) {
		t.Fatalf("Run() error = %v, want %v", err, wantErr)
	}
	if got := strings.Join(scanner.events, ","); got != "" {
		t.Fatalf("scanner events = %q, want no lifecycle calls", got)
	}
	if len(logger.records) != 1 || logger.records[0].WasRunningKnown {
		t.Fatalf("lookup-failure record = %#v, want unknown scan state", logger.records)
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
	waitStarted := make(chan struct{})
	scanner := &fakeScanner{running: true, waitBlocks: true, waitStarted: waitStarted}
	installed := false
	runner := newTestRunner(t, "v1.2.2", Release{Version: "v1.2.3"}, scanner)
	runner.ShutdownTimeout = 25 * time.Millisecond
	runner.PollInterval = time.Millisecond
	runner.Install = func(_ context.Context, _ config.Config, _ string, _ io.Writer) error {
		installed = true
		return nil
	}

	startedAt := time.Now()
	if err := runner.Run(context.Background(), config.Config{}); err == nil {
		t.Fatal("Run() error = nil, want shutdown timeout")
	} else if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run() error = %v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(startedAt); elapsed < runner.ShutdownTimeout {
		t.Fatalf("Run() returned after %s, want it to wait for the shutdown deadline", elapsed)
	}
	if installed {
		t.Fatal("installer ran after shutdown timeout")
	}
	if got := strings.Join(scanner.events, ","); got != "status,stop,wait" {
		t.Fatalf("events = %q, want no install or restart after timeout", got)
	}
}

func TestRunnerDistinguishesNonTimeoutShutdownFailure(t *testing.T) {
	wantErr := errors.New("scanner wait failed")
	scanner := &fakeScanner{running: true, waitErr: wantErr}
	logger := &fakeLogger{}
	runner := newTestRunner(t, "v1.2.2", Release{Version: "v1.2.3"}, scanner)
	runner.Logger = logger
	runner.Install = func(context.Context, config.Config, string, io.Writer) error { return nil }

	if err := runner.Run(context.Background(), config.Config{}); !errors.Is(err, wantErr) {
		t.Fatalf("Run() error = %v, want %v", err, wantErr)
	}
	if len(logger.records) != 1 || logger.records[0].Result != "shutdown-failed" {
		t.Fatalf("shutdown failure record = %#v, want shutdown-failed", logger.records)
	}
}

func TestRunnerRedactsTokenFromReturnedErrorAndLog(t *testing.T) {
	token := "secret-token-from-config"
	wantErr := errors.New("release request failed: api_token=" + token)
	logger := &fakeLogger{}
	runner := newTestRunner(t, "v1.2.2", Release{}, &fakeScanner{})
	runner.Releases = fakeReleaseClient{err: wantErr}
	runner.Logger = logger

	err := runner.Run(context.Background(), config.Config{Token: token})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run() error = %v, want wrapped release error", err)
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("returned error leaked token: %q", err)
	}
	if len(logger.records) != 1 || strings.Contains(logger.records[0].Error, token) {
		t.Fatalf("logged record leaked token: %#v", logger.records)
	}
}

func TestRunnerReloadsConfigAfterAcquiringUpdateLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	initial := config.Config{URL: "https://old.example", Token: "old-token", ASN: 16509, Limit: 100, CurlWorkers: 10, MasscanCPUPercent: 30, AgentID: "old-agent"}
	fresh := config.Config{URL: "https://new.example", Token: "new-token", ASN: 64512, Limit: 250, CurlWorkers: 40, MasscanCPUPercent: 45, AgentID: "new-agent"}
	if err := config.Save(path, initial); err != nil {
		t.Fatal(err)
	}

	scanner := &fakeScanner{}
	runner := newTestRunner(t, "v1.2.2", Release{Version: "v1.2.3"}, scanner)
	runner.ConfigPath = path
	runner.Install = func(_ context.Context, cfg config.Config, version string, _ io.Writer) error {
		if cfg != fresh {
			t.Fatalf("installer received config %#v, want fresh %#v", cfg, fresh)
		}
		if version != "v1.2.3" {
			t.Fatalf("installer version = %q, want v1.2.3", version)
		}
		return nil
	}

	if err := config.Save(path, fresh); err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), initial); err != nil {
		t.Fatalf("Run() error = %v", err)
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
	mu          sync.Mutex
	running     bool
	events      []string
	waitErr     error
	statusErr   error
	waitBlocks  bool
	waitStarted chan struct{}
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

func (fake *fakeScanner) WaitStopped(ctx context.Context, _ time.Duration) error {
	fake.mu.Lock()
	fake.events = append(fake.events, "wait")
	waitBlocks := fake.waitBlocks
	waitErr := fake.waitErr
	waitStarted := fake.waitStarted
	fake.mu.Unlock()
	if waitBlocks {
		if waitStarted != nil {
			close(waitStarted)
		}
		<-ctx.Done()
		return ctx.Err()
	}
	return waitErr
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
