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
	"strings"

	"github.com/neur0map/prowl/internal/config"
)

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
	// Semantic describes the embedding backlog: how many chunks the project has,
	// and how many still lack a vector. A caller uses it to say whether
	// meaning-based search is whole or still catching up in the background.
	Semantic struct {
		Chunks    int  `json:"chunks"`
		Embedded  int  `json:"embedded"`
		Remaining int  `json:"remaining"`
		Complete  bool `json:"complete"`
	} `json:"semantic"`
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
