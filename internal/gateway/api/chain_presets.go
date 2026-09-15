package api

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"

	"github.com/neur0map/prowl/internal/gateway"
)

// Presets: a set you can start from instead of curating a hundred rows.
//
// With free, paid and subscription models in one catalogue the routing table
// runs to a hundred rows, and building a useful set by hand is the worst part
// of the product. A preset is a named query over facts the catalogue already
// holds — no invented tiers, no hand-maintained membership. Task sets ("deep
// work", "writing") answer "for this work, these models"; catalogue cuts
// ("everything free") slice mechanically. Either is one click.
//
// Each definition is SQL because that is what keeps it honest: the count the
// dashboard shows and the rows the list receives come from the same predicate.

type chainPreset struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`

	// Group separates task-shaped sets from catalogue-mechanical cuts.
	Group string `json:"group"`

	// reqs are the conditions membership demands. One descriptor renders both
	// the SQL predicate and the English requirements, so the count, the rows
	// inserted, and the list shown cannot describe three different things.
	reqs  []req
	order string
}

// req pairs the SQL that selects for one condition with the label that names
// it, from a single value so the two cannot drift apart.
type req struct {
	sql  string
	text string
}

func reqTools() req     { return req{"m.supports_tools = 1", "Tool calling"} }
func reqReasoning() req { return req{"m.supports_reasoning = 1", "Reasoning"} }
func reqVision() req    { return req{"m.supports_vision = 1", "Accepts images"} }

func reqContext(floor int) req {
	return req{
		fmt.Sprintf("m.context_window >= %d", floor),
		formatContextK(floor) + "+ context",
	}
}

func reqPriceCeiling(perM float64) req {
	return req{
		fmt.Sprintf("(m.paid_output_per_m IS NULL OR m.paid_output_per_m <= %g)", perM),
		fmt.Sprintf("Free or ≤ $%g/M output", perM),
	}
}

func reqPlatforms(names ...string) req {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = "'" + n + "'"
	}
	return req{
		"m.platform IN (" + strings.Join(quoted, ", ") + ")",
		"Runs locally (" + strings.Join(names, ", ") + ")",
	}
}

// reqCapabilityQuantile cuts by a quantile of intelligence_rank computed from
// the enabled catalogue at query time. A constant is wrong: intelligence_rank
// is a per-provider position (2..212 here), so a fixed threshold would admit a
// different share on every install and rot as the catalogue grows.
func reqCapabilityQuantile(q float64) req {
	sub := fmt.Sprintf(`(SELECT x.intelligence_rank FROM models x
		 WHERE x.enabled = 1 ORDER BY x.intelligence_rank
		 LIMIT 1 OFFSET (SELECT CAST(%g * (COUNT(*) - 1) AS INTEGER)
			 FROM models y WHERE y.enabled = 1))`, q)
	return req{
		"m.intelligence_rank >= " + sub,
		fmt.Sprintf("Top %.0f%% by capability", (1-q)*100),
	}
}

// reqRaw carries a catalogue cut that is not a capability requirement.
func reqRaw(sql, text string) req { return req{sql, text} }

// formatContextK renders exact multiples of 1024 as "128K", the rest as thousands.
func formatContextK(tokens int) string {
	if tokens%1024 == 0 {
		return fmt.Sprintf("%dK", tokens/1024)
	}
	return fmt.Sprintf("%dK", tokens/1000)
}

// intelligenceOrder tries the most capable model first; the scorer re-ranks
// from there on live evidence.
const intelligenceOrder = "m.intelligence_rank DESC, m.context_window DESC, m.model_id ASC"

// cheapThresholdPerM is a stated price ceiling, not a quantile, so the set does
// not shift membership whenever one expensive model is added.
const cheapThresholdPerM = 2.0

var chainPresets = []chainPreset{
	// Task sets: "for this kind of work, these models."
	{
		ID: "deep-work", Name: "Deep work", Group: "task",
		Description: "Tool-using, reasoning models with room for a long problem.",
		reqs:        []req{reqTools(), reqReasoning(), reqContext(131072)},
		order:       intelligenceOrder,
	},
	{
		ID: "coding", Name: "Coding", Group: "task",
		Description: "Tool callers with a large context, most capable then fastest.",
		reqs:        []req{reqTools(), reqContext(131072)},
		order:       "m.intelligence_rank DESC, m.speed_rank DESC, m.model_id ASC",
	},
	{
		ID: "writing", Name: "Writing", Group: "task",
		Description: "The most capable models with enough context for a draft.",
		reqs:        []req{reqCapabilityQuantile(0.75), reqContext(32768)},
		order:       intelligenceOrder,
	},
	{
		ID: "quick-chores", Name: "Quick chores", Group: "task",
		Description: fmt.Sprintf(
			"Free or near-free models, quickest first, for cheap throwaway work under $%.0f per million output tokens.",
			cheapThresholdPerM),
		reqs:  []req{reqPriceCeiling(cheapThresholdPerM)},
		order: "m.speed_rank DESC, m.intelligence_rank DESC",
	},
	{
		ID: "long-documents", Name: "Long documents", Group: "task",
		Description: "Models that accept 200K tokens or more, widest first.",
		reqs:        []req{reqContext(200000)},
		order:       "m.context_window DESC, m.intelligence_rank DESC",
	},
	{
		ID: "vision", Name: "Images in", Group: "task",
		Description: "Models that accept images, most capable first.",
		reqs:        []req{reqVision()},
		order:       intelligenceOrder,
	},
	{
		ID: "local", Name: "Local only", Group: "task",
		Description: "Models served from a local runtime, most capable first.",
		reqs:        []req{reqPlatforms("ollama", "lmstudio", "llamacpp")},
		order:       intelligenceOrder,
	},

	// Catalogue cuts: mechanical slices of what is enrolled.
	{
		ID: "free", Name: "Everything free", Group: "catalogue",
		Description: "Models with no published per-token price.",
		reqs:        []req{reqRaw("m.paid_output_per_m IS NULL", "No published price")},
		order:       intelligenceOrder,
	},
	{
		ID: "paid", Name: "Paid models", Group: "catalogue",
		Description: "Models that bill per token, cheapest first.",
		reqs:        []req{reqRaw("m.paid_output_per_m IS NOT NULL", "Bills per token")},
		order:       "m.paid_output_per_m ASC, m.intelligence_rank DESC",
	},
	{
		ID: "subscriptions", Name: "My subscriptions", Group: "catalogue",
		Description: "Models served by a login you enrolled, most capable first.",
		reqs:        []req{reqRaw("m.source = 'login'", "From an enrolled login")},
		order:       intelligenceOrder,
	},
	{
		ID: "flagships", Name: "Flagships", Group: "catalogue",
		Description: "The most capable model from each provider.",
		reqs: []req{reqRaw(`m.intelligence_rank = (
			SELECT MAX(x.intelligence_rank) FROM models x
			 WHERE x.platform = m.platform AND x.enabled = 1)`,
			"Top model per provider")},
		order: intelligenceOrder,
	},
}

func (p chainPreset) where() string {
	parts := make([]string, len(p.reqs))
	for i, r := range p.reqs {
		parts[i] = r.sql
	}
	return strings.Join(parts, " AND ")
}

func (p chainPreset) requirements() []string {
	out := make([]string, 0, len(p.reqs))
	for _, r := range p.reqs {
		out = append(out, r.text)
	}
	return out
}

func presetByID(id string) (chainPreset, bool) {
	for _, preset := range chainPresets {
		if strings.EqualFold(preset.ID, id) {
			return preset, true
		}
	}
	return chainPreset{}, false
}

func (s *Server) registerChainPresetRoutes() {
	s.mux.HandleFunc("GET /api/profiles/presets", s.RequireSession(s.handleChainPresets))
	s.mux.HandleFunc("POST /api/profiles/presets/{id}", s.RequireSession(s.handleChainFromPreset))
	s.mux.HandleFunc("POST /api/profiles/from-selection", s.RequireSession(s.handleChainFromSelection))
}

type chainPresetRow struct {
	chainPreset
	Requirements []string `json:"requirements"`

	// Models is how many catalogue rows the preset matches right now, so an
	// empty one is visibly empty before it is created.
	Models int `json:"models"`
}

func (s *Server) handleChainPresets(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	out := make([]chainPresetRow, 0, len(chainPresets))
	for _, preset := range chainPresets {
		row := chainPresetRow{chainPreset: preset, Requirements: preset.requirements()}
		if err := s.engine.DB().QueryRowContext(r.Context(),
			"SELECT COUNT(*) FROM models m WHERE m.enabled = 1 AND ("+preset.where()+")",
		).Scan(&row.Models); err != nil {
			row.Models = 0
		}
		out = append(out, row)
	}
	WriteJSON(w, http.StatusOK, map[string]any{"presets": out})
}

func (s *Server) handleChainFromPreset(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	preset, ok := presetByID(r.PathValue("id"))
	if !ok {
		WriteError(w, http.StatusNotFound, TypeNotFound, "no such preset")
		return
	}

	var body struct {
		Name     string `json:"name"`
		Activate bool   `json:"activate"`
	}
	if !DecodeJSON(w, r, &body) {
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = preset.Name
	}

	tx, err := s.engine.DB().BeginTx(r.Context(), nil)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, err.Error())
		return
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(r.Context(),
		"INSERT INTO profiles(name, active, created_at) VALUES(?, 0, strftime('%s','now'))", name)
	if err != nil {
		// A duplicate name is the operator's mistake, not a server fault.
		WriteError(w, http.StatusConflict, TypeInvalidRequest,
			"a list named "+name+" already exists")
		return
	}
	profileID, _ := res.LastInsertId()

	// Positions are dense and follow the preset's own order, so the list
	// arrives ranked rather than in catalogue order.
	if _, err := tx.ExecContext(r.Context(), `
		INSERT INTO profile_models(profile_id, model_db_id, position)
		SELECT ?, m.id, ROW_NUMBER() OVER (ORDER BY `+preset.order+`)
		  FROM models m
		 WHERE m.enabled = 1 AND (`+preset.where()+`)`, profileID); err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer,
			"could not fill the list from that preset")
		return
	}

	var members int
	if err := tx.QueryRowContext(r.Context(),
		"SELECT COUNT(*) FROM profile_models WHERE profile_id = ?", profileID).Scan(&members); err != nil &&
		err != sql.ErrNoRows {
		WriteError(w, http.StatusInternalServerError, TypeServer, err.Error())
		return
	}

	if body.Activate {
		if err := routingActivateProfile(r.Context(), tx, profileID); err != nil {
			WriteError(w, http.StatusInternalServerError, TypeServer, "could not activate the list")
			return
		}
	}

	if err := tx.Commit(); err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, err.Error())
		return
	}

	WriteJSON(w, http.StatusCreated, map[string]any{
		"id": profileID, "name": name, "models": members,
		// The callable id, so the list is usable from any client immediately.
		"callAs": "auto:" + strings.ToLower(name),
		"active": body.Activate,
	})
}

// handleChainFromSelection saves a hand-picked selection as a named set, so the
// list a task browses becomes the list it routes to. Rows land in the order
// given as dense positions; ids that are not enabled models are skipped rather
// than failing the save, and the response reports how many rows actually landed
// so the client cannot claim more than it got.
func (s *Server) handleChainFromSelection(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	var body struct {
		Name       string  `json:"name"`
		ModelDBIDs []int64 `json:"modelDbIds"`
		Activate   bool    `json:"activate"`
	}
	if !DecodeJSON(w, r, &body) {
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		WriteError(w, http.StatusBadRequest, TypeInvalidRequest, "a set needs a name")
		return
	}
	if len(body.ModelDBIDs) == 0 {
		WriteError(w, http.StatusBadRequest, TypeInvalidRequest,
			"select at least one model to save a set")
		return
	}

	tx, err := s.engine.DB().BeginTx(r.Context(), nil)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, err.Error())
		return
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(r.Context(),
		"INSERT INTO profiles(name, active, created_at) VALUES(?, 0, strftime('%s','now'))", name)
	if err != nil {
		WriteError(w, http.StatusConflict, TypeInvalidRequest,
			"a list named "+name+" already exists")
		return
	}
	profileID, _ := res.LastInsertId()

	// One guarded insert per id keeps the positions dense: a row lands only if
	// the id is an enabled model, and the position advances only when it lands,
	// so a skipped or duplicate id leaves no gap.
	stmt, err := tx.PrepareContext(r.Context(), `
		INSERT OR IGNORE INTO profile_models(profile_id, model_db_id, position)
		SELECT ?, ?, ? WHERE EXISTS(SELECT 1 FROM models WHERE id = ? AND enabled = 1)`)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, err.Error())
		return
	}
	defer func() { _ = stmt.Close() }()

	inserted := 0
	for _, id := range body.ModelDBIDs {
		out, err := stmt.ExecContext(r.Context(), profileID, id, inserted+1, id)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, TypeServer, "could not save the selection")
			return
		}
		if n, _ := out.RowsAffected(); n > 0 {
			inserted++
		}
	}

	if body.Activate {
		if err := routingActivateProfile(r.Context(), tx, profileID); err != nil {
			WriteError(w, http.StatusInternalServerError, TypeServer, "could not activate the set")
			return
		}
	}

	if err := tx.Commit(); err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, err.Error())
		return
	}

	WriteJSON(w, http.StatusCreated, map[string]any{
		"id": profileID, "name": name, "models": inserted,
		"callAs": "auto:" + strings.ToLower(name),
		"active": body.Activate,
	})
}
