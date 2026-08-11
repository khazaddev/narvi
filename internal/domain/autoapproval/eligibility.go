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
	// EligibilityInput.ChangedFileCount (§62 review finding C1: the
	// server-fetched fact, never Verdict.FilesChanged).
	MaxFilesChanged int
	// SensitiveTags is the configurable-per-repo sensitive-path list
	// (§21.2: "no sensitive path touched -- configurable per repo").
	// Compared against EligibilityInput.TouchedBlastRadius: ANY overlap
	// disqualifies (§62 review finding C1: the server-DERIVED fact,
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
	// consulted by ComputeEligible below -- §62 review finding C1
	// (CRITICAL, fixed): Verdict.FilesChanged and Verdict.BlastRadius are
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
	// changed-file count (§62 review finding C1) -- len(ports.OpenPR.
	// ChangedFiles), populated by githubapi.fetchChangedFilePaths via
	// GitHub's own Pull Request Files API, NEVER Verdict.FilesChanged.
	// Compared against EligibilityConfig.MaxFilesChanged (§21.2: "diff
	// size under a configurable-per-repo threshold").
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
	// radius (§62 review finding C1) -- autoapproval.ClassifyChangedPaths
	// applied to ports.OpenPR.ChangedFiles (the same server-fetched
	// listing ChangedFileCount above is len() of), NEVER Verdict.
	// BlastRadius. Compared against EligibilityConfig.SensitiveTags: ANY
	// overlap disqualifies, exactly like Verdict.BlastRadius used to gate
	// before this fix -- only the SOURCE of the tags changed, not the
	// comparison itself.
	TouchedBlastRadius []review.Tag
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
	ReasonSensitivePathTouched Reason = "the diff touches a sensitive path"
)

// ComputeEligible is this package's single exported pure function
// (mirrors internal/domain/review's own "exactly three exported
// functions" discipline, and internal/domain/sentinelfix.
// EvaluateMergeGate's own single-function shape) -- see doc.go for the
// full criteria list, their order, and why "no floor raised" is not a
// separate, seventh check.
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
	// §62 review finding C1 (CRITICAL, fixed): both checks below now read
	// server-derived facts (EligibilityInput's own doc comment) -- never
	// in.Verdict.FilesChanged/in.Verdict.BlastRadius, which are the
	// reviewing model's own self-report and were the exploited hole.
	if in.ChangedFileCount > cfg.MaxFilesChanged {
		return false, ReasonDiffTooLarge
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
