package model

import (
	"regexp"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/neur0map/prowl/internal/ui/common"
	"github.com/neur0map/prowl/internal/ui/styles"
	"github.com/neur0map/prowl/internal/workspace"
	"github.com/stretchr/testify/require"
)

func TestSavingsReadoutDistinguishesUnavailableAndZero(t *testing.T) {
	t.Parallel()

	sty := styles.RyokutonePantera()
	m := &UI{com: &common.Common{Styles: &sty}}
	require.Empty(t, m.modelSavingsInfo(32))
	m.applyCodeIndex(codeIndexMsg{result: workspace.CodeIndexStatusResult{}})
	require.Empty(t, m.modelSavingsInfo(32))

	m.applyCodeIndex(codeIndexMsg{result: workspace.CodeIndexStatusResult{Available: true, Ready: true, OK: true}})
	counts := regexp.MustCompile(`[0-9][0-9,]*`).FindAllString(ansi.Strip(m.modelSavingsInfo(32)), -1)
	require.Equal(t, []string{"0", "0"}, counts, "a confirmed zero must remain visible")
}

// TestSavingsReadoutKeepsLastGoodOnFailedProbe guards the fake-zero bug: a
// probe that fails or times out (OK==false) must not clobber the last good
// counts and savings with fabricated zeros.
func TestSavingsReadoutKeepsLastGoodOnFailedProbe(t *testing.T) {
	t.Parallel()

	sty := styles.RyokutonePantera()
	m := &UI{com: &common.Common{Styles: &sty}}
	m.applyCodeIndex(codeIndexMsg{result: workspace.CodeIndexStatusResult{Available: true, Ready: true, SavedTokens: 15231, OK: true}})
	// A subsequent failed probe (the starvation/timeout case) carries no data.
	m.applyCodeIndex(codeIndexMsg{result: workspace.CodeIndexStatusResult{Available: true}})
	counts := regexp.MustCompile(`[0-9][0-9,]*`).FindAllString(ansi.Strip(m.modelSavingsInfo(32)), -1)
	require.Equal(t, []string{"0", "15,231"}, counts, "a failed probe must preserve the last-good total")
}

func TestSavingsReadoutSeparatesRunAndProjectTotals(t *testing.T) {
	t.Parallel()

	sty := styles.RyokutonePantera()
	m := &UI{com: &common.Common{Styles: &sty}}
	m.applyCodeIndex(codeIndexMsg{result: workspace.CodeIndexStatusResult{Available: true, Ready: true, SavedTokens: 12000, OK: true}})
	m.applyCodeIndex(codeIndexMsg{result: workspace.CodeIndexStatusResult{Available: true, Ready: true, SavedTokens: 15231, OK: true}})
	counts := regexp.MustCompile(`[0-9][0-9,]*`).FindAllString(ansi.Strip(m.modelSavingsInfo(32)), -1)
	require.Equal(t, []string{"3,231", "15,231"}, counts)
}
