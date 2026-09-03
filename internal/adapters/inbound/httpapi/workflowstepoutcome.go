// This file (workflowstepoutcome.go) implements §25.6's ("workflow
// execution engine", §25.6) own GENERIC step-outcome-posting tool: POST
// /sessions/{sessionID}/workflow/step-outcome. Structurally mirrors
// internal/domain/reviewpost's existing verdict-posting shape
// ({status, summary, structuredPayload}, per §25.6) and, at the transport
// level, PostReviewVerdict's own sandbox-bearer-authenticated-endpoint
// design exactly -- see reviewverdict.go's own doc comment for the full
// "why an HTTP endpoint, not a genuine OpenCode/LLM function-call tool"
// reasoning, which applies here without modification: nothing in this
// codebase's AgentRuntime port or sandbox WS protocol defines a native
// tool-calling mechanism, so this reuses the SAME established
// sandbox-bearer-token/gen-fencing scheme scmcredentials.go/snapshotmint.go/
// reviewverdict.go already establish for "agent-initiated, server-
// validated" actions. A step whose own prompt instructs the agent to call
// this endpoint (no §25.8 built-in does, since none of their prompts
// change) is the natural future place to document the URL/bearer
// token/gen header/JSON shape, exactly like review.RenderTurnPrompt
// already does for the review-verdict tool.
//
// Unlike PostReviewVerdict, this endpoint takes no run/step ids from the
// caller at all: it resolves the calling session's own currently-running
// WorkflowRun and that run's own live (status='running') WorkflowStepRun
// itself (§25.6: "the ONLY live attempt per run", migration 000057) -- an
// agent inside a sandbox has no reason to know either id, only that it is
// reporting on the step it is currently executing.

package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/narvidev/narvi/contracts/gen/go/restdtos"
	"github.com/narvidev/narvi/internal/adapters/outbound/postgres"
	"github.com/narvidev/narvi/internal/domain/sandbox"
	"github.com/narvidev/narvi/internal/platform"
)

// PostWorkflowStepOutcome backs POST /sessions/{sessionID}/workflow/step-outcome
// (note: no /api prefix -- a sandbox-to-CP endpoint, not a browser-facing
// REST route, mirroring scm-credentials/snapshot-mint/review-verdict
// exactly). Outcome table:
//
//  1. sessionID does not parse as a UUID, or no sandbox row exists for it
//     -> 404 (mirrors scmcredentials.go/reviewverdict.go's own identical
//     "malformed and nonexistent both mean no such session" precedent).
//  2. Authorization: Bearer <token> missing/malformed -> 401.
//  3. sandbox.IsDeadSandboxStatus(sandboxRow.Status) -> 410 (checked
//     before the gen/token comparisons, same ordering as every other
//     sandbox-bearer-authenticated endpoint in this package).
//  4. The presented X-Sandbox-Gen header is missing/malformed, or parses
//     but does not equal sandboxRow.Gen -> 403 (§9.3 scenario #6 parity).
//  5. The presented bearer token fails verifySandboxBearerToken -> 401.
//  6. Malformed request body (fails to decode as restdtos.
//     PostWorkflowStepOutcomeRequest -- the generated type's own
//     UnmarshalJSON already rejects a missing/out-of-enum status or an
//     empty summary) -> 400.
//  7. This session has no currently-running WorkflowRun, or that run has
//     no live (status='running') WorkflowStepRun -- meaningless calls,
//     this session's current turn is not a live, engine-tracked workflow
//     step attempt at all -> 400 (mirrors reviewverdict.go's own "no PR to
//     act on" 400 precedent for the structurally identical "nothing to
//     post onto" case).
//  8. The guarded UPDATE (WorkflowStore.SetStepRunOutcome, "AND
//     status = 'running'") affects zero rows -- a race: the live attempt
//     step 7 just found reached a terminal/awaiting_decision state
//     between that read and this write -> 409.
//  9. Otherwise -> 201 with restdtos.PostWorkflowStepOutcomeResponse.
func PostWorkflowStepOutcome(sandboxes *postgres.SandboxStore, workflows *postgres.WorkflowStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		var sessionID pgtype.UUID
		if err := sessionID.Scan(chi.URLParam(r, "sessionID")); err != nil {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		ctx = platform.WithSessionID(ctx, sessionID.String())
		logger := platform.Logger(ctx)

		token, ok := bearerTokenFromHeader(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "missing or malformed authorization header")
			return
		}

		sandboxRow, err := sandboxes.Get(ctx, sessionID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "session not found")
				return
			}
			logger.Error("httpapi: workflow-step-outcome: get sandbox failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		if sandbox.IsDeadSandboxStatus(sandbox.State(sandboxRow.Status)) {
			writeError(w, http.StatusGone, "session stopped")
			return
		}

		presentedGen, genErr := strconv.Atoi(r.Header.Get("X-Sandbox-Gen"))
		if genErr != nil || presentedGen != int(sandboxRow.Gen) {
			logger.Warn("httpapi: workflow-step-outcome: rejecting: gen mismatch",
				"presented_gen_header", r.Header.Get("X-Sandbox-Gen"), "sandbox_gen", sandboxRow.Gen)
			writeError(w, http.StatusForbidden, "no usable outcome-posting credential for this session")
			return
		}

		if !verifySandboxBearerToken(token, sandboxRow.TokenHash) {
			writeError(w, http.StatusUnauthorized, "invalid sandbox token")
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
		var req restdtos.PostWorkflowStepOutcomeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "malformed request body: "+err.Error())
			return
		}

		runRow, err := workflows.GetRunningRunForSession(ctx, sessionID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusBadRequest, "this session has no running workflow to post a step outcome onto")
				return
			}
			logger.Error("httpapi: workflow-step-outcome: get running run failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		stepRun, err := workflows.GetLiveStepRunForRun(ctx, runRow.ID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusBadRequest, "this session's workflow run has no live step to post an outcome onto")
				return
			}
			logger.Error("httpapi: workflow-step-outcome: get live step run failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		var payload []byte
		if req.StructuredPayload != nil {
			payload = *req.StructuredPayload
		}

		rowsAffected, err := workflows.SetStepRunOutcome(ctx, stepRun.ID, string(req.Status), req.Summary, payload)
		if err != nil {
			logger.Error("httpapi: workflow-step-outcome: set outcome failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if rowsAffected == 0 {
			// A race: the live attempt found above reached a terminal/
			// awaiting_decision state between that read and this guarded
			// write (e.g. the turn itself just finished and
			// OnTurnCompleted's own implicit-outcome path already beat this
			// call to it).
			writeError(w, http.StatusConflict, "the step this outcome targeted is no longer live")
			return
		}

		writeJSON(w, http.StatusCreated, restdtos.PostWorkflowStepOutcomeResponse{
			StepRunId:     stepRun.ID.String(),
			WorkflowRunId: runRow.ID.String(),
		})
	}
}
