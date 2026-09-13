package dialog

// ActionAuthorRule requests authoring a rule through the agent. Main
// synthesizes a rule-authoring prompt from Prompt (the user's instruction) and
// sends it as a message; the agent writes the rule file itself. Name is the
// suggested rule name.
type ActionAuthorRule struct {
	Name   string
	Prompt string
}

// ActionAddRule reports that a rule named Name was written to disk inline from
// the Rules modal, so Main can refresh the rules list.
type ActionAddRule struct {
	Name string
}
