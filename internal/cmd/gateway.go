package cmd

import (
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/pkg/browser"
	"github.com/spf13/cobra"

	"github.com/neur0map/prowl/internal/config"
	"github.com/neur0map/prowl/internal/gateway"
	"github.com/neur0map/prowl/internal/gateway/catalog"
	"github.com/neur0map/prowl/internal/gateway/service"
	prowllog "github.com/neur0map/prowl/internal/log"
)

var gatewayCmd = &cobra.Command{
	Use:   "gateway",
	Short: "Run the AI gateway and its configuration dashboard",
	Long: "Start the Prowl gateway: an OpenAI-compatible proxy that keeps your provider " +
		"API keys encrypted, routes each request across every provider you have configured, " +
		"and fails over when one is rate-limited or out of credit. The dashboard it serves is " +
		"where you paste keys and watch usage.",
	Example: `
# Start the gateway and open the dashboard
prowl gateway

# Run it without opening a browser (for a headless box)
prowl gateway --no-open

# Use a different port
prowl gateway --port 9001

# List the built-in provider directory without starting anything
prowl gateway providers
`,
	RunE: runGateway,
}

var gatewayProvidersCmd = &cobra.Command{
	Use:   "providers",
	Short: "List the built-in provider directory",
	RunE:  runGatewayProviders,
}

func init() {
	gatewayCmd.Flags().Int("port", gateway.DefaultPort, "Port to listen on (loopback only)")
	gatewayCmd.Flags().Bool("no-open", false, "Do not open the dashboard in a browser")
	gatewayCmd.Flags().Bool("quiet", false, "Do not mirror the gateway log to this terminal")
	gatewayCmd.Flags().Bool("register", true, "Register the gateway as a provider in the global config")
	gatewayProvidersCmd.Flags().Bool("free-only", false, "Only providers with a standing free tier")
	gatewayProvidersCmd.Flags().Bool("routable-only", false, "Only providers the gateway can proxy to today")
	gatewayCmd.AddCommand(gatewayProvidersCmd)
}

func runGateway(cmd *cobra.Command, _ []string) error {
	port, _ := cmd.Flags().GetInt("port")
	noOpen, _ := cmd.Flags().GetBool("no-open")
	register, _ := cmd.Flags().GetBool("register")
	quiet, _ := cmd.Flags().GetBool("quiet")

	// The root command discards the default logger until a config-derived log
	// file exists, so without this every routing decision would vanish. The
	// gateway's observable behaviour is in those lines, so they go to its log
	// file and, unless silenced, to the terminal running it.
	logFile := filepath.Join(gateway.Dir(), "gateway.log")
	if quiet {
		prowllog.Setup(logFile, false)
	} else {
		prowllog.Setup(logFile, false, cmd.ErrOrStderr())
	}

	svc, err := service.Open(cmd.Context(), service.Options{Dir: gateway.Dir(), Port: port})
	if err != nil {
		return err
	}
	defer func() { _ = svc.Close() }()

	// Prowl's own provider logins become pool candidates the dashboard can
	// enroll. Failing to load the config is not fatal: the gateway still
	// serves every key in its own vault.
	if store, cfgErr := loadGatewayConfigStore(cmd); cfgErr == nil {
		svc.Engine().SetCredentialSource(
			service.NewConfigLogins(store))
	} else {
		slog.Warn("Could not read Prowl logins for the pool", "error", cfgErr)
	}

	listener, err := svc.Listen(port)
	if err != nil {
		// A running harness already serves the gateway, so the useful
		// behaviour here is to point at it rather than refuse to start.
		if url, ok := gateway.Running(cmd.Context(), port, svc.Token()); ok {
			fmt.Fprintf(cmd.OutOrStdout(), "Prowl gateway is already running: %s\n", url)
			if !noOpen {
				_ = browser.OpenURL(url)
			}
			return nil
		}
		return err
	}

	// Registering here rather than on first use means the model picker shows
	// the gateway as soon as it has run once, and the entry always carries
	// the current port and token.
	if register {
		store, cfgErr := loadGatewayConfigStore(cmd)
		if cfgErr != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not register the gateway provider: %v\n", cfgErr)
		} else if err := gateway.Register(store, svc.BaseURL(), svc.Token()); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: %v\n", err)
		}
	}

	dashboard := "http://" + svc.Addr() + "/"

	keys, _ := svc.Engine().Vault().List(cmd.Context())
	configured := 0
	for _, k := range keys {
		if k.Enabled {
			configured++
		}
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Prowl gateway listening on %s\n", svc.Addr())
	fmt.Fprintf(out, "  dashboard   %s\n", dashboard)
	fmt.Fprintf(out, "  base url    %s\n", svc.BaseURL())
	fmt.Fprintf(out, "  providers   %d available, %d keys configured\n",
		len(svc.Engine().Registry().All()), configured)
	if code := svc.SetupCode(); code != "" {
		// Printed here because this is where the person who started the
		// gateway is looking, and setup needs it even from this machine:
		// sharing the host is not the same as being the operator.
		fmt.Fprintf(out, "\nFirst run. Create the dashboard account with this code:\n")
		fmt.Fprintf(out, "  setup code  %s\n", code)
	}
	if configured == 0 {
		fmt.Fprintf(out, "\nNo provider keys yet. Open the dashboard and add one:\n")
		fmt.Fprintf(out, "  %s.\n", gateway.FreeStarterHint)
	}
	fmt.Fprintf(out, "\nPress Ctrl+C to stop.\n")

	if !noOpen {
		if err := browser.OpenURL(dashboard); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "could not open a browser: %v\nopen %s yourself\n", err, dashboard)
		}
	}

	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return svc.Serve(ctx, listener)
}

// loadGatewayConfigStore initialises just enough config to register the
// provider entry, without starting an agent.
func loadGatewayConfigStore(cmd *cobra.Command) (*config.ConfigStore, error) {
	cwd, err := ResolveCwd(cmd)
	if err != nil {
		return nil, err
	}
	return config.Init(cwd, "", false)
}

func runGatewayProviders(cmd *cobra.Command, _ []string) error {
	freeOnly, _ := cmd.Flags().GetBool("free-only")
	routableOnly, _ := cmd.Flags().GetBool("routable-only")

	providers, err := catalog.All()
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	shown, freeModels := 0, 0
	for _, p := range providers {
		if freeOnly && !p.Free() {
			continue
		}
		if routableOnly && !p.Routable() {
			continue
		}
		shown++
		freeModels += p.FreeModels

		note := ""
		if !p.Routable() {
			switch {
			case len(p.RequiresVars) > 0:
				note = fmt.Sprintf("  (needs %v)", p.RequiresVars)
			default:
				note = fmt.Sprintf("  (%s API, not proxied yet)", p.Compat)
			}
		}
		// The api-key URL is the actionable link: it is where the reader goes
		// to get a credential. The base URL is the gateway's own business.
		link := p.APIKeyURL
		if link == "" {
			link = "— no signup page published"
		}
		fmt.Fprintf(out, "%-26s %-18s %-13s %3d free  %s%s\n",
			p.ID, p.Tier, p.Friction, p.FreeModels, link, note)
	}
	fmt.Fprintf(out, "\n%d of %d providers shown · %d free models\n",
		shown, len(providers), freeModels)
	return nil
}
