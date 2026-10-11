package menu

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"cf-san/internal/scanner"
)

func ensureZGrab2Blocklist(sourcePath, targetPath string) (bool, error) {
	info, err := os.Lstat(targetPath)
	if err == nil {
		if !info.Mode().IsRegular() {
			return false, fmt.Errorf("ZGrab2 blocklist path %q is not a regular file", targetPath)
		}
		if info.Size() > 0 {
			return false, scanner.ValidateBlocklistFile(targetPath)
		}
		if err := os.Remove(targetPath); err != nil {
			return false, fmt.Errorf("cannot replace empty ZGrab2 blocklist: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}

	source, err := os.Open(sourcePath)
	if err != nil {
		return false, fmt.Errorf("默认文件 %q 不可用；请先通过菜单 10 更新 cf-san：%w", sourcePath, err)
	}
	defer source.Close()
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return false, fmt.Errorf("cannot create ZGrab2 configuration directory: %w", err)
	}
	target, err := os.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, os.ErrExist) {
		return false, scanner.ValidateBlocklistFile(targetPath)
	}
	if err != nil {
		return false, fmt.Errorf("cannot create ZGrab2 blocklist: %w", err)
	}
	complete := false
	defer func() {
		_ = target.Close()
		if !complete {
			_ = os.Remove(targetPath)
		}
	}()
	if _, err := io.Copy(target, source); err != nil {
		return false, fmt.Errorf("cannot install ZGrab2 blocklist: %w", err)
	}
	if err := target.Chmod(0o644); err != nil {
		return false, fmt.Errorf("cannot set ZGrab2 blocklist permissions: %w", err)
	}
	if err := target.Sync(); err != nil {
		return false, fmt.Errorf("cannot sync ZGrab2 blocklist: %w", err)
	}
	if err := target.Close(); err != nil {
		return false, fmt.Errorf("cannot close ZGrab2 blocklist: %w", err)
	}
	if err := scanner.ValidateBlocklistFile(targetPath); err != nil {
		return false, err
	}
	complete = true
	return true, nil
}
