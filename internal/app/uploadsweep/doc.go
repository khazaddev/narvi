// Package uploadsweep implements Step 58's ("uploads, blob storage & the
// in-sandbox download_file tool", §28.4) abandonment sweep: a `pending`
// upload artifact row older than platform.Timeouts.UploadPendingSweepAfter
// (a browser or sandbox that minted and never transferred/confirmed) is
// marked `failed(abandoned)` with a `blob_delete` outbox entry (the
// object may half-exist), and the same CP-synthesized `artifact` event
// every other resolution path appends (§28.6's own blanket "failed-upload
// UX signal... rides the same channel every other session fact already
// rides" requirement).
//
// §28.4 names this as running "by the same app/scheduler recovery-sweep
// machinery §3.5 already runs" -- but by the time that machinery actually
// shipped (Steps 51/52, "automations: engine"), it landed directly inside
// internal/app/automation's own sweep.go, never in a real, generic
// internal/app/scheduler package (which remains, to this day, an
// unimplemented doc.go-only stub -- confirmed by direct reading before
// this Step started). This package instead mirrors the OTHER, real,
// already-shipped precedent for a single-purpose, own-interval sweep
// loop: internal/app/reconciler's own New<X>/Run(ctx)/<X>Once(ctx) shape,
// wired into cmd/control-plane/main.go's one shared errgroup exactly like
// every other background loop in this codebase.
//
// Deliberately its OWN small package, not folded into
// internal/adapters/inbound/httpapi (which owns confirmUploadCore's own,
// analogous "mark failed + event + outbox + broadcast" sequence): a
// modest amount of duplication between this package's own resolveAbandoned
// and httpapi's confirmUploadCore, in exchange for keeping this
// process-wide background loop decoupled from an inbound adapter package
// -- the same shape/dependency-direction tradeoff internal/app/reconciler
// and internal/app/automation already accept relative to each other.
package uploadsweep
