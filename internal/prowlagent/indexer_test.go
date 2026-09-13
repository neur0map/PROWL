package prowlagent

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/neur0map/prowl/internal/config"
	paembedded "github.com/neur0map/prowl/internal/paengine/pkg/embedded"
	"github.com/stretchr/testify/require"
)

// These tests replace the package-level engine seams, so they must not run in
// parallel with each other or with anything that indexes for real.

// indexScript records every refresh pass and answers from a fixed sequence, so a
// test can state exactly how much work is outstanding over time.
type indexScript struct {
	mu      sync.Mutex
	passes  []paembedded.IndexProgress
	calls   int
	stalled chan struct{}
}

func (s *indexScript) refresh(context.Context, string) (paembedded.IndexProgress, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if len(s.passes) == 0 {
		if s.stalled != nil {
			select {
			case s.stalled <- struct{}{}:
			default:
			}
		}
		return paembedded.IndexProgress{Embedder: true, Remaining: 10}, nil
	}
	pass := s.passes[0]
	s.passes = s.passes[1:]
	return pass, nil
}

func (s *indexScript) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// watcher records the change callback the keeper installed and lets a test fire
// it, standing in for a filesystem event.
type watcher struct {
	mu       sync.Mutex
	onChange func()
	fired    chan struct{}
}

func (w *watcher) watch(ctx context.Context, _ string, _ time.Duration, onChange func()) error {
	w.mu.Lock()
	w.onChange = onChange
	w.mu.Unlock()
	close(w.fired)
	<-ctx.Done()
	return ctx.Err()
}

func (w *watcher) change(t *testing.T) {
	t.Helper()
	w.mu.Lock()
	onChange := w.onChange
	w.mu.Unlock()
	require.NotNil(t, onChange, "keeper must install a change watcher")
	onChange()
}

// installSeams points the keeper at the given script and watcher for the
// duration of a test.
func installSeams(t *testing.T, script *indexScript, watch *watcher) {
	t.Helper()
	originalBootstrap, originalRefresh, originalWatch := bootstrapIndex, refreshIndex, watchProject
	t.Cleanup(func() {
		bootstrapIndex, refreshIndex, watchProject = originalBootstrap, originalRefresh, originalWatch
	})
	bootstrapIndex = func(context.Context, *config.ProwlAgentOptions, string) error { return nil }
	refreshIndex = script.refresh
	if watch != nil {
		watchProject = watch.watch
	}
}

func keeperTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, ".git"), 0o755))
	return dir
}

// TestKeeperDrainsThenWaitsForAChange proves the two halves of the keeper's
// contract: it keeps making bounded passes while work remains, and it does not
// keep indexing once the index is whole -- it waits for the project to change.
func TestKeeperDrainsThenWaitsForAChange(t *testing.T) {
	dir := keeperTestDir(t)
	script := &indexScript{passes: []paembedded.IndexProgress{
		{Embedder: true, Embedded: 32, Remaining: 64},
		{Embedder: true, Embedded: 32, Remaining: 32},
		{Embedder: true, Embedded: 32, Remaining: 0},
	}}
	watch := &watcher{fired: make(chan struct{})}
	installSeams(t, script, watch)

	cfg, err := config.Init(dir, "", false)
	require.NoError(t, err)

	keeper := StartIndexKeeper(t.Context(), cfg)
	require.NotNil(t, keeper)
	t.Cleanup(keeper.Stop)

	<-watch.fired
	require.Eventually(t, func() bool { return script.callCount() >= 3 }, 5*time.Second, 10*time.Millisecond,
		"the keeper must keep draining while work remains")

	// A whole index leaves the keeper idle: it waits for the project to change
	// instead of polling the refresh lock.
	time.Sleep(3 * indexPassPause)
	quiet := script.callCount()
	time.Sleep(3 * indexPassPause)
	require.Equal(t, quiet, script.callCount(), "a whole index must leave the keeper idle")

	// A change then earns a reindex, without the user asking for one.
	watch.change(t)
	require.Eventually(t, func() bool { return script.callCount() > quiet }, 5*time.Second, 10*time.Millisecond,
		"a change must trigger a reindex")
}

// TestKeeperStopsWhenNoPassMakesProgress guards against a tight loop: an
// embedder that is unavailable, failing, or simply not embedding anything must
// leave the keeper waiting rather than spinning on the refresh lock.
func TestKeeperStopsWhenNoPassMakesProgress(t *testing.T) {
	dir := keeperTestDir(t)
	script := &indexScript{passes: []paembedded.IndexProgress{
		{Embedder: true, Embedded: 0, Remaining: 4096, EmbeddingError: "embedder unavailable"},
	}}
	watch := &watcher{fired: make(chan struct{})}
	installSeams(t, script, watch)

	cfg, err := config.Init(dir, "", false)
	require.NoError(t, err)

	keeper := StartIndexKeeper(t.Context(), cfg)
	require.NotNil(t, keeper)
	t.Cleanup(keeper.Stop)

	<-watch.fired
	require.Eventually(t, func() bool { return script.callCount() >= 1 }, 5*time.Second, 10*time.Millisecond)

	time.Sleep(3 * indexPassPause)
	require.Equal(t, 1, script.callCount(),
		"a pass that embedded nothing must not be retried immediately")
}

// TestKeeperStopEndsIndexWork proves a session ending stops the keeper: no
// index pass may outlive the workspace it belongs to.
func TestKeeperStopEndsIndexWork(t *testing.T) {
	dir := keeperTestDir(t)
	script := &indexScript{}
	watch := &watcher{fired: make(chan struct{})}
	installSeams(t, script, watch)

	cfg, err := config.Init(dir, "", false)
	require.NoError(t, err)

	keeper := StartIndexKeeper(t.Context(), cfg)
	require.NotNil(t, keeper)
	<-watch.fired
	keeper.Stop()

	done := make(chan struct{})
	go func() {
		defer close(done)
		keeper.Stop()
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop must be idempotent and return once the keeper is stopped")
	}
}

// TestStartIndexKeeperSkipsNonProjects keeps a launch in an ordinary directory
// from enqueueing an index build for it.
func TestStartIndexKeeperSkipsNonProjects(t *testing.T) {
	cfg, err := config.Init(t.TempDir(), "", false)
	require.NoError(t, err)
	require.Nil(t, StartIndexKeeper(t.Context(), cfg))
}
