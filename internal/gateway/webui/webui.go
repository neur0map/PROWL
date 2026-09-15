// Package webui serves the gateway dashboard.
//
// The dashboard is FreeLLMAPI's client, rebranded to Prowl
// (github.com/tashfeenahmed/freellmapi @ 780a7d8d, v0.9.9, MIT — see
// NOTICE.md). Both its source (client/, with the shared/ types it imports) and
// its built output (dist/) are vendored: the source so the UI can be rebuilt
// from this repository alone with `task webui:build`, and the output so a plain
// `go build` produces a working binary with no Node toolchain at build time.
// Only dist is embedded below; client/ and shared/ are build-time source.
package webui

import (
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

// InlineBootstrapSHA authorises the one inline script the page carries: a
// pre-paint block that applies the stored theme and text direction before
// first render, so the UI does not flash the wrong colour scheme.
//
// It is pinned rather than computed at request time because a mismatch must
// fail loudly in tests instead of silently widening the policy. Recompute it
// with BootstrapHash when the vendored client is rebuilt.
const InlineBootstrapSHA = "'sha256-4Mz/yZAENQGlTAAeE1WqXruXCripvlvl0s+Q9S1VS4A='"

var inlineScript = regexp.MustCompile(`(?s)<script>(.*?)</script>`)

// FS returns the built client rooted at dist.
func FS() (fs.FS, error) {
	if dir := overrideDir(); dir != "" {
		return os.DirFS(dir), nil
	}
	return fs.Sub(distFS, "dist")
}

// overrideDir is a built asset directory to serve instead of the embedded
// copy, named by PROWL_WEBUI_DIR.
//
// The dashboard is compiled into the binary, so a UI change normally needs a
// rebuild AND a process restart before anyone can see it — which is how a
// stale dashboard gets mistaken for a change that did not work. Pointing this
// at the client's dist directory makes a rebuild visible on the next reload,
// with no restart. It is ignored unless the directory actually holds an
// index.html, so a stale or mistyped value falls back to the embedded assets
// rather than serving nothing.
func overrideDir() string {
	dir := strings.TrimSpace(os.Getenv("PROWL_WEBUI_DIR"))
	if dir == "" {
		return ""
	}
	if _, err := os.Stat(filepath.Join(dir, "index.html")); err != nil {
		return ""
	}
	return dir
}

// Index returns index.html, which the SPA fallback serves for every client
// route.
func Index() ([]byte, error) {
	if dir := overrideDir(); dir != "" {
		return os.ReadFile(filepath.Join(dir, "index.html"))
	}
	return distFS.ReadFile("dist/index.html")
}

// BootstrapHash computes the CSP hash of the page's inline bootstrap, so a
// test can assert the pinned constant still matches the vendored build.
func BootstrapHash() (string, bool) {
	page, err := Index()
	if err != nil {
		return "", false
	}
	m := inlineScript.FindSubmatch(page)
	if m == nil {
		return "", false
	}
	sum := sha256.Sum256(m[1])
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'", true
}

// ContentSecurityPolicy is the policy the dashboard is served under. It
// allows exactly one inline script by hash and no remote origins: every
// asset, including the fonts, is served from this binary, so the page works
// offline and cannot be told to fetch anything.
func ContentSecurityPolicy() string {
	return strings.Join([]string{
		"default-src 'self'",
		"script-src 'self' " + InlineBootstrapSHA,
		"style-src 'self' 'unsafe-inline'",
		"img-src 'self' data:",
		"font-src 'self'",
		"connect-src 'self'",
		"object-src 'none'",
		"base-uri 'none'",
		"frame-ancestors 'none'",
		"form-action 'none'",
	}, "; ")
}

// Handler serves the built assets with an SPA fallback: the client owns 21
// routes, and a reload on any of them must return the app rather than a 404.
func Handler() (http.Handler, error) {
	sub, err := FS()
	if err != nil {
		return nil, err
	}
	files := http.FileServer(http.FS(sub))
	page, err := Index()
	if err != nil {
		return nil, err
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", ContentSecurityPolicy())
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")

		clean := strings.TrimPrefix(r.URL.Path, "/")
		if clean != "" {
			if f, err := sub.Open(clean); err == nil {
				_ = f.Close()
				// Hashed asset names make the content immutable, so a long
				// cache is safe and keeps a reload from refetching 5 MB.
				if strings.HasPrefix(clean, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
		}

		// Any other path is a client route.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(page)
	}), nil
}
