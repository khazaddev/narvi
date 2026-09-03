// This file (identity.go) closes the H4 audit finding this batch
// (fix/audit-github-actor-rbac) fixes: GitHub ingress was the only ingress
// surface §13.2's ("identities + full RBAC", §13.2/§13.3) own RBAC work
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
// "github" identities row from signing in via GitHub OAuth (§13.1,
// internal/adapters/inbound/auth/callback.go's own
// externalID := strconv.FormatInt(ghUser.ID, 10) /
// identities.GetByProviderAndExternalID(..., IdentityProviderGithub, ...)
// -- the EXACT same (provider, external_id) key this file's own lookup
// uses). So there is nothing to auto-link here at all: either this exact
// GitHub user id already has a linked Narvi account (this file finds it
// with one direct lookup), or it doesn't.
//
// What happens to that second case (no linked account found) has since
// CHANGED: batch fix/deny-unlinked-github-actors' own caller-side change
// (coalesce.go now gating both its create/prompt checks through
// actorauthz.AuthorizeLinkedActor, not AuthorizeResolvedActor) means an
// invalid return from resolveCommenterActor below is no longer bot-
// attributed allow-and-proceed -- it is now DENIED, with an actionable
// "please sign in" reply posted back to the PR thread in its place (see
// coalesce.go's own updated doc comment and actornotauthorizedreply.go).
//
// That reversal is exactly why this file's resolution logic COULD NOT
// stay untouched: a confirmed audit finding on an earlier version of this
// batch traced that resolveCommenterActor still returned the SAME invalid
// pgtype.UUID{} for "no identities row at all" (genuinely never linked)
// AND for "the identities lookup itself failed" (a transient Postgres
// error, unrelated to link state) -- indistinguishable to every caller.
// Under the OLD AuthorizeResolvedActor-based allow-and-proceed behavior
// that conflation was harmless (either case simply meant "proceed under
// bot attribution"). Under THIS batch's own DENY-and-reply behavior it is
// not: an already-linked, fully-authorized commenter (an owner, say)
// hitting a transient DB blip would be permanently denied and publicly,
// FALSELY told "I don't recognize your GitHub account in Narvi yet" --
// wrong on both counts, since the real cause had nothing to do with link
// state. resolveCommenterActor below now returns (pgtype.UUID, error)
// specifically to let handler.go tell the two apart: err == nil (actor may
// be Valid or not -- a genuine, resolved verdict about link state) versus
// err != nil (the lookup itself failed; handler.go treats this exactly
// like any other backend failure -- release the webhook delivery claim
// for a real retry, log it, never post the sign-in reply -- see that
// file's own call site doc comment).

package github

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// authzSurface is this package's own "surface" label passed to every
// actorauthz.AuthorizeLinkedActor call (coalesce.go) -- mirrors Slack's/
// Linear's own identical constant exactly (see either package's own
// identity.go doc comment for why).
const authzSurface = "github"

// CommenterIdentityLookup is the narrow slice of
// *postgres.IdentityStore's own real surface (GetByProviderAndExternalID)
// resolveCommenterActor needs -- mirrors CommentPoster's/
// PullRequestResolver's own identical narrow-interface precedent
// (planawaitingreply.go/headresolve.go): a small, locally-defined
// interface so a unit test can inject a fake that forces a genuine,
// non-ErrNoRows lookup failure with no real Postgres round trip.
// *postgres.IdentityStore satisfies this directly, with no adapter-side
// change needed.
type CommenterIdentityLookup interface {
	GetByProviderAndExternalID(ctx context.Context, provider sqlcgen.IdentityProvider, externalID string) (sqlcgen.Identity, error)
}

// resolveCommenterActor looks up commenterID (a GitHub user id,
// mention.CommenterID, payload.go) directly against the identities table
// (provider=github) -- a plain, direct lookup, never an auto-linking
// algorithm; see this file's own top doc comment for why GitHub does not
// need one.
//
// Returns (an invalid pgtype.UUID, nil error) for every case that
// genuinely establishes "not linked": commenterID == 0 (defensive -- a
// real GitHub delivery's own comment.user.id is never actually zero, but
// payload.go does not assume that), or no identities row at all (pgx.
// ErrNoRows -- this commenter has genuinely never signed into Narvi). A
// non-nil error return is DISTINCT from both of those, and means the
// identities lookup itself failed for some other reason (a transient
// Postgres error) -- it says NOTHING about whether this commenter is
// linked, and the caller (handler.go) must not treat it as "not linked"
// (see this file's own top doc comment for the audit finding this
// separation closes: the two used to be conflated into the identical
// invalid pgtype.UUID{}, which -- since batch fix/deny-unlinked-github-
// actors switched the caller from allow-and-proceed to deny-and-reply --
// meant a transient DB blip on an already-linked, fully-authorized
// commenter produced a false, permanent "I don't recognize your GitHub
// account" denial).
//
// A resolved-but-invalid return (err == nil, !actor.Valid) is, since
// batch fix/deny-unlinked-github-actors, no longer bot attribution: it is
// now DENIED (actorauthz.AuthorizeLinkedActor), with an actionable reply
// posted back instead (see this file's own top doc comment and
// coalesce.go's own updated doc comment).
func resolveCommenterActor(ctx context.Context, identities CommenterIdentityLookup, commenterID int64) (pgtype.UUID, error) {
	if commenterID == 0 {
		return pgtype.UUID{}, nil
	}

	externalID := strconv.FormatInt(commenterID, 10)
	identity, err := identities.GetByProviderAndExternalID(ctx, sqlcgen.IdentityProviderGithub, externalID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return pgtype.UUID{}, nil
		}
		return pgtype.UUID{}, fmt.Errorf("github: look up commenter identity: %w", err)
	}
	return identity.UserID, nil
}
