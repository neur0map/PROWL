package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

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

# Start it in the background and get your terminal back
prowl gateway up

# Stop the background gateway
prowl gateway down

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

var gatewayUpCmd = &cobra.Command{
	Use:   "up",
	Short: "Start the gateway in the background and return the terminal",
	Long: "Start the gateway as a detached background process, so it keeps " +
		"running after you close this terminal. Reports the dashboard URL once " +
		"the gateway is answering. Stop it with `prowl gateway down`.",
	RunE: runGatewayUp,
}

var gatewayDownCmd = &cobra.Command{
	Use:   "down",
	Short: "Stop the background gateway",
	RunE:  runGatewayDown,
}

var gatewayStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Report whether the background gateway is running",
	RunE:  runGatewayStatus,
}

var gatewayRestartCmd = &cobra.Command{
	Use:   "restart",
	Short: "Restart the background gateway",
	RunE:  runGatewayRestart,
}

func init() {
	gatewayCmd.Flags().Int("port", gateway.DefaultPort, "Port to listen on (loopback only)")
	gatewayCmd.Flags().Bool("no-open", false, "Do not open the dashboard in a browser")
	gatewayCmd.Flags().Bool("quiet", false, "Do not mirror the gateway log to this terminal")
	gatewayCmd.Flags().Bool("register", true, "Register the gateway as a provider in the global config")
	gatewayProvidersCmd.Flags().Bool("free-only", false, "Only providers with a standing free tier")
	gatewayProvidersCmd.Flags().Bool("routable-only", false, "Only providers the gateway can proxy to today")

	// The background-control commands each take a port so they can address a
	// gateway started on a non-default one, and up/restart mirror --no-open.
	for _, c := range []*cobra.Command{gatewayUpCmd, gatewayDownCmd, gatewayStatusCmd, gatewayRestartCmd} {
		c.Flags().Int("port", gateway.DefaultPort, "Port the gateway listens on (loopback only)")
	}
	gatewayUpCmd.Flags().Bool("no-open", false, "Do not open the dashboard in a browser")
	gatewayRestartCmd.Flags().Bool("no-open", false, "Do not open the dashboard in a browser")
	gatewayCmd.AddCommand(gatewayUpCmd, gatewayDownCmd, gatewayStatusCmd, gatewayRestartCmd)
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

	// Record this process so `prowl gateway down` can find and stop it. Only
	// the process that actually bound the port records the pid, so the file
	// never points at a gateway that failed to start.
	if err := gateway.WritePIDFile(gateway.Dir()); err != nil {
		slog.Warn("Could not record gateway pid", "error", err)
	}
	defer func() { _ = gateway.RemovePIDFile(gateway.Dir()) }()

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

// gatewayUpTimeout bounds how long `up` waits for the spawned gateway to bind
// before declaring the launch failed. Opening the state store and seeding the
// catalogue on a cold start is well under this.
const gatewayUpTimeout = 20 * time.Second

// gatewayDownTimeout bounds how long `down` waits for a gateway to exit after
// it is signalled, before reporting that it would not stop.
const gatewayDownTimeout = 10 * time.Second

func runGatewayUp(cmd *cobra.Command, _ []string) error {
	port, _ := cmd.Flags().GetInt("port")
	noOpen, _ := cmd.Flags().GetBool("no-open")
	out := cmd.OutOrStdout()

	dir := gateway.Dir()
	token, err := gateway.EnsureToken(dir)
	if err != nil {
		return err
	}

	// Already up? Point at it. A second process cannot bind the port anyway,
	// so spawning one would only produce a confusing failure in the log.
	if url, ok := gateway.Running(cmd.Context(), port, token); ok {
		fmt.Fprintf(out, "Prowl gateway already running: %s\n", url)
		if !noOpen {
			_ = browser.OpenURL(url)
		}
		return nil
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate prowl executable: %w", err)
	}

	// The child runs the ordinary foreground gateway: it binds the port,
	// records its own pid, and registers the provider. --quiet keeps it off a
	// terminal it no longer has; --no-open leaves the browser to this command.
	args := []string{"gateway", "--quiet", "--no-open"}
	if port != gateway.DefaultPort {
		args = append(args, "--port", strconv.Itoa(port))
	}

	// context.Background so cancelling this command does not kill the daemon;
	// detachProcess is what actually severs it from this process's lifetime.
	child := exec.CommandContext(context.Background(), exe, args...)
	detachProcess(child)

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create gateway dir: %w", err)
	}
	logPath := filepath.Join(dir, "daemon.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open daemon log: %w", err)
	}
	defer func() { _ = logFile.Close() }()
	child.Stdout = logFile
	child.Stderr = logFile

	if err := child.Start(); err != nil {
		return fmt.Errorf("start gateway: %w", err)
	}
	if err := child.Process.Release(); err != nil {
		return fmt.Errorf("detach gateway: %w", err)
	}

	// Return success only once the dashboard actually answers, so a child that
	// crashed on startup surfaces as a failure here rather than a false "up".
	deadline := time.Now().Add(gatewayUpTimeout)
	for {
		if url, ok := gateway.Running(cmd.Context(), port, token); ok {
			fmt.Fprintf(out, "Prowl gateway started in the background.\n")
			fmt.Fprintf(out, "  dashboard   %s\n", url)
			fmt.Fprintf(out, "  base url    http://127.0.0.1:%d/v1\n", port)
			fmt.Fprintf(out, "  logs        %s\n", filepath.Join(dir, "gateway.log"))
			fmt.Fprintf(out, "\nStop it with: prowl gateway down\n")
			if !noOpen {
				_ = browser.OpenURL(url)
			}
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("gateway did not come up within %s; see %s", gatewayUpTimeout, logPath)
		}
		select {
		case <-cmd.Context().Done():
			return cmd.Context().Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func runGatewayDown(cmd *cobra.Command, _ []string) error {
	port, _ := cmd.Flags().GetInt("port")
	out := cmd.OutOrStdout()
	dir := gateway.Dir()

	pid, err := gateway.ReadPIDFile(dir)
	if err != nil {
		return err
	}

	token, _ := gateway.EnsureToken(dir)
	_, running := gateway.Running(cmd.Context(), port, token)

	if pid == 0 || !processAlive(pid) {
		_ = gateway.RemovePIDFile(dir)
		if running {
			// Something answers the port but this machine has no live record
			// of starting it -- a foreground `prowl gateway`, or another user.
			// Signalling a process we did not track would be reckless.
			fmt.Fprintf(out, "A gateway is answering on port %d but was not started with `gateway up`; stop it where it runs.\n", port)
			return nil
		}
		fmt.Fprintln(out, "Prowl gateway is not running.")
		return nil
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		_ = gateway.RemovePIDFile(dir)
		fmt.Fprintln(out, "Prowl gateway is not running.")
		return nil
	}
	if err := signalTerminate(proc); err != nil {
		// The process vanished between the liveness check and the signal.
		_ = gateway.RemovePIDFile(dir)
		fmt.Fprintln(out, "Prowl gateway is not running.")
		return nil
	}

	deadline := time.Now().Add(gatewayDownTimeout)
	for processAlive(pid) {
		if time.Now().After(deadline) {
			return fmt.Errorf("gateway (pid %d) did not stop within %s", pid, gatewayDownTimeout)
		}
		select {
		case <-cmd.Context().Done():
			return cmd.Context().Err()
		case <-time.After(200 * time.Millisecond):
		}
	}

	_ = gateway.RemovePIDFile(dir)
	fmt.Fprintln(out, "Prowl gateway stopped.")
	return nil
}

func runGatewayStatus(cmd *cobra.Command, _ []string) error {
	port, _ := cmd.Flags().GetInt("port")
	out := cmd.OutOrStdout()
	dir := gateway.Dir()

	token, err := gateway.EnsureToken(dir)
	if err != nil {
		return err
	}
	pid, _ := gateway.ReadPIDFile(dir)

	if url, ok := gateway.Running(cmd.Context(), port, token); ok {
		fmt.Fprintln(out, "Prowl gateway is running.")
		fmt.Fprintf(out, "  dashboard   %s\n", url)
		fmt.Fprintf(out, "  base url    http://127.0.0.1:%d/v1\n", port)
		if pid > 0 {
			fmt.Fprintf(out, "  pid         %d\n", pid)
		}
		return nil
	}

	// Not answering. Clear a pid file left behind by a crash so the next `up`
	// starts clean.
	if pid > 0 && !processAlive(pid) {
		_ = gateway.RemovePIDFile(dir)
	}
	fmt.Fprintln(out, "Prowl gateway is not running.")
	return nil
}

func runGatewayRestart(cmd *cobra.Command, args []string) error {
	if err := runGatewayDown(cmd, args); err != nil {
		return err
	}
	return runGatewayUp(cmd, args)
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
