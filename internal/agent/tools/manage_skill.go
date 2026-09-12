package tools

import (
	"context"
	_ "embed"
	"fmt"
	"strings"

	"charm.land/fantasy"
	"github.com/neur0map/prowl/internal/skills"
)

const ManageSkillToolName = "manage_skill"

//go:embed manage_skill.md
var manageSkillDescription string

// ManageSkillParams are the inputs to the manage_skill tool.
type ManageSkillParams struct {
	Action      string `json:"action" description:"One of: create, update, delete."`
	Name        string `json:"name" description:"Kebab-case managed skill name (lowercase letters, digits, hyphens)."`
	Description string `json:"description,omitempty" description:"One-line description used for skill discovery. Required for create and update."`
	Body        string `json:"body,omitempty" description:"Markdown body for SKILL.md, WITHOUT YAML frontmatter. Required for create and update."`
}

// NewManageSkillTool returns the manage_skill tool, which lets the agent author
// its own managed skills. isAuthored reports whether a name is already owned by
// a builtin or user skill (managed skills never shadow authored ones); refresh
// re-discovers skills so a newly written skill is advertised immediately.
func NewManageSkillTool(isAuthored func(string) bool, refresh func()) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		ManageSkillToolName,
		manageSkillDescription,
		func(_ context.Context, params ManageSkillParams, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
			action := strings.ToLower(strings.TrimSpace(params.Action))
			switch action {
			case "delete":
				if err := skills.DeleteManagedSkill(params.Name); err != nil {
					return fantasy.NewTextErrorResponse(err.Error()), nil
				}
				if refresh != nil {
					refresh()
				}
				norm, _ := skills.NormalizeManagedName(params.Name)
				return fantasy.NewTextResponse(fmt.Sprintf("Deleted managed skill %q.", norm)), nil
			case "create", "update":
				msg, err := applyManagedSkillWrite(action, params.Name, params.Description, params.Body, isAuthored, refresh)
				if err != nil {
					return fantasy.NewTextErrorResponse(err.Error()), nil
				}
				return fantasy.NewTextResponse(msg), nil
			default:
				return fantasy.NewTextErrorResponse(fmt.Sprintf("invalid action %q (must be create, update, or delete)", params.Action)), nil
			}
		},
	)
}

// applyManagedSkillWrite creates or updates a managed skill, enforcing the
// authored-name shadow guard, then triggers a skill refresh. It is shared by
// the manage_skill and learn tools. action must be "create" or "update".
func applyManagedSkillWrite(action, name, description, body string, isAuthored func(string) bool, refresh func()) (string, error) {
	if strings.TrimSpace(description) == "" || strings.TrimSpace(body) == "" {
		return "", fmt.Errorf("%q requires both a description and a body", action)
	}
	norm, err := skills.NormalizeManagedName(name)
	if err != nil {
		return "", err
	}
	if action == "create" && isAuthored != nil && isAuthored(norm) {
		return "", fmt.Errorf("a builtin or user skill named %q already exists; managed skills cannot shadow authored skills", norm)
	}
	path, err := skills.WriteManagedSkill(name, description, body, action == "create")
	if err != nil {
		return "", err
	}
	if refresh != nil {
		refresh()
	}
	verb := "Created"
	if action == "update" {
		verb = "Updated"
	}
	return fmt.Sprintf("%s managed skill %q (%s).", verb, norm, path), nil
}
