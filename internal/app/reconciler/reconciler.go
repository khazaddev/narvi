package reconciler

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/app/ports"
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

	orphansReaped metric.Int64Counter

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
func NewReconciler(sandboxes *postgres.SandboxStore, provider ports.SandboxProvider, timeouts platform.Timeouts) (*Reconciler, error) {
	meter := otel.Meter(meterName)

	orphansReaped, err := meter.Int64Counter(
		"orphans_reaped",
		metric.WithDescription("Number of provider-side sandbox instances found with no live Postgres owner and stopped by the reconciler."),
		metric.WithUnit("{sandbox}"),
	)
	if err != nil {
		return nil, fmt.Errorf("reconciler: construct orphans_reaped counter: %w", err)
	}

	return &Reconciler{
		sandboxes:     sandboxes,
		provider:      provider,
		timeouts:      timeouts,
		orphansReaped: orphansReaped,
		unexplained:   make(map[string]time.Time),
	}, nil
}

// Run runs the process-wide reconciler loop (§5.3: "60s loop against the
// provider API") until ctx is done -- mirrors app/sessionactor's own
// RunTimerPump exactly (timerpump.go): a ticker on
// platform.Timeouts.ReconcilerInterval, calling ReconcileOnce each tick,
// logging (never propagating) any per-tick error so one bad tick never
// kills the whole loop. The caller starts this via its own errgroup.Go
// exactly once per process, same as RunTimerPump's own doc comment
// describes.
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
