# Prowl notice

Prowl began as a fork of Crush, created by Charmbracelet, Inc. and the Crush
contributors.

The original team did the hard work that made this starting point possible. We
thank them for the care they put into the terminal experience, the provider
work, and the many parts that Prowl still carries.

Prowl is an independent Ryoku project. It is not endorsed by, sponsored by, or
joined to Charmbracelet. The names Charmbracelet and Crush, along with their
marks, belong to their owners.

Prowl does not follow Crush as an upstream project. We do not plan regular
merges, rebases, or feature copies. We may review and cherry-pick a Crush change
when it fixes a security problem that also affects Prowl.

The native OpenAI subscription integration is an explicit feature-port
exception, adapted from [Crush PR #3731](https://github.com/charmbracelet/crush/pull/3731).

The inherited work and its changes remain covered by the Functional Source
License, Version 1.1, MIT Future License in [LICENSE.md](LICENSE.md). The
original copyright notice is kept there.

Prowl-specific work is Copyright 2026 Ryoku and its contributors, subject to the
same repository license where it forms part of this program.

## MIT-licensed adaptations

The prose keyword scanner in `internal/reasoning/keyword.go` and the native
Claude OAuth/fingerprint compatibility code in `internal/oauth/anthropic/`
are adapted from [Oh My Pi](https://github.com/can1357/oh-my-pi). OpenAI
subscription compatibility also uses OMP's Codex protocol reference. The goal
and guided-goal workflow instructions, `/green` workflow, and hash-number
PR/issue completion behavior are also adapted from OMP. Its upstream notices
are:

Copyright (c) 2025 Mario Zechner
Copyright (c) 2025-2026 Can Bölük
Copyright (c) 2026 Stencil Labs, Inc.

The textarea wrapping algorithm in `internal/ui/model/ultrathink.go` is
adapted from [Bubbles](https://github.com/charmbracelet/bubbles). Its upstream
notice is:

Copyright (c) 2020-2026 Charmbracelet, Inc.

The user-triggered retrospective in `internal/skills/builtin/hindsight/` is
adapted from [EfficientStreet's Hindsight](https://github.com/EfficientStreet/hindsight),
which credits Jeffrey Smith. Its upstream license notice is:

Copyright (c) 2026 SomewhereSimulated

The optional response style in `internal/agent/templates/focus.md.tpl` is
adapted from [i-have-adhd](https://github.com/ayghri/i-have-adhd). Prowl's
adaptation does not assume a diagnosis, impose arbitrary output limits, or
require invented time estimates. Its upstream license notice is:

Copyright (c) 2026 Ayoub Ghriss

These adapted portions retain the following MIT license:

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.

## Bundled font

The OAuth callback page embeds [Manrope](https://github.com/sharanda/manrope),
Copyright 2018 The Manrope Project Authors. The font remains licensed under
the SIL Open Font License, Version 1.1, rather than the repository's software
license. The full notice is kept in
[`internal/oauth/callback/Manrope-OFL.txt`](internal/oauth/callback/Manrope-OFL.txt)
and embedded in the self-contained callback page alongside the font.

## AI gateway: provider catalog and routing

The built-in gateway derives two things from MIT-licensed upstreams.

**Provider catalog.** The directory of providers in
[`internal/gateway/catalog/providers.json`](internal/gateway/catalog/providers.json)
is generated from
[open-free-llm-api/awesome-freellm-apis](https://github.com/open-free-llm-api/awesome-freellm-apis),
Copyright (c) 2026 open-free-llm-api, MIT License, merged with provider
endpoints and key environment variable names from
[BerriAI/litellm](https://github.com/BerriAI/litellm). The generation and
deduplication procedure is documented in
[`internal/gateway/catalog/providers.NOTICE.md`](internal/gateway/catalog/providers.NOTICE.md).

**Routing behaviour.** The strategy set, cooldown classification, fallback
chain, and rate-limit admission in [`internal/gateway/router.go`](internal/gateway/router.go)
are derived from litellm's router (read at commit
`63386d6cc68414814acb064187133635e5c4e426`), Copyright (c) 2023 Berri AI,
MIT License. No litellm code is copied; the behaviour was reimplemented in Go.

litellm is **not uniformly MIT**: its own `LICENSE` states that content under
its `enterprise/` directory is licensed separately, and that everything
outside those directories is available under MIT. Every algorithm referenced
above lives outside `enterprise/`, so the MIT grant applies. Its licence
header is reproduced here in full for that reason:

> Portions of this software are licensed as follows:
>
> - All content that resides under the "enterprise/" directory of this
>   repository, if that directory exists, is licensed under the license
>   defined in "enterprise/LICENSE".
> - Content outside of the above mentioned directories or restrictions above
>   is available under the MIT license as defined below.

MIT License

Copyright (c) 2023 Berri AI
Copyright (c) 2026 open-free-llm-api

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.

## FreeLLMAPI

The gateway dashboard is FreeLLMAPI's client, rebranded to Prowl:

    https://github.com/tashfeenahmed/freellmapi
    Copyright (c) 2026 Tashfeen Ahmed
    MIT License
    Vendored from commit 780a7d8d6dcbc818eb10ec17da210635b569ae22 (v0.9.9)

FreeLLMAPI is a React + Tailwind + shadcn/ui application. Its source is vendored
under `internal/gateway/webui/client/` (with the `shared/` types it imports),
and its built output under `internal/gateway/webui/dist/`, which is embedded in
the Go binary so the dashboard ships with no Node toolchain at build time. The
source is kept so the UI can be rebuilt from this repository alone
(`task webui:build`). Prowl's changes are limited to the product name and title,
the removal of the hosted-premium upsell and licence-key flow (this build has no
such tier), and pointing the release and docs links at Prowl's own repository.
The upstream MIT licence travels with the code at
[`internal/gateway/webui/LICENSE.freellmapi`](internal/gateway/webui/LICENSE.freellmapi).

Its own upstream design system, shadcn/ui, is MIT licensed:

    https://github.com/shadcn-ui/ui
    Copyright (c) 2023 shadcn

## Geist

The dashboard ships the Geist and Geist Mono variable Latin subsets, taken
unmodified from the official @fontsource-variable distribution:

    https://github.com/vercel/geist-font
    Copyright 2024 The Geist Project Authors
    SIL Open Font License 1.1

The licence text travels with the fonts at
`internal/gateway/dashboard/fonts/geist.LICENSE`.
