package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func compatOf(t *testing.T, r *Registry, platform string) *compat {
	t.Helper()
	p, ok := r.Get(platform)
	if !ok {
		t.Fatalf("platform %q not registered", platform)
	}
	c, ok := p.(*compat)
	if !ok {
		t.Fatalf("platform %q is not a *compat", platform)
	}
	return c
}

func TestRegistryCoverage(t *testing.T) {
	r := NewRegistry()
	// A representative spread of platforms across adapter kinds must exist.
	for _, p := range []string{
		"groq", "openrouter", "google", "cohere", "cloudflare", "aihorde",
		"modelscope", "pollinations", "zhipu", "sail", "electronhub", "router9",
		"septor", "kilo", "ovh", "nvidia", "mistral", "custom",
	} {
		if !r.Has(p) {
			t.Errorf("missing platform %q", p)
		}
	}
	if got := len(r.All()); got < 45 {
		t.Fatalf("registered %d providers, want >= 45", got)
	}
}

func TestResolveCustom(t *testing.T) {
	r := NewRegistry()
	if _, ok := r.Resolve("custom", "   "); ok {
		t.Fatal("custom with blank base URL must not resolve")
	}
	p, ok := r.Resolve("custom", "https://relay.example/v1")
	if !ok {
		t.Fatal("custom with base URL should resolve")
	}
	if p.BaseURL() != "https://relay.example/v1" {
		t.Fatalf("custom base URL = %q", p.BaseURL())
	}
}

func TestAuthHeaderStyles(t *testing.T) {
	r := NewRegistry()

	// Bearer default.
	if h := compatOf(t, r, "groq").authMap("k"); h["Authorization"] != "Bearer k" {
		t.Fatalf("groq auth = %v", h)
	}
	// Google uses x-goog-api-key, never Authorization.
	g := compatOf(t, r, "google").authMap("k")
	if g["x-goog-api-key"] != "k" || g["Authorization"] != "" {
		t.Fatalf("google auth = %v", g)
	}
	// Truly key-less providers send no auth header.
	if h := compatOf(t, r, "kilo").authMap("ignored"); h["Authorization"] != "" {
		t.Fatalf("kilo (keyless) must send no auth header, got %v", h)
	}
	// OpenRouter carries its identity headers.
	o := compatOf(t, r, "openrouter").authMap("k")
	if o["X-Title"] != "FreeLLMAPI" || o["HTTP-Referer"] != "http://localhost:3001" {
		t.Fatalf("openrouter extra headers = %v", o)
	}
}

func TestCloudflareEndpointParse(t *testing.T) {
	base, token, err := cloudflareEndpoint("acct123:secrettoken")
	if err != nil {
		t.Fatal(err)
	}
	if token != "secrettoken" {
		t.Fatalf("token = %q", token)
	}
	if base != "https://api.cloudflare.com/client/v4/accounts/acct123/ai/v1" {
		t.Fatalf("base = %q", base)
	}
	if _, _, err := cloudflareEndpoint("no-colon"); err == nil {
		t.Fatal("compound key without a colon must error")
	}
}

func TestAIHordeEndpointSentinel(t *testing.T) {
	for _, in := range []string{"", "no-key", "0000000000"} {
		_, k, _ := aihordeEndpoint(in)
		if k != aihordeAnonKey {
			t.Fatalf("aihordeEndpoint(%q) key = %q, want anon", in, k)
		}
	}
	if _, k, _ := aihordeEndpoint("real-key"); k != "real-key" {
		t.Fatalf("registered key should pass through, got %q", k)
	}
}

func TestStrictMessageWhitelist(t *testing.T) {
	c := NewRegistry()
	groq := compatOf(t, c, "groq")
	body := groq.buildBody(&ChatRequest{
		Model: "m",
		Messages: []map[string]any{{
			"role": "user", "content": "hi",
			"partial": true, "reasoning_content": "secret",
		}},
	}, false)
	msgs := body["messages"].([]map[string]any)
	if _, ok := msgs[0]["partial"]; ok {
		t.Fatal("strict platform must drop `partial`")
	}
	if _, ok := msgs[0]["reasoning_content"]; ok {
		t.Fatal("strict platform must drop `reasoning_content`")
	}
	if msgs[0]["content"] != "hi" {
		t.Fatal("strict whitelist dropped a legitimate field")
	}
}

func TestForceSingleToolCall(t *testing.T) {
	r := NewRegistry()
	nvidia := compatOf(t, r, "nvidia")
	body := nvidia.buildBody(&ChatRequest{
		Model:  "m",
		Params: map[string]any{"tools": []any{map[string]any{"type": "function"}}},
	}, false)
	if body["parallel_tool_calls"] != false {
		t.Fatalf("nvidia must pin parallel_tool_calls=false when tools present, got %v", body["parallel_tool_calls"])
	}
	// No tools => the flag is not forced.
	body = nvidia.buildBody(&ChatRequest{Model: "m"}, false)
	if _, ok := body["parallel_tool_calls"]; ok {
		t.Fatal("parallel_tool_calls must not be set when there are no tools")
	}
}

func TestCohereStripsSchemaKeys(t *testing.T) {
	r := NewRegistry()
	cohere := compatOf(t, r, "cohere")
	body := cohere.buildBody(&ChatRequest{
		Model: "m",
		Params: map[string]any{"tools": []any{
			map[string]any{"function": map[string]any{"parameters": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"$schema":              "http://json-schema.org/draft-07/schema#",
				"properties":           map[string]any{"x": map[string]any{"type": "string"}},
			}}},
		}},
	}, false)
	params := body["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)["parameters"].(map[string]any)
	if _, ok := params["additionalProperties"]; ok {
		t.Fatal("cohere must strip additionalProperties")
	}
	if _, ok := params["$schema"]; ok {
		t.Fatal("cohere must strip $schema")
	}
	if _, ok := params["type"]; !ok {
		t.Fatal("cohere stripped a legitimate schema key")
	}
}

func TestAIHordeBodyConstraints(t *testing.T) {
	r := NewRegistry()
	horde := compatOf(t, r, "aihorde")
	body := horde.buildBody(&ChatRequest{
		Model: "m",
		Params: map[string]any{
			"max_tokens": 5,
			"stop":       "END",
			"tools":      []any{map[string]any{"type": "function"}},
		},
	}, false)
	if body["max_tokens"] != aihordeMinMaxTokens {
		t.Fatalf("max_tokens floored to %d, got %v", aihordeMinMaxTokens, body["max_tokens"])
	}
	if _, ok := body["stop"].([]any); !ok {
		t.Fatalf("stop must be wrapped in an array, got %T", body["stop"])
	}
	if _, ok := body["tools"]; ok {
		t.Fatal("aihorde must drop tools")
	}
}

// ── generic adapter over HTTP ────────────────────────────────────────────────

func customProvider(t *testing.T, url string) Provider {
	t.Helper()
	p, ok := NewRegistry().Resolve("custom", url)
	if !ok {
		t.Fatal("custom did not resolve")
	}
	return p
}

func TestGenericChatCompletion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/chat/completions" {
			t.Errorf("unexpected path %s", req.URL.Path)
		}
		if req.Header.Get("Authorization") != "Bearer sk-test" {
			t.Errorf("missing bearer, got %q", req.Header.Get("Authorization"))
		}
		io.WriteString(w, `{"id":"c1","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hello"}}]}`)
	}))
	defer srv.Close()

	resp, err := customProvider(t, srv.URL).ChatCompletion(context.Background(), "sk-test", &ChatRequest{
		Model:    "m",
		Messages: []map[string]any{{"role": "user", "content": "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "c1" || string(resp.Choices[0].Message.Content) != `"hello"` {
		t.Fatalf("bad response: %+v", resp)
	}
	if resp.RoutedVia == nil || resp.RoutedVia.Platform != "custom" || resp.RoutedVia.Model != "m" {
		t.Fatalf("routed_via not stamped: %+v", resp.RoutedVia)
	}
}

func TestGenericValidateTriState(t *testing.T) {
	// 200 from an endpoint that ACTUALLY checks the credential => valid. The
	// server must reject a wrong key, or a success proves nothing about the
	// key and the contrast probe correctly refuses to call it healthy.
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "Bearer k" {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":{"message":"bad key"}}`)
			return
		}
		io.WriteString(w, `{"data":[]}`)
	}))
	defer ok.Close()
	if r := customProvider(t, ok.URL).ValidateKey(context.Background(), "k"); !r.IsValid() {
		t.Fatalf("200 => %+v, want valid", r)
	}

	// An endpoint that serves everyone cannot confirm a key. Ollama Cloud does
	// exactly this, which had a dead credential showing as healthy.
	public := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"data":[]}`)
	}))
	defer public.Close()
	if r := customProvider(t, public.URL).ValidateKey(context.Background(), "k"); !r.IsInconclusive() {
		t.Fatalf("public endpoint => %+v, want inconclusive", r)
	}

	// 401 => invalid with the upstream reason.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"message":"revoked"}}`)
	}))
	defer bad.Close()
	r := customProvider(t, bad.URL).ValidateKey(context.Background(), "k")
	if !r.IsInvalid() {
		t.Fatalf("401 => %+v, want invalid", r)
	}

	// Transport failure => inconclusive, never invalid: a dead endpoint must
	// not disable a key.
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := dead.URL
	dead.Close()
	if r := customProvider(t, url).ValidateKey(context.Background(), "k"); !r.IsInconclusive() {
		t.Fatalf("dead endpoint => %+v, want inconclusive", r)
	}
}

func TestGenericStreaming(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"id\":\"c\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"he\"}}]}\n\n")
		io.WriteString(w, "data: {\"id\":\"c\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"llo\"},\"finish_reason\":\"stop\"}]}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	stream, err := customProvider(t, srv.URL).StreamChatCompletion(context.Background(), "k", &ChatRequest{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	var frames int
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if chunk.Model != "m" {
			t.Fatalf("chunk model = %q", chunk.Model)
		}
		frames++
	}
	if frames != 2 {
		t.Fatalf("got %d frames, want 2", frames)
	}
}

func TestDeferredWireFailsLoudly(t *testing.T) {
	// Google's chat wire is deferred: a chat call must error, never silently
	// send an OpenAI body to a Gemini endpoint.
	g, _ := NewRegistry().Get("google")
	if _, err := g.ChatCompletion(context.Background(), "k", &ChatRequest{Model: "m"}); err != ErrWireDeferred {
		t.Fatalf("google chat err = %v, want ErrWireDeferred", err)
	}
}
