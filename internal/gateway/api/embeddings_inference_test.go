package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/neur0map/prowl/internal/gateway"
)

// seedEmbeddingRoute registers a custom-endpoint key and an embedding model
// pointing at it. Custom endpoints are the only way to aim a real adapter at a
// test server.
func seedEmbeddingRoute(t *testing.T, s *Server, family, label, baseURL string, priority int) int64 {
	t.Helper()

	keyID, err := s.engine.Vault().Add("custom", "test-key-"+label, gateway.AddOptions{
		Label:   label,
		BaseURL: baseURL,
	})
	require.NoError(t, err)

	res, err := s.engine.DB().Exec(`
		INSERT INTO embedding_models
			(family, platform, model_id, display_name, dimensions, max_input_tokens,
			 priority, enabled, quota_label, key_id)
		VALUES (?, 'custom', ?, ?, 8, 1000, ?, 1, '', ?)`,
		family, "embed-"+label, "Embed "+label, priority, keyID)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	return id
}

// newEmbeddingUpstream serves an OpenAI-shaped embedding response, counting
// calls so a test can prove failover moved rather than merely returning a
// status.
func newEmbeddingUpstream(t *testing.T, status int, vectors int) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"message":"upstream refused"}}`))
			return
		}
		data := make([]map[string]any, 0, vectors)
		for i := range vectors {
			data = append(data, map[string]any{
				"object": "embedding", "index": i,
				"embedding": []float64{0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8},
			})
		}
		payload, _ := json.Marshal(map[string]any{
			"object": "list", "data": data, "model": "embed",
			"usage": map[string]int{"prompt_tokens": 7, "total_tokens": 7},
		})
		_, _ = w.Write(payload)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// TestEmbeddingsRoundTripsThroughTheEngine is the proof the modality is wired:
// before this, the embedding adapters had no caller at all.
func TestEmbeddingsRoundTripsThroughTheEngine(t *testing.T) {
	t.Parallel()

	s := testServer(t, Options{MachineKey: compatMachineKey})
	upstream, calls := newEmbeddingUpstream(t, http.StatusOK, 1)
	seedEmbeddingRoute(t, s, "test-family", "only", upstream.URL, 1)

	resp, body := postCompat(t, s, "/v1/embeddings",
		`{"model":"test-family","input":"hello world"}`)
	require.Equal(t, http.StatusOK, resp.StatusCode, "body was %s", body)
	require.Equal(t, int64(1), calls.Load())

	var out struct {
		Object string `json:"object"`
		Data   []struct {
			Object    string    `json:"object"`
			Index     int       `json:"index"`
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
		Model string `json:"model"`
		Usage struct {
			PromptTokens int `json:"prompt_tokens"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"usage"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &out), "body was %s", body)
	require.Equal(t, "list", out.Object)
	require.Len(t, out.Data, 1)
	require.Equal(t, "embedding", out.Data[0].Object)
	require.Len(t, out.Data[0].Embedding, 8)
	require.Equal(t, "test-family", out.Model, "the model echoed back is the one requested")
	require.Equal(t, 7, out.Usage.PromptTokens, "the provider's own count wins over the estimate")
}

// TestEmbeddingsAcceptsAnArrayInput covers the batch shape, and pins that
// vectors stay index-aligned with the inputs that produced them.
func TestEmbeddingsAcceptsAnArrayInput(t *testing.T) {
	t.Parallel()

	s := testServer(t, Options{MachineKey: compatMachineKey})
	upstream, _ := newEmbeddingUpstream(t, http.StatusOK, 3)
	seedEmbeddingRoute(t, s, "batch-family", "only", upstream.URL, 1)

	resp, body := postCompat(t, s, "/v1/embeddings",
		`{"model":"batch-family","input":["one","two","three"]}`)
	require.Equal(t, http.StatusOK, resp.StatusCode, "body was %s", body)

	var out struct {
		Data []struct {
			Index int `json:"index"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &out))
	require.Len(t, out.Data, 3)
	for i, entry := range out.Data {
		require.Equal(t, i, entry.Index, "vectors must stay aligned with their inputs")
	}
}

// TestEmbeddingsFailsOverAcrossProviders is why this routes through the engine
// rather than calling a provider directly: an exhausted candidate must be
// skipped and benched, not returned to the caller.
//
// The first upstream answers 429 rather than 500 deliberately. A 5xx is a
// platform-level verdict and the engine rules out every key of that platform,
// which is correct but would leave nothing to fail over to here, since a
// custom endpoint is the only way to aim an adapter at a test server and both
// candidates therefore share the "custom" platform.
func TestEmbeddingsFailsOverAcrossProviders(t *testing.T) {
	t.Parallel()

	s := testServer(t, Options{MachineKey: compatMachineKey})
	limited, limitedCalls := newEmbeddingUpstream(t, http.StatusTooManyRequests, 0)
	working, workingCalls := newEmbeddingUpstream(t, http.StatusOK, 1)

	// Same family, so both are candidates for one request; priority decides
	// which is tried first.
	seedEmbeddingRoute(t, s, "shared", "limited", limited.URL, 1)
	seedEmbeddingRoute(t, s, "shared", "working", working.URL, 2)

	resp, body := postCompat(t, s, "/v1/embeddings",
		`{"model":"shared","input":"hello"}`)
	require.Equal(t, http.StatusOK, resp.StatusCode, "body was %s", body)
	require.Equal(t, int64(1), limitedCalls.Load(), "the first candidate must have been tried")
	require.Equal(t, int64(1), workingCalls.Load(), "and the request must have moved to the second")
}

// TestEmbeddingsRejectsAnOverLongInput keeps the model's published cap as our
// clear verdict instead of an opaque upstream rejection that also spends quota.
func TestEmbeddingsRejectsAnOverLongInput(t *testing.T) {
	t.Parallel()

	s := testServer(t, Options{MachineKey: compatMachineKey})
	upstream, calls := newEmbeddingUpstream(t, http.StatusOK, 1)
	seedEmbeddingRoute(t, s, "capped", "only", upstream.URL, 1)

	// The seeded model caps input at 1000 tokens; the estimate is chars/4.
	long := make([]byte, 8000)
	for i := range long {
		long[i] = 'a'
	}
	resp, body := postCompat(t, s, "/v1/embeddings",
		`{"model":"capped","input":"`+string(long)+`"}`)

	require.Equal(t, http.StatusBadRequest, resp.StatusCode, "body was %s", body)
	require.Contains(t, body, "input_too_long")
	require.Zero(t, calls.Load(), "an over-long input must be refused before the upstream call")
	require.NotContains(t, body, "authentication_error")
}

// TestEmbeddingsWithNoModelsDegradesHonestly distinguishes the three reasons a
// pool can be empty, because they need different fixes from the operator.
func TestEmbeddingsWithNoModelsDegradesHonestly(t *testing.T) {
	t.Parallel()

	s := testServer(t, Options{MachineKey: compatMachineKey})

	// Nothing seeded at all: this build has no embedding models.
	resp, body := postCompat(t, s, "/v1/embeddings", `{"model":"anything","input":"hi"}`)
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode, "body was %s", body)
	require.Contains(t, body, "no_models")
	require.NotContains(t, body, "authentication_error")

	// A model exists, but the request names one that does not.
	upstream, _ := newEmbeddingUpstream(t, http.StatusOK, 1)
	seedEmbeddingRoute(t, s, "real-family", "only", upstream.URL, 1)

	resp, body = postCompat(t, s, "/v1/embeddings", `{"model":"no-such-family","input":"hi"}`)
	require.Equal(t, http.StatusNotFound, resp.StatusCode, "body was %s", body)
	require.Contains(t, body, "model_not_found")
}

// TestEmbeddingsRefusesUnsupportedEncoding: base64 is in the OpenAI schema but
// no adapter here produces it, and returning floats to a caller that asked for
// base64 would be a silently wrong answer.
func TestEmbeddingsRefusesUnsupportedEncoding(t *testing.T) {
	t.Parallel()

	s := testServer(t, Options{MachineKey: compatMachineKey})
	resp, body := postCompat(t, s, "/v1/embeddings",
		`{"model":"any","input":"hi","encoding_format":"base64"}`)

	require.Equal(t, http.StatusBadRequest, resp.StatusCode, "body was %s", body)
	require.Contains(t, body, "unsupported_encoding_format")
}

// TestEmbeddingModelIDsAreNamespaced guards the collision that would make an
// embedding failure bench an unrelated chat model: all three catalogues start
// their ids at 1 and share one penalty store.
func TestEmbeddingModelIDsAreNamespaced(t *testing.T) {
	t.Parallel()

	s := testServer(t, Options{MachineKey: compatMachineKey})
	upstream, _ := newEmbeddingUpstream(t, http.StatusOK, 1)
	rowID := seedEmbeddingRoute(t, s, "ns", "only", upstream.URL, 1)

	candidates, err := embeddingCandidates(t.Context(), s.engine.DB(), "ns")
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	require.Equal(t, rowID+embeddingModelIDBase, candidates[0].ModelDBID,
		"an embedding model must not share a penalty-store key with a chat model")
	require.Greater(t, candidates[0].ModelDBID, embeddingModelIDBase)
}

// TestEmbeddingsKnownModelWithoutAKeyIsNotNotFound is the distinction that
// decides what the operator does next. A family the catalogue ships but no key
// can serve is a missing credential, not a typo, and reporting "not found"
// would send them hunting for a misspelling that does not exist.
func TestEmbeddingsKnownModelWithoutAKeyIsNotNotFound(t *testing.T) {
	t.Parallel()

	s := testServer(t, Options{MachineKey: compatMachineKey})

	// A catalogue row with no usable key: no api_keys row is created for it.
	_, err := s.engine.DB().Exec(`
		INSERT INTO embedding_models
			(family, platform, model_id, display_name, dimensions, max_input_tokens,
			 priority, enabled, quota_label, key_id)
		VALUES ('keyless-family', 'google', 'embed-keyless', 'Keyless', 8, 1000, 1, 1, '', NULL)`)
	require.NoError(t, err)

	resp, body := postCompat(t, s, "/v1/embeddings",
		`{"model":"keyless-family","input":"hi"}`)

	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode, "body was %s", body)
	require.Contains(t, body, "no_provider_keys")
	require.NotContains(t, body, "model_not_found",
		"the model exists; the operator needs a key, not a corrected spelling")
}
