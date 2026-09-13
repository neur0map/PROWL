package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
)

func TestWireUsageDistinguishesMissingAndZeroProviderPrices(t *testing.T) {
	t.Parallel()
	for _, free := range []bool{false, true} {
		t.Run(fmt.Sprint(free), func(t *testing.T) {
			t.Parallel()
			price := ""
			if free {
				price = `,"cost":0`
			}
			wire := `{"usage":{"prompt_tokens":1000,"completion_tokens":100` + price + `}}`
			usage := &providerUsage{protocol: "openrouter", observedWire: true}
			body := &usageBody{ReadCloser: io.NopCloser(strings.NewReader(wire)), usage: usage}
			_, err := io.Copy(io.Discard, body)
			require.NoError(t, err)
			require.NoError(t, body.Close())
			model := Model{CatwalkCfg: catwalk.Model{CostPer1MIn: 10, CostPer1MOut: 20}}
			// The SDK reports the same zero-valued cost metadata in both cases.
			cost, equivalent, source, complete := usage.billing(model, usage.normalize(fantasy.Usage{}), new(float64(0)))
			require.True(t, complete)
			require.InDelta(t, 0.012, equivalent, 1e-12)
			if free {
				require.Zero(t, cost)
				require.Equal(t, "provider", source)
			} else {
				require.InDelta(t, 0.012, cost, 1e-12)
				require.Equal(t, "catalog", source)
			}
		})
	}
}

func TestUsageCaptureBoundsOversizedEventsWithoutTruncatingTheStream(t *testing.T) {
	t.Parallel()
	wire := "data: " + strings.Repeat("x", maxUsageEventBytes+1) + "\n\n" +
		"data: " + `{"usage":{"prompt_tokens":1000,"completion_tokens":100}}` + "\n\n"
	usage := &providerUsage{protocol: "openai"}
	body := &usageBody{ReadCloser: io.NopCloser(strings.NewReader(wire)), usage: usage, stream: true}
	copied, err := io.Copy(io.Discard, body)
	require.NoError(t, err)
	require.NoError(t, body.Close())
	require.Equal(t, int64(len(wire)), copied, "the accounting tap must not truncate the caller's stream")
	model := Model{CatwalkCfg: catwalk.Model{CostPer1MIn: 10, CostPer1MOut: 20}}
	cost, _, _, complete := usage.billing(model, usage.normalize(fantasy.Usage{}), nil)
	require.InDelta(t, 0.012, cost, 1e-12)
	require.False(t, complete, "later usage must survive, but a dropped event cannot establish a complete bill")
}

func TestUserOwnedCacheLifetimesNeedProviderBreakdownForAnExactBill(t *testing.T) {
	t.Parallel()
	policy := promptCachePolicy{protocol: catwalk.TypeAnthropic, anthropicMarkers: true, ttl: "1h"}
	usage := &providerUsage{protocol: "anthropic", policy: policy}
	var root map[string]any
	require.NoError(t, json.Unmarshal([]byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"stable","cache_control":{"type":"ephemeral","ttl":"1h"}},{"type":"text","text":"recent","cache_control":{"type":"ephemeral","ttl":"5m"}}]}]}`), &root))
	require.NoError(t, applyPromptCache(nil, root, &promptCacheRequest{policy: policy, usage: usage}, nil))
	model := Model{CatwalkCfg: catwalk.Model{CostPer1MIn: 10, CostPer1MInCached: 12.5}}
	usage.observe([]byte(`{"usage":{"input_tokens":0,"cache_creation_input_tokens":1000}}`))
	cost, _, _, complete := usage.billing(model, usage.normalize(fantasy.Usage{}), nil)
	require.InDelta(t, 0.0125, cost, 1e-12)
	require.False(t, complete, "a configured one-hour TTL must not reprice user-owned mixed writes")

	usage.observe([]byte(`{"usage":{"cache_creation":{"ephemeral_1h_input_tokens":300}}}`))
	cost, _, _, complete = usage.billing(model, usage.normalize(fantasy.Usage{}), nil)
	require.InDelta(t, 0.01475, cost, 1e-12)
	require.True(t, complete, "provider-reported lifetime counts establish the correct price")
}
