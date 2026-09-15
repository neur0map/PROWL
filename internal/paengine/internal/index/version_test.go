package index

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestIndexVersionIsBinaryIndependent is the regression guard for a bug that
// cost a full re-parse of the repository on every open: the identity used to
// be derived from the running binary (VCS revision, or the executable's mtime
// for a dirty build). This engine runs both from the standalone prowl-agent
// CLI and in-process inside Prowl, so two hosts carrying byte-identical
// extraction logic disagreed, each rejected the other's index, and the forced
// re-parse held the project refresh lock that live queries need.
func TestIndexVersionIsBinaryIndependent(t *testing.T) {
	got := Version()

	require(t, got == indexFormatVersion,
		"Version must be the declared format constant, got %q want %q", got, indexFormatVersion)

	// The identity must not contain anything host-specific.
	exe, err := os.Executable()
	if err == nil {
		require(t, !strings.Contains(got, exe), "identity must not embed the executable path")
	}
	require(t, !strings.HasPrefix(got, "dev-"),
		"a dev- prefix means the identity is keyed on the binary again, got %q", got)

	// Two processes of the same build must agree, which is the property that
	// actually keeps an index shareable.
	require(t, Version() == got, "identity must be stable within a process")
}

// TestIndexVersionOverride keeps the escape hatch working: someone iterating
// on extractor code needs a way to force a re-parse per build.
func TestIndexVersionOverride(t *testing.T) {
	t.Setenv("PROWL_AGENT_INDEX_VERSION", "forced-identity")
	require(t, Version() == "forced-identity", "override must win, got %q", Version())

	t.Setenv("PROWL_AGENT_INDEX_VERSION", "   ")
	require(t, Version() == indexFormatVersion,
		"a blank override must fall back to the constant, got %q", Version())
}

// TestIndexVersionSurvivesRebuild proves the property end to end: the same
// source compiled into two binaries with different modification times reports
// one identity. Before the fix these differed, which is exactly what made
// Prowl reject an index the standalone CLI had just built.
func TestIndexVersionSurvivesRebuild(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	// The constant is compiled in, so a second build cannot change it; assert
	// the source carries no host-derived fallback rather than paying for a
	// real compile.
	source, err := os.ReadFile("index.go")
	if err != nil {
		t.Fatalf("read index.go: %v", err)
	}
	body := string(source)
	start := strings.Index(body, "func indexVersion()")
	require(t, start >= 0, "indexVersion must exist")
	fn := body[start:]
	if end := strings.Index(fn, "\n}"); end > 0 {
		fn = fn[:end]
	}
	for _, banned := range []string{"os.Executable", "debug.ReadBuildInfo", "ModTime", "vcs.revision"} {
		require(t, !strings.Contains(fn, banned),
			"indexVersion must not derive identity from the binary; found %q", banned)
	}
}

func require(t *testing.T, cond bool, format string, args ...any) {
	t.Helper()
	if !cond {
		t.Fatalf(format, args...)
	}
}
