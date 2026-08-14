package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// EventStore is a thin, pass-through wrapper around the sqlc-generated
// events queries (§4.3, §6.1's append-only per-session event log). No
// caching, no retries, no business rules — appending an event as part of
// a state transition is app/sessionactor's job (Step 11+), always inside
// that transition's own transaction (§2: "state transition + appended
// event + outbox entries commit in ONE Postgres transaction"). Reading
// back a page of that log (ListForSession, Step 19) has no such
// transactional requirement — it always runs against the pool, never
// WithTx.
type EventStore struct {
	q *sqlcgen.Queries
}

// NewEventStore builds an EventStore backed by pool.
func NewEventStore(pool *pgxpool.Pool) *EventStore {
	return &EventStore{q: sqlcgen.New(pool)}
}

// WithTx returns an EventStore whose queries run on tx instead of the
// pool this store was built with — used by app/sessionactor's
// transactional-write helper (§2).
func (s *EventStore) WithTx(tx pgx.Tx) *EventStore {
	return &EventStore{q: s.q.WithTx(tx)}
}

// Create upserts an event row on (session_id, message_id) and returns it
// -- CreateEventRow.Inserted reports whether this call actually inserted a
// fresh row (true) or found an already-persisted one from an earlier call
// with the same messageId (false, a genuine resend/dedupe, §6.1).
func (s *EventStore) Create(ctx context.Context, arg sqlcgen.CreateEventParams) (sqlcgen.CreateEventRow, error) {
	return s.q.CreateEvent(ctx, arg)
}

// ListForSession returns up to limit events for sessionID with id >
// afterID, oldest first (afterID = 0 means "from the beginning" -- a null
// fetch_history cursor / the REST endpoint's own ?cursor= default). Shared
// by both the client WS hub's fetch_history/initial-replay handling and
// the REST GET .../events endpoint (§6.2, §6.3) -- one implementation,
// two callers, no duplication.
func (s *EventStore) ListForSession(ctx context.Context, sessionID pgtype.UUID, afterID int64, limit int32) ([]sqlcgen.Event, error) {
	return s.q.ListEventsForSession(ctx, sqlcgen.ListEventsForSessionParams{
		SessionID: sessionID,
		ID:        afterID,
		Limit:     limit,
	})
}

// ListRecentForSession returns up to limit of sessionID's own MOST RECENT
// events, newest id first -- the mirror-image pagination direction of
// ListForSession's own oldest-first cursor page. Used when a caller needs
// only the TAIL of a possibly-long event log (e.g. sessionactor's own
// best-effort plan-content extraction, Step 38) rather than a paginated
// walk from the very beginning of a session's entire history.
func (s *EventStore) ListRecentForSession(ctx context.Context, sessionID pgtype.UUID, limit int32) ([]sqlcgen.Event, error) {
	return s.q.ListRecentEventsForSession(ctx, sqlcgen.ListRecentEventsForSessionParams{
		SessionID: sessionID,
		Limit:     limit,
	})
}

// ListSubTaskStartsForGen and ListSubTaskFinishesForGen (Step 71, §26.4/
// §7.1) back post-hoc sub-task corroboration: reading back this session's
// own already-persisted sub_task_start/sub_task_finish trace, scoped to
// ONE sandbox gen -- the gen the turn being verdicted was actually
// dispatched at (turns.dispatched_sandbox_gen), never merely session_id
// alone. See queries/events.sql's own doc comment on these two queries
// for why gen-scoping (not just session-scoping) is a real correctness
// requirement here, not an optimization.
func (s *EventStore) ListSubTaskStartsForGen(ctx context.Context, sessionID pgtype.UUID, gen int32) ([]sqlcgen.Event, error) {
	return s.q.ListSubTaskStartEventsForGen(ctx, sqlcgen.ListSubTaskStartEventsForGenParams{
		SessionID: sessionID,
		Gen:       gen,
	})
}

// ListSubTaskFinishesForGen is ListSubTaskStartsForGen's own sibling --
// see that method's doc comment immediately above for the full "why".
func (s *EventStore) ListSubTaskFinishesForGen(ctx context.Context, sessionID pgtype.UUID, gen int32) ([]sqlcgen.Event, error) {
	return s.q.ListSubTaskFinishEventsForGen(ctx, sqlcgen.ListSubTaskFinishEventsForGenParams{
		SessionID: sessionID,
		Gen:       gen,
	})
}
