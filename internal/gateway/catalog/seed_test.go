package catalog

import (
	"context"
	"database/sql"
	"testing"

	"github.com/neur0map/prowl/internal/gateway/store"
	"github.com/stretchr/testify/require"
)

// seedDB opens a fresh migrated in-memory gateway database for a seed test.
func seedDB(t *testing.T) *sql.DB {
	t.Helper()
	st, err := store.OpenMemory(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return st.DB()
}

func count(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRow(query, args...).Scan(&n))
	return n
}

// TestSeedModelsPopulatesFreshCatalog is the anti-empty-product guarantee: a
// fresh data directory must open to the full shipped catalog, split correctly
// across the three tables and the media modalities.
func TestSeedModelsPopulatesFreshCatalog(t *testing.T) {
	t.Parallel()
	db := seedDB(t)

	res, err := SeedModels(context.Background(), db)
	require.NoError(t, err)

	require.Equal(t, 365, count(t, db, "SELECT count(*) FROM models"), "chat catalog is empty")
	require.Equal(t, 22, count(t, db, "SELECT count(*) FROM embedding_models"))
	require.Equal(t, 13, count(t, db, "SELECT count(*) FROM media_models"))

	// Media modalities: the reference ships no video model, so the Video tab is
	// legitimately empty. Asserting zero here keeps a future accidental video
	// row honest and documents the empty tab as intentional.
	require.Equal(t, 4, count(t, db, "SELECT count(*) FROM media_models WHERE modality = 'image'"))
	require.Equal(t, 3, count(t, db, "SELECT count(*) FROM media_models WHERE modality = 'audio'"))
	require.Equal(t, 6, count(t, db, "SELECT count(*) FROM media_models WHERE modality = 'transcription'"))
	require.Equal(t, 0, count(t, db, "SELECT count(*) FROM media_models WHERE modality = 'video'"))

	// A fresh seed is all inserts.
	require.Equal(t, 365, res.ChatInserted)
	require.Equal(t, 22, res.EmbeddingInserted)
	require.Equal(t, 13, res.MediaInserted)
	require.Zero(t, res.Updated())

	// Re-seeding an unchanged catalog inserts nothing and refreshes everything.
	res2, err := SeedModels(context.Background(), db)
	require.NoError(t, err)
	require.Zero(t, res2.Inserted())
	require.Equal(t, 365, res2.ChatUpdated)
	require.Equal(t, 22, res2.EmbeddingUpdated)
	require.Equal(t, 13, res2.MediaUpdated)
	// Counts are stable across re-seed (no duplicate rows).
	require.Equal(t, 365, count(t, db, "SELECT count(*) FROM models"))
	require.Equal(t, 22, count(t, db, "SELECT count(*) FROM embedding_models"))
	require.Equal(t, 13, count(t, db, "SELECT count(*) FROM media_models"))
}

// TestSeededAssetValuesArePinned pins exact shipped values against independent
// ground truth (the reference install's own data), so a corrupted or truncated
// asset — one that parsed but lost rows or fields — fails loudly instead of
// silently shipping a smaller or wrong catalog.
func TestSeededAssetValuesArePinned(t *testing.T) {
	t.Parallel()
	db := seedDB(t)
	_, err := SeedModels(context.Background(), db)
	require.NoError(t, err)

	// A null limit stays null (no published per-minute cap), not zero.
	var rpm, rpd, tpm, tpd sql.NullInt64
	require.NoError(t, db.QueryRow(
		`SELECT rpm_limit, rpd_limit, tpm_limit, tpd_limit FROM models
		 WHERE platform = 'agnes' AND model_id = 'agnes-2.0-flash'`,
	).Scan(&rpm, &rpd, &tpm, &tpd))
	require.False(t, rpm.Valid, "rpm_limit must be null, not 0")
	require.False(t, rpd.Valid)
	require.False(t, tpm.Valid)
	require.False(t, tpd.Valid)

	// A non-null pricing pair and a large context window.
	var ctx sql.NullInt64
	var in, out sql.NullFloat64
	require.NoError(t, db.QueryRow(
		`SELECT context_window, paid_input_per_m, paid_output_per_m FROM models
		 WHERE platform = 'google' AND model_id = 'gemini-2.5-flash'`,
	).Scan(&ctx, &in, &out))
	require.Equal(t, int64(1048576), ctx.Int64)
	require.True(t, in.Valid)
	require.Equal(t, 0.3, in.Float64)
	require.Equal(t, 2.5, out.Float64)

	// A catalog model the reference ships disabled (retired upstream) seeds
	// disabled, so it is never offered for routing.
	require.Equal(t, 0, count(t, db,
		`SELECT enabled FROM models WHERE platform = 'nvidia' AND model_id = 'google/gemma-4-31b-it'`))

	// An embedding row's dimension and input ceiling survive intact.
	var dims, maxIn sql.NullInt64
	var family string
	require.NoError(t, db.QueryRow(
		`SELECT family, dimensions, max_input_tokens FROM embedding_models
		 WHERE platform = 'cloudflare' AND model_id = '@cf/baai/bge-base-en-v1.5'`,
	).Scan(&family, &dims, &maxIn))
	require.Equal(t, "bge-base-en-v1.5", family)
	require.Equal(t, int64(768), dims.Int64)
	require.Equal(t, int64(512), maxIn.Int64)

	// A transcription row carries its adapter metadata verbatim.
	var modality string
	var meta sql.NullString
	require.NoError(t, db.QueryRow(
		`SELECT modality, meta_json FROM media_models
		 WHERE platform = 'cloudflare' AND model_id = '@cf/openai/whisper-large-v3-turbo'`,
	).Scan(&modality, &meta))
	require.Equal(t, "transcription", modality)
	require.Equal(t, `{"subtitleFormats":["vtt"],"requestStyle":"json"}`, meta.String)
}

// TestSeedModelsPreservesOperatorEdits is the survival property end to end: a
// re-seed must refresh catalog-owned metadata to the shipped values while
// leaving every operator-controlled decision — a disable, a hand-added model,
// a custom endpoint — exactly as the operator left it.
func TestSeedModelsPreservesOperatorEdits(t *testing.T) {
	t.Parallel()
	db := seedDB(t)
	ctx := context.Background()

	_, err := SeedModels(ctx, db)
	require.NoError(t, err)

	// Operator disables a catalog model AND (simulating a value the seed owns
	// having drifted) corrupts a catalog-owned field on it.
	_, err = db.Exec(
		`UPDATE models SET enabled = 0, display_name = 'OPERATOR JUNK', intelligence_rank = 999
		 WHERE platform = 'google' AND model_id = 'gemini-2.5-flash'`)
	require.NoError(t, err)

	// Operator hand-adds a model at a platform:model_id the catalog also ships
	// (source != 'catalog'); the seed must neither clobber nor adopt it.
	_, err = db.Exec(
		`UPDATE models SET source = 'user', display_name = 'MY FORK', enabled = 0
		 WHERE platform = 'google' AND model_id = 'gemini-2.5-flash-lite'`)
	require.NoError(t, err)

	// Operator registers a custom relay endpoint (non-empty endpoint_scope) —
	// its own row identity, never a seed target.
	_, err = db.Exec(
		`INSERT INTO models (platform, model_id, display_name, monthly_token_budget,
		                     enabled, source, endpoint_scope)
		 VALUES ('google', 'gemini-2.5-flash', 'My Relay', '', 1, 'user', 'relay-1')`)
	require.NoError(t, err)

	// Operator deletes a catalog model entirely.
	_, err = db.Exec(
		`DELETE FROM models WHERE platform = 'agnes' AND model_id = 'agnes-2.0-flash'`)
	require.NoError(t, err)

	before := count(t, db, "SELECT count(*) FROM models")

	_, err = SeedModels(ctx, db)
	require.NoError(t, err)

	// Catalog-owned metadata is refreshed back to the shipped values...
	var name string
	var rank, enabled int
	require.NoError(t, db.QueryRow(
		`SELECT display_name, intelligence_rank, enabled FROM models
		 WHERE platform = 'google' AND model_id = 'gemini-2.5-flash' AND endpoint_scope = ''`,
	).Scan(&name, &rank, &enabled))
	require.Equal(t, "Gemini 2.5 Flash", name, "catalog-owned display name must be refreshed")
	require.NotEqual(t, 999, rank, "catalog-owned rank must be refreshed")
	// ...but the operator's disable survives.
	require.Equal(t, 0, enabled, "operator's disable must survive the re-seed")

	// The hand-added (source='user') row is untouched.
	var forkName string
	var forkSource string
	require.NoError(t, db.QueryRow(
		`SELECT display_name, source FROM models
		 WHERE platform = 'google' AND model_id = 'gemini-2.5-flash-lite' AND endpoint_scope = ''`,
	).Scan(&forkName, &forkSource))
	require.Equal(t, "MY FORK", forkName, "operator-owned row must not be clobbered")
	require.Equal(t, "user", forkSource)

	// The custom relay endpoint is untouched.
	var relayName string
	require.NoError(t, db.QueryRow(
		`SELECT display_name FROM models
		 WHERE platform = 'google' AND model_id = 'gemini-2.5-flash' AND endpoint_scope = 'relay-1'`,
	).Scan(&relayName))
	require.Equal(t, "My Relay", relayName, "custom endpoint must not be touched")

	// The deleted catalog model is re-inserted (documented tombstone tradeoff:
	// deletion is non-durable, disable is the durable removal).
	require.Equal(t, 1, count(t, db,
		`SELECT count(*) FROM models WHERE platform = 'agnes' AND model_id = 'agnes-2.0-flash'`),
		"a deleted catalog model reappears on re-seed")

	// Net effect on the row count: the deleted catalog model came back, so we
	// are back where we were before the delete (the operator's extra relay row
	// is still there and was there before this re-seed too).
	require.Equal(t, before+1, count(t, db, "SELECT count(*) FROM models"))
}

// TestSeedResultCarriesChecksum proves the seed records the shipped-asset
// checksum as a fact about the binary, not a "seeded" flag that could disagree
// with the tables.
func TestSeedResultCarriesChecksum(t *testing.T) {
	t.Parallel()
	db := seedDB(t)
	res, err := SeedModels(context.Background(), db)
	require.NoError(t, err)
	require.Equal(t, SeedSHA(), res.AssetSHA)
	require.NotEmpty(t, res.AssetSHA)

	var stored string
	require.NoError(t, db.QueryRow(
		`SELECT value FROM settings WHERE key = 'catalog_seed_sha'`).Scan(&stored))
	require.Equal(t, SeedSHA(), stored)
}
