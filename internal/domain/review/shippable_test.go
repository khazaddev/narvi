package review_test

import (
	"testing"

	"github.com/khazaddev/narvi/internal/domain/review"
)

// rank is this test's own independent mirror of shippable.go's unexported
// total order (auto=0 < needs_human=1 < block=2), used ONLY to build
// expected values and inequality assertions below — it does not reach
// into the production package's own unexported rank()/shippableRank at
// all (this is an external _test package, matching every other domain
// package's own convention: internal/domain/gitstate_test's allTriggers,
// internal/domain/authz_test's TestAllRoles_MatchesRoleConstants). An
// unrecognized Shippable ranks one above Block here too, mirroring
// production's own defense-in-depth default — never exercised by a
// legitimate ComputeShippable/CoverageFloor/PremiseFloor result, since all
// three only ever return one of the three legal consts.
func rank(s review.Shippable) int {
	switch s {
	case review.ShippableAuto:
		return 0
	case review.ShippableNeedsHuman:
		return 1
	case review.ShippableBlock:
		return 2
	default:
		return 3
	}
}

// composeExpected mirrors ComputeShippable's own documented max(rank)
// composition, independently, purely to build this test's "want" values —
// see rank's own doc comment for why this independence is meaningful
// rather than circular.
func composeExpected(values ...review.Shippable) review.Shippable {
	best := review.ShippableAuto
	for _, v := range values {
		if rank(v) > rank(best) {
			best = v
		}
	}
	return best
}

// TestShippableRank_TotalOrder proves the three-level total order
// (auto < needs_human < block) holds behaviorally through the ONLY seam
// that exposes it, ComputeShippable's own max(rank) composition —
// directly exercising doc.go's "ranking is an explicit table" property:
// whichever of two conflicting inputs is objectively MORE conservative
// always wins, regardless of which argument position (baseline, coverage
// floor, or premise floor) it arrived through.
func TestShippableRank_TotalOrder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		risk     review.RiskLevel
		coverage review.TestsCoverageState
		premise  review.PremiseState
		want     review.Shippable
	}{
		// needs_human (from the coverage floor) beats auto (from a clean
		// baseline and a clean premise floor).
		{"coverage floor beats a clean baseline and clean premise", review.RiskLevelLow, review.TestsCoverageStateInsufficient, review.PremiseStateOK, review.ShippableNeedsHuman},
		// block (from the premise floor) beats auto from everything else.
		{"premise floor beats a clean baseline and clean coverage", review.RiskLevelLow, review.TestsCoverageStateAdequate, review.PremiseStateNotAPR, review.ShippableBlock},
		// block (from the premise floor) beats needs_human (from the risk
		// baseline) too -- proving block outranks needs_human, not just
		// auto.
		{"premise floor (block) beats a needs_human baseline", review.RiskLevelHigh, review.TestsCoverageStateAdequate, review.PremiseStateNotAPR, review.ShippableBlock},
		// needs_human (from the risk baseline) beats auto from both
		// clean floors -- proving the baseline genuinely participates in
		// the max, not just the two floors.
		{"risk baseline (needs_human) beats two clean floors", review.RiskLevelHigh, review.TestsCoverageStateAdequate, review.PremiseStateOK, review.ShippableNeedsHuman},
		// all three clean: auto survives untouched.
		{"all three clean: auto", review.RiskLevelLow, review.TestsCoverageStateAdequate, review.PremiseStateOK, review.ShippableAuto},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := review.ComputeShippable(tc.risk, tc.coverage, tc.premise)
			if got != tc.want {
				t.Errorf("ComputeShippable(%s, %s, %s) = %s, want %s", tc.risk, tc.coverage, tc.premise, got, tc.want)
			}
		})
	}
}

// TestComputeShippable_RiskBaseline is exhaustive over every RiskLevel
// value (the three legal ones, the zero value, and an unrecognized one),
// with both floors held perfectly clean (TestsCoverageStateAdequate,
// PremiseStateOK) so the result is exactly baselineFromRisk(risk) with
// nothing else influencing it -- proving RiskLevel's own baseline mapping
// (doc.go design call #2), including its fail-conservative zero-value
// handling.
func TestComputeShippable_RiskBaseline(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		risk review.RiskLevel
		want review.Shippable
	}{
		{"low", review.RiskLevelLow, review.ShippableAuto},
		{"medium", review.RiskLevelMedium, review.ShippableNeedsHuman},
		{"high", review.RiskLevelHigh, review.ShippableNeedsHuman},
		{"zero value fails conservative (needs_human, matching high)", review.RiskLevel(""), review.ShippableNeedsHuman},
		{"unrecognized value fails conservative (needs_human, matching high)", review.RiskLevel("bogus"), review.ShippableNeedsHuman},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := review.ComputeShippable(tc.risk, review.TestsCoverageStateAdequate, review.PremiseStateOK)
			if got != tc.want {
				t.Errorf("ComputeShippable(%s, adequate, ok) = %s, want %s", tc.risk, got, tc.want)
			}
		})
	}
}

// TestComputeShippable_FloorCompositionMatrix is the exhaustive
// coverage-floor-state × premise-floor-state matrix the Step brief asks
// for: every one of the five TestsCoverageState values under test
// (adequate/insufficient/skipped/zero/unrecognized) crossed with every one
// of the five PremiseState values under test
// (ok/questionable/not_a_pr/zero/unrecognized) = 25 rows, RiskLevel fixed
// at RiskLevelLow (baseline ShippableAuto, the most permissive) so each
// cell isolates exactly what the two floors alone compose to.
func TestComputeShippable_FloorCompositionMatrix(t *testing.T) {
	t.Parallel()

	coverageCases := []struct {
		name  string
		state review.TestsCoverageState
	}{
		{"adequate", review.TestsCoverageStateAdequate},
		{"insufficient", review.TestsCoverageStateInsufficient},
		{"skipped", review.TestsCoverageStateSkipped},
		{"zero", review.TestsCoverageState("")},
		{"unrecognized", review.TestsCoverageState("bogus")},
	}

	premiseCases := []struct {
		name  string
		state review.PremiseState
	}{
		{"ok", review.PremiseStateOK},
		{"questionable", review.PremiseStateQuestionable},
		{"not_a_pr", review.PremiseStateNotAPR},
		{"zero", review.PremiseState("")},
		{"unrecognized", review.PremiseState("bogus")},
	}

	for _, cov := range coverageCases {
		cov := cov
		for _, prem := range premiseCases {
			prem := prem
			t.Run(cov.name+"_x_"+prem.name, func(t *testing.T) {
				t.Parallel()

				covFloor := review.CoverageFloor(cov.state)
				premFloor := review.PremiseFloor(prem.state)
				want := composeExpected(review.ShippableAuto, covFloor, premFloor)

				got := review.ComputeShippable(review.RiskLevelLow, cov.state, prem.state)
				if got != want {
					t.Errorf("ComputeShippable(low, %s, %s) = %s, want %s (coverageFloor=%s, premiseFloor=%s)",
						cov.name, prem.name, got, want, covFloor, premFloor)
				}
			})
		}
	}
}

// TestComputeShippable_RaiseOnly proves the raise-only property directly,
// exhaustively across the full (risk × coverage × premise) input matrix
// (5×5×5 = 125 combinations, including every zero-value and unrecognized
// variant): ComputeShippable's result is NEVER ranked below what
// RiskLevel's own baseline alone implies, NEVER below CoverageFloor's own
// result alone, and NEVER below PremiseFloor's own result alone. This is
// the property the plan calls "raise-only" -- there is no input in this
// matrix for which applying a floor produces a LESS conservative outcome
// than not applying it.
func TestComputeShippable_RaiseOnly(t *testing.T) {
	t.Parallel()

	risks := []struct {
		name         string
		level        review.RiskLevel
		wantBaseline review.Shippable
	}{
		{"low", review.RiskLevelLow, review.ShippableAuto},
		{"medium", review.RiskLevelMedium, review.ShippableNeedsHuman},
		{"high", review.RiskLevelHigh, review.ShippableNeedsHuman},
		{"zero", review.RiskLevel(""), review.ShippableNeedsHuman},
		{"unrecognized", review.RiskLevel("bogus"), review.ShippableNeedsHuman},
	}

	coverages := []review.TestsCoverageState{
		review.TestsCoverageStateAdequate,
		review.TestsCoverageStateInsufficient,
		review.TestsCoverageStateSkipped,
		review.TestsCoverageState(""),
		review.TestsCoverageState("bogus"),
	}

	premises := []review.PremiseState{
		review.PremiseStateOK,
		review.PremiseStateQuestionable,
		review.PremiseStateNotAPR,
		review.PremiseState(""),
		review.PremiseState("bogus"),
	}

	for _, r := range risks {
		r := r
		for _, cov := range coverages {
			cov := cov
			for _, prem := range premises {
				prem := prem
				name := r.name + "_" + string(cov) + "_" + string(prem)
				t.Run(name, func(t *testing.T) {
					t.Parallel()

					covFloor := review.CoverageFloor(cov)
					premFloor := review.PremiseFloor(prem)
					got := review.ComputeShippable(r.level, cov, prem)

					if rank(got) < rank(r.wantBaseline) {
						t.Errorf("ComputeShippable(%s, %s, %s) = %s ranks BELOW the risk baseline %s alone -- raise-only violated",
							r.name, cov, prem, got, r.wantBaseline)
					}
					if rank(got) < rank(covFloor) {
						t.Errorf("ComputeShippable(%s, %s, %s) = %s ranks BELOW CoverageFloor(%s)=%s alone -- raise-only violated",
							r.name, cov, prem, got, cov, covFloor)
					}
					if rank(got) < rank(premFloor) {
						t.Errorf("ComputeShippable(%s, %s, %s) = %s ranks BELOW PremiseFloor(%s)=%s alone -- raise-only violated",
							r.name, cov, prem, got, prem, premFloor)
					}

					want := composeExpected(r.wantBaseline, covFloor, premFloor)
					if got != want {
						t.Errorf("ComputeShippable(%s, %s, %s) = %s, want %s", r.name, cov, prem, got, want)
					}
				})
			}
		}
	}
}
