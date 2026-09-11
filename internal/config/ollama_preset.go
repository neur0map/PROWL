// Package config — Ollama preset (local-inference provider).
package config

import (
	"charm.land/catwalk/pkg/catwalk"
)

// OllamaDefaultBaseURL points at the standard local Ollama server.
// Override with PROWL_OLLAMA_URL or with a per-workspace provider config.
const OllamaDefaultBaseURL = "http://localhost:11434/v1"

// ollamaDefaultContextWindow is the fallback context size used when the
// /api/show endpoint on the local Ollama server cannot be reached.
// Matches the typical shipped-window for stock Ollama tags.
const ollamaDefaultContextWindow int64 = 128_000

// ollamaDefaultMaxTokens matches oh-my-pi/OMP defaults for the
// openai-responses path on local Ollama. Ollama itself does not enforce a
// per-model output cap, but tool-using agents benefit from a sane ceiling.
const ollamaDefaultMaxTokens int64 = 8_192

// ollamaPresetProvider is the locally-targeted provider entry that is
// always available in the Switch Model menu, even before the user has
// explicitly added Ollama. When the local Ollama server is reachable,
// internal/discover/ollama.go enriches the model list with real
// context-window figures from /api/show.
var ollamaPresetProvider = catwalk.Provider{
	Name:                "Ollama",
	ID:                  catwalk.InferenceProvider("ollama"),
	Type:                "ollama",
	APIEndpoint:         OllamaDefaultBaseURL,
	DefaultLargeModelID: "llama3.3:70b-instruct-q4_K_M",
	DefaultSmallModelID: "llama3.2:3b-instruct-q5_K_M",
	Models: []catwalk.Model{
		{
			ID:               "llama3.3:70b-instruct-q4_K_M",
			Name:             "Llama 3.3 70B Instruct (Q4_K_M)",
			ContextWindow:    ollamaDefaultContextWindow,
			DefaultMaxTokens: ollamaDefaultMaxTokens,
			CanReason:        true,
		},
		{
			ID:               "qwen2.5-coder:32b-instruct-q4_K_M",
			Name:             "Qwen 2.5 Coder 32B Instruct (Q4_K_M)",
			ContextWindow:    ollamaDefaultContextWindow,
			DefaultMaxTokens: ollamaDefaultMaxTokens,
			CanReason:        true,
		},
		{
			ID:               "gemma3:27b-it-q4_K_S",
			Name:             "Gemma 3 27B IT (Q4_K_S)",
			ContextWindow:    ollamaDefaultContextWindow,
			DefaultMaxTokens: ollamaDefaultMaxTokens,
			CanReason:        true,
		},
		{
			ID:               "deepseek-r1:14b-qwen-distill-q5_K_M",
			Name:             "DeepSeek R1 14B (Qwen distill, Q5_K_M)",
			ContextWindow:    ollamaDefaultContextWindow,
			DefaultMaxTokens: ollamaDefaultMaxTokens,
			CanReason:        true,
		},
		{
			ID:               "llama3.2:3b-instruct-q5_K_M",
			Name:             "Llama 3.2 3B Instruct (Q5_K_M)",
			ContextWindow:    ollamaDefaultContextWindow,
			DefaultMaxTokens: ollamaDefaultMaxTokens,
			CanReason:        false,
		},
	},
}
