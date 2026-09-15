package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func extraSession(t *testing.T) (*Server, map[string]string) {
	t.Helper()
	s := testServer(t, Options{})
	h := settingsSession(t, s)
	h["Content-Type"] = "application/json"
	return s, h
}

// TestNewScalarSettingRoundTripsAndRejects exercises the shared registry the
// new scalar settings join: a valid value survives, an invalid one is refused
// without clobbering the stored value, and a key the registry does not know is
// rejected rather than silently written.
func TestNewScalarSettingRoundTripsAndRejects(t *testing.T) {
	t.Parallel()
	store := newSettingsStore(t)
	ctx := context.Background()

	require.NoError(t, store.set(ctx, settingOllamaEmulation, "key-required"))
	got, err := store.get(ctx, settingOllamaEmulation)
	require.NoError(t, err)
	require.Equal(t, "key-required", got)

	require.Error(t, store.set(ctx, settingOllamaEmulation, "bogus-mode"),
		"an out-of-enum value must be refused")
	got, err = store.get(ctx, settingOllamaEmulation)
	require.NoError(t, err)
	require.Equal(t, "key-required", got, "a rejected value must not overwrite the stored one")

	require.Error(t, store.set(ctx, "totally_unknown_key", "x"),
		"a key the registry does not know must be rejected")
}

func TestCompressionRoundTripAndReject(t *testing.T) {
	t.Parallel()
	s, h := extraSession(t)

	resp, body := do(t, s, http.MethodGet, "/api/settings/compression", "", h)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var def compressionConfig
	require.NoError(t, json.Unmarshal([]byte(body), &def))
	require.Equal(t, "off", def.Mode)
	require.Len(t, def.Engines, 8, "the default carries all eight engines")
	require.True(t, def.PrefixFreeze)

	resp, body = do(t, s, http.MethodPut, "/api/settings/compression",
		`{"mode":"aggressive","trustProjectFilters":true}`, h)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var got compressionConfig
	require.NoError(t, json.Unmarshal([]byte(body), &got))
	require.Equal(t, "aggressive", got.Mode)
	require.True(t, got.TrustProjectFilters)

	resp, body = do(t, s, http.MethodPut, "/api/settings/compression", `{"mode":"turbo"}`, h)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Equal(t, "invalid_request_error", errorType(t, body))

	resp, body = do(t, s, http.MethodGet, "/api/settings/compression", "", h)
	var after compressionConfig
	require.NoError(t, json.Unmarshal([]byte(body), &after))
	require.Equal(t, "aggressive", after.Mode, "a rejected mode must not clobber the stored one")
}

func TestFusionRoundTripAndReject(t *testing.T) {
	t.Parallel()
	s, h := extraSession(t)

	type fusionResp struct {
		Config fusionConfig `json:"config"`
		MaxK   int          `json:"maxK"`
	}
	resp, body := do(t, s, http.MethodGet, "/api/settings/fusion", "", h)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var d fusionResp
	require.NoError(t, json.Unmarshal([]byte(body), &d))
	require.Equal(t, fusionHardMaxK, d.MaxK)
	require.Equal(t, "auto", d.Config.Mode)

	resp, body = do(t, s, http.MethodPut, "/api/settings/fusion",
		`{"mode":"explicit","models":["a","b","a"],"judge":"  j  ","k":99,"strategy":"best_of","expose_panel":true}`, h)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NoError(t, json.Unmarshal([]byte(body), &d))
	require.Equal(t, "explicit", d.Config.Mode)
	require.Equal(t, []string{"a", "b"}, d.Config.Models, "the panel is de-duplicated")
	require.Equal(t, fusionHardMaxK, d.Config.K, "k is clamped to the ceiling")
	require.NotNil(t, d.Config.Judge)
	require.Equal(t, "j", *d.Config.Judge, "the judge is trimmed")
	require.True(t, d.Config.ExposePanel)

	resp, _ = do(t, s, http.MethodPut, "/api/settings/fusion",
		`{"mode":"auto","k":4,"strategy":"nope","expose_panel":false}`, h)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestUnifyRoundTripAndReject(t *testing.T) {
	t.Parallel()
	s, h := extraSession(t)

	type unifyResp struct {
		Enabled   bool           `json:"enabled"`
		Overrides unifyOverrides `json:"overrides"`
	}
	resp, body := do(t, s, http.MethodGet, "/api/settings/unify", "", h)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var d unifyResp
	require.NoError(t, json.Unmarshal([]byte(body), &d))
	require.True(t, d.Enabled, "unify defaults on")

	// The stored enabled flag is accepted for backward compatibility but a read
	// always reports on, matching the reference where the toggle was removed
	// (model-groups.ts:85-92). Overrides, by contrast, do persist.
	resp, body = do(t, s, http.MethodPut, "/api/settings/unify",
		`{"enabled":false,"overrides":{"merges":[{"into":"grp","keys":["a","b"]}],"splits":[]}}`, h)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NoError(t, json.Unmarshal([]byte(body), &d))
	require.True(t, d.Enabled, "unify is always on; writing false does not turn it off")
	require.Len(t, d.Overrides.Merges, 1)
	require.Equal(t, "grp", d.Overrides.Merges[0].Into)

	resp, body = do(t, s, http.MethodGet, "/api/settings/unify", "", h)
	require.NoError(t, json.Unmarshal([]byte(body), &d))
	require.True(t, d.Enabled, "a later read still reports on")
	require.Len(t, d.Overrides.Merges, 1, "the saved override persists")

	resp, _ = do(t, s, http.MethodPut, "/api/settings/unify",
		`{"overrides":{"merges":[{"into":"x","keys":[]}],"splits":[]}}`, h)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)

	resp, body = do(t, s, http.MethodGet, "/api/settings/unify", "", h)
	require.NoError(t, json.Unmarshal([]byte(body), &d))
	require.Len(t, d.Overrides.Merges, 1, "a rejected override must not clobber the stored one")
}

func TestFamilyMapsRoundTripAndReject(t *testing.T) {
	t.Parallel()
	s, h := extraSession(t)

	type mapResp struct {
		Map map[string]string `json:"map"`
	}
	resp, body := do(t, s, http.MethodGet, "/api/settings/anthropic-map", "", h)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var d mapResp
	require.NoError(t, json.Unmarshal([]byte(body), &d))
	require.Len(t, d.Map, 4)
	require.Equal(t, "auto", d.Map["opus"])

	resp, body = do(t, s, http.MethodPut, "/api/settings/anthropic-map", `{"opus":"my-model"}`, h)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NoError(t, json.Unmarshal([]byte(body), &d))
	require.Equal(t, "my-model", d.Map["opus"])
	require.Equal(t, "auto", d.Map["sonnet"], "a partial update leaves other families untouched")

	resp, _ = do(t, s, http.MethodPut, "/api/settings/anthropic-map", `{"o3":"x"}`, h)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode, "an unknown family is refused")

	resp, body = do(t, s, http.MethodPut, "/api/settings/gemini-map", `{"flashLite":"g"}`, h)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NoError(t, json.Unmarshal([]byte(body), &d))
	require.Equal(t, "g", d.Map["flashLite"])
}

func TestAgentCompatibilityRoundTripAndReject(t *testing.T) {
	t.Parallel()
	s, h := extraSession(t)

	resp, body := do(t, s, http.MethodPut, "/api/settings/agent-compatibility",
		`{"ollamaEmulation":"key-required","exposeClaudeDiscoveryAliases":true}`, h)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var d struct {
		OllamaEmulation string `json:"ollamaEmulation"`
		Expose          bool   `json:"exposeClaudeDiscoveryAliases"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &d))
	require.Equal(t, "key-required", d.OllamaEmulation)
	require.True(t, d.Expose)

	resp, _ = do(t, s, http.MethodPut, "/api/settings/agent-compatibility",
		`{"ollamaEmulation":"invalid"}`, h)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestEnableMCPRoundTripAndReject(t *testing.T) {
	t.Parallel()
	s, h := extraSession(t)

	resp, body := do(t, s, http.MethodGet, "/api/settings/enable-mcp", "", h)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, body, `"enabled":false`)

	resp, body = do(t, s, http.MethodPut, "/api/settings/enable-mcp", `{"enabled":true}`, h)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, body, `"enabled":true`)

	resp, _ = do(t, s, http.MethodPut, "/api/settings/enable-mcp", `{}`, h)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode, "a missing enabled is refused")
}

// TestProxySettingsRoundTripAndSecrecy checks the proxy settings round-trip,
// that the relay token is stored write-only (reported only as configured, never
// echoed), and that `active` is an honest false because Prowl does not proxy.
func TestProxySettingsRoundTripAndSecrecy(t *testing.T) {
	t.Parallel()
	s, h := extraSession(t)

	resp, body := do(t, s, http.MethodPut, "/api/settings/proxy",
		`{"proxyUrl":"socks5://host:1080","proxyMode":"forward","enabled":true,"fetchRelayToken":"top-secret-relay","bypassPlatforms":["openai","  "]}`, h)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NotContains(t, body, "top-secret-relay", "the relay token is never returned")

	var d map[string]any
	require.NoError(t, json.Unmarshal([]byte(body), &d))
	require.Equal(t, "socks5://host:1080", d["proxyUrl"])
	require.Equal(t, true, d["enabled"])
	require.Equal(t, true, d["fetchRelayTokenConfigured"])
	require.Equal(t, false, d["active"], "Prowl does not route through a proxy, so active stays false")
	require.Equal(t, []any{"openai"}, d["bypassPlatforms"], "blank bypass entries are dropped")

	resp, _ = do(t, s, http.MethodPut, "/api/settings/proxy", `{"proxyMode":"nope"}`, h)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)

	resp, _ = do(t, s, http.MethodPut, "/api/settings/proxy", `{"proxyUrl":"ftp://x"}`, h)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode, "an unsupported scheme is refused")
}

func TestProxyTestReportsUnsupported(t *testing.T) {
	t.Parallel()
	s, h := extraSession(t)
	resp, body := do(t, s, http.MethodPost, "/api/settings/proxy/test", `{}`, h)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var d map[string]any
	require.NoError(t, json.Unmarshal([]byte(body), &d))
	require.Equal(t, false, d["ok"])
	msg, _ := d["error"].(string)
	require.Contains(t, msg, "not supported")
}

// TestURLTokenLifecycle mints, lists and revokes a URL token, and pins the
// credential properties: the full token is returned once and never again, only
// its hash is stored, and a second revoke is a 404.
func TestURLTokenLifecycle(t *testing.T) {
	t.Parallel()
	s, h := extraSession(t)

	resp, body := do(t, s, http.MethodPost, "/api/settings/url-tokens", `{"label":"share"}`, h)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var minted struct {
		ID          int64  `json:"id"`
		Token       string `json:"token"`
		TokenPrefix string `json:"tokenPrefix"`
		Label       string `json:"label"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &minted))
	require.True(t, strings.HasPrefix(minted.Token, "flmurl_"))
	require.Equal(t, "share", minted.Label)
	require.NotEmpty(t, minted.TokenPrefix)

	resp, body = do(t, s, http.MethodGet, "/api/settings/url-tokens", "", h)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NotContains(t, body, minted.Token, "the full token is never returned by list")
	var list struct {
		Tokens []map[string]any `json:"tokens"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &list))
	require.Len(t, list.Tokens, 1)
	require.Nil(t, list.Tokens[0]["revokedAt"], "a nullable timestamp stays null, not zero")
	require.Nil(t, list.Tokens[0]["lastUsedAt"])

	var hash string
	require.NoError(t, s.engine.DB().QueryRow(`SELECT token_hash FROM url_tokens WHERE id=?`, minted.ID).Scan(&hash))
	require.NotEqual(t, minted.Token, hash)
	require.Len(t, hash, 64, "only the sha256 hex is stored")

	idPath := "/api/settings/url-tokens/" + strconv.FormatInt(minted.ID, 10)
	resp, _ = do(t, s, http.MethodDelete, idPath, "", h)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)

	resp, body = do(t, s, http.MethodDelete, idPath, "", h)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	require.Equal(t, "not_found_error", errorType(t, body))
}

// TestSettingsDefaultsMatchReference pins the fresh-install value of every
// settings pane the dashboard renders. This is the regression guard for the
// bug this change fixes: a brand-new install must arrive populated with the
// reference's defaults, never an empty object. Every expected value is cited to
// the reference source it was taken from.
func TestSettingsDefaultsMatchReference(t *testing.T) {
	t.Parallel()
	s, h := extraSession(t)

	get := func(path string) map[string]any {
		resp, body := do(t, s, http.MethodGet, path, "", h)
		require.Equal(t, http.StatusOK, resp.StatusCode, "%s must not be empty or missing", path)
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(body), &m), "%s body was %q", path, body)
		return m
	}

	// Compression: DEFAULT_COMPRESSION_CONFIG and the eight engine defaults
	// (compression/config.ts:14-36). Off by default, but the full config is
	// present so the pane reads as "off", not "missing".
	comp := get("/api/settings/compression")
	require.Equal(t, "off", comp["mode"])
	require.Equal(t, false, comp["trustProjectFilters"])
	require.Equal(t, true, comp["prefixFreeze"])
	require.NotContains(t, comp, "autoTriggerEstTokens", "no auto-trigger threshold by default")
	require.NotContains(t, comp, "targetTokens", "no target token count by default")
	engines, ok := comp["engines"].(map[string]any)
	require.True(t, ok)
	require.Len(t, engines, 8, "all eight engines ship")
	for id, e := range engines {
		require.Equal(t, true, e.(map[string]any)["enabled"], "engine %s enabled by default", id)
	}
	dedup := engines["dedup"].(map[string]any)
	require.Equal(t, float64(80), dedup["minBlockChars"])
	require.Equal(t, float64(3), dedup["minBlockLines"])
	tool := engines["toolfilter"].(map[string]any)
	require.Equal(t, "standard", tool["intensity"])
	require.Equal(t, float64(120), tool["maxLinesPerResult"])
	require.Equal(t, float64(12000), tool["maxCharsPerResult"])
	require.Equal(t, []any{}, tool["disabledFilters"])
	require.Equal(t, float64(8), engines["jsoncompact"].(map[string]any)["minRows"])
	require.Equal(t, float64(18000), engines["relevance"].(map[string]any)["maxChars"])
	aging := engines["aging"].(map[string]any)
	require.Equal(t, float64(3), aging["liveTurns"])
	require.Equal(t, float64(8), aging["condenseAfterTurns"])

	// Fusion: defaultSavedConfig with k = panelDefaultK (4) and maxK = 8
	// (fusion.ts:107, 40-41, 84-86).
	fusion := get("/api/settings/fusion")
	require.Equal(t, float64(fusionHardMaxK), fusion["maxK"])
	fcfg := fusion["config"].(map[string]any)
	require.Equal(t, "auto", fcfg["mode"])
	require.Equal(t, []any{}, fcfg["models"], "auto mode uses the fallback chain, not a saved panel")
	require.Nil(t, fcfg["judge"])
	require.Equal(t, float64(fusionDefaultK), fcfg["k"])
	require.Equal(t, "synthesize", fcfg["strategy"])
	require.Equal(t, false, fcfg["expose_panel"])

	// Unify: always on with empty overrides (model-groups.ts:45, 90-92).
	unify := get("/api/settings/unify")
	require.Equal(t, true, unify["enabled"])
	ov := unify["overrides"].(map[string]any)
	require.Equal(t, []any{}, ov["merges"])
	require.Equal(t, []any{}, ov["splits"])

	// Family maps: every Claude/Gemini family defaults to the 'auto' sentinel
	// (anthropic-map.ts:22, gemini-map.ts:10-15).
	for _, path := range []string{"/api/settings/anthropic-map", "/api/settings/gemini-map"} {
		m := get(path)["map"].(map[string]any)
		require.Len(t, m, 4, "%s carries all four families", path)
		require.Equal(t, "auto", m["default"])
		for fam, target := range m {
			require.Equal(t, "auto", target, "%s family %s defaults to auto", path, fam)
		}
	}

	// Agent compatibility: Ollama emulation off, no Claude discovery aliases
	// (ollama.ts:19-22 default 'off'; settings.ts:199).
	compat := get("/api/settings/agent-compatibility")
	require.Equal(t, "off", compat["ollamaEmulation"])
	require.Equal(t, false, compat["exposeClaudeDiscoveryAliases"])

	// MCP server: off on a fresh install (mcp.ts:326-330; the migration only
	// seeds '1' for an upgrade that already had provider keys).
	require.Equal(t, false, get("/api/settings/enable-mcp")["enabled"])

	// Proxy: enabled unless explicitly turned off (lib/proxy.ts:366), forward
	// mode, no URL, and never active because Prowl does not proxy.
	proxy := get("/api/settings/proxy")
	require.Equal(t, true, proxy["enabled"], "the reference defaults the proxy switch on")
	require.Equal(t, "forward", proxy["proxyMode"])
	require.Equal(t, "", proxy["proxyUrl"])
	require.Equal(t, false, proxy["fetchRelayTokenConfigured"])
	require.Equal(t, false, proxy["active"], "no request is proxied, so active is honestly false")
	require.Equal(t, []any{}, proxy["bypassPlatforms"])
}
