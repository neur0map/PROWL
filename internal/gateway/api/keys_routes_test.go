package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/neur0map/prowl/internal/gateway"
)

// session claims the dashboard and returns a bearer for the gated routes.
func session(t *testing.T, s *Server) string {
	t.Helper()
	return claimDashboard(t, s, "op@example.com", "correct horse battery")
}

func authed(token string) map[string]string {
	return map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/json",
	}
}

// TestKeysSurfaceRequiresASession keeps the credential surface behind the one
// gate that is allowed to end a session, so an unauthenticated read is refused
// as a real session failure rather than leaking key metadata.
func TestKeysSurfaceRequiresASession(t *testing.T) {
	t.Parallel()
	s := testServer(t, Options{})

	resp, body := do(t, s, http.MethodGet, "/api/keys", "", nil)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	require.Equal(t, string(TypeAuthentication), errorType(t, body))
}

// TestListedKeyIsMaskedAndCarriesNoPlaintext is the leak test: a key list must
// mask every credential and never emit the plaintext anywhere in the JSON.
func TestListedKeyIsMaskedAndCarriesNoPlaintext(t *testing.T) {
	t.Parallel()
	s := testServer(t, Options{})
	token := session(t, s)

	const secret = "sk-supersecret-plaintext-0xDEADBEEF01234567"
	id, err := s.engine.Vault().Add("groq", secret, gateway.AddOptions{Label: "prod"})
	require.NoError(t, err)

	resp, body := do(t, s, http.MethodGet, "/api/keys", "", authed(token))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NotContains(t, body, secret, "the plaintext must never appear in a list")

	var keys []map[string]any
	require.NoError(t, json.Unmarshal([]byte(body), &keys))
	require.Len(t, keys, 1)
	require.EqualValues(t, id, keys[0]["id"])
	require.Equal(t, "groq", keys[0]["platform"])

	masked, _ := keys[0]["maskedKey"].(string)
	require.NotEmpty(t, masked)
	require.NotEqual(t, secret, masked)
	// cooldowns is always an array, never null: the client maps over it.
	_, ok := keys[0]["cooldowns"].([]any)
	require.True(t, ok, "cooldowns must serialise as an array")
}

// TestRevealReturnsPlaintextOnlyOnItsEndpoint proves the reveal endpoint is the
// one path that hands back a credential in the clear.
func TestRevealReturnsPlaintextOnlyOnItsEndpoint(t *testing.T) {
	t.Parallel()
	s := testServer(t, Options{})
	token := session(t, s)

	const secret = "sk-reveal-me-9f8e7d6c5b4a"
	id, err := s.engine.Vault().Add("groq", secret, gateway.AddOptions{})
	require.NoError(t, err)

	resp, body := do(t, s, http.MethodPost, keyPath(id, "/reveal"), "", authed(token))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var out map[string]string
	require.NoError(t, json.Unmarshal([]byte(body), &out))
	require.Equal(t, secret, out["key"], "reveal must return the exact plaintext")
}

// TestAddKeyRejectsUnknownPlatform: an add for a platform no provider serves is
// refused with a message that names the offending platform.
func TestAddKeyRejectsUnknownPlatform(t *testing.T) {
	t.Parallel()
	s := testServer(t, Options{})
	token := session(t, s)

	resp, body := do(t, s, http.MethodPost, "/api/keys",
		`{"platform":"totally-made-up","key":"sk-x"}`, authed(token))
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Contains(t, body, "totally-made-up", "the rejection must name the bad platform")
}

// TestValidatingUpstreamRejectedKeyIsNotAnAuthError is the trap this slice
// turns on: probing a key relays the provider's verdict. A provider-confirmed
// bad credential must come back as a 200 saying status "error", NEVER as a 401
// carrying authentication_error — that single combination is what signs the
// operator out, and testing a bad key must not do it.
func TestValidatingUpstreamRejectedKeyIsNotAnAuthError(t *testing.T) {
	t.Parallel()
	s := testServer(t, Options{})
	token := session(t, s)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Invalid API key"}}`))
	}))
	defer upstream.Close()

	id, err := s.engine.Vault().Add("custom", "sk-bad", gateway.AddOptions{BaseURL: upstream.URL})
	require.NoError(t, err)

	resp, body := do(t, s, http.MethodPost, healthCheckPath(id), "", authed(token))
	require.Equal(t, http.StatusOK, resp.StatusCode, "a relayed 401 must not become a 401 here")
	require.NotContains(t, body, string(TypeAuthentication),
		"a relayed upstream rejection must not carry authentication_error")

	var out map[string]any
	require.NoError(t, json.Unmarshal([]byte(body), &out))
	require.Equal(t, string(gateway.StatusError), out["status"])

	// A single confirmed-bad probe records the verdict but leaves the key
	// enabled: it takes three in a row to auto-disable.
	row, ok, err := s.engine.Vault().Get(context.Background(), id)
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, row.Enabled, "one bad probe must not disable the key")
	require.Equal(t, gateway.StatusError, row.Status)
}

// TestInconclusiveValidationDoesNotDisableTheKey: a transport failure says
// nothing about the credential, so repeated unreachable probes must never flip
// the status or climb the auto-disable counter.
func TestInconclusiveValidationDoesNotDisableTheKey(t *testing.T) {
	t.Parallel()
	s := testServer(t, Options{})
	token := session(t, s)

	// A server that is closed immediately: every probe fails to connect, which
	// is inconclusive, not a rejection.
	down := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	unreachable := down.URL
	down.Close()

	id, err := s.engine.Vault().Add("custom", "sk-maybe", gateway.AddOptions{BaseURL: unreachable})
	require.NoError(t, err)

	for range 4 {
		resp, body := do(t, s, http.MethodPost, healthCheckPath(id), "", authed(token))
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var out map[string]any
		require.NoError(t, json.Unmarshal([]byte(body), &out))
		require.Equal(t, string(gateway.StatusUnknown), out["status"],
			"an inconclusive probe must leave the status untouched")
	}

	row, ok, err := s.engine.Vault().Get(context.Background(), id)
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, row.Enabled, "an unreachable provider must never disable a key")
	require.Equal(t, gateway.StatusUnknown, row.Status)
	require.Equal(t, 0, row.ConsecutiveFailures, "inconclusive probes must not climb the disable counter")
}

// TestDeletingAKeyRemovesItsRows deletes the credential and, through the
// ON DELETE CASCADE on models.key_id, its custom relay models with it.
func TestDeletingAKeyRemovesItsRows(t *testing.T) {
	t.Parallel()
	s := testServer(t, Options{})
	token := session(t, s)

	id, err := s.engine.Vault().Add("custom", "sk-c", gateway.AddOptions{BaseURL: "http://127.0.0.1:59999/v1"})
	require.NoError(t, err)
	_, err = s.engine.DB().Exec(
		`INSERT INTO models(platform, model_id, display_name, key_id, endpoint_scope) VALUES('custom','m1','M1',?,'ep')`, id)
	require.NoError(t, err)

	resp, _ := do(t, s, http.MethodDelete, keyPath(id, ""), "", authed(token))
	require.Equal(t, http.StatusOK, resp.StatusCode)

	_, found, err := s.engine.Vault().Get(context.Background(), id)
	require.NoError(t, err)
	require.False(t, found, "the key row must be gone")

	var n int
	require.NoError(t, s.engine.DB().QueryRow(`SELECT COUNT(*) FROM models WHERE key_id = ?`, id).Scan(&n))
	require.Equal(t, 0, n, "the key's custom models must cascade away with it")
}

// TestClearingCooldownsLiftsOnlyHeuristicBenches: the clear operation withdraws
// our own guesses and nothing else, so a provider-stated or out-of-credit bench
// survives the button press.
func TestClearingCooldownsLiftsOnlyHeuristicBenches(t *testing.T) {
	t.Parallel()
	s := testServer(t, Options{})
	token := session(t, s)

	id, err := s.engine.Vault().Add("groq", "sk-g", gateway.AddOptions{})
	require.NoError(t, err)
	for _, m := range []string{"llama-3", "mixtral"} {
		_, err := s.engine.DB().Exec(
			`INSERT INTO models(platform, model_id, display_name) VALUES('groq',?,?)`, m, m)
		require.NoError(t, err)
	}

	cds := s.engine.Cooldowns()
	cds.Bench(gateway.QuotaKey("groq", "llama-3", id), time.Hour, gateway.SourceHeuristic)
	cds.Bench(gateway.QuotaKey("groq", "mixtral", id), 24*time.Hour, gateway.SourceCredit)

	resp, body := do(t, s, http.MethodDelete, keyPath(id, "/cooldowns"), "", authed(token))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var out map[string]int
	require.NoError(t, json.Unmarshal([]byte(body), &out))
	require.Equal(t, 1, out["cleared"], "only the heuristic bench is ours to lift")

	_, heuristicStillActive := cds.Active(gateway.QuotaKey("groq", "llama-3", id))
	require.False(t, heuristicStillActive, "the heuristic bench must be gone")
	credit, creditStillActive := cds.Active(gateway.QuotaKey("groq", "mixtral", id))
	require.True(t, creditStillActive, "an out-of-credit bench must survive a clear")
	require.Equal(t, gateway.SourceCredit, credit.Source)
}

// TestListedKeySurfacesAnActiveCooldown proves the list explains why a healthy
// enabled key is idle: its active bench appears in the cooldowns array with a
// live remaining time.
func TestListedKeySurfacesAnActiveCooldown(t *testing.T) {
	t.Parallel()
	s := testServer(t, Options{})
	token := session(t, s)

	id, err := s.engine.Vault().Add("groq", "sk-idle", gateway.AddOptions{})
	require.NoError(t, err)
	_, err = s.engine.DB().Exec(`INSERT INTO models(platform, model_id, display_name) VALUES('groq','llama-3','Llama 3')`)
	require.NoError(t, err)
	s.engine.Cooldowns().Bench(gateway.QuotaKey("groq", "llama-3", id), 10*time.Minute, gateway.SourceHeuristic)

	_, body := do(t, s, http.MethodGet, "/api/keys", "", authed(token))
	var keys []struct {
		Cooldowns []struct {
			ModelID     string `json:"modelId"`
			RemainingMs int64  `json:"remainingMs"`
		} `json:"cooldowns"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &keys))
	require.Len(t, keys, 1)
	require.Len(t, keys[0].Cooldowns, 1)
	require.Equal(t, "llama-3", keys[0].Cooldowns[0].ModelID)
	require.Positive(t, keys[0].Cooldowns[0].RemainingMs, "an active bench must report time left")
}

// TestProviderCatalogueListsEveryRegisteredPlatform: the directory must show
// every built-in provider and exclude the per-key custom placeholder.
func TestProviderCatalogueListsEveryRegisteredPlatform(t *testing.T) {
	t.Parallel()
	s := testServer(t, Options{})
	token := session(t, s)

	_, body := do(t, s, http.MethodGet, "/api/keys/providers", "", authed(token))
	var out struct {
		Providers []struct {
			Platform string `json:"platform"`
			Name     string `json:"name"`
		} `json:"providers"`
		Summary map[string]int `json:"summary"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &out))

	registered := map[string]bool{}
	for _, p := range s.engine.Registry().All() {
		if p.Platform() == "custom" {
			continue // a per-key placeholder, excluded from the checklist
		}
		registered[p.Platform()] = true
	}
	require.Equal(t, len(registered), len(out.Providers), "every registered platform must be listed")

	got := map[string]bool{}
	for _, p := range out.Providers {
		require.NotEqual(t, "custom", p.Platform, "custom is not a checklist provider")
		got[p.Platform] = true
	}
	for platform := range registered {
		require.True(t, got[platform], "missing platform %q from the catalogue", platform)
	}
	require.Equal(t, len(out.Providers), out.Summary["total"])
}

// TestHealthRollupReportsHonestNullsAndDegradation covers the health rollup's
// two quiet contracts: a never-probed key reports lastCheckedAt as null rather
// than 0, and the ported-out quota view is an honest empty array while the
// degradation snapshot is computed live but never enters degraded mode.
func TestHealthRollupReportsHonestNullsAndDegradation(t *testing.T) {
	t.Parallel()
	s := testServer(t, Options{})
	token := session(t, s)

	_, err := s.engine.Vault().Add("groq", "sk-h", gateway.AddOptions{Label: "prod"})
	require.NoError(t, err)

	_, body := do(t, s, http.MethodGet, "/api/health", "", authed(token))
	var out struct {
		Platforms []struct {
			Platform    string `json:"platform"`
			HasProvider bool   `json:"hasProvider"`
			TotalKeys   int    `json:"totalKeys"`
			UnknownKeys int    `json:"unknownKeys"`
		} `json:"platforms"`
		Keys []struct {
			Status        string `json:"status"`
			LastCheckedAt *int64 `json:"lastCheckedAt"`
		} `json:"keys"`
		QuotaStates []any `json:"quotaStates"`
		Degradation struct {
			State            string  `json:"state"`
			TotalProviders   int     `json:"totalProviders"`
			HealthyProviders int     `json:"healthyProviders"`
			Ratio            float64 `json:"ratio"`
		} `json:"degradation"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &out))

	require.Len(t, out.Platforms, 1)
	require.Equal(t, "groq", out.Platforms[0].Platform)
	require.True(t, out.Platforms[0].HasProvider)
	require.Equal(t, 1, out.Platforms[0].TotalKeys)
	require.Equal(t, 1, out.Platforms[0].UnknownKeys)

	require.Len(t, out.Keys, 1)
	require.Equal(t, "unknown", out.Keys[0].Status)
	require.Nil(t, out.Keys[0].LastCheckedAt, "a never-probed key must report null, not 0")

	require.NotNil(t, out.QuotaStates)
	require.Empty(t, out.QuotaStates, "the quota view is honestly empty, not fabricated")

	// An unknown key counts as usable, so the sole provider is healthy and the
	// gateway is not degraded.
	require.Equal(t, "normal", out.Degradation.State)
	require.Equal(t, 1, out.Degradation.TotalProviders)
	require.Equal(t, 1, out.Degradation.HealthyProviders)
	require.Equal(t, 1.0, out.Degradation.Ratio)
}

func keyPath(id int64, suffix string) string {
	return "/api/keys/" + strconv.FormatInt(id, 10) + suffix
}

func healthCheckPath(id int64) string {
	return "/api/health/check/" + strconv.FormatInt(id, 10)
}
