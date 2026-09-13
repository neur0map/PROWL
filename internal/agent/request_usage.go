package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"hash"
	"log/slog"
	"time"

	"charm.land/fantasy"

	"github.com/neur0map/prowl/internal/session"
)

// observedModel charges individual provider attempts, not successful agent
// turns. This includes title fallbacks, classifiers, summaries, and usage
// received before a stream or a subsequent tool operation fails.
type observedModel struct {
	fantasy.LanguageModel
	agent     *sessionAgent
	model     Model
	sessionID string
	purpose   string
}

func (a *sessionAgent) observeModel(model Model, sessionID, purpose string) fantasy.LanguageModel {
	return observedModel{
		LanguageModel: model.Model,
		agent:         a, model: model, sessionID: sessionID, purpose: purpose,
	}
}

type requestUsage struct {
	started     time.Time
	firstOutput time.Duration
	firstText   time.Duration
	usage       fantasy.Usage
	metadata    fantasy.ProviderMetadata
	outcome     string
	raw         *providerUsage
}

func (m observedModel) Generate(ctx context.Context, call fantasy.Call) (*fantasy.Response, error) {
	request := requestUsage{started: time.Now(), outcome: "complete"}
	request.raw = &providerUsage{sessionID: m.sessionID}
	ctx = context.WithValue(ctx, providerUsageKey{}, request.raw)
	m.logPreparedRequest(ctx, call)
	response, err := m.LanguageModel.Generate(ctx, call)
	if response != nil {
		request.usage, request.metadata = response.Usage, response.ProviderMetadata
	}
	if err != nil {
		request.outcome = "error"
	}
	m.record(ctx, request)
	return response, err
}

func (m observedModel) Stream(ctx context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
	request := requestUsage{started: time.Now(), outcome: "incomplete"}
	request.raw = &providerUsage{sessionID: m.sessionID}
	ctx = context.WithValue(ctx, providerUsageKey{}, request.raw)
	m.logPreparedRequest(ctx, call)
	stream, err := m.LanguageModel.Stream(ctx, call)
	if err != nil {
		request.outcome = "error"
		m.record(ctx, request)
		return nil, err
	}
	return func(yield func(fantasy.StreamPart) bool) {
		defer func() { m.record(ctx, request) }()
		for part := range stream {
			if !usageIsZero(part.Usage) {
				// Usage parts are snapshots within a request, not increments.
				// Preserve counters omitted from a later partial snapshot.
				request.usage.InputTokens = max(request.usage.InputTokens, part.Usage.InputTokens)
				request.usage.OutputTokens = max(request.usage.OutputTokens, part.Usage.OutputTokens)
				request.usage.CacheReadTokens = max(request.usage.CacheReadTokens, part.Usage.CacheReadTokens)
				request.usage.CacheCreationTokens = max(request.usage.CacheCreationTokens, part.Usage.CacheCreationTokens)
				request.usage.ReasoningTokens = max(request.usage.ReasoningTokens, part.Usage.ReasoningTokens)
				request.usage.TotalTokens = max(request.usage.TotalTokens, part.Usage.TotalTokens)
			}
			if part.ProviderMetadata != nil {
				request.metadata = part.ProviderMetadata
			}
			if part.Delta != "" {
				switch part.Type {
				case fantasy.StreamPartTypeTextDelta:
					if request.firstText == 0 {
						request.firstText = time.Since(request.started)
					}
					fallthrough
				case fantasy.StreamPartTypeReasoningDelta, fantasy.StreamPartTypeToolInputDelta:
					if request.firstOutput == 0 {
						request.firstOutput = time.Since(request.started)
					}
				}
			}
			switch part.Type {
			case fantasy.StreamPartTypeFinish:
				request.outcome = "complete"
			case fantasy.StreamPartTypeError:
				request.outcome = "error"
			}
			if !yield(part) {
				return
			}
		}
	}, nil
}

func modelUsageCost(model Model, usage fantasy.Usage, override *float64) (cost, equivalent float64, source string) {
	prices := model.CatwalkCfg
	equivalent = prices.CostPer1MInCached/1e6*float64(usage.CacheCreationTokens) +
		prices.CostPer1MOutCached/1e6*float64(usage.CacheReadTokens) +
		prices.CostPer1MIn/1e6*float64(usage.InputTokens) +
		prices.CostPer1MOut/1e6*float64(usage.OutputTokens)
	cost, source = equivalent, "catalog"
	if prices.CostPer1MIn+prices.CostPer1MOut+prices.CostPer1MInCached+prices.CostPer1MOutCached == 0 {
		source = "unpriced"
	}
	if usage.InputTokens+usage.OutputTokens+usage.CacheCreationTokens+usage.CacheReadTokens == 0 {
		source = "unreported"
	}
	if override != nil {
		cost, source = *override, "provider"
	}
	if model.FlatRate {
		cost, source = 0, "subscription"
	}
	return cost, equivalent, source
}

func (m observedModel) record(ctx context.Context, request requestUsage) {
	if ctx.Err() != nil && request.outcome != "complete" {
		request.outcome = "cancelled"
	}
	request.usage = request.raw.normalize(request.usage)
	cost, equivalent, source, captureComplete := request.raw.billing(m.model, request.usage, m.agent.openrouterCost(request.metadata))
	persisted := true
	if cost != 0 {
		// A cancelled request may still have incurred a provider charge.
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		err := m.agent.sessions.AddCost(cleanupCtx, m.sessionID, cost)
		cancel()
		if err != nil {
			persisted = false
			slog.Error("Failed to persist model request cost", "session_id", m.sessionID,
				"purpose", m.purpose, "cost", cost, "error", err)
		}
	}
	if !usageIsZero(request.usage) {
		m.agent.eventTokensUsed(m.sessionID, m.model, request.usage, cost)
	}
	fields := []any{
		"session_id", m.sessionID, "session_hash", session.HashID(m.sessionID),
		"purpose", m.purpose, "provider", m.model.ModelCfg.Provider,
		"model", m.model.ModelCfg.Model, "outcome", request.outcome,
		"input_tokens", request.usage.InputTokens, "output_tokens", request.usage.OutputTokens,
		"cache_read_tokens", request.usage.CacheReadTokens, "cache_write_tokens", request.usage.CacheCreationTokens,
		"reasoning_tokens", request.usage.ReasoningTokens, "usage_reported", !usageIsZero(request.usage),
		"cost", cost, "api_equivalent_cost", equivalent, "cost_source", source,
		"cost_complete", source == "subscription" || (request.outcome == "complete" && captureComplete && source != "unreported" && source != "unpriced"),
		"cost_persisted", persisted, "duration_ms", time.Since(request.started).Milliseconds(),
	}
	if request.firstOutput > 0 {
		fields = append(fields, "first_output_ms", request.firstOutput.Milliseconds())
	}
	if request.firstText > 0 {
		fields = append(fields, "first_text_ms", request.firstText.Milliseconds())
	}
	slog.Info("Model request usage", fields...)
}

type fingerprintWriter struct {
	hash.Hash
	bytes int64
}

func (w *fingerprintWriter) Write(p []byte) (int, error) {
	n, err := w.Hash.Write(p)
	w.bytes += int64(n)
	return n, err
}

func (m observedModel) logPreparedRequest(ctx context.Context, call fantasy.Call) {
	if !slog.Default().Enabled(ctx, slog.LevelDebug) {
		return
	}
	var system, history, tools, options fingerprintWriter
	for _, writer := range []*fingerprintWriter{&system, &history, &tools, &options} {
		writer.Hash = sha256.New()
	}
	systemEncoder, historyEncoder := json.NewEncoder(&system), json.NewEncoder(&history)
	inHistory := false
	for _, message := range call.Prompt {
		encoder := systemEncoder
		if inHistory || message.Role != fantasy.MessageRoleSystem {
			inHistory, encoder = true, historyEncoder
		}
		if err := encoder.Encode(message); err != nil {
			slog.DebugContext(ctx, "Cannot fingerprint prepared messages", "error_type", fmt.Sprintf("%T", err))
			return
		}
	}
	if err := json.NewEncoder(&tools).Encode(call.Tools); err != nil {
		return
	}
	call.Prompt, call.Tools = nil, nil
	if err := json.NewEncoder(&options).Encode(call); err != nil {
		return
	}
	slog.DebugContext(ctx, "Prepared model request fingerprints",
		"session_hash", session.HashID(m.sessionID), "purpose", m.purpose,
		"provider", m.model.ModelCfg.Provider, "model", m.model.ModelCfg.Model,
		"system_sha256", fmt.Sprintf("%x", system.Sum(nil)), "system_bytes", system.bytes,
		"history_sha256", fmt.Sprintf("%x", history.Sum(nil)), "history_bytes", history.bytes,
		"tools_sha256", fmt.Sprintf("%x", tools.Sum(nil)), "tools_bytes", tools.bytes,
		"options_sha256", fmt.Sprintf("%x", options.Sum(nil)), "options_bytes", options.bytes)
}
