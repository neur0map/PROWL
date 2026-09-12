package prowlagent

import (
	"testing"

	"github.com/neur0map/prowl/internal/config"
	"github.com/stretchr/testify/require"
)

func TestStatusIndexed(t *testing.T) {
	t.Parallel()
	var s Status
	require.False(t, s.Indexed(), "zero status is not indexed")
	s.Counts.Files = 10
	require.False(t, s.Indexed(), "files without a last_index is not indexed")
	s.LastIndex = "0"
	require.False(t, s.Indexed(), "last_index of 0 is not indexed")
	s.LastIndex = "1789148556"
	require.True(t, s.Indexed())
}

func TestSlug(t *testing.T) {
	t.Parallel()
	require.Equal(t, "run-gofmt-before-commit", slug("Run gofmt before commit"))
	require.Equal(t, "a-b", slug("  a   b  "))
	require.Equal(t, "lesson", slug("!!!"))
	require.LessOrEqual(t, len(slug("this is a very long title that keeps going well beyond the length cap we set")), 48)
}

func TestBinaryDefaultAndOverride(t *testing.T) {
	t.Parallel()
	require.Equal(t, config.DefaultProwlAgentBinary, (*config.ProwlAgentOptions)(nil).Binary())
	require.Equal(t, "/custom/pa", (&config.ProwlAgentOptions{Path: "/custom/pa"}).Binary())
}

func TestResolveAndAvailable(t *testing.T) {
	t.Parallel()
	// The engine is linked in-process, so it is always resolvable and
	// available regardless of any configured binary path.
	_, ok := Resolve(&config.ProwlAgentOptions{Path: "/nonexistent/prowl-agent-xyzzy"})
	require.True(t, ok)
	require.True(t, Available(&config.ProwlAgentOptions{}))

	// The master enable gate still disables the integration.
	disabled := false
	require.False(t, Available(&config.ProwlAgentOptions{Enabled: &disabled}))
}
