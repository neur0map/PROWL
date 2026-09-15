package tools

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
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
	Memory   string      `json:"memory" description:"A durable, self-contained lesson worth remembering: what was learned, when it applies, and why."`
	Context  string      `json:"context,omitempty" description:"Optional source context for the lesson (where it came from)."`
	Target   string      `json:"target,omitempty" description:"Bundle-relative topic path to propose creating or updating, for example lessons/session-costs.md. Omit to derive a path from the title."`
	Evidence []string    `json:"evidence,omitempty" description:"Resolvable source anchors supporting the lesson, as path#symbol or path:start-end. Do not invent anchors."`
	Skill    *LearnSkill `json:"skill,omitempty" description:"Optionally also codify the lesson as a managed skill (for repeatable procedures)."`
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
			result, err := recordLesson(ctx, opts, workingDir, learnTitle(memory), body, params)
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
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

// recordLesson deduplicates and files a lesson. Automatic recording must not
// file the same insight twice, so it first compares the lesson against accepted
// knowledge and pending proposals; a near-duplicate records nothing and returns
// a plain "already recorded" message, which is a normal outcome, not a failure.
// Otherwise it proposes the lesson for human review and returns the receipt. A
// non-nil error is a real failure to record.
func recordLesson(ctx context.Context, opts *config.ProwlAgentOptions, workingDir, title, body string, params LearnParams) (string, error) {
	existing, err := prowlagent.ExistingLessons(ctx, opts, workingDir)
	if err != nil {
		return "", err
	}
	if prowlagent.LessonMatchesExisting(title, body, existing) {
		return "This lesson is already recorded, so nothing was filed. If it needs refining, propose an update to the existing entry with a specific target instead.", nil
	}

	out, err := prowlagent.ProposeKnowledge(ctx, opts, workingDir, prowlagent.KnowledgeProposal{
		Title:   title,
		Body:    body,
		Tags:    []string{"lesson"},
		Target:  params.Target,
		Anchors: params.Evidence,
	})
	if err != nil {
		return "", err
	}
	var receipt struct {
		Proposal struct {
			ID         string `json:"id"`
			Operation  string `json:"operation"`
			TargetPath string `json:"target_path"`
		} `json:"proposal"`
	}
	if err := json.Unmarshal([]byte(out), &receipt); err != nil || receipt.Proposal.ID == "" {
		return "Lesson proposed, but its receipt could not be decoded. Review the knowledge inbox before retrying.\n" + out, nil
	}
	return fmt.Sprintf("Proposal %s: %s %s. Pending human review; not accepted knowledge.", receipt.Proposal.ID, receipt.Proposal.Operation, receipt.Proposal.TargetPath), nil
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
