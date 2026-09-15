package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/neur0map/prowl/internal/gateway"
	"github.com/neur0map/prowl/internal/gateway/provider"
)

func (s *Server) registerMediaInferenceRoutes() {
	s.mux.HandleFunc("POST /v1/images/generations", s.RequireMachineKey(s.handleImageGeneration))
	s.mux.HandleFunc("POST /v1/audio/speech", s.RequireMachineKey(s.handleSpeech))
	s.mux.HandleFunc("POST /v1/audio/transcriptions", s.RequireMachineKey(s.handleTranscription))
	s.mux.HandleFunc("POST /v1/videos/generations", s.RequireMachineKey(s.handleVideoGeneration))
}

// maxUploadBody bounds a transcription upload. The audio is held in memory and
// never written to disk, so this is also the memory ceiling per request.
const maxUploadBody = 25 << 20

// ── image generation ────────────────────────────────────────────────────────

type imageRequestBody struct {
	Model          string `json:"model"`
	Prompt         string `json:"prompt"`
	N              int    `json:"n"`
	Size           string `json:"size"`
	ResponseFormat string `json:"response_format"`
}

func (s *Server) handleImageGeneration(w http.ResponseWriter, r *http.Request) {
	requestID := ensureRequestID(w, r)

	body, err := readInferenceBody(w, r)
	if err != nil {
		return
	}
	var req imageRequestBody
	if err := json.Unmarshal(body, &req); err != nil {
		WriteError(w, http.StatusBadRequest, TypeInvalidRequest, "invalid JSON body")
		return
	}
	if req.Prompt == "" {
		WriteError(w, http.StatusBadRequest, TypeInvalidRequest, "prompt is required")
		return
	}
	if req.N <= 0 {
		req.N = 1
	}

	relay := &mediaRelay{server: s, modality: "image", requestID: requestID}
	relay.attempt = func(ctx context.Context, prov provider.Provider, apiKey string, route gateway.Route) error {
		generator, ok := prov.(provider.ImageGenerator)
		if !ok {
			return errors.New("provider " + route.Platform + " does not generate images")
		}
		resp, err := generator.Images(ctx, apiKey, &provider.ImageRequest{
			Model: route.ModelID, Prompt: req.Prompt, N: req.N,
			Size: req.Size, ResponseFormat: req.ResponseFormat,
		})
		if err != nil {
			return err
		}
		relay.image = resp
		return nil
	}

	if !s.runModality(w, r, relay, "image", req.Model) {
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"created": time.Now().Unix(),
		"data":    relay.image.Data,
	})
}

// ── speech synthesis ────────────────────────────────────────────────────────

type speechRequestBody struct {
	Model          string `json:"model"`
	Input          string `json:"input"`
	Voice          string `json:"voice"`
	ResponseFormat string `json:"response_format"`
}

func (s *Server) handleSpeech(w http.ResponseWriter, r *http.Request) {
	requestID := ensureRequestID(w, r)

	body, err := readInferenceBody(w, r)
	if err != nil {
		return
	}
	var req speechRequestBody
	if err := json.Unmarshal(body, &req); err != nil {
		WriteError(w, http.StatusBadRequest, TypeInvalidRequest, "invalid JSON body")
		return
	}
	if req.Input == "" {
		WriteError(w, http.StatusBadRequest, TypeInvalidRequest, "input is required")
		return
	}

	relay := &mediaRelay{server: s, modality: "audio", requestID: requestID}
	relay.attempt = func(ctx context.Context, prov provider.Provider, apiKey string, route gateway.Route) error {
		synth, ok := prov.(provider.SpeechSynthesizer)
		if !ok {
			return errors.New("provider " + route.Platform + " does not synthesise speech")
		}
		resp, err := synth.Speech(ctx, apiKey, &provider.SpeechRequest{
			Model: route.ModelID, Input: req.Input,
			Voice: req.Voice, Format: req.ResponseFormat,
		})
		if err != nil {
			return err
		}
		relay.speech = resp
		return nil
	}

	if !s.runModality(w, r, relay, "audio", req.Model) {
		return
	}
	// Audio is returned as bytes, not JSON: the caller plays or saves it.
	contentType := relay.speech.ContentType
	if contentType == "" {
		contentType = "audio/mpeg"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(relay.speech.Audio)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(relay.speech.Audio)
}

// ── transcription ───────────────────────────────────────────────────────────

func (s *Server) handleTranscription(w http.ResponseWriter, r *http.Request) {
	requestID := ensureRequestID(w, r)

	// Multipart, not JSON: this is the one inference route that takes a file.
	if err := r.ParseMultipartForm(maxUploadBody); err != nil {
		WriteErrorCode(w, http.StatusBadRequest, TypeInvalidRequest, "invalid_multipart",
			"this endpoint takes a multipart form with a file field")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		WriteError(w, http.StatusBadRequest, TypeInvalidRequest, "file is required")
		return
	}
	defer func() { _ = file.Close() }()

	audio := make([]byte, 0, header.Size)
	buf := make([]byte, 32<<10)
	for {
		n, readErr := file.Read(buf)
		audio = append(audio, buf[:n]...)
		if len(audio) > maxUploadBody {
			WriteErrorCode(w, http.StatusRequestEntityTooLarge, TypeInvalidRequest,
				"file_too_large", "the audio file is too large")
			return
		}
		if readErr != nil {
			break
		}
	}
	if len(audio) == 0 {
		WriteError(w, http.StatusBadRequest, TypeInvalidRequest, "file is empty")
		return
	}

	model := r.FormValue("model")
	relay := &mediaRelay{server: s, modality: "transcription", requestID: requestID}
	relay.attempt = func(ctx context.Context, prov provider.Provider, apiKey string, route gateway.Route) error {
		transcriber, ok := prov.(provider.Transcriber)
		if !ok {
			return errors.New("provider " + route.Platform + " does not transcribe audio")
		}
		resp, err := transcriber.Transcribe(ctx, apiKey, &provider.TranscriptionRequest{
			Model: route.ModelID, File: audio, Filename: header.Filename,
			MimeType:       header.Header.Get("Content-Type"),
			Language:       r.FormValue("language"),
			Prompt:         r.FormValue("prompt"),
			ResponseFormat: r.FormValue("response_format"),
		})
		if err != nil {
			return err
		}
		relay.transcript = resp
		return nil
	}

	if !s.runModality(w, r, relay, "transcription", model) {
		return
	}

	if r.FormValue("response_format") == "text" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(relay.transcript.Text))
		return
	}
	payload := map[string]any{"text": relay.transcript.Text}
	if relay.transcript.Language != "" {
		payload["language"] = relay.transcript.Language
	}
	if relay.transcript.Duration > 0 {
		payload["duration"] = relay.transcript.Duration
	}
	if len(relay.transcript.Segments) > 0 {
		payload["segments"] = relay.transcript.Segments
	}
	WriteJSON(w, http.StatusOK, payload)
}

// ── video ───────────────────────────────────────────────────────────────────

// handleVideoGeneration exists so the route answers honestly. The shipped
// catalogue has no video models at all, and a 404 here would read as "this
// gateway has no such feature" while a fabricated job id would be worse.
func (s *Server) handleVideoGeneration(w http.ResponseWriter, r *http.Request) {
	ensureRequestID(w, r)
	modalityUnavailable(w, "video", "", hasEnabledMediaModels(r.Context(), s.engine.DB(), "video"), false)
}

// ── shared relay ────────────────────────────────────────────────────────────

// mediaRelay drives one media attempt per candidate through the shared
// failover loop, so a media request cools down a dead provider and lands in
// the request trail exactly like a chat request.
type mediaRelay struct {
	server   *Server
	modality string
	attempt  func(ctx context.Context, prov provider.Provider, apiKey string, route gateway.Route) error

	image      *provider.ImageResponse
	speech     *provider.SpeechResponse
	transcript *provider.TranscriptionResponse
	secrets    []string
	requestID  string
}

func (m *mediaRelay) Dispatch(ctx context.Context, route gateway.Route, _ int) gateway.DispatchResult {
	prov, ok := m.server.engine.Registry().Resolve(route.Platform, route.BaseURL)
	if !ok {
		m.server.logServerEvent(ctx, serverLogRecord{
			Level: "warn", Source: m.modality, Provider: route.Platform, Model: route.ModelID,
			Event: "no_provider_adapter", RequestID: m.requestID,
			Message: "no wire adapter is registered for " + route.Platform,
		})
		return gateway.DispatchResult{Err: gateway.UndispatchableError(route.Platform)}
	}

	apiKey := ""
	if !prov.Keyless() {
		revealed, err := m.server.engine.Vault().Reveal(ctx, route.KeyID)
		if err != nil {
			return gateway.DispatchResult{Err: err}
		}
		apiKey = revealed
		m.secrets = append(m.secrets, revealed)
	}

	// Media is billed per request rather than per token, so the lease reserves
	// a request slot and settles zero tokens.
	lease, admitted := m.server.engine.Ledger().Acquire(gateway.Admission{
		Platform: route.Platform,
		ModelID:  route.ModelID,
		KeyID:    route.KeyID,
		Limits: gateway.WindowLimits{
			RPD: derefLimit(route.RPDLimit), TPD: derefLimit(route.TPDLimit),
		},
	})
	if !admitted {
		return gateway.DispatchResult{
			Status: http.StatusTooManyRequests,
			Err:    errors.New("rate limit reached for " + route.Platform),
		}
	}
	settled := false
	defer func() {
		if !settled {
			lease.Release()
		}
	}()

	start := time.Now()
	if err := m.attempt(ctx, prov, apiKey, route); err != nil {
		m.server.logServerEvent(ctx, serverLogRecord{
			Level: "warn", Source: m.modality, Provider: route.Platform, Model: route.ModelID,
			Event: string(gateway.ClassifyAttempt(err)), RequestID: m.requestID,
			Message: redactedProviderMessage(err, m.secrets),
		})
		return dispatchFromError(err)
	}

	settled = true
	lease.Settle(0)
	m.server.recordModalityRequest(ctx, route, 0, 0, time.Since(start))
	return gateway.DispatchResult{Outcome: gateway.OutcomeDone}
}

// runModality resolves the pool, runs the loop, and reports whether the caller
// should render a success. It writes the failure itself when it returns false.
func (s *Server) runModality(w http.ResponseWriter, r *http.Request, relay *mediaRelay, modality, model string) bool {
	candidates, err := mediaCandidates(r.Context(), s.engine.DB(), modality, model)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not resolve "+modality+" models")
		return false
	}
	if len(candidates) == 0 {
		modalityUnavailable(w, modality, model,
			hasEnabledMediaModels(r.Context(), s.engine.DB(), modality),
			mediaModelKnown(r.Context(), s.engine.DB(), modality, model))
		return false
	}

	chain := &modalityChain{candidates: candidates}
	result := s.engine.Failover().Run(r.Context(), gateway.DispatchRequest{
		Chain:      chain,
		Dispatcher: relay,
		ClientGone: func() bool { return r.Context().Err() != nil },
	})
	if result != nil && result.Status == gateway.StatusSucceeded {
		return true
	}
	s.logServerEvent(r.Context(), serverLogRecord{
		Level: "error", Source: modality, Event: "exhausted", RequestID: relay.requestID,
		Message: modalityFailureMessage(result, relay.secrets),
	})
	writeModalityFailure(w, result, relay.secrets)
	return false
}
