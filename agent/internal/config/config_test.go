package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateRequiresHTTPSAndPositiveRateSettings(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
	}{
		{name: "http backend", cfg: Config{URL: "http://worker.example", Token: "token", Limit: 100, Senders: 20}},
		{name: "missing token", cfg: Config{URL: "https://worker.example", Limit: 100, Senders: 20}},
		{name: "zero rate", cfg: Config{URL: "https://worker.example", Token: "token", Limit: 0, Senders: 20}},
		{name: "zero senders", cfg: Config{URL: "https://worker.example", Token: "token", Limit: 100, Senders: 0}},
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
	cfg := Config{URL: "https://worker.example", Token: "sensitive-token", ASN: 64512, Limit: 100, Senders: 64, AgentID: "agent-test"}
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

func TestOverridesReplaceOnlyExplicitValues(t *testing.T) {
	base := Config{URL: "https://old.example", Token: "old", ASN: 64512, Limit: 100, Senders: 25, AgentID: "id"}
	asn := uint32(906)
	limit := uint64(250)
	got := MergeOverrides(base, Overrides{ASN: &asn, Limit: &limit})
	want := Config{URL: "https://old.example", Token: "old", ASN: 906, Limit: 250, Senders: 25, AgentID: "id"}
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
