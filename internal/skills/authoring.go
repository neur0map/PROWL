package skills

import (
	"fmt"
	"strings"
)

// BuildCreatePrompt synthesizes a standards-guided prompt for authoring a brand
// new skill. Mirroring the Nous /learn model, the returned text is handed to
// the agent as a normal turn (there is no dedicated authoring tool): it tells
// the agent exactly what the user wants and how a house-standard SKILL.md must
// look, then asks it to write the file under the managed skills directory.
func BuildCreatePrompt(userDesc string) string {
	desc := strings.TrimSpace(userDesc)
	if desc == "" {
		desc = "(no description provided; ask the user to clarify before writing)"
	}

	var b strings.Builder
	b.WriteString("Author a new prowl skill as a single SKILL.md file.\n\n")
	b.WriteString("What the skill should do:\n")
	b.WriteString(desc)
	b.WriteString("\n\n")
	b.WriteString(skillStandard())
	b.WriteString("\n\n")
	fmt.Fprintf(&b,
		"Write the new file to %s/<kebab-name>/SKILL.md, choosing a short "+
			"kebab-case <kebab-name> derived from the purpose above. Use the "+
			"write tool to create it.\n\n",
		managedSkillsDirHint(),
	)
	b.WriteString(validationTail())
	return b.String()
}

// BuildImprovePrompt synthesizes a standards-guided prompt for refining an
// existing skill. It embeds the current SKILL.md so the agent has full context
// and instructs it to edit the file in place at path.
func BuildImprovePrompt(name, path, currentContent, userInstruction string) string {
	name = strings.TrimSpace(name)
	instruction := strings.TrimSpace(userInstruction)
	if instruction == "" {
		instruction = "(no instruction provided; ask the user to clarify before editing)"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Improve the existing prowl skill %q (SKILL.md at %s).\n\n", name, path)
	b.WriteString("Requested improvement:\n")
	b.WriteString(instruction)
	b.WriteString("\n\n")
	b.WriteString("Current SKILL.md content:\n")
	b.WriteString("```markdown\n")
	b.WriteString(currentContent)
	if !strings.HasSuffix(currentContent, "\n") {
		b.WriteString("\n")
	}
	b.WriteString("```\n\n")
	b.WriteString(skillStandard())
	b.WriteString("\n\n")
	fmt.Fprintf(&b,
		"Edit the existing file in place at %s with the edit tool; do not "+
			"create a new file or change its directory name.\n\n",
		path,
	)
	b.WriteString(validationTail())
	return b.String()
}

// skillStandard describes the house SKILL.md format shared by the create and
// improve prompts. Keep it tight and imperative.
func skillStandard() string {
	return strings.Join([]string{
		"Follow the house SKILL.md standard exactly:",
		"- Begin with YAML frontmatter delimited by --- lines containing a " +
			"`name` field and a `description` field. Keep the description to " +
			"60 characters or fewer and make it say when to reach for the skill.",
		"- After the frontmatter, write these Markdown sections in order, each " +
			"as a `##` heading: When to Use, Procedure, Pitfalls, Verification.",
		"- Reference only real prowl tools (read, edit, write, bash, grep, " +
			"glob): no invented commands, flags, or tools.",
		"- Keep every instruction concrete and imperative; no filler.",
	}, "\n")
}

// managedSkillsDirHint returns the managed-skills root for the prompt, falling
// back to the documented default when the home config dir is unavailable.
func managedSkillsDirHint() string {
	if dir := ManagedSkillsDir(); dir != "" {
		return dir
	}
	return "~/.config/prowl/managed-skills"
}

// validationTail is the shared closing instruction: verify the file parses and
// note that discovery refreshes on its own.
func validationTail() string {
	return "After writing, re-read the file to confirm the YAML frontmatter " +
		"parses and every required section is present. Skill discovery " +
		"refreshes automatically once the file is saved."
}
