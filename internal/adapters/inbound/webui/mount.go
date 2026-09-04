package webui

import (
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/go-chi/chi/v5"
)

// protectedPrefixes are the 6 top-level route groups the controlplane
// package registers today (confirmed against every router.<Method>/
// router.Route call in that package at the time this was written) --
// enumerated here ONLY as a second, independent guard on top of Mount's
// real structural protection (r.NotFound, doc.go's own top comment), never
// as the sole mechanism: isProtectedPath's whole job is to keep answering
// "not the SPA" for these paths even on a request that names one of them
// but matches no currently-registered route (a typo, a route not yet
// added, one removed by a future refactor) -- see mount_test.go's own
// TestMount_ProtectedPrefixUnregisteredSubpath.
var protectedPrefixes = []string{
	"/.well-known/",
	"/api/",
	"/auth/",
	"/sessions/",
	"/webhooks/",
}

// isProtectedPath also treats "/health" and any "/health/..." path as
// protected, even though controlplane registers no wildcard sibling route under
// it today -- a health-check consumer (infra/monitoring) probing anything
// shaped like a health path must see a real 404 on a miss, never a 200 SPA
// shell that could read as "healthy".
func isProtectedPath(p string) bool {
	if p == "/health" || strings.HasPrefix(p, "/health/") {
		return true
	}
	for _, prefix := range protectedPrefixes {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

// placeholderHTML is served, with a 503, for every SPA route when assets is
// nil (the frontend was never built into this binary -- assets_stub.go's
// own default). It exists so a dev checkout that skipped `make web-build`
// gets an honest, readable message at "/" instead of a bare 404 or a panic.
const placeholderHTML = `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>Narvi</title></head>
<body style="font-family:system-ui,sans-serif;max-width:40em;margin:4em auto;padding:0 1em">
<h1>Frontend not built</h1>
<p>This control-plane binary was built without the web UI's static assets.
Run <code>make web-build</code>, then rebuild with
<code>go build -tags web_assets ./cmd/control-plane</code>.</p>
</body>
</html>
`

// Mount wires the web UI's SPA fallback onto r (§12.1: "narvi serve serves
// API + WS + UI on one port"). assets is the built frontend's root
// directory (index.html alongside the hashed asset files Vite emits under
// assets/) -- pass webui.DistFS in production. assets may be nil, meaning
// "not built into this binary" (assets_stub.go's default): every SPA
// route then serves placeholderHTML instead of real content, and protected
// paths still 404 exactly as they would with real assets.
//
// Mount is deliberately generic over assets (any fs.FS, or nil) rather than
// reaching for the package-level DistFS itself, so it is testable with an
// in-memory fstest.MapFS -- see mount_test.go, which is also where this
// function's own no-shadowing guarantee is pinned against every top-level
// route group controlplane actually registers.
func Mount(r chi.Router, assets fs.FS) {
	r.NotFound(func(w http.ResponseWriter, req *http.Request) {
		if isProtectedPath(req.URL.Path) {
			http.NotFound(w, req)
			return
		}
		// A non-GET/HEAD request to an unmatched path is never a real SPA
		// navigation -- refuse it with a real 404 instead of 200-ing an
		// HTML shell at whatever client sent it (e.g. a stray POST to a
		// typo'd API path should not silently succeed against the SPA).
		if req.Method != http.MethodGet && req.Method != http.MethodHead {
			http.NotFound(w, req)
			return
		}

		if assets == nil {
			servePlaceholder(w)
			return
		}
		serveSPA(w, req, assets)
	})
}

func serveSPA(w http.ResponseWriter, req *http.Request, assets fs.FS) {
	if servableFile(assets, req.URL.Path) {
		http.FileServer(http.FS(assets)).ServeHTTP(w, req)
		return
	}
	serveIndex(w, assets)
}

// servableFile reports whether urlPath names a real, regular file under
// assets -- e.g. /assets/index-abc123.js, /vite.svg. Anything else
// (including "/", any directory, and any client-side route path like
// /settings that the SPA's own router resolves) is not a built asset, and
// falls back to index.html instead.
func servableFile(assets fs.FS, urlPath string) bool {
	p := strings.TrimPrefix(path.Clean(urlPath), "/")
	if p == "" || p == "." {
		return false
	}
	info, err := fs.Stat(assets, p)
	if err != nil || info.IsDir() {
		return false
	}
	return true
}

func serveIndex(w http.ResponseWriter, assets fs.FS) {
	data, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		// Built with -tags web_assets but dist/ was somehow incomplete
		// (index.html missing) -- treat identically to "not built" rather
		// than a 500, since the user-facing remedy is the same either way.
		servePlaceholder(w)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// The SPA shell's own hashed asset references change every build --
	// never let a browser/CDN cache this past one response.
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func servePlaceholder(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte(placeholderHTML))
}
