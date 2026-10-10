package menu

import (
	"testing"

	"cf-san/internal/config"
)

func TestUpdateConfigSettingChangesOnlySelectedValue(t *testing.T) {
	cfg := config.Config{URL: "https://worker.example", Token: "hidden", ASN: 64512, Limit: 100, Senders: 10, AgentID: "id"}
	if err := UpdateSetting(&cfg, "senders", "40"); err != nil {
		t.Fatal(err)
	}
	if cfg.Senders != 40 || cfg.Limit != 100 || cfg.ASN != 64512 || cfg.Token != "hidden" {
		t.Fatalf("config after update = %#v", cfg)
	}
}

func TestUpdateConfigRejectsZeroAndUnknownSettings(t *testing.T) {
	cfg := config.Config{Limit: 100, Senders: 10}
	for _, tc := range []struct{ key, value string }{{"limit", "0"}, {"senders", "-1"}, {"asn", "0"}, {"unknown", "1"}} {
		if err := UpdateSetting(&cfg, tc.key, tc.value); err == nil {
			t.Errorf("UpdateSetting(%q, %q) error = nil", tc.key, tc.value)
		}
	}
}
