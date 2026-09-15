package agent

import (
	"context"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
)

// repoRoot stands in for the project the agent is working in, so an absolute
// path operand can be recognised as the root.
const repoRoot = "/home/nero/Work/ryoku-unstable"

type stubSearchTool struct {
	name    string
	content string
	isError bool
}

func (f stubSearchTool) Info() fantasy.ToolInfo                       { return fantasy.ToolInfo{Name: f.name} }
func (f stubSearchTool) ProviderOptions() fantasy.ProviderOptions     { return nil }
func (f stubSearchTool) SetProviderOptions(_ fantasy.ProviderOptions) {}
func (f stubSearchTool) Run(_ context.Context, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
	return fantasy.ToolResponse{Content: f.content, IsError: f.isError}, nil
}

func runRouted(t *testing.T, advisor *routingAdvisor, name, input, content string) string {
	t.Helper()
	wrapped := wrapToolsWithRouting([]fantasy.AgentTool{stubSearchTool{name: name, content: content}}, advisor, nil)
	require.Len(t, wrapped, 1)
	resp, err := wrapped[0].Run(t.Context(), fantasy.ToolCall{Name: name, Input: input})
	require.NoError(t, err)
	return resp.Content
}

// TestAdvisoryFiresOnRepoWideScan is the reported behaviour: the model
// answered a structural question with `rg` across the repository. That is
// exactly when the reminder has to arrive.
func TestAdvisoryFiresOnRepoWideScan(t *testing.T) {
	t.Parallel()

	got := runRouted(t, newRoutingAdvisor(repoRoot), "bash",
		`{"command":"rg -ril \"limine\" --glob '!*.lock' /home/nero/Work/ryoku-unstable"}`,
		"a.md\nb.md")

	require.Contains(t, got, "prowl_agent index", "a repo-wide rg scan must be routed")
	require.Contains(t, got, "a.md", "the tool's own output must survive")
}

// TestAdvisoryRepeatsAcrossTurns covers the complaint that the index is used
// "once or twice on the first message but not afterwards": the reminder is
// not a first-turn banner, so a later broad search is routed too.
func TestAdvisoryRepeatsAcrossTurns(t *testing.T) {
	t.Parallel()

	advisor := newRoutingAdvisor(repoRoot)
	reminded := 0
	for range 9 {
		if got := runRouted(t, advisor, "grep", `{"pattern":"x","path":"."}`, "hit"); contains(got, routingReminder) {
			reminded++
		}
	}

	require.Greater(t, reminded, 1, "the reminder must keep arriving on later searches")
	require.Less(t, reminded, 9, "reminding on every single hit trains the model to skim past it")
}

// TestAdvisorySilentOnBoundedSearch protects the legitimate uses. A search
// scoped to a file or directory is not the behaviour being corrected, and a
// reminder there is pure noise in the context window.
func TestAdvisorySilentOnBoundedSearch(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct{ tool, input string }{
		"grep in a subdirectory":      {"grep", `{"pattern":"x","path":"internal/agent"}`},
		"glob in a subdirectory":      {"glob", `{"pattern":"*.go","path":"internal"}`},
		"rg against one path":         {"bash", `{"command":"rg foo internal/agent/agent.go"}`},
		"find with a search root":     {"bash", `{"command":"find internal -name '*.go'"}`},
		"a command that is no search": {"bash", `{"command":"go build ./..."}`},
		"reading a file":              {"view", `{"file_path":"internal/agent/agent.go"}`},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := runRouted(t, newRoutingAdvisor(repoRoot), tc.tool, tc.input, "output")
			require.NotContains(t, got, routingReminder)
			require.Equal(t, "output", got)
		})
	}
}

// TestAdvisorySilentOnFailure keeps the advisory out of error paths, where the
// model needs the failure text and nothing else.
func TestAdvisorySilentOnFailure(t *testing.T) {
	t.Parallel()

	wrapped := wrapToolsWithRouting(
		[]fantasy.AgentTool{stubSearchTool{name: "bash", content: "rg: command failed", isError: true}},
		newRoutingAdvisor(repoRoot),
		nil,
	)
	resp, err := wrapped[0].Run(t.Context(), fantasy.ToolCall{
		Name:  "bash",
		Input: `{"command":"rg -ril foo ."}`,
	})
	require.NoError(t, err)
	require.NotContains(t, resp.Content, routingReminder)
}

// TestBroadSearchClassification pins the boundary directly, including the
// shell forms that made the original fallback look bounded.
func TestBroadSearchClassification(t *testing.T) {
	t.Parallel()

	broad := []string{
		// The verbatim command from the reported session: an absolute path
		// that happens to be the project root.
		`{"command":"rg -ril \"limine\" --glob '!*.lock' /home/nero/Work/ryoku-unstable"}`,
		`{"command":"rg -ril \"limine\" ."}`,
		`{"command":"grep -r pattern"}`,
		`{"command":"ls | grep foo"}`,
		`{"command":"fd '\\.go$'"}`,
	}
	for _, input := range broad {
		require.True(t, isMisroutedSearch("bash", input, repoRoot), "expected broad: %s", input)
	}

	bounded := []string{
		`{"command":"rg pattern internal/agent"}`,
		`{"command":"rg pattern /home/nero/Work/ryoku-unstable/ryoku/cli"}`,
		`{"command":"fd '\\.go$' internal"}`,
		`{"command":"git status --short"}`,
	}
	for _, input := range bounded {
		require.False(t, isMisroutedSearch("bash", input, repoRoot), "expected bounded: %s", input)
	}
}

func contains(haystack, needle string) bool {
	return countOccurrences(haystack, needle) > 0
}
