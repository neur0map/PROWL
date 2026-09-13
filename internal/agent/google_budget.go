package agent

import (
	"context"
	"maps"
	"net/http"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/google"
)

// Fantasy v0.43 clamps every Google budget below 128, including the native
// API's supported 0 (off) and -1 (dynamic). Prowl owns budget serialization at
// the HTTP boundary instead: the encoder never receives the budget, and the
// request context carries it without mutating shared options or model state.
type googleBudgetProvider struct {
	fantasy.Provider
}

func newGoogleProvider(client *http.Client, opts ...google.Option) (fantasy.Provider, error) {
	opts = append(opts, google.WithHTTPClient(promptCacheHTTPClient(client)))
	provider, err := google.New(opts...)
	if err != nil {
		return nil, err
	}
	return googleBudgetProvider{provider}, nil
}

func (p googleBudgetProvider) LanguageModel(ctx context.Context, modelID string) (fantasy.LanguageModel, error) {
	model, err := p.Provider.LanguageModel(ctx, modelID)
	if err != nil {
		return nil, err
	}
	return googleBudgetModel{model}, nil
}

type googleBudgetModel struct {
	fantasy.LanguageModel
}

type googleBudgetKey struct{}

func googleBudgetCall(ctx context.Context, call fantasy.Call) (context.Context, fantasy.Call, error) {
	opts, ok := call.ProviderOptions[google.Name].(*google.ProviderOptions)
	if !ok || opts.ThinkingConfig == nil || opts.ThinkingConfig.ThinkingBudget == nil {
		return ctx, call, nil
	}
	if opts.ThinkingConfig.ThinkingLevel != nil {
		return ctx, call, &fantasy.Error{
			Title:   "invalid argument",
			Message: "thinking_level and thinking_budget are mutually exclusive",
		}
	}
	budget := *opts.ThinkingConfig.ThinkingBudget
	thinking := *opts.ThinkingConfig
	thinking.ThinkingBudget = nil
	options := *opts
	options.ThinkingConfig = &thinking
	call.ProviderOptions = maps.Clone(call.ProviderOptions)
	call.ProviderOptions[google.Name] = &options
	return context.WithValue(ctx, googleBudgetKey{}, budget), call, nil
}

func (m googleBudgetModel) Generate(ctx context.Context, call fantasy.Call) (*fantasy.Response, error) {
	ctx, call, err := googleBudgetCall(ctx, call)
	if err != nil {
		return nil, err
	}
	response, err := m.LanguageModel.Generate(ctx, call)
	if response != nil {
		response.Usage = normalizeGoogleUsage(response.Usage)
	}
	return response, err
}

func (m googleBudgetModel) Stream(ctx context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
	ctx, call, err := googleBudgetCall(ctx, call)
	if err != nil {
		return nil, err
	}
	stream, err := m.LanguageModel.Stream(ctx, call)
	if err != nil {
		return nil, err
	}
	return func(yield func(fantasy.StreamPart) bool) {
		for part := range stream {
			part.Usage = normalizeGoogleUsage(part.Usage)
			if !yield(part) {
				return
			}
		}
	}, nil
}

// Google includes cached input in promptTokenCount but reports thoughts outside
// candidatesTokenCount. Normalize once to Fantasy's disjoint billing buckets.
func normalizeGoogleUsage(usage fantasy.Usage) fantasy.Usage {
	usage.InputTokens = max(0, usage.InputTokens-usage.CacheReadTokens)
	usage.OutputTokens += usage.ReasoningTokens
	usage.TotalTokens = usage.InputTokens + usage.CacheReadTokens +
		usage.CacheCreationTokens + usage.OutputTokens
	return usage
}
