package api

// The /api/conversations family backs the Playground page, ported from
// FreeLLMAPI (github.com/tashfeenahmed/freellmapi, MIT, v0.9.9 — see NOTICE.md;
// server/src/routes/conversations.ts). The page is the only client: it lists
// conversation summaries in a sidebar (never the bodies), loads one transcript
// when you switch to it, and PUTs the whole transcript back after each
// exchange.
//
// The transcript is the client's own ChatMessage[] stored verbatim as a JSON
// blob (spec-persistence.md §1.26 calls it "deliberately unnormalized"), so a
// restored conversation renders identically to the live one — routing meta,
// reasoning and image thumbnails included. Validation is shape-checking, not
// rewriting: the array and each message's role are checked, but the element
// bytes are preserved so a field the server does not model still survives.
//
// Every route is session gated. Only RequireSession may answer with
// TypeAuthentication, so the 400/404/413 refusals below carry no auth type and
// never sign the operator out.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/neur0map/prowl/internal/gateway"
)

const (
	maxConversationTitleLen  = 200
	maxConversationModelLen  = 200
	maxConversationPromptLen = 32_000

	// A conversation is prose plus, occasionally, inlined image data URIs. 2 MB
	// of serialised JSON is far more than any readable chat and still small
	// enough to hand back on every switch; past it the write is refused rather
	// than someone's history silently truncated (conversations.ts:19-24).
	maxConversationBytes = 2 * 1024 * 1024

	// The client renders this type verbatim; the value is the reference's
	// (conversations.ts:150-160, spec-api.md §3.1). It is not TypeAuthentication,
	// so a full transcript never signs the operator out.
	typeConversationTooLarge ErrorType = "conversation_too_large"
)

func (s *Server) registerConversationsRoutes() {
	s.mux.HandleFunc("GET /api/conversations", s.RequireSession(s.handleConversationList))
	s.mux.HandleFunc("GET /api/conversations/{id}", s.RequireSession(s.handleConversationGet))
	s.mux.HandleFunc("POST /api/conversations", s.RequireSession(s.handleConversationCreate))
	s.mux.HandleFunc("PUT /api/conversations/{id}", s.RequireSession(s.handleConversationUpdate))
	s.mux.HandleFunc("DELETE /api/conversations/{id}", s.RequireSession(s.handleConversationDelete))
}

// conversationSummary is the sidebar row: enough to list a conversation, never
// its transcript, so the list stays small no matter how long the conversations
// grow. model is nullable and must stay null rather than becoming "" — the
// picker distinguishes "no model pinned" from a real id.
type conversationSummary struct {
	ID           int64   `json:"id"`
	Title        string  `json:"title"`
	Model        *string `json:"model"`
	MessageCount int64   `json:"messageCount"`
	CreatedAt    int64   `json:"createdAt"`
	UpdatedAt    int64   `json:"updatedAt"`
}

// conversationFull is a conversation with its transcript, as GET /:id returns
// it. Messages is the stored blob passed through untouched.
type conversationFull struct {
	ID           int64           `json:"id"`
	Title        string          `json:"title"`
	Messages     json.RawMessage `json:"messages"`
	Model        *string         `json:"model"`
	SystemPrompt *string         `json:"systemPrompt"`
	CreatedAt    int64           `json:"createdAt"`
	UpdatedAt    int64           `json:"updatedAt"`
}

// conversationRequest decodes create and update bodies. Each field is a
// RawMessage so an absent field (nil) is distinguishable from an explicit null:
// on PUT an omitted field keeps its stored value, whereas an explicit null
// clears it. A plain *string could not tell the two apart.
type conversationRequest struct {
	Title        json.RawMessage `json:"title"`
	Messages     json.RawMessage `json:"messages"`
	Model        json.RawMessage `json:"model"`
	SystemPrompt json.RawMessage `json:"systemPrompt"`
}

func (s *Server) handleConversationList(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	// json_array_length reads the message count straight out of the stored blob
	// (SQLite's built-in JSON1) instead of parsing every transcript to count.
	rows, err := s.engine.DB().QueryContext(r.Context(), `
		SELECT id, title, model,
		       json_array_length(messages_json) AS message_count,
		       created_at_ms, updated_at_ms
		  FROM playground_conversations
		 ORDER BY updated_at_ms DESC, id DESC`)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not list conversations")
		return
	}
	defer rows.Close()

	out := []conversationSummary{}
	for rows.Next() {
		var (
			sum   conversationSummary
			model sql.NullString
			count sql.NullInt64
		)
		if err := rows.Scan(&sum.ID, &sum.Title, &model, &count, &sum.CreatedAt, &sum.UpdatedAt); err != nil {
			WriteError(w, http.StatusInternalServerError, TypeServer, "could not read a conversation")
			return
		}
		sum.Model = nullableString(model)
		sum.MessageCount = count.Int64
		out = append(out, sum)
	}
	if err := rows.Err(); err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not list conversations")
		return
	}
	WriteJSON(w, http.StatusOK, out)
}

func (s *Server) handleConversationGet(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	id, ok := conversationID(w, r)
	if !ok {
		return
	}
	conv, found, err := s.loadConversation(r.Context(), id)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not read the conversation")
		return
	}
	if !found {
		conversationNotFound(w)
		return
	}
	WriteJSON(w, http.StatusOK, conv)
}

func (s *Server) handleConversationCreate(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	var body conversationRequest
	if !DecodeJSON(w, r, &body) {
		return
	}

	title, ok := conversationTitle(w, body.Title, "")
	if !ok {
		return
	}
	model, ok := conversationNullableText(w, body.Model, maxConversationModelLen, nil)
	if !ok {
		return
	}
	prompt, ok := conversationNullableText(w, body.SystemPrompt, maxConversationPromptLen, nil)
	if !ok {
		return
	}
	messages, ok := conversationMessages(w, body.Messages, jsonEmptyArray)
	if !ok {
		return
	}

	now := time.Now().UnixMilli()
	res, err := s.engine.DB().ExecContext(r.Context(), `
		INSERT INTO playground_conversations
			(title, messages_json, model, system_prompt, created_at_ms, updated_at_ms)
		VALUES (?, ?, ?, ?, ?, ?)`,
		title, messages, model, prompt, now, now)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not save the conversation")
		return
	}
	id, err := res.LastInsertId()
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not save the conversation")
		return
	}
	conv, _, err := s.loadConversation(r.Context(), id)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not save the conversation")
		return
	}
	WriteJSON(w, http.StatusCreated, conv)
}

func (s *Server) handleConversationUpdate(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	id, ok := conversationID(w, r)
	if !ok {
		return
	}
	var body conversationRequest
	if !DecodeJSON(w, r, &body) {
		return
	}

	current, found, err := s.loadConversation(r.Context(), id)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not read the conversation")
		return
	}
	if !found {
		conversationNotFound(w)
		return
	}

	// PUT is a full upsert of the mutable state, but an omitted field keeps its
	// stored value: a rename does not have to re-send the transcript and cannot
	// race one away (conversations.ts:71-79).
	title, ok := conversationTitle(w, body.Title, current.Title)
	if !ok {
		return
	}
	model, ok := conversationNullableText(w, body.Model, maxConversationModelLen, current.Model)
	if !ok {
		return
	}
	prompt, ok := conversationNullableText(w, body.SystemPrompt, maxConversationPromptLen, current.SystemPrompt)
	if !ok {
		return
	}
	messages, ok := conversationMessages(w, body.Messages, string(current.Messages))
	if !ok {
		return
	}

	// One statement: title, transcript, model and system prompt always move
	// together, so a save can never leave a row half updated — the transcript
	// of one exchange under the title of another (conversations.ts:245-254).
	if _, err := s.engine.DB().ExecContext(r.Context(), `
		UPDATE playground_conversations
		   SET title = ?, messages_json = ?, model = ?, system_prompt = ?, updated_at_ms = ?
		 WHERE id = ?`,
		title, messages, model, prompt, time.Now().UnixMilli(), id); err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not save the conversation")
		return
	}
	conv, _, err := s.loadConversation(r.Context(), id)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not save the conversation")
		return
	}
	WriteJSON(w, http.StatusOK, conv)
}

func (s *Server) handleConversationDelete(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	id, ok := conversationID(w, r)
	if !ok {
		return
	}
	res, err := s.engine.DB().ExecContext(r.Context(),
		`DELETE FROM playground_conversations WHERE id = ?`, id)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not delete the conversation")
		return
	}
	affected, err := res.RowsAffected()
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not delete the conversation")
		return
	}
	if affected == 0 {
		conversationNotFound(w)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]bool{"success": true})
}

// jsonEmptyArray is the stored form of a conversation with no messages.
const jsonEmptyArray = "[]"

// loadConversation reads one row into its wire shape. A stored blob that is not
// valid JSON — only possible from tampering outside this router — is handed
// back as an empty transcript rather than 500-ing the whole page
// (conversations.ts:100-113).
func (s *Server) loadConversation(ctx context.Context, id int64) (conversationFull, bool, error) {
	var (
		conv     conversationFull
		messages string
		model    sql.NullString
		prompt   sql.NullString
	)
	err := s.engine.DB().QueryRowContext(ctx, `
		SELECT id, title, messages_json, model, system_prompt, created_at_ms, updated_at_ms
		  FROM playground_conversations
		 WHERE id = ?`, id).
		Scan(&conv.ID, &conv.Title, &messages, &model, &prompt, &conv.CreatedAt, &conv.UpdatedAt)
	if err == sql.ErrNoRows {
		return conversationFull{}, false, nil
	}
	if err != nil {
		return conversationFull{}, false, err
	}
	if !json.Valid([]byte(messages)) {
		messages = jsonEmptyArray
	}
	conv.Messages = json.RawMessage(messages)
	conv.Model = nullableString(model)
	conv.SystemPrompt = nullableString(prompt)
	return conv, true, nil
}

// conversationID reads and validates the {id} path segment.
func conversationID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		// No type: this is a validation refusal, not an auth failure
		// (conversations.ts:130-137).
		WriteError(w, http.StatusBadRequest, "", "Invalid conversation id")
		return 0, false
	}
	return id, true
}

// conversationTitle validates an optional title. Absent keeps the fallback; a
// present value must be a string within the length cap. Title is not nullable
// in the reference schema, so an explicit null is rejected.
func conversationTitle(w http.ResponseWriter, raw json.RawMessage, fallback string) (string, bool) {
	if raw == nil {
		return fallback, true
	}
	var title string
	if err := json.Unmarshal(raw, &title); err != nil || len(title) > maxConversationTitleLen {
		conversationInvalid(w)
		return "", false
	}
	return title, true
}

// conversationNullableText validates an optional, nullable string field (model
// or systemPrompt). Absent keeps the fallback; explicit null clears it; a
// string is bounded by max. The value is stored verbatim — no trimming — so it
// round-trips exactly.
func conversationNullableText(w http.ResponseWriter, raw json.RawMessage, max int, fallback *string) (*string, bool) {
	if raw == nil {
		return fallback, true
	}
	if string(raw) == "null" {
		return nil, true
	}
	var v string
	if err := json.Unmarshal(raw, &v); err != nil || len(v) > max {
		conversationInvalid(w)
		return nil, false
	}
	return &v, true
}

// conversationMessages validates the transcript and returns its canonical
// stored form. Absent keeps the fallback blob. A present value must be a JSON
// array whose every element is an object carrying a user/assistant role; the
// element bytes are preserved so nothing the page draws is stripped. The
// re-serialised array is refused past the storage cap with the reference's 413.
func conversationMessages(w http.ResponseWriter, raw json.RawMessage, fallback string) (string, bool) {
	if raw == nil {
		return fallback, true
	}
	var msgs []json.RawMessage
	if err := json.Unmarshal(raw, &msgs); err != nil {
		conversationInvalid(w)
		return "", false
	}
	for _, m := range msgs {
		var shape struct {
			Role string `json:"role"`
		}
		if err := json.Unmarshal(m, &shape); err != nil {
			conversationInvalid(w)
			return "", false
		}
		if shape.Role != "user" && shape.Role != "assistant" {
			conversationInvalid(w)
			return "", false
		}
	}
	// Re-marshal to a compact array whose elements are untouched: this is the
	// size we actually store, and it is deterministic regardless of the
	// client's whitespace.
	canon, err := json.Marshal(msgs)
	if err != nil {
		conversationInvalid(w)
		return "", false
	}
	if len(canon) > maxConversationBytes {
		kb := int(math.Round(float64(len(canon)) / 1024))
		WriteError(w, http.StatusRequestEntityTooLarge, typeConversationTooLarge, fmt.Sprintf(
			"Conversation is too large to save (%d KB; the limit is %d KB). "+
				"Start a new conversation to keep going.",
			kb, maxConversationBytes/1024))
		return "", false
	}
	return string(canon), true
}

func conversationInvalid(w http.ResponseWriter) {
	WriteError(w, http.StatusBadRequest, "", "Invalid conversation")
}

func conversationNotFound(w http.ResponseWriter) {
	WriteError(w, http.StatusNotFound, "", "Conversation not found")
}

// nullableString maps a SQL NULL to a nil *string so a nullable column
// serialises as JSON null, never as "".
func nullableString(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	s := v.String
	return &s
}
