// This file (workflowruns.go) implements §25.10's own two run-read
// routes: GET /api/sessions/{sessionID}/workflow-runs (a
// session's own runs, newest first) and GET /api/workflow-runs/{runId}
// (one run WITH its ordered step runs -- "a run without its steps
// answers no question anybody asks", §25.10). Both are READ-ONLY: runs
// are created and advanced exclusively by the execution engine
// (internal/app/workflowengine, §25.6), never via any request DTO here.
//
// Both routes are gated by the SAME session-read gate every other
// /api/sessions/{id}/... route in this package uses (§25.10's own route
// table: "the same session-read gate the other /api/sessions/{id}/...
// routes use — read them, do not invent one") -- session exists, caller
// is authenticated, no separate authz.Authorize call, mirroring
// GetSession/ListEvents/ListArtifacts' own identical precedent exactly:
// authz.ActionViewSessions (§13.3 row 1) already allows every role
// including viewer, so a per-call Authorize would add nothing a 401 from
// auth.Middleware doesn't already guarantee. GetWorkflowRun is NOT
// mounted under /api/sessions/{sessionID}/... at all (its own URL only
// carries runId), so it resolves the OWNING session from the run row
// itself before applying this same gate.

package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/narvidev/narvi/contracts/gen/go/restdtos"
	"github.com/narvidev/narvi/internal/adapters/outbound/postgres"
	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	appreviewtriage "github.com/narvidev/narvi/internal/app/reviewtriage"
	"github.com/narvidev/narvi/internal/platform"
)

// workflowRunToDTO converts one workflow_runs row to its own wire shape
// -- shared by ListSessionWorkflowRuns and GetWorkflowRun so neither
// independently drifts from the other's own field-by-field rendering.
func workflowRunToDTO(run sqlcgen.WorkflowRun) restdtos.WorkflowRun {
	var finishedAt *time.Time
	if run.FinishedAt.Valid {
		t := run.FinishedAt.Time
		finishedAt = &t
	}
	return restdtos.WorkflowRun{
		Id:                   run.ID.String(),
		SessionId:            run.SessionID.String(),
		Lane:                 restdtos.WorkflowRunLane(run.Lane),
		WorkflowDefinitionId: run.WorkflowDefinitionID.String(),
		DefinitionVersion:    int(run.DefinitionVersion),
		Status:               restdtos.WorkflowRunStatus(run.Status),
		CreatedAt:            run.CreatedAt.Time,
		UpdatedAt:            run.UpdatedAt.Time,
		FinishedAt:           finishedAt,
	}
}

// workflowStepRunToDTO converts one workflow_step_runs row (joined with
// its own turn's model_id/cost_usd, §25.15 -- ListWorkflowStepRunsForRun's
// own generated doc comment, queries/workflows.sql) to its own wire
// shape.
func workflowStepRunToDTO(sr sqlcgen.ListWorkflowStepRunsForRunRow) restdtos.WorkflowStepRun {
	var turnID *string
	if sr.TurnID.Valid {
		s := sr.TurnID.String()
		turnID = &s
	}
	// WorkflowStepRunOutcomeStatus is a {Value interface{}} wrapper, not a
	// plain string type -- its own JSON schema enum explicitly lists null
	// as a legal value ("enum": ["ok","needs_fix","blocked", null]),
	// which go-jsonschema renders as a wrapper struct whose MarshalJSON
	// just re-marshals Value verbatim (mirrors WorkflowStepRunDecision's
	// own identical shape, immediately below).
	var outcomeStatus *restdtos.WorkflowStepRunOutcomeStatus
	if sr.OutcomeStatus != nil {
		outcomeStatus = &restdtos.WorkflowStepRunOutcomeStatus{Value: string(*sr.OutcomeStatus)}
	}
	var decision *restdtos.WorkflowStepRunDecision
	if sr.Decision != nil {
		decision = &restdtos.WorkflowStepRunDecision{Value: string(*sr.Decision)}
	}
	var decidedAt *time.Time
	if sr.DecidedAt.Valid {
		t := sr.DecidedAt.Time
		decidedAt = &t
	}
	var decidedBy *string
	if sr.DecidedBy.Valid {
		s := sr.DecidedBy.String()
		decidedBy = &s
	}
	var finishedAt *time.Time
	if sr.FinishedAt.Valid {
		t := sr.FinishedAt.Time
		finishedAt = &t
	}
	// §25.15: modelId/costUsd are the joined turn's own turns.model_id/
	// turns.cost_usd -- absent (nil) exactly when turnId itself is nil
	// (sr.TurnID invalid) OR the turn's own column is itself NULL (model
	// inherited the session default; no cost has arrived yet). costUsd in
	// particular must stay nil rather than become a fabricated 0 --
	// NumericToFloat64's own ok=false return for a SQL NULL is exactly
	// what keeps that distinction (§25.15: "no cost yet must never render
	// as free").
	var costUSD *float64
	if v, ok := appreviewtriage.NumericToFloat64(sr.TurnCostUsd); ok {
		costUSD = &v
	}
	return restdtos.WorkflowStepRun{
		Id:               sr.ID.String(),
		WorkflowRunId:    sr.WorkflowRunID.String(),
		StepDefinitionId: sr.StepDefinitionID.String(),
		TurnId:           restdtos.WorkflowStepRunTurnId(turnID),
		Status:           restdtos.WorkflowStepRunStatus(sr.Status),
		OutcomeStatus:    outcomeStatus,
		OutcomeSummary:   restdtos.WorkflowStepRunOutcomeSummary(sr.OutcomeSummary),
		Decision:         decision,
		DecidedAt:        decidedAt,
		DecidedBy:        restdtos.WorkflowStepRunDecidedBy(decidedBy),
		CreatedAt:        sr.CreatedAt.Time,
		FinishedAt:       finishedAt,
		ModelId:          restdtos.WorkflowStepRunModelId(sr.TurnModelID),
		CostUsd:          restdtos.WorkflowStepRunCostUsd(costUSD),
	}
}

// ListSessionWorkflowRuns backs GET /api/sessions/{sessionID}/workflow-runs
// (§25.10): 404 if the session doesn't exist, else 200 with this
// session's own runs, newest first.
func ListSessionWorkflowRuns(sessions *postgres.SessionStore, workflows *postgres.WorkflowStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessionID, ok := parseSessionID(w, r)
		if !ok {
			return
		}
		ctx := platform.WithSessionID(r.Context(), sessionID.String())
		logger := platform.Logger(ctx)

		if _, err := sessions.Get(ctx, sessionID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "session not found")
				return
			}
			logger.Error("httpapi: get session for workflow-runs list failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		rows, err := workflows.ListRunsForSession(ctx, sessionID)
		if err != nil {
			logger.Error("httpapi: list workflow runs for session failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		out := make([]restdtos.WorkflowRun, 0, len(rows))
		for _, row := range rows {
			out = append(out, workflowRunToDTO(row))
		}

		writeJSON(w, http.StatusOK, restdtos.ListWorkflowRunsResponse{Runs: out})
	}
}

// GetWorkflowRun backs GET /api/workflow-runs/{runId} (§25.10): the run
// WITH its ordered step runs. 404 if runId names no run; the same
// session-read gate as ListSessionWorkflowRuns above, resolved via this
// run's own sessionId (this route's URL carries no sessionID of its
// own).
func GetWorkflowRun(sessions *postgres.SessionStore, workflows *postgres.WorkflowStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		logger := platform.Logger(ctx)

		runID, ok := parseWorkflowRunID(w, r)
		if !ok {
			return
		}

		run, err := workflows.GetRun(ctx, runID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "workflow run not found")
				return
			}
			logger.Error("httpapi: get workflow run failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		// Session-read gate, resolved via the run's own sessionId --
		// mirrors ListSessionWorkflowRuns' own identical "session exists,
		// caller authenticated" check (this file's own top doc comment).
		if _, err := sessions.Get(ctx, run.SessionID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// Defensive: workflow_runs.session_id is ON DELETE CASCADE
				// (migration 000057), so a run whose session was deleted
				// would already be gone too -- should be unreachable.
				writeError(w, http.StatusNotFound, "workflow run not found")
				return
			}
			logger.Error("httpapi: get session for workflow run failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		stepRuns, err := workflows.ListStepRunsForRun(ctx, runID)
		if err != nil {
			logger.Error("httpapi: list step runs for workflow run failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		wireStepRuns := make([]restdtos.WorkflowStepRun, 0, len(stepRuns))
		for _, sr := range stepRuns {
			wireStepRuns = append(wireStepRuns, workflowStepRunToDTO(sr))
		}

		writeJSON(w, http.StatusOK, restdtos.WorkflowRunDetail{
			Run:      workflowRunToDTO(run),
			StepRuns: wireStepRuns,
		})
	}
}
