package scanner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cf-san/internal/backend"
	"cf-san/internal/prefix"
	"cf-san/internal/probe"
)

type reportCollector struct {
	mu      sync.Mutex
	reports []backend.Report
}

func (c *reportCollector) Report(_ context.Context, report backend.Report) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reports = append(c.reports, report)
	return nil
}

func (c *reportCollector) snapshot() []backend.Report {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]backend.Report(nil), c.reports...)
}

func makeExecutable(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -eu\n"+content), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func testBlocklist(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "blocklist.conf")
	if err := os.WriteFile(path, []byte("192.0.2.0/24\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func baseBlock(cidr string) prefix.Block {
	return prefix.Block{ASN: 64512, SourcePrefix: "8.8.8.0/23", BlockCIDR: cidr, Status: "active"}
}

func response(server, body string) probe.TraceResponse {
	raw := "HTTP/1.1 404 Not Found\r\nServer: " + server + "\r\n\r\n" + body
	return probe.TraceResponse{RawResponse: raw, Body: body}
}

func TestRunUsesZMapRateAndZGrabSendersAndReportsOnlyValidCFTrace(t *testing.T) {
	dir := t.TempDir()
	zmapArgs := filepath.Join(dir, "zmap.args")
	zgrabArgs := filepath.Join(dir, "zgrab.args")
	t.Setenv("ZMAP_ARGS_FILE", zmapArgs)
	t.Setenv("ZGRAB_ARGS_FILE", zgrabArgs)
	makeExecutable(t, dir, "zmap", `
  printf '%s\n' "$*" >> "$ZMAP_ARGS_FILE"
  printf '8.8.8.10\n8.8.8.11\n8.8.8.12\n8.8.8.13\n'
`)
	makeExecutable(t, dir, "zgrab2", `
  printf '%s\n' "$*" > "$ZGRAB_ARGS_FILE"
  while IFS= read -r ip; do
    case "$ip" in
      8.8.8.10|8.8.8.11|8.8.8.12|8.8.8.13) printf '{"ip":"%s","data":{"tls":{"status":"success"}}}\n' "$ip" ;;
    esac
  done
  printf 'not-json\n'
`)
	collector := &reportCollector{}
	var probed []string
	runner := Runner{
		Config:   RunnerConfig{ZMapPath: filepath.Join(dir, "zmap"), ZGrab2Path: filepath.Join(dir, "zgrab2"), BlocklistPath: testBlocklist(t, dir), Limit: 17, Senders: 23, HeartbeatInterval: time.Hour},
		Reporter: collector,
		TraceProbe: func(_ context.Context, ip string) (probe.TraceResponse, error) {
			probed = append(probed, ip)
			switch ip {
			case "8.8.8.10":
				return response("cloudflare", "fl=abc\nloc=US\nts=1\n"), nil
			case "8.8.8.11":
				return response("nginx", "loc=US\n"), nil
			case "8.8.8.12":
				return response("cloudflare", "error code: 1034\nloc=US\n"), nil
			default:
				return response("cloudflare", "fl=abc\nts=1\n"), nil
			}
		},
	}
	blocks := []prefix.Block{baseBlock("8.8.8.0/24")}
	if err := runner.Run(context.Background(), 64512, blocks); err != nil {
		t.Fatal(err)
	}

	zmapData, err := os.ReadFile(zmapArgs)
	if err != nil {
		t.Fatal(err)
	}
	for _, argument := range []string{"-p 443", "-r 17", "--output-module=csv", "--output-fields=saddr", "--output-filter=success = 1", "8.8.8.0/24"} {
		if !strings.Contains(string(zmapData), argument) {
			t.Errorf("ZMap args %q do not contain %q", zmapData, argument)
		}
	}
	if strings.Contains(string(zmapData), "--no-header-row") {
		t.Fatalf("ZMap args contain an unsupported option: %q", zmapData)
	}
	zgrabData, err := os.ReadFile(zgrabArgs)
	if err != nil {
		t.Fatal(err)
	}
	for _, argument := range []string{"tls", "--port 443", "--senders 23", "--connect-timeout 5s", "--target-timeout 8s", "--blocklist-file", filepath.Join(dir, "blocklist.conf")} {
		if !strings.Contains(string(zgrabData), argument) {
			t.Errorf("ZGrab2 args %q do not contain %q", zgrabData, argument)
		}
	}
	if len(probed) != 4 {
		t.Fatalf("trace probe count = %d, want 4 (TLS-success JSON rows only)", len(probed))
	}
	reports := collector.snapshot()
	if len(reports) != 3 || reports[0].Status != "active" || reports[1].Candidate == nil || reports[1].Candidate.Region != "US" || reports[2].Status != "completed" {
		t.Fatalf("reports = %#v, want active, one valid candidate, completed", reports)
	}
}

func TestRunScansBlocksInAscendingOrderAndSkipsCompletedBlocks(t *testing.T) {
	dir := t.TempDir()
	zmapArgs := filepath.Join(dir, "zmap.args")
	t.Setenv("ZMAP_ARGS_FILE", zmapArgs)
	makeExecutable(t, dir, "zmap", `printf '%s\n' "$*" >> "$ZMAP_ARGS_FILE"; exit 0`)
	makeExecutable(t, dir, "zgrab2", `cat >/dev/null`)
	collector := &reportCollector{}
	runner := Runner{
		Config:   RunnerConfig{ZMapPath: filepath.Join(dir, "zmap"), ZGrab2Path: filepath.Join(dir, "zgrab2"), BlocklistPath: testBlocklist(t, dir), Limit: 100, Senders: 10, HeartbeatInterval: time.Hour},
		Reporter: collector,
		TraceProbe: func(context.Context, string) (probe.TraceResponse, error) {
			t.Fatal("no probe expected")
			return probe.TraceResponse{}, nil
		},
	}
	blocks := []prefix.Block{
		{SourcePrefix: "8.8.8.0/23", BlockCIDR: "8.8.9.0/24", Status: "active"},
		{SourcePrefix: "8.8.8.0/23", BlockCIDR: "8.8.8.0/24", Status: "completed"},
	}
	if err := runner.Run(context.Background(), 64512, blocks); err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(zmapArgs)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(args)); !strings.HasSuffix(got, "8.8.9.0/24") {
		t.Fatalf("only pending block should be scanned, args = %q", got)
	}
	reports := collector.snapshot()
	if len(reports) != 2 || reports[0].BlockCIDR != "8.8.9.0/24" || reports[1].Status != "completed" {
		t.Fatalf("reports = %#v", reports)
	}
}

func TestHeartbeatReportsActiveBlockDuringLongScan(t *testing.T) {
	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	makeExecutable(t, dir, "zmap", `printf '8.8.8.10\n'; touch "$STARTED_FILE"`)
	makeExecutable(t, dir, "zgrab2", `while IFS= read -r ip; do printf '{"ip":"%s","data":{"tls":{"status":"failure"}}}\n' "$ip"; sleep 0.18; done`)
	collector := &reportCollector{}
	runner := Runner{
		Config:   RunnerConfig{ZMapPath: filepath.Join(dir, "zmap"), ZGrab2Path: filepath.Join(dir, "zgrab2"), BlocklistPath: testBlocklist(t, dir), Limit: 100, Senders: 10, HeartbeatInterval: 20 * time.Millisecond},
		Reporter: collector,
		TraceProbe: func(context.Context, string) (probe.TraceResponse, error) {
			t.Fatal("TLS failure must not probe HTTPS")
			return probe.TraceResponse{}, nil
		},
	}
	t.Setenv("STARTED_FILE", started)
	if err := runner.Run(context.Background(), 64512, []prefix.Block{baseBlock("8.8.8.0/24")}); err != nil {
		t.Fatal(err)
	}
	activeReports := 0
	for _, report := range collector.snapshot() {
		if report.Status == "active" && report.Candidate == nil {
			activeReports++
		}
	}
	if activeReports < 2 {
		t.Fatalf("active progress reports = %d, want start plus heartbeat", activeReports)
	}
	foundLatest := false
	for _, report := range collector.snapshot() {
		if report.Status == "active" && report.Candidate == nil && report.LatestIP == "8.8.8.10" {
			foundLatest = true
		}
	}
	if !foundLatest {
		t.Fatal("heartbeat did not report the latest failed TLS target IP")
	}
}

func TestCancellationLeavesActiveBlockIncomplete(t *testing.T) {
	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	makeExecutable(t, dir, "zmap", `touch "$STARTED_FILE"; sleep 30`)
	makeExecutable(t, dir, "zgrab2", `cat >/dev/null`)
	collector := &reportCollector{}
	runner := Runner{
		Config:     RunnerConfig{ZMapPath: filepath.Join(dir, "zmap"), ZGrab2Path: filepath.Join(dir, "zgrab2"), BlocklistPath: testBlocklist(t, dir), Limit: 100, Senders: 10, HeartbeatInterval: time.Hour},
		Reporter:   collector,
		TraceProbe: func(context.Context, string) (probe.TraceResponse, error) { return probe.TraceResponse{}, nil },
	}
	t.Setenv("STARTED_FILE", started)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- runner.Run(ctx, 64512, []prefix.Block{baseBlock("8.8.8.0/24")}) }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("ZMap fake did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-result; err == nil {
		t.Fatal("Run() error = nil after cancellation")
	}
	for _, report := range collector.snapshot() {
		if report.Status == "completed" {
			t.Fatal("cancelled scan marked its active block complete")
		}
	}
}

func TestValidateBlocklistFileRejectsMissingAndEmptyFiles(t *testing.T) {
	if err := ValidateBlocklistFile(filepath.Join(t.TempDir(), "missing.conf")); err == nil {
		t.Fatal("ValidateBlocklistFile accepted a missing blocklist")
	}
	empty := filepath.Join(t.TempDir(), "empty.conf")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateBlocklistFile(empty); err == nil {
		t.Fatal("ValidateBlocklistFile accepted an empty blocklist")
	}
}
