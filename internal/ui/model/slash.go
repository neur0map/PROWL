package model

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/neur0map/prowl/internal/commands"
	"github.com/neur0map/prowl/internal/goals"
	"github.com/neur0map/prowl/internal/message"
	"github.com/neur0map/prowl/internal/ui/dialog"
	"github.com/neur0map/prowl/internal/ui/util"
)

// handleSlashCommand reserves builtins, then resolves existing file, skill and
// MCP commands. Unknown slash input remains a normal prompt, like OMP.
func (m *UI) handleSlashCommand(input string, attachments ...message.Attachment) (tea.Cmd, bool) {
	name, args, ok := commands.SplitSlash(input)
	if !ok {
		return nil, false
	}
	switch strings.ToLower(name) {
	case "goal":
		return m.handleGoalInput(args, input, attachments), true
	case "guided-goal":
		return m.runGoalCommand(goals.Request{Op: "guided", Objective: args}, input, attachments), true
	case "green":
		m.attachments.Reset()
		prompt := commands.GreenPrompt
		if args != "" {
			prompt += "\n\nAdditional user constraints:\n" + args
		}
		return m.sendMessage(prompt, attachments...), true
	case "help":
		return m.openCommandsDialog(), true
	}
	cmd, err := commands.FindSlashCustom(name, m.customCommands)
	if err != nil {
		return m.slashError(input, err), true
	}
	if cmd != nil {
		if cmd.Skill != nil {
			content := cmd.Skill.FormatInvocation()
			if args != "" {
				content += "\n\n" + args
			}
			m.attachments.Reset()
			return m.sendMessage(content, attachments...), true
		}
		if len(cmd.Arguments) > 0 && args == "" {
			m.dialog.OpenDialog(dialog.NewArguments(m.com, cmd.Name, "", cmd.Arguments,
				dialog.ActionRunCustomCommand{Content: cmd.Content, Arguments: cmd.Arguments}))
			return nil, true
		}
		content := cmd.Content
		if len(cmd.Arguments) > 0 {
			values, err := slashArgumentValues(cmd.Arguments, args)
			if err != nil {
				return m.slashError(input, err), true
			}
			content = substituteArgs(content, values)
		} else if args != "" {
			content += "\n\n" + args
		}
		m.attachments.Reset()
		return m.sendMessage(content, attachments...), true
	}
	for _, cmd := range m.mcpPrompts {
		if cmd.ID != name {
			continue
		}
		if len(cmd.Arguments) > 0 && args == "" {
			m.dialog.OpenDialog(dialog.NewArguments(m.com, cmd.Title, cmd.Description, cmd.Arguments,
				dialog.ActionRunMCPPrompt{ClientID: cmd.ClientID, PromptID: cmd.PromptID, Arguments: cmd.Arguments}))
			return nil, true
		}
		values, err := slashArgumentValues(cmd.Arguments, args)
		if err != nil {
			return m.slashError(input, err), true
		}
		return m.runMCPPrompt(cmd.ClientID, cmd.PromptID, values), true
	}
	return nil, false
}

func slashArgumentValues(arguments []commands.Argument, input string) (map[string]string, error) {
	values := make(map[string]string, len(arguments))
	if len(arguments) == 1 {
		values[arguments[0].ID] = input
		return values, nil
	}
	parts, err := commands.SplitArguments(input)
	if err != nil {
		return nil, err
	}
	if len(parts) > len(arguments) {
		return nil, fmt.Errorf("expected at most %d arguments; quote multi-word values", len(arguments))
	}
	for i, argument := range arguments {
		if i < len(parts) {
			values[argument.ID] = parts[i]
		}
		if argument.Required && strings.TrimSpace(values[argument.ID]) == "" {
			return nil, fmt.Errorf("missing argument %s", argument.Title)
		}
	}
	return values, nil
}

func (m *UI) handleGoalInput(args, draft string, attachments []message.Attachment) tea.Cmd {
	if args == "" {
		m.textarea.SetValue(draft)
		m.textarea.MoveToEnd()
		m.refreshLocalCompletions()
		return nil
	}
	first, rest := args, ""
	if i := strings.IndexFunc(args, unicode.IsSpace); i >= 0 {
		first, rest = args[:i], strings.TrimSpace(args[i:])
	}
	op := strings.ToLower(first)
	req := goals.Request{Op: op}
	switch op {
	case "set":
		if rest == "" {
			return m.goalArgumentDialog("set")
		}
		req.Objective = rest
	case "budget":
		if rest == "" {
			return m.goalArgumentDialog("budget")
		}
		if strings.ToLower(rest) != "off" {
			n, err := strconv.ParseInt(rest, 10, 64)
			if err != nil || n <= 0 {
				return m.slashError(draft, fmt.Errorf("goal budget must be a positive integer or off"))
			}
			req.TokenBudget = &n
		}
	case "show", "pause", "resume", "drop":
		if rest != "" {
			return m.slashError(draft, fmt.Errorf("usage: /goal %s", op))
		}
	default:
		return m.slashError(draft, fmt.Errorf("choose a goal action; use /goal set <objective> or /guided-goal to start"))
	}
	return m.runGoalCommand(req, draft, attachments)
}

func (m *UI) goalArgumentDialog(op string) tea.Cmd {
	title, description := "Goal objective", "The complete objective and success criteria"
	if op == "budget" {
		title, description = "Goal token budget", "A positive integer, or off to remove the cap"
	}
	m.dialog.OpenDialog(dialog.NewArguments(m.com, title, "", []commands.Argument{
		{ID: "value", Title: title, Description: description, Required: true},
	}, dialog.ActionRunSlashCommand{Command: "/goal " + op + " $value"}))
	return nil
}

func (m *UI) slashError(draft string, err error) tea.Cmd {
	if m.textarea.Value() == "" {
		m.textarea.SetValue(draft)
		m.textarea.MoveToEnd()
	}
	m.updateLayoutAndSize()
	return util.ReportError(err)
}
