package api

import (
	"net/http"
)

// The API reference for the public /v1 surface (docs.ts:1-23):
//   GET /v1/openapi.json — the spec
//   GET /v1/docs         — a self-contained viewer for it
//
// Both are static and expose nothing secret, so neither is gated by the
// unified key: a caller needs the reference in order to obtain one.
//
// The spec is built from v1Endpoints, and docs_routes_test asserts that list
// matches the routes actually registered on the mux in both directions. A
// reference that documents an endpoint the server does not serve is worse than
// no reference at all, and the only way to keep that honest is to check it.

// v1Endpoint is one documented operation.
type v1Endpoint struct {
	Method      string
	Path        string
	Summary     string
	Description string
	Auth        bool
	Streams     bool
}

// v1Endpoints is the whole public surface. Keep it in step with the mux; the
// test enforces it.
var v1Endpoints = []v1Endpoint{
	{
		Method: "POST", Path: "/v1/chat/completions", Auth: true, Streams: true,
		Summary:     "Create a chat completion",
		Description: "OpenAI-compatible. Pass \"auto\" as the model to let the router choose, or \"auto:<chain>\" to name a routing chain. Failures move to the next provider before the response commits.",
	},
	{
		Method: "POST", Path: "/v1/completions", Auth: true, Streams: true,
		Summary:     "Create a text completion",
		Description: "Legacy OpenAI text-completion shape, served from the same routing pool as chat.",
	},
	{
		Method: "POST", Path: "/v1/responses", Auth: true, Streams: true,
		Summary:     "Create a response",
		Description: "OpenAI Responses API shape, translated onto whichever provider serves the request.",
	},
	{
		Method: "POST", Path: "/v1/messages", Auth: true, Streams: true,
		Summary:     "Create a message",
		Description: "Anthropic Messages API shape, including SSE events, served from the same pool.",
	},
	{
		Method: "POST", Path: "/v1/messages/count_tokens", Auth: true,
		Summary:     "Count message tokens",
		Description: "Anthropic token counting for a message payload.",
	},
	{
		Method: "POST", Path: "/v1/messages/count", Auth: true,
		Summary:     "Count message tokens (alias)",
		Description: "Alias of /v1/messages/count_tokens.",
	},
	{
		Method: "POST", Path: "/v1/embeddings", Auth: true,
		Summary:     "Create embeddings",
		Description: "OpenAI-compatible embeddings, routed across the embedding catalogue.",
	},
	{
		Method: "POST", Path: "/v1/images/generations", Auth: true,
		Summary:     "Generate an image",
		Description: "Routed across providers whose catalogue entry declares image output.",
	},
	{
		Method: "POST", Path: "/v1/videos/generations", Auth: true,
		Summary:     "Generate a video",
		Description: "Routed across providers whose catalogue entry declares video output. Answers 503 while no configured key serves a video model.",
	},
	{
		Method: "POST", Path: "/v1/audio/speech", Auth: true,
		Summary:     "Synthesise speech",
		Description: "Text to audio, routed across providers declaring speech output.",
	},
	{
		Method: "POST", Path: "/v1/audio/transcriptions", Auth: true,
		Summary:     "Transcribe audio",
		Description: "Multipart audio upload, routed across providers declaring transcription.",
	},
	{
		Method: "GET", Path: "/v1/models", Auth: true,
		Summary:     "List routable models",
		Description: "Every model the configured keys can serve, plus the \"auto\" pseudo-model and one entry per routing chain.",
	},
	{
		Method: "GET", Path: "/v1/providers", Auth: true,
		Summary:     "Provider status rollup",
		Description: "Per-platform key health, active rate-limit resume times and observed request headroom: enough for a caller in front of this gateway to decide \"route here or skip\" without spending a request.",
	},
	{
		Method: "GET", Path: "/v1/quota-forecast", Auth: true,
		Summary:     "Daily quota headroom",
		Description: "Per-pool remaining requests, window reset, and a low-balance flag a caller can gate on before sending a request that would be refused.",
	},
	{
		Method: "GET", Path: "/v1/openapi.json",
		Summary:     "This specification",
		Description: "Served unauthenticated: a caller needs the reference in order to get a key.",
	},
	{
		Method: "GET", Path: "/v1/docs",
		Summary:     "API reference viewer",
		Description: "Self-contained HTML rendering of the specification. No external assets.",
	},
}

func (s *Server) registerDocsRoutes() {
	s.mux.HandleFunc("GET /v1/openapi.json", s.handleOpenAPISpec)
	s.mux.HandleFunc("GET /v1/docs", s.handleDocsPage)
}

func (s *Server) handleOpenAPISpec(w http.ResponseWriter, _ *http.Request) {
	paths := map[string]any{}
	for _, ep := range v1Endpoints {
		op := map[string]any{
			"summary":     ep.Summary,
			"description": ep.Description,
			"responses": map[string]any{
				"200": map[string]any{"description": "Success"},
			},
		}
		if ep.Auth {
			op["security"] = []any{map[string]any{"bearerAuth": []string{}}}
			op["responses"].(map[string]any)["401"] = map[string]any{
				"description": "Missing or wrong API key",
			}
			op["responses"].(map[string]any)["502"] = map[string]any{
				"description": "Every provider attempt failed",
			}
		}
		if ep.Streams {
			op["description"] = ep.Description +
				" Set \"stream\": true for server-sent events."
		}
		entry, ok := paths[ep.Path].(map[string]any)
		if !ok {
			entry = map[string]any{}
			paths[ep.Path] = entry
		}
		switch ep.Method {
		case "GET":
			entry["get"] = op
		case "POST":
			entry["post"] = op
		}
	}

	// The spec only changes when the binary does.
	w.Header().Set("Cache-Control", "public, max-age=3600")
	WriteJSON(w, http.StatusOK, map[string]any{
		"openapi": "3.0.3",
		"info": map[string]any{
			"title": "Prowl Gateway",
			"description": "One OpenAI-compatible endpoint over every provider you have configured. " +
				"Requests are scored, routed, and failed over across keys and models.",
			"version": "1.0.0",
		},
		"servers": []any{map[string]any{"url": "/"}},
		"components": map[string]any{
			"securitySchemes": map[string]any{
				"bearerAuth": map[string]any{
					"type":         "http",
					"scheme":       "bearer",
					"description":  "The unified API key from the dashboard's Keys page.",
					"bearerFormat": "opaque",
				},
			},
		},
		"paths": paths,
	})
}

func (s *Server) handleDocsPage(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(docsPage))
}

// docsPage renders the spec client-side through DOM APIs rather than string
// templating, so nothing in the spec can inject markup. It is self-contained
// for the same reason the rest of the dashboard is: Prowl ships as one binary
// and must work with no network.
const docsPage = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Prowl Gateway · API</title>
<style>
  :root { color-scheme: dark; }
  body { margin: 0; background: #0b0b0e; color: #e7e7ea;
         font: 15px/1.6 ui-sans-serif, system-ui, -apple-system, sans-serif; }
  main { max-width: 860px; margin: 0 auto; padding: 56px 24px 96px; }
  h1 { font-size: 30px; letter-spacing: -0.02em; margin: 0 0 6px; }
  p.lede { color: #9b9ba3; margin: 0 0 36px; max-width: 62ch; }
  section { border: 1px solid #22222a; border-radius: 16px; padding: 18px 20px;
            margin-bottom: 12px; background: #101015; }
  .row { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
  .m { font: 600 11px/1 ui-monospace, monospace; letter-spacing: 0.06em;
       padding: 5px 8px; border-radius: 6px; background: #1b2b22; color: #6ee7a8; }
  .m.get { background: #16212e; color: #7cc4f7; }
  code.path { font: 13px ui-monospace, monospace; color: #e7e7ea; }
  .tag { font-size: 11px; color: #9b9ba3; border: 1px solid #2a2a33;
         border-radius: 999px; padding: 2px 8px; }
  h2 { font-size: 14px; margin: 12px 0 2px; font-weight: 600; }
  .desc { color: #9b9ba3; font-size: 13.5px; margin: 0; }
  footer { color: #6b6b74; font-size: 12.5px; margin-top: 28px; }
</style>
</head>
<body>
<main>
  <h1 id="title">Prowl Gateway</h1>
  <p class="lede" id="lede"></p>
  <div id="ops"></div>
  <footer id="foot"></footer>
</main>
<script>
(async () => {
  const res = await fetch('openapi.json');
  const spec = await res.json();
  document.getElementById('title').textContent = spec.info.title;
  document.getElementById('lede').textContent = spec.info.description;

  const order = ['post', 'get'];
  const ops = document.getElementById('ops');
  for (const [path, methods] of Object.entries(spec.paths)) {
    for (const method of order) {
      const op = methods[method];
      if (!op) continue;
      const section = document.createElement('section');
      const row = document.createElement('div');
      row.className = 'row';
      const m = document.createElement('span');
      m.className = 'm ' + method;
      m.textContent = method.toUpperCase();
      const p = document.createElement('code');
      p.className = 'path';
      p.textContent = path;
      row.append(m, p);
      if (op.security) {
        const tag = document.createElement('span');
        tag.className = 'tag';
        tag.textContent = 'API key';
        row.append(tag);
      }
      const h = document.createElement('h2');
      h.textContent = op.summary;
      const d = document.createElement('p');
      d.className = 'desc';
      d.textContent = op.description;
      section.append(row, h, d);
      ops.append(section);
    }
  }
  document.getElementById('foot').textContent =
    'Authenticate with the unified API key from the dashboard. Version ' + spec.info.version + '.';
})();
</script>
</body>
</html>
`
