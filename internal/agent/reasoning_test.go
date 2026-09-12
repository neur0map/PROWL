package agent

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"charm.land/fantasy/providers/anthropic"
	"charm.land/fantasy/providers/google"
	"charm.land/fantasy/providers/openai"
	"charm.land/fantasy/providers/openaicompat"
	"charm.land/fantasy/providers/openrouter"
	"github.com/neur0map/prowl/internal/agent/notify"
	"github.com/neur0map/prowl/internal/config"
	"github.com/neur0map/prowl/internal/message"
	"github.com/neur0map/prowl/internal/pubsub"
	"github.com/neur0map/prowl/internal/reasoning"
	"github.com/stretchr/testify/require"
)

// The provider-payload tests exercise getProviderOptions the way the run's
// shapeReasoning closure does: the run-local model already carries the resolved
// effort/think, and the override signals that the persisted reasoning fields
// must be replaced while unrelated user options are preserved.

func TestGetProviderOptionsDiscreteOverrideWinsAndPreserves(t *testing.T) {
	model := Model{
		CatwalkCfg: catwalk.Model{
			ID:              "deepseek-reasoner",
			CanReason:       true,
			ReasoningLevels: []string{"low", "medium", "high"},
		},
		ModelCfg: config.SelectedModel{
			// shapeReasoning sets this to the resolved effort for the turn.
			ReasoningEffort: "high",
			// A persisted reasoning field the temporary decision must override,
			// plus an unrelated user option that must survive.
			ProviderOptions: map[string]any{
				"reasoning_effort":    "low",
				"parallel_tool_calls": false,
			},
		},
	}
	providerCfg := config.ProviderConfig{Type: openai.Name, ID: "openai"}

	opts := getProviderOptions(model, providerCfg, reasoningOverride{active: true, effort: "high", tier: "high", maxOut: 8192})

	parsed, ok := opts[openai.Name].(*openai.ProviderOptions)
	require.True(t, ok, "expected openai provider options")
	require.NotNil(t, parsed.ReasoningEffort)
	require.Equal(t, openai.ReasoningEffort("high"), *parsed.ReasoningEffort, "override must win over persisted reasoning_effort")
	require.NotNil(t, parsed.ParallelToolCalls, "unrelated user option must be preserved")
	require.False(t, *parsed.ParallelToolCalls)
}

func TestGetProviderOptionsDiscreteManualUnchanged(t *testing.T) {
	model := Model{
		CatwalkCfg: catwalk.Model{
			ID:              "deepseek-reasoner",
			CanReason:       true,
			ReasoningLevels: []string{"low", "medium", "high"},
		},
		ModelCfg: config.SelectedModel{
			ProviderOptions: map[string]any{"reasoning_effort": "low"},
		},
	}
	providerCfg := config.ProviderConfig{Type: openai.Name, ID: "openai"}

	// Inactive override reproduces the persisted behaviour: the user's explicit
	// reasoning_effort is respected, not replaced.
	opts := getProviderOptions(model, providerCfg, reasoningOverride{})
	parsed, ok := opts[openai.Name].(*openai.ProviderOptions)
	require.True(t, ok)
	require.NotNil(t, parsed.ReasoningEffort)
	require.Equal(t, openai.ReasoningEffort("low"), *parsed.ReasoningEffort)
}

func TestGetProviderOptionsAnthropicBudgetScaled(t *testing.T) {
	model := Model{
		CatwalkCfg: catwalk.Model{ID: "claude-sonnet-4", CanReason: true},
		ModelCfg: config.SelectedModel{
			// shapeReasoning turns Think on for an enabled decision.
			Think: true,
			// A persisted thinking budget the override must replace, plus an
			// unrelated option that must survive.
			ProviderOptions: map[string]any{
				"thinking":       map[string]any{"budget_tokens": 500},
				"send_reasoning": true,
			},
		},
	}
	providerCfg := config.ProviderConfig{Type: anthropic.Name, ID: "anthropic"}

	opts := getProviderOptions(model, providerCfg, reasoningOverride{active: true, effort: "on", tier: "max", maxOut: 64000})
	parsed, ok := opts[anthropic.Name].(*anthropic.ProviderOptions)
	require.True(t, ok, "expected anthropic provider options")
	require.NotNil(t, parsed.Thinking)
	require.Equal(t, int64(32000), parsed.Thinking.BudgetTokens, "ultrathink must raise the actual budget, not keep the persisted 500 or default 2000")
	require.NotNil(t, parsed.SendReasoning, "unrelated user option must be preserved")
	require.True(t, *parsed.SendReasoning)
}

func TestGetProviderOptionsGeminiFlashDisableViaOverride(t *testing.T) {
	model := Model{
		CatwalkCfg: catwalk.Model{ID: "gemini-2.5-flash", CanReason: true},
		ModelCfg:   config.SelectedModel{},
	}
	providerCfg := config.ProviderConfig{Type: google.Name, ID: "google"}

	// Auto classified the follow-up as trivial: a disable-capable Flash turns
	// thinking off (budget 0).
	opts := getProviderOptions(model, providerCfg, reasoningOverride{active: true, effort: "off", tier: "off", maxOut: 8192})
	parsed, ok := opts[google.Name].(*google.ProviderOptions)
	require.True(t, ok, "expected google provider options")
	require.NotNil(t, parsed.ThinkingConfig)
	require.NotNil(t, parsed.ThinkingConfig.ThinkingBudget)
	require.Equal(t, int64(0), *parsed.ThinkingConfig.ThinkingBudget)
}

func TestReasoningThinkingBudget(t *testing.T) {
	pro := catwalk.Model{ID: "gemini-2.5-pro", CanReason: true}
	flash := catwalk.Model{ID: "gemini-2.5-flash", CanReason: true}
	claude := catwalk.Model{ID: "claude-sonnet-4", CanReason: true}

	cases := []struct {
		name   string
		model  catwalk.Model
		over   reasoningOverride
		expect int
	}{
		{"pro mandatory min-on for trivial tier", pro, reasoningOverride{effort: "on", tier: "off", maxOut: 64000}, 128},
		{"pro ultra reaches its max", pro, reasoningOverride{effort: "on", tier: "max", maxOut: 64000}, 32768},
		{"flash disables when off", flash, reasoningOverride{effort: "off", tier: "off", maxOut: 64000}, 0},
		{"flash ultra reaches its max", flash, reasoningOverride{effort: "on", tier: "max", maxOut: 64000}, 24576},
		{"anthropic bounded under small max output", claude, reasoningOverride{effort: "on", tier: "max", maxOut: 8192}, 6144},
		{"anthropic medium tier scales", claude, reasoningOverride{effort: "on", tier: "medium", maxOut: 64000}, 12800},
		{"anthropic disabled floors to enabled min", claude, reasoningOverride{effort: "off", tier: "off", maxOut: 64000}, 1024},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.expect, reasoningThinkingBudget(tc.model, tc.over))
		})
	}
}

func TestParseClassifierTier(t *testing.T) {
	cases := []struct {
		in     string
		expect string
	}{
		{"high", "high"},
		{"  MEDIUM\n", "medium"},
		{"xhigh", "xhigh"},
		{"x-high", "xhigh"},
		{"low", "low"},
		{"med", "medium"},
		{"The difficulty is high.", "high"},
		// Earliest keyword wins over a later one.
		{"low, but leaning high", "low"},
		{"", ""},
		{"unparseable answer", ""},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			require.Equal(t, tc.expect, parseClassifierTier(tc.in))
		})
	}
}

func TestAutoResolvedEffortNeverSelectsMax(t *testing.T) {
	full := catwalk.Model{CanReason: true, ReasoningLevels: []string{"low", "medium", "high", "xhigh", "max"}}
	require.Equal(t, "xhigh", autoResolvedEffort(full, "max"), "auto tops out below the model max")
	require.Equal(t, "xhigh", autoResolvedEffort(full, "xhigh"))
	require.Equal(t, "high", autoResolvedEffort(full, "high"))
	require.Equal(t, "low", autoResolvedEffort(full, "low"))

	capped := catwalk.Model{CanReason: true, ReasoningLevels: []string{"low", "medium", "high"}}
	require.Equal(t, "high", autoResolvedEffort(capped, "xhigh"), "clamps down to the strongest supported level")

	// Sparse ladder: a request for a missing tier snaps up to the next
	// supported one, but auto still never reaches max.
	sparse := catwalk.Model{CanReason: true, ReasoningLevels: []string{"low", "xhigh", "max"}}
	require.Equal(t, "xhigh", autoResolvedEffort(sparse, "high"))
	require.Equal(t, "xhigh", autoResolvedEffort(sparse, "max"))
}

func TestAutoResolvedEffortToggleModel(t *testing.T) {
	toggle := catwalk.Model{CanReason: true}
	require.Equal(t, "on", autoResolvedEffort(toggle, "high"))
	require.Equal(t, "off", autoResolvedEffort(toggle, "low"))

	pro := catwalk.Model{ID: "gemini-2.5-pro", CanReason: true}
	require.Equal(t, "on", autoResolvedEffort(pro, "low"), "mandatory-thinking never resolves to off")
}

func TestReasoningOverridePreservesExtraBody(t *testing.T) {
	t.Parallel()
	model := Model{
		CatwalkCfg: catwalk.Model{CanReason: true, ReasoningLevels: []string{"low", "high"}},
		ModelCfg: config.SelectedModel{ReasoningEffort: "high", ProviderOptions: map[string]any{
			"extra_body": map[string]any{"top_k": 17, "reasoning_effort": "low"},
		}},
	}
	for _, providerType := range []string{openaicompat.Name, "ollama"} {
		opts := getProviderOptions(model, config.ProviderConfig{ID: "local", Type: catwalk.Type(providerType)}, reasoningOverride{active: true, effort: "high"})
		parsed := opts[openaicompat.Name].(*openaicompat.ProviderOptions)
		require.EqualValues(t, 17, parsed.ExtraBody["top_k"])
		require.Equal(t, "high", string(*parsed.ReasoningEffort))
		require.NotContains(t, parsed.ExtraBody, "reasoning_effort", "the stale nested effort must not override this request")
	}
}

func TestReasoningToggleOptionsRespectSelection(t *testing.T) {
	t.Parallel()
	model := Model{CatwalkCfg: catwalk.Model{ID: "gemini-2.5-flash", CanReason: true}, ModelCfg: config.SelectedModel{ReasoningEffort: "off", Think: true}}
	opts := getProviderOptions(model, config.ProviderConfig{Type: google.Name}, reasoningOverride{})
	require.Zero(t, *opts[google.Name].(*google.ProviderOptions).ThinkingConfig.ThinkingBudget, "explicit Off must override the legacy Think flag")
	model.ModelCfg.ReasoningEffort = "on"
	opts = getProviderOptions(model, config.ProviderConfig{Type: google.Name}, reasoningOverride{})
	require.Positive(t, *opts[google.Name].(*google.ProviderOptions).ThinkingConfig.ThinkingBudget)

	model.ModelCfg.ProviderOptions = map[string]any{"reasoning": map[string]any{"exclude": true, "max_tokens": 1024}}
	opts = getProviderOptions(model, config.ProviderConfig{Type: openrouter.Name}, reasoningOverride{active: true, effort: "on", tier: "max", maxOut: 64000})
	routed := opts[openrouter.Name].(*openrouter.ProviderOptions).Reasoning
	require.True(t, *routed.Exclude, "the override must preserve reasoning visibility preferences")
	require.True(t, *routed.Enabled)
	require.Greater(t, *routed.MaxTokens, int64(1024), "a stale budget cannot cap ultrathink")
}

func TestReasoningRejectsImpossibleThinkingBudget(t *testing.T) {
	t.Parallel()
	model := Model{CatwalkCfg: catwalk.Model{ID: "claude-sonnet-4", CanReason: true}, ModelCfg: config.SelectedModel{Think: true}}
	rr := &runReasoning{model: model, maxOut: 512, shape: func(o reasoningOverride) fantasy.ProviderOptions {
		o.maxOut = 512
		return getProviderOptions(model, config.ProviderConfig{Type: anthropic.Name}, o)
	}}
	err := rr.apply(t.Context(), "ultrathink solve this")
	require.ErrorContains(t, err, "max_tokens")
	require.ErrorContains(t, err, "512")
}

func TestReasoningClassifierTruncationPreservesUnicode(t *testing.T) {
	t.Parallel()
	input := "x" + strings.Repeat("界", reasoningClassifierInputLimit)
	output := truncateClassifierInput(input)
	require.True(t, utf8.ValidString(output))
	require.LessOrEqual(t, len(output), reasoningClassifierInputLimit+len("\n…\n"))
	require.True(t, strings.HasPrefix(input, strings.Split(output, "\n…\n")[0]))
	require.True(t, strings.HasSuffix(input, strings.Split(output, "\n…\n")[1]))
}

type reasoningCaptureModel struct {
	fantasy.LanguageModel
	calls chan fantasy.Call
}

func (m *reasoningCaptureModel) Stream(ctx context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
	call.Prompt = slices.Clone(call.Prompt)
	m.calls <- call
	return m.LanguageModel.Stream(ctx, call)
}

func receiveReasoningCall(t *testing.T, calls <-chan fantasy.Call) fantasy.Call {
	t.Helper()
	select {
	case call := <-calls:
		return call
	case <-time.After(5 * time.Second):
		t.Fatal("no model request arrived")
		return fantasy.Call{}
	}
}

func requestHasUltrathinkNotice(call fantasy.Call) bool {
	for _, msg := range call.Prompt {
		for _, part := range msg.Content {
			if text, ok := part.(fantasy.TextPart); ok && text.Text == reasoningUltrathinkNotice {
				return true
			}
		}
	}
	return false
}

func TestReasoningRunQueuedPromptRestoresPreference(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ mode, classification, nextEffort string }{
		{"medium", "low", "medium"},
		{reasoning.Auto, "low", "low"},
		{reasoning.Auto, "unparseable", "medium"},
	} {
		t.Run(tt.mode+"/"+tt.classification, func(t *testing.T) {
			t.Parallel()
			env := testEnv(t)
			gated := &gatedStreamModel{text: "done", gate: make(chan struct{}), entered: make(chan struct{})}
			large := &reasoningCaptureModel{LanguageModel: gated, calls: make(chan fantasy.Call, 4)}
			small := &reasoningCaptureModel{LanguageModel: &finishStreamModel{text: tt.classification}, calls: make(chan fantasy.Call, 4)}
			model := Model{Model: large, CatwalkCfg: catwalk.Model{ID: "test", CanReason: true, ContextWindow: 200000, ReasoningLevels: []string{"low", "medium", "high", "xhigh"}}, ModelCfg: config.SelectedModel{ReasoningEffort: tt.mode}}
			sa := NewSessionAgent(SessionAgentOptions{LargeModel: model, SmallModel: Model{Model: small}, IsYolo: true, Sessions: env.sessions, Messages: env.messages}).(*sessionAgent)
			sess, err := env.sessions.Create(t.Context(), "existing session")
			require.NoError(t, err)
			_, err = env.messages.Create(t.Context(), sess.ID, message.CreateMessageParams{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "earlier question"}}})
			require.NoError(t, err)
			shape := func(o reasoningOverride) fantasy.ProviderOptions {
				current := model
				if o.active {
					current.ModelCfg.ReasoningEffort = o.effort
				}
				return getProviderOptions(current, config.ProviderConfig{Type: openai.Name}, o)
			}
			done := make(chan error, 1)
			go func() {
				_, err := sa.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, RunID: "first", Prompt: "ultrathink solve the race", ShapeReasoning: shape})
				done <- err
			}()
			first := receiveReasoningCall(t, large.calls)
			require.Equal(t, "xhigh", string(*first.ProviderOptions[openai.Name].(*openai.ProviderOptions).ReasoningEffort))
			require.True(t, requestHasUltrathinkNotice(first))
			select {
			case <-small.calls:
				t.Fatal("ultrathink must bypass the classifier")
			default:
			}
			_, err = sa.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, RunID: "second", Prompt: "a plain follow-up", ShapeReasoning: shape})
			require.NoError(t, err)
			close(gated.gate)
			second := receiveReasoningCall(t, large.calls)
			require.Equal(t, tt.nextEffort, string(*second.ProviderOptions[openai.Name].(*openai.ProviderOptions).ReasoningEffort))
			require.False(t, requestHasUltrathinkNotice(second), "the prior notice must not enter history")
			select {
			case err := <-done:
				require.NoError(t, err)
			case <-time.After(5 * time.Second):
				t.Fatal("queued reasoning run did not complete")
			}
			require.Equal(t, tt.mode, sa.Model().ModelCfg.ReasoningEffort)
		})
	}
}

type reasoningErrorModel struct {
	finishStreamModel
	waitForCancel bool
}

func (m *reasoningErrorModel) Stream(ctx context.Context, _ fantasy.Call) (fantasy.StreamResponse, error) {
	if m.waitForCancel {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return nil, errors.New("provider rejected request")
}

func TestReasoningRunClearsOverrideAfterFailure(t *testing.T) {
	t.Parallel()
	for _, cancelRun := range []bool{false, true} {
		name := "provider error"
		if cancelRun {
			name = "cancellation"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			env := testEnv(t)
			failing := &reasoningErrorModel{waitForCancel: cancelRun}
			captured := &reasoningCaptureModel{LanguageModel: failing, calls: make(chan fantasy.Call, 4)}
			model := Model{Model: captured, CatwalkCfg: catwalk.Model{CanReason: true, ContextWindow: 200000, ReasoningLevels: []string{"low", "medium", "high"}}, ModelCfg: config.SelectedModel{ReasoningEffort: "low"}}
			events := pubsub.NewBroker[notify.Notification]()
			t.Cleanup(events.Shutdown)
			notices := events.Subscribe(t.Context())
			sa := NewSessionAgent(SessionAgentOptions{LargeModel: model, IsYolo: true, Sessions: env.sessions, Messages: env.messages, Notify: events}).(*sessionAgent)
			sess, err := env.sessions.Create(t.Context(), "existing session")
			require.NoError(t, err)
			_, err = env.messages.Create(t.Context(), sess.ID, message.CreateMessageParams{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "earlier question"}}})
			require.NoError(t, err)
			shape := func(o reasoningOverride) fantasy.ProviderOptions {
				current := model
				if o.active {
					current.ModelCfg.ReasoningEffort = o.effort
				}
				return getProviderOptions(current, config.ProviderConfig{Type: openai.Name}, o)
			}
			done := make(chan error, 1)
			go func() {
				_, err := sa.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "ultrathink inspect the failure", ShapeReasoning: shape})
				done <- err
			}()
			first := receiveReasoningCall(t, captured.calls)
			require.Equal(t, "high", string(*first.ProviderOptions[openai.Name].(*openai.ProviderOptions).ReasoningEffort))
			if cancelRun {
				sa.Cancel(sess.ID)
			}
			select {
			case err := <-done:
				require.Error(t, err)
			case <-time.After(5 * time.Second):
				t.Fatal("failed run did not settle")
			}
			var start, end notify.Notification
			for end.ReasoningTurnID == "" {
				select {
				case event := <-notices:
					if event.Payload.Type != notify.TypeReasoningChanged {
						continue
					}
					if event.Payload.ReasoningEffort == "" {
						end = event.Payload
					} else {
						start = event.Payload
					}
				case <-time.After(5 * time.Second):
					t.Fatal("failed run did not clear temporary reasoning")
				}
			}
			require.NotEmpty(t, start.ReasoningTurnID)
			require.Equal(t, start.ReasoningTurnID, end.ReasoningTurnID)
			captured.LanguageModel = &finishStreamModel{text: "recovered"}
			_, err = sa.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "a plain retry", ShapeReasoning: shape})
			require.NoError(t, err)
			retry := receiveReasoningCall(t, captured.calls)
			require.Equal(t, "low", string(*retry.ProviderOptions[openai.Name].(*openai.ProviderOptions).ReasoningEffort))
			require.False(t, requestHasUltrathinkNotice(retry))
		})
	}
}

func TestReasoningBudgetsDistinguishEffortWithinOutputCap(t *testing.T) {
	t.Parallel()
	model := Model{CatwalkCfg: catwalk.Model{ID: "claude-sonnet-4", CanReason: true}, ModelCfg: config.SelectedModel{Think: true}}
	var previous int64
	for _, tier := range []string{"medium", "high", "max"} {
		opts := getProviderOptions(model, config.ProviderConfig{Type: anthropic.Name}, reasoningOverride{active: true, effort: "on", tier: tier, maxOut: 8192})
		budget := opts[anthropic.Name].(*anthropic.ProviderOptions).Thinking.BudgetTokens
		require.Greater(t, budget, previous, "higher effort must raise the actual budget even with an output cap")
		require.Less(t, budget, int64(8192), "the response still needs output tokens")
		previous = budget
	}
}
