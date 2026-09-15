package agent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCompactionLeavesExactOutputAlone is the safety property: bytes the model
// feeds into a later edit, or that already ship inside a token budget, must
// survive untouched. A compressed file read becomes a corrupt edit.
func TestCompactionLeavesExactOutputAlone(t *testing.T) {
	t.Parallel()

	noisy := "line\n\n\n\n" + strings.Repeat("duplicated content line\n", 30)
	for _, tool := range []string{"view", "edit", "multiedit", "write", "patch", "prowl_agent"} {
		got, changed := compactToolOutput(tool, noisy)
		require.False(t, changed, "%s output must never be rewritten", tool)
		require.Equal(t, noisy, got)
	}
}

// TestCompactionCollapsesRepeatedLines covers the observed shape: a
// repository-wide scan returning the same matched line from dozens of files.
// Recurrence must stay visible while the repetition stops being paid for.
func TestCompactionCollapsesRepeatedLines(t *testing.T) {
	t.Parallel()

	var b strings.Builder
	for i := range 30 {
		b.WriteString("CHANGELOG.md: the limine bootloader entry was added\n")
		if i == 0 {
			b.WriteString("install.sh: limine install step\n")
		}
	}
	got, changed := compactToolOutput("bash", b.String())

	require.True(t, changed)
	require.Less(t, len(got), len(b.String())/2, "the dominant duplicate must be collapsed")
	require.Contains(t, got, "the limine bootloader entry was added",
		"the value itself must remain readable")
	require.Contains(t, got, "install.sh: limine install step",
		"unique lines must never be dropped")
	require.Contains(t, got, "duplicate line(s)", "the omission must be disclosed, not silent")
	require.Equal(t, 3, strings.Count(got, "CHANGELOG.md: the limine bootloader entry"),
		"the first few occurrences are kept so the model sees the pattern")
}

// TestCompactionStripsControlSequences removes styling that costs tokens and
// carries nothing actionable.
func TestCompactionStripsControlSequences(t *testing.T) {
	t.Parallel()

	got, changed := compactToolOutput("bash", "\x1b[31merror:\x1b[0m something broke   \n")

	require.True(t, changed)
	require.Equal(t, "error: something broke", got)
}

// TestCompactionCapsBlankRuns trims vertical padding without joining lines.
func TestCompactionCapsBlankRuns(t *testing.T) {
	t.Parallel()

	got, changed := compactToolOutput("bash", "first\n\n\n\n\nsecond\n")

	require.True(t, changed)
	require.Equal(t, "first\n\nsecond", got)
}

// TestCompactionIsIdempotent means a result can pass through the stage twice
// without degrading, which keeps retries and nested wrappers safe.
func TestCompactionIsIdempotent(t *testing.T) {
	t.Parallel()

	once, _ := compactToolOutput("bash", "a\n\n\n\nb\n"+strings.Repeat("repeated long line here\n", 20))
	twice, changed := compactToolOutput("bash", once)

	require.False(t, changed, "a compacted result must already be stable")
	require.Equal(t, once, twice)
}

// TestCompactionKeepsShortStructure protects separators and brackets, which
// legitimately repeat and are not content.
func TestCompactionKeepsShortStructure(t *testing.T) {
	t.Parallel()

	input := "{\n}\n{\n}\n{\n}\n{\n}\n{\n}\n{\n}\n"
	got, _ := compactToolOutput("bash", input)

	require.Equal(t, 6, strings.Count(got, "{"), "structural lines must survive")
	require.NotContains(t, got, "duplicate line(s)")
}

// TestCompactionLeavesCleanOutputUnchanged keeps the common case free: a
// result with nothing to trim must come back byte-identical.
func TestCompactionLeavesCleanOutputUnchanged(t *testing.T) {
	t.Parallel()

	clean := "internal/agent/agent.go:120: func run()"
	got, changed := compactToolOutput("grep", clean)

	require.False(t, changed)
	require.Equal(t, clean, got)
}
