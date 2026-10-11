package scanner

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"runtime"
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

type LeaseCoordinator interface {
	ClaimBlocks(context.Context, uint32, []prefix.Block, uint32) ([]backend.BlockClaim, int, error)
	ReleaseBlock(context.Context, prefix.Block, string) error
}

type TraceProber func(context.Context, string) (probe.TraceResponse, error)

type RunnerConfig struct {
	MasscanPath        string
	CPULimitPath       string
	CurlPath           string
	Limit              uint64
	CurlWorkers        uint32
	BlockWorkers       uint32
	CPUPercent         uint32
	CPUCount           uint32
	CPULimitCPUPercent uint64
	CPULimitForeground bool
	HeartbeatInterval  time.Duration
	LeasePollInterval  time.Duration
}

const ForcedCheckpointInterval time.Duration = 10 * time.Minute
const maxClaimCandidates int = 64

// MaxConcurrentBlocks is the default upper bound for concurrent Masscan blocks.
const MaxConcurrentBlocks uint32 = 4

type Runner struct {
	Config     RunnerConfig
	Reporter   Reporter
	TraceProbe TraceProber
	LogOutput  io.Writer
}

func (r *Runner) normalizedConfig() RunnerConfig {
	config := r.Config
	if config.MasscanPath == "" {
		config.MasscanPath = "masscan"
	}
	if config.CPULimitPath == "" {
		config.CPULimitPath = "cpulimit"
	}
	if config.CurlPath == "" {
		config.CurlPath = "curl"
	}
	if config.Limit == 0 {
		config.Limit = 100
	}
	if config.CurlWorkers == 0 {
		config.CurlWorkers = 100
	}
	if config.CPUPercent == 0 {
		config.CPUPercent = 30
	}
	if config.CPUCount == 0 {
		cpus := runtime.NumCPU()
		if cpus < 1 {
			cpus = 1
		}
		config.CPUCount = uint32(cpus)
	}
	if config.BlockWorkers == 0 || config.BlockWorkers > MaxConcurrentBlocks {
		config.BlockWorkers = MaxConcurrentBlocks
	}
	if config.HeartbeatInterval <= 0 {
		config.HeartbeatInterval = ForcedCheckpointInterval
	}
	if config.LeasePollInterval <= 0 {
		config.LeasePollInterval = 30 * time.Second
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
	return r.runBlocks(ctx, asn, pending, config)
}

func (r *Runner) RunPlan(ctx context.Context, plan *prefix.Plan) error {
	if plan == nil {
		return errors.New("backend scan plan is required")
	}
	config, err := r.validateRun()
	if err != nil {
		return err
	}
	if coordinator, ok := r.Reporter.(LeaseCoordinator); ok {
		return r.runLeasedPlan(ctx, plan, config, coordinator)
	}
	batchSize := r.blockConcurrency(config)
	batch := make([]prefix.Block, 0, batchSize)
	if err := plan.ForEachPending(func(block prefix.Block) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		batch = append(batch, block)
		if len(batch) < batchSize {
			return nil
		}
		if err := r.runBlockBatch(ctx, plan.ASN(), batch, config); err != nil {
			return err
		}
		batch = batch[:0]
		return nil
	}); err != nil {
		return err
	}
	return r.runBlockBatch(ctx, plan.ASN(), batch, config)
}

func (r *Runner) runLeasedPlan(
	ctx context.Context,
	plan *prefix.Plan,
	config RunnerConfig,
	coordinator LeaseCoordinator,
) error {
	batchSize := r.blockConcurrency(config)
	batch := make([]prefix.Block, 0, batchSize)
	var retryBlocks []prefix.Block
	runClaimedBatch := func() error {
		if len(batch) == 0 {
			return nil
		}
		current := append([]prefix.Block(nil), batch...)
		batch = batch[:0]
		if err := r.runBlockBatch(ctx, plan.ASN(), current, config); err != nil {
			r.releaseClaimedBlocks(coordinator, current)
			return err
		}
		return nil
	}
	processCandidates := func(candidates []prefix.Block) (pending []prefix.Block, claimedAny bool, err error) {
		for len(candidates) > 0 {
			if err := ctx.Err(); err != nil {
				return pending, claimedAny, err
			}
			if len(batch) == batchSize {
				if err := runClaimedBatch(); err != nil {
					return pending, claimedAny, err
				}
			}
			count := min(len(candidates), maxClaimCandidates)
			group := candidates[:count]
			remainingSlots := batchSize - len(batch)
			outcomes, examined, err := coordinator.ClaimBlocks(ctx, plan.ASN(), group, uint32(remainingSlots))
			if err != nil {
				return pending, claimedAny, fmt.Errorf("cannot claim scan blocks: %w", err)
			}
			if examined < 1 || examined > len(group) || len(outcomes) != examined {
				return pending, claimedAny, fmt.Errorf("backend returned an invalid scan block claim batch")
			}
			for index, outcome := range outcomes {
				candidate := group[index]
				switch outcome.State {
				case "claimed":
					block := outcome.Block
					if block.ASN != 0 && block.ASN != plan.ASN() ||
						block.SourcePrefix != candidate.SourcePrefix || block.BlockCIDR != candidate.BlockCIDR ||
						strings.TrimSpace(block.LeaseID) == "" {
						if block.LeaseID != "" {
							r.releaseClaimedBlocks(coordinator, []prefix.Block{block})
						}
						return pending, claimedAny, fmt.Errorf("backend returned an invalid lease for block %s", candidate.BlockCIDR)
					}
					block.ASN = plan.ASN()
					batch = append(batch, block)
					claimedAny = true
				case "leased":
					pending = append(pending, candidate)
				case "completed":
				default:
					return pending, claimedAny, fmt.Errorf("backend returned an invalid state for block %s", candidate.BlockCIDR)
				}
			}
			candidates = candidates[examined:]
			if len(batch) == batchSize {
				if err := runClaimedBatch(); err != nil {
					return pending, claimedAny, err
				}
			}
		}
		return pending, claimedAny, nil
	}

	var candidateWindow []prefix.Block
	if err := plan.ForEachPending(func(block prefix.Block) error {
		candidateWindow = append(candidateWindow, block)
		if len(candidateWindow) < maxClaimCandidates {
			return nil
		}
		pending, _, err := processCandidates(candidateWindow)
		retryBlocks = append(retryBlocks, pending...)
		candidateWindow = candidateWindow[:0]
		return err
	}); err != nil {
		r.releaseClaimedBlocks(coordinator, batch)
		return err
	}
	if len(candidateWindow) > 0 {
		pending, _, err := processCandidates(candidateWindow)
		retryBlocks = append(retryBlocks, pending...)
		candidateWindow = nil
		if err != nil {
			r.releaseClaimedBlocks(coordinator, batch)
			return err
		}
	}
	if len(batch) > 0 {
		if err := runClaimedBatch(); err != nil {
			return err
		}
	}

	for len(retryBlocks) > 0 {
		var pending []prefix.Block
		claimedAny := false
		for start := 0; start < len(retryBlocks); start += maxClaimCandidates {
			end := min(start+maxClaimCandidates, len(retryBlocks))
			stillPending, claimed, err := processCandidates(retryBlocks[start:end])
			pending = append(pending, stillPending...)
			claimedAny = claimedAny || claimed
			if err != nil {
				r.releaseClaimedBlocks(coordinator, batch)
				return err
			}
		}
		if len(batch) > 0 {
			if err := runClaimedBatch(); err != nil {
				return err
			}
		}
		retryBlocks = pending
		if len(retryBlocks) == 0 {
			return nil
		}
		if !claimedAny {
			timer := time.NewTimer(config.LeasePollInterval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	return nil
}

func (r *Runner) releaseClaimedBlocks(coordinator LeaseCoordinator, blocks []prefix.Block) {
	for _, block := range blocks {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := coordinator.ReleaseBlock(ctx, block, block.LeaseID); err != nil {
			r.logf("ASN %d: could not release lease for unstarted block %s: %v\n", block.ASN, block.BlockCIDR, err)
		}
		cancel()
	}
}

func (r *Runner) validateRun() (RunnerConfig, error) {
	if r.Reporter == nil {
		return RunnerConfig{}, errors.New("backend reporter is required")
	}
	config := r.normalizedConfig()
	if config.Limit == 0 || config.CurlWorkers == 0 {
		return RunnerConfig{}, errors.New("Masscan rate and curl worker count must be positive")
	}
	if config.CurlWorkers > 4096 {
		return RunnerConfig{}, errors.New("curl worker count cannot exceed 4096")
	}
	if config.CPUPercent > 100 {
		return RunnerConfig{}, errors.New("Masscan total CPU limit cannot exceed 100 percent")
	}
	config.CPULimitForeground = cpulimitSupportsForeground(config.CPULimitPath)
	return config, nil
}

func cpulimitSupportsForeground(path string) bool {
	output, _ := exec.Command(path, "--help").CombinedOutput()
	return strings.Contains(strings.ToLower(string(output)), "foreground")
}

func (r *Runner) blockConcurrency(config RunnerConfig) int {
	workers := config.BlockWorkers
	if workers > config.CurlWorkers {
		workers = config.CurlWorkers
	}
	if uint64(workers) > config.Limit {
		workers = uint32(config.Limit)
	}
	cpuBudget := uint64(config.CPUPercent) * uint64(config.CPUCount)
	if uint64(workers) > cpuBudget {
		workers = uint32(cpuBudget)
	}
	if workers == 0 {
		return 1
	}
	return int(workers)
}

func (r *Runner) runBlocks(ctx context.Context, asn uint32, blocks []prefix.Block, config RunnerConfig) error {
	batchSize := r.blockConcurrency(config)
	for start := 0; start < len(blocks); start += batchSize {
		end := min(start+batchSize, len(blocks))
		if err := r.runBlockBatch(ctx, asn, blocks[start:end], config); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func (r *Runner) runBlockBatch(
	parent context.Context,
	asn uint32,
	blocks []prefix.Block,
	config RunnerConfig,
) error {
	if len(blocks) == 0 {
		return parent.Err()
	}
	if err := parent.Err(); err != nil {
		return err
	}
	batchCtx, cancel := context.WithCancel(parent)
	defer cancel()

	errorsFound := make(chan error, len(blocks))
	var workers sync.WaitGroup
	for index, block := range blocks {
		blockConfig := config
		blockConfig.Limit = splitLimit(config.Limit, len(blocks), index)
		blockConfig.CurlWorkers = uint32(splitLimit(uint64(config.CurlWorkers), len(blocks), index))
		blockConfig.CPULimitCPUPercent = splitLimit(uint64(config.CPUPercent)*uint64(config.CPUCount), len(blocks), index)
		workers.Add(1)
		go func(index int, block prefix.Block, blockConfig RunnerConfig) {
			defer workers.Done()
			if err := r.runBlock(batchCtx, asn, block, blockConfig, index+1, len(blocks), config.Limit); err != nil {
				select {
				case errorsFound <- err:
				default:
				}
				cancel()
			}
		}(index, block, blockConfig)
	}
	workers.Wait()
	if err := parent.Err(); err != nil {
		return err
	}
	select {
	case err := <-errorsFound:
		return err
	default:
		return nil
	}
}

func splitLimit(total uint64, parts, index int) uint64 {
	base := total / uint64(parts)
	if uint64(index) < total%uint64(parts) {
		base++
	}
	return base
}

func (r *Runner) runBlock(
	parent context.Context,
	asn uint32,
	block prefix.Block,
	config RunnerConfig,
	workerIndex int,
	workerCount int,
	aggregateLimit uint64,
) error {
	coordinator, hasLeases := r.Reporter.(LeaseCoordinator)
	blockCompleted := false
	if hasLeases && block.LeaseID != "" {
		defer func() {
			if !blockCompleted {
				r.releaseClaimedBlocks(coordinator, []prefix.Block{block})
			}
		}()
	}
	if err := parent.Err(); err != nil {
		return err
	}
	progress := backend.Report{
		ASN: asn, SourcePrefix: block.SourcePrefix, BlockCIDR: block.BlockCIDR, Status: "active", LeaseID: block.LeaseID,
	}
	if err := r.reportWithRetry(parent, progress); err != nil {
		return fmt.Errorf("cannot mark scan block active: %w", err)
	}
	r.logf(
		"ASN %d: scanning block %s (source %s) at %d PPS; block worker %d/%d, aggregate cap %d PPS; Masscan CPU quota %d%% of one CPU (total cap %d%% across %d CPUs)\n",
		asn, block.BlockCIDR, block.SourcePrefix, config.Limit, workerIndex, workerCount, aggregateLimit,
		config.CPULimitCPUPercent, config.CPUPercent, config.CPUCount,
	)

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
					Status: "active", LatestIP: getLatestIP(), LeaseID: block.LeaseID,
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
			return probe.FetchCFTrace(ctx, ip, config.CurlPath)
		}
	}
	probeCandidate := func(probeCtx context.Context, ip string) error {
		trace, err := traceProbe(probeCtx, ip)
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
			Status: "active", LatestIP: ip, LeaseID: block.LeaseID, Candidate: candidate,
		}
		if err := r.reportWithRetry(probeCtx, report); err != nil {
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
		r.logf("ASN %d: block %s interrupted: %v\n", asn, block.BlockCIDR, err)
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
	blockCompleted = true
	r.logf("ASN %d: completed block %s (source %s)\n", asn, block.BlockCIDR, block.SourcePrefix)
	return nil
}

func (r *Runner) logf(format string, arguments ...any) {
	output := r.LogOutput
	if output == nil {
		output = os.Stderr
	}
	_, _ = fmt.Fprintf(output, format, arguments...)
}

func (r *Runner) runBlockPipeline(
	ctx context.Context,
	block prefix.Block,
	config RunnerConfig,
	onTarget func(string),
	probeCandidate func(context.Context, string) error,
) error {
	scanCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	masscanArgs := []string{
		"-p", "443",
		"--rate", strconv.FormatUint(config.Limit, 10),
		"--wait", "1",
		"--interactive",
		block.BlockCIDR,
	}
	args := make([]string, 0, len(masscanArgs)+5)
	if config.CPULimitForeground {
		args = append(args, "-f")
	}
	args = append(args, "-l", strconv.FormatUint(config.CPULimitCPUPercent, 10), "--", r.normalizedConfig().MasscanPath)
	args = append(args, masscanArgs...)
	command := newGroupCommand(scanCtx, config.CPULimitPath, args...)
	command.Stderr = os.Stderr
	output, err := command.StdoutPipe()
	if err != nil {
		return errors.New("cannot open Masscan output")
	}
	if err := command.Start(); err != nil {
		return errors.New("cannot start Masscan CPU limiter; verify cpulimit and Masscan are installed and run cf-san with root privileges")
	}

	targets := make(chan string, int(config.CurlWorkers))
	var workers sync.WaitGroup
	var firstError error
	var errorMu sync.Mutex
	setError := func(err error) {
		if err == nil {
			return
		}
		errorMu.Lock()
		if firstError == nil {
			firstError = err
			cancel()
		}
		errorMu.Unlock()
	}
	for worker := uint32(0); worker < config.CurlWorkers; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for ip := range targets {
				if scanCtx.Err() != nil {
					continue
				}
				if err := probeCandidate(scanCtx, ip); err != nil {
					setError(err)
					return
				}
			}
		}()
	}

	outputScanner := bufio.NewScanner(output)
	outputScanner.Buffer(make([]byte, 64*1024), 1024*1024)
	seen := make(map[string]struct{})
	for outputScanner.Scan() {
		if scanCtx.Err() != nil {
			break
		}
		ip, ok := parseMasscanInteractiveOpen443(outputScanner.Text(), block)
		if !ok {
			continue
		}
		if _, duplicate := seen[ip]; duplicate {
			continue
		}
		seen[ip] = struct{}{}
		onTarget(ip)
		select {
		case targets <- ip:
		case <-scanCtx.Done():
			break
		}
	}
	outputErr := outputScanner.Err()
	if outputErr != nil {
		setError(errors.New("cannot parse Masscan output stream"))
	}
	close(targets)
	workers.Wait()
	commandErr := command.Wait()

	errorMu.Lock()
	workerError := firstError
	errorMu.Unlock()
	if workerError != nil {
		return workerError
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if outputErr != nil {
		return errors.New("cannot parse Masscan output stream")
	}
	if commandErr != nil {
		return errors.New("Masscan scan failed")
	}
	return nil
}

func parseMasscanInteractiveOpen443(line string, block prefix.Block) (string, bool) {
	fields := strings.Fields(line)
	if len(fields) < 6 || fields[0] != "Discovered" || fields[1] != "open" ||
		fields[2] != "port" || fields[3] != "443/tcp" || fields[4] != "on" {
		return "", false
	}
	address, err := netip.ParseAddr(fields[5])
	if err != nil || !address.Is4() {
		return "", false
	}
	blockPrefix, err := netip.ParsePrefix(block.BlockCIDR)
	if err != nil || !blockPrefix.Contains(address) {
		return "", false
	}
	return address.String(), true
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
