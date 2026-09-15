package prowlagent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// bundleWithSource creates a project with an initialized prowl-agent bundle so
// the review-side operations have a real inbox to act on.
func bundleWithSource(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(root, "owner.go"),
		[]byte("package example\n\nfunc Owner() bool { return true }\n"),
		0o644,
	))
	require.NoError(t, EnsureIndex(t.Context(), nil, root))
	return root
}

// proposeID files a lesson and returns its proposal ID.
func proposeID(t *testing.T, root string, p KnowledgeProposal) string {
	t.Helper()
	raw, err := ProposeKnowledge(t.Context(), nil, root, p)
	require.NoError(t, err)
	var receipt struct {
		Proposal struct {
			ID string `json:"id"`
		} `json:"proposal"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &receipt))
	require.NotEmpty(t, receipt.Proposal.ID)
	return receipt.Proposal.ID
}

// TestProposalReviewLifecycle proves the human review path a reviewer drives: a
// filed lesson shows up in the pending inbox as `proposed` and never as
// `accepted`, rejecting it clears it from the inbox without accepting anything,
// and accepting a later one moves it into recallable knowledge and out of the
// inbox.
func TestProposalReviewLifecycle(t *testing.T) {
	root := bundleWithSource(t)

	// A fresh bundle has an empty inbox, not an error.
	pending, err := ListProposals(t.Context(), nil, root)
	require.NoError(t, err)
	require.Empty(t, pending)

	rejectID := proposeID(t, root, KnowledgeProposal{
		Title: "Rejected candidate",
		Body:  "This lesson is destined for rejection during review.",
		Type:  "Claim",
	})

	pending, err = ListProposals(t.Context(), nil, root)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, rejectID, pending[0].ID)
	require.Equal(t, "proposed", pending[0].Status,
		"an automatic proposal must land as proposed, never accepted")
	require.Equal(t, "Rejected candidate", pending[0].Title,
		"the inbox carries the candidate title for review")
	require.Contains(t, pending[0].Body, "destined for rejection")

	// Rejecting removes it from the pending inbox and accepts nothing.
	require.NoError(t, RejectProposal(t.Context(), nil, root, rejectID))
	pending, err = ListProposals(t.Context(), nil, root)
	require.NoError(t, err)
	require.Empty(t, pending, "a rejected proposal leaves the review inbox")
	docs, err := ListKnowledge(t.Context(), nil, root)
	require.NoError(t, err)
	require.Empty(t, docs, "rejection must not accept knowledge")

	// A second lesson, accepted, becomes recallable and leaves the inbox.
	acceptID := proposeID(t, root, KnowledgeProposal{
		Title: "Accepted candidate",
		Body:  "This lesson is destined for acceptance during review.",
		Type:  "Decision",
	})
	require.NoError(t, AcceptProposal(t.Context(), nil, root, acceptID))

	pending, err = ListProposals(t.Context(), nil, root)
	require.NoError(t, err)
	require.Empty(t, pending, "an accepted proposal leaves the review inbox")

	docs, err = ListKnowledge(t.Context(), nil, root)
	require.NoError(t, err)
	require.Len(t, docs, 1, "acceptance moves a proposal into recallable knowledge")
	require.Equal(t, "Accepted candidate", docs[0].Title)
}

// TestProposalOperationsWithoutBundleAreTyped proves every review-side call on a
// project that has no bundle returns ErrNoBundle, so a caller distinguishes
// "nothing recorded yet" from a real failure.
func TestProposalOperationsWithoutBundleAreTyped(t *testing.T) {
	root := t.TempDir() // No .prowl bundle.

	_, err := ListProposals(t.Context(), nil, root)
	require.ErrorIs(t, err, ErrNoBundle)
	require.ErrorIs(t, AcceptProposal(t.Context(), nil, root, "abc123"), ErrNoBundle)
	require.ErrorIs(t, RejectProposal(t.Context(), nil, root, "abc123"), ErrNoBundle)
	require.ErrorIs(t, DeleteAccepted(t.Context(), nil, root, "lessons/x.md"), ErrNoBundle)
}

// TestProposalDeleteAcceptedRefusesEscapingPath proves a path that climbs out of
// the knowledge bundle is refused before any file is touched, while a legitimate
// bundle-relative delete removes the accepted document.
func TestProposalDeleteAcceptedRefusesEscapingPath(t *testing.T) {
	root := bundleWithSource(t)

	// A sentinel inside .prowl but outside the knowledge directory must survive
	// an escape attempt that resolves to it.
	sentinel := filepath.Join(root, bundleDirName, "secret.md")
	require.NoError(t, os.WriteFile(sentinel, []byte("do not delete"), 0o644))

	for _, escape := range []string{"../secret.md", "..", "../../owner.go", "/etc/hostname"} {
		err := DeleteAccepted(t.Context(), nil, root, escape)
		require.Error(t, err, "escape %q must be refused", escape)
		require.NotErrorIs(t, err, ErrNoBundle,
			"an escape is a refusal, not a missing bundle")
	}
	require.FileExists(t, sentinel, "an escaping delete must not touch files outside the bundle")

	// A real accepted document is deletable by its bundle-relative path.
	id := proposeID(t, root, KnowledgeProposal{
		Title: "Deletable lesson",
		Body:  "This accepted lesson will be removed by a reviewer.",
		Type:  "Claim",
	})
	require.NoError(t, AcceptProposal(t.Context(), nil, root, id))
	docs, err := ListKnowledge(t.Context(), nil, root)
	require.NoError(t, err)
	require.Len(t, docs, 1)

	require.NoError(t, DeleteAccepted(t.Context(), nil, root, docs[0].Path))
	docs, err = ListKnowledge(t.Context(), nil, root)
	require.NoError(t, err)
	require.Empty(t, docs, "a deleted document is no longer recalled")
}

// TestLessonMatchesExistingRejectsNearDuplicateAcceptsNew proves the exported
// deduplication comparison: a lesson that only re-cases, re-punctuates, or
// reorders words of a recorded one is caught on either its title or body, while
// a genuinely new lesson is not.
func TestLessonMatchesExistingRejectsNearDuplicateAcceptsNew(t *testing.T) {
	existing := []ExistingLesson{{
		Title: "Session costs accumulate atomically",
		Body:  "Ordinary saves must not replace accumulated session costs.",
	}}

	// Re-cased, re-punctuated, extra whitespace on the title.
	require.True(t, LessonMatchesExisting(
		"  session   COSTS accumulate, atomically!! ",
		"an unrelated body about widget layout",
		existing,
	), "a cosmetically altered title is a duplicate")

	// Reordered words still map to the same token set.
	require.True(t, LessonMatchesExisting(
		"costs session accumulate atomically",
		"",
		existing,
	), "reordered words do not evade the check")

	// The body matches even when the title differs.
	require.True(t, LessonMatchesExisting(
		"A completely different heading",
		"ordinary saves MUST NOT replace accumulated session costs",
		existing,
	), "a duplicate body is caught")

	// A genuinely new lesson shares almost no vocabulary and is allowed.
	require.False(t, LessonMatchesExisting(
		"Build needs the sqlite fts5 tag",
		"Compiling requires CGO enabled and the greenteagc experiment.",
		existing,
	), "a genuinely new lesson is not a duplicate")

	// No prior lessons means nothing can be a duplicate.
	require.False(t, LessonMatchesExisting("First lesson", "First body", nil))
}

// TestExistingLessonsGathersAcceptedAndPending proves the dedup corpus spans
// both accepted knowledge and still-pending proposals, so a lesson filed twice
// before review is caught the second time even though nothing is accepted yet.
func TestExistingLessonsGathersAcceptedAndPending(t *testing.T) {
	root := bundleWithSource(t)

	acceptID := proposeID(t, root, KnowledgeProposal{
		Title: "Accepted lesson about caching",
		Body:  "The cache key must include the index format version.",
		Type:  "Decision",
	})
	require.NoError(t, AcceptProposal(t.Context(), nil, root, acceptID))

	// A second lesson left pending in the inbox.
	_ = proposeID(t, root, KnowledgeProposal{
		Title: "Pending lesson about retries",
		Body:  "Retrying a failed embed in a tight loop starves live queries.",
		Type:  "Claim",
	})

	corpus, err := ExistingLessons(t.Context(), nil, root)
	require.NoError(t, err)
	require.Len(t, corpus, 2, "corpus spans accepted knowledge and pending proposals")

	// The accepted body is recovered (via knowledge show), so a near-duplicate
	// of it is caught.
	require.True(t, LessonMatchesExisting(
		"Cache key detail",
		"the cache key MUST include the index format version",
		corpus,
	), "a near-duplicate of an accepted lesson is caught")

	// A near-duplicate of the still-pending lesson is caught too.
	require.True(t, LessonMatchesExisting(
		"Pending lesson about retries",
		"anything",
		corpus,
	), "a near-duplicate of a pending proposal is caught")
}
