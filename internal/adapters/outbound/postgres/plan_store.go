package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// PlanStore is a thin, pass-through wrapper around the sqlc-generated
// plans queries (Step 37, "plan mode, web", §8.1/§12.2 item 3). No
// caching, no retries, no business rules -- version numbering and
// supersede decisions live in internal/domain/plan; internal/app/
// sessionactor/planrecord.go and internal/adapters/inbound/httpapi/
// planapprove.go are this store's only callers.
type PlanStore struct {
	q *sqlcgen.Queries
}

// NewPlanStore builds a PlanStore backed by pool.
func NewPlanStore(pool *pgxpool.Pool) *PlanStore {
	return &PlanStore{q: sqlcgen.New(pool)}
}

// WithTx returns a PlanStore whose queries run on tx instead of the pool
// this store was built with -- every real caller uses this: a plan row's
// creation must supersede the prior awaiting_approval row (if any) and
// insert the new one in the SAME transaction as the producing turn's own
// terminal-state write (planrecord.go), and an approve/reject decision
// must run its guarded UPDATE in the SAME transaction as the session-row
// lock and (for approve) the new turn's insert (planapprove.go).
func (s *PlanStore) WithTx(tx pgx.Tx) *PlanStore {
	return &PlanStore{q: s.q.WithTx(tx)}
}

// Create inserts a new plan row and returns it.
func (s *PlanStore) Create(ctx context.Context, arg sqlcgen.CreatePlanParams) (sqlcgen.Plan, error) {
	return s.q.CreatePlan(ctx, arg)
}

// Supersede marks id 'superseded' -- a no-op (0 rows affected, no error)
// if id is not currently 'awaiting_approval'; see SupersedePlan's own
// generated doc comment for why that guard exists even though every real
// caller already pre-filters to StatusAwaitingApproval ids.
func (s *PlanStore) Supersede(ctx context.Context, id pgtype.UUID) error {
	return s.q.SupersedePlan(ctx, id)
}

// ListSummariesForSession fetches the minimal (id, version, status) shape
// internal/domain/plan.NextVersion/ShouldSupersede need for sessionID's
// own plan history.
func (s *PlanStore) ListSummariesForSession(ctx context.Context, sessionID pgtype.UUID) ([]sqlcgen.ListPlanSummariesForSessionRow, error) {
	return s.q.ListPlanSummariesForSession(ctx, sessionID)
}

// ListForSession fetches every FULL plan row for sessionID, ordered by
// version -- audit-fix batch (completeness/discoverability, M3) addition
// backing GET /api/sessions/:id/plans (internal/adapters/inbound/httpapi/
// plans.go), unlike ListSummariesForSession's own internal, minimal shape
// above.
func (s *PlanStore) ListForSession(ctx context.Context, sessionID pgtype.UUID) ([]sqlcgen.Plan, error) {
	return s.q.ListPlansForSession(ctx, sessionID)
}

// ApproveIfAwaitingApproval guardedly transitions planID to 'approved',
// only if it belongs to sessionID and is still 'awaiting_approval'.
// rowsAffected == 0 means it was already decided by someone else (or
// planID/sessionID don't match any real row) -- the caller's own signal
// to return 409 Conflict rather than treating this as a hard error.
func (s *PlanStore) ApproveIfAwaitingApproval(ctx context.Context, planID, sessionID, decidedBy pgtype.UUID) (int64, error) {
	return s.q.ApprovePlanIfAwaitingApproval(ctx, sqlcgen.ApprovePlanIfAwaitingApprovalParams{
		ID:        planID,
		SessionID: sessionID,
		DecidedBy: decidedBy,
	})
}

// RejectIfAwaitingApproval is ApproveIfAwaitingApproval's rejection twin.
func (s *PlanStore) RejectIfAwaitingApproval(ctx context.Context, planID, sessionID, decidedBy pgtype.UUID) (int64, error) {
	return s.q.RejectPlanIfAwaitingApproval(ctx, sqlcgen.RejectPlanIfAwaitingApprovalParams{
		ID:        planID,
		SessionID: sessionID,
		DecidedBy: decidedBy,
	})
}

// Get fetches a plan row by id -- Step 38's ("plan mode, cross-channel",
// §8.1/§13.3) own addition, used by httpapi.DecidePlanOnTx to re-fetch a
// plan's own current state (status, slack_channel_id/slack_message_ts)
// after its own guarded UPDATE, whether that UPDATE won or lost. Returns
// pgx.ErrNoRows (unwrapped) for a nonexistent id.
func (s *PlanStore) Get(ctx context.Context, id pgtype.UUID) (sqlcgen.Plan, error) {
	return s.q.GetPlan(ctx, id)
}

// SetSlackMessageRef persists the real Slack channel+message-timestamp a
// successful chat.postMessage call for planID's own approval-request
// message returned -- Step 38's own addition, called exactly once, by
// internal/app/outboxworker's Slack plan-approval notifier, right after
// that call succeeds. A later decision (from ANY entry point) reads this
// back (via Get above) to know which Slack message to chat.update.
func (s *PlanStore) SetSlackMessageRef(ctx context.Context, id pgtype.UUID, channelID, messageTS string) error {
	return s.q.SetPlanSlackMessageRef(ctx, sqlcgen.SetPlanSlackMessageRefParams{
		ID:             id,
		SlackChannelID: &channelID,
		SlackMessageTs: &messageTS,
	})
}

// ListAwaitingApproval returns every plan still 'awaiting_approval',
// system-wide, joined with each plan's own session for (created_by,
// title) -- Step 60's own awaiting_approval row source (see
// ListAwaitingApprovalPlans' own generated doc comment for the full
// design).
func (s *PlanStore) ListAwaitingApproval(ctx context.Context) ([]sqlcgen.ListAwaitingApprovalPlansRow, error) {
	return s.q.ListAwaitingApprovalPlans(ctx)
}

// ListRecentlyDecided returns up to limit plans decided (approved or
// rejected) at or after since -- Step 60's own decision-latency metric
// input (see ListRecentlyDecidedPlans' own generated doc comment).
func (s *PlanStore) ListRecentlyDecided(ctx context.Context, since pgtype.Timestamptz, limit int32) ([]sqlcgen.Plan, error) {
	return s.q.ListRecentlyDecidedPlans(ctx, sqlcgen.ListRecentlyDecidedPlansParams{DecidedAt: since, Limit: limit})
}
