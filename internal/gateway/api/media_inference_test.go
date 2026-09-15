package api

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/neur0map/prowl/internal/gateway"
)

// seedMediaRoute registers a custom-endpoint key and a media model of the
// given modality pointing at it.
func seedMediaRoute(t *testing.T, s *Server, modality, label, baseURL string) int64 {
	t.Helper()

	keyID, err := s.engine.Vault().Add("custom", "test-key-"+label, gateway.AddOptions{
		Label:   label,
		BaseURL: baseURL,
	})
	require.NoError(t, err)

	res, err := s.engine.DB().Exec(`
		INSERT INTO media_models
			(platform, model_id, display_name, modality, priority, enabled, quota_label, key_id)
		VALUES ('custom', ?, ?, ?, 1, 1, '', ?)`,
		modality+"-"+label, "Media "+label, modality, keyID)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	return id
}

// TestImageGenerationRoundTrips proves the image adapter is reachable; before
// this it had no caller at all.
func TestImageGenerationRoundTrips(t *testing.T) {
	t.Parallel()

	s := testServer(t, Options{MachineKey: compatMachineKey})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":1,"data":[{"b64_json":"AAAA"}]}`))
	}))
	t.Cleanup(upstream.Close)
	seedMediaRoute(t, s, "image", "only", upstream.URL)

	resp, body := postCompat(t, s, "/v1/images/generations",
		`{"model":"auto","prompt":"a cat","n":1}`)
	require.Equal(t, http.StatusOK, resp.StatusCode, "body was %s", body)

	var out struct {
		Created int64 `json:"created"`
		Data    []struct {
			B64JSON string `json:"b64_json"`
			URL     string `json:"url"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &out), "body was %s", body)
	require.Len(t, out.Data, 1)
	require.Equal(t, "AAAA", out.Data[0].B64JSON)
	require.Positive(t, out.Created)
}

// TestSpeechReturnsAudioBytes pins the property that distinguishes this route
// from every other one: the body is audio, not JSON.
func TestSpeechReturnsAudioBytes(t *testing.T) {
	t.Parallel()

	s := testServer(t, Options{MachineKey: compatMachineKey})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte("ID3fake-mp3-bytes"))
	}))
	t.Cleanup(upstream.Close)
	seedMediaRoute(t, s, "audio", "only", upstream.URL)

	resp, body := postCompat(t, s, "/v1/audio/speech",
		`{"model":"auto","input":"hello there","voice":"alloy"}`)
	require.Equal(t, http.StatusOK, resp.StatusCode, "body was %s", body)
	require.Contains(t, resp.Header.Get("Content-Type"), "audio/",
		"a caller saves or plays these bytes; a JSON content type would break it")
	require.Contains(t, body, "fake-mp3-bytes")
}

// TestTranscriptionAcceptsAMultipartUpload covers the only inference route that
// takes a file rather than JSON.
func TestTranscriptionAcceptsAMultipartUpload(t *testing.T) {
	t.Parallel()

	s := testServer(t, Options{MachineKey: compatMachineKey})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"the transcript","language":"en","duration":1.5}`))
	}))
	t.Cleanup(upstream.Close)
	seedMediaRoute(t, s, "transcription", "only", upstream.URL)

	var payload bytes.Buffer
	form := multipart.NewWriter(&payload)
	part, err := form.CreateFormFile("file", "clip.wav")
	require.NoError(t, err)
	_, err = part.Write([]byte("RIFFfake-wav"))
	require.NoError(t, err)
	require.NoError(t, form.WriteField("model", "auto"))
	require.NoError(t, form.Close())

	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", &payload)
	req.RemoteAddr = "127.0.0.1:50000"
	req.Header.Set("Content-Type", form.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+compatMachineKey)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "body was %s", rec.Body.String())
	var out struct {
		Text     string  `json:"text"`
		Language string  `json:"language"`
		Duration float64 `json:"duration"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Equal(t, "the transcript", out.Text)
	require.Equal(t, "en", out.Language)
}

// TestTranscriptionRequiresAFile keeps a malformed upload a clear 400 rather
// than a confusing failure further down.
func TestTranscriptionRequiresAFile(t *testing.T) {
	t.Parallel()

	s := testServer(t, Options{MachineKey: compatMachineKey})
	resp, body := postCompat(t, s, "/v1/audio/transcriptions", `{"model":"auto"}`)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode, "body was %s", body)
	require.NotContains(t, body, "authentication_error")
}

// TestVideoGenerationRefusesHonestly is the case the catalogue forces: there
// are no video models at all. The route must exist and say so — a 404 would
// read as "no such feature", and a fabricated job id would be worse.
func TestVideoGenerationRefusesHonestly(t *testing.T) {
	t.Parallel()

	s := testServer(t, Options{MachineKey: compatMachineKey})
	resp, body := postCompat(t, s, "/v1/videos/generations",
		`{"model":"auto","prompt":"a cat"}`)

	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode, "body was %s", body)
	require.Contains(t, body, "no_models")
	require.Contains(t, body, "video")
	require.NotContains(t, body, "authentication_error")
}

// TestMediaModelIDsAreNamespaced guards the shared penalty store: media and
// embedding ids must not collide with each other or with chat models.
func TestMediaModelIDsAreNamespaced(t *testing.T) {
	t.Parallel()

	s := testServer(t, Options{MachineKey: compatMachineKey})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	t.Cleanup(upstream.Close)
	rowID := seedMediaRoute(t, s, "image", "only", upstream.URL)

	candidates, err := mediaCandidates(t.Context(), s.engine.DB(), "image", "")
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	require.Equal(t, rowID+mediaModelIDBase, candidates[0].ModelDBID)
	require.Greater(t, candidates[0].ModelDBID, embeddingModelIDBase,
		"media ids must sit above the embedding namespace, not overlap it")
}

// TestMediaSurfacesRequireACredential keeps the new routes behind the same gate
// as the rest of the inference plane.
func TestMediaSurfacesRequireACredential(t *testing.T) {
	t.Parallel()

	s := testServer(t, Options{MachineKey: compatMachineKey})
	for _, path := range []string{
		"/v1/images/generations", "/v1/audio/speech", "/v1/videos/generations",
	} {
		resp, body := do(t, s, http.MethodPost, path, `{"model":"auto"}`,
			map[string]string{"Content-Type": "application/json"})
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "%s must be gated", path)
		require.NotContains(t, body, "authentication_error", "%s", path)
	}
}
