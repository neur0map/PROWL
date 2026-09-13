package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/neur0map/prowl/internal/agent/prompt"
	"github.com/neur0map/prowl/internal/config"
)

func TestRolePromptsRefreshScopedRules(t *testing.T) {
	t.Parallel()

	for name, build := range map[string]func(...prompt.Option) (*prompt.Prompt, error){
		"coder": coderPrompt,
		"task":  taskPrompt,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			global := filepath.Join(root, "global-rules")
			project := filepath.Join(root, "project-rules")
			require.NoError(t, os.WriteFile(global, []byte("GLOBAL_RULE_MARKER"), 0o600))
			require.NoError(t, os.WriteFile(project, []byte("PROJECT_RULE_BEFORE"), 0o600))
			store := config.NewTestStore(&config.Config{Options: &config.Options{
				GlobalContextPaths: []string{global},
				ContextPaths:       []string{project},
			}})
			p, err := build(prompt.WithWorkingDir(root))
			require.NoError(t, err)
			before, err := p.Build(context.Background(), store)
			require.NoError(t, err)
			require.Contains(t, before, global)
			require.Contains(t, before, project)
			require.Contains(t, before, "GLOBAL_RULE_MARKER")
			require.Contains(t, before, "PROJECT_RULE_BEFORE")
			require.Less(t, strings.Index(before, "GLOBAL_RULE_MARKER"), strings.Index(before, "PROJECT_RULE_BEFORE"))

			require.NoError(t, os.WriteFile(project, []byte("PROJECT_RULE_AFTER"), 0o600))
			after, err := p.Build(context.Background(), store)
			require.NoError(t, err)
			require.Contains(t, after, "GLOBAL_RULE_MARKER")
			require.Contains(t, after, "PROJECT_RULE_AFTER")
			require.NotContains(t, after, "PROJECT_RULE_BEFORE")
			require.Less(t, strings.Index(after, "GLOBAL_RULE_MARKER"), strings.Index(after, "PROJECT_RULE_AFTER"))
		})
	}
}

func TestWebResearchPromptDoesNotDiscloseWorkspaceRules(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	privateRules := filepath.Join(root, "private-project-rules")
	require.NoError(t, os.WriteFile(privateRules, []byte("PRIVATE_WORKSPACE_RULE_MARKER"), 0o600))
	store := config.NewTestStore(&config.Config{Options: &config.Options{
		GlobalContextPaths: []string{privateRules}, ContextPaths: []string{privateRules},
	}})
	p, err := prompt.NewPrompt("agentic_fetch", corePromptTmpl+agenticFetchPromptTmpl, prompt.WithWorkingDir(root))
	require.NoError(t, err)
	built, err := p.Build(context.Background(), store)
	require.NoError(t, err)
	require.NotContains(t, built, "PRIVATE_WORKSPACE_RULE_MARKER")
	require.NotContains(t, built, privateRules)
}
