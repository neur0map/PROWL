package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"charm.land/fantasy/providers/google"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/neur0map/prowl/internal/config"
)

func TestGeminiCacheReuseReloadInvalidationAndChangedPrefix(t *testing.T) {
	env := testEnv(t)
	current, err := env.sessions.Create(t.Context(), "Gemini lifecycle")
	require.NoError(t, err)
	var creations atomic.Int64
	var missing atomic.Bool
	requests := make(chan gjson.Result, 16)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		root := gjson.ParseBytes(body)
		requests <- root
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/cachedContents") {
			id := creations.Add(1)
			duration, err := time.ParseDuration(root.Get("ttl").String())
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			now := time.Now().UTC()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name": fmt.Sprintf("cachedContents/cache-%d", id), "createTime": now,
				"expireTime": now.Add(duration), "usageMetadata": map[string]any{"totalTokenCount": 10000},
			})
			return
		}
		cached := root.Get("cachedContent").String() != ""
		if cached && missing.Swap(false) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `{"error":{"code":404,"status":"NOT_FOUND","message":"Cached content not found"}}`)
			return
		}
		read := 0
		if cached {
			read = 10000
		}
		_, _ = fmt.Fprintf(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"Done."}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10100,"cachedContentTokenCount":%d,"candidatesTokenCount":10,"totalTokenCount":10110}}`, read)
	}))
	t.Cleanup(server.Close)
	selected := config.SelectedModel{
		Model: "gemini-3.8-flash", Provider: google.Name,
		PromptCache: &config.PromptCacheConfig{Mode: "explicit", TTL: "5m", StorageCostPer1MTokenHour: new(0.5)},
	}
	catalog := catwalk.Model{ID: selected.Model, CostPer1MIn: 2, CostPer1MOut: 4, CostPer1MOutCached: 0.2}
	newModel := func() fantasy.LanguageModel {
		t.Helper()
		// A fresh coordinator/provider cannot rely on a process-local lease.
		c := &coordinator{
			cfg:      config.NewTestStore(&config.Config{Options: &config.Options{}}),
			sessions: env.sessions, cacheGate: make(chan struct{}, 1),
		}
		provider, err := c.buildProvider(config.ProviderConfig{
			ID: google.Name, Type: google.Name, APIKey: "test-only", BaseURL: server.URL,
			Models: []catwalk.Model{catalog},
		}, selected, false)
		require.NoError(t, err)
		inner, err := provider.LanguageModel(t.Context(), selected.Model)
		require.NoError(t, err)
		a := &sessionAgent{sessions: env.sessions}
		return a.observeModel(Model{Model: inner, ModelCfg: selected, CatwalkCfg: catalog}, current.ID, "conversation")
	}
	call := fantasy.Call{Prompt: []fantasy.Message{
		fantasy.NewSystemMessage("Stable project rules."), fantasy.NewUserMessage("Answer directly."),
	}, Headers: sessionHeaders(current.ID)}
	generate := func(model fantasy.LanguageModel, cached bool) {
		t.Helper()
		response, err := model.Generate(t.Context(), call)
		require.NoError(t, err)
		require.Equal(t, "Done.", response.Content.Text())
		if cached {
			require.Equal(t, int64(10000), response.Usage.CacheReadTokens)
			require.Equal(t, int64(100), response.Usage.InputTokens)
		} else {
			require.Zero(t, response.Usage.CacheReadTokens)
			require.Equal(t, int64(10100), response.Usage.InputTokens)
		}
	}
	generate(newModel(), true)
	reloaded := newModel()
	call.Prompt = append(call.Prompt,
		fantasy.Message{Role: fantasy.MessageRoleAssistant, Content: []fantasy.MessagePart{fantasy.TextPart{Text: "Prior answer."}}},
		fantasy.NewUserMessage("Continue."))
	generate(reloaded, true)
	require.Equal(t, int64(1), creations.Load())
	missing.Store(true)
	generate(reloaded, false)
	generate(reloaded, true)
	call.Prompt[0] = fantasy.NewSystemMessage("Revised project rules.")
	generate(reloaded, true)
	require.Equal(t, int64(3), creations.Load())
	firstCreate := <-requests
	require.Equal(t, "models/gemini-3.8-flash", firstCreate.Get("model").String())
	require.Equal(t, "Stable project rules.", firstCreate.Get("systemInstruction.parts.0.text").String())
	first := <-requests
	require.Equal(t, "cachedContents/cache-1", first.Get("cachedContent").String())
	require.False(t, first.Get("systemInstruction").Exists(), "immutable cached fields must not also be sent to generation")
	second := <-requests
	require.Equal(t, first.Get("cachedContent").String(), second.Get("cachedContent").String())
	<-requests // The rejected cached request.
	uncached := <-requests
	require.False(t, uncached.Get("cachedContent").Exists())
	require.Equal(t, "Stable project rules.", uncached.Get("systemInstruction.parts.0.text").String())
	charged, err := env.sessions.Get(t.Context(), current.ID)
	require.NoError(t, err)
	// Three committed 10k-token, five-minute leases at $0.50/M-token/hour;
	// four cached requests and one uncached retry, including all output.
	require.InDelta(t, 0.03045, charged.Cost, 1e-12)
}

func TestGeminiCacheSmallPrefixFallsBackButAuthFailureDoesNot(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			env := testEnv(t)
			current, err := env.sessions.Create(t.Context(), "Gemini cache error")
			require.NoError(t, err)
			var generations atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(r.URL.Path, "/cachedContents") {
					w.WriteHeader(status)
					_, _ = fmt.Fprint(w, `{"error":{"status":"INVALID_ARGUMENT","message":"Cached content is too small. total_token_count=100, min_total_token_count=1024"}}`)
					return
				}
				generations.Add(1)
				_, _ = fmt.Fprint(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"Done."}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":10,"totalTokenCount":110}}`)
			}))
			t.Cleanup(server.Close)
			selected := config.SelectedModel{
				Model: "gemini-3.8-flash", Provider: google.Name,
				PromptCache: &config.PromptCacheConfig{Mode: "explicit", StorageCostPer1MTokenHour: new(0.5)},
			}
			c := &coordinator{cfg: config.NewTestStore(&config.Config{Options: &config.Options{}}), sessions: env.sessions}
			provider, err := c.buildProvider(config.ProviderConfig{
				ID: google.Name, Type: google.Name, APIKey: "test-only", BaseURL: server.URL,
			}, selected, false)
			require.NoError(t, err)
			inner, err := provider.LanguageModel(t.Context(), selected.Model)
			require.NoError(t, err)
			a := &sessionAgent{sessions: env.sessions}
			model := a.observeModel(Model{Model: inner, ModelCfg: selected}, current.ID, "conversation")
			response, err := model.Generate(t.Context(), fantasy.Call{Prompt: []fantasy.Message{
				fantasy.NewSystemMessage("Brief rules."), fantasy.NewUserMessage("Answer."),
			}})
			if status == http.StatusBadRequest {
				require.NoError(t, err)
				require.Equal(t, "Done.", response.Content.Text())
				require.Equal(t, int64(1), generations.Load())
			} else {
				require.Error(t, err)
				require.Zero(t, generations.Load())
			}
		})
	}
}
