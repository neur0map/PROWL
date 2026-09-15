package gateway

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sort"
)

// Enrolling a login only added a credential, leaving the router a key with
// nothing to route to it. Seeded rows are marked `source = 'login'` and carry
// the key id, so withdrawing the login removes them through the existing
// cascade.

const loginModelSource = "login"

// SeedLoginModels writes an enrolled login's models into the catalogue and
// appends them to the active routing chain. Returns how many rows landed.
func (e *Engine) SeedLoginModels(ctx context.Context, keyID int64, platform string, models []LinkedModel) (int, error) {
	if len(models) == 0 {
		return 0, nil
	}

	ranked := rankLinkedModels(models)

	tx, err := e.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	// Appended, not prepended: a new enrolment must not silently outrank
	// models the operator already ordered.
	var nextPosition int64
	if err := tx.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(position), 0) + 1 FROM fallback_config").Scan(&nextPosition); err != nil {
		return 0, err
	}

	seeded := 0
	for _, m := range ranked {
		var modelDBID int64
		err := tx.QueryRowContext(ctx, `
			INSERT INTO models(platform, model_id, display_name, intelligence_rank,
				speed_rank, size_label, context_window, enabled, supports_vision,
				supports_tools, supports_reasoning, key_id, paid_input_per_m,
				paid_output_per_m, source)
			VALUES(?, ?, ?, ?, ?, '', ?, 1, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(platform, model_id, endpoint_scope) DO UPDATE SET
				display_name = excluded.display_name,
				intelligence_rank = excluded.intelligence_rank,
				speed_rank = excluded.speed_rank,
				context_window = excluded.context_window,
				supports_vision = excluded.supports_vision,
				supports_tools = excluded.supports_tools,
				supports_reasoning = excluded.supports_reasoning,
				key_id = excluded.key_id,
				paid_input_per_m = excluded.paid_input_per_m,
				paid_output_per_m = excluded.paid_output_per_m,
				source = excluded.source
			RETURNING id`,
			platform, m.model.ID, displayName(m.model), m.intelligence, m.speed,
			nullableInt(m.model.ContextWindow), boolInt(m.model.Attachments),
			boolInt(m.model.Tools), boolInt(m.model.CanReason), keyID,
			nullablePrice(m.model.InputPerM), nullablePrice(m.model.OutputPerM),
			loginModelSource,
		).Scan(&modelDBID)
		if err != nil {
			return seeded, fmt.Errorf("seed %s/%s: %w", platform, m.model.ID, err)
		}

		if _, err := tx.ExecContext(ctx, `
			INSERT INTO fallback_config(model_db_id, position, enabled)
			VALUES(?, ?, 1)
			ON CONFLICT(model_db_id) DO NOTHING`, modelDBID, nextPosition); err != nil {
			return seeded, fmt.Errorf("chain %s/%s: %w", platform, m.model.ID, err)
		}
		nextPosition++
		seeded++
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	slog.Info("Seeded models for an enrolled login",
		"platform", platform, "key_id", keyID, "models", seeded)
	return seeded, nil
}

// rankedModel pairs a model with the ranks derived for it.
type rankedModel struct {
	model        LinkedModel
	intelligence int
	speed        int
}

// rankLinkedModels orders a provider's models using the vendor's own signals,
// in the order those signals are trustworthy.
//
// Catalogue POSITION comes first. The provider publishes its models in a
// curated order — newest and most capable first — and that is the only signal
// that tracks a vendor's current range. Price looked like the obvious proxy
// and is not: Anthropic's newer flagships are cheaper than its older ones, so
// a price ordering put claude-opus-4-1 above claude-opus-5 and would have
// sent the hardest work to the previous generation.
//
// The vendor's declared large/small defaults override position, since they are
// an explicit statement rather than an inference, and price is kept only as a
// tiebreak inside one position group.
//
// All of it is a starting order. The Models page ranks by live reliability and
// speed once traffic exists, and the operator can reorder or disable any row.
func rankLinkedModels(models []LinkedModel) []rankedModel {
	type entry struct {
		model    LinkedModel
		position int
	}
	entries := make([]entry, 0, len(models))
	for i, m := range models {
		entries = append(entries, entry{model: m, position: i})
	}

	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if a.model.Flagship != b.model.Flagship {
			return a.model.Flagship
		}
		if a.model.Small != b.model.Small {
			return b.model.Small
		}
		if a.position != b.position {
			return a.position < b.position
		}
		return a.model.OutputPerM > b.model.OutputPerM
	})

	// Ranks spread across the band the scorer expects, most capable first.
	// Speed is the mirror: the small end of a vendor's range is the quick end.
	out := make([]rankedModel, 0, len(entries))
	span := max(len(entries)-1, 1)
	for i, e := range entries {
		intelligence := 100 - (i * 60 / span) // 100 down to 40
		speed := 40 + (i * 55 / span)         // 40 up to 95
		out = append(out, rankedModel{model: e.model, intelligence: intelligence, speed: speed})
	}
	return out
}

func displayName(m LinkedModel) string {
	if m.Name != "" {
		return m.Name
	}
	return m.ID
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// nullableInt keeps an unknown context window NULL rather than storing zero,
// which the request gate would read as "no room for anything".
func nullableInt(v int64) any {
	if v <= 0 {
		return nil
	}
	return v
}

// nullablePrice distinguishes "free or included in a subscription" from
// "priced at zero", which would make a paid model look free.
func nullablePrice(v float64) any {
	if v <= 0 {
		return nil
	}
	return v
}

// LoginModelCount reports how many catalogue rows an enrolled login owns, for
// the dashboard to show what enrolling actually did.
func (e *Engine) LoginModelCount(ctx context.Context, keyID int64) int {
	var n int
	err := e.store.DB().QueryRowContext(ctx,
		"SELECT COUNT(*) FROM models WHERE key_id = ? AND source = ?",
		keyID, loginModelSource).Scan(&n)
	if err != nil && err != sql.ErrNoRows {
		return 0
	}
	return n
}
