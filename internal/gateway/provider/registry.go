package provider

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrWireDeferred marks a provider whose non-OpenAI chat wire format has not
// been ported yet (Google's Gemini generateContent, Sail's Responses API with
// background polling). Its ValidateKey still works — the health pass and the
// keys page function — but a chat request through it fails loudly rather than
// silently sending an OpenAI body to an endpoint that would reject or
// misinterpret it. Enumerated in the slice report as deferred.
var ErrWireDeferred = errors.New("provider chat wire format not yet ported (validation only)")

// Timeout floors. openai-compat's own default is 60s (openai-compat.ts:88,
// `timeoutMs ?? 60_000`); the BaseProvider default of 15s is for native
// adapters and is overridden per registration. Validation gets 30s because a
// large /v1/models catalog genuinely takes >10s from a high-latency region
// (openai-compat.ts:444-466). Custom relays get 120s (index.ts:535).
const (
	defaultChatTimeout     = 60 * time.Second
	defaultValidateTimeout = 30 * time.Second
	customChatTimeout      = 120 * time.Second
)

// authStyle is how the credential is presented on the wire.
type authStyle int

const (
	authBearer    authStyle = iota // Authorization: Bearer <key>
	authGoogleKey                  // x-goog-api-key: <key>
)

// providerCfg is one platform's registration. The plain OpenAI-compatible
// platforms differ only in these fields; the handful with a real wire deviation
// carry a hook.
type providerCfg struct {
	platform     string
	name         string
	baseURL      string
	auth         authStyle
	keyless      bool // store keeps a sentinel row; see Keyless.
	extraHeaders map[string]string

	validateURL     string // "" => baseURL + "/models"
	validateMethod  string // "" => GET
	timeout         time.Duration
	validateTimeout time.Duration

	forceSingleToolCall bool // pin parallel_tool_calls=false when tools present
	strict              bool // rebuild each message from a key whitelist
	wireDeferred        bool // chat/stream unimplemented; validate works
	noStream            bool // no upstream stream: buffer one chunk
	verifyModelIdentity bool // a named model may not silently substitute

	// baseURLFromKey rewrites the base URL and/or the bearer per credential —
	// Cloudflare's compound "account_id:token", AI Horde's anon fallback.
	baseURLFromKey func(apiKey string) (baseURL, effKey string, err error)
	// transformBody mutates the rendered request body in place.
	transformBody func(body map[string]any, modelID string)
	// checkResponse inspects a parsed non-stream response (identity, proxy-error).
	checkResponse func(reqModel string, resp *ChatResponse) error
	// validate overrides the default /models probe.
	validate func(ctx context.Context, c *compat, apiKey string) KeyValidationResult
}

// compat is the generic OpenAI-compatible adapter that backs every platform;
// per-provider behaviour lives entirely in cfg's hooks.
type compat struct {
	cfg    providerCfg
	client *http.Client

	cacheMu sync.Mutex
	cache   map[string]time.Time // per-key validation cache (modelscope)
}

func (c *compat) Platform() string { return c.cfg.platform }
func (c *compat) Name() string     { return c.cfg.name }
func (c *compat) BaseURL() string  { return c.cfg.baseURL }
func (c *compat) Keyless() bool    { return c.cfg.keyless }

// endpoint resolves the base URL and the bearer to send for this credential.
func (c *compat) endpoint(apiKey string) (string, string, error) {
	if c.cfg.baseURLFromKey != nil {
		return c.cfg.baseURLFromKey(apiKey)
	}
	return c.cfg.baseURL, apiKey, nil
}

// authMap builds the auth + extra headers. A truly key-less provider (kilo,
// ovh) sends no auth header; AI Horde is registered keyless for the store but
// still sends an anon bearer, which is why the mapping-present case sends one.
func (c *compat) authMap(effKey string) map[string]string {
	h := make(map[string]string, len(c.cfg.extraHeaders)+1)
	for k, v := range c.cfg.extraHeaders {
		h[k] = v
	}
	if c.cfg.keyless && c.cfg.baseURLFromKey == nil {
		return h
	}
	switch c.cfg.auth {
	case authGoogleKey:
		h["x-goog-api-key"] = effKey
	default:
		h["Authorization"] = "Bearer " + effKey
	}
	return h
}

var strictMessageKeys = map[string]bool{
	"role": true, "content": true, "name": true,
	"tool_calls": true, "tool_call_id": true,
}

// strictMessages rebuilds each message from a whitelist so `partial`,
// `reasoning_content` and thought signatures never reach a platform that 400s
// on them (STRICT_PLATFORMS, openai-compat.ts:179-243).
func strictMessages(msgs []map[string]any) []map[string]any {
	out := make([]map[string]any, len(msgs))
	for i, m := range msgs {
		nm := make(map[string]any, len(m))
		for k, v := range m {
			if strictMessageKeys[k] {
				nm[k] = v
			}
		}
		out[i] = nm
	}
	return out
}

// buildBody renders the wire request. It never mutates req: Params is copied so
// two concurrent requests with the same ChatRequest cannot race.
func (c *compat) buildBody(req *ChatRequest, stream bool) map[string]any {
	body := make(map[string]any, len(req.Params)+3)
	for k, v := range req.Params {
		body[k] = v
	}
	body["model"] = req.Model
	msgs := req.Messages
	if c.cfg.strict {
		msgs = strictMessages(msgs)
	}
	body["messages"] = msgs
	body["stream"] = stream
	if c.cfg.forceSingleToolCall && !isEmptyList(body["tools"]) {
		body["parallel_tool_calls"] = false
	}
	if c.cfg.transformBody != nil {
		c.cfg.transformBody(body, req.Model)
	}
	return body
}

func isEmptyList(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case []any:
		return len(t) == 0
	case []map[string]any:
		return len(t) == 0
	default:
		return false
	}
}

// httpCall makes one bounded request and reads the whole body. timeout>0 bounds
// the entire request; used by chat completions, validation probes and their
// overrides — never by streams, which manage their own first-byte watchdog.
func (c *compat) httpCall(ctx context.Context, method, url string, headers map[string]string, body []byte, timeout time.Duration) (int, []byte, http.Header, error) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, r)
	if err != nil {
		return 0, nil, nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if body != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxProviderResponse))
	return resp.StatusCode, b, resp.Header, err
}

// maxProviderResponse bounds a non-streaming provider reply. A custom endpoint
// is operator-supplied, so the peer is not necessarily well behaved: without a
// ceiling a single response can exhaust memory. Streaming replies do not pass
// through here; they are relayed frame by frame.
const maxProviderResponse = 32 << 20

// ChatCompletion is the non-streaming verb.
func (c *compat) ChatCompletion(ctx context.Context, apiKey string, req *ChatRequest) (*ChatResponse, error) {
	if c.cfg.wireDeferred {
		return nil, ErrWireDeferred
	}
	baseURL, effKey, err := c.endpoint(apiKey)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(c.buildBody(req, false))
	if err != nil {
		return nil, err
	}
	status, respBody, header, err := c.httpCall(ctx, http.MethodPost, baseURL+"/chat/completions", c.authMap(effKey), raw, c.cfg.timeout)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, httpErrorFrom(status, header, respBody)
	}
	var resp ChatResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("%s: parse response: %w", c.cfg.name, err)
	}
	resp.Raw = respBody
	resp.Headers = header
	normalizeChoices(&resp)
	resp.RoutedVia = &RoutedVia{Platform: c.cfg.platform, Model: resp.Model}
	if c.cfg.checkResponse != nil {
		if err := c.cfg.checkResponse(req.Model, &resp); err != nil {
			return nil, err
		}
	}
	return &resp, nil
}

// StreamChatCompletion is the streaming verb. A provider with no upstream
// stream (AI Horde) buffers a single completion into one chunk so the caller's
// stream contract still holds.
func (c *compat) StreamChatCompletion(ctx context.Context, apiKey string, req *ChatRequest) (ChatStream, error) {
	if c.cfg.wireDeferred {
		return nil, ErrWireDeferred
	}
	if c.cfg.noStream {
		resp, err := c.ChatCompletion(ctx, apiKey, req)
		if err != nil {
			return nil, err
		}
		return newBufferedStream(resp), nil
	}
	baseURL, effKey, err := c.endpoint(apiKey)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(c.buildBody(req, true))
	if err != nil {
		return nil, err
	}
	resp, cancel, err := c.doStream(ctx, baseURL+"/chat/completions", effKey, raw)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		cancel()
		return nil, httpErrorFrom(resp.StatusCode, resp.Header, b)
	}
	return newSSEStream(resp, cancel, c.cfg.verifyModelIdentity, req.Model), nil
}

// doStream issues the streaming POST with a first-byte watchdog: the per-
// provider timeout bounds the time to response headers, then the body is left
// open until Close or the caller's context ends. This mirrors the reference's
// `timeoutBounds:'headers'` for streams (base.ts:275-341) so a long, healthy
// stream is not killed by the chat timeout.
func (c *compat) doStream(ctx context.Context, url, effKey string, body []byte) (*http.Response, context.CancelFunc, error) {
	reqCtx, cancel := context.WithCancel(ctx)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		cancel()
		return nil, nil, err
	}
	for k, v := range c.authMap(effKey) {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	var timer *time.Timer
	if c.cfg.timeout > 0 {
		timer = time.AfterFunc(c.cfg.timeout, cancel)
	}
	resp, err := c.client.Do(req)
	if timer != nil {
		timer.Stop()
	}
	if err != nil {
		cancel()
		return nil, nil, err
	}
	return resp, cancel, nil
}

// ValidateKey is the credential probe. The default is GET validateURL (or
// baseURL+/models); only 401/403 confirm a bad key, everything else is live,
// and a transport failure is inconclusive.
func (c *compat) ValidateKey(ctx context.Context, apiKey string) KeyValidationResult {
	if c.cfg.validate != nil {
		return c.cfg.validate(ctx, c, apiKey)
	}
	baseURL, effKey, err := c.endpoint(apiKey)
	if err != nil {
		// A malformed compound credential is the key being wrong, not a
		// transport failure.
		return Invalid(err.Error())
	}
	url := c.cfg.validateURL
	if url == "" {
		url = baseURL + "/models"
	}
	method := c.cfg.validateMethod
	if method == "" {
		method = http.MethodGet
	}
	status, body, _, err := c.httpCall(ctx, method, url, c.authMap(effKey), nil, c.cfg.validateTimeout)
	if err != nil {
		return Inconclusive(err.Error())
	}
	result := classifyValidation(c.cfg.name, status, body)

	// A success only means something if the endpoint actually checks the
	// credential. Several providers serve their model list publicly — Ollama
	// Cloud answers /v1/models with no key at all — so a valid key, a bogus
	// key and no key are indistinguishable there, and reporting "healthy"
	// would be a verdict the probe never earned.
	//
	// The contrast probe settles it: if a deliberately invalid credential gets
	// the same acceptance, this endpoint authenticates nothing.
	if result.IsValid() && c.endpointIgnoresCredentials(ctx, method, url) {
		return Inconclusive(c.cfg.name +
			" serves this endpoint without a credential, so a probe cannot confirm the key; " +
			"its real state shows after a routed request")
	}
	return result
}

// sentinelInvalidKey is the credential used for the contrast probe. It is
// syntactically plausible and certainly not anyone's key.
const sentinelInvalidKey = "prowl-contrast-probe-0000000000000000"

// endpointIgnoresCredentials reports whether a validation endpoint accepts a
// deliberately invalid key. The answer is cached per platform for the process
// lifetime: it is a property of the provider's API, not of a key, and this
// doubles validation traffic if re-asked every time.
func (c *compat) endpointIgnoresCredentials(ctx context.Context, method, url string) bool {
	// Keyed by URL rather than platform: every custom endpoint is a
	// different server under one platform name, so a platform-wide verdict
	// would apply one operator's endpoint behaviour to all of them.
	if cached, ok := publicValidateEndpoints.Load(url); ok {
		return cached.(bool)
	}
	status, _, _, err := c.httpCall(ctx, method, url, c.authMap(sentinelInvalidKey), nil, c.cfg.validateTimeout)
	if err != nil {
		// Unreachable on the contrast probe says nothing either way; do not
		// cache a verdict drawn from a transport failure.
		return false
	}
	public := status != 401 && status != 403
	publicValidateEndpoints.Store(url, public)
	return public
}

var publicValidateEndpoints sync.Map

// per-key validation cache (modelscope): a success is trusted for ttl so the
// 5-minute health pass does not burn one paid completion per key per pass.
func (c *compat) cacheHit(fp string, ttl time.Duration) bool {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	last, ok := c.cache[fp]
	return ok && time.Since(last) < ttl
}

func (c *compat) cacheStore(fp string) {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	c.cache[fp] = time.Now()
}

func httpErrorFrom(status int, header http.Header, body []byte) *HTTPError {
	return &HTTPError{
		Status:     status,
		RetryAfter: parseRetryAfter(header.Get("Retry-After")),
		Message:    extractErrorDetail(body),
		Body:       body,
		Headers:    header,
	}
}

// ── Response normalisation ───────────────────────────────────────────────────

// normalizeChoices copies the reference's normalizeChoices (openai-compat.ts:
// 465-524): array content is joined to a string, and hidden reasoning is folded
// into content ONLY when content is empty and there are no tool calls, so a
// reasoning model that answered with a tool call is never overwritten.
func normalizeChoices(resp *ChatResponse) {
	for i := range resp.Choices {
		msg := &resp.Choices[i].Message
		if joined, isArray := joinedArrayContent(msg.Content); isArray {
			msg.Content = jsonString(joined)
		}
		if contentIsEmpty(msg.Content) && len(msg.ToolCalls) == 0 {
			if r := reasoningText(*msg); r != "" {
				msg.Content = jsonString(r)
			}
		}
	}
}

func joinedArrayContent(raw json.RawMessage) (string, bool) {
	if !strings.HasPrefix(strings.TrimSpace(string(raw)), "[") {
		return "", false
	}
	return contentString(raw), true
}

func contentIsEmpty(raw json.RawMessage) bool {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return true
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s) == ""
	}
	return false
}

func reasoningText(m RespMessage) string {
	for _, r := range []json.RawMessage{m.Reasoning, m.ReasoningAlt} {
		if s := contentString(r); strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// contentString flattens a content value (string, [{type,text}], or [string])
// to plain text.
func contentString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			b.WriteString(p.Text)
		}
		return b.String()
	}
	var strs []string
	if json.Unmarshal(raw, &strs) == nil {
		return strings.Join(strs, "")
	}
	return ""
}

func jsonString(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

// ── Streaming implementations ────────────────────────────────────────────────

type sseStream struct {
	resp        *http.Response
	scan        *bufio.Scanner
	cancel      context.CancelFunc
	verifyModel bool
	reqModel    string
	sawFinish   bool
	closed      bool
}

func newSSEStream(resp *http.Response, cancel context.CancelFunc, verify bool, reqModel string) *sseStream {
	sc := bufio.NewScanner(resp.Body)
	// SSE frames can carry a large tool-call payload on one line.
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	return &sseStream{resp: resp, scan: sc, cancel: cancel, verifyModel: verify, reqModel: reqModel}
}

func (s *sseStream) Recv() (*ChatChunk, error) {
	for s.scan.Scan() {
		line := strings.TrimSpace(s.scan.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(line[len("data:"):])
		if data == "[DONE]" {
			return nil, io.EOF
		}
		var chunk ChatChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		chunk.Raw = append([]byte(nil), data...)
		for _, ch := range chunk.Choices {
			if ch.FinishReason != nil && *ch.FinishReason != "" {
				s.sawFinish = true
			}
		}
		if s.verifyModel && s.reqModel != "auto" && chunk.Model != "" && chunk.Model != s.reqModel {
			return nil, &HTTPError{Status: 502, Message: "provider returned a different or missing model identity"}
		}
		return &chunk, nil
	}
	if err := s.scan.Err(); err != nil {
		return nil, err
	}
	// A stream that ends with neither [DONE] nor a finish_reason was truncated
	// (base.ts:407-474). Surfacing that lets the router fail over.
	if !s.sawFinish {
		return nil, io.ErrUnexpectedEOF
	}
	return nil, io.EOF
}

// ResponseHeaders exposes the upstream headers so the router can read its
// rate-limit families from a streamed response too.
func (s *sseStream) ResponseHeaders() http.Header {
	if s.resp == nil {
		return nil
	}
	return s.resp.Header
}

func (s *sseStream) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	if s.cancel != nil {
		s.cancel()
	}
	return s.resp.Body.Close()
}

// bufferedStream turns one completion into a single-frame stream (AI Horde).
type bufferedStream struct {
	chunk   *ChatChunk
	done    bool
	headers http.Header
}

func newBufferedStream(resp *ChatResponse) *bufferedStream {
	choices := make([]ChunkChoice, len(resp.Choices))
	for i, ch := range resp.Choices {
		delta, _ := json.Marshal(ch.Message)
		var fr *string
		if ch.FinishReason != "" {
			f := ch.FinishReason
			fr = &f
		}
		choices[i] = ChunkChoice{Index: ch.Index, Delta: delta, FinishReason: fr}
	}
	return &bufferedStream{
		chunk:   &ChatChunk{ID: resp.ID, Model: resp.Model, Choices: choices, Raw: resp.Raw},
		headers: resp.Headers,
	}
}

func (s *bufferedStream) Recv() (*ChatChunk, error) {
	if s.done {
		return nil, io.EOF
	}
	s.done = true
	return s.chunk, nil
}

func (s *bufferedStream) ResponseHeaders() http.Header { return s.headers }

func (s *bufferedStream) Close() error { return nil }

// ── Registry ─────────────────────────────────────────────────────────────────

// Registry is the runtime provider table (index.ts:21-24). It is read-only
// after construction, so Get/Has/All/Resolve are safe from request goroutines.
type Registry struct {
	providers map[string]Provider
	client    *http.Client
}

// Get returns the singleton provider for a built-in platform.
func (r *Registry) Get(platform string) (Provider, bool) {
	p, ok := r.providers[platform]
	return p, ok
}

// Has reports whether a platform is registered.
func (r *Registry) Has(platform string) bool {
	_, ok := r.providers[platform]
	return ok
}

// All returns every registered provider, platform-sorted for stable listing.
func (r *Registry) All() []Provider {
	out := make([]Provider, 0, len(r.providers))
	for _, p := range r.providers {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Platform() < out[j].Platform() })
	return out
}

// Resolve is the routing entry point (index.ts:547-559): built-in platforms
// return their singleton; `custom` builds a fresh adapter bound to the caller's
// base URL. A custom platform with no base URL resolves to nothing.
func (r *Registry) Resolve(platform, baseURL string) (Provider, bool) {
	if platform == "custom" {
		b := strings.TrimSpace(baseURL)
		if b == "" {
			return nil, false
		}
		return r.newCompat(providerCfg{
			platform: "custom",
			name:     "Custom (OpenAI-compatible)",
			baseURL:  b,
			timeout:  customChatTimeout,
		}), true
	}
	p, ok := r.providers[platform]
	return p, ok
}

func (r *Registry) newCompat(cfg providerCfg) *compat {
	if cfg.timeout == 0 {
		cfg.timeout = defaultChatTimeout
	}
	if cfg.validateTimeout == 0 {
		cfg.validateTimeout = defaultValidateTimeout
	}
	return &compat{cfg: cfg, client: r.client, cache: map[string]time.Time{}}
}

func (r *Registry) register(cfg providerCfg) {
	r.providers[cfg.platform] = r.newCompat(cfg)
}

// NewRegistry builds the provider table. The client has no global Timeout —
// each call bounds itself via the context — but sane transport limits so a hung
// upstream cannot pin a socket forever.
func NewRegistry() *Registry {
	r := &Registry{
		providers: make(map[string]Provider),
		client: &http.Client{
			// A redirect is how a checked URL becomes an unchecked one: the
			// operator's endpoint is screened against internal addresses when
			// it is saved, but a 302 would send the request — carrying the
			// provider credential — anywhere the peer names. No real provider
			// API redirects its inference endpoints, so refusing is free.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
			Transport: &http.Transport{
				MaxIdleConns:          100,
				MaxIdleConnsPerHost:   4,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   15 * time.Second,
				ExpectContinueTimeout: 1 * time.Second,
			},
		},
	}
	registerAll(r)
	return r
}

// defaultRegistry backs Known: the platform set is static, so a single cached
// registry answers membership without the caller holding one.
var (
	defaultRegistryOnce sync.Once
	defaultRegistry     *Registry
)

// Known reports whether platform is a registered built-in (or `custom`). It is
// used by the key vault's legacy import to decide whether an imported id is
// recognised, without coupling the vault to a Registry instance.
func Known(platform string) bool {
	defaultRegistryOnce.Do(func() { defaultRegistry = NewRegistry() })
	return defaultRegistry.Has(platform)
}

// registerAll mirrors server/src/providers/index.ts one entry at a time. Base
// URLs, timeouts, headers, keyless flags and validate overrides are all taken
// from that file (cited inline where the value would be hard to re-derive).
func registerAll(r *Registry) {
	plain := func(platform, name, baseURL string) {
		r.register(providerCfg{platform: platform, name: name, baseURL: baseURL})
	}

	// Google — Gemini wire (deferred); validate hits /v1beta/models with the
	// x-goog-api-key header (google.ts:20, index.ts:26).
	r.register(providerCfg{
		platform: "google", name: "Google AI Studio",
		baseURL: "https://generativelanguage.googleapis.com/v1beta",
		auth:    authGoogleKey, timeout: 60 * time.Second, wireDeferred: true,
	})

	r.register(providerCfg{platform: "groq", name: "Groq", baseURL: "https://api.groq.com/openai/v1", strict: true})
	r.register(providerCfg{platform: "cerebras", name: "Cerebras", baseURL: "https://api.cerebras.ai/v1", strict: true})

	// Sail — Responses API + background polling (deferred); default /models probe.
	r.register(providerCfg{
		platform: "sail", name: "Sail Research",
		baseURL: "https://api.sailresearch.com/v1",
		timeout: 600 * time.Second, wireDeferred: true,
	})

	r.register(providerCfg{
		platform: "electronhub", name: "ElectronHub",
		baseURL:       "https://api.electronhub.ai/v1",
		validateURL:   "https://api.electronhub.ai/v1/user/me", // electronhub.ts:24
		timeout:       90 * time.Second,
		checkResponse: electronHubCheck,
	})
	r.register(providerCfg{
		platform: "experiential", name: "Experiential Labs",
		baseURL:       "https://api.experientiallabs.ai/v1",
		transformBody: experientialTransform,
	})
	r.register(providerCfg{
		platform: "router9", name: "Router9",
		baseURL:  "https://api.router9.com/v1",
		validate: router9Validate,
	})
	r.register(providerCfg{
		platform: "septor", name: "Septor Labs",
		baseURL:             "https://api.septorlabs.com/v1",
		verifyModelIdentity: true,
		checkResponse:       septorCheck,
	})

	plain("bai", "B.AI", "https://api.b.ai/v1")
	plain("anyapi", "AnyAPI", "https://api.anyapi.ai/v1")
	r.register(providerCfg{
		platform: "radeon", name: "AMD Radeon Cloud",
		baseURL: "https://developer.amd.com.cn/radeon/api/v1",
		timeout: 600 * time.Second, forceSingleToolCall: true, // index.ts:110-118
	})
	r.register(providerCfg{
		platform: "nvidia", name: "NVIDIA NIM",
		baseURL: "https://integrate.api.nvidia.com/v1",
		timeout: 180 * time.Second, forceSingleToolCall: true, // index.ts:127-135
	})
	r.register(providerCfg{platform: "mistral", name: "Mistral", baseURL: "https://api.mistral.ai/v1", strict: true})
	r.register(providerCfg{
		platform: "openrouter", name: "OpenRouter",
		baseURL: "https://openrouter.ai/api/v1",
		extraHeaders: map[string]string{ // index.ts:136-138
			"HTTP-Referer": "http://localhost:3001",
			"X-Title":      "FreeLLMAPI",
		},
	})
	plain("github", "GitHub Models", "https://models.github.ai/inference")

	// Cohere — OpenAI-compatible compatibility endpoint; strips the two schema
	// keywords its tool validator rejects (cohere.ts:10,21).
	r.register(providerCfg{
		platform: "cohere", name: "Cohere",
		baseURL:       "https://api.cohere.ai/compatibility/v1",
		transformBody: cohereTransform,
	})

	// Cloudflare — compound "account_id:token" credential; the account id builds
	// the URL (cloudflare.ts:30-45). Two-scope token verify probe.
	r.register(providerCfg{
		platform: "cloudflare", name: "Cloudflare Workers AI",
		baseURLFromKey: cloudflareEndpoint,
		timeout:        60 * time.Second,
		validate:       cloudflareValidate,
	})

	// Zhipu — domestic default, global re-probe on a domestic 401/403 (zhipu.ts:6-8).
	r.register(providerCfg{
		platform: "zhipu", name: "Zhipu AI",
		baseURL:  "https://open.bigmodel.cn/api/paas/v4",
		timeout:  60 * time.Second,
		validate: zhipuValidate,
	})

	plain("huggingface", "HuggingFace Router", "https://router.huggingface.co/v1")
	r.register(providerCfg{platform: "ollama", name: "Ollama Cloud", baseURL: "https://ollama.com/v1", timeout: 120 * time.Second})
	r.register(providerCfg{
		platform: "kilo", name: "Kilo Gateway",
		baseURL:     "https://api.kilo.ai/api/gateway/v1",
		validateURL: "https://api.kilo.ai/api/gateway/models", // index.ts:208-215
		keyless:     true,
	})

	// Pollinations — /v1/models answers 200 for revoked keys, so validate probes
	// the authenticated /account/key instead (pollinations.ts:6-7).
	r.register(providerCfg{
		platform: "pollinations", name: "Pollinations",
		baseURL:  "https://gen.pollinations.ai/v1",
		validate: pollinationsValidate,
	})

	plain("llm7", "LLM7", "https://api.llm7.io/v1")
	plain("opencode", "OpenCode Zen", "https://opencode.ai/zen/v1")
	r.register(providerCfg{platform: "ovh", name: "OVH AI Endpoints", baseURL: "https://oai.endpoints.kepler.ai.cloud.ovh.net/v1", keyless: true})
	r.register(providerCfg{platform: "agnes", name: "Agnes AI", baseURL: "https://apihub.agnes-ai.com/v1", timeout: 60 * time.Second})
	plain("reka", "Reka", "https://api.reka.ai/v1")
	plain("siliconflow", "SiliconFlow", "https://api.siliconflow.com/v1")
	r.register(providerCfg{
		platform: "routeway", name: "Routeway",
		baseURL:      "https://api.routeway.ai/v1",
		extraHeaders: map[string]string{"User-Agent": "Mozilla/5.0 FreeLLMAPI/1.0"}, // index.ts routeway
	})
	plain("bazaarlink", "BazaarLink", "https://bazaarlink.ai/api/v1")
	plain("ainative", "AINative Studio", "https://api.ainative.studio/api/v1")
	plain("aion", "Aion Labs", "https://api.aionlabs.ai/v1")
	plain("requesty", "Requesty", "https://router.requesty.ai/v1")
	r.register(providerCfg{
		platform: "navy", name: "NavyAI",
		baseURL:      "https://api.navy/v1",
		extraHeaders: map[string]string{"User-Agent": "FreeLLMAPI/1.0"},
	})
	plain("nara", "NaraRouter", "https://router.bynara.id/v1")
	plain("sealion", "SEA-LION", "https://api.sea-lion.ai/v1")
	plain("orcarouter", "OrcaRouter", "https://api.orcarouter.ai/v1")
	plain("unorouter", "UnoRouter", "https://api.unorouter.com/v1")
	r.register(providerCfg{
		platform: "xkiro", name: "xKiro",
		baseURL:     "https://api.xkiro.com/v1",
		validateURL: "https://api.xkiro.com/v1/usage", // index.ts xkiro
	})

	// ModelScope — /models accepts garbage tokens; validate is a 1-token chat
	// probe cached 24h (modelscope.ts:6,14).
	r.register(providerCfg{
		platform: "modelscope", name: "ModelScope",
		baseURL:  "https://api-inference.modelscope.cn/v1",
		timeout:  90 * time.Second,
		validate: modelscopeValidate,
	})

	plain("qianfan", "Baidu Qianfan", "https://qianfan.baidubce.com/v2")
	plain("volcengine", "Volcengine Ark", "https://ark.cn-beijing.volces.com/api/v3")
	plain("longcat", "LongCat", "https://api.longcat.chat/openai/v1")
	plain("xfyun", "iFlytek Spark", "https://spark-api-open.xf-yun.com/v1")

	// AI Horde — keyless for the store but sends an anon bearer; no upstream
	// stream; body constraints (aihorde.ts:43-45,86-106).
	r.register(providerCfg{
		platform: "aihorde", name: "AI Horde",
		baseURLFromKey: aihordeEndpoint,
		keyless:        true,
		noStream:       true,
		timeout:        120 * time.Second,
		transformBody:  aihordeTransform,
	})

	// Platforms reached through a login Prowl already holds, rather than a key
	// the operator pastes. Enrolling one seeds its models into the catalogue,
	// so without an adapter here every one of those models is a candidate the
	// loop cannot dispatch — the whole subscription tier looked configured and
	// served nothing.
	//
	// Both speak the OpenAI wire: Hyper's fantasy endpoint is a different
	// protocol, but /v1 is OpenAI-shaped (verified live), and Anthropic serves
	// an OpenAI-compatible route beside its Messages API that accepts a
	// subscription bearer.
	plain("hyper", "Ryoku Hyper", "https://hyper.charm.land/v1")
	r.register(providerCfg{
		platform: "anthropic", name: "Anthropic",
		baseURL: "https://api.anthropic.com/v1",
		extraHeaders: map[string]string{
			"anthropic-version": "2023-06-01",
			// Declares the subscription-OAuth grant; harmless on a plain key.
			"anthropic-beta": "oauth-2025-04-20",
		},
	})

	// custom is registered as a placeholder so Has/Get behave; the real adapter
	// is built per key by Resolve (index.ts:527, 547-559).
	r.register(providerCfg{platform: "custom", name: "Custom (OpenAI-compatible)", baseURL: "", timeout: customChatTimeout})
}

// ── Per-provider deviations ──────────────────────────────────────────────────

// cloudflareEndpoint splits the compound credential and builds the account-
// scoped OpenAI-compatible base URL (cloudflare.ts:38-45).
func cloudflareEndpoint(apiKey string) (string, string, error) {
	i := strings.IndexByte(apiKey, ':')
	if i < 0 {
		return "", "", errors.New(`Cloudflare key must be in format "account_id:api_token"`)
	}
	accountID, token := apiKey[:i], apiKey[i+1:]
	return "https://api.cloudflare.com/client/v4/accounts/" + accountID + "/ai/v1", token, nil
}

// cloudflareValidate tries the user-scoped verify endpoint, then the account-
// scoped one, because a token 403s on the other scope's endpoint (cloudflare.ts
// :159-213). Only a token both scopes reject is invalid.
func cloudflareValidate(ctx context.Context, c *compat, apiKey string) KeyValidationResult {
	i := strings.IndexByte(apiKey, ':')
	if i < 0 {
		return Invalid(`Cloudflare key must be in format "account_id:api_token"`)
	}
	accountID, token := apiKey[:i], apiKey[i+1:]
	urls := []string{
		"https://api.cloudflare.com/client/v4/user/tokens/verify",
		"https://api.cloudflare.com/client/v4/accounts/" + accountID + "/tokens/verify",
	}
	var lastAuth KeyValidationResult
	for _, url := range urls {
		status, body, _, err := c.httpCall(ctx, http.MethodGet, url, map[string]string{"Authorization": "Bearer " + token}, nil, 10*time.Second)
		if err != nil {
			return Inconclusive(err.Error())
		}
		if status == 401 || status == 403 {
			lastAuth = classifyValidation(c.cfg.name, status, body)
			continue // token lacks access to THIS scope; try the other.
		}
		if status < 200 || status >= 300 {
			return Valid() // unexpected non-auth status: do not disable.
		}
		var v struct {
			Success bool `json:"success"`
			Result  struct {
				Status string `json:"status"`
			} `json:"result"`
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		_ = json.Unmarshal(body, &v)
		if v.Success && v.Result.Status == "active" {
			return Valid()
		}
		reason := "verify endpoint did not confirm an active token"
		if v.Result.Status != "" {
			reason = fmt.Sprintf("token status is %q", v.Result.Status)
		} else if len(v.Errors) > 0 && v.Errors[0].Message != "" {
			reason = v.Errors[0].Message
		}
		return Invalid(fmt.Sprintf("%s key validation failed: %s", c.cfg.name, reason))
	}
	return lastAuth
}

const aihordeAnonKey = "0000000000" // aihorde.ts:43
const aihordeMinMaxTokens = 16      // aihorde.ts:44
const aihordeDefaultMaxTokens = 512 // aihorde.ts:45

// aihordeEndpoint maps the stored sentinel to AI Horde's documented anonymous
// key; any other value is a registered key forwarded verbatim (aihorde.ts:78-82).
func aihordeEndpoint(apiKey string) (string, string, error) {
	k := strings.TrimSpace(apiKey)
	if k == "" || k == "no-key" || k == aihordeAnonKey {
		k = aihordeAnonKey
	}
	return "https://oai.aihorde.net/v1", k, nil
}

// aihordeTransform enforces the params the queue proxy rejects (aihorde.ts:86-106).
func aihordeTransform(body map[string]any, _ string) {
	mt := aihordeDefaultMaxTokens
	if v, ok := body["max_tokens"]; ok {
		if n, ok := toInt(v); ok {
			mt = n
		}
	}
	if mt < aihordeMinMaxTokens {
		mt = aihordeMinMaxTokens
	}
	body["max_tokens"] = mt
	if s, ok := body["stop"]; ok && s != nil {
		if _, isArr := s.([]any); !isArr {
			body["stop"] = []any{s}
		}
	}
	delete(body, "tools")
	delete(body, "tool_choice")
	delete(body, "parallel_tool_calls")
}

// cohereUnsupportedSchemaKeys are the JSON-Schema keywords Cohere's tool
// validator 400s on (cohere.ts:21).
var cohereUnsupportedSchemaKeys = map[string]bool{"additionalProperties": true, "$schema": true}

func cohereTransform(body map[string]any, _ string) {
	tools, ok := body["tools"].([]any)
	if !ok {
		return
	}
	for _, t := range tools {
		tm, ok := t.(map[string]any)
		if !ok {
			continue
		}
		fn, ok := tm["function"].(map[string]any)
		if !ok {
			continue
		}
		if params, ok := fn["parameters"]; ok {
			fn["parameters"] = stripSchemaKeys(params, cohereUnsupportedSchemaKeys)
		}
	}
}

// stripSchemaKeys removes the named keys everywhere in a JSON value.
func stripSchemaKeys(v any, keys map[string]bool) any {
	switch t := v.(type) {
	case map[string]any:
		for k := range t {
			if keys[k] {
				delete(t, k)
			}
		}
		for k, val := range t {
			t[k] = stripSchemaKeys(val, keys)
		}
		return t
	case []any:
		for i, e := range t {
			t[i] = stripSchemaKeys(e, keys)
		}
		return t
	default:
		return v
	}
}

// experientialFixedTempModels fix temperature at 1 and reject both sampling
// knobs (experiential.ts:8-11).
var experientialFixedTempModels = map[string]bool{
	"claude-fable-5": true, "claude-opus-5": true, "claude-sonnet-5": true,
	"claude-opus-4.6": true, "claude-opus-4.7": true, "claude-opus-4.8": true,
	"claude-sonnet-4.6": true,
}

// experientialTransform reproduces samplingForModel (experiential.ts:22-35):
// fixed-temperature routes drop both knobs; other Claude routes reject
// temperature and top_p together, so the requested temperature wins.
func experientialTransform(body map[string]any, modelID string) {
	_, hasTemp := body["temperature"]
	_, hasTopP := body["top_p"]
	if experientialFixedTempModels[modelID] {
		delete(body, "temperature")
		delete(body, "top_p")
		return
	}
	if strings.HasPrefix(modelID, "claude-") && hasTemp && hasTopP {
		delete(body, "top_p")
	}
}

// septorCheck fails a response whose returned model is not the requested one
// (septor.ts:6-14). An explicit `auto` route may substitute.
func septorCheck(reqModel string, resp *ChatResponse) error {
	if reqModel != "auto" && resp.Model != reqModel {
		return &HTTPError{Status: 502, Message: "Septor Labs returned a different or missing model identity"}
	}
	return nil
}

const electronHubProxyErrorPrefix = "### **Proxy error (HTTP " // electronhub.ts:9

// electronHubCheck rejects an HTTP-200 completion whose text is actually an
// upstream proxy-error banner (electronhub.ts:9-11,36-40).
func electronHubCheck(_ string, resp *ChatResponse) error {
	for i := range resp.Choices {
		text := strings.TrimLeft(contentString(resp.Choices[i].Message.Content), " \t\r\n")
		if strings.HasPrefix(text, electronHubProxyErrorPrefix) {
			return &HTTPError{Status: 502, Message: "ElectronHub upstream proxy error returned inside a completion"}
		}
	}
	return nil
}

// router9Validate authenticates with a deliberately empty chat request: a valid
// key gets 404 model_not_found (or 402/429) plus account credit headers, an
// invalid one gets 401 (router9.ts:20-45).
func router9Validate(ctx context.Context, c *compat, apiKey string) KeyValidationResult {
	status, body, header, err := c.httpCall(ctx, http.MethodPost, c.cfg.baseURL+"/chat/completions",
		map[string]string{"Authorization": "Bearer " + apiKey}, []byte(`{"messages":[]}`), c.cfg.validateTimeout)
	if err != nil {
		return Inconclusive(err.Error())
	}
	if status == 401 {
		return classifyValidation(c.cfg.name, status, body)
	}
	accountHeaders := isFiniteNumber(header.Get("x-credits-limit")) && isFiniteNumber(header.Get("x-credits-remaining"))
	var b struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &b)
	if accountHeaders && ((status == 404 && b.Error.Code == "model_not_found") || status == 402 || status == 429) {
		return Valid()
	}
	return Inconclusive(fmt.Sprintf("Router9 validation inconclusive (HTTP %d); no authenticated verdict", status))
}

// pollinationsValidate probes /account/key (pollinations.ts:42-70).
func pollinationsValidate(ctx context.Context, c *compat, apiKey string) KeyValidationResult {
	status, body, _, err := c.httpCall(ctx, http.MethodGet, "https://gen.pollinations.ai/account/key",
		map[string]string{"Authorization": "Bearer " + apiKey}, nil, c.cfg.validateTimeout)
	if err != nil {
		return Inconclusive(err.Error())
	}
	if status == 401 {
		return classifyValidation(c.cfg.name, status, body)
	}
	if (status >= 200 && status < 300) || status == 402 {
		return Valid()
	}
	if status == 403 {
		return Inconclusive("Pollinations /account/key HTTP 403: authenticated but lacks introspection permission; not treated as revoked")
	}
	return Inconclusive(fmt.Sprintf("Pollinations /account/key returned HTTP %d — key validity unknown", status))
}

// modelscopeValidate picks a live model then does a 1-token chat probe, caching
// a success 24h (modelscope.ts:82-153).
const modelscopeValidateCacheTTL = 24 * time.Hour // modelscope.ts:14

func modelscopeValidate(ctx context.Context, c *compat, apiKey string) KeyValidationResult {
	fp := sha256hex(apiKey)
	if c.cacheHit(fp, modelscopeValidateCacheTTL) {
		return Valid()
	}
	status, body, _, err := c.httpCall(ctx, http.MethodGet, c.cfg.baseURL+"/models",
		map[string]string{"Authorization": "Bearer " + apiKey}, nil, c.cfg.validateTimeout)
	if err != nil {
		return Inconclusive(err.Error())
	}
	if status < 200 || status >= 300 {
		return Inconclusive(fmt.Sprintf("ModelScope /models returned HTTP %d while picking a probe model", status))
	}
	var roster struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(body, &roster)
	if len(roster.Data) == 0 || roster.Data[0].ID == "" {
		return Inconclusive("ModelScope /models returned no models to probe key validity against")
	}
	probeBody, _ := json.Marshal(map[string]any{
		"model":      roster.Data[0].ID,
		"messages":   []map[string]any{{"role": "user", "content": "ping"}},
		"max_tokens": 1,
	})
	status2, body2, _, err := c.httpCall(ctx, http.MethodPost, c.cfg.baseURL+"/chat/completions",
		map[string]string{"Authorization": "Bearer " + apiKey}, probeBody, c.cfg.validateTimeout)
	if err != nil {
		return Inconclusive(err.Error())
	}
	res := classifyValidation(c.cfg.name, status2, body2)
	if res.IsValid() {
		c.cacheStore(fp)
	}
	return res
}

const zhipuGlobalBase = "https://api.z.ai/api/paas/v4" // zhipu.ts:7

// zhipuValidate re-probes the global console when the domestic one rejects the
// key, because the two consoles have disjoint key namespaces (zhipu.ts:30-108).
func zhipuValidate(ctx context.Context, c *compat, apiKey string) KeyValidationResult {
	status, _, _, err := c.httpCall(ctx, http.MethodGet, c.cfg.baseURL+"/models",
		map[string]string{"Authorization": "Bearer " + apiKey}, nil, c.cfg.validateTimeout)
	if err != nil {
		return Inconclusive(err.Error())
	}
	if status != 401 && status != 403 {
		return Valid()
	}
	gStatus, gBody, _, gErr := c.httpCall(ctx, http.MethodGet, zhipuGlobalBase+"/models",
		map[string]string{"Authorization": "Bearer " + apiKey}, nil, c.cfg.validateTimeout)
	if gErr != nil {
		return Inconclusive(gErr.Error())
	}
	if gStatus != 401 && gStatus != 403 {
		return Valid()
	}
	return classifyValidation(c.cfg.name, gStatus, gBody)
}

// ── small helpers ────────────────────────────────────────────────────────────

func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return int(i), true
		}
	}
	return 0, false
}

func isFiniteNumber(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	_, err := strconv.ParseFloat(s, 64)
	return err == nil
}

func sha256hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
