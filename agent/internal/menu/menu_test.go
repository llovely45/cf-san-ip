package menu

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"cf-san/internal/autoupdate"
	"cf-san/internal/config"
)

func TestMain(m *testing.M) {
	if os.Getenv("CF_SAN_MENU_MANAGER_HELPER") == "1" {
		managerHelperProcess()
		return
	}
	os.Exit(m.Run())
}

func managerHelperProcess() {
	eventsPath := os.Getenv("CF_SAN_MENU_MANAGER_EVENTS")
	if eventsPath != "" {
		file, err := os.OpenFile(eventsPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = file.WriteString(strings.Join(os.Args[1:], "\x1f") + "\n")
			_ = file.Close()
		}
	}
	waitForUpdateLock := func() error {
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
		<-signals
		return nil
	}
	if os.Getenv("CF_SAN_MENU_MANAGER_WAIT_FOR_LOCK") == "1" {
		if err := autoupdate.WithUpdateLockWait(os.Getenv("CF_SAN_MENU_MANAGER_LOCK"), waitForUpdateLock); err != nil {
			os.Exit(1)
		}
		return
	}
	waitForUpdateLock()
}

func TestPrintStatusDisplaysConfiguredToken(t *testing.T) {
	var output bytes.Buffer
	app := &App{Output: &output, Manager: &Manager{RunDir: filepath.Join(t.TempDir(), "run")}}
	cfg := config.Config{URL: "https://worker.example", Token: "visible-token", ASN: 16509, Limit: 250, CurlWorkers: 40, MasscanCPUPercent: 30, AgentID: "agent-1"}

	app.printStatus(cfg)

	if !strings.Contains(output.String(), "Token：visible-token") {
		t.Fatalf("status output does not display configured token: %q", output.String())
	}
	if !strings.Contains(output.String(), "并发扫描块上限：4") {
		t.Fatalf("status output does not display the four-block concurrency limit: %q", output.String())
	}
	if !strings.Contains(output.String(), "Masscan 总 CPU 上限：30%") {
		t.Fatalf("status output does not display the default total CPU cap: %q", output.String())
	}
}

func TestMenuShowsConfigurationAndUpdateChoices(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.Save(path, config.Config{URL: "https://worker.example", Token: "token", Limit: 100, CurlWorkers: 10, AgentID: "id"}); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	app := &App{ConfigPath: path, Input: strings.NewReader("12\n"), Output: &output}
	if err := app.Run(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"4. 设置 Masscan 发包速率（PPS）", "5. 设置 curl 验证并发数", "6. 设置 Masscan 总 CPU 上限（%）", "9. 修改后端地址", "10. 修改 API Token", "11. 更新 cf-san 到最新版本", "12. 查看扫描日志", "13. 退出"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("menu output missing %q: %s", want, output.String())
		}
	}
}

func TestMenuUpdateChoiceRunsUpdaterAndPersistsConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	initial := config.Config{URL: "https://worker.example", Token: "token", ASN: 16509, Limit: 100, CurlWorkers: 10, MasscanCPUPercent: 30, AgentID: "id"}
	if err := config.Save(path, initial); err != nil {
		t.Fatal(err)
	}
	var called bool
	app := &App{
		ConfigPath:     path,
		Input:          strings.NewReader("11\n13\n"),
		Output:         &bytes.Buffer{},
		UpdateLockPath: filepath.Join(t.TempDir(), "update.lock"),
		updateAgent: func(_ context.Context, cfg config.Config, _ io.Writer) error {
			called = true
			if cfg.URL != initial.URL || cfg.Token != initial.Token {
				t.Fatalf("updater received config %#v", cfg)
			}
			return nil
		},
	}
	if err := app.Run(); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("menu update option did not call updater")
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded != initial {
		t.Fatalf("saved config = %#v, want %#v", loaded, initial)
	}
}

func TestMenuUpdateRefusesWhileScannerIsRunning(t *testing.T) {
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
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			running, _ := manager.Status()
			if !running {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	})

	var called bool
	app := &App{
		ConfigPath:     filepath.Join(dir, "config.json"),
		Manager:        &manager,
		UpdateLockPath: filepath.Join(dir, "update.lock"),
		updateAgent: func(context.Context, config.Config, io.Writer) error {
			called = true
			return nil
		},
	}
	cfg := config.Config{URL: "https://worker.example", Token: "token", Limit: 100, CurlWorkers: 10}
	if err := app.updateLatest(&cfg); err == nil || !strings.Contains(err.Error(), "先暂停扫描") {
		t.Fatalf("updateLatest() error = %v, want pause-scan error", err)
	}
	if called {
		t.Fatal("updater ran while scanner was active")
	}
}

func TestMenuManualUpdateUsesSharedUpdateLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update.lock")
	entered := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- autoupdate.WithUpdateLock(path, func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered

	called := false
	app := &App{
		ConfigPath:     filepath.Join(t.TempDir(), "config.json"),
		UpdateLockPath: path,
		updateAgent: func(context.Context, config.Config, io.Writer) error {
			called = true
			return nil
		},
	}
	cfg := config.Config{URL: "https://worker.example", Token: "token", ASN: 16509, Limit: 100, CurlWorkers: 10, MasscanCPUPercent: 30, AgentID: "id"}

	err := app.updateLatest(&cfg)
	if !errors.Is(err, autoupdate.ErrUpdateInProgress) {
		t.Fatalf("updateLatest() error = %v, want ErrUpdateInProgress", err)
	}
	if called {
		t.Fatal("manual updater ran while automatic update lock was held")
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first lock holder error = %v", err)
	}
}

func TestMenuSettingReloadsFreshConfigBeforeApplyingChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	initial := config.Config{URL: "https://old.example", Token: "old-token", ASN: 16509, Limit: 100, CurlWorkers: 10, MasscanCPUPercent: 30, AgentID: "old-agent"}
	fresh := config.Config{URL: "https://new.example", Token: "new-token", ASN: 64512, Limit: 200, CurlWorkers: 40, MasscanCPUPercent: 45, AgentID: "new-agent"}
	if err := config.Save(path, initial); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(t.TempDir(), "update.lock")
	app := &App{ConfigPath: path, Output: &bytes.Buffer{}, UpdateLockPath: lockPath}
	cfg := initial
	if err := config.Save(path, fresh); err != nil {
		t.Fatal(err)
	}
	if err := app.updateSetting(bufio.NewReader(strings.NewReader("250\n")), &cfg, "4"); err != nil {
		t.Fatalf("updateSetting() error = %v", err)
	}
	got, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != fresh.URL || got.Token != fresh.Token || got.ASN != fresh.ASN || got.Limit != 250 || got.CurlWorkers != fresh.CurlWorkers || got.MasscanCPUPercent != fresh.MasscanCPUPercent || got.AgentID != fresh.AgentID {
		t.Fatalf("saved config = %#v, want fresh values with only limit changed", got)
	}
	if cfg != got {
		t.Fatalf("caller config = %#v, want synchronized config %#v", cfg, got)
	}
}

func TestMenuManualUpdateReloadsFreshConfigBeforeRunning(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	initial := config.Config{URL: "https://old.example", Token: "old-token", ASN: 16509, Limit: 100, CurlWorkers: 10, MasscanCPUPercent: 30, AgentID: "old-agent"}
	fresh := config.Config{URL: "https://new.example", Token: "new-token", ASN: 64512, Limit: 200, CurlWorkers: 40, MasscanCPUPercent: 45, AgentID: "new-agent"}
	if err := config.Save(path, initial); err != nil {
		t.Fatal(err)
	}
	var received config.Config
	lockPath := filepath.Join(t.TempDir(), "update.lock")
	app := &App{
		ConfigPath:     path,
		Manager:        &Manager{RunDir: filepath.Join(t.TempDir(), "run")},
		Output:         &bytes.Buffer{},
		UpdateLockPath: lockPath,
		updateAgent: func(_ context.Context, cfg config.Config, _ io.Writer) error {
			received = cfg
			return nil
		},
	}
	cfg := initial
	if err := config.Save(path, fresh); err != nil {
		t.Fatal(err)
	}
	if err := app.updateLatest(&cfg); err != nil {
		t.Fatalf("updateLatest() error = %v", err)
	}
	if received != fresh {
		t.Fatalf("updater received config %#v, want fresh %#v", received, fresh)
	}
	if cfg != fresh {
		t.Fatalf("caller config = %#v, want synchronized config %#v", cfg, fresh)
	}
}

func TestRunnerUsesRealManagerLifecycleWithExactRunConfigArguments(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	cfg := config.Config{URL: "https://worker.example", Token: "token", ASN: 16509, Limit: 100, CurlWorkers: 10, MasscanCPUPercent: 30, AgentID: "agent"}
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	eventsPath := filepath.Join(dir, "events.log")
	lockPath := filepath.Join(dir, "update.lock")
	t.Setenv("CF_SAN_MENU_MANAGER_HELPER", "1")
	t.Setenv("CF_SAN_MENU_MANAGER_EVENTS", eventsPath)
	t.Setenv("CF_SAN_MENU_MANAGER_LOCK", lockPath)
	manager := &Manager{
		Executable: os.Args[0],
		Arguments:  []string{"run", "--config", configPath},
		RunDir:     filepath.Join(dir, "run"),
		LogDir:     filepath.Join(dir, "log"),
	}
	t.Cleanup(func() {
		_ = manager.Stop()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = manager.WaitStopped(ctx, 5*time.Millisecond)
		cancel()
	})
	if err := manager.Start(); err != nil {
		t.Fatal(err)
	}
	waitForManagerEvents(t, eventsPath, 1)

	runner := autoupdate.Runner{
		CurrentVersion:  "v1.2.2",
		Repository:      "owner/repo",
		Releases:        integrationReleaseClient{},
		Scanner:         manager,
		LockPath:        lockPath,
		ShutdownTimeout: 5 * time.Second,
		PollInterval:    5 * time.Millisecond,
		Install: func(_ context.Context, installCfg config.Config, version string, _ io.Writer) error {
			if installCfg != cfg {
				t.Fatalf("installer received config %#v, want %#v", installCfg, cfg)
			}
			running, err := manager.Status()
			if err != nil {
				return err
			}
			if running {
				t.Fatal("installer ran before real Manager confirmed scanner stopped")
			}
			appendManagerEvent(t, eventsPath, "install:"+version)
			t.Setenv("CF_SAN_MENU_MANAGER_WAIT_FOR_LOCK", "1")
			return nil
		},
	}
	if err := runner.Run(context.Background(), cfg); err != nil {
		t.Fatalf("Runner.Run() error = %v", err)
	}
	running, err := manager.Status()
	if err != nil {
		t.Fatal(err)
	}
	if !running {
		t.Fatal("real Manager did not restart scanner")
	}
	lines := waitForManagerEvents(t, eventsPath, 3)
	expectedStart := strings.Join([]string{"run", "--config", configPath}, "\x1f")
	expected := []string{expectedStart, "install:v1.2.3", expectedStart}
	if strings.Join(lines, "\n") != strings.Join(expected, "\n") {
		t.Fatalf("manager lifecycle events = %#v, want %#v", lines, expected)
	}
}

type integrationReleaseClient struct{}

func (integrationReleaseClient) Latest(context.Context, string) (autoupdate.Release, error) {
	return autoupdate.Release{Version: "v1.2.3"}, nil
}

func appendManagerEvent(t *testing.T, path, event string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(event + "\n"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func waitForManagerEvents(t *testing.T, path string, count int) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			if len(lines) >= count {
				return lines
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d manager events in %s", count, path)
	return nil
}

func TestMenuStartUsesSharedUpdateLockBeforeScannerStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update.lock")
	entered := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- autoupdate.WithUpdateLock(path, func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered

	manager := &Manager{
		Executable: "/bin/sleep",
		Arguments:  []string{"30"},
		RunDir:     filepath.Join(t.TempDir(), "run"),
		LogDir:     filepath.Join(t.TempDir(), "log"),
	}
	app := &App{ConfigPath: filepath.Join(t.TempDir(), "config.json"), Manager: manager, UpdateLockPath: path}
	cfg := config.Config{URL: "https://worker.example", Token: "token", ASN: 16509, Limit: 100, CurlWorkers: 10, MasscanCPUPercent: 30, AgentID: "id"}

	err := app.start(&cfg)
	if !errors.Is(err, autoupdate.ErrUpdateInProgress) {
		t.Fatalf("start() error = %v, want ErrUpdateInProgress", err)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first lock holder error = %v", err)
	}
}

func TestMenuCanChangeBackendURLAndToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	initial := config.Config{URL: "https://worker.example", Token: "old-token", ASN: 16509, Limit: 100, CurlWorkers: 10, AgentID: "id"}
	if err := config.Save(path, initial); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	app := &App{
		ConfigPath:     path,
		Input:          strings.NewReader("9\nhttps://new-worker.example/\n10\nnew-token\n8\n13\n"),
		Output:         &output,
		UpdateLockPath: filepath.Join(t.TempDir(), "update.lock"),
	}
	if err := app.Run(); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.URL != "https://new-worker.example" || loaded.Token != "new-token" {
		t.Fatalf("saved endpoint configuration = %#v", loaded)
	}
	if !strings.Contains(output.String(), "Token：new-token") {
		t.Fatalf("updated token was not displayed by status: %s", output.String())
	}
}

func TestMenuCanChangeMasscanTotalCPUPercent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.Save(path, config.Config{URL: "https://worker.example", Token: "token", ASN: 16509, Limit: 100, CurlWorkers: 10, AgentID: "id"}); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	app := &App{
		ConfigPath:     path,
		Input:          strings.NewReader("6\n45\n8\n13\n"),
		Output:         &output,
		UpdateLockPath: filepath.Join(t.TempDir(), "update.lock"),
	}
	if err := app.Run(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if saved["masscanCpuPercent"] != float64(45) {
		t.Fatalf("saved masscan CPU cap = %#v, want 45", saved["masscanCpuPercent"])
	}
	if !strings.Contains(output.String(), "Masscan 总 CPU 上限：45%") {
		t.Fatalf("menu did not display the updated total CPU cap: %s", output.String())
	}
}

func TestMenuDisplaysRecentLogsBeforeExit(t *testing.T) {
	dir := t.TempDir()
	manager := &Manager{RunDir: filepath.Join(dir, "run"), LogDir: filepath.Join(dir, "log")}
	if err := os.MkdirAll(manager.LogDir, 0o700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(manager.LogDir, "agent.log")
	if err := os.WriteFile(logPath, []byte("old line\nlatest scanner error\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	if err := config.Save(path, config.Config{URL: "https://worker.example", Token: "token", Limit: 100, CurlWorkers: 10, AgentID: "id"}); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	app := &App{
		ConfigPath: path,
		Manager:    manager,
		Input:      strings.NewReader("12\n13\n"),
		Output:     &output,
	}
	if err := app.Run(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"12. 查看扫描日志", "13. 退出", "old line", "latest scanner error", logPath} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("menu output missing %q: %s", want, output.String())
		}
	}
}

func TestLogTailShowsOnlyLastRequestedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.log")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\nfour\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readLogTail(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "three\nfour\n" {
		t.Fatalf("readLogTail() = %q, want last two lines", got)
	}
}
