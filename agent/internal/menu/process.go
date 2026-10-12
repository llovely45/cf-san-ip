package menu

import (
	"context"
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
	"time"

	"cf-san/internal/autoupdate"
)

var ErrAlreadyRunning = errors.New("cf-san scanner is already running")

type Manager struct {
	Executable      string
	Arguments       []string
	RunDir          string
	LogDir          string
	ResumeStatePath string
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

// PendingResumePath returns the root-only state path shared with the updater.
func (m Manager) PendingResumePath() string {
	if m.ResumeStatePath != "" {
		return m.ResumeStatePath
	}
	if path := os.Getenv("CF_SAN_RESUME_STATE_PATH"); path != "" {
		return path
	}
	return autoupdate.DefaultResumeStatePath
}

// ClearPendingResume clears an automatic resume intent after an intentional
// manual scanner start or stop.
func (m Manager) ClearPendingResume() error {
	return autoupdate.ClearPendingResume(m.PendingResumePath())
}

// RegisterCurrent records a foreground cf-san run for updater lifecycle
// coordination. The caller must hold the shared update lock before invoking it.
func (m Manager) RegisterCurrent() error {
	running, err := m.Status()
	if err != nil {
		return err
	}
	if running {
		data, readErr := os.ReadFile(m.pidPath())
		var record pidRecord
		if readErr != nil || json.Unmarshal(data, &record) != nil || record.PID != os.Getpid() {
			return ErrAlreadyRunning
		}
		return nil
	}
	if m.Executable == "" {
		return errors.New("cf-san executable path is missing")
	}
	if m.RunDir == "" {
		m.RunDir = "/run/cf-san"
	}
	if err := os.MkdirAll(m.RunDir, 0o700); err != nil {
		return errors.New("cannot create scanner runtime directory")
	}
	_ = os.Chmod(m.RunDir, 0o700)
	return m.writePID(os.Getpid(), m.Executable)
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
	command.Env = append(os.Environ(), "CF_SAN_MANAGED_SCAN=1")
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return errors.New("cannot start scanner process")
	}
	_ = logFile.Close()
	err = m.writePID(command.Process.Pid, command.Path)
	if err != nil {
		killProcessGroup(command.Process, syscall.SIGKILL)
		_ = command.Wait()
		return errors.New("cannot write protected scanner PID file")
	}
	done := make(chan error, 1)
	go func() {
		done <- command.Wait()
		m.RemoveIfCurrent(command.Process.Pid)
	}()
	startContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	err = m.waitStarted(startContext, 10*time.Millisecond)
	cancel()
	if err != nil {
		killProcessGroup(command.Process, syscall.SIGKILL)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
		m.RemoveIfCurrent(command.Process.Pid)
		if errors.Is(err, context.DeadlineExceeded) {
			return errors.New("scanner did not become ready before startup timeout")
		}
		return err
	}
	return nil
}

func (m Manager) writePID(pid int, executable string) error {
	metadata, err := json.Marshal(pidRecord{PID: pid, Executable: executable})
	if err != nil {
		return err
	}
	return os.WriteFile(m.pidPath(), append(metadata, '\n'), 0o600)
}

func (m Manager) waitStarted(ctx context.Context, interval time.Duration) error {
	if ctx == nil {
		return errors.New("startup context must not be nil")
	}
	if interval <= 0 {
		interval = time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	seenRunning := false
	for {
		running, err := m.Status()
		if err != nil {
			return err
		}
		if running {
			if seenRunning {
				return nil
			}
			seenRunning = true
		} else {
			seenRunning = false
		}
		if _, err := os.Stat(m.pidPath()); errors.Is(err, os.ErrNotExist) {
			return errors.New("scanner exited before startup confirmation")
		} else if err != nil {
			return errors.New("cannot inspect scanner startup state")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func killProcessGroup(process *os.Process, signal syscall.Signal) {
	if process == nil {
		return
	}
	if err := syscall.Kill(-process.Pid, signal); err != nil {
		if signal == syscall.SIGKILL {
			_ = process.Kill()
		} else {
			_ = process.Signal(signal)
		}
	}
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

func (m Manager) WaitStopped(ctx context.Context, interval time.Duration) error {
	if ctx == nil {
		return errors.New("wait context must not be nil")
	}
	if interval <= 0 {
		interval = time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		running, err := m.Status()
		if err != nil || !running {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
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
