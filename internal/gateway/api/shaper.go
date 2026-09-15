package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/neur0map/prowl/internal/gateway"
	"github.com/neur0map/prowl/internal/gateway/provider"
)

// responseShaper renders one inference surface's wire format.
//
// Every inference route parses its own dialect into the normalised request and
// then runs the SAME engine path; only the rendering differs, and that is all
// this interface covers. A surface that needed to influence routing, quota or
// failover would have to bypass the engine, which is precisely what must not
// happen — an OpenAI request, an Anthropic message and a media call all fail
// over, cool down and consume quota identically.
type responseShaper interface {
	// buffered renders a complete (non-streaming) upstream response.
	buffered(resp *provider.ChatResponse) ([]byte, error)
	// newStream returns the per-request stream renderer.
	newStream() streamShaper
	// writeError writes this surface's error envelope. The message arrives
	// already scrubbed of every credential the request revealed, so a shaper
	// cannot leak one by rendering it differently. A shaper MUST NOT emit
	// TypeAuthentication: the dashboard ends the operator's session on a 401
	// carrying that type, so an application's failed inference call would sign
	// a human out of a browser tab they are not looking at.
	writeError(w http.ResponseWriter, status int, kind gateway.ExhaustionKind, code, message string, retryAt time.Time)
}

// streamShaper renders one surface's server-sent event frames.
type streamShaper interface {
	// frame renders a single upstream chunk, or nil to emit nothing.
	frame(chunk *provider.ChatChunk) []byte
	// done closes the stream.
	done() []byte
	// streamError renders a mid-stream failure. The HTTP status was already
	// sent as 200, so this is the only channel left to report it.
	streamError(message string) []byte
}

// openAIShaper relays the provider's OpenAI-shaped bytes unchanged.
//
// It is the default every native surface uses, and "unchanged" is the point:
// the provider already speaks this dialect, so re-encoding through a struct
// would drop fields a caller sent or a model returned. Fields this gateway
// does not model survive because the raw payload is what gets written.
type openAIShaper struct{}

func (openAIShaper) buffered(resp *provider.ChatResponse) ([]byte, error) {
	if len(resp.Raw) > 0 {
		return resp.Raw, nil
	}
	return json.Marshal(resp)
}

func (openAIShaper) newStream() streamShaper { return openAIStream{} }

func (openAIShaper) writeError(w http.ResponseWriter, status int, _ gateway.ExhaustionKind, code, message string, retryAt time.Time) {
	// A rate limit carries the structured retry moment and the Retry-After
	// header, which a client backing off reads instead of guessing.
	if status == http.StatusTooManyRequests && !retryAt.IsZero() {
		WriteRateLimited(w, message, retryAt)
		return
	}
	WriteErrorCode(w, status, exhaustionType(status), code, message)
}

// openAIStream re-emits each upstream frame verbatim, framed as SSE.
//
// The provider's reader strips the "data: " prefix and returns the payload in
// Raw, and treats the upstream sentinel as EOF, so both are restored here: a
// client reading this stream sees the same bytes the provider sent.
type openAIStream struct{}

func (openAIStream) frame(chunk *provider.ChatChunk) []byte {
	if len(chunk.Raw) == 0 {
		return nil
	}
	return append(append([]byte("data: "), chunk.Raw...), '\n', '\n')
}

func (openAIStream) done() []byte { return []byte("data: [DONE]\n\n") }

func (openAIStream) streamError(message string) []byte {
	frame, _ := json.Marshal(errorBody{Error: errorDetail{Message: message, Type: TypeStream}})
	// The terminator follows the error so a client's stream parser completes
	// rather than waiting for frames that will never come.
	return append(append([]byte("data: "), frame...), []byte("\n\ndata: [DONE]\n\n")...)
}
