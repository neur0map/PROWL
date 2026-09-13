package log

import (
	"crypto/sha256"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// NewHTTPClient adds content-free diagnostics without buffering responses.
func NewHTTPClient() *http.Client {
	return &http.Client{Transport: &HTTPRoundTripLogger{Transport: http.DefaultTransport}}
}

// HTTPRoundTripLogger records request fingerprints and response timing in debug
// mode. Bodies remain streaming; credentials, headers and content are not logged.
type HTTPRoundTripLogger struct {
	Transport http.RoundTripper
}

var httpRequestSequence atomic.Uint64

func (h *HTTPRoundTripLogger) RoundTrip(req *http.Request) (*http.Response, error) {
	transport := h.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	logger := slog.Default()
	if !logger.Enabled(req.Context(), slog.LevelDebug) {
		return transport.RoundTrip(req)
	}

	id := httpRequestSequence.Add(1)
	start := time.Now()
	logger.DebugContext(req.Context(), "HTTP request started",
		"request_id", id, "method", req.Method, "host", req.URL.Host,
		"content_length", req.ContentLength)
	if req.Body != nil && req.Body != http.NoBody {
		request := new(http.Request)
		*request = *req
		request.Body = newFingerprintBody(req.Body, func(bytes int64, fingerprint string, complete bool) {
			logger.Debug("HTTP body fingerprint", "request_id", id,
				"direction", "request", "bytes", bytes, "sha256", fingerprint, "complete", complete)
		})
		req = request
	}

	resp, err := transport.RoundTrip(req)
	if err != nil {
		logger.DebugContext(req.Context(), "HTTP request failed",
			"request_id", id, "error_type", fmt.Sprintf("%T", err),
			"duration_ms", time.Since(start).Milliseconds())
		return resp, err
	}
	logger.DebugContext(req.Context(), "HTTP response headers",
		"request_id", id, "status_code", resp.StatusCode,
		"headers_ms", time.Since(start).Milliseconds())
	if resp.Body != nil && resp.Body != http.NoBody {
		resp.Body = newFingerprintBody(resp.Body, func(bytes int64, fingerprint string, complete bool) {
			logger.Debug("HTTP body fingerprint", "request_id", id,
				"direction", "response", "bytes", bytes, "sha256", fingerprint,
				"complete", complete, "duration_ms", time.Since(start).Milliseconds())
		})
	}
	return resp, nil
}

type fingerprintBody struct {
	io.ReadCloser
	mu       sync.Mutex
	digest   hash.Hash
	bytes    int64
	finished bool
	report   func(int64, string, bool)
}

func newFingerprintBody(body io.ReadCloser, report func(int64, string, bool)) *fingerprintBody {
	return &fingerprintBody{ReadCloser: body, digest: sha256.New(), report: report}
}

func (b *fingerprintBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.mu.Lock()
	if !b.finished {
		_, _ = b.digest.Write(p[:n])
		b.bytes += int64(n)
	}
	b.mu.Unlock()
	if err != nil {
		b.finish(err == io.EOF)
	}
	return n, err
}

func (b *fingerprintBody) Close() error {
	err := b.ReadCloser.Close()
	b.finish(false)
	return err
}

func (b *fingerprintBody) finish(complete bool) {
	b.mu.Lock()
	if b.finished {
		b.mu.Unlock()
		return
	}
	b.finished = true
	bytes := b.bytes
	fingerprint := fmt.Sprintf("%x", b.digest.Sum(nil))
	b.mu.Unlock()
	b.report(bytes, fingerprint, complete)
}
