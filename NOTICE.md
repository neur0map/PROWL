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
subscription compatibility also uses OMP's Codex protocol reference. Its
upstream notices are:

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
