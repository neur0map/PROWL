package agent

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"golang.org/x/sync/errgroup"

	"github.com/neur0map/prowl/internal/agent/hyper"
	"github.com/neur0map/prowl/internal/agent/notify"
	"github.com/neur0map/prowl/internal/agent/prompt"
	"github.com/neur0map/prowl/internal/agent/tools"
	"github.com/neur0map/prowl/internal/agent/tools/mcp"
	"github.com/neur0map/prowl/internal/config"
	"github.com/neur0map/prowl/internal/discover"
	"github.com/neur0map/prowl/internal/event"
	"github.com/neur0map/prowl/internal/filetracker"
	"github.com/neur0map/prowl/internal/gateway"
	"github.com/neur0map/prowl/internal/goals"
	"github.com/neur0map/prowl/internal/history"
	"github.com/neur0map/prowl/internal/hooks"
	"github.com/neur0map/prowl/internal/log"
	"github.com/neur0map/prowl/internal/lsp"
	"github.com/neur0map/prowl/internal/message"
	"github.com/neur0map/prowl/internal/oauth"
	anthropicoauth "github.com/neur0map/prowl/internal/oauth/anthropic"
	"github.com/neur0map/prowl/internal/oauth/copilot"
	openaioauth "github.com/neur0map/prowl/internal/oauth/openai"
	"github.com/neur0map/prowl/internal/permission"
	"github.com/neur0map/prowl/internal/prowlagent"
	"github.com/neur0map/prowl/internal/pubsub"
	"github.com/neur0map/prowl/internal/question"
	"github.com/neur0map/prowl/internal/reasoning"
	"github.com/neur0map/prowl/internal/session"
	"github.com/neur0map/prowl/internal/skills"

	"charm.land/fantasy/providers/anthropic"
	"charm.land/fantasy/providers/azure"
	"charm.land/fantasy/providers/bedrock"
	"charm.land/fantasy/providers/google"
	"charm.land/fantasy/providers/openai"
	"charm.land/fantasy/providers/openaicompat"
	"charm.land/fantasy/providers/openrouter"
	"charm.land/fantasy/providers/vercel"
	openaisdk "github.com/openai/openai-go/v3/option"
	"github.com/qjebbs/go-jsons"
)

// Coordinator errors.
var (
	errCoderAgentNotConfigured         = errors.New("coder agent not configured")
	errModelProviderNotConfigured      = errors.New("model provider not configured")
	errLargeModelNotSelected           = errors.New("large model not selected")
	errSmallModelNotSelected           = errors.New("small model not selected")
	errLargeModelProviderNotConfigured = errors.New("large model provider not configured")
	errSmallModelProviderNotConfigured = errors.New("small model provider not configured")
	errLargeModelNotFound              = errors.New("large model not found in provider config")
	errSmallModelNotFound              = errors.New("small model not found in provider config")
)

// Copilot models that use the Responses API instead of Chat Completions.
var copilotResponsesModels = map[string]bool{
	"gpt-5.2":       true,
	"gpt-5.2-codex": true,
	"gpt-5.3-codex": true,
	"gpt-5.4":       true,
	"gpt-5.4-mini":  true,
	"gpt-5.5":       true,
	"gpt-5-mini":    true,
	"gpt-5.6-luna":  true,
	"gpt-5.6-terra": true,
	"gpt-5.6-sol":   true,
	"gpt-6-astra":   true,
	"grok-4.5":      true,
	"grok-4.6":      true,
}

// OpenCode models that use the Anthropic Messages API instead of Chat
// Completions. Which endpoint serves each model differs per provider, see
// https://opencode.ai/docs/zen and https://opencode.ai/docs/go.
func isOpenCodeMessagesModel(providerID, modelID string) bool {
	switch providerID {
	case string(catwalk.InferenceProviderOpenCodeGo):
		return strings.HasPrefix(modelID, "minimax-") ||
			strings.HasPrefix(modelID, "qwen3.6-") ||
			strings.HasPrefix(modelID, "qwen3.7-") ||
			strings.HasPrefix(modelID, "qwen3.8-")
	case string(catwalk.InferenceProviderOpenCodeZen):
		return strings.HasPrefix(modelID, "claude-") ||
			strings.HasPrefix(modelID, "qwen3.5-") ||
			strings.HasPrefix(modelID, "qwen3.6-") ||
			strings.HasPrefix(modelID, "qwen3.7-") ||
			strings.HasPrefix(modelID, "qwen3.8-")
	}
	return false
}

// OpenCode models that use the OpenAI Responses API instead of Chat
// Completions. See https://opencode.ai/docs/zen and https://opencode.ai/docs/go.
func isOpenCodeResponsesModel(modelID string) bool {
	return strings.HasPrefix(modelID, "gpt-") ||
		strings.HasPrefix(modelID, "grok-") ||
		strings.HasPrefix(modelID, "muse-spark-")
}

type Coordinator interface {
	// INFO: (kujtim) this is not used yet we will use this when we have multiple agents
	// SetMainAgent(string)
	Run(ctx context.Context, sessionID, prompt string, attachments ...message.Attachment) (*fantasy.AgentResult, error)
	// RunAccepted runs a call that was already accepted via
	// BeginAccepted on the fire-and-forget dispatch path. The handle is
	// the only carrier of accept-state across the backend.runAgent /
	// Coordinator / sessionAgent.Run layers: it reaches
	// sessionAgent.Run as SessionAgentCall.Accepted, where it is
	// consumed under dispatchMu once the accepted -> (cancel-on-entry |
	// queued | active) transition is chosen.
	RunAccepted(ctx context.Context, accept *AcceptedRun, sessionID, prompt string, attachments ...message.Attachment) (*fantasy.AgentResult, error)
	BeginAccepted(sessionID string) *AcceptedRun
	Cancel(sessionID string)
	CancelAll()
	IsSessionBusy(sessionID string) bool
	IsBusy() bool
	QueuedPrompts(sessionID string) int
	QueuedPromptsList(sessionID string) []string
	ClearQueue(sessionID string)
	Summarize(context.Context, string) error
	Model() Model
	UpdateModels(ctx context.Context) error
	GenerateTitle(ctx context.Context, sessionID, prompt string)
}

type coordinator struct {
	cfg         *config.ConfigStore
	sessions    session.Service
	goals       *goals.Service
	messages    message.Service
	permissions permission.Service
	questions   question.Service
	history     history.Service
	filetracker filetracker.Service
	lspManager  *lsp.Manager
	notify      pubsub.Publisher[notify.Notification]
	runComplete pubsub.Publisher[notify.RunComplete]
	interactive bool
	cacheGate   chan struct{}

	currentAgent SessionAgent
	agents       map[string]SessionAgent

	// Skills discovery results (session-start snapshot).
	allSkills    []*skills.Skill // Pre-filter: all discovered after dedup.
	activeSkills []*skills.Skill // Post-filter: active skills only.
	skillTracker *skills.Tracker
	// skillAdvisor points the model at a skill whose globs claim a file it
	// just edited; nil when no active skill declares any.
	skillAdvisor *skillAdvisor
	skillsMgr    *skills.Manager // Source of truth for live refresh; may be nil.

	// routingAdvisor rate-limits the code-index reminder across every tool
	// call in this process, so its counter is shared by all agents rather
	// than reset per turn.
	routingAdvisor *routingAdvisor

	readyWg errgroup.Group
}

// CoordinatorOptions holds the dependencies for NewCoordinator. Using a
// struct keeps the constructor self-documenting and avoids a long
// positional parameter list.
type CoordinatorOptions struct {
	Config      *config.ConfigStore
	Sessions    session.Service
	Goals       *goals.Service
	Messages    message.Service
	Permissions permission.Service
	Questions   question.Service
	History     history.Service
	FileTracker filetracker.Service
	LSPManager  *lsp.Manager
	Notify      pubsub.Publisher[notify.Notification]
	RunComplete pubsub.Publisher[notify.RunComplete]
	Skills      *skills.Manager
	Interactive bool
}

func NewCoordinator(ctx context.Context, opts CoordinatorOptions) (Coordinator, error) {
	// Skills are pre-discovered by the caller (see app.New /
	// backend.CreateWorkspace) and passed in via the manager. If no
	// manager was provided (legacy callers), fall back to an in-line
	// discovery so the coordinator still works.
	var allSkills, activeSkills []*skills.Skill
	if opts.Skills != nil {
		allSkills = opts.Skills.AllSkills()
		activeSkills = opts.Skills.ActiveSkills()
	} else {
		allSkills, activeSkills = discoverSkills(opts.Config)
	}
	skillTracker := skills.NewTracker(activeSkills)

	c := &coordinator{
		cfg:          opts.Config,
		sessions:     opts.Sessions,
		goals:        opts.Goals,
		messages:     opts.Messages,
		permissions:  opts.Permissions,
		questions:    opts.Questions,
		history:      opts.History,
		filetracker:  opts.FileTracker,
		lspManager:   opts.LSPManager,
		notify:       opts.Notify,
		runComplete:  opts.RunComplete,
		agents:       make(map[string]SessionAgent),
		allSkills:    allSkills,
		activeSkills: activeSkills,
		skillTracker: skillTracker,
		skillsMgr:    opts.Skills,
		interactive:  opts.Interactive,
		cacheGate:    make(chan struct{}, 1),

		routingAdvisor: newRoutingAdvisor(opts.Config.WorkingDir()),
		skillAdvisor:   newSkillAdvisor(activeSkills),
	}

	agentCfg, ok := opts.Config.Config().Agents[config.AgentCoder]
	if !ok {
		return nil, errCoderAgentNotConfigured
	}

	// TODO: make this dynamic when we support multiple agents
	prompt, err := coderPrompt(prompt.WithWorkingDir(c.cfg.WorkingDir()))
	if err != nil {
		return nil, err
	}

	agent, err := c.buildAgent(ctx, prompt, agentCfg, false)
	if err != nil {
		return nil, err
	}
	c.currentAgent = agent
	c.agents[config.AgentCoder] = agent
	return c, nil
}

// Run implements Coordinator.
func (c *coordinator) Run(ctx context.Context, sessionID string, prompt string, attachments ...message.Attachment) (*fantasy.AgentResult, error) {
	return c.run(ctx, nil, sessionID, prompt, attachments...)
}

// RunAccepted implements Coordinator.
func (c *coordinator) RunAccepted(ctx context.Context, accept *AcceptedRun, sessionID string, prompt string, attachments ...message.Attachment) (*fantasy.AgentResult, error) {
	return c.run(ctx, accept, sessionID, prompt, attachments...)
}

// run is the shared implementation behind Run and RunAccepted. When
// accept is non-nil it is threaded onto the SessionAgentCall as
// Accepted so sessionAgent.Run can consume the accept reservation under
// dispatchMu; when nil (the in-process/local path) no accept tracking
// applies.
func (c *coordinator) run(ctx context.Context, accept *AcceptedRun, sessionID string, prompt string, attachments ...message.Attachment) (*fantasy.AgentResult, error) {
	if err := c.readyWg.Wait(); err != nil {
		return nil, err
	}

	// MCP servers connect asynchronously (see mcp.Initialize).
	//
	// Interactive runs never wait for that to finish: the tool list below
	// is built from whatever is registered right now, servers still
	// connecting are simply absent from this run's palette, and they are
	// picked up by later runs once they register and publish
	// EventToolsListChanged. Blocking here froze the TUI for the duration
	// of the slowest server's connect timeout whenever a prompt was sent
	// before initialization finished — most visibly on the first message.
	//
	// Non-interactive runs get a single shot at the tool palette, so they
	// do wait for initialization to settle — but bounded by InitWaitBudget
	// rather than each server's connect timeout, so a server wedged
	// mid-handshake cannot stall a headless run for minutes. Past the
	// budget the turn proceeds without the stragglers; their tools simply
	// stay absent from this run.
	if !c.interactive {
		if err := mcp.WaitForInitBudget(ctx, mcp.InitWaitBudget); err != nil {
			return nil, fmt.Errorf("failed to wait for MCP initialization: %w", err)
		}
	}

	// refresh models before each run
	if err := c.UpdateModels(ctx); err != nil {
		return nil, fmt.Errorf("failed to update models: %w", err)
	}

	model := c.currentAgent.Model()
	maxTokens := model.CatwalkCfg.DefaultMaxTokens
	if model.ModelCfg.MaxTokens != 0 {
		maxTokens = model.ModelCfg.MaxTokens
	}

	providerCfg, ok := c.cfg.Config().Providers.Get(model.ModelCfg.Provider)
	if !ok {
		return nil, errModelProviderNotConfigured
	}

	mergedOptions, temp, topP, topK, freqPenalty, presPenalty := mergeCallOptions(model, providerCfg)

	// shapeReasoning rebuilds this run's provider options for a resolved
	// per-turn reasoning effort. sessionAgent.Run calls it once the run is
	// active (never a merely queued prompt), using a run-local copy of the
	// model so no persisted config or SetModels state is mutated. The
	// inactive override reproduces mergedOptions exactly, so a plain manual
	// turn is unchanged.
	shapeReasoning := func(override reasoningOverride) fantasy.ProviderOptions {
		if !override.active {
			return mergedOptions
		}
		override.maxOut = maxTokens
		runModel := model
		if len(model.CatwalkCfg.ReasoningLevels) > 0 {
			runModel.ModelCfg.ReasoningEffort = override.effort
		} else {
			runModel.ModelCfg.ReasoningEffort = ""
		}
		runModel.ModelCfg.Think = override.effort != "" &&
			override.effort != "off" && override.effort != "none"
		return getProviderOptions(runModel, providerCfg, override)
	}

	if err := c.refreshTokenIfExpired(ctx, providerCfg); err != nil {
		// NOTE(@andreynering): We don't return here because the event handling to ask the user to reauthenticate
		// depends on the flow below. If refresh fails, proceed with the token we have.
		slog.Error("Failed to refresh OAuth2 token. Proceeding with existing token.", "error", err)
	}

	// Coalesce per-attempt RunComplete payloads so only the final
	// outcome reaches subscribers. Without this, the first attempt's
	// failed RunComplete (unauthorized) would race ahead of the
	// retry's success, and `prowl run` would exit on the stale error
	// before ever seeing the retry result. Each attempt's
	// SessionAgentCall.OnComplete hook overwrites latest; we publish
	// exactly once after retries resolve, via PublishMustDeliver, so
	// a momentarily-full subscriber buffer can't silently drop the
	// terminal event.
	var (
		latest    notify.RunComplete
		hasLatest bool
	)
	onComplete := func(rc notify.RunComplete) {
		latest = rc
		hasLatest = true
	}
	// Propagate the caller-supplied RunID (set via agent.WithRunID
	// at the HTTP boundary in backend.SendMessage) onto the
	// SessionAgentCall so the terminal RunComplete event echoes it
	// back. Both attempts in the retry chain reuse the same RunID;
	// the coalesce closure publishes the final outcome under that
	// same correlator.
	runID := RunIDFromContext(ctx)
	run := func() (*fantasy.AgentResult, error) {
		return c.currentAgent.Run(ctx, SessionAgentCall{
			SessionID:        sessionID,
			RunID:            runID,
			Prompt:           prompt,
			Attachments:      attachments,
			MaxOutputTokens:  maxTokens,
			ProviderOptions:  mergedOptions,
			Temperature:      temp,
			TopP:             topP,
			TopK:             callTopK(providerCfg, topK),
			FrequencyPenalty: freqPenalty,
			PresencePenalty:  presPenalty,
			OnComplete:       onComplete,
			Accepted:         accept,
			OnAuthRefresh:    c.makeAuthRefreshCallback(providerCfg),
			ShapeReasoning:   shapeReasoning,
		})
	}
	beforeLoaded := c.skillTracker.LoadedNames()
	result, originalErr := run()
	logTurnSkillUsage(sessionID, prompt, c.activeSkills, c.skillTracker, beforeLoaded)

	// Notify only if still unauthorized after retry — a successful
	// retry means the user doesn't need to re-authenticate. AWS SSO is
	// handled transparently inside OnAuthRefresh, so it needs no post-run
	// notification here.
	if originalErr != nil && isUnauthorized(originalErr) && c.notify != nil && model.ModelCfg.Provider == hyper.Name {
		c.notify.Publish(pubsub.CreatedEvent, notify.Notification{
			Type:       notify.TypeReAuthenticate,
			ProviderID: model.ModelCfg.Provider,
		})
	}

	if hasLatest && c.runComplete != nil {
		c.runComplete.PublishMustDeliver(ctx, pubsub.UpdatedEvent, latest)
		// Signal to the dispatcher (backend.runAgent) that the
		// authoritative terminal RunComplete for this run was already
		// emitted, so it does not publish a duplicate fallback for the
		// error it is about to receive.
		MarkRunCompletePublished(ctx)
	}
	return result, originalErr
}

// effectiveReasoningEffort returns the reasoning effort to apply for provider calls.
// It prefers the user-selected effort when valid, otherwise the model default when
// valid, and finally falls back to the first configured reasoning level.
func effectiveReasoningEffort(model Model) string {
	if !model.CatwalkCfg.CanReason {
		return ""
	}

	if effort := model.ModelCfg.ReasoningEffort; effort != "" && slices.Contains(model.CatwalkCfg.ReasoningLevels, effort) {
		return effort
	}
	if effort := model.CatwalkCfg.DefaultReasoningEffort; effort != "" && slices.Contains(model.CatwalkCfg.ReasoningLevels, effort) {
		return effort
	}
	if len(model.CatwalkCfg.ReasoningLevels) > 0 {
		return model.CatwalkCfg.ReasoningLevels[0]
	}
	return ""
}

func getProviderOptions(model Model, providerCfg config.ProviderConfig, override reasoningOverride) fantasy.ProviderOptions {
	if len(model.CatwalkCfg.ReasoningLevels) == 0 {
		switch model.ModelCfg.ReasoningEffort {
		case "on":
			model.ModelCfg.Think = true
		case "off":
			model.ModelCfg.Think = false
		}
		model.ModelCfg.Think = model.ModelCfg.Think || reasoning.RequiresThinking(model.CatwalkCfg)
	}
	options := fantasy.ProviderOptions{}

	cfgOpts := []byte("{}")
	providerCfgOpts := []byte("{}")
	catwalkOpts := []byte("{}")

	if model.ModelCfg.ProviderOptions != nil {
		data, err := json.Marshal(model.ModelCfg.ProviderOptions)
		if err == nil {
			cfgOpts = data
		}
	}

	if providerCfg.ProviderOptions != nil {
		data, err := json.Marshal(providerCfg.ProviderOptions)
		if err == nil {
			providerCfgOpts = data
		}
	}

	if model.CatwalkCfg.Options.ProviderOptions != nil {
		data, err := json.Marshal(model.CatwalkCfg.Options.ProviderOptions)
		if err == nil {
			catwalkOpts = data
		}
	}

	readers := []io.Reader{
		bytes.NewReader(catwalkOpts),
		bytes.NewReader(providerCfgOpts),
		bytes.NewReader(cfgOpts),
	}

	got, err := jsons.Merge(readers)
	if err != nil {
		slog.Error("Could not merge call config", "err", err)
		return options
	}

	mergedOptions := make(map[string]any)

	err = json.Unmarshal([]byte(got), &mergedOptions)
	if err != nil {
		slog.Error("Could not create config for call", "err", err)
		return options
	}

	// A temporary per-turn reasoning decision (auto/ultrathink) must win over
	// the persisted provider_options reasoning fields while every other user
	// option is preserved. Drop only the reasoning-related keys so the
	// branches below re-derive them from this turn's resolved effort.
	if override.active {
		delete(mergedOptions, "reasoning_effort")
		delete(mergedOptions, "effort")
		delete(mergedOptions, "thinking")
		if routed, ok := mergedOptions["reasoning"].(map[string]any); ok {
			delete(routed, "effort")
			delete(routed, "enabled")
			delete(routed, "max_tokens")
		} else {
			delete(mergedOptions, "reasoning")
		}
		delete(mergedOptions, "thinking_config")
		if extraBody, ok := mergedOptions["extra_body"].(map[string]any); ok {
			delete(extraBody, "reasoning")
			delete(extraBody, "reasoning_effort")
			delete(extraBody, "thinking")
			delete(extraBody, "enable_thinking")
		}
	}

	reasoningEffort := effectiveReasoningEffort(model)
	shouldSetEffort := model.CatwalkCfg.CanReason &&
		reasoningEffort != "" &&
		slices.Contains(model.CatwalkCfg.ReasoningLevels, reasoningEffort)

	switch providerCfg.Type {
	case openai.Name, azure.Name:
		_, hasReasoningEffort := mergedOptions["reasoning_effort"]
		if !hasReasoningEffort && shouldSetEffort {
			mergedOptions["reasoning_effort"] = reasoningEffort
		}
		nativeFutureResponses := providerCfg.Type == openai.Name && openAIModelAtLeast(model.CatwalkCfg.ID, 6, 0)
		if openai.IsResponsesModel(model.CatwalkCfg.ID) || nativeFutureResponses {
			if openai.IsResponsesReasoningModel(model.CatwalkCfg.ID) || nativeFutureResponses {
				mergedOptions["reasoning_summary"] = "auto"
				mergedOptions["include"] = []openai.IncludeType{openai.IncludeReasoningEncryptedContent}
			}
			parsed, err := openai.ParseResponsesOptions(mergedOptions)
			if err == nil {
				options[openai.Name] = parsed
			}
		} else {
			parsed, err := openai.ParseOptions(mergedOptions)
			if err == nil {
				options[openai.Name] = parsed
			}
		}

	case anthropic.Name, bedrock.Name:
		var (
			_, hasEffort = mergedOptions["effort"]
			_, hasThink  = mergedOptions["thinking"]
			extraBody, _ = mergedOptions["extra_body"].(map[string]any)
		)
		if extraBody == nil {
			extraBody = make(map[string]any)
		}

		switch providerCfg.ID {
		case string(catwalk.InferenceProviderAlibabaSingapore), string(catwalk.InferenceProviderAlibabaUS):
			switch {
			case !hasEffort && shouldSetEffort:
				extraBody["reasoning_effort"] = reasoningEffort
			case !hasThink && model.CatwalkCfg.CanReason:
				if model.ModelCfg.Think {
					extraBody["thinking"] = map[string]any{"type": "enabled"}
				} else {
					extraBody["thinking"] = map[string]any{"type": "disabled"}
				}
			}
			mergedOptions["extra_body"] = extraBody

		default:
			switch {
			case !hasEffort && shouldSetEffort:
				mergedOptions["effort"] = reasoningEffort
			case !hasThink && model.ModelCfg.Think:
				// A temporary ultrathink/auto decision raises the actual
				// thinking budget within the model and output bounds instead
				// of leaving the persisted default in place.
				budget := 2000
				if override.active {
					budget = reasoningThinkingBudget(model.CatwalkCfg, override)
				}
				mergedOptions["thinking"] = map[string]any{"budget_tokens": budget}
			}
		}

		parsed, err := anthropic.ParseOptions(mergedOptions)
		if err == nil {
			options[anthropic.Name] = parsed
		}

	case openrouter.Name:
		_, hasReasoning := mergedOptions["reasoning"]
		if model.CatwalkCfg.CanReason && (!hasReasoning || override.active) {
			routed, _ := mergedOptions["reasoning"].(map[string]any)
			if routed == nil {
				routed = make(map[string]any)
			}
			routed["enabled"] = model.ModelCfg.Think || shouldSetEffort
			if shouldSetEffort {
				routed["enabled"] = reasoningEffort != "none" && reasoningEffort != "off"
				routed["effort"] = reasoningEffort
			} else if override.active && model.ModelCfg.Think {
				routed["max_tokens"] = reasoningThinkingBudget(model.CatwalkCfg, override)
			}
			mergedOptions["reasoning"] = routed
		}
		parsed, err := openrouter.ParseOptions(mergedOptions)
		if err == nil {
			options[openrouter.Name] = parsed
		}

	case vercel.Name:
		_, hasReasoning := mergedOptions["reasoning"]
		if model.CatwalkCfg.CanReason && (!hasReasoning || override.active) {
			routed, _ := mergedOptions["reasoning"].(map[string]any)
			if routed == nil {
				routed = make(map[string]any)
			}
			routed["enabled"] = model.ModelCfg.Think || shouldSetEffort
			if shouldSetEffort {
				routed["enabled"] = reasoningEffort != "none" && reasoningEffort != "off"
				routed["effort"] = reasoningEffort
			} else if override.active && model.ModelCfg.Think {
				routed["max_tokens"] = reasoningThinkingBudget(model.CatwalkCfg, override)
			}
			mergedOptions["reasoning"] = routed
		}
		parsed, err := vercel.ParseOptions(mergedOptions)
		if err == nil {
			options[vercel.Name] = parsed
		}

	case google.Name:
		_, hasReasoning := mergedOptions["thinking_config"]
		if !hasReasoning {
			if strings.HasPrefix(path.Base(model.CatwalkCfg.ID), "gemini-2") {
				// A temporary ultrathink/auto decision drives the actual
				// thinking budget (0 disables where allowed, mandatory Pro
				// floors to its minimum) instead of the persisted default.
				budget := 0
				if model.ModelCfg.Think {
					budget = 2000
				}
				if override.active {
					budget = reasoningThinkingBudget(model.CatwalkCfg, override)
				}
				mergedOptions["thinking_config"] = map[string]any{
					"thinking_budget":  budget,
					"include_thoughts": true,
				}
			} else {
				mergedOptions["thinking_config"] = map[string]any{
					"thinking_level":   reasoningEffort,
					"include_thoughts": true,
				}
			}
		}
		parsed, err := google.ParseOptions(mergedOptions)
		if err == nil {
			options[google.Name] = parsed
		}

	case openaicompat.Name, hyper.Name:
		extraBody, _ := mergedOptions["extra_body"].(map[string]any)
		if extraBody == nil {
			extraBody = make(map[string]any)
		}

		_, hasReasoningEffort := mergedOptions["reasoning_effort"]
		if !hasReasoningEffort && shouldSetEffort {
			switch providerCfg.ID {
			case string(catwalk.InferenceProviderIoNet):
				extraBody["reasoning"] = map[string]string{"effort": reasoningEffort}
			case string(catwalk.InferenceProviderOpenCodeGo), string(catwalk.InferenceProviderOpenCodeZen):
				// MiniMax models use the "thinking" parameter instead of
				// "reasoning_effort". Other models on these providers still
				// use the standard field.
				if !strings.HasPrefix(strings.ToLower(model.CatwalkCfg.ID), "minimax") {
					mergedOptions["reasoning_effort"] = reasoningEffort
				}
			default:
				mergedOptions["reasoning_effort"] = reasoningEffort
			}
		}

		// "reasoning effort" is a standard OpenAI field, but "thinking" is not.
		// Setting it in the right way for each provider.
		// TODO: Abstract this in Fantasy somehow?
		// TODO: Allow custom providers to specify how to set this?
		switch providerCfg.ID {
		case hyper.Name:
			extraBody["thinking"] = model.ModelCfg.Think
		case string(catwalk.InferenceProviderIoNet):
			if _, ok := extraBody["reasoning"]; !ok && model.CatwalkCfg.CanReason {
				if model.ModelCfg.Think {
					extraBody["reasoning"] = map[string]string{"effort": "medium"}
				} else {
					extraBody["reasoning"] = map[string]string{"effort": "none"}
				}
			}

		case string(catwalk.InferenceProviderZAI), string(catwalk.InferenceProviderDeepSeek):
			if model.ModelCfg.Think || reasoningEffort != "" {
				extraBody["thinking"] = map[string]any{"type": "enabled"}
			} else {
				extraBody["thinking"] = map[string]any{"type": "disabled"}
			}

		case string(catwalk.InferenceProviderFireworks):
			// NOTE: Fireworks break if we set both `reasoning_effort` and `thinking`.
			if reasoningEffort == "" {
				if model.ModelCfg.Think {
					extraBody["thinking"] = map[string]any{"type": "enabled"}
				} else {
					extraBody["thinking"] = map[string]any{"type": "disabled"}
				}
			}

		case string(catwalk.InferenceProviderBaseten):
			extraBody["chat_template_args"] = map[string]any{
				"enable_thinking": model.ModelCfg.Think || reasoningEffort != "" && reasoningEffort != "none",
			}

		case string(catwalk.InferenceProviderOpenCodeGo), string(catwalk.InferenceProviderOpenCodeZen):
			// MiniMax M3 uses the "thinking" parameter to control reasoning.
			// "reasoning_split" must be true so thinking content is returned
			// in the "reasoning_content" field instead of inline in "content".
			if strings.HasPrefix(strings.ToLower(model.CatwalkCfg.ID), "minimax") {
				if model.CatwalkCfg.CanReason && (model.ModelCfg.Think || reasoningEffort != "") {
					extraBody["thinking"] = map[string]any{"type": "adaptive"}
					extraBody["reasoning_split"] = true
				} else {
					extraBody["thinking"] = map[string]any{"type": "disabled"}
				}
			}

		case string(catwalk.InferenceProviderAlibabaSingapore), string(catwalk.InferenceProviderAlibabaUS):
			if model.CatwalkCfg.CanReason && !shouldSetEffort {
				extraBody["enable_thinking"] = model.ModelCfg.Think
			}
		}

		mergedOptions["extra_body"] = extraBody

		parsed, err := openaicompat.ParseOptions(mergedOptions)
		if err == nil {
			options[openaicompat.Name] = parsed
		}

	default:
		// Known custom providers are openai-compat under the hood.
		if discover.IsKnownCustomProvider(string(providerCfg.Type)) {
			if _, explicit := mergedOptions["reasoning_effort"]; !explicit && shouldSetEffort {
				mergedOptions["reasoning_effort"] = reasoningEffort
			}
			// Set "top_k" under "extra_body", as it is not part of the OpenAI protocol
			// and will be explicitly omitted by Fantasy downstream.
			topK := cmp.Or(model.ModelCfg.TopK, model.CatwalkCfg.Options.TopK)
			if topK != nil {
				extraBody, hasExtraBody := mergedOptions["extra_body"].(map[string]any)
				if !hasExtraBody {
					extraBody = make(map[string]any)
					mergedOptions["extra_body"] = extraBody
				}
				if _, hasTopK := extraBody["top_k"]; !hasTopK {
					extraBody["top_k"] = *topK
				}
			}

			parsed, err := openaicompat.ParseOptions(mergedOptions)
			if err == nil {
				options[openaicompat.Name] = parsed
			} else {
				if topK != nil {
					slog.Warn(
						"Failed to parse provider_options, falling back to top_k only",
						"provider", providerCfg.ID,
						"error", err,
					)

					fallbackMergeOptions := map[string]any{
						"extra_body": map[string]any{"top_k": *topK},
					}
					parsed, err := openaicompat.ParseOptions(fallbackMergeOptions)
					if err == nil {
						options[openaicompat.Name] = parsed
					} else {
						slog.Warn(
							"Failed to parse fallback provider options, this should never happen",
							"provider", providerCfg.ID,
							"error", err,
						)
					}
				}
			}
		}
	}

	return options
}

func mergeCallOptions(model Model, cfg config.ProviderConfig) (fantasy.ProviderOptions, *float64, *float64, *int64, *float64, *float64) {
	modelOptions := getProviderOptions(model, cfg, reasoningOverride{})
	temp := cmp.Or(model.ModelCfg.Temperature, model.CatwalkCfg.Options.Temperature)
	topP := cmp.Or(model.ModelCfg.TopP, model.CatwalkCfg.Options.TopP)
	topK := cmp.Or(model.ModelCfg.TopK, model.CatwalkCfg.Options.TopK)
	freqPenalty := cmp.Or(model.ModelCfg.FrequencyPenalty, model.CatwalkCfg.Options.FrequencyPenalty)
	presPenalty := cmp.Or(model.ModelCfg.PresencePenalty, model.CatwalkCfg.Options.PresencePenalty)
	return modelOptions, temp, topP, topK, freqPenalty, presPenalty
}

func (c *coordinator) buildAgent(ctx context.Context, prompt *prompt.Prompt, agent config.Agent, isSubAgent bool) (SessionAgent, error) {
	large, small, err := c.buildAgentModels(ctx, isSubAgent)
	if err != nil {
		return nil, err
	}

	largeProviderCfg, _ := c.cfg.Config().Providers.Get(large.ModelCfg.Provider)
	result := NewSessionAgent(SessionAgentOptions{
		LargeModel:           large,
		SmallModel:           small,
		SystemPromptPrefix:   largeProviderCfg.SystemPromptPrefix,
		SystemPrompt:         "",
		IsSubAgent:           isSubAgent,
		DisableAutoSummarize: c.cfg.Config().Options.DisableAutoSummarize,
		IsYolo:               c.permissions.SkipRequests(),
		Sessions:             c.sessions,
		Goals:                c.goals,
		Messages:             c.messages,
		Tools:                nil,
		Notify:               c.notify,
		RunComplete:          c.runComplete,
		RecallMemory: func() []prowlagent.KnowledgeDoc {
			return recallMemory(c.cfg.Config().Options.GetProwlAgent(), c.cfg.WorkingDir())
		},
	})

	// The readiness goroutines below perform one-time setup — building the
	// system prompt and the initial tool list — whose results the
	// coordinator needs for its whole lifetime, so they must survive the
	// caller's context being canceled. Several entry points build an agent
	// from a short-lived HTTP request context: the server's
	// InitAgent/UpdateAgent handlers, and UpdateModels -> buildTools ->
	// agentTool -> buildAgent for the sub-agent. The tool-list build reads
	// the MCP registry as it stands; servers still connecting are picked up
	// by later runs. WithoutCancel drops cancellation while keeping context
	// values; the work is local and always completes.
	initCtx := context.WithoutCancel(ctx)

	c.readyWg.Go(func() error {
		systemPrompt, err := prompt.Build(initCtx, c.cfg)
		if err != nil {
			return err
		}
		result.SetSystemPrompt(systemPrompt)
		return nil
	})

	c.readyWg.Go(func() error {
		tools, err := c.buildTools(initCtx, agent, isSubAgent)
		if err != nil {
			return err
		}
		result.SetTools(tools)
		return nil
	})

	return result, nil
}

func (c *coordinator) buildTools(ctx context.Context, agent config.Agent, isSubAgent bool) ([]fantasy.AgentTool, error) {
	var allTools []fantasy.AgentTool
	if slices.Contains(agent.AllowedTools, AgentToolName) {
		agentTool, err := c.agentTool(ctx)
		if err != nil {
			return nil, err
		}
		allTools = append(allTools, agentTool)
	}

	if slices.Contains(agent.AllowedTools, tools.AgenticFetchToolName) {
		agenticFetchTool, err := c.agenticFetchTool(ctx, nil)
		if err != nil {
			return nil, err
		}
		allTools = append(allTools, agenticFetchTool)
	}

	// Get the model name for the agent
	modelID := ""
	if modelCfg, ok := c.cfg.Config().Models[agent.Model]; ok {
		if model := c.cfg.Config().GetModel(modelCfg.Provider, modelCfg.Model); model != nil {
			modelID = model.ID
		}
	}

	logFile := filepath.Join(c.cfg.Config().Options.DataDirectory, "logs", "prowl.log")

	// Build hook runner if PreToolUse hooks are configured.
	var hookRunner *hooks.Runner
	if preToolHooks := c.cfg.Config().Hooks[hooks.EventPreToolUse]; len(preToolHooks) > 0 {
		hookRunner = hooks.NewRunner(preToolHooks, c.cfg.WorkingDir(), c.cfg.WorkingDir())
	}

	// Skill files, including the agent-authored managed-skills store, are
	// readable without a permission prompt.
	viewSkillPaths := append([]string{}, c.cfg.Config().Options.SkillsPaths...)
	if md := skills.ManagedSkillsDir(); md != "" {
		viewSkillPaths = append(viewSkillPaths, md)
	}

	allTools = append(
		allTools,
		tools.NewBashTool(c.permissions, c.cfg.WorkingDir(), c.cfg.Config().Options.Attribution, modelID),
		tools.NewProwlInfoTool(c.cfg, c.lspManager, c.allSkills, c.activeSkills, c.skillTracker),
		tools.NewProwlLogsTool(logFile),
		tools.NewJobOutputTool(),
		tools.NewJobKillTool(),
		tools.NewDownloadTool(c.permissions, c.cfg.WorkingDir(), nil),
		tools.NewEditTool(c.lspManager, c.permissions, c.history, c.filetracker, c.cfg.WorkingDir()),
		tools.NewMultiEditTool(c.lspManager, c.permissions, c.history, c.filetracker, c.cfg.WorkingDir()),
		tools.NewFetchTool(c.permissions, c.cfg.WorkingDir(), nil),
		tools.NewGlobTool(c.cfg.WorkingDir(), c.cfg.Config().Tools.Glob),
		tools.NewGrepTool(c.cfg.WorkingDir(), c.cfg.Config().Tools.Grep),
		tools.NewLsTool(c.permissions, c.cfg.WorkingDir(), c.cfg.Config().Tools.Ls),
		tools.NewSourcegraphTool(nil),
		tools.NewTodosTool(c.sessions),
		tools.NewViewTool(c.lspManager, c.permissions, c.filetracker, c.skillTracker, c.cfg.WorkingDir(), viewSkillPaths...),
		tools.NewWriteTool(c.lspManager, c.permissions, c.history, c.filetracker, c.cfg.WorkingDir()),
	)

	// Native prowl-agent code-intelligence tool, registered when enabled and
	// the binary is available on this machine.
	if paOpts := c.cfg.Config().Options.GetProwlAgent(); prowlagent.Available(paOpts) {
		allTools = append(allTools, tools.NewProwlAgentTool(paOpts, c.cfg.WorkingDir()))
	}

	if !isSubAgent && c.goals != nil {
		allTools = append(allTools, tools.NewGoalTool(c.goals))
	}

	// Autolearn tools let the agent evolve its own capabilities: authoring
	// managed skills and recording durable lessons. On by default (see
	// options.autolearn) and available to the top-level agent only. learn also
	// requires prowl-agent for knowledge persistence.
	if !isSubAgent {
		opts := c.cfg.Config().Options
		if opts.ManageSkillEnabled() {
			allTools = append(allTools, tools.NewManageSkillTool(c.authoredSkillNames, c.refreshSkills))
		}
		if opts.LearnEnabled() {
			if paOpts := opts.GetProwlAgent(); prowlagent.Available(paOpts) {
				allTools = append(allTools, tools.NewLearnTool(paOpts, c.cfg.WorkingDir(), c.authoredSkillNames, c.refreshSkills))
			}
		}
	}

	// Question tool is interactive-only and not available to sub-agents.
	if !isSubAgent && c.interactive {
		allTools = append(allTools, tools.NewQuestionTool(c.questions))
	}

	// Add LSP tools if user has configured LSPs or auto_lsp is enabled (nil or true).
	if len(c.cfg.Config().LSP) > 0 || c.cfg.Config().Options.AutoLSP == nil || *c.cfg.Config().Options.AutoLSP {
		allTools = append(
			allTools,
			tools.NewDiagnosticsTool(c.lspManager),
			tools.NewReferencesTool(c.lspManager),
			tools.NewLSPRestartTool(c.lspManager),
			tools.NewSymbolsTool(c.lspManager),
			tools.NewDefinitionTool(c.lspManager),
			tools.NewCallHierarchyTool(c.lspManager),
			tools.NewRenameTool(c.lspManager, c.permissions, c.history, c.filetracker),
			tools.NewReplaceSymbolTool(c.lspManager, c.permissions, c.history, c.filetracker),
		)
	}

	if len(c.cfg.Config().MCP) > 0 {
		allTools = append(
			allTools,
			tools.NewListMCPResourcesTool(c.cfg, c.permissions),
			tools.NewReadMCPResourceTool(c.cfg, c.permissions),
		)
	}

	var filteredTools []fantasy.AgentTool
	for _, tool := range allTools {
		if slices.Contains(agent.AllowedTools, tool.Info().Name) {
			filteredTools = append(filteredTools, tool)
		}
	}

	for _, tool := range tools.GetMCPTools(c.permissions, c.cfg, c.cfg.WorkingDir()) {
		if agent.AllowedMCP == nil {
			// No MCP restrictions
			filteredTools = append(filteredTools, tool)
			continue
		}
		if len(agent.AllowedMCP) == 0 {
			// No MCPs allowed
			slog.Debug("No MCPs allowed", "tool", tool.Name(), "agent", agent.Name)
			break
		}

		for mcp, tools := range agent.AllowedMCP {
			if mcp != tool.MCP() {
				continue
			}
			if len(tools) == 0 || slices.Contains(tools, tool.MCPToolName()) {
				filteredTools = append(filteredTools, tool)
				break
			}
			slog.Debug("MCP not allowed", "tool", tool.Name(), "agent", agent.Name)
		}
	}
	slices.SortFunc(filteredTools, func(a, b fantasy.AgentTool) int {
		return strings.Compare(a.Info().Name, b.Info().Name)
	})

	// Wrap tools with hook interception for the top-level agent only.
	// Sub-agents (the `agent` task tool, `agentic_fetch`, etc.) run
	// without hook interception to avoid firing the user's hook N times
	// per delegated turn. The top-level invocation of the sub-agent tool
	// itself is still wrapped from the coder's side.
	filteredTools = wrapToolsWithHooks(filteredTools, hookRunner, isSubAgent)

	// The post-tool stage is part of the harness rather than user
	// configuration, so it wraps unconditionally and includes sub-agents: a
	// delegated search is exactly where the index gets bypassed, and tool
	// output costs the same tokens either way. The advisory half stays quiet
	// when there is no index to route to; compaction always runs.
	advisor := c.routingAdvisor
	if !prowlagent.Available(c.cfg.Config().Options.GetProwlAgent()) {
		advisor = nil
	}
	filteredTools = wrapToolsWithRouting(filteredTools, advisor, c.skillAdvisor)

	return filteredTools, nil
}

// TODO: when we support multiple agents we need to change this so that we pass in the agent specific model config
func (c *coordinator) buildAgentModels(ctx context.Context, isSubAgent bool) (Model, Model, error) {
	largeModelCfg, ok := c.cfg.Config().Models[config.SelectedModelTypeLarge]
	if !ok {
		return Model{}, Model{}, errLargeModelNotSelected
	}
	smallModelCfg, ok := c.cfg.Config().Models[config.SelectedModelTypeSmall]
	if !ok {
		return Model{}, Model{}, errSmallModelNotSelected
	}

	largeProviderCfg, ok := c.cfg.Config().Providers.Get(largeModelCfg.Provider)
	if !ok {
		return Model{}, Model{}, errLargeModelProviderNotConfigured
	}

	largeProvider, err := c.buildProvider(largeProviderCfg, largeModelCfg, isSubAgent)
	if err != nil {
		return Model{}, Model{}, err
	}

	smallProviderCfg, ok := c.cfg.Config().Providers.Get(smallModelCfg.Provider)
	if !ok {
		return Model{}, Model{}, errSmallModelProviderNotConfigured
	}

	smallProvider, err := c.buildProvider(smallProviderCfg, smallModelCfg, true)
	if err != nil {
		return Model{}, Model{}, err
	}

	var largeCatwalkModel *catwalk.Model
	var smallCatwalkModel *catwalk.Model

	for _, m := range largeProviderCfg.Models {
		if m.ID == largeModelCfg.Model {
			largeCatwalkModel = &m
		}
	}
	for _, m := range smallProviderCfg.Models {
		if m.ID == smallModelCfg.Model {
			smallCatwalkModel = &m
		}
	}

	if largeCatwalkModel == nil {
		return Model{}, Model{}, errLargeModelNotFound
	}

	if smallCatwalkModel == nil {
		return Model{}, Model{}, errSmallModelNotFound
	}

	largeModelID := largeModelCfg.Model
	smallModelID := smallModelCfg.Model

	if largeModelCfg.Provider == openrouter.Name && isExactoSupported(largeModelID) {
		largeModelID += ":exacto"
	}

	if smallModelCfg.Provider == openrouter.Name && isExactoSupported(smallModelID) {
		smallModelID += ":exacto"
	}

	largeModel, err := largeProvider.LanguageModel(ctx, largeModelID)
	if err != nil {
		return Model{}, Model{}, err
	}
	smallModel, err := smallProvider.LanguageModel(ctx, smallModelID)
	if err != nil {
		return Model{}, Model{}, err
	}

	// Bound each request with the configured timeout so unreachable or hung
	// providers fail instead of blocking a session forever. The wrapper is
	// applied per request, so retries get a fresh budget each attempt.
	requestTimeout := c.cfg.Config().Options.GetRequestTimeout()
	largeModel = newRequestTimeoutModel(largeModel, requestTimeout)
	smallModel = newRequestTimeoutModel(smallModel, requestTimeout)

	return Model{
		Model:      largeModel,
		CatwalkCfg: *largeCatwalkModel,
		ModelCfg:   largeModelCfg,
		FlatRate:   largeProviderCfg.FlatRate,
	}, Model{
		Model:      smallModel,
		CatwalkCfg: *smallCatwalkModel,
		ModelCfg:   smallModelCfg,
		FlatRate:   smallProviderCfg.FlatRate,
	}, nil
}

func (c *coordinator) buildAnthropicProvider(baseURL, apiKey string, headers map[string]string, providerID string, token *oauth.Token) (fantasy.Provider, error) {
	var opts []anthropic.Option

	switch {
	case token != nil:
		opts = append(opts, anthropic.WithAPIKey(token.AccessToken))
	case strings.HasPrefix(apiKey, "Bearer "):
		// NOTE: Prevent the SDK from picking up the API key from env.
		os.Setenv("ANTHROPIC_API_KEY", "")
		headers["Authorization"] = apiKey
	case providerID == string(catwalk.InferenceProviderMiniMax) || providerID == string(catwalk.InferenceProviderMiniMaxChina):
		// NOTE: Prevent the SDK from picking up the API key from env.
		os.Setenv("ANTHROPIC_API_KEY", "")
		headers["Authorization"] = "Bearer " + apiKey
	case apiKey != "":
		// X-Api-Key header
		opts = append(opts, anthropic.WithAPIKey(apiKey))
	}

	if len(headers) > 0 {
		opts = append(opts, anthropic.WithHeaders(headers))
	}

	if baseURL != "" {
		opts = append(opts, anthropic.WithBaseURL(baseURL))
	}

	var httpClient *http.Client
	if c.cfg.Config().Options.Debug {
		httpClient = log.NewHTTPClient()
	}
	if token != nil {
		if httpClient == nil {
			httpClient = &http.Client{}
		}
		httpClient.Transport = &anthropicoauth.Transport{Base: httpClient.Transport, Token: token}
	}
	opts = append(opts, anthropic.WithHTTPClient(promptCacheHTTPClient(httpClient)))
	return anthropic.New(opts...)
}

func (c *coordinator) buildOpenaiProvider(baseURL, apiKey string, headers map[string]string, token *oauth.Token) (fantasy.Provider, error) {
	opts := []openai.Option{
		openai.WithAPIKey(apiKey),
		openai.WithUseResponsesAPI(),
		openai.WithResponsesAPIFunc(usesOpenAIResponses),
	}
	var httpClient *http.Client
	if c.cfg.Config().Options.Debug {
		httpClient = log.NewHTTPClient()
	}
	if token != nil {
		if httpClient == nil {
			httpClient = &http.Client{}
		}
		httpClient.Transport = &openaioauth.Transport{Base: httpClient.Transport, Token: token, Originator: "prowl"}
	}
	opts = append(opts, openai.WithHTTPClient(promptCacheHTTPClient(httpClient)))
	if len(headers) > 0 {
		opts = append(opts, openai.WithHeaders(headers))
	}
	if baseURL != "" {
		opts = append(opts, openai.WithBaseURL(baseURL))
	}
	return openai.New(opts...)
}

func (c *coordinator) buildOpenrouterProvider(_, apiKey string, headers map[string]string) (fantasy.Provider, error) {
	opts := []openrouter.Option{
		openrouter.WithAPIKey(apiKey),
	}
	var httpClient *http.Client
	if c.cfg.Config().Options.Debug {
		httpClient = log.NewHTTPClient()
	}
	opts = append(opts, openrouter.WithHTTPClient(promptCacheHTTPClient(httpClient)))
	if len(headers) > 0 {
		opts = append(opts, openrouter.WithHeaders(headers))
	}
	return openrouter.New(opts...)
}

func (c *coordinator) buildVercelProvider(_, apiKey string, headers map[string]string) (fantasy.Provider, error) {
	opts := []vercel.Option{
		vercel.WithAPIKey(apiKey),
	}
	var httpClient *http.Client
	if c.cfg.Config().Options.Debug {
		httpClient = log.NewHTTPClient()
	}
	opts = append(opts, vercel.WithHTTPClient(promptCacheHTTPClient(httpClient)))
	if len(headers) > 0 {
		opts = append(opts, vercel.WithHeaders(headers))
	}
	return vercel.New(opts...)
}

func (c *coordinator) buildOpenaiCompatProvider(baseURL, apiKey string, headers map[string]string, extraBody map[string]any, providerID string, isSubAgent bool) (fantasy.Provider, error) {
	opts := []openaicompat.Option{
		openaicompat.WithBaseURL(baseURL),
		openaicompat.WithAPIKey(apiKey),
	}

	// Set HTTP client based on provider and debug mode.
	var httpClient *http.Client
	switch providerID {
	case string(catwalk.InferenceProviderCopilot):
		opts = append(
			opts,
			openaicompat.WithUseResponsesAPI(),
			openaicompat.WithResponsesAPIFunc(func(modelID string) bool {
				return copilotResponsesModels[modelID]
			}),
		)
		httpClient = copilot.NewClient(isSubAgent, c.cfg.Config().Options.Debug)

	case string(catwalk.InferenceProviderOpenCodeGo), string(catwalk.InferenceProviderOpenCodeZen):
		opts = append(
			opts,
			openaicompat.WithUseResponsesAPI(),
			openaicompat.WithResponsesAPIFunc(isOpenCodeResponsesModel),
		)

	case hyper.Name, gateway.ProviderID:
		// Both of these route a request to a model chosen at dispatch, so
		// the turn must report what actually answered rather than the
		// placeholder the user selected.
		opts = append(
			opts,
			openaicompat.WithLanguageModelOptions(
				openai.WithLanguageModelHeaderFunc(hyper.HeaderFunc),
			),
		)
	}
	if httpClient == nil && c.cfg.Config().Options.Debug {
		httpClient = log.NewHTTPClient()
	}
	opts = append(opts, openaicompat.WithHTTPClient(promptCacheHTTPClient(httpClient)))

	if len(headers) > 0 {
		opts = append(opts, openaicompat.WithHeaders(headers))
	}

	for extraKey, extraValue := range extraBody {
		opts = append(opts, openaicompat.WithSDKOptions(openaisdk.WithJSONSet(extraKey, extraValue)))
	}

	return openaicompat.New(opts...)
}

func (c *coordinator) buildAzureProvider(baseURL, apiKey string, headers map[string]string, options map[string]string) (fantasy.Provider, error) {
	opts := []azure.Option{
		azure.WithBaseURL(baseURL),
		azure.WithAPIKey(apiKey),
		azure.WithUseResponsesAPI(),
	}
	var httpClient *http.Client
	if c.cfg.Config().Options.Debug {
		httpClient = log.NewHTTPClient()
	}
	opts = append(opts, azure.WithHTTPClient(promptCacheHTTPClient(httpClient)))
	if options == nil {
		options = make(map[string]string)
	}
	if apiVersion, ok := options["apiVersion"]; ok {
		opts = append(opts, azure.WithAPIVersion(apiVersion))
	}
	if len(headers) > 0 {
		opts = append(opts, azure.WithHeaders(headers))
	}

	return azure.New(opts...)
}

func (c *coordinator) buildBedrockProvider(apiKey string, headers map[string]string, providerID string) (fantasy.Provider, error) {
	var opts []bedrock.Option
	var httpClient *http.Client
	if c.cfg.Config().Options.Debug {
		httpClient = log.NewHTTPClient()
	}
	opts = append(opts, bedrock.WithHTTPClient(promptCacheHTTPClient(httpClient)))
	if len(headers) > 0 {
		opts = append(opts, bedrock.WithHeaders(headers))
	}

	switch {
	case apiKey != "":
		opts = append(opts, bedrock.WithAPIKey(apiKey))
	case os.Getenv("AWS_BEARER_TOKEN_BEDROCK") != "":
		opts = append(opts, bedrock.WithAPIKey(os.Getenv("AWS_BEARER_TOKEN_BEDROCK")))
	default:
		// Skip, let the SDK do authentication.
	}

	switch providerID {
	case string(catwalk.InferenceProviderBedrockEurope):
		opts = append(opts, bedrock.WithRegion("eu-west-1"))
	default:
		opts = append(opts, bedrock.WithRegion("us-east-1"))
	}

	return bedrock.New(opts...)
}

func (c *coordinator) buildGoogleProvider(baseURL, apiKey string, headers map[string]string) (fantasy.Provider, error) {
	opts := []google.Option{
		google.WithBaseURL(baseURL),
		google.WithGeminiAPIKey(apiKey),
	}
	var httpClient *http.Client
	if c.cfg.Config().Options.Debug {
		httpClient = log.NewHTTPClient()
	}
	if len(headers) > 0 {
		opts = append(opts, google.WithHeaders(headers))
	}
	return newGoogleProvider(httpClient, opts...)
}

func (c *coordinator) buildGoogleVertexProvider(headers map[string]string, options map[string]string) (fantasy.Provider, error) {
	opts := []google.Option{}
	var httpClient *http.Client
	if c.cfg.Config().Options.Debug {
		httpClient = log.NewHTTPClient()
	}
	if len(headers) > 0 {
		opts = append(opts, google.WithHeaders(headers))
	}

	project := options["project"]
	location := options["location"]

	opts = append(opts, google.WithVertex(project, location))

	return newGoogleProvider(httpClient, opts...)
}

func (c *coordinator) isAnthropicThinking(model config.SelectedModel) bool {
	if model.Think {
		return true
	}
	opts, err := anthropic.ParseOptions(model.ProviderOptions)
	return err == nil && opts.Thinking != nil
}

// anthropicNeedsInterleavedThinking reports whether the interleaved-thinking
// beta should be enabled at provider construction. A reasoning-capable
// configured model always opts in so a per-turn Auto/ultrathink request that
// enables thinking from a saved Off state still carries the header — the flag
// is a no-op when no thinking is requested — without persisting settings or
// rewriting per-call headers. Persisted Think or an explicit thinking provider
// option still opt in for non-catalogued models.
func (c *coordinator) anthropicNeedsInterleavedThinking(providerCfg config.ProviderConfig, model config.SelectedModel) bool {
	if c.isAnthropicThinking(model) {
		return true
	}
	for _, m := range providerCfg.Models {
		if m.ID == model.Model {
			return m.CanReason
		}
	}
	return false
}

func (c *coordinator) buildProvider(providerCfg config.ProviderConfig, model config.SelectedModel, isSubAgent bool) (provider fantasy.Provider, err error) {
	policy, err := resolvePromptCache(providerCfg, model)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err == nil {
			provider = promptCacheProvider{
				Provider: provider, policy: policy, sessions: c.sessions, gate: c.cacheGate,
			}
		}
	}()
	headers := maps.Clone(providerCfg.ExtraHeaders)
	if headers == nil {
		headers = make(map[string]string)
	}

	// handle special headers for anthropic
	if providerCfg.Type == anthropic.Name && c.anthropicNeedsInterleavedThinking(providerCfg, model) {
		const interleaved = "interleaved-thinking-2025-05-14"
		switch v, ok := headers["anthropic-beta"]; {
		case !ok || v == "":
			headers["anthropic-beta"] = interleaved
		case !strings.Contains(v, interleaved):
			headers["anthropic-beta"] = v + "," + interleaved
		}
	}

	apiKey, _ := c.cfg.Resolve(providerCfg.APIKey)
	baseURL, _ := c.cfg.Resolve(providerCfg.BaseURL)

	switch providerCfg.ID {
	case string(catwalk.InferenceProviderOpenCodeGo), string(catwalk.InferenceProviderOpenCodeZen):
		if isOpenCodeMessagesModel(providerCfg.ID, model.Model) {
			baseURL = strings.TrimSuffix(baseURL, "/v1")
			return c.buildAnthropicProvider(baseURL, apiKey, headers, providerCfg.ID, nil)
		}
	}

	switch providerCfg.Type {
	case openai.Name:
		token := providerCfg.OAuthToken
		if token != nil {
			baseURL = openaioauth.CodexBaseURL
			apiKey = token.AccessToken
		}
		return c.buildOpenaiProvider(baseURL, apiKey, headers, token)
	case anthropic.Name:
		token := providerCfg.OAuthToken
		if token != nil {
			baseURL = anthropicoauth.BaseURL
			apiKey = token.AccessToken
		}
		return c.buildAnthropicProvider(baseURL, apiKey, headers, providerCfg.ID, token)
	case openrouter.Name:
		return c.buildOpenrouterProvider(baseURL, apiKey, headers)
	case vercel.Name:
		return c.buildVercelProvider(baseURL, apiKey, headers)
	case azure.Name:
		return c.buildAzureProvider(baseURL, apiKey, headers, providerCfg.ExtraParams)
	case bedrock.Name:
		return c.buildBedrockProvider(apiKey, headers, providerCfg.ID)
	case google.Name:
		return c.buildGoogleProvider(baseURL, apiKey, headers)
	case "google-vertex":
		return c.buildGoogleVertexProvider(headers, providerCfg.ExtraParams)
	case openaicompat.Name, hyper.Name:
		switch providerCfg.ID {
		case hyper.Name:
			baseURL = hyper.BaseURL() + "/v1"
			headers["x-prowl-id"] = event.GetID()
		case string(catwalk.InferenceProviderZAI):
			if providerCfg.ExtraBody == nil {
				providerCfg.ExtraBody = map[string]any{}
			}
			providerCfg.ExtraBody["tool_stream"] = true
		}
		return c.buildOpenaiCompatProvider(baseURL, apiKey, headers, providerCfg.ExtraBody, providerCfg.ID, isSubAgent)
	default:
		// Known custom providers (litellm, ollama, omlx) are
		// openai-compat under the hood.
		if discover.IsKnownCustomProvider(string(providerCfg.Type)) {
			return c.buildOpenaiCompatProvider(baseURL, apiKey, headers, providerCfg.ExtraBody, providerCfg.ID, isSubAgent)
		}
		return nil, fmt.Errorf("provider type not supported: %q", providerCfg.Type)
	}
}

func isExactoSupported(modelID string) bool {
	supportedModels := []string{
		"moonshotai/kimi-k2-0905",
		"deepseek/deepseek-v3.1-terminus",
		"z-ai/glm-4.6",
		"openai/gpt-oss-120b",
		"qwen/qwen3-coder",
	}
	return slices.Contains(supportedModels, modelID)
}

// BeginAccepted reserves an accept slot for sessionID on the active
// agent and returns the ownership handle. It is the fire-and-forget
// dispatch path's only way to mark a run as accepted-but-not-yet-active
// so a cancel arriving before the run registers in activeRequests is not
// lost.
func (c *coordinator) BeginAccepted(sessionID string) *AcceptedRun {
	return c.currentAgent.BeginAccepted(sessionID)
}

func (c *coordinator) Cancel(sessionID string) {
	c.currentAgent.Cancel(sessionID)
}

func (c *coordinator) CancelAll() {
	c.currentAgent.CancelAll()
}

func (c *coordinator) ClearQueue(sessionID string) {
	c.currentAgent.ClearQueue(sessionID)
}

func (c *coordinator) IsBusy() bool {
	return c.currentAgent.IsBusy()
}

func (c *coordinator) IsSessionBusy(sessionID string) bool {
	return c.currentAgent.IsSessionBusy(sessionID)
}

func (c *coordinator) Model() Model {
	return c.currentAgent.Model()
}

func (c *coordinator) UpdateModels(ctx context.Context) error {
	// build the models again so we make sure we get the latest config
	large, small, err := c.buildAgentModels(ctx, false)
	if err != nil {
		return err
	}
	c.currentAgent.SetModels(large, small)

	agentCfg, ok := c.cfg.Config().Agents[config.AgentCoder]
	if !ok {
		return errCoderAgentNotConfigured
	}

	tools, err := c.buildTools(ctx, agentCfg, false)
	if err != nil {
		return err
	}
	c.currentAgent.SetTools(tools)
	return nil
}

func (c *coordinator) QueuedPrompts(sessionID string) int {
	return c.currentAgent.QueuedPrompts(sessionID)
}

func (c *coordinator) QueuedPromptsList(sessionID string) []string {
	return c.currentAgent.QueuedPromptsList(sessionID)
}

func (c *coordinator) Summarize(ctx context.Context, sessionID string) error {
	providerCfg, ok := c.cfg.Config().Providers.Get(c.currentAgent.Model().ModelCfg.Provider)
	if !ok {
		return errModelProviderNotConfigured
	}

	if err := c.refreshTokenIfExpired(ctx, providerCfg); err != nil {
		slog.Error("Failed to refresh OAuth2 token before summarize. Proceeding with existing token.", "error", err)
	}

	// Auth failures during summarize flow through fantasy's OnAuthRefresh,
	// the same path used by regular turns.
	return c.currentAgent.Summarize(ctx, sessionID, getProviderOptions(c.currentAgent.Model(), providerCfg, reasoningOverride{}), c.makeAuthRefreshCallback(providerCfg))
}

// GenerateTitle generates a session title using the current agent.
func (c *coordinator) GenerateTitle(ctx context.Context, sessionID, prompt string) {
	if c.currentAgent == nil {
		return
	}
	c.currentAgent.GenerateTitle(ctx, sessionID, prompt)
}

// refreshTokenIfExpired proactively refreshes the OAuth token if it has expired.
func (c *coordinator) refreshTokenIfExpired(ctx context.Context, providerCfg config.ProviderConfig) error {
	if providerCfg.OAuthToken == nil || !providerCfg.OAuthToken.IsExpired() {
		return nil
	}
	slog.Debug("Token needs to be refreshed", "provider", providerCfg.ID)
	return c.refreshOAuth2Token(ctx, providerCfg)
}

// retryAfterUnauthorized attempts to refresh credentials after an auth error
// and returns nil if the request should be retried. For OAuth providers whose
// refresh token is revoked, and for Bedrock providers whose AWS SSO session
// has expired, it triggers interactive re-authentication and blocks until the
// user completes it (or the context is cancelled).
func (c *coordinator) retryAfterUnauthorized(ctx context.Context, providerCfg config.ProviderConfig) error {
	switch {
	case providerCfg.OAuthToken != nil:
		slog.Debug("Received 401. Refreshing token and retrying", "provider", providerCfg.ID)
		if err := c.refreshOAuth2Token(ctx, providerCfg); err != nil {
			// If the refresh token was revoked, trigger interactive
			// re-auth and wait for the user to complete it.
			var exchangeErr *oauth.TokenExchangeError
			if c.notify != nil && errors.As(err, &exchangeErr) && exchangeErr.IsRefreshTokenRevoked() {
				slog.Info("Refresh token revoked, waiting for re-authentication", "provider", providerCfg.ID)
				c.notify.Publish(pubsub.CreatedEvent, notify.Notification{
					Type:       notify.TypeReAuthenticate,
					ProviderID: providerCfg.ID,
				})
				return c.waitForInteractiveReauth(ctx, providerCfg.ID)
			}
			return err
		}
		return nil
	case providerCfg.AWSAuthRefresh != "":
		return c.refreshAWSCredentials(ctx, providerCfg)
	case strings.Contains(providerCfg.APIKeyTemplate, "$"):
		slog.Debug("Received 401. Refreshing API Key template and retrying", "provider", providerCfg.ID)
		return c.refreshApiKeyTemplate(ctx, providerCfg)
	default:
		return nil
	}
}

// errNoInteractiveAuth is returned by an OnAuthRefresh callback when a
// provider needs interactive re-authentication but no notifier is available
// to drive it (e.g. headless runs). Returning it surfaces the original auth
// error rather than retrying.
var errNoInteractiveAuth = errors.New("interactive authentication unavailable")

// waitForInteractiveReauth blocks until interactive re-authentication for the
// provider completes (signalled via SignalAuthComplete) or the context is
// cancelled, then rebuilds models so the next attempt picks up fresh
// credentials. Returns nil when the caller should retry.
func (c *coordinator) waitForInteractiveReauth(ctx context.Context, providerID string) error {
	// Use a detached context with a generous timeout so the wait survives
	// agent run cancellation. The user needs time to complete browser-based
	// authentication.
	waitCtx, waitCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
	defer waitCancel()
	slog.Info("Blocking on WaitForTokenChange", "provider", providerID)
	if waitErr := c.cfg.WaitForTokenChange(waitCtx, providerID); waitErr != nil {
		slog.Info("WaitForTokenChange returned error", "provider", providerID, "error", waitErr)
		return waitErr
	}
	// If the original context was cancelled during the wait, fantasy's retry
	// would fail immediately, so surface the cancellation instead.
	if ctx.Err() != nil {
		slog.Warn("Original context cancelled during auth wait, cannot retry",
			"provider", providerID, "ctx_err", ctx.Err())
		return ctx.Err()
	}
	// Rebuild models so ModelProvider picks up the fresh credentials.
	if updateErr := c.UpdateModels(waitCtx); updateErr != nil {
		slog.Error("Failed to update models after re-authentication", "error", updateErr)
		return updateErr
	}
	slog.Info("Models updated, returning nil to retry", "provider", providerID)
	return nil
}

// isUnauthorized reports whether err is an HTTP 401 from a provider.
func isUnauthorized(err error) bool {
	var providerErr *fantasy.ProviderError
	return errors.As(err, &providerErr) && providerErr.StatusCode == http.StatusUnauthorized
}

// makeAuthRefreshCallback returns an OnAuthRefresh callback for fantasy that
// delegates to the coordinator's existing credential refresh logic. Returns
// nil if no refresh mechanism is configured for the provider.
func (c *coordinator) makeAuthRefreshCallback(providerCfg config.ProviderConfig) func(context.Context, *fantasy.ProviderError) error {
	if providerCfg.OAuthToken == nil &&
		!strings.Contains(providerCfg.APIKeyTemplate, "$") &&
		providerCfg.AWSAuthRefresh == "" {
		return nil
	}
	return func(ctx context.Context, _ *fantasy.ProviderError) error {
		return c.retryAfterUnauthorized(ctx, providerCfg)
	}
}

func (c *coordinator) refreshOAuth2Token(ctx context.Context, providerCfg config.ProviderConfig) error {
	if err := c.cfg.RefreshOAuthToken(ctx, config.ScopeGlobal, providerCfg.ID); err != nil {
		slog.Error("Failed to refresh OAuth token after 401 error", "provider", providerCfg.ID, "error", err)
		return err
	}
	if err := c.UpdateModels(ctx); err != nil {
		return err
	}
	return nil
}

func (c *coordinator) refreshApiKeyTemplate(ctx context.Context, providerCfg config.ProviderConfig) error {
	newAPIKey, err := c.cfg.Resolve(providerCfg.APIKeyTemplate)
	if err != nil {
		slog.Error("Failed to re-resolve API key after 401 error", "provider", providerCfg.ID, "error", err)
		return err
	}

	providerCfg.APIKey = newAPIKey
	c.cfg.Config().Providers.Set(providerCfg.ID, providerCfg)

	if err := c.UpdateModels(ctx); err != nil {
		return err
	}
	return nil
}

// subAgentParams holds the parameters for running a sub-agent.
type subAgentParams struct {
	Agent          SessionAgent
	SessionID      string
	AgentMessageID string
	ToolCallID     string
	Prompt         string
	SessionTitle   string
	// SessionSetup is an optional callback invoked after session creation
	// but before agent execution, for custom session configuration.
	SessionSetup func(sessionID string)
}

// callTopK returns topK for use on fantasy.Call.TopK, suppressing it for
// known custom providers: getProviderOptions already carries top_k for
// them via extra_body, and passing it here too makes Fantasy emit a
// spurious "top_k unsupported" warning for every turn.
func callTopK(providerCfg config.ProviderConfig, topK *int64) *int64 {
	if discover.IsKnownCustomProvider(string(providerCfg.Type)) {
		return nil
	}
	return topK
}

// runSubAgent creates a child session and runs the agent with the given prompt.
// Request-boundary charging propagates costs even if the subagent fails.
func (c *coordinator) runSubAgent(ctx context.Context, params subAgentParams) (fantasy.ToolResponse, error) {
	// Create sub-session
	agentToolSessionID := c.sessions.CreateAgentToolSessionID(params.AgentMessageID, params.ToolCallID)
	session, err := c.sessions.CreateTaskSession(ctx, agentToolSessionID, params.SessionID, params.SessionTitle)
	if err != nil {
		return fantasy.ToolResponse{}, fmt.Errorf("create session: %w", err)
	}

	// Call session setup function if provided
	if params.SessionSetup != nil {
		params.SessionSetup(session.ID)
	}

	// Get model configuration
	model := params.Agent.Model()
	maxTokens := model.CatwalkCfg.DefaultMaxTokens
	if model.ModelCfg.MaxTokens != 0 {
		maxTokens = model.ModelCfg.MaxTokens
	}

	providerCfg, ok := c.cfg.Config().Providers.Get(model.ModelCfg.Provider)
	if !ok {
		return fantasy.ToolResponse{}, errModelProviderNotConfigured
	}

	// Run the agent
	run := func() (*fantasy.AgentResult, error) {
		return params.Agent.Run(ctx, SessionAgentCall{
			SessionID:        session.ID,
			Prompt:           params.Prompt,
			MaxOutputTokens:  maxTokens,
			ProviderOptions:  getProviderOptions(model, providerCfg, reasoningOverride{}),
			Temperature:      model.ModelCfg.Temperature,
			TopP:             model.ModelCfg.TopP,
			TopK:             callTopK(providerCfg, model.ModelCfg.TopK),
			FrequencyPenalty: model.ModelCfg.FrequencyPenalty,
			PresencePenalty:  model.ModelCfg.PresencePenalty,
			NonInteractive:   true,
			OnAuthRefresh:    c.makeAuthRefreshCallback(providerCfg),
		})
	}
	result, err := run()
	// Notify only if still unauthorized after retry. AWS SSO is handled
	// transparently inside OnAuthRefresh, so it needs no post-run notice.
	if err != nil && isUnauthorized(err) && c.notify != nil && model.ModelCfg.Provider == hyper.Name {
		c.notify.Publish(pubsub.CreatedEvent, notify.Notification{
			Type:       notify.TypeReAuthenticate,
			ProviderID: model.ModelCfg.Provider,
		})
	}
	if err != nil {
		return fantasy.NewTextErrorResponse(fmt.Sprintf("Failed to generate response: %s", err)), nil
	}

	output := subAgentOutput(result)
	if output == "" {
		return fantasy.NewTextErrorResponse("Sub-agent completed but produced no text output."), nil
	}
	return fantasy.NewTextResponse(output), nil
}

func subAgentOutput(result *fantasy.AgentResult) string {
	if result == nil {
		return ""
	}
	return result.Response.Content.Text()
}

// discoverSkills is a thin fallback wrapper used only when no
// skills.Manager has been threaded through to the coordinator. All
// production call sites (backend.CreateWorkspace, setupLocalWorkspace)
// run discovery in advance and pass the results via the manager;
// reaching this path means a caller bypassed both. It deliberately does
// NOT publish to the package-level broker — there are no subscribers in
// that case, so doing so would be misleading without delivering the
// snapshot anywhere useful.
func discoverSkills(cfg *config.ConfigStore) (allSkills, activeSkills []*skills.Skill) {
	opts := cfg.Config().Options
	var paths, disabled []string
	if opts != nil {
		paths = opts.SkillsPaths
		disabled = opts.DisabledSkills
	}
	var resolver func(string) (string, error)
	if r := cfg.Resolver(); r != nil {
		resolver = r.ResolveValue
	}
	allSkills, activeSkills, states := skills.DiscoverFromConfig(skills.DiscoveryConfig{
		SkillsPaths:      paths,
		DisabledSkills:   disabled,
		Resolver:         resolver,
		ManagedSkillsDir: skills.ManagedSkillsDir(),
	})
	logDiscoveryStats(states, paths, allSkills, activeSkills, disabled)
	return allSkills, activeSkills
}

// refreshSkills re-discovers skills and rebuilds the system prompt so a newly
// authored or deleted managed skill is reflected to the model without a
// restart. It is passed to the manage_skill and learn tools. The coordinator's
// own snapshot fields are intentionally left untouched to avoid racing with
// buildTools; the manager (source of truth for the UI) is refreshed separately.
func (c *coordinator) refreshSkills() {
	if c.skillsMgr != nil {
		c.skillsMgr.Refresh()
	}
	if c.currentAgent == nil {
		return
	}
	p, err := coderPrompt(prompt.WithWorkingDir(c.cfg.WorkingDir()))
	if err != nil {
		slog.Warn("Failed to build prompt for skill refresh", "error", err)
		return
	}
	sp, err := p.Build(context.Background(), c.cfg)
	if err != nil {
		slog.Warn("Failed to rebuild system prompt for skill refresh", "error", err)
		return
	}
	c.currentAgent.SetSystemPrompt(sp)
}

// authoredSkillNames reports whether name is owned by a builtin or user
// (non-managed) skill, so managed writes cannot shadow authored skills.
func (c *coordinator) authoredSkillNames(name string) bool {
	src := c.allSkills
	if c.skillsMgr != nil {
		src = c.skillsMgr.AllSkills()
	}
	for _, s := range src {
		if !s.Managed && s.Name == name {
			return true
		}
	}
	return false
}

// logTurnSkillUsage emits a per-turn diagnostic line showing which skills
// (if any) were loaded during this turn and which looked relevant based on
// a cheap keyword match against the user prompt. The goal is to surface
// "should-have-loaded but didn't" situations for later analysis.
//
// Logged at Info level under component=skills; heavy fields are elided when
// there is nothing interesting to report.
func logTurnSkillUsage(
	sessionID string,
	prompt string,
	activeSkills []*skills.Skill,
	tracker *skills.Tracker,
	before []string,
) {
	if tracker == nil || len(activeSkills) == 0 {
		return
	}

	after := tracker.LoadedNames()

	beforeSet := make(map[string]bool, len(before))
	for _, n := range before {
		beforeSet[n] = true
	}
	var loadedThisTurn []string
	for _, n := range after {
		if !beforeSet[n] {
			loadedThisTurn = append(loadedThisTurn, n)
		}
	}

	slog.Info(
		"Skill turn summary",
		"component", "skills",
		"session_id", sessionID,
		"prompt_len", len(prompt),
		"active_total", len(activeSkills),
		"loaded_total", len(after),
		"loaded_this_turn", loadedThisTurn,
	)
}

// logDiscoveryStats emits a single structured log line summarising skill
// discovery for the current session. It is intentionally low-volume: one
// line per session start. Builtin vs user counts are derived from the
// SkillState.Path — builtin states use the "builtin/" embed prefix.
func logDiscoveryStats(
	states []*skills.SkillState,
	userPaths []string,
	allSkills, activeSkills []*skills.Skill,
	disabled []string,
) {
	var builtinOK, builtinErr, userOK, userErr int
	for _, s := range states {
		isBuiltin := strings.HasPrefix(s.Path, "builtin/")
		switch {
		case isBuiltin && s.State == skills.StateNormal:
			builtinOK++
		case isBuiltin && s.State == skills.StateError:
			builtinErr++
		case !isBuiltin && s.State == skills.StateNormal:
			userOK++
		case !isBuiltin && s.State == skills.StateError:
			userErr++
		}
	}

	activeNames := make([]string, 0, len(activeSkills))
	for _, s := range activeSkills {
		activeNames = append(activeNames, s.Name)
	}

	xml := skills.ToPromptXML(activeSkills)

	slog.Info(
		"Skill discovery complete",
		"component", "skills",
		"builtin_ok", builtinOK,
		"builtin_errors", builtinErr,
		"user_ok", userOK,
		"user_errors", userErr,
		"user_paths", len(userPaths),
		"deduped_total", len(allSkills),
		"active", len(activeSkills),
		"disabled", len(disabled),
		"prompt_bytes", len(xml),
		"prompt_tok_est", skills.ApproxTokenCount(xml),
		"active_names", activeNames,
	)
}
