package tools

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestKnowledgeReadsAreRecallOnly is a safety property, not plumbing: the
// knowledge command group mixes reads with writes, and the review workflow
// only means something if the agent cannot approve its own proposal. Recall
// must be reachable; accept, reject, propose, init, and export must not be.
func TestKnowledgeReadsAreRecallOnly(t *testing.T) {
	t.Parallel()

	require.Contains(t, prowlAgentReadOnlyCommands, "knowledge",
		"the agent must be able to recall accepted knowledge")

	for _, read := range []string{"list", "show", "lint"} {
		require.Contains(t, prowlAgentKnowledgeReads, read)
	}
	for _, write := range []string{"accept", "reject", "propose", "init", "export"} {
		require.False(t, slices.Contains(prowlAgentKnowledgeReads, write),
			"%q mutates the bundle or the review inbox and must stay out of the agent's reach", write)
	}
}

// TestReadOnlyCommandsExcludeMutators guards the wider allowlist against a
// mutating subcommand being added by habit.
func TestReadOnlyCommandsExcludeMutators(t *testing.T) {
	t.Parallel()

	for _, mutator := range []string{"init", "restart", "update", "skills", "graph", "explore", "docs"} {
		require.False(t, slices.Contains(prowlAgentReadOnlyCommands, mutator),
			"%q can reindex, write files, or reach the network", mutator)
	}
}
