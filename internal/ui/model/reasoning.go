package model

import (
	"context"
	"errors"

	tea "charm.land/bubbletea/v2"
	"charm.land/catwalk/pkg/catwalk"
	"github.com/neur0map/prowl/internal/agent/notify"
	"github.com/neur0map/prowl/internal/config"
	"github.com/neur0map/prowl/internal/reasoning"
	"github.com/neur0map/prowl/internal/ui/common"
	"github.com/neur0map/prowl/internal/ui/util"
	"github.com/neur0map/prowl/internal/workspace"
)

// changeReasoning is the entry point for the reasoning keybind and command.
// Every model that can reason opens the same picker: it offers Auto plus the
// model's concrete controls (discrete effort levels, or Off/On for a
// thinking-toggle model). Models with no reasoning control at all report a
// warning instead.
func (m *UI) changeReasoning() tea.Cmd {
	cfg := m.com.Config()
	if cfg == nil {
		return nil
	}
	agentCfg, ok := cfg.Agents[config.AgentCoder]
	if !ok {
		return nil
	}
	model := cfg.GetModelByType(agentCfg.Model)
	if model == nil || !model.CanReason {
		return util.ReportWarn("This model has no adjustable reasoning.")
	}
	return m.openReasoningDialog()
}

// applyReasoningSelection persists a deliberate reasoning choice for the coder
// model and refreshes the running agent. It records the exact selection
// (auto, a discrete level, or off/on) in reasoning_effort and keeps the legacy
// Think flag consistent for toggle-based models so older readers still work.
// All config I/O happens inside the command, never in Update.
func (m *UI) applyReasoningSelection(effort string) tea.Cmd {
	return m.updateAgentModelCmd(func() tea.Msg {
		cfg := m.com.Config()
		if cfg == nil {
			return util.ReportError(errors.New("configuration not found"))()
		}
		agentCfg, ok := cfg.Agents[config.AgentCoder]
		if !ok {
			return util.ReportError(errors.New("agent configuration not found"))()
		}
		selected := cfg.Models[agentCfg.Model]
		catModel := cfg.GetModelByType(agentCfg.Model)
		selected.ReasoningEffort = effort
		// Keep the boolean thinking flag aligned so a toggle-based model
		// still reads correctly through the legacy Think field.
		if catModel != nil && len(catModel.ReasoningLevels) == 0 {
			switch effort {
			case "on":
				selected.Think = true
			case "off":
				selected.Think = false
			}
		}
		if err := m.com.Workspace.UpdatePreferredModel(config.ScopeGlobal, agentCfg.Model, selected); err != nil {
			return util.ReportError(err)()
		}
		if err := m.com.Workspace.UpdateAgentModel(context.Background()); err != nil {
			return util.NewErrorMsg(err)
		}
		return util.NewInfoMsg(reasoningSelectionMessage(effort))
	})
}

// reasoningSelectionMessage renders the confirmation shown after a picker
// selection lands.
func reasoningSelectionMessage(effort string) string {
	switch effort {
	case reasoning.Auto:
		return "Reasoning set to Auto"
	case "on":
		return "Thinking enabled"
	case "off":
		return "Thinking disabled"
	default:
		return "Reasoning effort set to " + common.FormatReasoningEffort(effort)
	}
}

// activeReasoning captures the in-flight reasoning a run reported through
// notify.TypeReasoningChanged. It backs the live sidebar readout and is keyed
// by session; the turnID lets a stale end event be ignored so a newer run's
// display is never cleared out from under it.
type activeReasoning struct {
	turnID   string
	mode     string // auto | ultrathink | manual
	effort   string // resolved concrete effort
	modelID  string
	provider string
}

// handleReasoningChanged folds a notify.TypeReasoningChanged event into the
// per-session live-reasoning cache. A resolved event (non-empty effort)
// records the active mode; an end event (empty effort) clears it only when its
// turnID matches the shown run, so an out-of-order end for a superseded run is
// dropped.
func (m *UI) handleReasoningChanged(n notify.Notification) {
	if n.SessionID == "" {
		return
	}
	if m.reasoningActive == nil {
		m.reasoningActive = make(map[string]activeReasoning)
	}
	if n.ReasoningEffort == "" {
		if cur, ok := m.reasoningActive[n.SessionID]; ok && cur.turnID == n.ReasoningTurnID {
			delete(m.reasoningActive, n.SessionID)
		}
		return
	}
	m.reasoningActive[n.SessionID] = activeReasoning{
		turnID:   n.ReasoningTurnID,
		mode:     n.ReasoningMode,
		effort:   n.ReasoningEffort,
		modelID:  n.ModelID,
		provider: n.ProviderID,
	}
}

// activeReasoningForSession returns the live reasoning state for the current
// session, if any.
func (m *UI) activeReasoningForSession() (activeReasoning, bool) {
	if m.reasoningActive == nil || m.session == nil {
		return activeReasoning{}, false
	}
	a, ok := m.reasoningActive[m.session.ID]
	return a, ok
}

// reasoningDisplay computes the sidebar reasoning line and whether it should
// get the high-effort "neon" treatment. Precedence: an active run's resolved
// state (the real current behavior) wins; otherwise, while idle, a live
// ultrathink draft previews as a one-request override; otherwise the
// persistent saved setting shows. A queued override is never rendered as the
// active mode, and a nonreason model never claims a boosted capability.
func (m *UI) reasoningDisplay(model *workspace.AgentModel) (string, bool) {
	cat := model.CatwalkCfg
	sel := model.ModelCfg
	if !cat.CanReason {
		return "", false
	}

	if act, ok := m.activeReasoningForSession(); ok &&
		act.modelID == sel.Model && act.provider == sel.Provider {
		switch act.mode {
		case reasoning.Ultrathink:
			return "Ultrathink (" + shortEffortLabel(cat, act.effort) + ")", true
		case reasoning.Auto:
			return "Auto (" + shortEffortLabel(cat, act.effort) + ")", isHighEffort(act.effort)
		default:
			return manualEffortDisplay(cat, act.effort)
		}
	}

	if !m.isAgentBusy() && m.draftHasUltrathink() {
		return "Ultrathink (" + shortEffortLabel(cat, reasoning.HighestEffort(cat)) + ") · this request", true
	}

	return savedReasoningDisplay(cat, sel)
}

// savedReasoningDisplay renders the persistent reasoning setting stored in
// config for the given model.
func savedReasoningDisplay(cat catwalk.Model, sel config.SelectedModel) (string, bool) {
	if sel.ReasoningEffort == reasoning.Auto {
		return "Auto", false
	}
	if len(cat.ReasoningLevels) == 0 {
		// Route through ClampEffort so a mandatory-thinking model (which has
		// no real Off) reads as On even when the legacy Think flag is unset.
		requested := sel.ReasoningEffort
		if requested == "" {
			if sel.Think {
				requested = "on"
			} else {
				requested = "off"
			}
		}
		if reasoning.ClampEffort(cat, requested) == "on" {
			return "Thinking On", true
		}
		return "Thinking Off", false
	}
	effort := sel.ReasoningEffort
	if effort == "" {
		effort = cat.DefaultReasoningEffort
	}
	return "Reasoning " + common.FormatReasoningEffort(effort), isHighEffort(effort)
}

// manualEffortDisplay renders a concrete resolved effort as a full sidebar
// line.
func manualEffortDisplay(cat catwalk.Model, effort string) (string, bool) {
	if len(cat.ReasoningLevels) == 0 {
		if effort == "on" {
			return "Thinking On", true
		}
		return "Thinking Off", false
	}
	return "Reasoning " + common.FormatReasoningEffort(effort), isHighEffort(effort)
}

// shortEffortLabel renders a resolved effort compactly, for embedding in the
// Auto readout.
func shortEffortLabel(cat catwalk.Model, effort string) string {
	if len(cat.ReasoningLevels) == 0 {
		if effort == "on" {
			return "On"
		}
		return "Off"
	}
	return common.FormatReasoningEffort(effort)
}

// isHighEffort reports whether an effort warrants the high-effort treatment.
func isHighEffort(effort string) bool {
	switch effort {
	case "high", "xhigh", "max", "on":
		return true
	default:
		return false
	}
}
