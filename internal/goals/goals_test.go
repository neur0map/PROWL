package goals

import (
	"testing"
	"time"

	"github.com/neur0map/prowl/internal/db"
	"github.com/neur0map/prowl/internal/session"
	"github.com/stretchr/testify/require"
)

func TestGoalRestoreBudgetAndReplacement(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	conn, err := db.Connect(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	q := db.New(conn)
	sessions := session.NewService(q, conn)
	sess, err := sessions.Create(ctx, "Goal lifecycle")
	require.NoError(t, err)
	s := NewService(q)
	t.Cleanup(s.Shutdown)
	cap := int64(20)
	g, err := s.Apply(ctx, sess.ID, Request{Op: "set", Objective: "Verify the full objective", TokenBudget: &cap})
	require.NoError(t, err)
	g, err = s.Account(ctx, sess.ID, g.ID, 25, 2*time.Second)
	require.NoError(t, err)
	require.Equal(t, BudgetLimited, g.Status)
	require.Equal(t, int64(25), g.TokensUsed)
	require.Equal(t, 2.0, g.TimeUsedSeconds)
	_, err = s.Apply(ctx, sess.ID, Request{Op: "resume"})
	require.Error(t, err)
	g, err = s.Apply(ctx, sess.ID, Request{Op: "budget"})
	require.NoError(t, err)
	require.Equal(t, Active, g.Status)

	// A new process pauses inherited work; a live service does not pause its
	// own explicitly activated goals when the UI revisits the session.
	restored := NewService(q)
	t.Cleanup(restored.Shutdown)
	g, err = restored.Restore(ctx, sess.ID)
	require.NoError(t, err)
	require.Equal(t, Paused, g.Status)
	cap = 100
	g, err = restored.Apply(ctx, sess.ID, Request{Op: "budget", TokenBudget: &cap})
	require.NoError(t, err)
	require.Equal(t, Paused, g.Status)
	g, err = restored.Apply(ctx, sess.ID, Request{Op: "resume"})
	require.NoError(t, err)
	g, err = restored.Restore(ctx, sess.ID)
	require.NoError(t, err)
	require.Equal(t, Active, g.Status)
	oldID := g.ID
	g, err = restored.Apply(ctx, sess.ID, Request{Op: "set", Objective: "A different objective"})
	require.NoError(t, err)
	newID := g.ID
	_, err = restored.Apply(ctx, sess.ID, Request{Op: "complete", GoalID: oldID})
	require.Error(t, err)
	_, err = restored.PauseActive(ctx, sess.ID, oldID)
	require.NoError(t, err)
	g, err = restored.Account(ctx, sess.ID, oldID, 1000, time.Hour)
	require.NoError(t, err)
	require.Equal(t, newID, g.ID)
	require.Equal(t, Active, g.Status)
	require.Zero(t, g.TokensUsed)
	require.Zero(t, g.TimeUsedSeconds)
	_, err = restored.Apply(ctx, sess.ID, Request{Op: "drop"})
	require.NoError(t, err)
	g, err = restored.Get(ctx, sess.ID)
	require.NoError(t, err)
	require.Nil(t, g)
}
