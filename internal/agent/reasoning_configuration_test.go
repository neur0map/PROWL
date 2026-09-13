package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"charm.land/fantasy/providers/openai"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/neur0map/prowl/internal/config"
	"github.com/neur0map/prowl/internal/message"
)

func TestNativeReasoningHistoryAppendsAndRestoresAcrossResume(t *testing.T) {
	env := testEnv(t)
	current, err := env.sessions.Create(t.Context(), "Reasoning timeline")
	require.NoError(t, err)
	requests := make(chan gjson.Result, 5)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		requests <- gjson.ParseBytes(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"resp_test","object":"response","status":"completed","model":"gpt-6-astra","output":[{"id":"msg_test","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Done.","annotations":[]}]}],"usage":{"input_tokens":100,"output_tokens":10,"total_tokens":110}}`)
	}))
	t.Cleanup(server.Close)
	providerCfg := config.ProviderConfig{ID: openai.Name, Type: openai.Name, APIKey: "test-only", BaseURL: server.URL}
	model := Model{
		ModelCfg:   config.SelectedModel{Provider: openai.Name, Model: "gpt-6-astra"},
		CatwalkCfg: catwalk.Model{ID: "gpt-6-astra", CanReason: true, ReasoningLevels: []string{"low", "high", "max"}},
	}
	var observed []gjson.Result
	for i, effort := range []string{"low", "max", "low"} {
		// Reconstruct the agent and provider each time. History, not a model
		// instance's mutable state, must establish the original baseline.
		a := &sessionAgent{sessions: env.sessions, messages: env.messages}
		c := &coordinator{cfg: config.NewTestStore(&config.Config{Options: &config.Options{}}), sessions: env.sessions}
		model.ModelCfg.ReasoningEffort = effort
		provider, err := c.buildProvider(providerCfg, model.ModelCfg, false)
		require.NoError(t, err)
		inner, err := provider.LanguageModel(t.Context(), model.ModelCfg.Model)
		require.NoError(t, err)
		model.Model = inner
		options := getProviderOptions(model, providerCfg, reasoningOverride{})
		user, err := a.createUserMessage(t.Context(), SessionAgentCall{SessionID: current.ID, Prompt: fmt.Sprintf("Request %d", i)})
		require.NoError(t, err)
		require.NoError(t, a.saveTurnSettings(t.Context(), &user, model, options, "", ""))
		history, err := env.messages.List(t.Context(), current.ID)
		require.NoError(t, err)
		response, err := inner.Generate(t.Context(), fantasy.Call{Prompt: a.preparePrompt(history, true), ProviderOptions: options})
		require.NoError(t, err)
		require.Equal(t, "Done.", response.Content.Text())
		observed = append(observed, <-requests)
		_, err = env.messages.Create(t.Context(), current.ID, message.CreateMessageParams{
			Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: response.Content.Text()}},
		})
		require.NoError(t, err)
	}
	for _, request := range observed {
		require.Equal(t, "low", request.Get("reasoning.effort").String())
	}
	first, second, third := observed[0].Get("input").Array(), observed[1].Get("input").Array(), observed[2].Get("input").Array()
	for i := range first {
		require.JSONEq(t, first[i].Raw, second[i].Raw)
	}
	for i := range second {
		require.JSONEq(t, second[i].Raw, third[i].Raw)
	}
	require.Equal(t, "max", observed[1].Get(`input.#(type=="configuration_update").reasoning.effort`).String())
	updates := observed[2].Get(`input.#(type=="configuration_update")#.reasoning.effort`).Array()
	require.Equal(t, []string{"max", "low"}, []string{updates[0].String(), updates[1].String()})

	// Compaction starts a new prefix. It must not invent historical effort
	// changes that are no longer present in the persisted context.
	summary, err := env.messages.Create(t.Context(), current.ID, message.CreateMessageParams{
		Role: message.Assistant, IsSummaryMessage: true, Parts: []message.ContentPart{message.TextContent{Text: "Work completed so far."}},
	})
	require.NoError(t, err)
	current.SummaryMessageID = summary.ID
	current, err = env.sessions.Save(t.Context(), current)
	require.NoError(t, err)
	a := &sessionAgent{sessions: env.sessions, messages: env.messages}
	model.ModelCfg.ReasoningEffort = "high"
	options := getProviderOptions(model, providerCfg, reasoningOverride{})
	user, err := a.createUserMessage(t.Context(), SessionAgentCall{SessionID: current.ID, Prompt: "Continue after compaction."})
	require.NoError(t, err)
	require.NoError(t, a.saveTurnSettings(t.Context(), &user, model, options, "", ""))
	history, err := a.getSessionMessages(t.Context(), current)
	require.NoError(t, err)
	_, err = model.Model.Generate(t.Context(), fantasy.Call{Prompt: a.preparePrompt(history, true), ProviderOptions: options})
	require.NoError(t, err)
	compacted := <-requests
	require.Equal(t, "high", compacted.Get("reasoning.effort").String())
	require.False(t, compacted.Get(`input.#(type=="configuration_update")`).Exists())
}
