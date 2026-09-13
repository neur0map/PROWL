package model

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/neur0map/prowl/internal/config"
	"github.com/neur0map/prowl/internal/skills"
	"github.com/neur0map/prowl/internal/ui/common"
	uistyles "github.com/neur0map/prowl/internal/ui/styles"
	"github.com/stretchr/testify/require"
)

func heroUI(st *uistyles.Styles) *UI {
	return &UI{
		com: &common.Common{Styles: st},
		codeIndex: codeIndexState{
			savedTokens: 159204,
			available:   true,
			probed:      true,
		},
	}
}

// TestTokensSavedHeroSessionLine guards the reported idle-screen bug: when the
// session baseline equals the cumulative total, the hero must not print the
// same number twice ("159,204 this run · 159,204 total"). It shows one honest
// session line instead, and omits it entirely when there is no session data.
func TestTokensSavedHeroSessionLine(t *testing.T) {
	t.Parallel()
	st := uistyles.RyokutonePantera()

	// Unavailable / not probed → nothing rendered.
	require.Empty(t, (&UI{com: &common.Common{Styles: &st}}).tokensSavedHero(60))

	// Session == total (baseline captured at 0): one line, not a duplicated
	// number.
	dup := heroUI(&st)
	dup.codeIndex.haveBaseline = true
	dup.codeIndex.sessionBaseline = 0
	out := lipgloss.NewStyle().Render(dup.tokensSavedHero(60))
	plain := stripANSI(out)
	require.Contains(t, plain, "all from this session")
	require.Equal(t, 1, strings.Count(plain, "159,204"),
		"cumulative count must appear exactly once, not duplicated as run+total")

	// Partial session savings: an explicit +delta line.
	part := heroUI(&st)
	part.codeIndex.haveBaseline = true
	part.codeIndex.sessionBaseline = 147200
	partPlain := stripANSI(part.tokensSavedHero(60))
	require.Contains(t, partPlain, "+12,004 this session")

	// No baseline yet: the cumulative headline only, no session line.
	none := heroUI(&st)
	nonePlain := stripANSI(none.tokensSavedHero(60))
	require.Contains(t, nonePlain, "159,204 tokens saved")
	require.NotContains(t, nonePlain, "this session")
}

// TestSkillEntriesCarryLocationSortedExcludesDisabled is the Ctrl+K modal
// contract: every active skill is listed once with its on-disk location,
// name-sorted, and disabled skills are omitted.
func TestSkillEntriesCarryLocationSortedExcludesDisabled(t *testing.T) {
	t.Parallel()
	st := uistyles.RyokutonePantera()

	ui := &UI{
		com: &common.Common{
			Styles:    &st,
			Workspace: &testWorkspace{cfg: &config.Config{Options: &config.Options{DisabledSkills: []string{"ryoku"}}}},
		},
		skillStates: []*skills.SkillState{
			{Name: "zebra", Path: "/tmp/zebra/SKILL.md", State: skills.StateNormal},
			{Name: "alpha", Path: "/tmp/alpha/SKILL.md", State: skills.StateError},
			{Name: "ryoku", Path: "/tmp/ryoku/SKILL.md", State: skills.StateNormal},
		},
	}

	entries := ui.skillEntries()
	require.NotEmpty(t, entries)

	byName := make(map[string]dialogEntry)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name)
		byName[e.Name] = dialogEntry{path: e.Path, errored: e.Errored}
		require.NotEqual(t, "ryoku", e.Name, "disabled skill must be excluded")
	}

	require.True(t, sortedStrings(names), "entries must be name-sorted: %v", names)
	require.Equal(t, "/tmp/alpha/SKILL.md", byName["alpha"].path)
	require.True(t, byName["alpha"].errored, "errored state must propagate")
	require.Equal(t, "/tmp/zebra/SKILL.md", byName["zebra"].path)
}

type dialogEntry struct {
	path    string
	errored bool
}

func sortedStrings(s []string) bool {
	for i := 1; i < len(s); i++ {
		if s[i-1] > s[i] {
			return false
		}
	}
	return true
}
