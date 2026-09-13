// Package prowlagent centralizes Prowl's integration with prowl-agent's
// code-intelligence engine: running queries, checking index status, building or
// refreshing the index at launch and on demand, and proposing durable project
// knowledge.
//
// The code-intelligence engine is linked in process. Runtime queries do not
// require an external prowl-agent executable; builds use CGO and sqlite_fts5.
package prowlagent

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/neur0map/prowl/internal/config"
)

// indexBuildTimeout bounds a background index build so a pathological repo can
// never leave a goroutine running forever.
const indexBuildTimeout = 10 * time.Minute

// Resolve reports the backend's location label (a binary path, or a marker for
// the in-process engine) and whether the engine is available.
func Resolve(opts *config.ProwlAgentOptions) (string, bool) {
	return resolveBackend(opts)
}

// Available reports whether the integration is enabled and its engine is
// available. It is the single gate callers use before wiring prowl-agent in.
func Available(opts *config.ProwlAgentOptions) bool {
	return opts.IsEnabled() && availableBackend(opts)
}

// Run executes a prowl-agent subcommand against workingDir and returns its
// stdout and stderr.
func Run(ctx context.Context, opts *config.ProwlAgentOptions, workingDir string, args ...string) (stdout, stderr string, err error) {
	return runBackend(ctx, opts, workingDir, args...)
}

// Status is the subset of `prowl-agent status --json` Prowl needs to decide
// whether an index already exists.
type Status struct {
	Counts struct {
		Files   int `json:"files"`
		Symbols int `json:"symbols"`
	} `json:"counts"`
	LastIndex string `json:"last_index"`
	// Savings is prowl-agent's cumulative accounting of the context tokens
	// its cited answers spared the model versus reading whole files.
	Savings struct {
		Queries      int `json:"queries"`
		AnswerTokens int `json:"answer_tokens"`
		SavedTokens  int `json:"saved_tokens"`
	} `json:"savings"`
}

// Indexed reports whether a usable index already exists for the project.
func (s Status) Indexed() bool {
	return s.Counts.Files > 0 && s.LastIndex != "" && s.LastIndex != "0"
}

// QueryStatus runs `status --json` and parses it. A non-nil error means the
// status could not be read (no index yet, binary missing, or malformed
// output).
func QueryStatus(ctx context.Context, opts *config.ProwlAgentOptions, workingDir string) (Status, error) {
	out, serr, err := Run(ctx, opts, workingDir, "status", "--json")
	if err != nil {
		if m := strings.TrimSpace(serr); m != "" {
			return Status{}, fmt.Errorf("%s", m)
		}
		return Status{}, err
	}
	var st Status
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		return Status{}, fmt.Errorf("parse status: %w", err)
	}
	return st, nil
}

// EnsureIndex builds or refreshes the project index and refreshes the AGENTS.md
// map. `prowl-agent overview` requires an existing .prowl workspace, so when
// none exists yet EnsureIndex first bootstraps one with `init` (writing no
// integration files, only the gitignored .prowl/ index), then runs overview.
// It is the single idempotent call Prowl makes at launch and on demand.
func EnsureIndex(ctx context.Context, opts *config.ProwlAgentOptions, workingDir string) error {
	// Bootstrap the workspace/index when it does not exist yet. status errors
	// ("no .prowl workspace found") are the signal to run init.
	if _, err := QueryStatus(ctx, opts, workingDir); err != nil {
		if _, serr, ierr := Run(ctx, opts, workingDir, "init", "--no-input", "--yes", "--integrations", "none"); ierr != nil {
			if m := strings.TrimSpace(serr); m != "" {
				return fmt.Errorf("prowl-agent init: %s", m)
			}
			return fmt.Errorf("prowl-agent init: %w", ierr)
		}
	}
	_, serr, err := Run(ctx, opts, workingDir, "overview", "--format", "toon")
	if err != nil {
		if m := strings.TrimSpace(serr); m != "" {
			return fmt.Errorf("prowl-agent overview: %s", m)
		}
		return fmt.Errorf("prowl-agent overview: %w", err)
	}
	return nil
}

// EnsureIndexAsync refreshes the index in the background when the integration
// and auto-indexing are enabled, the binary is present, and the working
// directory looks like a real project. It never blocks the caller and logs the
// outcome; failures are non-fatal.
func EnsureIndexAsync(cfg *config.ConfigStore) {
	opts := cfg.Config().Options.GetProwlAgent()
	if !opts.AutoIndexEnabled() {
		return
	}
	if !Available(opts) {
		return
	}
	workingDir := cfg.WorkingDir()
	if !ShouldAutoIndex(workingDir) {
		slog.Debug("Skipping prowl-agent auto-index for non-project directory",
			"component", "prowl-agent", "dir", workingDir)
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), indexBuildTimeout)
		defer cancel()
		start := time.Now()
		if err := EnsureIndex(ctx, opts, workingDir); err != nil {
			slog.Warn("Prowl-agent index refresh failed",
				"component", "prowl-agent", "dir", workingDir, "error", err)
			return
		}
		slog.Info("Prowl-agent index refreshed",
			"component", "prowl-agent", "dir", workingDir, "took", time.Since(start).String())
	}()
}

// ShouldAutoIndex reports whether workingDir is safe to auto-index: a git
// working tree, or a folder already carrying a prowl-agent index. This keeps a
// bare launch in a large non-project directory (e.g. $HOME) from triggering a
// huge unsolicited index build.
func ShouldAutoIndex(workingDir string) bool {
	if _, err := os.Stat(filepath.Join(workingDir, ".git")); err == nil {
		return true
	}
	if _, err := os.Stat(filepath.Join(workingDir, ".prowl", "index.db")); err == nil {
		return true
	}
	return false
}

// KnowledgeProposal describes a durable lesson to add to the prowl-agent
// knowledge review inbox.
type KnowledgeProposal struct {
	Title   string
	Body    string
	Type    string // default "Claim"
	Tags    []string
	Anchors []string
	Author  string // default "prowl"
	Target  string // bundle-relative path; derived from the title when empty
}

// ProposeKnowledge records a durable lesson as a reviewable prowl-agent
// knowledge proposal. It initializes the knowledge bundle if needed (idempotent)
// and then runs `knowledge propose`, returning the raw JSON proposal output.
func ProposeKnowledge(ctx context.Context, opts *config.ProwlAgentOptions, workingDir string, p KnowledgeProposal) (string, error) {
	if strings.TrimSpace(p.Title) == "" {
		return "", errors.New("knowledge proposal needs a title")
	}
	if strings.TrimSpace(p.Body) == "" {
		return "", errors.New("knowledge proposal needs a body")
	}
	if _, serr, err := Run(ctx, opts, workingDir, "knowledge", "init", "--json"); err != nil {
		if m := strings.TrimSpace(serr); m != "" {
			return "", fmt.Errorf("knowledge init: %s", m)
		}
		return "", fmt.Errorf("knowledge init: %w", err)
	}
	target := strings.TrimSpace(p.Target)
	if target == "" {
		target = "lessons/" + slug(p.Title) + ".md"
	}
	args := []string{
		"knowledge", "propose",
		"--title", p.Title,
		"--body", p.Body,
		"--type", cmp.Or(strings.TrimSpace(p.Type), "Claim"),
		"--author", cmp.Or(strings.TrimSpace(p.Author), "prowl"),
		"--target", target,
		"--json",
	}
	for _, t := range p.Tags {
		if t = strings.TrimSpace(t); t != "" {
			args = append(args, "--tag", t)
		}
	}
	for _, anchor := range p.Anchors {
		args = append(args, "--anchor", anchor)
	}
	out, serr, err := Run(ctx, opts, workingDir, args...)
	if err != nil {
		if m := strings.TrimSpace(serr); m != "" {
			return "", fmt.Errorf("knowledge propose: %s", m)
		}
		return "", fmt.Errorf("knowledge propose: %w", err)
	}
	return out, nil
}

// slug converts a title into a short filesystem-safe token for a knowledge
// target path.
func slug(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 48 {
		out = strings.Trim(out[:48], "-")
	}
	if out == "" {
		out = "lesson"
	}
	return out
}
