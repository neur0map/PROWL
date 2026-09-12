package openai

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodexPreservesInstructionsAndToolHistory(t *testing.T) {
	t.Parallel()
	data, err := codexRequestBody([]byte(`{"stream":true,"store":true,"max_output_tokens":42,"temperature":0.5,"instructions":"existing","input":[{"role":"system","content":"Prowl rules"},{"role":"developer","content":[{"type":"input_text","text":"project rules"}]},{"role":"user","content":[{"type":"input_text","text":"run tool"}]},{"type":"function_call","name":"read_file","arguments":"{\"id\":9007199254740993}"},{"type":"function_call_output","call_id":"call","output":"result"}]}`))
	require.NoError(t, err)
	var body map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &body))
	var instructions string
	require.NoError(t, json.Unmarshal(body["instructions"], &instructions))
	require.Equal(t, "existing\n\nProwl rules\n\nproject rules", instructions)
	require.JSONEq(t, `[{"role":"user","content":[{"type":"input_text","text":"run tool"}]},{"type":"function_call","name":"read_file","arguments":"{\"id\":9007199254740993}"},{"type":"function_call_output","call_id":"call","output":"result"}]`, string(body["input"]))
	require.Equal(t, "false", string(body["store"]))
	require.NotContains(t, body, "max_output_tokens")
	require.NotContains(t, body, "temperature")
}
