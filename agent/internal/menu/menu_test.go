package menu

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cf-san/internal/config"
)

func TestPrintStatusDisplaysConfiguredToken(t *testing.T) {
	var output bytes.Buffer
	app := &App{Output: &output, Manager: &Manager{RunDir: filepath.Join(t.TempDir(), "run")}}
	cfg := config.Config{URL: "https://worker.example", Token: "visible-token", ASN: 16509, Limit: 250, Senders: 40, AgentID: "agent-1"}

	app.printStatus(cfg)

	if !strings.Contains(output.String(), "Token：visible-token") {
		t.Fatalf("status output does not display configured token: %q", output.String())
	}
}

func TestMenuShowsConfigurationAndUpdateChoices(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.Save(path, config.Config{URL: "https://worker.example", Token: "token", Limit: 100, Senders: 10, AgentID: "id"}); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	app := &App{ConfigPath: path, Input: strings.NewReader("11\n"), Output: &output}
	if err := app.Run(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"8. 修改后端地址", "9. 修改 API Token", "10. 更新 cf-san 和 ZGrab2 到最新版本", "11. 退出"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("menu output missing %q: %s", want, output.String())
		}
	}
}

func TestMenuUpdateChoiceRunsUpdaterAndPersistsConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	initial := config.Config{URL: "https://worker.example", Token: "token", ASN: 16509, Limit: 100, Senders: 10, AgentID: "id"}
	if err := config.Save(path, initial); err != nil {
		t.Fatal(err)
	}
	var called bool
	app := &App{
		ConfigPath: path,
		Input:      strings.NewReader("10\n11\n"),
		Output:     &bytes.Buffer{},
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
		ConfigPath: filepath.Join(dir, "config.json"),
		Manager:    &manager,
		updateAgent: func(context.Context, config.Config, io.Writer) error {
			called = true
			return nil
		},
	}
	cfg := config.Config{URL: "https://worker.example", Token: "token", Limit: 100, Senders: 10}
	if err := app.updateLatest(&cfg); err == nil || !strings.Contains(err.Error(), "先暂停扫描") {
		t.Fatalf("updateLatest() error = %v, want pause-scan error", err)
	}
	if called {
		t.Fatal("updater ran while scanner was active")
	}
}

func TestMenuCanChangeBackendURLAndToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	initial := config.Config{URL: "https://worker.example", Token: "old-token", ASN: 16509, Limit: 100, Senders: 10, AgentID: "id"}
	if err := config.Save(path, initial); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	app := &App{
		ConfigPath: path,
		Input:      strings.NewReader("8\nhttps://new-worker.example/\n9\nnew-token\n7\n11\n"),
		Output:     &output,
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
