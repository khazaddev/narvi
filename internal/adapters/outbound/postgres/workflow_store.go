package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// WorkflowStore is a thin, pass-through wrapper around the sqlc-generated
// workflow_* queries ("workflow execution engine", §25.6/§25.7/
// §25.8) -- the first real reader/writer of §25.4's own dark schema
// (migrations/000057_workflows.up.sql). No caching, no retries, no
// business rules: definition/binding resolution, run/step-run lifecycle
// decisions, and every workflow.NextStep consultation live in
// internal/app/workflowengine, this store's only caller. Every method
// returns/accepts plain sqlcgen types (never a domain/workflow value) --
// mirroring PlanStore/TurnStore's own identical convention -- so
// workflowengine, not this package, owns converting between the two.
type WorkflowStore struct {
	q *sqlcgen.Queries
}

// NewWorkflowStore builds a WorkflowStore backed by pool.
func NewWorkflowStore(pool *pgxpool.Pool) *WorkflowStore {
	return &WorkflowStore{q: sqlcgen.New(pool)}
}

// WithTx returns a WorkflowStore whose queries run on tx instead of the
// pool this store was built with -- every real caller uses this: engine
// resolution/bookkeeping always runs inside the SAME transaction as the
// turn insert (createTurnLocked) or the turn's own terminal-state write
// (sessionactor's completeProcessingTurn/handleTurnDeadlineTimer/
// failDispatchedTurn), never a second, independently-committed write.
func (s *WorkflowStore) WithTx(tx pgx.Tx) *WorkflowStore {
	return &WorkflowStore{q: s.q.WithTx(tx)}
}

// GetBindingForRepo fetches the repo-specific workflow_bindings row for
// (lane, repoFullName), if one exists -- returns pgx.ErrNoRows (unwrapped)
// when it doesn't, the caller's own signal to fall back to
// GetGlobalBinding (§25.4: a repo override shadows the global binding for
// that one repo only; it is optional).
func (s *WorkflowStore) GetBindingForRepo(ctx context.Context, lane, repoFullName string) (sqlcgen.WorkflowBinding, error) {
	return s.q.GetWorkflowBindingForRepo(ctx, sqlcgen.GetWorkflowBindingForRepoParams{
		Lane:         sqlcgen.WorkflowLane(lane),
		RepoFullName: &repoFullName,
	})
}

// GetGlobalBinding fetches the guaranteed (lane, repo_full_name = NULL)
// workflow_bindings row -- §25.4: seeded by migration 000057 for every
// lane, so this is never truly expected to return pgx.ErrNoRows in a
// correctly-migrated database (see workflowengine's own fail-open handling
// for the defensive case where it somehow does).
func (s *WorkflowStore) GetGlobalBinding(ctx context.Context, lane string) (sqlcgen.WorkflowBinding, error) {
	return s.q.GetGlobalWorkflowBinding(ctx, sqlcgen.WorkflowLane(lane))
}

// GetDefinition fetches one workflow_definitions row by id (minus its
// steps/edges -- see ListStepDefinitions/ListEdgesForDefinition).
func (s *WorkflowStore) GetDefinition(ctx context.Context, id pgtype.UUID) (sqlcgen.WorkflowDefinition, error) {
	return s.q.GetWorkflowDefinition(ctx, id)
}

// ListStepDefinitions fetches every workflow_step_definitions row for
// definitionID, ordered by step_order.
func (s *WorkflowStore) ListStepDefinitions(ctx context.Context, definitionID pgtype.UUID) ([]sqlcgen.WorkflowStepDefinition, error) {
	return s.q.ListWorkflowStepDefinitions(ctx, definitionID)
}

// ListEdgesForDefinition fetches every workflow_edges row for
// definitionID -- the caller groups these by FromStepID when assembling a
// domain/workflow.Definition value (each step carries its own outgoing
// edges there; the flat workflow_edges table has no such per-step
// grouping).
func (s *WorkflowStore) ListEdgesForDefinition(ctx context.Context, definitionID pgtype.UUID) ([]sqlcgen.WorkflowEdge, error) {
	return s.q.ListWorkflowEdgesForDefinition(ctx, definitionID)
}

// GetRunningRunForSession fetches sessionID's own single running
// workflow_runs row, if one exists -- returns pgx.ErrNoRows (unwrapped)
// when it doesn't (no run ever started, or the last one already reached a
// terminal/needs_review state) -- the caller's own signal to start a new
// run via CreateRun.
func (s *WorkflowStore) GetRunningRunForSession(ctx context.Context, sessionID pgtype.UUID) (sqlcgen.WorkflowRun, error) {
	return s.q.GetRunningWorkflowRunForSession(ctx, sessionID)
}

// GetRun fetches one workflow_runs row by id.
func (s *WorkflowStore) GetRun(ctx context.Context, id pgtype.UUID) (sqlcgen.WorkflowRun, error) {
	return s.q.GetWorkflowRun(ctx, id)
}

// CreateRun inserts a new workflow_runs row (status defaults to
// 'running') -- lane/definitionID/definitionVersion are pinned as
// provenance at start time (§25.6), never re-resolved later for this same
// run.
func (s *WorkflowStore) CreateRun(ctx context.Context, sessionID pgtype.UUID, lane string, definitionID pgtype.UUID, definitionVersion int32) (sqlcgen.WorkflowRun, error) {
	return s.q.CreateWorkflowRun(ctx, sqlcgen.CreateWorkflowRunParams{
		SessionID:            sessionID,
		Lane:                 sqlcgen.WorkflowLane(lane),
		WorkflowDefinitionID: definitionID,
		DefinitionVersion:    definitionVersion,
	})
}

// GetLiveStepRunForRun fetches runID's own single live (running or
// awaiting_decision) workflow_step_runs attempt, if one exists -- returns
// pgx.ErrNoRows (unwrapped) when it doesn't (defensive only: a running run
// should always have exactly one live attempt, per this Step's own
// invariant -- see workflowengine's own fail-open handling for this case).
func (s *WorkflowStore) GetLiveStepRunForRun(ctx context.Context, runID pgtype.UUID) (sqlcgen.WorkflowStepRun, error) {
	return s.q.GetLiveWorkflowStepRunForRun(ctx, runID)
}

// GetLiveStepRunByTurnID fetches the running workflow_step_runs attempt
// whose turn_id is turnID, if one exists -- returns pgx.ErrNoRows
// (unwrapped) when it doesn't: turnID was never engine-tracked at all (a
// session's very first turn, inserted by CreateSessionOnTx rather than
// createTurnLocked -- see internal/app/workflowengine/doc.go), or it was a
// turn ResolveStepForNewTurn deliberately resolved-but-did-not-track (see
// that function's own doc comment). Either way, the caller's own signal
// that OnTurnCompleted has nothing to do for this turn.
func (s *WorkflowStore) GetLiveStepRunByTurnID(ctx context.Context, turnID pgtype.UUID) (sqlcgen.WorkflowStepRun, error) {
	return s.q.GetLiveWorkflowStepRunByTurnID(ctx, turnID)
}

// CreateStepRun inserts a new workflow_step_runs row for stepDefinitionID
// within runID (status defaults to 'running', turn_id starts NULL) -- the
// caller attaches the real turn id once it exists (AttachTurn below).
func (s *WorkflowStore) CreateStepRun(ctx context.Context, runID, stepDefinitionID pgtype.UUID) (sqlcgen.WorkflowStepRun, error) {
	return s.q.CreateWorkflowStepRun(ctx, sqlcgen.CreateWorkflowStepRunParams{
		WorkflowRunID:    runID,
		StepDefinitionID: stepDefinitionID,
	})
}

// AttachTurn backfills stepRunID's own turn_id -- called right after the
// real turn insert succeeds (ResolveStepForNewTurn itself runs BEFORE that
// insert, since it must decide the step's PromptTemplate/ModelID to build
// the turn with in the first place).
func (s *WorkflowStore) AttachTurn(ctx context.Context, stepRunID, turnID pgtype.UUID) error {
	return s.q.AttachTurnToWorkflowStepRun(ctx, sqlcgen.AttachTurnToWorkflowStepRunParams{
		ID:     stepRunID,
		TurnID: turnID,
	})
}

// MarkAwaitingDecision transitions stepRunID to 'awaiting_decision'
// (§25.9's HITL gate -- the actual decision mechanics are §25.9's own
// job, but a HITLAfter-gated step's own attempt must still land here, not
// 'completed', once its turn finishes) -- outcomeStatus is recorded via
// COALESCE so an outcome ALREADY posted via the step-outcome tool during
// the turn is never overwritten. finished_at stays NULL (this attempt is
// still "live" per migration 000057's own doc comment).
func (s *WorkflowStore) MarkAwaitingDecision(ctx context.Context, stepRunID pgtype.UUID, outcomeStatus string) (sqlcgen.WorkflowStepRun, error) {
	status := sqlcgen.WorkflowStepOutcomeStatus(outcomeStatus)
	return s.q.MarkWorkflowStepRunAwaitingDecision(ctx, sqlcgen.MarkWorkflowStepRunAwaitingDecisionParams{
		ID:            stepRunID,
		OutcomeStatus: &status,
	})
}

// FinishStepRun transitions stepRunID to a terminal status (completed,
// failed, or cancelled -- never awaiting_decision, MarkAwaitingDecision's
// own exclusive job) and stamps finished_at. outcomeStatus is recorded via
// the SAME COALESCE-never-overwrite discipline as MarkAwaitingDecision.
func (s *WorkflowStore) FinishStepRun(ctx context.Context, stepRunID pgtype.UUID, status, outcomeStatus string) (sqlcgen.WorkflowStepRun, error) {
	os := sqlcgen.WorkflowStepOutcomeStatus(outcomeStatus)
	return s.q.FinishWorkflowStepRun(ctx, sqlcgen.FinishWorkflowStepRunParams{
		ID:            stepRunID,
		Status:        sqlcgen.WorkflowStepRunStatus(status),
		OutcomeStatus: &os,
	})
}

// CompleteRun transitions runID to 'completed' and stamps finished_at --
// workflow.NextComplete's own consequence (§25.4/§25.6).
func (s *WorkflowStore) CompleteRun(ctx context.Context, runID pgtype.UUID) (sqlcgen.WorkflowRun, error) {
	return s.q.CompleteWorkflowRun(ctx, runID)
}

// EscalateRun transitions runID to 'needs_review' -- workflow.NextEscalate's
// own consequence (§25.4/§25.9): non-terminal (finished_at stays NULL),
// parked for a human decision. Posting an actual notice about this
// escalation is §25.9's own job (§25.9: "one notice, never repeated");
// this method only ever flips the status column.
func (s *WorkflowStore) EscalateRun(ctx context.Context, runID pgtype.UUID) (sqlcgen.WorkflowRun, error) {
	return s.q.EscalateWorkflowRun(ctx, runID)
}

// FailRun transitions runID to 'failed' and stamps finished_at -- §25.9's
// own ("workflow HITL gate + circuit breaker", §25.9) consequence of a
// winning HITL reject verdict: mirrors CompleteRun's own shape exactly,
// landing on 'failed' instead of 'completed' (a human's reject IS the run's
// own final word, a terminal outcome like completion, never
// 'needs_review's "waiting on a human" semantics).
func (s *WorkflowStore) FailRun(ctx context.Context, runID pgtype.UUID) (sqlcgen.WorkflowRun, error) {
	return s.q.FailWorkflowRun(ctx, runID)
}

// SetStepRunOutcome persists the generic step-outcome-posting tool's own
// typed payload ({status, summary, structuredPayload}, §25.6) onto
// stepRunID -- guarded to the attempt currently actually 'running' (the
// underlying SQL's own "AND status = 'running'"), so rowsAffected == 0
// tells the caller there was no live attempt to post onto (already
// finished, or awaiting a HITL decision) without a separate existence
// check. outcomePayload is opaque json.RawMessage, stored and later
// consumed verbatim -- never re-parsed here (§25.6's own typed-handoff
// discipline, review.RenderTurnPrompt's precedent).
func (s *WorkflowStore) SetStepRunOutcome(ctx context.Context, stepRunID pgtype.UUID, outcomeStatus, summary string, payload []byte) (int64, error) {
	os := sqlcgen.WorkflowStepOutcomeStatus(outcomeStatus)
	return s.q.SetWorkflowStepRunOutcome(ctx, sqlcgen.SetWorkflowStepRunOutcomeParams{
		ID:             stepRunID,
		OutcomeStatus:  &os,
		OutcomeSummary: &summary,
		OutcomePayload: payload,
	})
}

// GetStepRun fetches one workflow_step_runs row by id -- §25.9's own
// ("workflow HITL gate + circuit breaker", §25.9) decide endpoint's first
// read (mirrors PlanStore.Get's identical role in decideplan.go): resolves
// the target attempt the caller named (POST /api/workflow-runs/:runId/
// steps/:stepRunId/decide's own stepRunId) before deciding it, and re-fetches
// it after the guarded decision UPDATE to learn its REAL current state
// either way (won or already decided).
func (s *WorkflowStore) GetStepRun(ctx context.Context, id pgtype.UUID) (sqlcgen.WorkflowStepRun, error) {
	return s.q.GetWorkflowStepRun(ctx, id)
}

// CountStepRunsForStepDefinition returns how many workflow_step_runs rows
// ALREADY exist for stepDefinitionID within runID -- §25.5's own "iteration
// count reads COUNT(*) on workflow_step_runs, no dedicated counter column",
// scoped to the ONE run and ONE step definition about to receive a new
// attempt. The engine (internal/app/workflowengine) consults this ONLY when
// a needs_fix edge is about to advance to this step, to decide whether
// loopguard.Evaluate even needs consulting at all: zero means this is the
// step's first-ever attempt in this run (not a re-fire, proceed directly);
// more than zero means it is, and the returned count is exactly the
// loopguard.State.AttemptCount to evaluate against.
func (s *WorkflowStore) CountStepRunsForStepDefinition(ctx context.Context, runID, stepDefinitionID pgtype.UUID) (int64, error) {
	return s.q.CountWorkflowStepRunsForStepDefinition(ctx, sqlcgen.CountWorkflowStepRunsForStepDefinitionParams{
		WorkflowRunID:    runID,
		StepDefinitionID: stepDefinitionID,
	})
}

// DecideStepRun renders verdict (approve/reject/revise) on stepRunID,
// guarded to the SAME "AND status = 'awaiting_decision'" precondition for
// all three (§25.9) -- mirrors PlanStore's own ApproveIfAwaitingApproval/
// RejectIfAwaitingApproval :execrows shape exactly: rowsAffected == 0 means
// this call did NOT win the decision (already decided by an earlier call,
// or a stale/foreign id), rowsAffected == 1 means it did. decisionText is
// nil for approve, optional for reject, and the caller's own job to require
// non-empty for revise (this store applies no such validation itself,
// mirroring SetStepRunOutcome's own "store persists, callers validate"
// division of labor). decidedBy mirrors DecidePlanOnTx's own decidedBy
// convention (an explicitly invalid pgtype.UUID for a not-yet-supported
// bot/channel-attributed decision -- every current caller of this endpoint
// is REST, always passing a real authenticated actorUserID).
func (s *WorkflowStore) DecideStepRun(ctx context.Context, stepRunID pgtype.UUID, verdict string, decisionText *string, decidedBy pgtype.UUID) (int64, error) {
	switch verdict {
	case "approve":
		return s.q.DecideWorkflowStepRunApprove(ctx, sqlcgen.DecideWorkflowStepRunApproveParams{ID: stepRunID, DecisionText: decisionText, DecidedBy: decidedBy})
	case "reject":
		return s.q.DecideWorkflowStepRunReject(ctx, sqlcgen.DecideWorkflowStepRunRejectParams{ID: stepRunID, DecisionText: decisionText, DecidedBy: decidedBy})
	case "revise":
		return s.q.DecideWorkflowStepRunRevise(ctx, sqlcgen.DecideWorkflowStepRunReviseParams{ID: stepRunID, DecisionText: decisionText, DecidedBy: decidedBy})
	default:
		return 0, fmt.Errorf("postgres: unrecognized workflow step decision verdict %q", verdict)
	}
}

// ClaimEscalationNotice atomically claims the right to send runID's own
// ONE-TIME "this run now needs a human's attention" notice (§25.9,
// mirroring §24.6's own "never repeated" exemption mechanism) -- guarded to
// "AND needs_review_notified_at IS NULL" (migrations/000058_workflow_hitl.up.sql),
// so of any number of concurrent or repeated calls against the SAME run,
// rowsAffected == 1 for exactly one of them (the caller that must actually
// enqueue the notification) and == 0 for every other (already claimed --
// send nothing further). Independent of, and always called alongside,
// EscalateRun -- claiming is idempotent regardless of how many times a run
// is (redundantly) escalated.
func (s *WorkflowStore) ClaimEscalationNotice(ctx context.Context, runID pgtype.UUID) (int64, error) {
	return s.q.ClaimWorkflowRunEscalationNotice(ctx, runID)
}
