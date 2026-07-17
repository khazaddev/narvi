// This file (correlation.go) implements the request-scoped identity carried
// through context.Context and (for correlation_id) HTTP headers: the
// correlation_id/session_id/sandbox_gen triple the stack-choices line in §1
// and §5.3 describe as the structured-logging envelope's carried values.
// §5.3: "correlation_id minted at ingress, propagated: webhook → CP →
// provider → sandbox-agent → OpenCode wrapper → back." This PR only builds
// the mechanism (context keys, accessors, the minting middleware); call
// sites that set session_id/sandbox_gen on the context belong to the PRs
// that own those concepts (the session actor, the sandbox state machine).

package platform

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

// CorrelationIDHeader is the HTTP header carrying the correlation id across
// process boundaries (§5.3: "correlation_id minted at ingress, propagated:
// webhook → CP → provider → sandbox-agent → OpenCode wrapper → back").
const CorrelationIDHeader = "X-Correlation-Id"

// Typed, unexported context-key types — one per carried value — so keys
// from other packages can never collide with these (Go idiom: never use a
// bare string or other exported type as a context key).
type (
	correlationIDKey struct{}
	sessionIDKey     struct{}
	sandboxGenKey    struct{}
)

// WithCorrelationID returns a copy of ctx carrying id as the correlation id.
func WithCorrelationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, correlationIDKey{}, id)
}

// CorrelationIDFromContext returns the correlation id stored in ctx (by
// WithCorrelationID or CorrelationIDMiddleware), and whether one was
// present.
func CorrelationIDFromContext(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(correlationIDKey{}).(string)
	return id, ok
}

// WithSessionID returns a copy of ctx carrying id as the session id.
func WithSessionID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, sessionIDKey{}, id)
}

// SessionIDFromContext returns the session id stored in ctx, and whether
// one was present.
func SessionIDFromContext(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(sessionIDKey{}).(string)
	return id, ok
}

// WithSandboxGen returns a copy of ctx carrying gen as the sandbox
// generation (§3.2: "sandbox.gen (monotonic, per session)").
func WithSandboxGen(ctx context.Context, gen int64) context.Context {
	return context.WithValue(ctx, sandboxGenKey{}, gen)
}

// SandboxGenFromContext returns the sandbox generation stored in ctx, and
// whether one was present.
func SandboxGenFromContext(ctx context.Context) (int64, bool) {
	gen, ok := ctx.Value(sandboxGenKey{}).(int64)
	return gen, ok
}

// newCorrelationID mints a fresh correlation id using only the standard
// library (crypto/rand + hex), deliberately avoiding a UUID dependency for
// this one string, per this PR's scope.
func newCorrelationID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

// CorrelationIDMiddleware reads the incoming X-Correlation-Id request
// header; if absent or empty, mints a new one. Either way it stores the id
// in the request's context (retrievable downstream via
// CorrelationIDFromContext), sets it on the response header (so callers or
// logs downstream of a proxy can see what was used), and calls next with
// the enriched request.
func CorrelationIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(CorrelationIDHeader)
		if id == "" {
			minted, err := newCorrelationID()
			if err != nil {
				http.Error(w, "failed to mint correlation id", http.StatusInternalServerError)
				return
			}
			id = minted
		}

		w.Header().Set(CorrelationIDHeader, id)
		ctx := WithCorrelationID(r.Context(), id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
