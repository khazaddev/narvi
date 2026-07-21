// Package reconciler is the process-wide provider-reconciliation and
// orphan-GC loop (§5.3; IMPLEMENTATION_PLAN.md row 25, "reconciler + GC":
// "60s loop against the provider API, orphan reaping, orphans_reaped
// metric") -- a sibling of app/sessionactor, not folded into it
// (TECHNICAL_PLAN.md's own repo-layout comment: "reconciler/ # provider
// reconciliation + orphan GC loop").
//
// Reconciler.Run (mirroring app/sessionactor's own RunTimerPump/PumpOnce
// shape exactly -- see timerpump.go, this package's direct precedent for
// loop shape, error handling, and doc-comment style) ticks every
// platform.Timeouts.ReconcilerInterval, calling ReconcileOnce -- exported
// separately, exactly like PumpOnce, so tests can drive exactly one tick
// deterministically. ReconcileOnce (a) calls ports.SandboxProvider.List
// for every real, currently-live provider-side sandbox, (b) queries
// Postgres (SandboxStore.ListLiveProviderIDs, a new, genuinely
// cross-session query -- every sibling query in queries/sandboxes.sql is
// scoped to one session_id) for the provider_id of every sandbox row
// currently in a LIVE status, and (c) any provider ref from (a) with no
// corresponding row in (b)'s expected-alive set is UNEXPLAINED -- but is
// NOT reaped on the tick that first observes it. A ref must be seen
// unexplained continuously for at least
// platform.Timeouts.ReconcilerOrphanConfirmationPeriod, across separate
// ticks, before ReconcileOnce actually calls StopSandbox on it (see that
// method's own doc comment, and the Timeouts field's own, for the full
// mechanics and the exact race this debounce closes -- in short: a
// sandbox genuinely mid-spawn, its status already live but its
// provider_id not yet committed by app/sessionactor/dispatch.go's own
// deliberate three-step spawn sequencing, would otherwise transiently
// look identical to a real orphan and get killed on first sighting). Two
// distinct ways a CONFIRMED orphan arises, both genuinely reaped here for
// the first time in this codebase (ports.SandboxProvider.StopSandbox has
// had zero production callers anywhere before this Step):
//
//   - A stale-epoch takeover rolled back the ONLY write that would have
//     recorded a just-created/just-resumed provider object (see
//     app/sessionactor/dispatch.go's own "Step 25's reconciler" comments,
//     and §9.3 scenario 5: "two concurrent spawns ... loser sandbox
//     reaped by GC") -- Postgres has zero trace of that attempt, so there
//     is no row to correct, only a live cloud resource with no Postgres
//     owner at all.
//   - A sandbox row reached a terminal status (Stopped/Failed) through
//     the ordinary lifecycle, but its own provider_id was simply never
//     explicitly torn down (no caller ever existed to do so before this
//     Step) -- excluded from the expected-alive set by construction
//     (ListLiveSandboxProviderIDs's own doc comment), so its lingering
//     provider object is reaped exactly like a stale-epoch orphan is.
//
// One failed StopSandbox call is logged and does NOT abort the rest of
// the batch -- exactly like timerpump.go's own deliver() per-item error
// isolation. Each successfully-stopped orphan increments the
// orphans_reaped OTel counter (otel.Meter("narvi/reconciler"),
// constructed once in NewReconciler, never per-tick -- this codebase's
// first custom OTel instrument of any kind; internal/platform/otel.go's
// own SetupOTel already registers the global MeterProvider this reads
// from, but defines no instruments itself. See otel.go's own top comment
// for the rest of §5.3's metric list and which Step owns each one).
//
// Deliberately, permanently out of scope for this package (do not revisit
// without a new Step): it never writes to a sandboxes row's own status
// column, and it never adds a 'stale' value to the Postgres sandbox_status
// enum. domain/sandbox already supports a three-way TriggerGraceExpired
// target (Stopped/Failed/Stale, state.go) for a FUTURE possibility
// mentioned in app/sessionactor/timerfired.go's own handleTerminalGraceTimer
// comment -- classifying an already-Suspect row as Stale once a reconciler
// independently confirms its cloud resource is gone -- but that is a
// currently-hypothetical concept this package does not build any part of.
// This package is pure cloud-side orphan reaping: it never routes through
// app/sessionactor.Registry.GetOrSpawn/Actor.Send into any session's own
// actor.
package reconciler
