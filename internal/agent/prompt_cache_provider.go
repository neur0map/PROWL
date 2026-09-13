package agent

import (
	"context"
	"maps"
	"net/http"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/openai"

	"github.com/neur0map/prowl/internal/session"
)

type promptCacheProvider struct {
	fantasy.Provider
	policy   promptCachePolicy
	sessions session.Service
	gate     chan struct{}
}

func (p promptCacheProvider) LanguageModel(ctx context.Context, modelID string) (fantasy.LanguageModel, error) {
	model, err := p.Provider.LanguageModel(ctx, modelID)
	if err != nil {
		return nil, err
	}
	return promptCacheModel{LanguageModel: model, provider: p}, nil
}

type promptCacheModel struct {
	fantasy.LanguageModel
	provider promptCacheProvider
}

type promptCacheRequestKey struct{}

type promptCacheRequest struct {
	policy      promptCachePolicy
	usage       *providerUsage
	sessionID   string
	sessions    session.Service
	gate        chan struct{}
	gemini      *geminiCacheApplication
	checkpoints []reasoningCheckpoint
	userCount   int
	reasoning   *openai.ResponsesProviderOptions
}

func (m promptCacheModel) prepare(ctx context.Context, call fantasy.Call) (context.Context, fantasy.Call, *providerUsage) {
	usage, _ := ctx.Value(providerUsageKey{}).(*providerUsage)
	if usage == nil {
		usage = &providerUsage{}
	}
	usage.mu.Lock()
	usage.protocol = string(m.provider.policy.protocol)
	usage.policy = m.provider.policy
	sessionID := usage.sessionID
	usage.mu.Unlock()
	request := &promptCacheRequest{
		policy: m.provider.policy, usage: usage, sessionID: sessionID,
		sessions: m.provider.sessions, gate: m.provider.gate,
	}
	if request.policy.configuration {
		request.checkpoints, request.userCount = reasoningCheckpoints(call.Prompt, request.policy)
	}
	if request.policy.protocol == openai.Name {
		call = prepareOpenAIRequestOptions(call, usesOpenAIResponses(m.Model()), request)
	}
	return context.WithValue(ctx, promptCacheRequestKey{}, request), call, usage
}

func (m promptCacheModel) Generate(ctx context.Context, call fantasy.Call) (*fantasy.Response, error) {
	ctx, call, usage := m.prepare(ctx, call)
	response, err := m.LanguageModel.Generate(ctx, call)
	if response != nil {
		response.Usage = usage.normalize(response.Usage)
	}
	return response, err
}

func (m promptCacheModel) Stream(ctx context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
	ctx, call, usage := m.prepare(ctx, call)
	stream, err := m.LanguageModel.Stream(ctx, call)
	if err != nil {
		return nil, err
	}
	return func(yield func(fantasy.StreamPart) bool) {
		for part := range stream {
			if !usageIsZero(part.Usage) || part.Type == fantasy.StreamPartTypeFinish || part.Type == fantasy.StreamPartTypeError {
				part.Usage = usage.normalize(part.Usage)
			}
			if !yield(part) {
				return
			}
		}
	}, nil
}

func prepareOpenAIRequestOptions(call fantasy.Call, responses bool, request *promptCacheRequest) fantasy.Call {
	key := call.Headers["x-session-id"]
	if request.policy.mode == "off" {
		key = ""
	}
	var options fantasy.ProviderOptionsData
	if responses {
		current, _ := call.ProviderOptions[openai.Name].(*openai.ResponsesProviderOptions)
		addKey := key != "" && (current == nil || current.PromptCacheKey == nil)
		adaptReasoning := request.policy.reasoning && current != nil && (current.ReasoningEffort != nil || current.ReasoningSummary != nil)
		if !addKey && !adaptReasoning {
			return call
		}
		copy := openai.ResponsesProviderOptions{}
		if current != nil {
			copy = *current
		}
		if addKey {
			copy.PromptCacheKey = &key
		}
		if adaptReasoning {
			// The SDK's model table predates Astra. Own serialization instead
			// of letting its capability gate silently drop valid controls.
			request.reasoning = current
			copy.ReasoningEffort, copy.ReasoningSummary = nil, nil
		}
		options = &copy
	} else {
		current, _ := call.ProviderOptions[openai.Name].(*openai.ProviderOptions)
		if key == "" || current != nil && current.PromptCacheKey != nil {
			return call
		}
		copy := openai.ProviderOptions{}
		if current != nil {
			copy = *current
		}
		copy.PromptCacheKey = &key
		options = &copy
	}
	call.ProviderOptions = maps.Clone(call.ProviderOptions)
	if call.ProviderOptions == nil {
		call.ProviderOptions = make(fantasy.ProviderOptions, 1)
	}
	call.ProviderOptions[openai.Name] = options
	return call
}

func promptCacheHTTPClient(client *http.Client) *http.Client {
	if client == nil {
		client = http.DefaultClient
	}
	if _, wrapped := client.Transport.(promptCacheTransport); wrapped {
		return client
	}
	copy := *client
	base := copy.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	copy.Transport = promptCacheTransport{RoundTripper: base}
	return &copy
}
