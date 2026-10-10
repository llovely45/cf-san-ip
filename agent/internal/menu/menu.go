package menu

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"cf-san/internal/backend"
	"cf-san/internal/config"
)

type App struct {
	ConfigPath  string
	Manager     *Manager
	Input       io.Reader
	Output      io.Writer
	updateAgent func(context.Context, config.Config, io.Writer) error
}

func NewApp() *App {
	return &App{
		ConfigPath: DefaultConfigPath(),
		Input:      os.Stdin,
		Output:     os.Stdout,
	}
}

func DefaultConfigPath() string {
	if path := os.Getenv("CF_SAN_CONFIG_PATH"); path != "" {
		return path
	}
	return "/etc/cf-san/config.json"
}

func (app *App) manager() *Manager {
	if app.Manager != nil {
		return app.Manager
	}
	manager := NewManager()
	return &manager
}

func (app *App) Run() error {
	if app.ConfigPath == "" {
		app.ConfigPath = DefaultConfigPath()
	}
	if app.Input == nil {
		app.Input = os.Stdin
	}
	if app.Output == nil {
		app.Output = os.Stdout
	}
	reader := bufio.NewReader(app.Input)
	cfg, err := loadConfig(app.ConfigPath)
	if err != nil {
		return err
	}
	for {
		fmt.Fprintln(app.Output, "\ncf-san ASN Scanner")
		fmt.Fprintln(app.Output, "1. 初始化环境 / 检查后端连接")
		fmt.Fprintln(app.Output, "2. 运行扫描（后台）")
		fmt.Fprintln(app.Output, "3. 暂停扫描")
		fmt.Fprintln(app.Output, "4. 设置 ZMap 发包速率（PPS）")
		fmt.Fprintln(app.Output, "5. 设置 ZGrab2 并发数")
		fmt.Fprintln(app.Output, "6. 设置 ASN")
		fmt.Fprintln(app.Output, "7. 查看配置与状态")
		fmt.Fprintln(app.Output, "8. 修改后端地址")
		fmt.Fprintln(app.Output, "9. 修改 API Token")
		fmt.Fprintln(app.Output, "10. 更新 cf-san 和 ZGrab2 到最新版本")
		fmt.Fprintln(app.Output, "11. 退出")
		choice, err := readLine(reader, app.Output, "请选择：")
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		switch choice {
		case "1":
			if err := app.initialize(reader, &cfg); err != nil {
				fmt.Fprintf(app.Output, "初始化失败：%v\n", err)
			}
		case "2":
			if err := app.start(&cfg); err != nil {
				fmt.Fprintf(app.Output, "启动失败：%v\n", err)
			} else {
				fmt.Fprintln(app.Output, "扫描已在后台启动。")
			}
		case "3":
			if err := app.manager().Stop(); err != nil {
				fmt.Fprintf(app.Output, "暂停失败：%v\n", err)
			} else {
				fmt.Fprintln(app.Output, "已发送安全暂停信号；当前块会在下次运行时重扫。")
			}
		case "4", "5", "6", "8", "9":
			if err := app.updateSetting(reader, &cfg, choice); err != nil {
				fmt.Fprintf(app.Output, "设置失败：%v\n", err)
			}
		case "7":
			app.printStatus(cfg)
		case "10":
			if err := app.updateLatest(&cfg); err != nil {
				fmt.Fprintf(app.Output, "更新失败：%v\n", err)
			} else {
				fmt.Fprintln(app.Output, "更新完成；请退出菜单后重新运行 cf-san，以加载新版本。")
			}
		case "11":
			return nil
		default:
			fmt.Fprintln(app.Output, "无效选项，请输入 1–11。")
		}
	}
}

func (app *App) initialize(reader *bufio.Reader, cfg *config.Config) error {
	if os.Geteuid() != 0 {
		return errors.New("请使用 sudo cf-san；本机配置文件需要由 root 安全保存")
	}
	if strings.TrimSpace(cfg.URL) == "" {
		value, err := readLine(reader, app.Output, "Worker HTTPS URL：")
		if err != nil {
			return err
		}
		cfg.URL = strings.TrimRight(value, "/")
	}
	if cfg.Token == "" {
		value, err := readLine(reader, app.Output, "API Token（明文输入，仅写入本机 0600 配置文件）：")
		if err != nil {
			return err
		}
		cfg.Token = value
	}
	if cfg.ASN == 0 {
		value, err := readLine(reader, app.Output, "ASN：")
		if err != nil {
			return err
		}
		if err := UpdateSetting(cfg, "asn", value); err != nil {
			return err
		}
	}
	if err := config.ValidateForScan(*cfg); err != nil {
		return err
	}
	if _, err := exec.LookPath("zmap"); err != nil {
		return errors.New("未找到 zmap；请检查安装")
	}
	if _, err := exec.LookPath("zgrab2"); err != nil {
		return errors.New("未找到 zgrab2；请检查安装")
	}
	if err := config.Save(app.ConfigPath, *cfg); err != nil {
		return err
	}
	client, err := backend.NewClient(cfg.URL, cfg.Token)
	if err != nil {
		return err
	}
	plan, err := client.Plan(context.Background(), cfg.ASN)
	if err != nil {
		return err
	}
	fmt.Fprintf(app.Output, "后端连接正常；当前 ASN %d 有 %d 个来源前缀，扫描断点已载入。\n", cfg.ASN, plan.PrefixCount())
	return nil
}

func (app *App) start(cfg *config.Config) error {
	if err := config.ValidateForScan(*cfg); err != nil {
		return err
	}
	if err := config.Save(app.ConfigPath, *cfg); err != nil {
		return err
	}
	return app.manager().Start()
}

func (app *App) updateLatest(cfg *config.Config) error {
	running, err := app.manager().Status()
	if err != nil {
		return err
	}
	if running {
		return errors.New("请先暂停扫描，再执行更新")
	}
	if err := config.Validate(*cfg); err != nil {
		return err
	}
	if err := config.Save(app.ConfigPath, *cfg); err != nil {
		return err
	}
	updater := app.updateAgent
	if updater == nil {
		updater = updateLatestFromWorker
	}
	return updater(context.Background(), *cfg, app.Output)
}

func (app *App) updateSetting(reader *bufio.Reader, cfg *config.Config, choice string) error {
	key, prompt := "", ""
	switch choice {
	case "4":
		key, prompt = "limit", "新的 ZMap 发包速率（PPS）："
	case "5":
		key, prompt = "senders", "新的 ZGrab2 并发数："
	case "6":
		key, prompt = "asn", "新的 ASN："
	case "8":
		key, prompt = "url", "新的 Worker HTTPS 地址："
	case "9":
		key, prompt = "token", "新的 API Token（明文保存）："
	}
	value, err := readLine(reader, app.Output, prompt)
	if err != nil {
		return err
	}
	if err := UpdateSetting(cfg, key, value); err != nil {
		return err
	}
	if err := config.Save(app.ConfigPath, *cfg); err != nil {
		return err
	}
	fmt.Fprintln(app.Output, "设置已保存。")
	return nil
}

func (app *App) printStatus(cfg config.Config) {
	manager := app.manager()
	running, _ := manager.Status()
	state := "已暂停"
	if running {
		state = "运行中"
	}
	tokenState := "未配置"
	if cfg.Token != "" {
		tokenState = cfg.Token
	}
	fmt.Fprintf(app.Output, "后端：%s\nASN：%d\nZMap PPS 上限：%d\nZGrab2 并发：%d\nToken：%s\nAgent ID：%s\n扫描状态：%s\n",
		cfg.URL, cfg.ASN, cfg.Limit, cfg.Senders, tokenState, cfg.AgentID, state)
}

func UpdateSetting(cfg *config.Config, key, raw string) error {
	value := strings.TrimSpace(raw)
	switch key {
	case "asn":
		parsed, err := strconv.ParseUint(value, 10, 32)
		if err != nil || parsed == 0 {
			return errors.New("ASN 必须是正整数")
		}
		cfg.ASN = uint32(parsed)
	case "limit":
		parsed, err := strconv.ParseUint(value, 10, 64)
		if err != nil || parsed == 0 || parsed > math.MaxInt64 {
			return errors.New("ZMap 发包速率必须是大于零的整数")
		}
		cfg.Limit = parsed
	case "senders":
		parsed, err := strconv.ParseUint(value, 10, 32)
		if err != nil || parsed == 0 {
			return errors.New("ZGrab2 并发数必须是正整数")
		}
		cfg.Senders = uint32(parsed)
	case "url":
		if err := config.ValidateBackendURL(value); err != nil {
			return err
		}
		cfg.URL = strings.TrimRight(value, "/")
	case "token":
		if value == "" {
			return errors.New("API Token 不能为空")
		}
		cfg.Token = value
	default:
		return errors.New("未知配置项")
	}
	return nil
}

func loadConfig(path string) (config.Config, error) {
	cfg, err := config.Load(path)
	if err == nil {
		return cfg, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return config.Defaults(), nil
	}
	return config.Config{}, err
}

func readLine(reader *bufio.Reader, writer io.Writer, prompt string) (string, error) {
	fmt.Fprint(writer, prompt)
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if errors.Is(err, io.EOF) && line == "" {
		return "", io.EOF
	}
	return strings.TrimSpace(line), nil
}
