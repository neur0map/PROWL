package cli

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/neur0map/prowl/internal/paengine/internal/application"
	contextpacket "github.com/neur0map/prowl/internal/paengine/internal/context"
	"github.com/neur0map/prowl/internal/paengine/internal/store"
)

func newContextCmd() *cobra.Command {
	command := &cobra.Command{Use: "context", Short: "Build bounded, cited context packets"}
	command.AddCommand(newContextSearchCmd(), newContextGetCmd(), newContextTracesCmd())
	return command
}

func newContextSearchCmd() *cobra.Command {
	var mode string
	var budgetTokens, budgetBytes int
	var asJSON bool
	command := &cobra.Command{
		Use:   "search <question>",
		Short: "Retrieve bounded context for a question",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			service, closeStore, err := openContextService(command.Context())
			if err != nil {
				return err
			}
			defer closeStore()
			packet, err := service.Search(command.Context(), contextpacket.Request{Question: joinArgs(args), Mode: contextpacket.Mode(mode), BudgetTokens: budgetTokens, BudgetBytes: budgetBytes})
			if err != nil {
				return err
			}
			return writeContextPacket(command, packet, asJSON)
		},
	}
	addContextFlags(command, &mode, &budgetTokens, &budgetBytes, &asJSON)
	return command
}

func newContextGetCmd() *cobra.Command {
	var mode string
	var budgetTokens, budgetBytes int
	var asJSON bool
	command := &cobra.Command{
		Use:   "get <id>...",
		Short: "Fetch selected context IDs within a budget",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			service, closeStore, err := openContextService(command.Context())
			if err != nil {
				return err
			}
			defer closeStore()
			packet, err := service.Get(command.Context(), contextpacket.Request{IDs: args, Mode: contextpacket.Mode(mode), BudgetTokens: budgetTokens, BudgetBytes: budgetBytes})
			if err != nil {
				return err
			}
			return writeContextPacket(command, packet, asJSON)
		},
	}
	addContextFlags(command, &mode, &budgetTokens, &budgetBytes, &asJSON)
	return command
}

func newContextTracesCmd() *cobra.Command {
	var limit int
	var asJSON bool
	command := &cobra.Command{
		Use:   "traces",
		Short: "Inspect privacy-safe context execution metadata",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			project, err := application.OpenProject(command.Context(), ".", application.Options{})
			if err != nil {
				return err
			}
			defer project.Close()
			release, err := project.ReadGuard(command.Context())
			if err != nil {
				return err
			}
			defer release()
			if err := project.Store.RequirePublishedGeneration(); err != nil {
				return err
			}
			runs, err := project.Store.ListContextRuns(limit)
			if err != nil {
				return err
			}
			if runs == nil {
				runs = []store.ContextRun{}
			}
			if asJSON {
				encoded, err := json.MarshalIndent(runs, "", "  ")
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(command.OutOrStdout(), string(encoded))
				return err
			}
			if len(runs) == 0 {
				_, err = fmt.Fprintln(command.OutOrStdout(), "No context traces.")
				return err
			}
			for _, run := range runs {
				hash := run.QueryHash
				if len(hash) > 12 {
					hash = hash[:12]
				}
				if _, err := fmt.Fprintf(command.OutOrStdout(), "%s %s status=%s mode=%s query_hash=%s cost=%dt/%dB selected=%s omissions=%s timing=%s\n", run.CreatedAt, run.ID, run.Status, run.Mode, hash, run.EstimatedTokens, run.EstimatedBytes, run.SelectedIDsJSON, run.OmissionsJSON, run.TimingsJSON); err != nil {
					return err
				}
			}
			return nil
		},
	}
	command.Flags().IntVar(&limit, "limit", 20, "maximum traces to show")
	command.Flags().BoolVar(&asJSON, "json", false, "emit stable privacy-safe JSON")
	return command
}

func addContextFlags(command *cobra.Command, mode *string, budgetTokens, budgetBytes *int, asJSON *bool) {
	command.Flags().StringVar(mode, "mode", string(contextpacket.ModeCompact), "detail mode: compact, standard, or full")
	command.Flags().IntVar(budgetTokens, "budget-tokens", 1800, "estimated token budget")
	command.Flags().IntVar(budgetBytes, "budget-bytes", 0, "byte budget (enforced with token budget when both are set)")
	command.Flags().BoolVar(asJSON, "json", false, "emit the stable context packet as JSON")
}

func openContextService(ctx context.Context) (*contextpacket.Service, func(), error) {
	project, err := application.OpenProject(ctx, "", application.Options{EnableAI: true, InferencerProvider: maybeInferencer})
	if err != nil {
		return nil, nil, err
	}
	return project.Context, func() { _ = project.Close() }, nil
}

func writeContextPacket(command *cobra.Command, packet contextpacket.Packet, asJSON bool) error {
	format, err := resolveFormat(command, command.OutOrStdout())
	if err != nil {
		return err
	}
	if asJSON {
		format = formatJSON
	}
	packet = contextpacket.CanonicalProjection(packet)
	encoded, err := contextpacket.EncodeBounded(&packet, nil, func(packet contextpacket.Packet) ([]byte, error) {
		text, err := formatValue(packet, format)
		return []byte(text + "\n"), err
	})
	if err != nil {
		return err
	}
	_, err = command.OutOrStdout().Write(encoded)
	return err
}
