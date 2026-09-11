package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/neur0map/prowl/internal/config"
	"github.com/stretchr/testify/require"
)

// TestShellConfigDotProwlrcTakesPrecedence verifies that a project-local
// .prowlrc overrides prowlrc in the same directory on conflicting settings.
func TestShellConfigDotProwlrcTakesPrecedence(t *testing.T) {
	isolated := t.TempDir()
	t.Setenv("HOME", isolated)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(isolated, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(isolated, ".local", "share"))

	workDir := t.TempDir()
	dataDir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(workDir, "prowlrc"),
		[]byte("option notifications bell\n"), 0o644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(workDir, ".prowlrc"),
		[]byte("option notifications osc\n"), 0o644,
	))

	store, err := config.Load(workDir, dataDir, false)
	require.NoError(t, err)
	require.Equal(t, "osc", store.Config().Options.Notifications,
		".prowlrc should win over prowlrc")
}
