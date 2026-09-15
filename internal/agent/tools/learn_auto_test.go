package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"

	"github.com/neur0map/prowl/internal/prowlagent"
)

// learnToolForTest builds the learn tool against an initialized prowl-agent
// bundle rooted at a fresh temp project. The managed-skill hooks are inert
// because these tests exercise only the automatic lesson-recording path.
func learnToolForTest(t *testing.T) (fantasy.AgentTool, string) {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(root, "owner.go"),
		[]byte("package example\n\nfunc Owner() bool { return true }\n"),
		0o644,
	))
	require.NoError(t, prowlagent.EnsureIndex(t.Context(), nil, root))
	tool := NewLearnTool(nil, root, func(string) bool { return false }, func() {})
	return tool, root
}

// runLearn invokes the learn tool with the given lesson and returns its
// response.
func runLearn(t *testing.T, tool fantasy.AgentTool, memory string) fantasy.ToolResponse {
	t.Helper()
	input, err := json.Marshal(LearnParams{Memory: memory})
	require.NoError(t, err)
	resp, err := tool.Run(t.Context(), fantasy.ToolCall{
		ID: "learn-call", Name: LearnToolName, Input: string(input),
	})
	require.NoError(t, err)
	return resp
}

// TestLearnAutomaticRecordingDedupesAndStaysProposed proves the automatic
// recording contract end to end: a genuinely new lesson is filed as a pending
// proposal (never accepted), a near-duplicate of it is refused with a plain
// non-error response and files nothing, and a second genuinely new lesson is
// filed alongside the first.
func TestLearnAutomaticRecordingDedupesAndStaysProposed(t *testing.T) {
	tool, root := learnToolForTest(t)

	first := runLearn(t, tool,
		"The index format version must be binary-independent so both hosts accept the same index.")
	require.False(t, first.IsError)
	require.Contains(t, first.Content, "Proposal")
	require.Contains(t, first.Content, "Pending human review")

	// The automatic proposal lands as `proposed` and is never accepted on its
	// own — the human decides.
	pending, err := prowlagent.ListProposals(t.Context(), nil, root)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, "proposed", pending[0].Status)
	docs, err := prowlagent.ListKnowledge(t.Context(), nil, root)
	require.NoError(t, err)
	require.Empty(t, docs, "an automatic proposal is not accepted knowledge")

	// A cosmetically altered restatement of the same lesson is refused as a
	// duplicate: a normal, non-error response, and nothing new is filed.
	dup := runLearn(t, tool,
		"the INDEX format version   must be binary-independent, so both hosts accept the same index!!")
	require.False(t, dup.IsError, "a duplicate is a normal outcome, not a failure")
	require.Contains(t, dup.Content, "already recorded")
	pending, err = prowlagent.ListProposals(t.Context(), nil, root)
	require.NoError(t, err)
	require.Len(t, pending, 1, "a duplicate lesson must not be filed again")

	// A genuinely new lesson is filed as a second pending proposal.
	second := runLearn(t, tool,
		"Retrying a failed embed in a tight loop starves the live queries that share the refresh lock.")
	require.False(t, second.IsError)
	require.Contains(t, second.Content, "Proposal")
	pending, err = prowlagent.ListProposals(t.Context(), nil, root)
	require.NoError(t, err)
	require.Len(t, pending, 2, "a genuinely new lesson is proposed")
}
