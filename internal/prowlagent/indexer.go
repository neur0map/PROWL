package prowlagent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/neur0map/prowl/internal/config"
	paembedded "github.com/neur0map/prowl/internal/paengine/pkg/embedded"
)

// Seams for the engine calls the keeper makes, so tests can drive its
// orchestration without building a real index.
var (
	bootstrapIndex = BootstrapIndex
	refreshIndex   = paembedded.RefreshIndex
	watchProject   = paembedded.WatchProject
	indexStatus    = QueryStatus
)

const (
	// indexPassPause separates bounded refresh passes. Without it the keeper
	// would re-acquire the project's refresh lock back to back, and a query
	// arriving between passes would queue behind another one every time.
	indexPassPause = 100 * time.Millisecond

	// indexWatchDebounce coalesces an editing burst into one reindex: an agent
	// writing a file in several steps, or a formatter rewriting a tree, is one
	// change as far as the index is concerned.
	indexWatchDebounce = 750 * time.Millisecond

	// indexStallRetry is how long the keeper waits after a pass that made no
	// progress before trying again. A failing or busy embedder must not spin,
	// but it should still heal without waiting for a file change that may never
	// come.
	indexStallRetry = 5 * time.Minute

	// indexWatchRetry is how long the keeper waits before re-establishing a
	// change watcher that failed, so a transient filesystem error does not end
	// automatic reindexing for the session.
	indexWatchRetry = 30 * time.Second
)

// BootstrapIndex prepares a project's index: it creates the .prowl workspace
// when none exists yet, then refreshes the AGENTS.md map an agent reasons from.
// It is idempotent, and cheap once the workspace exists.
func BootstrapIndex(ctx context.Context, opts *config.ProwlAgentOptions, workingDir string) error {
	// Bootstrap the workspace/index when it does not exist yet. `status --json`
	// is not the signal here: with no workspace it answers with the global list
	// of known projects (null when there are none) and still exits zero, so an
	// unchanged-status gate would silently skip the first index build. What
	// matters is whether this directory has a usable index at all.
	if st, err := QueryStatus(ctx, opts, workingDir); err != nil || !st.Indexed() {
		if _, serr, ierr := Run(ctx, opts, workingDir, "init", "--no-input", "--yes", "--integrations", "none"); ierr != nil {
			if m := strings.TrimSpace(serr); m != "" {
				return fmt.Errorf("prowl-agent init: %s", m)
			}
			return fmt.Errorf("prowl-agent init: %w", ierr)
		}
	}
	_, serr, err := Run(ctx, opts, workingDir, "overview", "--format", "toon")
	if err != nil {
		if m := strings.TrimSpace(serr); m != "" {
			return fmt.Errorf("prowl-agent overview: %s", m)
		}
		return fmt.Errorf("prowl-agent overview: %w", err)
	}
	return nil
}

// EnsureIndex builds a project's index to completion and returns once nothing
// is left to index: the workspace is created when missing, the structural index
// and its AGENTS.md map are refreshed, and the whole embedding backlog is
// drained. It is the blocking, user-visible counterpart of the background
// keeper.
func EnsureIndex(ctx context.Context, opts *config.ProwlAgentOptions, workingDir string) error {
	if err := BootstrapIndex(ctx, opts, workingDir); err != nil {
		return err
	}
	return drainIndex(ctx, workingDir, nil)
}

// RefreshProjectIndex brings the project's index up to date in bounded,
// incremental passes without the full overview bootstrap: the structural pass
// hash-skips unchanged files, so an edited file is re-parsed and only its
// chunks are re-embedded. It is the on-demand counterpart the UI triggers after
// an agent turn edits files, when the workspace already exists.
func RefreshProjectIndex(ctx context.Context, workingDir string) error {
	return drainIndex(ctx, workingDir, nil)
}

// ShouldAutoIndex reports whether workingDir is safe to auto-index: a git
// working tree, or a folder already carrying a prowl-agent index. This keeps a
// bare launch in a large non-project directory (e.g. $HOME) from triggering a
// huge unsolicited index build.
func ShouldAutoIndex(workingDir string) bool {
	if _, err := os.Stat(filepath.Join(workingDir, ".git")); err == nil {
		return true
	}
	if _, err := os.Stat(filepath.Join(workingDir, ".prowl", "index.db")); err == nil {
		return true
	}
	return false
}

// IndexKeeper keeps one project's index warm for as long as its session lives.
// It builds what is missing, drains the vector backlog in bounded passes, and
// rebuilds after the project changes -- all off the interactive path, so the
// first query of a session never pays for a full index build.
//
// A nil *IndexKeeper is a no-op, so callers may start one unconditionally.
type IndexKeeper struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// StartIndexKeeper starts the keeper for cfg's working directory and returns it,
// or nil when background indexing is off, the engine is unavailable, or the
// directory is not a project. The keeper runs until Stop is called or parent is
// cancelled.
func StartIndexKeeper(parent context.Context, cfg *config.ConfigStore) *IndexKeeper {
	opts := cfg.Config().Options.GetProwlAgent()
	if !opts.AutoIndexEnabled() {
		return nil
	}
	if !Available(opts) {
		return nil
	}
	workingDir := cfg.WorkingDir()
	if !ShouldAutoIndex(workingDir) {
		slog.Debug("Skipping prowl-agent background indexing for non-project directory",
			"component", "prowl-agent", "dir", workingDir)
		return nil
	}
	ctx, cancel := context.WithCancel(parent)
	keeper := &IndexKeeper{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(keeper.done)
		keepIndexFresh(ctx, opts, workingDir)
	}()
	return keeper
}

// Stop ends the keeper and waits for its goroutine, so no index work outlives
// the session that started it. It is idempotent and safe on a nil keeper.
func (k *IndexKeeper) Stop() {
	if k == nil {
		return
	}
	k.cancel()
	<-k.done
}

// keepIndexFresh is the keeper's loop: build what the project is missing, drain
// the backlog in bounded passes, then wait for the project to change and start
// over.
func keepIndexFresh(ctx context.Context, opts *config.ProwlAgentOptions, workingDir string) {
	start := time.Now()
	// Index on open only when the project has no usable index yet. A project
	// that already carries a valid DB skips the expensive overview/tree-walk
	// bootstrap; the change watcher and bounded drain passes below still pick
	// up edits made during the session.
	if st, err := indexStatus(ctx, opts, workingDir); err == nil && st.Indexed() {
		slog.Info("Prowl-agent index already present; skipping full bootstrap on open",
			"component", "prowl-agent", "dir", workingDir)
	} else if err := bootstrapIndex(ctx, opts, workingDir); err != nil {
		if ctx.Err() != nil {
			return
		}
		slog.Warn("Prowl-agent index bootstrap failed",
			"component", "prowl-agent", "dir", workingDir, "error", err)
	}

	changes := make(chan struct{}, 1)
	watchCtx, stopWatch := context.WithCancel(ctx)
	defer stopWatch()
	go watchForChanges(watchCtx, workingDir, changes)

	drained := false
	for {
		if err := drainIndex(ctx, workingDir, nil); err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Warn("Prowl-agent index refresh failed",
				"component", "prowl-agent", "dir", workingDir, "error", err)
		} else if !drained {
			drained = true
			slog.Info("Prowl-agent index ready",
				"component", "prowl-agent", "dir", workingDir, "took", time.Since(start).String())
		}
		select {
		case <-ctx.Done():
			return
		case <-changes:
		case <-time.After(indexStallRetry):
			// Wake on a schedule too: a pass that made no progress (a busy or
			// failing embedder) is retried, and an index that went stale without a
			// filesystem event we could observe is re-checked.
		}
	}
}

// drainIndex makes bounded refresh passes until the semantic index is whole or a
// pass stops making progress. Each pass is short and resumable, so a query
// waiting on the project's refresh lock is never stuck behind a whole rebuild.
func drainIndex(ctx context.Context, workingDir string, onPass func(paembedded.IndexProgress)) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		progress, err := refreshIndex(ctx, workingDir)
		if err != nil {
			return err
		}
		if onPass != nil {
			onPass(progress)
		}
		if progress.Whole() || !progress.Embedder || progress.Embedded == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(indexPassPause):
		}
	}
}

// watchForChanges feeds the keeper one signal per debounced burst of changes,
// re-establishing the watcher after a failure so automatic reindexing survives
// a transient filesystem error.
func watchForChanges(ctx context.Context, workingDir string, changes chan<- struct{}) {
	for ctx.Err() == nil {
		err := watchProject(ctx, workingDir, indexWatchDebounce, func() {
			select {
			case changes <- struct{}{}:
			default: // A signal is already pending; one rebuild covers both.
			}
		})
		if ctx.Err() != nil {
			return
		}
		slog.Warn("Prowl-agent change watcher stopped",
			"component", "prowl-agent", "dir", workingDir, "error", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(indexWatchRetry):
		}
	}
}
