package discover

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/stretchr/testify/require"
)

func TestOllamaEnricher(t *testing.T) {
	t.Parallel()

	t.Run("populates context window from /api/show", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "/api/show", r.URL.Path)
			require.Equal(t, http.MethodPost, r.Method)

			var req map[string]string
			json.NewDecoder(r.Body).Decode(&req)

			w.Header().Set("Content-Type", "application/json")
			switch req["model"] {
			case "llama3:latest":
				json.NewEncoder(w).Encode(ollamaShowResponse{
					ModelInfo: map[string]any{
						"llama.context_length": float64(8192),
					},
				})
			case "qwen2:latest":
				json.NewEncoder(w).Encode(ollamaShowResponse{
					ModelInfo: map[string]any{
						"qwen2.context_length": float64(32768),
					},
				})
			default:
				json.NewEncoder(w).Encode(ollamaShowResponse{})
			}
		}))
		defer srv.Close()

		// Base URL includes /v1 (as Prowl configures it); the enricher
		// strips it so /api/show resolves at the server root.
		cfg := Config{ID: "test-ollama", BaseURL: srv.URL + "/v1"}
		models := []catwalk.Model{
			{ID: "llama3:latest", Name: "llama3:latest"},
			{ID: "qwen2:latest", Name: "qwen2:latest"},
			{ID: "unknown:latest", Name: "unknown:latest"},
		}

		e := &ollamaEnricher{}
		result, err := e.EnrichModels(context.Background(), cfg, &mockResolver{}, models)
		require.NoError(t, err)
		require.Len(t, result, 3)
		require.Equal(t, int64(8192), result[0].ContextWindow)
		require.Equal(t, int64(32768), result[1].ContextWindow)
		require.Equal(t, int64(0), result[2].ContextWindow)
	})

	t.Run("preserves existing context window", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(ollamaShowResponse{
				ModelInfo: map[string]any{
					"llama.context_length": float64(8192),
				},
			})
		}))
		defer srv.Close()

		cfg := Config{ID: "test-ollama", BaseURL: srv.URL}
		models := []catwalk.Model{
			{ID: "llama3:latest", ContextWindow: 16384},
		}

		e := &ollamaEnricher{}
		result, err := e.EnrichModels(context.Background(), cfg, &mockResolver{}, models)
		require.NoError(t, err)
		require.Equal(t, int64(16384), result[0].ContextWindow)
	})

	t.Run("skips /api/show call when context window already set", func(t *testing.T) {
		t.Parallel()
		calls := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls++
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(ollamaShowResponse{})
		}))
		defer srv.Close()

		cfg := Config{ID: "test-ollama", BaseURL: srv.URL}
		models := []catwalk.Model{
			{ID: "m1", ContextWindow: 4096},
			{ID: "m2", ContextWindow: 8192},
		}

		e := &ollamaEnricher{}
		_, err := e.EnrichModels(context.Background(), cfg, &mockResolver{}, models)
		require.NoError(t, err)
		require.Equal(t, 0, calls)
	})
}

func TestExtractContextLength(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		info     map[string]any
		expected int64
	}{
		{"llama key", map[string]any{"llama.context_length": float64(8192)}, 8192},
		{"qwen2 key", map[string]any{"qwen2.context_length": float64(32768)}, 32768},
		{"no context key", map[string]any{"llama.block_count": float64(32)}, 0},
		{"empty map", map[string]any{}, 0},
		{"nil map", nil, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, extractContextLength(tt.info))
		})
	}
}

func TestDiscoverOllamaTags(t *testing.T) {
	t.Run("returns one model per pulled tag", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "/api/tags", r.URL.Path)
			require.Equal(t, http.MethodGet, r.Method)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(ollamaTagsResponse{
				Models: []ollamaTagEntry{
					{Name: "phi3:mini-4k-instruct-q4_0"},
					{Name: "mistral:7b-instruct-q4_K_M"},
					{Name: "user-local/qwen2.5:7b"},
				},
			})
		}))
		defer srv.Close()

		got := DiscoverOllamaTags(context.Background(), Config{ID: "ollama", BaseURL: srv.URL + "/v1"}, &mockResolver{})
		require.Len(t, got, 3)
		require.Equal(t, "phi3:mini-4k-instruct-q4_0", got[0].ID)
		require.Equal(t, "phi3:mini-4k-instruct-q4_0 (local)", got[0].Name, "display name carries the (local) marker so the curated preset and live detection are visually distinguished in the Switch Model menu")
		require.Equal(t, int64(128_000), got[0].ContextWindow, "falls back to oh-my-pi/OMP default context until /api/show fills it in")
		require.Equal(t, "user-local/qwen2.5:7b", got[2].ID, "namespaced tags round-trip unchanged")
	})

	t.Run("returns nil silently when server is unreachable", func(t *testing.T) {
		// Bind to an unused port to guarantee a connection refused error.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		deadURL := srv.URL
		srv.Close()

		got := DiscoverOllamaTags(context.Background(), Config{ID: "ollama", BaseURL: deadURL + "/v1"}, &mockResolver{})
		require.Nil(t, got, "a missing Ollama server must not poison startup; the curated preset stays available")
	})

	t.Run("returns nil on non-200 status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		}))
		defer srv.Close()
		got := DiscoverOllamaTags(context.Background(), Config{ID: "ollama", BaseURL: srv.URL + "/v1"}, &mockResolver{})
		require.Nil(t, got)
	})

	t.Run("skips empty tag names", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(ollamaTagsResponse{
				Models: []ollamaTagEntry{
					{Name: ""},
					{Name: "real:tag"},
				},
			})
		}))
		defer srv.Close()
		got := DiscoverOllamaTags(context.Background(), Config{ID: "ollama", BaseURL: srv.URL + "/v1"}, &mockResolver{})
		require.Len(t, got, 1)
		require.Equal(t, "real:tag", got[0].ID)
	})
}
