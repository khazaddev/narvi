package sandboxboot

// Hook names one of the boot-sequence scripts §6.4 defines (setup.sh,
// start.sh) plus §19.6's own addition, the optional delta script
// (sync.sh). This is a closed vocabulary controlled entirely by this
// package and its caller (internal/sandboxagent/boot's hook runner) -- no
// external input ever supplies a raw Hook value.
type Hook string

const (
	// HookSetup is the image-build-time dependency-install script.
	HookSetup Hook = "setup.sh"
	// HookStart is the per-boot service-start script.
	HookStart Hook = "start.sh"
	// HookDelta is the OPTIONAL, repo-authored delta script (§19.6):
	// "sync.sh", added to the closed hook vocabulary specifically to
	// run INSTEAD OF a full HookSetup rerun under BootModeRepoImage when
	// workspaceMoved is true but setup.sh itself is provably unchanged
	// since the built SHA (`git diff --quiet <built_sha> HEAD --
	// setup.sh`, computed by the caller -- this package never touches
	// git). A repo with no sync.sh on disk is a routine, silent no-op,
	// exactly like an absent setup.sh/start.sh always has been -- this is
	// purely additive and opt-in, never a new requirement on existing
	// repos.
	HookDelta Hook = "sync.sh"
)

// HookOutcome is EvaluateHook's decision for one (mode, hook, primary)
// combination: whether the hook should run at all, and -- if it runs --
// whether a non-zero exit or timeout is fatal to the whole boot sequence
// or merely a logged warning that lets the sequence continue.
type HookOutcome struct {
	ShouldRun      bool
	FatalOnFailure bool
}

// EvaluateHook implements §6.4's hook policy, AMENDED by §19.4 (warm-boot
// shared images) -- this amendment is a BREAKING CHANGE to the
// §6.4 contract, carrying the Conventional-Commits `!` marker on the
// commit that lands it: "setup.sh runs only in fresh/build (fatal only in
// build), OR in repo_image when workspaceMoved; start.sh runs in all
// non-build modes (primary repo fatal, secondaries warn)".
//
// workspaceMoved is only ever consulted for one cell of this policy table:
// HookSetup under BootModeRepoImage. It is otherwise ignored -- in
// particular start.sh's own policy is completely unaffected by it for
// every mode, and every OTHER (mode, hook, primary) combination's outcome
// is BYTE-IDENTICAL to the policy before this amendment (see hook_test.go's
// own truth table, which pins every pre-existing cell unchanged and adds
// only the new repo_image/HookSetup/workspaceMoved cases).
//
// Rationale (§19.4): under warm-from-tip shared images, `repo_image` no
// longer implies "image content == session content" the way an exact-SHA
// image fingerprint used to guarantee -- the boot-time workspace can have
// moved arbitrarily far from the SHA setup.sh last ran against at build
// time. Leaving repo_image's setup hook at an unconditional ShouldRun:
// false would produce sessions with silently missing dependencies, the
// worst failure class available (surfacing later as confusing agent/tool
// errors, not a boot error). Redefined contract: "repo_image means setup.sh
// ran at build time against a near-tip tree; if the boot-time workspace has
// MOVED from the built SHA, setup.sh runs again, NON-FATALLY (warn,
// continue) -- expected to be fast, because its outputs are already warm."
// A workspace that has NOT moved (workspaceMoved: false) still skips
// setup.sh exactly as before this amendment -- SHA-equality is a pure
// optimization, zero regression for that case.
//
// This makes the repo-authored setup.sh's own idempotency/incremental
// contract a real, load-bearing requirement rather than a nice-to-have:
// under this policy setup.sh reruns on essentially every warm boot (the
// freshness pump's own staleness window, §19.2, plus any ordinary branch
// activity makes an exact SHA match the exception, not the rule). See
// docs/environments.md for the named requirement this places on every
// repo, from the repo author's own side of the contract.
//
// setup.sh's FatalOnFailure never depends on primary -- only build-vs-not
// matters for it (it fails the whole sequence only during an image build,
// where there is no running session to fall back on) -- and, as of this
// amendment, a repo_image rerun is ALWAYS non-fatal regardless of
// workspaceMoved, matching §19.4's "never block a spawn" framing: a moved
// workspace proves nothing about dependencies, so it can never justify
// failing the boot. start.sh's FatalOnFailure is exactly the primary flag
// whenever it runs at all (a secondary repo's start.sh failing is only
// ever a warning).
//
// An unrecognized Hook value (anything other than HookSetup, HookStart, or
// HookDelta) is treated as a programming error, not a
// data error: Hook is a closed vocabulary this package and its own caller
// control, so this returns the zero HookOutcome (ShouldRun: false) rather
// than defining a third named error type for a case that can only arise
// from a bug in this codebase, never from external input.
//
// # §19.1 addition: HookDelta (§19.6)
//
// HookDelta's own policy row is deliberately the SAME eligibility envelope
// as HookSetup's own repo_image branch (mode == BootModeRepoImage &&
// workspaceMoved) -- this function only ever describes WHEN the delta
// script is conceptually in-scope, never whether it should be PREFERRED
// over a full setup.sh rerun: that preference (§19.6's own "prefer the
// delta script over full setup.sh when eligible") depends on a second,
// orthogonal predicate this package cannot evaluate (setup.sh itself
// provably unchanged since the built SHA, a live git check) and on
// sync.sh's own on-disk presence -- both resolved by the caller
// (internal/sandboxagent/boot's own graduated-ladder orchestration, which
// ALSO owns the "delta fails -> fall back to full setup.sh" sequencing;
// EvaluateHook is a stateless, per-cell function and has no way to express
// a multi-step fallback sequence). FatalOnFailure is unconditionally false,
// exactly mirroring HookSetup's own repo_image rerun: a delta-script
// failure can never fail the boot any more than a full setup.sh rerun's
// own failure can (§19.4's "a moved workspace proves nothing about
// dependencies" reasoning applies identically here -- see this function's
// own HookSetup case).
func EvaluateHook(mode BootMode, hook Hook, primary, workspaceMoved bool) HookOutcome {
	switch hook {
	case HookSetup:
		shouldRun := mode == BootModeBuild || mode == BootModeFresh ||
			(mode == BootModeRepoImage && workspaceMoved)
		return HookOutcome{
			ShouldRun:      shouldRun,
			FatalOnFailure: mode == BootModeBuild,
		}
	case HookStart:
		runs := mode != BootModeBuild
		return HookOutcome{
			ShouldRun:      runs,
			FatalOnFailure: runs && primary,
		}
	case HookDelta:
		return HookOutcome{
			ShouldRun:      mode == BootModeRepoImage && workspaceMoved,
			FatalOnFailure: false,
		}
	default:
		return HookOutcome{}
	}
}
