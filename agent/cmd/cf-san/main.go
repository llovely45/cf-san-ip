package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"cf-san/internal/autoupdate"
	"cf-san/internal/backend"
	"cf-san/internal/buildinfo"
	"cf-san/internal/config"
	"cf-san/internal/menu"
	"cf-san/internal/scanner"
)

type cliOptions struct {
	configPath string
	overrides  config.Overrides
}

var updateLockPath = autoupdate.DefaultUpdateLockPath

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "cf-san:", err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	command := "menu"
	if len(arguments) > 0 && !strings.HasPrefix(arguments[0], "-") {
		command = arguments[0]
		arguments = arguments[1:]
	}
	if command == "version" {
		fmt.Fprintln(os.Stdout, buildinfo.Display())
		return nil
	}
	if command == "auto-update" {
		if err := requireRoot(os.Geteuid()); err != nil {
			return err
		}
	}
	options, err := parseOptions(arguments)
	if err != nil {
		return err
	}
	if options.configPath == "" {
		options.configPath = menu.DefaultConfigPath()
	}
	switch command {
	case "menu":
		if os.Geteuid() != 0 {
			return errors.New("请使用 sudo cf-san 打开菜单；配置文件与 Masscan 扫描需要 root 权限")
		}
		if _, err := withConfigUpdate(options.configPath, options.overrides, func(cfg *config.Config) error {
			if config.Validate(*cfg) != nil {
				return nil
			}
			return config.Save(options.configPath, *cfg)
		}); err != nil {
			return err
		}
		app := menu.NewApp()
		app.ConfigPath = options.configPath
		manager := menu.NewManager()
		manager.Arguments = []string{"run", "--config", options.configPath}
		app.Manager = &manager
		return app.Run()
	case "configure":
		if _, err := withConfigUpdate(options.configPath, options.overrides, func(cfg *config.Config) error {
			return config.Save(options.configPath, *cfg)
		}); err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, "配置已写入本机文件；文件权限为 0600，凭据仍以明文保存。")
		return nil
	case "run":
		cfg, err := withConfigUpdateWait(options.configPath, options.overrides, func(cfg *config.Config) error {
			return config.Save(options.configPath, *cfg)
		})
		if err != nil {
			return err
		}
		return runScan(cfg, options.configPath)
	case "auto-update":
		manager := menu.NewManager()
		manager.Arguments = []string{"run", "--config", options.configPath}
		return runAutomaticUpdate(config.Config{}, options.configPath, manager)
	default:
		return fmt.Errorf("未知命令 %q；可用命令：menu、configure、run、version、auto-update", command)
	}
}

func requireRoot(uid int) error {
	if uid != 0 {
		return errors.New("automatic updates require root privileges")
	}
	return nil
}

func runAutomaticUpdate(cfg config.Config, configPath string, scanner autoupdate.Scanner) error {
	if configPath == "" {
		return errors.New("automatic update config path is missing")
	}
	logger, err := autoupdate.OpenLogger(autoupdate.DefaultUpdateLogPath)
	if err != nil {
		return err
	}
	runner := autoupdate.Runner{
		CurrentVersion: buildinfo.Version,
		Repository:     buildinfo.Repository,
		ConfigPath:     configPath,
		Releases:       autoupdate.NewGitHubClient(),
		Scanner:        scanner,
		Install: func(ctx context.Context, installCfg config.Config, version string, output io.Writer) error {
			return autoupdate.InstallFromWorker(ctx, installCfg, version, output)
		},
		Logger:   logger,
		LockPath: updateLockPath,
	}
	return runner.Run(context.Background(), cfg)
}

func withConfigUpdate(path string, overrides config.Overrides, update func(*config.Config) error) (config.Config, error) {
	return withConfigUpdateMode(path, overrides, update, false)
}

func withConfigUpdateWait(path string, overrides config.Overrides, update func(*config.Config) error) (config.Config, error) {
	return withConfigUpdateMode(path, overrides, update, true)
}

func withConfigUpdateMode(path string, overrides config.Overrides, update func(*config.Config) error, wait bool) (config.Config, error) {
	var cfg config.Config
	lock := autoupdate.WithUpdateLock
	if wait {
		lock = autoupdate.WithUpdateLockWait
	}
	err := lock(updateLockPath, func() error {
		fresh, err := loadConfigOrDefaults(path)
		if err != nil {
			return err
		}
		fresh = config.MergeOverrides(fresh, overrides)
		if update != nil {
			if err := update(&fresh); err != nil {
				return err
			}
		}
		cfg = fresh
		return nil
	})
	return cfg, err
}

func loadConfigOrDefaults(path string) (config.Config, error) {
	cfg, err := config.Load(path)
	if err == nil {
		return cfg, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return config.Defaults(), nil
	}
	return config.Config{}, err
}

func parseOptions(arguments []string) (cliOptions, error) {
	flags := flag.NewFlagSet("cf-san", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	urlValue := flags.String("url", "", "HTTPS Worker base URL")
	tokenValue := flags.String("token", "", "API Bearer token")
	asnValue := flags.Uint("asn", 0, "AS number")
	limitValue := flags.Uint64("limit", 0, "Aggregate Masscan packets-per-second cap")
	workersValue := flags.Uint("workers", 0, "Concurrent curl validations")
	sendersValue := flags.Uint("senders", 0, "Deprecated alias for --workers")
	cpuPercentValue := flags.Uint("cpu-percent", 0, "Aggregate Masscan CPU limit as percent of visible logical CPUs")
	configPath := flags.String("config", "", "local configuration path")
	if err := flags.Parse(arguments); err != nil {
		return cliOptions{}, err
	}
	if flags.NArg() != 0 {
		return cliOptions{}, errors.New("unexpected positional arguments")
	}
	options := cliOptions{configPath: *configPath}
	limitWasSet := false
	workersWasSet := false
	sendersWasSet := false
	cpuPercentWasSet := false
	flags.Visit(func(item *flag.Flag) {
		switch item.Name {
		case "limit":
			limitWasSet = true
		case "workers":
			workersWasSet = true
		case "senders":
			sendersWasSet = true
		case "cpu-percent":
			cpuPercentWasSet = true
		}
	})
	if flags.Lookup("url").Value.String() != "" {
		value := *urlValue
		options.overrides.URL = &value
	}
	if flags.Lookup("token").Value.String() != "" {
		value := *tokenValue
		options.overrides.Token = &value
	}
	if flags.Lookup("asn").Value.String() != "0" {
		if *asnValue > ^uint(0)>>32 {
			return cliOptions{}, errors.New("ASN exceeds 32-bit range")
		}
		value := uint32(*asnValue)
		options.overrides.ASN = &value
	}
	if limitWasSet {
		value := *limitValue
		options.overrides.Limit = &value
	}
	if workersWasSet && sendersWasSet {
		return cliOptions{}, errors.New("specify only one of --workers and deprecated --senders")
	}
	if workersWasSet || sendersWasSet {
		value := *workersValue
		if sendersWasSet {
			value = *sendersValue
		}
		if uint64(value) > uint64(^uint32(0)) {
			return cliOptions{}, errors.New("curl worker count exceeds 32-bit range")
		}
		parsed := uint32(value)
		options.overrides.CurlWorkers = &parsed
	}
	if cpuPercentWasSet {
		if *cpuPercentValue == 0 || *cpuPercentValue > 100 {
			return cliOptions{}, errors.New("Masscan total CPU limit must be between 1 and 100 percent")
		}
		value := uint32(*cpuPercentValue)
		options.overrides.MasscanCPUPercent = &value
	}
	return options, nil
}

func runScan(cfg config.Config, configPath string) error {
	if err := config.ValidateForScan(cfg); err != nil {
		return err
	}
	if os.Geteuid() != 0 {
		return errors.New("Masscan requires elevated privileges; run cf-san through sudo")
	}
	if _, err := exec.LookPath("masscan"); err != nil {
		return errors.New("masscan is not installed; rerun the cf-san installer")
	}
	if _, err := exec.LookPath("curl"); err != nil {
		return errors.New("curl is not installed; rerun the cf-san installer")
	}
	if _, err := exec.LookPath("cpulimit"); err != nil {
		return errors.New("cpulimit is not installed; rerun the cf-san installer")
	}
	client, err := backend.NewClient(cfg.URL, cfg.Token, cfg.AgentID)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	plan, err := client.Plan(ctx, cfg.ASN)
	if err != nil {
		return err
	}
	fmt.Fprintf(
		os.Stdout,
		"ASN %d: %d current source prefixes; scanning pending blocks in ascending batches (up to %d concurrent)\n",
		cfg.ASN, plan.PrefixCount(), scanner.MaxConcurrentBlocks,
	)
	manager := menu.NewManager()
	pid := os.Getpid()
	defer manager.RemoveIfCurrent(pid)
	runner := scanner.Runner{
		Config: scanner.RunnerConfig{
			MasscanPath:  "masscan",
			CPULimitPath: "cpulimit",
			CurlPath:     "curl",
			Limit:        cfg.Limit,
			CurlWorkers:  cfg.CurlWorkers,
			CPUPercent:   cfg.MasscanCPUPercent,
		},
		Reporter: client,
	}
	if err := runner.RunPlan(ctx, plan); err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, "当前 ASN 扫描计划已完成。")
	return nil
}
