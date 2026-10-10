package config

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	URL     string `json:"url"`
	Token   string `json:"token"`
	ASN     uint32 `json:"asn"`
	Limit   uint64 `json:"limit"`
	Senders uint32 `json:"senders"`
	AgentID string `json:"agentId"`
}

type Overrides struct {
	URL     *string
	Token   *string
	ASN     *uint32
	Limit   *uint64
	Senders *uint32
}

func Defaults() Config {
	return Config{Limit: 100, Senders: 100, AgentID: newAgentID()}
}

func Validate(cfg Config) error {
	parsed, err := url.Parse(strings.TrimSpace(cfg.URL))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("backend URL must be an HTTPS URL without credentials, query, or fragment")
	}
	if strings.TrimSpace(cfg.Token) == "" {
		return errors.New("API token is required")
	}
	if cfg.Limit == 0 {
		return errors.New("ZMap rate limit must be greater than zero")
	}
	if cfg.Senders == 0 {
		return errors.New("ZGrab2 sender count must be greater than zero")
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
	if overrides.Senders != nil {
		base.Senders = *overrides.Senders
	}
	if base.Limit == 0 {
		base.Limit = 100
	}
	if base.Senders == 0 {
		base.Senders = 100
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
	if err := Validate(cfg); err != nil {
		return Config{}, fmt.Errorf("agent config is invalid: %w", err)
	}
	return cfg, nil
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
