// Package reasoning defines request reasoning policy shared by the agent and UI.
package reasoning

import (
	"slices"
	"strings"

	"charm.land/catwalk/pkg/catwalk"
)

const (
	// Auto selects effort per prompt using the configured small model.
	Auto = "auto"
	// Ultrathink requests the highest supported effort for one prompt.
	Ultrathink = "ultrathink"
)

var effortOrder = [...]string{"off", "none", "minimal", "low", "medium", "high", "xhigh", "max", "on"}

// RequiresThinking reports known models whose API forbids disabling thinking.
// Other effort-only models express this by omitting off/none from their ladder.
func RequiresThinking(model catwalk.Model) bool {
	id := model.ID
	if i := strings.LastIndexByte(id, '/'); i >= 0 {
		id = id[i+1:]
	}
	return model.CanReason && (id == "gemini-2.5-pro" || strings.HasPrefix(id, "gemini-2.5-pro-"))
}

// Efforts returns the model's concrete controls in increasing effort order.
// A model without discrete levels exposes its existing off/on thinking toggle.
func Efforts(model catwalk.Model) []string {
	if !model.CanReason {
		return nil
	}
	if len(model.ReasoningLevels) == 0 {
		if RequiresThinking(model) {
			return []string{"on"}
		}
		return []string{"off", "on"}
	}
	levels := make([]string, 0, len(model.ReasoningLevels))
	for _, effort := range effortOrder {
		if slices.Contains(model.ReasoningLevels, effort) {
			levels = append(levels, effort)
		}
	}
	return levels
}

// HighestEffort returns the strongest concrete control the model advertises.
func HighestEffort(model catwalk.Model) string {
	if !model.CanReason {
		return ""
	}
	if len(model.ReasoningLevels) == 0 {
		return "on"
	}
	for i := len(effortOrder) - 1; i >= 0; i-- {
		if slices.Contains(model.ReasoningLevels, effortOrder[i]) {
			return effortOrder[i]
		}
	}
	return ""
}

// ClampEffort maps a requested level to the nearest supported level at or above
// it, or the model's maximum. Unknown requests use the model default or medium.
// On toggle-based models, low and below turn thinking off where supported;
// higher levels turn it on. This is a local policy, not a provider wire format.
func ClampEffort(model catwalk.Model, requested string) string {
	if !model.CanReason {
		return ""
	}
	rank := slices.Index(effortOrder[:], requested)
	if rank < 0 {
		rank = slices.Index(effortOrder[:], model.DefaultReasoningEffort)
		if rank < 0 {
			rank = slices.Index(effortOrder[:], "medium")
		}
	}
	if len(model.ReasoningLevels) == 0 {
		if rank <= slices.Index(effortOrder[:], "low") && !RequiresThinking(model) {
			return "off"
		}
		return "on"
	}
	for _, effort := range effortOrder[rank:] {
		if slices.Contains(model.ReasoningLevels, effort) {
			return effort
		}
	}
	return HighestEffort(model)
}
