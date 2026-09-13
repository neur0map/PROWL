package workspace

import (
	"context"

	"github.com/neur0map/prowl/internal/goals"
)

func (w *AppWorkspace) ControlGoal(ctx context.Context, sessionID string, req goals.Request) (*goals.Goal, error) {
	return w.app.ControlGoal(ctx, sessionID, req)
}

func (w *ClientWorkspace) ControlGoal(ctx context.Context, sessionID string, req goals.Request) (*goals.Goal, error) {
	return w.client.ControlGoal(ctx, w.workspaceID(), sessionID, req)
}
