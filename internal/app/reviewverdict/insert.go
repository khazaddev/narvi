package reviewverdict

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/domain/review"
	"github.com/khazaddev/narvi/internal/domain/reviewpost"
	"github.com/khazaddev/narvi/internal/domain/reviewtriage"
	"github.com/khazaddev/narvi/internal/domain/reviewverdict"
)

// Insert appends one review_verdicts row for (repoFullName, prNumber),
// forwarding verdict/headSHA verbatim -- §21.1: "pure storage, never
// re-parsing anything out of posted comment text". store is taken
// directly (already WithTx'd by the caller when a transaction is in
// play, e.g. httpapi.PostReviewVerdict's own existing tx) rather than
// this package opening its own -- mirrors reviewFindings.WithTx(tx)'s own
// identical calling convention at that SAME call site, so this insert
// commits atomically alongside the findings upserts and outbox write
// already happening there.
//
// headSHA == "" is refused outright (never inserted as an empty string)
// -- see migrations/000067_review_verdicts.up.sql's own doc comment for
// why a verdict with no known head SHA must not exist in this table at
// all, rather than existing with a value the auto-approval eligibility
// engine's own stale-verdict guard could never honestly evaluate.
//
// digest (Step 66, §26.1, extended by Step 67, §26.2) is forwarded
// verbatim onto the SAME row's own seven digest_* columns (migrations/
// 000077_review_verdicts_digest.up.sql,
// 000078_review_verdicts_description_adequacy.up.sql) -- digest.Summary/
// DescriptionAdequacy/AdequacyExplanation are expected non-empty/in-enum
// by the time this function is called in production
// (reviewpost.ValidateVerdictInput's own ErrEmptyDigestSummary/
// ErrInvalidDescriptionAdequacy/ErrEmptyAdequacyExplanation already
// rejected a malformed one before BuildVerdict ever ran), but this
// function does not itself re-validate that -- exactly like it does not
// re-validate verdict's own fields, trusting its one caller
// (httpapi.PostReviewVerdict) to have already done so.
// reviewPath (Step 68, §26.3) is the posting turn's own turns.
// review_depth, forwarded verbatim -- empty ("", reviewtriage.ReviewDepth
// zero value) is a legitimate, common value (a verdict whose own turn
// never resolved a depth, or a caller that predates this Step), persisted
// as a genuine SQL NULL, never the literal string "" (nonEmptyStringPtr
// below).
func Insert(ctx context.Context, store *postgres.ReviewVerdictStore, repoFullName string, prNumber int32, headSHA string, sessionID pgtype.UUID, verdict review.Verdict, digest reviewpost.Digest, reviewPath reviewtriage.ReviewDepth) (reviewverdict.Record, error) {
	if headSHA == "" {
		return reviewverdict.Record{}, fmt.Errorf("reviewverdict: insert: refusing to persist a verdict with no known head sha for %s#%d", repoFullName, prNumber)
	}

	blastRadiusJSON, err := marshalTags(verdict.BlastRadius)
	if err != nil {
		return reviewverdict.Record{}, fmt.Errorf("reviewverdict: insert: marshal blast radius: %w", err)
	}

	archDecisionsJSON, err := marshalArchDecisions(digest.ArchDecisions)
	if err != nil {
		return reviewverdict.Record{}, fmt.Errorf("reviewverdict: insert: marshal digest arch decisions: %w", err)
	}

	row, err := store.Insert(ctx, sqlcgen.InsertReviewVerdictParams{
		RepoFullName:              repoFullName,
		PrNumber:                  prNumber,
		HeadSha:                   headSHA,
		RiskLevel:                 string(verdict.RiskLevel),
		Premise:                   string(verdict.Premise),
		BlastRadius:               blastRadiusJSON,
		FilesChanged:              int32(verdict.FilesChanged),
		TestsCoverage:             string(verdict.TestsCoverage),
		DocsDrift:                 string(verdict.DocsDrift),
		ProposedShippable:         string(verdict.ProposedShippable),
		Shippable:                 string(verdict.Shippable),
		SessionID:                 sessionID,
		DigestSummary:             nonEmptyStringPtr(digest.Summary),
		DigestArchDecisions:       archDecisionsJSON,
		DigestStackRisks:          nonEmptyStringPtr(digest.StackRisks),
		DigestUnverifiedLimits:    nonEmptyStringPtr(digest.UnverifiedLimits),
		DigestDescriptionAdequacy: nonEmptyStringPtr(string(digest.DescriptionAdequacy)),
		DigestAdequacyExplanation: nonEmptyStringPtr(digest.AdequacyExplanation),
		DigestProposedBody:        nonEmptyStringPtr(digest.ProposedBody),
		ReviewPath:                nonEmptyStringPtr(string(reviewPath)),
	})
	if err != nil {
		return reviewverdict.Record{}, err
	}
	return recordFromRow(row), nil
}

// nonEmptyStringPtr returns nil for an empty string, or a pointer to s
// otherwise -- so an unset/blank digest field is stored as a real SQL
// NULL (migrations/000077's own "not yet computed" convention), never the
// empty string "", which this table's own reader (digestFromRow,
// convert.go) would otherwise be unable to distinguish from a genuinely
// empty-but-present value.
func nonEmptyStringPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
