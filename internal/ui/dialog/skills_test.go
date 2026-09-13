package dialog

import (
	"fmt"
	"image"
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/neur0map/prowl/internal/ui/common"
	"github.com/neur0map/prowl/internal/ui/styles"
	"github.com/stretchr/testify/require"
)

func TestSkillsModalSelectionAndScroll(t *testing.T) {
	t.Parallel()

	sty := styles.RyokutonePantera()
	entries := make([]SkillEntry, 40)
	for i := range entries {
		entries[i] = SkillEntry{Name: fmt.Sprintf("skill-%02d", i), Path: "/tmp/skill/SKILL.md"}
	}
	d := NewSkills(&common.Common{Styles: &sty}, entries)

	scr := uv.NewScreenBuffer(80, 24)
	require.NotPanics(t, func() { d.Draw(scr, image.Rect(0, 0, 80, 24)) })
	require.Equal(t, SkillsID, d.ID())
	require.Zero(t, d.selected)
	require.Zero(t, d.offset)

	// j/k and pgup/pgdn move the selection cursor, not just the scroll offset.
	d.HandleMsg(tea.KeyPressMsg{Code: 'j', Text: "j"})
	require.Equal(t, 1, d.selected)
	d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyPgDown})
	require.Equal(t, 6, d.selected)

	// Driving the selection to the end scrolls the window to keep it visible.
	for range entries {
		d.HandleMsg(tea.KeyPressMsg{Code: 'j', Text: "j"})
	}
	require.Equal(t, len(entries)-1, d.selected)
	d.Draw(scr, image.Rect(0, 0, 80, 24))
	require.Positive(t, d.offset)

	act := d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEscape})
	require.IsType(t, ActionClose{}, act)
}

func TestSkillsModalEnterOpensSelected(t *testing.T) {
	t.Parallel()

	sty := styles.RyokutonePantera()
	entries := []SkillEntry{
		{Name: "alpha", Path: "/u/alpha/SKILL.md"},
		{Name: "zebra", Path: "prowl://skills/zebra/SKILL.md", Builtin: true},
		{Name: "beta", Path: "/u/beta/SKILL.md"},
	}
	d := NewSkills(&common.Common{Styles: &sty}, entries)

	// Builtin entries are grouped first, so the initial selection is builtin.
	require.True(t, d.entries[0].Builtin)

	// Move onto an added entry, then Enter opens exactly that file.
	d.HandleMsg(tea.KeyPressMsg{Code: 'j', Text: "j"})
	sel := d.current()
	require.NotNil(t, sel)
	require.False(t, sel.Builtin)

	act := d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	open, ok := act.(ActionOpenSkillFile)
	require.True(t, ok, "Enter must yield ActionOpenSkillFile")
	require.Equal(t, sel.Path, open.Path)
}

func TestSkillsModalAuthoringActions(t *testing.T) {
	t.Parallel()

	sty := styles.RyokutonePantera()
	entries := []SkillEntry{{Name: "alpha", Path: "/u/alpha/SKILL.md"}}
	d := NewSkills(&common.Common{Styles: &sty}, entries)

	// n enters the create sub-mode; submitting yields a create action.
	d.HandleMsg(tea.KeyPressMsg{Code: 'n', Text: "n"})
	require.Equal(t, skillsModeCreate, d.mode)
	d.input.SetValue("track flaky tests")
	act := d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	create, ok := act.(ActionAuthorSkill)
	require.True(t, ok)
	require.Equal(t, "create", create.Mode)
	require.Equal(t, "track flaky tests", create.Prompt)
	require.Empty(t, create.Name)
	require.Equal(t, skillsModeList, d.mode, "submit returns to the list")

	// e enters the improve sub-mode carrying the selected skill's identity.
	d.HandleMsg(tea.KeyPressMsg{Code: 'e', Text: "e"})
	require.Equal(t, skillsModeImprove, d.mode)
	d.input.SetValue("tighten the pitfalls")
	act = d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	improve, ok := act.(ActionAuthorSkill)
	require.True(t, ok)
	require.Equal(t, "improve", improve.Mode)
	require.Equal(t, "alpha", improve.Name)
	require.Equal(t, "/u/alpha/SKILL.md", improve.Path)
	require.Equal(t, "tighten the pitfalls", improve.Prompt)

	// Esc cancels the sub-mode back to the list without acting.
	d.HandleMsg(tea.KeyPressMsg{Code: 'n', Text: "n"})
	require.Equal(t, skillsModeCreate, d.mode)
	require.Nil(t, d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEscape}))
	require.Equal(t, skillsModeList, d.mode)
}
