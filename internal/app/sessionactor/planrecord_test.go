package sessionactor

import (
	"errors"
	"testing"

	plandomain "github.com/khazaddev/narvi/internal/domain/plan"
)

// TestFindSummaryStatus is table-driven over the lookup helper
// recordPlanIfNeeded's supersede loop (planrecord.go) uses to read a plan
// row's own ACTUAL current Status back out of the already-loaded summaries
// slice -- the piece of state the audit-fix batch's defense-in-depth
// Transition check now reads, instead of the hardcoded
// plandomain.StatusAwaitingApproval constant it used to check against
// itself.
func TestFindSummaryStatus(t *testing.T) {
	summaries := []plandomain.Summary{
		{ID: "a", Version: 1, Status: plandomain.StatusApproved},
		{ID: "b", Version: 2, Status: plandomain.StatusAwaitingApproval},
	}

	tests := []struct {
		name       string
		id         plandomain.ID
		wantStatus plandomain.Status
		wantOK     bool
	}{
		{"existing approved row", "a", plandomain.StatusApproved, true},
		{"existing awaiting_approval row", "b", plandomain.StatusAwaitingApproval, true},
		{"unknown id", "z", "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotStatus, gotOK := findSummaryStatus(summaries, tc.id)
			if gotOK != tc.wantOK {
				t.Fatalf("findSummaryStatus(_, %q) ok = %v, want %v", tc.id, gotOK, tc.wantOK)
			}
			if gotOK && gotStatus != tc.wantStatus {
				t.Errorf("findSummaryStatus(_, %q) = %q, want %q", tc.id, gotStatus, tc.wantStatus)
			}
		})
	}
}

// TestRecordPlanIfNeeded_SupersedeDomainCheck_CatchesShouldSupersedeRegression
// proves the audit-fix batch's own L10 fix genuinely closes the gap its
// doc comment (planrecord.go) claims to close: if plandomain.ShouldSupersede
// ever regressed to also return the id of a row whose real status is NOT
// awaiting_approval, the supersede loop's own defense-in-depth check --
// findSummaryStatus followed by plandomain.Transition(status,
// plandomain.TriggerSupersede), the exact two steps recordPlanIfNeeded runs
// -- now genuinely returns a *plandomain.IllegalTransitionError for every
// such non-awaiting-approval status, where before the fix (checking the
// hardcoded plandomain.StatusAwaitingApproval constant against itself) it
// could never fail no matter what real status the row carried.
//
// The normal end-to-end supersede path (a real awaiting_approval row,
// against a real Postgres instance) is already proven by
// planrecord_integration_test.go's own
// TestCompleteProcessingTurn_SecondPlanModeTurn_SupersedesPriorAwaitingApproval
// and its Slack-ref siblings; this test isolates just the regression case
// those integration tests don't (and, so long as ShouldSupersede stays
// correct, normally can't) exercise.
func TestRecordPlanIfNeeded_SupersedeDomainCheck_CatchesShouldSupersedeRegression(t *testing.T) {
	for _, badStatus := range []plandomain.Status{plandomain.StatusApproved, plandomain.StatusRejected, plandomain.StatusSuperseded} {
		t.Run(string(badStatus), func(t *testing.T) {
			// Simulates ShouldSupersede having regressed to return the id
			// of a row whose real status is badStatus, not
			// awaiting_approval.
			summaries := []plandomain.Summary{
				{ID: "regressed-id", Version: 1, Status: badStatus},
			}

			status, ok := findSummaryStatus(summaries, "regressed-id")
			if !ok {
				t.Fatalf("findSummaryStatus did not find the seeded id")
			}

			_, err := plandomain.Transition(status, plandomain.TriggerSupersede)
			if err == nil {
				t.Fatalf("Transition(%s, TriggerSupersede) = nil error, want an error -- the fix should catch a ShouldSupersede regression returning a %s row", status, status)
			}
			var illegal *plandomain.IllegalTransitionError
			if !errors.As(err, &illegal) {
				t.Fatalf("error = %v, want *plandomain.IllegalTransitionError", err)
			}
			if illegal.From != status || illegal.Trigger != plandomain.TriggerSupersede {
				t.Errorf("IllegalTransitionError = %+v, want From=%s Trigger=%s", illegal, status, plandomain.TriggerSupersede)
			}
		})
	}
}

// TestRecordPlanIfNeeded_SupersedeDomainCheck_AllowsAwaitingApproval is the
// TestRecordPlanIfNeeded_SupersedeDomainCheck_CatchesShouldSupersedeRegression
// sibling proving the non-regressed case still passes: a row whose real
// status IS awaiting_approval (the only case ShouldSupersede should ever
// actually produce today) is a legal Transition edge, so the check does not
// spuriously fail the normal path.
func TestRecordPlanIfNeeded_SupersedeDomainCheck_AllowsAwaitingApproval(t *testing.T) {
	summaries := []plandomain.Summary{
		{ID: "normal-id", Version: 1, Status: plandomain.StatusAwaitingApproval},
	}

	status, ok := findSummaryStatus(summaries, "normal-id")
	if !ok {
		t.Fatalf("findSummaryStatus did not find the seeded id")
	}

	if _, err := plandomain.Transition(status, plandomain.TriggerSupersede); err != nil {
		t.Fatalf("Transition(%s, TriggerSupersede) unexpected error: %v", status, err)
	}
}
