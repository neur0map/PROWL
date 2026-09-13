package knowledge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// stubCodec satisfies the Codec interface for tests that never marshal or parse
// a document (recovery works on snapshots and the proposal record directly).
type stubCodec struct{}

func (stubCodec) Parse(path string, _ []byte) (*Document, error) { return &Document{Path: path}, nil }
func (stubCodec) Marshal(doc *Document) ([]byte, error)          { return []byte(doc.Path), nil }

// TestRecoverDecisionKeepsCommittedTargetAfterLaterDecision reproduces a crash
// window: a proposal's accept committed its canonical files and proposal record
// but crashed before removing its journal, and a later decision then rewrote the
// shared index/log. Recovery must reconcile the stale journal, never roll the
// already-committed target back to its pre-decision snapshot.
func TestRecoverDecisionKeepsCommittedTargetAfterLaterDecision(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	repo := NewRepository(filepath.Join(root, "knowledge"), stubCodec{})
	require.NoError(t, repo.Init())
	inbox := NewReviewInbox(filepath.Join(root, "proposals"), repo)

	const id = "prop1"
	target := "lessons/cost.md"
	targetContent := []byte("---\ntitle: Cumulative cost\n---\nCharges add to the running total.\n")
	indexA := []byte("# Knowledge\n\n- [Cumulative cost](lessons/cost.md)\n")
	logA := []byte("# Knowledge log\n\n- accepted lessons/cost.md\n")
	candidate := []byte("candidate bytes")

	// The committed on-disk state produced by a successful accept.
	require.NoError(t, os.MkdirAll(filepath.Join(repo.Root, "lessons"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo.Root, target), targetContent, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repo.Root, "index.md"), indexA, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repo.Root, "log.md"), logA, 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(inbox.Root, id), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(inbox.Root, id, "candidate.md"), candidate, 0o644))

	audit := DecisionAudit{
		SchemaVersion:   decisionAuditSchemaVersion,
		ProposalID:      id,
		Action:          DecisionAccept,
		IdempotencyKey:  "key-1",
		PrincipalID:     "tester",
		ExpectedVersion: strings.Repeat("a", 64),
		VersionBefore:   strings.Repeat("b", 64),
		VersionAfter:    strings.Repeat("c", 64),
		DecidedAt:       time.Now().UTC().Format(time.RFC3339Nano),
		Rollback:        RollbackPlan{Paths: []string{target, "index.md", "log.md"}},
	}
	proposal := Proposal{
		ID: id, Operation: "create", TargetPath: target,
		CandidatePath: id + "/candidate.md", Status: "accepted",
		Author: "test", CreatedAt: time.Now().UTC().Format(time.RFC3339),
		ReviewedAt: time.Now().UTC().Format(time.RFC3339), Decision: &audit,
	}
	require.NoError(t, inbox.writeProposal(&proposal))

	pre := persistentSnapshots([]fileSnapshot{
		{path: target, mode: 0o644, exists: false},
		{path: "index.md", data: []byte("# Knowledge\n"), mode: 0o644, exists: true},
		{path: "log.md", data: []byte("# Knowledge log\n"), mode: 0o644, exists: true},
	})
	post := persistentSnapshots([]fileSnapshot{
		{path: target, data: targetContent, mode: 0o644, exists: true},
		{path: "index.md", data: indexA, mode: 0o644, exists: true},
		{path: "log.md", data: logA, mode: 0o644, exists: true},
	})
	txn := &decisionTransaction{
		SchemaVersion: decisionTransactionSchemaVersion,
		Stage:         decisionTransactionCanonical,
		Proposal:      proposal,
		Audit:         audit,
		Candidate:     candidate,
		Snapshots:     pre,
		Results:       post,
	}
	require.NoError(t, inbox.writeDecisionTransaction(txn))

	// A later decision rewrote the shared index/log after this proposal
	// committed but before its journal was cleaned up.
	require.NoError(t, os.WriteFile(filepath.Join(repo.Root, "index.md"),
		[]byte("# Knowledge\n\n- a later decision rewrote this\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repo.Root, "log.md"),
		[]byte("# Knowledge log\n\n- later entry\n"), 0o644))

	require.NoError(t, inbox.recoverDecisionTransaction(id))

	got, err := repo.ReadBundleFile(target)
	require.NoError(t, err, "a committed target must survive recovery after a later decision")
	require.Contains(t, string(got), "Cumulative cost",
		"recovery must not roll a committed target back to its pre-decision state")
	_, statErr := os.Stat(filepath.Join(inbox.Root, id, decisionTransactionFile))
	require.True(t, os.IsNotExist(statErr), "recovery must remove the reconciled journal")
}
