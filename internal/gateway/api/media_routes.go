package api

import (
	"context"
	"database/sql"
	"net/http"
	"strconv"
	"strings"

	"github.com/neur0map/prowl/internal/gateway"
)

// Media models (image, video, audio/TTS, transcription) back the dashboard's
// Image, Video and Audio tabs. Like embeddings they live in their own table so
// a chat request can never misroute into an image model. This file is the
// management surface -- listing, per-modality usage, enable toggle, custom
// create/delete -- and shares the custom-endpoint key helpers defined in
// embeddings_routes.go (emResolveEndpointKey, emReapEndpointKey, ...).

func (s *Server) registerMediaRoutes() {
	s.mux.HandleFunc("GET /api/media", s.RequireSession(s.handleMediaList))
	s.mux.HandleFunc("GET /api/media/usage", s.RequireSession(s.handleMediaUsage))
	s.mux.HandleFunc("POST /api/media/custom", s.RequireSession(s.handleMediaCreateCustom))
	s.mux.HandleFunc("PUT /api/media/{id}", s.RequireSession(s.handleMediaToggle))
	s.mux.HandleFunc("DELETE /api/media/custom/{id}", s.RequireSession(s.handleMediaDeleteCustom))
}

type mediaModelOut struct {
	ID          int64  `json:"id"`
	Platform    string `json:"platform"`
	ModelID     string `json:"modelId"`
	DisplayName string `json:"displayName"`
	Modality    string `json:"modality"`
	Enabled     bool   `json:"enabled"`
	QuotaLabel  string `json:"quotaLabel"`
	KeyCount    int    `json:"keyCount"`
	IsCustom    bool   `json:"isCustom"`
}

// handleMediaList returns the flat media-model list the tabs consolidate into
// logical models client-side. An empty install answers with a well-formed
// empty list rather than null.
func (s *Server) handleMediaList(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	ctx := r.Context()
	db := s.engine.DB()

	keyCounts := keyCountsByPlatform(db, true)
	customHealthy := emCustomHealthyKeyIDs(ctx, db)

	rows, err := db.QueryContext(ctx, `
		SELECT id, platform, model_id, display_name, modality, enabled, quota_label, key_id
		  FROM media_models
		 ORDER BY modality, priority, id`)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not list media models")
		return
	}
	defer rows.Close()

	models := make([]mediaModelOut, 0)
	for rows.Next() {
		var (
			m       mediaModelOut
			enabled int
			keyID   sql.NullInt64
		)
		if err := rows.Scan(&m.ID, &m.Platform, &m.ModelID, &m.DisplayName, &m.Modality,
			&enabled, &m.QuotaLabel, &keyID); err != nil {
			WriteError(w, http.StatusInternalServerError, TypeServer, "could not list media models")
			return
		}
		m.Enabled = enabled != 0
		m.IsCustom = m.Platform == "custom"
		m.KeyCount = emKeyCount(m.Platform, keyID, keyCounts, customHealthy)
		models = append(models, m)
	}
	if err := rows.Err(); err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not list media models")
		return
	}

	WriteJSON(w, http.StatusOK, map[string]any{"models": models})
}

type mediaUsageModel struct {
	ID            int64   `json:"id"`
	Platform      string  `json:"platform"`
	ModelID       string  `json:"modelId"`
	DisplayName   string  `json:"displayName"`
	QuotaLabel    *string `json:"quotaLabel"`
	RequestsToday int64   `json:"requestsToday"`
	RequestsMonth int64   `json:"requestsMonth"`
}

// handleMediaUsage reports per-model request counts for one modality from the
// request trail. Image and audio calls are billed per image or per character,
// not per token, so requests -- not tokens -- is the honest unit and no token
// counts are reported (media.ts:46-55). A modality with no traffic reports
// zero totals and an empty-but-well-formed model list.
func (s *Server) handleMediaUsage(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	modality := r.URL.Query().Get("modality")
	if !mediaUsageModality(modality) {
		WriteError(w, http.StatusBadRequest, TypeInvalidRequest,
			"modality must be image, video, audio or transcription")
		return
	}
	ctx := r.Context()
	db := s.engine.DB()
	startOfDay, startOfMonth := emDayMonthStart()

	rows, err := db.QueryContext(ctx, `
		SELECT mm.id, mm.platform, mm.model_id, mm.display_name, mm.quota_label,
		       COALESCE(SUM(CASE WHEN r.created_at >= ? THEN 1 ELSE 0 END), 0) AS requests_today,
		       COALESCE(SUM(CASE WHEN r.created_at >= ? THEN 1 ELSE 0 END), 0) AS requests_month
		  FROM media_models mm
		  LEFT JOIN requests r
		    ON r.outcome = 'success'
		   AND r.platform = mm.platform
		   AND r.model_id = mm.model_id
		   AND r.created_at >= ?
		 WHERE mm.modality = ? AND mm.enabled = 1
		 GROUP BY mm.id
		 ORDER BY mm.priority ASC`, startOfDay, startOfMonth, startOfMonth, modality)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not read media usage")
		return
	}
	defer rows.Close()

	models := make([]mediaUsageModel, 0)
	var totalToday, totalMonth int64
	for rows.Next() {
		var (
			m     mediaUsageModel
			quota sql.NullString
		)
		if err := rows.Scan(&m.ID, &m.Platform, &m.ModelID, &m.DisplayName, &quota,
			&m.RequestsToday, &m.RequestsMonth); err != nil {
			WriteError(w, http.StatusInternalServerError, TypeServer, "could not read media usage")
			return
		}
		if quota.Valid {
			q := quota.String
			m.QuotaLabel = &q
		}
		totalToday += m.RequestsToday
		totalMonth += m.RequestsMonth
		models = append(models, m)
	}
	if err := rows.Err(); err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not read media usage")
		return
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"modality":           modality,
		"models":             models,
		"totalRequestsToday": totalToday,
		"totalRequestsMonth": totalMonth,
	})
}

type mediaCustomReq struct {
	BaseURL     string `json:"baseUrl"`
	Model       string `json:"model"`
	DisplayName string `json:"displayName"`
	Modality    string `json:"modality"`
	APIKey      string `json:"apiKey"`
	Label       string `json:"label"`
	QuotaLabel  string `json:"quotaLabel"`
}

// handleMediaCreateCustom registers an image/audio/transcription model against
// the operator's own OpenAI-compatible endpoint. Video is not offered here,
// matching the reference's create schema (media.ts:102-113): its providers are
// keyless and catalog-managed. The credential's validity is left to the
// existing key-health pass, the one validation seam.
func (s *Server) handleMediaCreateCustom(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	var req mediaCustomReq
	if !DecodeJSON(w, r, &req) {
		return
	}
	if !mediaCreateModality(req.Modality) {
		WriteError(w, http.StatusBadRequest, TypeInvalidRequest,
			"modality must be image, audio or transcription")
		return
	}
	ctx := r.Context()
	db := s.engine.DB()

	baseURL, ok := emNormalizeBaseURL(req.BaseURL)
	if !ok {
		WriteError(w, http.StatusBadRequest, TypeInvalidRequest, "baseUrl must be a valid URL")
		return
	}
	// A custom endpoint's base URL is stored and every later call carries the
	// credential to it, so it must not point at a link-local or cloud-metadata
	// address. Reuse the one shared SSRF guard rather than a second check.
	if ok, reason := keysCustomAssessURL(baseURL); !ok {
		WriteError(w, http.StatusBadRequest, TypeInvalidRequest, "baseUrl rejected: "+reason)
		return
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		WriteError(w, http.StatusBadRequest, TypeInvalidRequest, "model is required")
		return
	}
	quotaLabel := strings.TrimSpace(req.QuotaLabel)
	if quotaLabel == "" {
		quotaLabel = "custom endpoint"
	}
	submittedName := strings.TrimSpace(req.DisplayName)

	keyID, err := s.emResolveEndpointKey(ctx, baseURL, strings.TrimSpace(req.APIKey), strings.TrimSpace(req.Label))
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not store endpoint credential")
		return
	}

	modelDbID, err := mediaUpsertModel(ctx, db, keyID, model, submittedName, req.Modality, quotaLabel)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not register media model")
		return
	}

	var storedName string
	if err := db.QueryRowContext(ctx,
		"SELECT display_name FROM media_models WHERE id = ?", modelDbID).Scan(&storedName); err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not register media model")
		return
	}

	WriteJSON(w, http.StatusCreated, map[string]any{
		"success":     true,
		"keyId":       keyID,
		"modelDbId":   modelDbID,
		"platform":    "custom",
		"baseUrl":     baseURL,
		"model":       model,
		"displayName": storedName,
		"modality":    req.Modality,
		"maskedKey":   s.emMaskedKey(ctx, keyID),
	})
}

type mediaToggleReq struct {
	Enabled *bool `json:"enabled"`
}

// handleMediaToggle flips one media model's enabled flag. It is the only route
// keyed by a bare {id}: the reference's PUT /api/media/:id.
func (s *Server) handleMediaToggle(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		WriteError(w, http.StatusBadRequest, TypeInvalidRequest, "Invalid id")
		return
	}
	var req mediaToggleReq
	if !DecodeJSON(w, r, &req) {
		return
	}
	if req.Enabled == nil {
		WriteError(w, http.StatusBadRequest, TypeInvalidRequest, "Invalid request body")
		return
	}
	res, err := s.engine.DB().ExecContext(r.Context(),
		"UPDATE media_models SET enabled = ? WHERE id = ?", boolInt(*req.Enabled), id)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not update media model")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		WriteError(w, http.StatusNotFound, TypeNotFound, "Unknown media model "+strconv.FormatInt(id, 10))
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}

// handleMediaDeleteCustom removes a custom media model and reaps its endpoint
// key if nothing else is bound to it.
func (s *Server) handleMediaDeleteCustom(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		WriteError(w, http.StatusBadRequest, TypeInvalidRequest, "Invalid id")
		return
	}
	ctx := r.Context()
	db := s.engine.DB()

	var keyID sql.NullInt64
	err = db.QueryRowContext(ctx,
		"SELECT key_id FROM media_models WHERE id = ? AND platform = 'custom'", id).Scan(&keyID)
	if err == sql.ErrNoRows {
		WriteError(w, http.StatusNotFound, TypeNotFound, "Unknown custom media model "+strconv.FormatInt(id, 10))
		return
	}
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not delete media model")
		return
	}

	if _, err := db.ExecContext(ctx, "DELETE FROM media_models WHERE id = ? AND platform = 'custom'", id); err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not delete media model")
		return
	}
	if keyID.Valid {
		s.emReapEndpointKey(ctx, keyID.Int64)
	}

	WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}

// mediaUpsertModel writes one custom media row, keyed by (platform='custom',
// model_id). A blank name keeps the stored name on an update (#704). A model
// that changes modality is re-ordered into the tail of its new modality's
// chain.
func mediaUpsertModel(ctx context.Context, db *sql.DB, keyID int64, model, submittedName, modality, quotaLabel string) (int64, error) {
	var nameArg any
	if submittedName != "" {
		nameArg = submittedName
	}

	var existingID, existingPriority int64
	var existingModality string
	err := db.QueryRowContext(ctx,
		"SELECT id, modality, priority FROM media_models WHERE platform = 'custom' AND model_id = ? LIMIT 1", model).
		Scan(&existingID, &existingModality, &existingPriority)
	switch {
	case err == nil:
		priority := existingPriority
		if existingModality != modality {
			priority, err = mediaNextPriority(ctx, db, modality)
			if err != nil {
				return 0, err
			}
		}
		if _, err := db.ExecContext(ctx, `
			UPDATE media_models
			   SET display_name = COALESCE(?, display_name), modality = ?, priority = ?,
			       enabled = 1, quota_label = ?, key_id = ?
			 WHERE id = ?`,
			nameArg, modality, priority, quotaLabel, keyID, existingID); err != nil {
			return 0, err
		}
		return existingID, nil
	case err == sql.ErrNoRows:
		priority, err := mediaNextPriority(ctx, db, modality)
		if err != nil {
			return 0, err
		}
		insertName := submittedName
		if insertName == "" {
			insertName = model
		}
		res, err := db.ExecContext(ctx, `
			INSERT INTO media_models
			  (platform, model_id, display_name, modality, priority, enabled, quota_label, key_id)
			VALUES ('custom', ?, ?, ?, ?, 1, ?, ?)`,
			model, insertName, modality, priority, quotaLabel, keyID)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	default:
		return 0, err
	}
}

func mediaNextPriority(ctx context.Context, db *sql.DB, modality string) (int64, error) {
	var maxPriority int64
	err := db.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(priority), 0) FROM media_models WHERE modality = ?", modality).Scan(&maxPriority)
	return maxPriority + 1, err
}

// mediaUsageModality accepts every modality the usage endpoint reports on,
// including the read-only video and transcription rows.
func mediaUsageModality(m string) bool {
	switch m {
	case "image", "video", "audio", "transcription":
		return true
	}
	return false
}

// mediaCreateModality accepts only the modalities a custom endpoint can
// register: video is catalog/keyless-managed and never created here.
func mediaCreateModality(m string) bool {
	switch m {
	case "image", "audio", "transcription":
		return true
	}
	return false
}
