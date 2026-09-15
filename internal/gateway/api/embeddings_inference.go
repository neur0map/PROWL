package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/neur0map/prowl/internal/gateway"
	"github.com/neur0map/prowl/internal/gateway/provider"
)

func (s *Server) registerEmbeddingsInferenceRoutes() {
	s.mux.HandleFunc("POST /v1/embeddings", s.RequireMachineKey(s.handleEmbeddings))
}

type embeddingsRequest struct {
	Model          string          `json:"model"`
	Input          json.RawMessage `json:"input"`
	Dimensions     *int            `json:"dimensions"`
	EncodingFormat string          `json:"encoding_format"`
	User           string          `json:"user"`
}

func (s *Server) handleEmbeddings(w http.ResponseWriter, r *http.Request) {
	requestID := ensureRequestID(w, r)

	body, err := readInferenceBody(w, r)
	if err != nil {
		return
	}
	var req embeddingsRequest
	if err := json.Unmarshal(body, &req); err != nil {
		WriteError(w, http.StatusBadRequest, TypeInvalidRequest, "invalid JSON body")
		return
	}

	inputs, err := embeddingInputs(req.Input)
	if err != nil {
		WriteError(w, http.StatusBadRequest, TypeInvalidRequest, err.Error())
		return
	}
	if req.Model == "" {
		WriteError(w, http.StatusBadRequest, TypeInvalidRequest, "model is required")
		return
	}
	// base64 is in the OpenAI schema but no adapter here encodes it, and
	// returning float vectors to a caller that asked for base64 would be a
	// silently wrong answer rather than a refusal.
	if req.EncodingFormat != "" && req.EncodingFormat != "float" {
		WriteErrorCode(w, http.StatusBadRequest, TypeInvalidRequest, "unsupported_encoding_format",
			"encoding_format must be \"float\": this gateway does not re-encode provider vectors")
		return
	}

	candidates, err := embeddingCandidates(r.Context(), s.engine.DB(), req.Model)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not resolve embedding models")
		return
	}
	if len(candidates) == 0 {
		modalityUnavailable(w, "embedding", req.Model,
			hasEnabledEmbeddingModels(r.Context(), s.engine.DB()),
			embeddingModelKnown(r.Context(), s.engine.DB(), req.Model))
		return
	}

	// The cap is enforced before dispatch so an over-long input fails as our
	// clear verdict rather than an opaque upstream rejection that also spends
	// quota and benches a healthy provider.
	if limit := candidates[0].MaxInputTokens; limit > 0 {
		if estimate := embeddingTokenEstimate(inputs); estimate > limit {
			WriteErrorCode(w, http.StatusBadRequest, TypeInvalidRequest, "input_too_long",
				"input is about "+strconv.FormatInt(estimate, 10)+" tokens but this model accepts "+strconv.FormatInt(limit, 10))
			return
		}
	}

	chain := &modalityChain{candidates: candidates}
	relay := &embeddingRelay{
		server:    s,
		chain:     chain,
		inputs:    inputs,
		dims:      req.Dimensions,
		requestID: requestID,
	}

	result := s.engine.Failover().Run(r.Context(), gateway.DispatchRequest{
		Chain:      chain,
		Dispatcher: relay,
		ClientGone: func() bool { return r.Context().Err() != nil },
	})

	if result != nil && result.Status == gateway.StatusSucceeded && relay.response != nil {
		WriteJSON(w, http.StatusOK, embeddingsPayload(relay, req.Model))
		return
	}
	s.logServerEvent(r.Context(), serverLogRecord{
		Level: "error", Source: "embeddings", Event: "exhausted", RequestID: requestID,
		Message: modalityFailureMessage(result, relay.secrets),
	})
	writeModalityFailure(w, result, relay.secrets)
}

// embeddingInputs accepts the shapes OpenAI allows for input: one string, or an
// array of strings. A token-array input is refused rather than guessed at.
func embeddingInputs(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 {
		return nil, errors.New("input is required")
	}
	var one string
	if json.Unmarshal(raw, &one) == nil {
		if one == "" {
			return nil, errors.New("input must not be empty")
		}
		return []string{one}, nil
	}
	var many []string
	if json.Unmarshal(raw, &many) == nil {
		if len(many) == 0 {
			return nil, errors.New("input must not be empty")
		}
		return many, nil
	}
	return nil, errors.New("input must be a string or an array of strings")
}

func embeddingTokenEstimate(inputs []string) int64 {
	chars := 0
	for _, input := range inputs {
		chars += len(input)
	}
	return int64(chars / 4)
}

// embeddingRelay dispatches one embedding attempt per candidate, which is what
// lets the shared failover loop drive this modality.
type embeddingRelay struct {
	server *Server
	chain  *modalityChain
	inputs []string
	dims   *int

	response  *provider.EmbeddingResponse
	route     gateway.Route
	secrets   []string
	requestID string
}

func (e *embeddingRelay) Dispatch(ctx context.Context, route gateway.Route, _ int) gateway.DispatchResult {
	prov, ok := e.server.engine.Registry().Resolve(route.Platform, route.BaseURL)
	if !ok {
		e.server.logServerEvent(ctx, serverLogRecord{
			Level: "warn", Source: "embeddings", Provider: route.Platform, Model: route.ModelID,
			Event: "no_provider_adapter", RequestID: e.requestID,
			Message: "no wire adapter is registered for " + route.Platform,
		})
		return gateway.DispatchResult{Err: gateway.UndispatchableError(route.Platform)}
	}
	embedder, ok := prov.(provider.Embedder)
	if !ok {
		e.server.logServerEvent(ctx, serverLogRecord{
			Level: "warn", Source: "embeddings", Provider: route.Platform, Model: route.ModelID,
			Event: "no_provider_adapter", RequestID: e.requestID,
			Message: "provider " + route.Platform + " does not serve embeddings",
		})
		// A catalogue row for a platform whose adapter cannot embed is an
		// inconsistency worth reporting rather than hiding behind a skip list.
		return gateway.DispatchResult{
			Err: errors.New("provider " + route.Platform + " does not serve embeddings"),
		}
	}

	apiKey := ""
	if !prov.Keyless() {
		revealed, err := e.server.engine.Vault().Reveal(ctx, route.KeyID)
		if err != nil {
			return gateway.DispatchResult{Err: err}
		}
		apiKey = revealed
		e.secrets = append(e.secrets, revealed)
	}

	lease, admitted := e.server.engine.Ledger().Acquire(gateway.Admission{
		Platform:        route.Platform,
		ModelID:         route.ModelID,
		KeyID:           route.KeyID,
		EstimatedTokens: embeddingTokenEstimate(e.inputs),
		Limits: gateway.WindowLimits{
			RPD: derefLimit(route.RPDLimit), TPD: derefLimit(route.TPDLimit),
		},
	})
	if !admitted {
		return gateway.DispatchResult{
			Status: http.StatusTooManyRequests,
			Err:    errors.New("rate limit reached for " + route.Platform),
		}
	}
	settled := false
	defer func() {
		if !settled {
			lease.Release()
		}
	}()

	start := time.Now()
	resp, err := embedder.Embeddings(ctx, apiKey, &provider.EmbeddingRequest{
		Model:      route.ModelID,
		Input:      e.inputs,
		Dimensions: e.dims,
	})
	if err != nil {
		e.server.logServerEvent(ctx, serverLogRecord{
			Level: "warn", Source: "embeddings", Provider: route.Platform, Model: route.ModelID,
			Event: string(gateway.ClassifyAttempt(err)), RequestID: e.requestID,
			Message: redactedProviderMessage(err, e.secrets),
		})
		return dispatchFromError(err)
	}
	if len(resp.Vectors) != len(e.inputs) {
		// A provider that returns a different number of vectors than inputs
		// has answered a different question; failing over is safer than
		// handing the caller misaligned vectors.
		return gateway.DispatchResult{Err: errors.New("provider returned " +
			strconv.Itoa(len(resp.Vectors)) + " vectors for " + strconv.Itoa(len(e.inputs)) + " inputs")}
	}

	e.response = resp
	e.route = route

	tokens := int(embeddingTokenEstimate(e.inputs))
	if resp.InputTokens != nil {
		tokens = *resp.InputTokens
	}
	settled = true
	lease.Settle(int64(tokens))
	e.server.recordModalityRequest(ctx, route, tokens, 0, time.Since(start))

	return gateway.DispatchResult{Outcome: gateway.OutcomeDone}
}

func embeddingsPayload(relay *embeddingRelay, requested string) map[string]any {
	data := make([]map[string]any, 0, len(relay.response.Vectors))
	for index, vector := range relay.response.Vectors {
		data = append(data, map[string]any{
			"object": "embedding", "index": index, "embedding": vector,
		})
	}
	tokens := int(embeddingTokenEstimate(relay.inputs))
	if relay.response.InputTokens != nil {
		tokens = *relay.response.InputTokens
	}
	return map[string]any{
		"object": "list",
		"data":   data,
		"model":  requested,
		"usage":  map[string]int{"prompt_tokens": tokens, "total_tokens": tokens},
	}
}
