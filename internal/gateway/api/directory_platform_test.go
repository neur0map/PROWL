package api

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestADirectoryIDOnlyClaimsAnAdapterAtAWordBoundary is about attribution: a
// provider that merely starts with an adapter's letters is a different
// company. "hyperbolic" resolving to the "hyper" adapter made the directory
// show Hyperbolic with Charm Hyper's 34 models, its latency and its
// subscription access.
func TestADirectoryIDOnlyClaimsAnAdapterAtAWordBoundary(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		id, platform string
		want         bool
		why          string
	}{
		{"google-gemini", "google", true, "a qualifier after a dash is the normal shape"},
		{"ovhcloud_ai", "ovhcloud", true, "underscores separate too"},
		{"openrouter/free", "openrouter", true, "so does a slash"},
		{"hyperbolic", "hyper", false, "a different company, not a qualifier"},
		{"openrouterish", "openrouter", false, "letters continuing a word are not a boundary"},
		{"hyper", "hyper", false, "an exact match is handled before this rule"},
		{"gpt-4", "gpt", false, "too short to be an adapter name"},
	} {
		require.Equal(t, tc.want, separatorBoundary(tc.id, tc.platform),
			"%s vs %s: %s", tc.id, tc.platform, tc.why)
	}
}
