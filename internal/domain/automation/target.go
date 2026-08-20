package automation

import "errors"

// Target is one repo an automation dispatches a run against -- the SAME
// shape as restdtos.CreateSessionRequestReposElem (Name/Url/Branch), since
// a run's own job is ultimately "create a session for this one target"
// (app/automation's own fanout.go, reusing internal/adapters/inbound/
// httpapi.CreateSessionOnTx -- the shared session-creation core §5.1
// established and the GitHub/Slack/Linear ingress adapters already reuse three times). Kept as this
// package's own plain struct (never restdtos.CreateSessionRequestReposElem
// itself) so this package stays adapter/contracts-independent (§11) --
// app/automation converts at the boundary, exactly like every other
// domain<->adapter conversion in this codebase (e.g. internal/app/
// releasereview's own toDomainMergedPR).
type Target struct {
	Name   string
	URL    string
	Branch string // "" means "use the repo's own default branch", matching restdtos' own nil-Branch convention.
}

// MaxFanOutTargets is §3.5's own explicit fan-out cap: "one run per
// target, fan-out ≤10".
const MaxFanOutTargets = 10

// ErrNoTargets and ErrTooManyTargets are ValidateTargets' own sentinel
// errors -- mirrors this codebase's own established sentinel-error house
// style (internal/domain/environment's ErrEmptyPattern et al.) rather than
// a bare fmt.Errorf string.
var (
	ErrNoTargets      = errors.New("automation: invocation must name at least one target")
	ErrTooManyTargets = errors.New("automation: invocation names more targets than MaxFanOutTargets")
)

// ValidateTargets enforces §3.5's own fan-out cap on a candidate target
// list, before it is ever snapshotted onto a new invocation
// (app/automation's own CreateInvocation) -- 1..MaxFanOutTargets
// (inclusive), never zero (an invocation with no targets would fan out
// zero runs and could never close, since EvaluateInvocationOutcome's own
// "terminalRuns >= totalRuns" check trivially holds at totalRuns==0 with
// no run ever having existed to prove anything).
func ValidateTargets(targets []Target) error {
	if len(targets) == 0 {
		return ErrNoTargets
	}
	if len(targets) > MaxFanOutTargets {
		return ErrTooManyTargets
	}
	return nil
}
