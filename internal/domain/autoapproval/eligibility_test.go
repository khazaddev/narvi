package autoapproval_test

import (
	"testing"

	"github.com/khazaddev/narvi/internal/domain/autoapproval"
	"github.com/khazaddev/narvi/internal/domain/review"
)

// cleanVerdict is a Shippable == auto verdict -- the one baseline every
// table case below starts from, mutating EXACTLY one field/input at a
// time, so a failing case proves that ONE criterion and no other (this
// file's own mutation-testing discipline, matching the old
// internal/domain/decisioninbox/eligibility_test.go this package
// replaces). FilesChanged/BlastRadius are still populated here, matching
// what a real posted verdict looks like, but -- §62 review finding C1
// (CRITICAL, fixed) -- ComputeEligible never reads either field anymore;
// see TestComputeEligible_IgnoresModelSelfReportedFilesChangedAndBlastRadius
// below for the test that pins exactly that.
func cleanVerdict() review.Verdict {
	return review.Verdict{
		RiskLevel:         review.RiskLevelLow,
		Premise:           review.PremiseStateOK,
		BlastRadius:       []review.Tag{review.TagPublicAPI}, // display data only -- see doc comment above
		FilesChanged:      5,                                 // display data only -- see doc comment above
		TestsCoverage:     review.TestsCoverageStateAdequate,
		DocsDrift:         review.DocsDriftStateNone,
		ProposedShippable: review.ProposedShippableAuto,
		Shippable:         review.ShippableAuto,
	}
}

// cleanInput's ChangedFileCount/TouchedBlastRadius are the §62 review
// finding C1 fix's own two SERVER-DERIVED fields -- deliberately set to
// the SAME small/non-sensitive shape cleanVerdict's own (now-inert)
// FilesChanged/BlastRadius already modeled, so this baseline's own
// "eligible" outcome means the same thing it always did, just gated on
// the correct fields now.
func cleanInput() autoapproval.EligibilityInput {
	return autoapproval.EligibilityInput{
		Verdict:            cleanVerdict(),
		VerdictHeadSHA:     "abc123",
		CurrentHeadSHA:     "abc123",
		CIGreen:            true,
		HasNeedsHumanLabel: false,
		ChangedFileCount:   5,
		TouchedBlastRadius: []review.Tag{review.TagPublicAPI}, // present, but NOT in the default sensitive list
	}
}

func TestComputeEligible(t *testing.T) {
	t.Parallel()

	cfg := autoapproval.DefaultEligibilityConfig()

	tests := []struct {
		name         string
		in           autoapproval.EligibilityInput
		cfg          autoapproval.EligibilityConfig
		wantEligible bool
		wantReason   autoapproval.Reason
	}{
		{
			name:         "a clean, low-risk, fresh, CI-green verdict is eligible",
			in:           cleanInput(),
			cfg:          cfg,
			wantEligible: true,
			wantReason:   autoapproval.ReasonNone,
		},

		// --- criterion 1: the needs-human escape hatch, checked FIRST,
		// regardless of every other field's value ---
		{
			name:         "needs-human label overrides an otherwise-eligible verdict",
			in:           withNeedsHuman(cleanInput(), true),
			cfg:          cfg,
			wantEligible: false,
			wantReason:   autoapproval.ReasonNeedsHumanLabel,
		},

		// --- criterion 2: the stale-head-SHA guard ---
		{
			name:         "a verdict head sha that differs from the current head sha is stale",
			in:           withCurrentHeadSHA(cleanInput(), "def456"),
			cfg:          cfg,
			wantEligible: false,
			wantReason:   autoapproval.ReasonStaleVerdict,
		},
		{
			name:         "an empty verdict head sha is stale (never treated as a wildcard match)",
			in:           withVerdictHeadSHA(cleanInput(), ""),
			cfg:          cfg,
			wantEligible: false,
			wantReason:   autoapproval.ReasonStaleVerdict,
		},
		{
			// Isolates the empty-string clause from the inequality clause
			// next to it: with a NON-empty CurrentHeadSHA (every other case
			// in this table), "" != CurrentHeadSHA is ALREADY true, so the
			// inequality clause alone would independently catch an empty
			// VerdictHeadSHA regardless of the dedicated empty-string
			// check -- proven by mutation testing (deleting the
			// `in.VerdictHeadSHA == ""` clause left every other case in
			// this table still green). This case sets CurrentHeadSHA to ""
			// too, so ONLY the dedicated empty-string check -- never a
			// same-value coincidence -- can still refuse a verdict that
			// never recorded a real head sha in the first place.
			name: "both head shas empty is still stale, never read as a trivially-matching pair",
			in: func() autoapproval.EligibilityInput {
				in := withVerdictHeadSHA(cleanInput(), "")
				return withCurrentHeadSHA(in, "")
			}(),
			cfg:          cfg,
			wantEligible: false,
			wantReason:   autoapproval.ReasonStaleVerdict,
		},

		// --- criterion 3: CI green at the CURRENT head ---
		{
			name:         "CI not green at head is never eligible",
			in:           withCIGreen(cleanInput(), false),
			cfg:          cfg,
			wantEligible: false,
			wantReason:   autoapproval.ReasonCINotGreen,
		},

		// --- criterion 4: Shippable == auto, exercised via three
		// DISTINCT, independently-meaningful ways Shippable can fail to
		// be auto (risk baseline, coverage floor, premise floor) -- see
		// doc.go for why this ONE check is "no floor raised" in full,
		// never a redundant separate branch ---
		{
			name: "risk baseline alone (medium) prevents shippable=auto",
			in: withVerdict(cleanInput(), func(v review.Verdict) review.Verdict {
				v.RiskLevel = review.RiskLevelMedium
				v.Shippable = review.ComputeShippable(v.RiskLevel, v.TestsCoverage, v.Premise, review.DescriptionAdequacyOK, review.CounterReviewDone)
				return v
			}),
			cfg:          cfg,
			wantEligible: false,
			wantReason:   autoapproval.ReasonNotShippableAuto,
		},
		{
			name: "the coverage floor alone (insufficient) prevents shippable=auto",
			in: withVerdict(cleanInput(), func(v review.Verdict) review.Verdict {
				v.TestsCoverage = review.TestsCoverageStateInsufficient
				v.Shippable = review.ComputeShippable(v.RiskLevel, v.TestsCoverage, v.Premise, review.DescriptionAdequacyOK, review.CounterReviewDone)
				return v
			}),
			cfg:          cfg,
			wantEligible: false,
			wantReason:   autoapproval.ReasonNotShippableAuto,
		},
		{
			name: "the premise floor alone (questionable) prevents shippable=auto",
			in: withVerdict(cleanInput(), func(v review.Verdict) review.Verdict {
				v.Premise = review.PremiseStateQuestionable
				v.Shippable = review.ComputeShippable(v.RiskLevel, v.TestsCoverage, v.Premise, review.DescriptionAdequacyOK, review.CounterReviewDone)
				return v
			}),
			cfg:          cfg,
			wantEligible: false,
			wantReason:   autoapproval.ReasonNotShippableAuto,
		},

		// --- criterion 5: diff size under the configured threshold --
		// §62 review finding C1: now gated on ChangedFileCount (the
		// server-fetched fact), never Verdict.FilesChanged. ---
		{
			name:         "a diff exceeding the configured file-count threshold is not eligible",
			in:           withChangedFileCount(cleanInput(), cfg.MaxFilesChanged+1),
			cfg:          cfg,
			wantEligible: false,
			wantReason:   autoapproval.ReasonDiffTooLarge,
		},
		{
			name:         "a diff exactly AT the configured threshold is still eligible (inclusive boundary)",
			in:           withChangedFileCount(cleanInput(), cfg.MaxFilesChanged),
			cfg:          cfg,
			wantEligible: true,
			wantReason:   autoapproval.ReasonNone,
		},

		// --- criterion 6: no sensitive path touched -- §62 review finding
		// C1: now gated on TouchedBlastRadius (the server-DERIVED fact,
		// autoapproval.ClassifyChangedPaths over the PR's real changed
		// files), never Verdict.BlastRadius. ---
		{
			name:         "a touched-blast-radius tag in the configured sensitive list is not eligible",
			in:           withTouchedBlastRadius(cleanInput(), []review.Tag{review.TagAuth}),
			cfg:          cfg,
			wantEligible: false,
			wantReason:   autoapproval.ReasonSensitivePathTouched,
		},
		{
			name:         "a touched-blast-radius tag NOT in the configured sensitive list stays eligible",
			in:           withTouchedBlastRadius(cleanInput(), []review.Tag{review.TagDependencies}),
			cfg:          cfg,
			wantEligible: true,
			wantReason:   autoapproval.ReasonNone,
		},
		{
			name:         "a custom per-repo sensitive list is honored over the built-in default",
			in:           withTouchedBlastRadius(cleanInput(), []review.Tag{review.TagDependencies}),                                                // not in the DEFAULT list...
			cfg:          autoapproval.EligibilityConfig{MaxFilesChanged: cfg.MaxFilesChanged, SensitiveTags: []review.Tag{review.TagDependencies}}, // ...but IS in this repo's own custom list
			wantEligible: false,
			wantReason:   autoapproval.ReasonSensitivePathTouched,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotEligible, gotReason := autoapproval.ComputeEligible(tc.in, tc.cfg)
			if gotEligible != tc.wantEligible {
				t.Errorf("ComputeEligible(...) eligible = %v, want %v", gotEligible, tc.wantEligible)
			}
			if gotReason != tc.wantReason {
				t.Errorf("ComputeEligible(...) reason = %q, want %q", gotReason, tc.wantReason)
			}
		})
	}
}

// TestComputeEligible_IgnoresModelSelfReportedFilesChangedAndBlastRadius is
// the C1 regression test (§62 review, CRITICAL, fixed) at the pure-function
// level: this is the exact attack the reviewers verified reproducible --
// a reviewing agent posts a verdict with a tiny self-reported FilesChanged
// and an empty self-reported BlastRadius (so Shippable legitimately
// computes to auto), while ChangedFileCount/TouchedBlastRadius -- the
// SERVER-DERIVED facts a caller populates from the PR's real
// ports.OpenPR -- tell a completely different story: a 300-file diff
// touching a sensitive path. Both must independently refuse eligibility,
// proving the engine no longer trusts the model's own self-report for
// either criterion.
func TestComputeEligible_IgnoresModelSelfReportedFilesChangedAndBlastRadius(t *testing.T) {
	t.Parallel()

	cfg := autoapproval.DefaultEligibilityConfig()

	lyingVerdict := review.Verdict{
		RiskLevel:         review.RiskLevelLow,
		Premise:           review.PremiseStateOK,
		TestsCoverage:     review.TestsCoverageStateAdequate,
		DocsDrift:         review.DocsDriftStateNone,
		ProposedShippable: review.ProposedShippableAuto,
		FilesChanged:      1,   // LIE: the model claims a tiny, one-file diff...
		BlastRadius:       nil, // ...and claims it touches nothing sensitive at all.
	}
	lyingVerdict.Shippable = review.ComputeShippable(lyingVerdict.RiskLevel, lyingVerdict.TestsCoverage, lyingVerdict.Premise, review.DescriptionAdequacyOK, review.CounterReviewDone)
	if lyingVerdict.Shippable != review.ShippableAuto {
		t.Fatalf("test setup: lyingVerdict.Shippable = %v, want auto (the attack's own premise is that the server-computed Shippable legitimately computes to auto)", lyingVerdict.Shippable)
	}

	t.Run("diff size: real 300-file PR refuses despite a lying low FilesChanged", func(t *testing.T) {
		t.Parallel()
		in := autoapproval.EligibilityInput{
			Verdict:            lyingVerdict,
			VerdictHeadSHA:     "sha-under-attack",
			CurrentHeadSHA:     "sha-under-attack",
			CIGreen:            true,
			HasNeedsHumanLabel: false,
			ChangedFileCount:   300, // TRUTH: GitHub itself reports 300 changed files.
			TouchedBlastRadius: nil,
		}
		eligible, reason := autoapproval.ComputeEligible(in, cfg)
		if eligible {
			t.Fatal("ComputeEligible(...) eligible = true, want false -- a real 300-file diff must refuse regardless of what the model itself self-reported")
		}
		if reason != autoapproval.ReasonDiffTooLarge {
			t.Errorf("reason = %q, want %q", reason, autoapproval.ReasonDiffTooLarge)
		}
	})

	t.Run("sensitive path: real migrations+authz paths refuse despite a lying empty BlastRadius", func(t *testing.T) {
		t.Parallel()
		in := autoapproval.EligibilityInput{
			Verdict:            lyingVerdict,
			VerdictHeadSHA:     "sha-under-attack",
			CurrentHeadSHA:     "sha-under-attack",
			CIGreen:            true,
			HasNeedsHumanLabel: false,
			ChangedFileCount:   1, // small, so this subtest isolates the sensitive-path criterion specifically
			// TRUTH: autoapproval.ClassifyChangedPaths, applied to the PR's
			// real changed files, found migrations + authz touched -- see
			// TestRevalidateForMerge_LyingVerdictAgainstReal300FileSensitivePR
			// (internal/app/decisioninbox) for this SAME attack proven at
			// the full integration level, through the real classifier
			// over real ports.OpenPR.ChangedFiles rather than a
			// pre-computed TouchedBlastRadius literal.
			TouchedBlastRadius: []review.Tag{review.TagMigrations, review.TagAuth},
		}
		eligible, reason := autoapproval.ComputeEligible(in, cfg)
		if eligible {
			t.Fatal("ComputeEligible(...) eligible = true, want false -- a real sensitive-path touch must refuse regardless of what the model itself self-reported")
		}
		if reason != autoapproval.ReasonSensitivePathTouched {
			t.Errorf("reason = %q, want %q", reason, autoapproval.ReasonSensitivePathTouched)
		}
	})

	t.Run("conversely: a huge lying FilesChanged/sensitive lying BlastRadius do NOT block an actually-small/clean PR", func(t *testing.T) {
		t.Parallel()
		// The other direction of the SAME fix: Verdict.FilesChanged/
		// BlastRadius must be fully INERT, not just "insufficient on their
		// own to approve" -- a model that (for whatever reason)
		// over-reports risk must not be able to block a genuinely small,
		// clean PR either. Confirms the fields are ignored, not merely
		// overridden-when-worse.
		overReportingVerdict := lyingVerdict
		overReportingVerdict.FilesChanged = 9999
		overReportingVerdict.BlastRadius = []review.Tag{review.TagAuth, review.TagMigrations, review.TagSecrets}

		in := autoapproval.EligibilityInput{
			Verdict:            overReportingVerdict,
			VerdictHeadSHA:     "sha-clean",
			CurrentHeadSHA:     "sha-clean",
			CIGreen:            true,
			HasNeedsHumanLabel: false,
			ChangedFileCount:   1,
			TouchedBlastRadius: nil,
		}
		eligible, reason := autoapproval.ComputeEligible(in, cfg)
		if !eligible {
			t.Errorf("ComputeEligible(...) eligible = false (reason %q), want true -- Verdict.FilesChanged/BlastRadius must be fully inert, never gate anything in either direction", reason)
		}
	})
}

// TestDefaultEligibilityConfig_ReturnsFreshSlice pins that
// DefaultSensitiveTags never hands back a slice two callers could
// mutate into affecting each other -- a caller appending to (or
// overwriting an element of) its own returned slice must never leak into
// a LATER, independent call's own result.
func TestDefaultEligibilityConfig_ReturnsFreshSlice(t *testing.T) {
	t.Parallel()

	first := autoapproval.DefaultSensitiveTags()
	first[0] = review.Tag("tampered")

	second := autoapproval.DefaultSensitiveTags()
	if second[0] == review.Tag("tampered") {
		t.Fatalf("DefaultSensitiveTags: mutating one call's result affected a later call -- want an independent slice each time")
	}
}

func withNeedsHuman(in autoapproval.EligibilityInput, v bool) autoapproval.EligibilityInput {
	in.HasNeedsHumanLabel = v
	return in
}
func withCIGreen(in autoapproval.EligibilityInput, v bool) autoapproval.EligibilityInput {
	in.CIGreen = v
	return in
}
func withCurrentHeadSHA(in autoapproval.EligibilityInput, sha string) autoapproval.EligibilityInput {
	in.CurrentHeadSHA = sha
	return in
}
func withVerdictHeadSHA(in autoapproval.EligibilityInput, sha string) autoapproval.EligibilityInput {
	in.VerdictHeadSHA = sha
	return in
}
func withVerdict(in autoapproval.EligibilityInput, mutate func(review.Verdict) review.Verdict) autoapproval.EligibilityInput {
	in.Verdict = mutate(in.Verdict)
	return in
}
func withChangedFileCount(in autoapproval.EligibilityInput, n int) autoapproval.EligibilityInput {
	in.ChangedFileCount = n
	return in
}
func withTouchedBlastRadius(in autoapproval.EligibilityInput, tags []review.Tag) autoapproval.EligibilityInput {
	in.TouchedBlastRadius = tags
	return in
}
