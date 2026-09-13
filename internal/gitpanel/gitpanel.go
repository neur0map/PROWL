// Package gitpanel is a self-contained data and render layer for a
// VSCode-like "Git" sidebar tab. It shells out to git and gh with bounded
// contexts, degrades gracefully when a section fails (recording a short
// reason in Data.Errors rather than aborting), and renders a width-bounded
// panel including a vertical commit graph. It imports only internal/ui/styles
// for theming and never depends on any ui/model package, so it stays free of
// import cycles.
package gitpanel

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// sectionTimeout bounds each individual git/gh invocation so a single slow or
// hung command cannot stall the whole fetch.
const sectionTimeout = 6 * time.Second

// Worktree is one entry from `git worktree list`.
type Worktree struct {
	Path    string
	Branch  string
	Head    string
	Current bool
}

// Branch is a local branch with its tracking position relative to upstream.
type Branch struct {
	Name    string
	Current bool
	Ahead   int
	Behind  int
}

// PR is a pull request as reported by `gh pr list`.
type PR struct {
	Number int
	Title  string
	State  string
	Author string
	Draft  bool
}

// Issue is an issue as reported by `gh issue list`.
type Issue struct {
	Number int
	Title  string
	State  string
	Author string
	Labels []string
}

// Commit is one entry from `git log`, carrying its parents for graph lane
// assignment and its ref decorations for display.
type Commit struct {
	Hash      string
	ShortHash string
	Author    string
	Date      string
	Subject   string
	Parents   []string
	Refs      string
}

// Data is the full snapshot rendered by the panel. Errors holds per-section
// soft failures keyed by section name ("prs", "issues", "worktrees",
// "branch", "commits") so the UI can show a dim note instead of a blank
// section.
type Data struct {
	Repo      string
	Branch    string
	Dirty     bool
	Worktrees []Worktree
	Branches  []Branch
	PRs       []PR
	Issues    []Issue
	Commits   []Commit
	Errors    map[string]string
}

// setErr records a soft, per-section failure, lazily allocating the map.
func (d *Data) setErr(section, reason string) {
	if d.Errors == nil {
		d.Errors = make(map[string]string)
	}
	d.Errors[section] = reason
}

// Fetch collects git and gh data for repoDir. Each command runs under its own
// bounded sub-context; a failing section is non-fatal and is recorded in
// Data.Errors while the rest continues. It returns a hard error only when git
// itself is unusable (not on PATH); a directory that is not a repository
// yields a partial Data with per-section errors and a nil error.
func Fetch(ctx context.Context, repoDir string) (Data, error) {
	var d Data

	// Git must be executable; without it nothing here can work.
	if _, err := exec.LookPath("git"); err != nil {
		return d, err
	}

	// Core repository probe: toplevel path and repo name. A failure here
	// (e.g. not a git repository) is soft: record it and continue so gh and
	// any usable sections still populate.
	var top string
	if out, err := runGit(ctx, repoDir, "rev-parse", "--show-toplevel"); err != nil {
		d.setErr("repo", shortReason(err))
		d.Repo = filepath.Base(filepath.Clean(repoDir))
	} else {
		top = strings.TrimSpace(out)
		d.Repo = filepath.Base(top)
	}

	// Branch, dirty flag, ahead/behind from the porcelain status header.
	if out, serr := runGit(ctx, repoDir, "status", "--porcelain=v1", "-b", "-z"); serr != nil {
		d.setErr("branch", shortReason(serr))
	} else {
		branch, ahead, behind, dirty := parseStatus(out)
		d.Branch = branch
		d.Dirty = dirty
		if branch != "" {
			d.Branches = []Branch{{Name: branch, Current: true, Ahead: ahead, Behind: behind}}
		}
	}

	// Local branches (name + current marker). Ahead/behind is only known for
	// the current branch via the status header above; other branches list
	// name and current flag.
	if out, berr := runGit(ctx, repoDir, "branch", "--format=%(HEAD)%1f%(refname:short)"); berr != nil {
		if len(d.Branches) == 0 {
			d.setErr("branches", shortReason(berr))
		}
	} else {
		d.Branches = mergeBranches(d.Branches, parseBranches(out))
	}

	// Worktrees.
	if out, werr := runGit(ctx, repoDir, "worktree", "list", "--porcelain"); werr != nil {
		d.setErr("worktrees", shortReason(werr))
	} else {
		d.Worktrees = parseWorktrees(out, top)
	}

	// Commit graph history.
	if out, cerr := runGit(ctx, repoDir, "log", "--max-count=60", "-z",
		"--format=%H%x1f%h%x1f%an%x1f%ar%x1f%D%x1f%s%x1f%P"); cerr != nil {
		d.setErr("commits", shortReason(cerr))
	} else {
		d.Commits = parseCommits(out)
	}

	// Pull requests and issues via gh (soft-fail when gh is missing or the
	// repo is not authed/hosted).
	if prs, perr := fetchPRs(ctx, repoDir); perr != nil {
		d.setErr("prs", ghReason(perr))
	} else {
		d.PRs = prs
	}
	if issues, ierr := fetchIssues(ctx, repoDir); ierr != nil {
		d.setErr("issues", ghReason(ierr))
	} else {
		d.Issues = issues
	}

	return d, nil
}

// runGit runs a git subcommand in repoDir under a bounded sub-context and
// returns its trimmed stdout.
func runGit(ctx context.Context, repoDir string, args ...string) (string, error) {
	sub, cancel := context.WithTimeout(ctx, sectionTimeout)
	defer cancel()
	full := append([]string{"-C", repoDir}, args...)
	cmd := exec.CommandContext(sub, "git", full...)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return "", &cmdError{err: err, stderr: errBuf.String()}
	}
	return out.String(), nil
}

// runGh runs a gh subcommand with cwd=repoDir under a bounded sub-context and
// returns its raw stdout. It reports a clear error when gh is not installed.
func runGh(ctx context.Context, repoDir string, args ...string) ([]byte, error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return nil, errGhMissing
	}
	sub, cancel := context.WithTimeout(ctx, sectionTimeout)
	defer cancel()
	cmd := exec.CommandContext(sub, "gh", args...)
	cmd.Dir = repoDir
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return nil, &cmdError{err: err, stderr: errBuf.String()}
	}
	return out.Bytes(), nil
}

// fetchPRs lists open pull requests for repoDir.
func fetchPRs(ctx context.Context, repoDir string) ([]PR, error) {
	out, err := runGh(ctx, repoDir, "pr", "list",
		"--json", "number,title,state,author,isDraft", "--limit", "20")
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		State  string `json:"state"`
		Author struct {
			Login string `json:"login"`
		} `json:"author"`
		IsDraft bool `json:"isDraft"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &raw); err != nil {
		return nil, err
	}
	prs := make([]PR, 0, len(raw))
	for _, r := range raw {
		prs = append(prs, PR{
			Number: r.Number,
			Title:  r.Title,
			State:  r.State,
			Author: r.Author.Login,
			Draft:  r.IsDraft,
		})
	}
	return prs, nil
}

// fetchIssues lists open issues for repoDir.
func fetchIssues(ctx context.Context, repoDir string) ([]Issue, error) {
	out, err := runGh(ctx, repoDir, "issue", "list",
		"--json", "number,title,state,author,labels", "--limit", "20")
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		State  string `json:"state"`
		Author struct {
			Login string `json:"login"`
		} `json:"author"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &raw); err != nil {
		return nil, err
	}
	issues := make([]Issue, 0, len(raw))
	for _, r := range raw {
		labels := make([]string, 0, len(r.Labels))
		for _, l := range r.Labels {
			labels = append(labels, l.Name)
		}
		issues = append(issues, Issue{
			Number: r.Number,
			Title:  r.Title,
			State:  r.State,
			Author: r.Author.Login,
			Labels: labels,
		})
	}
	return issues, nil
}

// parseStatus parses `git status --porcelain=v1 -b -z` output. The first
// NUL-delimited record is the branch header ("## name...upstream [ahead N,
// behind M]"); any further records are file entries, whose presence means the
// worktree is dirty.
func parseStatus(out string) (branch string, ahead, behind int, dirty bool) {
	records := splitNul(out)
	for i, rec := range records {
		if i == 0 && strings.HasPrefix(rec, "## ") {
			branch, ahead, behind = parseBranchHeader(rec[3:])
			continue
		}
		if strings.TrimSpace(rec) != "" {
			dirty = true
		}
	}
	return branch, ahead, behind, dirty
}

// parseBranchHeader parses the body of a "## " status header line.
func parseBranchHeader(h string) (branch string, ahead, behind int) {
	// Detached HEAD renders as "HEAD (no branch)".
	if strings.HasPrefix(h, "HEAD ") || h == "HEAD" {
		return "(detached)", 0, 0
	}
	// Strip the "[ahead N, behind M]" suffix before extracting the name.
	name := h
	if idx := strings.Index(h, " ["); idx >= 0 {
		name = h[:idx]
		track := h[idx+2:]
		track = strings.TrimSuffix(track, "]")
		ahead = extractCount(track, "ahead ")
		behind = extractCount(track, "behind ")
	}
	// name is "branch...upstream"; keep the local part.
	if idx := strings.Index(name, "..."); idx >= 0 {
		name = name[:idx]
	}
	return strings.TrimSpace(name), ahead, behind
}

// extractCount reads the integer immediately following key within s, e.g.
// extractCount("ahead 3, behind 1", "behind ") == 1.
func extractCount(s, key string) int {
	idx := strings.Index(s, key)
	if idx < 0 {
		return 0
	}
	rest := s[idx+len(key):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	n, _ := strconv.Atoi(rest[:end])
	return n
}

// parseBranches parses `git branch --format=%(HEAD)%1f%(refname:short)`,
// where the leading field is "*" for the current branch and " " otherwise,
// separated from the name by a unit-separator byte (0x1f).
func parseBranches(out string) []Branch {
	var branches []Branch
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\x1f", 2)
		if len(parts) != 2 {
			continue
		}
		name := strings.TrimSpace(parts[1])
		if name == "" {
			continue
		}
		branches = append(branches, Branch{
			Name:    name,
			Current: strings.TrimSpace(parts[0]) == "*",
		})
	}
	return branches
}

// mergeBranches folds ahead/behind data (known only for the current branch)
// from the status probe into the full branch list, preferring the full list
// for ordering while carrying over tracking counts for the current branch.
func mergeBranches(withTracking, full []Branch) []Branch {
	if len(full) == 0 {
		return withTracking
	}
	var cur *Branch
	for i := range withTracking {
		if withTracking[i].Current {
			cur = &withTracking[i]
			break
		}
	}
	if cur == nil {
		return full
	}
	for i := range full {
		if full[i].Name == cur.Name {
			full[i].Current = true
			full[i].Ahead = cur.Ahead
			full[i].Behind = cur.Behind
		}
	}
	return full
}

// parseWorktrees parses `git worktree list --porcelain`. Records are separated
// by blank lines; each has "worktree <path>", "HEAD <sha>", and either
// "branch refs/heads/<name>" or "detached". current is the toplevel of the
// active repository, used to flag the current worktree.
func parseWorktrees(out, current string) []Worktree {
	var (
		wts []Worktree
		wt  Worktree
		has bool
	)
	flush := func() {
		if has {
			wt.Current = wt.Path != "" && sameDir(wt.Path, current)
			wts = append(wts, wt)
		}
		wt = Worktree{}
		has = false
	}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "worktree "):
			flush()
			wt.Path = strings.TrimPrefix(line, "worktree ")
			has = true
		case strings.HasPrefix(line, "HEAD "):
			head := strings.TrimPrefix(line, "HEAD ")
			wt.Head = shortHash(head)
		case strings.HasPrefix(line, "branch "):
			ref := strings.TrimPrefix(line, "branch ")
			wt.Branch = strings.TrimPrefix(ref, "refs/heads/")
		case line == "detached":
			wt.Branch = "(detached)"
		}
	}
	flush()
	return wts
}

// parseCommits parses the NUL-delimited, unit-separator-fielded log output.
func parseCommits(out string) []Commit {
	records := splitNul(out)
	commits := make([]Commit, 0, len(records))
	for _, rec := range records {
		if rec == "" {
			continue
		}
		f := strings.Split(rec, "\x1f")
		if len(f) < 7 {
			continue
		}
		var parents []string
		if p := strings.TrimSpace(f[6]); p != "" {
			parents = strings.Fields(p)
		}
		commits = append(commits, Commit{
			Hash:      f[0],
			ShortHash: f[1],
			Author:    f[2],
			Date:      f[3],
			Refs:      strings.TrimSpace(f[4]),
			Subject:   f[5],
			Parents:   parents,
		})
	}
	return commits
}

// splitNul splits on NUL and drops trailing empties while preserving order.
func splitNul(s string) []string {
	parts := strings.Split(s, "\x00")
	// Drop a single trailing empty produced by the terminator.
	for len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

// shortHash returns the first 7 characters of a commit hash.
func shortHash(h string) string {
	h = strings.TrimSpace(h)
	if len(h) > 7 {
		return h[:7]
	}
	return h
}

// sameDir compares two paths after cleaning, tolerating a missing value.
func sameDir(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return filepath.Clean(a) == filepath.Clean(b)
}
