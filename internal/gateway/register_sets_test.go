package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestDiscoverSetsOffersTheOperatorsOwnSets is what makes a set usable where
// the work happens: the TUI picker can only offer a set it knows about, and
// hardcoding the list is how `auto:small` ended up in the picker for months
// while the router answered 400.
func TestDiscoverSetsOffersTheOperatorsOwnSets(t *testing.T) {
	t.Parallel()

	var sawAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[
			{"id":"auto"},{"id":"auto:smart"},{"id":"auto:balanced"},
			{"id":"auto:default"},{"id":"auto:writing"},
			{"id":"auto:deep work"},{"id":"groq/some-model"}]}`))
	}))
	defer srv.Close()

	sets := DiscoverSets(context.Background(), srv.URL, "tok")

	var ids []string
	for _, m := range sets {
		ids = append(ids, m.ID)
	}
	require.Equal(t, []string{"auto:deep work", "auto:writing"}, ids,
		"only the operator's own sets: every sort axis (including ones this "+
			"picker does not list, like balanced) and the default list that "+
			"plain auto already routes are not sets")
	require.Equal(t, "Bearer tok", sawAuth)
	require.Equal(t, "Set: Writing", sets[1].Name,
		"a set must be labelled as one, or the picker reads as a second list of models")
}

// TestDiscoverSetsIsSilentWithoutAGateway keeps registration working when no
// gateway is running, which is the normal first-run case.
func TestDiscoverSetsIsSilentWithoutAGateway(t *testing.T) {
	t.Parallel()

	require.Empty(t, DiscoverSets(context.Background(), "http://127.0.0.1:1/v1", "tok"),
		"an unreachable gateway must contribute nothing rather than fail")

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer bad.Close()
	require.Empty(t, DiscoverSets(context.Background(), bad.URL, "tok"))
}
