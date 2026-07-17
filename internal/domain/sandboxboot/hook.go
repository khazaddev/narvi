package sandboxboot

// Hook names one of the two boot-sequence scripts §6.4 defines. This is a
// closed vocabulary controlled entirely by this package and its caller
// (internal/sandboxagent/boot's hook runner) -- no external input ever
// supplies a raw Hook value.
type Hook string

const (
	// HookSetup is the image-build-time dependency-install script.
	HookSetup Hook = "setup.sh"
	// HookStart is the per-boot service-start script.
	HookStart Hook = "start.sh"
)

// HookOutcome is EvaluateHook's decision for one (mode, hook, primary)
// combination: whether the hook should run at all, and -- if it runs --
// whether a non-zero exit or timeout is fatal to the whole boot sequence
// or merely a logged warning that lets the sequence continue.
type HookOutcome struct {
	ShouldRun      bool
	FatalOnFailure bool
}

// EvaluateHook implements §6.4's hook policy verbatim: "Hook policy:
// setup.sh runs only in fresh/build (fatal only in build); start.sh runs
// in all non-build modes (primary repo fatal, secondaries warn)".
//
// setup.sh's FatalOnFailure never depends on primary -- only build-vs-not
// matters for it (it fails the whole sequence only during an image build,
// where there is no running session to fall back on). start.sh's
// FatalOnFailure is exactly the primary flag whenever it runs at all (a
// secondary repo's start.sh failing is only ever a warning).
//
// An unrecognized Hook value (anything other than HookSetup or HookStart)
// is treated as a programming error, not a data error: Hook is a closed
// vocabulary this package and its own caller control, so this returns the
// zero HookOutcome (ShouldRun: false) rather than defining a third named
// error type for a case that can only arise from a bug in this codebase,
// never from external input.
func EvaluateHook(mode BootMode, hook Hook, primary bool) HookOutcome {
	switch hook {
	case HookSetup:
		return HookOutcome{
			ShouldRun:      mode == BootModeBuild || mode == BootModeFresh,
			FatalOnFailure: mode == BootModeBuild,
		}
	case HookStart:
		runs := mode != BootModeBuild
		return HookOutcome{
			ShouldRun:      runs,
			FatalOnFailure: runs && primary,
		}
	default:
		return HookOutcome{}
	}
}
