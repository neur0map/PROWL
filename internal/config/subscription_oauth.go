package config

import (
	"charm.land/catwalk/pkg/catwalk"
	anthropicoauth "github.com/neur0map/prowl/internal/oauth/anthropic"
	openaioauth "github.com/neur0map/prowl/internal/oauth/openai"
)

// setupSubscriptionOAuth selects the native protocol and active catalog for
// subscription credentials, including when default providers are disabled.
func (p *ProviderConfig) setupSubscriptionOAuth() {
	if p.OAuthToken == nil {
		return
	}
	switch p.ID {
	case "openai":
		p.Type = catwalk.TypeOpenAI
		p.BaseURL = openaioauth.CodexBaseURL
		p.Models = p.ChatGPTModels
	case "anthropic":
		p.Type = catwalk.TypeAnthropic
		p.BaseURL = anthropicoauth.BaseURL
	default:
		return
	}
	p.APIKey = ""
	p.APIKeyTemplate = ""
	p.FlatRate = true
}
