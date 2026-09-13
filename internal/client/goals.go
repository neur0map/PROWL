package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/neur0map/prowl/internal/goals"
)

func (c *Client) ControlGoal(ctx context.Context, workspaceID, sessionID string, req goals.Request) (*goals.Goal, error) {
	path := fmt.Sprintf("/workspaces/%s/sessions/%s/goal", url.PathEscape(workspaceID), url.PathEscape(sessionID))
	var rsp *http.Response
	var err error
	if req.Op == "get" || req.Op == "show" {
		rsp, err = c.get(ctx, path, nil, nil)
	} else {
		rsp, err = c.post(ctx, path, nil, jsonBody(req), http.Header{"Content-Type": []string{"application/json"}})
	}
	if err != nil {
		return nil, fmt.Errorf("goal request: %w", err)
	}
	defer rsp.Body.Close()
	if err := checkStatus(rsp); err != nil {
		return nil, err
	}
	var g *goals.Goal
	if err := json.NewDecoder(rsp.Body).Decode(&g); err != nil {
		return nil, fmt.Errorf("decode goal: %w", err)
	}
	return g, nil
}
