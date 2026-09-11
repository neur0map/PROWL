# Prowl Development Guide

## Project Overview

Prowl is the coding and daily Linux work harness for Ryoku Arch. It is written
in Go and brings models, project rules, local tools, and saved sessions into one
terminal app. Its main goal is to help small and mid-sized models do reliable
work with less context.

Prowl and `prowl-agent` are separate projects. Prowl will use `prowl-agent`
natively to map a project before the model reads files. It also supports hosted
and local models, language servers, MCP servers, hooks, and skills.

The module path is `github.com/neur0map/prowl`.

## Architecture

```
main.go                            CLI entry point (cobra via internal/cmd)
internal/
  app/app.go                       Top-level wiring: DB, config, agents, LSP, MCP, events
  cmd/                             CLI commands (root, run, login, models, stats, sessions)
  config/
    config.go                      Config struct, context file paths, agent definitions
    load.go                        prowlrc and prowl.json loading and validation
    provider.go                    Provider configuration and model resolution
  shellconfig/                      Bash-powered config format (prowlrc builtins)
  agent/
    agent.go                       SessionAgent: runs LLM conversations per session
    coordinator.go                 Coordinator: manages named agents ("coder", "task")
    hooked_tool.go                 Decorator that runs PreToolUse hooks before tool execution
    prompts.go                     Loads Go-template system prompts
    templates/                     System prompt templates (coder.md.tpl, task.md.tpl, etc.)
    tools/                         All built-in tools (bash, edit, view, grep, glob, etc.)
      mcp/                         MCP client integration
  hooks/                           Hook engine: runs user shell commands on hook events
    hooks.go                       Decision types, aggregation logic, event constants
    runner.go                      Parallel hook execution, timeout, dedup
    input.go                       Stdin payload builder, env vars, stdout parsing (Prowl + Claude Code compat)
  session/session.go               Session CRUD backed by SQLite
  message/                         Message model and content types
  db/                              SQLite via sqlc, with migrations
    sql/                           Raw SQL queries (consumed by sqlc)
    migrations/                    Schema migrations
  lsp/                             LSP client manager, auto-discovery, on-demand startup
  ui/                              Bubble Tea v2 TUI (see internal/ui/AGENTS.md)
  permission/                      Tool permission checking and allow-lists
  skills/                          Skill file discovery and loading
  shell/                           Bash command execution with background job support
  event/                           Telemetry (PostHog)
  pubsub/                          Internal pub/sub for cross-component messaging
  filetracker/                     Tracks files touched per session
  history/                         Prompt history
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
- **CGO disabled**: builds with `CGO_ENABLED=0` and
  `GOEXPERIMENT=greenteagc`.

## Build/Test/Lint Commands

- **Build**: `go build .` or `go run .`
- **Test**: `task test` or `go test ./...` (run single test:
  `go test ./internal/llm/prompt -run TestGetContextFromPaths`)
- **Update Golden Files**: `go test ./... -update` (regenerates `.golden`
  files when test output changes)
  - Update specific package:
    `go test ./internal/tui/components/core -update` (in this case,
    we're updating "core")
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

- size: 702 files, 17007 symbols, 12378 edges (resolved 8729, external deps 3361, unresolved 288)
- languages: go:621 markdown:36 yaml:32 json:5 bash:3 css:2 javascript:2 plist:1
- subsystems: internal/ui(167,go) · internal/agent(89,go) · internal/config(32,go) · internal/cmd(21,go) · internal/backend(17,go) · internal/server(17,go) · internal/shell(16,go) · internal/proto(14,go)
- entrypoints: internal/agent/agenttest/coordinator.go · main.go · internal/ui/logo/example/main.go
- central files (most depended-on): internal/ui/styles/grad.go · internal/ui/styles/quickstyle.go · internal/ui/styles/styles.go · internal/ui/styles/themes.go · internal/pubsub/broker.go
- read these guides first: README.md · AGENTS.md

Depth on demand: `prowl-agent find|def|outline|references <name>`, `search <text>`, `context search "<question>"`, `sketch <ui>`.
<!-- /prowl-agent:map -->
