package skills

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildCreatePromptStandards(t *testing.T) {
	t.Parallel()

	p := BuildCreatePrompt("summarize go test failures")
	require.NotEmpty(t, p)
	require.Contains(t, p, "SKILL.md")
	require.Contains(t, p, "summarize go test failures")
	for _, section := range []string{"When to Use", "Procedure", "Pitfalls", "Verification"} {
		require.Contains(t, p, section)
	}
	require.Contains(t, p, "no invented commands")
	require.Contains(t, p, "managed-skills")
}

func TestBuildImprovePromptStandards(t *testing.T) {
	t.Parallel()

	current := "---\nname: foo\ndescription: bar\n---\n## When to Use\nUse it.\n"
	p := BuildImprovePrompt("foo", "/x/foo/SKILL.md", current, "tighten the pitfalls")
	require.NotEmpty(t, p)
	require.Contains(t, p, "SKILL.md")
	require.Contains(t, p, "/x/foo/SKILL.md")
	require.Contains(t, p, "tighten the pitfalls")
	// The current content is embedded inline for context.
	require.Contains(t, p, current)
	for _, section := range []string{"When to Use", "Procedure", "Pitfalls", "Verification"} {
		require.Contains(t, p, section)
	}
	require.Contains(t, p, "no invented commands")
}
