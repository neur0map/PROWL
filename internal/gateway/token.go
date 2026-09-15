package gateway

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const tokenFileName = "token"

// tokenHeader carries the local authorisation token.
const tokenHeader = "X-Prowl-Gateway-Token"

// EnsureToken returns the machine-local bootstrap credential, creating it on
// first use. It is deliberately separate from a dashboard session and from the
// unified inference key: this one exists so a freshly launched harness can
// open its own dashboard without an account.
func EnsureToken(dir string) (string, error) { return loadOrCreateToken(dir) }

func loadOrCreateToken(dir string) (string, error) {
	path := filepath.Join(dir, tokenFileName)
	blob, err := os.ReadFile(path)
	if err == nil {
		if token := strings.TrimSpace(string(blob)); token != "" {
			return token, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read gateway token: %w", err)
	}

	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate gateway token: %w", err)
	}
	token := hex.EncodeToString(raw)
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		return "", fmt.Errorf("write gateway token: %w", err)
	}
	return token, nil
}
