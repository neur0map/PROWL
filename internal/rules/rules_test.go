package rules

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProjectDir(t *testing.T) {
	t.Parallel()
	require.Equal(t, filepath.Join("/proj", ".prowl", "rules"), ProjectDir("/proj"))
}

func TestWriteSanitizesNameAndBody(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	path, err := Write(dir, "My Rule!!  Name", "Always be nice.")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dir, "my-rule-name.md"), path)

	b, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "Always be nice.\n", string(b))
}

func TestWriteRejectsBadInput(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	_, err := Write(dir, "empty-body", "   ")
	require.Error(t, err)

	_, err = Write(dir, "!!!", "body")
	require.Error(t, err)

	_, err = Write(dir, "too-big", strings.Repeat("x", maxRuleBytes+1))
	require.Error(t, err)

	_, err = Write("", "name", "body")
	require.Error(t, err)
}

func TestWriteRefusesSymlink(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.md")
	require.NoError(t, os.WriteFile(outside, []byte("original"), 0o644))

	link := filepath.Join(dir, "target.md")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	_, err := Write(dir, "target", "hijacked")
	require.Error(t, err)
	require.Contains(t, err.Error(), "symlink")

	// The symlink target must be untouched.
	b, err := os.ReadFile(outside)
	require.NoError(t, err)
	require.Equal(t, "original", string(b))
}

func TestListSortsAndFiltersMarkdown(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "beta.md"), []byte("B"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "alpha.md"), []byte("A"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ignored.txt"), []byte("nope"), 0o644))

	list := List([]string{dir})
	require.Len(t, list, 2)
	require.Equal(t, "alpha", list[0].Name)
	require.Equal(t, "A", list[0].Content)
	require.Equal(t, "beta", list[1].Name)
}

func TestListEmptyAndMissing(t *testing.T) {
	t.Parallel()
	require.Nil(t, List(nil))
	require.Nil(t, List([]string{filepath.Join(t.TempDir(), "nope")}))
}

func TestListDeduplicatesSymlink(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	orig := filepath.Join(dir, "orig.md")
	require.NoError(t, os.WriteFile(orig, []byte("body"), 0o644))
	alias := filepath.Join(dir, "alias.md")
	if err := os.Symlink(orig, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	list := List([]string{dir})
	require.Len(t, list, 1)
	require.Equal(t, "orig", list[0].Name)
}
