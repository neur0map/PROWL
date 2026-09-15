package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/neur0map/prowl/internal/gateway"
)

// callAnalytics invokes a session-gated handler directly with a zero user: the
// gate itself is covered in server_test.go, and these tests are about the data
// each view returns.
func callAnalytics(t *testing.T, h func(http.ResponseWriter, *http.Request, gateway.SessionUser), target string, dst any) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	h(rec, req, gateway.SessionUser{})
	res := rec.Result()
	if dst != nil {
		body, _ := io.ReadAll(res.Body)
		require.NoError(t, json.Unmarshal(body, dst), "body=%s", body)
	}
	return res
}

func record(t *testing.T, s *Server, log RequestLog) int64 {
	t.Helper()
	id, err := s.RecordRequest(context.Background(), log)
	require.NoError(t, err)
	return id
}

// TestSummaryWindowExcludesRowsOutsideRange proves the range picker actually
// bounds the totals: a request older than the window must not be counted.
func TestSummaryWindowExcludesRowsOutsideRange(t *testing.T) {
	s := testServer(t, Options{})
	now := time.Now()

	record(t, s, RequestLog{CreatedAt: now, Platform: "groq", ModelID: "llama", Outcome: "success", InputTokens: 10})
	record(t, s, RequestLog{CreatedAt: now, Platform: "groq", ModelID: "llama", Outcome: "success", InputTokens: 10})
	record(t, s, RequestLog{CreatedAt: now.Add(-10 * 24 * time.Hour), Platform: "groq", ModelID: "llama", Outcome: "success", InputTokens: 10})

	var day summaryResponse
	callAnalytics(t, s.handleAnalyticsSummary, "/api/analytics/summary?range=24h", &day)
	require.Equal(t, int64(2), day.TotalRequests, "the 10-day-old request is outside the 24h window")

	var month summaryResponse
	callAnalytics(t, s.handleAnalyticsSummary, "/api/analytics/summary?range=30d", &month)
	require.Equal(t, int64(3), month.TotalRequests, "the 30d window includes all three")
}

// TestNullableStatsStayNull is the "-" vs "0 ms" contract: an absent
// measurement serialises as null, and a present one does not.
func TestNullableStatsStayNull(t *testing.T) {
	t.Run("no samples at all", func(t *testing.T) {
		s := testServer(t, Options{})
		var resp summaryResponse
		callAnalytics(t, s.handleAnalyticsSummary, "/api/analytics/summary?range=7d", &resp)
		require.Nil(t, resp.P50LatencyMs)
		require.Nil(t, resp.P95LatencyMs)
		require.Nil(t, resp.AvgTtfbMs)
	})

	t.Run("latency sample but no TTFT", func(t *testing.T) {
		s := testServer(t, Options{})
		record(t, s, RequestLog{Platform: "groq", ModelID: "llama", Outcome: "success", LatencyMs: 250})
		var resp summaryResponse
		callAnalytics(t, s.handleAnalyticsSummary, "/api/analytics/summary?range=7d", &resp)
		require.NotNil(t, resp.P50LatencyMs, "a latency sample exists, so the percentile is a number")
		require.Equal(t, int64(250), *resp.P50LatencyMs)
		require.Nil(t, resp.AvgTtfbMs, "no streaming row recorded a TTFT")
	})

	t.Run("TTFT sample", func(t *testing.T) {
		s := testServer(t, Options{})
		ttfb := int64(123)
		record(t, s, RequestLog{Platform: "groq", ModelID: "llama", Outcome: "success", LatencyMs: 400, TTFBMs: &ttfb})
		var resp summaryResponse
		callAnalytics(t, s.handleAnalyticsSummary, "/api/analytics/summary?range=7d", &resp)
		require.NotNil(t, resp.AvgTtfbMs)
		require.Equal(t, int64(123), *resp.AvgTtfbMs)
	})
}

// TestBreakdownGroupsByProviderAndModel proves two custom relays serving the
// same model id are told apart (the #889 collision), and a catalog provider
// keeps its bare slug.
func TestBreakdownGroupsByProviderAndModel(t *testing.T) {
	s := testServer(t, Options{})
	const a = "https://a.example.com/v1"
	const b = "https://b.example.com/v1"

	record(t, s, RequestLog{Platform: "groq", ModelID: "llama", Outcome: "success"})
	record(t, s, RequestLog{Platform: "custom", ModelID: "gpt", EndpointScope: a, Outcome: "success"})
	record(t, s, RequestLog{Platform: "custom", ModelID: "gpt", EndpointScope: a, Outcome: "error"})
	record(t, s, RequestLog{Platform: "custom", ModelID: "gpt", EndpointScope: b, Outcome: "success"})

	var models []byModelRow
	callAnalytics(t, s.handleAnalyticsByModel, "/api/analytics/by-model?range=7d", &models)
	byID := map[string]byModelRow{}
	for _, m := range models {
		byID[m.ProviderID] = m
	}
	require.Len(t, models, 3, "groq + two distinct custom endpoints, not one merged 'custom' row")
	require.Contains(t, byID, "groq")
	require.Contains(t, byID, "custom:"+a)
	require.Contains(t, byID, "custom:"+b)
	require.Equal(t, "a.example.com", byID["custom:"+a].Endpoint, "the bare /v1 path is dropped from the display name")
	require.Equal(t, int64(2), byID["custom:"+a].Requests, "the two calls on endpoint a group together")
	require.Equal(t, int64(1), byID["custom:"+b].Requests)

	var platforms []byPlatformRow
	callAnalytics(t, s.handleAnalyticsByPlatform, "/api/analytics/by-platform?range=7d", &platforms)
	seen := map[string]bool{}
	for _, p := range platforms {
		seen[p.ProviderID] = true
	}
	require.Len(t, platforms, 3, "by-platform splits the two custom endpoints as well")
	require.True(t, seen["custom:"+a] && seen["custom:"+b] && seen["groq"])
}

// TestPruningRawTrailLeavesRollupSummaryIntact is trap 5: the headline totals
// read the durable hourly rollup, so pruning the raw requests table leaves them
// standing while the raw-only percentiles degrade to null.
func TestPruningRawTrailLeavesRollupSummaryIntact(t *testing.T) {
	s := testServer(t, Options{})
	ttfb := int64(80)
	for range 3 {
		record(t, s, RequestLog{Platform: "groq", ModelID: "llama", Outcome: "success", InputTokens: 100, OutputTokens: 50, LatencyMs: 200, TTFBMs: &ttfb})
	}

	var before summaryResponse
	callAnalytics(t, s.handleAnalyticsSummary, "/api/analytics/summary?range=7d", &before)
	require.Equal(t, int64(3), before.TotalRequests)
	require.Equal(t, int64(300), before.TotalInputTokens)
	require.NotNil(t, before.P50LatencyMs)
	require.NotNil(t, before.AvgTtfbMs)

	// Simulate the retention prune of the raw trail. The rollup is untouched.
	_, err := s.engine.DB().Exec(`DELETE FROM requests`)
	require.NoError(t, err)

	var after summaryResponse
	callAnalytics(t, s.handleAnalyticsSummary, "/api/analytics/summary?range=7d", &after)
	require.Equal(t, int64(3), after.TotalRequests, "the rollup keeps the headline count after the prune")
	require.Equal(t, int64(300), after.TotalInputTokens)
	require.Equal(t, int64(3), after.LifetimeTotalRequests, "lifetime counter survives too")
	require.NotNil(t, after.FirstRequestAt)
	require.Nil(t, after.P50LatencyMs, "the raw-only percentile is null once the trail is gone, never a false 0")
	require.Nil(t, after.AvgTtfbMs)
}

// TestBareStringErrorFamily proves the recent-calls filters use the older bare
// {"error":"..."} family (analytics.ts:602,:726), while the logs route uses the
// {"error":{"message":...}} family -- the two are not interchangeable.
func TestBareStringErrorFamily(t *testing.T) {
	s := testServer(t, Options{})

	t.Run("invalid status filter is a bare string", func(t *testing.T) {
		res := callAnalytics(t, s.handleAnalyticsRequests, "/api/analytics/requests?status=bogus", nil)
		require.Equal(t, http.StatusBadRequest, res.StatusCode)
		body, _ := io.ReadAll(res.Body)
		var m map[string]any
		require.NoError(t, json.Unmarshal(body, &m))
		_, isString := m["error"].(string)
		require.True(t, isString, "want a bare-string error, got %s", body)
	})

	t.Run("invalid request id is a bare string", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/analytics/requests/abc", nil)
		req.SetPathValue("id", "abc")
		rec := httptest.NewRecorder()
		s.handleAnalyticsRequestDetail(rec, req, gateway.SessionUser{})
		res := rec.Result()
		require.Equal(t, http.StatusBadRequest, res.StatusCode)
		body, _ := io.ReadAll(res.Body)
		var m map[string]any
		require.NoError(t, json.Unmarshal(body, &m))
		_, isString := m["error"].(string)
		require.True(t, isString, "want a bare-string error, got %s", body)
	})

	t.Run("logs uses the object-message family, not bare", func(t *testing.T) {
		res := callAnalytics(t, s.handleLogs, "/api/logs?levels=bogus", nil)
		require.Equal(t, http.StatusBadRequest, res.StatusCode)
		body, _ := io.ReadAll(res.Body)
		var m map[string]any
		require.NoError(t, json.Unmarshal(body, &m))
		obj, isObj := m["error"].(map[string]any)
		require.True(t, isObj, "logs errors are objects, got %s", body)
		require.NotEmpty(t, obj["message"])
	})
}

// TestLogCursorAdvancesAndDoesNotRepeat is the poller contract: an empty page
// still advances the cursor (so it never spins on the same id), a filtered-out
// tail advances it too, and no row is handed back twice across pages.
func TestLogCursorAdvancesAndDoesNotRepeat(t *testing.T) {
	s := testServer(t, Options{})
	st := logStoreFor(s.engine.DB())
	ctx := context.Background()
	for i := range 3 {
		_, err := st.record(ctx, serverLogRecord{Level: "warn", Message: fmt.Sprintf("w%d", i)})
		require.NoError(t, err)
	}

	var first logsResponse
	callAnalytics(t, s.handleLogs, "/api/logs?levels=warn", &first)
	require.Len(t, first.Entries, 3)
	require.Equal(t, int64(3), first.NextID)
	require.Equal(t, int64(1), first.Entries[0].ID, "entries come back oldest-first")
	require.Equal(t, int64(3), first.Entries[2].ID)

	var caughtUp logsResponse
	callAnalytics(t, s.handleLogs, "/api/logs?sinceId=3", &caughtUp)
	require.Empty(t, caughtUp.Entries, "a caller at the head gets an empty page")
	require.Equal(t, int64(3), caughtUp.NextID, "the cursor holds at the head, it does not rewind")

	for i := 3; i < 5; i++ {
		_, err := st.record(ctx, serverLogRecord{Level: "warn", Message: fmt.Sprintf("w%d", i)})
		require.NoError(t, err)
	}
	var next logsResponse
	callAnalytics(t, s.handleLogs, "/api/logs?sinceId=3", &next)
	require.Len(t, next.Entries, 2, "only rows past the cursor, never a repeat of 1-3")
	require.Equal(t, int64(4), next.Entries[0].ID)
	require.Equal(t, int64(5), next.Entries[1].ID)
	require.Equal(t, int64(5), next.NextID)

	// A warn-only filter over a tail whose newest row is an error must still
	// advance the cursor past that filtered row, or the poller spins forever.
	_, err := st.record(ctx, serverLogRecord{Level: "error", Message: "boom"})
	require.NoError(t, err)
	var filtered logsResponse
	callAnalytics(t, s.handleLogs, "/api/logs?levels=warn&sinceId=5", &filtered)
	require.Empty(t, filtered.Entries, "the id-6 error does not match the warn filter")
	require.Equal(t, int64(6), filtered.NextID, "but the cursor advanced past it anyway")
}

// TestLogClearEmptiesRowsButKeepsCursor: clearing wipes the durable rows, yet
// the id counter must not rewind, or a tab holding a cursor is re-served ids it
// has already seen.
func TestLogClearEmptiesRowsButKeepsCursor(t *testing.T) {
	s := testServer(t, Options{})
	st := logStoreFor(s.engine.DB())
	ctx := context.Background()
	for i := range 3 {
		_, err := st.record(ctx, serverLogRecord{Level: "error", Message: fmt.Sprintf("e%d", i)})
		require.NoError(t, err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/logs/clear", nil)
	rec := httptest.NewRecorder()
	s.handleLogsClear(rec, req, gateway.SessionUser{})
	require.Equal(t, http.StatusOK, rec.Result().StatusCode)

	var after logsResponse
	callAnalytics(t, s.handleLogs, "/api/logs", &after)
	require.Empty(t, after.Entries)
	require.Equal(t, int64(3), after.NextID, "the counter is not reset by a clear")
}

// TestTimelineBucketsByHour proves the timeline reads the durable rollup and
// buckets it on the hour the viewer's clock sees. All three calls land in one
// hour, so they collapse to one point carrying the success/failure split.
func TestTimelineBucketsByHour(t *testing.T) {
	s := testServer(t, Options{})
	base := time.Now().Truncate(time.Hour)
	at := base.Add(30 * time.Minute)
	record(t, s, RequestLog{CreatedAt: at, Platform: "groq", ModelID: "llama", Outcome: "success"})
	record(t, s, RequestLog{CreatedAt: at, Platform: "groq", ModelID: "llama", Outcome: "success"})
	record(t, s, RequestLog{CreatedAt: at, Platform: "groq", ModelID: "llama", Outcome: "error"})

	var points []timelinePoint
	callAnalytics(t, s.handleAnalyticsTimeline, "/api/analytics/timeline?range=24h&interval=hour&tzOffset=0", &points)
	require.Len(t, points, 1, "all three calls share one hour bucket")
	require.Equal(t, base.UTC().Format("2006-01-02T15")+":00:00", points[0].Timestamp)
	require.Equal(t, int64(3), points[0].Requests)
	require.Equal(t, int64(2), points[0].SuccessCount)
	require.Equal(t, int64(1), points[0].FailureCount)
}
