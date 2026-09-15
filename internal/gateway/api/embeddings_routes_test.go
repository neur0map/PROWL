package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type embListResp struct {
	DefaultFamily string `json:"defaultFamily"`
	Families      []struct {
		Family         string `json:"family"`
		Dimensions     *int64 `json:"dimensions"`
		MaxInputTokens *int64 `json:"maxInputTokens"`
		IsDefault      bool   `json:"isDefault"`
		Providers      []struct {
			ID       int64  `json:"id"`
			Platform string `json:"platform"`
			ModelID  string `json:"modelId"`
			Enabled  bool   `json:"enabled"`
			KeyCount int    `json:"keyCount"`
			IsCustom bool   `json:"isCustom"`
		} `json:"providers"`
	} `json:"families"`
}

type embCreateResp struct {
	Success    bool   `json:"success"`
	KeyID      int64  `json:"keyId"`
	ModelDbID  int64  `json:"modelDbId"`
	Family     string `json:"family"`
	Dimensions *int64 `json:"dimensions"`
	MaskedKey  string `json:"maskedKey"`
}

func embList(t *testing.T, s *Server, token string) embListResp {
	t.Helper()
	resp, body := do(t, s, http.MethodGet, "/api/embeddings", "", authed(token))
	require.Equal(t, http.StatusOK, resp.StatusCode, "body was %q", body)
	var out embListResp
	require.NoError(t, json.Unmarshal([]byte(body), &out), "body was %q", body)
	return out
}

// TestEmbeddingsSurfaceRequiresASession keeps the tab behind the one gate
// allowed to end a session: an unauthenticated read is a real session failure,
// and only that path may carry authentication_error.
func TestEmbeddingsSurfaceRequiresASession(t *testing.T) {
	t.Parallel()
	s := testServer(t, Options{})

	resp, body := do(t, s, http.MethodGet, "/api/embeddings", "", nil)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	require.Equal(t, string(TypeAuthentication), errorType(t, body))
}

// TestEmbeddingsListEmptyButWellFormed is the fresh-install contract: no seed
// rows, but the client must still get a families array it can map over, not
// null, and the reference default family.
func TestEmbeddingsListEmptyButWellFormed(t *testing.T) {
	t.Parallel()
	s := testServer(t, Options{})
	token := session(t, s)

	resp, body := do(t, s, http.MethodGet, "/api/embeddings", "", authed(token))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, body, `"families":[]`, "families must serialise as an empty array, not null")

	var out embListResp
	require.NoError(t, json.Unmarshal([]byte(body), &out))
	require.Equal(t, "gemini-embedding-001", out.DefaultFamily)
	require.Empty(t, out.Families)
}

// TestModelFamiliesStaySeparate is the reason chat, embedding and media each
// get their own table: a model of one family must never surface in another's
// listing, or a chat request could pick an embedding model that cannot answer
// it. Asserted directly across all three listings.
func TestModelFamiliesStaySeparate(t *testing.T) {
	t.Parallel()
	s := testServer(t, Options{})
	token := session(t, s)

	const embID = "sep-embed-xyz"
	const mediaID = "sep-media-xyz"
	const chatID = "sep-chat-xyz"

	// A chat row lives only in `models`.
	_, err := s.engine.DB().Exec(
		"INSERT INTO models (platform, model_id, display_name) VALUES ('groq', ?, 'Chat Sep')", chatID)
	require.NoError(t, err)

	_, ecreate := do(t, s, http.MethodPost, "/api/embeddings/custom",
		`{"baseUrl":"http://127.0.0.1:9/v1","model":"`+embID+`"}`, authed(token))
	require.Contains(t, ecreate, `"success":true`)
	_, mcreate := do(t, s, http.MethodPost, "/api/media/custom",
		`{"baseUrl":"http://127.0.0.1:9/v1","model":"`+mediaID+`","modality":"image"}`, authed(token))
	require.Contains(t, mcreate, `"success":true`)

	_, chatBody := do(t, s, http.MethodGet, "/api/models", "", authed(token))
	require.Contains(t, chatBody, chatID, "the chat listing must carry the chat model")
	require.NotContains(t, chatBody, embID, "an embedding model must never appear in the chat listing")
	require.NotContains(t, chatBody, mediaID, "a media model must never appear in the chat listing")

	_, embBody := do(t, s, http.MethodGet, "/api/embeddings", "", authed(token))
	require.Contains(t, embBody, embID, "the embedding listing must carry the embedding model")
	require.NotContains(t, embBody, chatID, "a chat model must never appear in the embedding listing")
	require.NotContains(t, embBody, mediaID, "a media model must never appear in the embedding listing")

	_, mediaBody := do(t, s, http.MethodGet, "/api/media", "", authed(token))
	require.Contains(t, mediaBody, mediaID, "the media listing must carry the media model")
	require.NotContains(t, mediaBody, chatID, "a chat model must never appear in the media listing")
	require.NotContains(t, mediaBody, embID, "an embedding model must never appear in the media listing")
}

// TestCustomEmbeddingRoundTrips walks the full lifecycle the client drives:
// create against a relay, see it listed for the detail page, then delete it.
func TestCustomEmbeddingRoundTrips(t *testing.T) {
	t.Parallel()
	s := testServer(t, Options{})
	token := session(t, s)

	const model = "text-embedding-roundtrip"
	resp, body := do(t, s, http.MethodPost, "/api/embeddings/custom",
		`{"baseUrl":"http://relay.test/v1/","model":"`+model+`","apiKey":"sk-embed-secret-plaintext-01"}`,
		authed(token))
	require.Equal(t, http.StatusCreated, resp.StatusCode, "body was %q", body)
	require.NotContains(t, body, "sk-embed-secret-plaintext-01", "the plaintext key must never be echoed")

	var created embCreateResp
	require.NoError(t, json.Unmarshal([]byte(body), &created))
	require.True(t, created.Success)
	require.NotZero(t, created.ModelDbID)
	require.Equal(t, model, created.Family)
	require.NotEmpty(t, created.MaskedKey)
	require.NotEqual(t, "sk-embed-secret-plaintext-01", created.MaskedKey)

	list := embList(t, s, token)
	require.Len(t, list.Families, 1)
	require.Equal(t, model, list.Families[0].Family)
	require.Len(t, list.Families[0].Providers, 1)
	p := list.Families[0].Providers[0]
	require.Equal(t, model, p.ModelID)
	require.True(t, p.IsCustom)
	require.Equal(t, created.ModelDbID, p.ID)
	require.Equal(t, 1, p.KeyCount, "a fresh custom key counts as one usable key")

	resp, body = do(t, s, http.MethodDelete, "/api/embeddings/custom/"+strconv.FormatInt(created.ModelDbID, 10), "", authed(token))
	require.Equal(t, http.StatusOK, resp.StatusCode, "body was %q", body)
	require.Contains(t, body, `"success":true`)

	require.Empty(t, embList(t, s, token).Families, "the model must be gone after delete")

	// The endpoint key is reaped with its last model rather than stranded.
	var keys int
	require.NoError(t, s.engine.DB().QueryRow("SELECT COUNT(*) FROM api_keys WHERE platform = 'custom'").Scan(&keys))
	require.Zero(t, keys, "the custom endpoint key must be reaped once nothing binds it")
}

// TestNullableEmbeddingDimensionSerialisesAsNull defends the null-vs-zero
// contract: the client renders "-" for an unknown dimension and a real value
// for a known one, so an unprobed custom model must report null, never 0.
func TestNullableEmbeddingDimensionSerialisesAsNull(t *testing.T) {
	t.Parallel()
	s := testServer(t, Options{})
	token := session(t, s)

	resp, body := do(t, s, http.MethodPost, "/api/embeddings/custom",
		`{"baseUrl":"http://relay.test/v1","model":"dimless"}`, authed(token))
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	require.Contains(t, body, `"dimensions":null`, "an unprobed dimension must be null, not 0")

	_, listBody := do(t, s, http.MethodGet, "/api/embeddings", "", authed(token))
	require.Contains(t, listBody, `"dimensions":null`)
	require.Contains(t, listBody, `"maxInputTokens":null`)

	list := embList(t, s, token)
	require.Len(t, list.Families, 1)
	require.Nil(t, list.Families[0].Dimensions)
	require.Nil(t, list.Families[0].MaxInputTokens)
}

// TestFamilyDimensionConflictIsBadRequestNotAuth keeps a rejected registration
// a validation error, never an authentication_error that would sign the
// operator out. Two models in one family at different dimensions cannot share a
// vector space.
func TestFamilyDimensionConflictIsBadRequestNotAuth(t *testing.T) {
	t.Parallel()
	s := testServer(t, Options{})
	token := session(t, s)

	_, body := do(t, s, http.MethodPost, "/api/embeddings/custom",
		`{"baseUrl":"http://relay.test/v1","model":"first","family":"shared","dimensions":1024}`, authed(token))
	require.Contains(t, body, `"success":true`)

	resp, body := do(t, s, http.MethodPost, "/api/embeddings/custom",
		`{"baseUrl":"http://relay.test/v1","model":"second","family":"shared","dimensions":768}`, authed(token))
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Equal(t, string(TypeInvalidRequest), errorType(t, body))
	require.NotEqual(t, string(TypeAuthentication), errorType(t, body))
}

// TestEmbeddingsUsageZeroesThenReal proves the usage summary reports a
// zeroed-but-shaped row for a family with no traffic, and real figures once a
// matching request is on the trail -- and that only successful requests count.
func TestEmbeddingsUsageZeroesThenReal(t *testing.T) {
	t.Parallel()
	s := testServer(t, Options{})
	token := session(t, s)

	const model = "usage-embed"
	_, body := do(t, s, http.MethodPost, "/api/embeddings/custom",
		`{"baseUrl":"http://relay.test/v1","model":"`+model+`"}`, authed(token))
	require.Contains(t, body, `"success":true`)

	usage := embUsage(t, s, token)
	require.Len(t, usage.Families, 1)
	require.EqualValues(t, 0, usage.Families[0].RequestsToday)
	require.EqualValues(t, 0, usage.Families[0].TokensMonth)
	require.EqualValues(t, 0, usage.TotalRequestsToday)
	require.EqualValues(t, 0, usage.TotalTokensMonth)

	now := time.Now().UTC().Unix()
	insertRequest(t, s, now, "custom", model, "success", 100)
	insertRequest(t, s, now, "custom", model, "success", 40)
	insertRequest(t, s, now, "custom", model, "error", 999) // must not count

	usage = embUsage(t, s, token)
	require.Len(t, usage.Families, 1)
	require.EqualValues(t, 2, usage.Families[0].RequestsToday, "only successful requests count")
	require.EqualValues(t, 140, usage.Families[0].TokensMonth)
	require.EqualValues(t, 2, usage.TotalRequestsToday)
	require.EqualValues(t, 140, usage.TotalTokensMonth)
}

type embUsageResp struct {
	Families []struct {
		Family        string  `json:"family"`
		RequestsToday int64   `json:"requestsToday"`
		TokensMonth   int64   `json:"tokensMonth"`
		Platform      *string `json:"platform"`
		QuotaLabel    *string `json:"quotaLabel"`
	} `json:"families"`
	TotalTokensMonth   int64 `json:"totalTokensMonth"`
	TotalRequestsToday int64 `json:"totalRequestsToday"`
}

func embUsage(t *testing.T, s *Server, token string) embUsageResp {
	t.Helper()
	resp, body := do(t, s, http.MethodGet, "/api/embeddings/usage", "", authed(token))
	require.Equal(t, http.StatusOK, resp.StatusCode, "body was %q", body)
	require.True(t, strings.Contains(body, `"families":`), "families key present")
	var out embUsageResp
	require.NoError(t, json.Unmarshal([]byte(body), &out), "body was %q", body)
	return out
}

// insertRequest writes one row straight to the request trail, the shape the
// usage joins read.
func insertRequest(t *testing.T, s *Server, createdAt int64, platform, modelID, outcome string, inputTokens int64) {
	t.Helper()
	_, err := s.engine.DB().Exec(
		"INSERT INTO requests (created_at, platform, model_id, outcome, input_tokens) VALUES (?, ?, ?, ?, ?)",
		createdAt, platform, modelID, outcome, inputTokens)
	require.NoError(t, err)
}

// TestCustomEndpointRejectsLinkLocalSSRF keeps an operator from pointing a
// custom embedding or media endpoint at a link-local or cloud-metadata
// address: the stored base_url would otherwise carry the credential there on
// every later call. The refusal is a 400 invalid_request, never an
// authentication_error.
func TestCustomEndpointRejectsLinkLocalSSRF(t *testing.T) {
	t.Parallel()
	s := testServer(t, Options{})
	token := session(t, s)

	for _, tc := range []struct{ path, body string }{
		{"/api/embeddings/custom", `{"baseUrl":"http://169.254.169.254/v1","model":"m"}`},
		{"/api/media/custom", `{"baseUrl":"http://169.254.169.254/v1","model":"m","modality":"image"}`},
	} {
		resp, body := do(t, s, http.MethodPost, tc.path, tc.body, authed(token))
		require.Equal(t, http.StatusBadRequest, resp.StatusCode, "path %s body %q", tc.path, body)
		require.Equal(t, string(TypeInvalidRequest), errorType(t, body))
		require.NotEqual(t, string(TypeAuthentication), errorType(t, body))
	}
}
