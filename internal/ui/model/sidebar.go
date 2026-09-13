package model

import (
	"image"
	"strings"

	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/ultraviolet/layout"

	mcp "github.com/neur0map/prowl/internal/agent/tools/mcp"
	"github.com/neur0map/prowl/internal/config"
	"github.com/neur0map/prowl/internal/gitpanel"
	"github.com/neur0map/prowl/internal/session"
	"github.com/neur0map/prowl/internal/ui/common"
	"github.com/neur0map/prowl/internal/ui/logo"
)

// modelInfo renders the current model information including reasoning
// settings, context usage/cost, and code-index savings for the sidebar.
func (m *UI) modelInfo(width int) string {
	model := m.selectedLargeModel()
	reasoningInfo := ""
	reasoningHigh := false
	providerName := ""

	if model != nil {
		reasoningInfo, reasoningHigh = m.reasoningDisplay(model)
		if providerConfig, ok := m.com.Config().Providers.Get(model.ModelCfg.Provider); ok {
			providerName = providerConfig.Name
		}
	}

	var modelContext *common.ModelContextInfo
	if model != nil && m.session != nil {
		modelContext = &common.ModelContextInfo{
			ContextUsed:    m.session.CompletionTokens + m.session.PromptTokens,
			Cost:           m.session.Cost,
			ModelContext:   model.CatwalkCfg.ContextWindow,
			EstimatedUsage: m.session.EstimatedUsage,
		}
	}
	var modelName string
	if model != nil {
		modelName = model.CatwalkCfg.Name
	}
	info := common.ModelInfo(m.com.Styles, modelName, providerName, reasoningInfo, modelContext, width, m.hyperCredits, reasoningHigh)
	if m.session != nil && m.session.FocusMode == session.FocusModeOn {
		info = lipgloss.JoinVertical(lipgloss.Left, info, m.com.Styles.Sidebar.SessionTitle.Render("Focus on"))
	}
	if m.state == uiChat {
		if savings := m.modelSavingsInfo(width); savings != "" {
			info = lipgloss.JoinVertical(lipgloss.Left, info, savings)
		}
	}
	return info
}

// sidebarModeStripView renders the clickable "Status / Git" tab header for the
// sidebar. The active panel is highlighted; the strip's screen rect is captured
// in updateSidebarScrollState for click hit-testing.
func (m *UI) sidebarModeStripView(width int) string {
	t := m.com.Styles
	active := t.Sidebar.SessionTitle
	inactive := t.Resource.AdditionalText
	status, git := "Status", "Git"
	var l, r string
	if m.sidebarMode == sidebarPanelGit {
		l, r = inactive.Render(status), active.Render(git)
	} else {
		l, r = active.Render(status), inactive.Render(git)
	}
	return lipgloss.NewStyle().Width(width).Render(l + inactive.Render("  ·  ") + r)
}

// updateSidebarScrollState renders the sidebar content and computes scroll
// state (scrollability, max offset, clamp) before drawing. This keeps all
// state mutation in the update path rather than in the draw function.
func (m *UI) updateSidebarScrollState() {
	if m.session == nil || m.isCompact {
		return
	}

	const logoHeightBreakpoint = 30

	t := m.com.Styles
	width := m.layout.sidebar.Dx()
	height := m.layout.sidebar.Dy()

	contentWidth := max(width-2, 1)

	sidebarLogo := m.sidebarLogo
	if height < logoHeightBreakpoint {
		sidebarLogo = lipgloss.JoinVertical(lipgloss.Left, logo.SmallRender(m.com.Styles, contentWidth, logo.Opts{}), "")
	}

	modeStrip := m.sidebarModeStripView(contentWidth)
	stripHeight := lipgloss.Height(modeStrip)

	var logoRect, stripRect, contentRect image.Rectangle
	layout.Vertical(
		layout.Len(lipgloss.Height(sidebarLogo)),
		layout.Len(stripHeight),
		layout.Fill(1),
	).Split(m.layout.sidebar).Assign(&logoRect, &stripRect, &contentRect)

	contentHeight := contentRect.Dy()

	m.sidebarContentWidth = contentWidth
	m.sidebarContentHeight = contentHeight
	m.sidebarDrawLogo = sidebarLogo
	m.sidebarStrip = stripRect
	m.sidebarModeStrip = modeStrip

	// The git panel renders exactly contentHeight lines and manages its own
	// tab/selection, so the outer virtual scroller is disabled in git mode.
	if m.sidebarMode == sidebarPanelGit {
		m.sidebarContent = gitpanel.Render(m.gitData, t, contentWidth, contentHeight, m.gitTab, m.gitSel)
		m.sidebarTotalLines = max(1, strings.Count(m.sidebarContent, "\n")+1)
		m.sidebarScrollable = false
		m.sidebarMaxOffsetVal = 0
		m.sidebarOffset = 0
		return
	}

	title := t.Sidebar.SessionTitle.Width(contentWidth).MaxHeight(2).Render(m.session.Title)
	cwd := common.PrettyPath(t, m.com.Workspace.WorkingDir(), contentWidth)

	// Render all items without truncation; virtual scrolling handles overflow.
	lspSection := m.lspInfo(contentWidth, len(m.lspStates), true)
	mcpSection := m.mcpInfo(contentWidth, mcpCount(m.com.Config().MCP.Sorted(), m.mcpStates), true)
	skillsSection := m.skillsInfo(contentWidth, len(m.skillStatusItems()), true)
	filesSection := m.filesInfo(m.com.Workspace.WorkingDir(), contentWidth, fileChangeCount(m.sessionFiles), true)

	// Build the scrollable content.
	parts := []string{title, "", cwd, "", m.modelInfo(contentWidth)}
	parts = append(parts,
		"",
		filesSection,
		"",
		lspSection,
		"",
		mcpSection,
		"",
		skillsSection,
	)
	content := lipgloss.JoinVertical(lipgloss.Left, parts...)

	totalLines := strings.Count(content, "\n") + 1
	m.sidebarContent = content
	m.sidebarTotalLines = totalLines
	m.sidebarScrollable = totalLines > contentHeight
	m.sidebarMaxOffsetVal = max(0, totalLines-contentHeight)

	// If the sidebar is focused but no longer usable (not scrollable and not
	// in git mode), return focus to the chat.
	if m.focus == uiFocusSidebar && !m.sidebarScrollable && m.sidebarMode != sidebarPanelGit {
		m.focus = uiFocusMain
		m.chat.Focus()
	}

	// Clamp sidebarOffset.
	if m.sidebarOffset > m.sidebarMaxOffsetVal {
		m.sidebarOffset = m.sidebarMaxOffsetVal
	}
}

// drawSidebar renders the chat sidebar with a fixed logo and a
// virtual-scrolling content area with an auto-hiding scrollbar. While the
// sidebar is focused, the scrollbar stays visible.
func (m *UI) drawSidebar(scr uv.Screen, area uv.Rectangle) {
	if m.session == nil {
		return
	}

	sidebarLogo := m.sidebarDrawLogo
	contentWidth := m.sidebarContentWidth
	contentHeight := m.sidebarContentHeight
	totalLines := m.sidebarTotalLines
	stripHeight := lipgloss.Height(m.sidebarModeStrip)

	var logoRect, stripRect, contentRect image.Rectangle
	layout.Vertical(
		layout.Len(lipgloss.Height(sidebarLogo)),
		layout.Len(stripHeight),
		layout.Fill(1),
	).Split(area).Assign(&logoRect, &stripRect, &contentRect)

	// Slice visible lines (defensive against a stale offset after a resize).
	lines := strings.Split(m.sidebarContent, "\n")
	offset := max(0, min(m.sidebarOffset, len(lines)))
	end := max(offset, min(offset+contentHeight, len(lines)))
	visibleStr := strings.Join(lines[offset:end], "\n")

	// Determine scrollbar visibility: always visible when focused, otherwise
	// auto-hide.
	scrollbarVisible := totalLines > contentHeight && (m.sidebarScrollbarVisible || m.focus == uiFocusSidebar)

	// Draw the fixed logo.
	uv.NewStyledString(
		lipgloss.NewStyle().
			MaxWidth(contentWidth).
			MaxHeight(lipgloss.Height(sidebarLogo)).
			Render(sidebarLogo),
	).Draw(scr, logoRect)

	// Draw the fixed Status/Git mode strip.
	uv.NewStyledString(
		lipgloss.NewStyle().
			MaxWidth(contentWidth).
			MaxHeight(stripHeight).
			Render(m.sidebarModeStrip),
	).Draw(scr, stripRect)

	// Draw the visible content in the scrollable area.
	uv.NewStyledString(
		lipgloss.NewStyle().
			MaxWidth(contentWidth).
			MaxHeight(contentHeight).
			Render(visibleStr),
	).Draw(scr, contentRect)

	// Draw scrollbar in the reserved column.
	if scrollbarVisible {
		scrollbar := common.Scrollbar(m.com.Styles, contentHeight, totalLines, contentHeight, m.sidebarOffset)
		if scrollbar != "" {
			scrollbarArea := image.Rectangle{
				Min: image.Point{X: area.Max.X - 1, Y: contentRect.Min.Y},
				Max: image.Point{X: area.Max.X, Y: area.Max.Y},
			}
			uv.NewStyledString(scrollbar).Draw(scr, scrollbarArea)
		}
	}
}

// fileChangeCount returns the number of session files with non-zero additions
// or deletions.
func fileChangeCount(files []SessionFile) int {
	count := 0
	for _, f := range files {
		if f.Additions == 0 && f.Deletions == 0 {
			continue
		}
		count++
	}
	return count
}

// mcpCount returns the number of MCP servers that have a state entry.
func mcpCount(mcpCfgs []config.MCP, states map[string]mcp.ClientInfo) int {
	count := 0
	for _, cfg := range mcpCfgs {
		if _, ok := states[cfg.Name]; ok {
			count++
		}
	}
	return count
}
