package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"charm.land/catwalk/pkg/catwalk"
)

// Cache annotations belong to one serialized request, never shared tool or
// message objects. Unknown fields and integer-valued tool arguments survive.
type promptCacheTransport struct {
	http.RoundTripper
}

func (t promptCacheTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	state, _ := req.Context().Value(promptCacheRequestKey{}).(*promptCacheRequest)
	budget, hasBudget := req.Context().Value(googleBudgetKey{}).(int64)
	hasBudget = hasBudget && isGoogleGeneration(req.URL.Path)
	if req.Body != nil && (hasBudget || state != nil && state.policy.changesWireRequest()) {
		body, err := io.ReadAll(req.Body)
		closeErr := req.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("read provider request: %w", err)
		}
		if closeErr != nil {
			return nil, closeErr
		}
		var root map[string]any
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.UseNumber()
		if err := decoder.Decode(&root); err != nil {
			return nil, fmt.Errorf("decode provider cache request: %w", err)
		}
		if root == nil {
			return nil, fmt.Errorf("provider generation request must be an object")
		}
		if hasBudget {
			generation := wireChild(root, "generationConfig")
			wireChild(generation, "thinkingConfig")["thinkingBudget"] = budget
		}
		if state != nil {
			if err := applyPromptCache(req, root, state, t.RoundTripper); err != nil {
				return nil, err
			}
		}
		body, err = json.Marshal(root)
		if err != nil {
			return nil, fmt.Errorf("encode provider cache request: %w", err)
		}
		req = req.Clone(req.Context())
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.ContentLength = int64(len(body))
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(body)), nil
		}
	}
	response, err := t.RoundTripper.RoundTrip(req)
	if err == nil && response != nil && state != nil {
		response, err = retryMissingGeminiCache(req, response, state, t.RoundTripper)
	}
	if response != nil && response.Body != nil && state != nil {
		contentType := response.Header.Get("Content-Type")
		stream := strings.HasPrefix(contentType, "text/event-stream")
		if !strings.Contains(contentType, "amazon.eventstream") {
			state.usage.mu.Lock()
			state.usage.observedWire = true
			state.usage.mu.Unlock()
			response.Body = &usageBody{ReadCloser: response.Body, usage: state.usage, stream: stream}
		}
	}
	return response, err
}

func isGoogleGeneration(path string) bool {
	return strings.HasSuffix(path, ":generateContent") || strings.HasSuffix(path, ":streamGenerateContent")
}

func (p promptCachePolicy) changesWireRequest() bool {
	if p.reasoning {
		return true
	}
	if p.mode == "off" {
		return p.openAIWrites
	}
	return p.anthropicMarkers || p.ttl != "" || p.openAIWrites && p.mode == "explicit" || p.protocol == catwalk.TypeGoogle && p.mode == "explicit"
}

func applyPromptCache(req *http.Request, root map[string]any, state *promptCacheRequest, transport http.RoundTripper) error {
	p := state.policy
	if options := state.reasoning; options != nil {
		reasoning := wireChild(root, "reasoning")
		if options.ReasoningEffort != nil {
			reasoning["effort"] = string(*options.ReasoningEffort)
		}
		if options.ReasoningSummary != nil {
			reasoning["summary"] = string(*options.ReasoningSummary)
		}
	}
	if p.configuration {
		applyConfigurationUpdates(root, state.checkpoints, state.userCount)
	}
	if p.openAIWrites {
		if p.mode == "off" {
			root["prompt_cache_options"] = map[string]any{"mode": "explicit"}
			removeOpenAIBreakpoints(root)
		} else if p.mode == "explicit" || p.ttl != "" {
			options := wireChild(root, "prompt_cache_options")
			options["mode"] = p.mode
			if p.mode == "auto" {
				options["mode"] = "implicit"
			}
			if p.ttl != "" {
				options["ttl"] = p.ttl
			}
			if p.mode == "explicit" {
				markOpenAIInput(root)
			}
		}
		return nil
	}
	if p.mode == "off" {
		return nil
	}
	if p.anthropicMarkers {
		if hasWireMarker(root, "cache_control") {
			// User-owned markers can mix lifetimes. Without a provider
			// breakdown, the configured TTL cannot price every write.
			state.usage.mu.Lock()
			state.usage.unknownWriteTTL = true
			state.usage.policy.ttl = ""
			state.usage.mu.Unlock()
		} else {
			markAnthropicInput(root, p.ttl, p.protocol == catwalk.TypeAnthropic || p.protocol == catwalk.TypeBedrock)
		}
	} else if p.protocol == catwalk.TypeOpenAI && p.ttl != "" {
		root["prompt_cache_retention"] = p.ttl
	} else if p.protocol == catwalk.TypeGoogle && p.mode == "explicit" {
		return applyGeminiCache(req, root, state, transport)
	}
	return nil
}

func wireChild(parent map[string]any, key string) map[string]any {
	child, ok := parent[key].(map[string]any)
	if !ok {
		child = make(map[string]any)
		parent[key] = child
	}
	return child
}

func wireObject(value any) map[string]any {
	object, _ := value.(map[string]any)
	return object
}

func wireArray(value any) []any {
	array, _ := value.([]any)
	return array
}

func hasWireMarker(root map[string]any, key string) bool {
	if _, exists := root[key]; exists {
		return true
	}
	for _, section := range []string{"tools", "system", "messages", "input"} {
		for _, item := range wireArray(root[section]) {
			object := wireObject(item)
			if _, exists := object[key]; exists {
				return true
			}
			for _, field := range []string{"content", "output"} {
				for _, block := range wireArray(object[field]) {
					if _, exists := wireObject(block)[key]; exists {
						return true
					}
				}
			}
		}
	}
	return false
}

func markContent(object map[string]any, field, marker string, annotation any, openAI bool) bool {
	content := object[field]
	if text, ok := content.(string); ok && text != "" {
		kind := "text"
		if openAI {
			kind = "input_text"
		}
		object[field] = []any{map[string]any{"type": kind, "text": text, marker: annotation}}
		return true
	}
	blocks := wireArray(content)
	for i := len(blocks) - 1; i >= 0; i-- {
		block := wireObject(blocks[i])
		kind, _ := block["type"].(string)
		eligible := kind == "text" || kind == "image" || kind == "document" || kind == "tool_use" || kind == "tool_result"
		if openAI {
			eligible = kind == "input_text" || kind == "input_image" || kind == "input_file"
		}
		if eligible {
			block[marker] = annotation
			return true
		}
	}
	return false
}

func markAnthropicInput(root map[string]any, ttl string, native bool) {
	annotation := map[string]any{"type": "ephemeral", "ttl": ttl}
	if native {
		tools := wireArray(root["tools"])
		for i := len(tools) - 1; i >= 0; i-- {
			tool := wireObject(tools[i])
			if _, custom := tool["input_schema"]; custom {
				tool["cache_control"] = annotation
				break
			}
		}
		markContent(root, "system", "cache_control", annotation, false)
	}
	messages := wireArray(root["messages"])
	if !native {
		for i := len(messages) - 1; i >= 0; i-- {
			message := wireObject(messages[i])
			if message["role"] == "system" || message["role"] == "developer" {
				if markContent(message, "content", "cache_control", annotation, false) {
					break
				}
			}
		}
	}
	remaining := 2
	for i := len(messages) - 1; i >= 0 && remaining > 0; i-- {
		message := wireObject(messages[i])
		if message["role"] != "system" && message["role"] != "developer" && markContent(message, "content", "cache_control", annotation, false) {
			remaining--
		}
	}
}

func markOpenAIInput(root map[string]any) {
	if hasWireMarker(root, "prompt_cache_breakpoint") {
		return
	}
	annotation := map[string]any{"mode": "explicit"}
	input := wireArray(root["input"])
	chat := false
	if input == nil {
		input = wireArray(root["messages"])
		chat = true
	}
	remaining := 4
	for _, item := range input {
		message := wireObject(item)
		if message["role"] == "developer" || message["role"] == "system" {
			if markContent(message, "content", "prompt_cache_breakpoint", annotation, !chat) {
				remaining--
				break
			}
		}
	}
	for i := len(input) - 1; i >= 0 && remaining > 0; i-- {
		message := wireObject(input[i])
		if message["role"] == "developer" || message["role"] == "system" {
			continue
		}
		field := "content"
		if message["type"] == "function_call_output" {
			field = "output"
		}
		if markContent(message, field, "prompt_cache_breakpoint", annotation, !chat) {
			remaining--
		}
	}
}

func removeOpenAIBreakpoints(root map[string]any) {
	for _, section := range []string{"input", "messages"} {
		for _, item := range wireArray(root[section]) {
			object := wireObject(item)
			for _, field := range []string{"content", "output"} {
				for _, block := range wireArray(object[field]) {
					delete(wireObject(block), "prompt_cache_breakpoint")
				}
			}
		}
	}
}
