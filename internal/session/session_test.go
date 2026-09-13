package session

import (
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/neur0map/prowl/internal/db"
)

func TestEstimatedUsageStateSurvivesFetchModifySave(t *testing.T) {
	dataDir := t.TempDir()
	t.Cleanup(func() {
		require.NoError(t, db.Release(dataDir))
		db.ResetPool()
	})

	conn, err := db.Connect(t.Context(), dataDir)
	require.NoError(t, err)

	sessions := NewService(db.New(conn), conn)

	created, err := sessions.Create(t.Context(), "test")
	require.NoError(t, err)
	created.PromptTokens = 100
	created.CompletionTokens = 50
	created.EstimatedUsage = true

	saved, err := sessions.Save(t.Context(), created)
	require.NoError(t, err)
	require.True(t, saved.EstimatedUsage)

	fetched, err := sessions.Get(t.Context(), created.ID)
	require.NoError(t, err)
	require.True(t, fetched.EstimatedUsage)

	fetched.Todos = []Todo{{
		Content:    "Check estimate state",
		Status:     TodoStatusInProgress,
		ActiveForm: "Checking estimate state",
	}}

	updated, err := sessions.Save(t.Context(), fetched)
	require.NoError(t, err)
	require.True(t, updated.EstimatedUsage)

	refetched, err := sessions.Get(t.Context(), created.ID)
	require.NoError(t, err)
	require.True(t, refetched.EstimatedUsage)
}

func TestEstimatedUsageStateCanBeClearedByExplicitSave(t *testing.T) {
	dataDir := t.TempDir()
	t.Cleanup(func() {
		require.NoError(t, db.Release(dataDir))
		db.ResetPool()
	})

	conn, err := db.Connect(t.Context(), dataDir)
	require.NoError(t, err)

	sessions := NewService(db.New(conn), conn)

	created, err := sessions.Create(t.Context(), "test")
	require.NoError(t, err)
	created.PromptTokens = 100
	created.CompletionTokens = 50
	created.EstimatedUsage = true

	saved, err := sessions.Save(t.Context(), created)
	require.NoError(t, err)
	require.True(t, saved.EstimatedUsage)

	saved.EstimatedUsage = false
	updated, err := sessions.Save(t.Context(), saved)
	require.NoError(t, err)
	require.False(t, updated.EstimatedUsage)

	refetched, err := sessions.Get(t.Context(), created.ID)
	require.NoError(t, err)
	require.False(t, refetched.EstimatedUsage)
}

func TestRequestCostsRemainAtomicAcrossAncestorsAndStaleSaves(t *testing.T) {
	dataDir := t.TempDir()
	t.Cleanup(func() {
		require.NoError(t, db.Release(dataDir))
		db.ResetPool()
	})
	conn, err := db.Connect(t.Context(), dataDir)
	require.NoError(t, err)
	sessions := NewService(db.New(conn), conn)
	root, err := sessions.Create(t.Context(), "Root")
	require.NoError(t, err)
	child, err := sessions.CreateTaskSession(t.Context(), "child", root.ID, "Child")
	require.NoError(t, err)
	leaf, err := sessions.CreateTaskSession(t.Context(), "leaf", child.ID, "Leaf")
	require.NoError(t, err)
	sibling, err := sessions.CreateTaskSession(t.Context(), "sibling", root.ID, "Sibling")
	require.NoError(t, err)

	var workers sync.WaitGroup
	errors := make(chan error, 32)
	for range cap(errors) {
		workers.Go(func() { errors <- sessions.AddCost(t.Context(), leaf.ID, 0.01) })
	}
	workers.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	// This snapshot predates every request and must not reset their costs.
	root.PromptTokens = 8000
	_, err = sessions.Save(t.Context(), root)
	require.NoError(t, err)
	for _, id := range []string{root.ID, child.ID, leaf.ID} {
		charged, err := sessions.Get(t.Context(), id)
		require.NoError(t, err)
		require.InDelta(t, 0.32, charged.Cost, 1e-12)
	}
	uncharged, err := sessions.Get(t.Context(), sibling.ID)
	require.NoError(t, err)
	require.Zero(t, uncharged.Cost)
	require.ErrorIs(t, sessions.AddCost(t.Context(), "missing-session", 1), sql.ErrNoRows)
	unchanged, err := sessions.Get(t.Context(), root.ID)
	require.NoError(t, err)
	require.InDelta(t, 0.32, unchanged.Cost, 1e-12)
	require.Equal(t, int64(8000), unchanged.PromptTokens)
}

func TestPromptCacheCommitIsAtomicIdempotentAndSurvivesReload(t *testing.T) {
	dataDir := t.TempDir()
	t.Cleanup(func() {
		require.NoError(t, db.Release(dataDir))
		db.ResetPool()
	})
	conn, err := db.Connect(t.Context(), dataDir)
	require.NoError(t, err)
	sessions := NewService(db.New(conn), conn)
	root, err := sessions.Create(t.Context(), "Root")
	require.NoError(t, err)
	child, err := sessions.CreateTaskSession(t.Context(), "cache-child", root.ID, "Child")
	require.NoError(t, err)
	now := time.Now().UTC()
	lease := PromptCache{
		ID: "resource-identity", Key: "prefix-identity", Resource: "cachedContents/resource",
		SessionID: child.ID, CreatedAt: now, ExpiresAt: now.Add(time.Hour),
		Tokens: 10000, CreationCost: 0.01, StorageCost: 0.005,
	}
	var workers sync.WaitGroup
	results := make(chan error, 16)
	for range cap(results) {
		workers.Go(func() { results <- sessions.SavePromptCache(t.Context(), lease) })
	}
	workers.Wait()
	close(results)
	for err := range results {
		require.NoError(t, err)
	}
	reloaded := NewService(db.New(conn), conn)
	stored, err := reloaded.GetPromptCache(t.Context(), lease.Key)
	require.NoError(t, err)
	require.Equal(t, lease.Resource, stored.Resource)
	require.WithinDuration(t, lease.ExpiresAt, stored.ExpiresAt, 0)
	for _, id := range []string{root.ID, child.ID} {
		charged, err := reloaded.Get(t.Context(), id)
		require.NoError(t, err)
		require.InDelta(t, 0.015, charged.Cost, 1e-12)
	}
	require.NoError(t, reloaded.InvalidatePromptCache(t.Context(), lease.ID))
	_, err = reloaded.GetPromptCache(t.Context(), lease.Key)
	require.ErrorIs(t, err, sql.ErrNoRows)
	// Invalidating reuse must not erase the paid TTL or let a late duplicate
	// commit charge it again.
	require.NoError(t, reloaded.SavePromptCache(t.Context(), lease))
	invalid := lease
	invalid.ID, invalid.Key, invalid.SessionID = "orphan", "orphan-prefix", "missing-session"
	require.Error(t, reloaded.SavePromptCache(t.Context(), invalid))
	_, err = reloaded.GetPromptCache(t.Context(), invalid.Key)
	require.ErrorIs(t, err, sql.ErrNoRows)
	expired := lease
	expired.ID, expired.Key = "expired", "expired-prefix"
	expired.CreatedAt, expired.ExpiresAt = now.Add(-2*time.Hour), now.Add(-time.Hour)
	require.NoError(t, reloaded.SavePromptCache(t.Context(), expired))
	_, err = reloaded.GetPromptCache(t.Context(), expired.Key)
	require.ErrorIs(t, err, sql.ErrNoRows)
	charged, err := reloaded.Get(t.Context(), root.ID)
	require.NoError(t, err)
	require.InDelta(t, 0.03, charged.Cost, 1e-12)
}

func TestFocusPreferenceSurvivesStaleSavesAndServiceRestart(t *testing.T) {
	dataDir := t.TempDir()
	t.Cleanup(func() {
		require.NoError(t, db.Release(dataDir))
		db.ResetPool()
	})
	conn, err := db.Connect(t.Context(), dataDir)
	require.NoError(t, err)
	sessions := NewService(db.New(conn), conn)
	stale, err := sessions.Create(t.Context(), "Focus lifecycle")
	require.NoError(t, err)

	_, err = sessions.SetFocusMode(t.Context(), stale.ID, FocusModeOn)
	require.NoError(t, err)
	stale.PromptTokens = 8000
	saved, err := sessions.Save(t.Context(), stale)
	require.NoError(t, err)
	require.Equal(t, FocusModeOn, saved.FocusMode)

	sessions = NewService(db.New(conn), conn)
	resumed, err := sessions.Get(t.Context(), stale.ID)
	require.NoError(t, err)
	require.Equal(t, FocusModeOn, resumed.FocusMode)
	require.Equal(t, int64(8000), resumed.PromptTokens)
	_, err = sessions.SetFocusMode(t.Context(), stale.ID, FocusModeOff)
	require.NoError(t, err)
	saved, err = sessions.Save(t.Context(), resumed)
	require.NoError(t, err)
	require.Equal(t, FocusModeOff, saved.FocusMode)

	_, err = sessions.SetFocusMode(t.Context(), stale.ID, "")
	require.Error(t, err)
	unchanged, err := sessions.Get(t.Context(), stale.ID)
	require.NoError(t, err)
	require.Equal(t, FocusModeOff, unchanged.FocusMode)
}
