package api

import (
	"context"
	"math"
	"net/http"
	"sort"
	"time"

	"github.com/neur0map/prowl/internal/gateway"
	"github.com/neur0map/prowl/internal/gateway/catalog"
)

// The machine-readable status pair from the reference (status.ts:112-239):
// GET /v1/providers and GET /v1/quota-forecast.
//
// Both sit behind the unified key like the rest of /v1 and carry no key
// material. They exist so a caller in front of this gateway can decide "route
// here or skip" and "is this pool nearly spent" WITHOUT spending a request to
// find out — the dashboard's own views are session-authenticated and shaped
// for a browser, so they cannot serve that purpose.

// Low-balance thresholds, ported from quota-forecast.ts:17-23. The absolute
// floor only applies to windows large enough for "20 left" to be alarming:
// below that a small tier would warn from its first request onwards.
const (
	lowBalanceThreshold       = 0.1
	lowBalanceAbsolute        = 20
	lowBalanceAbsoluteMinimum = 200
)

func (s *Server) registerStatusV1Routes() {
	s.mux.HandleFunc("GET /v1/providers", s.RequireMachineKey(s.handleV1Providers))
	s.mux.HandleFunc("GET /v1/quota-forecast", s.RequireMachineKey(s.handleV1QuotaForecast))
}

type v1Provider struct {
	Platform string `json:"platform"`
	Name     string `json:"name"`
	Status   string `json:"status"`
	Keys     int    `json:"keys"`

	ResumeAt             string `json:"resume_at,omitempty"`
	LastError            string `json:"last_error,omitempty"`
	RequestsRemainingPct *int   `json:"requests_remaining_pct,omitempty"`
}

func (s *Server) handleV1Providers(w http.ResponseWriter, r *http.Request) {
	rows, err := s.engine.DB().QueryContext(r.Context(), `
		SELECT platform,
		       SUM(CASE WHEN enabled = 1 THEN 1 ELSE 0 END) AS enabled_keys,
		       SUM(CASE WHEN enabled = 1 AND status = 'healthy' THEN 1 ELSE 0 END) AS healthy_keys,
		       SUM(CASE WHEN enabled = 1 AND status = 'unknown' THEN 1 ELSE 0 END) AS unknown_keys,
		       SUM(CASE WHEN enabled = 1 AND status = 'invalid' THEN 1 ELSE 0 END) AS invalid_keys,
		       SUM(CASE WHEN enabled = 1 AND status = 'error' THEN 1 ELSE 0 END) AS error_keys
		  FROM api_keys
		 GROUP BY platform`)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, err.Error())
		return
	}
	defer func() { _ = rows.Close() }()

	type platformRow struct {
		enabled, healthy, unknown, invalid, errored int
	}
	byPlatform := map[string]platformRow{}
	for rows.Next() {
		var (
			platform string
			row      platformRow
		)
		if err := rows.Scan(&platform, &row.enabled, &row.healthy,
			&row.unknown, &row.invalid, &row.errored); err != nil {
			WriteError(w, http.StatusInternalServerError, TypeServer, err.Error())
			return
		}
		byPlatform[platform] = row
	}
	if err := rows.Err(); err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, err.Error())
		return
	}

	// Cooldowns are per (platform, model, key); the earliest expiry is when
	// any of that platform's routes frees up again.
	resumeAt := map[string]time.Time{}
	for keyID, bench := range s.engine.Cooldowns().BenchedByKey() {
		platform, ok := s.keyPlatform(r.Context(), keyID)
		if !ok {
			continue
		}
		if prev, seen := resumeAt[platform]; !seen || bench.Until.Before(prev) {
			resumeAt[platform] = bench.Until
		}
	}

	lastError := map[string]string{}
	if errRows, err := s.engine.DB().QueryContext(r.Context(), `
		SELECT platform, last_health_error
		  FROM api_keys
		 WHERE enabled = 1 AND last_health_error IS NOT NULL
		 ORDER BY last_checked_at DESC`); err == nil {
		defer func() { _ = errRows.Close() }()
		for errRows.Next() {
			var platform, msg string
			if err := errRows.Scan(&platform, &msg); err != nil {
				break
			}
			if _, seen := lastError[platform]; !seen {
				lastError[platform] = msg
			}
		}
	}

	// Tightest observed request headroom per platform: with several keys on
	// one account pool, the number that decides "can I keep calling" is the
	// smallest.
	headroom := map[string]int{}
	states, _ := s.engine.Ledger().QuotaStates(r.Context())
	for _, st := range states {
		if st.RequestsLimit == nil || *st.RequestsLimit <= 0 || st.RequestsRemaining == nil {
			continue
		}
		pct := int(math.Round(float64(*st.RequestsRemaining) / float64(*st.RequestsLimit) * 100))
		pct = min(max(pct, 0), 100)
		if prev, seen := headroom[st.Platform]; !seen || pct < prev {
			headroom[st.Platform] = pct
		}
	}

	counts := map[string]int{"healthy": 0, "rate_limited": 0, "invalid": 0, "unknown": 0}
	providers := make([]v1Provider, 0, len(byPlatform))
	for platform, row := range byPlatform {
		if row.enabled == 0 {
			continue
		}
		resume, cooling := resumeAt[platform]

		var status string
		switch {
		case row.healthy > 0 && !cooling:
			status = "healthy"
		case cooling:
			status = "rate_limited"
		case row.unknown > 0:
			status = "unknown"
		case row.invalid > 0 || row.errored > 0:
			status = "invalid"
		default:
			status = "unknown"
		}
		counts[status]++

		entry := v1Provider{
			Platform: platform,
			Name:     catalogName(platform),
			Status:   status,
			Keys:     row.enabled,
		}
		if status == "rate_limited" {
			entry.ResumeAt = resume.UTC().Format(time.RFC3339)
		}
		if status == "invalid" {
			entry.LastError = gateway.RedactWith(lastError[platform])
		}
		if pct, ok := headroom[platform]; ok {
			entry.RequestsRemainingPct = &pct
		}
		providers = append(providers, entry)
	}
	sort.Slice(providers, func(i, j int) bool { return providers[i].Platform < providers[j].Platform })

	WriteJSON(w, http.StatusOK, map[string]any{"providers": providers, "counts": counts})
}

type quotaForecastEntry struct {
	Platform          string  `json:"platform"`
	Pool              string  `json:"pool"`
	Used              *int64  `json:"used"`
	Remaining         *int64  `json:"remaining"`
	Limit             *int64  `json:"limit"`
	RemainingPct      *int    `json:"remaining_pct"`
	ResetAt           *string `json:"reset_at"`
	LowBalance        bool    `json:"low_balance"`
	SecondsUntilReset *int64  `json:"seconds_until_reset"`
}

func (s *Server) handleV1QuotaForecast(w http.ResponseWriter, r *http.Request) {
	states, err := s.engine.Ledger().QuotaStates(r.Context())
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, err.Error())
		return
	}

	now := time.Now().UTC()
	// Deduped to the TIGHTEST row per pool: keys sharing an account report the
	// same window, and the least headroom is the one that binds.
	byPool := map[string]quotaForecastEntry{}
	for _, st := range states {
		// Only request windows are predictable from quota headers; token
		// pools reset too differently across providers to forecast honestly.
		if st.RequestsLimit == nil || *st.RequestsLimit <= 0 {
			continue
		}
		limit := *st.RequestsLimit
		entry := quotaForecastEntry{
			Platform: st.Platform,
			Pool:     st.PoolKey,
			Limit:    &limit,
		}
		if entry.Pool == "" {
			entry.Pool = st.Platform + "::default"
		}
		if st.RequestsRemaining != nil {
			remaining := *st.RequestsRemaining
			used := max(limit-remaining, 0)
			pct := min(max(int(math.Round(float64(remaining)/float64(limit)*100)), 0), 100)
			entry.Remaining = &remaining
			entry.Used = &used
			entry.RemainingPct = &pct
			entry.LowBalance = (limit >= lowBalanceAbsoluteMinimum && remaining <= lowBalanceAbsolute) ||
				float64(remaining)/float64(limit) < lowBalanceThreshold
		}
		if st.ResetsAt != nil {
			reset := time.Unix(*st.ResetsAt, 0).UTC()
			iso := reset.Format(time.RFC3339)
			entry.ResetAt = &iso
			if secs := int64(reset.Sub(now).Seconds()); secs > 0 {
				entry.SecondsUntilReset = &secs
			}
		}

		prev, seen := byPool[entry.Pool]
		if !seen || tighter(entry, prev) {
			byPool[entry.Pool] = entry
		}
	}

	pools := make([]quotaForecastEntry, 0, len(byPool))
	for _, entry := range byPool {
		pools = append(pools, entry)
	}
	sort.Slice(pools, func(i, j int) bool { return pools[i].Pool < pools[j].Pool })

	WriteJSON(w, http.StatusOK, map[string]any{
		"generated_at": now.Format(time.RFC3339),
		"low_balance_threshold": map[string]any{
			"pct":                lowBalanceThreshold,
			"absolute":           lowBalanceAbsolute,
			"absolute_min_limit": lowBalanceAbsoluteMinimum,
		},
		"pools": pools,
	})
}

// tighter reports whether a has less headroom than b. A known remaining always
// beats an unknown one: an unmeasured pool must not mask a nearly spent pool.
func tighter(a, b quotaForecastEntry) bool {
	if a.Remaining == nil {
		return false
	}
	if b.Remaining == nil {
		return true
	}
	return *a.Remaining < *b.Remaining
}

// catalogName is the provider's published name, falling back to the platform
// slug when the directory does not list it.
func catalogName(platform string) string {
	entries, err := catalog.Directory()
	if err != nil {
		return platform
	}
	for i := range entries {
		if resolved, ok := gateway.PlatformForCatalogID(entries[i].ID); ok && resolved == platform {
			return entries[i].Name
		}
		if entries[i].ID == platform {
			return entries[i].Name
		}
	}
	return platform
}

// keyPlatform resolves a key id to its platform.
func (s *Server) keyPlatform(ctx context.Context, keyID int64) (string, bool) {
	var platform string
	if err := s.engine.DB().QueryRowContext(ctx,
		"SELECT platform FROM api_keys WHERE id = ?", keyID).Scan(&platform); err != nil {
		return "", false
	}
	return platform, true
}
