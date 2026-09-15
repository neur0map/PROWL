package api

import (
	"context"
	"log/slog"
	"math"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/neur0map/prowl/internal/gateway"
)

// The axis scorer turns the request trail into the four inputs the ordering
// needs. It is the half of the bandit that reads: scoring.go owns the maths,
// this owns the evidence.
const (
	// statsWindow and statsHalfLife weight recent behaviour over old: a
	// provider that failed last week must not outweigh one healthy since.
	// (router.ts:703-704.)
	statsWindow   = 7 * 24 * time.Hour
	statsHalfLife = 2 * 24 * time.Hour

	// statsCacheTTL bounds how often the trail is re-read. Scoring runs on
	// every request, and re-aggregating a week of rows each time would put a
	// SQL scan on the interactive path (CACHE_TTL_MS, router.ts:705).
	statsCacheTTL = 60 * time.Second
)

// routeStats is the decay-weighted evidence for one (platform, model, key).
type routeStats struct {
	successes float64
	failures  float64
	tokPerSec float64
	ttfbMs    float64
	hasTTFB   bool
	samples   float64
}

// Total is the decay-weighted sample count behind these figures. A dashboard
// needs it to say whether a score rests on evidence or on the prior, which is
// the difference between "this provider is unreliable" and "we have not tried
// it yet".
func (s routeStats) Total() float64 { return s.successes + s.failures }

// statsCache holds the aggregated trail between refreshes.
type statsCache struct {
	mu        sync.RWMutex
	byRoute   map[gateway.RouteKey]routeStats
	byModel   map[int64]routeStats
	refreshed time.Time
}

// newAxisScorer returns a scorer over the current trail. The snapshot is
// shared by every candidate in one request, so a model cannot be ranked
// against a different vintage of evidence than its rivals.
func (s *Server) newAxisScorer(ctx context.Context) gateway.AxisScorer {
	s.stats.refreshIfStale(ctx, s)
	return &axisScorer{
		server: s,
		rng:    rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 0x2545f491)),
	}
}

type axisScorer struct {
	server *Server
	rng    *rand.Rand
}

// Axes supplies a candidate's four inputs.
//
// sampled selects a Thompson draw for live routing versus the posterior mean
// for display. The draw's variance IS the exploration, which is why an
// untried model still earns traffic without any separate schedule; the mean
// is used on a dashboard because a number that moves when nothing changed is
// unreadable.
func (a *axisScorer) Axes(e *gateway.ChainEntry, sampled bool) gateway.Axes {
	stats := a.server.stats.forEntry(e)

	posterior := gateway.ReliabilityPosterior(stats.successes, stats.failures)
	reliability := posterior.Expected()
	if sampled {
		reliability = posterior.Sample(a.rng)
	}

	ttfb := -1.0
	if stats.hasTTFB {
		ttfb = stats.ttfbMs
	}

	return gateway.Axes{
		Reliability: reliability,
		Speed:       gateway.SpeedScore(stats.tokPerSec, ttfb),
		Headroom:    a.headroom(e),
		RateLimit:   a.rateLimitFactor(e),
	}
}

// headroom is the monthly-budget guardrail: a candidate short of budget is
// demoted gradually rather than dropped, because a throttled provider still
// beats no provider.
func (a *axisScorer) headroom(e *gateway.ChainEntry) float64 {
	if e.KeyID == nil {
		return 1
	}
	limits := windowLimitsOf(e)
	return a.server.engine.Ledger().Headroom(e.Platform, e.ModelID, []int64{*e.KeyID}, limits)
}

// windowLimitsOf converts the chain's nullable limits into the ledger's plain
// ones. A nil limit means the provider publishes none, which the ledger reads
// as zero: unknown, not zero-allowance.
func windowLimitsOf(e *gateway.ChainEntry) gateway.WindowLimits {
	deref := func(v *int64) int64 {
		if v == nil {
			return 0
		}
		return *v
	}
	return gateway.WindowLimits{
		RPM: deref(e.RPMLimit), TPM: deref(e.TPMLimit),
		RPD: deref(e.RPDLimit), TPD: deref(e.TPDLimit),
	}
}

// rateLimitFactor is the live penalty a recent rate limit imposes. It
// multiplies rather than reorders, so a model that is genuinely better stays
// ahead until it is actually being throttled.
func (a *axisScorer) rateLimitFactor(e *gateway.ChainEntry) float64 {
	penalty := a.server.engine.Penalties().Penalty(e.ModelDBID)
	if penalty <= 0 {
		return 1
	}
	// Bounded: the penalty is capped upstream at 10, so the worst case is a
	// heavy demotion, never removal from the chain.
	factor := 1 - float64(penalty)/20
	return math.Max(0.5, factor)
}

// refreshIfStale re-aggregates the trail when the cached snapshot has aged
// out. A failed refresh keeps the previous snapshot: stale evidence routes
// better than no evidence.
func (c *statsCache) refreshIfStale(ctx context.Context, s *Server) {
	c.mu.RLock()
	fresh := time.Since(c.refreshed) < statsCacheTTL && c.byRoute != nil
	c.mu.RUnlock()
	if fresh {
		return
	}

	byRoute, byModel, err := aggregateTrail(ctx, s)
	if err != nil {
		slog.Debug("Could not refresh routing stats", "error", err)
		return
	}

	c.mu.Lock()
	c.byRoute, c.byModel, c.refreshed = byRoute, byModel, time.Now()
	c.mu.Unlock()
}

// forEntry returns the evidence for a candidate, preferring its own
// (model, key) history and falling back to the model's aggregate so a brand
// new key on a known-good model is not treated as a total unknown.
func (c *statsCache) forEntry(e *gateway.ChainEntry) routeStats {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if e.KeyID != nil {
		key := gateway.RouteKey{Platform: e.Platform, ModelID: e.ModelID, KeyID: *e.KeyID}
		if stats, ok := c.byRoute[key]; ok {
			return stats
		}
	}
	if stats, ok := c.byModel[e.ModelDBID]; ok {
		return stats
	}
	return routeStats{}
}

// aggregateTrail reads the request trail and applies the decay weights.
//
// The decay is applied in Go rather than SQL: expressing an exponential
// half-life in SQLite would need either a UDF or a pile of CASE arms, and the
// day-bucket aggregation keeps the row count small enough that the arithmetic
// is free.
func aggregateTrail(ctx context.Context, s *Server) (
	map[gateway.RouteKey]routeStats, map[int64]routeStats, error,
) {
	cutoff := time.Now().Add(-statsWindow).Unix()
	rows, err := s.engine.DB().QueryContext(ctx,
		`SELECT platform, model_id, COALESCE(key_id, 0), outcome,
		        COALESCE(output_tokens, 0), COALESCE(latency_ms, 0), ttfb_ms,
		        CAST((? - created_at) / 86400 AS INTEGER) AS age_days,
		        count(*) AS n
		   FROM requests
		  WHERE created_at >= ? AND outcome <> 'canceled'
		  GROUP BY platform, model_id, key_id, outcome, age_days`,
		time.Now().Unix(), cutoff)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rows.Close() }()

	byRoute := map[gateway.RouteKey]routeStats{}
	for rows.Next() {
		var (
			platform, modelID, outcome string
			keyID                      int64
			outTokens, latencyMs       int64
			ttfb                       *int64
			ageDays                    int64
			n                          int64
		)
		if err := rows.Scan(&platform, &modelID, &keyID, &outcome,
			&outTokens, &latencyMs, &ttfb, &ageDays, &n); err != nil {
			return nil, nil, err
		}

		weight := decayWeight(ageDays) * float64(n)
		key := gateway.RouteKey{Platform: platform, ModelID: modelID, KeyID: keyID}
		stats := byRoute[key]

		switch outcome {
		case "success":
			stats.successes += weight
			if latencyMs > 0 && outTokens > 0 {
				stats.tokPerSec = float64(outTokens) * 1000 / float64(latencyMs)
			}
			if ttfb != nil {
				stats.ttfbMs = float64(*ttfb)
				stats.hasTTFB = true
			}
		default:
			// A timeout counts against reliability but still contributes its
			// wall-clock to speed, which is why failures are not simply
			// discarded here.
			stats.failures += weight
		}
		stats.samples += weight
		byRoute[key] = stats
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	// The per-model aggregate is the fallback for a key with no history.
	byModel := map[int64]routeStats{}
	modelRows, err := s.engine.DB().QueryContext(ctx,
		`SELECT m.id, r.platform, r.model_id FROM models m
		   JOIN requests r ON r.platform = m.platform AND r.model_id = m.model_id
		  WHERE r.created_at >= ?
		  GROUP BY m.id, r.platform, r.model_id`, cutoff)
	if err != nil {
		return byRoute, byModel, nil
	}
	defer func() { _ = modelRows.Close() }()

	for modelRows.Next() {
		var (
			modelDBID       int64
			platform, model string
		)
		if err := modelRows.Scan(&modelDBID, &platform, &model); err != nil {
			continue
		}
		agg := byModel[modelDBID]
		for key, stats := range byRoute {
			if key.Platform != platform || key.ModelID != model {
				continue
			}
			agg.successes += stats.successes
			agg.failures += stats.failures
			agg.samples += stats.samples
			if stats.tokPerSec > 0 {
				agg.tokPerSec = stats.tokPerSec
			}
			if stats.hasTTFB {
				agg.ttfbMs, agg.hasTTFB = stats.ttfbMs, true
			}
		}
		byModel[modelDBID] = agg
	}

	return byRoute, byModel, nil
}

// decayWeight halves a sample's influence every statsHalfLife.
func decayWeight(ageDays int64) float64 {
	return math.Pow(0.5, float64(ageDays)/(statsHalfLife.Hours()/24))
}

// keyBaseURL reads a key's endpoint, which a relay needs and which also tells
// the cooldown engine whether this is a local endpoint that recovers in
// seconds.
func (s *Server) keyBaseURL(keyID int64) string {
	var baseURL *string
	if err := s.engine.DB().QueryRow(
		`SELECT base_url FROM api_keys WHERE id = ?`, keyID).Scan(&baseURL); err != nil {
		return ""
	}
	if baseURL == nil {
		return ""
	}
	return *baseURL
}
