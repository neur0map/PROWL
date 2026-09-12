package model

import (
	"context"
	"fmt"
	"strconv"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/neur0map/prowl/internal/ui/styles"
	"github.com/neur0map/prowl/internal/workspace"
)

// codeIndexState is the memoized prowl-agent code-index status shown on the
// landing view and sidebar. It is populated off the Update goroutine by
// probeCodeIndexCmd and read on every frame, so it must never be mutated
// outside the Update loop.
type codeIndexState struct {
	files       int
	symbols     int
	savedTokens int
	queries     int
	ready       bool // a usable index exists
	available   bool // the prowl-agent integration is enabled and present
	probed      bool // at least one probe result has landed
	retries     int  // bounded re-probes while an available index is still building

	// sessionBaseline is savedTokens captured at the first probe with data,
	// so "this session" savings = savedTokens - sessionBaseline. haveBaseline
	// guards the one-time capture.
	sessionBaseline int
	haveBaseline    bool
}

// codeIndexMsg carries an off-thread code-index probe result back to the
// Update goroutine.
type codeIndexMsg struct {
	result workspace.CodeIndexStatusResult
}

// maxCodeIndexProbes bounds the "still building" re-probe loop so a project
// whose index never finishes cannot poll forever.
const maxCodeIndexProbes = 20

// codeIndexProber is the optional workspace capability the landing/sidebar use
// to surface index status. Only the in-process AppWorkspace implements it; in
// client/server mode the assertion fails and the readout is omitted.
type codeIndexProber interface {
	CodeIndexStatus(context.Context) workspace.CodeIndexStatusResult
}

// probeCodeIndexCmd probes the code-index status off the Update goroutine
// after the given delay, delivering a codeIndexMsg. It returns nil when the
// workspace does not expose the capability, so the readout is simply omitted.
// The closure captures only the prober, never m.
func (m *UI) probeCodeIndexCmd(delay time.Duration) tea.Cmd {
	if m.com == nil || m.com.Workspace == nil {
		return nil
	}
	prober, ok := m.com.Workspace.(codeIndexProber)
	if !ok {
		return nil
	}
	return tea.Tick(delay, func(time.Time) tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return codeIndexMsg{result: prober.CodeIndexStatus(ctx)}
	})
}

// applyCodeIndex stores a probe result and, while an available index is still
// building, schedules a bounded re-probe so the readout transitions from
// "indexing" to live counts without any user action. Runs on the Update
// goroutine.
func (m *UI) applyCodeIndex(msg codeIndexMsg) tea.Cmd {
	r := msg.result
	m.codeIndex.files = r.Files
	m.codeIndex.symbols = r.Symbols
	m.codeIndex.savedTokens = r.SavedTokens
	m.codeIndex.queries = r.Queries
	m.codeIndex.ready = r.Ready
	m.codeIndex.available = r.Available
	m.codeIndex.probed = true
	if r.Available && !m.codeIndex.haveBaseline {
		// First probe with the integration available fixes the session
		// baseline: everything saved from here on is attributed to this run.
		m.codeIndex.sessionBaseline = r.SavedTokens
		m.codeIndex.haveBaseline = true
	}
	if r.Available && !r.Ready && m.codeIndex.retries < maxCodeIndexProbes {
		m.codeIndex.retries++
		return m.probeCodeIndexCmd(3 * time.Second)
	}
	return nil
}

// refreshCodeIndexCmd re-probes the code-index status promptly. It is called
// on the busy->idle edge so the sidebar's token-savings readout updates
// after each turn, the way session token usage does.
func (m *UI) refreshCodeIndexCmd() tea.Cmd {
	if !m.codeIndex.available {
		return nil
	}
	return m.probeCodeIndexCmd(250 * time.Millisecond)
}

// codeIndexInfo renders the multi-line prowl-agent code-intelligence readout
// for the landing view, or "" when the integration is unavailable or a probe
// has not yet landed. It matches the model-info styling so it reads as part of
// the same at-a-glance header.
func (m *UI) codeIndexInfo(width int) string {
	if !m.codeIndex.available || !m.codeIndex.probed {
		return ""
	}
	t := m.com.Styles
	icon := t.ModelInfo.Icon.Render(styles.CodeIndexIcon)

	var head string
	if m.codeIndex.ready {
		head = fmt.Sprintf("Code intelligence · %s files · %s symbols",
			groupThousands(m.codeIndex.files), groupThousands(m.codeIndex.symbols))
	} else {
		head = "Code intelligence · indexing…"
	}
	lines := []string{fmt.Sprintf("%s %s", icon, t.ModelInfo.Provider.Render(head))}

	if savings := m.codeIndexSavings(); savings != "" {
		lines = append(lines, t.ModelInfo.Reasoning.Render(savings))
	}

	return lipgloss.NewStyle().Width(width).Render(
		lipgloss.JoinVertical(lipgloss.Left, lines...),
	)
}

// sessionSaved returns the tokens prowl-agent has saved during this run:
// the cumulative total minus the baseline captured at the first probe.
func (m *UI) sessionSaved() int {
	if !m.codeIndex.haveBaseline {
		return 0
	}
	return max(0, m.codeIndex.savedTokens-m.codeIndex.sessionBaseline)
}

// codeIndexSavings renders the token-savings phrase for the landing readout,
// showing the cumulative total plus this session's contribution, or "" when
// nothing has been saved yet.
func (m *UI) codeIndexSavings() string {
	if m.codeIndex.savedTokens <= 0 {
		return ""
	}
	if s := m.sessionSaved(); s > 0 {
		return fmt.Sprintf("%s tokens saved · %s this session",
			groupThousands(m.codeIndex.savedTokens), groupThousands(s))
	}
	return fmt.Sprintf("%s tokens saved", groupThousands(m.codeIndex.savedTokens))
}

// CodeIndexSavings reports prowl-agent's cumulative token savings for the
// project and the portion attributable to this session, plus whether any
// savings data is available. Used by the exit banner.
func (m *UI) CodeIndexSavings() (total, session int, ok bool) {
	if !m.codeIndex.available || !m.codeIndex.probed || m.codeIndex.savedTokens <= 0 {
		return 0, 0, false
	}
	return m.codeIndex.savedTokens, m.sessionSaved(), true
}

// modelSavingsInfo renders recorded prowl-agent savings beneath model usage.
// Zero remains visible; an unavailable or unprobed index has no known count.
func (m *UI) modelSavingsInfo(width int) string {
	if !m.codeIndex.available || !m.codeIndex.probed {
		return ""
	}
	t := m.com.Styles
	text := fmt.Sprintf("Prowl-agent tokens saved\n%s this run · %s total",
		groupThousands(m.sessionSaved()), groupThousands(m.codeIndex.savedTokens))
	return t.ModelInfo.Reasoning.Width(width).Render(text)
}

// groupThousands formats a non-negative count with comma thousands separators
// (e.g. 15231 -> "15,231").
func groupThousands(n int) string {
	s := strconv.Itoa(n)
	if n < 0 || len(s) <= 3 {
		return s
	}
	var b []byte
	lead := len(s) % 3
	if lead > 0 {
		b = append(b, s[:lead]...)
	}
	for i := lead; i < len(s); i += 3 {
		if len(b) > 0 {
			b = append(b, ',')
		}
		b = append(b, s[i:i+3]...)
	}
	return string(b)
}
