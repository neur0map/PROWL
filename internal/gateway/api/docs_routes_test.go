package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestOpenAPISpecMatchesTheServedSurface keeps the reference honest in both
// directions. Documenting an endpoint that does not exist sends callers to a
// 404, and omitting one hides real capability; neither is visible without
// comparing the spec to the routes actually served.
func TestOpenAPISpecMatchesTheServedSurface(t *testing.T) {
	t.Parallel()

	s := testServer(t, Options{MachineKey: compatMachineKey})

	resp, body := do(t, s, http.MethodGet, "/v1/openapi.json", "", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, "body was %s", body)

	var spec struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &spec))

	documented := map[string]bool{}
	for path, methods := range spec.Paths {
		for method := range methods {
			documented[strings.ToUpper(method)+" "+path] = true
		}
	}
	require.NotEmpty(t, documented)

	// Every documented operation must actually be served. An unauthenticated
	// probe is enough: 401 proves the route exists, 404 proves it does not.
	for op := range documented {
		method, path, ok := strings.Cut(op, " ")
		require.True(t, ok)
		probe, _ := do(t, s, method, path, "", nil)
		require.NotEqual(t, http.StatusNotFound, probe.StatusCode,
			"%s is documented but not served", op)
	}

	// And the whole list is what the server advertises.
	for _, ep := range v1Endpoints {
		require.True(t, documented[ep.Method+" "+ep.Path],
			"%s %s is in v1Endpoints but missing from the spec", ep.Method, ep.Path)
	}
}

// TestDocsPageIsSelfContained pins the constraint that makes the reference
// usable offline: Prowl ships as one binary, so the viewer must not reach for
// a CDN, and it must render the spec through DOM APIs rather than innerHTML.
func TestDocsPageIsSelfContained(t *testing.T) {
	t.Parallel()

	s := testServer(t, Options{MachineKey: compatMachineKey})
	resp, body := do(t, s, http.MethodGet, "/v1/docs", "", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, resp.Header.Get("Content-Type"), "text/html")
	require.NotContains(t, body, "//cdn.")
	require.NotContains(t, body, "unpkg")
	require.NotContains(t, body, "http://")
	require.NotContains(t, body, "innerHTML")
}
