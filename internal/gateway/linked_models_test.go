package gateway

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEnrolledLoginBecomesRoutable is the whole point of enrolling: a
// subscription's models have to reach the catalogue and the routing chain.
// Enrolling used to add a credential and nothing else, so the router had a key
// with nothing to send to it and the Models page showed no change.
func TestEnrolledLoginBecomesRoutable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	eng, err := OpenEngine(ctx, t.TempDir(), EngineOptions{SkipCatalogSeed: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = eng.Close() })

	keyID, err := eng.Vault().AddLinked(ctx, "anthropic", "Prowl login")
	require.NoError(t, err)

	seeded, err := eng.SeedLoginModels(ctx, keyID, "anthropic", []LinkedModel{
		{ID: "claude-opus-5", Name: "Claude Opus 5", ContextWindow: 1_000_000,
			InputPerM: 5, OutputPerM: 25, CanReason: true, Attachments: true, Tools: true},
		{ID: "claude-haiku-4-5", Name: "Claude Haiku 4.5", ContextWindow: 200_000,
			InputPerM: 0.8, OutputPerM: 4, Tools: true, Small: true},
	})
	require.NoError(t, err)
	require.Equal(t, 2, seeded)

	// In the catalogue, owned by the login and priced as published.
	rows, err := eng.DB().Query(`
		SELECT model_id, intelligence_rank, speed_rank, context_window,
		       supports_tools, paid_output_per_m, source, key_id
		  FROM models WHERE platform = 'anthropic' ORDER BY intelligence_rank DESC`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()

	type row struct {
		id                  string
		intelligence, speed int
		ctx                 int64
		tools               int
		outPerM             float64
		source              string
		keyID               int64
	}
	var got []row
	for rows.Next() {
		var r row
		require.NoError(t, rows.Scan(&r.id, &r.intelligence, &r.speed, &r.ctx,
			&r.tools, &r.outPerM, &r.source, &r.keyID))
		got = append(got, r)
	}
	require.Len(t, got, 2)

	// The dearer model ranks as the more capable one, which is what lets a
	// hard task route to it and a chore route away from it.
	require.Equal(t, "claude-opus-5", got[0].id)
	require.Greater(t, got[0].intelligence, got[1].intelligence)
	require.Less(t, got[0].speed, got[1].speed, "the cheap model must rank faster")
	require.Equal(t, int64(1_000_000), got[0].ctx)
	require.Equal(t, 1, got[0].tools)
	require.InDelta(t, 25.0, got[0].outPerM, 0.001, "the published price must be kept")
	require.Equal(t, "login", got[0].source)
	require.Equal(t, keyID, got[0].keyID)

	// And in the chain, or the router still cannot reach them.
	var inChain int
	require.NoError(t, eng.DB().QueryRow(`
		SELECT COUNT(*) FROM fallback_config f
		  JOIN models m ON m.id = f.model_db_id
		 WHERE m.platform = 'anthropic' AND f.enabled = 1`).Scan(&inChain))
	require.Equal(t, 2, inChain, "an enrolled login's models must be routable")

	require.Equal(t, 2, eng.LoginModelCount(ctx, keyID))
}

// TestWithdrawingALoginRemovesItsModels keeps a withdrawn subscription from
// leaving models in the chain that nothing can serve.
func TestWithdrawingALoginRemovesItsModels(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	eng, err := OpenEngine(ctx, t.TempDir(), EngineOptions{SkipCatalogSeed: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = eng.Close() })

	keyID, err := eng.Vault().AddLinked(ctx, "openai", "Prowl login")
	require.NoError(t, err)
	_, err = eng.SeedLoginModels(ctx, keyID, "openai", []LinkedModel{
		{ID: "gpt-5.2-codex", OutputPerM: 12, Tools: true},
	})
	require.NoError(t, err)

	require.NoError(t, eng.Vault().Delete(ctx, keyID))

	var models, chain int
	require.NoError(t, eng.DB().QueryRow(
		"SELECT COUNT(*) FROM models WHERE platform = 'openai'").Scan(&models))
	require.NoError(t, eng.DB().QueryRow(
		"SELECT COUNT(*) FROM fallback_config").Scan(&chain))
	require.Zero(t, models, "withdrawing must not leave unservable models")
	require.Zero(t, chain, "withdrawing must not leave them in the chain")
}

// TestSubscriptionWithoutPricesStillOrders covers a login that publishes no
// per-token price at all: the vendor's own large/small defaults are the only
// signal, and they must still produce an order.
func TestSubscriptionWithoutPricesStillOrders(t *testing.T) {
	t.Parallel()

	ranked := rankLinkedModels([]LinkedModel{
		{ID: "mid"},
		{ID: "small-one", Small: true},
		{ID: "flagship", Flagship: true},
	})
	require.Equal(t, "flagship", ranked[0].model.ID)
	require.Equal(t, "small-one", ranked[len(ranked)-1].model.ID)
}

// TestRankingPrefersTheVendorsOrderOverPrice is the trap price-based ranking
// walks into. Anthropic's newer flagships are CHEAPER than its older ones, so
// ordering by output price put claude-opus-4-1 ($75/M) above claude-opus-5
// ($25/M) — the hardest work would have gone to the previous generation while
// the current flagship looked like a mid-tier model.
func TestRankingPrefersTheVendorsOrderOverPrice(t *testing.T) {
	t.Parallel()

	// Catalogue order as the vendor publishes it: current generation first.
	ranked := rankLinkedModels([]LinkedModel{
		{ID: "claude-opus-5", OutputPerM: 25},
		{ID: "claude-opus-4-8", OutputPerM: 25},
		{ID: "claude-opus-4-1", OutputPerM: 75},
		{ID: "claude-haiku-4-5", OutputPerM: 4, Small: true},
	})

	order := make([]string, 0, len(ranked))
	for _, r := range ranked {
		order = append(order, r.model.ID)
	}
	require.Equal(t, "claude-opus-5", order[0],
		"the vendor's current flagship must lead, not the dearest legacy model")
	require.Less(t, indexOfModel(order, "claude-opus-5"), indexOfModel(order, "claude-opus-4-1"),
		"a newer, cheaper flagship must outrank an older, dearer one")
	require.Equal(t, "claude-haiku-4-5", order[len(order)-1],
		"the vendor's declared small model belongs at the cheap end")
}

// TestDeclaredFlagshipWinsOverPosition covers the explicit signal beating the
// inferred one: a vendor naming its large model means it.
func TestDeclaredFlagshipWinsOverPosition(t *testing.T) {
	t.Parallel()

	ranked := rankLinkedModels([]LinkedModel{
		{ID: "listed-first", OutputPerM: 30},
		{ID: "declared-large", OutputPerM: 10, Flagship: true},
	})
	require.Equal(t, "declared-large", ranked[0].model.ID)
}

func indexOfModel(order []string, id string) int {
	for i, v := range order {
		if v == id {
			return i
		}
	}
	return -1
}

// TestEngineOpenPrunesOrphanChainRows covers a chain row whose model is gone.
// It is unroutable, and it breaks every copy of the chain: creating a routing
// profile fails on the foreign key with "could not seed the profile". Rows
// like that appear whenever models are deleted by a connection that did not
// have foreign keys enabled, so open must not trust the cascade to have run.
func TestEngineOpenPrunesOrphanChainRows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()

	first, err := OpenEngine(ctx, dir, EngineOptions{SkipCatalogSeed: true})
	require.NoError(t, err)
	keyID, err := first.Vault().AddLinked(ctx, "anthropic", "")
	require.NoError(t, err)
	_, err = first.SeedLoginModels(ctx, keyID, "anthropic", []LinkedModel{
		{ID: "claude-opus-5", OutputPerM: 25},
	})
	require.NoError(t, err)

	// Delete the model with foreign keys off, exactly as an external sqlite
	// client does by default, so the cascade does not fire.
	_, err = first.DB().Exec("PRAGMA foreign_keys=OFF")
	require.NoError(t, err)
	_, err = first.DB().Exec("DELETE FROM models")
	require.NoError(t, err)

	var orphans int
	require.NoError(t, first.DB().QueryRow(`
		SELECT COUNT(*) FROM fallback_config f
		 LEFT JOIN models m ON m.id = f.model_db_id
		 WHERE m.id IS NULL`).Scan(&orphans))
	require.Positive(t, orphans, "the setup must actually leave an orphan")
	require.NoError(t, first.Close())

	second, err := OpenEngine(ctx, dir, EngineOptions{SkipCatalogSeed: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = second.Close() })

	require.NoError(t, second.DB().QueryRow(`
		SELECT COUNT(*) FROM fallback_config f
		 LEFT JOIN models m ON m.id = f.model_db_id
		 WHERE m.id IS NULL`).Scan(&orphans))
	require.Zero(t, orphans, "open must prune chain rows with no model")
}
