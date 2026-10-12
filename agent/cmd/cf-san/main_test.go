package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"cf-san/internal/autoupdate"
	"cf-san/internal/config"
)

func useTestUpdateLockPath(t *testing.T) string {
	t.Helper()
	previous := updateLockPath
	path := filepath.Join(t.TempDir(), "update.lock")
	updateLockPath = path
	t.Cleanup(func() { updateLockPath = previous })
	return path
}

func TestRequireRoot(t *testing.T) {
	if err := requireRoot(1000); err == nil {
		t.Fatal("requireRoot(1000) error = nil, want non-root rejection")
	}
	if err := requireRoot(0); err != nil {
		t.Fatalf("requireRoot(0) error = %v, want nil", err)
	}
}

func TestVersionCommandDoesNotReadConfig(t *testing.T) {
	missingPath := t.TempDir() + "/does-not-exist/config.json"
	if err := run([]string{"version", "--config", missingPath}); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			t.Fatalf("version command read missing config: %v", err)
		}
		t.Fatalf("version command error = %v", err)
	}
}

func TestForegroundRunDoesNotWaitForAnActiveUpdate(t *testing.T) {
	lockPath := useTestUpdateLockPath(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- autoupdate.WithUpdateLock(lockPath, func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered

	err := run([]string{"run", "--config", filepath.Join(t.TempDir(), "config.json")})
	if !errors.Is(err, autoupdate.ErrUpdateInProgress) {
		t.Fatalf("foreground run error = %v, want ErrUpdateInProgress without waiting", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("update lock holder error = %v", err)
	}
}

func TestParseOptionsAppliesCLIOverrides(t *testing.T) {
	options, err := parseOptions([]string{"--url", "https://worker.example/", "--token", "new-token", "--asn", "906", "--limit", "250", "--workers", "40", "--cpu-percent", "45", "--config", "/tmp/cf-san.json"})
	if err != nil {
		t.Fatal(err)
	}
	if options.configPath != "/tmp/cf-san.json" || options.overrides.URL == nil || *options.overrides.URL != "https://worker.example/" || options.overrides.Token == nil || *options.overrides.Token != "new-token" || options.overrides.ASN == nil || *options.overrides.ASN != 906 || options.overrides.Limit == nil || *options.overrides.Limit != 250 || options.overrides.CurlWorkers == nil || *options.overrides.CurlWorkers != 40 || options.overrides.MasscanCPUPercent == nil || *options.overrides.MasscanCPUPercent != 45 {
		t.Fatalf("parsed options = %#v", options)
	}
}

func TestLegacySendersFlagRemainsAliasForCurlWorkers(t *testing.T) {
	options, err := parseOptions([]string{"--senders", "32"})
	if err != nil {
		t.Fatal(err)
	}
	if options.overrides.CurlWorkers == nil || *options.overrides.CurlWorkers != 32 {
		t.Fatalf("legacy --senders override = %#v", options.overrides.CurlWorkers)
	}
}

func TestFlagDefaultsKeepMasscanAndCurlSettings(t *testing.T) {
	options, err := parseOptions([]string{"--url", "https://worker.example", "--token", "secret"})
	if err != nil {
		t.Fatal(err)
	}
	base := config.Defaults()
	got := config.MergeOverrides(base, options.overrides)
	if !reflect.DeepEqual(got.Limit, uint64(100)) || got.CurlWorkers != 100 || got.MasscanCPUPercent != 30 {
		t.Fatalf("defaults = masscan rate %d curl workers %d CPU percent %d", got.Limit, got.CurlWorkers, got.MasscanCPUPercent)
	}
}

func TestParseOptionsRejectsInvalidMasscanCPUPercent(t *testing.T) {
	for _, value := range []string{"0", "101"} {
		if _, err := parseOptions([]string{"--cpu-percent", value}); err == nil {
			t.Errorf("parseOptions(--cpu-percent %s) error = nil", value)
		}
	}
}

func TestConfigureCreatesMissingProtectedConfig(t *testing.T) {
	useTestUpdateLockPath(t)
	path := t.TempDir() + "/nested/config.json"
	err := run([]string{
		"configure",
		"--config", path,
		"--url", "https://worker.example",
		"--token", "local-test-token",
	})
	if err != nil {
		t.Fatalf("run(configure) error = %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}
	if cfg.Token != "local-test-token" || cfg.URL != "https://worker.example" {
		t.Fatalf("saved config = %#v", cfg)
	}
}

func TestConfigureReloadsFreshConfigBeforeApplyingOverride(t *testing.T) {
	useTestUpdateLockPath(t)
	path := filepath.Join(t.TempDir(), "config.json")
	initial := config.Config{URL: "https://old.example", Token: "old-token", ASN: 16509, Limit: 100, CurlWorkers: 10, MasscanCPUPercent: 30, AgentID: "old-agent"}
	fresh := config.Config{URL: "https://new.example", Token: "new-token", ASN: 64512, Limit: 250, CurlWorkers: 40, MasscanCPUPercent: 45, AgentID: "new-agent"}
	if err := config.Save(path, initial); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(path, fresh); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"configure", "--config", path, "--token", "configured-token"}); err != nil {
		t.Fatalf("run(configure) error = %v", err)
	}
	got, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != fresh.URL || got.Token != "configured-token" || got.ASN != fresh.ASN || got.Limit != fresh.Limit || got.CurlWorkers != fresh.CurlWorkers || got.MasscanCPUPercent != fresh.MasscanCPUPercent || got.AgentID != fresh.AgentID {
		t.Fatalf("saved config = %#v, want fresh values with only token changed", got)
	}
}
