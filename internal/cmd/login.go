package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/neur0map/prowl/internal/clipboard"
	"github.com/neur0map/prowl/internal/config"
	"github.com/neur0map/prowl/internal/oauth"
	"github.com/neur0map/prowl/internal/oauth/anthropic"
	"github.com/neur0map/prowl/internal/oauth/browserflow"
	"github.com/neur0map/prowl/internal/oauth/copilot"
	"github.com/neur0map/prowl/internal/oauth/hyper"
	"github.com/neur0map/prowl/internal/oauth/openai"
	"github.com/neur0map/prowl/internal/workspace"
	"github.com/pkg/browser"
	"github.com/spf13/cobra"
)

var loginCmd = &cobra.Command{
	Aliases: []string{"auth"},
	Use:     "login [platform]",
	Short:   "Login Prowl to a platform",
	Long: `Login Prowl to a specified platform.
The platform should be provided as an argument.
Available platforms are: hyper, copilot, openai, anthropic.`,
	Example: `
# Authenticate with Ryoku Hyper
prowl login

# Authenticate with GitHub Copilot
prowl login copilot

# Authenticate with an OpenAI/ChatGPT subscription
prowl login openai

# Authenticate with an Anthropic/Claude subscription
prowl login anthropic

# Force re-authentication even if already logged in
prowl login -f copilot
  `,
	ValidArgs: []cobra.Completion{
		"hyper",
		"copilot",
		"github",
		"github-copilot",
		"openai",
		"chatgpt",
		"anthropic",
		"claude",
	},
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, cleanup, err := setupWorkspaceWithProgressBar(cmd)
		if err != nil {
			return err
		}
		defer cleanup()

		provider := "hyper"
		if len(args) > 0 {
			provider = args[0]
		}
		force, _ := cmd.Flags().GetBool("force")
		switch provider {
		case "hyper":
			return loginHyper(ws, force)
		case "copilot", "github", "github-copilot":
			return loginCopilot(ws, force)
		case "openai", "chatgpt":
			return loginSubscription(cmd.Context(), ws, "openai", "OpenAI (ChatGPT)", force, openai.Start)
		case "anthropic", "claude":
			return loginSubscription(cmd.Context(), ws, "anthropic", "Anthropic (Claude)", force, anthropic.Start)
		default:
			return fmt.Errorf("unknown platform: %s", provider)
		}
	},
}

func init() {
	loginCmd.Flags().BoolP("force", "f", false, "Force re-authentication even if already logged in")
}

func loginHyper(ws workspace.Workspace, force bool) error {
	ctx := getLoginContext()

	if !force {
		cfg := ws.Config()
		if cfg != nil {
			if pc, ok := cfg.Providers.Get("hyper"); ok && pc.OAuthToken != nil {
				fmt.Println("You are already logged in to Hyper.")
				fmt.Println("Use --force to re-authenticate.")
				return nil
			}
		}
	}

	resp, err := hyper.InitiateDeviceAuth(ctx)
	if err != nil {
		return err
	}

	clipboard.WriteText(resp.UserCode)
	fmt.Println("The following code should be on clipboard already:")

	fmt.Println()
	lipgloss.Println(lipgloss.NewStyle().Bold(true).Render(resp.UserCode))
	fmt.Println()
	fmt.Println("Press enter to open this URL, and then paste it there:")
	fmt.Println()
	lipgloss.Println(lipgloss.NewStyle().Hyperlink(resp.VerificationURL, "id=hyper").Render(resp.VerificationURL))
	fmt.Println()
	waitEnter()
	if err := browser.OpenURL(resp.VerificationURL); err != nil {
		fmt.Println("Could not open the URL. You'll need to manually open the URL in your browser.")
	}

	fmt.Println("Exchanging authorization code...")
	refreshToken, err := hyper.PollForToken(ctx, resp.DeviceCode, resp.ExpiresIn)
	if err != nil {
		return err
	}

	fmt.Println("Exchanging refresh token for access token...")
	token, err := hyper.ExchangeToken(ctx, refreshToken)
	if err != nil {
		return err
	}

	fmt.Println("Verifying access token...")
	introspect, err := hyper.IntrospectToken(ctx, token.AccessToken)
	if err != nil {
		return fmt.Errorf("token introspection failed: %w", err)
	}
	if !introspect.Active {
		return fmt.Errorf("access token is not active")
	}

	if err := ws.SetProviderAPIKey(config.ScopeGlobal, "hyper", token); err != nil {
		return err
	}

	fmt.Println()
	fmt.Println("You're now authenticated with Hyper!")
	return nil
}

func loginCopilot(ws workspace.Workspace, force bool) error {
	loginCtx := getLoginContext()

	if !force {
		cfg := ws.Config()
		if cfg != nil {
			if pc, ok := cfg.Providers.Get("copilot"); ok && pc.OAuthToken != nil {
				fmt.Println("You are already logged in to GitHub Copilot.")
				fmt.Println("Use --force to re-authenticate.")
				return nil
			}
		}
	}

	diskToken, hasDiskToken := copilot.RefreshTokenFromDisk()
	var token *oauth.Token

	switch {
	case hasDiskToken:
		fmt.Println("Found existing GitHub Copilot token on disk. Using it to authenticate...")

		t, err := copilot.RefreshToken(loginCtx, diskToken)
		if err != nil {
			return fmt.Errorf("unable to refresh token from disk: %w", err)
		}
		token = t
	default:
		fmt.Println("Requesting device code from GitHub...")
		dc, err := copilot.RequestDeviceCode(loginCtx)
		if err != nil {
			return err
		}

		clipboard.WriteText(dc.UserCode)
		fmt.Println()
		fmt.Println("The following code should be on clipboard already:")
		fmt.Println()
		lipgloss.Println(lipgloss.NewStyle().Bold(true).Render(dc.UserCode))
		fmt.Println()
		fmt.Println("Press enter to open this URL and authenticate with GitHub Copilot:")
		fmt.Println()
		lipgloss.Println(lipgloss.NewStyle().Hyperlink(dc.VerificationURI, "id=copilot").Render(dc.VerificationURI))
		fmt.Println()
		waitEnter()
		if err := browser.OpenURL(dc.VerificationURI); err != nil {
			fmt.Println("Could not open the URL. You'll need to manually open the URL in your browser.")
		}

		fmt.Println("Waiting for authorization...")

		t, err := copilot.PollForToken(loginCtx, dc)
		if err == copilot.ErrNotAvailable {
			fmt.Println()
			fmt.Println("GitHub Copilot is unavailable for this account. To signup, go to the following page:")
			fmt.Println()
			lipgloss.Println(lipgloss.NewStyle().Hyperlink(copilot.SignupURL, "id=copilot-signup").Render(copilot.SignupURL))
			fmt.Println()
			fmt.Println("You may be able to request free access if eligible. For more information, see:")
			fmt.Println()
			lipgloss.Println(lipgloss.NewStyle().Hyperlink(copilot.FreeURL, "id=copilot-free").Render(copilot.FreeURL))
		}
		if err != nil {
			return err
		}
		token = t
	}

	if err := ws.SetProviderAPIKey(config.ScopeGlobal, "copilot", token); err != nil {
		return err
	}

	fmt.Println()
	fmt.Println("You're now authenticated with GitHub Copilot!")
	return nil
}

// loginSubscription runs the browser-based OAuth login shared by the
// subscription providers (OpenAI/ChatGPT, Anthropic/Claude). start begins
// a local callback listener and returns the authorization URL; the
// returned flow owns that listener and MUST be closed on every path. The
// wait honours ctx (the command's signal-aware context) and is further
// bounded by a timeout, so SIGINT or a stalled browser cancels cleanly
// without leaking a goroutine or calling os.Exit.
func loginSubscription(ctx context.Context, ws workspace.Workspace, providerID, displayName string, force bool, start func(context.Context) (*browserflow.Flow, error)) error {
	if !force {
		cfg := ws.Config()
		if cfg != nil {
			if pc, ok := cfg.Providers.Get(providerID); ok && pc.OAuthToken != nil {
				fmt.Printf("You are already logged in to %s.\n", displayName)
				fmt.Println("Use --force to re-authenticate.")
				return nil
			}
		}
	}

	flow, err := start(ctx)
	if err != nil {
		return fmt.Errorf("could not start %s login: %w (the local callback port may be in use by another login attempt; close it and try again)", displayName, err)
	}
	defer flow.Close()

	fmt.Printf("Opening your browser to authenticate with %s...\n", displayName)
	fmt.Println()
	lipgloss.Println(lipgloss.NewStyle().Hyperlink(flow.URL, "id="+providerID).Render(flow.URL))
	fmt.Println()
	if err := browser.OpenURL(flow.URL); err != nil {
		fmt.Println("Could not open the browser automatically. Open the URL above manually to continue.")
	}

	fmt.Println("Waiting for authorization...")

	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	token, err := flow.Wait(waitCtx)
	if err != nil {
		return err
	}

	if err := ws.SetProviderAPIKey(config.ScopeGlobal, providerID, token); err != nil {
		return err
	}

	fmt.Println()
	fmt.Printf("You're now authenticated with %s!\n", displayName)
	return nil
}

func getLoginContext() context.Context {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, os.Kill)
	go func() {
		<-ctx.Done()
		cancel()
		os.Exit(1)
	}()
	return ctx
}

func waitEnter() {
	_, _ = fmt.Scanln()
}
