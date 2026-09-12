package oauth

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTokenErrorsPreserveRevocationWithoutLeakingCredentials(t *testing.T) {
	t.Parallel()
	_, err := DecodeTokenResponse(&http.Response{StatusCode: http.StatusBadRequest, Body: io.NopCloser(strings.NewReader(`{"error":"invalid_grant","error_description":"refresh_token=private-secret"}`))})
	var exchange *TokenExchangeError
	require.ErrorAs(t, err, &exchange)
	require.True(t, exchange.IsRefreshTokenRevoked())
	require.NotContains(t, err.Error(), "private-secret")
}

func TestTokenResponseRejectsUnusableSuccess(t *testing.T) {
	t.Parallel()
	for _, body := range []string{`{"expires_in":3600}`, `{"access_token":"secret","expires_in":0}`, `{"access_token":"secret","expires_in":-1}`, `{"access_token":"secret","expires_in":9223372036854775807}`} {
		_, err := DecodeTokenResponse(&http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))})
		require.Error(t, err)
		require.NotContains(t, err.Error(), "secret")
	}
}
