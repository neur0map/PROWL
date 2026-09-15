package api

// The /api/client-profiles family manages per-client inference keys that carry
// a server-enforced system prompt (#411), ported from FreeLLMAPI
// (github.com/tashfeenahmed/freellmapi, MIT, v0.9.9 — see NOTICE.md;
// server/src/routes/client-profiles.ts, server/src/lib/system-prompt.ts). A
// profile key (sk-cp-...) is a second machine credential: it authenticates ONLY
// the inference plane, never this /api surface, which is session gated.
//
// Storage divergence from the reference, deliberately harder: the reference
// keeps a recoverable AES-GCM copy of the key it never serves
// (20260805_000002_client_profiles.ts:19-30) so it can render a mask, then
// includes the table in its backup dumps. This port keeps only the SHA-256
// token_hash (what auth resolves against) and a display mask. The full key is
// returned once from create and rotate and never persisted — the client's own
// contract is "shown once, never fetchable again"
// (client-profiles-section.tsx:33). So a backup can carry no working credential
// even in principle, and a restored profile still authenticates because its
// hash survives. Nothing else may answer with TypeAuthentication, so the
// 400/404 refusals below carry no auth type.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/neur0map/prowl/internal/gateway"
)

const (
	// The sk-cp- prefix is generic (secret-key / client-profile), not a brand,
	// and the inference path short-circuits on it before a DB lookup
	// (system-prompt.ts:11,61), so it MUST stay exactly this for a presented
	// key to resolve.
	clientProfileKeyPrefix = "sk-cp-"
	// 24 random bytes rendered hex, matching mintClientProfileKey
	// (system-prompt.ts:27-29).
	clientProfileKeyHexLen = 48

	maxProfileNameLen = 100
	// Generous ceiling — a system prompt is configuration, not a document
	// (client-profiles.ts:16-18).
	maxProfilePromptLen = 32_000
)

// profileKey is a freshly minted client-profile credential. It reuses the
// unified key's masking discipline (settings_routes.go): the plaintext is
// available only through explicit methods, and MarshalJSON masks by default so
// the secret cannot leak by being embedded in an aggregate response. Unlike the
// unified key it is never stored — only its Hash and Masked forms are — so it
// exists in memory only for the one response that reveals it.
type profileKey string

// Reveal returns the plaintext, for the create/rotate responses that show the
// key exactly once.
func (k profileKey) Reveal() string { return string(k) }

// Hash is the SHA-256 hex digest stored as token_hash and resolved against at
// auth, so the lookup never depends on the secret's bytes
// (system-prompt.ts:35-37).
func (k profileKey) Hash() string {
	sum := sha256.Sum256([]byte(k))
	return hex.EncodeToString(sum[:])
}

// Masked is the display form, first four and last four characters with an
// ellipsis between, reproducing the reference maskKey (crypto.ts maskKey) so
// the dashboard shows the same shape.
func (k profileKey) Masked() string {
	s := string(k)
	switch {
	case len(s) < 5:
		return "****"
	case len(s) <= 8:
		return "****" + s[len(s)-2:]
	default:
		return s[:4] + "..." + s[len(s)-4:]
	}
}

// MarshalJSON masks by default: a profileKey that reaches JSON through any path
// other than a deliberate Reveal is a leak, so the safe serialisation is the
// mask.
func (k profileKey) MarshalJSON() ([]byte, error) {
	return json.Marshal(k.Masked())
}

func mintProfileKey() (profileKey, error) {
	buf := make([]byte, clientProfileKeyHexLen/2)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return profileKey(clientProfileKeyPrefix + hex.EncodeToString(buf)), nil
}

// clientProfileJSON is the masked wire shape of a profile
// (client-profiles.ts:53-63). It never carries the full key; systemPrompt is
// nullable and stays null rather than "".
type clientProfileJSON struct {
	ID           int64   `json:"id"`
	Name         string  `json:"name"`
	MaskedKey    string  `json:"maskedKey"`
	SystemPrompt *string `json:"systemPrompt"`
	Enabled      bool    `json:"enabled"`
	CreatedAt    string  `json:"createdAt"`
	UpdatedAt    string  `json:"updatedAt"`
}

// clientProfileWithKey is the create/rotate response: the profile plus the one
// disclosure of the full key.
type clientProfileWithKey struct {
	clientProfileJSON
	Key string `json:"key"`
}

// ClientProfileAuth is the resolved inference identity behind an sk-cp- key.
// SystemPrompt is nil when the profile injects nothing.
type ClientProfileAuth struct {
	ID           int64
	Name         string
	SystemPrompt *string
}

func (s *Server) registerClientProfilesRoutes() {
	s.mux.HandleFunc("GET /api/client-profiles", s.RequireSession(s.handleClientProfileList))
	s.mux.HandleFunc("POST /api/client-profiles", s.RequireSession(s.handleClientProfileCreate))
	s.mux.HandleFunc("PATCH /api/client-profiles/{id}", s.RequireSession(s.handleClientProfileUpdate))
	s.mux.HandleFunc("POST /api/client-profiles/{id}/rotate", s.RequireSession(s.handleClientProfileRotate))
	s.mux.HandleFunc("DELETE /api/client-profiles/{id}", s.RequireSession(s.handleClientProfileDelete))
}

func (s *Server) handleClientProfileList(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	rows, err := s.engine.DB().QueryContext(r.Context(), `
		SELECT id, name, masked_key, system_prompt, enabled, created_at, updated_at
		  FROM client_profiles
		 ORDER BY id`)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not list client profiles")
		return
	}
	defer rows.Close()

	out := []clientProfileJSON{}
	for rows.Next() {
		p, err := scanClientProfile(rows)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, TypeServer, "could not read a client profile")
			return
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not list client profiles")
		return
	}
	WriteJSON(w, http.StatusOK, out)
}

func (s *Server) handleClientProfileCreate(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	var body struct {
		Name         json.RawMessage `json:"name"`
		SystemPrompt json.RawMessage `json:"systemPrompt"`
	}
	if !DecodeJSON(w, r, &body) {
		return
	}

	name, present, valid := profileName(body.Name)
	prompt, promptOK := profilePrompt(body.SystemPrompt, nil)
	if !present || !valid || !promptOK {
		// The reference answers every create validation failure with the same
		// message (client-profiles.ts:89-92). The prompt is never echoed back:
		// a stored value reflected into an error is an injection vector.
		WriteError(w, http.StatusBadRequest, "", "A profile name is required")
		return
	}

	key, err := mintProfileKey()
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not mint a profile key")
		return
	}
	now := time.Now().Unix()
	res, err := s.engine.DB().ExecContext(r.Context(), `
		INSERT INTO client_profiles
			(name, token_hash, masked_key, system_prompt, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, 1, ?, ?)`,
		name, key.Hash(), key.Masked(), prompt, now, now)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not create the client profile")
		return
	}
	id, err := res.LastInsertId()
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not create the client profile")
		return
	}
	p, _, err := s.loadClientProfile(r.Context(), id)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not create the client profile")
		return
	}
	WriteJSON(w, http.StatusCreated, clientProfileWithKey{clientProfileJSON: p, Key: key.Reveal()})
}

func (s *Server) handleClientProfileUpdate(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	id, ok := profileID(w, r)
	if !ok {
		return
	}
	var body struct {
		Name         json.RawMessage `json:"name"`
		SystemPrompt json.RawMessage `json:"systemPrompt"`
		Enabled      json.RawMessage `json:"enabled"`
	}
	if !DecodeJSON(w, r, &body) {
		return
	}

	current, found, err := s.loadClientProfile(r.Context(), id)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not read the client profile")
		return
	}
	if !found {
		profileNotFound(w)
		return
	}

	name, namePresent, nameValid := profileName(body.Name)
	if !namePresent {
		name = current.Name
	}
	prompt, promptOK := profilePrompt(body.SystemPrompt, current.SystemPrompt)
	enabled, enabledOK := profileEnabled(body.Enabled, current.Enabled)
	if (namePresent && !nameValid) || !promptOK || !enabledOK {
		WriteError(w, http.StatusBadRequest, "", "Invalid profile update")
		return
	}

	if _, err := s.engine.DB().ExecContext(r.Context(), `
		UPDATE client_profiles
		   SET name = ?, system_prompt = ?, enabled = ?, updated_at = ?
		 WHERE id = ?`,
		name, prompt, boolToInt(enabled), time.Now().Unix(), id); err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not update the client profile")
		return
	}
	p, _, err := s.loadClientProfile(r.Context(), id)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not update the client profile")
		return
	}
	WriteJSON(w, http.StatusOK, p)
}

func (s *Server) handleClientProfileRotate(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	id, ok := profileID(w, r)
	if !ok {
		return
	}
	_, found, err := s.loadClientProfile(r.Context(), id)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not read the client profile")
		return
	}
	if !found {
		profileNotFound(w)
		return
	}

	key, err := mintProfileKey()
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not mint a profile key")
		return
	}
	// Overwriting token_hash is what invalidates the old key: auth resolves by
	// hash, so the previous key stops matching on the very next request.
	if _, err := s.engine.DB().ExecContext(r.Context(), `
		UPDATE client_profiles
		   SET token_hash = ?, masked_key = ?, updated_at = ?
		 WHERE id = ?`,
		key.Hash(), key.Masked(), time.Now().Unix(), id); err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not rotate the client profile key")
		return
	}
	p, _, err := s.loadClientProfile(r.Context(), id)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not rotate the client profile key")
		return
	}
	WriteJSON(w, http.StatusOK, clientProfileWithKey{clientProfileJSON: p, Key: key.Reveal()})
}

func (s *Server) handleClientProfileDelete(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	id, ok := profileID(w, r)
	if !ok {
		return
	}
	res, err := s.engine.DB().ExecContext(r.Context(),
		`DELETE FROM client_profiles WHERE id = ?`, id)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not delete the client profile")
		return
	}
	affected, err := res.RowsAffected()
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not delete the client profile")
		return
	}
	if affected == 0 {
		profileNotFound(w)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]bool{"success": true})
}

// resolveClientProfile resolves an inference credential presented as an sk-cp-
// token to its enforced system prompt, or reports not-ok for an unknown,
// disabled, or non-profile token. This is the seam the inference plane wires
// into: the lead calls it after the unified-key check, prepends SystemPrompt to
// the request messages, and rejects a not-ok result exactly like a bad key. It
// looks up by SHA-256 token_hash so the query never depends on the secret's
// bytes, and a disabled profile is rejected like an unknown one so a revoked
// client cannot tell "disabled" from "deleted" (system-prompt.ts:56-70).
func resolveClientProfile(ctx context.Context, db *sql.DB, token string) (ClientProfileAuth, bool, error) {
	if !strings.HasPrefix(token, clientProfileKeyPrefix) {
		return ClientProfileAuth{}, false, nil
	}
	var (
		id      int64
		name    string
		prompt  sql.NullString
		enabled int
	)
	err := db.QueryRowContext(ctx, `
		SELECT id, name, system_prompt, enabled
		  FROM client_profiles
		 WHERE token_hash = ?`, profileKey(token).Hash()).
		Scan(&id, &name, &prompt, &enabled)
	if err == sql.ErrNoRows {
		return ClientProfileAuth{}, false, nil
	}
	if err != nil {
		return ClientProfileAuth{}, false, err
	}
	if enabled == 0 {
		return ClientProfileAuth{}, false, nil
	}
	return ClientProfileAuth{ID: id, Name: name, SystemPrompt: promptOrNil(prompt)}, true, nil
}

// loadClientProfile reads one profile into its masked wire shape.
func (s *Server) loadClientProfile(ctx context.Context, id int64) (clientProfileJSON, bool, error) {
	row := s.engine.DB().QueryRowContext(ctx, `
		SELECT id, name, masked_key, system_prompt, enabled, created_at, updated_at
		  FROM client_profiles
		 WHERE id = ?`, id)
	p, err := scanClientProfile(row)
	if err == sql.ErrNoRows {
		return clientProfileJSON{}, false, nil
	}
	if err != nil {
		return clientProfileJSON{}, false, err
	}
	return p, true, nil
}

// rowScanner is the shared surface of *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanClientProfile(row rowScanner) (clientProfileJSON, error) {
	var (
		p       clientProfileJSON
		prompt  sql.NullString
		enabled int
		created int64
		updated int64
	)
	if err := row.Scan(&p.ID, &p.Name, &p.MaskedKey, &prompt, &enabled, &created, &updated); err != nil {
		return clientProfileJSON{}, err
	}
	p.SystemPrompt = promptOrNil(prompt)
	p.Enabled = enabled == 1
	// Stored as Unix seconds, rendered in the reference's TEXT format because
	// the client treats these as date strings.
	p.CreatedAt = sqliteDateTime(created)
	p.UpdatedAt = sqliteDateTime(updated)
	return p, nil
}

// profileName validates the name field: (value, present, valid). Absent is a
// valid keep on update and a failure on create; a present value must be a
// non-empty trimmed string within the length cap. Name is trimmed on purpose —
// it is a label, not the security-relevant prompt.
func profileName(raw json.RawMessage) (string, bool, bool) {
	if raw == nil {
		return "", false, false
	}
	var v string
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", true, false
	}
	v = strings.TrimSpace(v)
	if v == "" || len(v) > maxProfileNameLen {
		return "", true, false
	}
	return v, true, true
}

// profilePrompt validates the nullable systemPrompt field. Absent keeps the
// current value; explicit null clears it; a string over the cap is rejected.
// The stored value is byte-for-byte what was sent — no trimming of content — so
// the enforced prompt round-trips exactly; only an all-whitespace value is
// treated as "no prompt", the same boundary the reference draws with `|| null`
// (client-profiles.ts:97), without mutating a meaningful value.
func profilePrompt(raw json.RawMessage, keep *string) (*string, bool) {
	if raw == nil {
		return keep, true
	}
	if string(raw) == "null" {
		return nil, true
	}
	var v string
	if err := json.Unmarshal(raw, &v); err != nil || len(v) > maxProfilePromptLen {
		return nil, false
	}
	if strings.TrimSpace(v) == "" {
		return nil, true
	}
	return &v, true
}

// profileEnabled validates the boolean enabled field. Absent keeps current.
func profileEnabled(raw json.RawMessage, keep bool) (bool, bool) {
	if raw == nil {
		return keep, true
	}
	var v bool
	if err := json.Unmarshal(raw, &v); err != nil {
		return false, false
	}
	return v, true
}

// promptOrNil maps a stored prompt to a nil *string when it is NULL or blank,
// so a profile that injects nothing serialises and resolves as null rather than
// "" (system-prompt.ts:66-68).
func promptOrNil(v sql.NullString) *string {
	if !v.Valid || strings.TrimSpace(v.String) == "" {
		return nil
	}
	s := v.String
	return &s
}

func profileID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		WriteError(w, http.StatusBadRequest, "", "Invalid profile id")
		return 0, false
	}
	return id, true
}

func profileNotFound(w http.ResponseWriter) {
	WriteError(w, http.StatusNotFound, "", "Client profile not found")
}
