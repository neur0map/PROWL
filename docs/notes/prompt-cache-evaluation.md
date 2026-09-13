# Prompt, cache, retrieval, and workflow evaluation

This records the implementation checks and paired live evaluation performed on
2026-09-13. It separates deterministic contract verification from model-answer
quality and provider-dependent cost observations. Configuration and supported
cache controls are described in [the config guide](../config/README.md#prompt-cache-controls).

## Paired live evaluation

The baseline executable was captured after the accounting pass, before the
prompt/cache/retrieval/workflow changes. The candidate includes those changes
and a general evidence-calibration rule distinguishing “not established” from
“ruled out.” No HTTP/authentication-specific instruction was added for the
failure fixture.

Each condition used a fresh, matching temporary Go repository and private
config/data directories. Both model roles were explicitly pinned to
`openrouter/anthropic/claude-haiku-4.5`; the observed request logs confirmed the
model. Configuration requested temperature 0 and an 8192-token output limit,
with no reasoning-effort override. Normal coding tools were enabled; network
and delegation tools were disabled. Focus mode was not enabled. These were
real provider calls, not replayed responses.

### Fixtures and independent criteria

- **Coding:** `Entry.Usable(now)` incorrectly treated the exact `ExpiresAt`
  instant as usable. The criterion was usable immediately before expiry and
  unusable at/after it, with the public API preserved. A separate consumer test
  was run after each model finished; a model could not pass by changing its own
  test expectation.
- **Investigation:** `RequestTimeout` delegates to an environment/project/global
  timeout resolver. Empty environment input must yield the project value, 2s;
  nonempty invalid input must return a parse error rather than silently fall
  back. The answer needed source citations and executed checks, without changes
  to the existing fixture files.
- **Failure:** a script prints a static `request failed: EOF` log and exits 1.
  The wrapper and log do not establish an authentication root cause, nor do
  they prove that authentication can be excluded in an unseen caller/server.
  The answer needed to distinguish those claims and stay within the fixture's
  evidence scope.

| Scenario | Baseline outcome | Candidate outcome | Baseline cost | Candidate cost | Requests, baseline → candidate |
| --- | --- | --- | ---: | ---: | ---: |
| Coding | Failed independent boundary check; changed its own test to accept the bug | Passed independent boundary check | $0.146877 | $0.083182 | 11 → 9 |
| Investigation | Correct precedence/error explanation; fixture unchanged | Correct precedence/error explanation; fixture unchanged | $0.072104 | $0.090244 | 6 → 9 |
| Failure | Did not pass strict uncertainty/scope review | Did not pass strict uncertainty/scope review | $0.241171 | $0.177164 | 16 → 16 |
| **Total** | **1/3 strict criteria passed** | **2/3 strict criteria passed** | **$0.460152** | **$0.350590** | **33 → 34** |

Costs include all observed requests, including title generation, and match the
persisted root-session ledger within floating-point tolerance. All priced
Haiku attempts reported complete, persisted provider costs. The sample's total
cost was **23.8% lower**, but investigation cost increased. Total input tokens
were 433,417 → 320,745; output tokens were 5,347 → 5,969. No cache reads or writes
were reported in this Haiku sample, so its cost difference must not be sold as
measured cache-hit savings.

For the coding fixture's first conversation request, system instructions were
23,664 → 7,427 bytes (**68.6% smaller**). Tool definitions were 24,072 → 24,448
bytes; new tool parameters are not free. These are serialized component byte
counts, not provider-tokenizer counts or whole-conversation savings.

Both failure answers still overstated what the sparse log excluded, and both
looked outside the fixture for additional context. Prompt instructions are not
a filesystem sandbox or a guarantee of calibrated reasoning. The narrower
candidate prompt did not solve that limitation. One run per scenario/model,
non-equivalent successful coding outcomes, live routing, and uncontrolled
latency prevent a general quality, speed, or cost claim.

### Pilot and unavailable route

An earlier pilot requested DeepSeek through configuration but actually used
Sonnet for conversations and Haiku for titles; request fingerprints exposed
the mismatch. It is not counted as a DeepSeek evaluation. Its observed costs
are retained here rather than discarded:

| Sonnet/Haiku pilot | Baseline | Candidate |
| --- | ---: | ---: |
| Coding | $0.08297785 | $0.05926620 |
| Investigation | $0.09242250 | $0.07125985 |
| Failure | $0.08942575 | $0.13873010 |
| Total | $0.26482610 | $0.26925615 |

Both pilot versions passed coding/investigation but overclaimed in the failure
answer. The candidate's total cost was higher. A subsequent explicitly pinned
DeepSeek route returned “No endpoints found.” Its six observed failed attempts
had no complete usage/cost report; zero recorded charges are **not** proof that
those attempts were free. The final comparison therefore used the reachable,
explicitly pinned Haiku model instead. Published results contain no credentials;
the temporary evaluation configuration and histories are removed after scoring.

## Deterministic and runtime verification

These checks prove the exercised contracts, not universal provider behavior:

- **Accounting:** atomic owner/ancestor updates survive concurrent charges and
  stale session saves. Root-only statistics avoid counting child charges twice.
  Live attempt totals matched the ledger for all twelve completed pilot/final
  runs. Missing wire prices differ from explicit zero prices; subscription
  marginal cost differs from API-equivalent value.
- **Auxiliary shutdown:** a fast conversation can finish while its title is in
  flight. Cancellation/shutdown drains title billing before returning. The
  partial-title regression persisted the expected $0.0102, without counting
  background titles as interactive busy work.
- **Streaming diagnostics:** debug on/off preserves streaming; diagnostics keep
  byte counts and fingerprints, not request/response bodies or credentials.
  Oversized usage events do not truncate the model stream and mark the bill
  incomplete when appropriate.
- **Provider protocols:** synthetic native OpenAI, Anthropic, router, Bedrock,
  and Gemini paths exercised supported cache controls, unsupported combinations,
  ordered tools, manual-routing precedence, inherited-off policy, and usage
  normalization. Native Astra low → max → low settings survived reload, with a
  new baseline after compaction; subscription paths retained their supported
  reasoning behavior without public-API-only cache fields.
- **Gemini leases:** creation/restart reuse, expiry, changed-prefix replacement,
  below-minimum fallback, one uncached retry for a missing managed resource, and
  authentication-error propagation were exercised. Three fixed leases plus five
  generation attempts totaled $0.03045 in the synthetic rate fixture. Concurrent
  duplicate lease saves charged the owner and ancestor once. These are configured
  rate calculations, not live Gemini invoices.
- **Retrieval:** budgets covered complete JSON/TOON/Markdown/text output and the
  complete MCP wrapper. Stale symbol IDs and unresolved spans did not return
  misleading source. Long queries, low-signal tokenizer files, cancellation,
  and recoverable oversized output were exercised. Two real-repository prompt
  assembly queries returned 6,476/6,704 bytes, estimated as 1,619/1,676 tokens,
  within their 1,800-token budget. These estimates use bytes, not a model tokenizer.
- **Focus lifecycle:** actual CLI activation, resume, explicit off, TUI command
  selection/badge, and TUI compaction were exercised. System/tool hashes stayed
  unchanged across style transitions. Post-compaction history established one
  fresh focus baseline. Server-backed CLI activation and restart/resume/off
  preserved the same session and correctly retained/replaced the preference.
- **Review safety:** the actual builtin Hindsight remained user-triggered. The
  `learn` tool emitted pending proposal receipts, supported same-topic updates,
  rejected unresolved anchors, and did not install accepted knowledge. A stale
  competing update could not overwrite a topic accepted in the disposable
  review fixture. Upstream MIT notices are retained in [NOTICE](../../NOTICE.md).

## Repository checks

The final full run passed **66 Go packages**, with **46 packages having no
tests**, under the race detector. The repository executable was built and run.
The runtime focus controls were also exercised through the installed candidate
TUI and the live HTTP API: valid updates returned 200; invalid or missing modes
returned 400 without changing the stored preference.

```sh
CGO_ENABLED=1 GOEXPERIMENT=greenteagc GOFLAGS=-tags=sqlite_fts5 \
  go test -race -count=1 ./... -timeout 300s
CGO_ENABLED=1 GOEXPERIMENT=greenteagc GOFLAGS=-tags=sqlite_fts5 \
  go build -o prowl .
```

Go formatting, log-capitalization checks, SQLC generation, config schema, and
Swagger generation were performed. Targeted static analysis of the changed
agent/log/session/message/proto/config/shellconfig/bridge/context/query packages
reported **0 issues** with golangci-lint v2.13.2.

Full-repository lint is **not clean**: 87 findings remain, principally in the
existing OAuth and embedded-engine work (body ownership, context propagation,
SQL cleanup, deprecations/style, and one whitespace finding). They were not
suppressed or silently excluded from the global run. Prowl doctor reported no
structural errors; its score remained 6/100 with existing file-size/churn
findings. This work does not claim a clean global static-analysis baseline.
