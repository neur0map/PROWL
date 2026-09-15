// Package embedded exposes prowl-agent's full CLI command tree as an in-process
// entry point so a host program (Prowl) can run it natively — no subprocess,
// no external binary — while preserving identical command behavior and output.
//
// prowl-agent's commands locate the repository through the process working
// directory. To stay correct when the host serves multiple workspaces, Execute
// serializes calls and swaps the working directory per invocation, restoring it
// afterward. Queries are sub-second, so the serialization cost is negligible;
// callers that need true parallelism across workspaces should move to the
// engine-level API (a later step).
package embedded

import (
	"context"
	"io"

	"github.com/spf13/cobra"

	"github.com/neur0map/prowl/internal/paengine/internal/cli"
	"github.com/neur0map/prowl/internal/paengine/internal/workspace"
)

// Version is the prowl-agent version reported by the embedded command tree. The
// host may override it at startup to match the bundled build.
var Version = "embedded"

// executionGate serializes engine commands. The workspace resolution base is
// process-global, so two concurrent calls against different directories would
// otherwise race. Calls are short and the host keeps them off the interactive
// path; the gate costs less than threading a base through every command.
var executionGate = make(chan struct{}, 1)

// Execute runs a prowl-agent command in-process against workdir and writes the
// command's output to stdout/stderr. args is the argument vector without the
// program name (e.g. []string{"find", "NewGui", "--format", "toon"}). A nil or
// empty workdir runs against the current process directory.
func Execute(ctx context.Context, workdir string, args []string, stdout, stderr io.Writer) error {
	select {
	case executionGate <- struct{}{}:
		defer func() { <-executionGate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	if workdir != "" {
		// Anchor relative lookups without os.Chdir: the process working
		// directory is shared with every other goroutine in the harness.
		defer workspace.SetBase(workdir)()
	}

	root := &cobra.Command{
		Use:           "prowl-agent",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       Version,
	}
	root.CompletionOptions.HiddenDefaultCmd = true
	cli.Register(root, Version, "")
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	return root.ExecuteContext(ctx)
}
