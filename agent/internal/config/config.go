package config

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	URL         string `json:"url"`
	Token       string `json:"token"`
	ASN         uint32 `json:"asn"`
	Limit       uint64 `json:"limit"`
	CurlWorkers uint32 `json:"curlWorkers"`
	AgentID     string `json:"agentId"`
}

const maxCurlWorkers = 4096

type Overrides struct {
	URL         *string
	Token       *string
	ASN         *uint32
	Limit       *uint64
	CurlWorkers *uint32
}

type configJSON struct {
	URL         string  `json:"url"`
	Token       string  `json:"token"`
	ASN         uint32  `json:"asn"`
	Limit       uint64  `json:"limit"`
	CurlWorkers uint32  `json:"curlWorkers"`
	Senders     *uint32 `json:"senders"`
	AgentID     string  `json:"agentId"`
}

// UnmarshalJSON migrates the pre-Masscan senders setting to CurlWorkers while
// continuing to reject unknown fields in the protected local config file.
func (cfg *Config) UnmarshalJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var value configJSON
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	workers := value.CurlWorkers
	if workers == 0 && value.Senders != nil {
		workers = *value.Senders
		if workers > maxCurlWorkers {
			workers = maxCurlWorkers
		}
	}
	*cfg = Config{
		URL:         value.URL,
		Token:       value.Token,
		ASN:         value.ASN,
		Limit:       value.Limit,
		CurlWorkers: workers,
		AgentID:     value.AgentID,
	}
	return nil
}

func Defaults() Config {
	return Config{Limit: 100, CurlWorkers: 100, AgentID: newAgentID()}
}

func ValidateBackendURL(value string) error {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("backend URL must be an HTTPS URL without credentials, query, or fragment")
	}
	return nil
}

func Validate(cfg Config) error {
	if err := ValidateBackendURL(cfg.URL); err != nil {
		return err
	}
	if strings.TrimSpace(cfg.Token) == "" {
		return errors.New("API token is required")
	}
	if cfg.Limit == 0 {
		return errors.New("aggregate Masscan rate limit must be greater than zero")
	}
	if cfg.CurlWorkers == 0 {
		return errors.New("curl worker count must be greater than zero")
	}
	if cfg.CurlWorkers > maxCurlWorkers {
		return errors.New("curl worker count cannot exceed 4096")
	}
	return nil
}

func ValidateForScan(cfg Config) error {
	if err := Validate(cfg); err != nil {
		return err
	}
	if cfg.ASN == 0 {
		return errors.New("ASN must be set before scanning")
	}
	if strings.TrimSpace(cfg.AgentID) == "" {
		return errors.New("agent ID is missing")
	}
	return nil
}

func MergeOverrides(base Config, overrides Overrides) Config {
	if overrides.URL != nil {
		base.URL = strings.TrimRight(strings.TrimSpace(*overrides.URL), "/")
	}
	if overrides.Token != nil {
		base.Token = *overrides.Token
	}
	if overrides.ASN != nil {
		base.ASN = *overrides.ASN
	}
	if overrides.Limit != nil {
		base.Limit = *overrides.Limit
	}
	if overrides.CurlWorkers != nil {
		base.CurlWorkers = *overrides.CurlWorkers
	}
	if base.Limit == 0 {
		base.Limit = 100
	}
	if base.CurlWorkers == 0 {
		base.CurlWorkers = 100
	}
	if base.AgentID == "" {
		base.AgentID = newAgentID()
	}
	return base
}

func Load(path string) (Config, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Config{}, fmt.Errorf("read agent config: %w", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return Config{}, errors.New("agent config permissions must be 0600 or stricter")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read agent config: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, errors.New("agent config is invalid JSON")
	}
	if err := configHasNoTrailingValue(decoder); err != nil {
		return Config{}, errors.New("agent config is invalid JSON")
	}
	if err := Validate(cfg); err != nil {
		return Config{}, fmt.Errorf("agent config is invalid: %w", err)
	}
	return cfg, nil
}

func configHasNoTrailingValue(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func Save(path string, cfg Config) error {
	if err := Validate(cfg); err != nil {
		return fmt.Errorf("cannot save agent config: %w", err)
	}
	if cfg.AgentID == "" {
		cfg.AgentID = newAgentID()
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return errors.New("cannot encode agent config")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return errors.New("cannot create protected config directory")
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".cf-san-config-*")
	if err != nil {
		return errors.New("cannot create protected config file")
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return errors.New("cannot protect config file permissions")
	}
	if _, err := temp.Write(append(data, '\n')); err != nil {
		_ = temp.Close()
		return errors.New("cannot write agent config")
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return errors.New("cannot flush agent config")
	}
	if err := temp.Close(); err != nil {
		return errors.New("cannot close agent config")
	}
	if err := os.Rename(tempPath, path); err != nil {
		return errors.New("cannot install protected agent config")
	}
	return nil
}

func newAgentID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "cf-san-agent"
	}
	return hex.EncodeToString(raw[:])
}
