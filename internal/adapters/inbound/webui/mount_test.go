package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/go-chi/chi/v5"
)

// stubBody marks a response as having come from one of the REAL,
// non-SPA handlers below, so a test assertion can tell "reached the real
// route" apart from "fell through to the SPA" unambiguously.
const stubBody = "real-handler:"

// newProtectedRouter builds a chi.Router registering literally the SAME
// top-level path shapes the controlplane package registers today for each
// of the 6 route groups doc.go/mount.go's protectedPrefixes describe --
// one representative route per group, with a stub handler standing in for
// the real one, plus the group's own name in the response body. If
// controlplane's own route wiring for one of these groups is ever renamed
// or restructured, THIS function is the one place that needs updating to
// match -- see this file's own package comment for why a synthetic router
// (rather than spinning up controlplane's real one, which needs a
// live Postgres pool and a fully loaded platform.Config) is what proves
// Mount's own contract instead.
func newProtectedRouter(t *testing.T) chi.Router {
	t.Helper()
	r := chi.NewRouter()

	stub := func(name string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(stubBody + name))
		}
	}

	// health
	r.Get("/health", stub("health"))
	// .well-known/*
	r.Get("/.well-known/openid-configuration", stub("oidc-discovery"))
	r.Get("/.well-known/jwks.json", stub("oidc-jwks"))
	// sessions/*
	r.Get("/sessions/{sessionID}/ws", stub("sessions-ws"))
	r.Post("/sessions/{sessionID}/uploads", stub("sessions-uploads"))
	// webhooks/*
	r.Post("/webhooks/slack", stub("webhooks-slack"))
	r.Post("/webhooks/github", stub("webhooks-github"))
	// auth/*
	r.Get("/auth/github/login", stub("auth-login"))
	r.Post("/auth/logout", stub("auth-logout"))
	// api/*
	r.Route("/api/members", func(r chi.Router) {
		r.Get("/", stub("api-members"))
	})

	return r
}

// testAssets is a minimal in-memory build output (fstest.MapFS -- the
// standard library's own fs.FS test double, exactly its documented use) so
// this test exercises Mount's REAL asset-serving/index-fallback logic
// without depending on `make web-build` having ever run.
func testAssets() fstest.MapFS {
	return fstest.MapFS{
		"index.html":            {Data: []byte("<html>spa-shell</html>")},
		"assets/app-abc123.js":  {Data: []byte("console.log('spa')")},
		"assets/app-abc123.css": {Data: []byte("body{color:red}")},
	}
}

// TestMount_DoesNotShadowRegisteredRoutes is this Step's own required
// proof: with the SPA mounted, a request to every existing top-level route
// group still reaches ITS OWN real handler, never the SPA. See this file's
// own package comment on newProtectedRouter for what "real handler" means
// here, and mount.go's doc.go sibling for why r.NotFound is what makes this
// true structurally (chi never invokes a NotFound handler once some other
// route has matched) rather than as an artifact of registration order --
// TestMount_OrderIndependent below pins that second property directly.
func TestMount_DoesNotShadowRegisteredRoutes(t *testing.T) {
	r := newProtectedRouter(t)
	Mount(r, testAssets())

	cases := []struct {
		method string
		path   string
		want   string
	}{
		{http.MethodGet, "/health", stubBody + "health"},
		{http.MethodGet, "/.well-known/openid-configuration", stubBody + "oidc-discovery"},
		{http.MethodGet, "/.well-known/jwks.json", stubBody + "oidc-jwks"},
		{http.MethodGet, "/sessions/abc-123/ws", stubBody + "sessions-ws"},
		{http.MethodPost, "/sessions/abc-123/uploads", stubBody + "sessions-uploads"},
		{http.MethodPost, "/webhooks/slack", stubBody + "webhooks-slack"},
		{http.MethodPost, "/webhooks/github", stubBody + "webhooks-github"},
		{http.MethodGet, "/auth/github/login", stubBody + "auth-login"},
		{http.MethodPost, "/auth/logout", stubBody + "auth-logout"},
		{http.MethodGet, "/api/members", stubBody + "api-members"},
	}

	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
			}
			if got := rec.Body.String(); got != tc.want {
				t.Errorf("body = %q, want %q -- the SPA fallback shadowed the real route", got, tc.want)
			}
		})
	}
}

// TestMount_ProtectedPrefixUnregisteredSubpath pins isProtectedPath's own
// belt-and-suspenders role (mount.go's doc comment): a path SHAPED like one
// of the 6 protected groups but matching no real registered route must
// still 404, never fall through to a 200 SPA shell -- the failure mode that
// would make a removed/typo'd route silently look like a healthy 200
// response instead of a loud test/ops failure.
func TestMount_ProtectedPrefixUnregisteredSubpath(t *testing.T) {
	r := newProtectedRouter(t)
	Mount(r, testAssets())

	for _, p := range []string{
		"/api/this-route-does-not-exist",
		"/.well-known/some-other-document",
		"/auth/some-unregistered-provider",
		"/sessions/abc-123/some-unregistered-subpath",
		"/webhooks/some-other-integration",
		"/health/sub",
	} {
		t.Run(p, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, p, nil)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if rec.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404 (body %q) -- a protected-shaped path fell through to the SPA", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestMount_SPAFallback covers the actual SPA-serving contract: real assets
// served byte for byte, everything else (a client-side route path, and the
// root) falling back to index.html.
func TestMount_SPAFallback(t *testing.T) {
	r := newProtectedRouter(t)
	Mount(r, testAssets())

	t.Run("root serves index.html", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || rec.Body.String() != "<html>spa-shell</html>" {
			t.Errorf("status=%d body=%q, want 200 <html>spa-shell</html>", rec.Code, rec.Body.String())
		}
	})

	t.Run("client-side route falls back to index.html", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/settings/environments", nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || rec.Body.String() != "<html>spa-shell</html>" {
			t.Errorf("status=%d body=%q, want 200 <html>spa-shell</html>", rec.Code, rec.Body.String())
		}
	})

	t.Run("real built asset is served byte for byte", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/assets/app-abc123.js", nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || rec.Body.String() != "console.log('spa')" {
			t.Errorf("status=%d body=%q, want 200 console.log('spa')", rec.Code, rec.Body.String())
		}
	})

	t.Run("non-GET/HEAD to an unmatched path 404s instead of serving the SPA", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/some/client/route", nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rec.Code)
		}
	})
}

// TestMount_NilAssets pins assets_stub.go's own contract: a nil assets
// fs.FS (the default, no-`web_assets`-tag build) serves the documented
// placeholder rather than panicking or 404ing every route, while protected
// paths are STILL protected -- the guard does not depend on assets being
// non-nil.
func TestMount_NilAssets(t *testing.T) {
	r := newProtectedRouter(t)
	Mount(r, nil)

	t.Run("SPA route serves the placeholder", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want 503", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "Frontend not built") {
			t.Errorf("body = %q, want the placeholder message", rec.Body.String())
		}
	})

	t.Run("protected route is unaffected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || rec.Body.String() != stubBody+"health" {
			t.Errorf("status=%d body=%q, want 200 %s", rec.Code, rec.Body.String(), stubBody+"health")
		}
	})
}

// TestMount_OrderIndependent proves the no-shadowing guarantee does not
// depend on WHEN Mount is called relative to the protected routes'
// registration -- calling it first still leaves every protected route
// reachable, because r.NotFound (mount.go) is a router-level fallback
// hook, not a competing route registration chi's matcher has to arbitrate
// between. This is the direct rebuttal to "ordering-dependent": swap the
// two calls below and, if this test still passes, mounting order was never
// the load-bearing property in the first place.
func TestMount_OrderIndependent(t *testing.T) {
	r := chi.NewRouter()
	Mount(r, testAssets()) // mounted BEFORE the protected routes exist
	r.Get("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(stubBody + "health"))
	})

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != stubBody+"health" {
		t.Errorf("status=%d body=%q, want 200 %s -- Mount() called first still shadowed /health", rec.Code, rec.Body.String(), stubBody+"health")
	}
}
