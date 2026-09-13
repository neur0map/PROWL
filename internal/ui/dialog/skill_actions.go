package dialog

// ActionOpenSkillFile requests that the host open a skill's SKILL.md in the
// user's external editor (nvim or $EDITOR). It is emitted when the user
// presses Enter on a selected row in the Skills modal.
type ActionOpenSkillFile struct {
	Path string
}

// ActionAuthorSkill requests that the host synthesize a standards-guided
// authoring prompt and hand it to the agent as a normal turn. Mode is either
// "create" (author a brand new skill from Prompt) or "improve" (refine the
// existing skill named Name at Path using Prompt as the instruction).
type ActionAuthorSkill struct {
	Mode   string
	Name   string
	Path   string
	Prompt string
}
