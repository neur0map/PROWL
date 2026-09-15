# Prowl Development Guide

## Project Overview

Prowl is the coding and daily Linux work harness for Ryoku Arch. It is written
in Go and brings models, project rules, local tools, and saved sessions into one
terminal app. Its main goal is to help small and mid-sized models do reliable
work with less context.

Prowl embeds the `prowl-agent` engine to map projects and retrieve bounded,
cited source. The standalone CLI is a separate development companion. Prowl
also supports hosted and local models, language servers, MCP servers, hooks,
and skills.

The module path is `github.com/neur0map/prowl`.

## Architecture

```
main.go                            CLI entry point (cobra via internal/cmd)
.agents/skills/                    Meta-skills used by Prowl for its own work
                                  (code-search, pr-review, change-safety,
                                  issue-resolution, durable-knowledge, etc.)
internal/
  app/app.go                       Top-level wiring: DB, config, agents, LSP, MCP, events
  backend/                         Inner app state (agent, session, events,
                                  permission, skills) wired into UI and server
  cmd/                             CLI commands (root, run, login, models, stats, sessions)
  commands/                        User-facing slash commands
  config/
    config.go                      Config struct, context file paths, agent definitions
    load.go                        prowlrc and prowl.json loading and validation
    provider.go                    Provider configuration and model resolution
  client/                          IPC client that talks to a running server
  server/                          Headless server (long-running daemon;
                                  socket classify, recover, e2e tests)
  proto/                           Wire types shared between client and server
  workspace/                       Workspace ownership model across apps and clients
  shellconfig/                     Bash-powered config format (prowlrc builtins)
  agent/
    agent.go                       SessionAgent: runs LLM conversations per session
    coordinator.go                 Coordinator: manages named agents ("coder", "task")
    hooked_tool.go                 Decorator that runs PreToolUse hooks before tool execution
    prompts.go                     Loads Go-template system prompts
    templates/                     System prompt templates (coder.md.tpl, task.md.tpl, etc.)
    tools/                         All built-in tools (bash, edit, view, grep, glob, etc.)
      mcp/                         MCP client integration
    hyper/                         Embedded Hyper provider definition (provider.json)
  hooks/                           Hook engine: runs user shell commands on hook events
    hooks.go                       Decision types, aggregation logic, event constants
    runner.go                      Parallel hook execution, timeout, dedup
    input.go                       Stdin payload builder, env vars, stdout parsing (Prowl + Claude Code compat)
  oauth/                           OAuth flows (Hyper device, Copilot, MCP)
  discover/                        Local model discovery (ollama, lmstudio, llamacpp, omlx, litellm)
  herdr/                           Integration with the Herdr service (translate, client)
  session/session.go               Session CRUD backed by SQLite
  message/                         Message model and content types
  db/                              SQLite via sqlc, with migrations
    sql/                           Raw SQL queries (consumed by sqlc)
    migrations/                    Schema migrations
  lsp/                             LSP client manager, auto-discovery, on-demand startup
  ui/                              Bubble Tea v2 TUI (see internal/ui/AGENTS.md)
  permission/                      Tool permission checking and allow-lists
  skills/                          Skill discovery; builtin/* is go:embed'd into the binary
  shell/                           Bash command execution with background job support
  event/                           Telemetry (PostHog)
  pubsub/                          Internal pub/sub for cross-component messaging
  filetracker/                     Tracks files touched per session
  history/                         Prompt history
  swagger/                         OpenAPI spec for the HTTP server
```

### Key Dependency Roles

- **`charm.land/fantasy`**: LLM provider abstraction layer. Handles protocol
  differences between Anthropic, OpenAI, Gemini, etc. Used in `internal/app`
  and `internal/agent`.
- **`charm.land/bubbletea/v2`**: TUI framework powering the interactive UI.
- **`charm.land/lipgloss/v2`**: Terminal styling.
- **`charm.land/glamour/v2`**: Markdown rendering in the terminal.
- **`charm.land/catwalk`**: Snapshot/golden-file testing for TUI components.
- **`sqlc`**: Generates Go code from SQL queries in `internal/db/sql/`.

### Key Patterns

- **Config is a Service**: accessed via `config.Service`, not global state.
- **Tools are self-documenting**: each tool has a `.go` implementation and a
  `.md` description file in `internal/agent/tools/`.
- **System prompts are Go templates**: `internal/agent/templates/*.md.tpl`
  with runtime data injected.
- **Context files**: Prowl reads AGENTS.md, PROWL.md, CLAUDE.md, GEMINI.md
  (and `.local` variants) from the working directory for project-specific
  instructions.
- **Bash config format**: Prowl's primary config format is `prowlrc` — a
  Bash script using builtins (`provider`, `model`, `mcp`, `lsp`,
  `permissions`, `hook`, `options`) to define config. `prowl.json` is still
  supported but is deprecated in favor of `prowlrc` and may be removed in a
  future release. Shell config files are discovered alongside JSON configs
  and deep-merged through the same pipeline. Builtins are registered via
  `shell.RegisterBuiltin` and gated by a `ConfigBuilder` on the context —
  they are no-ops during normal bash tool execution. See
  `internal/shellconfig/`.
- **Persistence**: SQLite + sqlc. All queries live in `internal/db/sql/`,
  generated code in `internal/db/`. Migrations in `internal/db/migrations/`.
- **Pub/sub**: `internal/pubsub` for decoupled communication between agent,
  UI, and services.
- **Hooks**: User-defined shell commands in `prowlrc` (or `prowl.json`)
  that fire before tool execution. The engine (`internal/hooks/`) is
  independent of fantasy and agent — it takes inputs, runs commands,
  returns decisions. The `hookedTool` decorator in
  `internal/agent/hooked_tool.go` wraps tools at the coordinator level.
  Hooks run before permission checks. See `HOOKS.md` for the user-facing
  protocol.
- **CGO enabled**: builds with `CGO_ENABLED=1`, `GOEXPERIMENT=greenteagc`, and
  `-tags=sqlite_fts5` (set via `GOFLAGS`). CGO is required because the
  prowl-agent code-intelligence engine is now vendored in-process under
  `internal/paengine/` (SQLite + sqlite-vec + tree-sitter). A pure
  `CGO_ENABLED=0` build no longer compiles the full module.
- **Reserved: local tool router (Cactus Needle).** Room is intentionally left
  for a future in-process router — Cactus Needle, a 26M single-shot
  function-calling model — that pre-selects the best-fit tool for a user query
  before the main model runs, to cut tokens and agent workload. It is NOT
  implemented yet (Needle works best fine-tuned on prowl's own tool catalog
  first). The plug-in point is the coordinator run path
  (`internal/agent/coordinator.go`): it should consume the active tool registry
  and emit an advisory tool hint, never a hard gate.

## Non-obvious gotchas

These are easy to miss from a single-file read and lead to long debugging
sessions when wrong. New agents usually stumble on them at least once.

- **CLI versus embedded engine.** The development commands `prowl-agent
  overview`, `find`, `def`, `references`, and `impact` use a separately
  installed CLI. Prowl's runtime tools use the engine vendored under
  `internal/paengine/` through `internal/prowlagent/`; they do not spawn that
  binary. Install the CLI for development queries, not as a runtime dependency.
- **Search routing is enforced in the shell, not just described.** Three
  separate places told the model to route structural questions to the index
  and it still ran `grep -rn` over a subtree — once in the same turn as a
  `prowl_agent` call that had already answered. `SearchRouterGuard`
  (`internal/agent/tools/search_router.go`) refuses a tree-scanning search
  before it executes, via the `shell.Guard` seam, and returns the tool to use
  instead. Guards differ from `BlockFunc`: a block is a security deny with one
  fixed message, a guard supplies its own. Pipelines (`cmd | grep x`),
  single-file greps and a `find` with predicates glob cannot express are
  deliberately untouched — a refusal that blocks real work teaches the model
  to fight the tool.
- **`prowl://skills/...` is virtual, not on disk.** Built-in skills are
  embedded into the binary from `internal/skills/builtin/*.md` via
  `//go:embed` in `internal/skills/embed.go`. The embedded FS exposes them
  under the prefix `prowl://skills/<name>/SKILL.md`. The View tool resolves
  this prefix natively; passing it to anything else (curl, MCP, etc.) will
  fail. User skills with the same name on disk override the embedded one.
- **`.agents/skills/*` are meta-skills for Prowl itself, not user skills.**
  `code-search`, `prowl-pr-review`, `prowl-change-safety`, etc. are
  instructions Prowl uses to do its own development work. They are
  distinct from `internal/skills/builtin/*`, which are skills Prowl
  exposes to end users. Don't conflate them.
- **Crush vs Prowl.** Prowl began as a Crush fork but is now independent
  (see `NOTICE.md`, `README.md`). Don't try to merge or chase Crush
  upstream; cherry-pick security fixes only (see `CONTRIBUTING.md`).
- **System-prompt verbatim matters.** VCR cassettes under
  `internal/agent/testdata/TestCoderAgent/**` encode the full Prowl system
  prompt. Wholesale edits to `internal/agent/templates/*.tpl` or to
  `internal/agent/prompts.go` invalidate cassettes and require a re-record
  (`task test:record`).
- **`PROWL_VERSION` is the literal string `devel` for unreleased builds.**
  `prowlrc` scripts can feature-detect it (see the `prowl-config` skill).
- **Task variables.** `task run CLI_ARGS="--help"` forwards extra args to
  the built binary. `RACE=1` turns on `-race` for `task build` / `task run`
  and tees stderr to `race.log` (the file's presence keeps race mode on).
- **Schema regeneration.** `task schema` regenerates `schema.json` from
  the live config types. Bump it whenever fields, defaults, or tags move.

## Build/Test/Lint Commands

The Taskfile sets the native-engine build environment. For direct Go commands,
set it explicitly:

```sh
export CGO_ENABLED=1 GOEXPERIMENT=greenteagc GOFLAGS=-tags=sqlite_fts5
```

- **Build**: `task build`, `go build .`, or `go run .`
- **Test**: `task test` or `go test -race ./...` (focused example:
  `go test -race ./internal/session`)
- **Update Golden Files**: `go test ./... -update` (regenerates `.golden`
  files when test output changes)
- **Lint**: `task lint:fix`
- **Format**: `task fmt` (`gofumpt -w .`)
- **Modernize**: `task modernize` (runs `modernize` which makes code
  simplifications)
- **Dev**: `task dev` (runs with profiling enabled)

## Code Style Guidelines

- **Imports**: Use `goimports` formatting, group stdlib, external, internal
  packages.
- **Formatting**: Use gofumpt (stricter than gofmt), enabled in
  golangci-lint.
- **Naming**: Standard Go conventions — PascalCase for exported, camelCase
  for unexported.
- **Types**: Prefer explicit types, use type aliases for clarity (e.g.,
  `type AgentName string`).
- **Error handling**: Return errors explicitly, use `fmt.Errorf` for
  wrapping.
- **Context**: Always pass `context.Context` as first parameter for
  operations.
- **Interfaces**: Define interfaces in consuming packages, keep them small
  and focused.
- **Structs**: Use struct embedding for composition, group related fields.
- **Constants**: Use typed constants with iota for enums, group in const
  blocks.
- **Testing**: Use testify's `require` package, parallel tests with
  `t.Parallel()`, `t.SetEnv()` to set environment variables. Always use
  `t.Tempdir()` when in need of a temporary directory. This directory does
  not need to be removed.
- **JSON tags**: Use snake_case for JSON field names.
- **File permissions**: Use octal notation (0o755, 0o644) for file
  permissions.
- **Log messages**: Log messages must start with a capital letter (e.g.,
  "Failed to save session" not "failed to save session").
  - This is enforced by `task lint:log` which runs as part of `task lint`.
- **Comments**: End comments in periods unless comments are at the end of the
  line.

## Testing with Mock Providers

When writing tests that involve provider configurations, use the mock
providers to avoid API calls:

```go
func TestYourFunction(t *testing.T) {
    // Enable mock providers for testing
    originalUseMock := config.UseMockProviders
    config.UseMockProviders = true
    defer func() {
        config.UseMockProviders = originalUseMock
        config.ResetProviders()
    }()

    // Reset providers to ensure fresh mock data
    config.ResetProviders()

    // Your test code here - providers will now return mock data
    providers := config.Providers()
    // ... test logic
}
```

## Formatting

- ALWAYS format any Go code you write.
  - First, try `gofumpt -w .`.
  - If `gofumpt` is not available, use `goimports`.
  - If `goimports` is not available, use `gofmt`.
  - You can also use `task fmt` to run `gofumpt -w .` on the entire project,
    as long as `gofumpt` is on the `PATH`.

## Comments

- Comments that live on their own lines should start with capital letters and
  end with periods. Wrap comments at 78 columns.

## Committing

- ALWAYS use semantic commits (`fix:`, `feat:`, `chore:`, `refactor:`,
  `docs:`, `sec:`, etc).
- Try to keep commits to one line, not including your attribution. Only use
  multi-line commits when additional context is truly necessary.

## Working on the TUI (UI)

Anytime you need to work on the TUI, read `internal/ui/AGENTS.md` before
starting work.

## Scoped development guides

Other directories contain narrower guides that override or complement this
file for their subsystem. Read them before editing inside those paths:

- `internal/ui/AGENTS.md` — Bubble Tea v2 / hybrid rendering rules
  (no `Update` IO, no nested models, use `ansi` package, layout in
  `model/ui.go`, draw via `uv.ScreenBuffer`).
- `internal/cmd/stats/AGENTS.md` — the stats web UI assets.
- `internal/oauth/callback/AGENTS.md` — the OAuth callback landing page.

## Styling System

The styling system lives in `internal/ui/styles/` and is organized into
three layers:

- **`quickstyle.go`**: The stable base theme builder. `quickStyle(opts)`
  constructs a `Styles` struct from `quickStyleOpts` — a palette of
  design tokens (primary, secondary, fgBase, bgBase, success, error, etc.).
  `quickStyle` must be fully token-driven: never hardcode specific
  `charmtone.*` colors here (except Chroma syntax highlighting, which is
  pending tokenization). This lets any theme reuse the base without
  inheriting Ryokutone-specific colors.
- **`themes.go`**: Defines concrete themes. Each theme function (e.g.
  `RyokutonePantera`) calls `quickStyle` with its palette, then applies
  theme-specific overrides as needed.
- **`styles.go`**: Defines the `Styles` struct and its documentation —
  the shape of what `quickStyle` produces.

**Adding theme-specific overrides**: When a style genuinely needs a
color that doesn't fit the token model (e.g. the bang prompt uses
Salt/Hazy/Larple), keep `quickStyle` on the closest semantic token and
override only the differing colors in the theme function:

```go
func RyokutonePantera() Styles {
	s := quickStyle(quickStyleOpts{ /* palette */ })

	// Override only the colors that differ from the token defaults.
	s.Editor.PromptBangIconFocused = s.Editor.PromptBangIconFocused.
		Foreground(charmtone.Salt).
		Background(charmtone.Hazy)

	return s
}
```

**Adding a new theme**: Add a function in `themes.go` that returns the
result of `quickStyle` with a `quickStyleOpts` palette (plus any needed
overrides), then wire it into `ThemeForProvider`.

<!-- prowl-agent -->
## Prowl project context

This repo has a Prowl index of its files, symbols, and how they connect. For any
semantic or structural question -- where code is, what it does, who calls it, or
what a change touches -- **run the read-only prowl-agent CLI first**; do not grep
or read whole files just to locate things. Prowl reindexes what changed before
each query, so answers stay current and are cited to file:line, returned in one
call instead of a grep hit list you then open files to disambiguate.

| Question | First command |
|---|---|
| Map the repository | `prowl-agent overview` |
| Locate a feature or concept | `prowl-agent search "<question>"` |
| Locate a named symbol | `prowl-agent find <name>` |
| Read one symbol's source | `prowl-agent def <name-or-id>` |
| Inspect a file's structure | `prowl-agent outline <path>` |
| Trace who uses a symbol | `prowl-agent references <name-or-id>` |
| Size a change's blast radius | `prowl-agent impact <path>` |
| Inspect uncommitted work | `prowl-agent wip` / `prowl-agent changed` |
| Read a located line range | `prowl-agent peek <file:start-end>` |

Keep grep for exact literal or regex text and glob for filename patterns. CLI
output is token-lean TOON by default; add --format human|toon|json|markdown. If
your harness also wires Prowl as an MCP server, the same index is reachable
there; the CLI needs no server and is the first choice.
<!-- /prowl-agent -->

<!-- prowl-agent:map -->
## Prowl project map

Auto-generated from the Prowl index, refreshed on each `overview`/`init`. Prefer retrieving from Prowl (and reading the cited files) over grepping or relying on training memory; this is the current shape of the repo.

- size: 1087 files, 110790 symbols, 18566 edges (resolved 13117, external deps 5148, unresolved 301)
- languages: go:979 markdown:58 yaml:34 json:9 bash:2 css:2 javascript:2 typescript:1
- subsystems: internal/paengine(229,go) · internal/ui(188,go) · internal/agent(117,go) · internal/config(38,go) · internal/oauth(23,go) · internal/cmd(22,go) · internal/backend(18,go) · internal/server(18,go)
- entrypoints: internal/agent/agenttest/coordinator.go · internal/paengine/internal/revieweval/run.go · main.go · internal/paengine/internal/revieweval/prepare.go · internal/ui/logo/example/main.go · internal/paengine/internal/agenteval/eval.go · internal/paengine/internal/revieweval/model.go
- central files (most depended-on): internal/ui/styles/grad.go · internal/ui/styles/quickstyle.go · internal/ui/styles/styles.go · internal/ui/styles/themes.go · internal/pubsub/broker.go
- read these guides first: README.md · AGENTS.md · CONTRIBUTING.md

Depth on demand: `prowl-agent find|def|outline|references <name>`, `search <text>`, `context search "<question>"`, `sketch <ui>`.
<!-- /prowl-agent:map -->
