// This file (githubtoken.go) holds decryptCreatorGitHubToken -- extracted
// out of pushpr.go (Step 21, "e2e happy path", design decision 8) so it
// reads as a genuinely shared Actor-level helper rather than something
// pushpr-specific: pushpr.go's own createPRBestEffort and dispatch.go's own
// resolveAndSetImage (Step 26, "image builds") both need the SAME "decrypt
// this session's creator's stored GitHub OAuth access token" logic, and
// duplicating it verbatim in two places was never the right call once a
// second caller existed. No behavior change from pushpr.go's own original
// version -- only the signature narrows from the whole sqlcgen.Session row
// down to just the one field (CreatedBy) this helper ever actually reads.

package sessionactor

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/platform"
)

// decryptCreatorGitHubToken mirrors internal/adapters/inbound/httpapi's own
// scmcredentials.go ScmCredentials handler outcome table exactly (design
// decision 8) -- the SAME "no created_by user, or no github identity, or
// no stored token, or a decrypt failure -> no usable credential" class of
// absence, just logged rather than turned into an HTTP response, since
// every caller of this helper is an internal, best-effort side effect, not
// a request awaiting a status code. This is the honest "no bot/service-
// account fallback exists" gap named in Step 21's own brief (§8.11's
// fallback half), not a bug to work around by inventing one -- and, as of
// Step 26, ALSO the documented reason a session whose creator has no
// usable GitHub token still spawns successfully on the base image, never
// blocked or failed by image resolution (§10 Phase 2: "never block a
// session").
//
// createdBy is sessionRow.CreatedBy -- callers pass just this one field
// (not the whole session row) since it's the only one this helper reads.
func (a *Actor) decryptCreatorGitHubToken(ctx context.Context, createdBy pgtype.UUID) (string, bool) {
	if !createdBy.Valid {
		a.logger.Warn("sessionactor: session has no created_by user; no bot fallback exists (§8.11); skipping")
		return "", false
	}

	identity, err := a.stores.identity.GetByUserAndProvider(ctx, createdBy, sqlcgen.IdentityProviderGithub)
	if err != nil {
		a.logger.Warn("sessionactor: no usable github identity; skipping", "error", err)
		return "", false
	}
	if identity.AccessTokenEncrypted == nil {
		a.logger.Warn("sessionactor: github identity has no stored access token; skipping")
		return "", false
	}

	plaintext, err := platform.DecryptToken(a.tokenEncryptionKey, identity.AccessTokenEncrypted)
	if err != nil {
		// The decrypt error itself is safe to log (it never contains the
		// ciphertext/plaintext, see platform.DecryptToken's own doc
		// comment) -- the plaintext token it would have produced is NEVER
		// logged, here or anywhere else.
		a.logger.Error("sessionactor: decrypt access token failed", "error", err)
		return "", false
	}
	return string(plaintext), true
}

// creatorMayGetPRAttribution is Step 39's own viewer guard (§13.3:
// "viewers never gain PR-reviewer attribution or git identity on session
// artifacts"), called by pushpr.go's createPRBestEffort BEFORE it ever
// decrypts/uses createdBy's own GitHub token to open a pull request --
// this is a SECOND, defense-in-depth check, distinct from (and in
// addition to) domain/authz.Authorize already refusing a viewer at
// session-CREATION time (httpapi.CreateSession, §13.3 row 2: "... on
// own/joined sessions: admin, maintainer, member" -- never viewer). That
// create-time check alone is not sufficient here: a session's creator's
// role AND disabled state are read FRESH, right here, at PR-creation
// time -- which can be long after creation -- so a user demoted to
// viewer, or disabled outright, AFTER creating a session (an admin's own
// role edit or disable, once Step 39's own "members API" half lands)
// must still never get PR attribution for a session they created back
// when they were neither. A missing user row (should be unreachable --
// created_by is a real FK) is treated the SAME as "no created_by at
// all": no attribution, nothing to fail loudly over, since
// decryptCreatorGitHubToken's own subsequent lookup would fail identically
// anyway.
//
// The disabled check mirrors this Step's own SECOND fix-pass finding
// applied everywhere else a resolved actor's authority is re-verified
// (internal/adapters/inbound/slack/identity.go's authorizeResolvedActor,
// linear/identity.go's twin, auth/middleware.go's Authenticate): Disabled
// and Role are independent columns (migrations/000002_users.up.sql), so
// checking Role alone lets a disabled (but non-viewer) creator's session
// still use their stored GitHub token for PR creation/attribution even
// though every other ingress path already refuses that same disabled
// user outright. This closes that gap for the sandbox's own
// push_complete-triggered PR creation too.
func (a *Actor) creatorMayGetPRAttribution(ctx context.Context, createdBy pgtype.UUID) bool {
	if !createdBy.Valid {
		return false
	}

	creator, err := a.stores.user.GetByID(ctx, createdBy)
	if err != nil {
		a.logger.Warn("sessionactor: get session creator for viewer guard failed; skipping PR creation", "error", err)
		return false
	}
	if creator.Disabled {
		a.logger.Warn("sessionactor: session creator is now disabled; refusing PR attribution (§13.3 viewer guard)", "user_id", createdBy.String())
		return false
	}
	if creator.Role == sqlcgen.UserRoleViewer {
		a.logger.Warn("sessionactor: session creator is now a viewer; refusing PR attribution (§13.3 viewer guard)", "user_id", createdBy.String())
		return false
	}
	return true
}
