Record a durable, reusable lesson so it is not lost across turns and sessions. The lesson is stored as a reviewable prowl-agent knowledge proposal (it enters the review inbox rather than being silently accepted), keeping the project's knowledge base curated and portable.

Use this for a fact, decision, convention, or pitfall that will matter again — not for transient task state. One precise lesson is better than several vague ones.

Inputs:
- `memory`: the lesson itself — self-contained: what was learned, when it applies, and why. Its first line becomes the proposal title.
- `context` (optional): where the lesson came from (a file, an error, a decision).
- `target` (optional): bundle-relative topic path, such as `lessons/session-costs.md`. Reuse an existing topic when refining it instead of creating a duplicate. Updating accepted knowledge remains a proposal, not an overwrite.
- `evidence` (optional): resolvable `path#symbol` or `path:start-end` anchors. Include observed supporting code; never fabricate anchors or present a hypothesis as a confirmed cause.
- `skill` (optional): immediately codify a repeatable PROCEDURE as a managed skill, independently of knowledge review. Provide `action` (create/update), `name` (kebab-case), `description` (one line), and `body` (Markdown, no frontmatter). Managed skills never shadow authored skills. Omit this field in review-first workflows such as Hindsight.

Guidance:
- Prefer memory-only for facts and decisions; attach a `skill` only for repeatable procedures worth executing again.
- The native knowledge engine validates proposals and returns a proposal ID and target. Report them as pending review, not as an accepted or active lesson. Conflicting edits require fresh evidence and review.

Examples:
- `{"memory": "Session costs accumulate atomically; ordinary saves must not replace them.", "target": "lessons/session-costs.md", "evidence": ["internal/session/session.go#AddCost"]}`
- `{"memory": "Cutting a release requires regenerating schema.json.", "skill": {"action": "create", "name": "release-cut", "description": "Use when cutting a release.", "body": "1. Run `task schema` ..."}}`
