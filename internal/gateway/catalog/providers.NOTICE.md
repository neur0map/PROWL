# Provider catalog attribution

`providers.json` is generated, not hand-written. It merges two MIT-licensed
upstream sources and deduplicates them by API endpoint.

## Sources

| Source | Used for | License |
|---|---|---|
| [open-free-llm-api/awesome-freellm-apis](https://github.com/open-free-llm-api/awesome-freellm-apis) | Free-tier providers: base URLs, API-key signup links, card/verification requirements, max context, modalities, and the curated best-free-model lists | MIT |
| [BerriAI/litellm](https://github.com/BerriAI/litellm) | Additional provider endpoints and their conventional API-key environment variable names | MIT |

Both upstreams are MIT licensed. Their copyright notices are reproduced in
`NOTICE.md` at the repository root.

## What the generation does

1. Parses the marker-delimited tables in the freellm `README.md`
   (`PERMANENT_FREE`, `RENEWABLE`, `QUICK_REF`, `BEST_MODELS`) into provider
   records with their free-model lists.
2. Parses litellm's `get_llm_provider_logic.py` for provider endpoints and
   `API_KEY` environment variable names, and adds any endpoint the freellm data
   does not already cover.
3. Drops entries with no endpoint (upstream ships a few placeholder rows that
   carry neither a base URL nor a key link, which cannot be routed to).
4. Drops providers whose authentication is SDK-specific rather than a bearer
   key — Bedrock, SageMaker, Vertex AI, Azure — because the gateway's premise
   is base URL plus API key. They are better served by their own SDKs.
5. Deduplicates by normalised base URL. Upstream lists some endpoints twice
   under different names (for example `xAI` and `Grok (xAI)` both resolve to
   `https://api.x.ai/v1`); the richer record wins, the other's models are
   folded in, and the discarded name is kept in `aliases` so a search for it
   still resolves.
6. Classifies each endpoint's wire format in `compat` and records any
   `{placeholder}` in the base URL as `requires_vars`. Only
   `compat: openai` entries with no unfilled placeholders are routable; the
   rest are listed in the dashboard with an explicit badge rather than offered
   as one-click setups that would fail on the first request.

## Current contents

- 47 providers, one per unique endpoint
- 28 with a standing free tier
- 41 routable as OpenAI-compatible endpoints today
- 6 listed but not routable: Google Gemini, Cohere, Cloudflare Workers AI,
  Ollama Cloud, AI21 Labs, Soniox
- 82 curated free models carrying concrete model ids

## Refreshing

The upstream free-tier data changes often — rate limits move and free models
come and go. Re-run the generation against both upstreams and review the diff;
the dedup and classification steps above are deliberate and must be preserved.
Treat `compat` and `requires_vars` as reviewed fields: a new provider defaults
to non-routable until someone confirms its wire format.
