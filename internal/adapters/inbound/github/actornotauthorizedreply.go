// This file (actornotauthorizedreply.go) is batch fix/deny-unlinked-
// github-actors' own addition: the repo owner's deliberate decision to
// align GitHub with Slack/Linear (deny a state-changing action from an
// UNLINKED actor outright, never bot attribution -- see coalesce.go's own
// updated doc comment and internal/app/actorauthz/authorize.go's own
// updated AuthorizeLinkedActor doc comment for the full "why" and the
// docs/TECHNICAL_PLAN.md §13.2 line this closes) means a GitHub commenter
// who has simply never signed into Narvi now gets NOTHING when they
// mention the bot -- no session, no turn, no reply -- unless something
// tells them why. Unlike Slack/Linear, GitHub has no magic-link/pending-
// link mechanism to send in parallel (identity.go's own top doc comment,
// actorauthz.AuthorizeLinkedActor's own doc comment): the only actionable
// thing this package can offer is pointing the commenter at the ordinary
// GitHub OAuth sign-in flow every real Narvi user already goes through
// (internal/adapters/inbound/auth), then asking them to repeat their
// mention once linked.
//
// This mirrors planawaitingreply.go's own shape deliberately (same
// CommentPoster interface, same best-effort/never-fail-the-ack posture) --
// see that file's own doc comment for the precedent this follows, rather
// than inventing a second reply mechanism.

package github

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/narvidev/narvi/internal/domain/reposource"
)

// actorNotAuthorizedReplyText renders the honest, actionable reply posted
// back to the PR thread when coalesce.go's CreateOrJoin denies an UNLINKED
// commenter's mention (ErrActorNotAuthorized, actor.Valid == false --
// handler.go's own branch distinguishes this from a LINKED-but-
// insufficiently-privileged denial, which keeps today's silent-200
// behavior unchanged, see handler.go's own doc comment on that branch).
//
// Deliberately honest about what will and will NOT happen (this batch's
// own explicit instruction): it does NOT promise the original mention will
// be replayed once the commenter links their account -- GitHub gives this
// package no mechanism to do that (no deferred/pending state to resolve
// later, unlike Slack/Linear's own magic-link prompt), so claiming
// otherwise would be dishonest. signInURL is publicBaseURL +
// "/auth/github/login" -- the same route cmd/control-plane/main.go mounts
// auth.NewLoginHandler at, and the SAME PublicBaseURL base every other
// externally-reachable URL this codebase constructs uses (identitylink.
// BuildMagicLinkURL's own precedent, internal/app/identitylink/service.go).
func actorNotAuthorizedReplyText(signInURL string) string {
	return "I don't recognize your GitHub account in Narvi yet, so this mention wasn't processed. Sign in once at " +
		signInURL + " to link it, then mention me again here -- this comment won't be replayed automatically."
}

// signInPath is the route auth.NewLoginHandler is mounted at
// (cmd/control-plane/main.go: router.Get("/auth/github/login", ...)) --
// defined here, not re-derived from that inbound package, mirroring
// identitylink.MagicLinkPath's own "defined once, next to its own
// consumer" precedent for the analogous Slack/Linear-side URL.
const signInPath = "/auth/github/login"

// ActorLinkNoticeClaimer is the narrow slice of
// *postgres.GitHubActorLinkNoticeStore's own real surface (Claim) this
// file needs -- mirrors CommentPoster's/PullRequestResolver's own
// identical narrow-interface precedent (planawaitingreply.go/
// headresolve.go): a small, locally-defined interface, not one of
// internal/app/ports' general-purpose abstractions, so a unit test can
// inject a fake that forces a genuine claim failure with no real Postgres
// round trip -- the exact seam a confirmed audit finding on this file
// noted was previously missing (shouldPostActorNotAuthorizedReply's own
// default/error branch had zero test coverage anywhere, because
// *postgres.GitHubActorLinkNoticeStore is a concrete struct, not an
// interface). *postgres.GitHubActorLinkNoticeStore satisfies this
// directly, with no adapter-side change needed.
type ActorLinkNoticeClaimer interface {
	Claim(ctx context.Context, repoFullName string, prNumber int32, commenterID int64, ttl time.Duration) (sqlcgen.ClaimGitHubActorLinkNoticeRow, error)
}

// claimActorNotAuthorizedNotice implements this batch's own anti-spam
// dedupe policy (§1 of the design decision this batch implements):
// atomically decides whether THIS call is the one responsible for posting
// a fresh "please sign in" reply for (repoFullName, prNumber,
// commenterID) right now, AND durably records that decision in the same
// call -- via notices.Claim (postgres.GitHubActorLinkNoticeStore.Claim),
// never a separate "check, then later record" pair.
//
// This deliberately replaces this function's own prior shape (a plain
// notices.Get lookup, with the caller expected to notices.Upsert only
// AFTER a successful PostIssueComment network round trip): a confirmed
// audit finding traced an unsynchronized TOCTOU race through that
// shape -- two concurrent webhook deliveries for the identical (repo, PR,
// commenter) could both observe "no live notice yet" via Get, both then
// spend 100ms+ on their own PostIssueComment call, and both post the
// duplicate reply, since neither had recorded anything via Upsert until
// AFTER the network call. Checking and recording atomically, in the SAME
// statement, BEFORE the network call, closes that window entirely: only
// one concurrent caller can ever win the claim for a given TTL window.
//
// It also closes a second, related finding: the prior shape's own
// "acceptable failure mode" (this batch's own design decision names "one
// duplicate reply per TTL window", never "a reply storm") was not actually
// upheld if Upsert specifically kept failing while Get kept succeeding
// (e.g. a write-scoped Postgres problem) -- Get would return pgx.ErrNoRows
// forever (nothing was ever durably written), re-triggering a fresh reply
// on every single mention, unbounded. With check-and-record merged into
// one atomic statement, there is no longer a separate read path that can
// diverge from the write path's own success/failure: this call either
// succeeds as a whole (claims, post the reply) or fails as a whole (fails
// closed below, logged, no post) -- never "reads succeed forever while
// writes silently vanish".
//
// notices == nil (this package's own handler_test.go, or any other
// minimal wiring that doesn't care about this dedupe) always reports true
// -- exactly like Comments == nil simply skipping the post entirely
// elsewhere in this package, this is the "dedupe unavailable" default, not
// a silent behavior change: without a store to check, there is nothing to
// dedupe against, so every mention gets its own reply (harmless in a test
// that never asserts on posted-comment counts).
//
// A claim failure OTHER than "already claimed within ttl" (pgx.ErrNoRows;
// see ClaimGitHubActorLinkNotice's own doc comment,
// queries/github_actor_link_notices.sql, for why a live-within-TTL row
// surfaces that way) -- i.e. a genuine, transient Postgres error -- fails
// toward NOT posting: logged, then treated like "a live notice already
// exists", rather than risking a spam burst under a flaky DB. See
// actornotauthorizedreply_test.go's own TestClaimActorNotAuthorizedNotice_
// ClaimErrorFailsClosed for direct coverage of exactly this branch (via a
// fakeActorLinkNoticeClaimer -- the seam the concrete store type never
// offered before).
func claimActorNotAuthorizedNotice(ctx context.Context, logger *slog.Logger, notices ActorLinkNoticeClaimer, noticeTTL time.Duration, repoFullName string, prNumber int32, commenterID int64) bool {
	if notices == nil {
		return true
	}

	_, err := notices.Claim(ctx, repoFullName, prNumber, commenterID, noticeTTL)
	switch {
	case err == nil:
		return true
	case errors.Is(err, pgx.ErrNoRows):
		return false
	default:
		logger.Warn("github: claim actor-link-notice failed, skipping reply to avoid a spam risk",
			"error", err, "repo", repoFullName, "pr_number", prNumber, "commenter_id", commenterID)
		return false
	}
}

// postActorNotAuthorizedReply posts actorNotAuthorizedReplyText back to
// repoFullName's prNumber for an UNLINKED commenter's denied mention --
// called ONLY after claimActorNotAuthorizedNotice above has already
// atomically claimed the right to do so (that call itself already
// recorded the notice; there is nothing left for this function to record
// afterward, unlike this function's own prior shape). Best-effort,
// exactly like postPlanAwaitingReply (planawaitingreply.go): poster ==
// nil or a failed post are only logged, never turning this
// already-acknowledged denial into an error/retry -- retrying a genuine
// GitHub redelivery of the SAME denied comment would only ever reproduce
// this exact outcome again (handler.go's own ErrActorNotAuthorized branch
// already does not release the delivery claim for exactly this reason).
//
// Trade-off, named explicitly: because the notice is now recorded BEFORE
// the network call rather than after it, a PostIssueComment failure here
// means this commenter gets NO reply at all for the rest of this TTL
// window (the claim already happened, so a later mention within the TTL
// will see "already claimed" and stay silent too) -- rather than the
// previous shape's own risk of an UNBOUNDED reply storm if the write path
// stayed broken while the read path kept succeeding. Silently missing at
// most one reply per TTL window is the accepted, bounded failure mode;
// an unbounded spam storm was not.
func postActorNotAuthorizedReply(ctx context.Context, logger *slog.Logger, poster CommentPoster, publicBaseURL, botToken, repoFullName string, prNumber int32, commenterID int64) {
	if poster == nil {
		return
	}
	owner, repo, ok := reposource.SplitFullName(repoFullName)
	if !ok {
		logger.Warn("github: could not split repo_full_name into owner/repo, skipping actor-not-authorized reply",
			"repo_full_name", repoFullName, "pr_number", prNumber)
		return
	}

	body := actorNotAuthorizedReplyText(publicBaseURL + signInPath)
	if err := poster.PostIssueComment(ctx, owner, repo, int(prNumber), botToken, body); err != nil {
		logger.Warn("github: post actor-not-authorized reply failed", "error", err, "repo", repoFullName, "pr_number", prNumber, "commenter_id", commenterID)
		return
	}
}
