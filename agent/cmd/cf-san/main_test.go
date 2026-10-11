package main

import (
	"reflect"
	"testing"

	"cf-san/internal/config"
)

func TestParseOptionsAppliesCLIOverrides(t *testing.T) {
	options, err := parseOptions([]string{"--url", "https://worker.example/", "--token", "new-token", "--asn", "906", "--limit", "250", "--workers", "40", "--config", "/tmp/cf-san.json"})
	if err != nil {
		t.Fatal(err)
	}
	if options.configPath != "/tmp/cf-san.json" || options.overrides.URL == nil || *options.overrides.URL != "https://worker.example/" || options.overrides.Token == nil || *options.overrides.Token != "new-token" || options.overrides.ASN == nil || *options.overrides.ASN != 906 || options.overrides.Limit == nil || *options.overrides.Limit != 250 || options.overrides.CurlWorkers == nil || *options.overrides.CurlWorkers != 40 {
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
	if !reflect.DeepEqual(got.Limit, uint64(100)) || got.CurlWorkers != 100 {
		t.Fatalf("defaults = masscan rate %d curl workers %d", got.Limit, got.CurlWorkers)
	}
}

func TestConfigureCreatesMissingProtectedConfig(t *testing.T) {
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
