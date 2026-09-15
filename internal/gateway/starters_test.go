package gateway

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/neur0map/prowl/internal/gateway/provider"
)

// TestFreeStarterPlatformsAreActuallyUsable stops the first-run advice from
// drifting away from what the product can do. The previous wording named
// Cerebras, which has a wire adapter but no models in the shipped catalogue:
// a new user who followed it added a key the router could never route to.
func TestFreeStarterPlatformsAreActuallyUsable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	eng, err := OpenEngine(ctx, t.TempDir(), EngineOptions{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = eng.Close() })

	registry := provider.NewRegistry()
	for _, platform := range FreeStarterPlatforms {
		_, ok := registry.Resolve(platform, "")
		require.True(t, ok, "%s is recommended but has no wire adapter", platform)

		var models int
		require.NoError(t, eng.DB().QueryRow(
			"SELECT COUNT(*) FROM models WHERE platform = ? AND enabled = 1",
			platform).Scan(&models))
		require.Positive(t, models,
			"%s is recommended but the shipped catalogue has no models for it, "+
				"so a key added for it cannot be routed", platform)

		require.Contains(t, FreeStarterHint, starterLabel(platform),
			"the hint text must name every platform it promises")
	}
}

// starterLabel is the name the hint uses for a platform.
func starterLabel(platform string) string {
	switch platform {
	case "google":
		return "Google"
	case "groq":
		return "Groq"
	case "openrouter":
		return "OpenRouter"
	}
	return platform
}
