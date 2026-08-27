// Package shadowscm is the typed half of the egress gate (§30.2 layer 1).
//
// It decorates ports.SourceControl, forwarding reads and suppressing
// writes for repositories the resolver reports as shadow, recording each
// suppressed write with its real types so the state machines that consume
// write results stay coherent (§30.7).
//
// It is deliberately not the only defence. The transport gate underneath
// it sees every request the GitHub adapter makes, including the mutating
// methods that live outside the port; this layer sees only the port, and
// exists for what a transport cannot do -- give a suppressed effect a
// type, and hand back a result the caller's own state machine can survive.
package shadowscm
