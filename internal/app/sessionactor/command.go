package sessionactor

// The 5 named persistent timers §2 lists explicitly: "connecting_deadline,
// liveness_check, inactivity, turn_deadline, terminal_grace." Named
// constants here (rather than bare string literals scattered across the
// package) so a typo in a timer name is a compile error at every call
// site that matters, even though the session_timers.name column itself is
// TEXT, not an enum (see migrations/000009_session_timers.up.sql).
const (
	TimerConnectingDeadline = "connecting_deadline"
	TimerLivenessCheck      = "liveness_check"
	TimerInactivity         = "inactivity"
	TimerTurnDeadline       = "turn_deadline"
	TimerTerminalGrace      = "terminal_grace"
)

// Command is the sum type an Actor's mailbox carries (§2: "one goroutine
// + mailbox (channel of commands) per active session"). isCommand is
// unexported so no type outside this package can implement Command --
// handle's type switch in timerfired.go can therefore treat its default
// case as unreachable dead-code protection, not a real possibility to
// handle.
type Command interface {
	isCommand()
}

// TimerFired is delivered by the timer pump (timerpump.go) when a named
// persistent timer becomes due (§2). Name is one of the 5 constants above
// in practice, but this type does not itself restrict it -- an unknown
// name is handled defensively (logged, ignored) by the dispatch switch in
// timerfired.go, the same deny-list-not-allow-list convention
// internal/domain/sandbox.IsDeadSandboxStatus and internal/domain/turn.
// IsTerminal already use.
type TimerFired struct {
	Name string
}

func (TimerFired) isCommand() {}
