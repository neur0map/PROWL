package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// insertTestProfile plants a client profile directly, so the test exercises
// the inference plane rather than the dashboard's create route.
func insertTestProfile(t *testing.T, s *Server, name, prompt string, enabled int) profileKey {
	t.Helper()
	key, err := mintProfileKey()
	require.NoError(t, err)
	_, err = s.engine.DB().Exec(`
		INSERT INTO client_profiles
			(name, token_hash, masked_key, system_prompt, enabled,
			 created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 0, 0)`,
		name, key.Hash(), key.Masked(), prompt, enabled)
	require.NoError(t, err)
	return key
}

// TestAProfileTokenAuthenticatesTheInferencePlane proves the second inference
// credential works at all. Until the gate consulted client_profiles, a profile
// key was indistinguishable from a bad key and the whole family was unusable.
func TestAProfileTokenAuthenticatesTheInferencePlane(t *testing.T) {
	t.Parallel()
	s := testServer(t, Options{MachineKey: "prowl-machine-key"})
	key := insertTestProfile(t, s, "agent", "Answer only in Latin.", 1)

	// A profile token must get past authentication. It fails later for want
	// of a routable chain, which is what proves it authenticated: an
	// unauthenticated request never reaches routing.
	res, body := do(t, s, "POST", "/v1/chat/completions",
		`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`,
		map[string]string{"Authorization": "Bearer " + string(key)})
	require.NotEqual(t, http.StatusUnauthorized, res.StatusCode,
		"a valid profile token must authenticate the inference plane, body was %q", body)
}

// TestADisabledProfileCannotAuthenticate pins the revocation path. A disabled
// profile must be rejected exactly like an unknown one, so a revoked client
// cannot distinguish "disabled" from "deleted".
func TestADisabledProfileCannotAuthenticate(t *testing.T) {
	t.Parallel()
	s := testServer(t, Options{MachineKey: "prowl-machine-key"})
	disabled := insertTestProfile(t, s, "revoked", "", 0)

	res, body := do(t, s, "POST", "/v1/chat/completions",
		`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`,
		map[string]string{"Authorization": "Bearer " + string(disabled)})
	require.Equal(t, http.StatusUnauthorized, res.StatusCode)

	unknown, err := mintProfileKey()
	require.NoError(t, err)
	res2, body2 := do(t, s, "POST", "/v1/chat/completions",
		`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`,
		map[string]string{"Authorization": "Bearer " + string(unknown)})
	require.Equal(t, http.StatusUnauthorized, res2.StatusCode)
	require.Equal(t, body2, body,
		"a disabled profile must be indistinguishable from an unknown one")

	// And neither may sign the operator out of the dashboard.
	require.NotEqual(t, string(TypeAuthentication), errorType(t, body))
}

// TestTheEnforcedPromptReachesTheProvider is the point of the whole family.
// The profile's prompt must arrive ahead of the caller's messages, and the
// caller's own system message must survive rather than be replaced.
func TestTheEnforcedPromptReachesTheProvider(t *testing.T) {
	t.Parallel()

	req, err := parseChatBody([]byte(
		`{"model":"gpt-4o","messages":[` +
			`{"role":"system","content":"caller rules"},` +
			`{"role":"user","content":"hi"}]}`))
	require.NoError(t, err)

	req.prependSystem("profile rules")

	require.Len(t, req.Messages, 3, "the caller's messages must all survive")
	require.Equal(t, "system", req.Messages[0]["role"])
	require.Equal(t, "profile rules", req.Messages[0]["content"],
		"the enforced prompt must lead")
	require.Equal(t, "caller rules", req.Messages[1]["content"],
		"the caller's own system message must not be discarded")
	require.Equal(t, "hi", req.Messages[2]["content"])
}

// TestAnEmptyEnforcedPromptChangesNothing keeps a profile with no prompt from
// injecting a blank system message, which some providers reject outright.
func TestAnEmptyEnforcedPromptChangesNothing(t *testing.T) {
	t.Parallel()

	req, err := parseChatBody([]byte(
		`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	require.NoError(t, err)

	req.prependSystem("")
	require.Len(t, req.Messages, 1)
	require.Equal(t, "user", req.Messages[0]["role"])
}

// recordingUpstream captures the body a provider actually received, so a test
// can assert on the wire rather than on the struct that produced it.
type recordingUpstream struct {
	*httptest.Server
	mu   sync.Mutex
	body string
}

func newRecordingUpstream(t *testing.T) *recordingUpstream {
	t.Helper()
	u := &recordingUpstream{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.body = string(raw)
		u.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"cmpl-1","object":"chat.completion","model":"m",
			"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},
			"finish_reason":"stop"}],
			"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	t.Cleanup(u.Close)
	return u
}

func (u *recordingUpstream) received() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.body
}

// TestTheEnforcedPromptArrivesAtTheUpstream is the end-to-end half: a request
// authenticated with a profile token must reach the provider carrying the
// profile's prompt. Asserting on the parsed struct alone would still pass if
// the dispatcher rebuilt the payload from the untouched raw JSON.
func TestTheEnforcedPromptArrivesAtTheUpstream(t *testing.T) {
	t.Parallel()

	s := testServer(t, Options{MachineKey: "prowl-test-machine-key"})
	upstream := newRecordingUpstream(t)
	seedRoute(t, s, "only", upstream.URL, "test-model", 1)
	key := insertTestProfile(t, s, "agent", "Answer only in Latin.", 1)

	resp, body := postChat(t, s, string(key), chatBody)
	require.Equal(t, http.StatusOK, resp.StatusCode, "body was %s", body)

	sent := upstream.received()
	require.Contains(t, sent, "Answer only in Latin.",
		"the profile's enforced prompt must reach the provider")
	require.Contains(t, sent, "hello", "the caller's own message must survive")
	require.Less(t, strings.Index(sent, "Answer only in Latin."),
		strings.Index(sent, "hello"),
		"the enforced prompt must precede the caller's messages")
}
