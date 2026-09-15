package api

import (
	"context"
	"database/sql"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/neur0map/prowl/internal/gateway"
)

// Embedding models are a separate surface from chat models, backing the
// dashboard's Embeddings tab. They live in their own table so a chat request
// can never misroute into one; this file is the management surface (list,
// per-family usage, reorder/enable, custom create/delete), not the routing
// plane, which does not run embedding traffic yet.

// emNoKey is the sentinel stored for a keyless custom endpoint, matching the
// reference's 'no-key' placeholder so one endpoint keeps exactly one row.
const emNoKey = "no-key"

// settingEmbeddingsDefaultFamily is read and written directly against the
// generic settings table. The typed settingsStore in settings_routes.go gates
// keys through a spec whitelist owned by that file; this slice's key is not in
// it, so this surface uses the same key/value table the reference's
// getSetting/setSetting do, without reaching across file ownership.
const settingEmbeddingsDefaultFamily = "embeddings_default_family"

// defaultEmbeddingFamily is the fallback when the operator has not chosen one,
// matching the reference default (embeddings.ts:54-56).
const defaultEmbeddingFamily = "gemini-embedding-001"

func (s *Server) registerEmbeddingsRoutes() {
	s.mux.HandleFunc("GET /api/embeddings", s.RequireSession(s.handleEmbeddingsList))
	s.mux.HandleFunc("PUT /api/embeddings", s.RequireSession(s.handleEmbeddingsUpdate))
	s.mux.HandleFunc("GET /api/embeddings/usage", s.RequireSession(s.handleEmbeddingsUsage))
	s.mux.HandleFunc("POST /api/embeddings/custom", s.RequireSession(s.handleEmbeddingsCreateCustom))
	s.mux.HandleFunc("DELETE /api/embeddings/custom/{id}", s.RequireSession(s.handleEmbeddingsDeleteCustom))
}

type embProviderOut struct {
	ID          int64  `json:"id"`
	Platform    string `json:"platform"`
	ModelID     string `json:"modelId"`
	DisplayName string `json:"displayName"`
	Priority    int64  `json:"priority"`
	Enabled     bool   `json:"enabled"`
	QuotaLabel  string `json:"quotaLabel"`
	KeyCount    int    `json:"keyCount"`
	IsCustom    bool   `json:"isCustom"`
}

type embFamilyOut struct {
	Family         string           `json:"family"`
	Dimensions     *int64           `json:"dimensions"`
	MaxInputTokens *int64           `json:"maxInputTokens"`
	IsDefault      bool             `json:"isDefault"`
	Providers      []embProviderOut `json:"providers"`
}

// handleEmbeddingsList groups the embedding rows by family, one entry per
// family with its provider chain in priority order -- the shape the Embeddings
// tab renders. An empty install answers with a well-formed empty families list
// rather than null so the client can map over it.
func (s *Server) handleEmbeddingsList(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	ctx := r.Context()
	db := s.engine.DB()

	keyCounts := keyCountsByPlatform(db, true)
	customHealthy := emCustomHealthyKeyIDs(ctx, db)

	rows, err := db.QueryContext(ctx, `
		SELECT id, family, platform, model_id, display_name, dimensions,
		       max_input_tokens, priority, enabled, quota_label, key_id
		  FROM embedding_models
		 ORDER BY family, priority`)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not list embedding models")
		return
	}
	defer rows.Close()

	order := make([]string, 0)
	byFamily := map[string]*embFamilyOut{}
	for rows.Next() {
		var (
			id, priority   int64
			family         string
			platform       string
			modelID        string
			displayName    string
			dims, maxInput sql.NullInt64
			enabled        int
			quotaLabel     string
			keyID          sql.NullInt64
		)
		if err := rows.Scan(&id, &family, &platform, &modelID, &displayName, &dims,
			&maxInput, &priority, &enabled, &quotaLabel, &keyID); err != nil {
			WriteError(w, http.StatusInternalServerError, TypeServer, "could not list embedding models")
			return
		}
		fam, ok := byFamily[family]
		if !ok {
			// The first row of a family (lowest priority) carries the family's
			// dimension and input ceiling, exactly as the reference reads
			// rows[0] (embeddings.ts:43-45).
			fam = &embFamilyOut{
				Family:         family,
				Dimensions:     nullInt64Ptr(dims),
				MaxInputTokens: nullInt64Ptr(maxInput),
			}
			byFamily[family] = fam
			order = append(order, family)
		}
		fam.Providers = append(fam.Providers, embProviderOut{
			ID:          id,
			Platform:    platform,
			ModelID:     modelID,
			DisplayName: displayName,
			Priority:    priority,
			Enabled:     enabled != 0,
			QuotaLabel:  quotaLabel,
			KeyCount:    emKeyCount(platform, keyID, keyCounts, customHealthy),
			IsCustom:    platform == "custom",
		})
	}
	if err := rows.Err(); err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not list embedding models")
		return
	}

	defaultFamily := emDefaultFamily(ctx, db)
	families := make([]embFamilyOut, 0, len(order))
	for _, name := range order {
		fam := byFamily[name]
		fam.IsDefault = name == defaultFamily
		families = append(families, *fam)
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"defaultFamily": defaultFamily,
		"families":      families,
	})
}

type embUpdateReq struct {
	DefaultFamily *string `json:"defaultFamily"`
	Providers     *[]struct {
		ID       int64 `json:"id"`
		Priority int64 `json:"priority"`
		Enabled  bool  `json:"enabled"`
	} `json:"providers"`
}

// handleEmbeddingsUpdate sets the default family and/or reorders and toggles a
// family's providers -- the Embeddings tab's only mutations besides custom
// create/delete.
func (s *Server) handleEmbeddingsUpdate(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	var req embUpdateReq
	if !DecodeJSON(w, r, &req) {
		return
	}
	ctx := r.Context()
	db := s.engine.DB()

	if req.DefaultFamily != nil && strings.TrimSpace(*req.DefaultFamily) != "" {
		family := strings.TrimSpace(*req.DefaultFamily)
		var exists int
		err := db.QueryRowContext(ctx, "SELECT 1 FROM embedding_models WHERE family = ? LIMIT 1", family).Scan(&exists)
		if err == sql.ErrNoRows {
			WriteError(w, http.StatusBadRequest, TypeInvalidRequest, "Unknown family '"+family+"'")
			return
		}
		if err != nil {
			WriteError(w, http.StatusInternalServerError, TypeServer, "could not update embeddings")
			return
		}
		if err := emSetDefaultFamily(ctx, db, family); err != nil {
			WriteError(w, http.StatusInternalServerError, TypeServer, "could not update embeddings")
			return
		}
	}

	if req.Providers != nil {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, TypeServer, "could not update embeddings")
			return
		}
		defer func() { _ = tx.Rollback() }()
		for _, p := range *req.Providers {
			if _, err := tx.ExecContext(ctx,
				"UPDATE embedding_models SET priority = ?, enabled = ? WHERE id = ?",
				p.Priority, boolInt(p.Enabled), p.ID); err != nil {
				WriteError(w, http.StatusInternalServerError, TypeServer, "could not update embeddings")
				return
			}
		}
		if err := tx.Commit(); err != nil {
			WriteError(w, http.StatusInternalServerError, TypeServer, "could not update embeddings")
			return
		}
	}

	WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}

type embUsageFamily struct {
	Family        string  `json:"family"`
	RequestsToday int64   `json:"requestsToday"`
	TokensMonth   int64   `json:"tokensMonth"`
	Platform      *string `json:"platform"`
	QuotaLabel    *string `json:"quotaLabel"`
}

// handleEmbeddingsUsage reports per-family spend from the request trail:
// requests today and tokens this calendar month, attributed by the model's
// (platform, model_id). A family with no traffic reports zeroes, not an empty
// object; there is deliberately no budget denominator because an embedding
// quota label ("10K neurons/day", "$0.10/mo credits") has no honest conversion
// to tokens (embeddings.ts:237-245).
func (s *Server) handleEmbeddingsUsage(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	ctx := r.Context()
	db := s.engine.DB()
	startOfDay, startOfMonth := emDayMonthStart()

	// Read the per-family legend BEFORE the streaming usage query below: the
	// store caps the pool at one connection, so opening a second query while
	// the usage rows are still open would deadlock.
	meta := emRepresentativeProviders(ctx, db)

	rows, err := db.QueryContext(ctx, `
		SELECT em.family,
		       COALESCE(SUM(CASE WHEN r.created_at >= ? THEN 1 ELSE 0 END), 0) AS requests_today,
		       COALESCE(SUM(CASE WHEN r.created_at >= ? THEN r.input_tokens ELSE 0 END), 0) AS tokens_month
		  FROM embedding_models em
		  LEFT JOIN requests r
		    ON r.outcome = 'success'
		   AND r.platform = em.platform
		   AND r.model_id = em.model_id
		   AND r.created_at >= ?
		 GROUP BY em.family`, startOfDay, startOfMonth, startOfMonth)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not read embedding usage")
		return
	}
	defer rows.Close()

	families := make([]embUsageFamily, 0)
	var totalTokens, totalRequests int64
	for rows.Next() {
		var f embUsageFamily
		if err := rows.Scan(&f.Family, &f.RequestsToday, &f.TokensMonth); err != nil {
			WriteError(w, http.StatusInternalServerError, TypeServer, "could not read embedding usage")
			return
		}
		if m, ok := meta[f.Family]; ok {
			p := m.platform
			f.Platform = &p
			f.QuotaLabel = m.quotaLabel
		}
		totalTokens += f.TokensMonth
		totalRequests += f.RequestsToday
		families = append(families, f)
	}
	if err := rows.Err(); err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not read embedding usage")
		return
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"families":           families,
		"totalTokensMonth":   totalTokens,
		"totalRequestsToday": totalRequests,
	})
}

type embCustomReq struct {
	BaseURL        string `json:"baseUrl"`
	Model          string `json:"model"`
	DisplayName    string `json:"displayName"`
	Family         string `json:"family"`
	APIKey         string `json:"apiKey"`
	Label          string `json:"label"`
	QuotaLabel     string `json:"quotaLabel"`
	MaxInputTokens *int64 `json:"maxInputTokens"`
	// Dimensions is accepted if a caller supplies it, but the current client
	// does not: Prowl cannot probe it (see the migration comment) and stores
	// NULL, which the tab renders as "-".
	Dimensions *int64 `json:"dimensions"`
}

// handleEmbeddingsCreateCustom registers an embedding model against the
// operator's own OpenAI-compatible endpoint. Unlike the reference it runs no
// inline dimension probe -- that live HTTP path is out of scope for the
// management surface, and inventing a second SSRF/HTTP client was explicitly
// declined. The credential's validity is left to the existing key-health pass
// (KeyVault.CheckKey), the one validation seam; the dimension degrades honestly
// to NULL rather than a fabricated number.
func (s *Server) handleEmbeddingsCreateCustom(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	var req embCustomReq
	if !DecodeJSON(w, r, &req) {
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
	family := strings.TrimSpace(req.Family)
	if family == "" {
		family = model
	}
	quotaLabel := strings.TrimSpace(req.QuotaLabel)
	if quotaLabel == "" {
		quotaLabel = "custom endpoint"
	}
	// A blank name means "no opinion": a new model takes its id, an existing
	// one keeps its stored name (#704).
	submittedName := strings.TrimSpace(req.DisplayName)

	// Vectors from mismatched spaces must never mix, so a family already on
	// record at a different KNOWN dimension is refused (embeddings service
	// registerCustomEmbeddingModel). Only enforced when a dimension is
	// supplied; without one there is nothing to conflict with.
	if req.Dimensions != nil {
		var sibling sql.NullInt64
		err := db.QueryRowContext(ctx, `
			SELECT dimensions FROM embedding_models
			 WHERE family = ? AND NOT (platform = 'custom' AND model_id = ?)
			   AND dimensions IS NOT NULL
			 LIMIT 1`, family, model).Scan(&sibling)
		if err != nil && err != sql.ErrNoRows {
			WriteError(w, http.StatusInternalServerError, TypeServer, "could not register embedding model")
			return
		}
		if sibling.Valid && sibling.Int64 != *req.Dimensions {
			WriteError(w, http.StatusBadRequest, TypeInvalidRequest,
				"Embedding family '"+family+"' is "+strconv.FormatInt(sibling.Int64, 10)+
					" dimensions, but '"+model+"' returned "+strconv.FormatInt(*req.Dimensions, 10)+
					". Use a new family name.")
			return
		}
	}

	keyID, err := s.emResolveEndpointKey(ctx, baseURL, strings.TrimSpace(req.APIKey), strings.TrimSpace(req.Label))
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not store endpoint credential")
		return
	}

	modelDbID, err := emUpsertEmbeddingModel(ctx, db, keyID, family, model, submittedName, req.Dimensions, req.MaxInputTokens, quotaLabel)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not register embedding model")
		return
	}

	var storedName string
	var storedDims sql.NullInt64
	if err := db.QueryRowContext(ctx,
		"SELECT display_name, dimensions FROM embedding_models WHERE id = ?", modelDbID).
		Scan(&storedName, &storedDims); err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not register embedding model")
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
		"family":      family,
		"dimensions":  nullInt64Ptr(storedDims),
		"maskedKey":   s.emMaskedKey(ctx, keyID),
	})
}

// handleEmbeddingsDeleteCustom removes a custom embedding model, reaps its
// endpoint key if nothing else is bound to it, and reassigns the default family
// when the deleted family was the default.
func (s *Server) handleEmbeddingsDeleteCustom(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		WriteError(w, http.StatusBadRequest, TypeInvalidRequest, "Invalid id")
		return
	}
	ctx := r.Context()
	db := s.engine.DB()

	var family string
	var keyID sql.NullInt64
	err = db.QueryRowContext(ctx,
		"SELECT family, key_id FROM embedding_models WHERE id = ? AND platform = 'custom'", id).
		Scan(&family, &keyID)
	if err == sql.ErrNoRows {
		WriteError(w, http.StatusNotFound, TypeNotFound, "Unknown custom embedding model "+strconv.FormatInt(id, 10))
		return
	}
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not delete embedding model")
		return
	}

	if _, err := db.ExecContext(ctx, "DELETE FROM embedding_models WHERE id = ? AND platform = 'custom'", id); err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not delete embedding model")
		return
	}
	if keyID.Valid {
		s.emReapEndpointKey(ctx, keyID.Int64)
	}

	if emDefaultFamily(ctx, db) == family {
		var replacement string
		err := db.QueryRowContext(ctx,
			"SELECT family FROM embedding_models ORDER BY family, priority LIMIT 1").Scan(&replacement)
		if err == nil {
			_ = emSetDefaultFamily(ctx, db, replacement)
		}
	}

	WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}

// emUpsertEmbeddingModel writes one custom embedding row, keyed by
// (platform='custom', model_id). A blank name or dimension keeps the stored
// value on an update rather than wiping it (#704), so a resubmit that only
// left a field blank is not destructive.
func emUpsertEmbeddingModel(ctx context.Context, db *sql.DB, keyID int64, family, model, submittedName string, dims, maxInput *int64, quotaLabel string) (int64, error) {
	var nameArg any
	if submittedName != "" {
		nameArg = submittedName
	}
	var dimsArg any
	if dims != nil {
		dimsArg = *dims
	}
	var maxArg any
	if maxInput != nil {
		maxArg = *maxInput
	}

	var existingID, existingPriority int64
	err := db.QueryRowContext(ctx,
		"SELECT id, priority FROM embedding_models WHERE platform = 'custom' AND model_id = ? LIMIT 1", model).
		Scan(&existingID, &existingPriority)
	switch {
	case err == nil:
		if _, err := db.ExecContext(ctx, `
			UPDATE embedding_models
			   SET family = ?, display_name = COALESCE(?, display_name),
			       dimensions = COALESCE(?, dimensions),
			       max_input_tokens = COALESCE(?, max_input_tokens),
			       priority = ?, enabled = 1, quota_label = ?, key_id = ?
			 WHERE id = ?`,
			family, nameArg, dimsArg, maxArg, existingPriority, quotaLabel, keyID, existingID); err != nil {
			return 0, err
		}
		return existingID, nil
	case err == sql.ErrNoRows:
		var maxPriority int64
		if err := db.QueryRowContext(ctx,
			"SELECT COALESCE(MAX(priority), 0) FROM embedding_models WHERE family = ?", family).
			Scan(&maxPriority); err != nil {
			return 0, err
		}
		insertName := submittedName
		if insertName == "" {
			insertName = model
		}
		res, err := db.ExecContext(ctx, `
			INSERT INTO embedding_models
			  (family, platform, model_id, display_name, dimensions, max_input_tokens, priority, enabled, quota_label, key_id)
			VALUES (?, 'custom', ?, ?, ?, ?, ?, 1, ?, ?)`,
			family, model, insertName, dimsArg, maxArg, maxPriority+1, quotaLabel, keyID)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	default:
		return 0, err
	}
}

// ── shared custom-endpoint helpers (used by media_routes.go too) ─────────────

// emResolveEndpointKey finds or creates the api_keys row for a custom endpoint
// and returns its id. A resubmitted secret reuses its row; a genuinely new
// secret is stored as an ADDITIONAL credential for the endpoint rather than
// replacing the one already there (#619). A create with no key stores the
// 'no-key' sentinel so a keyless relay still gets exactly one row. The
// plaintext never leaves this function.
func (s *Server) emResolveEndpointKey(ctx context.Context, baseURL, providedKey, label string) (int64, error) {
	db := s.engine.DB()
	rows, err := db.QueryContext(ctx,
		"SELECT id FROM api_keys WHERE platform = 'custom' AND base_url = ? ORDER BY id", baseURL)
	if err != nil {
		return 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	vault := s.engine.Vault()
	if providedKey != "" {
		for _, id := range ids {
			if secret, err := vault.Reveal(ctx, id); err == nil && secret == providedKey {
				return id, nil
			}
		}
		return vault.Add("custom", providedKey, gateway.AddOptions{BaseURL: baseURL, Label: label})
	}
	if len(ids) > 0 {
		return ids[0], nil
	}
	return vault.Add("custom", emNoKey, gateway.AddOptions{BaseURL: baseURL, Label: label})
}

// emReapEndpointKey drops a custom endpoint's key row once nothing across the
// chat, embedding and media tables is bound to it, so deleting the last model
// on an endpoint does not strand its credential.
func (s *Server) emReapEndpointKey(ctx context.Context, keyID int64) {
	db := s.engine.DB()
	var n int
	err := db.QueryRowContext(ctx, `
		SELECT
		  (SELECT COUNT(*) FROM models           WHERE platform = 'custom' AND key_id = ?) +
		  (SELECT COUNT(*) FROM embedding_models WHERE platform = 'custom' AND key_id = ?) +
		  (SELECT COUNT(*) FROM media_models     WHERE platform = 'custom' AND key_id = ?)`,
		keyID, keyID, keyID).Scan(&n)
	if err != nil || n > 0 {
		return
	}
	_ = s.engine.Vault().Delete(ctx, keyID)
}

// emMaskedKey returns the masked form of a key row through the vault, so a
// caller never handles the plaintext to display it.
func (s *Server) emMaskedKey(ctx context.Context, keyID int64) string {
	row, ok, err := s.engine.Vault().Get(ctx, keyID)
	if err != nil || !ok {
		return ""
	}
	return row.Masked
}

// emKeyCount reports how many usable keys back a provider row: for a custom row
// bound to a key, 1 when that key is enabled and not errored, else 0; for a
// built-in platform, the count of its usable keys.
func emKeyCount(platform string, keyID sql.NullInt64, keyCounts map[string]int, customHealthy map[int64]bool) int {
	if platform == "custom" && keyID.Valid {
		if customHealthy[keyID.Int64] {
			return 1
		}
		return 0
	}
	return keyCounts[platform]
}

// emCustomHealthyKeyIDs is the set of custom key ids that are enabled and not
// errored, matching the reference's status IN ('healthy','unknown') filter.
func emCustomHealthyKeyIDs(ctx context.Context, db *sql.DB) map[int64]bool {
	out := map[int64]bool{}
	rows, err := db.QueryContext(ctx,
		"SELECT id FROM api_keys WHERE platform = 'custom' AND enabled = 1 AND status IN ('healthy', 'unknown')")
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err == nil {
			out[id] = true
		}
	}
	return out
}

type emProviderMeta struct {
	platform   string
	quotaLabel *string
}

// emRepresentativeProviders picks one provider per family for the usage
// legend: the highest-priority enabled row, so the label matches whoever serves
// the family first (embeddings.ts:263-272).
func emRepresentativeProviders(ctx context.Context, db *sql.DB) map[string]emProviderMeta {
	out := map[string]emProviderMeta{}
	rows, err := db.QueryContext(ctx,
		"SELECT family, platform, quota_label FROM embedding_models WHERE enabled = 1 ORDER BY priority ASC")
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var family, platform string
		var quota sql.NullString
		if err := rows.Scan(&family, &platform, &quota); err != nil {
			continue
		}
		if _, seen := out[family]; seen {
			continue
		}
		meta := emProviderMeta{platform: platform}
		if quota.Valid {
			q := quota.String
			meta.quotaLabel = &q
		}
		out[family] = meta
	}
	return out
}

// emDefaultFamily reads the operator's chosen default family from the settings
// table, falling back to the reference default when unset.
func emDefaultFamily(ctx context.Context, db *sql.DB) string {
	var value string
	err := db.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = ?", settingEmbeddingsDefaultFamily).Scan(&value)
	if err != nil || strings.TrimSpace(value) == "" {
		return defaultEmbeddingFamily
	}
	return value
}

func emSetDefaultFamily(ctx context.Context, db *sql.DB, family string) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO settings(key, value, updated_at) VALUES(?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		settingEmbeddingsDefaultFamily, family, time.Now().Unix())
	return err
}

// emDayMonthStart returns the Unix-second boundaries the usage queries count
// from: the start of today and the start of this calendar month, both UTC to
// match how the request trail stores created_at.
func emDayMonthStart() (int64, int64) {
	now := time.Now().UTC()
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).Unix()
	startOfMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).Unix()
	return startOfDay, startOfMonth
}

// emNormalizeBaseURL trims a base URL, strips trailing slashes and confirms it
// is an absolute http(s) URL, the same shape the reference's zod .url()
// accepts.
func emNormalizeBaseURL(raw string) (string, bool) {
	trimmed := strings.TrimRight(strings.TrimSpace(raw), "/")
	if trimmed == "" {
		return "", false
	}
	u, err := url.Parse(trimmed)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", false
	}
	return trimmed, true
}
