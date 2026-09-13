package app

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/neur0map/prowl/internal/agent"
	"github.com/neur0map/prowl/internal/db"
	"github.com/neur0map/prowl/internal/goals"
	"github.com/neur0map/prowl/internal/session"
	"github.com/stretchr/testify/require"
)

type goalCancelGate struct {
	agent.Coordinator
	busy     atomic.Bool
	canceled chan struct{}
}

func (c *goalCancelGate) Cancel(string)             { close(c.canceled) }
func (c *goalCancelGate) IsSessionBusy(string) bool { return c.busy.Load() }

func TestGoalReplacementWaitsForCanceledWork(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	conn, err := db.Connect(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	q := db.New(conn)
	service := goals.NewService(q)
	t.Cleanup(service.Shutdown)
	sessions := session.NewService(q, conn)
	sess, err := sessions.Create(ctx, "Replacement")
	require.NoError(t, err)
	old, err := service.Apply(ctx, sess.ID, goals.Request{Op: "set", Objective: "Old work"})
	require.NoError(t, err)
	gate := &goalCancelGate{canceled: make(chan struct{})}
	gate.busy.Store(true)
	app := &App{Goals: service, Sessions: sessions, AgentCoordinator: gate}
	_, err = app.ControlGoal(ctx, sess.ID, goals.Request{Op: "set", Objective: " "})
	require.Error(t, err)
	select {
	case <-gate.canceled:
		t.Fatal("invalid input canceled valid work")
	default:
	}
	done := make(chan error, 1)
	go func() {
		_, err := app.ControlGoal(ctx, sess.ID, goals.Request{Op: "set", Objective: "New work"})
		done <- err
	}()
	select {
	case <-gate.canceled:
	case <-ctx.Done():
		t.Fatal("old work was not canceled")
	}
	current, err := service.Get(ctx, sess.ID)
	require.NoError(t, err)
	require.Equal(t, old.ID, current.ID, "replacement cannot start while canceled work owns the session")
	gate.busy.Store(false)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("replacement did not start")
	}
	current, err = service.Get(ctx, sess.ID)
	require.NoError(t, err)
	require.Equal(t, "New work", current.Objective)
	require.True(t, current.Active())
	_, err = service.Apply(ctx, sess.ID, goals.Request{Op: "pause"})
	require.NoError(t, err)
	oldID := current.ID
	_, err = service.Apply(ctx, sess.ID, goals.Request{Op: "resume"})
	require.NoError(t, err)
	_, err = service.PauseActive(ctx, sess.ID, oldID)
	require.NoError(t, err)
	current, err = service.Get(ctx, sess.ID)
	require.NoError(t, err)
	require.True(t, current.Active(), "late cleanup from an earlier activation cannot pause resumed work")
}
