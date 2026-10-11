package scanner

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"cf-san/internal/backend"
	"cf-san/internal/prefix"
	"cf-san/internal/probe"
)

type Reporter interface {
	Report(context.Context, backend.Report) error
}

type TraceProber func(context.Context, string) (probe.TraceResponse, error)

type RunnerConfig struct {
	ZMapPath          string
	ZGrab2Path        string
	BlocklistPath     string
	Limit             uint64
	Senders           uint32
	HeartbeatInterval time.Duration
}

type Runner struct {
	Config     RunnerConfig
	Reporter   Reporter
	TraceProbe TraceProber
}

type zgrabResult struct {
	IP   string `json:"ip"`
	Data struct {
		TLS struct {
			Status string `json:"status"`
		} `json:"tls"`
	} `json:"data"`
}

func (r *Runner) normalizedConfig() RunnerConfig {
	config := r.Config
	if config.ZMapPath == "" {
		config.ZMapPath = "zmap"
	}
	if config.ZGrab2Path == "" {
		config.ZGrab2Path = "zgrab2"
	}
	if config.BlocklistPath == "" {
		config.BlocklistPath = DefaultBlocklistPath
	}
	if config.Limit == 0 {
		config.Limit = 100
	}
	if config.Senders == 0 {
		config.Senders = 100
	}
	if config.HeartbeatInterval <= 0 {
		config.HeartbeatInterval = 10 * time.Minute
	}
	return config
}

func (r *Runner) Run(ctx context.Context, asn uint32, blocks []prefix.Block) error {
	config, err := r.validateRun()
	if err != nil {
		return err
	}
	pending, err := prefix.PendingBlocks(asn, blocks)
	if err != nil {
		return err
	}
	for _, block := range pending {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := r.runBlock(ctx, asn, block, config); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) RunPlan(ctx context.Context, plan *prefix.Plan) error {
	if plan == nil {
		return errors.New("backend scan plan is required")
	}
	config, err := r.validateRun()
	if err != nil {
		return err
	}
	return plan.ForEachPending(func(block prefix.Block) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return r.runBlock(ctx, plan.ASN(), block, config)
	})
}

func (r *Runner) validateRun() (RunnerConfig, error) {
	if r.Reporter == nil {
		return RunnerConfig{}, errors.New("backend reporter is required")
	}
	config := r.normalizedConfig()
	if config.Limit == 0 || config.Senders == 0 {
		return RunnerConfig{}, errors.New("ZMap rate and ZGrab2 sender count must be positive")
	}
	if err := ValidateBlocklistFile(config.BlocklistPath); err != nil {
		return RunnerConfig{}, err
	}
	return config, nil
}

func (r *Runner) runBlock(
	parent context.Context,
	asn uint32,
	block prefix.Block,
	config RunnerConfig,
) error {
	progress := backend.Report{
		ASN: asn, SourcePrefix: block.SourcePrefix, BlockCIDR: block.BlockCIDR, Status: "active",
	}
	if err := r.reportWithRetry(parent, progress); err != nil {
		return fmt.Errorf("cannot mark scan block active: %w", err)
	}

	blockCtx, cancel := context.WithCancel(parent)
	defer cancel()
	var latestMu sync.RWMutex
	latestIP := ""
	getLatestIP := func() string {
		latestMu.RLock()
		defer latestMu.RUnlock()
		return latestIP
	}

	heartbeatErrors := make(chan error, 1)
	heartbeatDone := make(chan struct{})
	ticker := time.NewTicker(config.HeartbeatInterval)
	go func() {
		defer close(heartbeatDone)
		defer ticker.Stop()
		for {
			select {
			case <-blockCtx.Done():
				return
			case <-ticker.C:
				heartbeat := backend.Report{
					ASN: asn, SourcePrefix: block.SourcePrefix, BlockCIDR: block.BlockCIDR,
					Status: "active", LatestIP: getLatestIP(),
				}
				if err := r.reportWithRetry(blockCtx, heartbeat); err != nil {
					select {
					case heartbeatErrors <- err:
					default:
					}
					cancel()
					return
				}
			}
		}
	}()

	traceProbe := r.TraceProbe
	if traceProbe == nil {
		traceProbe = func(ctx context.Context, ip string) (probe.TraceResponse, error) {
			return probe.FetchCFTrace(ctx, ip, nil)
		}
	}
	probeCandidate := func(ip string) error {
		latestMu.Lock()
		latestIP = ip
		latestMu.Unlock()
		trace, err := traceProbe(blockCtx, ip)
		if err != nil || !probe.ValidateCFResolveResponse(trace.RawResponse) {
			return nil
		}
		region, ok := probe.ParseTraceRegion(trace.Body)
		if !ok {
			return nil
		}
		candidate := &backend.Candidate{IP: ip, Port: 443, ASN: asn, Region: region}
		report := backend.Report{
			ASN: asn, SourcePrefix: block.SourcePrefix, BlockCIDR: block.BlockCIDR,
			Status: "active", LatestIP: ip, Candidate: candidate,
		}
		if err := r.reportWithRetry(blockCtx, report); err != nil {
			return fmt.Errorf("cannot persist valid scan result: %w", err)
		}
		return nil
	}

	onTarget := func(ip string) {
		latestMu.Lock()
		latestIP = ip
		latestMu.Unlock()
	}
	scanErr := r.runBlockPipeline(blockCtx, block, config, onTarget, probeCandidate)
	cancel()
	<-heartbeatDone
	select {
	case heartbeatErr := <-heartbeatErrors:
		return fmt.Errorf("scan heartbeat failed: %w", heartbeatErr)
	default:
	}
	if err := parent.Err(); err != nil {
		return err
	}
	if scanErr != nil {
		return scanErr
	}

	progress.Status = "completed"
	progress.LatestIP = getLatestIP()
	if err := r.reportWithRetry(parent, progress); err != nil {
		return fmt.Errorf("cannot mark scan block completed: %w", err)
	}
	return nil
}

func (r *Runner) runBlockPipeline(
	ctx context.Context,
	block prefix.Block,
	config RunnerConfig,
	onTarget func(string),
	probeCandidate func(string) error,
) error {
	zmapArgs := []string{
		"-M", "tcp_synscan",
		"-p", "443",
		"-r", strconv.FormatUint(config.Limit, 10),
		"--output-module=csv",
		"--output-fields=saddr",
		"--output-filter=success = 1",
		"-o", "-",
		block.BlockCIDR,
	}
	zmapCommand := newGroupCommand(ctx, r.normalizedConfig().ZMapPath, zmapArgs...)
	zmapCommand.Stderr = os.Stderr
	zmapOutput, err := zmapCommand.StdoutPipe()
	if err != nil {
		return errors.New("cannot open ZMap output")
	}

	zgrabArgs := []string{
		"tls",
		"--senders", strconv.FormatUint(uint64(config.Senders), 10),
		"--port", "443",
		"--connect-timeout", "5s",
		"--target-timeout", "8s",
		"--blocklist-file", config.BlocklistPath,
	}
	zgrabCommand := newGroupCommand(ctx, r.normalizedConfig().ZGrab2Path, zgrabArgs...)
	zgrabCommand.Stdin = zmapOutput
	zgrabCommand.Stderr = os.Stderr
	zgrabOutput, err := zgrabCommand.StdoutPipe()
	if err != nil {
		return errors.New("cannot open ZGrab2 output")
	}
	if err := zgrabCommand.Start(); err != nil {
		return errors.New("cannot start ZGrab2; verify it is installed")
	}
	if err := zmapCommand.Start(); err != nil {
		_ = zgrabCommand.Process.Kill()
		_ = zgrabCommand.Wait()
		return errors.New("cannot start ZMap; verify it is installed and has required privileges")
	}

	outputScanner := bufio.NewScanner(zgrabOutput)
	outputScanner.Buffer(make([]byte, 64*1024), 1024*1024)
	var callbackErr error
	for outputScanner.Scan() {
		line := outputScanner.Bytes()
		var result zgrabResult
		if err := json.Unmarshal(line, &result); err != nil {
			continue
		}
		address, err := netip.ParseAddr(result.IP)
		if err != nil || !address.Is4() {
			continue
		}
		blockPrefix, err := netip.ParsePrefix(block.BlockCIDR)
		if err != nil || !blockPrefix.Contains(address) {
			continue
		}
		onTarget(address.String())
		if !strings.EqualFold(result.Data.TLS.Status, "success") {
			continue
		}
		if err := probeCandidate(address.String()); err != nil {
			callbackErr = err
			_ = zgrabCommand.Process.Kill()
			_ = zmapCommand.Process.Kill()
			break
		}
	}
	readErr := outputScanner.Err()
	zgrabErr := zgrabCommand.Wait()
	zmapErr := zmapCommand.Wait()
	if callbackErr != nil {
		return callbackErr
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if readErr != nil {
		return errors.New("cannot parse ZGrab2 output stream")
	}
	if zmapErr != nil {
		return errors.New("ZMap scan failed")
	}
	if zgrabErr != nil {
		return errors.New("ZGrab2 TLS scan failed")
	}
	return nil
}

func newGroupCommand(ctx context.Context, path string, args ...string) *exec.Cmd {
	command := exec.CommandContext(ctx, path, args...)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-command.Process.Pid, syscall.SIGTERM)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	command.WaitDelay = 2 * time.Second
	return command
}

func (r *Runner) reportWithRetry(ctx context.Context, report backend.Report) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := r.Reporter.Report(ctx, report); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if attempt < 2 {
			timer := time.NewTimer(time.Duration(100*(1<<attempt)) * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	if lastErr == nil {
		return errors.New("report failed")
	}
	return lastErr
}
