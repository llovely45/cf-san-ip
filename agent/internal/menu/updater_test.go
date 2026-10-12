package menu

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

func TestUpdateLatestFromWorkerUsesUnpinnedInstaller(t *testing.T) {
	const token = "manual-update-secret"
	var backendURL string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/install" {
			t.Errorf("request = %s %s, want GET /install", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/x-shellscript")
		_, _ = io.WriteString(w, "#!/bin/sh\n# cf-san installer: supports --update-only\ntest \"$#\" -eq 1\ntest \"$1\" = --update-only\ntest \"$CF_SAN_BACKEND_URL\" = \""+backendURL+"\"\ntest \"$CF_SAN_API_TOKEN\" = "+token+"\nprintf 'manual update\\n'\n")
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
	if err := updateLatestFromWorker(context.Background(), cfg, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "manual update") {
		t.Fatalf("installer output = %q", output.String())
	}
	if strings.Contains(output.String(), token) {
		t.Fatalf("installer output unexpectedly printed the token: %q", output.String())
	}
}
