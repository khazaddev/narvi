package sessionactor

import (
	"context"
	"sync"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/platform"
)

// storeBundle bundles the store handles this package's Registry and every
// Actor it hydrates share -- built once in NewRegistry, then referenced
// (never rebuilt) everywhere else. Each store is pool-scoped by default;
// callers needing a transaction-scoped one call its WithTx method (see
// actor.go's transact and timerpump.go's claimDueTimers).
type storeBundle struct {
	session *postgres.SessionStore
	turn    *postgres.TurnStore
	sandbox *postgres.SandboxStore
	timer   *postgres.TimerStore
	event   *postgres.EventStore
}

func newStoreBundle(pool *pgxpool.Pool) storeBundle {
	return storeBundle{
		session: postgres.NewSessionStore(pool),
		turn:    postgres.NewTurnStore(pool),
		sandbox: postgres.NewSandboxStore(pool),
		timer:   postgres.NewTimerStore(pool),
		event:   postgres.NewEventStore(pool),
	}
}

// Registry is the process-wide supervisor of session actors (§2's "one
// goroutine + mailbox per active session", scoped to THIS process --
// other pods run their own independent Registry against the same
// Postgres). At most one live *Actor per session id exists in this
// process's actors map at any time; Registry's own mutex is what makes
// that true within a process, while the Postgres advisory lock
// (hydrateAndAcquire, hydrate.go) is what makes it true ACROSS processes.
type Registry struct {
	mu     sync.Mutex
	actors map[pgtype.UUID]*Actor

	pool     *pgxpool.Pool
	timeouts platform.Timeouts
	stores   storeBundle

	// broadcaster is threaded through to every Actor this Registry
	// hydrates (§6.2's "→ broadcast stream", made real via
	// internal/adapters/inbound/wshub's *Hub -- see
	// internal/app/ports.EventBroadcaster's own doc comment for the full
	// rationale). May be nil (some tests construct a Registry without
	// one) -- Actor.broadcastPending already guards against that.
	broadcaster ports.EventBroadcaster

	// group tracks every actor's mailbox-loop goroutine, so evicted/
	// crashed actors are cleanly reaped and Shutdown can wait on all of
	// them. Deliberately the zero value, NOT errgroup.WithContext(...) --
	// see doc.go's Concurrency section: a shared cancel-on-first-error
	// context would let one session's failure tear down every OTHER
	// session's actor sharing this process, which is exactly what
	// single-session ownership must not allow.
	group errgroup.Group

	// lifecycleCtx is the parent context every actor's run loop derives
	// its own cancellation from -- intentionally the PROCESS's lifetime
	// (supplied once at construction), never any individual caller's
	// request-scoped context: an actor spawned to satisfy one inbound
	// request or one timer-pump delivery must keep running long after
	// that request's own context is done. Storing a context in a struct
	// field is unusual, but this is the recognized exception -- a
	// long-lived component's own lifetime signal, not a request-scoped
	// value threaded through call arguments.
	lifecycleCtx context.Context
	cancel       context.CancelFunc
}

// NewRegistry builds a Registry backed by pool. ctx represents the
// process's own lifetime; Shutdown cancels the context every spawned
// actor's run loop derives from. broadcaster is threaded through to every
// Actor this Registry hydrates (§6.2's "→ broadcast stream") -- may be
// nil, in which case every Actor simply never broadcasts (see
// Actor.broadcastPending).
func NewRegistry(ctx context.Context, pool *pgxpool.Pool, timeouts platform.Timeouts, broadcaster ports.EventBroadcaster) *Registry {
	lifecycleCtx, cancel := context.WithCancel(ctx)
	return &Registry{
		actors:       make(map[pgtype.UUID]*Actor),
		pool:         pool,
		timeouts:     timeouts,
		stores:       newStoreBundle(pool),
		broadcaster:  broadcaster,
		lifecycleCtx: lifecycleCtx,
		cancel:       cancel,
	}
}

// GetOrSpawn returns the live local Actor for sessionID if this process
// already has one running, otherwise hydrates and starts a new one (§2:
// "hydration on demand"). Returns ErrSessionActorElsewhere if another
// owner already holds the session's advisory lock -- deliberately never
// blocks waiting for it, so a caller in a later Step can route the
// request to whichever pod actually holds the session rather than
// hanging on a lock that may not release for the rest of that actor's
// lifetime.
func (r *Registry) GetOrSpawn(ctx context.Context, sessionID pgtype.UUID) (*Actor, error) {
	if a := r.lookup(sessionID); a != nil {
		return a, nil
	}

	// Hydration + the advisory-lock attempt run WITHOUT the registry
	// mutex held: they are the slow, I/O-bound part, and the Postgres
	// advisory lock itself -- not this mutex -- is the mechanism that
	// must arbitrate two concurrent attempts for the SAME sessionID,
	// whether those two attempts come from two different processes, or
	// two goroutines in this same process each racing past the lookup
	// above before either has inserted into the map. Holding the mutex
	// across this whole sequence would needlessly serialize spawning of
	// completely UNRELATED sessions in this process, for no correctness
	// benefit.
	a, err := r.hydrateAndAcquire(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	// No re-check-the-map-after-hydrating race is possible here: the
	// Postgres advisory lock this call just won is the sole arbiter of
	// ownership, so by construction no OTHER goroutine (in this process
	// or any other) could have concurrently also won it and already
	// inserted a competing entry for sessionID.
	r.mu.Lock()
	r.actors[sessionID] = a
	r.mu.Unlock()

	r.group.Go(func() error {
		return a.run(r.lifecycleCtx)
	})

	return a, nil
}

func (r *Registry) lookup(sessionID pgtype.UUID) *Actor {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.actors[sessionID]
}

// evict removes a from the registry's map, but only if a is still the
// entry on file for sessionID -- defensive against a (should-be-
// impossible, per GetOrSpawn's own reasoning) double-insert.
func (r *Registry) evict(sessionID pgtype.UUID, a *Actor) {
	r.mu.Lock()
	if cur, ok := r.actors[sessionID]; ok && cur == a {
		delete(r.actors, sessionID)
	}
	r.mu.Unlock()
}

// Shutdown cancels every live actor's run loop (each releases its
// advisory lock and evicts itself as it exits, per Actor.shutdown) and
// waits for all of them, plus any timer-pump goroutine started through
// this Registry's own group, to finish.
func (r *Registry) Shutdown() error {
	r.cancel()
	return r.group.Wait()
}
