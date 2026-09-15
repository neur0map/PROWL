package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestQuotaSignalsMatchTheViewsContract is the crash this shape fixes. The
// dashboard's Quota signals tab renders one row per metric and calls string
// methods on `observedAt`; it was handed the stored pool row, with both
// metrics in one object and Unix integers for the instants, so the tab threw
// "e.includes is not a function" and the error boundary took the whole page.
func TestQuotaSignalsMatchTheViewsContract(t *testing.T) {
	t.Parallel()

	s := testServer(t, Options{MachineKey: compatMachineKey})
	tok := session(t, s)

	// A pool that reported both a request and a token window, which is what
	// Groq's headers actually carry.
	reset := time.Now().Add(time.Hour).Unix()
	_, err := s.engine.DB().Exec(`
		INSERT INTO provider_quota_state(quota_pool_key, platform, requests_limit,
			requests_remaining, tokens_limit, tokens_remaining, resets_at, observed_at)
		VALUES('groq/4', 'groq', 1000, 12, 8000, 900, ?, ?)`,
		reset, time.Now().Unix())
	require.NoError(t, err)

	resp, body := do(t, s, http.MethodGet, "/api/health", "", authed(tok))
	require.Equal(t, http.StatusOK, resp.StatusCode, "body was %s", body)

	var payload struct {
		QuotaStates []struct {
			Platform     string  `json:"platform"`
			KeyID        int64   `json:"keyId"`
			QuotaPoolKey string  `json:"quotaPoolKey"`
			Metric       string  `json:"metric"`
			Limit        *int64  `json:"limit"`
			Remaining    *int64  `json:"remaining"`
			ResetAt      *string `json:"resetAt"`
			Source       string  `json:"source"`
			Confidence   float64 `json:"confidence"`
			ObservedAt   string  `json:"observedAt"`
			UpdatedAt    string  `json:"updatedAt"`
		} `json:"quotaStates"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &payload))

	byMetric := map[string]int{}
	for _, row := range payload.QuotaStates {
		byMetric[row.Metric]++
		// The client calls string methods on these; a number here is the bug.
		require.NotEmpty(t, row.ObservedAt, "observedAt must be a date string")
		require.NotEmpty(t, row.UpdatedAt, "updatedAt must be a date string")
		require.Contains(t, row.ObservedAt, "-", "observedAt must parse as a date")
		require.Equal(t, "header", row.Source, "a header reading must say so")
		require.InDelta(t, 1.0, row.Confidence, 0.001)
		require.Equal(t, "groq", row.Platform)
		require.Equal(t, int64(4), row.KeyID, "the key id must come off the pool key")
		require.Equal(t, "groq/4", row.QuotaPoolKey)
		require.NotNil(t, row.ResetAt, "a provider-stated reset must be carried")
	}
	require.Equal(t, 1, byMetric["requests"], "the request window must be its own row")
	require.Equal(t, 1, byMetric["tokens"], "the token window must be its own row")
}

// TestQuotaSignalsOmitMetricsNoProviderReported keeps the view from showing a
// window that was never observed.
func TestQuotaSignalsOmitMetricsNoProviderReported(t *testing.T) {
	t.Parallel()

	s := testServer(t, Options{MachineKey: compatMachineKey})
	tok := session(t, s)

	_, err := s.engine.DB().Exec(`
		INSERT INTO provider_quota_state(quota_pool_key, platform, requests_limit,
			requests_remaining, observed_at)
		VALUES('kilo/7', 'kilo', NULL, 0, ?)`, time.Now().Unix())
	require.NoError(t, err)

	_, body := do(t, s, http.MethodGet, "/api/health", "", authed(tok))
	var payload struct {
		QuotaStates []struct {
			Metric  string  `json:"metric"`
			ResetAt *string `json:"resetAt"`
		} `json:"quotaStates"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &payload))
	require.Len(t, payload.QuotaStates, 1, "only the observed metric may appear")
	require.Equal(t, "requests", payload.QuotaStates[0].Metric)
	require.Nil(t, payload.QuotaStates[0].ResetAt, "an unobserved reset must stay absent")
}
