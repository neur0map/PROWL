package config

import (
	"slices"
	"sync"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/stretchr/testify/require"
)

// resetProviderCacheForTest clears the sync.Once memoization so a subsequent
// Providers() call re-runs the assembly with the current Config. The real
// Providers() function caches the result globally inside the package; this
// helper exists only for tests that exercise the "default providers on/off"
// branches and would otherwise observe whatever the previous test cached.
func resetProviderCacheForTest(t *testing.T) {
	t.Helper()
	providerOnce = sync.Once{}
	providerList = nil
	providerErr = nil
}

func TestOllamaPreset_SurfacesInProvidersByDefault(t *testing.T) {
	resetProviderCacheForTest(t)
	cfg := &Config{Options: &Options{}}
	providers, err := Providers(cfg)
	require.NoError(t, err)
	require.NotEmpty(t, providers)

	idx := slices.IndexFunc(providers, func(p catwalk.Provider) bool { return string(p.ID) == "ollama" })
	require.GreaterOrEqual(t, idx, 0, "Ollama preset must be present even without explicit setup")

	p := providers[idx]
	require.Equal(t, "Ollama", p.Name)
	require.Equal(t, "http://localhost:11434/v1", p.APIEndpoint)
	require.Equal(t, "llama3.3:70b-instruct-q4_K_M", p.DefaultLargeModelID)
	require.Equal(t, "llama3.2:3b-instruct-q5_K_M", p.DefaultSmallModelID)
	require.GreaterOrEqual(t, len(p.Models), 3, "preset should ship at least three curated model entries")
}

func TestOllamaPreset_RespectsDisableDefaultProviders(t *testing.T) {
	resetProviderCacheForTest(t)
	cfg := &Config{Options: &Options{DisableDefaultProviders: true}}
	providers, err := Providers(cfg)
	require.NoError(t, err)

	for _, p := range providers {
		require.NotEqual(t, "ollama", string(p.ID),
			"DisableDefaultProviders must suppress Ollama's preset the same way it suppresses Catwalk defaults")
	}
}
