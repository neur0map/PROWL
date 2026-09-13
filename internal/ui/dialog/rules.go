package dialog

import (
	"fmt"
	"path/filepath"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/neur0map/prowl/internal/rules"
	"github.com/neur0map/prowl/internal/ui/common"
)

const RulesID = "rules"

// rulesPageStep is how many rows pgup/pgdn move the selection.
const rulesPageStep = 5

// rulesMode is the Rules modal's current interaction mode: browsing the list,
// authoring a rule through the agent, or writing one inline.
type rulesMode int

const (
	rulesModeList rulesMode = iota
	rulesModeNew
	rulesModeAuthor
)

// rulesStage tracks the two-step wizard both creation modes share: first a
// name, then either the rule body (inline) or the authoring instruction.
type rulesStage int

const (
	rulesStageName rulesStage = iota
	rulesStageContent
)

// Rules is a modal listing the active rule files, each with its on-disk
// location. Rules are the highest-priority instruction files, above project
// context and skills. 'n' writes a rule inline; 'a' asks the agent to author
// one.
type Rules struct {
	com      *common.Common
	entries  []rules.Rule
	selected int
	offset   int

	mode    rulesMode
	stage   rulesStage
	name    string
	prompt  string
	errText string
	input   textinput.Model
}

var _ Dialog = (*Rules)(nil)

// NewRules creates the Rules browser modal from name-sorted entries.
func NewRules(com *common.Common, entries []rules.Rule) *Rules {
	input := textinput.New()
	if com != nil && com.Styles != nil {
		input.SetStyles(com.Styles.TextInput)
	}
	return &Rules{com: com, entries: entries, input: input}
}

// ID implements [Dialog].
func (d *Rules) ID() string { return RulesID }

// HandleMsg implements [Dialog].
func (d *Rules) HandleMsg(msg tea.Msg) Action {
	if d.mode != rulesModeList {
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
		d.moveSelection(-rulesPageStep)
	case "pgdown":
		d.moveSelection(rulesPageStep)
	case "a":
		return ActionCmd{Cmd: d.enterWizard(rulesModeAuthor)}
	case "n":
		return ActionCmd{Cmd: d.enterWizard(rulesModeNew)}
	}
	return nil
}

// handleInput drives the two-step creation wizard's text input.
func (d *Rules) handleInput(msg tea.Msg) Action {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch {
		case matchesClose(k):
			d.exitWizard()
			return nil
		case k.String() == "enter":
			return d.advance()
		}
	}
	var cmd tea.Cmd
	d.input, cmd = d.input.Update(msg)
	return ActionCmd{Cmd: cmd}
}

// advance moves the wizard forward: from the name stage to the content stage,
// then to the finished action. An empty field is a no-op.
func (d *Rules) advance() Action {
	value := strings.TrimSpace(d.input.Value())
	if d.stage == rulesStageName {
		if value == "" {
			return nil
		}
		d.name = value
		d.stage = rulesStageContent
		d.errText = ""
		d.input.Reset()
		if d.mode == rulesModeAuthor {
			d.prompt = "What should this rule enforce?"
		} else {
			d.prompt = "Rule body"
		}
		return ActionCmd{Cmd: d.input.Focus()}
	}

	if value == "" {
		return nil
	}
	if d.mode == rulesModeAuthor {
		name := d.name
		d.exitWizard()
		return ActionAuthorRule{Name: name, Prompt: value}
	}

	dir := d.projectDir()
	if dir == "" {
		d.errText = "cannot resolve the project rules directory"
		return nil
	}
	path, err := rules.Write(dir, d.name, value)
	if err != nil {
		d.errText = err.Error()
		return nil
	}
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	d.exitWizard()
	return ActionAddRule{Name: name}
}

// enterWizard switches into a creation sub-mode at the name stage and focuses
// the text input.
func (d *Rules) enterWizard(mode rulesMode) tea.Cmd {
	d.mode = mode
	d.stage = rulesStageName
	d.name = ""
	d.errText = ""
	d.input.Reset()
	if mode == rulesModeAuthor {
		d.prompt = "Name for the rule to author"
	} else {
		d.prompt = "Rule name"
	}
	return d.input.Focus()
}

// exitWizard returns to the list, discarding any typed text.
func (d *Rules) exitWizard() {
	d.mode = rulesModeList
	d.stage = rulesStageName
	d.name = ""
	d.errText = ""
	d.input.Blur()
	d.input.Reset()
}

// moveSelection clamps the selection cursor within the entry list.
func (d *Rules) moveSelection(delta int) {
	if len(d.entries) == 0 {
		return
	}
	d.selected = max(0, min(d.selected+delta, len(d.entries)-1))
}

// projectDir returns the project's rules directory, or "" when the working
// directory cannot be resolved.
func (d *Rules) projectDir() string {
	if d.com == nil || d.com.Workspace == nil {
		return ""
	}
	wd := d.com.Workspace.WorkingDir()
	if wd == "" {
		return ""
	}
	return rules.ProjectDir(wd)
}

// Draw implements [Dialog].
func (d *Rules) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
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
		fmt.Sprintf("Rules (%d)", len(d.entries)),
		inner,
		t.Dialog.TitleGradFromColor,
		t.Dialog.TitleGradToColor,
	)

	if d.mode != rulesModeList {
		return d.drawInput(scr, area, frame, width, inner, title)
	}

	var lines []string
	if len(d.entries) == 0 {
		lines = []string{t.Resource.AdditionalText.Render("No rules found")}
	} else {
		rows := d.buildRows(inner)
		listHeight := min(len(rows), max(1, height-2))
		if d.selected < d.offset {
			d.offset = d.selected
		} else if d.selected >= d.offset+listHeight {
			d.offset = d.selected - listHeight + 1
		}
		d.offset = max(0, min(d.offset, len(rows)-listHeight))
		lines = rows[d.offset:min(len(rows), d.offset+listHeight)]
	}

	out := []string{title}
	out = append(out, lines...)
	out = append(out, ansi.Truncate(
		"↑/↓ select · a author · n new · esc close", inner, "…"),
	)

	DrawCenter(scr, area, frame.Width(width).Render(strings.Join(out, "\n")))
	return nil
}

// buildRows renders one row per rule, highlighting the selection.
func (d *Rules) buildRows(inner int) []string {
	t := d.com.Styles
	rows := make([]string, 0, len(d.entries))
	for i, e := range d.entries {
		row := fmt.Sprintf("%s %s  %s",
			t.Resource.OnlineIcon.String(),
			t.Resource.Name.Render(e.Name),
			t.Resource.AdditionalText.Render(e.Path),
		)
		row = ansi.Truncate(row, max(1, inner-2), "…")
		if i == d.selected {
			rows = append(rows, t.Dialog.SelectedItem.Render(row))
		} else {
			rows = append(rows, t.Dialog.NormalItem.Render(row))
		}
	}
	return rows
}

// drawInput renders the creation wizard's prompt, text field, and any error.
func (d *Rules) drawInput(scr uv.Screen, area uv.Rectangle, frame lipgloss.Style, width, inner int, title string) *tea.Cursor {
	t := d.com.Styles
	d.input.SetWidth(max(1, inner-2))

	hint := "enter next · esc cancel"
	if d.stage == rulesStageContent {
		hint = "enter submit · esc cancel"
	}

	parts := []string{
		title,
		t.Resource.Name.Render(ansi.Truncate(d.prompt, inner, "…")),
		t.Dialog.InputPrompt.Render(d.input.View()),
	}
	if d.errText != "" {
		parts = append(parts, t.Resource.ErrorIcon.String()+" "+
			ansi.Truncate(d.errText, max(1, inner-2), "…"))
	}
	parts = append(parts, ansi.Truncate(hint, inner, "…"))

	cur := InputCursor(t, d.input.Cursor())
	DrawCenterCursor(scr, area, frame.Width(width).Render(strings.Join(parts, "\n")), cur)
	return cur
}
