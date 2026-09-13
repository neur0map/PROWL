package context

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPacketBudgetIncludesSerializedEvidenceAndMetadata(t *testing.T) {
	t.Parallel()
	var candidates []Candidate
	for i := range 8 {
		path := fmt.Sprintf("internal/components/component%d.go", i)
		text := strings.Repeat("quoted \"évidence\" <&>\n", 4)
		candidate := sourceCandidate(path, 10, 14, text, text, []string{"evidence"}, "source match")
		candidate.LexicalScore = float64(100 - i)
		candidates = append(candidates, candidate)
	}
	packet, err := Pack(Request{Question: "evidence", Mode: ModeCompact, BudgetTokens: 500, BudgetBytes: 2000}, candidates, nil)
	require.NoError(t, err)
	encoded, err := json.Marshal(packet)
	require.NoError(t, err)
	require.LessOrEqual(t, len(encoded), 2000)
	require.LessOrEqual(t, ByteQuarterEstimator{}.Tokens(string(encoded)), 500)
	require.Equal(t, len(encoded), packet.Budget.ExactBytes)
	require.Equal(t, ByteQuarterEstimator{}.Tokens(string(encoded)), packet.Budget.EstimatedTokens)
	require.NotEmpty(t, packet.Items)
	require.Equal(t, candidates[0].ID, packet.Items[0].ID, "fitting the budget must preserve the best evidence")
	require.Positive(t, packet.Omitted["budget"])
}

func TestSourceOwnershipBeatsDenseGeneratedVocabulary(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		question string
		want     string
	}{
		{"How are system prompts assembled?", "internal/prompt/prompt.go"},
		{"Where is the tokenizer vocabulary?", "models/tokenizer.json"},
	} {
		terms := queryTerms(scenario.question)
		candidates := []Candidate{
			sourceCandidate("models/tokenizer.json", 1, 1, "system prompts assembled tokenizer vocabulary", strings.Repeat("system prompts assembled tokenizer vocabulary ", 20), terms, "source match"),
			sourceCandidate("internal/prompt/prompt.go", 1, 8, "Build renders a prompt.", "func Build() { renderPrompt() }", terms, "source match"),
		}
		applyPathMatch(candidates, scenario.question)
		packet, err := Pack(Request{Question: scenario.question, Mode: ModeCompact, BudgetTokens: 500}, candidates, nil)
		require.NoError(t, err)
		require.NotEmpty(t, packet.Items)
		require.Equal(t, scenario.want, packet.Items[0].Citations[0].Path, scenario.question)
	}
}
