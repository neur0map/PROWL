package prowlagent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestKnowledgeRoundTripsThroughTheEmbeddedEngine closes the memory loop end
// to end: a lesson is proposed, a human accepts it, and a later session can
// read it back. Before this the loop was write-only — the learn tool filed
// proposals and nothing ever recalled accepted knowledge, so a durable
// decision never reached another session.
//
// It runs against the in-process engine rather than the standalone CLI,
// because that is the path a Prowl session actually uses.
func TestKnowledgeRoundTripsThroughTheEmbeddedEngine(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(root, "owner.go"),
		[]byte("package example\n\nfunc Indexed() bool { return true }\n"),
		0o644,
	))
	require.NoError(t, EnsureIndex(t.Context(), nil, root))

	// A project with a bundle but nothing accepted recalls nothing.
	docs, err := ListKnowledge(t.Context(), nil, root)
	require.NoError(t, err)
	require.Empty(t, docs, "no accepted knowledge means no memory block")

	raw, err := ProposeKnowledge(t.Context(), nil, root, KnowledgeProposal{
		Title: "Index identity is a constant",
		Body:  "The index version must not derive from the running binary.",
		Type:  "Decision",
		Tags:  []string{"decision"},
	})
	require.NoError(t, err)

	var receipt struct {
		Proposal struct {
			ID         string `json:"id"`
			Status     string `json:"status"`
			TargetPath string `json:"target_path"`
		} `json:"proposal"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &receipt))
	require.NotEmpty(t, receipt.Proposal.ID)
	require.Equal(t, "proposed", receipt.Proposal.Status,
		"a proposal is not an accepted instruction until a human reviews it")

	// A proposal alone must not be recalled: that is the whole point of the
	// review step.
	docs, err = ListKnowledge(t.Context(), nil, root)
	require.NoError(t, err)
	require.Empty(t, docs, "an unreviewed proposal must not enter memory")

	_, stderr, err := Run(t.Context(), nil, root, "knowledge", "accept", receipt.Proposal.ID, "--json")
	require.NoError(t, err, stderr)

	docs, err = ListKnowledge(t.Context(), nil, root)
	require.NoError(t, err)
	require.Len(t, docs, 1, "accepted knowledge must be recallable")
	require.Equal(t, "Index identity is a constant", docs[0].Title)
	require.Equal(t, "Decision", docs[0].Type)
	require.Equal(t, receipt.Proposal.TargetPath, docs[0].Path,
		"the recalled path is what the agent reads the full document with")
}

// TestListKnowledgeOnProjectWithoutBundle covers the common case: most
// projects have no knowledge bundle at all, and that must yield no memory
// rather than an error that breaks prompt construction.
func TestListKnowledgeOnProjectWithoutBundle(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(root, "main.go"),
		[]byte("package main\n\nfunc main() {}\n"),
		0o644,
	))

	docs, err := ListKnowledge(t.Context(), nil, root)
	require.NoError(t, err, "a missing bundle is not a failure")
	require.Empty(t, docs)
}
