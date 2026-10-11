package menu

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

var ErrAlreadyRunning = errors.New("cf-san scanner is already running")

type Manager struct {
	Executable string
	Arguments  []string
	RunDir     string
	LogDir     string
}

type pidRecord struct {
	PID        int    `json:"pid"`
	Executable string `json:"executable"`
}

func NewManager() Manager {
	executable, _ := os.Executable()
	runDir := os.Getenv("CF_SAN_RUN_DIR")
	if runDir == "" {
		runDir = "/run/cf-san"
	}
	logDir := os.Getenv("CF_SAN_LOG_DIR")
	if logDir == "" {
		logDir = "/var/log/cf-san"
	}
	return Manager{Executable: executable, Arguments: []string{"run"}, RunDir: runDir, LogDir: logDir}
}

func (m Manager) LogPath() string {
	dir := m.LogDir
	if dir == "" {
		dir = os.Getenv("CF_SAN_LOG_DIR")
	}
	if dir == "" {
		dir = "/var/log/cf-san"
	}
	return filepath.Join(dir, "agent.log")
}

func (m Manager) pidPath() string {
	dir := m.RunDir
	if dir == "" {
		dir = "/run/cf-san"
	}
	return filepath.Join(dir, "cf-san.pid")
}

func (m Manager) Start() error {
	running, err := m.Status()
	if err != nil {
		return err
	}
	if running {
		return ErrAlreadyRunning
	}
	if m.Executable == "" {
		return errors.New("cf-san executable path is missing")
	}
	if m.RunDir == "" {
		m.RunDir = "/run/cf-san"
	}
	if m.LogDir == "" {
		m.LogDir = "/var/log/cf-san"
	}
	if err := os.MkdirAll(m.RunDir, 0o700); err != nil {
		return errors.New("cannot create scanner runtime directory")
	}
	if m.RunDir != "" {
		_ = os.Chmod(m.RunDir, 0o700)
	}
	if err := os.MkdirAll(m.LogDir, 0o700); err != nil {
		return errors.New("cannot create scanner log directory")
	}
	_ = os.Chmod(m.LogDir, 0o700)
	logFile, err := os.OpenFile(filepath.Join(m.LogDir, "agent.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return errors.New("cannot open scanner log")
	}
	_ = logFile.Chmod(0o600)
	arguments := m.Arguments
	if arguments == nil {
		arguments = []string{"run"}
	}
	command := exec.Command(m.Executable, arguments...)
	command.Stdout = logFile
	command.Stderr = logFile
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return errors.New("cannot start scanner process")
	}
	_ = logFile.Close()
	metadata, err := json.Marshal(pidRecord{PID: command.Process.Pid, Executable: command.Path})
	if err == nil {
		err = os.WriteFile(m.pidPath(), append(metadata, '\n'), 0o600)
	}
	if err != nil {
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGTERM)
		go func() { _ = command.Wait() }()
		return errors.New("cannot write protected scanner PID file")
	}
	go func() { _ = command.Wait() }()
	return nil
}

func (m Manager) Status() (bool, error) {
	path := m.pidPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, errors.New("cannot read scanner PID file")
	}
	var record pidRecord
	if err := json.Unmarshal(data, &record); err != nil || record.PID <= 0 || record.Executable == "" {
		_ = os.Remove(path)
		return false, nil
	}
	process, err := os.FindProcess(record.PID)
	if err != nil || process.Signal(syscall.Signal(0)) != nil || !processMatches(record.PID, record.Executable) {
		_ = os.Remove(path)
		return false, nil
	}
	return true, nil
}

func (m Manager) Stop() error {
	running, err := m.Status()
	if err != nil {
		return err
	}
	if !running {
		return nil
	}
	data, err := os.ReadFile(m.pidPath())
	if err != nil {
		return errors.New("cannot read scanner PID file")
	}
	var record pidRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return errors.New("scanner PID file is invalid")
	}
	if err := syscall.Kill(record.PID, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return errors.New("cannot signal scanner process")
	}
	return nil
}

func (m Manager) RemoveIfCurrent(pid int) {
	data, err := os.ReadFile(m.pidPath())
	if err != nil {
		return
	}
	var record pidRecord
	if json.Unmarshal(data, &record) == nil && record.PID == pid {
		_ = os.Remove(m.pidPath())
	}
}

func processMatches(pid int, executable string) bool {
	if runtime.GOOS != "linux" {
		return true
	}
	procDir := filepath.Join("/proc", strconv.Itoa(pid))
	stat, err := os.ReadFile(filepath.Join(procDir, "stat"))
	if err != nil {
		return false
	}
	_, processState, found := strings.Cut(string(stat), ") ")
	if !found || strings.HasPrefix(processState, "Z") {
		return false
	}
	runningExecutable, err := os.Readlink(filepath.Join(procDir, "exe"))
	if err != nil || strings.HasSuffix(runningExecutable, " (deleted)") {
		return false
	}
	expectedExecutable, err := filepath.EvalSymlinks(executable)
	if err != nil {
		expectedExecutable = executable
	}
	return filepath.Clean(runningExecutable) == filepath.Clean(expectedExecutable)
}

func (m Manager) String() string {
	running, err := m.Status()
	if err != nil {
		return fmt.Sprintf("状态未知：%v", err)
	}
	if running {
		return "运行中"
	}
	return "已暂停"
}
