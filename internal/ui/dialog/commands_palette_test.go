package dialog

import (
	"strings"
	"testing"

	"github.com/neur0map/prowl/internal/ui/common"
	"github.com/neur0map/prowl/internal/ui/styles"
	"github.com/stretchr/testify/require"
)

// TestOptionsPaletteHidesGoals is the Part-C contract: the Ctrl+P Options
// palette omits the goal workflow (which stays reachable via "/" completion),
// while still surfacing other slash commands like /green.
func TestOptionsPaletteHidesGoals(t *testing.T) {
	t.Parallel()

	sty := styles.RyokutonePantera()
	c := &Commands{com: &common.Common{Styles: &sty}}

	var haveGoal, haveGreen bool
	for _, item := range c.slashCommands() {
		id := item.ID()
		if strings.HasPrefix(id, "slash:goal") || id == "slash:guided-goal" {
			haveGoal = true
		}
		if id == "slash:green" {
			haveGreen = true
		}
	}

	require.False(t, haveGoal, "goal commands must not appear in the Options palette")
	require.True(t, haveGreen, "non-goal slash commands must still appear")
}
