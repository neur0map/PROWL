package tools

import (
	"path/filepath"
	"slices"
	"strings"

	"github.com/neur0map/prowl/internal/shell"
)

// Prowl tells the model three times — in the bash description, in the grep
// description, and in a post-tool reminder — to route structural questions to
// the index and literal text to the grep tool. It still shells out to
// `grep -rn` over a subtree, sometimes in the same turn as a prowl_agent call
// that already answered the question. Prose it can read and then ignore is not
// a routing rule; this refuses the call and names the tool to use instead.
//
// Deliberately narrow, because a refusal that blocks real work teaches the
// model to fight the tool: a pipeline filter (`go test ./... | grep FAIL`), a
// single-file check, and a find with predicates glob cannot express all still
// run.

// treeSearchUtilities are the tools whose whole purpose is walking a source
// tree. The grep family also has legitimate stdin and single-file uses, which
// the checks below preserve.
var treeSearchUtilities = []string{
	"grep", "egrep", "fgrep", "rg", "ripgrep", "ag", "ack", "ack-grep", "ucg",
}

// walkUtilities enumerate paths rather than search contents.
var walkUtilities = []string{"find", "fd", "fdfind"}

// recursiveByDefault searchers need no -r: given no path they scan the whole
// tree, so for them an absent path operand is the widest search there is.
var recursiveByDefault = []string{"rg", "ripgrep", "ag", "ack", "ack-grep", "ucg"}

// globPredicates are the find predicates the glob tool already covers. A find
// carrying anything else (-newer, -delete, -exec, -size) is doing work no
// built-in tool does and is left alone.
var globPredicates = []string{"-name", "-iname", "-path", "-ipath", "-type", "-maxdepth", "-mindepth"}

const routeToIndex = "Prowl routes searches, so this command was not run. " +
	"For code structure or meaning — where a symbol is defined, what it does, " +
	"who calls it, a file's shape, a change's blast radius — call prowl_agent " +
	"(search, find, def, outline, references, impact): it answers from a cited " +
	"index in one call. For exact text call the grep tool with a path, and for " +
	"filenames call glob. Piping output into grep still works, as does grepping " +
	"a single file."

// SearchRouterGuard refuses a shell tree-search whose job belongs to
// prowl_agent, the grep tool or glob.
func SearchRouterGuard(args []string) string {
	if len(args) == 0 {
		return ""
	}
	utility := filepath.Base(args[0])
	operands := args[1:]

	// `git grep` is the same tree scan wearing a different name, and like
	// ripgrep it covers the whole worktree when given no path.
	if utility == "git" && len(operands) > 0 && operands[0] == "grep" {
		utility, operands = "rg", operands[1:]
	}

	switch {
	case slices.Contains(treeSearchUtilities, utility):
		if !scansATree(utility, operands) {
			return ""
		}
	case slices.Contains(walkUtilities, utility):
		if !onlyMatchesNames(operands) {
			return ""
		}
	default:
		return ""
	}
	return routeToIndex
}

// scansATree reports whether a content search covers a directory rather than
// reading stdin or a named file. ripgrep recurses by default, so for it the
// absence of operands is the widest scan there is.
func scansATree(utility string, operands []string) bool {
	recursive := false
	var words []string
	for _, operand := range operands {
		if strings.HasPrefix(operand, "-") {
			if isRecursiveFlag(operand) {
				recursive = true
			}
			continue
		}
		words = append(words, operand)
	}
	// Every one of these takes the pattern first; whatever follows is a path.
	paths := words
	if len(paths) > 0 {
		paths = paths[1:]
	}

	if recursive {
		return true
	}
	if len(paths) == 0 {
		// No path means stdin for grep — a pipeline filter — but the whole
		// tree for the searchers that recurse without being asked.
		return slices.Contains(recursiveByDefault, utility)
	}
	for _, path := range paths {
		if looksLikeADirectory(path) {
			return true
		}
	}
	return false
}

func isRecursiveFlag(flag string) bool {
	switch flag {
	case "-r", "-R", "--recursive", "--dereference-recursive":
		return true
	}
	// Bundled short flags, as in `grep -rn`.
	return !strings.HasPrefix(flag, "--") &&
		(strings.ContainsRune(flag, 'r') || strings.ContainsRune(flag, 'R'))
}

// looksLikeADirectory is decided from the operand's shape, not the filesystem:
// the guard runs before execution and must not be influenced by whether a
// path happens to exist right now.
func looksLikeADirectory(path string) bool {
	trimmed := strings.Trim(path, `"'`)
	switch trimmed {
	case ".", "..", "./", "../", "/", "*":
		return true
	}
	if strings.HasSuffix(trimmed, "/") {
		return true
	}
	// A glob over a tree ("**/*.go") is a tree scan; a plain filename is not.
	if strings.Contains(trimmed, "*") {
		return true
	}
	return filepath.Ext(trimmed) == ""
}

// onlyMatchesNames reports whether a find/fd invocation does nothing glob
// cannot: match by name or path within a directory.
func onlyMatchesNames(operands []string) bool {
	sawPredicate := false
	for _, operand := range operands {
		if !strings.HasPrefix(operand, "-") {
			continue
		}
		if !slices.Contains(globPredicates, operand) {
			return false
		}
		sawPredicate = true
	}
	// A bare `find dir` lists a tree, which glob also does.
	return sawPredicate || len(operands) <= 1
}

// SearchGuards is what the bash tool installs.
func SearchGuards() []shell.Guard { return []shell.Guard{SearchRouterGuard} }
