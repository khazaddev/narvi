// This file (authcontext.go) implements the request-scoped identity
// §13.1 ("auth v1") introduces: the AuthenticatedUser value internal/adapters/
// inbound/auth's own Middleware attaches to a request's context once a
// user-session cookie has been verified (§13.1, §13.4 "route middleware").
//
// Deliberately named "User"/"Auth", never "Session" alone: correlation.go's
// own WithSessionID (this same package) already means "the domain
// coding-agent session id" (§2/§3's own session concept) — reusing
// "session" here for the human/browser login session this Step introduces
// would create a second, confusingly-different meaning for the same word
// in the same context-key style. See correlation.go's own top comment,
// which already anticipates this distinction.

package platform

import "context"

// AuthenticatedUser is the plain, adapter-independent value carried on a
// request's context after internal/adapters/inbound/auth's own Middleware
// has verified a user-session cookie. ID is a STRING, matching
// WithSessionID's own existing convention of storing UUIDs as strings in
// context — re-parsed via pgtype.UUID.Scan at the point of use, exactly
// like internal/adapters/inbound/wshub's sandbox.go/client.go already do
// for the URL's own sessionID (see internal/adapters/inbound/httpapi's
// create.go/wstoken.go for this Step's own call sites).
type AuthenticatedUser struct {
	ID    string
	Role  string
	Email string
}

// userKey is the unexported context-key type for AuthenticatedUser,
// mirroring correlationIDKey/sessionIDKey/sandboxGenKey (correlation.go)
// exactly — never a bare string or other exported type as a context key.
type userKey struct{}

// WithUser returns a copy of ctx carrying u as the authenticated user.
func WithUser(ctx context.Context, u AuthenticatedUser) context.Context {
	return context.WithValue(ctx, userKey{}, u)
}

// UserFromContext returns the AuthenticatedUser stored in ctx (by
// internal/adapters/inbound/auth's own Middleware, or WithUser directly in
// a test), and whether one was present.
func UserFromContext(ctx context.Context) (AuthenticatedUser, bool) {
	u, ok := ctx.Value(userKey{}).(AuthenticatedUser)
	return u, ok
}
