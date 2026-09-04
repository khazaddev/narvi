// This file (cloudidentitycapability.go) implements the ONE, group-level
// fail-closed gate every browser-facing cloud-identity management route
// group shares: "the whole capability is off (and binding CRUD refuses,
// fail-closed) when unset" (§27.3, verbatim; repeated at the §27.3 row:
// "capability off and binding CRUD refusing, fail-closed, when unset").
//
// The 4 sandbox/public-facing cloud-identity surfaces (oidcdiscovery.go's
// OIDCDiscovery/OIDCJWKS, cloudidentitytoken.go's MintCloudIdentityToken)
// each already carry their own inline `if issuerURL == "" { 503 }` check,
// because each is a single free-function handler with no shared route
// group to hang a middleware off of. The two BROWSER-facing management
// route groups (cloud-identity-bindings' environment- and global-scoped
// groups, cloudidentitybindings.go) are different: they are each already
// a chi.Router group mounted behind auth.Middleware via r.Use(...), so a
// SECOND r.Use(...) closes the gate for the whole group at once, exactly
// once, rather than remembering to add a fourth/fifth/sixth copy of the
// same inline check to every handler that group ever gains. This was the
// actual defect an adversarial review caught: cloudidentitybindings.go's
// four shared handler cores (create/list/update/delete) had NO issuerURL
// parameter at all, and main.go mounted both binding route groups behind
// auth.Middleware alone -- binding CRUD stayed fail-OPEN (writable) the
// entire time the capability was off, contradicting §27.3's own explicit
// requirement and platform.Config.CloudIdentityIssuerURL's own doc
// comment (which asserted, incorrectly, that "every one of those surfaces
// checks this field directly").
//
// Deliberately a 503 status via the SAME message the 4 sibling surfaces
// already use, not a 404 from conditional route registration: this
// package's own established precedent (oidcdiscovery.go's doc comment)
// is that a capability that is off must be OBSERVABLE as off, never
// indistinguishable from a route that was never built at all -- 404 would
// look identical to "this deployment's binary predates cloud identity
// entirely", where 503 unambiguously says "this deployment KNOWS about
// cloud identity, and it's turned off".
//
// RequireCloudIdentityCapability also fits the admin-only signing-keys
// rotation route group (cloudidentitykeys.go) -- see
// RotateCloudIdentitySigningKey's own doc comment for why it too refuses
// when the capability is off. The controlplane package applies this
// middleware to all three groups uniformly; RotateCloudIdentitySigningKey
// itself keeps NO redundant inline check once this middleware covers its
// own route group, so there is exactly one place per request path that
// decides "is cloud identity on". Exported (unlike most of this package's
// own handler-internal helpers) because controlplane, not
// this package, owns route-group construction (r.Use(...)) -- mirrors
// auth.Middleware's own identical cross-package export shape.

package httpapi

import "net/http"

// RequireCloudIdentityCapability returns chi middleware that responds 503
// (fail-closed, matching every other cloud-identity surface's own
// message) for the ENTIRE route group it wraps when issuerURL is empty --
// the SAME "unset means off" test platform.Config.CloudIdentityIssuerURL's
// own doc comment already documents, applied once per group instead of
// once per handler. Any handler added to a group wrapped with this
// middleware inherits the gate automatically; there is nothing for a
// future PR to remember to add.
func RequireCloudIdentityCapability(issuerURL string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if issuerURL == "" {
				writeError(w, http.StatusServiceUnavailable, "cloud identity federation is not configured")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
