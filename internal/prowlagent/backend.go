package prowlagent

import (
	"bytes"
	"context"

	"github.com/neur0map/prowl/internal/config"
	paembedded "github.com/neur0map/prowl/internal/paengine/pkg/embedded"
)

// runBackend runs an engine command in this process against workingDir. The
// engine is linked into Prowl, so there is no binary to locate and no process
// to spawn; the argv form is only how its commands are addressed.
func runBackend(ctx context.Context, _ *config.ProwlAgentOptions, workingDir string, args ...string) (stdout, stderr string, err error) {
	var so, se bytes.Buffer
	err = paembedded.Execute(ctx, workingDir, args, &so, &se)
	return so.String(), se.String(), err
}
