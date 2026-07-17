package sandbox

// deadStates are the states in which no live sandbox can legitimately act
// on the session: spawn gave up (Failed) or the sandbox was shut down
// (Stopped/Stale). This is a DENY-list, not an allow-list: an unknown
// future state must read as live, so callers fall through to their own
// checks (e.g. a token comparison) instead of locking out every sandbox.
var deadStates = map[State]bool{
	StateStopped: true,
	StateStale:   true,
	StateFailed:  true,
}

// IsDeadSandboxStatus reports whether status is one of the sandbox's
// terminal, non-recoverable-without-a-new-spawn states.
func IsDeadSandboxStatus(status State) bool {
	return deadStates[status]
}
