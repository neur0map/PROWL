package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type mediaListResp struct {
	Models []struct {
		ID       int64  `json:"id"`
		Platform string `json:"platform"`
		ModelID  string `json:"modelId"`
		Modality string `json:"modality"`
		Enabled  bool   `json:"enabled"`
		KeyCount int    `json:"keyCount"`
		IsCustom bool   `json:"isCustom"`
	} `json:"models"`
}

type mediaCreateResp struct {
	Success   bool   `json:"success"`
	ModelDbID int64  `json:"modelDbId"`
	Modality  string `json:"modality"`
	MaskedKey string `json:"maskedKey"`
}

type mediaUsageResp struct {
	Modality string `json:"modality"`
	Models   []struct {
		ID            int64   `json:"id"`
		ModelID       string  `json:"modelId"`
		QuotaLabel    *string `json:"quotaLabel"`
		RequestsToday int64   `json:"requestsToday"`
		RequestsMonth int64   `json:"requestsMonth"`
	} `json:"models"`
	TotalRequestsToday int64 `json:"totalRequestsToday"`
	TotalRequestsMonth int64 `json:"totalRequestsMonth"`
}

func mediaListModels(t *testing.T, s *Server, token string) mediaListResp {
	t.Helper()
	resp, body := do(t, s, http.MethodGet, "/api/media", "", authed(token))
	require.Equal(t, http.StatusOK, resp.StatusCode, "body was %q", body)
	var out mediaListResp
	require.NoError(t, json.Unmarshal([]byte(body), &out), "body was %q", body)
	return out
}

func mediaUsage(t *testing.T, s *Server, token, modality string) mediaUsageResp {
	t.Helper()
	resp, body := do(t, s, http.MethodGet, "/api/media/usage?modality="+modality, "", authed(token))
	require.Equal(t, http.StatusOK, resp.StatusCode, "body was %q", body)
	var out mediaUsageResp
	require.NoError(t, json.Unmarshal([]byte(body), &out), "body was %q", body)
	return out
}

// TestMediaListEmptyButWellFormed is the fresh-install contract for the
// Image/Video/Audio tabs: an empty but mappable models array, never null.
func TestMediaListEmptyButWellFormed(t *testing.T) {
	t.Parallel()
	s := testServer(t, Options{})
	token := session(t, s)

	resp, body := do(t, s, http.MethodGet, "/api/media", "", authed(token))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, body, `"models":[]`, "models must serialise as an empty array, not null")
}

// TestCustomMediaRoundTrips walks create -> list -> toggle -> delete, the full
// lifecycle the Image tab and MediaDetailPage drive.
func TestCustomMediaRoundTrips(t *testing.T) {
	t.Parallel()
	s := testServer(t, Options{})
	token := session(t, s)

	const model = "img-roundtrip"
	resp, body := do(t, s, http.MethodPost, "/api/media/custom",
		`{"baseUrl":"http://relay.test/v1","model":"`+model+`","modality":"image","apiKey":"sk-media-secret-plaintext-9"}`,
		authed(token))
	require.Equal(t, http.StatusCreated, resp.StatusCode, "body was %q", body)
	require.NotContains(t, body, "sk-media-secret-plaintext-9", "the plaintext key must never be echoed")

	var created mediaCreateResp
	require.NoError(t, json.Unmarshal([]byte(body), &created))
	require.True(t, created.Success)
	require.NotZero(t, created.ModelDbID)
	require.Equal(t, "image", created.Modality)
	require.NotEmpty(t, created.MaskedKey)

	list := mediaListModels(t, s, token)
	require.Len(t, list.Models, 1)
	require.Equal(t, model, list.Models[0].ModelID)
	require.Equal(t, "image", list.Models[0].Modality)
	require.True(t, list.Models[0].Enabled, "a new custom model is enabled")
	require.True(t, list.Models[0].IsCustom)
	require.Equal(t, 1, list.Models[0].KeyCount)

	idPath := "/api/media/" + strconv.FormatInt(created.ModelDbID, 10)
	resp, body = do(t, s, http.MethodPut, idPath, `{"enabled":false}`, authed(token))
	require.Equal(t, http.StatusOK, resp.StatusCode, "body was %q", body)

	list = mediaListModels(t, s, token)
	require.Len(t, list.Models, 1)
	require.False(t, list.Models[0].Enabled, "the toggle must persist")

	resp, body = do(t, s, http.MethodDelete, "/api/media/custom/"+strconv.FormatInt(created.ModelDbID, 10), "", authed(token))
	require.Equal(t, http.StatusOK, resp.StatusCode, "body was %q", body)
	require.Empty(t, mediaListModels(t, s, token).Models, "the model must be gone after delete")

	var keys int
	require.NoError(t, s.engine.DB().QueryRow("SELECT COUNT(*) FROM api_keys WHERE platform = 'custom'").Scan(&keys))
	require.Zero(t, keys, "the endpoint key must be reaped with its last model")
}

// TestMediaToggleUnknownIs404 keeps a toggle of a missing model a not-found,
// never an authentication_error.
func TestMediaToggleUnknownIs404(t *testing.T) {
	t.Parallel()
	s := testServer(t, Options{})
	token := session(t, s)

	resp, body := do(t, s, http.MethodPut, "/api/media/999999", `{"enabled":false}`, authed(token))
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	require.Equal(t, string(TypeNotFound), errorType(t, body))
}

// TestMediaCreateRejectsVideo: video providers are keyless and catalog-managed,
// so the custom-create form never offers video. A video body is a bad request,
// not a fabricated success.
func TestMediaCreateRejectsVideo(t *testing.T) {
	t.Parallel()
	s := testServer(t, Options{})
	token := session(t, s)

	resp, body := do(t, s, http.MethodPost, "/api/media/custom",
		`{"baseUrl":"http://relay.test/v1","model":"veo","modality":"video"}`, authed(token))
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Equal(t, string(TypeInvalidRequest), errorType(t, body))
}

// TestMediaUsageZeroesThenReal proves a modality with no models reports a
// zeroed, well-shaped summary (not an empty object), a model with no traffic
// reports zeroes, and a recorded request lights up the right modality.
func TestMediaUsageZeroesThenReal(t *testing.T) {
	t.Parallel()
	s := testServer(t, Options{})
	token := session(t, s)

	// A modality with no models: empty list, zero totals, right shape.
	empty := mediaUsage(t, s, token, "video")
	require.Equal(t, "video", empty.Modality)
	require.Empty(t, empty.Models)
	require.EqualValues(t, 0, empty.TotalRequestsToday)
	require.EqualValues(t, 0, empty.TotalRequestsMonth)

	const model = "usage-img"
	_, body := do(t, s, http.MethodPost, "/api/media/custom",
		`{"baseUrl":"http://relay.test/v1","model":"`+model+`","modality":"image"}`, authed(token))
	require.Contains(t, body, `"success":true`)

	usage := mediaUsage(t, s, token, "image")
	require.Len(t, usage.Models, 1)
	require.EqualValues(t, 0, usage.Models[0].RequestsToday)
	require.EqualValues(t, 0, usage.TotalRequestsToday)

	now := time.Now().UTC().Unix()
	insertRequest(t, s, now, "custom", model, "success", 0)
	insertRequest(t, s, now, "custom", model, "error", 0) // must not count

	usage = mediaUsage(t, s, token, "image")
	require.Len(t, usage.Models, 1)
	require.EqualValues(t, 1, usage.Models[0].RequestsToday, "only successful requests count")
	require.EqualValues(t, 1, usage.TotalRequestsToday)

	// The traffic must not bleed into another modality's summary.
	other := mediaUsage(t, s, token, "audio")
	require.EqualValues(t, 0, other.TotalRequestsToday)
}
