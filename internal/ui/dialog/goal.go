package dialog

import (
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/neur0map/prowl/internal/goals"
	"github.com/neur0map/prowl/internal/pubsub"
	"github.com/neur0map/prowl/internal/ui/common"
)

const GoalID = "goal"

type goalChoice struct{ label, command string }

// Goal presents the durable objective and its controls without hiding long
// objectives: page up/down scroll the details independently of menu selection.
type Goal struct {
	com          *common.Common
	goal         *goals.Goal
	sessionID    string
	choices      []goalChoice
	selected     int
	offset       int
	wrappedWidth int
	wrapped      []string
}

func NewGoal(com *common.Common, goal *goals.Goal, sessionID string) *Goal {
	d := &Goal{com: com, sessionID: sessionID}
	d.setGoal(goal)
	return d
}

func (d *Goal) ID() string { return GoalID }

func (d *Goal) setGoal(g *goals.Goal) {
	d.goal = g
	d.wrapped = nil
	d.choices = nil
	if g != nil && g.Status != goals.Complete {
		if g.Active() {
			d.choices = append(d.choices, goalChoice{"Pause", "/goal pause"})
		} else {
			d.choices = append(d.choices, goalChoice{"Resume", "/goal resume"})
		}
		d.choices = append(d.choices, goalChoice{"Adjust budget", "/goal budget"})
	}
	d.choices = append(d.choices, goalChoice{"Set objective", "/goal set"})
	if g == nil || g.Status == goals.Complete {
		d.choices = append(d.choices, goalChoice{"Guided setup", "/guided-goal"})
	}
	if g != nil {
		d.choices = append(d.choices, goalChoice{"Drop goal", "/goal drop"})
	}
	d.selected = min(d.selected, len(d.choices)-1)
}

func (d *Goal) HandleMsg(msg tea.Msg) Action {
	switch msg := msg.(type) {
	case pubsub.Event[goals.Goal]:
		if msg.Payload.SessionID == d.sessionID {
			if msg.Type == pubsub.DeletedEvent {
				d.setGoal(nil)
			} else {
				d.setGoal(&msg.Payload)
			}
		}
	case tea.KeyPressMsg:
		if key.Matches(msg, CloseKey) {
			return ActionClose{}
		}
		switch msg.String() {
		case "up", "shift+tab", "ctrl+p":
			d.selected = (d.selected + len(d.choices) - 1) % len(d.choices)
		case "down", "tab", "ctrl+n":
			d.selected = (d.selected + 1) % len(d.choices)
		case "pgup":
			d.offset = max(0, d.offset-5)
		case "pgdown":
			d.offset += 5
		case "enter":
			return ActionRunSlashCommand{Command: d.choices[d.selected].command}
		}
	}
	return nil
}

func (d *Goal) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	t := d.com.Styles
	frame := t.Dialog.View
	width := max(0, min(84, area.Dx()))
	inner := max(1, width-frame.GetHorizontalFrameSize())
	height := max(0, area.Dy()-frame.GetVerticalFrameSize())
	if height == 0 || width == 0 {
		return nil
	}
	if d.wrapped == nil || d.wrappedWidth != inner {
		d.wrapped = strings.Split(ansi.Hardwrap(ansi.Strip(d.goal.Summary()), inner, true), "\n")
		d.wrappedWidth = inner
	}
	menuHeight := min(len(d.choices), max(0, height-2))
	bodyHeight := min(10, max(0, height-menuHeight-2))
	d.offset = min(d.offset, max(0, len(d.wrapped)-bodyHeight))
	lines := []string{common.DialogTitle(t, "Goal", inner, t.Dialog.TitleGradFromColor, t.Dialog.TitleGradToColor)}
	if bodyHeight > 0 {
		lines = append(lines, strings.Join(d.wrapped[d.offset:min(len(d.wrapped), d.offset+bodyHeight)], "\n"))
	}
	menuStart := max(0, min(d.selected-menuHeight/2, len(d.choices)-menuHeight))
	for i := menuStart; i < menuStart+menuHeight; i++ {
		style := t.Button.Blurred
		prefix := "  "
		if i == d.selected {
			style = t.Button.Focused
			prefix = "> "
		}
		labelWidth := max(0, inner-style.GetHorizontalFrameSize())
		lines = append(lines, style.Width(inner).Render(ansi.Truncate(prefix+d.choices[i].label, labelWidth, "…")))
	}
	if height > 1 {
		lines = append(lines, ansi.Truncate("↑/↓ choose  enter confirm  pgup/pgdown details  esc close", inner, "…"))
	}
	DrawCenter(scr, area, frame.Width(width).Render(strings.Join(lines, "\n")))
	return nil
}
