package scanner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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

func baseBlock(cidr string) prefix.Block {
	return prefix.Block{ASN: 64512, SourcePrefix: "8.8.8.0/23", BlockCIDR: cidr, Status: "active"}
}

func response(server, body string) probe.TraceResponse {
	raw := "HTTP/1.1 404 Not Found\r\nServer: " + server + "\r\n\r\n" + body
	return probe.TraceResponse{RawResponse: raw, Body: body}
}

func TestParseMasscanInteractiveLineAcceptsOnlyOpenTCP443InBlock(t *testing.T) {
	block := baseBlock("8.8.8.0/24")
	tests := []struct {
		line string
		want string
	}{
		{"Discovered open port 443/tcp on 8.8.8.10", "8.8.8.10"},
		{"Discovered open port 80/tcp on 8.8.8.10", ""},
		{"Discovered open port 443/udp on 8.8.8.10", ""},
		{"Discovered closed port 443/tcp on 8.8.8.10", ""},
		{"Discovered open port 443/tcp on 9.9.9.9", ""},
		{"#masscan output", ""},
		{"Discovered open port 443/tcp on not-an-ip", ""},
	}
	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			got, ok := parseMasscanInteractiveOpen443(tt.line, block)
			if tt.want == "" {
				if ok {
					t.Fatalf("parseMasscanInteractiveOpen443(%q) = %q, want no target", tt.line, got)
				}
				return
			}
			if !ok || got != tt.want {
				t.Fatalf("parseMasscanInteractiveOpen443(%q) = (%q, %v), want (%q, true)", tt.line, got, ok, tt.want)
			}
		})
	}
}

func TestRunUsesMasscanThenConcurrentCurlValidation(t *testing.T) {
	dir := t.TempDir()
	masscanArgs := filepath.Join(dir, "masscan.args")
	t.Setenv("MASSCAN_ARGS_FILE", masscanArgs)
	masscan := makeExecutable(t, dir, "masscan", fmt.Sprintf(`
  printf '%%s\\n' "$*" > "$MASSCAN_ARGS_FILE"
  cat <<'OUTPUT'
Discovered open port 443/tcp on 8.8.8.10
Discovered open port 443/tcp on 8.8.8.11
Discovered open port 80/tcp on 8.8.8.12
Discovered open port 443/tcp on 8.8.8.12
Discovered open port 443/tcp on 8.8.8.13
Discovered open port 443/tcp on 8.8.8.10
Discovered open port 443/tcp on 9.9.9.9
OUTPUT
`))

	collector := &reportCollector{}
	var active, maxActive atomic.Int32
	var callsMu sync.Mutex
	calls := make(map[string]int)
	runner := Runner{
		Config:   RunnerConfig{MasscanPath: masscan, Limit: 17, CurlWorkers: 2, HeartbeatInterval: time.Hour},
		Reporter: collector,
		TraceProbe: func(_ context.Context, ip string) (probe.TraceResponse, error) {
			n := active.Add(1)
			for previous := maxActive.Load(); n > previous && !maxActive.CompareAndSwap(previous, n); previous = maxActive.Load() {
			}
			time.Sleep(20 * time.Millisecond)
			active.Add(-1)
			callsMu.Lock()
			calls[ip]++
			callsMu.Unlock()
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
	if err := runner.Run(context.Background(), 64512, []prefix.Block{baseBlock("8.8.8.0/24")}); err != nil {
		t.Fatal(err)
	}

	args, err := os.ReadFile(masscanArgs)
	if err != nil {
		t.Fatal(err)
	}
	for _, argument := range []string{"--rate 17", "--wait 1", "-p 443", "--interactive", "--nostatus", "8.8.8.0/24"} {
		if !strings.Contains(string(args), argument) {
			t.Errorf("Masscan args %q do not contain %q", args, argument)
		}
	}
	for _, removed := range []string{"--output-flush", "-oL", "zmap", "zgrab2"} {
		if strings.Contains(strings.ToLower(string(args)), removed) {
			t.Errorf("scanner arguments still reference removed tool %q: %s", removed, args)
		}
	}
	if len(calls) != 4 {
		t.Fatalf("curl validations = %v, want 4 unique open TCP/443 targets in the block", calls)
	}
	if calls["8.8.8.10"] != 1 || calls["8.8.8.11"] != 1 || calls["8.8.8.12"] != 1 || calls["8.8.8.13"] != 1 {
		t.Fatalf("curl validation targets = %v", calls)
	}
	if got := maxActive.Load(); got != 2 {
		t.Fatalf("maximum concurrent curl validations = %d, want 2", got)
	}
	reports := collector.snapshot()
	validCandidates := 0
	for _, report := range reports {
		if report.Candidate != nil {
			validCandidates++
			if report.Candidate.IP != "8.8.8.10" || report.Candidate.Region != "US" {
				t.Fatalf("unexpected candidate report: %#v", report.Candidate)
			}
		}
	}
	if validCandidates != 1 || reports[0].Status != "active" || reports[len(reports)-1].Status != "completed" {
		t.Fatalf("reports = %#v, want active, one valid candidate, completed", reports)
	}
}

func TestRunScansBlocksInAscendingOrderAndSkipsCompletedBlocks(t *testing.T) {
	dir := t.TempDir()
	masscanArgs := filepath.Join(dir, "masscan.args")
	t.Setenv("MASSCAN_ARGS_FILE", masscanArgs)
	masscan := makeExecutable(t, dir, "masscan", `printf '%s\n' "$*" >> "$MASSCAN_ARGS_FILE"`)
	collector := &reportCollector{}
	runner := Runner{
		Config:   RunnerConfig{MasscanPath: masscan, Limit: 100, CurlWorkers: 10, HeartbeatInterval: time.Hour},
		Reporter: collector,
		TraceProbe: func(context.Context, string) (probe.TraceResponse, error) {
			t.Fatal("no open targets expected")
			return probe.TraceResponse{}, nil
		},
	}
	blocks := []prefix.Block{
		{SourcePrefix: "8.8.8.0/22", BlockCIDR: "8.8.10.0/24", Status: "active"},
		{SourcePrefix: "8.8.8.0/22", BlockCIDR: "8.8.8.0/24", Status: "active"},
		{SourcePrefix: "8.8.8.0/22", BlockCIDR: "8.8.9.0/24", Status: "completed"},
	}
	if err := runner.Run(context.Background(), 64512, blocks); err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(masscanArgs)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(args)), "\n")
	if len(lines) != 2 || !strings.HasSuffix(lines[0], "8.8.8.0/24") || !strings.HasSuffix(lines[1], "8.8.10.0/24") {
		t.Fatalf("Masscan block order = %q, want pending 8.8.8.0/24 then 8.8.10.0/24", lines)
	}
	reports := collector.snapshot()
	if len(reports) != 4 || reports[0].BlockCIDR != "8.8.8.0/24" || reports[2].BlockCIDR != "8.8.10.0/24" || reports[3].Status != "completed" {
		t.Fatalf("reports = %#v", reports)
	}
}

func TestHeartbeatReportsLatestOpenPortDuringLongMasscanRun(t *testing.T) {
	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	masscan := makeExecutable(t, dir, "masscan", `printf 'Discovered open port 443/tcp on 8.8.8.10\n'; touch "$STARTED_FILE"; sleep 0.18`)
	collector := &reportCollector{}
	runner := Runner{
		Config:   RunnerConfig{MasscanPath: masscan, Limit: 100, CurlWorkers: 2, HeartbeatInterval: 20 * time.Millisecond},
		Reporter: collector,
		TraceProbe: func(context.Context, string) (probe.TraceResponse, error) {
			return probe.TraceResponse{}, fmt.Errorf("curl failed")
		},
	}
	t.Setenv("STARTED_FILE", started)
	if err := runner.Run(context.Background(), 64512, []prefix.Block{baseBlock("8.8.8.0/24")}); err != nil {
		t.Fatal(err)
	}
	activeReports := 0
	foundLatest := false
	for _, report := range collector.snapshot() {
		if report.Status == "active" && report.Candidate == nil {
			activeReports++
			if report.LatestIP == "8.8.8.10" {
				foundLatest = true
			}
		}
	}
	if activeReports < 2 || !foundLatest {
		t.Fatalf("active progress reports = %d, latest open IP reported = %v", activeReports, foundLatest)
	}
}

func TestCancellationLeavesActiveMasscanBlockIncomplete(t *testing.T) {
	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	masscan := makeExecutable(t, dir, "masscan", `touch "$STARTED_FILE"; sleep 30`)
	collector := &reportCollector{}
	runner := Runner{
		Config:   RunnerConfig{MasscanPath: masscan, Limit: 100, CurlWorkers: 2, HeartbeatInterval: time.Hour},
		Reporter: collector,
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
			t.Fatal("Masscan fake did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-result; err == nil {
		t.Fatal("Run() error = nil after cancellation")
	}
	for _, report := range collector.snapshot() {
		if report.Status == "completed" {
			t.Fatal("cancelled scan marked its active block completed")
		}
	}
}
