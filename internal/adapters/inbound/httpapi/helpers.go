package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/platform"
)

// maxRequestBodyBytes bounds every JSON request body this package decodes
// (via http.MaxBytesReader). This is the FIRST Step in this codebase to
// decode a JSON request body at all, so this figure has no existing
// precedent to match -- 1 MiB is a generous, reasonable cap against a
// malicious/broken client's oversized payload, comfortably above any
// legitimate CreateSessionRequest (a handful of repo entries plus a
// prompt string).
const maxRequestBodyBytes = 1 << 20 // 1 MiB

// parseSessionID parses chi's own "sessionID" URL path param as a UUID,
// writing a 400 response and returning ok=false on failure. REST callers
// get a plain, idiomatic Bad Request for a malformed path segment here,
// distinct from the 404 a well-formed-but-nonexistent session id gets
// from whichever store lookup follows -- unlike the client/sandbox WS
// handshakes (internal/adapters/inbound/wshub), which deliberately
// collapse both cases to 404 for reasons specific to those two protocols'
// own reconnect-loop status-code classification (see wshub/sandbox.go's
// own doc comment) -- a plain REST client has no such concern, so the
// ordinary REST convention (400 malformed vs 404 not-found) applies here
// instead.
func parseSessionID(w http.ResponseWriter, r *http.Request) (pgtype.UUID, bool) {
	raw := chi.URLParam(r, "sessionID")
	var id pgtype.UUID
	if err := id.Scan(raw); err != nil {
		writeError(w, http.StatusBadRequest, "malformed session id")
		return pgtype.UUID{}, false
	}
	return id, true
}

// writeError writes a minimal {"error": message} JSON body at status.
func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// writeJSON writes v as a JSON body at status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// authenticatedUserID resolves platform.UserFromContext(r.Context()) into a
// pgtype.UUID -- the real authenticated caller's id, now that every route
// in this package is mounted behind internal/adapters/inbound/auth.
// Middleware (Step 20, "auth v1"; see doc.go's own updated writeup). Writes
// a 500 response (logging the failure server-side) and returns ok=false if
// either the context lookup fails (should never happen for a request
// routed behind that middleware, but defended against anyway rather than
// silently proceeding with a NULL that could have been avoided) or the
// stored id string fails to parse as a UUID.
func authenticatedUserID(w http.ResponseWriter, r *http.Request) (pgtype.UUID, bool) {
	ctx := r.Context()
	logger := platform.Logger(ctx)

	authUser, ok := platform.UserFromContext(ctx)
	if !ok {
		logger.Error("httpapi: no authenticated user in context (route not mounted behind auth.Middleware?)")
		writeError(w, http.StatusInternalServerError, "internal error")
		return pgtype.UUID{}, false
	}

	var id pgtype.UUID
	if err := id.Scan(authUser.ID); err != nil {
		logger.Error("httpapi: parse authenticated user id failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return pgtype.UUID{}, false
	}
	return id, true
}
