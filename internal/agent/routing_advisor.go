package agent

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"

	"charm.land/fantasy"
	"github.com/tidwall/gjson"

	"github.com/neur0map/prowl/internal/agent/tools"
)

// The code index is part of Prowl, not an optional skill the model may
const routingReminder = "Prowl routing reminder: for structural repository questions — where code is, " +
	"what a symbol does, who calls it, a file's shape, or a change's blast radius — the built-in " +
	"prowl_agent index (search, find, def, outline, references, impact) answers in one cited call " +
	"instead of scanning many files. Reserve grep for an exact literal or regex match and glob for " +
	"filename patterns; follow a citation with def, outline, or peek rather than opening whole files."

// alreadyAsked sharpens the advisory when the index was consulted in this
// session and the model searched manually anyway — the reported failure was
// exactly this, both in one turn.
const alreadyAsked = "Prowl routing reminder: you have already queried the prowl_agent index " +
	"this session, and this search covers ground it indexes. When a result looks short, " +
	"follow its citations with def, outline, references or peek, or re-query with another " +
	"term — a manual scan of the same code returns no citations and costs far more tokens. " +
	"Reserve grep for an exact literal and glob for filename patterns."

// structuralPattern matches a search pattern that is approximating a symbol
// question: an alternation of spellings, a wildcard, or a declaration keyword.
// Those are `find`, `def` and `references` queries written as text search, and
// they are misrouted whether or not the path is bounded — the reported case
// was an alternation of six spellings of one command name, scoped to a
// subdirectory, which no path-width rule would ever notice.
var structuralPattern = regexp.MustCompile(
	`\\\||\||\.\*|\b(func|type|class|struct|interface|impl|def)\b`)

// treeSearch matches the shell utilities that fan out across a source tree.
// The structural questions they approximate are the ones the index answers in
// a single cited call.
var treeSearch = regexp.MustCompile(`(^|[|&;\s])(rg|grep|egrep|fgrep|ag|ack|find|fd|fdfind)\b`)

// wordSplit pulls operands off a command segment, keeping quoted runs whole.
var wordSplit = regexp.MustCompile(`"[^"]*"|'[^']*'|[^\s]+`)

// routingAdvisor appends the reminder after a repository-wide manual search.
type routingAdvisor struct {
	// remindEvery is the number of broad searches between reminders. The
	// first broad search of a session always gets one.
	remindEvery int64
	seen        atomic.Int64

	// workingDir lets an absolute path operand be recognised as the project
	// root. Without it `rg foo /home/me/project` reads as a bounded search
	// when it is in fact the widest scan there is.
	workingDir string

	// indexCalls counts prowl_agent calls, which is what distinguishes "has
	// not tried the index" from "tried it and scanned anyway".
	indexCalls atomic.Int64
}

func newRoutingAdvisor(workingDir string) *routingAdvisor {
	return &routingAdvisor{remindEvery: 4, workingDir: workingDir}
}

// shouldRemind reports whether this search earns a reminder.
func (a *routingAdvisor) shouldRemind() bool {
	n := a.seen.Add(1)
	return n == 1 || n%a.remindEvery == 1
}

// reminder picks the advisory that fits what the model has already done.
func (a *routingAdvisor) reminder() string {
	if a.indexCalls.Load() > 0 {
		return alreadyAsked
	}
	return routingReminder
}

// routedTool is the post-tool stage: it compacts what a tool returned and,
// after a repository-wide manual search, appends the routing advisory.
type routedTool struct {
	inner fantasy.AgentTool
	// advisor may be nil, which disables the advisory while leaving output
	// compaction in place.
	advisor *routingAdvisor
	// skills may be nil, which disables the skill advisory. It is separate
	// because the two fire on different calls: one on a broad search, the
	// other on touching a file a skill claims.
	skills *skillAdvisor
}

// wrapToolsWithRouting decorates every tool with the post-tool stage. Unlike
func wrapToolsWithRouting(tools []fantasy.AgentTool, advisor *routingAdvisor, skillHints *skillAdvisor) []fantasy.AgentTool {
	out := make([]fantasy.AgentTool, len(tools))
	for i, tool := range tools {
		out[i] = &routedTool{inner: tool, advisor: advisor, skills: skillHints}
	}
	return out
}

func (r *routedTool) Info() fantasy.ToolInfo { return r.inner.Info() }

func (r *routedTool) ProviderOptions() fantasy.ProviderOptions { return r.inner.ProviderOptions() }

func (r *routedTool) SetProviderOptions(opts fantasy.ProviderOptions) {
	r.inner.SetProviderOptions(opts)
}

func (r *routedTool) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	resp, err := r.inner.Run(ctx, call)
	if err != nil {
		return resp, err
	}
	// Compaction runs before the advisory so the reminder itself is never
	// dedup'd or trimmed, and it runs on errors too: a failed command's
	// output is frequently the noisiest thing in a session.
	if compacted, changed := compactToolOutput(call.Name, resp.Content); changed {
		resp.Content = compacted
	}
	// A skill claiming the file the model just edited is worth saying once,
	// even when the call itself failed: the skill is frequently why it did.
	if hint := r.skills.advise(call.Name, call.Input); hint != "" {
		resp.Content += "\n\n" + hint
	}

	if r.advisor == nil {
		return resp, nil
	}
	if call.Name == tools.ProwlAgentToolName {
		r.advisor.indexCalls.Add(1)
		return resp, nil
	}
	// A failed search says nothing about routing, and an empty result is
	// already covered by the prompt's "absence is not proof" rule.
	if resp.IsError || resp.Content == "" {
		return resp, nil
	}
	if !isMisroutedSearch(call.Name, call.Input, r.advisor.workingDir) {
		return resp, nil
	}
	if !r.advisor.shouldRemind() {
		return resp, nil
	}
	resp.Content += "\n\n" + r.advisor.reminder()
	return resp, nil
}

// isMisroutedSearch reports whether a completed call was a manual search the
// index answers better: one that covered the whole project, or one whose
// pattern was a symbol question in regex clothing.
func isMisroutedSearch(toolName, input, workingDir string) bool {
	switch toolName {
	case "grep":
		if structuralPattern.MatchString(gjson.Get(input, "pattern").String()) {
			return true
		}
		return repoWidePath(gjson.Get(input, "path").String(), workingDir)
	case "glob":
		return repoWidePath(gjson.Get(input, "path").String(), workingDir)
	case "bash":
		command := gjson.Get(input, "command").String()
		return treeSearch.MatchString(command) && bashIsRepoWide(command, workingDir)
	default:
		return false
	}
}

// repoWidePath reports whether a search path covers the whole project. An
func repoWidePath(path, workingDir string) bool {
	trimmed := strings.Trim(strings.TrimSpace(path), `"'`)
	switch trimmed {
	case "", ".", "./", "/":
		return true
	}
	if workingDir == "" || !filepath.IsAbs(trimmed) {
		return false
	}
	root := filepath.Clean(workingDir)
	candidate := filepath.Clean(trimmed)
	if candidate == root {
		return true
	}
	// An ancestor of the project root is wider still.
	rel, err := filepath.Rel(candidate, root)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// bashIsRepoWide reports whether a shell search fans across the tree.
func bashIsRepoWide(command, workingDir string) bool {
	for _, segment := range strings.FieldsFunc(command, func(r rune) bool {
		return r == '|' || r == '&' || r == ';' || r == '\n'
	}) {
		match := treeSearch.FindStringSubmatchIndex(segment)
		if match == nil {
			continue
		}
		utility := segment[match[4]:match[5]]
		var words []string
		for _, word := range wordSplit.FindAllString(segment[match[1]:], -1) {
			if !strings.HasPrefix(word, "-") {
				words = append(words, word)
			}
		}
		// find/fd take a search root as their first operand; the grep family
		// takes the pattern first, so its roots start one word later.
		operands := words
		if utility != "find" {
			if len(operands) > 0 {
				operands = operands[1:]
			}
		}
		if len(operands) == 0 {
			return true
		}
		for _, path := range operands {
			if repoWidePath(path, workingDir) {
				return true
			}
		}
	}
	return false
}
