package prowlagent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func TestNativeReviewKeepsSymbolContextWithinBudget(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", append([]string{
			"-c", "user.name=Review fixture", "-c", "user.email=review@example.invalid",
			"-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null",
		}, args...)...)
		cmd.Dir = root
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, string(output))
	}
	git("init", "-q")
	sourcePath := filepath.Join(root, "contract.go")
	require.NoError(t, os.WriteFile(sourcePath, []byte("package fixture\n"), 0o644))
	require.NoError(t, EnsureIndex(t.Context(), nil, root))
	git("add", ".")
	git("commit", "-qm", "Initial fixture")
	paddingLine := "// " + strings.Repeat("padding ", 64)
	padding := strings.Repeat(paddingLine+"\n", 180)
	addition := padding + "func VisibleContract() bool { return true }\n"
	require.NoError(t, os.WriteFile(sourcePath, []byte("package fixture\n\n"+addition), 0o644))

	stdout, stderr, err := Run(t.Context(), nil, root, "review", "plan", "--structured", "--json")
	require.NoError(t, err, stderr)
	var plan struct {
		ReviewID string `json:"review_id"`
		Units    []struct {
			UnitID string `json:"unit_id"`
		} `json:"primary_units"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &plan))
	require.Len(t, plan.Units, 1)
	stdout, stderr, err = Run(t.Context(), nil, root, "review", "unit",
		plan.ReviewID+"/"+plan.Units[0].UnitID, "--budget-bytes", "16384", "--budget-tokens", "4096", "--json")
	require.NoError(t, err, stderr)
	var packet struct {
		Mandatory struct {
			Hunks []struct {
				Patch string `json:"patch_base64"`
			} `json:"hunks"`
		} `json:"mandatory"`
		Context struct {
			Items []struct {
				Kind    string `json:"kind"`
				Content string `json:"content"`
			} `json:"items"`
		} `json:"context"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &packet))
	var patch strings.Builder
	for _, hunk := range packet.Mandatory.Hunks {
		data, err := base64.StdEncoding.DecodeString(hunk.Patch)
		require.NoError(t, err)
		patch.Write(data)
	}
	require.Equal(t, 180, strings.Count(patch.String(), "+"+paddingLine+"\n"))
	require.Contains(t, patch.String(), "+func VisibleContract() bool { return true }")
	var symbols string
	for _, item := range packet.Context.Items {
		if item.Kind == "symbols_signatures" {
			symbols = item.Content
		}
	}
	_, encoded, found := strings.Cut(symbols, "\npayload: ")
	require.True(t, found,
		"optional symbol evidence must fit without another copy of the complete mandatory patch")
	encoded, _, _ = strings.Cut(encoded, "\n")
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	require.NoError(t, err)
	require.Contains(t, string(decoded), "VisibleContract")
}

func TestNativeReviewCitationsResolveToOwnedEvidence(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", append([]string{
			"-c", "user.name=Review fixture", "-c", "user.email=review@example.invalid",
			"-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null",
		}, args...)...)
		cmd.Dir = root
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, string(output))
	}
	git("init", "-q")
	alpha := "package fixture\n"
	for i := 0; i < 40; i++ {
		alpha += fmt.Sprintf("var A%d = %d\n", i, i)
	}
	alphaPath := filepath.Join(root, "alpha.go")
	betaPath := filepath.Join(root, "beta.go")
	require.NoError(t, os.WriteFile(alphaPath, []byte(alpha), 0o644))
	require.NoError(t, os.WriteFile(betaPath, []byte("package fixture\n\nfunc Beta() int { return 1 }\n"), 0o644))
	require.NoError(t, EnsureIndex(t.Context(), nil, root))
	git("add", ".")
	git("commit", "-qm", "Initial fixture")

	// Two edits far apart in alpha.go become two distinct hunks; beta.go adds a
	// third hunk in a different file. A correct plan cites each hunk against its
	// own file, so the citations must span more than one path.
	lines := strings.Split(alpha, "\n")
	lines[1] = "var A0 = 1000"
	lines[38] = "var A37 = 2000"
	require.NoError(t, os.WriteFile(alphaPath, []byte(strings.Join(lines, "\n")), 0o644))
	require.NoError(t, os.WriteFile(betaPath, []byte("package fixture\n\nfunc Beta() int { return 42 }\n"), 0o644))

	stdout, stderr, err := Run(t.Context(), nil, root, "review", "plan", "--structured", "--json")
	require.NoError(t, err, stderr)
	var plan struct {
		ReviewID string `json:"review_id"`
		Units    []struct {
			UnitID string `json:"unit_id"`
			Hunks  []struct {
				HunkID  string `json:"hunk_id"`
				OldPath string `json:"old_path"`
				NewPath string `json:"new_path"`
			} `json:"hunks"`
		} `json:"primary_units"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &plan))
	require.NotEmpty(t, plan.Units)

	stdout, stderr, err = Run(t.Context(), nil, root, "review", "unit", plan.ReviewID+"/"+plan.Units[0].UnitID, "--json")
	require.NoError(t, err, stderr)
	var packet struct {
		Citations map[string]struct {
			ID   string `json:"id"`
			Path string `json:"path"`
		} `json:"citations"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &packet))

	citedPaths := map[string]bool{}
	hunks := 0
	for _, unit := range plan.Units {
		for _, hunk := range unit.Hunks {
			hunks++
			proof, ok := packet.Citations[hunk.HunkID]
			require.True(t, ok, "hunk %s has no citation proof", hunk.HunkID)
			want := hunk.NewPath
			if want == "" {
				want = hunk.OldPath
			}
			require.Equal(t, want, proof.Path,
				"hunk %s citation must resolve to its own file, not an unrelated one", hunk.HunkID)
			citedPaths[proof.Path] = true
		}
	}
	require.Greater(t, hunks, 1)
	require.Greater(t, len(citedPaths), 1,
		"owned hunk citations must not all collapse onto a single unrelated file")
}
