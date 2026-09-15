# Prowl Gateway

One local proxy in front of every LLM provider you have access to. You paste
API keys once in a dashboard; the gateway keeps them encrypted, picks a
provider and model per request from live evidence, and fails over when one is
rate-limited, out of credit, or simply down.

The routing engine and the dashboard are ported from
[FreeLLMAPI](https://github.com/tashfeenahmed/freellmapi) (MIT, Copyright (c)
2026 Tashfeen Ahmed) and reimplemented in Go — see `NOTICE.md`. Prowl ships as
one binary: no Node, no Docker, nothing to install alongside it.

## Start it

```bash
prowl gateway
```

That binds `127.0.0.1:8787` and `[::1]:8787`, opens the dashboard, and
registers itself as a provider in your global config so the model picker
offers it. The harness also serves the gateway for as long as a Prowl session
is open, so `/gateway` in the TUI opens the same dashboard without a second
command.

| Flag | Effect |
|---|---|
| `--port N` | Listen elsewhere (loopback only, always) |
| `--no-open` | Do not open a browser, for a headless box |
| `--register=false` | Do not touch `prowl.json` |
| `--quiet` | Log to the file only, not to this terminal |

Several Prowl windows on one machine share a single gateway: the first to bind
the port owns it, the rest attach after probing that it really is a Prowl
gateway. That keeps one usage ledger, one set of cooldowns and one reliability
history — per-window engines would each have to rediscover that a provider is
out of credit, and would each learn a different answer.

## First run

The dashboard is protected by an account, because it holds provider
credentials and can spend their quota.

On first launch the gateway prints a one-time setup code:

```
Gateway first-run setup code: EC5TAJTSQ6
```

A browser **on this machine** can create the account without it — whoever is
at the keyboard has already proved more than a code would. A request from any
other address must present it, which is what stops a reachable fresh install
from being claimed by whoever finds it first. The code is logged, never
served: a code an unauthenticated caller could fetch would protect nothing.

### Where things are

Five destinations, one question each:

| Page | Answers |
|---|---|
| **Models** | What can serve me? The whole catalogue — search, filter by cost/capability/context/provider, and a switch per model that turns it on for the router. A model you have no key for is still listed, with a link to add one. |
| **Providers** | Who serves it, and how well? Every provider the gateway knows, with your key's state, how many of its models are routable, and its measured latency and reliability. |
| **Routing** | How does it choose? Strategy and weights, named lists, the order the router walks, budgets, and current pressure. |
| **Activity** | What did it do? Requests, tokens and cost over time, the request log, and the quota each provider reports. |
| **Playground** | Does it work? A chat window against your own gateway. |

A model's own page lists every provider that serves it, side by side: price in
and out, context, latency, throughput, reliability, key state, and a switch
per provider — so "use this model, but not through that provider" is one
click rather than a config edit.

## Three credentials, deliberately separate

| Credential | Guards | Notes |
|---|---|---|
| Dashboard account | `/api/*` management surface | email + password, scrypt, 30-day sessions stored as hashes |
| Unified API key | `/v1` inference plane | `prowl-gate-<48 hex>`, shown in the dashboard, regenerate takes effect immediately |
| Machine-local token | first dashboard load | minted on launch so a freshly started harness can open its own dashboard |

They are not interchangeable on purpose. An application key that leaks must
not also be able to reconfigure the gateway, and a leaked dashboard session
must not become an inference credential.

One consequence worth knowing: the dashboard ends its session on a `401` that
carries `authentication_error` and on nothing else. Every other `401` in the
system — a provider rejecting a key you are testing, an application calling
`/v1` with a stale key — uses a different type, so an unrelated failure can
never sign you out.

## How a request is routed

A candidate is not picked by one measurement. Three normalised axes are
combined convexly, then damped by multiplicative guardrails:

```
base      = w_rel·reliability + w_speed·speed + w_intel·intelligence
effective = base × headroomFactor × rateLimitFactor
```

- **Reliability** is Thompson sampling over a Beta posterior built from a
  7-day window of your own traffic with a 2-day half-life. The draw's variance
  *is* the exploration: an untried model occasionally wins and earns the data
  that would rank it, with no separate schedule to tune. The dashboard shows
  the posterior mean instead, because a number that moves when nothing changed
  is unreadable.
- **Speed** blends measured throughput with time-to-first-byte. An unmeasured
  model gets an optimistic prior, or the fastest provider could never be
  discovered.
- **Intelligence** is tier-first with a square-root-compressed rank, so rank
  refines an order inside a tier but can never lift a small model above a
  frontier one.
- **Guardrails multiply rather than reorder.** A better model stays ahead of a
  worse one until it is actually short of quota, which is why quota pressure
  demotes gradually instead of flipping the order.

Strategies: `balanced` (default, 0.5/0.25/0.25), `smartest`, `fastest`,
`reliable`, `custom` weights, or `priority` for a hand-ordered chain.

## When a provider fails

The chain is walked up to 20 hops. The 45-second budget bounds *churn*, not
total time: the single most expensive attempt is excused, because an agent
turn can legitimately stream for minutes inside one honest attempt and
charging that to the budget left the chain unable to fail over at all.

What went wrong decides what gets benched and for how long.

| Failure | Scope | Bench |
|---|---|---|
| 401 invalid key | that key | 5 min, plus an immediate re-probe |
| 402 out of credit | that key | 24 h — credit does not appear on retry |
| 403 tier | that model | 24 h |
| 429 daily exhausted | model on that key | to the provider's reset |
| 429 transient | model on that key | ladder: 2m → 10m → 1h → 24h |
| 5xx / unreachable edge | the whole **platform** | ladder |
| cut connection | model on that key | ladder |

The 5xx row surprises people. A 5xx is evidence about the *provider*, so
retrying a sibling model on a provider that is down just spends the budget
rediscovering the outage. A **cut** connection is different: the edge answered
and then the stream died, which indicts the model that was serving it — an
aggregator relaying "upstream overloaded" for one model says nothing about the
rest of its catalogue.

Go's transport vocabulary is what decides that: `unexpected EOF`, `connection
reset by peer` and `broken pipe` are cuts, while `connection refused`, DNS
failure and TLS errors mean the edge is unreachable. Both fail over; only the
second benches the platform.

Two rules govern how long a bench lasts. A provider's own `Retry-After` is a
**floor**, never a cap, and is marked authoritative so nothing later shortens
it. Our own guesses are capped — an inferred exhaustion with no published
daily limit tops out at 10 minutes, because a wrong guess must not cost hours.
Local endpoints (loopback, private ranges) get 5 seconds and never escalate: a
restarted Ollama should not look like a broken gateway.

Benches are persisted, so restarting the harness does not hand a rate-limited
key straight back to the router.

A model is benched across every key only after 3 failures in 15 minutes, and a
model penalty is applied only when **no sibling key** could have served it — a
usable sibling means the model is not what failed.

A stream that commits and then fails cannot be rerouted — the bytes are gone —
so it is recorded as an error with the upstream reason rather than booked as a
success. The response commits on the first frame carrying real content, not on
a provider's role-only opener, which is what keeps early failures reroutable.

Two failures are about the gateway rather than the provider. A candidate whose
platform has **no wire adapter** is stepped over and its platform excluded for
the rest of that request — no bench, no penalty, because nothing was asked. A
**fatal** verdict (a 400 for an unsupported parameter, say) ends the run and is
rendered as the provider stated it, with the provider and model recorded
against the request: reporting those as "every provider attempt failed" hid
requests that had in fact been dispatched and refused.

## Degraded mode

When most configured providers are unhealthy at once the cause is usually
local: no network, a captive portal, dead DNS. The gateway notices and stops
exploring unmeasured models, because in that state exploration spends the
retry budget proving the obvious *and* poisons the reliability of models that
were never at fault.

Both transitions are slow and asymmetric — 60 seconds to enter, 120 to leave —
so the gateway cannot oscillate at exactly the moment you are trying to
diagnose it. A fleet of fewer than three providers is never judged, since one
bad key out of two is 50% and would read as an outage.

## Quota

Four sliding windows per credential (RPM, TPM, RPD, TPD), persisted so a
restart cannot hand back quota a provider has already counted. A request
reserves its projected tokens **before** the upstream call and settles to the
real number afterwards, so concurrent requests cannot each see the same
headroom and collectively blow a limit.

Per-model daily windows slide over a trailing 24 hours; provider-account caps
reset at UTC midnight, because that is what those providers actually do.

The gateway also reads each provider's own rate-limit headers and shows that
alongside its own counters. When the two disagree, the disagreement is the
interesting part.

## Authorisation

Loopback is not a trust boundary: every user on the machine can reach
127.0.0.1. Nothing is served unauthenticated, including the dashboard page and
its assets.

| Caller | Credential |
|---|---|
| A browser, first load | `?token=…`, exchanged for an httpOnly `SameSite=Strict` cookie and redirected, dropping the token from the URL |
| The dashboard afterwards | that cookie; the page holds no credential in JavaScript |
| An OpenAI-compatible client | the unified key as a Bearer, i.e. in its api-key field |

Tokens are compared in constant time. Credentials are redacted from logs,
stored rows and error responses — providers routinely echo a rejected key back
in their error message, and that message becomes a log line that outlives the
session.

## State on disk

| Path | Contents |
|---|---|
| `<data>/gateway/gateway.db` | providers, keys, models, chains, profiles, quota, cooldowns, the request trail |
| `<data>/gateway/master.key` | the key that encrypts provider credentials (never in the database it protects) |
| `<data>/gateway/token` | the machine-local bootstrap credential |
| `<data>/gateway/gateway.log` | routing decisions and failovers |

The directory is `0700` and the files `0600`. Provider keys are AES-256-GCM
encrypted per row with the authentication tag pinned, so a truncated or
tampered ciphertext fails loudly rather than decrypting to garbage.

## Using it

Point anything OpenAI-compatible at the base URL with the unified key:

```
base url   http://127.0.0.1:8787/v1
api key    prowl-gate-…        (copy it from the dashboard)
model      auto                (or a sort axis / named list — see below)
```

`GET /v1/models` lists every id you can send, which is the point of asking it:

- `auto` — the active list, ranked by the configured strategy.
- `auto:smart`, `auto:fast`, `auto:cheap`, `auto:reliable`, `auto:balanced` —
  the whole enabled catalogue sorted on one axis, ignoring list order.
- `auto:<set>` — one of your own sets, by name.
- any concrete model id, which still fails over to the rest of the set.

### Sets

A **set** is a named, ordered group of models the router may use. You keep one
per kind of work and switch when the work changes: a set for the task you
care about, a cheaper one for chores.

Start from a preset, grouped by the question you are asking:

| For a kind of work | Requires |
|---|---|
| Deep work | tool calling, reasoning, 128K+ context |
| Coding | tool calling, 128K+ context |
| Writing | top 25% by capability, 32K+ context |
| Quick chores | free, or under $2 per million output tokens |
| Long documents | 200K+ context |
| Images in | accepts images |
| Local only | runs on this machine |

plus the catalogue cuts: everything free, paid models, my subscriptions,
flagships. Each preset is a live query, and the requirement list beside it is
rendered from the same predicate that fills it — so what a set contains and
what it claims to contain cannot drift.

Or curate one: filter the Models page, select the rows you want, and save the
selection as a set. Either way it becomes callable as `auto:<name>` and
appears in the set switcher above the model list.

```
GET  /api/profiles/presets          the presets, with live counts and requirements
POST /api/profiles/presets/{id}     build a set from one; {name, activate}
POST /api/profiles/from-selection   save a hand-picked selection; {name, modelDbIds, activate}
POST /api/profiles/active           switch the active set
```

In Prowl itself, pick **Prowl Gateway** in the model picker (`Ctrl+L`). It
offers `auto`, the four sort axes, and every set you have saved — so switching
set per task does not mean leaving the TUI. It is offered even before the
gateway has run, because selecting it mints the token and registers the
provider for you.

Response headers tell you what actually happened:

| Header | Meaning |
|---|---|
| `X-Routed-Via` | the provider and model that served you |
| `X-Fallback-Attempts` | how many earlier hops failed |
| `X-Fallback-Trail` | which ones, and why each failed |
| `Retry-After` | on a 429 exhaustion, when to come back |
| `X-Prism-Model-Id` / `-Name` | the same answer in the form the harness reads |

In the harness those last two become the footer on every turn: it shows
`Auto (smart routing) → kilo/openrouter/free`, so the model that actually
answered is never hidden behind the `auto` you selected.

## Borrowing your own logins

A Copilot or Claude subscription you have already signed in to is spendable
capacity, and the pool could not previously reach it. The **Providers** page
lists every provider Prowl holds a login for and enrolls one with a click.

The pool row stores a *reference*, never a copy of the token. The credential is
resolved on each request, so a refresh is picked up automatically and signing
out revokes the pool's access with it.

Enrolling also seeds what that login serves into the catalogue and appends it
to the chain, so the models appear on the Models page under **Prowl login**
and are selectable by the `subscriptions` preset. They are appended rather
than preferred: free capacity stays first until you order a list yourself.

Subscription budget is reported the way each provider states it — Anthropic
publishes rolling 5-hour and 7-day utilisation, Hyper a remaining credit
balance — so a pool with no published quota shows nothing rather than an
invented number.

```
GET    /api/logins              what can be borrowed, and what already is
POST   /api/logins/{id}/enroll  add it to the pool
DELETE /api/logins/{id}/enroll  take it back out
```

## Asking the gateway about itself

Two machine-readable endpoints sit behind the unified key, for a caller in
front of this gateway that wants to decide *before* spending a request:

```
GET /v1/providers        per-platform health, resume times, request headroom
GET /v1/quota-forecast   per-pool remaining, window reset, low-balance flag
```

The API reference is served from the binary, with no network and no CDN:

```
GET /v1/openapi.json     the spec
GET /v1/docs             a viewer for it
```

In the dashboard, the Keys page answers the same questions for a human: each
key shows what it carried, how many hops it refused, whether the router is
holding it back, and the provider's own words for why.
