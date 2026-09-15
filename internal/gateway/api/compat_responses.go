package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/neur0map/prowl/internal/gateway"
	"github.com/neur0map/prowl/internal/gateway/provider"
)

// The OpenAI Responses surface and the legacy completions surface.
//
// Both are translations onto the same engine path as /v1/chat/completions:
// they parse their own dialect, run the normalised request through failover,
// quota and cooldown unchanged, and render the answer back in their own shape.
//
// Ported from the reference's routes/responses.ts and the legacy handler in
// routes/proxy.ts. Parameters the reference accepts and ignores — metadata,
// previous_response_id, store — are accepted and ignored here too, with one
// deliberate exception noted on the handler: continuation is refused rather
// than silently dropped.

func (s *Server) registerResponseCompatRoutes() {
	s.mux.HandleFunc("POST /v1/responses", s.RequireMachineKey(s.handleResponses))
	s.mux.HandleFunc("POST /v1/completions", s.RequireMachineKey(s.handleLegacyCompletions))
}

// ── legacy completions ──────────────────────────────────────────────────────

// legacyCompletionRequest is the prompt-in, text-out shape editor autocomplete
// clients still send.
type legacyCompletionRequest struct {
	Model       string          `json:"model"`
	Prompt      json.RawMessage `json:"prompt"`
	Suffix      *string         `json:"suffix"`
	MaxTokens   *int            `json:"max_tokens"`
	Temperature *float64        `json:"temperature"`
	TopP        *float64        `json:"top_p"`
	Stop        json.RawMessage `json:"stop"`
	Stream      bool            `json:"stream"`
}

// legacyCompletionMaxTokens is the reference's default for this surface
// (proxy.ts:1030). Autocomplete wants a short answer, and no cap at all would
// let a chat model write an essay into a ghost-text box.
const legacyCompletionMaxTokens = 128

func (s *Server) handleLegacyCompletions(w http.ResponseWriter, r *http.Request) {
	requestID := ensureRequestID(w, r)

	body, err := readInferenceBody(w, r)
	if err != nil {
		return
	}
	var req legacyCompletionRequest
	if err := json.Unmarshal(body, &req); err != nil {
		WriteError(w, http.StatusBadRequest, TypeInvalidRequest, "Invalid request: body is not valid JSON")
		return
	}

	prompt := legacyPromptText(req.Prompt)
	if prompt == "" {
		WriteError(w, http.StatusBadRequest, TypeInvalidRequest, "prompt is required")
		return
	}

	suffix := ""
	if req.Suffix != nil {
		suffix = *req.Suffix
	}

	converted := &chatRequestBody{
		Model:    modelOrAuto(req.Model),
		Stream:   req.Stream,
		Params:   map[string]any{},
		Messages: legacyPromptMessages(prompt, suffix),
	}

	maxTokens := legacyCompletionMaxTokens
	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		maxTokens = *req.MaxTokens
	}
	converted.Params["max_tokens"] = maxTokens
	if req.Temperature != nil {
		converted.Params["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		converted.Params["top_p"] = *req.TopP
	}
	if len(req.Stop) > 0 {
		var stop any
		if json.Unmarshal(req.Stop, &stop) == nil {
			converted.Params["stop"] = stop
		}
	}

	s.runInference(w, r, converted, legacyCompletionShaper{model: modelOrAuto(req.Model)}, requestID)
}

// legacyPromptText accepts the two prompt shapes OpenAI allows: one string, or
// an array of strings. A token-array prompt is not supported by any provider
// here, so it reads as empty and the caller is told the prompt is missing
// rather than being served a completion of nothing.
func legacyPromptText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var asString string
	if json.Unmarshal(raw, &asString) == nil {
		return asString
	}
	var asList []string
	if json.Unmarshal(raw, &asList) == nil {
		return strings.Join(asList, "")
	}
	return ""
}

// legacyPromptMessages reproduces the reference's autocomplete framing
// (proxy.ts completionPromptToMessages). The system message is what stops a
// chat model answering conversationally into a ghost-text box.
func legacyPromptMessages(prompt, suffix string) []map[string]any {
	instruction := "You are a code autocomplete engine. " +
		"Complete at the cursor and return only the text to insert. " +
		"Do not include markdown fences, explanations, or repeat surrounding code."

	user := "Prefix before cursor:\n" + prompt + "\n\nCompletion to insert:"
	if suffix != "" {
		user = "Prefix before cursor:\n" + prompt +
			"\n\nSuffix after cursor:\n" + suffix + "\n\nCompletion to insert:"
	}
	return []map[string]any{
		{"role": "system", "content": instruction},
		{"role": "user", "content": user},
	}
}

// legacyCompletionShaper renders the text_completion shape. Errors reuse the
// OpenAI envelope, which is what this surface's clients parse.
type legacyCompletionShaper struct {
	openAIShaper
	model string
}

func (l legacyCompletionShaper) buffered(resp *provider.ChatResponse) ([]byte, error) {
	text := ""
	finish := any(nil)
	if len(resp.Choices) > 0 {
		text = jsonText(resp.Choices[0].Message.Content)
		if reason := resp.Choices[0].FinishReason; reason != "" {
			finish = reason
		}
	}
	payload := map[string]any{
		"id":      legacyCompletionID(resp.ID),
		"object":  "text_completion",
		"created": time.Now().Unix(),
		"model":   l.model,
		"choices": []map[string]any{{
			"text": text, "index": 0, "logprobs": nil, "finish_reason": finish,
		}},
	}
	if resp.Usage != nil {
		payload["usage"] = map[string]int{
			"prompt_tokens":     resp.Usage.PromptTokens,
			"completion_tokens": resp.Usage.CompletionTokens,
			"total_tokens":      resp.Usage.TotalTokens,
		}
	}
	return json.Marshal(payload)
}

func (l legacyCompletionShaper) newStream() streamShaper {
	return &legacyCompletionStream{model: l.model}
}

func legacyCompletionID(upstream string) string {
	if strings.HasPrefix(upstream, "cmpl-") {
		return upstream
	}
	if upstream != "" {
		return "cmpl-" + upstream
	}
	return "cmpl-" + newRequestID()
}

// legacyCompletionStream re-frames each chat delta as a text_completion chunk,
// which is the only shape this surface's clients parse.
type legacyCompletionStream struct {
	model string
	id    string
}

func (l *legacyCompletionStream) frame(chunk *provider.ChatChunk) []byte {
	var delta struct {
		Content string `json:"content"`
	}
	if len(chunk.Choices) > 0 && len(chunk.Choices[0].Delta) > 0 {
		_ = json.Unmarshal(chunk.Choices[0].Delta, &delta)
	}
	finish := any(nil)
	if len(chunk.Choices) > 0 && chunk.Choices[0].FinishReason != nil && *chunk.Choices[0].FinishReason != "" {
		finish = *chunk.Choices[0].FinishReason
	}
	if delta.Content == "" && finish == nil {
		return nil
	}
	if l.id == "" {
		l.id = legacyCompletionID(chunk.ID)
	}
	payload, err := json.Marshal(map[string]any{
		"id":      l.id,
		"object":  "text_completion",
		"created": time.Now().Unix(),
		"model":   l.model,
		"choices": []map[string]any{{
			"text": delta.Content, "index": 0, "logprobs": nil, "finish_reason": finish,
		}},
	})
	if err != nil {
		return nil
	}
	return append(append([]byte("data: "), payload...), '\n', '\n')
}

func (l *legacyCompletionStream) done() []byte { return []byte("data: [DONE]\n\n") }

func (l *legacyCompletionStream) streamError(message string) []byte {
	return openAIStream{}.streamError(message)
}

// ── Responses API ───────────────────────────────────────────────────────────

type responsesRequest struct {
	Model              string          `json:"model"`
	Input              json.RawMessage `json:"input"`
	Instructions       *string         `json:"instructions"`
	MaxOutputTokens    *int            `json:"max_output_tokens"`
	Temperature        *float64        `json:"temperature"`
	TopP               *float64        `json:"top_p"`
	Stream             bool            `json:"stream"`
	PreviousResponseID *string         `json:"previous_response_id"`
	Tools              json.RawMessage `json:"tools"`
	ToolChoice         json.RawMessage `json:"tool_choice"`
}

func (s *Server) handleResponses(w http.ResponseWriter, r *http.Request) {
	requestID := ensureRequestID(w, r)

	body, err := readInferenceBody(w, r)
	if err != nil {
		return
	}
	var req responsesRequest
	if err := json.Unmarshal(body, &req); err != nil {
		WriteError(w, http.StatusBadRequest, TypeInvalidRequest, "Invalid request: body is not valid JSON")
		return
	}

	// Continuation is refused rather than ignored. This gateway stores no
	// response state, so honouring the parameter is impossible — and silently
	// dropping it would answer without the conversation the client believes it
	// referenced, which is a wrong answer rather than an error.
	if req.PreviousResponseID != nil && *req.PreviousResponseID != "" {
		WriteErrorCode(w, http.StatusBadRequest, TypeInvalidRequest, "unsupported_parameter",
			"previous_response_id is not supported: this gateway stores no response state, "+
				"so send the prior turns in input instead")
		return
	}

	messages, err := responsesInputMessages(req.Input)
	if err != nil {
		WriteError(w, http.StatusBadRequest, TypeInvalidRequest, "Invalid request: "+err.Error())
		return
	}
	if req.Instructions != nil && *req.Instructions != "" {
		messages = append([]map[string]any{
			{"role": "system", "content": *req.Instructions},
		}, messages...)
	}
	if len(messages) == 0 {
		WriteError(w, http.StatusBadRequest, TypeInvalidRequest, "input is required")
		return
	}

	converted := &chatRequestBody{
		Model:    modelOrAuto(req.Model),
		Stream:   req.Stream,
		Params:   map[string]any{},
		Messages: messages,
	}
	if req.MaxOutputTokens != nil && *req.MaxOutputTokens > 0 {
		converted.Params["max_tokens"] = *req.MaxOutputTokens
	}
	if req.Temperature != nil {
		converted.Params["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		converted.Params["top_p"] = *req.TopP
	}
	if len(req.Tools) > 0 {
		var tools any
		if json.Unmarshal(req.Tools, &tools) == nil {
			converted.Params["tools"] = tools
		}
	}
	if len(req.ToolChoice) > 0 {
		var choice any
		if json.Unmarshal(req.ToolChoice, &choice) == nil {
			converted.Params["tool_choice"] = choice
		}
	}

	shaper := responsesShaper{
		model:       modelOrAuto(req.Model),
		responseID:  "resp_" + newRequestID(),
		inputTokens: int(estimateTokens(converted)),
	}
	s.runInference(w, r, converted, shaper, requestID)
}

// responsesInputMessages accepts the three input shapes the Responses API
// allows: a bare string, an array of messages, and an array of content parts
// belonging to a single user turn.
func responsesInputMessages(raw json.RawMessage) ([]map[string]any, error) {
	if len(raw) == 0 {
		return nil, nil
	}

	var asString string
	if json.Unmarshal(raw, &asString) == nil {
		if asString == "" {
			return nil, nil
		}
		return []map[string]any{{"role": "user", "content": asString}}, nil
	}

	var items []map[string]any
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, errInputShape
	}

	out := make([]map[string]any, 0, len(items))
	var loose []map[string]any
	for _, item := range items {
		role, hasRole := item["role"].(string)
		if !hasRole {
			// A bare content part belongs to a user turn, which is how the
			// single-message form is written.
			loose = append(loose, item)
			continue
		}
		content := responsesContent(item["content"])
		out = append(out, map[string]any{"role": role, "content": content})
	}
	if len(loose) > 0 {
		out = append(out, map[string]any{"role": "user", "content": responsesPartList(loose)})
	}
	return out, nil
}

var errInputShape = &inputShapeError{}

type inputShapeError struct{}

func (*inputShapeError) Error() string {
	return "input must be a string, an array of messages, or an array of content parts"
}

// responsesContent renders one message's content. Text-only content collapses
// back to a plain string because that is what the chat providers expect; an
// array survives only when a part cannot be represented as text.
func responsesContent(value any) any {
	switch typed := value.(type) {
	case string:
		return typed
	case []any:
		parts := make([]map[string]any, 0, len(typed))
		for _, entry := range typed {
			if part, ok := entry.(map[string]any); ok {
				parts = append(parts, part)
			}
		}
		return responsesPartList(parts)
	default:
		return ""
	}
}

// responsesPartList maps Responses content parts onto chat content blocks.
// input_text, output_text and summary_text all carry plain text; a refusal is
// folded in as text so a replayed assistant turn is not silently emptied.
func responsesPartList(parts []map[string]any) any {
	var (
		text    strings.Builder
		blocks  []map[string]any
		hasFile bool
	)
	for _, part := range parts {
		partType, _ := part["type"].(string)
		switch partType {
		case "text", "input_text", "output_text", "summary_text", "":
			if value, ok := part["text"].(string); ok {
				text.WriteString(value)
				blocks = append(blocks, map[string]any{"type": "text", "text": value})
			}
		case "refusal":
			if value, ok := part["refusal"].(string); ok {
				text.WriteString(value)
				blocks = append(blocks, map[string]any{"type": "text", "text": value})
			}
		case "input_image", "image_url", "image", "computer_screenshot":
			if url := responsesPartImageURL(part); url != "" {
				blocks = append(blocks, map[string]any{
					"type":      "image_url",
					"image_url": map[string]any{"url": url},
				})
				hasFile = true
			}
		}
	}
	if !hasFile {
		return text.String()
	}
	return blocks
}

// responsesPartImageURL reads the image out of the shapes the Responses API
// and the chat API each use for one.
func responsesPartImageURL(part map[string]any) string {
	if url, ok := part["image_url"].(string); ok {
		return url
	}
	if nested, ok := part["image_url"].(map[string]any); ok {
		if url, ok := nested["url"].(string); ok {
			return url
		}
	}
	if url, ok := part["url"].(string); ok {
		return url
	}
	return ""
}

func modelOrAuto(model string) string {
	if strings.TrimSpace(model) == "" {
		return "auto"
	}
	return model
}

// responsesShaper renders the Responses object shape.
type responsesShaper struct {
	openAIShaper
	model       string
	responseID  string
	inputTokens int
}

func (rs responsesShaper) buffered(resp *provider.ChatResponse) ([]byte, error) {
	text := ""
	var calls []toolCall
	if len(resp.Choices) > 0 {
		text = jsonText(resp.Choices[0].Message.Content)
		calls = parseToolCalls(resp.Choices[0].Message.ToolCalls)
	}

	prompt, completion := rs.inputTokens, 0
	if resp.Usage != nil {
		prompt, completion = resp.Usage.PromptTokens, resp.Usage.CompletionTokens
	}
	return json.Marshal(rs.object(text, calls, "completed", prompt, completion))
}

// object builds the Responses envelope (responses.ts buildResponseObject).
func (rs responsesShaper) object(text string, calls []toolCall, status string, prompt, completion int) map[string]any {
	output := make([]map[string]any, 0, 1+len(calls))
	if text != "" {
		output = append(output, map[string]any{
			"type":    "message",
			"id":      "msg_" + newRequestID(),
			"status":  "completed",
			"role":    "assistant",
			"content": []map[string]any{{"type": "output_text", "text": text, "annotations": []any{}}},
		})
	}
	for _, call := range calls {
		arguments := "{}"
		if raw, err := json.Marshal(call.Input); err == nil {
			arguments = string(raw)
		}
		output = append(output, map[string]any{
			"type": "function_call", "id": "fc_" + newRequestID(), "call_id": call.ID,
			"name": call.Name, "arguments": arguments, "status": "completed",
		})
	}
	return map[string]any{
		"id":          rs.responseID,
		"object":      "response",
		"created_at":  time.Now().Unix(),
		"status":      status,
		"model":       rs.model,
		"output":      output,
		"output_text": text,
		"usage": map[string]any{
			"input_tokens":          prompt,
			"input_tokens_details":  map[string]any{"cached_tokens": 0},
			"output_tokens":         completion,
			"output_tokens_details": map[string]any{"reasoning_tokens": 0},
			"total_tokens":          prompt + completion,
		},
	}
}

func (rs responsesShaper) newStream() streamShaper {
	return &responsesStream{shaper: rs}
}

// responsesStream renders the Responses event sequence. The names are the
// contract: a client switches on them, so an OpenAI chat chunk written here is
// unparseable rather than merely different.
type responsesStream struct {
	shaper  responsesShaper
	started bool
	text    strings.Builder
	finish  string
	itemID  string
}

func (rs *responsesStream) frame(chunk *provider.ChatChunk) []byte {
	var delta struct {
		Content string `json:"content"`
	}
	if len(chunk.Choices) > 0 && len(chunk.Choices[0].Delta) > 0 {
		_ = json.Unmarshal(chunk.Choices[0].Delta, &delta)
	}
	if len(chunk.Choices) > 0 && chunk.Choices[0].FinishReason != nil {
		rs.finish = *chunk.Choices[0].FinishReason
	}
	if delta.Content == "" {
		return nil
	}

	var out []byte
	if !rs.started {
		rs.started = true
		rs.itemID = "msg_" + newRequestID()
		out = append(out, sseEvent("response.created", map[string]any{
			"type":     "response.created",
			"response": rs.shaper.object("", nil, "in_progress", rs.shaper.inputTokens, 0),
		})...)
		out = append(out, sseEvent("response.in_progress", map[string]any{
			"type":     "response.in_progress",
			"response": rs.shaper.object("", nil, "in_progress", rs.shaper.inputTokens, 0),
		})...)
		out = append(out, sseEvent("response.output_item.added", map[string]any{
			"type":         "response.output_item.added",
			"output_index": 0,
			"item": map[string]any{
				"type": "message", "id": rs.itemID, "status": "in_progress",
				"role": "assistant", "content": []any{},
			},
		})...)
		out = append(out, sseEvent("response.content_part.added", map[string]any{
			"type": "response.content_part.added", "item_id": rs.itemID,
			"output_index": 0, "content_index": 0,
			"part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}},
		})...)
	}

	rs.text.WriteString(delta.Content)
	out = append(out, sseEvent("response.output_text.delta", map[string]any{
		"type": "response.output_text.delta", "item_id": rs.itemID,
		"output_index": 0, "content_index": 0, "delta": delta.Content,
	})...)
	return out
}

func (rs *responsesStream) done() []byte {
	text := rs.text.String()
	var out []byte
	if rs.started {
		out = append(out, sseEvent("response.output_text.done", map[string]any{
			"type": "response.output_text.done", "item_id": rs.itemID,
			"output_index": 0, "content_index": 0, "text": text,
		})...)
		out = append(out, sseEvent("response.content_part.done", map[string]any{
			"type": "response.content_part.done", "item_id": rs.itemID,
			"output_index": 0, "content_index": 0,
			"part": map[string]any{"type": "output_text", "text": text, "annotations": []any{}},
		})...)
		out = append(out, sseEvent("response.output_item.done", map[string]any{
			"type": "response.output_item.done", "output_index": 0,
			"item": map[string]any{
				"type": "message", "id": rs.itemID, "status": "completed", "role": "assistant",
				"content": []map[string]any{{"type": "output_text", "text": text, "annotations": []any{}}},
			},
		})...)
	}
	// Output tokens are estimated: the provider reports usage in a frame this
	// surface never sees, and zero would be a wrong number rather than an
	// approximate one.
	out = append(out, sseEvent("response.completed", map[string]any{
		"type":     "response.completed",
		"response": rs.shaper.object(text, nil, "completed", rs.shaper.inputTokens, len(text)/4),
	})...)
	return out
}

func (rs *responsesStream) streamError(message string) []byte {
	return sseEvent("response.failed", map[string]any{
		"type": "response.failed",
		"response": map[string]any{
			"id": rs.shaper.responseID, "object": "response", "status": "failed",
			"error": map[string]any{"code": "upstream_error", "message": message},
		},
	})
}

// writeError keeps the OpenAI envelope: the Responses API is an OpenAI family
// surface and its SDKs parse that shape. The type is derived from the engine's
// coarse kind so a caller can still branch on what went wrong.
func (rs responsesShaper) writeError(w http.ResponseWriter, status int, kind gateway.ExhaustionKind, code, message string, retryAt time.Time) {
	_ = kind
	rs.openAIShaper.writeError(w, status, kind, code, message, retryAt)
}
