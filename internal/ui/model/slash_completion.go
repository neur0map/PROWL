package model

import (
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/neur0map/prowl/internal/commands"
	"github.com/neur0map/prowl/internal/githubref"
	"github.com/neur0map/prowl/internal/ui/completions"
)

// textareaCursorIndex converts the editor's logical rune coordinates to the
// byte offsets used by completion ranges. It never uses rendered cell widths.
func (m *UI) textareaCursorIndex(value string) int {
	row, col := 0, 0
	for i, r := range value {
		if row == m.textarea.Line() && col == m.textarea.Column() {
			return i
		}
		if r == '\n' {
			row++
			col = 0
		} else {
			col++
		}
	}
	return len(value)
}

// slashNameMatches accepts word prefixes in order: "goal sh" finds "goal show",
// and "goal" also finds "guided-goal", without fuzzy jumps between actions.
func slashNameMatches(name, query string) bool {
	name = strings.ToLower(name)
	if strings.HasPrefix(name, query) {
		return true
	}
	for word := range strings.FieldsFuncSeq(name, func(r rune) bool {
		return unicode.IsSpace(r) || r == '-' || r == ':' || r == '_'
	}) {
		part, rest, _ := strings.Cut(query, " ")
		if strings.HasPrefix(word, part) {
			if rest == "" {
				return true
			}
			query = rest
		}
	}
	return false
}

func (m *UI) slashCommandItems(raw string) ([]completions.CommandCompletionValue, string, bool) {
	query := strings.ToLower(strings.Join(strings.Fields(raw), " "))
	trailingSpace := strings.TrimRightFunc(raw, unicode.IsSpace) != raw
	var items []completions.CommandCompletionValue
	inArguments := false
	add := func(item completions.CommandCompletionValue, alias string) {
		for _, name := range []string{item.Name, alias} {
			if name == "" {
				continue
			}
			name = strings.ToLower(name)
			if strings.EqualFold(name, alias) {
				reserved := false
				for _, builtin := range commands.BuiltinSlashCommands {
					root, _, _ := strings.Cut(builtin.Name, " ")
					reserved = reserved || strings.EqualFold(root, name)
				}
				if reserved {
					continue
				}
			}
			if strings.HasPrefix(query, name+" ") || query == name && trailingSpace {
				inArguments = true
			}
		}
		if slashNameMatches(item.Name, query) || alias != "" && slashNameMatches(alias, query) {
			items = append(items, item)
		}
	}
	for _, cmd := range commands.BuiltinSlashCommands {
		add(completions.CommandCompletionValue{
			Name: cmd.Name, Description: cmd.Description, Group: cmd.Group,
			Arguments: cmd.Arguments, NeedsInput: cmd.NeedsInput,
		}, "")
	}
	for _, skills := range []bool{true, false} {
		for _, cmd := range m.customCommands {
			if (cmd.Skill != nil) != skills {
				continue
			}
			item := completions.CommandCompletionValue{
				Name: cmd.ID, Description: cmd.Name, Group: "Custom commands",
				Arguments: "[instructions]",
			}
			alias := cmd.ID[strings.LastIndex(cmd.ID, ":")+1:]
			if cmd.Skill != nil {
				alias, item.Group = cmd.Skill.Name, "Skills"
			}
			if len(cmd.Arguments) > 0 {
				item.Arguments = "[arguments]"
				for _, arg := range cmd.Arguments {
					item.NeedsInput = item.NeedsInput || arg.Required
				}
				if item.NeedsInput {
					item.Arguments = "<arguments>"
				}
			}
			add(item, alias)
		}
	}
	for _, cmd := range m.mcpPrompts {
		item := completions.CommandCompletionValue{Name: cmd.ID, Description: cmd.Title, Group: "MCP prompts"}
		if len(cmd.Arguments) > 0 {
			item.Arguments = "[arguments]"
			for _, arg := range cmd.Arguments {
				item.NeedsInput = item.NeedsInput || arg.Required
			}
			if item.NeedsInput {
				item.Arguments = "<arguments>"
			}
		}
		add(item, "")
	}
	return items, query, !inArguments
}

// refreshLocalCompletions handles slash names and #numbers synchronously. The
// existing @ loader is the only completion path that performs IO.
func (m *UI) refreshLocalCompletions() {
	value := m.textarea.Value()
	end := m.textareaCursorIndex(value)
	prefix := value[:end]
	if m.bangMode {
		if m.completionsKind != "file" {
			m.closeCompletions()
		}
		return
	}
	trimmed := strings.TrimLeftFunc(prefix, unicode.IsSpace)
	if strings.HasPrefix(trimmed, "/") && !strings.HasPrefix(trimmed, "//") {
		items, query, commandQuery := m.slashCommandItems(trimmed[1:])
		if commandQuery {
			start := len(prefix) - len(trimmed)
			if m.completionsOpen && m.completionsKind == "slash" && m.completionsQuery == query && m.completionsStartIndex == start && m.completionsEndIndex == end {
				return
			}
			m.completionsKind, m.completionsOpen = "slash", true
			m.completionsStartIndex, m.completionsEndIndex = start, end
			m.completionsQuery = query
			m.completionsPositionStart = m.layout.editor.Min
			m.completions.SetCommands(items, query)
			return
		}
	}
	start, refs := githubref.Complete(prefix)
	if len(refs) == 0 {
		if m.completionsKind != "file" {
			m.closeCompletions()
		}
		return
	}
	query := prefix[start:]
	if m.completionsOpen && m.completionsKind == "github" && m.completionsQuery == query && m.completionsStartIndex == start && m.completionsEndIndex == end {
		return
	}
	items := make([]completions.TextCompletionValue, 0, len(refs))
	for _, ref := range refs {
		items = append(items, completions.TextCompletionValue{Label: ref.Label, Text: ref.URI})
	}
	m.completionsKind, m.completionsOpen = "github", true
	m.completionsStartIndex, m.completionsEndIndex = start, end
	m.completionsQuery = query
	m.completionsPositionStart = m.completionsPosition()
	m.completions.SetItems(nil, nil, items...)
}

func (m *UI) insertTextCompletion(text string) tea.Cmd {
	previous := m.textarea.Height()
	m.insertCompletionText(text)
	m.closeCompletions()
	return m.handleTextareaHeightChange(previous)
}

func (m *UI) selectSlashCompletion(selection completions.CommandSelectionMsg) tea.Cmd {
	// Editing a command in the middle of a draft must never submit its suffix.
	execute := selection.Execute && m.completionsEndIndex == len(m.textarea.Value())
	resize := m.insertTextCompletion("/" + selection.Value.Name)
	if !execute {
		return resize
	}
	// Reuse the regular submit path, including attachment and busy-state handling.
	return tea.Batch(resize, m.handleKeyPressMsg(tea.KeyPressMsg{Code: tea.KeyEnter}))
}
