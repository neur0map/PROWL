package api

import (
	"context"
	"database/sql"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/neur0map/prowl/internal/gateway"
)

// The analytics, usage and logs surface. Every route here is gated by the
// dashboard session -- these views name providers, keys and failure reasons,
// which the inference-plane key must never open.
//
// Two data tiers back it. Headline totals (request counts, token sums, the
// success rate, lifetime figures) read the durable request_hourly rollup and
// the settings lifetime counters, so they survive the retention prune of the
// raw requests table. Everything that the rollup cannot express -- latency
// percentiles, time-to-first-token, estimated savings, the per-call list and
// the failover ladder -- reads the raw requests / request_attempts trail and
// therefore only sees rows still inside the retention window. A metric with no
// raw samples serialises as null, never 0: the client renders "-" for null and
// "0 ms" for zero, and conflating them lies about the data.
func (s *Server) registerAnalyticsRoutes() {
	s.mux.HandleFunc("GET /api/analytics/summary", s.RequireSession(s.handleAnalyticsSummary))
	s.mux.HandleFunc("GET /api/analytics/by-model", s.RequireSession(s.handleAnalyticsByModel))
	s.mux.HandleFunc("GET /api/analytics/by-platform", s.RequireSession(s.handleAnalyticsByPlatform))
	s.mux.HandleFunc("GET /api/analytics/by-client", s.RequireSession(s.handleAnalyticsByClient))
	s.mux.HandleFunc("GET /api/analytics/by-key", s.RequireSession(s.handleAnalyticsByKey))
	s.mux.HandleFunc("GET /api/analytics/timeline", s.RequireSession(s.handleAnalyticsTimeline))
	s.mux.HandleFunc("GET /api/analytics/error-distribution", s.RequireSession(s.handleAnalyticsErrorDistribution))
	s.mux.HandleFunc("GET /api/analytics/errors", s.RequireSession(s.handleAnalyticsErrors))
	s.mux.HandleFunc("GET /api/analytics/requests", s.RequireSession(s.handleAnalyticsRequests))
	s.mux.HandleFunc("GET /api/analytics/requests/{id}", s.RequireSession(s.handleAnalyticsRequestDetail))

	s.mux.HandleFunc("GET /api/logs", s.RequireSession(s.handleLogs))
	s.mux.HandleFunc("POST /api/logs/clear", s.RequireSession(s.handleLogsClear))

	// Seed the log store's id counter from MAX(id) at boot so a persisted
	// history and any freshly minted id never collide (persistence 7.4 trap 4).
	logStoreFor(s.engine.DB()).seed(context.Background())
}

// ── Shared helpers ───────────────────────────────────────────────────────────

// rangeSince maps the dashboard's range picker to a Unix-seconds cutoff.
// Unknown values fall back to 7d, matching the reference (analytics.ts:28-42).
func rangeSince(rng string) int64 {
	now := time.Now()
	switch rng {
	case "24h":
		return now.Add(-24 * time.Hour).Unix()
	case "30d":
		return now.Add(-30 * 24 * time.Hour).Unix()
	case "90d":
		return now.Add(-90 * 24 * time.Hour).Unix()
	default: // "7d"
		return now.Add(-7 * 24 * time.Hour).Unix()
	}
}

// floorHour truncates a Unix-seconds instant to the start of its UTC hour,
// the granularity request_hourly is keyed on.
func floorHour(unix int64) int64 { return unix - unix%3600 }

// sqliteDateTime renders a Unix-seconds instant the way FreeLLMAPI's SQLite
// text timestamps read: UTC, space separator, no fractional part. The savings
// projection and the first-request marker depend on this exact shape.
func sqliteDateTime(unix int64) string {
	return time.Unix(unix, 0).UTC().Format("2006-01-02 15:04:05")
}

// isoZ renders a Unix-seconds instant as the zoned ISO string the recent-call
// and per-request views hand the client to parse.
func isoZ(unix int64) string {
	return time.Unix(unix, 0).UTC().Format("2006-01-02T15:04:05Z")
}

func round1(x float64) float64 { return math.Round(x*10) / 10 }
func round2(x float64) float64 { return math.Round(x*100) / 100 }

func nullEmpty(v string) any {
	if v == "" {
		return nil
	}
	return v
}
func nullInt(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}
func nullStr(v *string) any {
	if v == nil {
		return nil
	}
	return *v
}
func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// Fallback $/M for models with no mapping, matching db/model-pricing.ts:196-197.
const (
	fallbackInputPerM  = 0.20
	fallbackOutputPerM = 0.80
)

// normalizeBaseURL is the trailing-slash-insensitive endpoint identity that
// endpoint_scope is stored under (endpoint-scope.ts:25-27).
func normalizeBaseURL(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}

// versionSeg matches a lone API-version path segment (/v1, /v1beta), which
// carries no endpoint identity of its own (provider-identity.ts:75).
var versionSeg = regexp.MustCompile(`(?i)^v\d+[a-z0-9._-]*$`)

// endpointHost is the host[:port] of an endpoint scope, or "" when it does not
// parse -- callers then fall back to the generic 'custom' id.
func endpointHost(scope string) string {
	if scope == "" {
		return ""
	}
	u, err := url.Parse(scope)
	if err != nil {
		return ""
	}
	return u.Host
}

// endpointPathLabel keeps a scope's path when it carries identity (a tenant or
// mount point) and drops the trivial root or bare version segment, mirroring
// provider-identity.ts:66-77.
func endpointPathLabel(scope string) string {
	u, err := url.Parse(scope)
	if err != nil {
		return ""
	}
	path := strings.TrimRight(u.Path, "/")
	if path == "" {
		return ""
	}
	segs := strings.Split(path[1:], "/")
	if len(segs) == 1 && versionSeg.MatchString(segs[0]) {
		return ""
	}
	return path
}

// providerIDFor is the stable row id: the bare platform slug for catalog
// providers, 'custom:<scope>' for a custom relay, 'custom' when the scope is
// unknown (provider-identity.ts:46-52). Because Prowl stores the normalized
// endpoint_scope on the request row itself, no api_keys join is needed to
// reconstruct it -- the collision #889 warns about cannot arise.
func providerIDFor(platform, scope string) string {
	if platform != "custom" {
		return platform
	}
	if scope != "" {
		return "custom:" + scope
	}
	return "custom"
}

// providerDisplayName is the operator-readable name: the platform slug, or a
// custom endpoint's host plus any identifying path (provider-identity.ts:98-107).
func providerDisplayName(platform, scope string) string {
	if platform != "custom" || scope == "" {
		return platform
	}
	host := endpointHost(scope)
	if host == "" {
		return platform
	}
	return host + endpointPathLabel(scope)
}

// ── Summary ──────────────────────────────────────────────────────────────────

type typeCounts struct {
	Chat      int64 `json:"chat"`
	Embedding int64 `json:"embedding"`
}

type summaryResponse struct {
	TotalRequests         int64      `json:"totalRequests"`
	SuccessRate           float64    `json:"successRate"`
	TotalInputTokens      int64      `json:"totalInputTokens"`
	TotalOutputTokens     int64      `json:"totalOutputTokens"`
	AvgLatencyMs          int64      `json:"avgLatencyMs"`
	P50LatencyMs          *int64     `json:"p50LatencyMs"`
	P95LatencyMs          *int64     `json:"p95LatencyMs"`
	AvgTtfbMs             *int64     `json:"avgTtfbMs"`
	RequestTypeCounts     typeCounts `json:"requestTypeCounts"`
	EstimatedCostSavings  float64    `json:"estimatedCostSavings"`
	PinnedRequests        int64      `json:"pinnedRequests"`
	PinHonoredRequests    int64      `json:"pinHonoredRequests"`
	FirstRequestAt        *string    `json:"firstRequestAt"`
	LifetimeTotalRequests int64      `json:"lifetimeTotalRequests"`
}

func (s *Server) handleAnalyticsSummary(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	ctx := r.Context()
	db := s.engine.DB()
	since := rangeSince(r.URL.Query().Get("range"))

	// Headline totals come from the durable hourly rollup so they stay right
	// after the raw trail is pruned.
	var (
		totalReq, successes, failures, inTok, outTok int64
		minHour                                      sql.NullInt64
	)
	_ = db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(requests),0), COALESCE(SUM(successes),0),
		       COALESCE(SUM(failures),0), COALESCE(SUM(input_tokens),0),
		       COALESCE(SUM(output_tokens),0), MIN(hour_start)
		  FROM request_hourly WHERE hour_start >= ?`,
		floorHour(since)).Scan(&totalReq, &successes, &failures, &inTok, &outTok, &minHour)

	// A canceled request counts in the totals but is neither a success nor a
	// failure, so it must not dilute the rate (analytics.ts:100-104).
	decided := successes + failures
	successRate := 0.0
	if decided > 0 {
		successRate = float64(successes) / float64(decided) * 100
	}

	// Everything below is raw-trail scoped: only rows still inside the window.
	var avgLatency sql.NullFloat64
	_ = db.QueryRowContext(ctx,
		`SELECT AVG(latency_ms) FROM requests WHERE created_at >= ?`, since).Scan(&avgLatency)

	var savings float64
	_ = db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(CASE WHEN r.outcome = 'success' THEN
		         r.input_tokens  * COALESCE(m.paid_input_per_m,  ?) / 1000000.0 +
		         r.output_tokens * COALESCE(m.paid_output_per_m, ?) / 1000000.0
		       ELSE 0 END), 0)
		  FROM requests r
		  LEFT JOIN models m ON m.platform = r.platform
		       AND m.model_id = r.model_id AND m.endpoint_scope = r.endpoint_scope
		 WHERE r.created_at >= ?`,
		fallbackInputPerM, fallbackOutputPerM, since).Scan(&savings)

	// Pinned = the client named a concrete model (not the auto/fusion alias);
	// honored = that model actually served it. The write path stores the
	// requested model in routed_from.
	var pinnedN, honoredN sql.NullInt64
	_ = db.QueryRowContext(ctx, `
		SELECT SUM(CASE WHEN routed_from IS NOT NULL AND routed_from NOT IN ('', 'auto', 'fusion') THEN 1 ELSE 0 END),
		       SUM(CASE WHEN routed_from = model_id THEN 1 ELSE 0 END)
		  FROM requests WHERE created_at >= ?`, since).Scan(&pinnedN, &honoredN)

	// Percentiles range over the raw rows and are null when the window is
	// empty -- a placeholder, never a false zero.
	var rawCount int64
	_ = db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM requests WHERE created_at >= ?`, since).Scan(&rawCount)
	percentile := func(fraction float64) *int64 {
		if rawCount == 0 {
			return nil
		}
		offset := int64(math.Floor(float64(rawCount-1) * fraction))
		var v sql.NullInt64
		_ = db.QueryRowContext(ctx, `
			SELECT latency_ms FROM requests WHERE created_at >= ?
			 ORDER BY latency_ms ASC LIMIT 1 OFFSET ?`, since, offset).Scan(&v)
		if !v.Valid {
			return nil
		}
		return new(v.Int64)
	}

	var ttfb sql.NullFloat64
	_ = db.QueryRowContext(ctx, `
		SELECT AVG(ttfb_ms) FROM requests
		 WHERE created_at >= ? AND ttfb_ms IS NOT NULL`, since).Scan(&ttfb)
	var avgTtfb *int64
	if ttfb.Valid {
		avgTtfb = new(int64(math.Round(ttfb.Float64)))
	}

	resp := summaryResponse{
		TotalRequests:         totalReq,
		SuccessRate:           round1(successRate),
		TotalInputTokens:      inTok,
		TotalOutputTokens:     outTok,
		AvgLatencyMs:          int64(math.Round(avgLatency.Float64)),
		P50LatencyMs:          percentile(0.5),
		P95LatencyMs:          percentile(0.95),
		AvgTtfbMs:             avgTtfb,
		RequestTypeCounts:     typeCounts{Chat: rawCount, Embedding: 0},
		EstimatedCostSavings:  round2(savings),
		PinnedRequests:        pinnedN.Int64,
		PinHonoredRequests:    honoredN.Int64,
		FirstRequestAt:        s.firstRequestAt(ctx, minHour),
		LifetimeTotalRequests: settingInt(ctx, db, "total_requests"),
	}
	WriteJSON(w, http.StatusOK, resp)
}

// firstRequestAt prefers the lifetime marker (never pruned) and falls back to
// the oldest hour still in the rollup window.
func (s *Server) firstRequestAt(ctx context.Context, minHour sql.NullInt64) *string {
	var v string
	err := s.engine.DB().QueryRowContext(ctx,
		`SELECT value FROM settings WHERE key = 'first_request_at'`).Scan(&v)
	if err == nil && v != "" {
		return &v
	}
	if minHour.Valid {
		return new(sqliteDateTime(minHour.Int64))
	}
	return nil
}

func settingInt(ctx context.Context, db *sql.DB, key string) int64 {
	var v string
	if err := db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v); err != nil {
		return 0
	}
	n, _ := strconv.ParseInt(v, 10, 64)
	return n
}

// ── By-model ─────────────────────────────────────────────────────────────────

type byModelRow struct {
	Platform          string  `json:"platform"`
	ProviderID        string  `json:"providerId"`
	Endpoint          string  `json:"endpoint"`
	ModelID           string  `json:"modelId"`
	DisplayName       string  `json:"displayName"`
	Requests          int64   `json:"requests"`
	SuccessRate       float64 `json:"successRate"`
	AvgLatencyMs      int64   `json:"avgLatencyMs"`
	TotalInputTokens  int64   `json:"totalInputTokens"`
	TotalOutputTokens int64   `json:"totalOutputTokens"`
	PinnedRequests    int64   `json:"pinnedRequests"`
	EstimatedCost     float64 `json:"estimatedCost"`
}

func (s *Server) handleAnalyticsByModel(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	ctx := r.Context()
	since := rangeSince(r.URL.Query().Get("range"))

	// Group by (platform, endpoint_scope, model_id): the same model id served
	// by two relays is two rows, not one merged "custom" row whose numbers
	// describe neither endpoint (#889). The models join is endpoint-scoped too,
	// so it picks the row that actually served the request (#651).
	rows, err := s.engine.DB().QueryContext(ctx, `
		SELECT r.platform, r.endpoint_scope, r.model_id, m.display_name,
		       COUNT(*) AS requests,
		       SUM(CASE WHEN r.outcome = 'success' THEN 1 ELSE 0 END) * 100.0
		         / NULLIF(SUM(CASE WHEN r.outcome <> 'canceled' THEN 1 ELSE 0 END), 0) AS success_rate,
		       AVG(r.latency_ms) AS avg_latency_ms,
		       SUM(r.input_tokens) AS in_tok,
		       SUM(r.output_tokens) AS out_tok,
		       SUM(CASE WHEN r.routed_from = r.model_id THEN 1 ELSE 0 END) AS pinned,
		       SUM(CASE WHEN r.outcome = 'success' THEN
		             r.input_tokens  * COALESCE(m.paid_input_per_m,  ?) / 1000000.0 +
		             r.output_tokens * COALESCE(m.paid_output_per_m, ?) / 1000000.0
		           ELSE 0 END) AS est_cost
		  FROM requests r
		  LEFT JOIN models m ON m.platform = r.platform
		       AND m.model_id = r.model_id AND m.endpoint_scope = r.endpoint_scope
		 WHERE r.created_at >= ?
		 GROUP BY r.platform, r.endpoint_scope, r.model_id
		 ORDER BY requests DESC`,
		fallbackInputPerM, fallbackOutputPerM, since)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "analytics query failed")
		return
	}
	defer func() { _ = rows.Close() }()

	out := []byModelRow{}
	for rows.Next() {
		var (
			platform, scope, modelID string
			display                  sql.NullString
			reqs, pinned             int64
			successRate, avgLat      sql.NullFloat64
			inTok, outTok            sql.NullInt64
			estCost                  sql.NullFloat64
		)
		if err := rows.Scan(&platform, &scope, &modelID, &display, &reqs,
			&successRate, &avgLat, &inTok, &outTok, &pinned, &estCost); err != nil {
			continue
		}
		name := modelID
		if display.Valid && display.String != "" {
			name = display.String
		}
		out = append(out, byModelRow{
			Platform:          platform,
			ProviderID:        providerIDFor(platform, scope),
			Endpoint:          providerDisplayName(platform, scope),
			ModelID:           modelID,
			DisplayName:       name,
			Requests:          reqs,
			SuccessRate:       round1(successRate.Float64),
			AvgLatencyMs:      int64(math.Round(avgLat.Float64)),
			TotalInputTokens:  inTok.Int64,
			TotalOutputTokens: outTok.Int64,
			PinnedRequests:    pinned,
			EstimatedCost:     round2(estCost.Float64),
		})
	}
	WriteJSON(w, http.StatusOK, out)
}

// ── By-platform ──────────────────────────────────────────────────────────────

type byPlatformRow struct {
	Platform           string   `json:"platform"`
	ProviderID         string   `json:"providerId"`
	Endpoint           string   `json:"endpoint"`
	Requests           int64    `json:"requests"`
	SuccessRate        float64  `json:"successRate"`
	AvgLatencyMs       int64    `json:"avgLatencyMs"`
	P95LatencyMs       *int64   `json:"p95LatencyMs"`
	AvgTtfbMs          *int64   `json:"avgTtfbMs"`
	ErrorCount         int64    `json:"errorCount"`
	AvgTokensPerSecond *float64 `json:"avgTokensPerSecond"`
	TotalInputTokens   int64    `json:"totalInputTokens"`
	TotalOutputTokens  int64    `json:"totalOutputTokens"`
}

func (s *Server) handleAnalyticsByPlatform(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	ctx := r.Context()
	db := s.engine.DB()
	since := rangeSince(r.URL.Query().Get("range"))

	rows, err := db.QueryContext(ctx, `
		SELECT r.platform, r.endpoint_scope,
		       COUNT(*) AS requests,
		       SUM(CASE WHEN r.outcome = 'success' THEN 1 ELSE 0 END) * 100.0
		         / NULLIF(SUM(CASE WHEN r.outcome <> 'canceled' THEN 1 ELSE 0 END), 0) AS success_rate,
		       AVG(r.latency_ms) AS avg_latency_ms,
		       AVG(r.ttfb_ms) AS avg_ttfb_ms,
		       SUM(CASE WHEN r.outcome = 'error' THEN 1 ELSE 0 END) AS error_count,
		       AVG(CASE WHEN r.output_tokens > 0 AND r.latency_ms > 0
		             THEN r.output_tokens / (r.latency_ms / 1000.0) ELSE NULL END) AS tps,
		       SUM(r.input_tokens) AS in_tok,
		       SUM(r.output_tokens) AS out_tok
		  FROM requests r
		 WHERE r.created_at >= ?
		 GROUP BY r.platform, r.endpoint_scope
		 ORDER BY requests DESC`, since)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "analytics query failed")
		return
	}

	// Drain the grouped rows fully before the per-group p95 pass. The gateway
	// pins the pool to one SQLite connection, so a nested query issued while
	// this cursor is still open would wait on the very connection the cursor
	// holds -- a deadlock, not slowness.
	type platformAgg struct {
		platform, scope     string
		reqs, errCount      int64
		successRate, avgLat sql.NullFloat64
		avgTtfb, tps        sql.NullFloat64
		inTok, outTok       sql.NullInt64
	}
	var aggs []platformAgg
	for rows.Next() {
		var a platformAgg
		if err := rows.Scan(&a.platform, &a.scope, &a.reqs, &a.successRate, &a.avgLat,
			&a.avgTtfb, &a.errCount, &a.tps, &a.inTok, &a.outTok); err != nil {
			continue
		}
		aggs = append(aggs, a)
	}
	_ = rows.Close()

	// p95 is a per-group nearest-rank pick (SQLite has no percentile
	// aggregate). The WHERE matches the grouping exactly -- platform AND scope
	// -- or one custom endpoint's tail latency would bleed into another's.
	p95Stmt, err := db.PrepareContext(ctx, `
		SELECT latency_ms FROM requests
		 WHERE created_at >= ? AND platform = ? AND endpoint_scope = ?
		 ORDER BY latency_ms ASC LIMIT 1 OFFSET ?`)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "analytics query failed")
		return
	}
	defer func() { _ = p95Stmt.Close() }()

	out := []byPlatformRow{}
	for _, a := range aggs {
		var p95 *int64
		if a.reqs > 0 {
			offset := int64(math.Floor(float64(a.reqs-1) * 0.95))
			var v sql.NullInt64
			if err := p95Stmt.QueryRowContext(ctx, since, a.platform, a.scope, offset).Scan(&v); err == nil && v.Valid {
				p95 = new(v.Int64)
			}
		}
		var ttfbPtr *int64
		if a.avgTtfb.Valid {
			ttfbPtr = new(int64(math.Round(a.avgTtfb.Float64)))
		}
		var tpsPtr *float64
		if a.tps.Valid {
			v := round1(a.tps.Float64)
			tpsPtr = &v
		}
		out = append(out, byPlatformRow{
			Platform:           a.platform,
			ProviderID:         providerIDFor(a.platform, a.scope),
			Endpoint:           providerDisplayName(a.platform, a.scope),
			Requests:           a.reqs,
			SuccessRate:        round1(a.successRate.Float64),
			AvgLatencyMs:       int64(math.Round(a.avgLat.Float64)),
			P95LatencyMs:       p95,
			AvgTtfbMs:          ttfbPtr,
			ErrorCount:         a.errCount,
			AvgTokensPerSecond: tpsPtr,
			TotalInputTokens:   a.inTok.Int64,
			TotalOutputTokens:  a.outTok.Int64,
		})
	}
	WriteJSON(w, http.StatusOK, out)
}

// ── By-client ────────────────────────────────────────────────────────────────

type byClientRow struct {
	ClientAgent       string  `json:"clientAgent"`
	Requests          int64   `json:"requests"`
	SuccessRate       float64 `json:"successRate"`
	AvgLatencyMs      int64   `json:"avgLatencyMs"`
	TotalInputTokens  int64   `json:"totalInputTokens"`
	TotalOutputTokens int64   `json:"totalOutputTokens"`
	LastSeenAt        string  `json:"lastSeenAt"`
}

// Prowl's requests table records no per-caller client-agent classification, so
// every call lands in a single 'unknown' agent bucket. The shape the client
// reads is unchanged; the dimension is simply not populated yet.
func (s *Server) handleAnalyticsByClient(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	ctx := r.Context()
	since := rangeSince(r.URL.Query().Get("range"))

	var (
		reqs                int64
		successRate, avgLat sql.NullFloat64
		inTok, outTok       sql.NullInt64
		lastSeen            sql.NullInt64
	)
	_ = s.engine.DB().QueryRowContext(ctx, `
		SELECT COUNT(*),
		       SUM(CASE WHEN outcome = 'success' THEN 1 ELSE 0 END) * 100.0
		         / NULLIF(SUM(CASE WHEN outcome <> 'canceled' THEN 1 ELSE 0 END), 0),
		       AVG(latency_ms), SUM(input_tokens), SUM(output_tokens), MAX(created_at)
		  FROM requests WHERE created_at >= ?`, since).
		Scan(&reqs, &successRate, &avgLat, &inTok, &outTok, &lastSeen)

	out := []byClientRow{}
	if reqs > 0 {
		last := ""
		if lastSeen.Valid {
			last = isoZ(lastSeen.Int64)
		}
		out = append(out, byClientRow{
			ClientAgent:       "unknown",
			Requests:          reqs,
			SuccessRate:       round1(successRate.Float64),
			AvgLatencyMs:      int64(math.Round(avgLat.Float64)),
			TotalInputTokens:  inTok.Int64,
			TotalOutputTokens: outTok.Int64,
			LastSeenAt:        last,
		})
	}
	WriteJSON(w, http.StatusOK, out)
}

// ── By-key ───────────────────────────────────────────────────────────────────

type byKeyRow struct {
	KeyID             int64   `json:"keyId"`
	Label             *string `json:"label"`
	Platform          *string `json:"platform"`
	Requests          int64   `json:"requests"`
	SuccessRate       float64 `json:"successRate"`
	AvgLatencyMs      int64   `json:"avgLatencyMs"`
	TotalInputTokens  int64   `json:"totalInputTokens"`
	TotalOutputTokens int64   `json:"totalOutputTokens"`
}

func (s *Server) handleAnalyticsByKey(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	ctx := r.Context()
	since := rangeSince(r.URL.Query().Get("range"))

	// LEFT JOIN so a request whose key was later deleted still shows up with a
	// null label -- the keyId is always returned.
	rows, err := s.engine.DB().QueryContext(ctx, `
		SELECT r.key_id, k.label, k.platform,
		       COUNT(*) AS requests,
		       SUM(CASE WHEN r.outcome = 'success' THEN 1 ELSE 0 END) * 100.0
		         / NULLIF(SUM(CASE WHEN r.outcome <> 'canceled' THEN 1 ELSE 0 END), 0) AS success_rate,
		       AVG(r.latency_ms) AS avg_latency_ms,
		       SUM(r.input_tokens) AS in_tok, SUM(r.output_tokens) AS out_tok
		  FROM requests r
		  LEFT JOIN api_keys k ON k.id = r.key_id
		 WHERE r.key_id IS NOT NULL AND r.created_at >= ?
		 GROUP BY r.key_id
		 ORDER BY requests DESC
		 LIMIT 50`, since)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "analytics query failed")
		return
	}
	defer func() { _ = rows.Close() }()

	out := []byKeyRow{}
	for rows.Next() {
		var (
			keyID               int64
			label, platform     sql.NullString
			reqs                int64
			successRate, avgLat sql.NullFloat64
			inTok, outTok       sql.NullInt64
		)
		if err := rows.Scan(&keyID, &label, &platform, &reqs, &successRate, &avgLat, &inTok, &outTok); err != nil {
			continue
		}
		row := byKeyRow{
			KeyID:             keyID,
			Requests:          reqs,
			SuccessRate:       round1(successRate.Float64),
			AvgLatencyMs:      int64(math.Round(avgLat.Float64)),
			TotalInputTokens:  inTok.Int64,
			TotalOutputTokens: outTok.Int64,
		}
		if label.Valid {
			row.Label = new(label.String)
		}
		if platform.Valid {
			row.Platform = new(platform.String)
		}
		out = append(out, row)
	}
	WriteJSON(w, http.StatusOK, out)
}

// ── Timeline ─────────────────────────────────────────────────────────────────

type timelinePoint struct {
	Timestamp    string `json:"timestamp"`
	Requests     int64  `json:"requests"`
	SuccessCount int64  `json:"successCount"`
	FailureCount int64  `json:"failureCount"`
	InputTokens  int64  `json:"inputTokens"`
	OutputTokens int64  `json:"outputTokens"`
}

func (s *Server) handleAnalyticsTimeline(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	ctx := r.Context()
	q := r.URL.Query()
	rng := q.Get("range")
	since := rangeSince(rng)
	interval := q.Get("interval")
	if interval == "" {
		if rng == "24h" {
			interval = "hour"
		} else {
			interval = "day"
		}
	}

	// tzOffset: the viewer's offset from UTC in minutes, so bucket boundaries
	// follow the reader's wall clock. Whitelisted to a sane range; a bad value
	// is treated as UTC.
	tzOffset := 0
	if v, err := strconv.Atoi(q.Get("tzOffset")); err == nil && v >= -720 && v <= 840 {
		tzOffset = v
	}

	rows, err := s.engine.DB().QueryContext(ctx, `
		SELECT hour_start, SUM(requests), SUM(successes), SUM(failures),
		       SUM(input_tokens), SUM(output_tokens)
		  FROM request_hourly WHERE hour_start >= ?
		 GROUP BY hour_start ORDER BY hour_start ASC`, floorHour(since))
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "analytics query failed")
		return
	}
	defer func() { _ = rows.Close() }()

	order := []string{}
	byBucket := map[string]*timelinePoint{}
	for rows.Next() {
		var hourStart, reqs, succ, fail, inTok, outTok int64
		if err := rows.Scan(&hourStart, &reqs, &succ, &fail, &inTok, &outTok); err != nil {
			continue
		}
		local := time.Unix(hourStart+int64(tzOffset)*60, 0).UTC()
		var bucket string
		if interval == "hour" {
			bucket = local.Format("2006-01-02T15") + ":00:00"
		} else {
			bucket = local.Format("2006-01-02")
		}
		p := byBucket[bucket]
		if p == nil {
			p = &timelinePoint{Timestamp: bucket}
			byBucket[bucket] = p
			order = append(order, bucket)
		}
		p.Requests += reqs
		p.SuccessCount += succ
		p.FailureCount += fail
		p.InputTokens += inTok
		p.OutputTokens += outTok
	}

	out := make([]timelinePoint, 0, len(order))
	for _, k := range order {
		out = append(out, *byBucket[k])
	}
	WriteJSON(w, http.StatusOK, out)
}

// ── Error distribution ───────────────────────────────────────────────────────

// errorCategorySQL classifies an error by keyword, reproducing the reference's
// CASE ladder (analytics.ts:491-500) against Prowl's error_message column.
const errorCategorySQL = `
	CASE
	  WHEN error_message LIKE '%429%' OR error_message LIKE '%rate limit%' OR error_message LIKE '%too many%' OR error_message LIKE '%quota%' THEN 'Rate Limited (429)'
	  WHEN error_message LIKE '%401%' OR error_message LIKE '%unauthorized%' OR error_message LIKE '%invalid.*key%' THEN 'Auth Error (401)'
	  WHEN error_message LIKE '%403%' OR error_message LIKE '%forbidden%' THEN 'Forbidden (403)'
	  WHEN error_message LIKE '%404%' OR error_message LIKE '%not found%' THEN 'Not Found (404)'
	  WHEN error_message LIKE '%timeout%' OR error_message LIKE '%ETIMEDOUT%' OR error_message LIKE '%ECONNREFUSED%' THEN 'Timeout/Connection'
	  WHEN error_message LIKE '%500%' OR error_message LIKE '%internal server%' THEN 'Server Error (500)'
	  WHEN error_message LIKE '%503%' OR error_message LIKE '%unavailable%' THEN 'Unavailable (503)'
	  ELSE 'Other'
	END`

type categoryCount struct {
	Category string `json:"category"`
	Count    int64  `json:"count"`
}

type detailedError struct {
	Platform      string `json:"platform"`
	ModelID       string `json:"model_id"`
	ErrorCategory string `json:"error_category"`
	Count         int64  `json:"count"`
}

type errorPlatform struct {
	Platform   string `json:"platform"`
	ProviderID string `json:"providerId"`
	Endpoint   string `json:"endpoint"`
	Count      int64  `json:"count"`
}

type errorDistributionResponse struct {
	ByCategory []categoryCount `json:"byCategory"`
	ByPlatform []errorPlatform `json:"byPlatform"`
	Detailed   []detailedError `json:"detailed"`
}

func (s *Server) handleAnalyticsErrorDistribution(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	ctx := r.Context()
	db := s.engine.DB()
	since := rangeSince(r.URL.Query().Get("range"))

	resp := errorDistributionResponse{
		ByCategory: []categoryCount{},
		ByPlatform: []errorPlatform{},
		Detailed:   []detailedError{},
	}

	detRows, err := db.QueryContext(ctx, `
		SELECT platform, model_id, `+errorCategorySQL+` AS error_category, COUNT(*) AS count
		  FROM requests WHERE outcome = 'error' AND created_at >= ?
		 GROUP BY platform, error_category ORDER BY count DESC`, since)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "analytics query failed")
		return
	}
	for detRows.Next() {
		var d detailedError
		if err := detRows.Scan(&d.Platform, &d.ModelID, &d.ErrorCategory, &d.Count); err == nil {
			resp.Detailed = append(resp.Detailed, d)
		}
	}
	_ = detRows.Close()

	catRows, err := db.QueryContext(ctx, `
		SELECT `+errorCategorySQL+` AS category, COUNT(*) AS count
		  FROM requests WHERE outcome = 'error' AND created_at >= ?
		 GROUP BY category ORDER BY count DESC`, since)
	if err == nil {
		for catRows.Next() {
			var c categoryCount
			if err := catRows.Scan(&c.Category, &c.Count); err == nil {
				resp.ByCategory = append(resp.ByCategory, c)
			}
		}
		_ = catRows.Close()
	}

	platRows, err := db.QueryContext(ctx, `
		SELECT platform, endpoint_scope, COUNT(*) AS count
		  FROM requests WHERE outcome = 'error' AND created_at >= ?
		 GROUP BY platform, endpoint_scope ORDER BY count DESC`, since)
	if err == nil {
		for platRows.Next() {
			var platform, scope string
			var count int64
			if err := platRows.Scan(&platform, &scope, &count); err == nil {
				resp.ByPlatform = append(resp.ByPlatform, errorPlatform{
					Platform:   platform,
					ProviderID: providerIDFor(platform, scope),
					Endpoint:   providerDisplayName(platform, scope),
					Count:      count,
				})
			}
		}
		_ = platRows.Close()
	}

	WriteJSON(w, http.StatusOK, resp)
}

// ── Recent errors ────────────────────────────────────────────────────────────

type recentErrorRow struct {
	ID         int64   `json:"id"`
	Platform   string  `json:"platform"`
	ProviderID string  `json:"providerId"`
	Endpoint   string  `json:"endpoint"`
	ModelID    string  `json:"modelId"`
	Error      *string `json:"error"`
	LatencyMs  int64   `json:"latencyMs"`
	CreatedAt  string  `json:"createdAt"`
}

func (s *Server) handleAnalyticsErrors(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	ctx := r.Context()
	since := rangeSince(r.URL.Query().Get("range"))

	rows, err := s.engine.DB().QueryContext(ctx, `
		SELECT id, platform, endpoint_scope, model_id, error_message, latency_ms, created_at
		  FROM requests WHERE outcome = 'error' AND created_at >= ?
		 ORDER BY created_at DESC LIMIT 50`, since)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "analytics query failed")
		return
	}
	defer func() { _ = rows.Close() }()

	out := []recentErrorRow{}
	for rows.Next() {
		var (
			id, latency              int64
			platform, scope, modelID string
			errMsg                   sql.NullString
			createdAt                int64
		)
		if err := rows.Scan(&id, &platform, &scope, &modelID, &errMsg, &latency, &createdAt); err != nil {
			continue
		}
		row := recentErrorRow{
			ID:         id,
			Platform:   platform,
			ProviderID: providerIDFor(platform, scope),
			Endpoint:   providerDisplayName(platform, scope),
			ModelID:    modelID,
			LatencyMs:  latency,
			CreatedAt:  sqliteDateTime(createdAt),
		}
		if errMsg.Valid {
			row.Error = new(errMsg.String)
		}
		out = append(out, row)
	}
	WriteJSON(w, http.StatusOK, out)
}

// ── Recent calls ─────────────────────────────────────────────────────────────

type recentCallRow struct {
	ID              int64   `json:"id"`
	Platform        string  `json:"platform"`
	ModelID         string  `json:"modelId"`
	RequestedModel  *string `json:"requestedModel"`
	RequestType     string  `json:"requestType"`
	Status          string  `json:"status"`
	InputTokens     int64   `json:"inputTokens"`
	OutputTokens    int64   `json:"outputTokens"`
	LatencyMs       int64   `json:"latencyMs"`
	Error           *string `json:"error"`
	ClientIP        *string `json:"clientIp"`
	ClientUserAgent *string `json:"clientUserAgent"`
	ClientAgent     *string `json:"clientAgent"`
	CreatedAt       string  `json:"createdAt"`
	KeyLabel        *string `json:"keyLabel"`
	AttemptCount    int64   `json:"attemptCount"`
}

type recentCallsResponse struct {
	Total int64           `json:"total"`
	Rows  []recentCallRow `json:"rows"`
}

func (s *Server) handleAnalyticsRequests(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	ctx := r.Context()
	db := s.engine.DB()
	q := r.URL.Query()
	since := rangeSince(q.Get("range"))

	limit := 100
	if v, err := strconv.Atoi(q.Get("limit")); err == nil && v > 0 {
		limit = v
	}
	if limit > 500 {
		limit = 500
	}
	offset := 0
	if v, err := strconv.Atoi(q.Get("offset")); err == nil && v > 0 {
		offset = v
	}

	// This route uses the older bare-string error family on purpose
	// (analytics.ts:602): the client tolerates both, but the contract is
	// per-route.
	status := q.Get("status")
	if status != "" && status != "success" && status != "error" && status != "canceled" {
		WriteBareError(w, http.StatusBadRequest, "invalid status filter (expected 'success', 'error' or 'canceled')")
		return
	}

	filterSQL := ""
	var filterArgs []any
	if status != "" {
		filterSQL += " AND r.outcome = ?"
		filterArgs = append(filterArgs, status)
	}

	provider := q.Get("provider")
	platform := q.Get("platform")
	switch {
	case provider != "":
		if len(provider) > 256 || strings.ContainsAny(provider, "\r\n") {
			WriteBareError(w, http.StatusBadRequest, "invalid provider filter")
			return
		}
		switch {
		case provider == "custom":
			// The bare 'custom' id is the orphan bucket (endpoint unknown), not
			// "all custom traffic": match exactly what the by-platform row
			// counted (#889).
			filterSQL += " AND r.platform = 'custom' AND r.endpoint_scope = ''"
		case strings.HasPrefix(provider, "custom:"):
			filterSQL += " AND r.platform = 'custom' AND r.endpoint_scope = ?"
			filterArgs = append(filterArgs, normalizeBaseURL(provider[len("custom:"):]))
		case slugRE.MatchString(provider):
			filterSQL += " AND r.platform = ?"
			filterArgs = append(filterArgs, provider)
		default:
			WriteBareError(w, http.StatusBadRequest, "invalid provider filter")
			return
		}
	case platform != "":
		if !slugRE.MatchString(platform) {
			WriteBareError(w, http.StatusBadRequest, "invalid platform filter")
			return
		}
		filterSQL += " AND r.platform = ?"
		filterArgs = append(filterArgs, platform)
	}

	var total int64
	countArgs := append([]any{since}, filterArgs...)
	_ = db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM requests r WHERE r.created_at >= ?`+filterSQL, countArgs...).Scan(&total)

	listArgs := append(append([]any{since}, filterArgs...), limit, offset)
	rows, err := db.QueryContext(ctx, `
		SELECT r.id, r.platform, r.model_id, r.routed_from, r.outcome,
		       r.input_tokens, r.output_tokens, r.latency_ms, r.error_message,
		       r.created_at,
		       (SELECT COUNT(*) FROM request_attempts a WHERE a.request_id = r.id) AS attempt_count,
		       k.label
		  FROM requests r
		  LEFT JOIN api_keys k ON k.id = r.key_id
		 WHERE r.created_at >= ?`+filterSQL+`
		 ORDER BY r.created_at DESC, r.id DESC
		 LIMIT ? OFFSET ?`, listArgs...)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "analytics query failed")
		return
	}
	defer func() { _ = rows.Close() }()

	resp := recentCallsResponse{Total: total, Rows: []recentCallRow{}}
	for rows.Next() {
		var (
			id, inTok, outTok, latency, attemptCount int64
			platformCol, modelID, outcome            string
			routedFrom, errMsg, keyLabel             sql.NullString
			createdAt                                int64
		)
		if err := rows.Scan(&id, &platformCol, &modelID, &routedFrom, &outcome,
			&inTok, &outTok, &latency, &errMsg, &createdAt, &attemptCount, &keyLabel); err != nil {
			continue
		}
		row := recentCallRow{
			ID:           id,
			Platform:     platformCol,
			ModelID:      modelID,
			RequestType:  "chat",
			Status:       outcome,
			InputTokens:  inTok,
			OutputTokens: outTok,
			LatencyMs:    latency,
			CreatedAt:    isoZ(createdAt),
			AttemptCount: attemptCount,
		}
		if routedFrom.Valid && routedFrom.String != "" {
			row.RequestedModel = new(routedFrom.String)
		}
		if errMsg.Valid {
			row.Error = new(errMsg.String)
		}
		if keyLabel.Valid {
			row.KeyLabel = new(keyLabel.String)
		}
		resp.Rows = append(resp.Rows, row)
	}
	WriteJSON(w, http.StatusOK, resp)
}

var slugRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ── Per-request detail ───────────────────────────────────────────────────────

type attemptRow struct {
	Ordinal       int64   `json:"ordinal"`
	Platform      string  `json:"platform"`
	ModelID       string  `json:"modelId"`
	KeyOrdinal    int64   `json:"keyOrdinal"`
	KeyLabel      *string `json:"keyLabel"`
	Outcome       string  `json:"outcome"`
	StartOffsetMs int64   `json:"startOffsetMs"`
	DurationMs    int64   `json:"durationMs"`
	ErrorSummary  *string `json:"errorSummary"`
}

type requestDetail struct {
	ID              int64        `json:"id"`
	Platform        string       `json:"platform"`
	ModelID         string       `json:"modelId"`
	RequestedModel  *string      `json:"requestedModel"`
	ServedModel     *string      `json:"servedModel"`
	RequestType     string       `json:"requestType"`
	Status          string       `json:"status"`
	InputTokens     int64        `json:"inputTokens"`
	OutputTokens    int64        `json:"outputTokens"`
	LatencyMs       int64        `json:"latencyMs"`
	TtfbMs          *int64       `json:"ttfbMs"`
	Error           *string      `json:"error"`
	ClientIP        *string      `json:"clientIp"`
	ClientUserAgent *string      `json:"clientUserAgent"`
	ClientAgent     *string      `json:"clientAgent"`
	CreatedAt       string       `json:"createdAt"`
	Attempts        []attemptRow `json:"attempts"`
}

func (s *Server) handleAnalyticsRequestDetail(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	ctx := r.Context()
	db := s.engine.DB()

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		// Bare-string family, matching analytics.ts:726.
		WriteBareError(w, http.StatusBadRequest, "invalid request id")
		return
	}

	var (
		platform, modelID, outcome string
		routedFrom, errMsg         sql.NullString
		inTok, outTok, latency     int64
		ttfb                       sql.NullInt64
		createdAt                  int64
	)
	err = db.QueryRowContext(ctx, `
		SELECT platform, model_id, routed_from, outcome, input_tokens, output_tokens,
		       latency_ms, ttfb_ms, error_message, created_at
		  FROM requests WHERE id = ?`, id).
		Scan(&platform, &modelID, &routedFrom, &outcome, &inTok, &outTok, &latency, &ttfb, &errMsg, &createdAt)
	if err == sql.ErrNoRows {
		WriteBareError(w, http.StatusNotFound, "request not found")
		return
	}
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "analytics query failed")
		return
	}

	detail := requestDetail{
		ID:           id,
		Platform:     platform,
		ModelID:      modelID,
		RequestType:  "chat",
		Status:       outcome,
		InputTokens:  inTok,
		OutputTokens: outTok,
		LatencyMs:    latency,
		CreatedAt:    isoZ(createdAt),
		Attempts:     []attemptRow{},
	}
	if routedFrom.Valid && routedFrom.String != "" {
		detail.RequestedModel = new(routedFrom.String)
	}
	if ttfb.Valid {
		detail.TtfbMs = new(ttfb.Int64)
	}
	if errMsg.Valid {
		detail.Error = new(errMsg.String)
	}

	attemptRows, err := db.QueryContext(ctx, `
		SELECT a.attempt, a.platform, a.model_id, a.key_id, a.error_kind,
		       a.error_message, a.latency_ms, a.created_at, k.label
		  FROM request_attempts a
		  LEFT JOIN api_keys k ON k.id = a.key_id
		 WHERE a.request_id = ? ORDER BY a.attempt ASC`, id)
	if err == nil {
		// keyOrdinal anonymizes the internal key id into a per-request key1/
		// key2 the same way X-Fallback-Trail does; internal ids never leak.
		keyOrdinals := map[int64]int64{}
		var firstCreated int64 = -1
		for attemptRows.Next() {
			var (
				attempt, latencyA, createdA int64
				platformA, modelA           string
				keyID                       sql.NullInt64
				errKind, errMsgA, label     sql.NullString
			)
			if err := attemptRows.Scan(&attempt, &platformA, &modelA, &keyID, &errKind,
				&errMsgA, &latencyA, &createdA, &label); err != nil {
				continue
			}
			if firstCreated < 0 {
				firstCreated = createdA
			}
			var keyOrd int64
			if keyID.Valid {
				if o, ok := keyOrdinals[keyID.Int64]; ok {
					keyOrd = o
				} else {
					keyOrd = int64(len(keyOrdinals) + 1)
					keyOrdinals[keyID.Int64] = keyOrd
				}
			}
			outcomeA := "ok"
			if errKind.Valid && errKind.String != "" {
				outcomeA = errKind.String
			}
			offset := (createdA - firstCreated) * 1000
			if offset < 0 {
				offset = 0
			}
			row := attemptRow{
				Ordinal:       attempt,
				Platform:      platformA,
				ModelID:       modelA,
				KeyOrdinal:    keyOrd,
				Outcome:       outcomeA,
				StartOffsetMs: offset,
				DurationMs:    latencyA,
			}
			if label.Valid {
				row.KeyLabel = new(label.String)
			}
			if errMsgA.Valid && errMsgA.String != "" {
				row.ErrorSummary = new(errMsgA.String)
			}
			detail.Attempts = append(detail.Attempts, row)
		}
		_ = attemptRows.Close()
	}

	WriteJSON(w, http.StatusOK, detail)
}

// ── Rollup write path ────────────────────────────────────────────────────────

// RequestAttemptLog is one upstream hop of a served request.
type RequestAttemptLog struct {
	Attempt      int
	Platform     string
	ModelID      string
	KeyID        *int64
	StatusCode   int
	LatencyMs    int64
	ErrorKind    string
	ErrorMessage string
	CreatedAt    time.Time
}

// RequestLog is one completed inference call to persist. It is the input to the
// rollup write path.
type RequestLog struct {
	CreatedAt      time.Time
	Platform       string
	ModelID        string
	EndpointScope  string
	KeyID          *int64
	Outcome        string // "success" | "error" | "canceled"
	StatusCode     int
	InputTokens    int64
	OutputTokens   int64
	Estimated      bool
	LatencyMs      int64
	TTFBMs         *int64
	CostUSD        float64
	Attempts       int
	RequestedModel *string // the client-pinned model id; nil for auto/fusion
	Class          string
	Effort         string
	ErrorKind      string
	ErrorMessage   string
	Trail          []RequestAttemptLog
}

// RecordRequest persists a completed call and, in the same transaction, folds
// it into the durable request_hourly rollup and the lifetime settings counters.
//
// Nothing else populates these tables yet -- the inference proxy still records
// to the legacy JSONL usage log -- so this is the sole writer of the SQL trail
// the analytics surface reads. The rollup upsert is what lets a headline number
// survive the retention prune of the raw requests table.
func (s *Server) RecordRequest(ctx context.Context, log RequestLog) (int64, error) {
	if log.CreatedAt.IsZero() {
		log.CreatedAt = time.Now()
	}
	if log.Outcome == "" {
		log.Outcome = "success"
	}
	attempts := log.Attempts
	if attempts < 1 {
		attempts = 1
	}
	createdUnix := log.CreatedAt.UTC().Unix()

	tx, err := s.engine.DB().BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `
		INSERT INTO requests
		  (created_at, platform, model_id, endpoint_scope, key_id, status, outcome,
		   input_tokens, output_tokens, estimated, latency_ms, ttfb_ms, cost_usd,
		   attempts, routed_from, class, effort, error_kind, error_message)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		createdUnix, log.Platform, log.ModelID, log.EndpointScope, nullInt(log.KeyID),
		log.StatusCode, log.Outcome, log.InputTokens, log.OutputTokens, boolInt(log.Estimated),
		log.LatencyMs, nullInt(log.TTFBMs), log.CostUSD, attempts,
		nullStr(log.RequestedModel), nullEmpty(log.Class), nullEmpty(log.Effort),
		nullEmpty(log.ErrorKind), nullEmpty(log.ErrorMessage))
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}

	for _, a := range log.Trail {
		created := a.CreatedAt
		if created.IsZero() {
			created = log.CreatedAt
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO request_attempts
			  (request_id, attempt, platform, model_id, key_id, status, latency_ms,
			   error_kind, error_message, created_at)
			VALUES (?,?,?,?,?,?,?,?,?,?)`,
			id, a.Attempt, a.Platform, a.ModelID, nullInt(a.KeyID), a.StatusCode,
			a.LatencyMs, nullEmpty(a.ErrorKind), nullEmpty(a.ErrorMessage), created.UTC().Unix()); err != nil {
			return 0, err
		}
	}

	var success, failure int64
	switch log.Outcome {
	case "success":
		success = 1
	case "error":
		failure = 1
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO request_hourly
		  (hour_start, platform, model_id, endpoint_scope, requests, successes,
		   failures, input_tokens, output_tokens, cost_usd, latency_ms_sum)
		VALUES (?,?,?,?,1,?,?,?,?,?,?)
		ON CONFLICT(hour_start, platform, model_id, endpoint_scope) DO UPDATE SET
		  requests       = requests + 1,
		  successes      = successes + excluded.successes,
		  failures       = failures + excluded.failures,
		  input_tokens   = input_tokens + excluded.input_tokens,
		  output_tokens  = output_tokens + excluded.output_tokens,
		  cost_usd       = cost_usd + excluded.cost_usd,
		  latency_ms_sum = latency_ms_sum + excluded.latency_ms_sum`,
		floorHour(createdUnix), log.Platform, log.ModelID, log.EndpointScope,
		success, failure, log.InputTokens, log.OutputTokens, log.CostUSD, log.LatencyMs); err != nil {
		return 0, err
	}

	now := time.Now().Unix()
	if err := incrSetting(ctx, tx, "total_requests", 1, now); err != nil {
		return 0, err
	}
	if err := incrSetting(ctx, tx, "total_input_tokens", log.InputTokens, now); err != nil {
		return 0, err
	}
	if err := incrSetting(ctx, tx, "total_output_tokens", log.OutputTokens, now); err != nil {
		return 0, err
	}
	// first_request_at is set once and never pruned, so "all time" survives the
	// raw-row prune entirely.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO settings (key, value, updated_at) VALUES ('first_request_at', ?, ?)
		ON CONFLICT(key) DO NOTHING`, sqliteDateTime(createdUnix), now); err != nil {
		return 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

// incrSetting adds delta to an integer-valued settings row, creating it at
// delta when absent. The arithmetic is one statement so it stays atomic under
// concurrent writers.
func incrSetting(ctx context.Context, tx *sql.Tx, key string, delta, now int64) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET
		  value = CAST(CAST(settings.value AS INTEGER) + CAST(excluded.value AS INTEGER) AS TEXT),
		  updated_at = excluded.updated_at`,
		key, strconv.FormatInt(delta, 10), now)
	return err
}

// ── Server-log store ─────────────────────────────────────────────────────────

// serverLogRecord is one line offered to the store. Only warn/error tiers are
// durable, mirroring the reference's PERSISTED_LEVELS (server-logs.ts:38).
type serverLogRecord struct {
	Level     string
	Source    string
	Provider  string
	Model     string
	Event     string
	RequestID string
	Message   string
	CreatedAt time.Time
}

type serverLogEntry struct {
	ID        int64
	Level     string
	TSms      int64
	Source    string
	Provider  string
	Model     string
	Event     string
	RequestID string
	Message   string
}

// serverLogStore is the durable warn/error store behind GET /api/logs. Its id
// counter is application-assigned (the table's id is not AUTOINCREMENT) and
// seeded from MAX(id) at boot, so a persisted row and a freshly minted one
// never share an id -- the property the poller's cursor depends on.
type serverLogStore struct {
	db       *sql.DB
	seedOnce sync.Once
	mu       sync.Mutex
	lastID   int64
}

var (
	logStoresMu sync.Mutex
	logStores   = map[*sql.DB]*serverLogStore{}
)

// logStoreFor returns the store bound to a database, one per DB so parallel
// tests (each with their own DB) do not share a counter.
func logStoreFor(db *sql.DB) *serverLogStore {
	logStoresMu.Lock()
	defer logStoresMu.Unlock()
	if st, ok := logStores[db]; ok {
		return st
	}
	st := &serverLogStore{db: db}
	logStores[db] = st
	return st
}

// seed reads MAX(id) once. The query runs outside the store's own mutex so no
// lock is ever held across the I/O.
func (st *serverLogStore) seed(ctx context.Context) {
	st.seedOnce.Do(func() {
		var max sql.NullInt64
		_ = st.db.QueryRowContext(ctx, `SELECT MAX(id) FROM server_logs`).Scan(&max)
		st.mu.Lock()
		if max.Valid && max.Int64 > st.lastID {
			st.lastID = max.Int64
		}
		st.mu.Unlock()
	})
}

// record assigns the next id, then persists the line if its level is durable.
// The id is minted under the lock; the INSERT runs after releasing it.
func (st *serverLogStore) record(ctx context.Context, rec serverLogRecord) (serverLogEntry, error) {
	st.seed(ctx)
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = time.Now()
	}
	st.mu.Lock()
	st.lastID++
	id := st.lastID
	st.mu.Unlock()

	entry := serverLogEntry{
		ID: id, Level: rec.Level, TSms: rec.CreatedAt.UnixMilli(),
		Source: rec.Source, Provider: rec.Provider, Model: rec.Model,
		Event: rec.Event, RequestID: rec.RequestID, Message: rec.Message,
	}
	if rec.Level == "warn" || rec.Level == "error" {
		if _, err := st.db.ExecContext(ctx, `
			INSERT INTO server_logs
			  (id, level, source, provider, model, event, request_id, message, created_at_ms)
			VALUES (?,?,?,?,?,?,?,?,?)`,
			id, rec.Level, nullEmpty(rec.Source), nullEmpty(rec.Provider), nullEmpty(rec.Model),
			nullEmpty(rec.Event), nullEmpty(rec.RequestID), rec.Message, entry.TSms); err != nil {
			return entry, err
		}
	}
	return entry, nil
}

// logServerEvent persists a warn/error line for the Activity > Logs view. It
// writes on a cancel-detached context and swallows the error into slog.Debug,
// like the neighbouring RecordRequest calls, so surfacing a failure never
// blocks or fails the request that detected it. Only warn/error persist by
// design, so an info/debug caller writes nothing.
func (s *Server) logServerEvent(ctx context.Context, rec serverLogRecord) {
	if _, err := logStoreFor(s.engine.DB()).record(context.WithoutCancel(ctx), rec); err != nil {
		slog.Debug("Could not record server log", "event", rec.Event, "error", err)
	}
}

// maxID is the highest id handed out so far -- what the client echoes as
// sinceId. It advances on every recorded line regardless of level or filter,
// so a poll whose matches were all filtered out still moves the cursor forward
// instead of re-scanning the same tail forever.
func (st *serverLogStore) maxID(ctx context.Context) int64 {
	st.seed(ctx)
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.lastID
}

func (st *serverLogStore) query(ctx context.Context, sinceID *int64, levels []string, needle, provider string, limit int) ([]serverLogEntry, error) {
	// A caller already caught up costs one comparison, not a scan.
	if sinceID != nil && *sinceID >= st.maxID(ctx) {
		return nil, nil
	}
	conds := []string{}
	var args []any
	if sinceID != nil {
		conds = append(conds, "id > ?")
		args = append(args, *sinceID)
	}
	if len(levels) > 0 {
		conds = append(conds, "level IN ("+placeholders(len(levels))+")")
		for _, l := range levels {
			args = append(args, l)
		}
	}
	if provider != "" {
		conds = append(conds, "provider = ?")
		args = append(args, provider)
	}
	if needle != "" {
		like := "%" + strings.ToLower(needle) + "%"
		conds = append(conds, "(lower(message) LIKE ? OR lower(COALESCE(provider,'')) LIKE ? OR lower(COALESCE(source,'')) LIKE ? OR lower(COALESCE(event,'')) LIKE ?)")
		args = append(args, like, like, like, like)
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	args = append(args, limit)

	rows, err := st.db.QueryContext(ctx, `
		SELECT id, level, source, provider, model, event, request_id, message, created_at_ms
		  FROM server_logs`+where+`
		 ORDER BY id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	entries := []serverLogEntry{}
	for rows.Next() {
		var (
			e                              serverLogEntry
			level, source, provider, model sql.NullString
			event, requestID               sql.NullString
		)
		if err := rows.Scan(&e.ID, &level, &source, &provider, &model, &event, &requestID, &e.Message, &e.TSms); err != nil {
			continue
		}
		e.Level = level.String
		e.Source, e.Provider, e.Model = source.String, provider.String, model.String
		e.Event, e.RequestID = event.String, requestID.String
		entries = append(entries, e)
	}
	// Walked newest-first for the LIMIT; hand them back oldest->newest.
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}
	return entries, nil
}

type logLevelCounts struct {
	Debug int64 `json:"debug"`
	Info  int64 `json:"info"`
	Warn  int64 `json:"warn"`
	Error int64 `json:"error"`
}

// counts are the per-level totals over the most recent window, for the level
// badges. Only warn/error are persisted, so debug/info stay zero until a log
// producer is wired.
func (st *serverLogStore) counts(ctx context.Context) logLevelCounts {
	counts := logLevelCounts{}
	rows, err := st.db.QueryContext(ctx, `
		SELECT level, COUNT(*) FROM
		  (SELECT level FROM server_logs ORDER BY id DESC LIMIT 1000)
		 GROUP BY level`)
	if err != nil {
		return counts
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var level sql.NullString
		var n int64
		if err := rows.Scan(&level, &n); err != nil {
			continue
		}
		switch level.String {
		case "warn":
			counts.Warn += n
		case "error":
			counts.Error += n
		case "trace", "debug":
			counts.Debug += n
		case "info":
			counts.Info += n
		}
	}
	return counts
}

// clear empties the durable rows. The id counter is deliberately NOT reset: a
// dashboard tab holding a cursor would otherwise be handed ids it has already
// seen (server-logs.ts:473-474).
func (st *serverLogStore) clear(ctx context.Context) error {
	st.seed(ctx)
	_, err := st.db.ExecContext(ctx, `DELETE FROM server_logs`)
	return err
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimRight(strings.Repeat("?,", n), ",")
}

// ── Log routes ───────────────────────────────────────────────────────────────

var knownLogLevels = map[string]bool{
	"trace": true, "debug": true, "info": true, "warn": true, "error": true,
}

type logEntryJSON struct {
	ID        int64  `json:"id"`
	TS        string `json:"ts"`
	Level     string `json:"level"`
	Source    string `json:"source,omitempty"`
	Provider  string `json:"provider,omitempty"`
	Model     string `json:"model,omitempty"`
	Event     string `json:"event,omitempty"`
	RequestID string `json:"requestId,omitempty"`
	Message   string `json:"message"`
}

type logsResponse struct {
	Entries []logEntryJSON `json:"entries"`
	NextID  int64          `json:"nextId"`
	Counts  logLevelCounts `json:"counts"`
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	ctx := r.Context()
	st := logStoreFor(s.engine.DB())
	q := r.URL.Query()

	var levels []string
	if raw := strings.TrimSpace(q.Get("levels")); raw != "" {
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			// An unknown level is rejected, not ignored: a silently filtered
			// view that does not match the requested filter is worse than an
			// error (logs.ts:61-68). This uses the {error:{message}} envelope,
			// not the bare-string one, matching logs.ts:31.
			if !knownLogLevels[part] {
				WriteError(w, http.StatusBadRequest, "", "Unknown log level '"+part+"'. Known levels: trace, debug, info, warn, error")
				return
			}
			levels = append(levels, part)
		}
	}

	var sinceID *int64
	if raw := strings.TrimSpace(q.Get("sinceId")); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || v < 0 {
			WriteError(w, http.StatusBadRequest, "", "sinceId must be a non-negative integer")
			return
		}
		sinceID = &v
	}

	// A limit is a preference: clamp an out-of-range one rather than reject it
	// (logs.ts:83-86, server-logs.ts:415-422).
	limit := 200
	if raw := strings.TrimSpace(q.Get("limit")); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil {
			limit = v
		}
	}
	if limit < 1 {
		limit = 1
	}
	if limit > 500 {
		limit = 500
	}

	entries, err := st.query(ctx, sinceID, levels, strings.TrimSpace(q.Get("q")), strings.TrimSpace(q.Get("provider")), limit)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "log query failed")
		return
	}

	resp := logsResponse{Entries: []logEntryJSON{}, NextID: st.maxID(ctx), Counts: st.counts(ctx)}
	for _, e := range entries {
		resp.Entries = append(resp.Entries, logEntryJSON{
			ID:        e.ID,
			TS:        time.UnixMilli(e.TSms).UTC().Format("2006-01-02T15:04:05.000Z"),
			Level:     e.Level,
			Source:    e.Source,
			Provider:  e.Provider,
			Model:     e.Model,
			Event:     e.Event,
			RequestID: e.RequestID,
			Message:   e.Message,
		})
	}
	WriteJSON(w, http.StatusOK, resp)
}

func (s *Server) handleLogsClear(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	if err := logStoreFor(s.engine.DB()).clear(r.Context()); err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "clearing logs failed")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}
