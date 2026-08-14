package reviewverdict

// This file implements §21.1's own "filesChanged drift canary": the
// comparison paragraph that section names but explicitly declines to
// wire up ("two numbers stand for one fact, with opposite provenance and
// no comparison between them ... recorded as a known, accepted gap
// rather than an oversight"), plus the two constraints that same
// paragraph states are "load-bearing should that comparison ever be
// wired as a drift signal." This Step is that wiring.
//
// review.Verdict.FilesChanged (verdict.go, that package's own doc
// comment: "Purely descriptive data in this package") is the reviewing
// agent's OWN self-report of how many files its diff touched.
// reviewtriage.DecisionRecord.ChangedFilesCount (record.go) is the SAME
// quantity, computed server-side at turn-creation time from GitHub's own
// "changed_files" integer on the SAME GetPullRequest call the review-
// context fetch already makes (review.PreFetchedContext.ChangedFilesCount,
// context.go) -- carried forward to verdict-post time via
// turns.review_depth_decision, the one existing per-turn carrier that
// already threads a value from context-fetch time to verdict-post time
// (DecisionRecord.ChangedFilesCount's own doc comment covers why this
// rides that carrier rather than a new turns column).
//
// FilesChangedDrifted (below) is the ONE comparison this file performs.
// Its caller (httpapi.PostReviewVerdict) is responsible for §21.1's first
// load-bearing constraint by construction, not by convention: this
// function returns a plain bool, never a Verdict, a Shippable, or
// anything else that could be assigned back onto the record being
// posted -- there is no parameter here through which a caller COULD feed
// this result back into the verdict even by mistake. A caller is expected
// to log/observe a true result, nothing more.

// FilesChangedDriftRatioThreshold and FilesChangedDriftAbsoluteThreshold
// are the two thresholds FilesChangedDrifted (below) requires TOGETHER
// before it reports drift -- named, documented constants rather than a
// literal buried at the comparison site, so a future tuning pass changes
// exactly one place and this doc comment's own reasoning travels with it.
//
// Neither threshold alone is safe on its own, which is why both are
// required (see FilesChangedDrifted's own "&&", never "||"):
//
//   - A RATIO alone is noisy on a small PR: a 1-file self-report against
//     a 2-file server count is 100% off by ratio, yet the absolute
//     difference (a single file) is well within what an honest reviewer
//     could plausibly round, miscount, or describe loosely in a
//     free-text-adjacent field.
//   - An ABSOLUTE COUNT alone is noisy on a large PR: a self-reported
//     495 against a server-computed 500 is a 5-file absolute difference
//     on a 500-file PR -- 1% relative error, well within what "the
//     reviewer looked at substantially the whole diff" comfortably
//     covers, yet 5 files alone would trip a naive absolute-only rule
//     tuned for a typical small PR.
//
// Requiring both together is what makes a fired canary worth a second
// look on PRs of any size, rather than a constant background hum an
// operator learns to ignore (this file's own top comment: a canary that
// cries wolf is strictly worse than not having built it).
//
// 0.5 (50%) and 5 (files) are this Step's own proposed starting values,
// in the same spirit as reviewtriage's own "v1 rules (initial
// thresholds)" (doc.go) -- deliberately conservative (requires a LARGE
// divergence, in both senses at once, before firing) so this Step ships
// a genuinely low-noise signal rather than one calibrated after the fact
// against data that does not exist yet. Revisiting either number is a
// plausible later refinement once this canary has real firing data to
// tune against -- not designed here, mirroring §26.3's own identical
// "start conservative, tune from telemetry" posture for its light/deep
// thresholds.
const (
	FilesChangedDriftRatioThreshold    = 0.5
	FilesChangedDriftAbsoluteThreshold = 5
)

// FilesChangedDrifted reports whether selfReported (review.Verdict.
// FilesChanged, the reviewing agent's own self-report) and serverComputed
// (reviewtriage.DecisionRecord.ChangedFilesCount, this SAME review turn's
// own server-computed count) have diverged enough, by BOTH
// FilesChangedDriftRatioThreshold AND FilesChangedDriftAbsoluteThreshold
// at once (that constant's own doc comment for why neither alone is
// sufficient), to be worth a diagnostic flag that a verdict may have been
// produced against a diff its reviewer did not actually read in full
// (§21.1's own framing for why this divergence is "the cheapest signal
// available anywhere in this design" for that failure mode).
//
// serverComputed <= 0 ALWAYS returns false, unconditionally, before
// either threshold is even evaluated -- §21.1's own second load-bearing
// constraint, stated there in exactly these terms: "it must tolerate
// ChangedFilesCount == 0, which §26.3 documents as indistinguishable from
// a genuinely empty diff whenever GetPullRequest fails: a canary that
// reads zero as truth fires on every transient GitHub fault." A
// serverComputed of exactly 0 is ambiguous by construction (review.
// PreFetchedContext.ChangedFilesCount's own doc comment: "0 for a failed
// GetPullRequest fetch, indistinguishable from a genuinely empty diff")
// and DecisionRecord.ChangedFilesCount carries that SAME ambiguity
// forward (its own doc comment: also 0 for a turn that predates this
// field, or whose own marshal step failed) -- treating either case as
// "confidently zero, so ANY non-zero self-report is 100% drift" would
// make this canary fire on routine degradation, not genuine divergence,
// which is exactly the "cries wolf" failure this function exists to
// avoid, never to cause. selfReported has no equivalent floor: a
// self-reported 0 against a real, positive serverComputed count is
// exactly the kind of divergence this canary exists to catch (a reviewer
// that claims to have touched nothing on a PR that plainly changed
// files), so it is evaluated normally, the same as any other value.
//
// Diagnostic-only by construction, not merely by convention: see this
// file's own top comment for why a bool return, with no verdict/Shippable
// parameter anywhere in this signature, makes "never edit the verdict,
// never move a risk level, never fail the request" (§21.1's own first
// load-bearing constraint) structurally true of every caller, not merely
// a discipline every caller has to remember to uphold.
func FilesChangedDrifted(selfReported, serverComputed int) bool {
	if serverComputed <= 0 {
		return false
	}

	delta := selfReported - serverComputed
	if delta < 0 {
		delta = -delta
	}
	if delta < FilesChangedDriftAbsoluteThreshold {
		return false
	}

	ratio := float64(delta) / float64(serverComputed)
	return ratio >= FilesChangedDriftRatioThreshold
}
