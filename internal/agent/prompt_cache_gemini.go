package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/neur0map/prowl/internal/session"
)

type geminiCacheApplication struct {
	lease  session.PromptCache
	fields map[string]any
}

func applyGeminiCache(req *http.Request, root map[string]any, state *promptCacheRequest, transport http.RoundTripper) error {
	if existing, ok := root["cachedContent"].(string); ok && existing != "" {
		return fmt.Errorf("managed Gemini caching cannot be combined with provider_options.cached_content")
	}
	if state.sessions == nil || state.sessionID == "" {
		return fmt.Errorf("managed Gemini caching requires an owning Prowl session")
	}
	fields := make(map[string]any, 3)
	for _, field := range []string{"systemInstruction", "tools", "toolConfig"} {
		if value, exists := root[field]; exists {
			fields[field] = value
		}
	}
	if len(fields) == 0 {
		return nil
	}
	payload := make(map[string]any, len(fields)+2)
	for key, value := range fields {
		payload[key] = value
	}
	ttl, err := time.ParseDuration(state.policy.ttl)
	if err != nil {
		return err
	}
	payload["model"] = "models/" + strings.TrimPrefix(state.policy.modelID, "models/")
	payload["ttl"] = fmt.Sprintf("%.9fs", ttl.Seconds())
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	// Scope resources to the endpoint and credential identity without storing
	// credentials, their standalone fingerprints, or any prefix text.
	hash := sha256.New()
	for _, value := range []string{
		state.policy.providerID, req.URL.Scheme, req.URL.Host,
		req.Header.Get("Authorization"), req.Header.Get("x-goog-api-key"), req.URL.Query().Get("key"),
	} {
		_, _ = io.WriteString(hash, value)
		_, _ = hash.Write([]byte{0})
	}
	scope := hash.Sum(nil)
	_, _ = io.WriteString(hash, state.sessionID)
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(body)
	key := hex.EncodeToString(hash.Sum(nil))

	lease, err := state.sessions.GetPromptCache(req.Context(), key)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("look up Gemini cache lease: %w", err)
	}
	if errors.Is(err, sql.ErrNoRows) {
		if state.gate != nil {
			select {
			case state.gate <- struct{}{}:
				defer func() { <-state.gate }()
			case <-req.Context().Done():
				return req.Context().Err()
			}
		}
		// Another agent may have created this prefix while we waited.
		lease, err = state.sessions.GetPromptCache(req.Context(), key)
		if errors.Is(err, sql.ErrNoRows) {
			lease, err = createGeminiCache(req, body, state, transport)
			if errors.Is(err, errGeminiCacheTooSmall) {
				slog.Debug("Skipped Gemini cache below provider minimum", "session_id", state.sessionID)
				return nil
			}
			if err != nil {
				return err
			}
			hash.Reset()
			_, _ = hash.Write(scope)
			_, _ = io.WriteString(hash, lease.Resource)
			lease.ID = hex.EncodeToString(hash.Sum(nil))
			lease.Key, lease.SessionID = key, state.sessionID
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(req.Context()), 5*time.Second)
			err = state.sessions.SavePromptCache(cleanupCtx, lease)
			cancel()
			if err != nil {
				return fmt.Errorf("created Gemini cache until %s but its charge could not be persisted: %w", lease.ExpiresAt.Format(time.RFC3339Nano), err)
			}
			slog.Info("Committed Gemini cache lease", "session_id", state.sessionID,
				"provider", state.policy.providerID, "model", state.policy.modelID,
				"cache_tokens", lease.Tokens, "expires_at", lease.ExpiresAt,
				"creation_cost", lease.CreationCost, "storage_cost", lease.StorageCost,
				"cost_source", "configured_rates", "cost_persisted", true)
		} else if err != nil {
			return fmt.Errorf("look up Gemini cache lease: %w", err)
		}
	}
	for field := range fields {
		delete(root, field)
	}
	root["cachedContent"] = lease.Resource
	state.gemini = &geminiCacheApplication{lease: lease, fields: fields}
	return nil
}

var errGeminiCacheTooSmall = errors.New("cached content is below the Gemini provider minimum")

func createGeminiCache(req *http.Request, body []byte, state *promptCacheRequest, transport http.RoundTripper) (session.PromptCache, error) {
	endpoint := *req.URL
	modelPath := strings.LastIndex(endpoint.Path, "/models/")
	if modelPath < 0 {
		return session.PromptCache{}, fmt.Errorf("managed cachedContents requires a native Gemini generation endpoint")
	}
	endpoint.Path, endpoint.RawPath = endpoint.Path[:modelPath]+"/cachedContents", ""
	query := endpoint.Query()
	query.Del("alt")
	endpoint.RawQuery = query.Encode()
	create, err := http.NewRequestWithContext(req.Context(), http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return session.PromptCache{}, err
	}
	create.Header = req.Header.Clone()
	create.Header.Del("Content-Length")
	create.Header.Set("Content-Type", "application/json")
	create.Header.Set("Accept", "application/json")
	response, err := transport.RoundTrip(create)
	if err != nil {
		return session.PromptCache{}, fmt.Errorf("create Gemini cache: remote creation and storage charges are unknown: %w", err)
	}
	defer response.Body.Close()
	result, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return session.PromptCache{}, fmt.Errorf("read Gemini cache creation result; storage charges are unknown: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var failure struct {
			Error struct {
				Status  string `json:"status"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(result, &failure)
		if response.StatusCode == http.StatusBadRequest && failure.Error.Status == "INVALID_ARGUMENT" &&
			strings.HasPrefix(failure.Error.Message, "Cached content is too small.") {
			return session.PromptCache{}, errGeminiCacheTooSmall
		}
		return session.PromptCache{}, fmt.Errorf("create Gemini cache: HTTP %d", response.StatusCode)
	}
	var cache struct {
		Name          string    `json:"name"`
		CreateTime    time.Time `json:"createTime"`
		ExpireTime    time.Time `json:"expireTime"`
		UsageMetadata struct {
			TotalTokenCount int64 `json:"totalTokenCount"`
		} `json:"usageMetadata"`
	}
	if err := json.Unmarshal(result, &cache); err != nil {
		return session.PromptCache{}, fmt.Errorf("decode Gemini cache lease; storage charges are unknown: %w", err)
	}
	if !strings.HasPrefix(cache.Name, "cachedContents/") || cache.CreateTime.IsZero() ||
		!cache.ExpireTime.After(cache.CreateTime) || cache.UsageMetadata.TotalTokenCount <= 0 {
		return session.PromptCache{}, fmt.Errorf("incomplete Gemini cache lease: storage charges are unknown")
	}
	tokens := cache.UsageMetadata.TotalTokenCount
	creationCost := float64(tokens) / 1e6 * state.policy.creationRate
	storageCost := float64(tokens) / 1e6 * state.policy.storageRate * cache.ExpireTime.Sub(cache.CreateTime).Hours()
	if state.policy.flatRate {
		creationCost, storageCost = 0, 0
	}
	return session.PromptCache{
		Resource: cache.Name, CreatedAt: cache.CreateTime, ExpiresAt: cache.ExpireTime,
		Tokens: tokens, CreationCost: creationCost, StorageCost: storageCost,
	}, nil
}

func retryMissingGeminiCache(req *http.Request, response *http.Response, state *promptCacheRequest, transport http.RoundTripper) (*http.Response, error) {
	if response.StatusCode != http.StatusNotFound || state.gemini == nil {
		return response, nil
	}
	// A cache is an optimization, not a prerequisite. A missing leased
	// resource gets exactly one uncached retry; auth and other errors do not.
	_ = response.Body.Close()
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(req.Context()), 5*time.Second)
	err := state.sessions.InvalidatePromptCache(cleanupCtx, state.gemini.lease.ID)
	cancel()
	if err != nil {
		slog.Error("Failed to invalidate missing Gemini cache", "session_id", state.sessionID, "error", err)
	}
	body, err := req.GetBody()
	if err != nil {
		return nil, err
	}
	var root map[string]any
	decoder := json.NewDecoder(body)
	decoder.UseNumber()
	err = decoder.Decode(&root)
	_ = body.Close()
	if err != nil {
		return nil, err
	}
	delete(root, "cachedContent")
	for field, value := range state.gemini.fields {
		root[field] = value
	}
	data, err := json.Marshal(root)
	if err != nil {
		return nil, err
	}
	retry := req.Clone(req.Context())
	retry.Body = io.NopCloser(bytes.NewReader(data))
	retry.ContentLength = int64(len(data))
	retry.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }
	slog.Debug("Retrying without missing Gemini cache", "session_id", state.sessionID)
	return transport.RoundTrip(retry)
}
