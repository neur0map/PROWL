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

	m.applyCodeIndex(codeIndexMsg{result: workspace.CodeIndexStatusResult{Available: true, Ready: true}})
	counts := regexp.MustCompile(`[0-9][0-9,]*`).FindAllString(ansi.Strip(m.modelSavingsInfo(32)), -1)
	require.Equal(t, []string{"0", "0"}, counts, "a confirmed zero must remain visible")
}

func TestSavingsReadoutSeparatesRunAndProjectTotals(t *testing.T) {
	t.Parallel()

	sty := styles.RyokutonePantera()
	m := &UI{com: &common.Common{Styles: &sty}}
	m.applyCodeIndex(codeIndexMsg{result: workspace.CodeIndexStatusResult{Available: true, Ready: true, SavedTokens: 12000}})
	m.applyCodeIndex(codeIndexMsg{result: workspace.CodeIndexStatusResult{Available: true, Ready: true, SavedTokens: 15231}})
	counts := regexp.MustCompile(`[0-9][0-9,]*`).FindAllString(ansi.Strip(m.modelSavingsInfo(32)), -1)
	require.Equal(t, []string{"3,231", "15,231"}, counts)
}
