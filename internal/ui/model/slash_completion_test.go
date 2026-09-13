package model

import (
	"strings"
	"testing"

	"github.com/neur0map/prowl/internal/githubref"
	"github.com/stretchr/testify/require"
)

func TestReferenceCompletionPreservesCursorAndSuffix(t *testing.T) {
	t.Parallel()
	u := newTestUI()
	u.textarea.SetWidth(12)
	before := "前の行\nReview issue #123"
	suffix := " and retain this suffix\nnext line"
	u.textarea.SetValue(before + suffix)
	u.textarea.MoveToBegin()
	u.textarea.CursorDown()
	u.textarea.SetCursorColumn(len([]rune("Review issue #123")))
	end := u.textareaCursorIndex(u.textarea.Value())
	require.Equal(t, len(before), end)
	start, candidates := githubref.Complete(before)
	require.Equal(t, "issue://123", candidates[0].URI)
	u.completionsStartIndex, u.completionsEndIndex = start, end
	require.True(t, u.insertCompletionText(candidates[0].URI))
	want := "前の行\nReview issue://123 " + suffix
	require.Equal(t, want, u.textarea.Value())
	u.textarea.InsertString("HERE")
	require.Equal(t, strings.Replace(want, "123 ", "123 HERE", 1), u.textarea.Value())
}

func TestGoalCommandSearchOffersDirectAndGuidedActions(t *testing.T) {
	t.Parallel()
	u := &UI{}
	items, _, commandQuery := u.slashCommandItems("goal")
	require.True(t, commandQuery)
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, item.Name)
	}
	require.Contains(t, names, "goal set")
	require.Contains(t, names, "guided-goal")
	require.Contains(t, names, "goal show")
	require.NotContains(t, names, "goal", "the namespace is not an executable command")

	items, _, commandQuery = u.slashCommandItems("goal sh")
	require.True(t, commandQuery)
	require.Len(t, items, 1)
	require.Equal(t, "goal show", items[0].Name)

	items, _, _ = u.slashCommandItems("settings")
	require.Empty(t, items, "Settings belongs in the Commands modal")
}

func TestGoalCommandPickerYieldsToArguments(t *testing.T) {
	t.Parallel()
	u := &UI{}
	_, _, commandQuery := u.slashCommandItems("goal ")
	require.True(t, commandQuery, "a goal namespace still needs an action")
	_, _, commandQuery = u.slashCommandItems("goal set ")
	require.False(t, commandQuery, "the objective is entered as ordinary prompt text")
	_, _, commandQuery = u.slashCommandItems("goal set fix goal budget")
	require.False(t, commandQuery, "command words inside an objective are not new actions")
	_, _, commandQuery = u.slashCommandItems("guided-goal an idea")
	require.False(t, commandQuery)
}
