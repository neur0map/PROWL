package backend

import (
	"context"

	"github.com/neur0map/prowl/internal/goals"
)

func (b *Backend) ControlGoal(ctx context.Context, workspaceID, sessionID string, req goals.Request) (*goals.Goal, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	return ws.App.ControlGoal(ctx, sessionID, req)
}
