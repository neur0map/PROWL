package dialog

import (
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/neur0map/prowl/internal/config"
	"github.com/neur0map/prowl/internal/ui/common"
)

const (
	// SettingsID is the dialog ID for the settings panel.
	SettingsID = "settings"

	settingsDialogMaxWidth = 76
)

// ActionSetSetting persists a config field to Value (global scope). Display is
// the human-readable new state shown in the confirmation toast.
type ActionSetSetting struct {
	Key     string
	Value   any
	Label   string
	Display string
}

// settingSpec is one row of the settings panel. A row is either a boolean
// checkbox (get != nil) or a value that opens a sub-dialog (display != nil).
// emit returns the action performed when the row is activated; cur is the row's
// current boolean state (false for sub-dialog rows).
type settingSpec struct {
	section string
	title   string
	desc    string
	get     func(*config.Options) bool
	display func(*config.Options) string
	emit    func(cur bool) Action
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// settingSpecs is the consolidated list of user preferences. Frequent session
// toggles with their own keybindings (yolo, help, to-dos) stay in the command
// palette; everything set-and-forget lives here.
var settingSpecs = []settingSpec{
	{
		section: "Agent",
		title:   "Durable knowledge",
		desc:    "Record reusable lessons to prowl-agent knowledge (learn tool).",
		get:     func(o *config.Options) bool { return o.LearnEnabled() },
		emit: func(cur bool) Action {
			return ActionSetSetting{Key: "options.autolearn.learn", Value: !cur, Label: "Durable knowledge", Display: onOff(!cur)}
		},
	},
	{
		section: "Agent",
		title:   "Managed skills",
		desc:    "Let the agent create and update its own skills (manage_skill).",
		get:     func(o *config.Options) bool { return o.ManageSkillEnabled() },
		emit: func(cur bool) Action {
			return ActionSetSetting{Key: "options.autolearn.manage_skill", Value: !cur, Label: "Managed skills", Display: onOff(!cur)}
		},
	},
	{
		section: "Code intelligence",
		title:   "Enabled",
		desc:    "prowl-agent index and the native code-intelligence tool.",
		get:     func(o *config.Options) bool { return o.GetProwlAgent().IsEnabled() },
		emit: func(cur bool) Action {
			return ActionSetSetting{Key: "options.prowl_agent.enabled", Value: !cur, Label: "Code intelligence", Display: onOff(!cur)}
		},
	},
	{
		section: "Code intelligence",
		title:   "Auto-index on launch",
		desc:    "Build or refresh the code index in the background at startup.",
		get:     func(o *config.Options) bool { return o.GetProwlAgent().AutoIndexEnabled() },
		emit: func(cur bool) Action {
			return ActionSetSetting{Key: "options.prowl_agent.auto_index", Value: !cur, Label: "Auto-index on launch", Display: onOff(!cur)}
		},
	},
	{
		section: "Interface",
		title:   "Compact mode",
		desc:    "Denser layout that hides the sidebar.",
		get:     settingCompact,
		emit:    func(bool) Action { return ActionToggleCompactMode{} },
	},
	{
		section: "Interface",
		title:   "Mouse support",
		desc:    "Mouse selection, scrolling, and clicks.",
		get:     settingMouse,
		emit:    func(bool) Action { return ActionToggleMouseSupport{} },
	},
	{
		section: "Interface",
		title:   "Transparent background",
		desc:    "Use the terminal's background instead of the theme's.",
		get:     settingTransparent,
		emit:    func(bool) Action { return ActionToggleTransparentBackground{} },
	},
	{
		section: "Interface",
		title:   "Notifications",
		desc:    "How prowl notifies you when a turn finishes.",
		display: settingNotifications,
		emit:    func(bool) Action { return ActionOpenDialog{DialogID: NotificationsID} },
	},
}

func settingCompact(o *config.Options) bool { return o != nil && o.TUI != nil && o.TUI.CompactMode }

func settingMouse(o *config.Options) bool {
	if o == nil || o.TUI == nil || o.TUI.Mouse == nil {
		return true
	}
	return *o.TUI.Mouse
}

func settingTransparent(o *config.Options) bool {
	return o != nil && o.TUI != nil && o.TUI.IsTransparent()
}

func settingNotifications(o *config.Options) string {
	if o == nil || o.Notifications == "" {
		return "auto"
	}
	return o.Notifications
}

type settingsKeyMap struct {
	Up     key.Binding
	Down   key.Binding
	Toggle key.Binding
	Close  key.Binding
}

// Settings is the consolidated preferences panel.
type Settings struct {
	com    *common.Common
	cursor int
	keyMap settingsKeyMap
	help   help.Model
}

// NewSettings creates the settings panel dialog.
func NewSettings(com *common.Common) *Settings {
	h := help.New()
	h.Styles = com.Styles.DialogHelpStyles()
	s := &Settings{com: com, help: h}
	s.keyMap.Up = key.NewBinding(key.WithKeys("up", "ctrl+p"), key.WithHelp("↑", "up"))
	s.keyMap.Down = key.NewBinding(key.WithKeys("down", "ctrl+n"), key.WithHelp("↓", "down"))
	s.keyMap.Toggle = key.NewBinding(key.WithKeys("enter", " ", "tab"), key.WithHelp("enter", "change"))
	s.keyMap.Close = CloseKey
	return s
}

// ID implements Dialog.
func (s *Settings) ID() string { return SettingsID }

// HandleMsg implements Dialog.
func (s *Settings) HandleMsg(msg tea.Msg) Action {
	km, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	switch {
	case key.Matches(km, s.keyMap.Close):
		return ActionClose{}
	case key.Matches(km, s.keyMap.Up):
		s.cursor = (s.cursor - 1 + len(settingSpecs)) % len(settingSpecs)
	case key.Matches(km, s.keyMap.Down):
		s.cursor = (s.cursor + 1) % len(settingSpecs)
	case key.Matches(km, s.keyMap.Toggle):
		spec := settingSpecs[s.cursor]
		cur := false
		if spec.get != nil {
			cur = spec.get(s.opts())
		}
		return spec.emit(cur)
	}
	return nil
}

// opts returns the live config options, which may be nil (getters are nil-safe).
func (s *Settings) opts() *config.Options {
	if cfg := s.com.Config(); cfg != nil {
		return cfg.Options
	}
	return nil
}

// Draw implements Dialog.
func (s *Settings) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	t := s.com.Styles
	width := max(0, min(settingsDialogMaxWidth, area.Dx()-t.Dialog.View.GetHorizontalBorderSize()))
	innerWidth := width - t.Dialog.View.GetHorizontalFrameSize()
	opts := s.opts()

	var b strings.Builder
	lastSection := ""
	for i, spec := range settingSpecs {
		if spec.section != lastSection {
			if i > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(t.Dialog.ListItem.InfoBlurred.Render(strings.ToUpper(spec.section)))
			b.WriteByte('\n')
			lastSection = spec.section
		}

		rowStyle := t.Dialog.NormalItem
		if i == s.cursor {
			rowStyle = t.Dialog.SelectedItem
		}
		var line string
		if spec.get != nil {
			box := "[ ] "
			if spec.get(opts) {
				box = "[x] "
			}
			line = box + spec.title
		} else {
			line = spec.title + ": " + spec.display(opts) + " ›"
		}
		b.WriteString(rowStyle.Render(line))
		b.WriteByte('\n')
		b.WriteString(t.Dialog.ListItem.InfoBlurred.Render("    " + ansi.Truncate(spec.desc, max(0, innerWidth-4), "…")))
		b.WriteByte('\n')
	}

	rc := NewRenderContext(t, width)
	rc.Title = "Settings"
	rc.AddPart(b.String())
	rc.Help = renderDialogHelp(t, &s.help, s, innerWidth)

	DrawCenter(scr, area, rc.Render())
	return nil
}

// ShortHelp implements help.KeyMap.
func (s *Settings) ShortHelp() []key.Binding {
	return []key.Binding{s.keyMap.Up, s.keyMap.Down, s.keyMap.Toggle, s.keyMap.Close}
}

// FullHelp implements help.KeyMap.
func (s *Settings) FullHelp() [][]key.Binding {
	return [][]key.Binding{s.ShortHelp()}
}
