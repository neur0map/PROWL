package anthropic

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAttestationMatchesIndependentBunVectorAndOnlyPatchesBilling(t *testing.T) {
	t.Parallel()
	// Bun.hash.xxHash64(body, 0x4d659218e32a3268n) = 7099324936d6f44a.
	original := `{"messages":[{"role":"user","content":"cch=00000"}],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.257.abc; cc_entrypoint=cli; cch=00000;"},{"type":"text","text":"cch=00000"}]}`
	body := []byte(original)
	require.True(t, patchCch(body))
	require.Equal(t, strings.Replace(original, "cli; cch=00000;", "cli; cch=6f44a;", 1), string(body))
	require.False(t, patchCch([]byte(`{"system":[{"type":"text","text":"caller cch=00000"}]}`)))
}

func TestBillingFingerprintUsesJavaScriptUTF16Concatenation(t *testing.T) {
	t.Parallel()
	// Selected UTF-16 units from separate emoji combine before UTF-8 hashing.
	require.Contains(t, createClaudeBillingHeader("abcd😀😀1234567890123"), "cc_version=2.1.257.be5;")
}

func TestClaudeToolsAndSignedHistorySurviveWireAdaptation(t *testing.T) {
	t.Parallel()
	body, names, err := claudeRequestBody([]byte(`{"system":[{"type":"text","text":"Prowl rules","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":"read file"},{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":"signed-thought"},{"type":"tool_use","id":"call","name":"_read_file","input":{"name":"_read_file","id":9007199254740993}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call","content":"done"}]}],"tools":[{"name":"_read_file","input_schema":{"properties":{"name":{"type":"string"}}}},{"type":"web_search_20250305","name":"web_search"}],"tool_choice":{"type":"tool","name":"_read_file"},"max_tokens":256}`), "session", "account")
	require.NoError(t, err)
	require.Equal(t, map[string]string{"__read_file": "_read_file"}, names)
	var request struct {
		System   []json.RawMessage `json:"system"`
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
		Choice struct {
			Name string `json:"name"`
		} `json:"tool_choice"`
	}
	require.NoError(t, json.Unmarshal(body, &request))
	require.JSONEq(t, `{"type":"text","text":"Prowl rules","cache_control":{"type":"ephemeral"}}`, string(request.System[2]))
	require.Equal(t, "__read_file", request.Tools[0].Name)
	require.Equal(t, "web_search", request.Tools[1].Name)
	require.Equal(t, "__read_file", request.Choice.Name)
	require.Contains(t, string(request.Messages[1].Content), `"signature":"signed-thought"`)
	require.Contains(t, string(request.Messages[1].Content), `"id":9007199254740993`)
	require.Contains(t, string(request.Messages[1].Content), `"input":{"name":"_read_file","id":9007199254740993}`)
	require.JSONEq(t, `[{"type":"tool_result","tool_use_id":"call","content":"done"}]`, string(request.Messages[2].Content))
	restored, err := restoreToolNames([]byte(`{"type":"message","content":[{"type":"tool_use","id":"call","name":"__read_file","input":{"name":"__read_file"}}]}`), names)
	require.NoError(t, err)
	require.JSONEq(t, `{"type":"message","content":[{"type":"tool_use","id":"call","name":"_read_file","input":{"name":"__read_file"}}]}`, string(restored))
}

func TestToolStreamPreservesLargeDeltasAndRestoresNames(t *testing.T) {
	t.Parallel()
	large := `data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"` + strings.Repeat("x", 128*1024) + `"}}` + "\n\n"
	tool := `data: {"type":"content_block_start","content_block":{"type":"tool_use","name":"_read_file","input":{"name":"_read_file"}}}` + "\n\n"
	source := io.NopCloser(strings.NewReader(large + tool))
	stream := &toolNameStream{source: source, reader: bufio.NewReader(source), names: map[string]string{"_read_file": "read_file"}}
	data, err := io.ReadAll(stream)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(data), large))
	tail := strings.TrimSpace(strings.TrimPrefix(string(data), large))
	require.JSONEq(t, `{"type":"content_block_start","content_block":{"type":"tool_use","name":"read_file","input":{"name":"_read_file"}}}`, strings.TrimPrefix(tail, "data: "))
	require.NoError(t, stream.Close())
}
