package tools

import (
	"context"

	"charm.land/fantasy"
	"github.com/neur0map/prowl/internal/goals"
)

const GoalToolName = "goal"

type GoalParams struct {
	Op          string `json:"op" enum:"create,get,complete,pause,resume,drop" description:"Goal operation"`
	Objective   string `json:"objective,omitempty" description:"Complete user objective; required for create"`
	TokenBudget *int64 `json:"token_budget,omitempty" description:"Optional positive token budget for create"`
}

func NewGoalTool(service *goals.Service) fantasy.AgentTool {
	return fantasy.NewAgentTool(GoalToolName, `Manage this session's persistent autonomous objective.
Create only when the user requests goal mode or completes a guided-goal interview; provide the entire objective and an optional positive token_budget. Creation enables autonomous continuation. Get returns state and usage. Resume reactivates a paused goal. Pause when a genuine blocker or the objective's stop condition requires human input. Drop removes the objective without claiming success.
Call complete only when the full objective and every deliverable have been verified against current evidence. Never complete merely because a token budget is exhausted, a subset is done, or you want to end a turn. A paused goal must be explicitly resumed before working on it.`,
		func(ctx context.Context, params GoalParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			switch params.Op {
			case "create", "get", "complete", "pause", "resume", "drop":
			default:
				return fantasy.NewTextErrorResponse("Unknown goal operation. Use create, get, complete, pause, resume or drop."), nil
			}
			goalID := ""
			if params.Op != "get" && params.Op != "create" {
				goalID = getContextValue(ctx, GoalIDContextKey, "")
			}
			g, err := service.Apply(ctx, GetSessionFromContext(ctx), goals.Request{
				Op: params.Op, Objective: params.Objective, TokenBudget: params.TokenBudget, GoalID: goalID,
			})
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			return fantasy.WithResponseMetadata(fantasy.NewTextResponse(g.Summary()), g), nil
		})
}
