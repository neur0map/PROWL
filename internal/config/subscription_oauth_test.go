package config

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/neur0map/prowl/internal/csync"
	"github.com/neur0map/prowl/internal/env"
	"github.com/neur0map/prowl/internal/oauth"
	"github.com/stretchr/testify/require"
)

func TestSubscriptionOAuthSurvivesProviderPreparation(t *testing.T) {
	for _, id := range []string{"anthropic", "openai"} {
		t.Run(id, func(t *testing.T) {
			token := &oauth.Token{AccessToken: "subscription-token", RefreshToken: "refresh"}
			cfg := &Config{Providers: csync.NewMapFrom(map[string]ProviderConfig{
				id: {OAuthToken: token, ChatGPTModels: []catwalk.Model{{ID: "subscription-model"}}},
			})}
			cfg.setDefaults(t.TempDir(), "")
			e := env.NewFromMap(map[string]string{})
			known := []catwalk.Provider{{ID: catwalk.InferenceProvider(id), Type: catwalk.Type(id), APIKey: "$MISSING_SUBSCRIPTION_TEST_KEY", APIEndpoint: "https://api.example.com", Models: []catwalk.Model{{ID: "api-model"}}}}
			require.NoError(t, cfg.configureProviders(context.Background(), testStore(cfg), e, NewShellVariableResolver(e), known))
			pc, ok := cfg.Providers.Get(id)
			require.True(t, ok, "OAuth must configure a provider without an API key")
			require.Equal(t, token, pc.OAuthToken)
			require.True(t, pc.FlatRate)
			if id == "openai" {
				require.True(t, cfg.IsModelAvailable(id, "subscription-model"))
				require.False(t, cfg.IsModelAvailable(id, "api-model"), "API-only models must not be offered to a subscription")
			} else {
				require.True(t, cfg.IsModelAvailable(id, "api-model"))
			}
		})
	}
}

func TestSubscriptionOAuthWithDefaultProvidersDisabled(t *testing.T) {
	cfg := &Config{
		Options: &Options{DisableDefaultProviders: true},
		Providers: csync.NewMapFrom(map[string]ProviderConfig{
			"openai": {
				OAuthToken:    &oauth.Token{AccessToken: "subscription-token"},
				ChatGPTModels: []catwalk.Model{{ID: "subscription-model"}},
				Models:        []catwalk.Model{{ID: "api-only-model"}},
			},
		}),
	}
	cfg.setDefaults(t.TempDir(), "")
	e := env.NewFromMap(map[string]string{})
	require.NoError(t, cfg.configureProviders(context.Background(), testStore(cfg), e, NewShellVariableResolver(e), nil))
	require.True(t, cfg.IsModelAvailable("openai", "subscription-model"))
	require.False(t, cfg.IsModelAvailable("openai", "api-only-model"))
}

func TestSubscriptionCredentialSwitchAndRefresh(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"openai", "anthropic"} {
		t.Run(id, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "prowl.json")
			store := &ConfigStore{
				globalDataPath: path,
				config:         &Config{Providers: csync.NewMapFrom(map[string]ProviderConfig{id: {ID: id, APIKey: "old-api-key"}})},
				fetchOpenAIModels: func(context.Context, *oauth.Token) ([]catwalk.Model, error) {
					return []catwalk.Model{{ID: "subscription-model"}}, nil
				},
				exchangeToken: func(_ context.Context, provider, refresh string) (*oauth.Token, error) {
					require.Equal(t, id, provider)
					require.Equal(t, "refresh", refresh)
					return &oauth.Token{AccessToken: "new-access", RefreshToken: "rotated", ExpiresIn: 3600, ExpiresAt: time.Now().Add(time.Hour).Unix()}, nil
				},
			}
			token := &oauth.Token{AccessToken: "access", RefreshToken: "refresh", AccountID: "workspace", IDToken: "identity", ExpiresAt: time.Now().Add(-time.Hour).Unix()}
			require.NoError(t, store.SetProviderAPIKey(ScopeGlobal, id, token))
			require.NoError(t, store.RefreshOAuthToken(context.Background(), ScopeGlobal, id))
			pc, _ := store.Config().Providers.Get(id)
			require.Equal(t, "workspace", pc.OAuthToken.AccountID, "refresh may omit routing identity")
			require.Equal(t, "rotated", pc.OAuthToken.RefreshToken)
			require.Empty(t, pc.APIKey, "OAuth must not become an API-key credential")
			require.NoError(t, store.SetProviderAPIKey(ScopeGlobal, id, "new-api-key"))
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			var saved struct {
				Providers map[string]ProviderConfig `json:"providers"`
			}
			require.NoError(t, json.Unmarshal(data, &saved))
			require.Equal(t, "new-api-key", saved.Providers[id].APIKey)
			require.Nil(t, saved.Providers[id].OAuthToken, "switching to an API key must retire the OAuth grant")
			require.Empty(t, saved.Providers[id].ChatGPTModels)
			pc, _ = store.Config().Providers.Get(id)
			require.Nil(t, pc.OAuthToken)
		})
	}
}

func TestSubscriptionCatalogFailurePreservesExistingCredential(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "prowl.json")
	original := []byte(`{"providers":{"openai":{"api_key":"existing"}}}`)
	require.NoError(t, os.WriteFile(path, original, 0o600))
	store := &ConfigStore{
		globalDataPath: path,
		config:         &Config{Providers: csync.NewMapFrom(map[string]ProviderConfig{"openai": {ID: "openai", APIKey: "existing"}})},
		fetchOpenAIModels: func(context.Context, *oauth.Token) ([]catwalk.Model, error) {
			return nil, errors.New("catalog unavailable")
		},
	}
	require.Error(t, store.SetProviderAPIKey(ScopeGlobal, "openai", &oauth.Token{AccessToken: "new"}))
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, original, after)
	pc, _ := store.Config().Providers.Get("openai")
	require.Equal(t, "existing", pc.APIKey)
}
