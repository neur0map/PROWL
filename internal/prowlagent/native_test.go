package prowlagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNativeRetrievalBudgetsPreciseRecoveryAndStaleEvidence(t *testing.T) {
	root := t.TempDir()
	owner := "package example\n\n// BuildPrompt combines rules and a request.\nfunc BuildPrompt(rules, request string) string {\n\treturn rules + \"\\n\" + request\n}\n\nfunc Unrelated() int { return 7 }\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "owner.go"), []byte(owner), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "noise.go"), []byte("package example\n\nconst Noise = \"BuildPrompt BuildPrompt BuildPrompt\"\n"), 0o644))
	require.NoError(t, EnsureIndex(t.Context(), nil, root))
	stdout, stderr, err := Run(t.Context(), nil, root, "search", "BuildPrompt", "--format", "json", "--budget-bytes", "1600", "--budget-tokens", "400")
	require.NoError(t, err, stderr)
	var packet struct {
		Items []struct {
			ID        string `json:"id"`
			Content   string `json:"content"`
			Citations []struct {
				Path string `json:"path"`
			} `json:"citations"`
		} `json:"items"`
		Budget struct {
			ExactBytes      int `json:"exact_bytes"`
			EstimatedTokens int `json:"estimated_tokens"`
		} `json:"budget"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &packet))
	require.LessOrEqual(t, len(stdout), 1600)
	require.Equal(t, len(stdout), packet.Budget.ExactBytes)
	require.LessOrEqual(t, packet.Budget.EstimatedTokens, 400)
	require.NotEmpty(t, packet.Items)
	require.Equal(t, "owner.go", packet.Items[0].Citations[0].Path)
	id := packet.Items[0].ID
	require.Contains(t, id, "symbol:")

	stdout, stderr, err = Run(t.Context(), nil, root, "context", "get", id, "--mode", "full", "--budget-tokens", "1200", "--format", "json")
	require.NoError(t, err, stderr)
	require.NoError(t, json.Unmarshal([]byte(stdout), &packet))
	require.Len(t, packet.Items, 1)
	require.Contains(t, packet.Items[0].Content, `return rules + "\n" + request`)
	require.NotContains(t, packet.Items[0].Content, "func Unrelated")

	for _, format := range []string{"toon", "human", "markdown"} {
		stdout, stderr, err = Run(t.Context(), nil, root, "search", "BuildPrompt", "--format", format, "--budget-bytes", "1600", "--budget-tokens", "400")
		require.NoError(t, err, stderr)
		require.LessOrEqual(t, len(stdout), 1600, format)
		require.Contains(t, stdout, "owner.go", format)
	}

	stdout, stderr, err = Run(t.Context(), nil, root, "outline", "owner.go", "--format", "json")
	require.NoError(t, err, stderr)
	var outline struct {
		Symbols []struct {
			Name string `json:"name"`
		} `json:"symbols"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &outline))
	var names []string
	for _, symbol := range outline.Symbols {
		names = append(names, symbol.Name)
	}
	require.Contains(t, names, "BuildPrompt")
	require.Contains(t, names, "Unrelated", "the index must not collapse adjacent function definitions")

	require.NoError(t, os.WriteFile(filepath.Join(root, "owner.go"), []byte("package example\n\nfunc BuildPrompt(rules, request string) string { return request + rules }\n"), 0o644))
	stdout, _, err = Run(t.Context(), nil, root, "context", "get", id, "--mode", "full", "--budget-tokens", "1200", "--format", "json")
	if err == nil {
		require.NoError(t, json.Unmarshal([]byte(stdout), &packet))
		require.Empty(t, packet.Items, "an old recovery ID must not silently resolve to changed source")
	} else {
		require.Empty(t, stdout, "a stale lookup may fail explicitly, but must not return unrelated evidence")
	}
}

func TestNativeCancellationStopsBeforeCreatingAnIndex(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	stdout, _, err := Run(ctx, nil, root, "overview")
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, stdout)
	_, err = os.Stat(filepath.Join(root, ".prowl"))
	require.ErrorIs(t, err, os.ErrNotExist)
}
