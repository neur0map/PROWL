package completions

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// CommandCompletionValue keeps command syntax separate from its explanation.
type CommandCompletionValue struct {
	Name        string
	Description string
	Group       string
	Arguments   string
	NeedsInput  bool
}

// CommandSelectionMsg distinguishes filling a command from running it.
type CommandSelectionMsg struct {
	Value   CommandCompletionValue
	Execute bool
}

type commandPicker struct {
	items            []CommandCompletionValue
	selected, offset int
	maxWidth         int
	maxHeight        int
	width, height    int
	view             string
}

// SetCommands opens a top-to-bottom command picker, preserving a selection
// while the user narrows the query. File and reference completions stay separate.
func (c *Completions) SetCommands(items []CommandCompletionValue, query string) {
	previous := ""
	width, height := 80, 18
	if p := c.command; p != nil {
		width, height = p.maxWidth, p.maxHeight
		if c.open && p.selected < len(p.items) {
			previous = p.items[p.selected].Name
		}
	}
	p := &commandPicker{items: items, maxWidth: width, maxHeight: height}
	for i := range items {
		items[i].Description = strings.Join(strings.Fields(ansi.Strip(items[i].Description)), " ")
	}
	for i := range items {
		if items[i].Name == previous {
			p.selected = i
			break
		}
	}
	c.command, c.query, c.open = p, query, true
}

// SetCommandBounds keeps the picker inside the space above the editor.
func (c *Completions) SetCommandBounds(width, height int) {
	if p := c.command; p != nil {
		width, height = max(0, min(80, width)), max(0, min(18, height))
		if width != p.maxWidth || height != p.maxHeight {
			p.maxWidth, p.maxHeight, p.view = width, height, ""
		}
	}
}

func (c *Completions) updateCommand(msg tea.KeyPressMsg) (tea.Msg, bool) {
	p := c.command
	if (p.maxWidth < 12 || p.maxHeight < 6) && msg.String() != "esc" && msg.String() != "alt+esc" {
		return nil, false
	}
	switch msg.String() {
	case "esc", "alt+esc":
		c.Close()
		return ClosedMsg{}, true
	case "up", "ctrl+p", "down", "ctrl+n":
		if len(p.items) > 0 {
			delta := 1
			if msg.String() == "up" || msg.String() == "ctrl+p" {
				delta = -1
			}
			p.selected = (p.selected + delta + len(p.items)) % len(p.items)
			p.view = ""
		}
		return nil, true
	case "enter", "tab", "ctrl+y":
		if len(p.items) == 0 {
			return nil, msg.String() != "enter"
		}
		value := p.items[p.selected]
		c.Close()
		return CommandSelectionMsg{Value: value, Execute: msg.String() == "enter" && !value.NeedsInput}, true
	}
	return nil, false
}

// commandWindow counts group headings as rows, so a selected action never
// disappears behind the footer on a short terminal.
func commandWindow(items []CommandCompletionValue, start, rows int) int {
	group := ""
	end := start
	for end < len(items) {
		cost := 1
		if items[end].Group != group {
			cost++
		}
		if cost > rows {
			break
		}
		rows -= cost
		group = items[end].Group
		end++
	}
	return end
}

func (c *Completions) renderCommands() string {
	p := c.command
	if p.view != "" {
		return p.view
	}
	p.width, p.height = p.maxWidth, 0
	if p.maxWidth < 12 || p.maxHeight < 6 {
		return ""
	}

	// Use the active theme's surface, foreground, and selection accent.
	base := c.normalStyle.Padding(0).Margin(0)
	accent := base.Foreground(c.focusedStyle.GetBackground()).Bold(true)
	muted := base.Faint(true)
	focused := c.focusedStyle.Padding(0).Margin(0)
	inner := p.maxWidth - 4
	wide := inner >= 60
	footerRows := 1
	if p.maxHeight >= 10 {
		footerRows = 2
		if !wide {
			footerRows = 3
		}
	}
	bodyRows := p.maxHeight - 3 - footerRows
	rows := make([]string, 0, p.maxHeight-2)
	count := fmt.Sprintf("%d commands", len(p.items))
	if c.query != "" {
		count = fmt.Sprintf("%d matches", len(p.items))
	}
	if len(p.items) == 1 {
		if c.query == "" {
			count = "1 command"
		} else {
			count = "1 match"
		}
	}
	heading := "Commands"
	if inner >= 26 {
		gap := max(1, inner-ansi.StringWidth(heading)-ansi.StringWidth(count))
		rows = append(rows, accent.Render(heading)+base.Render(strings.Repeat(" ", gap))+muted.Render(count))
	} else {
		rows = append(rows, accent.Render(ansi.Truncate(heading, inner, "…")))
	}

	end := 0
	if len(p.items) == 0 {
		rows = append(rows, muted.Render(ansi.Truncate("No commands match /"+c.query, inner, "…")))
	} else {
		p.offset = min(p.offset, p.selected)
		end = commandWindow(p.items, p.offset, bodyRows)
		for p.selected >= end && p.offset < p.selected {
			p.offset++
			end = commandWindow(p.items, p.offset, bodyRows)
		}
		group := ""
		for i := p.offset; i < end; i++ {
			item := p.items[i]
			if item.Group != group {
				group = item.Group
				rows = append(rows, muted.Bold(true).Render(ansi.Truncate(group, inner, "…")))
			}
			style := base
			marker := "  "
			if i == p.selected {
				style, marker = focused, "▸ "
			}
			name := "/" + item.Name
			syntax := name
			if item.Arguments != "" {
				syntax += " " + item.Arguments
			}
			nameWidth := min(ansi.StringWidth(name), inner-2)
			text := marker + ansi.Truncate(syntax, inner-2, "…")
			if wide {
				column := min(34, inner/2)
				nameWidth = min(nameWidth, column-2)
				left := ansi.Truncate(syntax, column-2, "…")
				right := ansi.Truncate(item.Description, inner-column-2, "…")
				text = marker + left + strings.Repeat(" ", column-ansi.StringWidth(left)) + right
			}
			row := style.Width(inner).Render(text)
			row = lipgloss.StyleRanges(row, lipgloss.NewRange(2, 2+nameWidth, style.Bold(true)))
			if i != p.selected {
				row = lipgloss.StyleRanges(row, lipgloss.NewRange(2+nameWidth, inner, style.Faint(true)))
			}
			rows = append(rows, row)
		}
	}

	if footerRows >= 2 {
		rows = append(rows, muted.Render(strings.Repeat("─", inner)))
	}
	if footerRows == 3 {
		detail := "Keep typing to filter commands"
		if len(p.items) > 0 {
			item := p.items[p.selected]
			detail = item.Description
			if item.Arguments != "" {
				detail = "/" + item.Name + " " + item.Arguments
			}
		}
		rows = append(rows, base.Render(ansi.Truncate(detail, inner, "…")))
	}
	hint := "↑↓ choose  tab fill  enter run  esc close"
	if len(p.items) > 0 && p.items[p.selected].NeedsInput {
		hint = "↑↓ choose  tab/enter fill  esc close"
	}
	if inner < 44 {
		hint = "↑↓  tab fill  enter  esc"
	}
	if end < len(p.items) || p.offset > 0 {
		position := fmt.Sprintf("%d/%d", p.selected+1, len(p.items))
		hint = ansi.Truncate(hint, max(0, inner-len(position)-2), "…")
		hint += strings.Repeat(" ", max(1, inner-ansi.StringWidth(hint)-len(position))) + position
	}
	rows = append(rows, muted.Render(ansi.Truncate(hint, inner, "…")))
	frame := base.Border(lipgloss.RoundedBorder()).BorderForeground(c.focusedStyle.GetBackground()).Padding(0, 1).Width(p.maxWidth)
	p.view = frame.Render(strings.Join(rows, "\n"))
	p.width, p.height = lipgloss.Size(p.view)
	return p.view
}
