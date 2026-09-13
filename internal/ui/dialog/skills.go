package dialog

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/neur0map/prowl/internal/ui/common"
)

const SkillsID = "skills"

// skillsPageStep is how many rows pgup/pgdn move the selection.
const skillsPageStep = 5

// skillsMode is the Skills modal's current interaction mode: browsing the
// list, or typing an authoring instruction.
type skillsMode int

const (
	skillsModeList skillsMode = iota
	skillsModeCreate
	skillsModeImprove
)

// SkillEntry is one row in the Skills modal: a discovered skill and where it
// lives on disk (or the embedded builtin path). Builtin marks skills that ship
// with prowl, so the modal can group them apart from user-added skills.
type SkillEntry struct {
	Name    string
	Path    string
	Errored bool
	Builtin bool
}

// Skills is a modal listing every active skill, grouped into Builtin and Added
// sections, with a single flat selection cursor. Enter opens the selected
// skill in the external editor; e/n start an inline authoring prompt. The
// landing card only summarizes skills; this is the full browser (Ctrl+K).
type Skills struct {
	com      *common.Common
	entries  []SkillEntry
	nBuiltin int
	selected int
	offset   int

	mode   skillsMode
	prompt string
	input  textinput.Model
}

// NewSkills creates the Skills browser modal. Entries arrive name-sorted; they
// are re-grouped into builtin-first, added-second order so the flat selection
// index lines up with the two rendered sections.
func NewSkills(com *common.Common, entries []SkillEntry) *Skills {
	builtin := make([]SkillEntry, 0, len(entries))
	added := make([]SkillEntry, 0, len(entries))
	for _, e := range entries {
		if e.Builtin {
			builtin = append(builtin, e)
		} else {
			added = append(added, e)
		}
	}
	ordered := append(builtin, added...)

	input := textinput.New()
	input.Placeholder = "Describe the change"
	if com != nil && com.Styles != nil {
		input.SetStyles(com.Styles.TextInput)
	}

	return &Skills{
		com:      com,
		entries:  ordered,
		nBuiltin: len(builtin),
		input:    input,
	}
}

// ID implements [Dialog].
func (d *Skills) ID() string { return SkillsID }

// HandleMsg implements [Dialog].
func (d *Skills) HandleMsg(msg tea.Msg) Action {
	if d.mode != skillsModeList {
		return d.handleInput(msg)
	}

	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	if matchesClose(k) {
		return ActionClose{}
	}
	switch k.String() {
	case "up", "ctrl+p", "k":
		d.moveSelection(-1)
	case "down", "ctrl+n", "j":
		d.moveSelection(1)
	case "pgup":
		d.moveSelection(-skillsPageStep)
	case "pgdown":
		d.moveSelection(skillsPageStep)
	case "enter":
		if e := d.current(); e != nil {
			return ActionOpenSkillFile{Path: e.Path}
		}
	case "e":
		if d.current() != nil {
			return ActionCmd{Cmd: d.enterInput(skillsModeImprove)}
		}
	case "n":
		return ActionCmd{Cmd: d.enterInput(skillsModeCreate)}
	}
	return nil
}

// handleInput drives the inline authoring text-input sub-mode.
func (d *Skills) handleInput(msg tea.Msg) Action {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch {
		case matchesClose(k):
			d.exitInput()
			return nil
		case k.String() == "enter":
			return d.submitInput()
		}
	}
	var cmd tea.Cmd
	d.input, cmd = d.input.Update(msg)
	return ActionCmd{Cmd: cmd}
}

// submitInput turns the typed instruction into an authoring action, or does
// nothing when the field is empty.
func (d *Skills) submitInput() Action {
	text := strings.TrimSpace(d.input.Value())
	if text == "" {
		return nil
	}
	mode := d.mode
	entry := d.current()
	d.exitInput()
	if mode == skillsModeCreate {
		return ActionAuthorSkill{Mode: "create", Prompt: text}
	}
	if entry == nil {
		return nil
	}
	return ActionAuthorSkill{
		Mode:   "improve",
		Name:   entry.Name,
		Path:   entry.Path,
		Prompt: text,
	}
}

// enterInput switches into an authoring sub-mode and focuses the text input.
func (d *Skills) enterInput(mode skillsMode) tea.Cmd {
	d.mode = mode
	d.input.Reset()
	switch mode {
	case skillsModeImprove:
		name := ""
		if e := d.current(); e != nil {
			name = e.Name
		}
		d.prompt = fmt.Sprintf("How should I improve %s?", name)
	default:
		d.prompt = "What should the new skill do?"
	}
	return d.input.Focus()
}

// exitInput returns to the list, discarding any typed text.
func (d *Skills) exitInput() {
	d.mode = skillsModeList
	d.input.Blur()
	d.input.Reset()
}

// moveSelection clamps the selection cursor within the entry list.
func (d *Skills) moveSelection(delta int) {
	if len(d.entries) == 0 {
		return
	}
	d.selected = max(0, min(d.selected+delta, len(d.entries)-1))
}

// current returns the selected entry, or nil when the list is empty.
func (d *Skills) current() *SkillEntry {
	if d.selected < 0 || d.selected >= len(d.entries) {
		return nil
	}
	return &d.entries[d.selected]
}

// matchesClose reports whether the key press closes/cancels the dialog.
func matchesClose(msg tea.KeyPressMsg) bool {
	return key.Matches(msg, CloseKey)
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

	title := common.DialogTitle(
		t,
		fmt.Sprintf("Skills (%d)", len(d.entries)),
		inner,
		t.Dialog.TitleGradFromColor,
		t.Dialog.TitleGradToColor,
	)

	if d.mode != skillsModeList {
		return d.drawInput(scr, area, frame, width, inner, title)
	}

	var lines []string
	if len(d.entries) == 0 {
		lines = []string{t.Resource.AdditionalText.Render("No skills discovered")}
	} else {
		rows, selLine := d.buildRows(inner)
		listHeight := min(len(rows), max(1, height-2))
		if selLine >= 0 {
			if selLine < d.offset {
				d.offset = selLine
			} else if selLine >= d.offset+listHeight {
				d.offset = selLine - listHeight + 1
			}
		}
		d.offset = max(0, min(d.offset, len(rows)-listHeight))
		lines = rows[d.offset:min(len(rows), d.offset+listHeight)]
	}

	out := []string{title}
	out = append(out, lines...)
	out = append(out, ansi.Truncate(
		"↑/↓ select · enter open · e improve · n new · esc close", inner, "…"),
	)

	DrawCenter(scr, area, frame.Width(width).Render(strings.Join(out, "\n")))
	return nil
}

// buildRows renders the two labeled sections into a flat display slice and
// reports the display index of the currently selected entry (-1 if none).
func (d *Skills) buildRows(inner int) (lines []string, selLine int) {
	t := d.com.Styles
	selLine = -1

	renderEntry := func(e SkillEntry, idx int) string {
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
		row = ansi.Truncate(row, max(1, inner-2), "…")
		if idx == d.selected {
			return t.Dialog.SelectedItem.Render(row)
		}
		return t.Dialog.NormalItem.Render(row)
	}

	addSection := func(label string, start, end int) {
		if end <= start {
			return
		}
		lines = append(lines, t.Resource.Heading.Render(label))
		for i := start; i < end; i++ {
			if i == d.selected {
				selLine = len(lines)
			}
			lines = append(lines, renderEntry(d.entries[i], i))
		}
	}

	addSection("Builtin", 0, d.nBuiltin)
	addSection("Added", d.nBuiltin, len(d.entries))
	return lines, selLine
}

// drawInput renders the inline authoring prompt and its text field.
func (d *Skills) drawInput(scr uv.Screen, area uv.Rectangle, frame lipgloss.Style, width, inner int, title string) *tea.Cursor {
	t := d.com.Styles
	d.input.SetWidth(max(1, inner-2))

	out := strings.Join([]string{
		title,
		t.Resource.Name.Render(ansi.Truncate(d.prompt, inner, "…")),
		t.Dialog.InputPrompt.Render(d.input.View()),
		ansi.Truncate("enter submit · esc cancel", inner, "…"),
	}, "\n")

	cur := InputCursor(t, d.input.Cursor())
	DrawCenterCursor(scr, area, frame.Width(width).Render(out), cur)
	return cur
}

var _ Dialog = (*Skills)(nil)
