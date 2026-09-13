package callback

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestWrite_EscapesUntrustedText proves provider-supplied strings cannot
// inject markup. Error descriptions come straight off a query string.
func TestWrite_EscapesUntrustedText(t *testing.T) {
	t.Parallel()

	var b strings.Builder
	require.NoError(t, Write(&b, Result{
		Subject:          `<img src=x onerror=alert(1)>`,
		ErrorCode:        `<script>`,
		ErrorDescription: `</div><script>alert(2)</script>`,
	}))
	page := b.String()

	require.NotContains(t, page, "<img src=x")
	require.NotContains(t, page, "<script>alert(2)")
	require.Contains(t, page, "&lt;img")
}

func TestServe_StatusCodes(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		result Result
		want   int
	}{
		"success": {Result{Subject: "linear"}, http.StatusOK},
		"failure": {Result{ErrorCode: "access_denied"}, http.StatusBadRequest},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			require.NoError(t, Serve(rec, tc.result))
			require.Equal(t, tc.want, rec.Code)
			require.Equal(t, "text/html; charset=utf-8", rec.Header().Get("Content-Type"))
			// The page reports a one-time result and must not be replayed
			// from cache on a later visit to the same localhost URL.
			require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
		})
	}
}
