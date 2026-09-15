package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/neur0map/prowl/internal/config"
	"github.com/neur0map/prowl/internal/prowlagent"
)

var knowledgeCmd = &cobra.Command{
	Use:   "knowledge",
	Short: "Review the lessons the agent recorded",
	Long: "The agent records durable lessons on its own, but nothing it records becomes project " +
		"knowledge until you accept it. These commands are that review: list what is pending, read " +
		"one, edit it, accept it, reject it, or delete something already accepted.",
	Example: `
# What is waiting for review
prowl knowledge

# Read one pending lesson
prowl knowledge show a392fac1

# Edit before accepting
prowl knowledge edit a392fac1

# Accept or reject
prowl knowledge accept a392fac1
prowl knowledge reject a392fac1

# Remove something already accepted
prowl knowledge list --accepted
prowl knowledge delete decisions/index-identity.md
`,
	RunE: runKnowledgeList,
}

var (
	knowledgeListCmd = &cobra.Command{
		Use:   "list",
		Short: "List pending proposals, or accepted documents with --accepted",
		RunE:  runKnowledgeList,
	}
	knowledgeShowCmd = &cobra.Command{
		Use:   "show <id-or-path>",
		Short: "Print one pending proposal or accepted document",
		Args:  cobra.ExactArgs(1),
		RunE:  runKnowledgeShow,
	}
	knowledgeEditCmd = &cobra.Command{
		Use:   "edit <id>",
		Short: "Open a pending proposal in $EDITOR before deciding on it",
		Args:  cobra.ExactArgs(1),
		RunE:  runKnowledgeEdit,
	}
	knowledgeAcceptCmd = &cobra.Command{
		Use:   "accept <id>",
		Short: "Accept a pending proposal into project knowledge",
		Args:  cobra.ExactArgs(1),
		RunE:  runKnowledgeDecision("accept"),
	}
	knowledgeRejectCmd = &cobra.Command{
		Use:   "reject <id>",
		Short: "Reject a pending proposal",
		Args:  cobra.ExactArgs(1),
		RunE:  runKnowledgeDecision("reject"),
	}
	knowledgeDeleteCmd = &cobra.Command{
		Use:   "delete <path>",
		Short: "Delete an accepted knowledge document",
		Args:  cobra.ExactArgs(1),
		RunE:  runKnowledgeDelete,
	}
)

func init() {
	knowledgeListCmd.Flags().Bool("accepted", false, "List accepted documents instead of pending proposals")
	knowledgeCmd.Flags().Bool("accepted", false, "List accepted documents instead of pending proposals")
	knowledgeCmd.AddCommand(
		knowledgeListCmd,
		knowledgeShowCmd,
		knowledgeEditCmd,
		knowledgeAcceptCmd,
		knowledgeRejectCmd,
		knowledgeDeleteCmd,
	)
}

// knowledgeContext resolves the working directory and prowl-agent options
// every knowledge command needs.
func knowledgeContext(cmd *cobra.Command) (string, *config.ProwlAgentOptions, error) {
	cwd, err := ResolveCwd(cmd)
	if err != nil {
		return "", nil, err
	}
	store, err := config.Init(cwd, "", false)
	if err != nil {
		return "", nil, err
	}
	return cwd, store.Config().Options.GetProwlAgent(), nil
}

func runKnowledgeList(cmd *cobra.Command, _ []string) error {
	cwd, opts, err := knowledgeContext(cmd)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()

	if accepted, _ := cmd.Flags().GetBool("accepted"); accepted {
		docs, err := prowlagent.ListKnowledge(cmd.Context(), opts, cwd)
		if err != nil {
			return err
		}
		if len(docs) == 0 {
			fmt.Fprintln(out, "No accepted knowledge yet.")
			return nil
		}
		for _, d := range docs {
			fmt.Fprintf(out, "%-12s %-52s %s\n", d.Type, d.Title, d.Path)
		}
		fmt.Fprintf(out, "\n%d accepted document(s).\n", len(docs))
		return nil
	}

	proposals, err := prowlagent.ListProposals(cmd.Context(), opts, cwd)
	if err != nil {
		if errors.Is(err, prowlagent.ErrNoBundle) {
			fmt.Fprintln(out, "No knowledge bundle in this project yet; nothing has been recorded.")
			return nil
		}
		return err
	}
	if len(proposals) == 0 {
		fmt.Fprintln(out, "Nothing pending review.")
		return nil
	}
	for _, p := range proposals {
		fmt.Fprintf(out, "%-22s %-52s %s\n", p.ID, truncate(p.Title, 52), p.TargetPath)
	}
	fmt.Fprintf(out, "\n%d lesson(s) awaiting review. Accept with `prowl knowledge accept <id>`.\n", len(proposals))
	return nil
}

func runKnowledgeShow(cmd *cobra.Command, args []string) error {
	cwd, opts, err := knowledgeContext(cmd)
	if err != nil {
		return err
	}
	target := args[0]

	proposals, listErr := prowlagent.ListProposals(cmd.Context(), opts, cwd)
	if listErr == nil {
		for _, p := range proposals {
			if strings.HasPrefix(p.ID, target) {
				fmt.Fprintf(cmd.OutOrStdout(), "# %s\n\nid:     %s\ntarget: %s\nstatus: %s\n\n%s\n",
					p.Title, p.ID, p.TargetPath, p.Status, p.Body)
				return nil
			}
		}
	}

	// Not a pending id, so treat it as an accepted document path.
	body, _, err := prowlagent.Run(cmd.Context(), opts, cwd, "knowledge", "show", target)
	if err != nil {
		return fmt.Errorf("no pending proposal or accepted document matches %q", target)
	}
	fmt.Fprintln(cmd.OutOrStdout(), strings.TrimRight(body, "\n"))
	return nil
}

// runKnowledgeEdit opens the candidate in $EDITOR. Editing before accepting is
// the difference between a review that can only say yes or no and one that can
// fix a lesson's wording or scope.
func runKnowledgeEdit(cmd *cobra.Command, args []string) error {
	cwd, opts, err := knowledgeContext(cmd)
	if err != nil {
		return err
	}
	proposals, err := prowlagent.ListProposals(cmd.Context(), opts, cwd)
	if err != nil {
		return err
	}
	var match *prowlagent.Proposal
	for i := range proposals {
		if strings.HasPrefix(proposals[i].ID, args[0]) {
			match = &proposals[i]
			break
		}
	}
	if match == nil {
		return fmt.Errorf("no pending proposal matches %q", args[0])
	}
	candidate, err := prowlagent.CandidatePath(cwd, match.ID)
	if err != nil {
		return err
	}

	editor := strings.TrimSpace(os.Getenv("VISUAL"))
	if editor == "" {
		editor = strings.TrimSpace(os.Getenv("EDITOR"))
	}
	if editor == "" {
		fmt.Fprintf(cmd.OutOrStdout(),
			"No $EDITOR set. Edit this file, then accept it:\n  %s\n", candidate)
		return nil
	}

	editCmd := exec.CommandContext(cmd.Context(), editor, candidate)
	editCmd.Stdin, editCmd.Stdout, editCmd.Stderr = os.Stdin, cmd.OutOrStdout(), cmd.ErrOrStderr()
	if err := editCmd.Run(); err != nil {
		return fmt.Errorf("editor exited with an error: %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Edited %s. Accept it with `prowl knowledge accept %s`.\n",
		match.ID, match.ID[:min(8, len(match.ID))])
	return nil
}

func runKnowledgeDecision(action string) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		cwd, opts, err := knowledgeContext(cmd)
		if err != nil {
			return err
		}
		id, err := resolveProposalID(cmd, opts, cwd, args[0])
		if err != nil {
			return err
		}
		switch action {
		case "accept":
			err = prowlagent.AcceptProposal(cmd.Context(), opts, cwd, id)
		default:
			err = prowlagent.RejectProposal(cmd.Context(), opts, cwd, id)
		}
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%sed %s.\n", strings.Title(action), id) //nolint:staticcheck
		return nil
	}
}

func runKnowledgeDelete(cmd *cobra.Command, args []string) error {
	cwd, opts, err := knowledgeContext(cmd)
	if err != nil {
		return err
	}
	if err := prowlagent.DeleteAccepted(cmd.Context(), opts, cwd, args[0]); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Deleted %s.\n", args[0])
	return nil
}

// resolveProposalID accepts an unambiguous id prefix, so a user can type the
// first few characters they see in the list.
func resolveProposalID(cmd *cobra.Command, opts *config.ProwlAgentOptions, cwd, prefix string) (string, error) {
	proposals, err := prowlagent.ListProposals(cmd.Context(), opts, cwd)
	if err != nil {
		return "", err
	}
	var matches []string
	for _, p := range proposals {
		if strings.HasPrefix(p.ID, prefix) {
			matches = append(matches, p.ID)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return "", fmt.Errorf("no pending proposal matches %q", prefix)
	default:
		return "", fmt.Errorf("%q matches %d proposals; use a longer id", prefix, len(matches))
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}
