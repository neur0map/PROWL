package openai

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/neur0map/prowl/internal/oauth"
	"github.com/stretchr/testify/require"
)

func TestRefreshRetainsGrantAndExtractsAccountRouting(t *testing.T) {
	access := "header." + base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_account_id":"subscription-account"}}`)) + ".signature"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" || r.ParseForm() != nil || r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "old-grant" || r.Form.Get("client_id") != ClientID {
			http.Error(w, "invalid refresh", 400)
			return
		}
		fmt.Fprintf(w, `{"access_token":%q,"id_token":"opaque","expires_in":3600}`, access)
	}))
	defer server.Close()
	old := tokenEndpoint
	tokenEndpoint = server.URL
	t.Cleanup(func() { tokenEndpoint = old })
	token, err := RefreshToken(t.Context(), "old-grant")
	require.NoError(t, err)
	require.Equal(t, "old-grant", token.RefreshToken)
	require.Equal(t, "subscription-account", token.AccountID)
	require.False(t, token.IsExpired())
}

func TestCatalogExcludesModelsUnavailableToSubscription(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer grant" || r.Header.Get("chatgpt-account-id") != "account" || r.URL.Query().Get("client_version") == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"models":[{"slug":"available","display_name":"Available","visibility":"list","context_window":200000,"default_reasoning_level":"medium","supported_reasoning_levels":[{"effort":"low"},{"effort":"medium"}]},{"slug":"hidden","visibility":"hide"},{"slug":"","visibility":"list"}]}`)
	}))
	defer server.Close()
	old := modelsEndpoint
	modelsEndpoint = server.URL
	t.Cleanup(func() { modelsEndpoint = old })
	models, err := FetchModels(t.Context(), &oauth.Token{AccessToken: "grant", AccountID: "account"})
	require.NoError(t, err)
	require.Len(t, models, 1)
	require.Equal(t, "available", models[0].ID)
	require.Equal(t, []string{"low", "medium"}, models[0].ReasoningLevels)
}
