package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/neur0map/prowl/internal/gateway"
)

// registerSettingsExtraRoutes mounts the rest of the Settings dialog and the
// Agents page the base settings file does not: prompt compression, model
// unification, the saved fusion default, the MCP toggle, the Claude/Gemini
// family maps, agent-compatibility, the outbound proxy, and the shareable URL
// tokens. Several of these control behaviours Prowl does not implement yet; each
// such setting is stored and returned faithfully so the dashboard round-trips
// it, and the divergence is noted at the handler. Every route is session gated,
// matching the reference's requireAuth mount (app.ts:244-267).
func (s *Server) registerSettingsExtraRoutes() {
	s.mux.HandleFunc("GET /api/settings/compression", s.RequireSession(s.handleGetCompression))
	s.mux.HandleFunc("PUT /api/settings/compression", s.RequireSession(s.handlePutCompression))
	s.mux.HandleFunc("GET /api/settings/unify", s.RequireSession(s.handleGetUnify))
	s.mux.HandleFunc("PUT /api/settings/unify", s.RequireSession(s.handlePutUnify))
	s.mux.HandleFunc("GET /api/settings/fusion", s.RequireSession(s.handleGetFusion))
	s.mux.HandleFunc("PUT /api/settings/fusion", s.RequireSession(s.handlePutFusion))
	s.mux.HandleFunc("GET /api/settings/enable-mcp", s.RequireSession(s.handleGetEnableMCP))
	s.mux.HandleFunc("PUT /api/settings/enable-mcp", s.RequireSession(s.handlePutEnableMCP))
	s.mux.HandleFunc("GET /api/settings/anthropic-map", s.RequireSession(s.handleGetAnthropicMap))
	s.mux.HandleFunc("PUT /api/settings/anthropic-map", s.RequireSession(s.handlePutAnthropicMap))
	s.mux.HandleFunc("GET /api/settings/gemini-map", s.RequireSession(s.handleGetGeminiMap))
	s.mux.HandleFunc("PUT /api/settings/gemini-map", s.RequireSession(s.handlePutGeminiMap))
	s.mux.HandleFunc("GET /api/settings/agent-compatibility", s.RequireSession(s.handleGetAgentCompatibility))
	s.mux.HandleFunc("PUT /api/settings/agent-compatibility", s.RequireSession(s.handlePutAgentCompatibility))
	s.mux.HandleFunc("GET /api/settings/proxy", s.RequireSession(s.handleGetProxy))
	s.mux.HandleFunc("PUT /api/settings/proxy", s.RequireSession(s.handlePutProxy))
	s.mux.HandleFunc("POST /api/settings/proxy/test", s.RequireSession(s.handleProxyTest))
	s.mux.HandleFunc("GET /api/settings/url-tokens", s.RequireSession(s.handleListURLTokens))
	s.mux.HandleFunc("POST /api/settings/url-tokens", s.RequireSession(s.handleMintURLToken))
	s.mux.HandleFunc("DELETE /api/settings/url-tokens/{id}", s.RequireSession(s.handleRevokeURLToken))
}

// Setting keys this file owns. Each is validated where it is written; the
// scalar ones join the shared settingSpecs registry (see init) so an unknown
// key or an invalid value is refused at the same boundary as the base settings,
// while the JSON-valued ones carry their validation in their typed accessors.
const (
	settingCompression     = "compression"
	settingUnifyEnabled    = "unify_models_enabled"
	settingUnifyOverrides  = "model_unify_overrides"
	settingFusionConfig    = "fusion_config"
	settingEnableMCP       = "enable_mcp"
	settingAnthropicMap    = "anthropic_model_map"
	settingGeminiMap       = "gemini_model_map"
	settingOllamaEmulation = "ollama_emulation"
	settingExposeCCAliases = "expose_cc_discovery_aliases"
	settingProxyURL        = "proxy_url"
	settingProxyMode       = "proxy_mode"
	settingProxyEnabled    = "proxy_enabled"
	settingProxyBypass     = "proxy_bypass"
	settingFetchRelayToken = "fetch_relay_token"
)

// The scalar settings this file adds go into the one registry that already
// knows the base settings, so `settingsStore.set` validates and canonicalises
// them and rejects an unknown key exactly as it does for the update-check flag.
func init() {
	settingSpecs[settingEnableMCP] = settingSpec{def: "0", canonicalize: canonicalizeBool}
	settingSpecs[settingUnifyEnabled] = settingSpec{def: "1", canonicalize: canonicalizeBool}
	settingSpecs[settingExposeCCAliases] = settingSpec{def: "0", canonicalize: canonicalizeBool}
	settingSpecs[settingOllamaEmulation] = settingSpec{def: "off", canonicalize: canonicalizeEnum("off", "open-loopback", "key-required")}
}

// canonicalizeEnum returns a validator that accepts only one of the given
// values, storing it verbatim, so a typo cannot poison a later read.
func canonicalizeEnum(allowed ...string) func(string) (string, error) {
	return func(raw string) (string, error) {
		v := strings.TrimSpace(raw)
		for _, a := range allowed {
			if v == a {
				return v, nil
			}
		}
		return "", fmt.Errorf("must be one of %s, got %q", strings.Join(allowed, ", "), raw)
	}
}

// rawSetting reads a settings value by key without consulting the spec
// registry, for the JSON-valued settings whose validation lives in their typed
// accessors rather than a single canonicalize function.
func (st *settingsStore) rawSetting(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := st.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

// putRawSetting persists a value the caller has already validated.
func (st *settingsStore) putRawSetting(ctx context.Context, key, value string) error {
	_, err := st.db.ExecContext(ctx,
		`INSERT INTO settings(key, value, updated_at) VALUES(?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, time.Now().Unix())
	return err
}

func (st *settingsStore) boolSetting(ctx context.Context, key string) bool {
	v, err := st.get(ctx, key)
	return err == nil && v == "1"
}

// ── Compression ──────────────────────────────────────────────────────────────
//
// Prompt compression is not applied by Prowl's inference plane yet; this
// endpoint stores and returns the operator's configuration faithfully so the
// Settings dialog round-trips, but no request is compressed by it.

type compressionConfig struct {
	Mode                 string                    `json:"mode"`
	Engines              map[string]map[string]any `json:"engines"`
	AutoTriggerEstTokens *int                      `json:"autoTriggerEstTokens,omitempty"`
	TargetTokens         *int                      `json:"targetTokens,omitempty"`
	TrustProjectFilters  bool                      `json:"trustProjectFilters"`
	PrefixFreeze         bool                      `json:"prefixFreeze"`
}

var compressionModes = map[string]bool{"off": true, "lossless": true, "standard": true, "aggressive": true}

// defaultCompressionConfig mirrors the reference DEFAULT_COMPRESSION_CONFIG and
// the eight engine defaults (compression/config.ts:14-36).
func defaultCompressionConfig() compressionConfig {
	return compressionConfig{
		Mode: "off",
		Engines: map[string]map[string]any{
			"dedup":          {"enabled": true, "minBlockChars": 80, "minBlockLines": 3},
			"lite":           {"enabled": true},
			"read-lifecycle": {"enabled": true},
			"toolfilter":     {"enabled": true, "intensity": "standard", "maxLinesPerResult": 120, "maxCharsPerResult": 12000, "disabledFilters": []any{}},
			"jsoncompact":    {"enabled": true, "minRows": 8},
			"relevance":      {"enabled": true, "maxChars": 18000},
			"aging":          {"enabled": true, "liveTurns": 3, "condenseAfterTurns": 8},
			"hard-budget":    {"enabled": true},
		},
		TrustProjectFilters: false,
		PrefixFreeze:        true,
	}
}

// mergeCompressionDefaults fills in any engine the stored config is missing, so
// a config saved before a new engine existed still returns every engine.
func mergeCompressionDefaults(cfg compressionConfig) compressionConfig {
	def := defaultCompressionConfig()
	if cfg.Engines == nil {
		cfg.Engines = map[string]map[string]any{}
	}
	for id, fallback := range def.Engines {
		if _, ok := cfg.Engines[id]; !ok {
			cfg.Engines[id] = fallback
		}
	}
	if cfg.Mode == "" {
		cfg.Mode = "off"
	}
	return cfg
}

func (st *settingsStore) getCompression(ctx context.Context) (compressionConfig, error) {
	raw, ok, err := st.rawSetting(ctx, settingCompression)
	if err != nil {
		return compressionConfig{}, err
	}
	if !ok {
		return defaultCompressionConfig(), nil
	}
	var cfg compressionConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return defaultCompressionConfig(), nil
	}
	return mergeCompressionDefaults(cfg), nil
}

type compressionUpdate struct {
	Mode                 *string                   `json:"mode"`
	Engines              map[string]map[string]any `json:"engines"`
	AutoTriggerEstTokens json.RawMessage           `json:"autoTriggerEstTokens"`
	TargetTokens         json.RawMessage           `json:"targetTokens"`
	TrustProjectFilters  *bool                     `json:"trustProjectFilters"`
	PrefixFreeze         *bool                     `json:"prefixFreeze"`
}

// nullableIntPatch interprets a possibly-absent, possibly-null JSON field:
// absent keeps the current value, explicit null clears it, a positive integer
// sets it, and anything else is an error.
func nullableIntPatch(raw json.RawMessage, current *int) (*int, error) {
	if len(raw) == 0 {
		return current, nil
	}
	if string(raw) == "null" {
		return nil, nil
	}
	var n int
	if err := json.Unmarshal(raw, &n); err != nil || n <= 0 {
		return current, errors.New("must be a positive integer or null")
	}
	return &n, nil
}

func (st *settingsStore) setCompression(ctx context.Context, update compressionUpdate) (compressionConfig, error) {
	if update.Mode != nil && !compressionModes[*update.Mode] {
		return compressionConfig{}, fmt.Errorf("mode must be one of off, lossless, standard, aggressive")
	}
	cfg, err := st.getCompression(ctx)
	if err != nil {
		return compressionConfig{}, err
	}
	if update.Mode != nil {
		cfg.Mode = *update.Mode
	}
	for id, patch := range update.Engines {
		engine := cfg.Engines[id]
		if engine == nil {
			engine = map[string]any{"enabled": true}
		}
		for k, v := range patch {
			engine[k] = v
		}
		if _, ok := engine["enabled"]; !ok {
			engine["enabled"] = true
		}
		cfg.Engines[id] = engine
	}
	if cfg.AutoTriggerEstTokens, err = nullableIntPatch(update.AutoTriggerEstTokens, cfg.AutoTriggerEstTokens); err != nil {
		return compressionConfig{}, fmt.Errorf("autoTriggerEstTokens %w", err)
	}
	if cfg.TargetTokens, err = nullableIntPatch(update.TargetTokens, cfg.TargetTokens); err != nil {
		return compressionConfig{}, fmt.Errorf("targetTokens %w", err)
	}
	if update.TrustProjectFilters != nil {
		cfg.TrustProjectFilters = *update.TrustProjectFilters
	}
	if update.PrefixFreeze != nil {
		cfg.PrefixFreeze = *update.PrefixFreeze
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		return compressionConfig{}, err
	}
	if err := st.putRawSetting(ctx, settingCompression, string(encoded)); err != nil {
		return compressionConfig{}, err
	}
	return cfg, nil
}

func (s *Server) handleGetCompression(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	cfg, err := s.settings.getCompression(r.Context())
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not read compression settings")
		return
	}
	WriteJSON(w, http.StatusOK, cfg)
}

func (s *Server) handlePutCompression(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	var update compressionUpdate
	if !DecodeJSON(w, r, &update) {
		return
	}
	cfg, err := s.settings.setCompression(r.Context(), update)
	if err != nil {
		WriteError(w, http.StatusBadRequest, TypeInvalidRequest, "invalid compression settings: "+err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, cfg)
}

// ── Unify ────────────────────────────────────────────────────────────────────
//
// Cross-provider model unification is stored and returned faithfully; Prowl's
// router does not yet group models by this setting, so the value round-trips
// but does not change routing.

type unifyMerge struct {
	Into string   `json:"into"`
	Keys []string `json:"keys"`
}

type unifySplit struct {
	Member   string  `json:"member"`
	GroupKey *string `json:"groupKey,omitempty"`
}

type unifyOverrides struct {
	Merges []unifyMerge `json:"merges"`
	Splits []unifySplit `json:"splits"`
}

func emptyUnifyOverrides() unifyOverrides {
	return unifyOverrides{Merges: []unifyMerge{}, Splits: []unifySplit{}}
}

func (st *settingsStore) getUnifyOverrides(ctx context.Context) (unifyOverrides, error) {
	raw, ok, err := st.rawSetting(ctx, settingUnifyOverrides)
	if err != nil {
		return unifyOverrides{}, err
	}
	if !ok {
		return emptyUnifyOverrides(), nil
	}
	var ov unifyOverrides
	if err := json.Unmarshal([]byte(raw), &ov); err != nil {
		return emptyUnifyOverrides(), nil
	}
	if ov.Merges == nil {
		ov.Merges = []unifyMerge{}
	}
	if ov.Splits == nil {
		ov.Splits = []unifySplit{}
	}
	return ov, nil
}

func validateUnifyOverrides(ov unifyOverrides) error {
	for _, m := range ov.Merges {
		if strings.TrimSpace(m.Into) == "" || len(m.Keys) == 0 {
			return errors.New("each merge needs a non-empty `into` and at least one key")
		}
		for _, k := range m.Keys {
			if strings.TrimSpace(k) == "" {
				return errors.New("merge keys must be non-empty")
			}
		}
	}
	for _, sp := range ov.Splits {
		if strings.TrimSpace(sp.Member) == "" {
			return errors.New("each split needs a non-empty member")
		}
	}
	return nil
}

func (s *Server) handleGetUnify(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	s.writeUnify(w, r)
}

func (s *Server) writeUnify(w http.ResponseWriter, r *http.Request) {
	ov, err := s.settings.getUnifyOverrides(r.Context())
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not read unify settings")
		return
	}
	// Unification is always on in the reference: the on/off toggle was removed
	// from the product, so isUnifyEnabled() unconditionally returns true and
	// the stored flag is ignored (model-groups.ts:85-92). PUT still records the
	// flag for backward compatibility (see handlePutUnify), but a read always
	// reports enabled, exactly as the reference does.
	WriteJSON(w, http.StatusOK, map[string]any{
		"enabled":   true,
		"overrides": ov,
	})
}

func (s *Server) handlePutUnify(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	var body struct {
		Enabled   *bool           `json:"enabled"`
		Overrides *unifyOverrides `json:"overrides"`
	}
	if !DecodeJSON(w, r, &body) {
		return
	}
	ctx := r.Context()
	if body.Enabled != nil {
		if err := s.settings.set(ctx, settingUnifyEnabled, boolValue(*body.Enabled)); err != nil {
			WriteError(w, http.StatusBadRequest, TypeInvalidRequest, "invalid unify settings: "+err.Error())
			return
		}
	}
	if body.Overrides != nil {
		if body.Overrides.Merges == nil {
			body.Overrides.Merges = []unifyMerge{}
		}
		if body.Overrides.Splits == nil {
			body.Overrides.Splits = []unifySplit{}
		}
		if err := validateUnifyOverrides(*body.Overrides); err != nil {
			WriteError(w, http.StatusBadRequest, TypeInvalidRequest, "invalid unify settings: "+err.Error())
			return
		}
		encoded, err := json.Marshal(*body.Overrides)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, TypeServer, "could not save unify settings")
			return
		}
		if err := s.settings.putRawSetting(ctx, settingUnifyOverrides, string(encoded)); err != nil {
			WriteError(w, http.StatusInternalServerError, TypeServer, "could not save unify settings")
			return
		}
	}
	s.writeUnify(w, r)
}

// ── Fusion ───────────────────────────────────────────────────────────────────

const (
	fusionDefaultK = 4
	fusionHardMaxK = 8
)

type fusionConfig struct {
	Mode        string   `json:"mode"`
	Models      []string `json:"models"`
	Judge       *string  `json:"judge"`
	K           int      `json:"k"`
	Strategy    string   `json:"strategy"`
	ExposePanel bool     `json:"expose_panel"`
}

func defaultFusionConfig() fusionConfig {
	return fusionConfig{Mode: "auto", Models: []string{}, Judge: nil, K: fusionDefaultK, Strategy: "synthesize", ExposePanel: false}
}

func (st *settingsStore) getFusion(ctx context.Context) (fusionConfig, error) {
	raw, ok, err := st.rawSetting(ctx, settingFusionConfig)
	if err != nil {
		return fusionConfig{}, err
	}
	if !ok {
		return defaultFusionConfig(), nil
	}
	var cfg fusionConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return defaultFusionConfig(), nil
	}
	if cfg.Mode != "auto" && cfg.Mode != "explicit" {
		return defaultFusionConfig(), nil
	}
	if cfg.Models == nil {
		cfg.Models = []string{}
	}
	return cfg, nil
}

type fusionConfigInput struct {
	Mode        *string  `json:"mode"`
	Models      []string `json:"models"`
	Judge       *string  `json:"judge"`
	K           *int     `json:"k"`
	Strategy    *string  `json:"strategy"`
	ExposePanel *bool    `json:"expose_panel"`
}

func (st *settingsStore) setFusion(ctx context.Context, in fusionConfigInput) (fusionConfig, error) {
	if in.Mode == nil || (*in.Mode != "auto" && *in.Mode != "explicit") {
		return fusionConfig{}, errors.New("mode must be auto or explicit")
	}
	if in.Strategy == nil || (*in.Strategy != "synthesize" && *in.Strategy != "best_of") {
		return fusionConfig{}, errors.New("strategy must be synthesize or best_of")
	}
	if in.K == nil || *in.K < 1 {
		return fusionConfig{}, errors.New("k must be a positive integer")
	}
	if in.ExposePanel == nil {
		return fusionConfig{}, errors.New("expose_panel is required")
	}
	// De-dup the panel and clamp to the operator ceiling, so a read/modify/
	// write round trip cannot store a value the getter would reject.
	seen := map[string]bool{}
	models := []string{}
	for _, m := range in.Models {
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		models = append(models, m)
		if len(models) >= fusionHardMaxK {
			break
		}
	}
	var judge *string
	if in.Judge != nil {
		if trimmed := strings.TrimSpace(*in.Judge); trimmed != "" {
			judge = &trimmed
		}
	}
	k := *in.K
	if k > fusionHardMaxK {
		k = fusionHardMaxK
	}
	cfg := fusionConfig{Mode: *in.Mode, Models: models, Judge: judge, K: k, Strategy: *in.Strategy, ExposePanel: *in.ExposePanel}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		return fusionConfig{}, err
	}
	if err := st.putRawSetting(ctx, settingFusionConfig, string(encoded)); err != nil {
		return fusionConfig{}, err
	}
	return cfg, nil
}

func (s *Server) handleGetFusion(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	cfg, err := s.settings.getFusion(r.Context())
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not read fusion settings")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"config": cfg, "maxK": fusionHardMaxK})
}

func (s *Server) handlePutFusion(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	var in fusionConfigInput
	if !DecodeJSON(w, r, &in) {
		return
	}
	cfg, err := s.settings.setFusion(r.Context(), in)
	if err != nil {
		WriteError(w, http.StatusBadRequest, TypeInvalidRequest, "invalid fusion config: "+err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"config": cfg, "maxK": fusionHardMaxK})
}

// ── Enable MCP ───────────────────────────────────────────────────────────────

func (s *Server) handleGetEnableMCP(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	WriteJSON(w, http.StatusOK, map[string]any{"enabled": s.settings.boolSetting(r.Context(), settingEnableMCP)})
}

func (s *Server) handlePutEnableMCP(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if !DecodeJSON(w, r, &body) {
		return
	}
	if body.Enabled == nil {
		WriteError(w, http.StatusBadRequest, TypeInvalidRequest, "invalid MCP setting: enabled must be a boolean")
		return
	}
	if err := s.settings.set(r.Context(), settingEnableMCP, boolValue(*body.Enabled)); err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not save the MCP setting")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"enabled": *body.Enabled})
}

// ── Claude / Gemini family maps ──────────────────────────────────────────────

var (
	claudeFamilies = []string{"default", "opus", "sonnet", "haiku"}
	geminiFamilies = []string{"default", "pro", "flash", "flashLite"}
)

func (st *settingsStore) getFamilyMap(ctx context.Context, key string, families []string) (map[string]string, error) {
	out := map[string]string{}
	for _, f := range families {
		out[f] = "auto"
	}
	raw, ok, err := st.rawSetting(ctx, key)
	if err != nil {
		return nil, err
	}
	if !ok {
		return out, nil
	}
	var stored map[string]string
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return out, nil
	}
	for _, f := range families {
		if v, present := stored[f]; present && strings.TrimSpace(v) != "" {
			out[f] = v
		}
	}
	return out, nil
}

func (st *settingsStore) setFamilyMap(ctx context.Context, key string, families []string, patch map[string]string) (map[string]string, error) {
	allowed := map[string]bool{}
	for _, f := range families {
		allowed[f] = true
	}
	for f, v := range patch {
		if !allowed[f] {
			return nil, fmt.Errorf("unknown family %q", f)
		}
		if strings.TrimSpace(v) == "" {
			return nil, fmt.Errorf("%s must be a non-empty model id or 'auto'", f)
		}
	}
	current, err := st.getFamilyMap(ctx, key, families)
	if err != nil {
		return nil, err
	}
	for f, v := range patch {
		current[f] = v
	}
	encoded, err := json.Marshal(current)
	if err != nil {
		return nil, err
	}
	if err := st.putRawSetting(ctx, key, string(encoded)); err != nil {
		return nil, err
	}
	return current, nil
}

func (s *Server) handleGetAnthropicMap(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	s.writeFamilyMap(w, r, settingAnthropicMap, claudeFamilies)
}

func (s *Server) handlePutAnthropicMap(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	s.putFamilyMap(w, r, settingAnthropicMap, claudeFamilies, "anthropic model map")
}

func (s *Server) handleGetGeminiMap(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	s.writeFamilyMap(w, r, settingGeminiMap, geminiFamilies)
}

func (s *Server) handlePutGeminiMap(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	s.putFamilyMap(w, r, settingGeminiMap, geminiFamilies, "Gemini model map")
}

func (s *Server) writeFamilyMap(w http.ResponseWriter, r *http.Request, key string, families []string) {
	m, err := s.settings.getFamilyMap(r.Context(), key, families)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not read the model map")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"map": m})
}

func (s *Server) putFamilyMap(w http.ResponseWriter, r *http.Request, key string, families []string, label string) {
	var patch map[string]string
	if !DecodeJSON(w, r, &patch) {
		return
	}
	m, err := s.settings.setFamilyMap(r.Context(), key, families, patch)
	if err != nil {
		WriteError(w, http.StatusBadRequest, TypeInvalidRequest, "invalid "+label+": "+err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"map": m})
}

// ── Agent compatibility ──────────────────────────────────────────────────────
//
// The Ollama emulation mode and the Claude-discovery-alias toggle are stored
// and returned faithfully; enforcing the emulation modes is the inference
// plane's job and is out of this change's scope.

func (s *Server) handleGetAgentCompatibility(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	s.writeAgentCompatibility(w, r)
}

func (s *Server) writeAgentCompatibility(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	mode, err := s.settings.get(ctx, settingOllamaEmulation)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not read agent compatibility settings")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"ollamaEmulation":              mode,
		"exposeClaudeDiscoveryAliases": s.settings.boolSetting(ctx, settingExposeCCAliases),
	})
}

func (s *Server) handlePutAgentCompatibility(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	var body struct {
		OllamaEmulation              *string `json:"ollamaEmulation"`
		ExposeClaudeDiscoveryAliases *bool   `json:"exposeClaudeDiscoveryAliases"`
	}
	if !DecodeJSON(w, r, &body) {
		return
	}
	ctx := r.Context()
	if body.OllamaEmulation != nil {
		if err := s.settings.set(ctx, settingOllamaEmulation, *body.OllamaEmulation); err != nil {
			WriteError(w, http.StatusBadRequest, TypeInvalidRequest, "invalid agent compatibility settings: "+err.Error())
			return
		}
	}
	if body.ExposeClaudeDiscoveryAliases != nil {
		if err := s.settings.set(ctx, settingExposeCCAliases, boolValue(*body.ExposeClaudeDiscoveryAliases)); err != nil {
			WriteError(w, http.StatusInternalServerError, TypeServer, "could not save agent compatibility settings")
			return
		}
	}
	s.writeAgentCompatibility(w, r)
}

// ── Proxy ────────────────────────────────────────────────────────────────────
//
// Prowl does not route upstream traffic through an outbound proxy, so `active`
// is always false: the operator's URL/mode/enabled/bypass settings are stored
// and returned faithfully, but no request is actually proxied. The relay token
// is stored write-only and never returned; the response reports only whether
// one is configured.

var proxyModes = map[string]bool{"forward": true, "fetch-relay": true}
var proxySchemes = map[string]bool{"http:": true, "https:": true, "socks5:": true, "socks5h:": true, "socks4:": true, "socks4a:": true}

func proxyURLError(rawURL, mode string) string {
	if rawURL == "" {
		return ""
	}
	if mode == "fetch-relay" {
		u, err := url.Parse(rawURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return "Relay URL must be a valid http or https URL"
		}
		return ""
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" {
		return "Invalid proxy URL — must be a valid URL like socks5://host:port"
	}
	if !proxySchemes[u.Scheme+":"] {
		return "Proxy URL must use http, https, socks5, socks5h, socks4, or socks4a scheme"
	}
	return ""
}

func (s *Server) proxyState(ctx context.Context) (map[string]any, error) {
	get := func(key string) string {
		v, _, err := s.settings.rawSetting(ctx, key)
		if err != nil {
			return ""
		}
		return v
	}
	mode := get(settingProxyMode)
	if mode == "" {
		mode = "forward"
	}
	bypass := []string{}
	if csv := get(settingProxyBypass); csv != "" {
		for _, p := range strings.Split(csv, ",") {
			if t := strings.TrimSpace(p); t != "" {
				bypass = append(bypass, t)
			}
		}
	}
	return map[string]any{
		"proxyUrl":                  get(settingProxyURL),
		"proxyMode":                 mode,
		"fetchRelayTokenConfigured": get(settingFetchRelayToken) != "",
		// The reference enables the proxy unless it was explicitly turned off:
		// isProxyEnabled() is `getSetting('proxy_enabled') !== '0'`, so an
		// unset value reads as enabled (lib/proxy.ts:366, proxy-restore.test.ts
		// :89). A fresh install therefore shows the switch on with no URL, which
		// leaves `active` false rather than routing anything.
		"enabled":         get(settingProxyEnabled) != "0",
		"bypassPlatforms": bypass,
		// Honest: nothing on this gateway routes through the proxy yet.
		"active": false,
	}, nil
}

func (s *Server) handleGetProxy(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	state, err := s.proxyState(r.Context())
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not read proxy settings")
		return
	}
	WriteJSON(w, http.StatusOK, state)
}

func (s *Server) handlePutProxy(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	var body struct {
		ProxyURL        *string   `json:"proxyUrl"`
		ProxyMode       *string   `json:"proxyMode"`
		FetchRelayToken *string   `json:"fetchRelayToken"`
		Enabled         *bool     `json:"enabled"`
		BypassPlatforms *[]string `json:"bypassPlatforms"`
	}
	if !DecodeJSON(w, r, &body) {
		return
	}
	ctx := r.Context()
	if body.ProxyMode != nil && !proxyModes[*body.ProxyMode] {
		WriteError(w, http.StatusBadRequest, TypeInvalidRequest, "Proxy mode must be forward or fetch-relay")
		return
	}
	if body.ProxyURL != nil || body.ProxyMode != nil {
		nextMode := "forward"
		if body.ProxyMode != nil {
			nextMode = *body.ProxyMode
		} else if v, _, _ := s.settings.rawSetting(ctx, settingProxyMode); v != "" {
			nextMode = v
		}
		nextURL := ""
		if body.ProxyURL != nil {
			nextURL = strings.TrimSpace(*body.ProxyURL)
		} else {
			nextURL, _, _ = s.settings.rawSetting(ctx, settingProxyURL)
		}
		if msg := proxyURLError(nextURL, nextMode); msg != "" {
			WriteError(w, http.StatusBadRequest, TypeInvalidRequest, msg)
			return
		}
	}
	save := func(key, value string) bool {
		if err := s.settings.putRawSetting(ctx, key, value); err != nil {
			WriteError(w, http.StatusInternalServerError, TypeServer, "could not save proxy settings")
			return false
		}
		return true
	}
	if body.ProxyURL != nil && !save(settingProxyURL, strings.TrimSpace(*body.ProxyURL)) {
		return
	}
	if body.ProxyMode != nil && !save(settingProxyMode, *body.ProxyMode) {
		return
	}
	if body.FetchRelayToken != nil && !save(settingFetchRelayToken, strings.TrimSpace(*body.FetchRelayToken)) {
		return
	}
	if body.Enabled != nil && !save(settingProxyEnabled, boolValue(*body.Enabled)) {
		return
	}
	if body.BypassPlatforms != nil {
		cleaned := []string{}
		for _, p := range *body.BypassPlatforms {
			if t := strings.TrimSpace(p); t != "" {
				cleaned = append(cleaned, t)
			}
		}
		if !save(settingProxyBypass, strings.Join(cleaned, ",")) {
			return
		}
	}
	state, err := s.proxyState(ctx)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not read proxy settings")
		return
	}
	WriteJSON(w, http.StatusOK, state)
}

func (s *Server) handleProxyTest(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	var body struct {
		ProxyMode *string `json:"proxyMode"`
	}
	if r.ContentLength != 0 {
		if !DecodeJSON(w, r, &body) {
			return
		}
	}
	if body.ProxyMode != nil && !proxyModes[*body.ProxyMode] {
		WriteError(w, http.StatusBadRequest, TypeInvalidRequest, "Proxy mode must be forward or fetch-relay")
		return
	}
	// Honest degradation: Prowl has no outbound-proxy dispatcher to probe, so
	// the Test button reports an explicit unsupported result rather than a
	// fabricated success.
	WriteJSON(w, http.StatusOK, map[string]any{
		"ok":        false,
		"latencyMs": 0,
		"error":     "Proxy connectivity testing is not supported by this gateway.",
	})
}

// ── URL tokens ───────────────────────────────────────────────────────────────

type urlTokenView struct {
	ID          int64   `json:"id"`
	Label       string  `json:"label"`
	TokenPrefix string  `json:"tokenPrefix"`
	CreatedAt   string  `json:"createdAt"`
	LastUsedAt  *string `json:"lastUsedAt"`
	RevokedAt   *string `json:"revokedAt"`
}

func isoOrNil(sec sql.NullInt64) *string {
	if !sec.Valid {
		return nil
	}
	s := time.Unix(sec.Int64, 0).UTC().Format(time.RFC3339)
	return &s
}

func (s *Server) listURLTokens(ctx context.Context) ([]urlTokenView, error) {
	rows, err := s.engine.DB().QueryContext(ctx,
		`SELECT id, label, token_prefix, created_at, last_used_at, revoked_at
		 FROM url_tokens ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []urlTokenView{}
	for rows.Next() {
		var (
			v        urlTokenView
			created  int64
			lastUsed sql.NullInt64
			revoked  sql.NullInt64
		)
		if err := rows.Scan(&v.ID, &v.Label, &v.TokenPrefix, &created, &lastUsed, &revoked); err != nil {
			return nil, err
		}
		v.CreatedAt = time.Unix(created, 0).UTC().Format(time.RFC3339)
		v.LastUsedAt = isoOrNil(lastUsed)
		v.RevokedAt = isoOrNil(revoked)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Server) handleListURLTokens(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	tokens, err := s.listURLTokens(r.Context())
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not list url tokens")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"tokens": tokens})
}

func (s *Server) handleMintURLToken(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	var body struct {
		Label string `json:"label"`
	}
	if r.ContentLength != 0 {
		if !DecodeJSON(w, r, &body) {
			return
		}
	}
	if len(body.Label) > 120 {
		WriteError(w, http.StatusBadRequest, TypeInvalidRequest, "invalid URL token label")
		return
	}
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not mint a url token")
		return
	}
	token := "flmurl_" + base64.RawURLEncoding.EncodeToString(raw)
	prefix := token[:12] + "…"
	sum := sha256.Sum256([]byte(token))
	now := time.Now().Unix()
	res, err := s.engine.DB().ExecContext(r.Context(),
		`INSERT INTO url_tokens (token_hash, label, token_prefix, created_at) VALUES (?, ?, ?, ?)`,
		hex.EncodeToString(sum[:]), strings.TrimSpace(body.Label), prefix, now)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not mint a url token")
		return
	}
	id, _ := res.LastInsertId()
	WriteJSON(w, http.StatusCreated, map[string]any{
		"id":          id,
		"label":       strings.TrimSpace(body.Label),
		"tokenPrefix": prefix,
		"createdAt":   time.Unix(now, 0).UTC().Format(time.RFC3339),
		"lastUsedAt":  nil,
		"revokedAt":   nil,
		// The full token is returned exactly once, at mint; only its hash is
		// stored, so a copy of the database can never reconstruct it.
		"token": token,
	})
}

func (s *Server) handleRevokeURLToken(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	raw := r.PathValue("id")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || raw == "" {
		WriteError(w, http.StatusBadRequest, TypeInvalidRequest, "invalid URL token id")
		return
	}
	res, err := s.engine.DB().ExecContext(r.Context(),
		`UPDATE url_tokens SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`,
		time.Now().Unix(), id)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not revoke the url token")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		WriteError(w, http.StatusNotFound, TypeNotFound, "active URL token not found")
		return
	}
	// 204: the client's apiFetch reads an empty body as undefined.
	WriteNoContent(w)
}

// boolValue renders a Go bool as the reference's stored '1'/'0'.
func boolValue(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
