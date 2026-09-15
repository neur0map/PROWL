package gateway

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestAnalyzeIntentReadsTheVerbs is the core of the lexical signal: the words,
// not the shape, decide reasoning versus mechanical intent, and word matching
// must not fire on substrings.
func TestAnalyzeIntentReadsTheVerbs(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		text           string
		wantReasoning  bool
		wantMechanical bool
	}{
		"short-hard proof":  {"Prove there is no closed form for the Collatz stopping time", true, false},
		"design intent":     {"Design a schema for multi-tenant billing", true, false},
		"long-easy extract": {"Extract the timestamps from this log", false, true},
		"translate intent":  {"Translate the following to French", false, true},
		// A plain conversational line is neither; shape decides.
		"neutral chatter": {"Can you help me with my homework", false, false},
		// Boundary: "listen" must not fire the mechanical "list" cue, and
		// "approved" must not fire the reasoning "prove" cue.
		"substring trap": {"I listen to music and approved the change", false, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sig := analyzeIntent(tc.text)
			require.Equal(t, tc.wantReasoning, sig.reasoning, "reasoning cue")
			require.Equal(t, tc.wantMechanical, sig.mechanical, "mechanical cue")
		})
	}
}

// TestAnalyzeIntentCountsRequirementsAndCode covers the secondary features the
// signal carries beyond the two verb classes.
func TestAnalyzeIntentCountsRequirementsAndCode(t *testing.T) {
	t.Parallel()

	enumerated := analyzeIntent("Do these:\n1. rename the file\n2. move it\n3. update imports")
	require.GreaterOrEqual(t, enumerated.requirements, 3, "a numbered list is several asks")

	questions := analyzeIntent("what is this? and this? and this?")
	require.GreaterOrEqual(t, questions.requirements, 3, "multiple questions are several asks")

	fenced := analyzeIntent("fix this:\n```go\nx := 1\n```")
	require.True(t, fenced.fencedCode, "a fenced block must be noticed")

	require.False(t, analyzeIntent("just a sentence").fencedCode)
}

// TestAnalyzeIntentIsBounded keeps the per-request scan flat while still
// catching the ask. A request either opens with the instruction or appends it
// after a paste, so both ends are read and only the middle bulk is skipped.
func TestAnalyzeIntentIsBounded(t *testing.T) {
	t.Parallel()

	filler := strings.Repeat("a", intentScanLimit*4)

	require.False(t, analyzeIntent(filler+"prove"+filler).reasoning,
		"the middle of a large paste is bulk and must not be scanned")
	require.True(t, analyzeIntent("prove this claim. "+filler).reasoning,
		"an instruction that opens the request must be seen")
	require.True(t, analyzeIntent(filler+" now prove this claim.").reasoning,
		"an instruction appended after a paste must be seen")
}
