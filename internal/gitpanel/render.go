package gitpanel

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/neur0map/prowl/internal/ui/styles"
)

// Render draws a width×height git panel for the active tab. The first line is
// the tab strip (active tab highlighted); the remaining lines are the active
// tab's list, scrolled to keep row sel visible and truncated to width. It is
// pure and stateless: identical inputs yield identical output.
func Render(d Data, st *styles.Styles, width, height int, tab Tab, sel int) string {
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}

	out := make([]string, 0, height)
	out = append(out, renderTabStrip(st, tab, width))

	bodyHeight := height - len(out)
	if bodyHeight < 0 {
		bodyHeight = 0
	}

	rows := buildRows(d, tab)
	body := renderBody(d, st, tab, rows, width, bodyHeight, sel)
	out = append(out, body...)

	// Pad to the full height so the panel occupies a fixed box.
	for len(out) < height {
		out = append(out, "")
	}
	if len(out) > height {
		out = out[:height]
	}
	return strings.Join(out, "\n")
}

// renderTabStrip builds the header row with the active tab highlighted. It
// prefers themed dialog styles; the active tab reuses the selected-item
// treatment and the inactive tabs a subdued one.
func renderTabStrip(st *styles.Styles, active Tab, width int) string {
	activeStyle := st.Dialog.SelectedItem.Padding(0, 1)
	inactiveStyle := st.Dialog.SecondaryText.Padding(0, 1)
	parts := make([]string, 0, len(Tabs()))
	for _, t := range Tabs() {
		if t == active {
			parts = append(parts, activeStyle.Render(t.Label()))
		} else {
			parts = append(parts, inactiveStyle.Render(t.Label()))
		}
	}
	return ansi.Truncate(strings.Join(parts, ""), width, "…")
}

// renderBody renders the scrolled, selection-highlighted list for the active
// tab into at most bodyHeight lines. When the tab has no rows it shows either
// a dim soft-error note (if one is recorded) or a "None" placeholder.
func renderBody(d Data, st *styles.Styles, tab Tab, rows []string, width, bodyHeight, sel int) []string {
	dim := st.Dialog.SecondaryText.Padding(0, 0)
	if len(rows) == 0 {
		if bodyHeight == 0 {
			return nil
		}
		if reason, ok := d.Errors[tab.errorKey()]; ok && reason != "" {
			return []string{dim.Render(ansi.Truncate(reason, width, "…"))}
		}
		return []string{dim.Render(ansi.Truncate("None", width, "…"))}
	}
	if bodyHeight == 0 {
		return nil
	}

	if sel < 0 {
		sel = 0
	}
	if sel >= len(rows) {
		sel = len(rows) - 1
	}
	offset := scrollOffset(sel, len(rows), bodyHeight)

	normal := st.Dialog.NormalItem.Padding(0, 0)
	selected := st.Dialog.SelectedItem.Padding(0, 0).Width(width)

	lines := make([]string, 0, bodyHeight)
	for i := offset; i < len(rows) && len(lines) < bodyHeight; i++ {
		content := ansi.Truncate(rows[i], width, "…")
		if i == sel {
			lines = append(lines, selected.Render(content))
		} else {
			lines = append(lines, normal.Render(content))
		}
	}
	return lines
}

// scrollOffset returns the first visible row index so that sel stays within a
// window of bodyHeight rows.
func scrollOffset(sel, total, bodyHeight int) int {
	if bodyHeight <= 0 || total <= bodyHeight {
		return 0
	}
	offset := sel - bodyHeight/2
	if offset < 0 {
		offset = 0
	}
	if offset > total-bodyHeight {
		offset = total - bodyHeight
	}
	return offset
}

// buildRows produces the raw (unstyled) content string for each row of the
// active tab. Truncation and selection styling are applied later.
func buildRows(d Data, tab Tab) []string {
	switch tab {
	case TabGraph:
		return graphRows(d.Commits)
	case TabWorktrees:
		return worktreeRows(d.Worktrees)
	case TabBranches:
		return branchRows(d.Branches)
	case TabPRs:
		return prRows(d.PRs)
	case TabIssues:
		return issueRows(d.Issues)
	default:
		return nil
	}
}

// graphRows pairs each commit with its rail glyphs.
func graphRows(commits []Commit) []string {
	rails := buildGraph(commits)
	rows := make([]string, len(commits))
	for i, c := range commits {
		var sb strings.Builder
		sb.WriteString(rails[i].Rail)
		sb.WriteByte(' ')
		sb.WriteString(c.ShortHash)
		sb.WriteByte(' ')
		sb.WriteString(c.Subject)
		if refs := shortRefs(c.Refs); refs != "" {
			sb.WriteString(" (")
			sb.WriteString(refs)
			sb.WriteByte(')')
		}
		rows[i] = sb.String()
	}
	return rows
}

// shortRefs trims a %D decoration to its first couple of refs so it stays
// compact in a narrow panel.
func shortRefs(refs string) string {
	if refs == "" {
		return ""
	}
	parts := strings.Split(refs, ", ")
	for i, p := range parts {
		parts[i] = strings.TrimPrefix(p, "HEAD -> ")
	}
	if len(parts) > 2 {
		parts = append(parts[:2], "…")
	}
	return strings.Join(parts, ", ")
}

// worktreeRows renders one line per worktree.
func worktreeRows(wts []Worktree) []string {
	rows := make([]string, len(wts))
	for i, w := range wts {
		marker := "  "
		if w.Current {
			marker = "* "
		}
		branch := w.Branch
		if branch == "" {
			branch = w.Head
		}
		rows[i] = fmt.Sprintf("%s%s  %s", marker, branch, prettyPath(w.Path))
	}
	return rows
}

// branchRows renders one line per branch with tracking counts.
func branchRows(branches []Branch) []string {
	rows := make([]string, len(branches))
	for i, b := range branches {
		marker := "  "
		if b.Current {
			marker = "* "
		}
		track := ""
		if b.Ahead > 0 {
			track += fmt.Sprintf(" ↑%d", b.Ahead)
		}
		if b.Behind > 0 {
			track += fmt.Sprintf(" ↓%d", b.Behind)
		}
		rows[i] = marker + b.Name + track
	}
	return rows
}

// prRows renders one line per pull request.
func prRows(prs []PR) []string {
	rows := make([]string, len(prs))
	for i, p := range prs {
		draft := ""
		if p.Draft {
			draft = " [draft]"
		}
		rows[i] = fmt.Sprintf("#%d %s%s", p.Number, p.Title, draft)
	}
	return rows
}

// issueRows renders one line per issue.
func issueRows(issues []Issue) []string {
	rows := make([]string, len(issues))
	for i, is := range issues {
		labels := ""
		if len(is.Labels) > 0 {
			labels = " [" + strings.Join(is.Labels, ", ") + "]"
		}
		rows[i] = fmt.Sprintf("#%d %s%s", is.Number, is.Title, labels)
	}
	return rows
}

// prettyPath shortens a worktree path to its final two segments so long
// absolute paths stay readable in the sidebar.
func prettyPath(p string) string {
	p = strings.TrimRight(p, "/")
	if p == "" {
		return p
	}
	parts := strings.Split(p, "/")
	if len(parts) <= 2 {
		return p
	}
	return ".../" + strings.Join(parts[len(parts)-2:], "/")
}
