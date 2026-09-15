package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// dashSession claims the first account and returns session auth headers plus a
// JSON content type, so the session-gated routes under test are reachable.
func dashSession(t *testing.T, s *Server) map[string]string {
	t.Helper()
	return authed(claimDashboard(t, s, "op@example.com", "correct horse battery"))
}

type convResponse struct {
	ID           int64           `json:"id"`
	Title        string          `json:"title"`
	Messages     json.RawMessage `json:"messages"`
	Model        *string         `json:"model"`
	SystemPrompt *string         `json:"systemPrompt"`
	CreatedAt    int64           `json:"createdAt"`
	UpdatedAt    int64           `json:"updatedAt"`
}

type convSummary struct {
	ID           int64   `json:"id"`
	Title        string  `json:"title"`
	Model        *string `json:"model"`
	MessageCount int64   `json:"messageCount"`
	CreatedAt    int64   `json:"createdAt"`
	UpdatedAt    int64   `json:"updatedAt"`
}

// TestConversationRoundTrip is the Playground's whole storage lifecycle:
// create, see it in the sidebar, load its transcript, delete it. The transcript
// is stored verbatim, so the message fields the page draws — reasoning, meta,
// image thumbnails — must survive the round trip unchanged.
func TestConversationRoundTrip(t *testing.T) {
	s := testServer(t, Options{})
	auth := dashSession(t, s)

	messages := `[
		{"role":"user","content":"hello","images":["data:image/png;base64,AAAA"]},
		{"role":"assistant","content":"hi there","reasoning":"thinking…",
		 "meta":{"platform":"openrouter","model":"gpt-x","latency":42}}
	]`
	create := fmt.Sprintf(`{"title":"Greeting","messages":%s,"model":"auto","systemPrompt":"be nice"}`, messages)

	resp, body := do(t, s, http.MethodPost, "/api/conversations", create, auth)
	require.Equal(t, http.StatusCreated, resp.StatusCode, "body was %q", body)
	var created convResponse
	require.NoError(t, json.Unmarshal([]byte(body), &created))
	require.NotZero(t, created.ID)
	require.Equal(t, "Greeting", created.Title)
	require.NotNil(t, created.Model)
	require.Equal(t, "auto", *created.Model)
	require.NotNil(t, created.SystemPrompt)
	require.Equal(t, "be nice", *created.SystemPrompt)
	require.NotZero(t, created.CreatedAt)
	// Every drawn field survives: compare the stored transcript semantically to
	// what was sent.
	require.JSONEq(t, messages, string(created.Messages))

	// Sidebar: the summary carries the count read from the blob, never the
	// bodies.
	_, listBody := do(t, s, http.MethodGet, "/api/conversations", "", auth)
	var list []convSummary
	require.NoError(t, json.Unmarshal([]byte(listBody), &list))
	require.Len(t, list, 1)
	require.Equal(t, created.ID, list[0].ID)
	require.Equal(t, int64(2), list[0].MessageCount)
	require.NotContains(t, listBody, "hi there", "the sidebar must not carry message bodies")

	// Load the full transcript back.
	getResp, getBody := do(t, s, http.MethodGet, fmt.Sprintf("/api/conversations/%d", created.ID), "", auth)
	require.Equal(t, http.StatusOK, getResp.StatusCode)
	var loaded convResponse
	require.NoError(t, json.Unmarshal([]byte(getBody), &loaded))
	require.JSONEq(t, messages, string(loaded.Messages))

	// Delete, and it is gone from both the list and by id.
	delResp, delBody := do(t, s, http.MethodDelete, fmt.Sprintf("/api/conversations/%d", created.ID), "", auth)
	require.Equal(t, http.StatusOK, delResp.StatusCode)
	require.JSONEq(t, `{"success":true}`, delBody)

	_, emptyBody := do(t, s, http.MethodGet, "/api/conversations", "", auth)
	require.JSONEq(t, `[]`, emptyBody)

	gone, _ := do(t, s, http.MethodGet, fmt.Sprintf("/api/conversations/%d", created.ID), "", auth)
	require.Equal(t, http.StatusNotFound, gone.StatusCode)

	delAgain, _ := do(t, s, http.MethodDelete, fmt.Sprintf("/api/conversations/%d", created.ID), "", auth)
	require.Equal(t, http.StatusNotFound, delAgain.StatusCode)
}

// TestOversizedConversationIsRefused reproduces the reference's 2 MB cap: a
// transcript past it is refused with 413 and the client-recognised
// conversation_too_large type, and nothing is stored — the operator's existing
// history is not clobbered by a write that could not be saved.
func TestOversizedConversationIsRefused(t *testing.T) {
	s := testServer(t, Options{})
	auth := dashSession(t, s)

	huge := strings.Repeat("a", 2*1024*1024+1024)
	body := fmt.Sprintf(`{"messages":[{"role":"user","content":%q}]}`, huge)

	resp, respBody := do(t, s, http.MethodPost, "/api/conversations", body, auth)
	require.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode)
	require.Equal(t, "conversation_too_large", errorType(t, respBody))
	require.Contains(t, respBody, "too large")

	// The refused write left no row behind.
	_, listBody := do(t, s, http.MethodGet, "/api/conversations", "", auth)
	require.JSONEq(t, `[]`, listBody)
}

// TestConversationUpdateKeepsOmittedFields defends the PUT contract: a field
// left out keeps its stored value (a rename cannot race the transcript away),
// while an explicit null clears a nullable field.
func TestConversationUpdateKeepsOmittedFields(t *testing.T) {
	s := testServer(t, Options{})
	auth := dashSession(t, s)

	resp, body := do(t, s, http.MethodPost, "/api/conversations",
		`{"title":"First","messages":[{"role":"user","content":"hi"}],"model":"gpt-x","systemPrompt":"keep"}`, auth)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var created convResponse
	require.NoError(t, json.Unmarshal([]byte(body), &created))

	// Rename only: transcript, model and prompt are untouched.
	putResp, putBody := do(t, s, http.MethodPut,
		fmt.Sprintf("/api/conversations/%d", created.ID), `{"title":"Renamed"}`, auth)
	require.Equal(t, http.StatusOK, putResp.StatusCode)
	var renamed convResponse
	require.NoError(t, json.Unmarshal([]byte(putBody), &renamed))
	require.Equal(t, "Renamed", renamed.Title)
	require.JSONEq(t, `[{"role":"user","content":"hi"}]`, string(renamed.Messages))
	require.NotNil(t, renamed.Model)
	require.Equal(t, "gpt-x", *renamed.Model)
	require.NotNil(t, renamed.SystemPrompt)
	require.Equal(t, "keep", *renamed.SystemPrompt)

	// Explicit null clears the nullable model; the prompt, omitted, stays.
	clrResp, clrBody := do(t, s, http.MethodPut,
		fmt.Sprintf("/api/conversations/%d", created.ID), `{"model":null}`, auth)
	require.Equal(t, http.StatusOK, clrResp.StatusCode)
	var cleared convResponse
	require.NoError(t, json.Unmarshal([]byte(clrBody), &cleared))
	require.Nil(t, cleared.Model, "explicit null clears the model")
	require.NotNil(t, cleared.SystemPrompt)
	require.Equal(t, "keep", *cleared.SystemPrompt)
}

// TestConversationsRequireASession guards the wiring: without a session the
// route answers 401 with authentication_error — the one place the client is
// allowed to end its session — rather than serving the operator's transcripts
// to an unauthenticated caller.
func TestConversationsRequireASession(t *testing.T) {
	s := testServer(t, Options{})
	resp, body := do(t, s, http.MethodGet, "/api/conversations", "", nil)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	require.Equal(t, "authentication_error", errorType(t, body))
}
