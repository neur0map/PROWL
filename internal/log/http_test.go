package log

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHTTPDiagnosticsPreserveStreaming(t *testing.T) {
	for _, level := range []slog.Level{slog.LevelInfo, slog.LevelDebug} {
		t.Run(level.String(), func(t *testing.T) {
			old := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: level})))
			t.Cleanup(func() { slog.SetDefault(old) })
			release := make(chan struct{})
			var unblock sync.Once
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: first\n\n")
				w.(http.Flusher).Flush()
				select {
				case <-release:
					_, _ = io.WriteString(w, "data: last\n\n")
				case <-r.Context().Done():
				}
			}))
			t.Cleanup(server.Close)
			t.Cleanup(func() { unblock.Do(func() { close(release) }) })
			request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, nil)
			require.NoError(t, err)
			type result struct {
				data string
				err  error
			}
			returned := make(chan result, 2)
			go func() {
				response, err := NewHTTPClient().Do(request)
				if err != nil {
					returned <- result{err: err}
					return
				}
				defer response.Body.Close()
				first := make([]byte, len("data: first\n\n"))
				_, err = io.ReadFull(response.Body, first)
				returned <- result{data: string(first), err: err}
				if err != nil {
					return
				}
				rest, err := io.ReadAll(response.Body)
				returned <- result{data: string(rest), err: err}
			}()
			select {
			case first := <-returned:
				require.NoError(t, first.err)
				require.Equal(t, "data: first\n\n", first.data)
				unblock.Do(func() { close(release) })
				rest := <-returned
				require.NoError(t, rest.err)
				require.Equal(t, "data: last\n\n", rest.data)
			case <-time.After(2 * time.Second):
				unblock.Do(func() { close(release) })
				<-returned
				t.Fatal("Response headers were withheld until the stream finished")
			}
		})
	}
}

type diagnosticBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *diagnosticBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(p)
}

func (b *diagnosticBuffer) text() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.String()
}

func TestHTTPDiagnosticsFingerprintWithoutContent(t *testing.T) {
	var logs diagnosticBuffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })
	const requestBody = `{"prompt":"private-request-é"}`
	const responseBody = `{"error":"private-response"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != requestBody {
			http.Error(w, "Changed request body", http.StatusBadRequest)
			return
		}
		w.Header().Set("X-Secret", "response-header-secret")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, responseBody)
	}))
	t.Cleanup(server.Close)
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/?api_key=query-secret", strings.NewReader(requestBody))
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer header-secret")
	response, err := NewHTTPClient().Do(request)
	require.NoError(t, err)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, http.StatusInternalServerError, response.StatusCode)
	require.Equal(t, responseBody, string(body))
	text := logs.text()
	for _, secret := range []string{"private-request", "private-response", "query-secret", "header-secret", "response-header-secret"} {
		require.NotContains(t, text, secret)
	}
	fingerprints := map[string]map[string]any{}
	decoder := json.NewDecoder(strings.NewReader(text))
	for decoder.More() {
		var entry map[string]any
		require.NoError(t, decoder.Decode(&entry))
		if direction, ok := entry["direction"].(string); ok {
			fingerprints[direction] = entry
		}
	}
	for direction, content := range map[string]string{"request": requestBody, "response": responseBody} {
		entry := fingerprints[direction]
		require.NotNil(t, entry)
		require.Equal(t, float64(len(content)), entry["bytes"])
		require.Equal(t, fmt.Sprintf("%x", sha256.Sum256([]byte(content))), entry["sha256"])
	}
}
