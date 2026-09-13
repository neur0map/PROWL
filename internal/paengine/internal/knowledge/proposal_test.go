package knowledge_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/neur0map/prowl/internal/paengine/internal/knowledge"
	"github.com/neur0map/prowl/internal/paengine/internal/knowledge/okfv01"
	"github.com/neur0map/prowl/internal/paengine/internal/parse/extract"
)

func TestProposalRejectsUnresolvedAndStaleEvidence(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	source := filepath.Join(root, "ledger.go")
	require.NoError(t, os.WriteFile(source, []byte("package ledger\n\nfunc AddCost(total, charge float64) float64 { return total + charge }\n"), 0o600))
	repo := knowledge.NewRepository(filepath.Join(root, "knowledge"), okfv01.Codec{})
	require.NoError(t, repo.Init())
	inbox := knowledge.NewReviewInbox(filepath.Join(root, "proposals"), repo)
	candidatePath := filepath.Join(root, "candidate.md")
	candidate := func(symbol string) {
		t.Helper()
		data, err := okfv01.BuildCandidate(okfv01.CaptureInput{
			Type: "Claim", Title: "Cumulative cost", Body: "Charges add to the running total.",
			Anchors: []knowledge.Anchor{{Path: "ledger.go", Symbol: symbol}},
		})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(candidatePath, data, 0o600))
	}

	candidate("DoesNotExist")
	_, _, err := inbox.Propose(candidatePath, "lessons/cost.md", "test", root, extract.SymbolRange, time.Now())
	require.Error(t, err)
	pending, err := inbox.List()
	require.NoError(t, err)
	require.Empty(t, pending)

	candidate("AddCost")
	valid, _, err := inbox.Propose(candidatePath, "lessons/cost.md", "test", root, extract.SymbolRange, time.Now())
	require.NoError(t, err)
	captured := filepath.Join(inbox.Root, filepath.FromSlash(valid.CandidatePath))
	require.NoError(t, os.WriteFile(source, []byte("package ledger\n\nfunc AddCost(total, charge float64) float64 { return charge }\n"), 0o600))
	_, _, err = inbox.Propose(captured, "lessons/cost.md", "test", root, extract.SymbolRange, time.Now())
	require.Error(t, err)
	pending, err = inbox.List()
	require.NoError(t, err)
	require.Len(t, pending, 1)
	_, err = os.Stat(filepath.Join(repo.Root, "lessons", "cost.md"))
	require.True(t, os.IsNotExist(err), "proposal validation must never install accepted knowledge")
}
