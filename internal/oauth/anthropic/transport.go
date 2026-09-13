package anthropic

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/neur0map/prowl/internal/oauth"
)

var fingerprintHeaders = claudeCodeStaticHeaders()

// Transport supplies Claude's subscription wire contract without replacing
// Prowl's system prompt, tool execution, or conversation ownership.
type Transport struct {
	Base      http.RoundTripper
	Token     *oauth.Token
	once      sync.Once
	sessionID string
}

func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	if t.Token == nil {
		return base.RoundTrip(req)
	}
	r := req.Clone(req.Context())
	r.Header.Del("X-Api-Key")
	r.Header.Del("Authorization")
	if r.URL.Scheme != "https" || !strings.EqualFold(r.URL.Hostname(), "api.anthropic.com") {
		return base.RoundTrip(r)
	}
	r.Header.Set("Authorization", "Bearer "+t.Token.AccessToken)
	r.Header.Set("User-Agent", claudeCodeUserAgent)
	r.Header.Set("anthropic-beta", mergeBetas(r.Header.Get("anthropic-beta")))
	r.Header.Set("anthropic-dangerous-direct-browser-access", "true")
	r.Header.Set("x-app", "cli")
	for key, value := range fingerprintHeaders {
		r.Header.Set(key, value)
	}
	if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/messages") {
		return base.RoundTrip(r)
	}
	if r.Body == nil {
		return nil, fmt.Errorf("claude request has no body")
	}
	data, err := io.ReadAll(r.Body)
	_ = r.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("read Claude request: %w", err)
	}
	sessionID := r.Header.Get("x-session-id")
	if sessionID != "" {
		sessionID = uuid.NewSHA1(uuid.NameSpaceOID, []byte(sessionID)).String()
	} else {
		t.once.Do(func() { t.sessionID = uuid.NewString() })
		sessionID = t.sessionID
	}
	r.Header.Set("x-claude-code-session-id", sessionID)
	data, names, err := claudeRequestBody(data, sessionID, t.Token.AccountID)
	if err != nil {
		return nil, err
	}
	r.Body = io.NopCloser(bytes.NewReader(data))
	r.ContentLength = int64(len(data))
	r.Header.Del("Content-Length")
	r.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }
	resp, err := base.RoundTrip(r)
	if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 || len(names) == 0 {
		return resp, err
	}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		resp.Body = &toolNameStream{source: resp.Body, reader: bufio.NewReader(resp.Body), names: names}
	} else if strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		body, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		body, err = restoreToolNames(body, names)
		if err != nil {
			return nil, err
		}
		resp.Body = io.NopCloser(bytes.NewReader(body))
	} else {
		return resp, nil
	}
	resp.ContentLength = -1
	resp.Header.Del("Content-Length")
	return resp, nil
}

func claudeRequestBody(data []byte, sessionID, accountID string) ([]byte, map[string]string, error) {
	var body map[string]json.RawMessage
	if json.Unmarshal(data, &body) != nil || body == nil {
		return nil, nil, fmt.Errorf("claude request must be a JSON object")
	}
	var messages []map[string]json.RawMessage
	if json.Unmarshal(body["messages"], &messages) != nil {
		return nil, nil, fmt.Errorf("claude request messages must be an array")
	}
	firstUser := ""
	for _, message := range messages {
		if jsonString(message["role"]) != "user" {
			continue
		}
		if json.Unmarshal(message["content"], &firstUser) != nil {
			var blocks []map[string]json.RawMessage
			_ = json.Unmarshal(message["content"], &blocks)
			for _, block := range blocks {
				if jsonString(block["type"]) == "text" {
					firstUser = jsonString(block["text"])
					break
				}
			}
		}
		break
	}
	textBlock := func(text string) json.RawMessage {
		block, _ := json.Marshal(struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}{"text", text})
		return block
	}
	system := []json.RawMessage{textBlock(createClaudeBillingHeader(firstUser)), textBlock(claudeCodeSystemInstruction)}
	if raw := body["system"]; len(raw) > 0 && string(raw) != "null" {
		var text string
		if json.Unmarshal(raw, &text) == nil {
			if text != "" {
				system = append(system, textBlock(text))
			}
		} else {
			var blocks []json.RawMessage
			if json.Unmarshal(raw, &blocks) != nil {
				return nil, nil, fmt.Errorf("invalid Claude system prompt")
			}
			system = append(system, blocks...)
		}
	}
	body["system"], _ = json.Marshal(system)
	var metadata map[string]json.RawMessage
	if raw := body["metadata"]; len(raw) > 0 && string(raw) != "null" {
		if json.Unmarshal(raw, &metadata) != nil {
			return nil, nil, fmt.Errorf("invalid Claude metadata")
		}
	}
	if metadata == nil {
		metadata = make(map[string]json.RawMessage)
	}
	if userID, ok := resolveMetadataUserID(jsonString(metadata["user_id"]), sessionID, accountID); ok {
		metadata["user_id"], _ = json.Marshal(userID)
	}
	if len(metadata) > 0 {
		body["metadata"], _ = json.Marshal(metadata)
	}
	names := make(map[string]string)
	prefixName := func(object map[string]json.RawMessage) {
		name := jsonString(object["name"])
		if name == "" {
			return
		}
		wire := applyClaudeToolPrefix(name)
		if wire == name {
			return
		}
		names[wire] = name
		object["name"], _ = json.Marshal(wire)
	}
	if raw := body["tools"]; len(raw) > 0 && string(raw) != "null" {
		var tools []map[string]json.RawMessage
		if json.Unmarshal(raw, &tools) != nil {
			return nil, nil, fmt.Errorf("invalid Claude tools")
		}
		for _, tool := range tools {
			prefixName(tool)
		}
		body["tools"], _ = json.Marshal(tools)
	}
	for _, message := range messages {
		var blocks []map[string]json.RawMessage
		if json.Unmarshal(message["content"], &blocks) != nil {
			continue
		}
		changed := false
		for _, block := range blocks {
			if jsonString(block["type"]) == "tool_use" {
				prefixName(block)
				changed = true
			}
		}
		if changed {
			message["content"], _ = json.Marshal(blocks)
		}
	}
	body["messages"], _ = json.Marshal(messages)
	if raw := body["tool_choice"]; len(raw) > 0 && string(raw) != "null" {
		var choice map[string]json.RawMessage
		if json.Unmarshal(raw, &choice) != nil {
			return nil, nil, fmt.Errorf("invalid Claude tool choice")
		}
		if jsonString(choice["type"]) == "tool" {
			prefixName(choice)
			body["tool_choice"], _ = json.Marshal(choice)
		}
	}
	var maxTokens int64
	if json.Unmarshal(body["max_tokens"], &maxTokens) == nil && maxTokens > 64000 {
		body["max_tokens"] = json.RawMessage("64000")
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, nil, err
	}
	if !patchCch(data) {
		return nil, nil, fmt.Errorf("could not attest Claude request")
	}
	return data, names, nil
}

func jsonString(raw json.RawMessage) string {
	var value string
	_ = json.Unmarshal(raw, &value)
	return value
}

func restoreToolNames(data []byte, names map[string]string) ([]byte, error) {
	var event map[string]json.RawMessage
	if json.Unmarshal(data, &event) != nil {
		return nil, fmt.Errorf("invalid Claude response JSON")
	}
	changed := false
	restore := func(block map[string]json.RawMessage) bool {
		if jsonString(block["type"]) != "tool_use" {
			return false
		}
		name, ok := names[jsonString(block["name"])]
		if !ok {
			return false
		}
		block["name"], _ = json.Marshal(name)
		return true
	}
	if jsonString(event["type"]) == "content_block_start" {
		var block map[string]json.RawMessage
		if json.Unmarshal(event["content_block"], &block) == nil && restore(block) {
			event["content_block"], _ = json.Marshal(block)
			changed = true
		}
	} else if jsonString(event["type"]) == "message" {
		var blocks []map[string]json.RawMessage
		if json.Unmarshal(event["content"], &blocks) == nil {
			for _, block := range blocks {
				if restore(block) {
					changed = true
				}
			}
			if changed {
				event["content"], _ = json.Marshal(blocks)
			}
		}
	}
	if !changed {
		return data, nil
	}
	return json.Marshal(event)
}

// The reader rewrites only complete SSE data lines, without a scanner's size
// limit or a background goroutine that can outlive a canceled response.
type toolNameStream struct {
	source  io.ReadCloser
	reader  *bufio.Reader
	names   map[string]string
	pending []byte
	err     error
}

func (s *toolNameStream) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for len(s.pending) == 0 {
		if s.err != nil {
			return 0, s.err
		}
		line, err := s.reader.ReadBytes('\n')
		s.err = err
		if bytes.HasPrefix(line, []byte("data:")) {
			payload := bytes.TrimSpace(line[len("data:"):])
			if len(payload) > 0 && !bytes.Equal(payload, []byte("[DONE]")) {
				converted, convertErr := restoreToolNames(payload, s.names)
				if convertErr != nil {
					s.err = convertErr
					return 0, convertErr
				}
				line = append([]byte("data: "), converted...)
				line = append(line, '\n')
			}
		}
		s.pending = line
	}
	n := copy(p, s.pending)
	s.pending = s.pending[n:]
	return n, nil
}
func (s *toolNameStream) Close() error { return s.source.Close() }
