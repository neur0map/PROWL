package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"charm.land/fantasy"
	"github.com/neur0map/prowl/internal/config"
	"github.com/stretchr/testify/require"
)

// The read-only contract is load-bearing: the tool must refuse mutating or
// side-effecting prowl-agent subcommands before ever executing the binary.
func TestProwlAgentToolRejectsNonReadOnlyCommands(t *testing.T) {
	t.Parallel()
	tool := NewProwlAgentTool(&config.ProwlAgentOptions{Path: "prowl-agent"}, t.TempDir())
	for _, cmd := range []string{"init", "restart", "update", "knowledge", "skills", "graph", "explore", "docs"} {
		input, err := json.Marshal(ProwlAgentParams{Command: cmd})
		require.NoError(t, err)
		resp, err := tool.Run(context.Background(), fantasy.ToolCall{ID: "t", Name: ProwlAgentToolName, Input: string(input)})
		require.NoError(t, err)
		require.True(t, resp.IsError, "command %q must be rejected", cmd)
		require.Contains(t, resp.Content, "unsupported")
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

func TestProwlAgentHasFormatFlag(t *testing.T) {
	t.Parallel()
	require.True(t, prowlAgentHasFormatFlag([]string{"--json"}))
	require.True(t, prowlAgentHasFormatFlag([]string{"--format", "human"}))
	require.True(t, prowlAgentHasFormatFlag([]string{"--format=json"}))
	require.False(t, prowlAgentHasFormatFlag([]string{"NewGui"}))
}

func TestProwlAgentClamp(t *testing.T) {
	t.Parallel()
	require.Equal(t, "abc", prowlAgentClamp("abc", 10))
	out := prowlAgentClamp("abcdefghij", 4)
	require.True(t, strings.HasPrefix(out, "abcd"))
	require.Contains(t, out, "truncated")
}
