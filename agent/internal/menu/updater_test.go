package menu

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cf-san/internal/config"
)

const validInstallerFixture = "#!/bin/sh\n# cf-san installer: supports --update-only\necho updated\n"

func TestLoadInstallerScriptFromWorker(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/prefix/install" {
			t.Errorf("request = %s %s, want GET /prefix/install", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/x-shellscript")
		_, _ = io.WriteString(w, validInstallerFixture)
	}))
	defer server.Close()

	script, err := loadInstallerScript(context.Background(), server.Client(), server.URL+"/prefix/")
	if err != nil {
		t.Fatal(err)
	}
	if string(script) != validInstallerFixture {
		t.Fatalf("installer script = %q", script)
	}
}

func TestLoadInstallerScriptRejectsNonInstallerResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "<html>not an installer</html>")
	}))
	defer server.Close()

	if _, err := loadInstallerScript(context.Background(), server.Client(), server.URL); err == nil {
		t.Fatal("loadInstallerScript accepted an HTML response")
	}
}

func TestRunInstallerScriptPassesConfigWithoutPrintingToken(t *testing.T) {
	var output strings.Builder
	cfg := config.Config{URL: "https://worker.example", Token: "secret-for-test"}
	script := "test \"$1\" = --update-only\ntest \"$CF_SAN_BACKEND_URL\" = https://worker.example\ntest \"$CF_SAN_API_TOKEN\" = secret-for-test\necho update-started\n"
	if err := runInstallerScript(context.Background(), []byte(script), cfg, &output, "/bin/sh"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "update-started") {
		t.Fatalf("installer output = %q", output.String())
	}
	if strings.Contains(output.String(), cfg.Token) {
		t.Fatalf("installer output unexpectedly printed the token: %q", output.String())
	}
}
