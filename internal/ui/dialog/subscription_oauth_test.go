package dialog

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/neur0map/prowl/internal/oauth"
	"github.com/neur0map/prowl/internal/oauth/browserflow"
	"github.com/stretchr/testify/require"
)

func TestSubscriptionDialogDismissalCancelsPendingFlow(t *testing.T) {
	t.Parallel()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	require.NoError(t, listener.Close())
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	flow, err := browserflow.Start(ctx, browserflow.Config{AuthorizeURL: "https://example.com/authorize", ClientID: "client", RedirectURI: "http://" + address + "/callback", Exchange: func(context.Context, string, string, string, string) (*oauth.Token, error) {
		return nil, context.Canceled
	}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = flow.Close() })
	old := &SubscriptionOAuth{ctx: ctx, cancel: cancel, state: OAuthStateInitializing}
	var overlay Overlay
	overlay.OpenDialog(old)
	overlay.CloseDialog(OAuthID)
	require.Eventually(t, func() bool {
		listener, err := net.Listen("tcp4", address)
		if err != nil {
			return false
		}
		_ = listener.Close()
		return true
	}, time.Second, time.Millisecond)
	// A delayed command result from the dismissed dialog must not start a
	// browser or credential save in its replacement.
	replacement := &SubscriptionOAuth{state: subscriptionChoosing}
	require.Nil(t, replacement.HandleMsg(subscriptionStarted{owner: old, flow: flow}))
	require.Nil(t, replacement.HandleMsg(subscriptionAuthorized{owner: old, token: &oauth.Token{AccessToken: "old-grant"}}))
}
