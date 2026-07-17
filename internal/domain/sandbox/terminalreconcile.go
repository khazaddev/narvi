package sandbox

// transientBootStates are the states meaningless once the owning session
// has gone terminal: no turn will ever run on this sandbox again, so a box
// still parked in one of these mid-boot phases (most often left behind by
// an interrupted spawn no watchdog ever reconciled) would otherwise make
// the UI show a phantom "Starting sandbox…" forever and hide the relaunch
// affordance (which only acts on Stopped/Failed/Stale).
//
// Ready/Snapshotting/Suspect are deliberately excluded: a LIVE sandbox on a
// terminal session is a separate, real bug for something else to handle,
// not this function's job. Stopped/Failed/Stale are already terminal, so
// there's nothing to reconcile.
var transientBootStates = map[State]bool{
	StatePending:    true,
	StateSpawning:   true,
	StateConnecting: true,
	StateBooting:    true,
}

// ReconcileTerminalSandboxStatus reconciles the sandbox status when its
// session becomes terminal (completed/failed/cancelled/archived). It
// returns (StateStopped, true) when status is one of the transient boot
// states above, or ("", false) when no change is needed (the sandbox is
// live, already terminal, or snapshotting -- none of which are misleading
// on a terminal session).
func ReconcileTerminalSandboxStatus(status State) (State, bool) {
	if transientBootStates[status] {
		return StateStopped, true
	}
	return "", false
}
