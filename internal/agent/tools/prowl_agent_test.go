package tools

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"

	"github.com/neur0map/prowl/internal/config"
)

// The read-only contract is load-bearing: the tool must refuse mutating or
// side-effecting subcommands before executing the native engine.
func TestProwlAgentToolRejectsNonReadOnlyCommands(t *testing.T) {
	t.Parallel()
	tool := NewProwlAgentTool(&config.ProwlAgentOptions{Path: "prowl-agent"}, t.TempDir())
	for _, cmd := range []string{"init", "restart", "update", "knowledge", "skills", "graph", "explore", "docs"} {
		input, err := json.Marshal(ProwlAgentParams{Command: cmd})
		require.NoError(t, err)
		resp, err := tool.Run(context.Background(), fantasy.ToolCall{ID: "t", Name: ProwlAgentToolName, Input: string(input)})
		require.NoError(t, err)
		require.True(t, resp.IsError, "command %q must be rejected", cmd)
	}
}

func TestProwlAgentToolRequiresCommand(t *testing.T) {
	t.Parallel()
	tool := NewProwlAgentTool(nil, t.TempDir())
	input, err := json.Marshal(ProwlAgentParams{Command: "  "})
	require.NoError(t, err)
	resp, err := tool.Run(context.Background(), fantasy.ToolCall{ID: "t", Name: ProwlAgentToolName, Input: string(input)})
	require.NoError(t, err)
	require.True(t, resp.IsError)
}

func TestProwlAgentDiagnosticByteBudgetPreservesUTF8(t *testing.T) {
	t.Parallel()
	for _, limit := range []int{0, 4, 16, 80} {
		input := strings.Repeat("évidence\n", 20)
		out := prowlAgentClamp(input, limit)
		require.LessOrEqual(t, len(out), limit)
		require.True(t, utf8.ValidString(out))
	}
}

func TestProwlAgentOversizedOutputRemainsRecoverable(t *testing.T) {
	t.Parallel()
	input := strings.Repeat("complete quoted \"évidence\" <&>\n", 3000)
	out, err := prowlAgentOutput(input, t.TempDir())
	require.NoError(t, err)
	require.LessOrEqual(t, len(out), maxProwlAgentOutput)
	var result struct {
		Snapshot string `json:"snapshot"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	recovered, err := os.ReadFile(result.Snapshot)
	require.NoError(t, err)
	require.Equal(t, input, string(recovered))
	info, err := os.Stat(result.Snapshot)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}
