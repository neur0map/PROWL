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

Press `ctrl+l` inside Prowl to change models. Run `prowl --help` for the full
command list.

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
