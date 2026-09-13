package completions

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func TestHiddenCommandPickerDoesNotSelectInvisibleActions(t *testing.T) {
	t.Parallel()
	for _, size := range [][2]int{{11, 18}, {80, 5}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			t.Parallel()
			c := New(lipgloss.NewStyle(), lipgloss.NewStyle(), lipgloss.NewStyle())
			c.SetCommands([]CommandCompletionValue{{Name: "green"}}, "gr")
			c.SetCommandBounds(size[0], size[1])
			require.Empty(t, c.Render())
			msg, handled := c.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			require.False(t, handled, "a hidden picker must leave Enter to the editor")
			require.Nil(t, msg)

			c.SetCommandBounds(80, 18)
			msg, handled = c.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			require.True(t, handled)
			require.True(t, msg.(CommandSelectionMsg).Execute)
		})
	}
}

func TestCommandPickerSelectionDistinguishesFillAndRun(t *testing.T) {
	t.Parallel()
	items := []CommandCompletionValue{
		{Name: "goal set", NeedsInput: true, Group: "Start"},
		{Name: "guided-goal", Group: "Start"},
	}
	c := New(lipgloss.NewStyle(), lipgloss.NewStyle(), lipgloss.NewStyle())
	c.SetCommands(items, "goal")
	msg, handled := c.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.True(t, handled)
	selection := msg.(CommandSelectionMsg)
	require.Equal(t, "goal set", selection.Value.Name)
	require.False(t, selection.Execute, "a required objective must be filled before submitting")

	c.SetCommands(items, "goal")
	c.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	c.SetCommands(items[1:], "guided")
	msg, _ = c.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	selection = msg.(CommandSelectionMsg)
	require.Equal(t, "guided-goal", selection.Value.Name)
	require.True(t, selection.Execute, "Enter should start the selected interview")

	c.SetCommands(items[1:], "guided")
	msg, _ = c.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	require.False(t, msg.(CommandSelectionMsg).Execute, "Tab must never launch a workflow")
}

func TestCommandPickerKeepsSelectionVisibleWithinBounds(t *testing.T) {
	t.Parallel()
	for _, size := range [][2]int{{80, 18}, {44, 12}, {20, 8}, {12, 6}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			t.Parallel()
			items := make([]CommandCompletionValue, 20)
			for i := range items {
				items[i] = CommandCompletionValue{
					Name: fmt.Sprintf("cmd-%02d", i), Group: fmt.Sprintf("Group %d", i/3),
					Description: "A multiline\ndescription with wide text 日本語",
				}
			}
			c := New(lipgloss.NewStyle(), lipgloss.NewStyle(), lipgloss.NewStyle())
			c.SetCommands(items, "")
			c.SetCommandBounds(size[0], size[1])
			for i := range items {
				view := c.Render()
				w, h := lipgloss.Size(view)
				require.LessOrEqual(t, w, size[0])
				require.LessOrEqual(t, h, size[1])
				if size[0] >= 20 {
					require.Contains(t, ansi.Strip(view), "/"+items[i].Name)
				}
				c.Update(tea.KeyPressMsg{Code: tea.KeyDown})
			}
			// Wraparound returns to the first action, not a hidden group header.
			msg, _ := c.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			require.Equal(t, items[0].Name, msg.(CommandSelectionMsg).Value.Name)
		})
	}
}

func TestCommandPickerUsesVisualTopToBottomOrder(t *testing.T) {
	t.Parallel()
	c := New(lipgloss.NewStyle(), lipgloss.NewStyle(), lipgloss.NewStyle())
	c.SetCommands([]CommandCompletionValue{
		{Name: "goal set", Group: "Start"},
		{Name: "guided-goal", Group: "Start"},
		{Name: "goal pause", Group: "Current"},
	}, "goal")
	view := ansi.Strip(c.Render())
	require.Less(t, strings.Index(view, "/goal set"), strings.Index(view, "/guided-goal"))
	c.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	msg, _ := c.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.Equal(t, "guided-goal", msg.(CommandSelectionMsg).Value.Name)
}
