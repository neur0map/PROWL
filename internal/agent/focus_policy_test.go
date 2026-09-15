package agent

import (
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"

	"github.com/neur0map/prowl/internal/message"
)

// userTurn builds a persisted user message carrying the focus settings the
// agent would have stamped on it.
func userTurn(text, mode, instructions string) message.Message {
	return message.Message{
		Role: message.User,
		Parts: []message.ContentPart{
			message.TextContent{Text: text},
			message.TurnSettings{FocusMode: mode, Instructions: instructions},
		},
	}
}

func assistantTurn(text string) message.Message {
	return message.Message{
		Role:  message.Assistant,
		Parts: []message.ContentPart{message.TextContent{Text: text}},
	}
}

// requestText flattens every user-visible text part the model would receive.
func requestText(t *testing.T, msgs []message.Message) string {
	t.Helper()
	a := &sessionAgent{}
	var out string
	for _, m := range a.preparePrompt(msgs, true) {
		for _, part := range m.Content {
			if text, ok := fantasy.AsMessagePart[fantasy.TextPart](part); ok {
				out += text.Text + "\n"
			}
		}
	}
	return out
}

// TestFocusOffRemovesTheOnPolicy is the bug the user reported: focus turned
// on but never off. The "on" block declares it holds "until the user turns it
// off" and stays in the replayed transcript forever, so a later one-line
// "off" competed with it instead of replacing it.
func TestFocusOffRemovesTheOnPolicy(t *testing.T) {
	t.Parallel()

	history := []message.Message{
		userTurn("first", "on", focusOnInstructions),
		assistantTurn("ok"),
		userTurn("second", "on", ""),
		assistantTurn("ok"),
		userTurn("third", "off", focusOffInstructions),
	}

	got := requestText(t, history)

	require.NotContains(t, got, focusOnInstructions,
		"the stale on-policy must not be replayed after focus is turned off")
	require.NotContains(t, got, focusOffInstructions,
		"with the on-policy gone there is nothing to rebut, so the off line is noise")
	for _, turn := range []string{"first", "second", "third"} {
		require.Contains(t, got, turn, "user prose must survive untouched")
	}
}

// TestFocusOnPolicyStaysLiveWhileOn guards the other direction: the fix must
// not stop focus from working. The policy is stamped only on the turn where
// the mode changed, so it has to keep being replayed on later turns.
func TestFocusOnPolicyStaysLiveWhileOn(t *testing.T) {
	t.Parallel()

	history := []message.Message{
		userTurn("first", "on", focusOnInstructions),
		assistantTurn("ok"),
		userTurn("second", "on", ""),
		assistantTurn("ok"),
		userTurn("third", "on", ""),
	}

	require.Contains(t, requestText(t, history), focusOnInstructions,
		"focus is still on, so its policy must reach the model on every turn")
}

// TestFocusReEnabledAfterOff covers on -> off -> on, where two stale blocks
// exist and only the newest may survive.
func TestFocusReEnabledAfterOff(t *testing.T) {
	t.Parallel()

	history := []message.Message{
		userTurn("first", "on", focusOnInstructions),
		userTurn("second", "off", focusOffInstructions),
		userTurn("third", "on", focusOnInstructions),
	}

	got := requestText(t, history)

	require.Contains(t, got, focusOnInstructions, "the newest transition is the live policy")
	require.NotContains(t, got, focusOffInstructions, "the superseded off line must be dropped")
	// The on-policy must appear exactly once even though two turns carry it.
	require.Equal(t, 1, countOccurrences(got, focusOnInstructions),
		"a repeated policy wastes tokens and reads as emphasis")
}

// TestFocusNeverUsedLeavesPromptClean proves the default path is untouched.
func TestFocusNeverUsedLeavesPromptClean(t *testing.T) {
	t.Parallel()

	history := []message.Message{
		userTurn("first", "", ""),
		assistantTurn("ok"),
		userTurn("second", "", ""),
	}

	got := requestText(t, history)
	require.NotContains(t, got, focusOnInstructions)
	require.NotContains(t, got, focusOffInstructions)
	require.Contains(t, got, "first")
	require.Contains(t, got, "second")
}

// TestFocusStripPreservesReasoningSettings checks that dropping the replayed
// prose does not drop the model/effort recorded on the same settings part,
// which the reasoning-checkpoint pass depends on.
func TestFocusStripPreservesReasoningSettings(t *testing.T) {
	t.Parallel()

	stale := message.Message{
		Role: message.User,
		Parts: []message.ContentPart{
			message.TextContent{Text: "first"},
			message.TurnSettings{
				FocusMode:       "on",
				Instructions:    focusOnInstructions,
				Provider:        "hyper",
				Model:           "deepseek-v4",
				ReasoningEffort: "high",
			},
		},
	}

	stripped := withoutFocusInstructions(stale)
	got := stripped.TurnSettings()

	require.Empty(t, got.Instructions, "only the replayed prose is dropped")
	require.Equal(t, "on", got.FocusMode)
	require.Equal(t, "hyper", got.Provider)
	require.Equal(t, "deepseek-v4", got.Model)
	require.Equal(t, "high", got.ReasoningEffort)

	unchanged := stale.TurnSettings()
	require.Equal(t, focusOnInstructions, unchanged.Instructions,
		"the stored message must not be mutated")
}

func countOccurrences(haystack, needle string) int {
	if needle == "" {
		return 0
	}
	count, from := 0, 0
	for {
		idx := indexFrom(haystack, needle, from)
		if idx < 0 {
			return count
		}
		count++
		from = idx + len(needle)
	}
}

func indexFrom(haystack, needle string, from int) int {
	if from >= len(haystack) {
		return -1
	}
	rest := haystack[from:]
	for i := 0; i+len(needle) <= len(rest); i++ {
		if rest[i:i+len(needle)] == needle {
			return from + i
		}
	}
	return -1
}
