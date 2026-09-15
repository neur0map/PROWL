package commands

import (
	"fmt"
	"strings"
	"unicode"
)

type SlashCommand struct {
	Name        string
	Description string
	Group       string
	Arguments   string
	NeedsInput  bool
}

// BuiltinSlashCommands reserves these names before file and skill commands.
// Completion consumes the full catalog; the Options palette filters some out.
var BuiltinSlashCommands = []SlashCommand{
	{Name: "goal set", Description: "Start from a clear objective", Group: "Start a goal", Arguments: "<objective>", NeedsInput: true},
	{Name: "guided-goal", Description: "Shape the objective together", Group: "Start a goal", Arguments: "[idea]"},
	{Name: "goal show", Description: "See progress, usage and budget", Group: "Current goal"},
	{Name: "goal pause", Description: "Stop working; keep the goal", Group: "Current goal"},
	{Name: "goal resume", Description: "Pick up where you left off", Group: "Current goal"},
	{Name: "goal budget", Description: "Set a token cap or remove it", Group: "Current goal", Arguments: "<tokens|off>", NeedsInput: true},
	{Name: "goal drop", Description: "Stop and remove the goal", Group: "Current goal"},
	{Name: "green", Description: "Fix CI until the latest commit passes", Group: "Workflows", Arguments: "[constraints]"},
	{Name: "gateway", Description: "Open the model gateway dashboard", Group: "More"},
	{Name: "help", Description: "Browse the Commands modal", Group: "More"},
}

func SplitSlash(input string) (name, args string, ok bool) {
	input = strings.TrimSpace(input)
	if !strings.HasPrefix(input, "/") || strings.HasPrefix(input, "//") {
		return "", "", false
	}
	input = input[1:]
	if i := strings.IndexFunc(input, unicode.IsSpace); i >= 0 {
		return input[:i], strings.TrimSpace(input[i:]), i > 0
	}
	return input, "", input != ""
}

// FindSlashCustom accepts a canonical ID or an unambiguous short name. Builtin
// precedence is handled by the caller; colliding user/project names require an
// explicit qualified ID instead of depending on filesystem traversal order.
func FindSlashCustom(name string, commands []CustomCommand) (*CustomCommand, error) {
	for i := range commands {
		if commands[i].ID == name {
			return &commands[i], nil
		}
	}
	var match *CustomCommand
	for i := range commands {
		cmd := &commands[i]
		short := cmd.ID[strings.LastIndex(cmd.ID, ":")+1:]
		if cmd.Skill != nil {
			short = cmd.Skill.Name
		}
		if short != name {
			continue
		}
		if match != nil {
			return nil, fmt.Errorf("ambiguous command /%s; use /%s or /%s", name, match.ID, cmd.ID)
		}
		match = cmd
	}
	return match, nil
}

// GreenPrompt deliberately resolves Git metadata in the agent's working
// directory, not in the TUI client (which can be connected to another host).
const GreenPrompt = `/green: keep working on CI for the current branch until the workflows for the latest HEAD are green. This is not a request for a single fix attempt.

First inspect the repository's current branch, HEAD and any tag pointing at HEAD. Determine the push remote from branch.<branch>.pushRemote, then branch.<branch>.remote, falling back to origin. Read the repository's contribution and release rules. Do not create a pull request unless the user separately asks for one.

Use available GitHub tools or the gh CLI to inspect and watch workflow runs. Runs for the current HEAD commit, not an older successful commit or a branch badge, are the source of truth. Wait for the relevant runs to finish; inspect failed job logs; fix the root cause with the smallest appropriate change; run useful local checks; commit and push through the normal permission controls; then watch the new HEAD. Repeat until the latest HEAD's relevant workflow runs all succeed. No matching runs is not evidence of success: wait for the expected trigger or explain the blocker. Never make CI green by disabling checks, deleting meaningful coverage or hiding failures.

If HEAD originally had a tag and the workflow requires moving it, preserve that release invariant: once policy permits retagging, move it to the new HEAD and push the branch and tag together in ONE atomic transaction: git push --atomic <remote> <branch> +refs/tags/<tag>. Quote all actual ref names. Never push the branch first and the tag later. Do not force-push a branch or overwrite a protected/immutable release tag; stop and ask if repository policy forbids the required operation.

Finish only after verifying that the latest HEAD's workflow runs succeeded and any applicable tag points to that same commit. Report the verified commit and run links. If credentials, permissions, a risky release operation or an external service blocks progress, state the exact blocker rather than claiming CI is green.`
