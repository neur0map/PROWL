package app

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/neur0map/prowl/internal/agent/tools"
	"github.com/neur0map/prowl/internal/config"
	"github.com/neur0map/prowl/internal/goals"
)

// ControlGoal is shared by local and server-backed workspaces. Restoring a
// saved record never starts an agent; only an explicit set/resume does that.
func (app *App) ControlGoal(ctx context.Context, sessionID string, req goals.Request) (*goals.Goal, error) {
	if app.Goals == nil {
		return nil, errors.New("goal service is unavailable")
	}
	if _, err := app.Sessions.Get(ctx, sessionID); err != nil {
		return nil, err
	}
	previous, err := app.Goals.Restore(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if req.Op == "restore" || req.Op == "get" || req.Op == "show" {
		return previous, nil
	}
	if req.Op == "create" || req.Op == "set" || req.Op == "resume" || req.Op == "budget" || req.Op == "guided" {
		if app.config != nil && !slices.Contains(app.Config().Agents[config.AgentCoder].AllowedTools, tools.GoalToolName) {
			return nil, errors.New("goal mode is disabled because the goal tool is disabled")
		}
	}
	if req.Op == "guided" {
		return previous, nil
	}
	// Reject invalid input before interrupting any existing work.
	if req.TokenBudget != nil && *req.TokenBudget <= 0 {
		return nil, errors.New("goal budget must be a positive integer")
	}
	replace := req.Op == "set" || req.Op == "create"
	if replace && strings.TrimSpace(req.Objective) == "" {
		return nil, errors.New("goal objective is required")
	}
	if req.Op == "create" && previous != nil && previous.Status != goals.Complete {
		return nil, errors.New("a goal already exists; resume it or drop it before creating another")
	}
	reactivate := req.Op == "resume" && previous != nil && !previous.Active() && previous.Status != goals.Complete
	if req.Op == "budget" && previous != nil && previous.Status == goals.BudgetLimited {
		reactivate = req.TokenBudget == nil || *req.TokenBudget > previous.TokensUsed
	}
	if req.GoalID == "" && app.AgentCoordinator != nil {
		if replace || (previous != nil && (req.Op == "pause" || req.Op == "drop")) {
			app.AgentCoordinator.Cancel(sessionID)
		}
		if replace || reactivate {
			// A new activation must not be queued behind a canceled run: its
			// error path deliberately does not dispatch queued user prompts.
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for app.AgentCoordinator.IsSessionBusy(sessionID) {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-ticker.C:
				}
			}
		}
	}
	return app.Goals.Apply(ctx, sessionID, req)
}
