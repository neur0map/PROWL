package cmd

import (
	"fmt"
	"time"

	"github.com/neur0map/prowl/internal/config"
	"github.com/neur0map/prowl/internal/prowlagent"
	"github.com/spf13/cobra"
)

var indexCmd = &cobra.Command{
	Use:   "index",
	Short: "Build or refresh the prowl-agent code index for this project",
	Long: "Build or refresh the prowl-agent code index for the current project, " +
		"refresh the AGENTS.md map, and embed every chunk so semantic search is " +
		"whole. Prowl keeps the index warm the same way but off the interactive " +
		"path; run this to force a complete build now or to see the result.",
	RunE: func(cmd *cobra.Command, _ []string) error {
		debug, _ := cmd.Flags().GetBool("debug")
		dataDir, _ := cmd.Flags().GetString("data-dir")

		cwd, err := ResolveCwd(cmd)
		if err != nil {
			return err
		}
		store, err := config.Init(cwd, dataDir, debug)
		if err != nil {
			return err
		}

		opts := store.Config().Options.GetProwlAgent()
		if !opts.IsEnabled() {
			return fmt.Errorf("prowl-agent integration is disabled (set options.prowl_agent.enabled=true)")
		}
		bin, ok := prowlagent.Resolve(opts)
		if !ok {
			return fmt.Errorf("prowl-agent binary not found; install it from https://github.com/neur0map/prowl-agent or set options.prowl_agent.path")
		}

		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "Indexing %s with %s …\n", store.WorkingDir(), bin)
		start := time.Now()
		if err := prowlagent.EnsureIndex(cmd.Context(), opts, store.WorkingDir()); err != nil {
			return err
		}
		if st, err := prowlagent.QueryStatus(cmd.Context(), opts, store.WorkingDir()); err == nil {
			fmt.Fprintf(out, "Indexed %d files, %d symbols in %s.\n",
				st.Counts.Files, st.Counts.Symbols, time.Since(start).Round(time.Millisecond))
			// Semantic search is the slow half of a first build, so report how far
			// it got rather than leaving the extra minutes unexplained.
			if st.Semantic.Chunks > 0 {
				fmt.Fprintf(out, "Embedded %d of %d chunks for semantic search.\n",
					st.Semantic.Embedded, st.Semantic.Chunks)
			}
		} else {
			fmt.Fprintf(out, "Index refreshed in %s.\n", time.Since(start).Round(time.Millisecond))
		}
		return nil
	},
}
