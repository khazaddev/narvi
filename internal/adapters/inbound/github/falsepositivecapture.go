// This file implements Step 63's own §22.2 capture command: an explicit
// `false positive: <reason>` PR-thread comment teaches a repo-scoped
// false-positive pattern. Handled DISPATCH-BEFORE-ROUTER, exactly like
// pullrequestevent.go's own `pull_request`/action=="closed" merge-gating
// lane: checked in handler.go BEFORE parseMention ever runs, so a capture
// command never reaches CreateOrJoin/the mention router at all -- it is
// not a review-session trigger, and a maintainer teaching a pattern must
// never accidentally spawn or prompt a review session as a side effect
// (this table is repo-scoped, not PR-scoped, so there is nothing
// PR-review-shaped about this action in the first place).
//
// Authorization reuses domain/authz.Authorize DIRECTLY (§13.3's own
// "every state-changing actor command... calls it identically"), via the
// SAME actorauthz.AuthorizeLinkedActor gate coalesce.go's own
// create/prompt checks already use -- never a parallel permission model
// invented for this one command (§22.2's own explicit instruction).

package github

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/actorauthz"
	"github.com/khazaddev/narvi/internal/app/auditlog"
	"github.com/khazaddev/narvi/internal/domain/authz"
	"github.com/khazaddev/narvi/internal/domain/falsepositive"
)

// FalsePositivePatternCapturer is the narrow slice of
// *postgres.FalsePositivePatternStore this file needs -- a small,
// locally-defined interface (mirroring CommenterIdentityLookup's own
// established precedent, identity.go) so a unit test can inject a fake
// with no real Postgres connection. Deliberately a SEPARATE interface
// from reviewcontext.FalsePositivePatternsFetcher (the advisory-injection
// READ path, Config.FalsePositivePatterns): this is the WRITE path, a
// structurally different capability -- both are satisfied by the SAME
// *postgres.FalsePositivePatternStore instance at production wiring time
// (cmd/control-plane/main.go), never two independently-constructed
// copies.
type FalsePositivePatternCapturer interface {
	Upsert(ctx context.Context, repoFullName string, commentID int64, reason string, createdBy pgtype.UUID) (sqlcgen.ReviewFalsePositivePattern, bool, error)
}

// falsePositiveCandidate is the small, common shape both event-type
// parsers below produce -- everything tryCaptureFalsePositivePattern
// needs, regardless of which event type actually carried the comment.
type falsePositiveCandidate struct {
	RepoFullName   string
	CommentID      int64
	CommenterID    int64
	CommenterLogin string
	Body           string
}

// parseFalsePositiveCandidate mirrors parseIssueComment/
// parsePullRequestReviewComment's own decode-and-gate shape (payload.go)
// exactly, MINUS mention-regex matching (a capture command needs no
// @mention at all -- it is detected purely by falsepositive.Match's own
// deterministic prefix, dispatch-before-router) and minus every
// head-branch/SHA/stack field neither this file nor review_false_positive_
// patterns has any use for (this table is repo-scoped, never PR- or
// commit-scoped). ok=false means "not a real, action=created comment on a
// pull request" -- the caller falls through to the ordinary mention
// pipeline exactly as if this file did not exist.
func parseFalsePositiveCandidate(eventType string, body []byte) (falsePositiveCandidate, bool, error) {
	switch eventType {
	case eventTypeIssueComment:
		var p issueCommentPayload
		if err := json.Unmarshal(body, &p); err != nil {
			return falsePositiveCandidate{}, false, err
		}
		if p.Action != commentActionCreated || p.Issue.PullRequest == nil {
			return falsePositiveCandidate{}, false, nil
		}
		return falsePositiveCandidate{
			RepoFullName:   p.Repository.FullName,
			CommentID:      p.Comment.ID,
			CommenterID:    p.Comment.User.ID,
			CommenterLogin: p.Comment.User.Login,
			Body:           p.Comment.Body,
		}, true, nil
	case eventTypePullRequestReviewComment:
		var p pullRequestReviewCommentPayload
		if err := json.Unmarshal(body, &p); err != nil {
			return falsePositiveCandidate{}, false, err
		}
		if p.Action != commentActionCreated {
			return falsePositiveCandidate{}, false, nil
		}
		return falsePositiveCandidate{
			RepoFullName:   p.Repository.FullName,
			CommentID:      p.Comment.ID,
			CommenterID:    p.Comment.User.ID,
			CommenterLogin: p.Comment.User.Login,
			Body:           p.Comment.Body,
		}, true, nil
	default:
		return falsePositiveCandidate{}, false, nil
	}
}

// falsePositiveOutcome is tryCaptureFalsePositivePattern's own result --
// handler.go branches on this to decide what to acknowledge GitHub with
// and whether to release the webhook-delivery claim (deliveries.Release)
// for a redelivery to retry, mirroring parseMention's own established
// "release on genuine failure, never on a business-rule outcome" split
// (handler.go's own top-level doc comment on that exact distinction).
type falsePositiveOutcome int

const (
	// falsePositiveNotApplicable means this delivery is not a capture
	// command at all (wrong event type/action, not a PR comment, or the
	// comment body doesn't start with falsepositive.Prefix) -- the
	// caller falls through to the ordinary mention/router pipeline
	// completely unaffected, as if this file did not exist.
	falsePositiveNotApplicable falsePositiveOutcome = iota
	// falsePositiveHandled means this delivery WAS a capture command and
	// has been fully acted on (captured, or a business-rule no-op: an
	// unauthorized/unlinked actor, or an empty reason) -- acknowledge 200,
	// never release the claim (a redelivery would only ever reproduce the
	// identical outcome).
	falsePositiveHandled
	// falsePositiveFailed means this delivery WAS a capture command but a
	// genuine backend failure (a lookup or write error) prevented acting
	// on it -- acknowledge with an error status and release the claim, so
	// a human-triggered GitHub redelivery (§L4's own "redelivery is
	// manual-only" precedent) can retry once the backend recovers.
	falsePositiveFailed
)

// tryCaptureFalsePositivePattern is this file's own one entry point,
// called from handler.go BEFORE parseMention (dispatch-before-router).
// unmarshal failures inside parseFalsePositiveCandidate are treated as
// falsePositiveNotApplicable, never falsePositiveFailed: a malformed
// payload for an event type this function doesn't even recognize as
// relevant is exactly parseMention's own job to reject with its own
// error handling, not this function's.
func tryCaptureFalsePositivePattern(ctx context.Context, logger *slog.Logger, identities CommenterIdentityLookup, users *postgres.UserStore, auditLog *postgres.AuditLogStore, patterns FalsePositivePatternCapturer, eventType string, body []byte) falsePositiveOutcome {
	candidate, ok, err := parseFalsePositiveCandidate(eventType, body)
	if err != nil || !ok {
		return falsePositiveNotApplicable
	}

	reason, matched := falsepositive.Match(candidate.Body)
	if !matched {
		return falsePositiveNotApplicable
	}

	// From here on, this delivery IS a capture command -- every path
	// below returns falsePositiveHandled or falsePositiveFailed, never
	// falsePositiveNotApplicable: this comment is fully claimed by this
	// dispatch-before-router path regardless of outcome, never falling
	// through to the ordinary mention pipeline.

	actor, err := resolveCommenterActor(ctx, identities, candidate.CommenterID)
	if err != nil {
		logger.Error("github: false-positive capture: resolve commenter identity failed", "error", err, "repo", candidate.RepoFullName, "comment_id", candidate.CommentID)
		return falsePositiveFailed
	}

	if !actorauthz.AuthorizeLinkedActor(ctx, logger, authzSurface, users, actor, authz.ActionTeachFalsePositivePattern, authz.Resource{}) {
		logger.Warn("github: false-positive capture denied by authz", "repo", candidate.RepoFullName, "comment_id", candidate.CommentID, "commenter_login", candidate.CommenterLogin)
		return falsePositiveHandled
	}

	if err := falsepositive.ValidateReason(reason); err != nil {
		logger.Warn("github: false-positive capture rejected: empty reason", "repo", candidate.RepoFullName, "comment_id", candidate.CommentID)
		return falsePositiveHandled
	}

	row, inserted, err := patterns.Upsert(ctx, candidate.RepoFullName, candidate.CommentID, reason, actor)
	if err != nil {
		logger.Error("github: false-positive capture: upsert pattern failed", "error", err, "repo", candidate.RepoFullName, "comment_id", candidate.CommentID)
		return falsePositiveFailed
	}

	if err := auditlog.Record(ctx, auditLog, actor, "false_positive_pattern.teach", "false_positive_pattern", row.ID.String(), map[string]any{
		"repo_full_name": candidate.RepoFullName,
		"comment_id":     candidate.CommentID,
		"reason":         reason,
		"inserted":       inserted,
	}); err != nil {
		logger.Error("github: false-positive capture: record audit log failed", "error", err, "repo", candidate.RepoFullName, "pattern_id", row.ID.String())
		return falsePositiveFailed
	}

	logger.Info("github: false-positive pattern captured", "repo", candidate.RepoFullName, "pattern_id", row.ID.String(), "newly_inserted", inserted)
	return falsePositiveHandled
}
