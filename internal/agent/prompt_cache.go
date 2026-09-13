package agent

import (
	"fmt"
	"math"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy/providers/anthropic"
	"charm.land/fantasy/providers/bedrock"
	"charm.land/fantasy/providers/google"
	"charm.land/fantasy/providers/openai"
	"charm.land/fantasy/providers/openrouter"
	"charm.land/fantasy/providers/vercel"

	"github.com/neur0map/prowl/internal/config"
)

// promptCachePolicy contains only resolved capabilities, never credentials.
// Keep compatibility decisions here rather than sending speculative API fields.
type promptCachePolicy struct {
	providerID       string
	modelID          string
	protocol         catwalk.Type
	mode             string
	ttl              string
	anthropicMarkers bool
	openAIWrites     bool
	reasoning        bool
	configuration    bool
	storageRate      float64
	creationRate     float64
	flatRate         bool
}

func resolvePromptCache(provider config.ProviderConfig, model config.SelectedModel) (promptCachePolicy, error) {
	settings := config.PromptCacheConfig{Mode: "auto"}
	for _, override := range []*config.PromptCacheConfig{provider.PromptCache, model.PromptCache} {
		if override == nil {
			continue
		}
		if override.Mode != "" {
			settings.Mode = override.Mode
		}
		if override.TTL != "" {
			settings.TTL = override.TTL
		}
		if override.StorageCostPer1MTokenHour != nil {
			settings.StorageCostPer1MTokenHour = override.StorageCostPer1MTokenHour
		}
	}
	if settings.TTL == "auto" {
		settings.TTL = ""
	}
	p := promptCachePolicy{
		providerID: provider.ID,
		modelID:    model.Model,
		protocol:   provider.Type,
		mode:       settings.Mode,
		ttl:        settings.TTL,
		flatRate:   provider.FlatRate,
	}
	if p.providerID == "" {
		p.providerID = model.Provider
	}
	if isOpenCodeMessagesModel(provider.ID, model.Model) {
		p.protocol = anthropic.Name
	}
	invalid := func(reason string) (promptCachePolicy, error) {
		return p, fmt.Errorf("prompt cache for %s/%s: %s", p.providerID, model.Model, reason)
	}
	switch p.mode {
	case "auto", "off", "explicit":
	default:
		return invalid("mode must be auto, off, or explicit")
	}
	if rate := settings.StorageCostPer1MTokenHour; rate != nil {
		if *rate < 0 || math.IsNaN(*rate) || math.IsInf(*rate, 0) {
			return invalid("storage_cost_per_1m_token_hour must be finite and nonnegative")
		}
		p.storageRate = *rate
	}

	name := strings.ToLower(path.Base(model.Model))
	claude := strings.Contains(name, "claude")
	switch p.protocol {
	case anthropic.Name:
		p.anthropicMarkers = claude || provider.ID == anthropic.Name
	case bedrock.Name:
		p.anthropicMarkers = claude || strings.Contains(name, "anthropic.fable-") || strings.Contains(name, "anthropic.mythos-")
	case openrouter.Name, vercel.Name:
		p.anthropicMarkers = claude
		p.openAIWrites = openAIModelAtLeast(name, 5, 6)
	case openai.Name:
		p.openAIWrites = openAIModelAtLeast(name, 5, 6)
		// Subscription endpoints are not the documented public Responses API.
		// Keep their supported request-level effort semantics instead.
		p.reasoning = name == "gpt-6-astra" || strings.HasPrefix(name, "gpt-6-astra-")
		p.configuration = p.reasoning && provider.OAuthToken == nil
	}
	if provider.OAuthToken != nil && p.protocol == openai.Name {
		p.openAIWrites = false
		if p.mode != "off" && (p.mode == "explicit" || p.ttl != "") {
			return invalid("explicit cache controls and retention are not supported by the subscription endpoint")
		}
	}
	if p.anthropicMarkers {
		if disabled, _ := strconv.ParseBool(os.Getenv("PROWL_DISABLE_ANTHROPIC_CACHE")); disabled {
			p.mode = "off"
		}
	}
	if p.mode == "off" {
		return p, nil
	}

	switch {
	case p.anthropicMarkers:
		if p.ttl == "" {
			p.ttl = "5m"
		}
		duration, err := time.ParseDuration(p.ttl)
		if err != nil || (duration != 5*time.Minute && duration != time.Hour) {
			return invalid("Anthropic cache ttl must be 5m or 1h")
		}
		if duration == time.Hour {
			if p.protocol == bedrock.Name && !bedrockHourCache(name) {
				return invalid("1h caching is not documented for this Bedrock model")
			}
			p.ttl = "1h"
		} else {
			p.ttl = "5m"
		}
	case p.openAIWrites:
		if p.ttl != "" && p.ttl != "30m" {
			return invalid("GPT-5.6 and later support only a 30m minimum cache ttl")
		}
	case p.protocol == openai.Name:
		if p.mode == "explicit" {
			return invalid("this model supports only implicit caching")
		}
		if p.ttl == "" {
			break
		}
		if p.ttl != "in_memory" && p.ttl != "24h" {
			return invalid("this model's cache retention must be in_memory or 24h")
		}
		if (openAIModelFamily(name, "gpt-5.5") || openAIModelFamily(name, "gpt-5.5-pro")) && p.ttl != "24h" {
			return invalid("GPT-5.5 supports only 24h cache retention")
		}
		if p.ttl == "24h" && !openAIExtendedRetention(name) {
			return invalid("24h retention is not documented for this model")
		}
	case p.protocol == google.Name && p.mode == "explicit":
		if settings.StorageCostPer1MTokenHour == nil {
			return invalid("explicit Gemini caching requires storage_cost_per_1m_token_hour from your model's billing plan")
		}
		if !strings.HasPrefix(name, "gemini-") {
			return invalid("explicit cachedContents requires a Gemini model")
		}
		if p.ttl == "" {
			p.ttl = "5m"
		}
		duration, err := time.ParseDuration(p.ttl)
		if err != nil || duration < time.Minute || duration > 24*time.Hour {
			return invalid("explicit Gemini cache ttl must be between 1m and 24h")
		}
		for _, candidate := range provider.Models {
			if candidate.ID == model.Model {
				p.creationRate = candidate.CostPer1MInCached
				break
			}
		}
	default:
		if p.mode == "explicit" {
			return invalid("explicit caching is not supported by this provider/model combination")
		}
		if p.ttl != "" {
			return invalid("cache lifetime cannot be selected for this provider/model combination")
		}
	}
	return p, nil
}

func openAIModelAtLeast(model string, major, minor int) bool {
	model = strings.ToLower(path.Base(model))
	version, ok := strings.CutPrefix(model, "gpt-")
	if !ok {
		return false
	}
	version, _, _ = strings.Cut(version, "-")
	majorText, minorText, _ := strings.Cut(version, ".")
	actualMajor, err := strconv.Atoi(majorText)
	if err != nil {
		return false
	}
	actualMinor := 0
	if minorText != "" {
		actualMinor, err = strconv.Atoi(minorText)
		if err != nil {
			return false
		}
	}
	return actualMajor > major || actualMajor == major && actualMinor >= minor
}

// openAIModelFamily accepts an alias or its dated snapshot, not unrelated
// variants with different retention support (for example, a mini model).
func openAIModelFamily(model, family string) bool {
	if model == family {
		return true
	}
	suffix, ok := strings.CutPrefix(model, family+"-")
	if !ok {
		return false
	}
	_, err := time.Parse("2006-01-02", suffix)
	return err == nil
}

func openAIExtendedRetention(model string) bool {
	for _, family := range []string{
		"gpt-5.5", "gpt-5.5-pro", "gpt-5.4", "gpt-5.2", "gpt-5.1-codex-max",
		"gpt-5.1", "gpt-5.1-codex", "gpt-5.1-codex-mini", "gpt-5.1-chat-latest",
		"gpt-5", "gpt-5-codex", "gpt-4.1",
	} {
		if openAIModelFamily(model, family) {
			return true
		}
	}
	return false
}

func usesOpenAIResponses(model string) bool {
	return openai.IsResponsesModel(model) || openAIModelAtLeast(model, 6, 0)
}

func bedrockHourCache(model string) bool {
	for _, family := range []string{
		"claude-opus-4-5", "claude-opus-4-6", "claude-opus-4-7",
		"claude-sonnet-4-5", "claude-sonnet-4-6", "claude-haiku-4-5",
		"mythos-preview", "mythos-5", "fable-5",
	} {
		if strings.Contains(model, family) {
			return true
		}
	}
	return false
}
