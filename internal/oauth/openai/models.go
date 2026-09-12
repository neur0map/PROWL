package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/neur0map/prowl/internal/oauth"
)

// This pin matches OMP's Codex feature-version negotiation, not Prowl's version.
const clientVersion = "0.153.0"

var modelsEndpoint = CodexBaseURL + "/models"

// FetchModels returns only the models offered to the signed-in subscription.
func FetchModels(ctx context.Context, token *oauth.Token) ([]catwalk.Model, error) {
	if token == nil || token.AccessToken == "" {
		return nil, fmt.Errorf("ChatGPT model discovery requires OAuth")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsEndpoint+"?client_version="+clientVersion, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("originator", "prowl")
	req.Header.Set("version", clientVersion)
	if token.AccountID != "" {
		req.Header.Set("chatgpt-account-id", token.AccountID)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch ChatGPT catalog: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ChatGPT model catalog returned HTTP %d", resp.StatusCode)
	}
	var catalog struct {
		Models []struct {
			Slug             string `json:"slug"`
			Name             string `json:"display_name"`
			Visibility       string `json:"visibility"`
			ContextWindow    int64  `json:"context_window"`
			MaxOutputTokens  int64  `json:"max_output_tokens"`
			DefaultReasoning string `json:"default_reasoning_level"`
			Levels           []struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
		} `json:"models"`
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(body) > 4<<20 || json.Unmarshal(body, &catalog) != nil {
		return nil, fmt.Errorf("invalid ChatGPT model catalog")
	}
	models := make([]catwalk.Model, 0, len(catalog.Models))
	for _, model := range catalog.Models {
		if model.Slug == "" || model.Visibility != "list" {
			continue
		}
		levels := make([]string, 0, len(model.Levels))
		for _, level := range model.Levels {
			if level.Effort != "" {
				levels = append(levels, level.Effort)
			}
		}
		name := model.Name
		if name == "" {
			name = model.Slug
		}
		models = append(models, catwalk.Model{ID: model.Slug, Name: name, ContextWindow: model.ContextWindow, DefaultMaxTokens: model.MaxOutputTokens, CanReason: len(levels) > 0, ReasoningLevels: levels, DefaultReasoningEffort: model.DefaultReasoning, SupportsImages: true})
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("ChatGPT subscription has no available models")
	}
	return models, nil
}
