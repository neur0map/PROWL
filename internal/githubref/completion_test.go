package githubref

import (
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReferenceCompletionBoundaries(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		input, prefix string
		uris          []string
	}{
		{"Review #42", "Review ", []string{"pr://42", "issue://42"}},
		{"修正 issue #123", "修正 ", []string{"issue://123"}},
		{"(pull #9", "(", []string{"pr://9"}},
		{"#0", "", nil},
		{"C#12", "", nil},
		{"owner/repo#12", "", nil},
		{"https://example.com/#12", "", nil},
		{"#9223372036854775808", "", nil},
	} {
		t.Run(tt.input, func(t *testing.T) {
			start, candidates := Complete(tt.input)
			var uris []string
			for _, candidate := range candidates {
				uris = append(uris, candidate.URI)
			}
			require.Equal(t, tt.uris, uris)
			if len(uris) > 0 {
				require.Equal(t, tt.prefix, tt.input[:start])
			}
		})
	}
}

func TestReferenceParsingRejectsArgumentsAndTraversal(t *testing.T) {
	t.Parallel()
	r, err := Parse("pr://github.example/owner/repo/123/diff")
	require.NoError(t, err)
	require.Equal(t, Reference{Kind: "pr", Number: 123, Repository: "github.example/owner/repo", Diff: true}, r)
	for _, uri := range []string{"pr://--help", "issue://owner/../12", "pr://owner/repo/12;touch", "issue://12/diff", "pr://0"} {
		_, err := Parse(uri)
		require.Error(t, err, uri)
	}
}

func TestOutputLimitCannotBeBypassedByCopy(t *testing.T) {
	t.Parallel()
	b := &boundedOutput{limit: 8}
	// Hide WriterTo to exercise io.Copy's ReaderFrom optimization too.
	_, err := io.Copy(b, struct{ io.Reader }{strings.NewReader(strings.Repeat("x", 10000))})
	require.NoError(t, err)
	require.Equal(t, "xxxxxxxx", b.data.String())
	require.True(t, b.overflow)
}
