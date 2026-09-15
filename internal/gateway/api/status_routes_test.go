package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestPremiumReportsUnsubscribedNotFound: every page mounts the premium query,
// so the route must answer 200 with a well-formed unsubscribed status — not the
// 404 the gap left, and not a fabricated subscription — so the page renders its
// free-tier path.
func TestPremiumReportsUnsubscribedNotFound(t *testing.T) {
	t.Parallel()
	s := testServer(t, Options{})
	headers := settingsSession(t, s)

	resp, body := do(t, s, http.MethodGet, "/api/premium", "", headers)
	require.Equal(t, http.StatusOK, resp.StatusCode, "premium must not 404")

	var status premiumStatus
	require.NoError(t, json.Unmarshal([]byte(body), &status), "body was %q", body)
	require.False(t, status.HasKey, "a local install has no licence key")
	require.Nil(t, status.MaskedKey, "no key means no masked key")
	require.Nil(t, status.License, "no subscription means a null licence, not a fake one")
	require.Nil(t, status.Catalog.AppliedTier, "a bundled catalog reports no applied live tier")
	require.Empty(t, status.SiteURL, "the rebranded page renders no external premium links, so siteUrl is empty")
	require.Empty(t, status.Catalog.BaseURL, "no live catalog service is contacted, so no base url is leaked")
}

// TestPremiumMutationsRefuseWithoutSigningOut: the write endpoints refuse
// clearly. The refusals must never carry the authentication type on a 401,
// which would sign the operator out; here they are 4xx with a useful message
// the client renders inline, and DELETE/sync stay graceful.
func TestPremiumMutationsRefuseWithoutSigningOut(t *testing.T) {
	t.Parallel()
	s := testServer(t, Options{})
	headers := settingsSession(t, s)

	activateResp, activateBody := do(t, s, http.MethodPost, "/api/premium/key",
		`{"key":"fla_example_license_key_0001"}`, headers)
	require.Equal(t, http.StatusBadRequest, activateResp.StatusCode)
	require.Contains(t, activateBody, "not available", "the refusal must say why")
	require.NotContains(t, activateBody, "authentication_error",
		"a licence refusal must never read as a session failure")

	portalResp, portalBody := do(t, s, http.MethodPost, "/api/premium/portal", "", headers)
	require.Equal(t, http.StatusBadRequest, portalResp.StatusCode)
	require.Contains(t, portalBody, "No license key configured.")
	require.NotContains(t, portalBody, "authentication_error")

	delResp, delBody := do(t, s, http.MethodDelete, "/api/premium/key", "", headers)
	require.Equal(t, http.StatusOK, delResp.StatusCode, "removing a key that never existed is idempotent")
	var afterDelete premiumStatus
	require.NoError(t, json.Unmarshal([]byte(delBody), &afterDelete))
	require.False(t, afterDelete.HasKey)

	syncResp, syncBody := do(t, s, http.MethodPost, "/api/premium/sync", "", headers)
	require.Equal(t, http.StatusOK, syncResp.StatusCode, "sync must succeed so the button shows no error")
	var afterSync struct {
		HasKey bool           `json:"hasKey"`
		Sync   map[string]any `json:"sync"`
	}
	require.NoError(t, json.Unmarshal([]byte(syncBody), &afterSync))
	require.False(t, afterSync.HasKey)
	require.NotNil(t, afterSync.Sync, "the reference returns a sync result the client tolerates")
}

// TestFreeTierGroupsPoolsWithScaledBudgetAndQuota: two models on one platform
// collapse into a single provider pool, the documented budget scales by usable
// keys, and the pool carries the provider's observed token headroom.
func TestFreeTierGroupsPoolsWithScaledBudgetAndQuota(t *testing.T) {
	t.Parallel()
	s := testServer(t, Options{})
	headers := settingsSession(t, s)
	db := s.engine.DB()
	now := time.Now().Unix()

	_, err := db.Exec(`INSERT INTO api_keys(platform, encrypted_key, iv, auth_tag, status, enabled, created_at)
		VALUES('groq','x','x','x','healthy',1,?)`, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO models(platform, model_id, display_name, monthly_token_budget, enabled)
		VALUES('groq','llama-3.1-8b','Llama 3.1 8B','~120M',1)`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO models(platform, model_id, display_name, monthly_token_budget, enabled)
		VALUES('groq','llama-3.3-70b','Llama 3.3 70B','~30M',1)`)
	require.NoError(t, err)
	reset := now + 3600
	// Pool key is the credential pool (platform/keyId), the way Prowl records
	// observed quota.
	_, err = db.Exec(`INSERT INTO provider_quota_state(quota_pool_key, platform, tokens_limit, tokens_remaining, resets_at, observed_at)
		VALUES('groq/1','groq',1000000,250000,?,?)`, reset, now)
	require.NoError(t, err)

	resp, body := do(t, s, http.MethodGet, "/api/free-tier", "", headers)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var out freeTierResponse
	require.NoError(t, json.Unmarshal([]byte(body), &out), "body was %q", body)
	require.Len(t, out.Pools, 1, "both models share one groq pool")

	pool := out.Pools[0]
	require.Equal(t, "groq::account", pool.PoolKey)
	require.Equal(t, "groq", pool.Platform)
	require.Equal(t, 2, pool.ModelCount)
	require.ElementsMatch(t, []string{"llama-3.1-8b", "llama-3.3-70b"}, pool.MemberModelIDs)
	require.Equal(t, "documented", pool.Kind)
	require.Equal(t, "~120M", pool.BestLabel, "the largest documented label wins")
	require.Equal(t, 1, pool.KeyCount)
	require.Equal(t, float64(120_000_000), pool.DocumentedBudget, "budget is the pool max scaled by one usable key")

	require.NotNil(t, pool.Quota)
	require.Equal(t, "tokens", pool.Quota.Metric, "a token axis is preferred over a request counter")
	require.NotNil(t, pool.Quota.Limit)
	require.Equal(t, int64(1_000_000), *pool.Quota.Limit)
	require.NotNil(t, pool.Quota.Remaining)
	require.Equal(t, int64(250_000), *pool.Quota.Remaining)
	require.NotNil(t, pool.Quota.ResetAt)
	require.Equal(t, 1, pool.Quota.KeyCount)

	require.Equal(t, 1, out.Summary.PoolCount)
	require.Equal(t, float64(120_000_000), out.Summary.DocumentedMonthlyTokens)
	require.Zero(t, out.Summary.CreditsBasedPools)
	require.Zero(t, out.Summary.UnpublishedPools)
}

// TestFreeTierQuotaNullsAndAbsenceAreHonest: an axis a provider does not publish
// stays null instead of reading as zero, and a pool with no observation at all
// carries a null quota rather than a zeroed object.
func TestFreeTierQuotaNullsAndAbsenceAreHonest(t *testing.T) {
	t.Parallel()
	s := testServer(t, Options{})
	headers := settingsSession(t, s)
	db := s.engine.DB()
	now := time.Now().Unix()

	// groq: a remaining reading with no published limit.
	_, err := db.Exec(`INSERT INTO api_keys(platform, encrypted_key, iv, auth_tag, status, enabled, created_at)
		VALUES('groq','x','x','x','healthy',1,?)`, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO models(platform, model_id, display_name, monthly_token_budget, enabled)
		VALUES('groq','llama-3.1-8b','Llama 3.1 8B','~120M',1)`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO provider_quota_state(quota_pool_key, platform, tokens_limit, tokens_remaining, resets_at, observed_at)
		VALUES('groq/1','groq',NULL,500000,NULL,?)`, now)
	require.NoError(t, err)

	// cerebras: a usable key and a model, but no observation yet.
	_, err = db.Exec(`INSERT INTO api_keys(platform, encrypted_key, iv, auth_tag, status, enabled, created_at)
		VALUES('cerebras','x','x','x','healthy',1,?)`, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO models(platform, model_id, display_name, monthly_token_budget, enabled)
		VALUES('cerebras','llama-3.3-70b','Llama 3.3 70B','~5M',1)`)
	require.NoError(t, err)

	resp, body := do(t, s, http.MethodGet, "/api/free-tier", "", headers)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var out freeTierResponse
	require.NoError(t, json.Unmarshal([]byte(body), &out), "body was %q", body)

	byKey := map[string]freeTierPool{}
	for _, p := range out.Pools {
		byKey[p.PoolKey] = p
	}

	groq, ok := byKey["groq::account"]
	require.True(t, ok)
	require.NotNil(t, groq.Quota)
	require.Nil(t, groq.Quota.Limit, "an unpublished limit must stay null, never zero")
	require.NotNil(t, groq.Quota.Remaining)
	require.Equal(t, int64(500000), *groq.Quota.Remaining)
	require.Nil(t, groq.Quota.ResetAt, "no reset published means null")

	cerebras, ok := byKey["cerebras::shared"]
	require.True(t, ok)
	require.Nil(t, cerebras.Quota, "no observation means a null quota, not a zeroed one")
}

// TestCacheStatsZeroedNotAbsent: Prowl has no response cache, so the readout is
// present, disabled and zeroed. The Analytics page needs every field present to
// key its card on `enabled` without reading undefined counters.
func TestCacheStatsZeroedNotAbsent(t *testing.T) {
	t.Parallel()
	s := testServer(t, Options{})
	headers := settingsSession(t, s)

	resp, body := do(t, s, http.MethodGet, "/api/cache/stats", "", headers)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(body), &raw))
	for _, field := range []string{
		"enabled", "entries", "totalHits", "estimatedRequestsSaved",
		"savedPromptTokens", "savedCompletionTokens", "lookupHits", "lookupMisses",
		"hitRate", "savedTokens",
	} {
		require.Contains(t, raw, field, "cache stats must be zeroed, not absent")
	}

	var stats cacheStats
	require.NoError(t, json.Unmarshal([]byte(body), &stats))
	require.False(t, stats.Enabled)
	require.Zero(t, stats.SavedTokens)
	require.Zero(t, stats.HitRate)
	require.Zero(t, stats.Entries)
}

// TestCompressionStatsDisabled: with no compression pipeline, the config is the
// disabled default and every counter is zero, with byMode/engines as empty
// objects (never null, which the client cannot iterate).
func TestCompressionStatsDisabled(t *testing.T) {
	t.Parallel()
	s := testServer(t, Options{})
	headers := settingsSession(t, s)

	resp, body := do(t, s, http.MethodGet, "/api/compression/stats", "", headers)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var out struct {
		Config         map[string]any  `json:"config"`
		Requests       int             `json:"requests"`
		SavingsPercent float64         `json:"savingsPercent"`
		EstSavedTokens int             `json:"estSavedTokens"`
		ByMode         json.RawMessage `json:"byMode"`
		Engines        json.RawMessage `json:"engines"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &out), "body was %q", body)
	require.Zero(t, out.Requests)
	require.Zero(t, out.SavingsPercent)
	require.Zero(t, out.EstSavedTokens)
	require.Equal(t, "off", out.Config["mode"], "compression is off on a local install")
	require.Equal(t, "{}", string(out.ByMode), "byMode is an empty object, not null")
	require.Equal(t, "{}", string(out.Engines), "engines is an empty object, not null")
}

// TestCompressionPreviewReturnsInputUnchanged: the preview echoes exactly what
// would be sent, with a zero saving, rather than pretending to compress.
func TestCompressionPreviewReturnsInputUnchanged(t *testing.T) {
	t.Parallel()
	s := testServer(t, Options{})
	headers := settingsSession(t, s)

	req := `{"mode":"standard","messages":[{"role":"user","content":"hello world"}]}`
	resp, body := do(t, s, http.MethodPost, "/api/compression/preview", req, headers)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var out struct {
		Mode       string          `json:"mode"`
		Original   json.RawMessage `json:"original"`
		Compressed json.RawMessage `json:"compressed"`
		Diff       struct {
			BeforeChars int `json:"beforeChars"`
			AfterChars  int `json:"afterChars"`
			SavedChars  int `json:"savedChars"`
		} `json:"diff"`
		Stats struct {
			EstSavedTokens int `json:"estSavedTokens"`
			OriginalChars  int `json:"originalChars"`
		} `json:"stats"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &out), "body was %q", body)
	require.Equal(t, "standard", out.Mode, "the requested mode is echoed")
	require.True(t, bytes.Equal(out.Original, out.Compressed), "the output must be the untouched input")
	require.Zero(t, out.Diff.SavedChars, "nothing is compressed, so nothing is saved")
	require.Equal(t, out.Diff.BeforeChars, out.Diff.AfterChars)
	require.Equal(t, 11, out.Stats.OriginalChars, `"hello world" is 11 characters`)
	require.Zero(t, out.Stats.EstSavedTokens)
}

// TestCompressionPreviewAcceptsStringBody: the {body:"<string>"} form the
// settings dialog sends for plain text becomes a single user message, echoed
// unchanged.
func TestCompressionPreviewAcceptsStringBody(t *testing.T) {
	t.Parallel()
	s := testServer(t, Options{})
	headers := settingsSession(t, s)

	resp, body := do(t, s, http.MethodPost, "/api/compression/preview",
		`{"mode":"off","body":"just text"}`, headers)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var out struct {
		Compressed []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"compressed"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &out), "body was %q", body)
	require.Len(t, out.Compressed, 1)
	require.Equal(t, "user", out.Compressed[0].Role)
	require.Equal(t, "just text", out.Compressed[0].Content)
}

// TestCompressionPreviewRejectsEmpty: a request with no usable messages is a
// 400 with the validation type, the same as the reference.
func TestCompressionPreviewRejectsEmpty(t *testing.T) {
	t.Parallel()
	s := testServer(t, Options{})
	headers := settingsSession(t, s)

	resp, body := do(t, s, http.MethodPost, "/api/compression/preview", `{}`, headers)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Equal(t, string(TypeInvalidRequest), errorType(t, body))
}

// TestStatusRoutesRequireASession: every status route the reference gates
// behind requireAuth answers a properly typed 401 when there is no session, so
// the client signs out for a real session failure and only then.
func TestStatusRoutesRequireASession(t *testing.T) {
	t.Parallel()
	s := testServer(t, Options{})

	for _, path := range []string{
		"/api/premium", "/api/free-tier", "/api/cache/stats",
		"/api/compression/stats",
	} {
		resp, body := do(t, s, http.MethodGet, path, "", nil)
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "path %s", path)
		require.Equal(t, string(TypeAuthentication), errorType(t, body), "path %s", path)
	}
}
