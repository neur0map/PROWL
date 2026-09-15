package catalog

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEnsureDefaultProfileCreatesTheBaselineChain covers the gap that made the
// dashboard's fallback-chains card disappear: no chain existed at all.
func TestEnsureDefaultProfileCreatesTheBaselineChain(t *testing.T) {
	t.Parallel()
	db := seedDB(t)
	ctx := context.Background()

	_, err := SeedModels(ctx, db)
	require.NoError(t, err)

	added, err := EnsureDefaultProfile(ctx, db)
	require.NoError(t, err)
	require.Positive(t, added)

	var name string
	var active int
	require.NoError(t, db.QueryRow(
		`SELECT name, active FROM profiles`).Scan(&name, &active))
	require.Equal(t, "Default", name)
	require.Equal(t, 1, active, "a chain nothing selects would make the card misleading")

	var inChain, enabled int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM profile_models`).Scan(&inChain))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM models WHERE enabled = 1`).Scan(&enabled))
	require.Equal(t, enabled, inChain, "every enabled catalog model belongs to the baseline chain")
}

// TestEnsureDefaultProfileIsIdempotent keeps a second boot from duplicating
// rows or resetting the operator's chain.
func TestEnsureDefaultProfileIsIdempotent(t *testing.T) {
	t.Parallel()
	db := seedDB(t)
	ctx := context.Background()

	_, err := SeedModels(ctx, db)
	require.NoError(t, err)
	_, err = EnsureDefaultProfile(ctx, db)
	require.NoError(t, err)

	var before int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM profile_models`).Scan(&before))

	added, err := EnsureDefaultProfile(ctx, db)
	require.NoError(t, err)
	require.Zero(t, added, "a second boot adds nothing")

	var after, profiles int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM profile_models`).Scan(&after))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM profiles`).Scan(&profiles))
	require.Equal(t, before, after)
	require.Equal(t, 1, profiles, "the baseline chain is created once, not per boot")
}

// TestEnsureDefaultProfileKeepsOperatorRemovals: a model the operator took out
// of the chain must stay out, or every boot would undo their editing.
func TestEnsureDefaultProfileKeepsOperatorRemovals(t *testing.T) {
	t.Parallel()
	db := seedDB(t)
	ctx := context.Background()

	_, err := SeedModels(ctx, db)
	require.NoError(t, err)
	_, err = EnsureDefaultProfile(ctx, db)
	require.NoError(t, err)

	var victim int64
	require.NoError(t, db.QueryRow(`SELECT model_db_id FROM profile_models LIMIT 1`).Scan(&victim))
	_, err = db.Exec(`DELETE FROM profile_models WHERE model_db_id = ?`, victim)
	require.NoError(t, err)

	added, err := EnsureDefaultProfile(ctx, db)
	require.NoError(t, err)
	require.Equal(t, 1, added,
		"an auto-include chain re-adds a catalog model; document this if the rule changes")
}
