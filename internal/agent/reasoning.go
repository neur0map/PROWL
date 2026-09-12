package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"charm.land/fantasy/providers/anthropic"
	"github.com/google/uuid"
	"github.com/neur0map/prowl/internal/agent/notify"
	"github.com/neur0map/prowl/internal/pubsub"
	"github.com/neur0map/prowl/internal/reasoning"
)

// reasoningClassifierTimeout bounds the per-turn auto-thinking classification.
// The turn falls back to a concrete supported effort if the classifier does
// not answer within this window, mirroring OMP's 4s ceiling.
const reasoningClassifierTimeout = 4 * time.Second

// reasoningClassifierMaxTokens caps the classifier completion. It is large
// enough that the single-word answer still lands after any thinking preamble a
// reasoning small model emits, while a non-thinking model returns in a handful
// of tokens regardless.
const reasoningClassifierMaxTokens = 4096

// reasoningClassifierInputLimit bounds the prompt handed to the classifier so a
// huge paste does not blow up classifier latency or cost.
const reasoningClassifierInputLimit = 6000

const reasoningModeManual = "manual"

// reasoningUltrathinkNotice is the brief careful-work request appended to a
// prompt that mentions the ultrathink keyword. It is added per request only
// and is never persisted to the session or system prompt.
const reasoningUltrathinkNotice = "<system-notice>\nMulti-step reasoning: think carefully through the problem before responding.\n</system-notice>"

// reasoningClassifierSystemPrompt asks for a tier no higher than xhigh.
// The result is then mapped to the model's actual supported controls.
const reasoningClassifierSystemPrompt = "Coding-agent request difficulty classifier: read the user's request; choose this turn's reasoning effort.\n\n" +
	"Reply exactly one word: `low`, `medium`, `high`, `xhigh`. No punctuation, explanation, or other text.\n\n" +
	"Levels:\n" +
	"- `low`: trivial/mechanical — rename, typo, one-line edit, formatting tweak, direct factual question, obvious solution.\n" +
	"- `medium`: localized change needing reasoning — small self-contained feature, straightforward one-place bug fix, explain moderate code.\n" +
	"- `high`: non-trivial — multiple files or callers, real debugging, moderate design decision, refactor with several moving parts.\n" +
	"- `xhigh`: deep/open-ended — subtle concurrency or algorithmic problem, cross-system reasoning, ambiguous requirements, large or risky refactor, hard root-cause debugging.\n\n" +
	"Judge inherent task difficulty, not phrasing politeness or verbosity. If torn between levels, choose lower.\n /no_think"

// reasoningOverride carries a resolved per-turn reasoning decision into
// getProviderOptions. active signals the persistent provider reasoning fields
// must be replaced by this turn's choice; effort is the resolved supported
// control (a level, or off/on for toggle/budget models); tier is the raw
// classifier level used to scale thinking budgets; maxOut bounds thinking
// budgets against the request's output limit. The zero value is inactive and
// reproduces the persisted config behaviour exactly.
type reasoningOverride struct {
	active bool
	effort string
	tier   string
	maxOut int64
}

// reasoningLanguageModel wraps a language model so each request carries the
// run's currently resolved provider options. Prowl's fantasy loop reads the
// model through AgentStreamCall.ModelProvider on every step and retry, so
// injecting options here lets a folded follow-up prompt change the reasoning
// effort mid-turn without the (fixed) AgentStreamCall.ProviderOptions and
// without a prior ultrathink leaking into a later step.
type reasoningLanguageModel struct {
	fantasy.LanguageModel
	options func() fantasy.ProviderOptions
}

func (m reasoningLanguageModel) Generate(ctx context.Context, call fantasy.Call) (*fantasy.Response, error) {
	if m.options != nil {
		if opts := m.options(); opts != nil {
			call.ProviderOptions = opts
		}
	}
	return m.LanguageModel.Generate(ctx, call)
}

func (m reasoningLanguageModel) Stream(ctx context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
	if m.options != nil {
		if opts := m.options(); opts != nil {
			call.ProviderOptions = opts
		}
	}
	return m.LanguageModel.Stream(ctx, call)
}

// runReasoning holds the reasoning decision for a single active Run instance.
// It is created after a run becomes active (never for a merely queued prompt),
// resolves the effort from the current reasoning request (the initial prompt,
// updated when PrepareStep folds an untracked follow-up), and shapes the
// provider options the request wrapper injects. All mutable state is guarded by
// mu because apply runs both before Stream and inside PrepareStep while the
// wrapper reads options concurrently on each step.
type runReasoning struct {
	agent      *sessionAgent
	sessionID  string
	turnID     string
	model      Model
	maxOut     int64
	baseAuto   bool
	emitEvents bool
	shape      func(reasoningOverride) fantasy.ProviderOptions

	mu             sync.Mutex
	options        fantasy.ProviderOptions
	ultra          bool
	started        bool
	emittedMode    string
	emittedEffort  string
	classifierCost float64
}

// newRunReasoning builds the reasoning state for an active run using a
// run-local copy of the model. It never mutates global config or calls
// SetModels. options defaults to the call's persisted provider options so the
// wrapper is safe to read before the first apply.
func newRunReasoning(a *sessionAgent, call SessionAgentCall, model Model) *runReasoning {
	return &runReasoning{
		agent:      a,
		sessionID:  call.SessionID,
		turnID:     uuid.NewString(),
		model:      model,
		maxOut:     call.MaxOutputTokens,
		baseAuto:   model.CatwalkCfg.CanReason && strings.EqualFold(model.ModelCfg.ReasoningEffort, reasoning.Auto),
		emitEvents: a.notify != nil && !a.isSubAgent && !call.NonInteractive,
		shape:      call.ShapeReasoning,
		options:    call.ProviderOptions,
	}
}

// apply resolves the reasoning decision for requestText, updates the provider
// options the request wrapper injects, and emits the reasoning event. It runs
// once before Stream for the initial prompt and again whenever PrepareStep
// folds an untracked follow-up, so the most recently folded prompt
// becomes the current reasoning request.
func (rr *runReasoning) apply(ctx context.Context, requestText string) error {
	if rr == nil {
		return nil
	}
	mode, effort, tier, ultra := rr.decide(ctx, requestText)
	if err := ctx.Err(); err != nil {
		return err
	}

	rr.mu.Lock()
	rr.ultra = ultra
	var o reasoningOverride
	if mode != reasoningModeManual {
		o = reasoningOverride{active: true, effort: effort, tier: tier}
	}
	if rr.shape != nil {
		rr.options = rr.shape(o)
	}
	if opts, ok := rr.options[anthropic.Name].(*anthropic.ProviderOptions); ok &&
		opts.Thinking != nil && rr.maxOut > 0 && opts.Thinking.BudgetTokens >= rr.maxOut {
		rr.mu.Unlock()
		return fmt.Errorf("thinking requires max_tokens greater than %d; configured %d", opts.Thinking.BudgetTokens, rr.maxOut)
	}
	temporary := mode != reasoningModeManual
	var emit bool
	switch {
	case !rr.started:
		// The turn only starts owing an end event once a temporary mode
		// (auto/ultrathink) actually applies. A plain manual turn stays
		// silent so the UI keeps showing the persisted preference.
		if temporary {
			rr.started = true
			emit = true
		}
	case mode != rr.emittedMode || effort != rr.emittedEffort:
		// Already started: report any change, including a fold reverting a
		// prior ultrathink back to auto/manual.
		emit = true
	}
	if emit {
		rr.emittedMode, rr.emittedEffort = mode, effort
	}
	rr.mu.Unlock()

	if emit {
		rr.publish(mode, effort)
	}
	return nil
}

// decide classifies requestText against the run-local model. Ultrathink in
// prose bypasses the classifier and takes the model's maximum; auto runs the
// small-model classifier (bounded, cancellable) and falls back to a supported
// medium on any failure; otherwise the persisted manual preference stands.
func (rr *runReasoning) decide(ctx context.Context, requestText string) (mode, effort, tier string, ultra bool) {
	cm := rr.model.CatwalkCfg
	if !cm.CanReason {
		return reasoningModeManual, "", "", false
	}
	if reasoning.HasUltrathink(requestText) {
		return reasoning.Ultrathink, reasoning.HighestEffort(cm), "max", true
	}
	if rr.baseAuto {
		t, err := rr.agent.classifyEffort(ctx, rr.sessionID, requestText, rr)
		if err != nil {
			slog.Debug("Auto reasoning classification failed; using fallback effort",
				"session", rr.sessionID, "error", err)
			t = "medium"
		}
		return reasoning.Auto, autoResolvedEffort(cm, t), t, false
	}
	return reasoningModeManual, rr.persistedControl(), "", false
}

// persistedControl reports the effort the persisted manual preference resolves
// to, for the reasoning event only. Mandatory-thinking models always report on.
func (rr *runReasoning) persistedControl() string {
	cm := rr.model.CatwalkCfg
	if !cm.CanReason {
		return ""
	}
	if len(cm.ReasoningLevels) > 0 {
		return effectiveReasoningEffort(rr.model)
	}
	if control := rr.model.ModelCfg.ReasoningEffort; control == "on" || control == "off" {
		return reasoning.ClampEffort(cm, control)
	}
	if reasoning.RequiresThinking(cm) || rr.model.ModelCfg.Think {
		return "on"
	}
	return "off"
}

// currentOptions returns the provider options resolved for the current step.
func (rr *runReasoning) currentOptions() fantasy.ProviderOptions {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	return rr.options
}

// wantsNotice reports whether the ultrathink careful-work notice should be
// added to the current step: it is present on every model call while the
// current reasoning request is ultrathink, and drops the moment a folded
// follow-up supersedes it. The notice lives only in the step's transient
// messages and is never written to the session or system prompt.
func (rr *runReasoning) wantsNotice() bool {
	if rr == nil {
		return false
	}
	rr.mu.Lock()
	defer rr.mu.Unlock()
	return rr.ultra
}

// addClassifierCost accumulates classifier spend to be charged at the next
// OnStepFinish so it lands on the session cost without touching the main
// context token counters.
func (rr *runReasoning) addClassifierCost(cost float64) {
	if rr == nil || cost == 0 {
		return
	}
	rr.mu.Lock()
	rr.classifierCost += cost
	rr.mu.Unlock()
}

// takeClassifierCost returns and clears the accumulated classifier cost.
func (rr *runReasoning) takeClassifierCost() float64 {
	if rr == nil {
		return 0
	}
	rr.mu.Lock()
	defer rr.mu.Unlock()
	cost := rr.classifierCost
	rr.classifierCost = 0
	return cost
}

// finish emits the matching end event for a turn that started one. It runs on
// every Run exit (success, error, cancel) via defer.
func (rr *runReasoning) finish(ctx context.Context) {
	if rr == nil {
		return
	}
	if cost := rr.takeClassifierCost(); cost != 0 {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		current, err := rr.agent.sessions.Get(cleanupCtx, rr.sessionID)
		if err == nil {
			current.Cost += cost
			_, err = rr.agent.sessions.Save(cleanupCtx, current)
		}
		if err != nil {
			slog.Error("Failed to save reasoning classifier cost", "session", rr.sessionID, "error", err)
		}
	}
	rr.mu.Lock()
	started := rr.started
	mode := rr.emittedMode
	rr.mu.Unlock()
	if started {
		rr.publish(mode, "")
	}
}

// publish emits a reasoning_changed notification. An empty effort marks the end
// of the turn's reasoning; the UI ignores an end whose turn id is stale.
func (rr *runReasoning) publish(mode, effort string) {
	if !rr.emitEvents || rr.agent.notify == nil {
		return
	}
	rr.agent.notify.Publish(pubsub.CreatedEvent, notify.Notification{
		Type:            notify.TypeReasoningChanged,
		SessionID:       rr.sessionID,
		ProviderID:      rr.model.ModelCfg.Provider,
		ModelID:         rr.model.ModelCfg.Model,
		ReasoningTurnID: rr.turnID,
		ReasoningMode:   mode,
		ReasoningEffort: effort,
	})
}

// classifyEffort runs the configured small model to classify promptText into a
// difficulty tier. It is bounded by reasoningClassifierTimeout and cancellable
// via ctx; the caller supplies a fallback on error. Classifier spend is
// accumulated onto rr for later session-cost charging.
func (a *sessionAgent) classifyEffort(ctx context.Context, sessionID, promptText string, rr *runReasoning) (string, error) {
	small := a.smallModel.Get()
	if small.Model == nil {
		return "", errors.New("no small model configured for auto reasoning")
	}
	cctx, cancel := context.WithTimeout(ctx, reasoningClassifierTimeout)
	defer cancel()

	classifier := fantasy.NewAgent(
		small.Model,
		fantasy.WithSystemPrompt(reasoningClassifierSystemPrompt),
		fantasy.WithMaxOutputTokens(reasoningClassifierMaxTokens),
		fantasy.WithUserAgent(userAgent),
	)
	res, err := classifier.Stream(cctx, fantasy.AgentStreamCall{
		Prompt:  truncateClassifierInput(promptText),
		Headers: sessionHeaders(sessionID),
	})
	if err != nil {
		return "", err
	}
	rr.addClassifierCost(classifierUsageCost(small, res.TotalUsage))
	text := res.Response.Content.Text()
	tier := parseClassifierTier(text)
	if tier == "" {
		return "", fmt.Errorf("unparseable classification: %q", text)
	}
	return tier, nil
}

// classifierUsageCost computes the dollar cost of a classifier completion. A
// flat-rate small model contributes nothing.
func classifierUsageCost(model Model, usage fantasy.Usage) float64 {
	if model.FlatRate {
		return 0
	}
	mc := model.CatwalkCfg
	return mc.CostPer1MInCached/1e6*float64(usage.CacheCreationTokens) +
		mc.CostPer1MOutCached/1e6*float64(usage.CacheReadTokens) +
		mc.CostPer1MIn/1e6*float64(usage.InputTokens) +
		mc.CostPer1MOut/1e6*float64(usage.OutputTokens)
}

// truncateClassifierInput bounds the classifier prompt to keep classification
// fast and cheap, keeping the head and tail so both intent and any trailing
// keyword survive.
func truncateClassifierInput(text string) string {
	text = strings.TrimSpace(text)
	if len(text) <= reasoningClassifierInputLimit {
		return text
	}
	half := reasoningClassifierInputLimit / 2
	start, end := half, len(text)-half
	for start > 0 && !utf8.RuneStart(text[start]) {
		start--
	}
	for end < len(text) && !utf8.RuneStart(text[end]) {
		end++
	}
	return text[:start] + "\n…\n" + text[end:]
}

var (
	classifierTierXHigh  = regexp.MustCompile(`x[\s_-]?high`)
	classifierTierMax    = regexp.MustCompile(`\bmax\b`)
	classifierTierHigh   = regexp.MustCompile(`\bhigh\b`)
	classifierTierMedium = regexp.MustCompile(`\bmed(?:ium)?\b`)
	classifierTierLow    = regexp.MustCompile(`\blow\b`)
)

// parseClassifierTier maps the classifier output to a difficulty tier; the
// earliest keyword wins so a leading answer beats any trailing rationale. `max`
// is parsed even though the classifier is not offered it, so a stray answer is
// clamped down rather than failing the turn.
func parseClassifierTier(text string) string {
	lower := strings.ToLower(text)
	best := ""
	bestPos := -1
	consider := func(re *regexp.Regexp, tier string) {
		loc := re.FindStringIndex(lower)
		if loc == nil {
			return
		}
		if bestPos < 0 || loc[0] < bestPos {
			bestPos = loc[0]
			best = tier
		}
	}
	consider(classifierTierXHigh, "xhigh")
	consider(classifierTierMax, "max")
	consider(classifierTierHigh, "high")
	consider(classifierTierMedium, "medium")
	consider(classifierTierLow, "low")
	return best
}

// autoResolvedEffort clamps a classifier tier to the model's supported ladder
// without using a distinct max-only tier when a lower control exists.
func autoResolvedEffort(cm catwalk.Model, tier string) string {
	effort := reasoning.ClampEffort(cm, tier)
	levels := reasoning.Efforts(cm)
	ceiling := autoCeiling(levels)
	ei := indexOf(levels, effort)
	ci := indexOf(levels, ceiling)
	if ei >= 0 && ci >= 0 && ei > ci {
		return ceiling
	}
	return effort
}

// autoCeiling returns the strongest control auto may select: the highest
// supported control that is not the model's maximum "max" tier.
func autoCeiling(levels []string) string {
	for i := len(levels) - 1; i >= 0; i-- {
		if levels[i] != "max" {
			return levels[i]
		}
	}
	if len(levels) > 0 {
		return levels[0]
	}
	return ""
}

func indexOf(levels []string, v string) int {
	for i, l := range levels {
		if l == v {
			return i
		}
	}
	return -1
}

// reasoningBudgetBounds returns the min/max thinking-token budget for a
// budget-controlled model. Gemini 2.5 Pro is mandatory-thinking (min 128, max
// 32768); Gemini 2.5 Flash and other Gemini models may disable (min 0, max
// 24576); Anthropic and other budget-token models keep a 1024 minimum when
// enabled. These are the known legacy generateContent bounds.
func reasoningBudgetBounds(cm catwalk.Model) (minBudget, maxBudget int) {
	id := cm.ID
	if i := strings.LastIndexByte(id, '/'); i >= 0 {
		id = id[i+1:]
	}
	id = strings.ToLower(id)
	switch {
	case strings.HasPrefix(id, "gemini") && strings.Contains(id, "pro"):
		return 128, 32768
	case strings.HasPrefix(id, "gemini"):
		return 0, 24576
	default:
		return 1024, 32000
	}
}

// reasoningTierFraction maps a classifier tier to a fraction of the model's
// maximum thinking budget.
func reasoningTierFraction(tier string) float64 {
	switch tier {
	case "off", "none":
		return 0
	case "minimal":
		return 0.1
	case "low":
		return 0.2
	case "medium":
		return 0.4
	case "high":
		return 0.7
	case "xhigh":
		return 0.9
	case "max", "on":
		return 1.0
	default:
		return 0.4
	}
}

// reasoningThinkingBudget computes the thinking-token budget for a temporary
// reasoning decision on a budget-controlled model. A disabled decision floors
// to the model minimum (0 for disable-capable models, 128 for mandatory Pro);
// an enabled decision scales by the classifier tier, always kept within the
// model bounds and under the request's max output with room for the answer.
func reasoningThinkingBudget(cm catwalk.Model, o reasoningOverride) int {
	minBudget, maxBudget := reasoningBudgetBounds(cm)
	if o.effort == "" || o.effort == "off" || o.effort == "none" {
		return minBudget
	}
	frac := reasoningTierFraction(o.tier)
	if frac <= 0 {
		return minBudget
	}
	if o.maxOut > 0 {
		reserve := max(int(o.maxOut)/4, 1024)
		maxBudget = min(maxBudget, max(int(o.maxOut)-reserve, minBudget))
	} else {
		maxBudget = min(maxBudget, 2000)
	}
	budget := max(minBudget, int(float64(maxBudget)*frac))
	return budget
}
