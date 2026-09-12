Query the local prowl-agent code index for fast, cited answers about this repository. This is your FIRST move for any structural or semantic question — where code lives, what a symbol is, who calls it, a file's shape, or a change's blast radius — instead of grepping and reading whole files. prowl-agent reindexes changed files before each query, so answers stay current and come back cited to `file:line` for a fraction of the tokens.

Set `command` to a read-only subcommand and `args` to its arguments. Output is token-lean TOON by default.

Common commands:
- `overview` — high-level map of the project (languages, subsystems, entrypoints, hotspots).
- `search` `["<question>"]` — semantic + full-text content search ("how does X work", "where is feature Y").
- `find` `["<name>"]` — locate a symbol (function, type, setting, component) by name; ranked, cited.
- `def` `["<name-or-id>"]` — read one symbol's source (signature + body), bounded, instead of the whole file.
- `outline` `["<path>"]` — a file's structure (symbols, signatures, line ranges) without reading it.
- `references` `["<name-or-id>"]` — where a symbol is used (call sites / reference edges).
- `impact` `["<path>"]` — blast radius of changing a file (dependents, subsystems, importers).
- `peek` `["<file:start-end>"]` — read a bounded, cited line range (turn a citation into code).
- `context` `["<question>"]` — build a bounded, cited context packet.
- `brief` `["<path>"]` — cited orientation for a path/subsystem (warm-start).
- `callers` / `callees` / `history` / `hotspots` / `clusters` / `entrypoints` / `relations` / `tests` / `span` / `changed` / `wip` / `violations` / `status` / `capabilities` — see `capabilities` for the full routing table.

Guidance:
- Prefer this tool over grep/glob for locating, reading, tracing, or sizing code. Reserve grep for exact literal/regex text and glob for filename patterns.
- Resolve a `find`/`search`/`references` citation into code with `def`, `outline`, or `peek` rather than opening whole files.
- If a query returns nothing, try an alternate strategy (different term, broader path) before concluding the target does not exist.

Examples:
- `{"command": "find", "args": ["NewCoordinator"]}`
- `{"command": "search", "args": ["how are skills discovered"]}`
- `{"command": "impact", "args": ["internal/config/config.go"]}`
- `{"command": "outline", "args": ["internal/agent/coordinator.go"]}`
