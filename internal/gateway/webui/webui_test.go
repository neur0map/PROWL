package webui

import (
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestPinnedCSPHashMatchesTheVendoredBuild is the trap this guards: the page
// carries one inline script, and the policy authorises it by hash. Rebuild the
// client with any change to that bootstrap and the pinned constant goes stale,
// at which point the browser blocks the script and the dashboard renders
// unstyled with no error anyone would connect to CSP.
func TestPinnedCSPHashMatchesTheVendoredBuild(t *testing.T) {
	t.Parallel()

	computed, ok := BootstrapHash()
	require.True(t, ok, "the vendored index.html must carry the inline bootstrap")
	require.Equal(t, InlineBootstrapSHA, computed,
		"the vendored client was rebuilt: update InlineBootstrapSHA to the computed value")
}

// TestPolicyAllowsNothingRemote keeps the dashboard self-contained. Every
// asset, including the fonts, ships in the binary, so the page must work with
// no network and must not be able to be told to fetch anything.
func TestPolicyAllowsNothingRemote(t *testing.T) {
	t.Parallel()

	policy := ContentSecurityPolicy()
	require.Contains(t, policy, "default-src 'self'")
	require.Contains(t, policy, "script-src 'self' "+InlineBootstrapSHA)
	require.Contains(t, policy, "object-src 'none'")
	require.Contains(t, policy, "frame-ancestors 'none'")
	require.NotContains(t, policy, "http://")
	require.NotContains(t, policy, "https://")
	require.NotContains(t, policy, "'unsafe-eval'")
	require.NotContains(t, policy, "script-src 'self' 'unsafe-inline'",
		"an inline allowance by hash must not be widened to all inline script")
}

// TestClientRoutesFallBackToTheApp covers a reload on any of the client's own
// routes: a file server alone would 404 them, which is the classic SPA break.
func TestClientRoutesFallBackToTheApp(t *testing.T) {
	t.Parallel()

	h, err := Handler()
	require.NoError(t, err)

	for _, path := range []string{"/", "/models/chat", "/keys", "/analytics", "/settings/deep/link"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

		require.Equal(t, http.StatusOK, rec.Code, "%s must serve the app", path)
		body, _ := io.ReadAll(rec.Body)
		require.Contains(t, string(body), `<div id="root">`, "%s must return the SPA shell", path)
		require.Contains(t, rec.Header().Get("Content-Type"), "text/html")
	}
}

// TestAssetsAreServedAndImmutable checks the real bundle is reachable and
// cacheable. Asset names carry a content hash, so a long cache is safe and is
// what keeps a reload from refetching several megabytes.
func TestAssetsAreServedAndImmutable(t *testing.T) {
	t.Parallel()

	h, err := Handler()
	require.NoError(t, err)

	page, err := Index()
	require.NoError(t, err)

	// Take the script and stylesheet the page actually references, rather
	// than hardcoding hashed filenames that change on every rebuild.
	for _, marker := range []string{`src="`, `href="/assets/`} {
		idx := strings.Index(string(page), marker)
		require.GreaterOrEqual(t, idx, 0)
	}

	for _, ref := range assetRefs(string(page)) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, ref, nil))
		require.Equal(t, http.StatusOK, rec.Code, "%s must be served from the binary", ref)

		// Only /assets/ names carry a content hash. The favicon does not, so
		// caching it immutably would pin a stale icon forever.
		if strings.HasPrefix(ref, "/assets/") {
			require.Contains(t, rec.Header().Get("Cache-Control"), "immutable",
				"%s is content-hashed and must be cacheable", ref)
		} else {
			require.NotContains(t, rec.Header().Get("Cache-Control"), "immutable",
				"%s is not content-hashed and must not be pinned", ref)
		}
	}
}

// TestRebrandIsComplete is the user-visible contract: the product is Prowl,
// and no upstream brand string may survive in what the browser receives.
func TestRebrandIsComplete(t *testing.T) {
	t.Parallel()

	page, err := Index()
	require.NoError(t, err)
	require.Contains(t, string(page), "<title>Prowl")
	require.NotContains(t, string(page), "FreeLLMAPI")

	sub, err := FS()
	require.NoError(t, err)
	h, err := Handler()
	require.NoError(t, err)
	_ = sub

	for _, ref := range assetRefs(string(page)) {
		if !strings.HasSuffix(ref, ".js") {
			continue
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, ref, nil))
		body, _ := io.ReadAll(rec.Body)
		require.NotContains(t, string(body), "FreeLLMAPI",
			"%s still carries the upstream brand", ref)
	}
}

// assetRefs pulls the asset paths index.html references.
func assetRefs(page string) []string {
	var out []string
	for _, attr := range []string{`src="`, `href="`} {
		rest := page
		for {
			i := strings.Index(rest, attr)
			if i < 0 {
				break
			}
			rest = rest[i+len(attr):]
			j := strings.Index(rest, `"`)
			if j < 0 {
				break
			}
			ref := rest[:j]
			if strings.HasPrefix(ref, "/assets/") || ref == "/favicon.svg" {
				out = append(out, ref)
			}
			rest = rest[j:]
		}
	}
	return out
}

// TestBundleHasNoBareImports catches a silent build defect that cost real
// debugging time: an incomplete dependency install made the bundler emit
// `import{…}from"react-is"` instead of inlining it. A browser cannot resolve a
// bare specifier, so the module never executed — and it failed with NO console
// error, NO failed request and NO page error. The dashboard was simply blank.
//
// Any bare specifier in the vendored build means the same class of failure, so
// this asserts there are none rather than blacklisting one package.
func TestBundleHasNoBareImports(t *testing.T) {
	t.Parallel()

	sub, err := FS()
	require.NoError(t, err)

	entries, err := fs.ReadDir(sub, "assets")
	require.NoError(t, err)

	// A bare specifier does not start with "/", "./" or "../".
	bare := regexp.MustCompile(`(?:^|[;\s}])(?:import|export)[^"']*from\s*["']([a-zA-Z@][^"']*)["']`)

	checked := 0
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".js") {
			continue
		}
		body, err := fs.ReadFile(sub, "assets/"+entry.Name())
		require.NoError(t, err)
		checked++

		if m := bare.FindSubmatch(body); m != nil {
			t.Fatalf("%s imports %q as a bare specifier: the browser cannot resolve it, "+
				"so the module silently never runs and the dashboard renders blank. "+
				"Rebuild the client with a complete dependency install.",
				entry.Name(), m[1])
		}
	}
	require.Positive(t, checked, "no bundle was checked")
}

// TestEntryScriptIsReferencedAndPresent guards the other half of the same
// failure: an index.html pointing at a filename the build no longer emits.
func TestEntryScriptIsReferencedAndPresent(t *testing.T) {
	t.Parallel()

	page, err := Index()
	require.NoError(t, err)
	sub, err := FS()
	require.NoError(t, err)

	refs := assetRefs(string(page))
	require.NotEmpty(t, refs, "index.html must reference its bundle")

	sawJS := false
	for _, ref := range refs {
		f, err := sub.Open(strings.TrimPrefix(ref, "/"))
		require.NoError(t, err, "index.html references %s, which is not in the build", ref)
		_ = f.Close()
		if strings.HasSuffix(ref, ".js") {
			sawJS = true
		}
	}
	require.True(t, sawJS, "index.html must reference a script bundle")
}

// TestWebUIDirServesFromDisk covers the change-visibility problem: the
// dashboard is compiled into the binary, so a UI edit needs a rebuild and a
// process restart before anyone sees it — and a stale dashboard is
// indistinguishable from a change that silently failed. PROWL_WEBUI_DIR makes
// a rebuild visible on the next reload.
func TestWebUIDirServesFromDisk(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "index.html"),
		[]byte("<!doctype html><title>from disk</title>"), 0o644))
	t.Setenv("PROWL_WEBUI_DIR", dir)

	page, err := Index()
	require.NoError(t, err)
	require.Contains(t, string(page), "from disk")

	handler, err := Handler()
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/keys", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "from disk",
		"a client route must fall back to the on-disk index")
}

// TestWebUIDirIgnoresAnUnusableDirectory keeps a stale or mistyped value from
// serving nothing at all: the embedded copy is the floor.
func TestWebUIDirIgnoresAnUnusableDirectory(t *testing.T) {
	t.Setenv("PROWL_WEBUI_DIR", filepath.Join(t.TempDir(), "does-not-exist"))

	page, err := Index()
	require.NoError(t, err)
	require.Contains(t, string(page), "<!doctype html",
		"an unusable override must fall back to the embedded dashboard")
}
