package controlplane

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
)

// Routes returns every "METHOD /path" a.Router actually serves, sorted --
// TestBuild_RouteTableMatchesGolden's own subject (testdata/routes.golden).
// Unlike internal/ops.ScanRegisteredRoutes (a static, go/ast-based scan of
// source), this walks the REAL, live router chi.Walk traverses, so it
// reports exactly what Build wired for THIS process, including anything a
// future module hook mounts.
//
// chi.Walk itself reports a route group's own root handler -- every
// `router.Route("/api/members", func(r chi.Router) { r.Get("/", h) })`
// registers, which is how nearly every list/create endpoint in this
// binary is written -- as "/api/members/", WITH a trailing slash: that is
// chi's own internal pattern string for a mounted sub-router's "/" leaf,
// not a second, distinct route (a live request to "/api/members", no
// trailing slash, reaches the exact same handler; confirmed empirically
// against this exact shape while writing this function -- chi never
// redirects or 404s either form). internal/ops.ScanRegisteredRoutes's own
// joinRoutePath already normalizes this same shape away for exactly this
// reason (its own doc comment: "prefix + sub-path "/" becomes exactly
// prefix, never prefix+"/""), so routes.golden (generated through that
// scanner) never contains a trailing slash. trimGroupRootSlash below
// applies chi.Walk's OWN reported paths that SAME normalization, so this
// function's output is comparable to that golden in the first place --
// without it, EVERY group-root route in this binary would spuriously
// "differ" on representation alone, never on anything Build actually
// wired differently.
func (a *App) Routes() []string {
	var routes []string
	// walkFn below never returns a non-nil error, so chi.Walk's own return
	// here is always nil (see its doc comment: "the error returned from
	// walkFn"); checked anyway rather than silently discarded.
	err := chi.Walk(a.Router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		routes = append(routes, method+" "+trimGroupRootSlash(route))
		return nil
	})
	if err != nil {
		panic(fmt.Sprintf("controlplane: chi.Walk reported an error even though its own walkFn never returns one: %v", err))
	}
	sort.Strings(routes)
	return routes
}

// trimGroupRootSlash strips route's own trailing "/", unless route IS "/"
// -- see Routes's own doc comment for why. Mirrors internal/ops/routes.go's
// joinRoutePath's identical rule exactly, applied to chi.Walk's already-
// joined path instead of that scanner's own prefix+sub-path pair.
func trimGroupRootSlash(route string) string {
	if route == "/" {
		return route
	}
	return strings.TrimSuffix(route, "/")
}
