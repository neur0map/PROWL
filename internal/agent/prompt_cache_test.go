package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"charm.land/fantasy/providers/anthropic"
	"charm.land/fantasy/providers/openai"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/neur0map/prowl/internal/config"
	"github.com/neur0map/prowl/internal/oauth"
)

func TestCacheCapabilitiesRejectUnsupportedWireControls(t *testing.T) {
	cases := []struct {
		name, protocol, model, ttl string
		mode                       string
		storage                    *float64
		oauth                      bool
		valid                      bool
	}{
		{name: "Anthropic hour", protocol: "anthropic", model: "claude-sonnet-4-5", ttl: "1h", valid: true},
		{name: "Older Bedrock", protocol: "bedrock", model: "anthropic.claude-3-7-sonnet", ttl: "1h"},
		{name: "Recent Bedrock", protocol: "bedrock", model: "anthropic.claude-sonnet-4-5", ttl: "1h", valid: true},
		{name: "Foreign Anthropic protocol", protocol: "anthropic", model: "minimax-m2.1", ttl: "1h"},
		{name: "OpenAI new writes", protocol: "openai", model: "gpt-5.6", ttl: "30m", mode: "explicit", valid: true},
		{name: "OpenAI old explicit", protocol: "openai", model: "gpt-4.1", mode: "explicit"},
		{name: "OpenAI documented retention", protocol: "openai", model: "gpt-4.1-2025-04-14", ttl: "24h", valid: true},
		{name: "OpenAI unsupported variant", protocol: "openai", model: "gpt-4.1-mini", ttl: "24h"},
		{name: "OpenAI subscription", protocol: "openai", model: "gpt-6-astra", mode: "explicit", oauth: true},
		{name: "Disabled subscription inheritance", protocol: "openai", model: "gpt-6-astra", ttl: "30m", mode: "off", oauth: true, valid: true},
		{name: "Gemini missing storage quote", protocol: "google", model: "gemini-3.8-flash", mode: "explicit"},
		{name: "Gemini paid storage", protocol: "google", model: "gemini-3.8-flash", mode: "explicit", storage: new(0.5), valid: true},
		{name: "Compatible endpoint", protocol: "openai-compat", model: "gpt-5.6", mode: "explicit"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			provider := config.ProviderConfig{
				ID: "test-provider", Type: catwalk.Type(test.protocol),
				PromptCache: &config.PromptCacheConfig{Mode: test.mode, TTL: test.ttl, StorageCostPer1MTokenHour: test.storage},
			}
			if test.oauth {
				provider.OAuthToken = &oauth.Token{}
			}
			_, err := resolvePromptCache(provider, config.SelectedModel{Provider: provider.ID, Model: test.model})
			if test.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestOpenAIWireCacheWritesAreDisjointAndCharged(t *testing.T) {
	env := testEnv(t)
	current, err := env.sessions.Create(t.Context(), "Native OpenAI")
	require.NoError(t, err)
	requests := make(chan json.RawMessage, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		requests <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"resp_test","object":"response","status":"completed","model":"gpt-5.6","output":[{"id":"msg_test","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Done.","annotations":[]}]}],"usage":{"input_tokens":6000,"input_tokens_details":{"cached_tokens":2000,"cache_write_tokens":3000},"output_tokens":100,"output_tokens_details":{"reasoning_tokens":20},"total_tokens":6100}}`)
	}))
	t.Cleanup(server.Close)
	selected := config.SelectedModel{
		Model: "gpt-5.6", Provider: openai.Name,
		PromptCache: &config.PromptCacheConfig{Mode: "explicit", TTL: "30m"},
	}
	c := &coordinator{cfg: config.NewTestStore(&config.Config{Options: &config.Options{}}), sessions: env.sessions}
	provider, err := c.buildProvider(config.ProviderConfig{
		ID: openai.Name, Type: openai.Name, APIKey: "test-only", BaseURL: server.URL,
	}, selected, false)
	require.NoError(t, err)
	inner, err := provider.LanguageModel(t.Context(), selected.Model)
	require.NoError(t, err)
	a := &sessionAgent{sessions: env.sessions}
	model := Model{
		Model: inner, ModelCfg: selected,
		CatwalkCfg: catwalk.Model{CostPer1MIn: 10, CostPer1MOut: 20, CostPer1MOutCached: 1},
	}
	response, err := a.observeModel(model, current.ID, "conversation").Generate(t.Context(), fantasy.Call{
		Prompt:  []fantasy.Message{fantasy.NewSystemMessage("Stable project rules."), fantasy.NewUserMessage("Answer directly.")},
		Headers: sessionHeaders(current.ID),
	})
	require.NoError(t, err)
	require.Equal(t, "Done.", response.Content.Text())
	require.Equal(t, fantasy.Usage{
		InputTokens: 1000, CacheCreationTokens: 3000, CacheReadTokens: 2000,
		OutputTokens: 100, ReasoningTokens: 20, TotalTokens: 6100,
	}, response.Usage)
	wire := gjson.ParseBytes(<-requests)
	require.Equal(t, "explicit", wire.Get("prompt_cache_options.mode").String())
	require.Equal(t, "30m", wire.Get("prompt_cache_options.ttl").String())
	require.Equal(t, sessionHeaders(current.ID)["x-session-id"], wire.Get("prompt_cache_key").String())
	require.Equal(t, "explicit", wire.Get("input.0.content.0.prompt_cache_breakpoint.mode").String())
	require.Equal(t, "explicit", wire.Get("input.1.content.0.prompt_cache_breakpoint.mode").String())
	charged, err := env.sessions.Get(t.Context(), current.ID)
	require.NoError(t, err)
	require.InDelta(t, 0.0515, charged.Cost, 1e-12)
}

func TestAnthropicCancellationChargesReportedStartUsage(t *testing.T) {
	env := testEnv(t)
	current, err := env.sessions.Create(t.Context(), "Native Anthropic")
	require.NoError(t, err)
	requests := make(chan json.RawMessage, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		requests <- body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: message_start\ndata: "+`{"type":"message_start","message":{"id":"msg_test","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[],"usage":{"input_tokens":1000,"cache_creation_input_tokens":2000,"cache_read_input_tokens":3000,"cache_creation":{"ephemeral_1h_input_tokens":2000}}}}`+"\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_start\ndata: "+`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`+"\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_delta\ndata: "+`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Partial."}}`+"\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)
	selected := config.SelectedModel{
		Model: "claude-sonnet-4-5", Provider: anthropic.Name,
		PromptCache: &config.PromptCacheConfig{TTL: "1h"},
	}
	c := &coordinator{cfg: config.NewTestStore(&config.Config{Options: &config.Options{}}), sessions: env.sessions}
	provider, err := c.buildProvider(config.ProviderConfig{
		ID: anthropic.Name, Type: anthropic.Name, APIKey: "test-only", BaseURL: server.URL,
	}, selected, false)
	require.NoError(t, err)
	inner, err := provider.LanguageModel(t.Context(), selected.Model)
	require.NoError(t, err)
	model := Model{
		Model: inner, ModelCfg: selected,
		CatwalkCfg: catwalk.Model{CostPer1MIn: 10, CostPer1MOut: 20, CostPer1MInCached: 12.5, CostPer1MOutCached: 1},
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	a := &sessionAgent{sessions: env.sessions}
	stream, err := a.observeModel(model, current.ID, "conversation").Stream(ctx, fantasy.Call{
		Prompt:          []fantasy.Message{fantasy.NewSystemMessage("Stable project rules."), fantasy.NewUserMessage("Answer directly.")},
		MaxOutputTokens: new(int64(100)),
	})
	require.NoError(t, err)
	for part := range stream {
		require.NoError(t, part.Error)
		if part.Type == fantasy.StreamPartTypeTextDelta {
			require.Equal(t, "Partial.", part.Delta)
			cancel()
			break
		}
	}
	wire := gjson.ParseBytes(<-requests)
	require.Equal(t, "1h", wire.Get("system.0.cache_control.ttl").String())
	require.Equal(t, "1h", wire.Get("messages.0.content.0.cache_control.ttl").String())
	charged, err := env.sessions.Get(t.Context(), current.ID)
	require.NoError(t, err)
	// No message_stop was received. Input and cache charges were nevertheless
	// reported by message_start; the unreported output remains unknown.
	require.InDelta(t, 0.053, charged.Cost, 1e-12)
}
