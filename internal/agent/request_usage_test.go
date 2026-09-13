package agent

import (
	"context"
	"testing"
	"time"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"charm.land/fantasy/providers/openrouter"
	"github.com/stretchr/testify/require"

	"github.com/neur0map/prowl/internal/config"
)

func TestReportedUsageSurvivesCancellationAndStaleSave(t *testing.T) {
	env := testEnv(t)
	parent, err := env.sessions.Create(t.Context(), "Parent")
	require.NoError(t, err)
	child, err := env.sessions.CreateTaskSession(t.Context(), "cancelled-child", parent.ID, "Child")
	require.NoError(t, err)
	child.PromptTokens, child.CompletionTokens = 5000, 700
	child, err = env.sessions.Save(t.Context(), child)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	inner := &fakeLanguageModel{stream: func(yield func(fantasy.StreamPart) bool) {
		if !yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextDelta, Delta: "partial", Usage: fantasy.Usage{InputTokens: 1000}}) {
			return
		}
		cancel()
		yield(fantasy.StreamPart{
			Type: fantasy.StreamPartTypeError, Error: context.Canceled,
			Usage: fantasy.Usage{InputTokens: 1000, OutputTokens: 100},
		})
	}}
	agent := &sessionAgent{sessions: env.sessions}
	model := Model{Model: inner, CatwalkCfg: catwalk.Model{CostPer1MIn: 10, CostPer1MOut: 20}}
	stream, err := agent.observeModel(model, child.ID, "conversation").Stream(ctx, fantasy.Call{})
	require.NoError(t, err)
	var streamErr error
	for part := range stream {
		if part.Error != nil {
			streamErr = part.Error
		}
	}
	require.ErrorIs(t, streamErr, context.Canceled)
	// A later context/todo save must not erase this cancelled request's cost.
	_, err = env.sessions.Save(t.Context(), child)
	require.NoError(t, err)
	charged, err := env.sessions.Get(t.Context(), child.ID)
	require.NoError(t, err)
	require.InDelta(t, 0.012, charged.Cost, 1e-12)
	require.Equal(t, int64(5000), charged.PromptTokens)
	require.Equal(t, int64(700), charged.CompletionTokens)
	chargedParent, err := env.sessions.Get(t.Context(), parent.ID)
	require.NoError(t, err)
	require.InDelta(t, 0.012, chargedParent.Cost, 1e-12)
}

func usageTestStream(text string, usage fantasy.Usage, reason fantasy.FinishReason) *fakeLanguageModel {
	return &fakeLanguageModel{stream: func(yield func(fantasy.StreamPart) bool) {
		for _, part := range []fantasy.StreamPart{
			{Type: fantasy.StreamPartTypeTextStart, ID: "text"},
			{Type: fantasy.StreamPartTypeTextDelta, ID: "text", Delta: text},
			{Type: fantasy.StreamPartTypeTextEnd, ID: "text"},
			{Type: fantasy.StreamPartTypeFinish, Usage: usage, FinishReason: reason},
		} {
			if !yield(part) {
				return
			}
		}
	}}
}

func TestTitleFallbackChargesEveryAttemptWithoutChangingContextSize(t *testing.T) {
	env := testEnv(t)
	current, err := env.sessions.Create(t.Context(), "Untitled")
	require.NoError(t, err)
	current.PromptTokens, current.CompletionTokens = 5000, 700
	_, err = env.sessions.Save(t.Context(), current)
	require.NoError(t, err)
	small := Model{
		Model:      usageTestStream("unfinished", fantasy.Usage{InputTokens: 1000, OutputTokens: 40}, fantasy.FinishReasonLength),
		CatwalkCfg: catwalk.Model{CostPer1MIn: 10, CostPer1MOut: 20},
	}
	large := Model{
		Model:      usageTestStream("Chosen title", fantasy.Usage{InputTokens: 1000, OutputTokens: 50}, fantasy.FinishReasonStop),
		CatwalkCfg: catwalk.Model{CostPer1MIn: 30, CostPer1MOut: 60, CanReason: true, DefaultMaxTokens: 500},
	}
	agent := NewSessionAgent(SessionAgentOptions{LargeModel: large, SmallModel: small, Sessions: env.sessions})
	agent.GenerateTitle(t.Context(), current.ID, "Investigate cached request accounting")
	updated, err := env.sessions.Get(t.Context(), current.ID)
	require.NoError(t, err)
	require.Equal(t, "Chosen title", updated.Title)
	require.InDelta(t, 0.0438, updated.Cost, 1e-12)
	require.Equal(t, int64(5000), updated.PromptTokens)
	require.Equal(t, int64(700), updated.CompletionTokens)
}

func TestConversationChargesCacheWritesAndReadsExactlyOnce(t *testing.T) {
	env := testEnv(t)
	current, err := env.sessions.Create(t.Context(), "Conversation")
	require.NoError(t, err)
	usage := fantasy.Usage{InputTokens: 1000, CacheCreationTokens: 2000, CacheReadTokens: 3000, OutputTokens: 100}
	model := Model{
		Model:      usageTestStream("Done.", usage, fantasy.FinishReasonStop),
		ModelCfg:   config.SelectedModel{Model: "fake-model", Provider: "fake"},
		CatwalkCfg: catwalk.Model{CostPer1MIn: 10, CostPer1MInCached: 12.5, CostPer1MOutCached: 1, CostPer1MOut: 20},
	}
	titleModel := model
	titleModel.Model = usageTestStream("Request title", fantasy.Usage{InputTokens: 100, OutputTokens: 10}, fantasy.FinishReasonStop)
	agent := NewSessionAgent(SessionAgentOptions{
		LargeModel: model, SmallModel: titleModel, Sessions: env.sessions, Messages: env.messages,
		DisableAutoSummarize: true,
	})
	result, err := agent.Run(t.Context(), SessionAgentCall{SessionID: current.ID, Prompt: "Answer directly", NonInteractive: true})
	require.NoError(t, err)
	require.Equal(t, "Done.", result.Response.Content.Text())
	// The background title is separately billable, but is not main-context
	// occupancy. Wait for its final state before observing the session total.
	require.Eventually(t, func() bool {
		saved, err := env.sessions.Get(t.Context(), current.ID)
		return err == nil && saved.Title == "Request title" && saved.Cost > 0.04
	}, 5*time.Second, time.Millisecond)
	updated, err := env.sessions.Get(t.Context(), current.ID)
	require.NoError(t, err)
	require.InDelta(t, 0.0412, updated.Cost, 1e-12)
	require.Equal(t, int64(6000), updated.PromptTokens)
	require.Equal(t, int64(100), updated.CompletionTokens)
}

func TestModelUsageCostDistinguishesReportedOverridesAndSubscriptions(t *testing.T) {
	t.Parallel()
	model := Model{CatwalkCfg: catwalk.Model{CostPer1MIn: 10, CostPer1MOut: 20}}
	usage := fantasy.Usage{InputTokens: 1000, OutputTokens: 100}
	override := 0.005
	cost, equivalent, source := modelUsageCost(model, usage, &override)
	require.Equal(t, override, cost)
	require.InDelta(t, 0.012, equivalent, 1e-12)
	require.Equal(t, "provider", source)
	model.FlatRate = true
	cost, equivalent, source = modelUsageCost(model, usage, &override)
	require.Zero(t, cost)
	require.InDelta(t, 0.012, equivalent, 1e-12)
	require.Equal(t, "subscription", source)
	model.FlatRate = false
	_, _, source = modelUsageCost(model, fantasy.Usage{TotalTokens: 100}, nil)
	require.Equal(t, "unreported", source, "an unsplit total does not establish a billable cost")
}

func TestProviderOverrideIsPersistedInsteadOfCatalogEstimate(t *testing.T) {
	env := testEnv(t)
	current, err := env.sessions.Create(t.Context(), "Override")
	require.NoError(t, err)
	model := Model{CatwalkCfg: catwalk.Model{CostPer1MIn: 10}}
	providerCost := 0.004
	metadata := fantasy.ProviderMetadata{openrouter.Name: &openrouter.ProviderMetadata{
		Usage: openrouter.UsageAccounting{Cost: providerCost},
	}}
	model.Model = &fakeLanguageModel{stream: func(yield func(fantasy.StreamPart) bool) {
		yield(fantasy.StreamPart{
			Type:  fantasy.StreamPartTypeFinish,
			Usage: fantasy.Usage{InputTokens: 1000}, ProviderMetadata: metadata,
		})
	}}
	agent := &sessionAgent{sessions: env.sessions}
	stream, err := agent.observeModel(model, current.ID, "classifier").Stream(t.Context(), fantasy.Call{})
	require.NoError(t, err)
	for range stream {
	}
	updated, err := env.sessions.Get(t.Context(), current.ID)
	require.NoError(t, err)
	require.Equal(t, providerCost, updated.Cost)
}

func TestShutdownDrainsCancelledTitleBillingAfterConversationFinishes(t *testing.T) {
	env := testEnv(t)
	current, err := env.sessions.Create(t.Context(), "Untitled")
	require.NoError(t, err)
	started := make(chan struct{})
	slow := &fakeLanguageModel{}
	slow.stream = func(yield func(fantasy.StreamPart) bool) {
		close(started)
		<-slow.streamCtx.Done()
		yield(fantasy.StreamPart{
			Type: fantasy.StreamPartTypeError, Error: slow.streamCtx.Err(),
			Usage: fantasy.Usage{InputTokens: 1000, OutputTokens: 10},
		})
	}
	a := NewSessionAgent(SessionAgentOptions{
		LargeModel: Model{Model: usageTestStream("Done.", fantasy.Usage{}, fantasy.FinishReasonStop)},
		SmallModel: Model{Model: slow, CatwalkCfg: catwalk.Model{CostPer1MIn: 10, CostPer1MOut: 20}},
		Sessions:   env.sessions, Messages: env.messages, DisableAutoSummarize: true,
	})
	_, err = a.Run(t.Context(), SessionAgentCall{
		SessionID: current.ID, Prompt: "Complete the request without waiting for its title.", NonInteractive: true,
	})
	require.NoError(t, err)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("Title request did not start")
	}
	require.False(t, a.IsBusy(), "background titles must not block interactive work")

	a.CancelAll()
	charged, err := env.sessions.Get(t.Context(), current.ID)
	require.NoError(t, err)
	require.InDelta(t, 0.0102, charged.Cost, 1e-12, "shutdown must finish partial title billing before returning")
}
