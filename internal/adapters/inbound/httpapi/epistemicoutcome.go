// This file (epistemicoutcome.go) implements Step 61's ("domain/turn:
// builder epistemic pre-action check", §20.2) own STRUCTURED-SIGNAL-
// REPORTING TOOL: POST /sessions/{sessionID}/turn/epistemic-outcome. This
// is the ONLY way a build turn's own devil's-advocate preamble check
// (internal/domain/turn.RenderEpistemicPreamble) reaches turns.
// epistemic_outcome (migrations/000066_builder_epistemic_check.up.sql) --
// §20.2 is explicit this is "never prompt-only": an agent's own
// natural-language reply mentioning what it noticed is advisory only and
// is never re-parsed as the outcome of record (this codebase's standing
// invariant that a structured signal is a typed field on a payload, never
// a marker scraped from markdown -- Step 45's own rule, restated at
// §26.4/§29).
//
// # Why an HTTP endpoint, not a genuine OpenCode/LLM function-call tool
//
// Structurally IDENTICAL to PostReviewVerdict (reviewverdict.go) and
// PostWorkflowStepOutcome (workflowstepoutcome.go) -- see reviewverdict.go's
// own doc comment for the full "why an HTTP endpoint, not a genuine
// OpenCode/LLM tool-call" reasoning, which applies here without
// modification: nothing in this codebase's AgentRuntime port or sandbox WS
// protocol defines a native tool-calling mechanism, so this reuses the SAME
// sandbox-bearer-token/gen-fencing scheme scmcredentials.go/snapshotmint.go/
// reviewverdict.go/workflowstepoutcome.go already establish for
// "agent-initiated, server-validated" actions. The devil's-advocate
// preamble itself (internal/domain/turn.RenderEpistemicPreamble) is the
// natural place to instruct the agent HOW to call this endpoint (URL,
// bearer token, gen header, JSON shape) -- exactly like
// review.RenderTurnPrompt already does for the review-verdict tool, and
// workflowstepoutcome.go's own doc comment anticipates for a future
// workflow step's prompt.
//
// # Resolution: session id only, no turn id from the caller
//
// Mirrors PostWorkflowStepOutcome's own "no run/step ids from the caller"
// convention exactly, one layer down: an agent inside a sandbox has no
// reason to know its own turn's id, only that it is reporting on the turn
// it is currently executing. This endpoint resolves "the session's own
// CURRENTLY live turn" itself (TurnStore.GetProcessingTurnForSession) --
// turns_one_processing_per_session (migrations/000005_turns.up.sql)
// guarantees at most one candidate.

package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/domain/sandbox"
	"github.com/khazaddev/narvi/internal/domain/turn"
	"github.com/khazaddev/narvi/internal/platform"
)

// PostEpistemicOutcome backs POST /sessions/{sessionID}/turn/epistemic-outcome
// (note: no /api prefix -- a sandbox-to-CP endpoint, not a browser-facing
// REST route, mirroring scm-credentials/snapshot-mint/review-verdict/
// workflow-step-outcome exactly). Outcome table (mirrors
// PostWorkflowStepOutcome's own, one status value over):
//
//  1. sessionID does not parse as a UUID, or no sandbox row exists for it
//     -> 404.
//  2. Authorization: Bearer <token> missing/malformed -> 401.
//  3. sandbox.IsDeadSandboxStatus(sandboxRow.Status) -> 410.
//  4. The presented X-Sandbox-Gen header is missing/malformed, or parses
//     but does not equal sandboxRow.Gen -> 403 (§9.3 scenario #6 parity).
//  5. The presented bearer token fails verifySandboxBearerToken -> 401.
//  6. Malformed request body (fails to decode as restdtos.
//     PostEpistemicOutcomeRequest -- the generated type's own
//     UnmarshalJSON already rejects a missing/out-of-enum outcome) -> 400.
//  7. This session has no currently-processing turn -- a meaningless call,
//     this session's current turn (if any) is not a live, in-flight one
//     -> 400 (mirrors reviewverdict.go/workflowstepoutcome.go's own "no
//     PR/step to act on" 400 precedent for the structurally identical
//     "nothing to post onto" case).
//  8. The guarded UPDATE (TurnStore.SetEpistemicOutcome, "AND
//     status = 'processing'") affects zero rows -- a race: the live turn
//     step 7 just found reached a terminal state between that read and
//     this write -> 409.
//  9. Otherwise -> 201 with restdtos.PostEpistemicOutcomeResponse.
func PostEpistemicOutcome(sandboxes *postgres.SandboxStore, turns *postgres.TurnStore) http.HandlerFunc {
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
			logger.Error("httpapi: epistemic-outcome: get sandbox failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		if sandbox.IsDeadSandboxStatus(sandbox.State(sandboxRow.Status)) {
			writeError(w, http.StatusGone, "session stopped")
			return
		}

		presentedGen, genErr := strconv.Atoi(r.Header.Get("X-Sandbox-Gen"))
		if genErr != nil || presentedGen != int(sandboxRow.Gen) {
			logger.Warn("httpapi: epistemic-outcome: rejecting: gen mismatch",
				"presented_gen_header", r.Header.Get("X-Sandbox-Gen"), "sandbox_gen", sandboxRow.Gen)
			writeError(w, http.StatusForbidden, "no usable outcome-posting credential for this session")
			return
		}

		if !verifySandboxBearerToken(token, sandboxRow.TokenHash) {
			writeError(w, http.StatusUnauthorized, "invalid sandbox token")
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
		var req restdtos.PostEpistemicOutcomeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "malformed request body: "+err.Error())
			return
		}

		processingTurn, err := turns.GetProcessingTurnForSession(ctx, sessionID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusBadRequest, "this session has no processing turn to post an epistemic outcome onto")
				return
			}
			logger.Error("httpapi: epistemic-outcome: get processing turn failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		// Defense in depth, not the primary enforcement: RenderEpistemicPreamble
		// (and therefore this endpoint's own placeholder-substituted URL) is
		// never even present in a plan-mode turn's prompt in the first place
		// (§20.3, ShouldInjectEpistemicPreamble) -- but nothing stops an
		// agent from calling this endpoint anyway on a plan-mode turn it
		// somehow learned the real URL for (e.g. a stale, cached prompt from
		// before a mid-session plan_mode flip is not a real scenario today,
		// but this keeps the invariant true at the ONE place it actually
		// matters regardless of how a caller got here). Reported the same
		// way step 7 above is: 400, "nothing to post onto" -- a plan-mode
		// turn is, from this endpoint's own point of view, not a turn the
		// epistemic check ever applies to.
		if processingTurn.PlanMode {
			writeError(w, http.StatusBadRequest, "this session's processing turn is a plan-mode turn; the epistemic check does not apply")
			return
		}

		outcome := sqlcgen.TurnEpistemicOutcome(req.Outcome)
		if !turn.IsValidEpistemicOutcome(turn.EpistemicOutcome(outcome)) {
			// Unreachable in practice -- restdtos.PostEpistemicOutcomeRequest's
			// own generated UnmarshalJSON already rejects any value outside
			// {none, minor, strong} at decode time above (step 6). Kept as an
			// explicit, fail-closed second check rather than trusting the
			// generated type alone -- mirrors this codebase's general "never
			// trust a single validation layer for a value that ends up in
			// Postgres" posture.
			writeError(w, http.StatusBadRequest, "invalid outcome")
			return
		}

		rowsAffected, err := turns.SetEpistemicOutcome(ctx, processingTurn.ID, outcome)
		if err != nil {
			logger.Error("httpapi: epistemic-outcome: set outcome failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if rowsAffected == 0 {
			// A race: the live processing turn found above reached a
			// terminal state between that read and this guarded write.
			writeError(w, http.StatusConflict, "the turn this outcome targeted is no longer processing")
			return
		}

		writeJSON(w, http.StatusCreated, restdtos.PostEpistemicOutcomeResponse{
			TurnId: processingTurn.ID.String(),
		})
	}
}
