// Package egressmode is §30.8's own resolver: the one place in this
// codebase allowed to decide whether a given repository's
// customer-visible egress is LIVE (writes really reach GitHub/Slack/
// Linear) or SHADOW (every write suppressed and recorded instead, §30.2/
// §30.6) for one call, and the sole source of the Capability token that
// decision takes the shape of.
//
// # Callers
//
// Resolve is load-bearing on several production paths now -- the §30.2
// transport gate and port decorator, the §30.3 Slack and Linear seams,
// the outbox's enqueue-time stamp (§30.6), the SCM-credential
// substitution (§30.4) and the calibration-read exclusion (§30.7). This
// doc used to say "nothing in this codebase calls Resolve yet", which
// was true when the package shipped dark and stopped being true one Step
// later; it is recorded here because someone changing Resolve's
// fail-closed posture or its signature would have read that sentence and
// treated the package as unconstrained.
//
// It exists so those seams -- the §30.2 transport gate and port
// decorator, the §30.3 single-instance Slack/Linear clients, the outbox's
// own enqueue-time stamp (§30.6) -- have exactly one, already-correct
// place to ask the question, rather than each growing its own copy of
// the fail-closed repo-settings read this package centralizes. Building
// the answer before anything asks the question is deliberate: every one
// of those future call sites gets the "cannot accidentally observe live
// on a degraded read" property for free, by construction, rather than by
// each remembering to reproduce it.
//
// # The one authority, and the one formula
//
// live_egress_enabled (migrations/
// 000101_repo_settings_live_egress_enabled.up.sql) is repo_settings' own
// per-repo authority; platform.Config.ShadowMode (NARVI_SHADOW_MODE) is
// the deployment-level master switch (§30.8). The effective mode Resolve
// computes is exactly §30.8's own formula: platformShadow OR NOT
// live_egress_enabled. There is deliberately no third input and no
// per-session override of either direction -- §30.8 forbids a per-session
// "go live" override outright (it would reintroduce the disciplinary leak
// this whole capability exists to remove), and this package does not add
// one for suppression either, even though §30.8 allows that direction
// later: nothing in this Step needs it, and adding an unused knob now
// would be scope this Step was never asked to carry.
//
// # No per-call error to get wrong
//
// Resolve returns a bare Capability, not (Capability, error) -- see its
// own doc comment for the full reasoning. In short: this codebase's own
// established fail-closed repo-settings idiom (internal/app/reviewverdict.
// AutoMergeEnabled; internal/app/sessionactor/reviewretrigger.go's own
// auto-retrigger read) already returns a bare bool for exactly this
// reason, and Resolve follows the same shape so there is no error return
// value for a caller to ignore on the way to accidentally treating a
// degraded read as live.
//
// # Capability is unforgeable
//
// Capability's one field is unexported, and the only function able to
// produce a Capability whose Live() reports true is this package's own
// unexported liveCapability -- whose callers are enumerated on that
// function itself. No other package can construct a live Capability by
// any means other than calling one of this package's resolvers, and a
// zero-value Capability (from a forgotten
// assignment, a zero-initialized struct field, or any other accident) is
// always the shadow capability. See capability.go's own doc comment.
//
// # Using this for the §30.8 epoch stamp
//
// §30.8's own unifying correction is that this system takes its
// *decisions* at enqueue/record time, and the mistake to avoid is
// re-reading the flag at *effect* time -- a flag flip between the two
// must never change what an already-enqueued action does. This package's
// shape is built for that discipline even though nothing here has a
// durable artifact of its own to stamp yet: a caller building one later --
// an outbox row's own suppressed_in_shadow mark (§30.6), a
// review_verdicts row's own egress-mode stamp (§30.8) -- should call
// Resolve exactly once, at the moment that row is created, and persist the
// result -- Capability.Suppressed(), or an equivalent stamp -- onto the
// row in the SAME transaction. Every later reader of that row trusts the
// stamp; none of them calls Resolve again to "recheck" it, since that
// second call is exactly the effect-time re-read §30.8 names as the
// design's own first-draft mistake.
package egressmode
