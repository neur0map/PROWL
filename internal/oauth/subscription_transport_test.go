package oauth_test

import (
	"net/http"
	"testing"

	"github.com/neur0map/prowl/internal/oauth"
	"github.com/neur0map/prowl/internal/oauth/anthropic"
	"github.com/neur0map/prowl/internal/oauth/openai"
	"github.com/stretchr/testify/require"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSubscriptionTokensNeverReachUntrustedDestinations(t *testing.T) {
	t.Parallel()
	for _, provider := range []struct {
		name, host string
		transport  func(http.RoundTripper, *oauth.Token) http.RoundTripper
	}{
		{"openai", "chatgpt.com", func(base http.RoundTripper, token *oauth.Token) http.RoundTripper {
			return &openai.Transport{Base: base, Token: token}
		}},
		{"anthropic", "api.anthropic.com", func(base http.RoundTripper, token *oauth.Token) http.RoundTripper {
			return &anthropic.Transport{Base: base, Token: token}
		}},
	} {
		t.Run(provider.name, func(t *testing.T) {
			t.Parallel()
			for _, destination := range []string{"https://untrusted.example/path", "http://" + provider.host + "/path"} {
				var seen *http.Request
				transport := provider.transport(roundTripFunc(func(r *http.Request) (*http.Response, error) { seen = r; return &http.Response{StatusCode: 200}, nil }), &oauth.Token{AccessToken: "subscription-secret", AccountID: "account-secret"})
				req, err := http.NewRequest(http.MethodGet, destination, nil)
				require.NoError(t, err)
				req.Header.Set("Authorization", "Bearer subscription-secret")
				req.Header.Set("X-Api-Key", "subscription-secret")
				if provider.name == "openai" {
					req.Header.Set("chatgpt-account-id", "account-secret")
				}
				_, err = transport.RoundTrip(req)
				require.NoError(t, err)
				require.Empty(t, seen.Header.Get("Authorization"))
				require.Empty(t, seen.Header.Get("X-Api-Key"))
				require.Empty(t, seen.Header.Get("chatgpt-account-id"))
				require.Equal(t, "Bearer subscription-secret", req.Header.Get("Authorization"))
			}
		})
	}
}
