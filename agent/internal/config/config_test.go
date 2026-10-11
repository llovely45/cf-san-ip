package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultsPersistMasscanCPUPercent(t *testing.T) {
	data, err := json.Marshal(Defaults())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"masscanCpuPercent":30`) {
		t.Fatalf("default config = %s, want 30%% total CPU cap", data)
	}
}

func TestLoadMigratesMissingMasscanCPUPercentToThirty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	legacy := `{"url":"https://worker.example","token":"token","asn":64512,"limit":250,"curlWorkers":40,"agentId":"legacy"}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"masscanCpuPercent": 30`) {
		t.Fatalf("migrated config = %s, want 30%% default CPU cap", data)
	}
}

func TestValidateRequiresHTTPSAndPositiveMasscanAndCurlSettings(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
	}{
		{name: "http backend", cfg: Config{URL: "http://worker.example", Token: "token", Limit: 100, CurlWorkers: 20, MasscanCPUPercent: 30}},
		{name: "missing token", cfg: Config{URL: "https://worker.example", Limit: 100, CurlWorkers: 20, MasscanCPUPercent: 30}},
		{name: "zero rate", cfg: Config{URL: "https://worker.example", Token: "token", Limit: 0, CurlWorkers: 20, MasscanCPUPercent: 30}},
		{name: "zero curl workers", cfg: Config{URL: "https://worker.example", Token: "token", Limit: 100, CurlWorkers: 0, MasscanCPUPercent: 30}},
		{name: "zero CPU limit", cfg: Config{URL: "https://worker.example", Token: "token", Limit: 100, CurlWorkers: 20, MasscanCPUPercent: 0}},
		{name: "CPU limit over 100 percent", cfg: Config{URL: "https://worker.example", Token: "token", Limit: 100, CurlWorkers: 20, MasscanCPUPercent: 101}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := Validate(tt.cfg); err == nil {
				t.Fatal("Validate() error = nil, want an error")
			}
		})
	}
}

func TestSaveWritesJSONWithMode0600AndLoadRejectsLoosePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := Config{URL: "https://worker.example", Token: "sensitive-token", ASN: 64512, Limit: 100, CurlWorkers: 64, MasscanCPUPercent: 30, AgentID: "agent-test"}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"curlWorkers": 64`) || strings.Contains(string(data), `"senders"`) {
		t.Fatalf("saved config did not use the new curlWorkers key: %s", data)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded != cfg {
		t.Fatalf("Load() = %#v, want %#v", loaded, cfg)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load() accepted a config readable by group/others")
	}
}

func TestLoadMigratesLegacySendersToCurlWorkers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	legacy := `{"url":"https://worker.example","token":"token","asn":64512,"limit":250,"senders":40,"agentId":"legacy"}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CurlWorkers != 40 {
		t.Fatalf("legacy senders migrated to CurlWorkers = %d, want 40", cfg.CurlWorkers)
	}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"senders"`) || !strings.Contains(string(data), `"curlWorkers": 40`) {
		t.Fatalf("migrated config was not rewritten in the new format: %s", data)
	}
}

func TestLoadCapsLegacySendersAtCurlWorkerLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	legacy := `{"url":"https://worker.example","token":"token","asn":64512,"limit":250,"senders":8192,"agentId":"legacy"}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() rejected a legacy config with more senders than the new worker limit: %v", err)
	}
	if cfg.CurlWorkers != 4096 {
		t.Fatalf("legacy senders migrated to CurlWorkers = %d, want capped value 4096", cfg.CurlWorkers)
	}

	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"curlWorkers": 4096`) || strings.Contains(string(data), `"senders"`) {
		t.Fatalf("legacy config was not rewritten with the capped worker count: %s", data)
	}
}

func TestLoadStillRejectsUnknownConfigFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"url":"https://worker.example","token":"token","limit":100,"curlWorkers":10,"agentId":"id","unexpected":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load() accepted an unknown configuration field")
	}
}

func TestOverridesReplaceOnlyExplicitValues(t *testing.T) {
	base := Config{URL: "https://old.example", Token: "old", ASN: 64512, Limit: 100, CurlWorkers: 25, MasscanCPUPercent: 30, AgentID: "id"}
	asn := uint32(906)
	limit := uint64(250)
	workers := uint32(40)
	got := MergeOverrides(base, Overrides{ASN: &asn, Limit: &limit, CurlWorkers: &workers})
	want := Config{URL: "https://old.example", Token: "old", ASN: 906, Limit: 250, CurlWorkers: 40, MasscanCPUPercent: 30, AgentID: "id"}
	if got != want {
		t.Fatalf("MergeOverrides() = %#v, want %#v", got, want)
	}
}

func TestValidateBackendURL(t *testing.T) {
	for _, tc := range []struct {
		url string
		ok  bool
	}{
		{"https://worker.example", true},
		{"https://worker.example/base", true},
		{"http://worker.example", false},
		{"https://user:pass@worker.example", false},
		{"https://worker.example?token=secret", false},
		{"https://worker.example/#fragment", false},
	} {
		err := ValidateBackendURL(tc.url)
		if (err == nil) != tc.ok {
			t.Errorf("ValidateBackendURL(%q) error = %v, want success=%v", tc.url, err, tc.ok)
		}
	}
}
