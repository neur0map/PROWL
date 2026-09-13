package agent

import (
	"context"
	_ "embed"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/openai"

	"github.com/neur0map/prowl/internal/message"
	"github.com/neur0map/prowl/internal/session"
)

// The style is adapted from ayghri/i-have-adhd; see templates/focus.LICENSE.
//
//go:embed templates/focus.md.tpl
var focusOnInstructions string

const focusOffInstructions = "Focus mode is off. Resume the normal response style and the user's requested format. This changes presentation only: preserve the full task, evidence, uncertainty, and verification obligations."

func previousFocusMode(history []message.Message) string {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == message.User {
			if mode := history[i].TurnSettings().FocusMode; mode != "" {
				return mode
			}
		}
	}
	return ""
}

func (a *sessionAgent) saveTurnSettings(ctx context.Context, user *message.Message, model Model, options fantasy.ProviderOptions, focusMode session.FocusMode, previousFocus string) error {
	settings := user.TurnSettings()
	original := settings
	settings.FocusMode = string(focusMode)
	if focusMode != "" && settings.FocusMode != previousFocus {
		switch focusMode {
		case session.FocusModeOn:
			settings.Instructions = focusOnInstructions
		case session.FocusModeOff:
			settings.Instructions = focusOffInstructions
		}
	}
	effort := openAIEffort(options)
	if effort != "" && model.ModelCfg.Provider != "" && model.ModelCfg.Model != "" {
		settings.Provider, settings.Model, settings.ReasoningEffort = model.ModelCfg.Provider, model.ModelCfg.Model, effort
	}
	if settings == original {
		return nil
	}
	replaced := false
	for i, part := range user.Parts {
		if _, ok := part.(message.TurnSettings); ok {
			user.Parts[i] = settings
			replaced = true
			break
		}
	}
	if !replaced {
		user.Parts = append(user.Parts, settings)
	}
	if err := a.messages.Update(ctx, *user); err != nil {
		return err
	}
	// The setting must survive cancellation or a restart after the request,
	// not just an eventual message-store debounce flush.
	return a.messages.Flush(ctx, user.ID)
}

func openAIEffort(options fantasy.ProviderOptions) string {
	switch options := options[openai.Name].(type) {
	case *openai.ResponsesProviderOptions:
		if options != nil && options.ReasoningEffort != nil {
			return string(*options.ReasoningEffort)
		}
	case *openai.ProviderOptions:
		if options != nil && options.ReasoningEffort != nil {
			return string(*options.ReasoningEffort)
		}
	}
	return ""
}

type reasoningCheckpoint struct {
	user   int
	effort string
}

func reasoningCheckpoints(prompt []fantasy.Message, policy promptCachePolicy) ([]reasoningCheckpoint, int) {
	var checkpoints []reasoningCheckpoint
	user := 0
	for _, item := range prompt {
		if item.Role != fantasy.MessageRoleUser {
			continue
		}
		settings, ok := item.ProviderOptions[message.TurnSettingsProvider].(*message.TurnSettings)
		if ok && settings.Provider == policy.providerID && settings.Model == policy.modelID {
			if !configurationEffort(settings.ReasoningEffort) {
				return nil, 0
			}
			checkpoints = append(checkpoints, reasoningCheckpoint{user: user, effort: settings.ReasoningEffort})
		}
		user++
	}
	return checkpoints, user
}

func configurationEffort(effort string) bool {
	switch effort {
	case "none", "minimal", "low", "medium", "high", "xhigh", "max":
		return true
	default:
		return false
	}
}

func applyConfigurationUpdates(root map[string]any, checkpoints []reasoningCheckpoint, expectedUsers int) {
	if len(checkpoints) == 0 || root["previous_response_id"] != nil || root["conversation"] != nil {
		return
	}
	reasoning := wireObject(root["reasoning"])
	current, _ := reasoning["effort"].(string)
	if current != checkpoints[len(checkpoints)-1].effort {
		// An unspecified default, model switch, or unrecorded override must
		// retain ordinary request-level semantics rather than guess a reset.
		return
	}
	input := wireArray(root["input"])
	users := 0
	for _, item := range input {
		if wireObject(item)["type"] == "configuration_update" {
			return
		}
		if wireObject(item)["role"] == "user" {
			users++
		}
	}
	if users != expectedUsers {
		return
	}
	baseline := checkpoints[0].effort
	changes := 0
	previous := baseline
	for _, checkpoint := range checkpoints[1:] {
		if checkpoint.effort != previous {
			changes++
			previous = checkpoint.effort
		}
	}
	if changes == 0 {
		return
	}
	updated := make([]any, 0, len(input)+changes)
	user, next := 0, 0
	previous = baseline
	for _, item := range input {
		if wireObject(item)["role"] == "user" {
			if next < len(checkpoints) && checkpoints[next].user == user {
				effort := checkpoints[next].effort
				if effort != previous {
					updated = append(updated, map[string]any{
						"type": "configuration_update", "reasoning": map[string]any{"effort": effort},
					})
					previous = effort
				}
				next++
			}
			user++
		}
		updated = append(updated, item)
	}
	reasoning["effort"] = baseline
	root["input"] = updated
}
