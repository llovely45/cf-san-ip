package autoupdate

import (
	"bytes"
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
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

func TestLoadInstallerScriptRejectsNonHTTPSWorker(t *testing.T) {
	if _, err := loadInstallerScript(context.Background(), http.DefaultClient, "http://worker.example"); err == nil {
		t.Fatal("loadInstallerScript accepted a non-HTTPS Worker URL")
	}
}

func TestLoadInstallerScriptRejectsHTTPSDowngradeRedirect(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("HTTP downgrade target was contacted")
		_, _ = io.WriteString(w, validInstallerFixture)
	}))
	defer target.Close()

	worker := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer worker.Close()

	_, err := loadInstallerScript(context.Background(), worker.Client(), worker.URL)
	if err == nil || !strings.Contains(err.Error(), "non-HTTPS") {
		t.Fatalf("loadInstallerScript error = %v, want an HTTPS downgrade rejection", err)
	}
}

func TestLoadInstallerScriptRejectsCrossHostRedirect(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("cross-host redirect target was contacted")
		_, _ = io.WriteString(w, validInstallerFixture)
	}))
	defer target.Close()

	worker := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer worker.Close()

	client := worker.Client()
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("worker client transport = %T, want *http.Transport", client.Transport)
	}
	transport = transport.Clone()
	transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	transport.TLSClientConfig.RootCAs = transport.TLSClientConfig.RootCAs.Clone()
	transport.TLSClientConfig.RootCAs.AddCert(target.Certificate())
	client.Transport = transport

	_, err := loadInstallerScript(context.Background(), client, worker.URL)
	if err == nil || !strings.Contains(err.Error(), "different host") {
		t.Fatalf("loadInstallerScript error = %v, want a cross-host redirect rejection", err)
	}
}

func TestLoadInstallerScriptRejectsMalformedWorkerResponses(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "html response", body: "<html>not an installer</html>"},
		{name: "missing installer marker", body: "#!/bin/sh\necho update\n"},
		{name: "missing update mode marker", body: "#!/bin/sh\n# cf-san installer: old\necho update\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, tt.body)
			}))
			defer server.Close()

			if _, err := loadInstallerScript(context.Background(), server.Client(), server.URL); err == nil {
				t.Fatal("loadInstallerScript accepted a malformed response")
			}
		})
	}
}

func TestLoadInstallerScriptRejectsOversizedResponse(t *testing.T) {
	body := "#!/bin/sh\n# cf-san installer: supports --update-only\n" + strings.Repeat("x", maxInstallerScriptBytes)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()

	if _, err := loadInstallerScript(context.Background(), server.Client(), server.URL); err == nil {
		t.Fatal("loadInstallerScript accepted an oversized response")
	}
}

func TestRunInstallerScriptPassesPinnedVersionAndEnvironmentWithoutPrintingToken(t *testing.T) {
	var output bytes.Buffer
	cfg := config.Config{URL: "https://worker.example", Token: "secret-for-test"}
	script := "test \"$#\" -eq 3\ntest \"$1\" = --update-only\ntest \"$2\" = --version\ntest \"$3\" = v1.2.3\ntest \"$CF_SAN_BACKEND_URL\" = https://worker.example\ntest \"$CF_SAN_API_TOKEN\" = secret-for-test\necho update-started\n"
	if err := runInstallerScript(context.Background(), []byte(script), cfg, "v1.2.3", &output, "/bin/sh"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "update-started") {
		t.Fatalf("installer output = %q", output.String())
	}
	if strings.Contains(output.String(), cfg.Token) {
		t.Fatalf("installer output unexpectedly printed the token: %q", output.String())
	}
}

func TestRunInstallerScriptLeavesManualUpdateUnpinned(t *testing.T) {
	var output bytes.Buffer
	cfg := config.Config{URL: "https://worker.example", Token: "secret-for-test"}
	script := "test \"$#\" -eq 1\ntest \"$1\" = --update-only\ntest \"$CF_SAN_BACKEND_URL\" = https://worker.example\ntest \"$CF_SAN_API_TOKEN\" = secret-for-test\necho latest-update\n"
	if err := runInstallerScript(context.Background(), []byte(script), cfg, "", &output, "/bin/sh"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "latest-update") {
		t.Fatalf("installer output = %q", output.String())
	}
}

func TestInstallFromWorkerRejectsInvalidVersionBeforeDownload(t *testing.T) {
	cfg := config.Config{
		URL:               "https://worker.example",
		Token:             "secret-for-test",
		Limit:             1,
		CurlWorkers:       1,
		MasscanCPUPercent: 1,
	}
	for _, version := range []string{"latest", "v1.2.3-rc.1", "v1.02.3", "v1.2"} {
		t.Run(version, func(t *testing.T) {
			if err := InstallFromWorker(context.Background(), cfg, version, io.Discard); err == nil {
				t.Fatalf("InstallFromWorker accepted invalid version %q", version)
			}
		})
	}
}

func TestInstallFromWorkerPassesPinnedVersion(t *testing.T) {
	const token = "worker-install-secret"
	var backendURL string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "#!/bin/sh\n# cf-san installer: supports --update-only\ntest \"$#\" -eq 3\ntest \"$1\" = --update-only\ntest \"$2\" = --version\ntest \"$3\" = v1.2.3\ntest \"$CF_SAN_BACKEND_URL\" = \""+backendURL+"\"\ntest \"$CF_SAN_API_TOKEN\" = "+token+"\nprintf 'pinned update\\n'\n")
	}))
	defer server.Close()
	backendURL = server.URL

	originalTransport := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = originalTransport })

	cfg := config.Config{
		URL:               backendURL,
		Token:             token,
		Limit:             1,
		CurlWorkers:       1,
		MasscanCPUPercent: 1,
	}
	var output bytes.Buffer
	if err := InstallFromWorker(context.Background(), cfg, "v1.2.3", &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "pinned update") {
		t.Fatalf("installer output = %q", output.String())
	}
	if strings.Contains(output.String(), token) {
		t.Fatalf("installer output unexpectedly printed the token: %q", output.String())
	}
}
