package identitylink

import (
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/adapters/inbound/auth"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/app/identitylink"
	"github.com/khazaddev/narvi/internal/platform"
)

// githubLoginPath is the fixed path this handler redirects an
// unauthenticated visitor to (with its own ?next= carrying them back here
// afterward, auth.NewLoginHandler's own Step 39 addition) -- must match
// cmd/control-plane/main.go's own literal route registration
// ("/auth/github/login") exactly; no exported constant exists for it in
// internal/adapters/inbound/auth (that package's own route strings are
// only ever literals in main.go today, never exported for a second
// package to reference).
const githubLoginPath = "/auth/github/login"

// Deps bundles every dependency NewConsumeHandler needs. UserSessions/
// Users back auth.Authenticate's own "is this visitor signed in" check;
// AppIdentityLink is the app-layer Deps internal/app/identitylink.Consume
// itself needs (Pool/Users/Identities/LinkPrompts/AuditLog/...) -- kept as
// its own field (rather than flattening its members into this struct)
// since it is passed straight through to Consume unchanged, mirroring how
// internal/adapters/inbound/{slack,linear} each carry an
// identitylink.Deps field of their own for the SAME reason.
type Deps struct {
	UserSessions    *postgres.UserSessionStore
	Users           *postgres.UserStore
	AppIdentityLink identitylink.Deps
}

// NewConsumeHandler backs GET /auth/identity-link/{nonce} -- see this
// package's own doc.go for the complete design: redirect through the
// existing GitHub OAuth login flow first when the visitor isn't signed in
// yet, otherwise call internal/app/identitylink.Consume and render its
// outcome as a small, honest HTML page.
func NewConsumeHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		logger := platform.Logger(ctx)

		nonce := chi.URLParam(r, "nonce")
		if nonce == "" {
			writeOutcomePage(w, http.StatusBadRequest, "Missing link", "This link is missing its own token and can't be used.")
			return
		}

		authUser, ok := auth.Authenticate(ctx, deps.UserSessions, deps.Users, r)
		if !ok {
			// r.URL.Path alone (never RequestURI/the query string) is
			// exactly the "/auth/identity-link/{nonce}" shape
			// isSafeRedirectNext (internal/adapters/inbound/auth/
			// login.go) expects, and carries everything this handler
			// itself needs to run again after login -- the nonce is
			// already IN the path, there is no separate query param on
			// this route to preserve.
			next := r.URL.Path
			http.Redirect(w, r, githubLoginPath+"?next="+url.QueryEscape(next), http.StatusFound)
			return
		}

		var userID pgtype.UUID
		if err := userID.Scan(authUser.ID); err != nil {
			logger.Error("identitylink: parse authenticated user id failed", "error", err)
			writeOutcomePage(w, http.StatusInternalServerError, "Something went wrong", "Please try again.")
			return
		}

		_, err := identitylink.Consume(ctx, deps.AppIdentityLink, nonce, userID)
		switch {
		case err == nil:
			writeOutcomePage(w, http.StatusOK, "Account connected",
				"Your Narvi account is now connected. You can close this tab and go back to Slack or Linear.")
		case errors.Is(err, identitylink.ErrLinkPromptNotFound):
			writeOutcomePage(w, http.StatusNotFound, "Link not found",
				"This link is invalid or has already been used.")
		case errors.Is(err, identitylink.ErrLinkPromptExpired):
			writeOutcomePage(w, http.StatusGone, "Link expired",
				"This link has expired. Mention Narvi again to get a new one.")
		case errors.Is(err, identitylink.ErrIdentityAlreadyLinked):
			writeOutcomePage(w, http.StatusConflict, "Already connected",
				"This identity is already connected to a Narvi account.")
		default:
			logger.Error("identitylink: consume failed", "error", err)
			writeOutcomePage(w, http.StatusInternalServerError, "Something went wrong", "Please try again.")
		}
	}
}

// writeOutcomePage renders a minimal, dependency-free HTML page -- see
// this package's own doc.go for why (no template engine, no UI framework
// exists yet outside the Phase 6/7 mockups). title/body are HTML-escaped
// (html.EscapeString) even though every call site today passes a fixed,
// hardcoded string -- never string-built from anything request-derived --
// defensive against a future call site doing so without having to
// remember to escape it there.
func writeOutcomePage(w http.ResponseWriter, status int, title, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><title>%s</title></head><body><h1>%s</h1><p>%s</p></body></html>`,
		html.EscapeString(title), html.EscapeString(title), html.EscapeString(body))
}
