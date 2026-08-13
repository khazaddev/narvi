//go:build integration

// Integration tests for Step 62's own (§21.1) read-only analytics route
// (reviewanalytics.go's own GetReviewAnalytics), against a real Postgres
// instance -- sharing this package's own testRig (httpapi_integration_test.go).
package httpapi_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	appreviewverdict "github.com/khazaddev/narvi/internal/app/reviewverdict"
	"github.com/khazaddev/narvi/internal/domain/review"
	"github.com/khazaddev/narvi/internal/domain/reviewpost"
)

// TestGetReviewAnalytics_ViewerAllowed_NothingComputedYet proves TWO
// things at once: (1) authz.ActionViewAnalytics is genuinely a §13.3 row
// 1 action -- a VIEWER, the one role every §21.2 write-side route in this
// package denies outright, can still read this endpoint; (2) a repo with
// no review_verdicts/review_findings rows at all renders every one of
// the three "not yet computed" sentinels as false/nil, never a 500 and
// never a falsely-reassuring real zero.
func TestGetReviewAnalytics_ViewerAllowed_NothingComputedYet(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleViewer)

	var resp restdtos.ReviewAnalytics
	status := rig.doJSON(t, http.MethodGet, "/api/repos/acme/analytics-empty/review-analytics", nil, &resp, token)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}

	if resp.TimeseriesComputed {
		t.Errorf("TimeseriesComputed = true, want false (no verdicts posted)")
	}
	if resp.Timeseries != nil {
		t.Errorf("Timeseries = %v, want nil", resp.Timeseries)
	}
	if resp.TopRiskDriversComputed {
		t.Errorf("TopRiskDriversComputed = true, want false (no verdicts posted)")
	}
	if resp.TopRiskDrivers != nil {
		t.Errorf("TopRiskDrivers = %v, want nil", resp.TopRiskDrivers)
	}
	if resp.FindingOutcomesComputed {
		t.Errorf("FindingOutcomesComputed = true, want false (no findings reported)")
	}
	if resp.FindingOutcomes != nil {
		t.Errorf("FindingOutcomes = %v, want nil", resp.FindingOutcomes)
	}
}

// TestGetReviewAnalytics_RendersComputedRollups seeds one review_verdicts
// row (Shippable=auto, one BlastRadius tag) and one review_findings row,
// then proves all three rollups render as real, computed data -- the
// positive twin of the all-sentinels test above, and the ONE case in this
// file that exercises the actual count/grouping logic end to end through
// the real HTTP surface (internal/domain/reviewverdict's own Timeseries/
// TopRiskDrivers/FindingOutcomes are unit-tested directly; this is the
// wiring proof).
func TestGetReviewAnalytics_RendersComputedRollups(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := rig.createAuthenticatedUser(ctx, t)

	const repoFullName = "acme/analytics-populated"

	verdict := review.Verdict{
		RiskLevel:         review.RiskLevelLow,
		Premise:           review.PremiseStateOK,
		BlastRadius:       []review.Tag{review.TagAuth},
		TestsCoverage:     review.TestsCoverageStateAdequate,
		DocsDrift:         review.DocsDriftStateNone,
		ProposedShippable: review.ProposedShippableAuto,
		FilesChanged:      2,
	}
	verdict.Shippable = review.ComputeShippable(verdict.RiskLevel, verdict.TestsCoverage, verdict.Premise)
	seededDigest := reviewpost.Digest{
		Summary: "Test-seeded verdict.",
		ArchDecisions: []reviewpost.ArchDecision{
			{Decision: "Use a shared retry helper.", RejectedAlternative: "Inline retry logic per call site.", ConventionConformance: "Matches internal/platform's existing retry helper pattern."},
		},
	}
	insertedRecord, err := appreviewverdict.Insert(ctx, rig.reviewVerdicts, repoFullName, 7, "deadbeef", pgtype.UUID{}, verdict, seededDigest)
	if err != nil {
		t.Fatalf("seed review_verdicts row: %v", err)
	}
	// Insert's own returned Record is the digest's read-back path
	// (recordFromRow's own ArchDecisions JSON unmarshal, convert.go) --
	// every other call site in this codebase discards it (`if _, err :=
	// ...`), so this is the one place that path is actually exercised
	// end to end: it must round-trip verbatim, not just the write side
	// TestPostReviewVerdict_PersistsDigestColumns already covers.
	if insertedRecord.Digest.Summary != seededDigest.Summary {
		t.Errorf("Insert() returned Digest.Summary = %q, want %q", insertedRecord.Digest.Summary, seededDigest.Summary)
	}
	if len(insertedRecord.Digest.ArchDecisions) != 1 || insertedRecord.Digest.ArchDecisions[0] != seededDigest.ArchDecisions[0] {
		t.Errorf("Insert() returned Digest.ArchDecisions = %+v, want %+v", insertedRecord.Digest.ArchDecisions, seededDigest.ArchDecisions)
	}

	if _, err := rig.reviewFindings.Upsert(ctx, sqlcgen.UpsertReviewFindingParams{
		RepoFullName: repoFullName,
		PrNumber:     7,
		IdentityHash: "finding-hash-1",
		Severity:     "medium",
		FilePath:     "internal/example.go",
		Description:  "example finding",
	}); err != nil {
		t.Fatalf("seed review_findings row: %v", err)
	}

	var resp restdtos.ReviewAnalytics
	status := rig.doJSON(t, http.MethodGet, "/api/repos/"+repoFullName+"/review-analytics", nil, &resp, token)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}

	if !resp.TimeseriesComputed {
		t.Fatalf("TimeseriesComputed = false, want true (one verdict was posted)")
	}
	if resp.Timeseries == nil || len(*resp.Timeseries) != 1 {
		t.Fatalf("Timeseries = %v, want exactly 1 bucket", resp.Timeseries)
	}
	if got := (*resp.Timeseries)[0].AutoCount; got != 1 {
		t.Errorf("Timeseries[0].AutoCount = %d, want 1", got)
	}

	if !resp.TopRiskDriversComputed {
		t.Fatalf("TopRiskDriversComputed = false, want true")
	}
	if resp.TopRiskDrivers == nil || len(*resp.TopRiskDrivers) != 1 {
		t.Fatalf("TopRiskDrivers = %v, want exactly 1 tag", resp.TopRiskDrivers)
	}
	if got := (*resp.TopRiskDrivers)[0]; got.Tag != restdtos.ReviewAnalyticsTagCountTagAuth || got.Count != 1 {
		t.Errorf("TopRiskDrivers[0] = %+v, want {auth 1}", got)
	}

	if !resp.FindingOutcomesComputed {
		t.Fatalf("FindingOutcomesComputed = false, want true (one finding was reported)")
	}
	if resp.FindingOutcomes == nil || len(*resp.FindingOutcomes) != 1 {
		t.Fatalf("FindingOutcomes = %v, want exactly 1 status", resp.FindingOutcomes)
	}
	if got := (*resp.FindingOutcomes)[0]; got.Status != restdtos.ReviewAnalyticsFindingStatusCountStatusOpen || got.Count != 1 {
		t.Errorf("FindingOutcomes[0] = %+v, want {open 1}", got)
	}
}
