package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/neur0map/prowl/internal/gateway"
)

// fakeUpstream stands in for a provider. It counts calls so a test can prove
// failover actually moved rather than merely returning the right status.
type fakeUpstream struct {
	*httptest.Server
	calls atomic.Int64
}

// newFakeUpstream serves an OpenAI-shaped completion, or the given status.
func newFakeUpstream(t *testing.T, status int, content string) *fakeUpstream {
	t.Helper()
	f := &fakeUpstream{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		if status != http.StatusOK {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = fmt.Fprintf(w, `{"error":{"message":"upstream says %d"}}`, status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"cmpl-1","object":"chat.completion","model":"m",
			"choices":[{"index":0,"message":{"role":"assistant","content":%q},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`, content)
	}))
	t.Cleanup(f.Close)
	return f
}

// seedRoute registers a custom-endpoint key and model so the chain has a
// candidate pointing at a fake upstream. Custom endpoints are the only way to
// aim a real provider adapter at a test server.
func seedRoute(t *testing.T, s *Server, label, baseURL, modelID string, position int) int64 {
	t.Helper()
	ctx := context.Background()

	keyID, err := s.engine.Vault().Add("custom", "test-key-"+label, gateway.AddOptions{
		Label:   label,
		BaseURL: baseURL,
	})
	require.NoError(t, err)

	res, err := s.engine.DB().ExecContext(ctx,
		`INSERT INTO models (platform, model_id, display_name, key_id, endpoint_scope,
		                     size_label, intelligence_rank, enabled, supports_tools)
		 VALUES ('custom', ?, ?, ?, ?, 'Medium', 5, 1, 1)`,
		modelID, modelID, keyID, baseURL)
	require.NoError(t, err)
	modelDBID, err := res.LastInsertId()
	require.NoError(t, err)

	_, err = s.engine.DB().ExecContext(ctx,
		`INSERT INTO fallback_config (model_db_id, position, enabled) VALUES (?, ?, 1)`,
		modelDBID, position)
	require.NoError(t, err)
	return modelDBID
}

// usePriorityOrder pins candidate order to the configured positions.
func usePriorityOrder(t *testing.T, s *Server) {
	t.Helper()
	_, err := s.engine.DB().Exec(
		`INSERT INTO settings (key, value, updated_at) VALUES ('routing_strategy', 'priority', 0)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`)
	require.NoError(t, err)
}

func postChat(t *testing.T, s *Server, key, body string) (*http.Response, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:50000"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec.Result(), rec.Body.String()
}

const chatBody = `{"model":"auto","messages":[{"role":"user","content":"hello"}]}`

// TestRoutesThroughTheEngineToAProvider is the end-to-end proof that the
// ported engine is actually wired: one request, resolved through the chain,
// admitted by the ledger, dispatched to a provider, relayed back.
func TestRoutesThroughTheEngineToAProvider(t *testing.T) {
	t.Parallel()

	const machineKey = "prowl-test-machine-key"
	s := testServer(t, Options{MachineKey: machineKey})
	upstream := newFakeUpstream(t, http.StatusOK, "hi there")
	seedRoute(t, s, "only", upstream.URL, "test-model", 1)

	resp, body := postChat(t, s, machineKey, chatBody)

	require.Equal(t, http.StatusOK, resp.StatusCode, "body was %s", body)
	require.Equal(t, int64(1), upstream.calls.Load(), "the provider must have been called exactly once")
	require.Contains(t, body, "hi there")
	require.NotEmpty(t, resp.Header.Get("X-Request-ID"))
	require.Contains(t, resp.Header.Get("X-Routed-Via"), "custom",
		"the caller must be told which provider served them")
}

// TestFailoverMovesToTheNextProvider proves the loop is driving real routes:
// the first upstream rejects, the second answers, and the caller never sees
// the failure.
func TestFailoverMovesToTheNextProvider(t *testing.T) {
	t.Parallel()

	const machineKey = "prowl-test-machine-key"
	s := testServer(t, Options{MachineKey: machineKey})

	// A rate limit is scoped to the (model, key) that hit it, so the sibling
	// candidate stays eligible. A 5xx would instead skip the whole platform,
	// which TestProviderLevelFailureSkipsThePlatform covers.
	broken := newFakeUpstream(t, http.StatusTooManyRequests, "")
	working := newFakeUpstream(t, http.StatusOK, "second provider answered")
	seedRoute(t, s, "broken", broken.URL, "broken-model", 1)
	seedRoute(t, s, "working", working.URL, "working-model", 2)

	// Both candidates are unmeasured, so a score-based strategy would order
	// them arbitrarily and the test would assert an order it never
	// established. Priority makes position decide, which is what this test is
	// actually about: what happens AFTER the first choice fails.
	usePriorityOrder(t, s)

	resp, body := postChat(t, s, machineKey, chatBody)

	require.Equal(t, http.StatusOK, resp.StatusCode, "a failing provider must not reach the caller: %s", body)
	require.Contains(t, body, "second provider answered")
	require.GreaterOrEqual(t, broken.calls.Load(), int64(1), "the first provider must have been tried")
	require.Equal(t, int64(1), working.calls.Load(), "the second must have served it")
	require.Equal(t, "1", resp.Header.Get("X-Fallback-Attempts"),
		"the caller must be told a hop was needed")
	require.NotEmpty(t, resp.Header.Get("X-Fallback-Trail"))
}

// TestProviderLevelFailureSkipsThePlatform documents a deliberate and
// initially surprising behaviour: a 5xx is evidence about the PROVIDER, not
// about one model or key, so the rest of that platform is abandoned for this
// request. Retrying a sibling model on a provider that is down would just
// spend the wall-clock budget discovering the same outage.
func TestCustomEndpointsFailOverIndependently(t *testing.T) {
	t.Parallel()

	const machineKey = "prowl-test-machine-key"
	s := testServer(t, Options{MachineKey: machineKey})

	down := newFakeUpstream(t, http.StatusInternalServerError, "")
	sibling := newFakeUpstream(t, http.StatusOK, "the sibling answered")
	seedRoute(t, s, "down", down.URL, "down-model", 1)
	seedRoute(t, s, "sibling", sibling.URL, "sibling-model", 2)
	usePriorityOrder(t, s)

	resp, body := postChat(t, s, machineKey, chatBody)

	// A 5xx rules out a hosted platform, because every key of one talks to the
	// same endpoint. Custom endpoints are the exception this asserts: each key
	// is a DIFFERENT operator-supplied server, so one being down says nothing
	// about the others, and sweeping them made a self-hosted pool fail over
	// exactly once and then give up.
	//
	// The hosted-platform behaviour is covered where it can be exercised
	// honestly: errclass_test.go pins a 5xx as SkipPlatform, and chain_test.go
	// pins Eligible honouring SkipPlatforms.
	require.Equal(t, http.StatusOK, resp.StatusCode, "body was %s", body)
	require.Equal(t, int64(1), down.calls.Load())
	require.Equal(t, int64(1), sibling.calls.Load(),
		"a second custom endpoint is a different server and must still be tried")
	require.Contains(t, body, "the sibling answered")
}

// TestEveryProviderFailingIsNotA500 pins the exhaustion taxonomy at the edge:
// the gateway must name what went wrong upstream rather than claim its own
// internal error.
func TestEveryProviderFailingIsNotA500(t *testing.T) {
	t.Parallel()

	const machineKey = "prowl-test-machine-key"
	s := testServer(t, Options{MachineKey: machineKey})

	first := newFakeUpstream(t, http.StatusInternalServerError, "")
	second := newFakeUpstream(t, http.StatusBadGateway, "")
	seedRoute(t, s, "first", first.URL, "first-model", 1)
	seedRoute(t, s, "second", second.URL, "second-model", 2)
	usePriorityOrder(t, s)

	resp, body := postChat(t, s, machineKey, chatBody)

	require.NotEqual(t, http.StatusInternalServerError, resp.StatusCode,
		"an upstream failure is never the gateway's own 500: %s", body)
	require.GreaterOrEqual(t, resp.StatusCode, 400)
	require.NotEqual(t, string(TypeAuthentication), errorType(t, body),
		"an upstream failure must not sign the operator out")
}

// TestUnconfiguredGatewayExplainsItself is the first-run experience: with no
// keys, the answer must say what to do rather than fail opaquely.
func TestUnconfiguredGatewayExplainsItself(t *testing.T) {
	t.Parallel()

	const machineKey = "prowl-test-machine-key"
	s := testServer(t, Options{MachineKey: machineKey})

	resp, body := postChat(t, s, machineKey, chatBody)

	require.GreaterOrEqual(t, resp.StatusCode, 400)

	// The contract is actionable guidance, not one particular word: the reply
	// must name the surface that fixes it.
	lower := strings.ToLower(body)
	require.True(t,
		strings.Contains(lower, "models page") || strings.Contains(lower, "keys page"),
		"an unconfigured gateway must point the user at where to fix it: %s", body)
}

// TestRequestTrailRecordsTheEvidence matters because the trail IS the
// reliability and speed evidence the bandit reads back. A served request that
// leaves no row would make the router permanently blind.
func TestRequestTrailRecordsTheEvidence(t *testing.T) {
	t.Parallel()

	const machineKey = "prowl-test-machine-key"
	s := testServer(t, Options{MachineKey: machineKey})
	upstream := newFakeUpstream(t, http.StatusOK, "recorded")
	seedRoute(t, s, "only", upstream.URL, "test-model", 1)

	resp, _ := postChat(t, s, machineKey, chatBody)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var (
		platform, outcome string
		inTok, outTok     int
		latency           int64
	)
	err := s.engine.DB().QueryRow(
		`SELECT platform, outcome, input_tokens, output_tokens, latency_ms FROM requests ORDER BY id DESC LIMIT 1`,
	).Scan(&platform, &outcome, &inTok, &outTok, &latency)
	require.NoError(t, err, "a served request must leave evidence for the router to learn from")
	require.Equal(t, "custom", platform)
	require.Equal(t, "success", outcome)
	require.Equal(t, 11, inTok, "the reported prompt tokens are recorded as input")
	require.Equal(t, 7, outTok, "the reported completion tokens are recorded as output, not the summed total")
	require.GreaterOrEqual(t, latency, int64(0))
}

// TestModelsListsRoutingAliasesFirst keeps the aliases discoverable: they are
// the ids a client should normally use, because they are what lets the gateway
// choose at all.
func TestModelsListsRoutingAliasesFirst(t *testing.T) {
	t.Parallel()

	const machineKey = "prowl-test-machine-key"
	s := testServer(t, Options{MachineKey: machineKey})
	upstream := newFakeUpstream(t, http.StatusOK, "x")
	seedRoute(t, s, "only", upstream.URL, "test-model", 1)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.RemoteAddr = "127.0.0.1:50000"
	req.Header.Set("Authorization", "Bearer "+machineKey)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var parsed struct {
		Object string `json:"object"`
		Data   []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &parsed))
	require.Equal(t, "list", parsed.Object)
	require.NotEmpty(t, parsed.Data)
	require.Equal(t, "auto", parsed.Data[0].ID, "the routing alias must lead the list")
}

// TestServedRequestFeedsTheRollupAndLifetimeCounters is why the relay writes
// through RecordRequest rather than a plain INSERT. Analytics reads the hourly
// rollup and the lifetime counters precisely so its charts survive the raw
// trail being pruned; a writer that only inserted the trail row would leave
// the dashboard empty after the first retention pass.
func TestServedRequestFeedsTheRollupAndLifetimeCounters(t *testing.T) {
	t.Parallel()

	const machineKey = "prowl-test-machine-key"
	s := testServer(t, Options{MachineKey: machineKey})
	upstream := newFakeUpstream(t, http.StatusOK, "counted")
	seedRoute(t, s, "only", upstream.URL, "test-model", 1)

	resp, _ := postChat(t, s, machineKey, chatBody)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var rollupRequests, rollupIn, rollupOut int64
	require.NoError(t, s.engine.DB().QueryRow(
		`SELECT SUM(requests), SUM(input_tokens), SUM(output_tokens) FROM request_hourly`).Scan(&rollupRequests, &rollupIn, &rollupOut))
	require.Equal(t, int64(1), rollupRequests, "the hourly rollup must see the request")
	require.Equal(t, int64(11), rollupIn, "the rollup carries the prompt tokens as input")
	require.Equal(t, int64(7), rollupOut, "the rollup carries the completion tokens as output")

	var lifetime string
	require.NoError(t, s.engine.DB().QueryRow(
		`SELECT value FROM settings WHERE key = 'total_requests'`).Scan(&lifetime))
	require.Equal(t, "1", lifetime,
		"the lifetime counter must advance, since it is what survives pruning")
}

// TestFailedHopsArePersistedWithoutLeakingCredentials covers the attempt
// trail. A provider's rejection commonly echoes the key back, and that
// message lands in a stored row the dashboard renders.
func TestFailedHopsArePersistedWithoutLeakingCredentials(t *testing.T) {
	t.Parallel()

	const machineKey = "prowl-test-machine-key"
	s := testServer(t, Options{MachineKey: machineKey})

	rejecting := newFakeUpstream(t, http.StatusTooManyRequests, "")
	working := newFakeUpstream(t, http.StatusOK, "served")
	seedRoute(t, s, "rejecting", rejecting.URL, "rejecting-model", 1)
	seedRoute(t, s, "working", working.URL, "working-model", 2)
	usePriorityOrder(t, s)

	resp, _ := postChat(t, s, machineKey, chatBody)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	rows, err := s.engine.DB().Query(
		`SELECT attempt, platform, model_id, error_kind, COALESCE(error_message,'')
		   FROM request_attempts ORDER BY attempt`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()

	seen := 0
	for rows.Next() {
		var (
			attempt           int
			platform, modelID string
			kind, message     string
		)
		require.NoError(t, rows.Scan(&attempt, &platform, &modelID, &kind, &message))
		seen++
		require.NotEmpty(t, kind, "a persisted hop must say how it failed")
		require.NotContains(t, message, "test-key-",
			"a stored hop must never carry the credential the provider echoed back")
	}
	require.Equal(t, 1, seen, "the failed hop must be persisted for the trail view")
}

// lastRequestTokens reads the input/output token columns of the most recent
// requests row, which is what analytics, cost and budget maths all read.
func lastRequestTokens(t *testing.T, s *Server) (int, int) {
	t.Helper()
	var in, out int
	require.NoError(t, s.engine.DB().QueryRow(
		`SELECT input_tokens, output_tokens FROM requests ORDER BY id DESC LIMIT 1`).Scan(&in, &out))
	return in, out
}

// TestChatRecordsPromptAndCompletionSeparately proves the relay no longer
// collapses usage into the output column: a 90-prompt / 16-completion answer
// must record input 90 and output 16, not 0 and the 106 sum.
func TestChatRecordsPromptAndCompletionSeparately(t *testing.T) {
	t.Parallel()

	const machineKey = "prowl-test-machine-key"
	s := testServer(t, Options{MachineKey: machineKey})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c","object":"chat.completion","model":"m",
			"choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":90,"completion_tokens":16,"total_tokens":106}}`))
	}))
	t.Cleanup(upstream.Close)
	seedRoute(t, s, "only", upstream.URL, "test-model", 1)

	resp, body := postChat(t, s, machineKey, chatBody)
	require.Equal(t, http.StatusOK, resp.StatusCode, "body was %s", body)

	in, out := lastRequestTokens(t, s)
	require.Equal(t, 90, in)
	require.Equal(t, 16, out)
}

// TestStreamedChatRecordsUsageSplit proves the streamed path reads the split
// from the late usage frame, not a sum. The streaming upstream's final frame
// reports prompt 5 / completion 3.
func TestStreamedChatRecordsUsageSplit(t *testing.T) {
	t.Parallel()

	const machineKey = "prowl-test-machine-key"
	s := testServer(t, Options{MachineKey: machineKey})
	upstream := newStreamingUpstream(t, "hel", "lo")
	seedRoute(t, s, "only", upstream.URL, "test-model", 1)

	resp, body := postChat(t, s, machineKey,
		`{"model":"auto","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	require.Equal(t, http.StatusOK, resp.StatusCode, "body was %s", body)

	in, out := lastRequestTokens(t, s)
	require.Equal(t, 5, in)
	require.Equal(t, 3, out)
}

// TestChatWithoutUsageRecordsZeros proves a provider that omits usage records
// honest zeros rather than an invented total.
func TestChatWithoutUsageRecordsZeros(t *testing.T) {
	t.Parallel()

	const machineKey = "prowl-test-machine-key"
	s := testServer(t, Options{MachineKey: machineKey})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c","object":"chat.completion","model":"m",
			"choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(upstream.Close)
	seedRoute(t, s, "only", upstream.URL, "test-model", 1)

	resp, body := postChat(t, s, machineKey, chatBody)
	require.Equal(t, http.StatusOK, resp.StatusCode, "body was %s", body)

	in, out := lastRequestTokens(t, s)
	require.Zero(t, in)
	require.Zero(t, out)
}

// TestFailedAttemptLeavesAWarnLogWithoutSecrets proves a failed hop is no
// longer silent: it lands one warn line in the Logs store naming the provider,
// model and classified event, with the credential the provider echoed scrubbed.
func TestFailedAttemptLeavesAWarnLogWithoutSecrets(t *testing.T) {
	t.Parallel()

	const machineKey = "prowl-test-machine-key"
	s := testServer(t, Options{MachineKey: machineKey})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		// A careless provider echoes the credential back; the stored line must not.
		_, _ = fmt.Fprintf(w, `{"error":{"message":"rate limit for %s"}}`, r.Header.Get("Authorization"))
	}))
	t.Cleanup(upstream.Close)
	seedRoute(t, s, "only", upstream.URL, "test-model", 1)

	resp, _ := postChat(t, s, machineKey, chatBody)
	require.NotEqual(t, http.StatusOK, resp.StatusCode)

	var count int
	require.NoError(t, s.engine.DB().QueryRow(
		`SELECT COUNT(*) FROM server_logs WHERE level = 'warn'`).Scan(&count))
	require.Equal(t, 1, count, "one failed hop leaves exactly one warn line")

	var provider, model, event, message string
	require.NoError(t, s.engine.DB().QueryRow(
		`SELECT COALESCE(provider,''), COALESCE(model,''), COALESCE(event,''), message
		   FROM server_logs WHERE level = 'warn' ORDER BY id DESC LIMIT 1`).
		Scan(&provider, &model, &event, &message))
	require.Equal(t, "custom", provider)
	require.Equal(t, "test-model", model)
	require.Equal(t, "rate_limited", event)
	require.NotContains(t, message, "test-key-only", "the credential must be redacted from the log line")
}

// TestExhaustedRunLeavesAnErrorLog proves a run that fails every candidate
// leaves an error line an operator can find, not just an empty Logs page.
func TestExhaustedRunLeavesAnErrorLog(t *testing.T) {
	t.Parallel()

	const machineKey = "prowl-test-machine-key"
	s := testServer(t, Options{MachineKey: machineKey})
	upstream := newFakeUpstream(t, http.StatusInternalServerError, "")
	seedRoute(t, s, "only", upstream.URL, "test-model", 1)

	resp, _ := postChat(t, s, machineKey, chatBody)
	require.NotEqual(t, http.StatusOK, resp.StatusCode)

	var count int
	require.NoError(t, s.engine.DB().QueryRow(
		`SELECT COUNT(*) FROM server_logs WHERE level = 'error' AND event = 'exhausted'`).Scan(&count))
	require.Equal(t, 1, count, "an exhausted run must be recorded")
}

// TestSuccessfulRequestLeavesNoLogs keeps the buffer signal, not traffic: a
// served request writes nothing, so warn/error rows always mean real trouble.
func TestSuccessfulRequestLeavesNoLogs(t *testing.T) {
	t.Parallel()

	const machineKey = "prowl-test-machine-key"
	s := testServer(t, Options{MachineKey: machineKey})
	upstream := newFakeUpstream(t, http.StatusOK, "ok")
	seedRoute(t, s, "only", upstream.URL, "test-model", 1)

	resp, body := postChat(t, s, machineKey, chatBody)
	require.Equal(t, http.StatusOK, resp.StatusCode, "body was %s", body)

	var count int
	require.NoError(t, s.engine.DB().QueryRow(`SELECT COUNT(*) FROM server_logs`).Scan(&count))
	require.Zero(t, count, "a successful request must leave the log buffer empty")
}
