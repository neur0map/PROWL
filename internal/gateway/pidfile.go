package gateway

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const pidFileName = "gateway.pid"

// PIDFilePath is where a gateway serving from dir records its process id, so a
// separate `gateway down` invocation can find and stop it.
func PIDFilePath(dir string) string { return filepath.Join(dir, pidFileName) }

// WritePIDFile records the current process as the gateway holding dir. It
// creates dir if the gateway has never run before.
func WritePIDFile(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create gateway dir: %w", err)
	}
	pid := strconv.Itoa(os.Getpid())
	if err := os.WriteFile(PIDFilePath(dir), []byte(pid), 0o600); err != nil {
		return fmt.Errorf("write gateway pid: %w", err)
	}
	return nil
}

// ReadPIDFile returns the recorded pid, or 0 when no gateway has recorded one.
func ReadPIDFile(dir string) (int, error) {
	blob, err := os.ReadFile(PIDFilePath(dir))
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read gateway pid: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(blob)))
	if err != nil {
		return 0, fmt.Errorf("parse gateway pid %q: %w", strings.TrimSpace(string(blob)), err)
	}
	return pid, nil
}

// RemovePIDFile clears the recorded pid. A missing file is not an error: the
// point is that no record remains.
func RemovePIDFile(dir string) error {
	err := os.Remove(PIDFilePath(dir))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
