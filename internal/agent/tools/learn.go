package tools

import (
	"context"
	_ "embed"
	"strings"

	"charm.land/fantasy"
	"github.com/neur0map/prowl/internal/config"
	"github.com/neur0map/prowl/internal/prowlagent"
)

const LearnToolName = "learn"

//go:embed learn.md
var learnDescription string

// LearnParams are the inputs to the learn tool.
type LearnParams struct {
	Memory  string      `json:"memory" description:"A durable, self-contained lesson worth remembering: what was learned, when it applies, and why."`
	Context string      `json:"context,omitempty" description:"Optional source context for the lesson (where it came from)."`
	Skill   *LearnSkill `json:"skill,omitempty" description:"Optionally also codify the lesson as a managed skill (for repeatable procedures)."`
}

// LearnSkill optionally attaches a managed-skill create/update to a learned
// lesson.
type LearnSkill struct {
	Action      string `json:"action" description:"create or update."`
	Name        string `json:"name" description:"Kebab-case managed skill name."`
	Description string `json:"description" description:"One-line description used for discovery."`
	Body        string `json:"body" description:"Markdown body for SKILL.md (no frontmatter)."`
}

// NewLearnTool returns the learn tool, which records a durable lesson as a
// reviewable prowl-agent knowledge proposal and can optionally codify it as a
// managed skill. opts/workingDir drive the prowl-agent knowledge call;
// isAuthored and refresh support the optional managed-skill write.
func NewLearnTool(opts *config.ProwlAgentOptions, workingDir string, isAuthored func(string) bool, refresh func()) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		LearnToolName,
		learnDescription,
		func(ctx context.Context, params LearnParams, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
			memory := strings.TrimSpace(params.Memory)
			if memory == "" {
				return fantasy.NewTextErrorResponse("memory is required: state the lesson to remember"), nil
			}

			body := memory
			if c := strings.TrimSpace(params.Context); c != "" {
				body += "\n\nContext: " + c
			}
			if _, err := prowlagent.ProposeKnowledge(ctx, opts, workingDir, prowlagent.KnowledgeProposal{
				Title: learnTitle(memory),
				Body:  body,
				Tags:  []string{"lesson"},
			}); err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}

			result := "Lesson proposed to prowl-agent knowledge (review inbox)."
			if params.Skill != nil {
				msg, err := applyManagedSkillWrite(
					strings.ToLower(strings.TrimSpace(params.Skill.Action)),
					params.Skill.Name, params.Skill.Description, params.Skill.Body,
					isAuthored, refresh,
				)
				if err != nil {
					// The lesson already persisted; report the skill failure
					// as a partial outcome rather than losing the lesson.
					return fantasy.NewTextResponse(result + " However, the managed skill could not be written: " + err.Error()), nil
				}
				result += " " + msg
			}
			return fantasy.NewTextResponse(result), nil
		},
	)
}

// learnTitle derives a short knowledge title from the lesson text: its first
// non-empty line, trimmed and length-capped.
func learnTitle(memory string) string {
	line := memory
	if i := strings.IndexByte(memory, '\n'); i >= 0 {
		line = memory[:i]
	}
	line = strings.TrimSpace(line)
	const maxTitle = 80
	if len(line) > maxTitle {
		line = strings.TrimSpace(line[:maxTitle])
	}
	if line == "" {
		line = "Lesson"
	}
	return line
}
