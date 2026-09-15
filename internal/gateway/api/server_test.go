package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/neur0map/prowl/internal/gateway"
)

// testServer builds the HTTP surface over an engine with an EMPTY catalog.
//
// Production seeds 400 shipped models on open, but a route test almost always
// wants to assert on rows it inserted itself: a seeded catalog makes list
// shapes, counts and generated ids depend on what the catalog happens to ship,
// so a test would fail when the catalog grows rather than when its subject
// breaks. Tests that genuinely need the shipped catalog use testSeededServer.
func testServer(t *testing.T, opts Options) *Server {
	t.Helper()
	return newTestServer(t, opts, gateway.EngineOptions{SkipCatalogSeed: true})
}

// testSeededServer builds the surface over an engine in its PRODUCTION state,
// catalog and all, for the routes whose whole job is to serve it.
func testSeededServer(t *testing.T, opts Options) *Server {
	t.Helper()
	return newTestServer(t, opts, gateway.EngineOptions{})
}

func newTestServer(t *testing.T, opts Options, engineOpts gateway.EngineOptions) *Server {
	t.Helper()
	engine, err := gateway.OpenEngine(context.Background(), t.TempDir(), engineOpts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = engine.Close() })

	s, err := NewServer(engine, opts)
	require.NoError(t, err)
	return s
}

func do(t *testing.T, s *Server, method, path, body string, headers map[string]string) (*http.Response, string) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.RemoteAddr = "127.0.0.1:50000"
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec.Result(), rec.Body.String()
}

func errorType(t *testing.T, body string) string {
	t.Helper()
	var parsed struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &parsed), "body was %q", body)
	return parsed.Error.Type
}

// claimDashboard creates the first account and returns its session token.
//
// Setup requires the first-run code from every caller, including one on
// loopback, so the code is minted and presented here. This is the one place
// tests bootstrap a session: the family helpers below wrap it rather than
// repeating the exchange, so a change to the setup contract lands once.
func claimDashboard(t *testing.T, s *Server, email, password string) string {
	t.Helper()
	code := s.engine.Auth().MintSetupCode(context.Background())
	_, body := do(t, s, http.MethodPost, "/api/auth/setup",
		`{"email":"`+email+`","password":"`+password+`","setupCode":"`+code+`"}`,
		map[string]string{"Content-Type": "application/json"})
	var sess sessionResponse
	require.NoError(t, json.Unmarshal([]byte(body), &sess), "setup body was %q", body)
	require.NotEmpty(t, sess.Token)
	return sess.Token
}

// TestOnlyASessionFailureEndsTheSession is the subtlest constraint in the
// whole surface. The dashboard signs the operator out on a 401 carrying
// authentication_error and on nothing else, so any OTHER 401 in the system
// must use a different type. Otherwise an application calling /v1 with a
// stale api key would log the human out of a browser tab they are not even
// looking at.
func TestOnlyASessionFailureEndsTheSession(t *testing.T) {
	t.Parallel()
	s := testServer(t, Options{MachineKey: "prowl-machine-key"})

	// A missing dashboard session: this one SHOULD end the session.
	resp, body := do(t, s, http.MethodGet, "/api/auth/me", "", nil)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	require.Equal(t, string(TypeAuthentication), errorType(t, body),
		"a real session failure must be the one thing that signs the operator out")

	// An inference-plane key failure is a 401 too, but for an application,
	// not for the operator's browser. Asserted at the gate rather than
	// through a mounted route, so the property holds regardless of which
	// /v1 handlers exist yet.
	gated := s.RequireMachineKey(func(w http.ResponseWriter, _ *http.Request) {
		WriteJSON(w, http.StatusOK, map[string]string{"served": "yes"})
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	req.RemoteAddr = "127.0.0.1:50000"
	req.Header.Set("Authorization", "Bearer wrong-key")
	rec := httptest.NewRecorder()
	gated(rec, req)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.NotEqual(t, string(TypeAuthentication), errorType(t, rec.Body.String()),
		"an application's bad api key must NOT sign the operator out")
}

// TestLockoutIsARateLimitNotACredentialVerdict keeps a throttled login from
// ending the session: "wait a while" is not "your credentials are wrong".
func TestLockoutIsARateLimitNotACredentialVerdict(t *testing.T) {
	t.Parallel()
	s := testServer(t, Options{})
	ctx := context.Background()

	_, _, err := s.engine.Auth().Setup(ctx, "op@example.com", "correct horse battery", s.engine.Auth().MintSetupCode(ctx))
	require.NoError(t, err)

	const wrong = `{"email":"op@example.com","password":"nope"}`
	var last string
	var lastResp *http.Response
	for range 6 {
		lastResp, last = do(t, s, http.MethodPost, "/api/auth/login", wrong,
			map[string]string{"Content-Type": "application/json"})
	}

	require.Equal(t, http.StatusTooManyRequests, lastResp.StatusCode)
	require.Equal(t, string(TypeRateLimit), errorType(t, last),
		"a lockout must read as a rate limit, not as an authentication failure")
}

// TestSetupStatusGuidesTheFirstScreen: the client decides between the
// create-account and sign-in forms from this response alone.
func TestSetupStatusGuidesTheFirstScreen(t *testing.T) {
	t.Parallel()
	s := testServer(t, Options{})

	// Serving mints the code; this harness builds the surface without it.
	require.NotEmpty(t, s.engine.Auth().MintSetupCode(context.Background()))

	resp, body := do(t, s, http.MethodGet, "/api/auth/status", "", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var status authStatusResponse
	require.NoError(t, json.Unmarshal([]byte(body), &status))
	require.True(t, status.NeedsSetup)
	require.False(t, status.Authenticated)
	require.True(t, status.SetupCodeRequired,
		"every caller needs the code, so the first screen must ask for it")

	// Claim it, then the same endpoint must steer to sign-in.
	require.NotEmpty(t, claimDashboard(t, s, "op@example.com", "correct horse battery"))

	_, body = do(t, s, http.MethodGet, "/api/auth/status", "", nil)
	require.NoError(t, json.Unmarshal([]byte(body), &status))
	require.False(t, status.NeedsSetup, "a claimed dashboard must not offer setup again")
}

// TestSetupCannotBeClaimedTwice is the same property at the route level, with
// the status code the client branches on. The second attempt presents a fresh
// code, so the conflict is decided by the dashboard already being claimed
// rather than by a missing code.
func TestSetupCannotBeClaimedTwice(t *testing.T) {
	t.Parallel()
	s := testServer(t, Options{})

	require.NotEmpty(t, claimDashboard(t, s, "op@example.com", "correct horse battery"))

	code := s.engine.Auth().MintSetupCode(context.Background())
	resp, body := do(t, s, http.MethodPost, "/api/auth/setup",
		`{"email":"attacker@example.com","password":"another password",`+
			`"setupCode":"`+code+`"}`,
		map[string]string{"Content-Type": "application/json"})
	require.Equal(t, http.StatusConflict, resp.StatusCode)
	require.Equal(t, string(TypeSetupComplete), errorType(t, body))
}

// TestSessionTokenIsAcceptedBothWays covers the two headers the client uses:
// its own and a plain bearer.
func TestSessionTokenIsAcceptedBothWays(t *testing.T) {
	t.Parallel()
	s := testServer(t, Options{})

	token := claimDashboard(t, s, "op@example.com", "correct horse battery")

	for name, headers := range map[string]map[string]string{
		"dashboard header": {sessionHeader: token},
		"bearer":           {"Authorization": "Bearer " + token},
		"lowercase bearer": {"Authorization": "bearer " + token},
	} {
		resp, _ := do(t, s, http.MethodGet, "/api/auth/me", "", headers)
		require.Equal(t, http.StatusOK, resp.StatusCode, "%s must authenticate", name)
	}
}

// TestUnmatchedAPIPathAnswersInJSON stops the SPA fallback from swallowing a
// mistyped endpoint. Returning the app shell with a 200 makes the client
// report "the API isn't reachable at this origin", which sends whoever is
// debugging it after the wrong problem entirely.
func TestUnmatchedAPIPathAnswersInJSON(t *testing.T) {
	t.Parallel()
	s := testServer(t, Options{})

	for _, path := range []string{"/api/nope", "/api/keys/typo", "/v1/nope", "/v1beta/models"} {
		resp, body := do(t, s, http.MethodGet, path, "", nil)
		require.Equal(t, http.StatusNotFound, resp.StatusCode, "%s must 404", path)
		require.Contains(t, resp.Header.Get("Content-Type"), "application/json", "%s must answer in JSON", path)
		require.NotContains(t, body, "<div id=\"root\">", "%s must not return the app shell", path)
	}
}

// TestPingNeedsNoCredential keeps the health check usable: one that requires
// a token is one nobody wires up.
func TestPingNeedsNoCredential(t *testing.T) {
	t.Parallel()
	s := testServer(t, Options{})

	resp, body := do(t, s, http.MethodGet, "/api/ping", "", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, body, `"status":"ok"`)
}

// TestUnconfiguredInferencePlaneRefusesRatherThanServingOpen is the safe
// default: with no credential configured, /v1 is closed.
//
// It answers 401 rather than "not configured", deliberately. A 503 would tell
// an unauthenticated caller whether this gateway has been set up yet, and
// "no credential matches" is the honest answer either way.
func TestUnconfiguredInferencePlaneRefusesRatherThanServingOpen(t *testing.T) {
	t.Parallel()
	s := testServer(t, Options{})

	handler := s.RequireMachineKey(func(w http.ResponseWriter, _ *http.Request) {
		WriteJSON(w, http.StatusOK, map[string]string{"served": "yes"})
	})

	for name, headers := range map[string]map[string]string{
		"no credential":   {},
		"invented bearer": {"Authorization": "Bearer made-up"},
		"empty bearer":    {"Authorization": "Bearer "},
	} {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
		req.RemoteAddr = "127.0.0.1:50000"
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		handler(rec, req)

		require.Equal(t, http.StatusUnauthorized, rec.Code, "%s must be refused", name)
		require.NotEqual(t, string(TypeAuthentication), errorType(t, rec.Body.String()),
			"%s must not sign the operator out", name)
		require.NotContains(t, rec.Body.String(), "served")
	}
}

// TestSPAServesClientRoutes proves the dashboard is reachable through the same
// origin as the API, which is what the vendored client assumes.
func TestSPAServesClientRoutes(t *testing.T) {
	t.Parallel()
	s := testServer(t, Options{})

	resp, body := do(t, s, http.MethodGet, "/models/chat", "", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, body, `<div id="root">`)
	require.Contains(t, body, "<title>Prowl")
}

// TestRateLimiterBoundsItsMemory stops a spray of forged source addresses
// from growing the limiter without limit.
func TestRateLimiterBoundsItsMemory(t *testing.T) {
	t.Parallel()

	l := newRateLimiter()
	for i := range maxTrackedIPs + 100 {
		l.allow(adminBucket, "10.0.0."+strings.Repeat("x", i%3)+string(rune('a'+i%26))+string(rune('0'+i%10)))
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	require.LessOrEqual(t, len(l.windows[adminBucket]), maxTrackedIPs+1,
		"the limiter must not grow without bound under spoofed sources")
}

// TestBucketsAreIndependent keeps busy inference traffic from throttling the
// operator out of their own settings page.
func TestBucketsAreIndependent(t *testing.T) {
	t.Parallel()

	l := newRateLimiter()
	const ip = "127.0.0.1"
	for range bucketLimits[proxyBucket] {
		require.True(t, l.allow(proxyBucket, ip))
	}
	require.False(t, l.allow(proxyBucket, ip), "the proxy budget must be spent")
	require.True(t, l.allow(adminBucket, ip), "the dashboard budget must be untouched")
}
