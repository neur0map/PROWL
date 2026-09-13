package agent

import (
	"context"
	_ "embed"

	"github.com/neur0map/prowl/internal/agent/prompt"
	"github.com/neur0map/prowl/internal/config"
)

//go:embed templates/core.md.tpl
var corePromptTmpl string

//go:embed templates/coder.md.tpl
var coderPromptTmpl string

//go:embed templates/task.md.tpl
var taskPromptTmpl string

//go:embed templates/initialize.md.tpl
var initializePromptTmpl string

func coderPrompt(opts ...prompt.Option) (*prompt.Prompt, error) {
	return prompt.NewPrompt("coder", corePromptTmpl+coderPromptTmpl, opts...)
}

func taskPrompt(opts ...prompt.Option) (*prompt.Prompt, error) {
	return prompt.NewPrompt("task", corePromptTmpl+taskPromptTmpl, opts...)
}

func InitializePrompt(cfg *config.ConfigStore) (string, error) {
	systemPrompt, err := prompt.NewPrompt("initialize", initializePromptTmpl)
	if err != nil {
		return "", err
	}
	return systemPrompt.Build(context.Background(), cfg)
}
