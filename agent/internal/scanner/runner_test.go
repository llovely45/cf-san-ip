package scanner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
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

type sharedTestLeases struct {
	mu        sync.Mutex
	owners    map[string]string
	claimed   map[string]string
	completed map[string]bool
	reports   []backend.Report
	releases  int
}

type testLeaseAgent struct {
	id     string
	shared *sharedTestLeases
}

func (a *testLeaseAgent) ClaimBlocks(_ context.Context, asn uint32, candidates []prefix.Block, limit uint32) ([]backend.BlockClaim, int, error) {
	a.shared.mu.Lock()
	defer a.shared.mu.Unlock()
	if a.shared.owners == nil {
		a.shared.owners = make(map[string]string)
		a.shared.claimed = make(map[string]string)
		a.shared.completed = make(map[string]bool)
	}
	var results []backend.BlockClaim
	claimedCount := uint32(0)
	for _, candidate := range candidates {
		block := candidate
		block.ASN = asn
		if a.shared.completed[block.BlockCIDR] {
			results = append(results, backend.BlockClaim{Block: block, State: "completed"})
			continue
		}
		if _, exists := a.shared.owners[block.BlockCIDR]; exists {
			results = append(results, backend.BlockClaim{Block: block, State: "leased"})
			continue
		}
		leaseID := a.id + ":" + block.BlockCIDR
		block.LeaseID = leaseID
		a.shared.owners[block.BlockCIDR] = leaseID
		a.shared.claimed[block.BlockCIDR] = a.id
		results = append(results, backend.BlockClaim{Block: block, State: "claimed"})
		claimedCount++
		if claimedCount >= limit {
			break
		}
	}
	return results, len(results), nil
}

func (a *testLeaseAgent) ReleaseBlock(_ context.Context, block prefix.Block, leaseID string) error {
	a.shared.mu.Lock()
	defer a.shared.mu.Unlock()
	if a.shared.owners[block.BlockCIDR] == leaseID {
		delete(a.shared.owners, block.BlockCIDR)
		a.shared.releases++
	}
	return nil
}

func (a *testLeaseAgent) Report(_ context.Context, report backend.Report) error {
	a.shared.mu.Lock()
	defer a.shared.mu.Unlock()
	a.shared.reports = append(a.shared.reports, report)
	if report.Status == "completed" && a.shared.owners[report.BlockCIDR] == report.LeaseID {
		delete(a.shared.owners, report.BlockCIDR)
		a.shared.completed[report.BlockCIDR] = true
	}
	return nil
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

func makeCPULimitStub(t *testing.T, dir string) string {
	t.Helper()
	return makeExecutable(t, dir, "cpulimit", `
  if [ "$1" = "--help" ]; then
    echo 'launch target in foreground and wait'
    exit 0
  fi
  if [ "$1" = "-f" ]; then shift; fi
  [ "$1" = "-l" ] || exit 11
  shift 2
  [ "$1" = "--" ] || exit 13
  shift
  exec "$@"
`)
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
	var scanLog strings.Builder
	var active, maxActive atomic.Int32
	var callsMu sync.Mutex
	calls := make(map[string]int)
	runner := Runner{
		Config:    RunnerConfig{MasscanPath: masscan, CPULimitPath: makeCPULimitStub(t, dir), Limit: 17, CurlWorkers: 2, BlockWorkers: 1, HeartbeatInterval: time.Hour},
		Reporter:  collector,
		LogOutput: &scanLog,
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
	for _, argument := range []string{"--rate 17", "--wait 1", "-p 443", "--interactive", "8.8.8.0/24"} {
		if !strings.Contains(string(args), argument) {
			t.Errorf("Masscan args %q do not contain %q", args, argument)
		}
	}
	if strings.Contains(string(args), "--nostatus") {
		t.Errorf("Masscan args %q must allow status output", args)
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
	for _, want := range []string{"scanning block 8.8.8.0/24", "completed block 8.8.8.0/24"} {
		if !strings.Contains(scanLog.String(), want) {
			t.Errorf("scan log %q does not contain %q", scanLog.String(), want)
		}
	}
}

func TestRunScansBlocksInAscendingOrderAndSkipsCompletedBlocks(t *testing.T) {
	dir := t.TempDir()
	masscanArgs := filepath.Join(dir, "masscan.args")
	t.Setenv("MASSCAN_ARGS_FILE", masscanArgs)
	masscan := makeExecutable(t, dir, "masscan", `printf '%s\n' "$*" >> "$MASSCAN_ARGS_FILE"`)
	collector := &reportCollector{}
	runner := Runner{
		Config:   RunnerConfig{MasscanPath: masscan, CPULimitPath: makeCPULimitStub(t, dir), Limit: 100, CurlWorkers: 10, BlockWorkers: 1, HeartbeatInterval: time.Hour},
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

func TestRunScansFourBlocksConcurrentlyAndSharesConfiguredLimits(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "masscan.args")
	startedDir := filepath.Join(dir, "started")
	if err := os.Mkdir(startedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MASSCAN_ARGS_FILE", argsFile)
	t.Setenv("MASSCAN_STARTED_DIR", startedDir)
	masscan := makeExecutable(t, dir, "masscan", `
  printf '%s\n' "$*" >> "$MASSCAN_ARGS_FILE"
  last=""
  for argument do last="$argument"; done
  case "$last" in
    8.8.0.0/24) prefix=8.8.0 ;;
    8.8.1.0/24) prefix=8.8.1 ;;
    8.8.2.0/24) prefix=8.8.2 ;;
    8.8.3.0/24) prefix=8.8.3 ;;
    *) exit 8 ;;
  esac
  marker=$(printf '%s' "$last" | tr '/.' '__')
  touch "$MASSCAN_STARTED_DIR/$marker.started"
  attempt=0
  while :; do
    count=$(find "$MASSCAN_STARTED_DIR" -type f -name '*.started' | wc -l | tr -d ' ')
    [ "$count" -ge 4 ] && break
    attempt=$((attempt + 1))
    [ "$attempt" -lt 300 ] || exit 9
    sleep 0.01
  done
  for host in 1 2 3 4; do
    printf 'Discovered open port 443/tcp on %s.%s\n' "$prefix" "$host"
  done
`)
	collector := &reportCollector{}
	var active, maxActive atomic.Int32
	var validations atomic.Int32
	runner := Runner{
		Config:   RunnerConfig{MasscanPath: masscan, CPULimitPath: makeCPULimitStub(t, dir), Limit: 100, CurlWorkers: 8, BlockWorkers: 4, HeartbeatInterval: time.Hour},
		Reporter: collector,
		TraceProbe: func(context.Context, string) (probe.TraceResponse, error) {
			n := active.Add(1)
			for previous := maxActive.Load(); n > previous && !maxActive.CompareAndSwap(previous, n); previous = maxActive.Load() {
			}
			time.Sleep(20 * time.Millisecond)
			active.Add(-1)
			validations.Add(1)
			return response("cloudflare", "loc=US\n"), nil
		},
	}
	blocks := []prefix.Block{
		{ASN: 64512, SourcePrefix: "8.8.0.0/24", BlockCIDR: "8.8.0.0/24", Status: "active"},
		{ASN: 64512, SourcePrefix: "8.8.1.0/24", BlockCIDR: "8.8.1.0/24", Status: "active"},
		{ASN: 64512, SourcePrefix: "8.8.2.0/24", BlockCIDR: "8.8.2.0/24", Status: "active"},
		{ASN: 64512, SourcePrefix: "8.8.3.0/24", BlockCIDR: "8.8.3.0/24", Status: "active"},
	}
	if err := runner.Run(context.Background(), 64512, blocks); err != nil {
		t.Fatal(err)
	}

	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(args)), "\n")
	if len(lines) != 4 {
		t.Fatalf("Masscan invocations = %d, want 4: %q", len(lines), lines)
	}
	for _, line := range lines {
		if !strings.Contains(line, "--rate 25") {
			t.Errorf("Masscan args %q do not use one quarter of the 100 PPS aggregate cap", line)
		}
	}

	statuses := make(map[string]map[string]bool)
	for _, report := range collector.snapshot() {
		if statuses[report.BlockCIDR] == nil {
			statuses[report.BlockCIDR] = make(map[string]bool)
		}
		statuses[report.BlockCIDR][report.Status] = true
	}
	if len(statuses) != 4 {
		t.Fatalf("reported blocks = %#v, want all 4 concurrent blocks", statuses)
	}
	for _, block := range blocks {
		if !statuses[block.BlockCIDR]["active"] || !statuses[block.BlockCIDR]["completed"] {
			t.Errorf("block %s reports = %#v, want active and completed", block.BlockCIDR, statuses[block.BlockCIDR])
		}
	}
	if got := validations.Load(); got != 16 {
		t.Errorf("curl validations = %d, want 16 discovered open targets", got)
	}
	if got := maxActive.Load(); got > 8 {
		t.Errorf("maximum concurrent curl validations = %d, want no more than configured global limit 8", got)
	}
}

func TestRunSharesThirtyPercentTotalCPUBudgetAcrossMasscanBlocks(t *testing.T) {
	dir := t.TempDir()
	cpuArgsFile := filepath.Join(dir, "cpulimit.args")
	t.Setenv("CPULIMIT_ARGS_FILE", cpuArgsFile)
	currentPath := os.Getenv("PATH")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+currentPath)
	makeExecutable(t, dir, "cpulimit", `
  if [ "$1" = "--help" ]; then
    echo 'launch target in foreground and wait'
    exit 0
  fi
  if [ "$1" = "-f" ]; then foreground=1; shift; else foreground=0; fi
  [ "$1" = "-l" ] || exit 11
  printf '%s %s\n' "$foreground" "$2" >> "$CPULIMIT_ARGS_FILE"
  shift 2
  [ "$1" = "--" ] || exit 13
  shift
  exec "$@"
`)
	masscan := makeExecutable(t, dir, "masscan", `printf '# no open ports\n'`)
	runner := Runner{
		Config:   RunnerConfig{MasscanPath: masscan, Limit: 100, CurlWorkers: 8, BlockWorkers: 4, HeartbeatInterval: time.Hour},
		Reporter: &reportCollector{},
	}
	blocks := []prefix.Block{
		{ASN: 64512, SourcePrefix: "8.8.0.0/24", BlockCIDR: "8.8.0.0/24", Status: "active"},
		{ASN: 64512, SourcePrefix: "8.8.1.0/24", BlockCIDR: "8.8.1.0/24", Status: "active"},
		{ASN: 64512, SourcePrefix: "8.8.2.0/24", BlockCIDR: "8.8.2.0/24", Status: "active"},
		{ASN: 64512, SourcePrefix: "8.8.3.0/24", BlockCIDR: "8.8.3.0/24", Status: "active"},
	}
	if err := runner.Run(context.Background(), 64512, blocks); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(cpuArgsFile)
	if err != nil {
		t.Fatalf("Masscan was not launched through the CPU limiter: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 4 {
		t.Fatalf("CPU limiter invocations = %d, want 4: %q", len(lines), lines)
	}
	var total int
	quotas := make([]int, 0, len(lines))
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != "1" {
			t.Fatalf("cpulimit did not use foreground mode: %q", line)
		}
		quota, err := strconv.Atoi(fields[1])
		if err != nil {
			t.Fatalf("invalid per-process CPU quota %q: %v", fields[1], err)
		}
		quotas = append(quotas, quota)
		total += quota
	}
	if total != runtime.NumCPU()*30 {
		t.Fatalf("sum of per-block cpulimit quotas = %d, want 30%% of %d logical CPUs (%d)", total, runtime.NumCPU(), runtime.NumCPU()*30)
	}
	sort.Ints(quotas)
	if quotas[3]-quotas[0] > 1 {
		t.Fatalf("per-block CPU quotas are not evenly distributed: %v", quotas)
	}
}

func TestRunPlanClaimsDifferentBlocksAcrossAgents(t *testing.T) {
	dir := t.TempDir()
	masscan := makeExecutable(t, dir, "masscan", `
	last=""
	for argument do last="$argument"; done
	case "$last" in
	  8.8.*.0/24) sleep 0.04 ;;
	  *) exit 8 ;;
	esac
`)
	prefixes := make([]string, 8)
	for index := range prefixes {
		prefixes[index] = fmt.Sprintf("8.8.%d.0/24", index)
	}
	plan, err := prefix.NewPlan(64512, prefixes, nil)
	if err != nil {
		t.Fatal(err)
	}
	shared := &sharedTestLeases{}
	makeRunner := func(agentID string) Runner {
		return Runner{
			Config: RunnerConfig{
				MasscanPath: masscan, CPULimitPath: makeCPULimitStub(t, dir),
				Limit: 400, CurlWorkers: 16, BlockWorkers: 4, HeartbeatInterval: time.Hour, LeasePollInterval: 5 * time.Millisecond,
			},
			Reporter: &testLeaseAgent{id: agentID, shared: shared},
		}
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	for _, agentID := range []string{"agent-a", "agent-b"} {
		runner := makeRunner(agentID)
		go func() {
			<-start
			results <- runner.RunPlan(context.Background(), plan)
		}()
	}
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}

	shared.mu.Lock()
	defer shared.mu.Unlock()
	if len(shared.claimed) != len(prefixes) {
		t.Fatalf("claimed blocks = %d, want %d; owners=%#v", len(shared.claimed), len(prefixes), shared.claimed)
	}
	activeReports := make(map[string]int)
	for _, report := range shared.reports {
		if report.Status == "active" {
			activeReports[report.BlockCIDR]++
		}
	}
	for _, cidr := range prefixes {
		blockCIDR := cidr
		if activeReports[blockCIDR] != 1 {
			t.Errorf("block %s active reports = %d, want exactly one leased owner", blockCIDR, activeReports[blockCIDR])
		}
	}
}

func TestPauseReleasesClaimedBlockImmediately(t *testing.T) {
	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	masscan := makeExecutable(t, dir, "masscan", `touch "$STARTED_FILE"; sleep 30`)
	block := baseBlock("8.8.8.0/24")
	plan, err := prefix.NewPlan(64512, []string{block.BlockCIDR}, nil)
	if err != nil {
		t.Fatal(err)
	}
	shared := &sharedTestLeases{}
	runner := Runner{
		Config: RunnerConfig{
			MasscanPath: masscan, CPULimitPath: makeCPULimitStub(t, dir),
			Limit: 100, CurlWorkers: 2, BlockWorkers: 1,
			HeartbeatInterval: time.Hour, LeasePollInterval: time.Millisecond,
		},
		Reporter: &testLeaseAgent{id: "agent-a", shared: shared},
	}
	t.Setenv("STARTED_FILE", started)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- runner.RunPlan(ctx, plan) }()
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
		t.Fatal("RunPlan() error = nil after pause")
	}
	shared.mu.Lock()
	defer shared.mu.Unlock()
	if shared.releases != 1 {
		t.Fatalf("released leases = %d, want 1", shared.releases)
	}
	if _, stillOwned := shared.owners[block.BlockCIDR]; stillOwned {
		t.Fatalf("paused agent still owns block %s", block.BlockCIDR)
	}
}

func TestRunUsesAlpineCPULimitCommandModeWithoutDebianForegroundFlag(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "cpulimit.args")
	t.Setenv("CPULIMIT_ARGS_FILE", argsFile)
	cpulimit := makeExecutable(t, dir, "cpulimit", `
  if [ "$1" = "--help" ]; then
    echo 'Usage: cpulimit [OPTIONS...] TARGET'
    exit 0
  fi
  printf '%s\n' "$*" > "$CPULIMIT_ARGS_FILE"
  [ "$1" = "-l" ] || exit 11
  shift 2
  [ "$1" = "--" ] || exit 12
  shift
  exec "$@"
`)
	masscan := makeExecutable(t, dir, "masscan", `printf '# no open ports\n'`)
	runner := Runner{
		Config:   RunnerConfig{MasscanPath: masscan, CPULimitPath: cpulimit, Limit: 10, CurlWorkers: 2, BlockWorkers: 1},
		Reporter: &reportCollector{},
	}
	if err := runner.Run(context.Background(), 64512, []prefix.Block{baseBlock("8.8.8.0/24")}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Fields(string(data))
	if len(args) < 5 || args[0] != "-l" {
		t.Fatalf("Alpine cpulimit args = %q, want -l quota -- masscan ...", data)
	}
	for _, arg := range args {
		if arg == "-f" || arg == "-i" {
			t.Fatalf("Alpine cpulimit args include unsupported flag %q: %q", arg, data)
		}
	}
	if args[2] != "--" || args[3] != masscan {
		t.Fatalf("Alpine cpulimit target args = %q, want -- %s ...", data, masscan)
	}
}

func TestHeartbeatReportsLatestOpenPortDuringLongMasscanRun(t *testing.T) {
	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	masscan := makeExecutable(t, dir, "masscan", `printf 'Discovered open port 443/tcp on 8.8.8.10\n'; touch "$STARTED_FILE"; sleep 0.18`)
	collector := &reportCollector{}
	runner := Runner{
		Config:   RunnerConfig{MasscanPath: masscan, CPULimitPath: makeCPULimitStub(t, dir), Limit: 100, CurlWorkers: 2, HeartbeatInterval: 20 * time.Millisecond},
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
		Config:   RunnerConfig{MasscanPath: masscan, CPULimitPath: makeCPULimitStub(t, dir), Limit: 100, CurlWorkers: 2, HeartbeatInterval: time.Hour},
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
