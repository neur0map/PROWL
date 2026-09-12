package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/google"
	"github.com/stretchr/testify/require"
)

func TestGoogleThinkingBudgetTransitions(t *testing.T) {
	t.Parallel()
	budgets := make(chan *int64, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			GenerationConfig struct {
				ThinkingConfig struct {
					ThinkingBudget *int64 `json:"thinkingBudget"`
				} `json:"thinkingConfig"`
			} `json:"generationConfig"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		budgets <- body.GenerationConfig.ThinkingConfig.ThinkingBudget
		response := `{"candidates":[{"content":{"role":"model","parts":[{"text":"Done."}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":2,"totalTokenCount":10}}`
		if strings.HasSuffix(r.URL.Path, ":streamGenerateContent") {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "data: %s\n\n", response)
		} else {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, response)
		}
	}))
	t.Cleanup(server.Close)

	provider, err := newGoogleProvider(nil, google.WithGeminiAPIKey("test-only"), google.WithBaseURL(server.URL))
	require.NoError(t, err)
	model, err := provider.LanguageModel(t.Context(), "gemini-2.5-flash")
	require.NoError(t, err)
	options := func(budget int64) fantasy.ProviderOptions {
		return fantasy.ProviderOptions{google.Name: &google.ProviderOptions{
			ThinkingConfig: &google.ThinkingConfig{ThinkingBudget: &budget},
		}}
	}
	request := func(opts fantasy.ProviderOptions, streaming bool) *int64 {
		t.Helper()
		call := fantasy.Call{
			Prompt:          []fantasy.Message{{Role: fantasy.MessageRoleUser, Content: []fantasy.MessagePart{fantasy.TextPart{Text: "Budget transition."}}}},
			ProviderOptions: opts,
		}
		if streaming {
			stream, err := model.Stream(t.Context(), call)
			require.NoError(t, err)
			for part := range stream {
				require.NotEqual(t, fantasy.StreamPartTypeError, part.Type, "%+v", part)
			}
		} else {
			_, err := model.Generate(t.Context(), call)
			require.NoError(t, err)
		}
		return <-budgets
	}

	// Reusing Off after a boosted stream must still disable thinking. This
	// exercises both API encoders and catches mutation of the saved options.
	off := options(0)
	require.Equal(t, new(int64(0)), request(off, false))
	require.Equal(t, new(int64(6144)), request(options(6144), true))
	require.Equal(t, new(int64(0)), request(off, true))
	// Dynamic thinking is a distinct native sentinel, not a minimum budget.
	require.Equal(t, new(int64(-1)), request(options(-1), false))
	require.Nil(t, request(nil, true), "an unspecified budget must retain the provider's default")
}
