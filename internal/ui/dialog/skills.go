package dialog

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/neur0map/prowl/internal/ui/common"
)

const SkillsID = "skills"

// SkillEntry is one row in the Skills modal: a discovered skill and where it
// lives on disk (or the embedded builtin path).
type SkillEntry struct {
	Name    string
	Path    string
	Errored bool
}

// Skills is a read-only modal listing every active skill, sorted by name, with
// its location. The landing card only summarizes skills; this is the full
// browser, opened with Ctrl+K.
type Skills struct {
	com     *common.Common
	entries []SkillEntry
	offset  int
}

// NewSkills creates the Skills browser modal from pre-sorted entries.
func NewSkills(com *common.Common, entries []SkillEntry) *Skills {
	return &Skills{com: com, entries: entries}
}

// ID implements [Dialog].
func (d *Skills) ID() string { return SkillsID }

// HandleMsg implements [Dialog].
func (d *Skills) HandleMsg(msg tea.Msg) Action {
	if msg, ok := msg.(tea.KeyPressMsg); ok {
		if key.Matches(msg, CloseKey) {
			return ActionClose{}
		}
		switch msg.String() {
		case "up", "ctrl+p", "k":
			d.offset = max(0, d.offset-1)
		case "down", "ctrl+n", "j":
			d.offset++
		case "pgup":
			d.offset = max(0, d.offset-5)
		case "pgdown":
			d.offset += 5
		}
	}
	return nil
}

// Draw implements [Dialog].
func (d *Skills) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	t := d.com.Styles
	frame := t.Dialog.View
	width := max(0, min(84, area.Dx()))
	inner := max(1, width-frame.GetHorizontalFrameSize())
	height := max(0, area.Dy()-frame.GetVerticalFrameSize())
	if height == 0 || width == 0 {
		return nil
	}

	var rows []string
	if len(d.entries) == 0 {
		rows = []string{t.Resource.AdditionalText.Render("No skills discovered")}
	} else {
		rows = make([]string, 0, len(d.entries))
		for _, e := range d.entries {
			icon := t.Resource.OnlineIcon.String()
			if e.Errored {
				icon = t.Resource.ErrorIcon.String()
			}
			loc := e.Path
			if loc == "" {
				loc = "(embedded)"
			}
			row := fmt.Sprintf("%s %s  %s",
				icon,
				t.Resource.Name.Render(e.Name),
				t.Resource.AdditionalText.Render(loc),
			)
			rows = append(rows, ansi.Truncate(row, inner, "…"))
		}
	}

	// Reserve one line each for the title and the footer hint.
	listHeight := min(len(rows), max(1, height-2))
	d.offset = min(d.offset, max(0, len(rows)-listHeight))
	visible := rows[d.offset:min(len(rows), d.offset+listHeight)]

	lines := []string{common.DialogTitle(
		t,
		fmt.Sprintf("Skills (%d)", len(d.entries)),
		inner,
		t.Dialog.TitleGradFromColor,
		t.Dialog.TitleGradToColor,
	)}
	lines = append(lines, visible...)
	lines = append(lines, ansi.Truncate("↑/↓ scroll  esc close", inner, "…"))

	DrawCenter(scr, area, frame.Width(width).Render(strings.Join(lines, "\n")))
	return nil
}
