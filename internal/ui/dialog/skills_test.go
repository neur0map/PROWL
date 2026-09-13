package dialog

import (
	"image"
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/neur0map/prowl/internal/ui/common"
	"github.com/neur0map/prowl/internal/ui/styles"
	"github.com/stretchr/testify/require"
)

func TestSkillsModalRendersAndScrolls(t *testing.T) {
	t.Parallel()

	sty := styles.RyokutonePantera()
	entries := make([]SkillEntry, 40)
	for i := range entries {
		entries[i] = SkillEntry{Name: "skill", Path: "/tmp/skill/SKILL.md"}
	}
	d := NewSkills(&common.Common{Styles: &sty}, entries)

	// Draw must not panic and the dialog identifies itself.
	scr := uv.NewScreenBuffer(80, 24)
	require.NotPanics(t, func() { d.Draw(scr, image.Rect(0, 0, 80, 24)) })
	require.Equal(t, SkillsID, d.ID())
	require.Zero(t, d.offset)

	// Down scrolls; a closing key returns ActionClose.
	d.HandleMsg(tea.KeyPressMsg{Code: 'j', Text: "j"})
	require.Equal(t, 1, d.offset)
	d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyPgDown})
	require.Equal(t, 6, d.offset)

	act := d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEscape})
	require.IsType(t, ActionClose{}, act)
}
