---
name: anti-slop
description: Use when the user wants to vendor, install, configure, or update the anti-slop Oxlint rules in a TypeScript or JavaScript repository — copying the plugin into the repo, wiring it into the oxlint config, enabling the generic and Effect rule groups, or refreshing a vendored copy without clobbering local edits.
---

# anti-slop — Vendored Oxlint Rules

anti-slop (https://github.com/dmmulroy/anti-slop, MIT, by Dillon Mulroy) is a
set of Oxlint rules that reject low-evidence, low-signal TypeScript and
JavaScript patterns. It is **meant to be vendored**, not installed as an npm
dependency: there is no published package. Copy the rules into the target
repository, then treat the vendored files as the repo's own to read and edit.

This skill installs or updates that plugin. The authoring principles behind it
already ship in Prowl's system prompt; use this skill only when a real TS/JS
repo should run the actual lint plugin.

## Vendor the plugin

1. Retrieve upstream `src/` at a known revision (clone the repo or download the
   `src/` tree). Record the commit you took so future updates can diff against
   it.
2. Copy `src/` into the repo under a stable vendored path, conventionally
   `tools/oxlint/anti-slop/`. The copied entry point is
   `tools/oxlint/anti-slop/index.ts`; the optional Effect entry point is
   `tools/oxlint/anti-slop/effect/index.ts`.
3. Install `@oxlint/plugins`. If the repo already uses `oxlint`, pin
   `@oxlint/plugins` at **exactly** the resolved oxlint version. If oxlint is
   not yet present, install the same current version of both packages. Keep
   both versions exact so upgrades move them together.

## Register in the oxlint config

Add the plugin and enable every generic rule in `oxlint.config.ts` (a Vite+
project uses the same shape under its `lint` key):

```ts
import { defineConfig } from "oxlint";

export default defineConfig({
  ignorePatterns: [
    "tools/oxlint/anti-slop/**",
    // plus any agent tooling dirs already in the repo, e.g.
    ".claude/**",
    ".cursor/**",
  ],
  jsPlugins: [
    { name: "anti-slop", specifier: "./tools/oxlint/anti-slop/index.ts" },
  ],
  rules: {
    "oxc/no-accumulating-spread": "error",
    "anti-slop/no-array-filter-map": "error",
    "anti-slop/no-reduce-accumulator-copy": "error",
    "anti-slop/no-chained-type-assertions": "error",
    "anti-slop/no-conditional-empty-object-spread": "error",
    "anti-slop/no-known-value-widening": "error",
    "anti-slop/no-module-mocking": "error",
    "anti-slop/no-object-parameters": "error",
    "anti-slop/no-reflect-apply": "error",
    "anti-slop/no-reflect-get": "error",
    "anti-slop/no-runtime-typeof": "error",
    "anti-slop/no-shape-in-symbol-names": "error",
    "anti-slop/no-unknown-parameters": "error",
    "anti-slop/no-unknown-returns": "error",
    "anti-slop/no-unknown-type-aliases": "error",
    "anti-slop/no-unsafe-dictionary-type": "error",
    "anti-slop/no-widen-then-assert": "error",
    "anti-slop/require-readable-spacing": "error",
    "anti-slop/require-safety-comment-for-type-assertion": "error",
  },
});
```

Vendor the vendored path into any ignore lists. Preserve existing
`ignorePatterns`; add the vendored directory and the repo's own agent tooling
directories rather than broadly ignoring every dot-directory. In a Vite+
project, also add those same patterns to `fmt.ignorePatterns` so `vp check`
does not reformat the vendored plugin.

## Optional Effect rules

The Effect-specific rules carry Effect architecture policy, so register them
**only** in repositories that depend directly on Effect. Add the second
plugin entry and enable the Effect rule group:

```ts
jsPlugins: [
  { name: "anti-slop", specifier: "./tools/oxlint/anti-slop/index.ts" },
  {
    name: "anti-slop-effect",
    specifier: "./tools/oxlint/anti-slop/effect/index.ts",
  },
],
rules: {
  // ...generic rules above...
  "anti-slop-effect/no-manual-effect-error-tag": "error",
  "anti-slop-effect/no-manual-tag-comparison": "error",
  "anti-slop-effect/no-manual-tagged-construction": "error",
  "anti-slop-effect/no-service-constructor-imports": "error",
  "anti-slop-effect/prefer-effect-match": "error",
},
```

`require-readable-spacing` vendors a comment-aware engine from ESLint
Stylistic under MIT; copy its `LICENSE` and provenance alongside the code when
you take that rule.

## Validate

Run `oxlint` (or `vp lint`) once after wiring. For the whitespace rule the
recommended order is `oxlint --fix`, then the repo's formatter, then lint
again, since the rule only inserts blank lines and defers wrapping to the
formatter.

## Update a vendored copy without clobbering local edits

The vendored files are the repo's own, so never force-replace the directory.

1. Fetch upstream `src/` at the target revision into a separate staging path.
2. If the original upstream snapshot you recorded on install is recoverable,
   do a three-way merge (recorded-base -> new-upstream, against the local
   vendored copy). Otherwise port reviewed changes conservatively, rule by
   rule.
3. Preserve local rule edits and config. Ask before overwriting a rule whose
   local policy diverged, and before enabling any newly added rule.
4. Record the new upstream revision as the base for the next update.
