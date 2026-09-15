package hyper

import (
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/stretchr/testify/require"
)

// TestEmbeddedKeepsForkDisplayName pins the literal display name users see
// for the Hyper provider. provider.json is regenerated from the upstream
// endpoint by //go:generate (and by the "update generated files" workflow),
// and that payload carries Charm's own name — a regeneration must not
// silently rename the provider.
func TestEmbeddedKeepsForkDisplayName(t *testing.T) {
	t.Parallel()

	require.Equal(t, "Ryoku Hyper", DisplayName)
	require.Equal(t, "Ryoku Hyper", Embedded().Name)
}

// TestEmbeddedIsUsable guards against a regeneration landing a payload that
// parses but carries no models, which would leave the provider unselectable.
func TestEmbeddedIsUsable(t *testing.T) {
	t.Parallel()

	p := Embedded()
	require.Equal(t, catwalk.InferenceProvider(Name), p.ID)
	require.NotEmpty(t, p.Models)
	require.NotEmpty(t, p.DefaultLargeModelID)
	require.NotEmpty(t, p.DefaultSmallModelID)
}

// TestRebrandPreservesPayload checks that rebranding only renames: model
// data fetched from the live API must survive untouched.
func TestRebrandPreservesPayload(t *testing.T) {
	t.Parallel()

	got := Rebrand(catwalk.Provider{
		Name:   "Charm Hyper",
		ID:     "hyper",
		Models: []catwalk.Model{{ID: "model-1", Name: "Model 1"}},
	})

	require.Equal(t, DisplayName, got.Name)
	require.Equal(t, catwalk.InferenceProvider("hyper"), got.ID)
	require.Equal(t, "model-1", got.Models[0].ID)
}
