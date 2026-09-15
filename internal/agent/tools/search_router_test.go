package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/neur0map/prowl/internal/shell"
)

// TestTheGuardRefusesTheSearchesTheIndexAnswers is the behaviour the user
// reported: Prowl states the routing rule in the bash description, in the
// grep description and in a post-tool reminder, and the model still shells
// out to `grep -rn` over a subtree — once in the same turn as a prowl_agent
// call that had already answered the question.
func TestTheGuardRefusesTheSearchesTheIndexAnswers(t *testing.T) {
	t.Parallel()

	for _, command := range [][]string{
		// The reported command, verbatim in shape: a structural question
		// approximated by an alternation, bounded to a subtree.
		{"grep", "-rn", `"track"\|track --source\|cmdTrack`, "ryoku/cli/"},
		{"grep", "-r", "NewShell", "internal"},
		{"grep", "-rni", "handler", "."},
		{"rg", "buildTools"},
		{"rg", "--type", "go", "Registry"},
		{"/usr/bin/grep", "-R", "token", "internal/gateway"},
		{"git", "grep", "-n", "routingAdvisor"},
		{"find", ".", "-name", "*.go"},
		{"fd", "-name", "*.tsx"},
		{"ag", "sessionAgent"},
	} {
		reason := SearchRouterGuard(command)
		require.NotEmpty(t, reason, "%v should be routed to a tool", command)
		require.Contains(t, reason, "prowl_agent",
			"a refusal must name what to call instead: %v", command)
	}
}

// TestTheGuardLeavesRealWorkAlone is what keeps the rule from teaching the
// model to fight the tool. A refusal that blocks legitimate shell work is
// worse than the misroute it prevents.
func TestTheGuardLeavesRealWorkAlone(t *testing.T) {
	t.Parallel()

	for _, command := range [][]string{
		// A pipeline filter: grep with no path reads stdin.
		{"grep", "FAIL"},
		{"grep", "-c", "error"},
		{"grep", "-v", "^ok"},
		// One named file is not a tree scan.
		{"grep", "-n", "Version", "internal/version/version.go"},
		{"grep", "TODO", "main.go"},
		// find doing something glob cannot express.
		{"find", ".", "-name", "*.log", "-delete"},
		{"find", "internal", "-newer", "go.mod"},
		{"find", ".", "-type", "f", "-exec", "wc", "-l", "{}", ";"},
		// Unrelated commands.
		{"go", "test", "./..."},
		{"git", "status", "--porcelain"},
		{"sqlite3", "db", "select 1"},
		{},
	} {
		require.Empty(t, SearchRouterGuard(command),
			"%v is legitimate shell work and must run", command)
	}
}

// TestTheRefusalTellsTheModelWhereEachKindOfSearchGoes keeps the message
// actionable: the model has to be able to re-issue the call correctly from it.
func TestTheRefusalTellsTheModelWhereEachKindOfSearchGoes(t *testing.T) {
	t.Parallel()

	reason := SearchRouterGuard([]string{"grep", "-rn", "foo", "internal"})
	for _, needed := range []string{"prowl_agent", "grep tool", "glob", "single file"} {
		require.True(t, strings.Contains(reason, needed),
			"the refusal must mention %q so the model can act on it: %s", needed, reason)
	}
}

// TestTheGuardStopsTheCommandInTheShell proves the refusal is enforced where
// it matters rather than only advertised: the command must not execute, and
// the model must see why. A guard that only lives in a description is the
// thing that already failed.
func TestTheGuardStopsTheCommandInTheShell(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hit.txt"),
		[]byte("needle\n"), 0o644))

	var out, errOut strings.Builder
	err := shell.Run(context.Background(), shell.RunOptions{
		Command: "grep -rn needle .",
		Cwd:     dir,
		Env:     os.Environ(),
		Stdout:  &out,
		Stderr:  &errOut,
		Guards:  SearchGuards(),
	})

	require.Error(t, err, "the guarded command must not succeed")
	require.Contains(t, err.Error()+errOut.String(), "prowl_agent",
		"the shell must report why, not just fail")
	require.NotContains(t, out.String(), "needle",
		"the search must not have run")

	// The same shell still runs the work the guard is not about.
	out.Reset()
	require.NoError(t, shell.Run(context.Background(), shell.RunOptions{
		Command: "cat hit.txt | grep needle",
		Cwd:     dir,
		Env:     os.Environ(),
		Stdout:  &out,
		Stderr:  &errOut,
		Guards:  SearchGuards(),
	}))
	require.Contains(t, out.String(), "needle",
		"a pipeline filter is not a tree scan and must still work")
}
