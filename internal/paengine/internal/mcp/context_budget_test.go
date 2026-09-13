package mcp

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	contextpacket "github.com/neur0map/prowl/internal/paengine/internal/context"
)

func TestContextBudgetCoversMCPContentAndStructuredOutput(t *testing.T) {
	t.Parallel()
	packet := contextpacket.Packet{
		SchemaVersion: contextpacket.PacketSchemaVersion,
		Summary:       "Selected source evidence.",
		Budget:        contextpacket.Budget{RequestedTokens: 500, RequestedBytes: 2000},
		Omitted:       map[string]int{}, Next: []string{},
	}
	for i := range 6 {
		packet.Items = append(packet.Items, contextpacket.Item{
			ID: fmt.Sprintf("source:%d", i), Kind: "source", Title: fmt.Sprintf("owner%d.go", i),
			Summary:        strings.Repeat("quoted \"évidence\" <&> ", 5),
			DetailResource: fmt.Sprintf("prowl://workspace/current/source/owner%d.go", i),
			Citations:      []contextpacket.Citation{{Path: fmt.Sprintf("owner%d.go", i), LineStart: 1, LineEnd: 5}},
		})
	}
	result, packet, err := boundedPacketResult(packet, nil)
	require.NoError(t, err)
	encoded, err := json.Marshal(result)
	require.NoError(t, err)
	require.LessOrEqual(t, len(encoded), 2000)
	require.Equal(t, len(encoded), packet.Budget.ExactBytes)
	require.Equal(t, (len(encoded)+3)/4, packet.Budget.EstimatedTokens)
	require.NotEmpty(t, packet.Items)
	require.Equal(t, "owner0.go", packet.Items[0].Citations[0].Path)
	require.Positive(t, packet.Omitted["budget"])
}
