package scanner

import (
	"errors"
	"fmt"
	"os"
)

const DefaultBlocklistPath = "/etc/cf-san/zgrab2-blocklist.conf"

func ValidateBlocklistFile(path string) error {
	if path == "" {
		return errors.New("ZGrab2 blocklist path is not configured")
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("ZGrab2 blocklist file %q is unavailable: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("ZGrab2 blocklist path %q is not a regular file", path)
	}
	if info.Size() == 0 {
		return fmt.Errorf("ZGrab2 blocklist file %q is empty", path)
	}
	return nil
}
