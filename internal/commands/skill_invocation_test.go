package commands

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/neur0map/prowl/internal/skills"
)

// TestSkillReachableByBareName is the reported gap: there was no way to tell
// the agent "use skill ryoku". Every active skill must resolve from its plain
// name, not only from the qualified `user:` id.
func TestSkillReachableByBareName(t *testing.T) {
	t.Parallel()

	cmds := FromSkillCatalog([]skills.CatalogEntry{
		{ID: "prowl://skills/ryoku/SKILL.md", Name: "ryoku", Description: "Customize a Ryoku desktop."},
		{ID: "prowl://skills/jq/SKILL.md", Name: "jq", Description: "Query JSON."},
	})

	got, err := FindSlashCustom("ryoku", cmds)
	require.NoError(t, err)
	require.NotNil(t, got, "a bare skill name must resolve")
	require.NotNil(t, got.Skill)
	require.Equal(t, "ryoku", got.Skill.Name)
	require.Equal(t, "prowl://skills/ryoku/SKILL.md", got.Skill.SkillFilePath,
		"the location is what the body is later read from")
}

// TestSkillResolutionReportsAmbiguity keeps the failure mode explicit rather
// than silently picking one of two same-named skills.
func TestSkillResolutionReportsAmbiguity(t *testing.T) {
	t.Parallel()

	cmds := FromSkillCatalog([]skills.CatalogEntry{
		{ID: "a/SKILL.md", Name: "dup", Label: "user:dup"},
		{ID: "b/SKILL.md", Name: "dup", Label: "project:dup"},
	})

	_, err := FindSlashCustom("dup", cmds)
	require.Error(t, err)
	require.Contains(t, err.Error(), "ambiguous")
}

// TestUnknownSkillDoesNotResolve proves a typo is reported rather than
// silently treated as an invocation.
func TestUnknownSkillDoesNotResolve(t *testing.T) {
	t.Parallel()

	cmds := FromSkillCatalog([]skills.CatalogEntry{
		{ID: "prowl://skills/jq/SKILL.md", Name: "jq"},
	})

	got, err := FindSlashCustom("nope", cmds)
	require.NoError(t, err)
	require.Nil(t, got)
}

// TestSkillInvocationCarriesTheBody pins the other half of the bug: the
// catalog holds metadata only, so an invocation built from it alone produced a
// skill header wrapped around an empty <instructions> block.
func TestSkillInvocationCarriesTheBody(t *testing.T) {
	t.Parallel()

	metadataOnly := skills.Skill{
		Name:          "ryoku",
		Description:   "Customize a Ryoku desktop.",
		SkillFilePath: "prowl://skills/ryoku/SKILL.md",
	}
	require.NotContains(t, metadataOnly.FormatInvocation(), "Read the vault first",
		"guard premise: metadata alone carries no procedure")

	loaded := metadataOnly
	loaded.Instructions = "Read the vault first; act through commands."
	invocation := loaded.FormatInvocation()

	require.Contains(t, invocation, "Read the vault first")
	require.Contains(t, invocation, "ryoku")
	require.Contains(t, invocation, "prowl://skills/ryoku/SKILL.md")
}
