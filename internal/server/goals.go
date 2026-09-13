package server

import (
	"encoding/json"
	"net/http"

	"github.com/neur0map/prowl/internal/goals"
)

// handleWorkspaceGoal reads or controls a session goal.
//
// @Summary Read or control a goal
// @Tags sessions
// @Accept json
// @Produce json
// @Param id path string true "Workspace ID"
// @Param sid path string true "Session ID"
// @Param request body goals.Request false "Goal operation"
// @Success 200 {object} goals.Goal
// @Router /workspaces/{id}/sessions/{sid}/goal [get]
// @Router /workspaces/{id}/sessions/{sid}/goal [post]
func (c *controllerV1) handleWorkspaceGoal(w http.ResponseWriter, r *http.Request) {
	req := goals.Request{Op: "get"}
	if r.Method == http.MethodPost {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, http.StatusBadRequest, "invalid goal request")
			return
		}
	}
	g, err := c.backend.ControlGoal(r.Context(), r.PathValue("id"), r.PathValue("sid"), req)
	if err != nil {
		c.handleError(w, r, err)
		return
	}
	jsonEncode(w, g)
}
