{{define "antislop" -}}
Code-authoring rules — apply them whenever you write or edit code. The first group is language-agnostic; the second applies only when you touch TypeScript or JavaScript. They reject low-evidence, low-signal patterns.

Language-agnostic:
- Never fabricate evidence to satisfy a type checker or compiler; make the code actually true, because a green build over a false claim hides the very bug you were asked to fix.
- Do not widen a known value to an untyped, `any`, `unknown`, or `object` type and later assert it back to a narrower one; the round trip throws away evidence you already had and reintroduces the risk it removed.
- Parse and validate untrusted data once at the boundary into a typed value, instead of scattering ad hoc runtime type probes downstream, because one checked edge is verifiable where scattered probes are not.
- Do not write a type assertion you cannot justify; when one is genuinely unavoidable, state in a comment the invariant that makes it safe, so a later reader can re-check it.
- Do not mock or stub a module when a real dependency seam exists; exercise the real seam, because a mock proves only that your mock agrees with itself.
- Do not name a symbol after its shape (`Data`, `Info`, `...Shape`); name it for what it means, because shape names carry no meaning and go stale the moment the shape changes.
- Prefer a single pass over a value to chained eager passes such as filter-then-map that allocate throwaway intermediate collections; one pass states the intent and skips the extra allocation.
- Never copy the accumulator inside a fold or reduce; mutate one locally owned accumulator and return it, because a per-step copy turns a linear fold quadratic.

TypeScript / JavaScript:
- Do not annotate a parameter, return, type alias, or dictionary value as `unknown`, `any`, `object`, `{}`, or `Record<string, unknown>`; these erase the caller's known types, so accept the specific type or parse at the boundary instead.
- Do not use a conditional empty-object spread like `...(cond ? { x } : {})` to omit a field, because an omitted key is not the same as one set to `undefined` and readers conflate the two.
- Do not reach through `Reflect.get` or `Reflect.apply`; use typed property access or a typed call, because Reflect returns `any` and discards every guarantee.
- Reject nested `as X as Y` assertion chains that launder one type into another, and precede any remaining single assertion with a `SAFETY:` comment stating why it holds.
- Prefer lazy iterator pipelines like `values().filter(...).map(...).toArray()` over eager `array.filter(...).map(...)` where the runtime supports them, to avoid materializing the intermediate array.
- In repositories that depend on Effect, prefer tagged handlers and `Match` over manual `_tag` comparisons, `_tag` switches, and literal `_tag` construction, because the library's constructors and matchers stay exhaustive as the tags change.
{{- end}}
