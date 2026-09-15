package tools

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"charm.land/fantasy"

	"github.com/neur0map/prowl/internal/config"
	"github.com/neur0map/prowl/internal/prowlagent"
)

const ProwlAgentToolName = "prowl_agent"

//go:embed prowl_agent.md
var prowlAgentDescription string

// prowlAgentReadOnlyCommands is the set of prowl-agent subcommands the tool
// exposes. Every entry is a read-only query; mutating or side-effecting
// subcommands (init, restart, update, skills, graph, explore, docs) are
// intentionally excluded so the model cannot reindex, rewrite config, write
// files, or reach the network through this tool.
var prowlAgentReadOnlyCommands = []string{
	"overview", "search", "find", "def", "outline", "references", "impact",
	"peek", "context", "brief", "callers", "callees", "history", "hotspots",
	"clusters", "entrypoints", "relations", "tests", "status", "wip",
	"changed", "violations", "capabilities", "span", "sketch", "doctor",
	"knowledge",
}

// prowlAgentKnowledgeReads are the only `knowledge` subcommands the tool
// allows. The command group is mixed: list/show/lint read, while propose,
// accept, reject, init, and export write to the bundle or the review inbox.
// Recall is what the model needs; accepting its own proposal would defeat the
// human review the knowledge workflow exists for.
var prowlAgentKnowledgeReads = []string{"list", "show", "lint"}

// maxProwlAgentOutput caps tool output so a large answer cannot flood the
// model context. prowl-agent packets are token-lean by design, so this is a
// generous ceiling that only trips on pathological queries.
const maxProwlAgentOutput = 64 * 1024

// ProwlAgentParams are the inputs to the prowl_agent tool.
type ProwlAgentParams struct {
	Command string   `json:"command" description:"The read-only prowl-agent subcommand to run, e.g. search, find, def, outline, references, impact, overview."`
	Args    []string `json:"args,omitempty" description:"Arguments for the subcommand, e.g. [\"NewGui\"] for find, [\"internal/app/app.go\"] for outline, or [\"how does discovery work\"] for search."`
}

// NewProwlAgentTool returns a tool that runs read-only prowl-agent queries in
// the workspace, giving the model a cited code index instead of grep-and-read
// loops. workingDir is the repository root; opts controls enablement.
func NewProwlAgentTool(opts *config.ProwlAgentOptions, workingDir string) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		ProwlAgentToolName,
		prowlAgentDescription,
		func(ctx context.Context, params ProwlAgentParams, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
			cmd := strings.TrimSpace(params.Command)
			if cmd == "" {
				return fantasy.NewTextErrorResponse("command is required (e.g. search, find, def, outline, references, impact)"), nil
			}
			if !slices.Contains(prowlAgentReadOnlyCommands, cmd) {
				return fantasy.NewTextErrorResponse(fmt.Sprintf(
					"unsupported prowl-agent command %q; allowed read-only commands: %s",
					cmd, strings.Join(prowlAgentReadOnlyCommands, ", "),
				)), nil
			}
			// `knowledge` is the one mixed group: gate its subcommand so
			// recall is available but the review workflow stays human.
			if cmd == "knowledge" {
				sub := ""
				if len(params.Args) > 0 {
					sub = strings.TrimSpace(params.Args[0])
				}
				if !slices.Contains(prowlAgentKnowledgeReads, sub) {
					return fantasy.NewTextErrorResponse(fmt.Sprintf(
						"knowledge %q is not available to the agent; allowed: %s. "+
							"Record a new lesson with the learn tool, which files a proposal for human review.",
						sub, strings.Join(prowlAgentKnowledgeReads, ", "),
					)), nil
				}
			}

			args := append([]string{cmd}, params.Args...)
			if !prowlAgentHasFormatFlag(params.Args) {
				args = append(args, "--format", "toon")
			}

			stdout, stderr, runErr := runProwlAgentWithRefreshRetry(ctx, opts, workingDir, args)
			out := strings.TrimRight(stdout, "\n")
			if runErr != nil {
				msg := strings.TrimSpace(stderr)
				if msg == "" {
					msg = runErr.Error()
				}
				// A refresh in flight is a transient condition, not a reason
				// to abandon the index: telling the model the query "failed"
				// here is what sent it to grep for the rest of the session.
				if isIndexRefreshContention(msg) {
					return fantasy.NewTextErrorResponse(fmt.Sprintf(
						"The code index is refreshing and could not answer %q yet. Retry the same query — "+
							"do not switch to a text search, the index is the cheaper and cited path.", cmd,
					)), nil
				}
				// Surface the failure without ending the turn so the model
				// can refine the query against the index.
				return fantasy.NewTextErrorResponse(fmt.Sprintf(
					"prowl-agent %s failed: %s", cmd, prowlAgentClamp(msg, 4000),
				)), nil
			}
			if out == "" {
				out = "(prowl-agent returned no output)"
			}
			out, err := prowlAgentOutput(out, filepath.Join(config.GlobalCacheDir(), "query-results"))
			if err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("Query succeeded, but preserving its complete output failed: %v. Retry with a bounded search or a narrower query.", err)), nil
			}
			return fantasy.NewTextResponse(out), nil
		},
	)
}

// indexRefreshBusy matches the engine's refresh-lock contention error. A
// query arriving while an incremental refresh holds the project lock fails
// with this rather than with anything wrong in the query itself.
const indexRefreshBusy = "project refresh lock was not acquired"

// isIndexRefreshContention reports whether a failure is the transient
// refresh-lock case rather than a real query error.
func isIndexRefreshContention(msg string) bool {
	return strings.Contains(msg, indexRefreshBusy)
}

// runProwlAgentWithRefreshRetry runs a query, waiting out a refresh that holds
// the project lock. The engine's own lock attempt is 25ms, short enough that a
// routine incremental refresh can lose a race the model then reads as "the
// index is broken". Retrying here keeps a transient collision invisible.
func runProwlAgentWithRefreshRetry(ctx context.Context, opts *config.ProwlAgentOptions, workingDir string, args []string) (string, string, error) {
	const attempts = 3
	backoff := 150 * time.Millisecond
	var stdout, stderr string
	var err error
	for attempt := range attempts {
		stdout, stderr, err = prowlagent.Run(ctx, opts, workingDir, args...)
		if err == nil {
			return stdout, stderr, nil
		}
		detail := stderr
		if strings.TrimSpace(detail) == "" {
			detail = err.Error()
		}
		if !isIndexRefreshContention(detail) || attempt == attempts-1 {
			return stdout, stderr, err
		}
		select {
		case <-ctx.Done():
			return stdout, stderr, err
		case <-time.After(backoff):
		}
		backoff *= 2
	}
	return stdout, stderr, err
}

// prowlAgentHasFormatFlag reports whether the caller already selected an
// output format, so the tool does not override it with the default toon.
func prowlAgentHasFormatFlag(args []string) bool {
	for _, a := range args {
		if a == "--json" || a == "--format" || strings.HasPrefix(a, "--format=") {
			return true
		}
	}
	return false
}

// prowlAgentClamp bounds diagnostic text, including the truncation marker.
func prowlAgentClamp(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	if maxBytes <= 0 {
		return ""
	}
	marker := "\n… (truncated)"
	if len(marker) > maxBytes {
		marker = ""
	}
	cut := maxBytes - len(marker)
	for cut > 0 && !utf8RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + marker
}

// utf8RuneStart reports whether b is the first byte of a UTF-8 rune.
func utf8RuneStart(b byte) bool {
	return b&0xC0 != 0x80
}

// prowlAgentOutput preserves oversized answers instead of returning broken JSON
// or silently losing evidence. Snapshots stay in the user's private cache.
func prowlAgentOutput(out, cacheDir string) (string, error) {
	if len(out) <= maxProwlAgentOutput {
		return out, nil
	}
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(cacheDir, "query-*.txt")
	if err != nil {
		return "", err
	}
	_, writeErr := file.WriteString(out)
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		_ = os.Remove(file.Name())
		return "", err
	}
	encoded, err := json.Marshal(struct {
		Snapshot string `json:"snapshot"`
		Bytes    int    `json:"bytes"`
		Next     string `json:"next"`
	}{
		Snapshot: file.Name(), Bytes: len(out),
		Next: "The complete answer exceeds the tool budget. Read this snapshot with view in bounded line ranges, or extract selected JSON fields with bash.",
	})
	return string(encoded), err
}
