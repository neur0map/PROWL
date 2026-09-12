package anthropic

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRefreshRotatesClaudeGrantAndIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var grant map[string]string
		if r.Header.Get("Content-Type") != "application/json" || r.Header.Get("anthropic-beta") != oauthBeta || json.NewDecoder(r.Body).Decode(&grant) != nil || grant["grant_type"] != "refresh_token" || grant["refresh_token"] != "old-grant" || grant["client_id"] != clientID {
			http.Error(w, "invalid grant", 400)
			return
		}
		fmt.Fprint(w, `{"access_token":"new-access","refresh_token":"rotated-grant","expires_in":3600,"account":{"uuid":"account-uuid"}}`)
	}))
	defer server.Close()
	old := tokenEndpoint
	tokenEndpoint = server.URL
	t.Cleanup(func() { tokenEndpoint = old })
	token, err := RefreshToken(t.Context(), "old-grant")
	require.NoError(t, err)
	require.Equal(t, "rotated-grant", token.RefreshToken)
	require.Equal(t, "account-uuid", token.AccountID)
	require.False(t, token.IsExpired())
}
