// This file (automationinvocations.go) implements the plan-mode-and-
// automations UI's own "automations health/runs table" half (§12.2 item 4,
// §8.4): GET /api/automations/{automationID}/
// invocations, the read model behind mockups.html's own Automations view
// "expandable invocation → runs rows" -- a gap automations.go's own
// pre-existing Automation DTO could not close by itself (lastRunAt/
// lastRunStatus/artifactSummary there are AUTOMATION-level, most-recent-
// invocation-only fields; the mockup's own expandable table needs several
// PAST invocations, each with its own per-target runs).
//
// One route, mirroring GetAutomation's own exact shape (parseAutomationID,
// existence 404 check, then the read, then writeJSON) -- and the SAME "no
// extra RBAC beyond logged in" precedent automations.go's own top doc
// comment already establishes for GetAutomation/ListAutomations: this is a
// plain read of an automation's own run history, changing no state,
// visible to every signed-in role exactly like the automation row itself
// already is.
//
// Deliberately bounded and newest-first (listAutomationInvocationsLimit),
// never an unbounded full archive -- mirrors ListPlansResponse's own "a
// session's own plan history is expected to stay small" precedent one
// level up in the same package (plans.go).

package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/platform"
)

// listAutomationInvocationsLimit bounds how many of an automation's own
// most recent invocations this endpoint returns -- generous for the
// mockup's own expandable table (which shows a small handful of recent
// runs), while staying a fixed, safe upper bound rather than an unbounded
// scan of an automation's entire lifetime history.
const listAutomationInvocationsLimit = 20

// ListAutomationInvocations backs GET /api/automations/{automationID}/
// invocations. 404 if the automation doesn't exist; otherwise up to
// listAutomationInvocationsLimit of its own most recent invocations,
// newest first, each with every one of its own fanned-out runs nested
// (AutomationInvocationStore.ListForAutomation + AutomationRunStore.
// ListForInvocation per invocation -- bounded on both axes, ≤20
// invocations × ≤automation.MaxFanOutTargets runs each, never an
// unbounded join).
func ListAutomationInvocations(automations *postgres.AutomationStore, invocations *postgres.AutomationInvocationStore, runs *postgres.AutomationRunStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		logger := platform.Logger(ctx)

		automationID, ok := parseAutomationID(w, r)
		if !ok {
			return
		}

		if _, err := automations.Get(ctx, automationID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "automation not found")
				return
			}
			logger.Error("httpapi: get automation failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		invocationRows, err := invocations.ListForAutomation(ctx, automationID, listAutomationInvocationsLimit)
		if err != nil {
			logger.Error("httpapi: list automation invocations failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		wire := make([]restdtos.AutomationInvocation, len(invocationRows))
		for i, inv := range invocationRows {
			runRows, err := runs.ListForInvocation(ctx, inv.ID)
			if err != nil {
				logger.Error("httpapi: list automation runs for invocation failed", "error", err, "invocation_id", inv.ID.String())
				writeError(w, http.StatusInternalServerError, "internal error")
				return
			}
			wire[i] = automationInvocationToDTO(inv, runRows)
		}

		writeJSON(w, http.StatusOK, restdtos.ListAutomationInvocationsResponse{Invocations: wire})
	}
}

// automationInvocationToDTO converts one sqlcgen.AutomationInvocation row
// (plus its own already-fetched runs) into restdtos.AutomationInvocation --
// mirrors automationToDTO's own "explicit conversions for enum/nullability-
// representation mismatches" shape (automations.go).
func automationInvocationToDTO(inv sqlcgen.AutomationInvocation, runRows []sqlcgen.AutomationRun) restdtos.AutomationInvocation {
	var closedAt restdtos.AutomationInvocationClosedAt
	if inv.ClosedAt.Valid {
		t := inv.ClosedAt.Time
		closedAt = restdtos.AutomationInvocationClosedAt(&t)
	}

	runWire := make([]restdtos.AutomationRun, len(runRows))
	for i, run := range runRows {
		runWire[i] = automationRunToDTO(run)
	}

	return restdtos.AutomationInvocation{
		Id:           inv.ID.String(),
		AutomationId: inv.AutomationID.String(),
		Status:       restdtos.AutomationInvocationStatus(inv.Status),
		TotalRuns:    int(inv.TotalRuns),
		ClosedAt:     closedAt,
		CreatedAt:    inv.CreatedAt.Time,
		Runs:         runWire,
	}
}

// automationRunToDTO converts one sqlcgen.AutomationRun row into
// restdtos.AutomationRun. target's own raw JSONB (written exclusively by
// this codebase's own fan-out path, internal/app/automation/fanout.go,
// never attacker-controlled) is decoded straight into
// restdtos.AutomationReposElem -- the identical {name,url,branch} wire
// shape internal/app/automation.Target already establishes for this same
// column (see that package's own target.go) -- degrading to the zero value
// on a decode failure, mirroring decodeSessionRepos's own "log + honest
// zero-value fallback, never a 500" precedent (session.go) rather than
// failing the whole response over one malformed row.
func automationRunToDTO(run sqlcgen.AutomationRun) restdtos.AutomationRun {
	var target restdtos.AutomationReposElem
	if err := json.Unmarshal(run.Target, &target); err != nil {
		slog.Default().Error("httpapi: automation_runs.target is not valid JSON (corrupt row?)", "error", err, "run_id", run.ID.String())
	}

	var sessionID restdtos.AutomationRunSessionId
	if run.SessionID.Valid {
		s := run.SessionID.String()
		sessionID = restdtos.AutomationRunSessionId(&s)
	}

	var runningAt restdtos.AutomationRunRunningAt
	if run.RunningAt.Valid {
		t := run.RunningAt.Time
		runningAt = restdtos.AutomationRunRunningAt(&t)
	}

	var completedAt restdtos.AutomationRunCompletedAt
	if run.CompletedAt.Valid {
		t := run.CompletedAt.Time
		completedAt = restdtos.AutomationRunCompletedAt(&t)
	}

	return restdtos.AutomationRun{
		Id:           run.ID.String(),
		InvocationId: run.InvocationID.String(),
		AutomationId: run.AutomationID.String(),
		Target:       target,
		SessionId:    sessionID,
		Status:       restdtos.AutomationRunStatus(run.Status),
		StartedAt:    run.StartedAt.Time,
		RunningAt:    runningAt,
		CompletedAt:  completedAt,
	}
}
