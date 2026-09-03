// This file (enqueue.go) implements blocking-finding fix #1's own FIRST
// phase: a fast, cheap, durable hand-off, called inline from
// internal/adapters/inbound/github's own webhook handler, BEFORE its own
// ack -- see this package's own top doc comment and migrations/
// 000050_release_manifest_pending.up.sql's own doc comment for the full
// "why" this exists as a separate phase from Run (run.go).

package releasereview

import (
	"context"
	"log/slog"

	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// PendingEnqueuer is the narrow slice of *postgres.ReleaseManifestPendingStore
// this package needs -- mirrors OutboxEnqueuer's own identical "narrow
// interface over one store's Create method" precedent, purely for this
// package's own unit tests (no real DB round trip needed there).
type PendingEnqueuer interface {
	Create(ctx context.Context, arg sqlcgen.CreateReleaseManifestPendingParams) (sqlcgen.ReleaseManifestPending, error)
}

// Enqueue writes ONE durable release_manifest_pending row so Worker (a
// SEPARATE background loop, worker.go) can run the actual manifest check
// (Run) later, entirely decoupled from the caller's own context/lifetime.
// Deliberately as fast and cheap as a single INSERT -- this is the ONLY
// call the webhook handler makes synchronously, inline, before its own
// ack; the actual GitHub-API-heavy work (Run's own ListMergedBetween
// call) never runs on this path.
//
// Best-effort, mirroring Run's own identical discipline: a failure here
// is logged and this function simply returns, never propagating an error
// the caller would have to decide whether to fail an already-successful
// session creation over. in.Token is deliberately NEVER persisted onto
// the row (Worker re-supplies its own statically-configured bot
// credential at run time instead, exactly like the pre-fix inline call
// already did) -- avoiding writing a credential into a durable table at
// rest that has no need to carry one.
func Enqueue(ctx context.Context, logger *slog.Logger, store PendingEnqueuer, in Input) {
	if _, err := store.Create(ctx, sqlcgen.CreateReleaseManifestPendingParams{
		SessionID:     in.SessionID,
		Owner:         in.Owner,
		Repo:          in.Repo,
		PrNumber:      in.PRNumber,
		BaseRef:       in.BaseRef,
		HeadRef:       in.HeadRef,
		CorrelationID: in.CorrelationID,
	}); err != nil {
		logger.Error("releasereview: enqueue release manifest pending check failed",
			"error", err, "owner", in.Owner, "repo", in.Repo, "pr_number", in.PRNumber)
	}
}
