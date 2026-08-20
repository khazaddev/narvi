// Package rollout implements Step 76's own ("feature-flagged cohort
// rollout of sessions, with documented rollback", §10 Phase 6, §32) ONE
// pure admission decision -- shared, byte-for-byte identically, between
// BOTH of §32's two independent gates: the primary, session-creation-time
// gate (internal/adapters/inbound/httpapi.CreateSessionOnTx, run once per
// create, inside the same transaction that is about to insert the
// session) and the dispatch-time re-check (internal/app/sessionactor's
// own tryPlanSpawn, run fresh on every Spawn/Restore/Resume attempt for
// the session's entire lifetime) -- mirroring §27.5's own identical
// "one pure function, two independent call sites" shape exactly
// (internal/domain/environment.CheckSubstrateCapabilities is that Step's
// own twin of this package).
//
// Pure: no I/O, no clock, no randomness (CLAUDE.md §11: "no I/O ...  in
// /internal/domain"). Both call sites do their own I/O (a repo_settings
// read, on their own transaction/connection) BEFORE ever calling Decide,
// and fold every degraded outcome -- an absent row, a genuine read
// error, an unresolvable/unsupported-host repo URL -- into a single
// RepoAdmission.Enrolled == false fact ahead of time. This package never
// sees a Postgres error, a pgx.ErrNoRows, or a URL: only the already-
// resolved booleans a caller decided from them.
package rollout

// Mode is platform.Config's own master switch for Step 76 (NARVI_ROLLOUT_MODE,
// §32) -- exactly two values. platform.Config.RolloutMode is typed as
// THIS package's own Mode (not a parallel, independently-defined enum in
// internal/platform) specifically so there is exactly one place in this
// codebase that names a rollout mode value -- a future third mode cannot
// be added to one type and silently forgotten on the other.
type Mode string

const (
	// ModeOpen is the unset/default value (§32: "Unset -> open, so this
	// Step lands as a byte-for-byte no-op for every existing deployment
	// and for CI"). Decide admits unconditionally in this mode, without
	// even inspecting repos -- see Decide's own doc comment.
	ModeOpen Mode = "open"
	// ModeCohort is the enrolled-repos-only mode: Decide requires EVERY
	// repo a session names to be independently enrolled.
	ModeCohort Mode = "cohort"
)

// RepoAdmission is one named repo's already-resolved enrollment fact, as
// the caller determined it BEFORE ever calling Decide. FullName is
// "owner/repo" when the caller could resolve one (reposource.
// CheckRepoHost + ParseOwnerRepo both succeeded) -- carried here purely
// for Decision.RepoFullName's own logging/observability value, never
// compared or parsed by this package. Enrolled is FAIL-CLOSED: false for
// an absent repo_settings row, a genuine read error, OR a repo whose
// clone URL could not be resolved to a trusted, host-verified owner/repo
// identity at all -- §32's own security requirement that
// reposource.ParseOwnerRepo's deliberate host-agnosticism (it never
// inspects a URL's host) can never be exploited to spoof a DIFFERENT
// host's enrolled repo by reusing its owner/repo path. This package
// never re-derives or re-validates any of that itself; it only reasons
// over the boolean fact already gathered.
type RepoAdmission struct {
	FullName string
	Enrolled bool
}

// RefusalReason names why Decide refused a create/dispatch -- a typed,
// terminal outcome (§32: "a machine-checkable refusal marker ... so
// these branches can distinguish policy refusal from a transient failure
// structurally, not by string matching").
type RefusalReason string

const (
	// ReasonNone is the zero value -- Decision.Admitted is true.
	ReasonNone RefusalReason = ""
	// ReasonRepoNotEnrolled means at least one named repo's own
	// RepoAdmission.Enrolled was false in ModeCohort.
	ReasonRepoNotEnrolled RefusalReason = "repo_not_enrolled"
)

// Decision is Decide's own result.
type Decision struct {
	// Admitted is true iff every requirement ModeCohort imposes was met
	// (or mode was not ModeCohort at all).
	Admitted bool
	// Reason is ReasonNone when Admitted is true, otherwise
	// ReasonRepoNotEnrolled today (the only refusal this package
	// currently knows how to produce).
	Reason RefusalReason
	// RepoFullName is the FIRST repo (in the caller's own repos slice
	// order) whose RepoAdmission.Enrolled was false -- "" when Admitted
	// is true. Named here so a caller's own logging/audit line can report
	// exactly which repo blocked the session, without re-scanning its
	// own input a second time.
	RepoFullName string
}

// admitted is the one Decision value meaning "proceed" -- returned by
// every path through Decide that does not refuse, so a future added
// field on Decision only needs updating in one place.
var admitted = Decision{Admitted: true}

// Decide is the ONE pure admission decision §32 requires (mode x
// per-repo lookups -> admit or a typed refusal reason).
//
// ModeOpen (or any mode other than ModeCohort) admits unconditionally --
// repos is never even iterated, preserving §32's own "byte-for-byte
// no-op with NARVI_ROLLOUT_MODE unset" property regardless of what repos
// contains or how many entries it has. A caller running in ModeOpen is
// free to pass a nil/empty repos slice without ever having done any I/O
// to populate it -- this is what lets both call sites skip the
// repo_settings read entirely when mode is not ModeCohort.
//
// ModeCohort requires EVERY repo in repos to be Enrolled ("multi-repo
// sessions: all named repos must be enrolled", §32) -- refuses on the
// FIRST one that is not (stop-at-first-failure, mirroring
// httpapi.validateCreateSessionRequest's own identical repo-validation
// precedent: report the first failure, never attempt to collect every
// one at once). A repos slice with zero entries is vacuously admitted in
// ModeCohort too -- Decide itself never asserts "at least one repo";
// that is validateCreateSessionRequest's own, separate, already-enforced
// invariant (repos must be non-empty) upstream of either call site.
func Decide(mode Mode, repos []RepoAdmission) Decision {
	if mode != ModeCohort {
		return admitted
	}
	for _, r := range repos {
		if !r.Enrolled {
			return Decision{Admitted: false, Reason: ReasonRepoNotEnrolled, RepoFullName: r.FullName}
		}
	}
	return admitted
}
