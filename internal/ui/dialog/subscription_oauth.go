package dialog

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/catwalk/pkg/catwalk"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/neur0map/prowl/internal/config"
	"github.com/neur0map/prowl/internal/oauth"
	"github.com/neur0map/prowl/internal/oauth/anthropic"
	"github.com/neur0map/prowl/internal/oauth/browserflow"
	"github.com/neur0map/prowl/internal/oauth/openai"
	"github.com/neur0map/prowl/internal/ui/common"
	"github.com/neur0map/prowl/internal/ui/util"
	"github.com/pkg/browser"
)

const subscriptionChoosing OAuthState = -1

// ActionUseAPIKey switches the native-provider dialog to the existing key flow.
type ActionUseAPIKey struct {
	Provider  catwalk.Provider
	Model     config.SelectedModel
	ModelType config.SelectedModelType
}

// SubscriptionOAuth offers API-key or browser-based subscription authentication.
type SubscriptionOAuth struct {
	com          *common.Common
	isOnboarding bool
	provider     catwalk.Provider
	model        config.SelectedModel
	modelType    config.SelectedModelType
	state        OAuthState
	choice       int
	spinner      spinner.Model
	ctx          context.Context
	cancel       context.CancelFunc
	closed       bool
	url          string
	err          error
}

// NewSubscriptionOAuth creates the choice screen without starting any IO.
func NewSubscriptionOAuth(com *common.Common, onboarding bool, provider catwalk.Provider, model config.SelectedModel, modelType config.SelectedModelType) (*SubscriptionOAuth, tea.Cmd) {
	m := &SubscriptionOAuth{com: com, isOnboarding: onboarding, provider: provider, model: model, modelType: modelType, state: subscriptionChoosing, spinner: spinner.New(spinner.WithSpinner(spinner.Dot), spinner.WithStyle(com.Styles.Dialog.OAuth.Spinner))}
	if cfg := com.Config(); cfg != nil {
		if p, ok := cfg.Providers.Get(string(provider.ID)); ok && p.OAuthToken != nil {
			m.choice = 1
		}
	}
	return m, nil
}
func (m *SubscriptionOAuth) ID() string { return OAuthID }

// Close cancels pending HTTP work even when the overlay removes us indirectly.
func (m *SubscriptionOAuth) Close() {
	m.closed = true
	if m.cancel != nil {
		m.cancel()
	}
}

type subscriptionStarted struct {
	owner *SubscriptionOAuth
	flow  *browserflow.Flow
	err   error
}
type subscriptionAuthorized struct {
	owner *SubscriptionOAuth
	token *oauth.Token
	err   error
}
type subscriptionSaved struct {
	owner *SubscriptionOAuth
	err   error
}

func (m *SubscriptionOAuth) HandleMsg(msg tea.Msg) Action {
	if m.closed {
		return nil
	}
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if key.Matches(msg, CloseKey) {
			if m.state == OAuthStateSaving {
				return nil
			}
			if m.state == OAuthStateSuccess {
				return m.selectModel()
			}
			m.Close()
			return ActionClose{}
		}
		switch m.state {
		case subscriptionChoosing:
			switch msg.String() {
			case "up", "down", "j", "k", "tab", "shift+tab":
				m.choice = 1 - m.choice
			case "enter":
				if m.choice == 0 {
					return ActionUseAPIKey{m.provider, m.model, m.modelType}
				}
				m.state = OAuthStateInitializing
				m.ctx, m.cancel = context.WithTimeout(context.Background(), 5*time.Minute)
				ctx, providerID, owner := m.ctx, string(m.provider.ID), m
				return ActionCmd{tea.Batch(m.spinner.Tick, func() tea.Msg {
					var flow *browserflow.Flow
					var err error
					switch providerID {
					case "openai":
						flow, err = openai.Start(ctx)
					case "anthropic":
						flow, err = anthropic.Start(ctx)
					default:
						err = fmt.Errorf("provider does not support subscription OAuth")
					}
					return subscriptionStarted{owner, flow, err}
				})}
			}
		case OAuthStateDisplay:
			switch msg.String() {
			case "enter":
				return ActionCmd{m.openURL()}
			case "u":
				return ActionCmd{common.CopyToClipboard(m.url, "Authorization URL copied")}
			}
		case OAuthStateSuccess:
			if msg.String() == "enter" {
				return m.selectModel()
			}
		}
	case spinner.TickMsg:
		if m.state == OAuthStateInitializing || m.state == OAuthStateDisplay || m.state == OAuthStateSaving {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return ActionCmd{cmd}
		}
	case subscriptionStarted:
		if msg.owner != m {
			return nil
		}
		if msg.err != nil {
			m.state = OAuthStateError
			m.err = msg.err
			return nil
		}
		m.url = msg.flow.URL
		m.state = OAuthStateDisplay
		ctx, owner, flow := m.ctx, m, msg.flow
		return ActionCmd{tea.Batch(m.openURL(), func() tea.Msg { token, err := flow.Wait(ctx); return subscriptionAuthorized{owner, token, err} })}
	case subscriptionAuthorized:
		if msg.owner != m {
			return nil
		}
		if msg.err != nil {
			m.state = OAuthStateError
			m.err = msg.err
			return nil
		}
		m.state = OAuthStateSaving
		ctx, owner, ws, providerID, token := m.ctx, m, m.com.Workspace, string(m.provider.ID), msg.token
		return ActionCmd{func() tea.Msg {
			if err := ctx.Err(); err != nil {
				return subscriptionSaved{owner, err}
			}
			return subscriptionSaved{owner, ws.SetProviderAPIKey(config.ScopeGlobal, providerID, token)}
		}}
	case subscriptionSaved:
		if msg.owner != m {
			return nil
		}
		if msg.err != nil {
			m.state = OAuthStateError
			m.err = msg.err
		} else {
			m.state = OAuthStateSuccess
		}
	}
	return nil
}

func (m *SubscriptionOAuth) selectModel() Action {
	return ActionSelectModel{Provider: m.provider, Model: m.model, ModelType: m.modelType}
}

func (m *SubscriptionOAuth) openURL() tea.Cmd {
	url := m.url
	return func() tea.Msg {
		if err := browser.OpenURL(url); err != nil {
			return util.ReportError(fmt.Errorf("could not open browser; press u to copy the authorization URL: %w", err))()
		}
		return nil
	}
}

func (m *SubscriptionOAuth) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	t := m.com.Styles
	width := max(0, min(60, area.Dx()-t.Dialog.View.GetHorizontalBorderSize()))
	inner := max(1, width-t.Dialog.View.GetHorizontalFrameSize())
	name := "ChatGPT"
	if m.provider.ID == "anthropic" {
		name = "Claude"
	}
	lines := []string{t.Dialog.Title.Render("Authenticate with " + name), ""}
	switch m.state {
	case subscriptionChoosing:
		for index, label := range []string{"API key · usage-based billing", name + " subscription · browser login"} {
			style := t.Dialog.NormalItem
			if index == m.choice {
				style = t.Dialog.SelectedItem
			}
			lines = append(lines, style.Width(inner).Render(label))
		}
		lines = append(lines, "", t.Dialog.PrimaryText.Width(inner).Render("Subscription login keeps Prowl's tools and agent. API keys remain available separately."))
		if m.provider.ID == "anthropic" {
			lines = append(lines, t.Dialog.PrimaryText.Width(inner).Render("Claude subscription access is unofficial and may be restricted by Anthropic."))
		}
		lines = append(lines, "", t.Dialog.OAuth.Instructions.Render("↑/↓ choose · enter continue · esc cancel"))
	case OAuthStateInitializing:
		lines = append(lines, m.spinner.View()+" Starting secure browser login…", t.Dialog.OAuth.Instructions.Render("esc cancel"))
	case OAuthStateDisplay:
		lines = append(lines, m.spinner.View()+" Waiting for browser authorization…", "", t.Dialog.PrimaryText.Width(inner).Render("Finish signing in in your browser. This window will update automatically."), "", t.Dialog.OAuth.Instructions.Render("enter open browser · u copy URL · esc cancel"))
	case OAuthStateSaving:
		lines = append(lines, m.spinner.View()+" Saving credentials and loading models…")
	case OAuthStateSuccess:
		lines = append(lines, t.Dialog.OAuth.Success.Render("Signed in. Credentials saved."), "", t.Dialog.OAuth.Instructions.Render("enter continue"))
	case OAuthStateError:
		lines = append(lines, t.Dialog.OAuth.ErrorText.Width(inner).Render("Authentication failed: "+m.err.Error()), "", t.Dialog.OAuth.Instructions.Render("esc close and retry"))
	}
	view := strings.Join(lines, "\n")
	if m.isOnboarding {
		DrawOnboarding(scr, area, view)
	} else {
		DrawCenter(scr, area, t.Dialog.View.Width(width).Render(view))
	}
	return nil
}
