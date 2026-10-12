package probe

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func makeCurl(t *testing.T, dir, output string, exitCode int) string {
	t.Helper()
	path := filepath.Join(dir, "curl")
	script := fmt.Sprintf("#!/bin/sh\nset -eu\nprintf '%%s\\n' \"$*\" > \"$CURL_ARGS_FILE\"\nprintf '%%s' \"$CURL_RESPONSE\"\nexit %d\n", exitCode)
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CURL_RESPONSE", output)
	return path
}

func TestFetchCFTraceUsesCurlOnIPAndConventionalPath(t *testing.T) {
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "curl.args")
	t.Setenv("CURL_ARGS_FILE", argsPath)
	curl := makeCurl(t, dir, "HTTP/2 404\r\nserver: cloudflare\r\n\r\nfl=abc\nloc=US\n", 0)
	result, err := FetchCFTrace(context.Background(), "8.8.8.8", curl)
	if err != nil {
		t.Fatal(err)
	}
	if !ValidateCFResolveResponse(result.RawResponse) {
		t.Fatalf("raw response was rejected: %q", result.RawResponse)
	}
	if result.Body != "fl=abc\nloc=US\n" {
		t.Fatalf("body = %q", result.Body)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--silent", "--show-error", "--include", "--connect-timeout 1", "--max-time 1", "https://8.8.8.8:443/cdn-cgi/trace"} {
		if !strings.Contains(string(args), want) {
			t.Errorf("curl args %q do not contain %q", args, want)
		}
	}
	if strings.Contains(string(args), "--insecure") {
		t.Fatalf("curl args disable TLS certificate verification: %s", args)
	}
	if strings.Contains(string(args), "Authorization") || strings.Contains(string(args), "token") {
		t.Fatalf("backend credentials appeared in curl args: %s", args)
	}
}

func TestFetchCFTraceRejectsInvalidIPAndCurlFailure(t *testing.T) {
	if _, err := FetchCFTrace(context.Background(), "not-an-ip", "curl"); err == nil {
		t.Fatal("FetchCFTrace() error = nil for invalid address")
	}
	dir := t.TempDir()
	curl := makeCurl(t, dir, "", 7)
	if _, err := FetchCFTrace(context.Background(), "8.8.8.8", curl); err == nil {
		t.Fatal("FetchCFTrace() error = nil when curl fails")
	}
}
