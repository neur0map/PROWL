package prowlagent

import (
	"bytes"
	"context"

	"github.com/neur0map/prowl/internal/config"
	paembedded "github.com/neur0map/prowl/internal/paengine/pkg/embedded"
)

// nativeEngineLabel is the Resolve label reported for the in-process engine,
// which has no external binary path.
const nativeEngineLabel = "(embedded prowl-agent)"

func resolveBackend(_ *config.ProwlAgentOptions) (string, bool) {
	return nativeEngineLabel, true
}

func availableBackend(_ *config.ProwlAgentOptions) bool {
	return true
}

// runBackend executes a prowl-agent command in process against workingDir.
func runBackend(ctx context.Context, _ *config.ProwlAgentOptions, workingDir string, args ...string) (stdout, stderr string, err error) {
	var so, se bytes.Buffer
	err = paembedded.Execute(ctx, workingDir, args, &so, &se)
	return so.String(), se.String(), err
}
