package browserflow

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/neur0map/prowl/internal/oauth"
	"github.com/stretchr/testify/require"
)

func callbackAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	require.NoError(t, listener.Close())
	return address
}

func TestCallbackRejectsUnboundStateWithoutConsumingGrant(t *testing.T) {
	t.Parallel()
	address := callbackAddress(t)
	redirect := "http://" + address + "/callback"
	var verifier, exchangedState string
	calls := 0
	flow, err := Start(t.Context(), Config{AuthorizeURL: "https://example.com/authorize", ClientID: "public-client", RedirectURI: redirect, Exchange: func(_ context.Context, code, redirectURI, proof, state string) (*oauth.Token, error) {
		calls++
		if code != "valid" || redirectURI != redirect {
			return nil, context.Canceled
		}
		verifier, exchangedState = proof, state
		return &oauth.Token{AccessToken: "accepted"}, nil
	}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = flow.Close() })
	authorize, err := url.Parse(flow.URL)
	require.NoError(t, err)
	state := authorize.Query().Get("state")
	client := &http.Client{Timeout: time.Second}
	for _, query := range []string{"code=valid", "state=wrong&code=valid", "state=" + state + "&state=wrong&code=valid", "state=" + state} {
		resp, err := client.Get(redirect + "?" + query)
		require.NoError(t, err)
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	}
	resp, err := client.Get(redirect + "?state=" + state + "&code=valid")
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	token, err := flow.Wait(t.Context())
	require.NoError(t, err)
	require.Equal(t, "accepted", token.AccessToken)
	require.Equal(t, 1, calls)
	require.Equal(t, state, exchangedState)
	challenge := sha256.Sum256([]byte(verifier))
	require.Equal(t, base64.RawURLEncoding.EncodeToString(challenge[:]), authorize.Query().Get("code_challenge"))
	listener, err := net.Listen("tcp4", address)
	require.NoError(t, err)
	require.NoError(t, listener.Close())
}

func TestCancellationReleasesListenerBeforeWait(t *testing.T) {
	t.Parallel()
	address := callbackAddress(t)
	ctx, cancel := context.WithCancel(t.Context())
	flow, err := Start(ctx, Config{AuthorizeURL: "https://example.com/authorize", ClientID: "client", RedirectURI: "http://" + address + "/callback", Exchange: func(context.Context, string, string, string, string) (*oauth.Token, error) {
		t.Error("canceled flow exchanged a grant")
		return nil, nil
	}})
	require.NoError(t, err)
	cancel()
	require.Eventually(t, func() bool {
		listener, err := net.Listen("tcp4", address)
		if err != nil {
			return false
		}
		_ = listener.Close()
		return true
	}, time.Second, time.Millisecond)
	_, err = flow.Wait(t.Context())
	require.ErrorIs(t, err, context.Canceled)
}
