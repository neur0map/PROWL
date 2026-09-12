package model

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	uistyles "github.com/neur0map/prowl/internal/ui/styles"
	"github.com/stretchr/testify/require"
)

// TestBorderedCardLinesUniformWidth is the alignment contract: every line the
// card emits — top edge with the embedded title, each body row, the bottom
// edge — must span exactly the requested width, so the right border never
// frays. It exercises unicode in the title and over/under-width body lines.
func TestBorderedCardLinesUniformWidth(t *testing.T) {
	t.Parallel()

	st := uistyles.RyokutonePantera()
	body := lipgloss.JoinVertical(lipgloss.Left,
		"◇ Poolside: Laguna S 2.1 (free) via OpenRouter",
		"",
		"● code-search",
		"a line quite a bit longer than the card so it must be truncated to fit",
	)

	for _, w := range []int{24, 48, 92} {
		card := borderedCard(&st, "~/Work/日本語-Query", body, w)
		lines := strings.Split(card, "\n")
		require.Greater(t, len(lines), 2)
		for i, ln := range lines {
			require.Equalf(t, w, lipgloss.Width(ln),
				"width %d: line %d must span the full card: %q", w, i, ln)
		}
	}
}

func TestPadLineWidth(t *testing.T) {
	t.Parallel()

	require.Equal(t, 10, lipgloss.Width(padLine("abc", 10)))
	require.LessOrEqual(t, lipgloss.Width(padLine("abcdefghij", 4)), 4)
	// Wide runes (2 cols each) are counted correctly, not padded past width.
	require.Equal(t, 6, lipgloss.Width(padLine("日本語", 6)))
}
