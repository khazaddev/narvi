package workflow_test

import (
	"testing"

	"github.com/narvidev/narvi/internal/domain/intent"
	"github.com/narvidev/narvi/internal/domain/workflow"
)

// TestAllLanes_MatchesLaneConstants pins AllLanes to exactly the three
// declared Lane values, in declaration order -- a silent
// addition/removal would invalidate every exhaustive range over it
// (including the seed-row integration assertions) without failing
// anything else, mirroring authz.TestAllRoles_MatchesRoleConstants.
func TestAllLanes_MatchesLaneConstants(t *testing.T) {
	t.Parallel()

	want := []workflow.Lane{workflow.LaneReview, workflow.LaneRequest, workflow.LanePlan}
	if len(workflow.AllLanes) != len(want) {
		t.Fatalf("len(AllLanes) = %d, want %d", len(workflow.AllLanes), len(want))
	}
	for i, l := range want {
		if workflow.AllLanes[i] != l {
			t.Errorf("AllLanes[%d] = %s, want %s", i, workflow.AllLanes[i], l)
		}
	}
}

func TestIsValidLane(t *testing.T) {
	t.Parallel()

	for _, l := range workflow.AllLanes {
		if !workflow.IsValidLane(l) {
			t.Errorf("IsValidLane(%s) = false, want true", l)
		}
	}
	for _, l := range []workflow.Lane{"", "Review", "release", "build", "bogus"} {
		if workflow.IsValidLane(l) {
			t.Errorf("IsValidLane(%q) = true, want false", l)
		}
	}
}

// TestLaneFor is table-driven over every (target, mode) pair the intent
// package's own real vocabulary produces, plus the unrecognized-input
// rows proving §25.13's fail-open requirement: LaneFor is total and an
// unresolved classification degrades to today's exact dispatch behavior
// (plan when the deterministic plan-mode signal says so, the passthrough
// request lane otherwise), never a blocked dispatch.
func TestLaneFor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		target string
		mode   string
		want   workflow.Lane
	}{
		// The review-vs-request category's own review value.
		{"review + build", intent.TargetReview, intent.ModeBuild, workflow.LaneReview},
		{"review + plan (mode ignored for reviews)", intent.TargetReview, intent.ModePlan, workflow.LaneReview},
		{"review + empty mode", intent.TargetReview, "", workflow.LaneReview},

		// §15's release-vs-feature category: both values are review
		// flavors (release PR review vs ordinary feature/fix PR review,
		// intent/release.go) -- both map into the review lane, never out
		// of it (§25.4 keeps Lane a closed 3-value enum).
		{"release + build", intent.TargetRelease, intent.ModeBuild, workflow.LaneReview},
		{"release + plan (mode ignored)", intent.TargetRelease, intent.ModePlan, workflow.LaneReview},
		{"feature + build", intent.TargetFeature, intent.ModeBuild, workflow.LaneReview},
		{"feature + plan (mode ignored)", intent.TargetFeature, intent.ModePlan, workflow.LaneReview},

		// The request target splits on mode -- exactly the existing
		// planMode boolean's own semantics (rubric.go: ModeBuild is the
		// false/default value, ModePlan is true).
		{"request + plan", intent.TargetRequest, intent.ModePlan, workflow.LanePlan},
		{"request + build", intent.TargetRequest, intent.ModeBuild, workflow.LaneRequest},

		// Fail-open (§25.13): unrecognized/empty inputs never block --
		// the deterministic plan-mode signal is still honored, everything
		// else lands in the passthrough request lane.
		{"empty target + plan mode", "", intent.ModePlan, workflow.LanePlan},
		{"empty target + build mode", "", intent.ModeBuild, workflow.LaneRequest},
		{"empty target + empty mode", "", "", workflow.LaneRequest},
		{"foreign target + plan mode", "escalate", intent.ModePlan, workflow.LanePlan},
		{"foreign target + build mode", "escalate", intent.ModeBuild, workflow.LaneRequest},
		{"foreign target + foreign mode", "escalate", "hover", workflow.LaneRequest},
		{"request + foreign mode", intent.TargetRequest, "hover", workflow.LaneRequest},
		{"request + empty mode", intent.TargetRequest, "", workflow.LaneRequest},
		{"case-sensitive: Review is not review", "Review", intent.ModeBuild, workflow.LaneRequest},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := workflow.LaneFor(tc.target, tc.mode)
			if got != tc.want {
				t.Fatalf("LaneFor(%q, %q) = %s, want %s", tc.target, tc.mode, got, tc.want)
			}
			if !workflow.IsValidLane(got) {
				t.Errorf("LaneFor(%q, %q) = %q, not a valid Lane -- LaneFor must be total over the closed enum", tc.target, tc.mode, got)
			}
		})
	}
}
