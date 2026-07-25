// This file (identity.go) closes the H4 audit finding this batch
// (fix/audit-github-actor-rbac) fixes: GitHub ingress was the only ingress
// surface Step 39's ("identities + full RBAC", §13.2/§13.3) own RBAC work
// never gated -- it did zero actor resolution and never called domain/
// authz.Authorize before creating sessions/turns, even though Slack and
// Linear ingress already enforce exactly that (internal/adapters/inbound/
// {slack,linear}'s own identity.go, both now delegating to internal/app/
// actorauthz).
//
// resolveCommenterActor below is deliberately NOT a port of Slack's
// resolveSlackActor / Linear's resolveActor: those both run a real
// auto-linking ALGORITHM (internal/app/identitylink.Resolve -- fetch a
// profile email from the provider, match it against known users/verified
// identities, decide, maybe mint a magic-link prompt) because a Slack or
// Linear user id has no OTHER way to already be known to this codebase.
// A GitHub user id is different: every real Narvi user already has a
// "github" identities row from signing in via GitHub OAuth (Step 20,
// internal/adapters/inbound/auth/callback.go's own
// externalID := strconv.FormatInt(ghUser.ID, 10) /
// identities.GetByProviderAndExternalID(..., IdentityProviderGithub, ...)
// -- the EXACT same (provider, external_id) key this file's own lookup
// uses). So there is nothing to auto-link here at all: either this exact
// GitHub user id already has a linked Narvi account (this file finds it
// with one direct lookup), or it doesn't, and this batch's own explicit
// scope (see the finding this batch fixes) is to fall back to today's
// existing bot-attributed behavior for that case, unchanged -- hardening
// THAT posture (for Slack/Linear too) is a deliberately separate, later
// batch, not this one.

package github

import (
	"context"
	"errors"
	"log/slog"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// authzSurface is this package's own "surface" label passed to every
// actorauthz.AuthorizeResolvedActor call (coalesce.go) -- mirrors Slack's/
// Linear's own identical constant exactly (see either package's own
// identity.go doc comment for why).
const authzSurface = "github"

// resolveCommenterActor looks up commenterID (a GitHub user id,
// mention.CommenterID, payload.go) directly against the identities table
// (provider=github) -- a plain, direct lookup, never an auto-linking
// algorithm; see this file's own top doc comment for why GitHub does not
// need one.
//
// Returns an invalid pgtype.UUID (Valid == false) for every case that
// isn't a clean, direct match: commenterID == 0 (defensive -- a real
// GitHub delivery's own comment.user.id is never actually zero, but
// payload.go does not assume that), no identities row at all (pgx.
// ErrNoRows -- this commenter has never signed into Narvi), or a lookup
// failure (logged, then treated exactly like "no match" -- a transient
// Postgres error here must never turn into a hard failure of the whole
// webhook delivery, mirroring Slack's/Linear's own resolveActor "any
// failure means bot attribution" precedent). Every one of these returns
// is indistinguishable to CreateOrJoin's own caller (coalesce.go): bot
// attribution, exactly today's existing behavior for an unresolved actor.
func resolveCommenterActor(ctx context.Context, logger *slog.Logger, identities *postgres.IdentityStore, commenterID int64) pgtype.UUID {
	if commenterID == 0 {
		return pgtype.UUID{}
	}

	externalID := strconv.FormatInt(commenterID, 10)
	identity, err := identities.GetByProviderAndExternalID(ctx, sqlcgen.IdentityProviderGithub, externalID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			logger.Warn("github: look up commenter identity failed", "error", err, "commenter_id", commenterID)
		}
		return pgtype.UUID{}
	}
	return identity.UserID
}
