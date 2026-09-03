// This file implements §26.4's own §26.5 capture command: an explicit
// `arch recap wrong: <reason>` PR-thread comment contests the deep path's
// own architecture-recap digest section. Handled DISPATCH-BEFORE-ROUTER,
// exactly mirroring falsepositivecapture.go's own identical `false
// positive: <reason>` capture command (§22.2) -- checked in
// handler.go BEFORE parseMention ever runs, so a contest command never
// reaches CreateOrJoin/the mention router at all: it is not a review-
// session trigger, and a maintainer contesting a recap must never
// accidentally spawn or prompt a review session as a side effect.
//
// Authorization reuses domain/authz.Authorize DIRECTLY (§13.3's own
// "every state-changing actor command... calls it identically"), via the
// SAME actorauthz.AuthorizeLinkedActor gate falsepositivecapture.go/
// coalesce.go's own create/prompt checks already use -- never a parallel
// permission model invented for this one command (mirrors §22.2's own
// explicit instruction, extended by §26.5 to this sibling command).
//
// Unlike falsepositivecapture.go, this file ALSO needs to resolve WHICH
// digest section content is being contested: the maintainer's own comment
// never repeats the recap text itself, so this handler reads this PR's
// own LATEST posted verdict (appreviewverdict.GetLatest, the SAME read
// internal/app/reviewverdict already exposes for the auto-approval
// eligibility engine/decision inbox) and hashes its own Digest.
// ArchDecisions (reviewpost.ComputeDigestSectionIdentity(reviewpost.
// DigestSectionArchRecap, reviewpost.ArchRecapText(...))) -- §22.1's
// content-hash identity discipline, extended to digest sections, exactly
// as §26.5 specifies.

package github

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/narvidev/narvi/internal/adapters/outbound/postgres"
	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/narvidev/narvi/internal/app/actorauthz"
	"github.com/narvidev/narvi/internal/app/auditlog"
	appreviewverdict "github.com/narvidev/narvi/internal/app/reviewverdict"
	"github.com/narvidev/narvi/internal/domain/archrecap"
	"github.com/narvidev/narvi/internal/domain/authz"
	"github.com/narvidev/narvi/internal/domain/reviewpost"
)

// ArchRecapContestCapturer is the narrow slice of *postgres.
// ReviewDigestSectionFeedbackStore this file needs -- a small, locally-
// defined interface (mirroring FalsePositivePatternCapturer's own
// established precedent, falsepositivecapture.go) so a unit test can
// inject a fake with no real Postgres connection.
type ArchRecapContestCapturer interface {
	Upsert(ctx context.Context, repoFullName string, prNumber int32, section, contentHash, commentType string, commentID int64, reason string, createdBy pgtype.UUID) (sqlcgen.ReviewDigestSectionFeedback, bool, error)
}

// archRecapContestCandidate is the small, common shape both event-type
// parsers below produce -- mirrors falsePositiveCandidate exactly, PLUS
// PRNumber (falsePositiveCandidate never needed it: that table is
// repo-scoped, this one is PR-scoped, since a digest section's own
// content is a per-PR fact).
type archRecapContestCandidate struct {
	RepoFullName   string
	PRNumber       int32
	CommentID      int64
	CommentType    string
	CommenterID    int64
	CommenterLogin string
	Body           string
}

// parseArchRecapContestCandidate mirrors parseFalsePositiveCandidate
// (falsepositivecapture.go) exactly, additionally extracting PRNumber
// from each event type's own already-decoded payload (issueCommentPayload.
// Issue.Number for issue_comment, pullRequestReviewCommentPayload.
// PullRequest.Number for pull_request_review_comment -- both already
// parsed elsewhere in this package, no new payload shape needed).
func parseArchRecapContestCandidate(eventType string, body []byte) (archRecapContestCandidate, bool, error) {
	switch eventType {
	case eventTypeIssueComment:
		var p issueCommentPayload
		if err := json.Unmarshal(body, &p); err != nil {
			return archRecapContestCandidate{}, false, err
		}
		if p.Action != commentActionCreated || p.Issue.PullRequest == nil {
			return archRecapContestCandidate{}, false, nil
		}
		return archRecapContestCandidate{
			RepoFullName:   p.Repository.FullName,
			PRNumber:       p.Issue.Number,
			CommentID:      p.Comment.ID,
			CommentType:    eventTypeIssueComment,
			CommenterID:    p.Comment.User.ID,
			CommenterLogin: p.Comment.User.Login,
			Body:           p.Comment.Body,
		}, true, nil
	case eventTypePullRequestReviewComment:
		var p pullRequestReviewCommentPayload
		if err := json.Unmarshal(body, &p); err != nil {
			return archRecapContestCandidate{}, false, err
		}
		if p.Action != commentActionCreated {
			return archRecapContestCandidate{}, false, nil
		}
		return archRecapContestCandidate{
			RepoFullName:   p.Repository.FullName,
			PRNumber:       p.PullRequest.Number,
			CommentID:      p.Comment.ID,
			CommentType:    eventTypePullRequestReviewComment,
			CommenterID:    p.Comment.User.ID,
			CommenterLogin: p.Comment.User.Login,
			Body:           p.Comment.Body,
		}, true, nil
	default:
		return archRecapContestCandidate{}, false, nil
	}
}

// archRecapContestOutcome mirrors falsePositiveOutcome's own identical
// three-value shape (falsepositivecapture.go) -- see that type's own doc
// comment for the full "why" behind each value; the same reasoning
// applies here verbatim.
type archRecapContestOutcome int

const (
	archRecapContestNotApplicable archRecapContestOutcome = iota
	archRecapContestHandled
	archRecapContestFailed
)

// tryCaptureArchRecapContest is this file's own one entry point, called
// from handler.go BEFORE parseMention (dispatch-before-router). Mirrors
// tryCaptureFalsePositivePattern's own control flow exactly, with one
// additional step between authorization and the write: resolving the
// content hash of the CURRENTLY-CONTESTED arch recap (this PR's own
// latest posted verdict, if any).
func tryCaptureArchRecapContest(ctx context.Context, logger *slog.Logger, identities CommenterIdentityLookup, users *postgres.UserStore, auditLog *postgres.AuditLogStore, reviewVerdicts appreviewverdict.Deps, feedback ArchRecapContestCapturer, eventType string, body []byte) archRecapContestOutcome {
	candidate, ok, err := parseArchRecapContestCandidate(eventType, body)
	if err != nil || !ok {
		return archRecapContestNotApplicable
	}

	reason, matched := archrecap.Match(candidate.Body)
	if !matched {
		return archRecapContestNotApplicable
	}

	// From here on, this delivery IS a contest command -- every path below
	// returns archRecapContestHandled or archRecapContestFailed, never
	// archRecapContestNotApplicable: this comment is fully claimed by this
	// dispatch-before-router path regardless of outcome, never falling
	// through to the ordinary mention pipeline (mirrors
	// tryCaptureFalsePositivePattern's own identical discipline).

	actor, err := resolveCommenterActor(ctx, identities, candidate.CommenterID)
	if err != nil {
		logger.Error("github: arch-recap contest: resolve commenter identity failed", "error", err, "repo", candidate.RepoFullName, "pr_number", candidate.PRNumber, "comment_id", candidate.CommentID)
		return archRecapContestFailed
	}

	if !actorauthz.AuthorizeLinkedActor(ctx, logger, authzSurface, users, actor, authz.ActionContestArchRecap, authz.Resource{}) {
		logger.Warn("github: arch-recap contest denied by authz", "repo", candidate.RepoFullName, "pr_number", candidate.PRNumber, "comment_id", candidate.CommentID, "commenter_login", candidate.CommenterLogin)
		return archRecapContestHandled
	}

	if err := archrecap.ValidateReason(reason); err != nil {
		logger.Warn("github: arch-recap contest rejected: empty reason", "repo", candidate.RepoFullName, "pr_number", candidate.PRNumber, "comment_id", candidate.CommentID)
		return archRecapContestHandled
	}

	// reviewVerdicts.ReviewVerdicts == nil: a misconfiguration (production
	// wiring always sets this alongside feedback, cmd/control-plane/
	// main.go) -- guarded explicitly rather than trusting that sibling
	// field's own non-nil-ness, since *postgres.ReviewVerdictStore's own
	// methods are not themselves nil-receiver-safe.
	if reviewVerdicts.ReviewVerdicts == nil {
		logger.Error("github: arch-recap contest: no ReviewVerdicts store configured", "repo", candidate.RepoFullName, "pr_number", candidate.PRNumber)
		return archRecapContestFailed
	}

	// Resolve the content hash of the recap actually being contested --
	// this PR's own latest posted verdict (appreviewverdict.GetLatest, the
	// SAME read the auto-approval eligibility engine/decision inbox
	// already use). ok=false (no verdict ever posted for this PR at all)
	// is a legitimate, if unusual, business-rule no-op -- there is nothing
	// to hash and nothing to contest, handled exactly like an
	// unauthorized/empty-reason command above, never a genuine failure.
	record, verdictOK, err := appreviewverdict.GetLatest(ctx, reviewVerdicts, candidate.RepoFullName, candidate.PRNumber)
	if err != nil {
		logger.Error("github: arch-recap contest: get latest verdict failed", "error", err, "repo", candidate.RepoFullName, "pr_number", candidate.PRNumber)
		return archRecapContestFailed
	}
	if !verdictOK {
		logger.Warn("github: arch-recap contest: no verdict on record for this PR, nothing to contest", "repo", candidate.RepoFullName, "pr_number", candidate.PRNumber)
		return archRecapContestHandled
	}
	contentHash := reviewpost.ComputeDigestSectionIdentity(reviewpost.DigestSectionArchRecap, reviewpost.ArchRecapText(record.Digest.ArchDecisions))

	row, inserted, err := feedback.Upsert(ctx, candidate.RepoFullName, candidate.PRNumber, string(reviewpost.DigestSectionArchRecap), contentHash, candidate.CommentType, candidate.CommentID, reason, actor)
	if err != nil {
		logger.Error("github: arch-recap contest: upsert feedback failed", "error", err, "repo", candidate.RepoFullName, "pr_number", candidate.PRNumber, "comment_id", candidate.CommentID)
		return archRecapContestFailed
	}

	if err := auditlog.Record(ctx, auditLog, actor, "arch_recap_feedback.contest", "review_digest_section_feedback", row.ID.String(), map[string]any{
		"repo_full_name": candidate.RepoFullName,
		"pr_number":      candidate.PRNumber,
		"comment_id":     candidate.CommentID,
		"comment_type":   candidate.CommentType,
		"section":        string(reviewpost.DigestSectionArchRecap),
		"content_hash":   contentHash,
		"reason":         reason,
		"inserted":       inserted,
	}); err != nil {
		logger.Error("github: arch-recap contest: record audit log failed", "error", err, "repo", candidate.RepoFullName, "pr_number", candidate.PRNumber, "feedback_id", row.ID.String())
		return archRecapContestFailed
	}

	logger.Info("github: arch-recap contest captured", "repo", candidate.RepoFullName, "pr_number", candidate.PRNumber, "feedback_id", row.ID.String(), "newly_inserted", inserted)
	return archRecapContestHandled
}
