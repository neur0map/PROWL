package gateway

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestOpenEngineSeedsCatalog is the one place the real 400-row startup state is
// exercised end to end: opening an engine with production defaults must leave
// the catalog populated AND queryable through the same DB handle the HTTP layer
// reads. The api package opts out of seeding by default, so without this a
// product whose real startup state no test ever sees could regress unnoticed.
func TestOpenEngineSeedsCatalog(t *testing.T) {
	t.Parallel()

	e, err := OpenEngine(context.Background(), t.TempDir(), EngineOptions{})
	require.NoError(t, err)
	defer func() { _ = e.Close() }()

	db := e.DB()
	require.Equal(t, 365, catalogCount(t, db, "models"), "fresh engine opened to an empty chat catalog")
	require.Equal(t, 22, catalogCount(t, db, "embedding_models"))
	require.Equal(t, 13, catalogCount(t, db, "media_models"))

	// Read a known model the way the dashboard's list query does, proving the
	// rows are reachable through a normal read, not merely present.
	var name string
	require.NoError(t, db.QueryRow(
		`SELECT display_name FROM models
		 WHERE platform = 'google' AND model_id = 'gemini-2.5-flash' AND endpoint_scope = ''`,
	).Scan(&name))
	require.Equal(t, "Gemini 2.5 Flash", name)
}

// TestOpenEngineSkipCatalogSeed proves the opt-out a test needs when it wants a
// deliberately empty catalog: it gets one, and has to ask for it out loud.
func TestOpenEngineSkipCatalogSeed(t *testing.T) {
	t.Parallel()

	e, err := OpenEngine(context.Background(), t.TempDir(), EngineOptions{SkipCatalogSeed: true})
	require.NoError(t, err)
	defer func() { _ = e.Close() }()

	require.Equal(t, 0, catalogCount(t, e.DB(), "models"), "SkipCatalogSeed must leave the catalog empty")
}

func catalogCount(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRow("SELECT count(*) FROM "+table).Scan(&n))
	return n
}
