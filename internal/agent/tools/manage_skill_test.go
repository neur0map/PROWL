package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"charm.land/fantasy"
	"github.com/neur0map/prowl/internal/skills"
	"github.com/stretchr/testify/require"
)

func runManageSkill(t *testing.T, tool fantasy.AgentTool, p ManageSkillParams) fantasy.ToolResponse {
	t.Helper()
	input, err := json.Marshal(p)
	require.NoError(t, err)
	resp, err := tool.Run(context.Background(), fantasy.ToolCall{ID: "c", Name: ManageSkillToolName, Input: string(input)})
	require.NoError(t, err)
	return resp
}

func TestManageSkillCreateAndDeleteRefreshes(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv(skills.ManagedSkillsDirEnv, tmp)

	refreshed := 0
	tool := NewManageSkillTool(func(string) bool { return false }, func() { refreshed++ })

	resp := runManageSkill(t, tool, ManageSkillParams{
		Action: "create", Name: "demo", Description: "when to use it", Body: "do the thing",
	})
	require.False(t, resp.IsError, resp.Content)
	require.FileExists(t, filepath.Join(tmp, "demo", skills.SkillFileName))
	require.Equal(t, 1, refreshed)

	resp = runManageSkill(t, tool, ManageSkillParams{Action: "delete", Name: "demo"})
	require.False(t, resp.IsError, resp.Content)
	require.Equal(t, 2, refreshed)
	require.NoFileExists(t, filepath.Join(tmp, "demo", skills.SkillFileName))
}

func TestManageSkillCannotShadowAuthored(t *testing.T) {
	t.Setenv(skills.ManagedSkillsDirEnv, t.TempDir())
	tool := NewManageSkillTool(func(name string) bool { return name == "taken" }, func() {})

	resp := runManageSkill(t, tool, ManageSkillParams{
		Action: "create", Name: "taken", Description: "d", Body: "b",
	})
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "already exists")
}

func TestManageSkillRejectsUnknownAction(t *testing.T) {
	t.Setenv(skills.ManagedSkillsDirEnv, t.TempDir())
	tool := NewManageSkillTool(func(string) bool { return false }, func() {})
	resp := runManageSkill(t, tool, ManageSkillParams{Action: "frobnicate", Name: "x"})
	require.True(t, resp.IsError)
}
