package agent

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"charm.land/fantasy"
	"github.com/neur0map/prowl/internal/goals"
	"github.com/neur0map/prowl/internal/session"
)

var errGoalStopped = errors.New("goal paused, dropped or budget-limited")

type runGoal struct {
	service        *goals.Service
	sessionID      string
	autonomous     bool
	goal           *goals.Goal
	ownedID        string
	stepGoalID     string
	stepStarted    time.Time
	usageAccounted bool
	wasActive      bool
	halted         bool
}

func (a *sessionAgent) newRunGoal(sess session.Session) *runGoal {
	if a.goals == nil {
		return nil
	}
	id := sess.ID
	if a.isSubAgent && sess.ParentSessionID != "" {
		id = sess.ParentSessionID
	}
	return &runGoal{service: a.goals, sessionID: id, autonomous: !a.isSubAgent}
}

func (r *runGoal) prepare(ctx context.Context) (string, error) {
	if r == nil {
		return "", nil
	}
	g, err := r.service.Restore(ctx, r.sessionID)
	if err != nil {
		return "", err
	}
	r.goal = g
	if r.stopped() {
		return "", errGoalStopped
	}
	r.stepGoalID = ""
	r.stepStarted = time.Now()
	r.usageAccounted = false
	if g.Active() {
		r.wasActive = true
		if r.ownedID == "" {
			r.ownedID = g.ID
		}
		r.stepGoalID = g.ID
		if r.autonomous {
			return g.Context(), nil
		}
	}
	return "", nil
}

// Account provider usage before tools run, so goal get/complete see the cost
// of the request that invoked them. Missing usage is estimated at step finish.
func (r *runGoal) streamFinished(ctx context.Context, usage fantasy.Usage) error {
	if r == nil || usage.InputTokens+usage.OutputTokens+usage.CacheCreationTokens+usage.CacheReadTokens == 0 {
		return nil
	}
	elapsed := time.Duration(0)
	if r.autonomous {
		elapsed = time.Since(r.stepStarted)
	}
	g, err := r.service.Account(ctx, r.sessionID, r.stepGoalID,
		max(0, usage.InputTokens)+max(0, usage.CacheCreationTokens)+max(0, usage.OutputTokens), elapsed)
	if err != nil {
		return err
	}
	r.goal = g
	r.usageAccounted = true
	r.stepStarted = time.Now()
	return nil
}

func (r *runGoal) account(ctx context.Context, usage fantasy.Usage, halt bool) error {
	if r == nil {
		return nil
	}
	if r.usageAccounted {
		usage = fantasy.Usage{}
	}
	elapsed := time.Duration(0)
	if r.autonomous {
		elapsed = time.Since(r.stepStarted)
	}
	g, err := r.service.Account(ctx, r.sessionID, r.stepGoalID,
		max(0, usage.InputTokens)+max(0, usage.CacheCreationTokens)+max(0, usage.OutputTokens), elapsed)
	if err != nil {
		return err
	}
	r.stepGoalID = ""
	r.goal = g
	if halt && r.autonomous {
		r.halted = true
		r.goal, err = r.service.PauseActive(ctx, r.sessionID, r.ownedID)
	}
	return err
}

func (r *runGoal) stopped() bool {
	return r != nil && r.wasActive && (r.goal == nil || r.goal.ID != r.ownedID || r.goal.Status == goals.Paused || r.goal.Status == goals.BudgetLimited)
}

func (r *runGoal) continuation(ctx context.Context) (string, error) {
	if r == nil || !r.autonomous || r.halted {
		return "", nil
	}
	g, err := r.service.Get(ctx, r.sessionID)
	if err != nil {
		return "", err
	}
	r.goal = g
	if r.stopped() {
		return "", nil
	}
	if g.Active() && r.ownedID == "" {
		r.ownedID, r.wasActive = g.ID, true
	}
	return g.Continuation(), nil
}

func (a *sessionAgent) pauseGoal(sessionID, expectedID string) {
	if a.goals == nil || a.isSubAgent {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := a.goals.PauseActive(ctx, sessionID, expectedID); err != nil {
		slog.Error("Failed to pause goal", "session_id", sessionID, "error", err)
	}
}

func (r *runGoal) fail() {
	if r == nil || !r.autonomous || r.ownedID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if r.stepGoalID != "" {
		if _, err := r.service.Account(ctx, r.sessionID, r.stepGoalID, 0, time.Since(r.stepStarted)); err != nil {
			slog.Error("Failed to account interrupted goal time", "error", err)
		}
	}
	if _, err := r.service.PauseActive(ctx, r.sessionID, r.ownedID); err != nil {
		slog.Error("Failed to pause interrupted goal", "error", err)
	}
}

func accumulateGoalResult(total, next *fantasy.AgentResult) *fantasy.AgentResult {
	if total == nil {
		return next
	}
	if next == nil {
		return total
	}
	total.Steps = append(total.Steps, next.Steps...)
	total.Response = next.Response
	total.TotalUsage.InputTokens += next.TotalUsage.InputTokens
	total.TotalUsage.OutputTokens += next.TotalUsage.OutputTokens
	total.TotalUsage.TotalTokens += next.TotalUsage.TotalTokens
	total.TotalUsage.ReasoningTokens += next.TotalUsage.ReasoningTokens
	total.TotalUsage.CacheCreationTokens += next.TotalUsage.CacheCreationTokens
	total.TotalUsage.CacheReadTokens += next.TotalUsage.CacheReadTokens
	return total
}
