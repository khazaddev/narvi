package autoapproval

import "github.com/khazaddev/narvi/internal/domain/review"

// defaultMaxFilesChanged is DefaultEligibilityConfig's own diff-size
// threshold when a repo has never configured one (repo_settings.
// max_auto_approve_files_changed IS NULL) -- chosen the same way this
// codebase's other "not specified, chosen conservatively" defaults are
// (platform/timeouts.go's own established convention for an unspecified
// figure): small enough that a PR this size is plausible to have been
// reviewed thoroughly in one pass, generous enough not to exclude the
// common case of a focused, single-purpose change touching a handful of
// files. An admin can raise or lower this per repo once real calibration
// data (the contradiction-rate read model, internal/domain/reviewverdict)
// justifies a different figure -- this default is a starting point, never
// a claimed-correct number.
const defaultMaxFilesChanged = 20

// DefaultSensitiveTags is DefaultEligibilityConfig's own sensitive-path
// tag list -- §21.2's own named defaults, verbatim: "migrations, auth
// code, /contracts by default". Returned as a fresh slice on every call
// (never a shared package-level slice a caller could mutate) -- mirrors
// this codebase's own defensive-copy convention wherever a package
// exposes a slice a caller might otherwise be tempted to append to
// in-place.
func DefaultSensitiveTags() []review.Tag {
	return []review.Tag{review.TagMigrations, review.TagAuth, review.TagContracts}
}

// EligibilityConfig is the per-repo-configurable half of §21.2's
// criteria list -- sourced from repo_settings (internal/app/reviewverdict's
// own LoadEligibilityConfig resolves a missing/NULL column to
// DefaultEligibilityConfig's own values, fail-CONSERVATIVE in the sense
// that follows from those defaults being deliberately narrow, never
// fail-open via an accidentally-unbounded threshold or empty tag list).
type EligibilityConfig struct {
	// MaxFilesChanged is the diff-size threshold (§21.2: "diff size
	// under a configurable-per-repo threshold") -- compared against
	// EligibilityInput.ChangedFileCount (the
	// server-fetched fact, never Verdict.FilesChanged).
	MaxFilesChanged int
	// SensitiveTags is the configurable-per-repo sensitive-path list
	// (§21.2: "no sensitive path touched -- configurable per repo").
	// Compared against EligibilityInput.TouchedBlastRadius: ANY overlap
	// disqualifies (the server-DERIVED fact,
	// never Verdict.BlastRadius).
	SensitiveTags []review.Tag
}

// DefaultEligibilityConfig is the engine's own built-in default --
// applied whenever a repo has not configured EligibilityConfig's own
// fields (internal/app/reviewverdict.LoadEligibilityConfig's own doc
// comment).
func DefaultEligibilityConfig() EligibilityConfig {
	return EligibilityConfig{
		MaxFilesChanged: defaultMaxFilesChanged,
		SensitiveTags:   DefaultSensitiveTags(),
	}
}

// EligibilityInput is ComputeEligible's own input -- every field is
// already-fetched data (never re-fetched by this pure function itself,
// per this package's own no-I/O doc comment).
type EligibilityInput struct {
	// Verdict is the LATEST posted review.Verdict for this PR
	// (review_verdicts' own DISTINCT ON (repo, pr_number) ... ORDER BY
	// created_at DESC reduction, §21.1). ONLY Verdict.Shippable is ever
	// consulted by ComputeEligible below: Verdict.FilesChanged and Verdict.BlastRadius are
	// BOTH the reviewing LLM's own self-report, typed into its own POST
	// body (restdtos.PostReviewVerdictRequest) alongside the untrusted PR
	// diff §5.2 explicitly classifies as untrusted content -- a reviewing
	// agent that posts riskLevel=low/premise=ok/testsCoverage=adequate
	// (so Shippable legitimately computes to auto) plus a LOW
	// filesChanged and an EMPTY blastRadius, regardless of the PR's real
	// shape, used to sail through both criteria below unconditionally.
	// ChangedFileCount/TouchedBlastRadius (below) are this fix: the SAME
	// two criteria, now gated on facts GitHub itself reports. Verdict.
	// FilesChanged/BlastRadius remain readable on this struct purely as
	// DISPLAY/audit data for a caller that wants to show what the model
	// itself claimed -- ComputeEligible never reads either.
	Verdict review.Verdict
	// VerdictHeadSHA is review_verdicts.head_sha for Verdict above --
	// the commit Verdict was actually produced against.
	VerdictHeadSHA string
	// ChangedFileCount is this PR's own CURRENT, server-fetched
	// changed-file count -- ports.OpenPR.
	// ChangedFilesCount, GitHub's own authoritative "changed_files"
	// scalar, NEVER Verdict.FilesChanged. Compared against
	// EligibilityConfig.MaxFilesChanged (§21.2: "diff size under a
	// configurable-per-repo threshold").
	//
	// Phase 5 audit finding 2 (fixed): deliberately NOT len(ports.OpenPR.
	// ChangedFiles) -- githubapi.fetchChangedFilePaths caps that listing
	// at one page (per_page=100, no pagination), so len() silently
	// UNDERCOUNTS any PR larger than that page. A PR author fully
	// controls both filenames and diff order (both attacker-
	// influenceable), so a large, sensitive diff could otherwise be
	// padded past the page boundary to report as "100 files" regardless
	// of its real size. ports.OpenPR.ChangedFilesCount is GitHub's own
	// separately-reported scalar total, never subject to this cap --
	// see that field's own doc comment for why it is unconditionally
	// reliable whenever an OpenPR was built at all.
	//
	// Chosen over Additions+Deletions (ports.OpenPR's OTHER diff-size
	// signal, also server-fetched): EligibilityConfig.MaxFilesChanged is
	// already named and shaped as a FILE-COUNT threshold (defaultMaxFilesChanged's
	// own doc comment, this same package: "a PR this size is plausible to
	// have been reviewed thoroughly in one pass... touching a handful of
	// files") -- §21.2's own "diff size" text does not mandate a line-count
	// interpretation over a file-count one, and switching the MEANING of an
	// already-shipped, already-repo-configurable threshold field (rather
	// than adding a new one) would silently reinterpret every repo's own
	// already-configured number. A future Step is free to ALSO gate on
	// Additions+Deletions via a new, separately-named config field --
	// this fix deliberately does not invent one un-asked-for.
	ChangedFileCount int
	// TouchedBlastRadius is this PR's own CURRENT, server-DERIVED blast
	// radius -- autoapproval.ClassifyChangedPaths
	// applied to ports.OpenPR.ChangedFiles (the SAME server-fetched
	// listing, though -- Phase 5 audit finding 2 -- that listing is
	// capped at one GitHub page and is NOT what ChangedFileCount above
	// is derived from anymore; see that field's own doc comment), NEVER
	// Verdict.BlastRadius. Compared against EligibilityConfig.
	// SensitiveTags: ANY overlap disqualifies, exactly like Verdict.
	// BlastRadius used to gate before this fix -- only the SOURCE of the
	// tags changed, not the comparison itself.
	//
	// TouchedBlastRadiusKnown MUST be true for this field to be trusted
	// at all -- see that field's own doc comment immediately below.
	TouchedBlastRadius []review.Tag
	// TouchedBlastRadiusKnown (Phase 5 audit findings 1+2, both fixed)
	// reports whether TouchedBlastRadius above reflects a COMPLETE
	// classification of this PR's real changed-file paths, as opposed to
	// one derived from an absent or page-truncated listing. A caller
	// populates this from !ports.OpenPR.ChangedFilesListDegraded -- see
	// that field's own doc comment for the two independent ways the
	// underlying GitHub fetch can leave TouchedBlastRadius incomplete
	// (a failed fetch, or a genuinely large diff whose Pull Request
	// Files listing was truncated at GitHub's own one-page cap).
	//
	// Deliberately the BOOLEAN ZERO VALUE for "unknown", never for
	// "known" -- mirrors this package's own established fail-conservative
	// convention (every enum in internal/domain/review defaults its
	// unset zero value to the WORST legitimate case, doc.go's own
	// "Fail-conservative policy for every closed enum" section) applied
	// here to a plain bool: a caller that constructs an EligibilityInput
	// and simply FORGETS to set this field gets false ("unknown, fail
	// closed"), never true ("confirmed clean") by accident. This is
	// exactly the C1 hole Phase 5 found again,
	// one layer down: before this fix, EligibilityInput's own
	// ChangedFileCount==0/TouchedBlastRadius==nil zero values WERE
	// themselves the permissive end of both criteria, so a swallowed
	// fetch error (githubapi's own filesErr != nil -> files = nil)
	// silently reached ComputeEligible looking identical to a genuinely
	// empty, confirmed-clean diff. This field exists so that ambiguity
	// can never recur: "confirmed empty" (TouchedBlastRadiusKnown=true,
	// TouchedBlastRadius=nil) and "could not be established"
	// (TouchedBlastRadiusKnown=false) are now two DISTINCT, explicitly-
	// represented states, never collapsed into one by a caller's
	// omission.
	TouchedBlastRadiusKnown bool
	// CurrentHeadSHA is the PR's LIVE current head SHA, fetched fresh
	// (never cached -- see internal/app/decisioninbox/revalidate.go's
	// own "the whole point of this function is a fresh read" doc
	// comment, which this engine's own stale-verdict guard depends on
	// exactly as much as the rest of that function does).
	CurrentHeadSHA string
	// CIGreen is the PR's CI conclusion at CurrentHeadSHA specifically
	// (never at VerdictHeadSHA, which may already be stale) -- re-
	// derived live via the STRICT ports.CIConclusion check
	// (githubapi.fetchCIConclusionLive: an incomplete or cancelled
	// check is never green).
	CIGreen bool
	// HasNeedsHumanLabel is reviewpost.LabelNeedsHuman's own current
	// presence on the PR -- §21.2's escape hatch, unconditional and
	// checked first, regardless of every other field's value.
	HasNeedsHumanLabel bool
}

// Reason is ComputeEligible's own short, human-readable explanation for
// an ineligible verdict -- one fixed string per criterion, suitable for
// a 409 response body or a log line (mirrors internal/app/decisioninbox/
// revalidate.go's own existing RevalidateForMerge reason convention).
// Not a Go error: this is a normal, expected domain OUTCOME (a PR simply
// not (yet) qualifying), never a failure this package failed to handle.
type Reason string

// The Reason values ComputeEligible returns -- ReasonNone accompanies
// eligible=true; every other value accompanies eligible=false and names
// exactly which criterion failed.
const (
	ReasonNone                 Reason = ""
	ReasonNeedsHumanLabel      Reason = "review:needs-human label is present"
	ReasonStaleVerdict         Reason = "the verdict relied on was produced against an earlier commit"
	ReasonCINotGreen           Reason = "CI is not green at the current head"
	ReasonNotShippableAuto     Reason = "the verdict's shippable classification is not auto"
	ReasonDiffTooLarge         Reason = "the diff exceeds this repo's auto-approval file-count threshold"
	ReasonBlastRadiusUnknown   Reason = "the diff's sensitive-path facts could not be established from GitHub"
	ReasonSensitivePathTouched Reason = "the diff touches a sensitive path"
)

// ComputeEligible is this package's single exported pure function
// (mirrors internal/domain/review's own "exactly three exported
// functions" discipline, and internal/domain/sentinelfix.
// EvaluateMergeGate's own single-function shape) -- see doc.go for the
// full criteria list, their order, and why "no floor raised" is not a
// separate check of its own.
func ComputeEligible(in EligibilityInput, cfg EligibilityConfig) (eligible bool, reason Reason) {
	if in.HasNeedsHumanLabel {
		return false, ReasonNeedsHumanLabel
	}
	if in.VerdictHeadSHA == "" || in.VerdictHeadSHA != in.CurrentHeadSHA {
		return false, ReasonStaleVerdict
	}
	if !in.CIGreen {
		return false, ReasonCINotGreen
	}
	if in.Verdict.Shippable != review.ShippableAuto {
		return false, ReasonNotShippableAuto
	}
	// both checks below now read
	// server-derived facts (EligibilityInput's own doc comment) -- never
	// in.Verdict.FilesChanged/in.Verdict.BlastRadius, which are the
	// reviewing model's own self-report and were the exploited hole.
	//
	// The size check runs FIRST, on in.ChangedFileCount alone (Phase 5
	// audit finding 2: this is now GitHub's own authoritative scalar,
	// EligibilityInput.ChangedFileCount's own doc comment -- always
	// reliable independent of whether the SEPARATE changed-file path
	// LISTING below was ever successfully or completely fetched). A PR
	// already over threshold on this fact alone is refused here, before
	// this function ever needs to ask whether TouchedBlastRadius is even
	// trustworthy -- deliberate ordering, not merely incidental: on the
	// common case (a repo's own MaxFilesChanged at or below GitHub's
	// 100-per-page cap, e.g. this package's own defaultMaxFilesChanged of
	// 20), any diff large enough to have a truncated path listing at all
	// is already disqualified right here, so the sensitive-path check
	// below never even has to reason about a partial listing for that
	// PR. This ordering is a genuine efficiency/clarity win, but it is
	// NOT what makes the check below safe -- a repo free to configure
	// MaxFilesChanged above 100 could still have a diff that clears THIS
	// check yet whose path listing is truncated, which is exactly why
	// the check below is an explicit, independently-enforced gate of its
	// own, never something this function merely infers from reaching
	// this point.
	if in.ChangedFileCount > cfg.MaxFilesChanged {
		return false, ReasonDiffTooLarge
	}
	// Phase 5 audit findings 1+2 (both fixed): TouchedBlastRadiusKnown
	// must be true before TouchedBlastRadius is trusted for anything --
	// see that field's own doc comment for the full "why" (a failed OR
	// page-truncated GitHub changed-files fetch must never be read as
	// "confirmed nothing sensitive"). This is a DISTINCT criterion from
	// the sensitive-path comparison immediately below it, with its own
	// Reason, so a 409/log line can tell "we could not establish this"
	// apart from "we established it and it IS sensitive".
	if !in.TouchedBlastRadiusKnown {
		return false, ReasonBlastRadiusUnknown
	}
	if touchesSensitivePath(in.TouchedBlastRadius, cfg.SensitiveTags) {
		return false, ReasonSensitivePathTouched
	}
	return true, ReasonNone
}

// touchesSensitivePath reports whether ANY tag in blastRadius also
// appears in sensitiveTags -- a plain O(n*m) membership scan (both
// slices are small, single-digit lengths in every real case: review.Tag
// has exactly eight legal values total, and a repo's own configured
// sensitive list is a subset of those eight) rather than building a
// set/map for what is never a hot path.
func touchesSensitivePath(blastRadius, sensitiveTags []review.Tag) bool {
	for _, tag := range blastRadius {
		for _, sensitive := range sensitiveTags {
			if tag == sensitive {
				return true
			}
		}
	}
	return false
}
