package menu

import (
	"encoding/json"
	"strings"
	"testing"

	"cf-san/internal/config"
)

func TestUpdateConfigSettingChangesOnlySelectedValue(t *testing.T) {
	cfg := config.Config{URL: "https://worker.example", Token: "hidden", ASN: 64512, Limit: 100, CurlWorkers: 10, AgentID: "id"}
	if err := UpdateSetting(&cfg, "workers", "40"); err != nil {
		t.Fatal(err)
	}
	if cfg.CurlWorkers != 40 || cfg.Limit != 100 || cfg.ASN != 64512 || cfg.Token != "hidden" {
		t.Fatalf("config after update = %#v", cfg)
	}
}

func TestUpdateConfigSettingChangesMasscanTotalCPUPercent(t *testing.T) {
	cfg := config.Config{URL: "https://worker.example", Token: "hidden", ASN: 64512, Limit: 100, CurlWorkers: 10, AgentID: "id"}
	if err := UpdateSetting(&cfg, "cpu", "45"); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"masscanCpuPercent":45`) || cfg.CurlWorkers != 10 || cfg.Limit != 100 || cfg.ASN != 64512 {
		t.Fatalf("config after CPU cap update = %#v", cfg)
	}
}

func TestUpdateConfigRejectsZeroAndUnknownSettings(t *testing.T) {
	cfg := config.Config{Limit: 100, CurlWorkers: 10}
	for _, tc := range []struct{ key, value string }{{"limit", "0"}, {"workers", "-1"}, {"asn", "0"}, {"cpu", "0"}, {"cpu", "101"}, {"unknown", "1"}} {
		if err := UpdateSetting(&cfg, tc.key, tc.value); err == nil {
			t.Errorf("UpdateSetting(%q, %q) error = nil", tc.key, tc.value)
		}
	}
}

func TestUpdateConfigAllowsBackendURLAndTokenChanges(t *testing.T) {
	cfg := config.Config{URL: "https://old.example", Token: "old-token", ASN: 64512, Limit: 100, CurlWorkers: 10, AgentID: "id"}
	if err := UpdateSetting(&cfg, "url", " https://new.example/ "); err != nil {
		t.Fatal(err)
	}
	if err := UpdateSetting(&cfg, "token", " new-token "); err != nil {
		t.Fatal(err)
	}
	if cfg.URL != "https://new.example" || cfg.Token != "new-token" || cfg.ASN != 64512 || cfg.Limit != 100 || cfg.CurlWorkers != 10 {
		t.Fatalf("config after changes = %#v", cfg)
	}
}

func TestUpdateConfigRejectsInvalidBackendURLAndEmptyToken(t *testing.T) {
	cfg := config.Config{URL: "https://old.example", Token: "old-token", Limit: 100, CurlWorkers: 10}
	for _, tc := range []struct{ key, value string }{
		{"url", "http://worker.example"},
		{"url", "https://user:pass@worker.example"},
		{"url", "https://worker.example?token=secret"},
		{"token", "   "},
	} {
		if err := UpdateSetting(&cfg, tc.key, tc.value); err == nil {
			t.Errorf("UpdateSetting(%q, %q) error = nil", tc.key, tc.value)
		}
	}
}
