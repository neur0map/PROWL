# PROWL

[![Build](https://github.com/neur0map/PROWL/actions/workflows/build.yml/badge.svg)](https://github.com/neur0map/PROWL/actions/workflows/build.yml)
[![Security](https://github.com/neur0map/PROWL/actions/workflows/security.yml/badge.svg)](https://github.com/neur0map/PROWL/actions/workflows/security.yml)

Prowl is the coding and system-work harness for Ryoku Arch Linux. It brings a
model, project rules, file tools, shell work, and saved sessions into one place.

The aim is simple: help small and mid-sized models finish work at the standard
people usually expect from a much larger model, while using less context and
fewer paid tokens.

Prowl is not a reskin of another assistant. Its direction is set by the needs of
Ryoku: careful changes, short context, strong local tools, and useful help with
both code and everyday Linux work.

## How Prowl works

Prowl gives the model a small set of direct tools for reading, changing, and
running a project. It keeps project rules close to the work and asks tools for
facts instead of placing whole files into every prompt.

Prowl and `prowl-agent` are separate projects, but they are designed to work as
one path on Ryoku:

- `prowl` is the harness you talk to.
- `prowl-agent` keeps the local map of a project.
- Prowl will call `prowl-agent` directly before the model reads files.
- It will use that map to open only the parts needed for the job.

This will be Prowl's native project lookup, not an optional add-on or a copy of
`prowl-agent` inside this repository.

This is a design goal, not a claim that every model is equal. The model still
matters. Prowl is meant to make better use of the model you choose.

## What it includes

- A terminal app for coding and daily Linux work.
- Saved work sessions for each project.
- Support for hosted and local models.
- File, shell, search, language, and web tools.
- Project rules from files such as `AGENTS.md` and `PROWL.md`.
- Tool permissions and hooks for local safety rules.
- Language-server support for exact code lookups and safe renames.
- Skills that add focused instructions without filling every prompt.

Ryoku Arch Linux is the main target. Other systems may still build and run, but
Prowl will not shape its plans around matching every platform.

## Install

Prowl needs Go 1.27 or newer.

Install the current source with Go:

```sh
go install github.com/neur0map/prowl@latest
```

Or build it yourself:

```sh
git clone https://github.com/neur0map/PROWL.git
cd PROWL
CGO_ENABLED=0 GOEXPERIMENT=greenteagc go build -o prowl .
./prowl
```

Tagged releases publish Linux builds for x86-64 and ARM64. Ryoku packages can
use the same release files.

## First run

Start Prowl in the folder you want to work on:

```sh
prowl
```

Choose a model when asked. You can also set a key before starting Prowl:

```sh
export ANTHROPIC_API_KEY="your-key"
# or
export OPENAI_API_KEY="your-key"
```

OpenAI and Anthropic also offer browser-based subscription login:

```sh
prowl login openai      # alias: chatgpt
prowl login anthropic  # alias: claude
```

The TUI authentication screen offers **API key** or **subscription**. Both
subscription flows keep Prowl's own agent and tools; they do not start Codex,
Claude Code, or an ACP agent. Switching authentication replaces the previous
credential for that provider.

ChatGPT login uses the Codex backend and loads the models available to your
subscription, separately from OpenAI's API-key catalog. The browser must be
able to reach this machine's loopback callback: port **1455** for OpenAI or
**54545** for Claude. On a remote host, forward the corresponding port before
logging in. Canceling login releases the port; concurrent logins for the same
provider cannot share it.

**Claude subscription access is unofficial.** It follows Oh My Pi's native
OAuth and Claude Code compatibility behavior. Anthropic previously requested
that Crush remove this integration; access may be restricted or stop working.
Use an API key if you need the supported API authentication path.

Credentials are saved in Prowl's global data configuration and refreshed
automatically. Use `prowl login openai --force` (or `anthropic`) to sign in
again, and `prowl logout openai` (or `anthropic`) to remove saved credentials.
Environment or shell-config API keys remain external to that logout.

Press `ctrl+l` inside Prowl to change models. Run `prowl --help` for the full
command list.

For a reasoning-capable model, `alt+r` opens the reasoning picker, including
**Auto**. Type the lowercase word **ultrathink** in your question to highlight
it and use the model's strongest supported reasoning for that request only.
Your saved setting stays unchanged. See [reasoning controls](docs/config/README.md#reasoning-controls).

## Commands, goals, and GitHub references

At an empty prompt, **`\` opens the Commands modal**. **`/` opens a grouped
command picker** above the prompt; `ctrl+p` also opens the Commands modal.
Use **↑/↓** to choose, **Tab** to fill the prompt, and **Enter** to run an action
(or fill it when an argument is required). **Esc** dismisses suggestions.

Type `/goal` to browse goal actions, or `/goal sh` to narrow to `/goal show`.
Start with **`/goal set`** when you know the objective, or **`/guided-goal`** to
shape it together. `/goal` is a command group, not a standalone action.
Settings stays in the Commands modal, not in the slash picker.

Custom commands and skills can also be invoked by slash name; use their full
displayed name when a short name is ambiguous.

The goal and CI workflows follow [Oh My Pi](https://github.com/can1357/oh-my-pi):

| Command | Action |
| --- | --- |
| `/goal show` | Show the objective, status, usage, and controls. |
| `/goal set <objective>` | Set or replace the session's goal and start work. |
| `/guided-goal [rough idea]` | Interview one question at a time, then create the agreed goal. |
| `/goal pause` | Interrupt autonomous work without claiming completion. |
| `/goal resume` | Continue a paused goal. |
| `/goal drop` | Stop and remove the goal. |
| `/goal budget <tokens>` | Set a positive total token budget. |
| `/goal budget off` | Remove the token cap. |
| `/green [constraints]` | Inspect CI, fix failures, and verify the latest HEAD is green. |

Goals survive context compaction and are stored with the session. The agent
continues after ordinary replies until it explicitly completes the verified
objective. Interruption, permission denial, provider failure, or exhausted
budget leaves the goal **incomplete**, not completed. Restarting the owning
Prowl process pauses saved active goals; reconnecting to a running server does
not interrupt its ongoing work.

Budgets count uncached input, cache writes, and output for agent steps, including
delegated steps; cache reads are excluded. They are soft boundaries: an in-flight
step can exceed the cap. Raising or removing an exhausted budget allows work to
continue. An explicitly paused goal still needs `/goal resume`.

`/green` is a workflow prompt, not a permission bypass or a persistent mode. It
can commit and push fixes under the usual permission and repository rules, and
checks runs for the current commit rather than trusting an older green result.
It does not open a pull request unless you ask for one. Repository rules still
govern release tags.

Type **`#123`** to choose **PR #123** or **Issue #123** without a network lookup
while typing. `pr #123`, `pull #123`, and `issue #123` select the kind explicitly.
Choosing a suggestion inserts `pr://123` or `issue://123` into the prompt.
The `view` tool reads these through authenticated **`gh`** in the repository
where the agent runs. Explicit references such as `pr://owner/repo/123`,
`issue://owner/repo/123`, and `pr://owner/repo/123/diff` also work.
These pin the reference in the conversation; they do not modify GitHub.

## Set it up

Prowl works without a config file. When you need one, create
`~/.config/prowl/prowlrc`:

```sh
provider add local \
  --type openai-compat \
  --base-url "http://localhost:11434/v1"

model add local/qwen3-coder \
  --name "Qwen 3 Coder" \
  --context-window 131072

model large local/qwen3-coder
permissions allow read glob grep
```

The file is a shell script read when Prowl starts. Keep it private if it holds
keys. Project files named `.prowlrc` or `prowlrc` may change settings for that
project, so read them before starting Prowl in a folder you do not trust.

Full guides:

- [Config](docs/config/README.md)
- [Hooks](docs/hooks/README.md)

## Keeping prompts small

Prowl follows a few plain rules:

1. Find the right place before opening files.
2. Read the part that matters, not the whole tree.
3. Check what depends on a file before changing it.
4. Run the real command or program before calling the work done.
5. Keep long-lived project facts in project files instead of repeating them in
   every prompt.

`prowl-agent` will supply the project map used by the first three steps. It is
not a model and it does not replace Prowl.

In local-workspace mode, the conversation's model panel shows `prowl-agent`'s
estimated tokens saved directly beneath context usage and cost, including zero.
“This run” is the increase since Prowl's first index-status probe; “total” is
cumulative for the workspace. The estimate compares index answers with reading
the referenced files in full. It is separate from provider cache tokens and is
not a usage total attributed to an individual saved conversation.

## Privacy

Prowl can send prompts and selected project text to the model provider you pick.
Local models keep that part on your machine; hosted models follow their own
terms.

Public Prowl builds do not include a metrics address or key, so use counts stay
off by default. A private setup may turn them on by setting both
`PROWL_POSTHOG_ENDPOINT` and `PROWL_POSTHOG_KEY`.

You can still force them off with either setting:

```sh
export PROWL_DISABLE_METRICS=1
# or
export DO_NOT_TRACK=1
```

Prowl does not send prompt or reply text as part of those counts.

## Relationship to Crush

Prowl began as a fork of [Crush](https://github.com/charmbracelet/crush), made
by Charmbracelet and its contributors. Their work made this project possible.
We are grateful for the care, ideas, and craft they put into Crush.

Prowl is now an independent Ryoku project. It is not endorsed by or joined to
Charmbracelet.

Prowl will **not** keep pace with Crush or regularly merge its upstream work.
The projects are heading in different directions. We may review and cherry-pick
a Crush change when it fixes a security problem that also affects Prowl. That
is the only upstream work we plan to take.

`Charmbracelet` and `Crush` belong to their respective owners. See
[NOTICE.md](NOTICE.md) for the full project notice.

## Contributing

Read [CONTRIBUTING.md](CONTRIBUTING.md) before sending a change. Security
problems should follow [SECURITY.md](SECURITY.md).

## License

Prowl is a derivative of Crush and remains under the
[Functional Source License, Version 1.1, MIT Future License](LICENSE.md) used by
the upstream code. The future MIT grant in that file applies on its stated
timetable. Read the license before redistributing Prowl or using it in a
product.
