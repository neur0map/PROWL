package service

import (
	"context"
	"fmt"
	"strings"

	"charm.land/catwalk/pkg/catwalk"

	"github.com/neur0map/prowl/internal/config"
	"github.com/neur0map/prowl/internal/gateway"
)

// The pool only knew about keys pasted into the dashboard, ignoring the
// subscription capacity a signed-in user already has. Credentials resolve at
// dispatch so a refreshed token is picked up and a revoked login stops
// working immediately.

// ConfigLogins resolves pool credentials and models from Prowl's provider
// configuration and the provider catalogue it already maintains.
type ConfigLogins struct {
	store *config.ConfigStore
}

// NewConfigLogins builds a credential source over the loaded configuration.
func NewConfigLogins(store *config.ConfigStore) *ConfigLogins {
	return &ConfigLogins{store: store}
}

// freshToken re-reads the provider's token after a refresh, since the store
// updates its own copy rather than the one this loop is iterating.
func (c *ConfigLogins) freshToken(providerID string) (string, bool, error) {
	cfg := c.config()
	if cfg == nil {
		return "", false, nil
	}
	for pid, p := range cfg.Providers.Seq2() {
		if pid != providerID || p.OAuthToken == nil {
			continue
		}
		if p.OAuthToken.AccessToken == "" {
			return "", true, fmt.Errorf("the %s login has no credential after refresh", providerID)
		}
		return p.OAuthToken.AccessToken, true, nil
	}
	return "", false, nil
}

func (c *ConfigLogins) config() *config.Config {
	if c == nil || c.store == nil {
		return nil
	}
	return c.store.Config()
}

// Credential returns the live bearer credential for a configured provider.
func (c *ConfigLogins) Credential(ctx context.Context, provider string) (string, bool, error) {
	cfg := c.config()
	if cfg == nil {
		return "", false, nil
	}
	id := strings.ToLower(strings.TrimSpace(provider))
	for pid, p := range cfg.Providers.Seq2() {
		if strings.ToLower(pid) != id || p.Disable {
			continue
		}
		if p.OAuthToken != nil && p.OAuthToken.AccessToken != "" {
			// A subscription access token lives about an hour. Reading the
			// stored one is not enough: it is routinely expired by the time
			// the gateway needs it, and handing that to a provider earns a
			// 401 that looks like a broken enrolment. Refresh through the
			// store, which holds the cross-process lock and writes the
			// rotated token back for everyone.
			if p.OAuthToken.IsExpired() {
				if err := c.store.RefreshOAuthToken(ctx, config.ScopeGlobal, pid); err != nil {
					return "", true, fmt.Errorf("refreshing the %s login: %w", pid, err)
				}
				return c.freshToken(pid)
			}
			return p.OAuthToken.AccessToken, true, nil
		}
		if p.APIKey == "" {
			return "", true, nil
		}
		resolver := c.store.Resolver()
		if resolver == nil {
			return p.APIKey, true, nil
		}
		resolved, err := resolver.ResolveValue(p.APIKey)
		if err != nil {
			return "", true, err
		}
		return resolved, true, nil
	}
	return "", false, nil
}

// Linkable lists the provider logins the pool can borrow. Gateway-backed
// entries are excluded: enrolling Prowl's own gateway into its own pool would
// route requests back into the router.
func (c *ConfigLogins) Linkable(ctx context.Context) []gateway.LinkableProvider {
	cfg := c.config()
	if cfg == nil {
		return nil
	}
	var out []gateway.LinkableProvider
	for pid, p := range cfg.Providers.Seq2() {
		if p.Disable || strings.Contains(strings.ToLower(pid), "prowl-gateway") {
			continue
		}
		kind, detail := "api_key", ""
		switch {
		case p.OAuthToken != nil && p.OAuthToken.AccessToken != "":
			kind = "oauth"
			detail = p.OAuthToken.AccountID
		case p.APIKey != "":
		default:
			// Nothing to borrow: no key and no login.
			continue
		}
		name := p.Name
		if name == "" {
			name = pid
		}
		out = append(out, gateway.LinkableProvider{
			ID: pid, Name: name, Kind: kind, Detail: detail,
		})
	}
	return out
}

// Models lists what an enrolled login can serve, from the provider catalogue
// the harness already keeps current.
//
// This is what makes enrolling a login do something: without it the pool gets
// a credential and no models, so the router has nothing to send anywhere and
// the Models page shows no change. Configured models win over the catalogue's,
// since an operator who pinned a list meant it.
func (c *ConfigLogins) Models(ctx context.Context, provider string) []gateway.LinkedModel {
	cfg := c.config()
	if cfg == nil {
		return nil
	}
	id := strings.ToLower(strings.TrimSpace(provider))

	var (
		models  []catwalk.Model
		large   string
		small   string
		matched bool
	)
	for pid, p := range cfg.Providers.Seq2() {
		if strings.ToLower(pid) != id {
			continue
		}
		matched = true
		// A subscription serves the provider's own catalogue; an OpenAI
		// subscription is the exception, whose ChatGPT models are a separate
		// list from its API models.
		if p.OAuthToken != nil && len(p.ChatGPTModels) > 0 {
			models = p.ChatGPTModels
		} else if len(p.Models) > 0 {
			models = p.Models
		}
		break
	}
	if !matched {
		return nil
	}

	// The catalogue is the source of the vendor's own large/small defaults,
	// and of the model list when config pins none.
	for _, known := range c.store.KnownProviders() {
		if strings.ToLower(string(known.ID)) != id {
			continue
		}
		if len(models) == 0 {
			models = known.Models
		}
		large, small = known.DefaultLargeModelID, known.DefaultSmallModelID
		break
	}

	out := make([]gateway.LinkedModel, 0, len(models))
	for _, m := range models {
		out = append(out, gateway.LinkedModel{
			ID:            m.ID,
			Name:          m.Name,
			ContextWindow: m.ContextWindow,
			MaxTokens:     m.DefaultMaxTokens,
			InputPerM:     m.CostPer1MIn,
			OutputPerM:    m.CostPer1MOut,
			CanReason:     m.CanReason,
			Attachments:   m.SupportsImages,
			// Every provider Prowl drives through this path speaks tool
			// calling; the catalogue does not carry a per-model flag for it.
			Tools:    true,
			Flagship: m.ID == large,
			Small:    m.ID == small,
		})
	}
	return out
}
