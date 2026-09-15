package agent

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/neur0map/prowl/internal/skills"
)

func qmlSkill() []*skills.Skill {
	return []*skills.Skill{{
		Name:          "quickshell-bar-widget",
		Globs:         []string{"**/*.qml"},
		SkillFilePath: "/home/u/.config/prowl/skills/quickshell-bar-widget/SKILL.md",
	}}
}

// TestSkillAdvisorClaimsEditedFile is the gap this closes: a skill's globs
// were parsed and advertised but read by nothing, so a model editing exactly
// the files a skill exists to explain was never pointed at it.
func TestSkillAdvisorClaimsEditedFile(t *testing.T) {
	t.Parallel()

	a := newSkillAdvisor(qmlSkill())
	require.NotNil(t, a)

	hint := a.advise("edit", `{"file_path":"/tmp/probe/shell.qml"}`)
	require.Contains(t, hint, "quickshell-bar-widget")
	require.Contains(t, hint, "/home/u/.config/prowl/skills/quickshell-bar-widget/SKILL.md",
		"the reminder must carry the exact location, since the model loads it by path")

	// Once said, it stays said: repeating it on every subsequent edit would
	// be noise the model learns to skip.
	require.Empty(t, a.advise("edit", `{"file_path":"/tmp/probe/other.qml"}`))
}

// TestSkillAdvisorStaysQuietWhenAlreadyLoaded keeps the reminder from talking
// over a model that did the right thing.
func TestSkillAdvisorStaysQuietWhenAlreadyLoaded(t *testing.T) {
	t.Parallel()

	a := newSkillAdvisor(qmlSkill())
	require.Empty(t, a.advise("view",
		`{"file_path":"/home/u/.config/prowl/skills/quickshell-bar-widget/SKILL.md"}`),
		"reading the skill is not a reason to be told to read the skill")
	require.Empty(t, a.advise("edit", `{"file_path":"/tmp/probe/shell.qml"}`),
		"a loaded skill must not be advertised again")
}

// TestSkillAdvisorIgnoresUnclaimedWork stops the reminder firing on files no
// skill claims, and on calls where a path is incidental rather than the thing
// being worked on.
func TestSkillAdvisorIgnoresUnclaimedWork(t *testing.T) {
	t.Parallel()

	a := newSkillAdvisor(qmlSkill())
	require.Empty(t, a.advise("edit", `{"file_path":"/tmp/probe/main.go"}`))
	require.Empty(t, a.advise("grep", `{"pattern":"PanelWindow","path":"/tmp/probe/shell.qml"}`),
		"searching for a term is not working on the file")
	require.Empty(t, a.advise("bash", `{"command":"cat shell.qml"}`))
}

// TestSkillAdvisorSkipsSkillsThatMakeNoClaim leaves description-matched skills
// to the prompt: inventing a claim for them would fire on everything.
func TestSkillAdvisorSkipsSkillsThatMakeNoClaim(t *testing.T) {
	t.Parallel()

	require.Nil(t, newSkillAdvisor([]*skills.Skill{{
		Name: "prose-only", SkillFilePath: "/x/SKILL.md",
	}}))
	require.Nil(t, newSkillAdvisor([]*skills.Skill{{
		Name: "opted-out", Globs: []string{"*.qml"},
		DisableModelInvocation: true, SkillFilePath: "/x/SKILL.md",
	}}))
}

// TestSkillAdvisorGlobForms covers the patterns an author actually writes.
func TestSkillAdvisorGlobForms(t *testing.T) {
	t.Parallel()

	for _, glob := range []string{"**/*.qml", "*.qml"} {
		a := newSkillAdvisor([]*skills.Skill{{
			Name: "s", Globs: []string{glob}, SkillFilePath: "/x/SKILL.md",
		}})
		require.NotEmpty(t, a.advise("write", `{"file_path":"/deep/nested/shell.qml"}`),
			"glob %q must claim a nested file", glob)
	}
}
