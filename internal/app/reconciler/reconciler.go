package reconciler

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/app/repodemotion"
	"github.com/khazaddev/narvi/internal/platform"
)

// meterName is this package's own OTel meter name (§5.3's "orphan GC
// count" instrument lives here) -- "narvi/<package>" mirrors this
// codebase's own module path style without hardcoding the full import
// path, and reads unambiguously in exported metric data.
const meterName = "narvi/reconciler"

// Reconciler is the process-wide provider-reconciliation and orphan-GC
// loop (see doc.go for the full writeup). Constructed once per process
// (NewReconciler), then run via its own Run method -- exactly like
// app/sessionactor.Registry's RunTimerPump/PumpOnce pair.
type Reconciler struct {
	sandboxes *postgres.SandboxStore
	provider  ports.SandboxProvider
	timeouts  platform.Timeouts

	// repoSettings backs the demotion-obligation retry in
	// ReconcileDemotions. A demotion stamps demotion_sweep_pending_at in
	// the same statement that flips the flag, precisely so the obligation
	// outlives the transition that created it; this store is how a sweep
	// that failed halfway, or never ran because its process died, is
	// picked up again rather than lost.
	repoSettings *postgres.RepoSettingsStore

	orphansReaped metric.Int64Counter

	// demotionsTerminated (§30.4) counts every sandbox this reconciler has
	// actually terminated because a repo-demotion sweep
	// (internal/app/repodemotion.Sweep, called from internal/app/seed's
	// own live_egress_enabled writer) flagged it -- see
	// ReconcileDemotions' own doc comment.
	demotionsTerminated metric.Int64Counter

	// unexplained is ReconcileOnce's own in-memory, cross-tick debounce
	// state: for each ProviderID currently suspected of being an orphan
	// (seen in provider.List() with no matching row in the expected-alive
	// set on some PAST tick, but not yet confirmed/reaped), the wall-clock
	// time it was FIRST seen that way. See ReconcileOnce's own doc comment
	// for exactly how this is used, and platform.Timeouts.
	// ReconcilerOrphanConfirmationPeriod's own doc comment for why this
	// debounce exists at all.
	//
	// Deliberately NOT persisted to Postgres or shared across pods -- a
	// cold cache on process restart is fine and safe: it only means the
	// FIRST tick after a restart never reaps anything on first sight
	// (falls back to the same "record, don't reap yet" behavior every
	// brand-new ref already gets), which delays real orphan cleanup by at
	// most one extra tick interval and never causes an incorrect reap.
	//
	// Deliberately a PLAIN, unsynchronized map, not mutex-guarded: Run's
	// own ticker loop calls ReconcileOnce strictly sequentially, one tick
	// at a time (never concurrently with itself), and every test in this
	// package (reconciler_integration_test.go) does the same -- drives
	// ReconcileOnce from a single goroutine, one call at a time. If a
	// future caller ever needs to invoke ReconcileOnce concurrently with
	// itself, this map needs a mutex (or those calls need serializing)
	// first; do not add concurrent ReconcileOnce callers without also
	// revisiting this.
	unexplained map[string]time.Time
}

// NewReconciler builds a Reconciler backed by sandboxes (the Postgres
// store; see SandboxStore.ListLiveProviderIDs), provider (the real
// ports.SandboxProvider whose List/StopSandbox this reconciler drives),
// and timeouts (for ReconcilerInterval, consulted by Run).
//
// The orphans_reaped OTel counter is constructed exactly once, here, at
// construction time -- not per-tick, and not per-orphan -- via
// otel.Meter(meterName).Int64Counter, reading whatever MeterProvider is
// globally registered at call time (internal/platform.SetupOTel registers
// the real one in production; a test that wants to assert on the
// counter's value registers its own before constructing a Reconciler,
// there being no other way to intercept a package-level otel.Meter call).
func NewReconciler(sandboxes *postgres.SandboxStore, repoSettings *postgres.RepoSettingsStore, provider ports.SandboxProvider, timeouts platform.Timeouts) (*Reconciler, error) {
	meter := otel.Meter(meterName)

	orphansReaped, err := meter.Int64Counter(
		"orphans_reaped",
		metric.WithDescription("Number of provider-side sandbox instances found with no live Postgres owner and stopped by the reconciler."),
		metric.WithUnit("{sandbox}"),
	)
	if err != nil {
		return nil, fmt.Errorf("reconciler: construct orphans_reaped counter: %w", err)
	}

	demotionsTerminated, err := meter.Int64Counter(
		"demotions_terminated",
		metric.WithDescription("Number of live sandboxes stopped because a repo-demotion sweep flagged them (§30.4)."),
		metric.WithUnit("{sandbox}"),
	)
	if err != nil {
		return nil, fmt.Errorf("reconciler: construct demotions_terminated counter: %w", err)
	}

	return &Reconciler{
		sandboxes:           sandboxes,
		repoSettings:        repoSettings,
		provider:            provider,
		timeouts:            timeouts,
		orphansReaped:       orphansReaped,
		demotionsTerminated: demotionsTerminated,
		unexplained:         make(map[string]time.Time),
	}, nil
}

// Run runs the process-wide reconciler loop (§5.3: "60s loop against the
// provider API") until ctx is done -- mirrors app/sessionactor's own
// RunTimerPump exactly (timerpump.go): a ticker on
// platform.Timeouts.ReconcilerInterval, calling ReconcileOnce (orphan GC)
// and ReconcileDemotions (§30.4) each tick, logging (never propagating)
// either one's error so one bad tick never kills the whole loop, and
// never lets one starve the other of its own tick. The caller starts
// this via its own errgroup.Go exactly once per process, same as
// RunTimerPump's own doc comment describes.
func (r *Reconciler) Run(ctx context.Context) error {
	ticker := time.NewTicker(r.timeouts.ReconcilerInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := r.ReconcileOnce(ctx); err != nil {
				platform.Logger(ctx).Error("reconciler: tick failed", "error", err)
			}
			if err := r.ReconcileDemotions(ctx); err != nil {
				platform.Logger(ctx).Error("reconciler: demotion-sweep tick failed", "error", err)
			}
		}
	}
}

// ReconcileOnce runs exactly one reconciliation tick. Exported (rather
// than only reachable through Run's own loop) so tests can drive exactly
// one tick deterministically -- matching PumpOnce's own precedent
// (timerpump.go).
//
// (a) provider.List returns every REAL, currently-live provider-side
// sandbox this provider currently knows about. (b) sandboxes.
// ListLiveProviderIDs returns the provider_id of every sandbox row
// currently in a LIVE status (Spawning/Connecting/Booting/Ready/
// Snapshotting/Suspect), across ALL sessions -- this reconciler's own
// "expected still alive" set. (c) any ref from (a) whose ProviderID is NOT
// in (b) is UNEXPLAINED -- but an unexplained ref is NOT necessarily a
// real orphan yet, and is NOT reaped on the tick that first observes it.
//
// This is deliberate, not a missing optimization: internal/app/
// sessionactor/dispatch.go's own three-step spawn sequencing (see that
// file's own top comment) commits a sandboxes row already in a LIVE
// status (status='spawning') with provider_id still NULL, calls the real,
// network-bound SandboxProvider.CreateSandbox OUTSIDE any transaction,
// THEN commits a second, later transact that finally records provider_id.
// ListLiveProviderIDs requires provider_id IS NOT NULL, so that row is
// invisible to (b) for the whole window between CreateSandbox returning
// success and provider_id actually being committed -- a tick landing in
// that window sees a real, already-created, wanted cloud object with no
// match in (b), indistinguishable at that single instant from a genuine
// orphan. Reaping on first sighting would kill a legitimate, in-flight
// spawn; this requires no race with a second actor, no double-click --
// it is inherent to every successful spawn's own normal timing.
//
// So: r.unexplained (a plain, in-memory, cross-tick map -- see its own
// doc comment on the Reconciler struct) tracks, per ProviderID, the time
// an unexplained ref was FIRST seen this way. A ref unexplained for the
// FIRST time this tick is only RECORDED (r.unexplained[id] = now) --
// StopSandbox is NOT called for it yet. A ref STILL unexplained on a
// LATER tick, once platform.Timeouts.ReconcilerOrphanConfirmationPeriod
// has elapsed since it was first recorded, is now a CONFIRMED orphan:
// provider.StopSandbox is called for it. A ref recorded unexplained on
// some past tick that is NOT unexplained anymore (its provider_id got
// recorded, or the row otherwise became genuinely tracked) is simply
// dropped from r.unexplained -- it resolved, and is correctly never
// reaped. One failed StopSandbox call is logged and does NOT abort the
// rest of the batch -- exactly like timerpump.go's own deliver() per-item
// error isolation -- and does NOT increment orphansReaped (only a
// successfully-stopped orphan does, exactly once each); it also stays
// tracked in r.unexplained (already past its confirmation period) so the
// very next tick retries the reap immediately, without waiting out a
// fresh confirmation period.
//
// A failure in EITHER (a) or (b) -- the two batch-level calls this whole
// tick depends on -- aborts the tick and returns the error (Run logs it);
// only a per-orphan StopSandbox failure gets the isolated, non-aborting
// treatment described above. r.unexplained is left untouched on such an
// abort (no ref is considered "seen" -- explained or otherwise -- this
// tick), so a bad tick never resets or falsely advances any ref's own
// confirmation clock.
func (r *Reconciler) ReconcileOnce(ctx context.Context) error {
	live, err := r.provider.List(ctx)
	if err != nil {
		return fmt.Errorf("reconciler: provider list: %w", err)
	}

	expected, err := r.sandboxes.ListLiveProviderIDs(ctx)
	if err != nil {
		return fmt.Errorf("reconciler: list live provider ids: %w", err)
	}

	expectedAlive := make(map[string]struct{}, len(expected))
	for _, id := range expected {
		expectedAlive[id] = struct{}{}
	}

	now := time.Now()
	stillUnexplained := make(map[string]time.Time, len(r.unexplained))

	for _, ref := range live {
		if _, ok := expectedAlive[ref.ProviderID]; ok {
			// Explained (a genuinely live row backs it) -- if a past tick
			// had recorded this ref as unexplained, that record is simply
			// not carried forward into stillUnexplained: it resolved, and
			// is cleared from tracking rather than ever being reaped on
			// some later tick after it's already fine.
			continue
		}

		firstSeen, recorded := r.unexplained[ref.ProviderID]
		if !recorded {
			// First sighting ever (or first since it last resolved/was
			// reaped) -- record only. Do NOT call StopSandbox yet: this is
			// exactly the window a sandbox genuinely mid-spawn (status
			// already live, provider_id not yet committed -- see this
			// method's own doc comment above) can transiently look like.
			stillUnexplained[ref.ProviderID] = now
			continue
		}

		if now.Sub(firstSeen) < r.timeouts.ReconcilerOrphanConfirmationPeriod {
			// Unexplained again, but not yet continuously unexplained for
			// the full confirmation period -- keep waiting, preserving the
			// ORIGINAL first-seen time (not resetting it to now).
			stillUnexplained[ref.ProviderID] = firstSeen
			continue
		}

		// Confirmed: unexplained continuously since firstSeen, and at
		// least ReconcilerOrphanConfirmationPeriod has now elapsed. This
		// is a real orphan -- reap it.
		if err := r.provider.StopSandbox(ctx, ref); err != nil {
			platform.Logger(ctx).Error("reconciler: stop orphaned sandbox failed",
				"error", err, "provider_id", ref.ProviderID)
			// Stay tracked (at the original firstSeen, already past its
			// confirmation period) so the very next tick retries the reap
			// immediately rather than re-arming a fresh debounce wait.
			stillUnexplained[ref.ProviderID] = firstSeen
			continue
		}

		r.orphansReaped.Add(ctx, 1)
		// Successfully stopped -- drop from tracking. If provider.List
		// still returns it on a later tick (the stop not yet fully
		// applied provider-side), it is treated as a brand-new sighting
		// and re-debounced, which is conservative and safe.
	}

	r.unexplained = stillUnexplained

	return nil
}

// ReconcileDemotions implements §30.4's own mandatory-termination half:
// "demotion ... must terminate (or respawn) every sandbox of the repo".
// internal/app/repodemotion.Sweep (called from internal/app/seed's own
// live_egress_enabled writer, the ONLY place that flag flips today) is
// Postgres-only -- it never touches a real provider, since the seed CLI
// that calls it never constructs one -- so it merely FLAGS every
// affected sandbox (sandboxes.demotion_terminate_requested_at,
// migrations/000108_sandbox_demotion_termination.up.sql). This method is
// what actually terminates the real cloud resource: it runs inside
// "control-plane serve", the one process that holds a real
// ports.SandboxProvider, on the SAME ReconcilerInterval-ticking loop
// ReconcileOnce already uses (Run, above) -- so a demotion recorded on
// any pod, at any time, is acted on by whichever pod's own reconciler
// next ticks, with no direct coupling between the seed CLI and this one.
//
// A sandbox flagged with no provider_id yet (a spawn attempt genuinely
// in flight -- dispatch.go's own three-step spawn sequencing can commit
// a live-status row before provider_id is recorded, mirroring
// ReconcileOnce's own identical, already-documented race) is left FLAGGED
// for the next tick to retry, exactly like a failed StopSandbox call
// below: there is nothing yet to stop, but the flag must not be dropped
// just because this tick was too early.
//
// One failed StopSandbox call is logged and does NOT abort the rest of
// the batch, mirroring ReconcileOnce's own per-orphan error isolation --
// and stays flagged so the very next tick retries.
func (r *Reconciler) ReconcileDemotions(ctx context.Context) error {
	// Stage 1: any demotion that stamped an obligation no sweep has
	// cleared. This runs BEFORE the termination pass below so a sweep
	// recovered here flags its sandboxes in time for this same tick.
	if err := r.sweepOwedDemotions(ctx); err != nil {
		// Logged, never propagated past the termination pass: an
		// unreachable repo_settings must not also stop sandboxes already
		// flagged by an earlier, successful sweep from being terminated.
		platform.Logger(ctx).Error("reconciler: retry owed demotion sweeps failed; continuing to the termination pass", "error", err)
	}

	rows, err := r.sandboxes.ListPendingDemotionTermination(ctx)
	if err != nil {
		return fmt.Errorf("reconciler: list sandboxes pending demotion termination: %w", err)
	}

	for _, row := range rows {
		if row.ProviderID == nil {
			platform.Logger(ctx).Warn("reconciler: sandbox flagged for demotion termination has no provider_id yet; leaving flagged for a later tick",
				"session_id", row.SessionID.String())
			continue
		}

		if err := r.provider.StopSandbox(ctx, ports.SandboxRef{ProviderID: *row.ProviderID}); err != nil {
			platform.Logger(ctx).Error("reconciler: stop demoted sandbox failed; leaving flagged for retry",
				"error", err, "session_id", row.SessionID.String(), "provider_id", *row.ProviderID)
			continue
		}

		if _, err := r.sandboxes.ClearDemotionTerminationRequested(ctx, row.SessionID); err != nil {
			// The sandbox was genuinely stopped -- a failure to clear the
			// flag only means the NEXT tick harmlessly calls StopSandbox
			// again against an already-gone resource (most provider APIs
			// treat a double-stop as a benign no-op), never a correctness
			// problem, but still logged so it stays observable.
			platform.Logger(ctx).Error("reconciler: clear demotion termination request failed after a successful stop",
				"error", err, "session_id", row.SessionID.String())
		}

		r.demotionsTerminated.Add(ctx, 1)
	}

	return nil
}

// demotionSweepBatch bounds one tick's recovery work. Demotions are rare
// and this list is empty on an ordinary deployment, so the bound exists
// only to keep a pathological backlog from monopolising a tick -- what it
// does not sweep this tick, the next one does.
const demotionSweepBatch = 50

// sweepOwedDemotions is §30.4's demotion requirement made recoverable.
//
// The obligation used to live only in the difference between a
// pre-transaction read and a declared value -- and the commit destroyed
// it. A sweep that failed on its third sandbox abandoned the rest with
// nothing anywhere recording that they were owed a termination, and the
// operator's obvious response (re-run the manifest) found false -> false,
// reported success, and swept nothing. The write credential those
// sandboxes still held stayed usable for its full TTL.
//
// Now the flip stamps demotion_sweep_pending_at in its own statement, and
// this retries until a sweep completes without error. Clearing ONLY on a
// clean sweep is the point: a partial sweep leaves the obligation
// standing, so the next tick starts over. Sweep is idempotent -- flagging
// an already-flagged sandbox and cancelling an already-cancelled push are
// both no-ops -- so repeating it costs nothing but the query.
func (r *Reconciler) sweepOwedDemotions(ctx context.Context) error {
	owed, err := r.repoSettings.ListOwedDemotionSweep(ctx, demotionSweepBatch)
	if err != nil {
		return fmt.Errorf("reconciler: list repos owed a demotion sweep: %w", err)
	}

	for _, row := range owed {
		marked, sweepErr := repodemotion.Sweep(ctx, r.sandboxes, row.RepoFullName)
		if sweepErr != nil {
			// Left standing deliberately -- the next tick retries. A
			// partially-swept repo whose obligation was cleared is the
			// exact silent gap this column exists to close.
			platform.Logger(ctx).Error("reconciler: demotion sweep failed; leaving the obligation standing for a later tick",
				"error", sweepErr, "repo_full_name", row.RepoFullName)
			continue
		}
		if _, err := r.repoSettings.ClearDemotionSweepPending(ctx, row.RepoFullName); err != nil {
			// The sweep itself succeeded; failing to clear only means the
			// next tick sweeps the same repo again, which is harmless.
			platform.Logger(ctx).Error("reconciler: clear demotion sweep obligation failed after a clean sweep",
				"error", err, "repo_full_name", row.RepoFullName)
			continue
		}
		platform.Logger(ctx).Warn("reconciler: recovered a demotion sweep that had not completed; sandboxes flagged for termination",
			"repo_full_name", row.RepoFullName, "sandboxes_flagged", marked)
	}
	return nil
}
