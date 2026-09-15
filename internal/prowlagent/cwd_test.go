package prowlagent

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/neur0map/prowl/internal/config"
)

// TestEngineCallLeavesProcessCwdAlone is the hazard this guards: the engine
// runs in-process, so changing the working directory to target a project
// would change it for every other goroutine in the harness at the same time.
// A file tool resolving a relative path mid-call would silently read from the
// wrong project.
func TestEngineCallLeavesProcessCwdAlone(t *testing.T) {
	before, err := os.Getwd()
	require.NoError(t, err)

	project := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(project, "main.go"), []byte("package main\n"), 0o644))

	// A concurrent observer stands in for the rest of the harness: it reads
	// the working directory while the engine call is in flight.
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		observed []string
		stop     = make(chan struct{})
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				if dir, err := os.Getwd(); err == nil {
					mu.Lock()
					observed = append(observed, dir)
					mu.Unlock()
				}
			}
		}
	}()

	opts := &config.ProwlAgentOptions{}
	_, _, _ = Run(context.Background(), opts, project, "status", "--json")

	close(stop)
	wg.Wait()

	after, err := os.Getwd()
	require.NoError(t, err)
	require.Equal(t, before, after, "the engine must restore the working directory")

	mu.Lock()
	defer mu.Unlock()
	for _, dir := range observed {
		require.NotEqual(t, project, dir,
			"no other goroutine may ever observe the engine's target directory as the process cwd")
	}
}
