Create, update, or delete a managed skill — a reusable SKILL.md the agent authors for itself. Use this to codify a repeatable procedure or hard-won convention so it is available in future turns and sessions.

Managed skills live in a dedicated store at the lowest discovery precedence: they never override a builtin or user (authored) skill. `create` fails if an authored skill already owns the name. After a successful write the active skill list refreshes immediately, so the new skill is advertised without a restart.

Inputs:
- `action`: `create`, `update`, or `delete`.
- `name`: kebab-case (lowercase letters, digits, hyphens; max 64 chars). This is the skill's identity and directory name.
- `description`: one line stating WHEN the skill applies (its triggering conditions) — used for discovery. Required for create/update.
- `body`: the Markdown instructions. Do NOT include YAML frontmatter; it is generated from `name` and `description`. Required for create/update.

Guidance:
- Use sparingly and only for genuinely repeatable procedures worth remembering. One precise skill beats several vague ones.
- Write the body as instructions to a future agent: concrete steps, commands, and pitfalls.
- For a durable fact or lesson (not a procedure), prefer the `learn` tool, which records it as reviewable project knowledge.

Examples:
- `{"action": "create", "name": "release-cut", "description": "Use when cutting a release: bump version, regenerate schema, tag.", "body": "1. Run `task schema`.\n2. ..."}`
- `{"action": "delete", "name": "release-cut"}`
