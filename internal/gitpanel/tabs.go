package gitpanel

// Tab identifies one section of the git panel.
type Tab int

const (
	// TabGraph shows the vertical commit graph.
	TabGraph Tab = iota
	// TabWorktrees lists checked-out worktrees.
	TabWorktrees
	// TabBranches lists local branches.
	TabBranches
	// TabPRs lists open pull requests.
	TabPRs
	// TabIssues lists open issues.
	TabIssues
)

// Tabs returns the tabs in display order.
func Tabs() []Tab {
	return []Tab{TabGraph, TabWorktrees, TabBranches, TabPRs, TabIssues}
}

// String returns the tab's identifier for logs and tests.
func (t Tab) String() string {
	switch t {
	case TabGraph:
		return "graph"
	case TabWorktrees:
		return "worktrees"
	case TabBranches:
		return "branches"
	case TabPRs:
		return "prs"
	case TabIssues:
		return "issues"
	default:
		return "unknown"
	}
}

// Label returns the tab's title as shown in the header strip.
func (t Tab) Label() string {
	switch t {
	case TabGraph:
		return "Graph"
	case TabWorktrees:
		return "Worktrees"
	case TabBranches:
		return "Branches"
	case TabPRs:
		return "PRs"
	case TabIssues:
		return "Issues"
	default:
		return "?"
	}
}

// errorKey maps a tab to its Data.Errors key so Render can show a soft-error
// note for the active section.
func (t Tab) errorKey() string {
	switch t {
	case TabWorktrees:
		return "worktrees"
	case TabBranches:
		return "branches"
	case TabPRs:
		return "prs"
	case TabIssues:
		return "issues"
	case TabGraph:
		return "commits"
	default:
		return ""
	}
}

// ItemCount returns the number of selectable rows in the given tab.
func ItemCount(d Data, tab Tab) int {
	switch tab {
	case TabGraph:
		return len(d.Commits)
	case TabWorktrees:
		return len(d.Worktrees)
	case TabBranches:
		return len(d.Branches)
	case TabPRs:
		return len(d.PRs)
	case TabIssues:
		return len(d.Issues)
	default:
		return 0
	}
}
