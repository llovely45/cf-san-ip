package autoupdate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"cf-san/internal/buildinfo"
	"cf-san/internal/config"
)

const (
	maxInstallerScriptBytes = 1 << 20
	workerRequestTimeout    = 30 * time.Second
)

// InstallFromWorker fetches and executes the installer served by the Worker.
// An empty version preserves the menu updater's latest-release behavior. A
// nonempty version pins the installer to one exact stable release tag.
func InstallFromWorker(ctx context.Context, cfg config.Config, version string, output io.Writer) error {
	if ctx == nil {
		return errors.New("installer context must not be nil")
	}
	if err := config.Validate(cfg); err != nil {
		return err
	}
	if err := validateInstallerVersion(version); err != nil {
		return err
	}

	client := &http.Client{Timeout: workerRequestTimeout}
	script, err := loadInstallerScript(ctx, client, cfg.URL)
	if err != nil {
		return err
	}
	return runInstallerScript(ctx, script, cfg, version, output, "sh")
}

func validateInstallerVersion(version string) error {
	if version == "" {
		return nil
	}
	if _, err := buildinfo.CompareStableTags(version, version); err != nil {
		return fmt.Errorf("installer version is invalid: %w", err)
	}
	return nil
}

func loadInstallerScript(ctx context.Context, client *http.Client, backendURL string) ([]byte, error) {
	if ctx == nil {
		return nil, errors.New("installer context must not be nil")
	}
	base, err := url.Parse(strings.TrimRight(backendURL, "/"))
	if err != nil || base.Scheme != "https" || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, errors.New("backend URL is invalid")
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/install"
	base.RawPath = ""
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
	if err != nil {
		return nil, errors.New("cannot create Worker installer request")
	}
	request.Header.Set("Accept", "text/x-shellscript")
	request.Header.Set("User-Agent", "cf-san-agent/1")
	if client == nil {
		client = &http.Client{Timeout: workerRequestTimeout}
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("cannot download Worker installer script: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("Worker installer endpoint returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxInstallerScriptBytes+1))
	if err != nil {
		return nil, errors.New("cannot read Worker installer script")
	}
	if len(data) > maxInstallerScriptBytes {
		return nil, errors.New("Worker installer script exceeds size limit")
	}
	if !bytes.HasPrefix(data, []byte("#!/bin/sh")) || !bytes.Contains(data, []byte("cf-san installer:")) || !bytes.Contains(data, []byte("--update-only")) {
		return nil, errors.New("Worker installer is outdated or invalid; update the Worker first")
	}
	return data, nil
}

func runInstallerScript(ctx context.Context, script []byte, cfg config.Config, version string, output io.Writer, shell string) error {
	if ctx == nil {
		return errors.New("installer context must not be nil")
	}
	if err := validateInstallerVersion(version); err != nil {
		return err
	}
	if shell == "" {
		shell = "sh"
	}
	args := []string{"-s", "--", "--update-only"}
	if version != "" {
		args = append(args, "--version", version)
	}
	command := exec.CommandContext(ctx, shell, args...)
	command.Stdin = bytes.NewReader(script)
	if output == nil {
		output = io.Discard
	}
	command.Stdout = output
	command.Stderr = output
	command.Env = replaceEnvironment(os.Environ(), map[string]string{
		"CF_SAN_BACKEND_URL": cfg.URL,
		"CF_SAN_API_TOKEN":   cfg.Token,
	})
	if err := command.Run(); err != nil {
		return fmt.Errorf("update installer failed: %w", err)
	}
	return nil
}

func replaceEnvironment(environment []string, overrides map[string]string) []string {
	result := make([]string, 0, len(environment)+len(overrides))
	for _, entry := range environment {
		key, _, found := strings.Cut(entry, "=")
		if !found {
			result = append(result, entry)
			continue
		}
		if _, replace := overrides[key]; !replace {
			result = append(result, entry)
		}
	}
	for key, value := range overrides {
		result = append(result, key+"="+value)
	}
	return result
}
