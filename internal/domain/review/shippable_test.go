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
			// review.DescriptionAdequacyOK imposes no floor of its own
			// (AdequacyFloor's own doc comment) -- held fixed here so this
			// table's own pre-existing risk/coverage/premise interplay
			// assertions are unaffected by the §26.2/Step 67 addition; see
			// TestComputeShippable_AdequacyRaiseOnly and
			// TestComputeShippable_ThreeFloorCompositionMatrix below for the
			// adequacy floor's own dedicated coverage.
			got := review.ComputeShippable(tc.risk, tc.coverage, tc.premise, review.DescriptionAdequacyOK)
			if got != tc.want {
				t.Errorf("ComputeShippable(%s, %s, %s, ok) = %s, want %s", tc.risk, tc.coverage, tc.premise, got, tc.want)
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
			got := review.ComputeShippable(tc.risk, review.TestsCoverageStateAdequate, review.PremiseStateOK, review.DescriptionAdequacyOK)
			if got != tc.want {
				t.Errorf("ComputeShippable(%s, adequate, ok, ok) = %s, want %s", tc.risk, got, tc.want)
			}
		})
	}
}

// TestComputeShippable_ThreeFloorCompositionMatrix is the exhaustive
// coverage-floor-state × premise-floor-state × description-adequacy-
// floor-state matrix (extended by §26.2/Step 67 from its own original
// two-floor version): every one of the five TestsCoverageState values
// under test (adequate/insufficient/skipped/zero/unrecognized) crossed
// with every one of the five PremiseState values under test
// (ok/questionable/not_a_pr/zero/unrecognized) crossed with every one of
// the five DescriptionAdequacy values under test
// (ok/drift/misleading/zero/unrecognized) = 125 rows, RiskLevel fixed at
// RiskLevelLow (baseline ShippableAuto, the most permissive) so each cell
// isolates exactly what the three floors alone compose to.
func TestComputeShippable_ThreeFloorCompositionMatrix(t *testing.T) {
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

	adequacyCases := []struct {
		name  string
		state review.DescriptionAdequacy
	}{
		{"ok", review.DescriptionAdequacyOK},
		{"drift", review.DescriptionAdequacyDrift},
		{"misleading", review.DescriptionAdequacyMisleading},
		{"zero", review.DescriptionAdequacy("")},
		{"unrecognized", review.DescriptionAdequacy("bogus")},
	}

	for _, cov := range coverageCases {
		cov := cov
		for _, prem := range premiseCases {
			prem := prem
			for _, adeq := range adequacyCases {
				adeq := adeq
				t.Run(cov.name+"_x_"+prem.name+"_x_"+adeq.name, func(t *testing.T) {
					t.Parallel()

					covFloor := review.CoverageFloor(cov.state)
					premFloor := review.PremiseFloor(prem.state)
					adeqFloor := review.AdequacyFloor(adeq.state)
					want := composeExpected(review.ShippableAuto, covFloor, premFloor, adeqFloor)

					got := review.ComputeShippable(review.RiskLevelLow, cov.state, prem.state, adeq.state)
					if got != want {
						t.Errorf("ComputeShippable(low, %s, %s, %s) = %s, want %s (coverageFloor=%s, premiseFloor=%s, adequacyFloor=%s)",
							cov.name, prem.name, adeq.name, got, want, covFloor, premFloor, adeqFloor)
					}
				})
			}
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
					got := review.ComputeShippable(r.level, cov, prem, review.DescriptionAdequacyOK)

					if rank(got) < rank(r.wantBaseline) {
						t.Errorf("ComputeShippable(%s, %s, %s, ok) = %s ranks BELOW the risk baseline %s alone -- raise-only violated",
							r.name, cov, prem, got, r.wantBaseline)
					}
					if rank(got) < rank(covFloor) {
						t.Errorf("ComputeShippable(%s, %s, %s, ok) = %s ranks BELOW CoverageFloor(%s)=%s alone -- raise-only violated",
							r.name, cov, prem, got, cov, covFloor)
					}
					if rank(got) < rank(premFloor) {
						t.Errorf("ComputeShippable(%s, %s, %s, ok) = %s ranks BELOW PremiseFloor(%s)=%s alone -- raise-only violated",
							r.name, cov, prem, got, prem, premFloor)
					}

					want := composeExpected(r.wantBaseline, covFloor, premFloor)
					if got != want {
						t.Errorf("ComputeShippable(%s, %s, %s, ok) = %s, want %s", r.name, cov, prem, got, want)
					}
				})
			}
		}
	}
}

// TestComputeShippable_AdequacyRaiseOnly is AdequacyFloor's own dedicated
// raise-only proof (§26.2/Step 67), isolated from every other input:
// risk/coverage/premise are held at their cleanest legal values (low/
// adequate/ok, baseline ShippableAuto) so each row isolates exactly what
// the description-adequacy floor alone contributes, exhaustively over
// every DescriptionAdequacy value under test
// (ok/drift/misleading/zero/unrecognized) -- mirrors
// TestComputeShippable_RiskBaseline's own identical "isolate one input,
// hold the others clean" shape.
func TestComputeShippable_AdequacyRaiseOnly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		adequacy review.DescriptionAdequacy
		want     review.Shippable
	}{
		{"ok", review.DescriptionAdequacyOK, review.ShippableAuto},
		{"drift", review.DescriptionAdequacyDrift, review.ShippableAuto},
		{"misleading raises to needs_human", review.DescriptionAdequacyMisleading, review.ShippableNeedsHuman},
		{"zero value fails conservative (needs_human, matching misleading)", review.DescriptionAdequacy(""), review.ShippableNeedsHuman},
		{"unrecognized value fails conservative (needs_human, matching misleading)", review.DescriptionAdequacy("bogus"), review.ShippableNeedsHuman},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			adeqFloor := review.AdequacyFloor(tc.adequacy)
			got := review.ComputeShippable(review.RiskLevelLow, review.TestsCoverageStateAdequate, review.PremiseStateOK, tc.adequacy)

			if rank(got) < rank(adeqFloor) {
				t.Errorf("ComputeShippable(low, adequate, ok, %s) = %s ranks BELOW AdequacyFloor(%s)=%s alone -- raise-only violated",
					tc.adequacy, got, tc.adequacy, adeqFloor)
			}
			if got != tc.want {
				t.Errorf("ComputeShippable(low, adequate, ok, %s) = %s, want %s", tc.adequacy, got, tc.want)
			}
		})
	}
}

// TestComputeShippable_MisleadingRaisesShippable is the single, explicit
// pin this Step's own process requirements name by phrase: "the floor
// actually raising Shippable on misleading". A misleading description
// alone (everything else at its cleanest: low risk, adequate coverage, ok
// premise -- which alone would compute ShippableAuto) raises the RESULT
// to ShippableNeedsHuman, never leaving it at the clean baseline.
func TestComputeShippable_MisleadingRaisesShippable(t *testing.T) {
	t.Parallel()

	clean := review.ComputeShippable(review.RiskLevelLow, review.TestsCoverageStateAdequate, review.PremiseStateOK, review.DescriptionAdequacyOK)
	if clean != review.ShippableAuto {
		t.Fatalf("sanity check failed: an all-clean verdict computed %s, want %s (test setup is broken, not the property under test)", clean, review.ShippableAuto)
	}

	misleading := review.ComputeShippable(review.RiskLevelLow, review.TestsCoverageStateAdequate, review.PremiseStateOK, review.DescriptionAdequacyMisleading)
	if misleading != review.ShippableNeedsHuman {
		t.Errorf("ComputeShippable(low, adequate, ok, misleading) = %s, want %s (misleading must raise Shippable off an otherwise-clean auto baseline)", misleading, review.ShippableNeedsHuman)
	}
}
