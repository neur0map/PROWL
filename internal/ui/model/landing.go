package model

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/neur0map/prowl/internal/home"
	"github.com/neur0map/prowl/internal/ui/common"
	"github.com/neur0map/prowl/internal/ui/styles"
	"github.com/neur0map/prowl/internal/workspace"
)

// Landing card sizing. The card is bounded so it neither sprawls across a wide
// terminal nor overflows a narrow or short one.
const (
	landingCardMaxWidth = 92 // widest the card grows on a large terminal
	landingStackInner   = 62 // below this inner width, capability sections stack
)

// selectedLargeModel returns the currently selected large language model as
// memoized by the off-thread busy/agent probe (see workspace_cache.go), or
// nil when the agent isn't ready. It must never probe the workspace: it is
// called on every frame and AgentIsReady/AgentModel are synchronous HTTP
// round-trips in client/server mode.
func (m *UI) selectedLargeModel() *workspace.AgentModel {
	if m.agentReady {
		model := m.agentModel
		return &model
	}
	return nil
}

// landingView renders the landing page as a single bounded card: model and
// code-intelligence identity on top, capability sections (LSPs, MCPs, Skills)
// below. The card width tracks the terminal (clamped) and the capability
// sections switch from three columns to a stack on narrow widths, so the
// layout stays contained instead of leaving a sprawl of dead space.
func (m *UI) landingView() string {
	t := m.com.Styles
	avail := m.layout.main.Dx()

	cardWidth := min(avail-1, landingCardMaxWidth)
	contentW := cardWidth - 4 // two borders + one column of padding each side
	if contentW < 12 {
		// Degenerate width: skip the frame and just stack the identity.
		m.landingScrollable, m.landingMaxOffset = false, 0
		return lipgloss.NewStyle().PaddingTop(1).Render(m.landingIdentity(max(avail-1, 1)))
	}

	// Reserve one column for the scrollbar gutter so the width is stable
	// whether or not the content overflows.
	bodyW := contentW - 1
	lines := strings.Split(m.landingBody(bodyW), "\n")

	// The card grows to fit its content but never past the available height;
	// anything beyond scrolls.
	viewport := min(len(lines), max(4, m.layout.main.Dy()-3))
	maxOffset := max(0, len(lines)-viewport)
	offset := max(0, min(m.landingOffset, maxOffset))
	m.landingOffset = offset
	m.landingMaxOffset = maxOffset
	m.landingScrollable = maxOffset > 0

	visible := lines[offset : offset+viewport]
	var sbLines []string
	if m.landingScrollable {
		sbLines = strings.Split(common.Scrollbar(t, viewport, len(lines), viewport, offset), "\n")
	}

	rows := make([]string, len(visible))
	for i, ln := range visible {
		gutter := " "
		if i < len(sbLines) {
			gutter = sbLines[i]
		}
		rows[i] = padLine(ln, bodyW) + gutter
	}

	card := borderedCard(t, home.Short(m.com.Workspace.WorkingDir()), strings.Join(rows, "\n"), cardWidth)
	return lipgloss.NewStyle().PaddingTop(1).Render(card)
}

// landingBody renders the full card content (no height cap; scrolling handles
// overflow): identity + LSP/MCP on the left and the skills list on the right
// when there is room, otherwise a single stacked column.
func (m *UI) landingBody(contentW int) string {
	const allSkills = 1 << 30 // no cap; the viewport scrolls
	if contentW >= landingStackInner {
		rightW := min(32, contentW/2)
		leftW := contentW - rightW - 2
		left := lipgloss.JoinVertical(lipgloss.Left,
			m.landingIdentity(leftW), "",
			m.lspInfo(leftW, 4, true), "",
			m.mcpInfo(leftW, 4, true),
		)
		right := m.skillsInfo(rightW, allSkills, true)
		return lipgloss.JoinHorizontal(lipgloss.Top,
			lipgloss.NewStyle().Width(leftW).Render(left), "  ", right)
	}
	return lipgloss.JoinVertical(lipgloss.Left,
		m.landingIdentity(contentW), "",
		m.lspInfo(contentW, 4, true), "",
		m.mcpInfo(contentW, 4, true), "",
		m.skillsInfo(contentW, allSkills, true),
	)
}

// landingIdentity renders the model line and the prowl-agent code-intelligence
// readout stacked, sized to width.
func (m *UI) landingIdentity(width int) string {
	parts := []string{m.modelInfo(width)}
	if ci := m.codeIndexInfo(width); ci != "" {
		parts = append(parts, ci)
	}
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// borderedCard wraps body in a rounded border whose top edge carries the
// title (e.g. "╭─ ~/Work/Ryoku-Query ───────╮"), sized to exactly totalWidth
// columns. Width math is ANSI/unicode-safe via lipgloss.Width, so wide runes
// in the title or body do not break the frame.
func borderedCard(t *styles.Styles, title, body string, totalWidth int) string {
	br := lipgloss.RoundedBorder()
	bs := t.Section.Line
	inner := totalWidth - 2 // between the two side borders
	contentW := inner - 2   // one column of padding inside each border
	if contentW < 1 {
		return body
	}

	// Top edge: ╭─ <title> ───────────╮
	maxTitle := inner - 4
	if maxTitle < 1 {
		maxTitle = 1
	}
	title = ansi.Truncate(title, maxTitle, "…")
	dashes := inner - 3 - lipgloss.Width(title)
	if dashes < 0 {
		dashes = 0
	}
	top := bs.Render(br.TopLeft+br.Top+" ") +
		t.Section.Title.Render(title) +
		bs.Render(" "+strings.Repeat(br.Top, dashes)+br.TopRight)

	rows := []string{top}
	for _, ln := range strings.Split(body, "\n") {
		rows = append(rows, bs.Render(br.Left)+" "+padLine(ln, contentW)+" "+bs.Render(br.Right))
	}
	rows = append(rows, bs.Render(br.BottomLeft+strings.Repeat(br.Top, inner)+br.BottomRight))
	return strings.Join(rows, "\n")
}

// padLine pads or truncates s to exactly w display columns, honoring ANSI
// styling and wide runes so bordered rows always align.
func padLine(s string, w int) string {
	switch sw := lipgloss.Width(s); {
	case sw > w:
		return ansi.Truncate(s, w, "")
	case sw < w:
		return s + strings.Repeat(" ", w-sw)
	default:
		return s
	}
}
