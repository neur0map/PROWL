Record a durable, reusable lesson so it is not lost across turns and sessions. The lesson is stored as a reviewable prowl-agent knowledge proposal (it enters the review inbox rather than being silently accepted), keeping the project's knowledge base curated and portable.

Use this for a fact, decision, convention, or pitfall that will matter again — not for transient task state. One precise lesson is better than several vague ones.

Inputs:
- `memory`: the lesson itself — self-contained: what was learned, when it applies, and why. Its first line becomes the proposal title.
- `context` (optional): where the lesson came from (a file, an error, a decision).
- `skill` (optional): also codify the lesson as a managed skill when it describes a repeatable PROCEDURE. Provide `action` (create/update), `name` (kebab-case), `description` (one line), and `body` (Markdown, no frontmatter). Managed skills never shadow authored (builtin/user) skills.

Guidance:
- Prefer memory-only for facts and decisions; attach a `skill` only for repeatable procedures worth executing again.
- Requires the prowl-agent integration; the lesson is proposed for review, so mention to the user that it is pending acceptance when relevant.

Examples:
- `{"memory": "Prowl builds with CGO_ENABLED=0 and GOEXPERIMENT=greenteagc; never add a cgo dependency to the main binary."}`
- `{"memory": "Cutting a release requires regenerating schema.json.", "skill": {"action": "create", "name": "release-cut", "description": "Use when cutting a release.", "body": "1. Run `task schema` ..."}}`
