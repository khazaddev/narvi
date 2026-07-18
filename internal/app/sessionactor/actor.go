package sessionactor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/platform"
)

// mailboxBufferSize is the mailbox channel's queue depth -- a plain
// count, not a duration/interval, so it does not belong in
// platform.Timeouts (notimeliteral only forbids time.Duration unit
// literals; this is a plain int). Chosen generously: an actor processes
// one command at a time (that serialization is what makes §2's
// single-writer rule true), so a deep buffer just means a burst of
// timer/command deliveries queues up instead of blocking whoever is
// sending them.
const mailboxBufferSize = 32

// Actor is the single in-process owner of one session's state (§2: "All
// mutations of a session's state go through its actor -- no other code
// path writes session/sandbox/turn rows"). Built exclusively by
// Registry.hydrateAndAcquire (hydrate.go), which is the only code that
// may construct one -- every field is unexported.
type Actor struct {
	sessionID pgtype.UUID
	epoch     int64

	pool     *pgxpool.Pool
	timeouts platform.Timeouts
	stores   storeBundle

	// broadcaster is how a successfully committed event reaches every
	// currently-subscribed browser client for this session, live (§6.2's
	// "→ broadcast stream" made real -- design decision: broadcasting is a
	// generic, automatic property of transact's own commit path, not
	// something each command handler must remember to call; see
	// pendingBroadcast below and transact's own doc comment). May be nil
	// (some tests construct an Actor without one, e.g. via a fake
	// storeBundle-only setup) -- appendEvent/appendRawEvent still append to
	// pendingBroadcast unconditionally, but transact skips the delivery
	// loop entirely when broadcaster is nil.
	broadcaster ports.EventBroadcaster

	// pendingBroadcast queues each event appended (via appendEvent/
	// appendRawEvent) during the CURRENT transact attempt, in order. Safe
	// unsynchronized: Actor.handle processes exactly one command at a time
	// (the same single-goroutine invariant every other Actor field already
	// relies on, see doc.go's Concurrency section), so no two goroutines
	// ever touch this slice concurrently. Reset to nil on every transact
	// entry path (see transact's own doc comment) -- never accumulates
	// across separate commands or separate attempts.
	pendingBroadcast []json.RawMessage

	registry *Registry

	// lockConn holds the session-scoped Postgres advisory lock for this
	// Actor's entire life (§2: "held for the actor's lifetime"). It is
	// NEVER used for the Actor's own transactional writes -- transact
	// (below) always acquires a FRESH connection from the pool -- so it
	// never becomes a concurrency bottleneck.
	lockConn *pgxpool.Conn

	mailbox chan Command
	// done is closed exactly once, the instant run's loop returns for any
	// reason (idle-TTL eviction, ErrStaleEpoch, or the lifecycle context
	// being cancelled) -- via run's own deferred close, BEFORE shutdown's
	// slower eviction/unlock steps, so Send starts rejecting as early as
	// possible. Send checks it to report ErrActorStopped instead of
	// blocking forever, or sending on a channel nobody will ever read
	// from again.
	done chan struct{}

	logger *slog.Logger
}

// Send delivers cmd to the actor's mailbox, or reports ErrActorStopped if
// the actor's run loop has already exited.
//
// The done check runs FIRST, on its own, so that once the actor is known
// dead every Send deterministically fails -- without this priority stage,
// a single select with a buffered mailbox case and a closed done case
// would pick between the two pseudo-randomly, sometimes "accepting" a
// command into a mailbox nobody will ever read again.
//
// One narrow race is inherent to any channel-based mailbox and documented
// rather than hidden: a Send that passes the done check just as the run
// loop is exiting can still enqueue into the buffer and return nil. Those
// commands are drained and logged by shutdown on a best-effort basis (an
// enqueue racing the drain itself can still slip through unlogged) -- so
// delivery through Send is at-most-once by construction, and a command
// that must not be lost needs its own redelivery mechanism, exactly as
// TimerFired has (the timer pump's claim window redelivers an unhandled
// timer once TimerClaimDuration elapses).
func (a *Actor) Send(ctx context.Context, cmd Command) error {
	select {
	case <-a.done:
		return ErrActorStopped
	default:
	}
	select {
	case a.mailbox <- cmd:
		return nil
	case <-a.done:
		return ErrActorStopped
	case <-ctx.Done():
		return ctx.Err()
	}
}

// run is the actor's own mailbox-processing loop, started exactly once by
// Registry.GetOrSpawn via errgroup.Group.Go. It processes exactly one
// command at a time against the mailbox channel and a shutdown signal
// (ctx.Done, or its own idle timer) -- this serialization is what makes
// §2's "single writer" true even though Postgres itself would happily
// accept concurrent connections from this same process.
//
// Deferred-cleanup ordering matters here (LIFO): close(a.done) is
// registered LAST so it runs FIRST the instant the loop exits -- marking
// the actor dead for Send before shutdown starts its slower map-eviction
// and network-round-trip unlock steps. Without that ordering, the whole
// unlock round-trip is a window during which GetOrSpawn still hands this
// dead actor out and Send still accepts commands nobody will ever read.
func (a *Actor) run(ctx context.Context) error {
	defer a.shutdown()
	defer close(a.done)

	idleTimer := time.NewTimer(a.timeouts.ActorIdleTTL)
	defer idleTimer.Stop()

	for {
		select {
		case <-ctx.Done():
			a.logger.Info("sessionactor: run loop stopping: context done", "error", ctx.Err())
			return ctx.Err()

		case <-idleTimer.C:
			// §2: "evicts after idle TTL (default 30 min without
			// commands or connected clients)". This Step has no
			// mechanism to observe "connected clients" (that's the
			// client WS hub, Steps 18+) -- only "no commands" is
			// checked here; see doc.go for the same documented gap.
			a.logger.Info("sessionactor: evicting self: idle TTL elapsed with no commands")
			return nil

		case cmd, ok := <-a.mailbox:
			if !ok {
				return nil
			}

			if err := a.handle(ctx, cmd); err != nil {
				if errors.Is(err, ErrStaleEpoch) {
					a.logger.Error("sessionactor: stale epoch detected -- a newer actor has taken over; evicting self", "error", err)
					return err
				}
				// Any other error is logged but non-fatal to the actor
				// itself: a transient failure handling one command must
				// not stop it from processing the next one, or make it
				// permanently unreachable (that would require an
				// external eviction + a fresh GetOrSpawn to recover
				// from, for no correctness benefit -- this actor is
				// still the legitimate, non-fenced owner of the
				// session).
				a.logger.Error("sessionactor: command handling failed", "error", err)
			}

			if !idleTimer.Stop() {
				select {
				case <-idleTimer.C:
				default:
				}
			}
			idleTimer.Reset(a.timeouts.ActorIdleTTL)
		}
	}
}

// shutdown finishes tearing the actor down after run's own deferred
// close(a.done) has already marked it dead. Deferred by run, so it
// executes exactly once regardless of which branch run returned from.
// Step order is deliberate:
//
//  1. evict from the Registry's map FIRST, so GetOrSpawn stops handing
//     this dead actor out (a caller arriving during the remaining steps
//     misses the map, attempts hydration, and gets a clean
//     ErrSessionActorElsewhere until the lock below is released --
//     fail-fast, retryable, never a silent black hole);
//  2. drain the mailbox, logging any command that slipped in through
//     Send's inherent enqueue-vs-death race (see Send's own comment) --
//     dropped commands are observable in logs, never silently retained;
//  3. release the advisory lock LAST -- the slow, network-bound step,
//     safe to do last precisely because steps 1-2 already made this
//     actor unreachable.
//
// Uses context.Background() rather than run's own ctx: by the time this
// runs, ctx may already be Done (process shutdown, or the idle-TTL/
// ErrStaleEpoch paths that return before any external cancellation) and
// the unlock statement must still be attempted with a live, un-cancelled
// context.
func (a *Actor) shutdown() {
	a.registry.evict(a.sessionID, a)
	a.drainMailbox()
	unlockAndRelease(context.Background(), a.lockConn, a.sessionID)
}

// drainMailbox empties whatever commands were still buffered (or raced
// in) when the run loop exited, logging each one: they were accepted by
// Send but will never be processed by THIS actor. TimerFired commands are
// re-delivered automatically by the timer pump once their claim window
// elapses; any future command type without redelivery semantics will at
// least be visibly accounted for here instead of vanishing.
func (a *Actor) drainMailbox() {
	for {
		select {
		case cmd := <-a.mailbox:
			a.logger.Error(
				"sessionactor: dropping command accepted during shutdown; it will not be processed by this actor",
				"command", fmt.Sprintf("%T", cmd),
			)
		default:
			return
		}
	}
}

// appendRawEvent inserts a session event row inside tx, exactly like
// timerfired.go's own appendEvent, but for a caller that already holds raw
// wire bytes to persist verbatim (Step 18's sandboxevent.go) rather than a
// map[string]any this package would otherwise have to marshal itself --
// skipping that round-trip means the append-only event log holds precisely
// what the sandbox sent, not a lossy re-encoding through an intermediate Go
// value.
func (a *Actor) appendRawEvent(ctx context.Context, tx pgx.Tx, eventType string, raw json.RawMessage) error {
	if _, err := a.stores.event.WithTx(tx).Create(ctx, sqlcgen.CreateEventParams{
		SessionID: a.sessionID,
		Type:      eventType,
		Payload:   raw,
	}); err != nil {
		return fmt.Errorf("sessionactor: append raw %s event: %w", eventType, err)
	}
	// Queue for broadcast AFTER commit (§6.2's "→ broadcast stream", made
	// generic rather than per-handler -- see transact's own doc comment
	// for the commit-then-broadcast, discard-on-rollback ordering).
	a.pendingBroadcast = append(a.pendingBroadcast, raw)
	return nil
}

// transact is the ONLY way this package writes session/turn/sandbox state
// (§2's transactional-write rule): it acquires a FRESH connection from
// the pool (never lockConn),
// begins a transaction, re-reads the session's actor_epoch INSIDE that
// transaction and fences it against the epoch this Actor was hydrated
// with -- returning ErrStaleEpoch (never running fn, never committing) if
// they no longer match, since that proves a newer actor has since taken
// ownership -- and otherwise runs the caller's fn (the actual
// state-transition writes) and commits. §2: "state transition + appended
// event + outbox entries commit in ONE Postgres transaction. There is no
// such thing as a fire-and-forget state write."
//
// Broadcasting (§6.2's "→ broadcast stream") is wired here, generically,
// rather than in any individual command handler: appendEvent/
// appendRawEvent both queue their own already-marshaled payload onto
// a.pendingBroadcast as they run inside fn (see each of their own doc
// comments). On any early return -- fn's own error, or tx.Commit's own
// error -- that queue is discarded (a.pendingBroadcast reset to nil)
// without ever calling a.broadcaster: an event that might still roll back,
// or whose surrounding transaction never actually committed, must never
// be announced to a live client. Only on a SUCCESSFUL commit is the queue
// taken, the field reset to nil, and THEN (deliberately after the reset,
// so a panic or re-entrancy inside Broadcast can never corrupt the next
// call's own queue) each payload handed to a.broadcaster.Broadcast, once
// per item, in order. A nil a.broadcaster (some tests construct an Actor
// without one) is guarded against by skipping the loop entirely.
func (a *Actor) transact(ctx context.Context, fn func(ctx context.Context, tx pgx.Tx) error) error {
	conn, err := a.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("sessionactor: transact: acquire connection: %w", err)
	}
	defer conn.Release()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("sessionactor: transact: begin: %w", err)
	}
	// Rollback is a safety net for every return path other than a
	// successful Commit below; pgx reports (and this discards) an
	// already-closed-transaction error on the no-op case where Commit
	// already ran.
	defer func() { _ = tx.Rollback(ctx) }()

	currentEpoch, err := a.stores.session.WithTx(tx).GetActorEpochForUpdate(ctx, a.sessionID)
	if err != nil {
		return fmt.Errorf("sessionactor: transact: get actor epoch for update: %w", err)
	}
	if currentEpoch != a.epoch {
		return ErrStaleEpoch
	}

	if err := fn(ctx, tx); err != nil {
		a.pendingBroadcast = nil
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		a.pendingBroadcast = nil
		return fmt.Errorf("sessionactor: transact: commit: %w", err)
	}

	a.broadcastPending()
	return nil
}

// broadcastPending delivers every queued event payload (in order) to
// a.broadcaster, then resets the queue -- called only after transact's own
// successful commit above. Takes and resets a.pendingBroadcast BEFORE
// calling Broadcast for any item, so a panic or re-entrant call partway
// through the loop can never leave a stale/duplicated queue for the next
// transact attempt to inherit.
func (a *Actor) broadcastPending() {
	pending := a.pendingBroadcast
	a.pendingBroadcast = nil

	if a.broadcaster == nil {
		return
	}
	for _, payload := range pending {
		a.broadcaster.Broadcast(a.sessionID.String(), payload)
	}
}
