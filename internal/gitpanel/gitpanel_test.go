package gitpanel

import (
	"context"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/neur0map/prowl/internal/ui/styles"
	"github.com/stretchr/testify/require"
)

func TestParseStatusAheadBehindDirty(t *testing.T) {
	// Header plus two dirty file entries, NUL-delimited.
	out := "## main...origin/main [ahead 2, behind 1]\x00 M internal/foo.go\x00?? new.txt\x00"
	branch, ahead, behind, dirty := parseStatus(out)
	require.Equal(t, "main", branch)
	require.Equal(t, 2, ahead)
	require.Equal(t, 1, behind)
	require.True(t, dirty)
}

func TestParseStatusCleanNoUpstream(t *testing.T) {
	out := "## feature/x\x00"
	branch, ahead, behind, dirty := parseStatus(out)
	require.Equal(t, "feature/x", branch)
	require.Zero(t, ahead)
	require.Zero(t, behind)
	require.False(t, dirty)
}

func TestParseStatusDetached(t *testing.T) {
	out := "## HEAD (no branch)\x00"
	branch, ahead, behind, _ := parseStatus(out)
	require.Equal(t, "(detached)", branch)
	require.Zero(t, ahead)
	require.Zero(t, behind)
}

func TestParseWorktrees(t *testing.T) {
	out := strings.Join([]string{
		"worktree /home/u/proj",
		"HEAD 1111111222222233333334444444555555566666",
		"branch refs/heads/main",
		"",
		"worktree /home/u/proj-wt",
		"HEAD aaaaaaabbbbbbbcccccccddddddd",
		"detached",
		"",
	}, "\n")
	wts := parseWorktrees(out, "/home/u/proj")
	require.Len(t, wts, 2)

	require.Equal(t, "/home/u/proj", wts[0].Path)
	require.Equal(t, "main", wts[0].Branch)
	require.Equal(t, "1111111", wts[0].Head)
	require.True(t, wts[0].Current)

	require.Equal(t, "/home/u/proj-wt", wts[1].Path)
	require.Equal(t, "(detached)", wts[1].Branch)
	require.False(t, wts[1].Current)
}

func TestParseBranches(t *testing.T) {
	out := "*\x1fmain\n \x1ffeature/x\n \x1fbugfix\n"
	branches := parseBranches(out)
	require.Len(t, branches, 3)
	require.Equal(t, "main", branches[0].Name)
	require.True(t, branches[0].Current)
	require.Equal(t, "feature/x", branches[1].Name)
	require.False(t, branches[1].Current)
}

func TestParseCommits(t *testing.T) {
	// Two records: fields joined by \x1f, records terminated by NUL.
	rec := func(h, sh, an, ar, d, s, p string) string {
		return strings.Join([]string{h, sh, an, ar, d, s, p}, "\x1f")
	}
	out := rec("hash1", "h1", "Alice", "2 hours ago", "HEAD -> main", "first", "hash2") + "\x00" +
		rec("hash2", "h2", "Bob", "3 hours ago", "", "second", "") + "\x00"
	commits := parseCommits(out)
	require.Len(t, commits, 2)
	require.Equal(t, "hash1", commits[0].Hash)
	require.Equal(t, "h1", commits[0].ShortHash)
	require.Equal(t, "Alice", commits[0].Author)
	require.Equal(t, "first", commits[0].Subject)
	require.Equal(t, []string{"hash2"}, commits[0].Parents)
	require.Equal(t, "HEAD -> main", commits[0].Refs)
	require.Empty(t, commits[1].Parents)
}

func TestTabsAndItemCount(t *testing.T) {
	require.Equal(t,
		[]Tab{TabGraph, TabWorktrees, TabBranches, TabPRs, TabIssues},
		Tabs())

	d := Data{
		Commits:   make([]Commit, 3),
		Worktrees: make([]Worktree, 2),
		Branches:  make([]Branch, 4),
		PRs:       make([]PR, 1),
		Issues:    make([]Issue, 5),
	}
	require.Equal(t, 3, ItemCount(d, TabGraph))
	require.Equal(t, 2, ItemCount(d, TabWorktrees))
	require.Equal(t, 4, ItemCount(d, TabBranches))
	require.Equal(t, 1, ItemCount(d, TabPRs))
	require.Equal(t, 5, ItemCount(d, TabIssues))
}

func TestRenderBoundedGraph(t *testing.T) {
	st := styles.RyokutonePantera()
	d := Data{
		Repo:   "prowl",
		Branch: "main",
		Commits: []Commit{
			{Hash: "a", ShortHash: "aaa", Subject: "third commit with a fairly long subject line", Parents: []string{"b"}},
			{Hash: "b", ShortHash: "bbb", Subject: "second", Parents: []string{"c"}},
			{Hash: "c", ShortHash: "ccc", Subject: "first"},
		},
	}
	const w, h = 32, 8
	got := Render(d, &st, w, h, TabGraph, 1)
	lines := strings.Split(got, "\n")
	require.Len(t, lines, h, "panel must occupy exactly height lines")
	for _, ln := range lines {
		require.LessOrEqual(t, lipgloss.Width(ln), w, "line %q exceeds width", ln)
	}
	// Header strip names the active tab and the body shows commit nodes.
	require.Contains(t, lines[0], "Graph")
	require.Contains(t, got, "●")
}

func TestRenderSoftErrorNote(t *testing.T) {
	st := styles.RyokutonePantera()
	d := Data{Errors: map[string]string{"prs": "gh not available"}}
	got := Render(d, &st, 40, 5, TabPRs, 0)
	require.Contains(t, got, "gh not available")
}

func TestRenderEmptyPlaceholder(t *testing.T) {
	st := styles.RyokutonePantera()
	got := Render(Data{}, &st, 40, 5, TabIssues, 0)
	require.Contains(t, got, "None")
}

func TestFetchNonRepoSoftFails(t *testing.T) {
	// A directory that is not a git repository must yield a partial Data with
	// per-section soft errors and no hard error (git itself is usable).
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	d, err := Fetch(ctx, dir)
	require.NoError(t, err)
	require.NotEmpty(t, d.Errors, "expected soft errors for a non-repo dir")
	require.Empty(t, d.Commits)
	require.Empty(t, d.Worktrees)
}
