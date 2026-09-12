package reasoning

import (
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/stretchr/testify/require"
)

func TestUltrathinkProseBoundaries(t *testing.T) {
	t.Parallel()
	for _, text := range []string{
		"ultrathink about this API",
		"Please, ultrathink!",
		"“ultrathink” — compare the designs",
		"中文：ultrathink。",
		"`unclosed ultrathink",
		"<unclosed> ultrathink",
		"2 < 3; ultrathink.",
	} {
		t.Run(text, func(t *testing.T) {
			t.Parallel()
			require.True(t, HasUltrathink(text))
			spans := UltrathinkSpans(text)
			require.Equal(t, []string{Ultrathink}, matchedText(text, spans))
		})
	}
	for _, text := range []string{
		"Ultrathink",
		"ultrathinking",
		"中文ultrathink",
		"ultrathink2",
		"my_ultrathink",
		"/tmp/ultrathink",
		"C:\\ultrathink",
		"ultrathink.md",
		"ultrathink-mode",
		"foo::ultrathink",
		"ultrathink()",
		"`ultrathink`",
		"``example ` ultrathink``",
		"```go\nultrathink\n```",
		"~~~text\nultrathink\n~~~~",
		"```\nunclosed ultrathink",
		"<!-- ultrathink -->",
		"<!-- unclosed ultrathink",
		"<tag value='ultrathink' />",
		"<tag value='>'>ultrathink</tag>",
		"<tag><TAG>ultrathink</TAG></tag>",
	} {
		t.Run(text, func(t *testing.T) {
			t.Parallel()
			require.False(t, HasUltrathink(text))
			require.Empty(t, UltrathinkSpans(text))
		})
	}
}

func TestUltrathinkSpansPreserveOriginalOffsets(t *testing.T) {
	t.Parallel()
	text := "λ `ultrathink` then ultrathink.\n<tag>ultrathink</tag>\n```\nultrathink\n```\n再：ultrathink!"
	spans := UltrathinkSpans(text)
	require.Equal(t, []string{Ultrathink, Ultrathink}, matchedText(text, spans))
	require.Equal(t, "ultrathink.\n<tag>", text[spans[0].Start:spans[0].End+7])
	require.Equal(t, "!", text[spans[1].End:])
}

func matchedText(text string, spans []Span) []string {
	var matches []string
	for _, span := range spans {
		matches = append(matches, text[span.Start:span.End])
	}
	return matches
}

func TestClampEffortUsesSupportedLadder(t *testing.T) {
	t.Parallel()
	model := catwalk.Model{CanReason: true, ReasoningLevels: []string{"xhigh", "low", "high"}}
	require.Equal(t, []string{"low", "high", "xhigh"}, Efforts(model))
	require.Equal(t, "high", ClampEffort(model, "medium"))
	require.Equal(t, "xhigh", ClampEffort(model, "max"))
	require.Equal(t, "xhigh", HighestEffort(model))
	model.DefaultReasoningEffort = "low"
	require.Equal(t, "low", ClampEffort(model, "invalid"))
	model.CanReason = false
	require.Empty(t, ClampEffort(model, "high"))
	require.Empty(t, HighestEffort(model))
}

func TestClampEffortMapsBooleanThinking(t *testing.T) {
	t.Parallel()
	model := catwalk.Model{CanReason: true}
	require.Equal(t, "off", ClampEffort(model, "low"))
	require.Equal(t, "on", ClampEffort(model, "medium"))
	require.Equal(t, "on", HighestEffort(model))
}

func TestMandatoryThinkingNeverOffersOff(t *testing.T) {
	t.Parallel()
	model := catwalk.Model{ID: "google/gemini-2.5-pro-preview-06-05", CanReason: true}
	require.Equal(t, []string{"on"}, Efforts(model))
	require.Equal(t, "on", ClampEffort(model, "off"))
	require.Equal(t, "on", ClampEffort(model, "low"))
	model.ID = "gemini-2.5-flash"
	require.Equal(t, "off", ClampEffort(model, "low"))
}
