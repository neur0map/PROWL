package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// routingServer builds a session-authenticated surface for the routing family.
// It presents the token in the session header rather than as a bearer, which
// is the transport the dashboard itself uses.
func routingServer(t *testing.T) (*Server, map[string]string) {
	t.Helper()
	s := testServer(t, Options{})
	token := claimDashboard(t, s, "op@example.com", "correct horse battery staple")
	return s, map[string]string{sessionHeader: token, "Content-Type": "application/json"}
}

// seedModel adds a catalogue model and makes sure its platform has a usable
// key. A chain candidate is a (model, key) pair, so a model whose platform
// holds no credential is correctly absent from every chain.
func seedModel(t *testing.T, db *sql.DB, platform, modelID, name, sizeLabel string) int64 {
	t.Helper()
	res, err := db.Exec(
		`INSERT INTO models (platform, model_id, display_name, size_label, enabled) VALUES (?, ?, ?, ?, 1)`,
		platform, modelID, name, sizeLabel)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)

	var keys int
	require.NoError(t, db.QueryRow(
		`SELECT COUNT(*) FROM api_keys WHERE platform = ?`, platform).Scan(&keys))
	if keys == 0 {
		_, err = db.Exec(`
			INSERT INTO api_keys (platform, label, encrypted_key, iv, auth_tag, status, enabled, created_at)
			VALUES (?, 'test', 'x', 'y', 'z', 'healthy', 1, 0)`, platform)
		require.NoError(t, err)
	}
	return id
}

func seedFallback(t *testing.T, db *sql.DB, modelDBID int64, position int64, enabled bool) {
	t.Helper()
	e := 0
	if enabled {
		e = 1
	}
	_, err := db.Exec(`INSERT INTO fallback_config (model_db_id, position, enabled) VALUES (?, ?, ?)`,
		modelDBID, position, e)
	require.NoError(t, err)
}

func seedProfile(t *testing.T, db *sql.DB, name string) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO profiles (name, active, created_at) VALUES (?, 0, ?)`, name, time.Now().Unix())
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	return id
}

// TestRoutingStrategyPersistsAndReadsBack: a valid strategy round-trips through
// the settings table and is reported on the next read.
func TestRoutingStrategyPersistsAndReadsBack(t *testing.T) {
	t.Parallel()
	s, auth := routingServer(t)

	resp, body := do(t, s, http.MethodPut, "/api/fallback/routing", `{"strategy":"smartest"}`, auth)
	require.Equal(t, http.StatusOK, resp.StatusCode, "body was %q", body)

	resp, body = do(t, s, http.MethodGet, "/api/fallback/routing", "", auth)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var data struct {
		Strategy string      `json:"strategy"`
		Weights  *weightsOut `json:"weights"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &data))
	require.Equal(t, "smartest", data.Strategy)
	// smartest is a bandit preset, so weights are the preset vector, not null.
	require.NotNil(t, data.Weights)
	require.InDelta(t, 0.55, data.Weights.Intelligence, 1e-9)
}

// TestUnknownStrategyIsRejected: a typo is a 400, not a silent coercion to the
// default — the operator must never think they set one thing while the router
// does another. The stored strategy is left untouched.
func TestUnknownStrategyIsRejected(t *testing.T) {
	t.Parallel()
	s, auth := routingServer(t)

	_, _ = do(t, s, http.MethodPut, "/api/fallback/routing", `{"strategy":"reliable"}`, auth)

	resp, body := do(t, s, http.MethodPut, "/api/fallback/routing", `{"strategy":"turbo"}`, auth)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Equal(t, string(TypeInvalidRequest), errorType(t, body))

	_, body = do(t, s, http.MethodGet, "/api/fallback/routing", "", auth)
	var data struct {
		Strategy string `json:"strategy"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &data))
	require.Equal(t, "reliable", data.Strategy, "a rejected strategy must not overwrite the saved one")
}

// TestCustomWeightsNormalizeAndRejectAllZero: a custom vector is renormalised to
// sum one, and an all-zero vector is refused rather than dividing by zero.
func TestCustomWeightsNormalizeAndRejectAllZero(t *testing.T) {
	t.Parallel()
	s, auth := routingServer(t)

	resp, body := do(t, s, http.MethodPut, "/api/fallback/routing",
		`{"strategy":"custom","weights":{"reliability":2,"speed":1,"intelligence":1}}`, auth)
	require.Equal(t, http.StatusOK, resp.StatusCode, "body was %q", body)

	_, body = do(t, s, http.MethodGet, "/api/fallback/routing", "", auth)
	var data struct {
		CustomWeights weightsOut `json:"customWeights"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &data))
	require.InDelta(t, 0.5, data.CustomWeights.Reliability, 1e-9)
	require.InDelta(t, 0.25, data.CustomWeights.Speed, 1e-9)
	require.InDelta(t, 1.0,
		data.CustomWeights.Reliability+data.CustomWeights.Speed+data.CustomWeights.Intelligence, 1e-9)

	resp, _ = do(t, s, http.MethodPut, "/api/fallback/routing",
		`{"strategy":"custom","weights":{"reliability":0,"speed":0,"intelligence":0}}`, auth)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// TestChainReadPrefersActiveProfileOverGlobalConfig: with a profile active the
// read reflects the profile's membership and order, not the global
// fallback_config, so the dashboard never shows one chain's rows under another.
func TestChainReadPrefersActiveProfileOverGlobalConfig(t *testing.T) {
	t.Parallel()
	s, auth := routingServer(t)
	db := s.engine.DB()

	m1 := seedModel(t, db, "p", "model-1", "One", "Large")
	m2 := seedModel(t, db, "p", "model-2", "Two", "Large")
	m3 := seedModel(t, db, "p", "model-3", "Three", "Large")
	// Global chain order: m1, m2 (m3 not in it).
	seedFallback(t, db, m1, 1, true)
	seedFallback(t, db, m2, 2, true)

	type feRow struct {
		ModelDBID int64 `json:"modelDbId"`
		Enabled   bool  `json:"enabled"`
	}
	enabledOrder := func(body string) []int64 {
		var rows []feRow
		require.NoError(t, json.Unmarshal([]byte(body), &rows))
		var out []int64
		for _, r := range rows {
			if r.Enabled {
				out = append(out, r.ModelDBID)
			}
		}
		return out
	}

	// Before any profile is active the read is the global chain.
	_, body := do(t, s, http.MethodGet, "/api/fallback", "", auth)
	require.Equal(t, []int64{m1, m2}, enabledOrder(body))

	// A profile with a different membership and order: m3, then m1.
	p := seedProfile(t, db, "coding")
	_, err := db.Exec(`INSERT INTO profile_models (profile_id, model_db_id, position) VALUES (?, ?, 1), (?, ?, 2)`,
		p, m3, p, m1)
	require.NoError(t, err)

	resp, body := do(t, s, http.MethodPost, "/api/profiles/active", fmt.Sprintf(`{"profileId":%d}`, p), auth)
	require.Equal(t, http.StatusOK, resp.StatusCode, "body was %q", body)

	_, body = do(t, s, http.MethodGet, "/api/fallback", "", auth)
	require.Equal(t, []int64{m3, m1}, enabledOrder(body),
		"the active profile's chain must win over the global fallback_config")
}

// TestProfileReorderIsAtomicAndPositional: a reorder numbers positions densely
// with no duplicates and never renumbers models.id; a reorder that would collide
// rolls back whole, leaving the prior membership intact.
func TestProfileReorderIsAtomicAndPositional(t *testing.T) {
	t.Parallel()
	s, auth := routingServer(t)
	db := s.engine.DB()

	m1 := seedModel(t, db, "p", "model-1", "One", "Large")
	m2 := seedModel(t, db, "p", "model-2", "Two", "Large")
	m3 := seedModel(t, db, "p", "model-3", "Three", "Large")
	p := seedProfile(t, db, "coding")

	positions := func() map[int64]int64 {
		rows, err := db.Query(`SELECT model_db_id, position FROM profile_models WHERE profile_id = ?`, p)
		require.NoError(t, err)
		defer rows.Close()
		out := map[int64]int64{}
		for rows.Next() {
			var id, pos int64
			require.NoError(t, rows.Scan(&id, &pos))
			out[id] = pos
		}
		return out
	}

	body := fmt.Sprintf(`[{"modelDbId":%d,"priority":1,"enabled":true},{"modelDbId":%d,"priority":2,"enabled":true},{"modelDbId":%d,"priority":3,"enabled":true}]`, m3, m1, m2)
	resp, rb := do(t, s, http.MethodPut, fmt.Sprintf("/api/profiles/%d/reorder", p), body, auth)
	require.Equal(t, http.StatusOK, resp.StatusCode, "body was %q", rb)

	got := positions()
	require.Equal(t, map[int64]int64{m3: 1, m1: 2, m2: 3}, got)
	// Positions are unique.
	seen := map[int64]bool{}
	for _, pos := range got {
		require.False(t, seen[pos], "duplicate position %d", pos)
		seen[pos] = true
	}
	// models.id is addressed by the chain tables, so it must be untouched.
	var ids []int64
	rows, err := db.Query(`SELECT id FROM models ORDER BY id`)
	require.NoError(t, err)
	for rows.Next() {
		var id int64
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	rows.Close()
	require.Equal(t, []int64{m1, m2, m3}, ids)

	// A duplicate model id collides on the (profile_id, model_db_id) key; the
	// whole write must roll back, leaving the prior membership exactly.
	bad := fmt.Sprintf(`[{"modelDbId":%d,"priority":1,"enabled":true},{"modelDbId":%d,"priority":2,"enabled":true}]`, m3, m3)
	resp, _ = do(t, s, http.MethodPut, fmt.Sprintf("/api/profiles/%d/reorder", p), bad, auth)
	require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	require.Equal(t, map[int64]int64{m3: 1, m1: 2, m2: 3}, positions(),
		"a failed reorder must not partially rewrite the chain")
}

// TestActivatingAProfileDeactivatesThePrevious: there is exactly one active
// profile, and activating a second one replaces the first.
func TestActivatingAProfileDeactivatesThePrevious(t *testing.T) {
	t.Parallel()
	s, auth := routingServer(t)
	db := s.engine.DB()

	a := seedProfile(t, db, "alpha")
	b := seedProfile(t, db, "beta")

	activeID := func() *int64 {
		_, body := do(t, s, http.MethodGet, "/api/profiles/active", "", auth)
		var data struct {
			ActiveProfileID *int64 `json:"activeProfileId"`
		}
		require.NoError(t, json.Unmarshal([]byte(body), &data))
		return data.ActiveProfileID
	}

	resp, _ := do(t, s, http.MethodPost, "/api/profiles/active", fmt.Sprintf(`{"profileId":%d}`, a), auth)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NotNil(t, activeID())
	require.Equal(t, a, *activeID())

	resp, _ = do(t, s, http.MethodPost, "/api/profiles/active", fmt.Sprintf(`{"profileId":%d}`, b), auth)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, b, *activeID())

	// The active flag column agrees: exactly one profile is active, and it is b.
	var count, activeCol int64
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM profiles WHERE active = 1`).Scan(&count))
	require.Equal(t, int64(1), count)
	require.NoError(t, db.QueryRow(`SELECT id FROM profiles WHERE active = 1`).Scan(&activeCol))
	require.Equal(t, b, activeCol)
}

// TestPenaltyInspectorReflectsAHitAndClearEmptiesIt: a recorded rate-limit hit
// shows up as a demotion, and the clear operation removes it.
func TestPenaltyInspectorReflectsAHitAndClearEmptiesIt(t *testing.T) {
	t.Parallel()
	s, auth := routingServer(t)
	db := s.engine.DB()
	m1 := seedModel(t, db, "p", "model-1", "One", "Large")

	s.engine.Penalties().RecordRateLimitHit(m1)

	type inspResp struct {
		Rows []struct {
			ModelDBID *int64 `json:"modelDbId"`
			Penalty   struct {
				Hits            int     `json:"hits"`
				Value           float64 `json:"value"`
				RateLimitFactor float64 `json:"rateLimitFactor"`
			} `json:"penalty"`
		} `json:"rows"`
	}

	_, body := do(t, s, http.MethodGet, "/api/fallback/penalty-inspector", "", auth)
	var got inspResp
	require.NoError(t, json.Unmarshal([]byte(body), &got))
	require.Len(t, got.Rows, 1)
	require.NotNil(t, got.Rows[0].ModelDBID)
	require.Equal(t, m1, *got.Rows[0].ModelDBID)
	require.Equal(t, 1, got.Rows[0].Penalty.Hits)
	require.Greater(t, got.Rows[0].Penalty.Value, 0.0)
	// A demotion damps the score below one but never removes the model.
	require.Less(t, got.Rows[0].Penalty.RateLimitFactor, 1.0)
	require.GreaterOrEqual(t, got.Rows[0].Penalty.RateLimitFactor, 0.4)

	resp, body := do(t, s, http.MethodDelete, "/api/fallback/penalty-inspector", "", auth)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var cleared struct {
		Penalties int `json:"penalties"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &cleared))
	require.GreaterOrEqual(t, cleared.Penalties, 1)

	_, body = do(t, s, http.MethodGet, "/api/fallback/penalty-inspector", "", auth)
	require.NoError(t, json.Unmarshal([]byte(body), &got))
	require.Empty(t, got.Rows, "clear must empty the inspector")
}

// TestScoringViewReportsPerAxisAndUsesStableExpectedReliability: the breakdown
// carries each axis and both guardrail multipliers separately, and reliability
// is the posterior mean — deterministic across refreshes, not a Thompson draw
// that would jitter with nothing changed.
func TestScoringViewReportsPerAxisAndUsesStableExpectedReliability(t *testing.T) {
	t.Parallel()
	s, auth := routingServer(t)
	db := s.engine.DB()
	m1 := seedModel(t, db, "p", "model-1", "One", "Large")
	seedFallback(t, db, m1, 1, true)

	now := time.Now().Unix()
	for range 9 {
		_, err := db.Exec(
			`INSERT INTO requests (created_at, platform, model_id, status, outcome, latency_ms) VALUES (?, 'p', 'model-1', 200, 'success', 100)`,
			now)
		require.NoError(t, err)
	}
	_, err := db.Exec(
		`INSERT INTO requests (created_at, platform, model_id, status, outcome, latency_ms) VALUES (?, 'p', 'model-1', 500, 'error', 100)`,
		now)
	require.NoError(t, err)

	type scoreRow struct {
		ModelDBID     int64   `json:"modelDbId"`
		Reliability   float64 `json:"reliability"`
		Speed         float64 `json:"speed"`
		Intelligence  float64 `json:"intelligence"`
		Headroom      float64 `json:"headroom"`
		RateLimit     float64 `json:"rateLimit"`
		Score         float64 `json:"score"`
		TotalRequests int     `json:"totalRequests"`
	}
	type routingResp struct {
		Scores []scoreRow `json:"scores"`
	}
	read := func() scoreRow {
		_, body := do(t, s, http.MethodGet, "/api/fallback/routing", "", auth)
		var data routingResp
		require.NoError(t, json.Unmarshal([]byte(body), &data))
		require.Len(t, data.Scores, 1)
		return data.Scores[0]
	}

	got := read()
	require.Equal(t, m1, got.ModelDBID)
	require.Equal(t, 10, got.TotalRequests)
	// Expected of Beta(9+1, 1+1) = 10/12; a Thompson draw would almost never
	// land on this exact value.
	require.InDelta(t, 10.0/12.0, got.Reliability, 1e-9)
	// Each axis and guardrail is a separate figure, not folded into the score.
	require.Greater(t, got.Speed, 0.0)
	require.GreaterOrEqual(t, got.Intelligence, 0.0)
	require.LessOrEqual(t, got.Intelligence, 1.0)
	require.InDelta(t, 1.0, got.Headroom, 1e-9)
	require.InDelta(t, 1.0, got.RateLimit, 1e-9)
	require.Greater(t, got.Score, 0.0)

	// Stable: a second read returns the identical reliability, because it is the
	// posterior mean rather than a sample.
	require.Equal(t, got.Reliability, read().Reliability)
}

// TestRateLimitUsageSerialisesAbsentWindowsAsNull: a window with no published
// limit is null, never {used:0,limit:0} — the client renders "—" for null and a
// number for a real limit, so a zero would misreport an unmetered axis as spent.
func TestRateLimitUsageSerialisesAbsentWindowsAsNull(t *testing.T) {
	t.Parallel()
	s, auth := routingServer(t)
	db := s.engine.DB()

	// A model with an RPM ceiling but no TPM ceiling.
	_, err := db.Exec(
		`INSERT INTO models (platform, model_id, display_name, rpm_limit, tpm_limit, enabled) VALUES ('p', 'model-1', 'One', 60, NULL, 1)`)
	require.NoError(t, err)
	// One eligible key so the model is routable and its RPM window is reported.
	_, err = db.Exec(
		`INSERT INTO api_keys (platform, label, encrypted_key, iv, auth_tag, status, enabled, created_at) VALUES ('p', 'k', 'x', 'y', 'z', 'healthy', 1, ?)`,
		time.Now().Unix())
	require.NoError(t, err)

	resp, body := do(t, s, http.MethodGet, "/api/fallback/rate-limit-usage", "", auth)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var data struct {
		Rows []struct {
			ModelDBID int64               `json:"modelDbId"`
			RPM       *rateLimitWindowOut `json:"rpm"`
			RPD       *rateLimitWindowOut `json:"rpd"`
			TPM       *rateLimitWindowOut `json:"tpm"`
		} `json:"rows"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &data))
	require.Len(t, data.Rows, 1)
	row := data.Rows[0]
	// RPM has a limit, so it is an object with the real ceiling.
	require.NotNil(t, row.RPM)
	require.Equal(t, int64(60), row.RPM.Limit)
	// TPM and RPD have no limit, so they are null, not a zeroed object.
	require.Nil(t, row.TPM)
	require.Nil(t, row.RPD)
	// And the wire form is literally null, not {"used":0,"limit":0}.
	require.Contains(t, body, `"tpm":null`)
	require.NotContains(t, body, `"tpm":{`)
}

// TestRelayedProviderFailureNeverEndsTheSession is the batch's load-bearing
// contract: only a genuine dashboard-session failure may carry
// TypeAuthentication. A session-gated routing route must answer a bad session
// with 401 authentication_error (so the client signs out), but must never emit
// that type for anything else.
func TestSessionGateOnlyAuthErrorIs401(t *testing.T) {
	t.Parallel()
	s, _ := routingServer(t)

	// No session token: the gate answers 401 authentication_error.
	resp, body := do(t, s, http.MethodGet, "/api/fallback/routing", "", map[string]string{"Content-Type": "application/json"})
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	require.Equal(t, string(TypeAuthentication), errorType(t, body))

	// A validation failure on a routing route is a 400 with a different type, so
	// a client testing input is never signed out.
	s2, auth := routingServer(t)
	resp, body = do(t, s2, http.MethodPut, "/api/fallback/routing", `{"strategy":"nope"}`, auth)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.NotEqual(t, string(TypeAuthentication), errorType(t, body))
}
