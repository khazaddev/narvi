//go:build integration

// Integration proof for migrations/000057_workflows.up.sql (
// "domain/workflow + loopguard + schema", §25.4/§25.8): the three
// built-in workflow definitions, their steps and edge, and the three
// global bindings come out of migrate-up seeded and well-formed in
// exactly §25.8's zero-config shapes, and the schema's structural
// invariants (one built-in per lane, one global binding per lane,
// binding-lane/definition-lane agreement, cross-definition edges
// unrepresentable, one deterministic edge per (step, outcome), one
// running run per session, one live attempt per run) hold at the DB
// level, not just in application code. Deliberately raw SQL throughout:
// NO store layer exists for these tables yet -- Step 54 is dark by
// design (schema/domain/contracts/RBAC only), and Steps 55-56 own the
// first real read/write paths.
package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// The fixed system-row ids migration 000057 seeds (see its own header
// comment for why these are constants rather than gen_random_uuid()).
// builtInPlanStep1ID is the ONE step that survives migration 000057's own
// two-step plan seed -- migration 000088_plan_builtin_passthrough (Step
// 56's own corrective follow-up, §25.8/§25.9) removed the second step
// (originally id ...032) and its hitl_after=true, so there is no
// builtInPlanStep2ID constant any longer.
const (
	builtInReviewDefID  = "00000000-0000-4000-8000-000000000001"
	builtInRequestDefID = "00000000-0000-4000-8000-000000000002"
	builtInPlanDefID    = "00000000-0000-4000-8000-000000000003"
	builtInPlanStep1ID  = "00000000-0000-4000-8000-000000000031"
)

// expectPgErrCode asserts err is a *pgconn.PgError with the given
// SQLSTATE and constraint -- the same idiom
// postgres_integration_test.go's own one-processing-turn assertion uses.
func expectPgErrCode(t *testing.T, err error, code, constraint string) {
	t.Helper()
	if err == nil {
		t.Fatalf("statement succeeded, want SQLSTATE %s on constraint %s", code, constraint)
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("error = %v (%T), want a *pgconn.PgError", err, err)
	}
	if pgErr.Code != code {
		t.Fatalf("error code = %q (constraint %q), want %q", pgErr.Code, pgErr.ConstraintName, code)
	}
	if pgErr.ConstraintName != constraint {
		t.Fatalf("constraint = %q, want %q", pgErr.ConstraintName, constraint)
	}
}

// TestWorkflowSeed_BuiltInDefinitions proves the three system templates
// exist exactly as §25.8 shapes them TODAY, post migration
// 000088_plan_builtin_passthrough (Step 56's own corrective follow-up):
// review/request/plan are now IDENTICALLY shaped single passthrough
// steps (ModelID nil so turns.model_id/sessions.build_model_id inherit
// exactly as today -- Step 55's zero-config proof), no HITL, no edges.
//
// Migration 000057 originally seeded plan as a 2-step approve/build
// shape with HITL after step 1 and a needs_fix self-loop edge; an audit
// found that shape was a genuine design incoherence -- it silently
// double-parked a workflow-level HITL gate (workflow_step_runs.status =
// 'awaiting_decision', resolved only via POST /api/workflow-runs/:runId/
// steps/:stepRunId/decide) against classic plan mode's own pre-existing,
// UNCONDITIONAL persisted-state awaiting-plan gate (Steps 37/38, §8.1:
// plans.status, plan.MatchVerdict/MatchRevise, turn.go's own
// ErrPlanAwaitingApproval) on every single plan-mode session, since
// internal/adapters/inbound/httpapi's createTurnLocked (Step 55) wires
// the workflow engine into EVERY new turn unconditionally. Migration
// 000088 (see its own header comment for the full "why") corrected this:
// classic plan mode stays the SOLE plan-approval authority, and the
// built-in plan workflow became a genuine single-step passthrough,
// matching review/request. Workflow-driven plan HITL is deferred to the
// Phase 7 canvas editor (§25.12), where a custom (non-built-in)
// definition can still use hitl_after -- the mechanism itself is
// unaffected, only this built-in SEED changed.
func TestWorkflowSeed_BuiltInDefinitions(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	var total int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow_definitions`).Scan(&total); err != nil {
		t.Fatalf("count workflow_definitions: %v", err)
	}
	if total != 3 {
		t.Fatalf("workflow_definitions count = %d, want exactly the 3 seeded built-ins", total)
	}

	for lane, defID := range map[string]string{
		"review":  builtInReviewDefID,
		"request": builtInRequestDefID,
		"plan":    builtInPlanDefID,
	} {
		var (
			name      string
			isBuiltIn bool
			version   int
		)
		err := pool.QueryRow(ctx,
			`SELECT name, is_built_in, version FROM workflow_definitions WHERE id = $1 AND lane = $2`,
			defID, lane).Scan(&name, &isBuiltIn, &version)
		if err != nil {
			t.Fatalf("seeded %s definition %s: %v", lane, defID, err)
		}
		if name != lane || !isBuiltIn || version != 1 {
			t.Errorf("%s definition = (name %q, is_built_in %v, version %d), want (%q, true, 1)", lane, name, isBuiltIn, version, lane)
		}
	}

	// review/request/plan: exactly one passthrough step each, no edges --
	// identically shaped since migration 000088 (see this test's own top
	// doc comment). builtInPlanStep1ID (the plan lane's own single
	// surviving step) is asserted directly below, alongside review/
	// request's own step ids, rather than via a separate id constant per
	// lane -- this loop already reads every step id it needs from the DB.
	for _, defID := range []string{builtInReviewDefID, builtInRequestDefID, builtInPlanDefID} {
		var (
			stepCount, edgeCount int
			id                   string
			order                int
			kind, prompt         string
			modelID              *string
			scope, continuity    string
			before, after        bool
		)
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM workflow_step_definitions WHERE workflow_definition_id = $1`, defID).Scan(&stepCount); err != nil {
			t.Fatalf("count steps for %s: %v", defID, err)
		}
		if stepCount != 1 {
			t.Fatalf("definition %s step count = %d, want 1 (single passthrough step, §25.8)", defID, stepCount)
		}
		err := pool.QueryRow(ctx, `
			SELECT id, step_order, kind, model_id, prompt_template, execution_scope, conversation_continuity, hitl_before, hitl_after
			FROM workflow_step_definitions WHERE workflow_definition_id = $1`, defID).
			Scan(&id, &order, &kind, &modelID, &prompt, &scope, &continuity, &before, &after)
		if err != nil {
			t.Fatalf("read step for %s: %v", defID, err)
		}
		if order != 1 || kind != "agent" || modelID != nil || prompt != "{{prompt}}" ||
			scope != "same_session" || continuity != "continue" || before || after {
			t.Errorf("definition %s step = (id %s, order %d, kind %q, model %v, prompt %q, scope %q, continuity %q, hitl %v/%v), want the §25.8 passthrough shape",
				defID, id, order, kind, modelID, prompt, scope, continuity, before, after)
		}
		if defID == builtInPlanDefID && id != builtInPlanStep1ID {
			t.Errorf("plan step id = %s, want the surviving step %s", id, builtInPlanStep1ID)
		}
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM workflow_edges WHERE workflow_definition_id = $1`, defID).Scan(&edgeCount); err != nil {
			t.Fatalf("count edges for %s: %v", defID, err)
		}
		if edgeCount != 0 {
			t.Errorf("definition %s edge count = %d, want 0 (migration 000088 removed plan's own needs_fix self-loop edge along with its second step)", defID, edgeCount)
		}
	}
}

// TestWorkflowSeed_GlobalBindings proves §25.4's "this row is never
// absent" guarantee holds from migrate-up: exactly one global
// (repo_full_name IS NULL) binding per lane, each pointing at that
// lane's own built-in definition at version 1 -- so binding resolution
// is always a two-step lookup with a guaranteed second step, never an
// "absent row -> default" branch.
func TestWorkflowSeed_GlobalBindings(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	var total int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow_bindings`).Scan(&total); err != nil {
		t.Fatalf("count workflow_bindings: %v", err)
	}
	if total != 3 {
		t.Fatalf("workflow_bindings count = %d, want exactly the 3 seeded global rows", total)
	}

	for lane, wantDefID := range map[string]string{
		"review":  builtInReviewDefID,
		"request": builtInRequestDefID,
		"plan":    builtInPlanDefID,
	} {
		var (
			defID   string
			version int
		)
		err := pool.QueryRow(ctx, `
			SELECT workflow_definition_id, definition_version
			FROM workflow_bindings WHERE lane = $1 AND repo_full_name IS NULL`, lane).
			Scan(&defID, &version)
		if err != nil {
			t.Fatalf("global %s binding (must never be absent, §25.4): %v", lane, err)
		}
		if defID != wantDefID || version != 1 {
			t.Errorf("global %s binding = (%s, v%d), want (%s, v1)", lane, defID, version, wantDefID)
		}
	}
}

// TestWorkflowSchema_StructuralInvariants exercises the
// constraint-over-convention guarantees a caller cannot violate even
// with raw SQL: exactly one built-in per lane, exactly one global
// binding per lane (a repo override remains legal), binding lane must
// equal the bound definition's lane, an edge cannot cross definitions,
// and (from step, outcome) routes to at most one target.
func TestWorkflowSchema_StructuralInvariants(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	t.Run("second built-in per lane rejected", func(t *testing.T) {
		_, err := pool.Exec(ctx,
			`INSERT INTO workflow_definitions (lane, name, is_built_in) VALUES ('review', 'review-2', true)`)
		expectPgErrCode(t, err, "23505", "workflow_definitions_built_in_lane_uniq")
	})

	t.Run("custom definition on the same lane allowed", func(t *testing.T) {
		if _, err := pool.Exec(ctx,
			`INSERT INTO workflow_definitions (lane, name) VALUES ('review', 'custom-review')`); err != nil {
			t.Fatalf("custom (non-built-in) review definition rejected: %v", err)
		}
	})

	t.Run("second global binding per lane rejected", func(t *testing.T) {
		_, err := pool.Exec(ctx, `
			INSERT INTO workflow_bindings (lane, repo_full_name, workflow_definition_id, definition_version)
			VALUES ('review', NULL, $1, 1)`, builtInReviewDefID)
		expectPgErrCode(t, err, "23505", "workflow_bindings_global_uniq")
	})

	t.Run("repo override binding allowed alongside the global row", func(t *testing.T) {
		if _, err := pool.Exec(ctx, `
			INSERT INTO workflow_bindings (lane, repo_full_name, workflow_definition_id, definition_version)
			VALUES ('review', 'acme/api', $1, 1)`, builtInReviewDefID); err != nil {
			t.Fatalf("repo-override binding rejected: %v", err)
		}
		_, err := pool.Exec(ctx, `
			INSERT INTO workflow_bindings (lane, repo_full_name, workflow_definition_id, definition_version)
			VALUES ('review', 'acme/api', $1, 1)`, builtInReviewDefID)
		expectPgErrCode(t, err, "23505", "workflow_bindings_repo_uniq")
	})

	t.Run("binding lane must match definition lane", func(t *testing.T) {
		_, err := pool.Exec(ctx, `
			INSERT INTO workflow_bindings (lane, repo_full_name, workflow_definition_id, definition_version)
			VALUES ('plan', 'acme/api', $1, 1)`, builtInReviewDefID)
		expectPgErrCode(t, err, "23503", "workflow_bindings_definition_lane_fk")
	})

	t.Run("cross-definition edge unrepresentable", func(t *testing.T) {
		// The (definition, step) composite FK: plan's step 1 does not
		// exist under the review definition, so this edge cannot be
		// stated at all.
		_, err := pool.Exec(ctx, `
			INSERT INTO workflow_edges (workflow_definition_id, from_step_id, to_step_id, on_status)
			VALUES ($1, $2, $3, 'ok')`,
			builtInReviewDefID, "00000000-0000-4000-8000-000000000011", builtInPlanStep1ID)
		expectPgErrCode(t, err, "23503", "workflow_edges_to_step_fk")
	})

	t.Run("second edge for the same (step, outcome) rejected", func(t *testing.T) {
		// Migration 000088 left every built-in definition edgeless (the
		// plan built-in's own needs_fix self-loop went with its second
		// step), so this constraint no longer has a seeded pair to
		// collide against -- it is proven here by inserting one edge and
		// then its exact duplicate, rather than by leaning on a seeded
		// row that no longer exists. A self-loop is used because the plan
		// definition now has exactly one step, and the composite
		// (definition, step) FK forbids naming any other definition's.
		if _, err := pool.Exec(ctx, `
			INSERT INTO workflow_edges (workflow_definition_id, from_step_id, to_step_id, on_status)
			VALUES ($1, $2, $2, 'needs_fix')`,
			builtInPlanDefID, builtInPlanStep1ID); err != nil {
			t.Fatalf("insert first edge: %v", err)
		}
		t.Cleanup(func() {
			if _, err := pool.Exec(ctx,
				`DELETE FROM workflow_edges WHERE workflow_definition_id = $1`, builtInPlanDefID); err != nil {
				t.Errorf("clean up inserted edge: %v", err)
			}
		})

		_, err := pool.Exec(ctx, `
			INSERT INTO workflow_edges (workflow_definition_id, from_step_id, to_step_id, on_status)
			VALUES ($1, $2, $2, 'needs_fix')`,
			builtInPlanDefID, builtInPlanStep1ID)
		expectPgErrCode(t, err, "23505", "workflow_edges_from_status_uniq")
	})

	t.Run("bound definition not deletable", func(t *testing.T) {
		// The seeded global binding references it (NO ACTION FK) --
		// §25.4's resolution guarantee cannot be hollowed out by a
		// delete, even before the application-layer is_built_in refusal
		// (see migration 000057's header) exists to also stop this.
		_, err := pool.Exec(ctx, `DELETE FROM workflow_definitions WHERE id = $1`, builtInReviewDefID)
		expectPgErrCode(t, err, "23503", "workflow_bindings_definition_lane_fk")
	})
}

// TestWorkflowSchema_SequentialExecutionIndexes proves the two partial
// unique indexes that make §25.6's strictly-sequential execution model
// structural: at most one running run per session (mirroring
// turns_one_processing_per_session), and at most one live
// (running/awaiting_decision) step attempt per run -- while a SECOND
// attempt row for the same step is legal once the first is terminal,
// which is exactly the re-execution shape §25.5's COUNT(*) iteration
// read depends on.
func TestWorkflowSchema_SequentialExecutionIndexes(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	var sessionID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO sessions (spawn_source) VALUES ('web') RETURNING id`).Scan(&sessionID); err != nil {
		t.Fatalf("insert session: %v", err)
	}

	var runID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO workflow_runs (session_id, lane, workflow_definition_id, definition_version)
		VALUES ($1, 'plan', $2, 1) RETURNING id`, sessionID, builtInPlanDefID).Scan(&runID); err != nil {
		t.Fatalf("insert workflow run: %v", err)
	}

	_, err := pool.Exec(ctx, `
		INSERT INTO workflow_runs (session_id, lane, workflow_definition_id, definition_version)
		VALUES ($1, 'plan', $2, 1)`, sessionID, builtInPlanDefID)
	expectPgErrCode(t, err, "23505", "workflow_runs_one_running_per_session")

	// Parking the first run in needs_review frees the session for a
	// successor run (§25.9's escalation must not hold the session
	// hostage -- see migration 000057's own workflow_runs comment).
	if _, err := pool.Exec(ctx,
		`UPDATE workflow_runs SET status = 'needs_review' WHERE id = $1`, runID); err != nil {
		t.Fatalf("park run in needs_review: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO workflow_runs (session_id, lane, workflow_definition_id, definition_version)
		VALUES ($1, 'plan', $2, 1)`, sessionID, builtInPlanDefID); err != nil {
		t.Fatalf("successor run alongside a needs_review run rejected: %v", err)
	}

	var attemptID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO workflow_step_runs (workflow_run_id, step_definition_id)
		VALUES ($1, $2) RETURNING id`, runID, builtInPlanStep1ID).Scan(&attemptID); err != nil {
		t.Fatalf("insert step attempt: %v", err)
	}

	// One LIVE attempt per RUN, whichever step it names -- proven here
	// with the same step, since migration 000088 left the plan built-in
	// single-step. That is if anything the stricter reading of the
	// constraint: it rejects a second live attempt even for the step
	// already running, not merely for a different one.
	_, err = pool.Exec(ctx, `
		INSERT INTO workflow_step_runs (workflow_run_id, step_definition_id)
		VALUES ($1, $2)`, runID, builtInPlanStep1ID)
	expectPgErrCode(t, err, "23505", "workflow_step_runs_one_live_per_run")

	if _, err := pool.Exec(ctx, `
		UPDATE workflow_step_runs SET status = 'completed', outcome_status = 'needs_fix' WHERE id = $1`, attemptID); err != nil {
		t.Fatalf("complete first attempt: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO workflow_step_runs (workflow_run_id, step_definition_id)
		VALUES ($1, $2)`, runID, builtInPlanStep1ID); err != nil {
		t.Fatalf("re-execution attempt after a terminal one rejected: %v", err)
	}

	var attempts int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM workflow_step_runs WHERE workflow_run_id = $1 AND step_definition_id = $2`,
		runID, builtInPlanStep1ID).Scan(&attempts); err != nil {
		t.Fatalf("count attempts: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempt COUNT(*) = %d, want 2 -- §25.5's loopguard iteration read", attempts)
	}
}
