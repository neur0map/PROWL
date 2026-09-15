Record a durable, reusable lesson the moment you resolve something non-obvious that cost real effort and would recur. Recording is your job, not the user's: you create the lesson, and a human later reviews, edits, or removes it. The lesson enters the prowl-agent knowledge review inbox as a proposal — never silently accepted — so the project's knowledge base stays curated and portable.

Record whenever you hit one of these — the effort is already spent, so capture it:
- A root cause you found only after a wrong hypothesis: write down the actual cause and why the first guess was wrong.
- A convention the code relies on but does not state: an ordering, an invariant, a naming or wiring rule you had to infer.
- A trap that wasted time: a footgun, a surprising API, an environment or build quirk that will bite the next session.
- A fix whose reason is not visible in the diff: the code change looks arbitrary without the rationale, so record the rationale.

Skip recording when it would add noise:
- The routine and obvious, or anything a competent reader sees straight from the code.
- Something already documented — refine the existing entry with a specific `target` instead of filing a near-duplicate. Duplicate lessons are detected and dropped automatically, so do not restate what is already recorded.
- Anything unverified: never file a hypothesis as a confirmed cause, and never invent evidence anchors.

Inputs:
- `memory`: the lesson itself — self-contained: what was learned, when it applies, and why. Its first line becomes the proposal title.
- `context` (optional): where the lesson came from (a file, an error, a decision).
- `target` (optional): bundle-relative topic path, such as `lessons/session-costs.md`. Reuse an existing topic when refining it instead of creating a duplicate. Updating accepted knowledge remains a proposal, not an overwrite.
- `evidence` (optional): resolvable `path#symbol` or `path:start-end` anchors. Include observed supporting code; never fabricate anchors or present a hypothesis as a confirmed cause.
- `skill` (optional): also codify a repeatable PROCEDURE as a managed skill, independently of knowledge review. Provide `action` (create/update), `name` (kebab-case), `description` (one line), and `body` (Markdown, no frontmatter). Managed skills never shadow authored skills. Use this only for a procedure worth executing again, not for a plain fact or decision.

Outcome: a new lesson returns a proposal ID and target, pending human review — report it as pending, not as accepted or active knowledge. A lesson that repeats one already recorded returns an "already recorded" note and files nothing; that is expected, not an error.

Examples:
- `{"memory": "Session costs accumulate atomically; ordinary saves must not replace them.", "target": "lessons/session-costs.md", "evidence": ["internal/session/session.go#AddCost"]}`
- `{"memory": "Cutting a release requires regenerating schema.json.", "skill": {"action": "create", "name": "release-cut", "description": "Use when cutting a release.", "body": "1. Run `task schema` ..."}}`
