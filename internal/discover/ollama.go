package discover

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	"charm.land/catwalk/pkg/catwalk"
)

func init() {
	RegisterEnricher("ollama", &ollamaEnricher{})
}

// ollamaShowResponse mirrors the response from Ollama's POST /api/show
// endpoint. Only the fields we care about are decoded.
type ollamaShowResponse struct {
	ModelInfo map[string]any `json:"model_info"`
}

// ollamaTagsResponse mirrors the response from Ollama's GET /api/tags
// endpoint, which enumerates every model tag currently pulled onto the
// local server. Unlike /api/show, /api/tags requires no per-model
// request and is the canonical "what's installed" enumeration.
type ollamaTagsResponse struct {
	Models []ollamaTagEntry `json:"models"`
}

// ollamaTagEntry is a single tag returned by /api/tags.
type ollamaTagEntry struct {
	Name       string           `json:"name"`
	ModifiedAt string           `json:"modified_at"`
	Size       int64            `json:"size"`
	Details    ollamaTagDetails `json:"details"`
}

// ollamaTagDetails are the per-tag descriptor fields Ollama returns in
// /api/tags. We read size and family only to surface them in the
// Switch Model menu's display name; no code branches on them.
type ollamaTagDetails struct {
	ParameterSize     string `json:"parameter_size"`
	QuantizationLevel string `json:"quantization_level"`
	Family            string `json:"family"`
}

// ollamaEnricher fetches model metadata from Ollama's /api/show
// endpoint and populates context window on discovered models.
type ollamaEnricher struct{}

func (e *ollamaEnricher) EnrichModels(ctx context.Context, cfg Config, resolver Resolver, models []catwalk.Model) ([]catwalk.Model, error) {
	// Collect indices that need enrichment.
	var needEnrichment []int
	for i := range models {
		if models[i].ContextWindow == 0 {
			needEnrichment = append(needEnrichment, i)
		}
	}
	if len(needEnrichment) == 0 {
		return models, nil
	}

	// Fetch metadata concurrently with bounded parallelism.
	type result struct {
		index         int
		contextLength int64
	}

	results := make([]result, len(needEnrichment))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 5) // Max 5 concurrent requests.

	for ri, idx := range needEnrichment {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()

			resp, err := doRequest(ctx, http.MethodPost, stripV1Suffix(cfg.BaseURL), "/api/show",
				cfg.APIKey, cfg.ExtraHeaders, resolver,
				map[string]string{"model": models[idx].ID})
			if err != nil {
				return
			}
			defer resp.Body.Close()

			var showResp ollamaShowResponse
			if err := json.NewDecoder(resp.Body).Decode(&showResp); err != nil {
				return
			}

			if cl := extractContextLength(showResp.ModelInfo); cl > 0 {
				results[ri] = result{index: idx, contextLength: cl}
			}
		})
	}
	wg.Wait()

	for _, r := range results {
		if r.contextLength > 0 {
			models[r.index].ContextWindow = r.contextLength
		}
	}

	return models, nil
}

// extractContextLength finds the context_length value in Ollama's
// model_info map. The key is architecture-specific (e.g.
// "llama.context_length", "qwen2.context_length"), so we scan for any
// key ending in ".context_length".
func extractContextLength(info map[string]any) int64 {
	for k, v := range info {
		if !strings.HasSuffix(k, ".context_length") {
			continue
		}
		switch n := v.(type) {
		case float64:
			return int64(n)
		case int64:
			return n
		case json.Number:
			i, err := n.Int64()
			if err == nil {
				return i
			}
		}
	}
	return 0
}

// DiscoverOllamaTags hits Ollama's GET /api/tags endpoint and returns
// one catwalk.Model per locally-downloaded model tag. Bails silently on
// any error so a non-running Ollama server during startup degrades
// gracefully to the curated preset only — callers receive nil and fall
// back without surfacing a warning.
// The returned Model names carry a "(local)" suffix so the Switch Model
// menu distinguishes freshly discovered tags from the curated preset.
func DiscoverOllamaTags(ctx context.Context, cfg Config, resolver Resolver) []catwalk.Model {
	resp, err := doRequest(ctx, http.MethodGet, stripV1Suffix(cfg.BaseURL), "/api/tags", cfg.APIKey, cfg.ExtraHeaders, resolver, nil)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}

	var tagsResp ollamaTagsResponse
	if err := json.NewDecoder(resp.Body).Decode(&tagsResp); err != nil {
		return nil
	}

	out := make([]catwalk.Model, 0, len(tagsResp.Models))
	for _, t := range tagsResp.Models {
		if t.Name == "" {
			continue
		}
		// Use conservative defaults until the per-tag /api/show call fills them
		// in; the existing ollamaEnricher already does that when models flow
		// through the user-config path. 128_000/8_192 mirror the OpenAI-
		// responses defaults shipped by oh-my-pi/OMP.
		out = append(out, catwalk.Model{
			ID:               t.Name,
			Name:             t.Name + " (local)",
			ContextWindow:    128_000,
			DefaultMaxTokens: 8_192,
		})
	}
	return out
}
