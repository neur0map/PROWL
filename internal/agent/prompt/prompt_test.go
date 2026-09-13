package prompt

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/neur0map/prowl/internal/config"
)

func TestBuildPreservesConfiguredContextOrder(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	alpha := filepath.Join(root, "alpha.txt")
	beta := filepath.Join(root, "beta.txt")
	gamma := filepath.Join(root, "gamma.txt")
	for path, body := range map[string]string{alpha: "alpha", beta: "beta", gamma: "gamma"} {
		require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	}
	store := config.NewTestStore(&config.Config{Options: &config.Options{
		ContextPaths: []string{beta, root, alpha, filepath.Join(root, ".")},
	}})
	p, err := NewPrompt("context-order", "{{range .ContextFiles}}{{.Content}}\n{{end}}")
	require.NoError(t, err)
	for range 100 {
		built, err := p.Build(context.Background(), store)
		require.NoError(t, err)
		require.Equal(t, "beta\nalpha\ngamma\n", built)
	}
}

func TestContextFilesKeepCaseDistinctPaths(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	upper := filepath.Join(root, "Rule.txt")
	lower := filepath.Join(root, "rule.txt")
	require.NoError(t, os.WriteFile(upper, []byte("upper"), 0o600))
	require.NoError(t, os.WriteFile(lower, []byte("lower"), 0o600))
	upperInfo, err := os.Stat(upper)
	require.NoError(t, err)
	lowerInfo, err := os.Stat(lower)
	require.NoError(t, err)
	if os.SameFile(upperInfo, lowerInfo) {
		t.Skip("The temporary filesystem is case-insensitive")
	}
	store := config.NewTestStore(&config.Config{Options: &config.Options{}})
	files := loadContextFiles([]string{upper, lower}, store)
	require.Equal(t, []ContextFile{{Path: upper, Content: "upper"}, {Path: lower, Content: "lower"}}, files)
}

func TestContextFilesDeduplicateSymbolicLinks(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	original := filepath.Join(root, "original.txt")
	alias := filepath.Join(root, "alias.txt")
	require.NoError(t, os.WriteFile(original, []byte("one instruction"), 0o600))
	if err := os.Symlink(original, alias); err != nil {
		t.Skipf("Symbolic links unavailable: %v", err)
	}
	store := config.NewTestStore(&config.Config{Options: &config.Options{}})
	files := loadContextFiles([]string{alias, original, root}, store)
	require.Equal(t, []ContextFile{{Path: canonicalContextPath(original), Content: "one instruction"}}, files)
}

func TestBuildKeepsGlobalAndProjectContextScopes(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	global := filepath.Join(root, "global.txt")
	project := filepath.Join(root, "project.txt")
	require.NoError(t, os.WriteFile(global, []byte("global"), 0o600))
	require.NoError(t, os.WriteFile(project, []byte("project"), 0o600))
	store := config.NewTestStore(&config.Config{Options: &config.Options{
		GlobalContextPaths: []string{global}, ContextPaths: []string{project},
	}})
	p, err := NewPrompt("scopes", "{{range .GlobalContextFiles}}{{.Content}}{{end}}/{{range .ContextFiles}}{{.Content}}{{end}}")
	require.NoError(t, err)
	built, err := p.Build(context.Background(), store)
	require.NoError(t, err)
	require.Equal(t, "global/project", built)
}
