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

	"cf-san/internal/backend"
	"cf-san/internal/config"
	"cf-san/internal/menu"
	"cf-san/internal/scanner"
)

type cliOptions struct {
	configPath string
	overrides  config.Overrides
}

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
	options, err := parseOptions(arguments)
	if err != nil {
		return err
	}
	if options.configPath == "" {
		options.configPath = menu.DefaultConfigPath()
	}
	cfg, loadErr := config.Load(options.configPath)
	if loadErr != nil && !errors.Is(loadErr, os.ErrNotExist) {
		return loadErr
	}
	if errors.Is(loadErr, os.ErrNotExist) {
		cfg = config.Defaults()
	}
	cfg = config.MergeOverrides(cfg, options.overrides)

	switch command {
	case "menu":
		if os.Geteuid() != 0 {
			return errors.New("请使用 sudo cf-san 打开菜单；配置文件与 ZMap 扫描需要 root 权限")
		}
		if config.Validate(cfg) == nil {
			if err := config.Save(options.configPath, cfg); err != nil {
				return err
			}
		}
		app := menu.NewApp()
		app.ConfigPath = options.configPath
		manager := menu.NewManager()
		manager.Arguments = []string{"run", "--config", options.configPath}
		app.Manager = &manager
		return app.Run()
	case "configure":
		if err := config.Validate(cfg); err != nil {
			return err
		}
		if err := config.Save(options.configPath, cfg); err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, "配置已写入本机文件；文件权限为 0600，凭据仍以明文保存。")
		return nil
	case "run":
		if err := config.Save(options.configPath, cfg); err != nil {
			return err
		}
		return runScan(cfg, options.configPath)
	default:
		return fmt.Errorf("未知命令 %q；可用命令：menu、configure、run", command)
	}
}

func parseOptions(arguments []string) (cliOptions, error) {
	flags := flag.NewFlagSet("cf-san", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	urlValue := flags.String("url", "", "HTTPS Worker base URL")
	tokenValue := flags.String("token", "", "API Bearer token")
	asnValue := flags.Uint("asn", 0, "AS number")
	limitValue := flags.Uint64("limit", 0, "ZMap packets per second")
	sendersValue := flags.Uint("senders", 0, "ZGrab2 sender concurrency")
	configPath := flags.String("config", "", "local configuration path")
	if err := flags.Parse(arguments); err != nil {
		return cliOptions{}, err
	}
	if flags.NArg() != 0 {
		return cliOptions{}, errors.New("unexpected positional arguments")
	}
	options := cliOptions{configPath: *configPath}
	limitWasSet := false
	sendersWasSet := false
	flags.Visit(func(item *flag.Flag) {
		if item.Name == "limit" {
			limitWasSet = true
		}
		if item.Name == "senders" {
			sendersWasSet = true
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
	if sendersWasSet {
		if uint64(*sendersValue) > uint64(^uint32(0)) {
			return cliOptions{}, errors.New("sender count exceeds 32-bit range")
		}
		value := uint32(*sendersValue)
		options.overrides.Senders = &value
	}
	return options, nil
}

func runScan(cfg config.Config, configPath string) error {
	if err := config.ValidateForScan(cfg); err != nil {
		return err
	}
	if _, err := exec.LookPath("zmap"); err != nil {
		return errors.New("zmap is not installed")
	}
	if _, err := exec.LookPath("zgrab2"); err != nil {
		return errors.New("zgrab2 is not installed")
	}
	if os.Geteuid() != 0 {
		return errors.New("ZMap requires elevated privileges; run cf-san through sudo")
	}
	client, err := backend.NewClient(cfg.URL, cfg.Token)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	plan, err := client.Plan(ctx, cfg.ASN)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "ASN %d: %d current source prefixes; scanning pending blocks in ascending order\n", cfg.ASN, plan.PrefixCount())
	manager := menu.NewManager()
	pid := os.Getpid()
	defer manager.RemoveIfCurrent(pid)
	runner := scanner.Runner{
		Config: scanner.RunnerConfig{
			ZMapPath:      "zmap",
			ZGrab2Path:    "zgrab2",
			BlocklistPath: scanner.DefaultBlocklistPath,
			Limit:         cfg.Limit,
			Senders:       cfg.Senders,
		},
		Reporter: client,
	}
	if err := runner.RunPlan(ctx, plan); err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, "当前 ASN 扫描计划已完成。")
	return nil
}
