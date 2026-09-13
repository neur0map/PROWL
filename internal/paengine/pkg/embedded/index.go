package embedded

import (
	"context"
	"time"

	"github.com/neur0map/prowl/internal/paengine/internal/application"
	"github.com/neur0map/prowl/internal/paengine/internal/cli"
	"github.com/neur0map/prowl/internal/paengine/internal/index"
)

// IndexProgress reports what one index refresh did for a project.
type IndexProgress struct {
	// Indexed, Parsed, Skipped, and Deleted count files in the structural pass.
	Indexed int
	Parsed  int
	Skipped int
	Deleted int

	// Embedded is how many chunks gained a vector during this pass. Remaining is
	// how many chunks still lack one afterwards, so zero means semantic search
	// is whole.
	Embedded  int
	Remaining int

	// Embedder reports whether an embedding backend was available at all. A host
	// must not wait for vectors when it is false: no pass will ever produce them.
	Embedder bool

	// EmbeddingError is the best-effort embedding failure, if any. A host backs
	// off rather than retrying a failing embedder in a tight loop.
	EmbeddingError string
}

// Whole reports whether the semantic index is complete.
func (p IndexProgress) Whole() bool { return p.Remaining == 0 }

// RefreshIndex refreshes root's index in a single pass: the structural index is
// brought up to date, embedding makes bounded progress, and the pass returns.
//
// It addresses root explicitly instead of through the process working directory,
// so a host can keep a project's index warm in the background while queries,
// which do swap the working directory, run against the same project. Embedding
// is deliberately bounded: a caller that wants the whole backlog drained calls
// this repeatedly, which keeps every acquisition of the project's refresh lock
// short.
func RefreshIndex(ctx context.Context, root string) (IndexProgress, error) {
	project, err := application.OpenProject(ctx, root, application.Options{
		EnableAI: true, InferencerProvider: cli.DefaultInferencer,
	})
	if err != nil {
		return IndexProgress{}, err
	}
	defer func() { _ = project.Close() }()

	initial := project.InitialRefresh
	progress := IndexProgress{
		Indexed:   initial.Summary.Indexed,
		Parsed:    initial.Summary.Parsed,
		Skipped:   initial.Summary.Skipped,
		Deleted:   initial.Summary.Deleted,
		Embedded:  initial.Embedded,
		Remaining: initial.VectorsRemaining,
		Embedder:  project.Inferencer != nil,
	}
	if initial.EmbeddingError != nil {
		progress.EmbeddingError = initial.EmbeddingError.Error()
	}
	return progress, nil
}

// WatchProject watches root recursively, calling onChange once per debounced
// burst of source changes, and blocks until ctx is cancelled. It is how a host
// learns that a project changed instead of polling for it.
func WatchProject(ctx context.Context, root string, debounce time.Duration, onChange func()) error {
	return index.Watch(ctx, root, debounce, onChange)
}
